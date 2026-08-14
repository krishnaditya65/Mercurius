// Package jointholding implements the other half of FEATURES.md §1's
// "Nominee management, joint holding support": registering an account's
// HOLDING STRUCTURE — INDIVIDUAL (one holder) vs JOINT (2+ named
// holders) — with a real holding-mode designation and a real primary
// holder, modeled on the standard modes Indian depositories/brokerages
// actually use for joint demat and bank accounts:
//
//   - JOINTLY: every holder must act together — operations that matter
//     (e.g. changing bank details, closing the account) require consent
//     from ALL holders before this package will call them authorized.
//   - EITHER_OR_SURVIVOR: exactly two holders; either one alone can
//     operate the account, and on one holder's death the account
//     continues with the survivor. Any single holder's consent
//     authorizes an operation.
//   - ANYONE_OR_SURVIVOR: three or more holders; any one of them alone
//     can operate the account, continuing with the survivors on any
//     holder's death. Any single holder's consent authorizes an
//     operation — same authorization rule as EITHER_OR_SURVIVOR, just
//     permitting more than two holders.
//
// This package is intentionally separate from nomineedesignation (a
// sibling package in this same service): joint holding is about who
// legally co-owns and can operate an account, nominee designation is
// about who receives the assets on the account holder's death — related
// account-opening concepts, but different questions with different
// validation rules, so kept as two focused packages rather than one
// mixed one (mirrors this repo's existing convention of one concern per
// internal/ package — see kycstate/bankverification/riskprofiling being
// three separate packages rather than one).
//
// TODO(real build): in-memory only, no persistence; no auth (anyone who
// can reach these endpoints can register or alter any account's holding
// structure); "consent" here is just a holder id asserted by the caller
// — there is no e-signature, OTP, or any other real proof that the named
// holder actually gave that consent, exactly the same honesty gap
// bankverification and nomineesuccession already document for their own
// unverified-identity fields.
package jointholding

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// AccountHoldingType is whether an account is solely owned or jointly
// held.
type AccountHoldingType string

const (
	HoldingTypeIndividual AccountHoldingType = "INDIVIDUAL"
	HoldingTypeJoint      AccountHoldingType = "JOINT"
)

// JointHoldingMode enumerates the standard brokerage/depository joint
// holding operating modes — only meaningful when AccountHoldingType is
// HoldingTypeJoint.
type JointHoldingMode string

const (
	// ModeJointly requires every holder's consent before an operation
	// this package gates is considered authorized.
	ModeJointly JointHoldingMode = "JOINTLY"

	// ModeEitherOrSurvivor requires exactly two holders; any single
	// holder's consent authorizes an operation.
	ModeEitherOrSurvivor JointHoldingMode = "EITHER_OR_SURVIVOR"

	// ModeAnyoneOrSurvivor requires three or more holders; any single
	// holder's consent authorizes an operation.
	ModeAnyoneOrSurvivor JointHoldingMode = "ANYONE_OR_SURVIVOR"
)

const minimumJointHolders = 2
const eitherOrSurvivorExactHolderCount = 2
const minimumAnyoneOrSurvivorHolderCount = 3

var (
	// ErrAccountIdentifierRequired is returned when a caller supplies an
	// empty accountIdentifier.
	ErrAccountIdentifierRequired = errors.New("accountIdentifier is required")

	// ErrHolderFullNameRequired is returned when any named holder's full
	// name is blank.
	ErrHolderFullNameRequired = errors.New("every holder's fullName is required")

	// ErrInvalidHoldingMode is returned for anything other than
	// ModeJointly, ModeEitherOrSurvivor, or ModeAnyoneOrSurvivor.
	ErrInvalidHoldingMode = errors.New("holdingMode must be JOINTLY, EITHER_OR_SURVIVOR, or ANYONE_OR_SURVIVOR")

	// ErrJointAccountRequiresAtLeastTwoHolders is returned when a JOINT
	// registration supplies fewer than two holders.
	ErrJointAccountRequiresAtLeastTwoHolders = fmt.Errorf("a JOINT account requires at least %d holders", minimumJointHolders)

	// ErrEitherOrSurvivorRequiresExactlyTwoHolders is returned when
	// ModeEitherOrSurvivor is registered with a holder count other than
	// exactly two.
	ErrEitherOrSurvivorRequiresExactlyTwoHolders = fmt.Errorf("EITHER_OR_SURVIVOR requires exactly %d holders", eitherOrSurvivorExactHolderCount)

	// ErrAnyoneOrSurvivorRequiresAtLeastThreeHolders is returned when
	// ModeAnyoneOrSurvivor is registered with fewer than three holders
	// (two holders is EITHER_OR_SURVIVOR's mode, not this one).
	ErrAnyoneOrSurvivorRequiresAtLeastThreeHolders = fmt.Errorf("ANYONE_OR_SURVIVOR requires at least %d holders", minimumAnyoneOrSurvivorHolderCount)

	// ErrPrimaryHolderIndexOutOfRange is returned when
	// primaryHolderIndex doesn't index into the supplied holders slice.
	ErrPrimaryHolderIndexOutOfRange = errors.New("primaryHolderIndex must index into the supplied holders")

	// ErrSoleHolderFullNameRequired is returned when RegisterIndividualAccount
	// is called with a blank sole holder name.
	ErrSoleHolderFullNameRequired = errors.New("soleHolderFullName is required")

	// ErrNoHoldingStructureFound is returned when a query or an
	// authorization check targets an account with no registered holding
	// structure at all.
	ErrNoHoldingStructureFound = errors.New("no holding structure is registered for this account")

	// ErrConsentFromUnknownHolder is returned when AuthorizeOperation is
	// given a holder id that doesn't belong to the account's registered
	// holders.
	ErrConsentFromUnknownHolder = errors.New("consent was supplied from a holder id not registered on this account")

	// ErrAtLeastOneConsentRequired is returned when AuthorizeOperation is
	// called with zero consenting holder ids.
	ErrAtLeastOneConsentRequired = errors.New("at least one consenting holder id is required")

	// ErrJointlyModeRequiresAllHoldersConsent is returned when
	// AuthorizeOperation is called on a ModeJointly account without every
	// holder's consent present.
	ErrJointlyModeRequiresAllHoldersConsent = errors.New("JOINTLY mode requires every holder's consent to authorize this operation")
)

