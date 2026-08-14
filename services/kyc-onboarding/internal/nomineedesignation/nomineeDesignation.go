// Package nomineedesignation implements the KYC-time-and-after nominee
// DESIGNATION step referenced by FEATURES.md §1's "Nominee management,
// joint holding support": the real brokerage nomination form an account
// holder fills in — one or more nominees with a relationship, date of
// birth, a percentage allocation across all of them, and guardian details
// when a nominee is a minor — plus the standard "I do not wish to
// nominate" opt-out an account holder can select instead.
//
// Scope boundary, stated loudly: this package is DISTINCT from
// services/backoffice's internal/nomineesuccession, which is a much
// later, entirely separate concern — the death-claim WORKFLOW (state
// machine: SUBMITTED -> UNDER_REVIEW -> APPROVED -> TRANSFERRED) that
// gets triggered by a death certificate submission long after account
// opening. This package only manages the DESIGNATION record itself — who
// the nominee(s) are and at what allocation — that a later succession
// claim would reference by nominee identity. It does not know about death
// certificates, review queues, or asset transfer, and it deliberately
// allows MULTIPLE nominees with percentage splits, which
// nomineesuccession's single-nominee "last write wins" model does not.
//
// Real validation modeled on an actual brokerage/depository nomination
// form (e.g. CDSL/NSDL-style demat nomination):
//   - every nominee needs a full name, relationship, and date of birth;
//   - if the computed age is under 18, guardian details (name,
//     relationship to the minor, and an identity document reference) are
//     required — a minor cannot receive/operate assets directly;
//   - percentage allocations across every nominee for one account must
//     sum to EXACTLY 100% for the designation to be considered a
//     complete, submittable form (SubmitNomination enforces this as a
//     hard gate — this is the literal "nomination form" submission);
//   - an account must have at least one nominee, OR the account holder
//     must explicitly opt out of nomination altogether — SubmitNomination
//     rejects zero nominees with no opt-out, exactly like a real form
//     that requires you to tick "I decline to nominate" if you're not
//     naming anyone.
//
// TODO(real build): in-memory only, no persistence; no auth (anyone who
// can reach these endpoints can register or clear any account's
// nominees); no real identity verification of the nominee or guardian —
// GuardianIdentityDocumentReference is accepted as an unverified string,
// exactly like nomineesuccession's own honesty note about
// NomineeIdentityDocumentRef; no e-signature / witness requirement that a
// real nomination form usually needs.
package nomineedesignation

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	minValidPercentageAllocation = 1
	maxValidPercentageAllocation = 100
	requiredCompleteAllocation   = 100
	minorAgeThresholdYears       = 18
)

var (
	// ErrAccountIdentifierRequired is returned when a caller supplies an
	// empty accountIdentifier.
	ErrAccountIdentifierRequired = errors.New("accountIdentifier is required")

	// ErrNomineeFullNameRequired is returned when a nominee's full name is
	// blank.
	ErrNomineeFullNameRequired = errors.New("nominee fullName is required")

	// ErrNomineeRelationshipRequired is returned when a nominee's
	// relationship to the account holder is blank.
	ErrNomineeRelationshipRequired = errors.New("nominee relationship is required")

	// ErrInvalidDateOfBirth is returned when a nominee's date of birth is
	// zero-valued or in the future.
	ErrInvalidDateOfBirth = errors.New("nominee dateOfBirth is required and cannot be in the future")

	// ErrInvalidPercentageAllocation is returned when a single nominee's
	// percentage allocation falls outside [1, 100].
	ErrInvalidPercentageAllocation = fmt.Errorf("percentageAllocation must be between %d and %d", minValidPercentageAllocation, maxValidPercentageAllocation)

	// ErrGuardianDetailsRequiredForMinorNominee is returned when a nominee
	// computed as under 18 is missing guardian name, relationship, or
	// identity document reference.
	ErrGuardianDetailsRequiredForMinorNominee = errors.New("guardianFullName, guardianRelationship, and guardianIdentityDocumentReference are all required for a nominee who is a minor")

	// ErrPercentageAllocationsMustSumTo100 is returned by SubmitNomination
	// when the submitted nominees' percentage allocations don't add up to
	// exactly 100 — the hard gate on a complete, submittable nomination
	// form.
	ErrPercentageAllocationsMustSumTo100 = errors.New("percentage allocations across all nominees must sum to exactly 100")

	// ErrPercentageAllocationsExceed100 is returned by AddNominee/
	// UpdateNominee when applying the change would push the account's
	// total allocation over 100 — allowed to stay under 100 as an
	// incomplete draft (see IsComplete), but never allowed to exceed it.
	ErrPercentageAllocationsExceed100 = errors.New("percentage allocations across all nominees would exceed 100")

	// ErrAtLeastOneNomineeOrOptOutRequired is returned by SubmitNomination
	// when zero nominees are supplied and isOptedOutOfNomination is false
	// — a real form requires an explicit opt-out if you're naming no one.
	ErrAtLeastOneNomineeOrOptOutRequired = errors.New("at least one nominee is required, or isOptedOutOfNomination must be true")

	// ErrNoDesignationFound is returned by AddNominee/UpdateNominee/
	// RemoveNominee when the account has never submitted or built any
	// nominee designation yet.
	ErrNoDesignationFound = errors.New("no nominee designation exists for this account")

	// ErrNomineeNotFound is returned by UpdateNominee/RemoveNominee when
	// nomineeId doesn't match any nominee currently on the account.
	ErrNomineeNotFound = errors.New("no nominee with that nomineeId exists for this account")
)