// Holder is one named co-owner of a joint account (or the sole owner of
// an individual account).
type Holder struct {
	HolderId        string `json:"holderId"`
	FullName        string `json:"fullName"`
	IsPrimaryHolder bool   `json:"isPrimaryHolder"`
}

// HoldingStructure is the full, real record of how one account is held.
type HoldingStructure struct {
	AccountIdentifier string             `json:"accountIdentifier"`
	HoldingType       AccountHoldingType `json:"holdingType"`
	HoldingMode       JointHoldingMode   `json:"holdingMode,omitempty"`
	Holders           []Holder           `json:"holders"`
}

// PrimaryHolder returns the holder flagged IsPrimaryHolder, if any is
// registered (RegisterIndividualAccount and RegisterJointAccount both
// always set exactly one, so in practice this is only false for a
// zero-value HoldingStructure).
func (structure HoldingStructure) PrimaryHolder() (Holder, bool) {
	for _, holder := range structure.Holders {
		if holder.IsPrimaryHolder {
			return holder, true
		}
	}
	return Holder{}, false
}

// HoldingRegistry is the mutex-guarded, in-memory home for every
// account's holding structure. Safe for concurrent use.
type HoldingRegistry struct {
	mutexGuardingState sync.Mutex
	structureByAccount map[string]*HoldingStructure
}

// NewHoldingRegistry builds an empty registry.
func NewHoldingRegistry() *HoldingRegistry {
	return &HoldingRegistry{
		structureByAccount: make(map[string]*HoldingStructure),
	}
}

// RegisterIndividualAccount registers (or overwrites) accountIdentifier
// as INDIVIDUAL, with a single sole holder who is trivially the primary
// holder.
func (registry *HoldingRegistry) RegisterIndividualAccount(accountIdentifier string, soleHolderFullName string) (HoldingStructure, error) {
	if accountIdentifier == "" {
		return HoldingStructure{}, ErrAccountIdentifierRequired
	}
	if soleHolderFullName == "" {
		return HoldingStructure{}, ErrSoleHolderFullNameRequired
	}

	structure := &HoldingStructure{
		AccountIdentifier: accountIdentifier,
		HoldingType:       HoldingTypeIndividual,
		Holders: []Holder{
			{HolderId: generateHolderId(), FullName: soleHolderFullName, IsPrimaryHolder: true},
		},
	}

	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()
	registry.structureByAccount[accountIdentifier] = structure
	return *structure, nil
}

// RegisterJointAccount registers (or overwrites) accountIdentifier as
// JOINT with holdingMode and holderFullNames, designating the holder at
// primaryHolderIndex (into holderFullNames) as the primary holder. Real
// per-mode validation:
//   - every mode requires at least minimumJointHolders (2) names, none
//     blank;
//   - ModeEitherOrSurvivor requires EXACTLY 2 holders;
//   - ModeAnyoneOrSurvivor requires AT LEAST 3 holders;
//   - ModeJointly accepts 2 or more holders, no upper structural limit.
func (registry *HoldingRegistry) RegisterJointAccount(
	accountIdentifier string,
	holdingMode JointHoldingMode,
	holderFullNames []string,
	primaryHolderIndex int,
) (HoldingStructure, error) {
	if accountIdentifier == "" {
		return HoldingStructure{}, ErrAccountIdentifierRequired
	}
	if holdingMode != ModeJointly && holdingMode != ModeEitherOrSurvivor && holdingMode != ModeAnyoneOrSurvivor {
		return HoldingStructure{}, ErrInvalidHoldingMode
	}
	for _, name := range holderFullNames {
		if name == "" {
			return HoldingStructure{}, ErrHolderFullNameRequired
		}
	}
	if len(holderFullNames) < minimumJointHolders {
		return HoldingStructure{}, ErrJointAccountRequiresAtLeastTwoHolders
	}
	if holdingMode == ModeEitherOrSurvivor && len(holderFullNames) != eitherOrSurvivorExactHolderCount {
		return HoldingStructure{}, ErrEitherOrSurvivorRequiresExactlyTwoHolders
	}
	if holdingMode == ModeAnyoneOrSurvivor && len(holderFullNames) < minimumAnyoneOrSurvivorHolderCount {
		return HoldingStructure{}, ErrAnyoneOrSurvivorRequiresAtLeastThreeHolders
	}
	if primaryHolderIndex < 0 || primaryHolderIndex >= len(holderFullNames) {
		return HoldingStructure{}, ErrPrimaryHolderIndexOutOfRange
	}

	holders := make([]Holder, len(holderFullNames))
	for index, name := range holderFullNames {
		holders[index] = Holder{
			HolderId:        generateHolderId(),
			FullName:        name,
			IsPrimaryHolder: index == primaryHolderIndex,
		}
	}

	structure := &HoldingStructure{
		AccountIdentifier: accountIdentifier,
		HoldingType:       HoldingTypeJoint,
		HoldingMode:       holdingMode,
		Holders:           holders,
	}

	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()
	registry.structureByAccount[accountIdentifier] = structure
	return *structure, nil
}

// GetHoldingStructure returns the currently-registered holding structure
// for an account, if any.
func (registry *HoldingRegistry) GetHoldingStructure(accountIdentifier string) (HoldingStructure, bool) {
	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	existing, exists := registry.structureByAccount[accountIdentifier]
	if !exists {
		return HoldingStructure{}, false
	}
	return *existing, true
}

// AuthorizeOperation is the real per-mode consent rule this package
// enforces: given the set of holder ids who consented to some sensitive
// operation, it reports whether that's enough to authorize it under the
// account's registered holding mode.
//   - INDIVIDUAL: the sole holder's own consent is always sufficient (and
//     is the only holder id that can be supplied).
//   - JOINTLY: every registered holder's id must be present in
//     consentingHolderIds.
//   - EITHER_OR_SURVIVOR / ANYONE_OR_SURVIVOR: any single registered
//     holder's id is sufficient.
//
// Every id in consentingHolderIds must belong to a holder actually
// registered on the account, or this returns ErrConsentFromUnknownHolder
// — a real build would also want each "consent" to be independently
// verified (e-signature/OTP), which this package does not do; see the
// package doc's TODO.
func (registry *HoldingRegistry) AuthorizeOperation(accountIdentifier string, consentingHolderIds []string) (bool, error) {
	if accountIdentifier == "" {
		return false, ErrAccountIdentifierRequired
	}
	if len(consentingHolderIds) == 0 {
		return false, ErrAtLeastOneConsentRequired
	}

	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	structure, exists := registry.structureByAccount[accountIdentifier]
	if !exists {
		return false, ErrNoHoldingStructureFound
	}

	validHolderIds := make(map[string]bool, len(structure.Holders))
	for _, holder := range structure.Holders {
		validHolderIds[holder.HolderId] = true
	}
	for _, consentingId := range consentingHolderIds {
		if !validHolderIds[consentingId] {
			return false, ErrConsentFromUnknownHolder
		}
	}

	if structure.HoldingType == HoldingTypeIndividual || structure.HoldingMode == ModeEitherOrSurvivor || structure.HoldingMode == ModeAnyoneOrSurvivor {
		// Any single valid holder's consent is enough — and it's already
		// been validated as belonging to this account above.
		return true, nil
	}

	// ModeJointly: every holder must have consented.
	consentedIds := make(map[string]bool, len(consentingHolderIds))
	for _, consentingId := range consentingHolderIds {
		consentedIds[consentingId] = true
	}
	for _, holder := range structure.Holders {
		if !consentedIds[holder.HolderId] {
			return false, ErrJointlyModeRequiresAllHoldersConsent
		}
	}
	return true, nil
}

// ListAccountsByHoldingType returns every registered accountIdentifier
// currently of holdingType, sorted for a deterministic response.
func (registry *HoldingRegistry) ListAccountsByHoldingType(holdingType AccountHoldingType) []string {
	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	matching := make([]string, 0)
	for accountIdentifier, structure := range registry.structureByAccount {
		if structure.HoldingType == holdingType {
			matching = append(matching, accountIdentifier)
		}
	}
	sort.Strings(matching)
	return matching
}

func generateHolderId() string {
	randomBytes := make([]byte, 8)
	if _, readErr := rand.Read(randomBytes); readErr != nil {
		return fmt.Sprintf("holder-fallback-%d", len(randomBytes))
	}
	return "holder-" + hex.EncodeToString(randomBytes)
}