// NomineeInput is the caller-supplied shape of one nominee — everything
// except the server-assigned NomineeId.
type NomineeInput struct {
	FullName                          string    `json:"fullName"`
	Relationship                      string    `json:"relationship"`
	DateOfBirth                       time.Time `json:"dateOfBirth"`
	PercentageAllocation              int       `json:"percentageAllocation"`
	GuardianFullName                  string    `json:"guardianFullName,omitempty"`
	GuardianRelationship              string    `json:"guardianRelationship,omitempty"`
	GuardianIdentityDocumentReference string    `json:"guardianIdentityDocumentReference,omitempty"`
}

// Nominee is one real, stored nominee — NomineeInput plus the id the
// registry assigned it on creation.
type Nominee struct {
	NomineeId string `json:"nomineeId"`
	NomineeInput
}

// IsMinorAsOf reports whether this nominee's computed age, as of asOfTime,
// is under minorAgeThresholdYears.
func (nominee Nominee) IsMinorAsOf(asOfTime time.Time) bool {
	return computeAgeInYears(nominee.DateOfBirth, asOfTime) < minorAgeThresholdYears
}

// NomineeDesignation is the full nomination record for one account: every
// currently-registered nominee, and whether the account holder has
// explicitly opted out of nominating anyone.
type NomineeDesignation struct {
	AccountIdentifier      string    `json:"accountIdentifier"`
	IsOptedOutOfNomination bool      `json:"isOptedOutOfNomination"`
	Nominees               []Nominee `json:"nominees"`
	LastUpdatedAtTime      time.Time `json:"lastUpdatedAtTime"`
}

// TotalPercentageAllocated sums every nominee's PercentageAllocation.
func (designation NomineeDesignation) TotalPercentageAllocated() int {
	total := 0
	for _, nominee := range designation.Nominees {
		total += nominee.PercentageAllocation
	}
	return total
}

// IsComplete reports whether this designation is a fully valid,
// submittable nomination: either explicitly opted out, or at least one
// nominee with allocations summing to exactly 100.
func (designation NomineeDesignation) IsComplete() bool {
	if designation.IsOptedOutOfNomination {
		return true
	}
	return len(designation.Nominees) > 0 && designation.TotalPercentageAllocated() == requiredCompleteAllocation
}

// NomineeDesignationRegistry is the mutex-guarded, in-memory home for
// every account's nominee designation. Safe for concurrent use.
type NomineeDesignationRegistry struct {
	mutexGuardingState   sync.Mutex
	designationByAccount map[string]*NomineeDesignation
}

// NewNomineeDesignationRegistry builds an empty registry.
func NewNomineeDesignationRegistry() *NomineeDesignationRegistry {
	return &NomineeDesignationRegistry{
		designationByAccount: make(map[string]*NomineeDesignation),
	}
}

// SubmitNomination is the real "fill in the nomination form and submit
// it" action: it REPLACES any prior designation for the account with the
// supplied nominees (or the opt-out flag), applying the full hard-gate
// validation a completed form requires — every nominee's own fields valid,
// guardian details present for any minor, and (unless opted out)
// allocations summing to EXACTLY 100 with at least one nominee. On any
// validation failure, the account's prior designation (if any) is left
// untouched.
func (registry *NomineeDesignationRegistry) SubmitNomination(
	accountIdentifier string,
	nomineeInputs []NomineeInput,
	isOptedOutOfNomination bool,
	asOfTime time.Time,
) (NomineeDesignation, error) {
	if accountIdentifier == "" {
		return NomineeDesignation{}, ErrAccountIdentifierRequired
	}

	nominees, buildErr := buildValidatedNominees(nomineeInputs, asOfTime)
	if buildErr != nil {
		return NomineeDesignation{}, buildErr
	}

	if !isOptedOutOfNomination {
		if len(nominees) == 0 {
			return NomineeDesignation{}, ErrAtLeastOneNomineeOrOptOutRequired
		}
		if totalPercentage(nominees) != requiredCompleteAllocation {
			return NomineeDesignation{}, ErrPercentageAllocationsMustSumTo100
		}
	} else {
		// Opting out is mutually exclusive with naming nominees — a real
		// form doesn't let you tick "I decline to nominate" and also list
		// beneficiaries.
		nominees = nil
	}

	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	designation := &NomineeDesignation{
		AccountIdentifier:      accountIdentifier,
		IsOptedOutOfNomination: isOptedOutOfNomination,
		Nominees:               nominees,
		LastUpdatedAtTime:      asOfTime,
	}
	registry.designationByAccount[accountIdentifier] = designation
	return *designation, nil
}

// AddNominee appends one nominee to the account's existing designation
// (creating an empty one first if none exists yet), un-setting any prior
// opt-out. Unlike SubmitNomination, this does NOT require the resulting
// allocation to already total 100 — a draft is allowed to stay under 100
// while it's being built up — but it DOES reject any add that would push
// the total over 100, and it always validates the new nominee's own
// fields (including guardian details if a minor).
func (registry *NomineeDesignationRegistry) AddNominee(
	accountIdentifier string,
	input NomineeInput,
	asOfTime time.Time,
) (NomineeDesignation, error) {
	if accountIdentifier == "" {
		return NomineeDesignation{}, ErrAccountIdentifierRequired
	}

	newNominee, validateErr := validateNomineeInput(input, asOfTime)
	if validateErr != nil {
		return NomineeDesignation{}, validateErr
	}

	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	existing := registry.designationByAccount[accountIdentifier]
	existingNominees := []Nominee{}
	if existing != nil {
		existingNominees = append(existingNominees, existing.Nominees...)
	}

	if totalPercentage(existingNominees)+newNominee.PercentageAllocation > maxValidPercentageAllocation {
		return NomineeDesignation{}, ErrPercentageAllocationsExceed100
	}

	newNominee.NomineeId = generateNomineeId()
	updatedNominees := append(existingNominees, newNominee)

	designation := &NomineeDesignation{
		AccountIdentifier:      accountIdentifier,
		IsOptedOutOfNomination: false,
		Nominees:               updatedNominees,
		LastUpdatedAtTime:      asOfTime,
	}
	registry.designationByAccount[accountIdentifier] = designation
	return *designation, nil
}

// UpdateNominee replaces the fields of an existing nominee (identified by
// nomineeId) in place, re-validating that nominee's own fields and that
// the resulting total allocation still does not exceed 100 (it may
// legitimately still be under 100 — see AddNominee's doc comment).
func (registry *NomineeDesignationRegistry) UpdateNominee(
	accountIdentifier string,
	nomineeId string,
	input NomineeInput,
	asOfTime time.Time,
) (NomineeDesignation, error) {
	if accountIdentifier == "" {
		return NomineeDesignation{}, ErrAccountIdentifierRequired
	}

	updatedNominee, validateErr := validateNomineeInput(input, asOfTime)
	if validateErr != nil {
		return NomineeDesignation{}, validateErr
	}

	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	existing := registry.designationByAccount[accountIdentifier]
	if existing == nil {
		return NomineeDesignation{}, ErrNoDesignationFound
	}

	matchedIndex := -1
	for index, nominee := range existing.Nominees {
		if nominee.NomineeId == nomineeId {
			matchedIndex = index
			break
		}
	}
	if matchedIndex == -1 {
		return NomineeDesignation{}, ErrNomineeNotFound
	}

	updatedNominees := make([]Nominee, len(existing.Nominees))
	copy(updatedNominees, existing.Nominees)
	updatedNominee.NomineeId = nomineeId
	updatedNominees[matchedIndex] = updatedNominee

	if totalPercentage(updatedNominees) > maxValidPercentageAllocation {
		return NomineeDesignation{}, ErrPercentageAllocationsExceed100
	}

	designation := &NomineeDesignation{
		AccountIdentifier:      accountIdentifier,
		IsOptedOutOfNomination: existing.IsOptedOutOfNomination,
		Nominees:               updatedNominees,
		LastUpdatedAtTime:      asOfTime,
	}
	registry.designationByAccount[accountIdentifier] = designation
	return *designation, nil
}

// RemoveNominee drops one nominee (identified by nomineeId) from the
// account's designation. Removing always reduces the total allocation, so
// it can never violate the "must not exceed 100" rule — but the resulting
// designation may become incomplete (see IsComplete) if it now has zero
// nominees and isOptedOutOfNomination is still false; that's a valid,
// queryable draft state, not an error.
func (registry *NomineeDesignationRegistry) RemoveNominee(
	accountIdentifier string,
	nomineeId string,
	asOfTime time.Time,
) (NomineeDesignation, error) {
	if accountIdentifier == "" {
		return NomineeDesignation{}, ErrAccountIdentifierRequired
	}

	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	existing := registry.designationByAccount[accountIdentifier]
	if existing == nil {
		return NomineeDesignation{}, ErrNoDesignationFound
	}

	matchedIndex := -1
	for index, nominee := range existing.Nominees {
		if nominee.NomineeId == nomineeId {
			matchedIndex = index
			break
		}
	}
	if matchedIndex == -1 {
		return NomineeDesignation{}, ErrNomineeNotFound
	}

	updatedNominees := make([]Nominee, 0, len(existing.Nominees)-1)
	updatedNominees = append(updatedNominees, existing.Nominees[:matchedIndex]...)
	updatedNominees = append(updatedNominees, existing.Nominees[matchedIndex+1:]...)

	designation := &NomineeDesignation{
		AccountIdentifier:      accountIdentifier,
		IsOptedOutOfNomination: existing.IsOptedOutOfNomination,
		Nominees:               updatedNominees,
		LastUpdatedAtTime:      asOfTime,
	}
	registry.designationByAccount[accountIdentifier] = designation
	return *designation, nil
}

// GetDesignation returns the currently-stored nominee designation for an
// account, if any has ever been submitted or built.
func (registry *NomineeDesignationRegistry) GetDesignation(accountIdentifier string) (NomineeDesignation, bool) {
	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	existing, exists := registry.designationByAccount[accountIdentifier]
	if !exists {
		return NomineeDesignation{}, false
	}
	return *existing, true
}

// buildValidatedNominees validates every input and assigns each a fresh
// NomineeId. Returns the first validation error encountered, if any.
func buildValidatedNominees(inputs []NomineeInput, asOfTime time.Time) ([]Nominee, error) {
	nominees := make([]Nominee, 0, len(inputs))
	for _, input := range inputs {
		nominee, validateErr := validateNomineeInput(input, asOfTime)
		if validateErr != nil {
			return nil, validateErr
		}
		nominee.NomineeId = generateNomineeId()
		nominees = append(nominees, nominee)
	}
	return nominees, nil
}

// validateNomineeInput checks one nominee's own fields in isolation
// (nothing about the wider set's total allocation) and returns the
// resulting Nominee with a blank NomineeId (the caller assigns one).
func validateNomineeInput(input NomineeInput, asOfTime time.Time) (Nominee, error) {
	if input.FullName == "" {
		return Nominee{}, ErrNomineeFullNameRequired
	}
	if input.Relationship == "" {
		return Nominee{}, ErrNomineeRelationshipRequired
	}
	if input.DateOfBirth.IsZero() || input.DateOfBirth.After(asOfTime) {
		return Nominee{}, ErrInvalidDateOfBirth
	}
	if input.PercentageAllocation < minValidPercentageAllocation || input.PercentageAllocation > maxValidPercentageAllocation {
		return Nominee{}, ErrInvalidPercentageAllocation
	}

	nominee := Nominee{NomineeInput: input}
	if nominee.IsMinorAsOf(asOfTime) {
		if input.GuardianFullName == "" || input.GuardianRelationship == "" || input.GuardianIdentityDocumentReference == "" {
			return Nominee{}, ErrGuardianDetailsRequiredForMinorNominee
		}
	}
	return nominee, nil
}

func totalPercentage(nominees []Nominee) int {
	total := 0
	for _, nominee := range nominees {
		total += nominee.PercentageAllocation
	}
	return total
}

// computeAgeInYears computes a whole-years age as of asOfTime, correctly
// handling whether the birthday has occurred yet this year.
func computeAgeInYears(dateOfBirth time.Time, asOfTime time.Time) int {
	age := asOfTime.Year() - dateOfBirth.Year()
	birthdayThisYear := time.Date(asOfTime.Year(), dateOfBirth.Month(), dateOfBirth.Day(), 0, 0, 0, 0, asOfTime.Location())
	if asOfTime.Before(birthdayThisYear) {
		age--
	}
	return age
}

func generateNomineeId() string {
	randomBytes := make([]byte, 8)
	if _, readErr := rand.Read(randomBytes); readErr != nil {
		// crypto/rand.Read on the standard reader essentially never fails
		// on supported platforms; falling back to a fixed-but-unique-
		// enough suffix keeps this function's signature simple (no error
		// return) rather than propagating a practically-unreachable error
		// through every caller.
		return fmt.Sprintf("nominee-fallback-%d", time.Now().UnixNano())
	}
	return "nominee-" + hex.EncodeToString(randomBytes)
}
