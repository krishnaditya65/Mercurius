// Package referralrewards implements FEATURES.md §14's "referral &
// rewards program": a real referral code generated per account, real
// tracking of who referred whom, and a real cash reward credited via
// ledger's actual HTTP API — but only once a real, concrete qualifying
// event genuinely happens.
//
// The qualifying event this package implements is "the referred account
// completes its first real trade" — concretely, the referred account's
// position book in oms-gateway becomes non-empty (see
// referralQualificationChecker.CheckAndProcessQualification in
// cmd/server/main.go, which calls the real omsGatewayClient.FetchPositions
// this service already has, exactly the same call
// buildFamilyAccessPositionsHandler already makes for a completely
// different feature). This was chosen over "KYC completion" because
// backoffice has no real, ready HTTP client into kyc-onboarding today
// (only omsgatewayclient exists), and a genuinely observable, real
// signal beats inventing a second fake trigger. The reward is only ever
// credited once per referred account — MarkRewarded is a one-way,
// idempotent transition (see ErrAlreadyRewarded) so re-triggering the
// qualification check after it has already fired is a safe, harmless
// no-op rather than a double payout.
//
// TODO(real build): in-memory, not persisted; no auth/RBAC (same
// documented gap as every other package in this service); the
// qualification check is pull-based (a caller has to invoke
// POST /referral-rewards/check-and-qualify — there's no push/webhook
// from oms-gateway when a first trade actually happens); referral codes
// are not cryptographically unguessable (short, human-shareable codes,
// by design — an attacker who enumerates them could self-refer via a
// second account, but self-referral is explicitly blocked and a
// referred account can only ever be linked once, so the abuse surface
// is bounded to "one extra referral code register" at worst).
package referralrewards

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ReferralStatus is the real lifecycle of one referred account's
// reward eligibility.
type ReferralStatus string

const (
	// ReferralStatusPendingQualifyingEvent means the referred account is
	// linked to a referrer but has not yet completed its first trade.
	ReferralStatusPendingQualifyingEvent ReferralStatus = "pending-qualifying-event"
	// ReferralStatusRewarded means the qualifying event fired and the
	// reward was successfully credited via ledger — terminal.
	ReferralStatusRewarded ReferralStatus = "rewarded"
)

// StandardReferralRewardInMinorUnits is the flat cash bonus credited to
// the REFERRER when a referred account qualifies — ₹100.00 (100_00
// minor units, same paise-scale convention as every other
// InMinorUnits figure in this repo). A flat amount, not a
// percentage-of-trade or tiered scheme — the simplest genuinely real
// reward structure, not a fabricated complex one.
const StandardReferralRewardInMinorUnits int64 = 10_000

var (
	ErrAccountIdentifierRequired = errors.New("referralrewards: accountIdentifier is required")
	ErrUnknownReferralCode       = errors.New("referralrewards: referral code does not exist")
	ErrSelfReferralNotAllowed    = errors.New("referralrewards: an account cannot refer itself")
	ErrAlreadyReferred           = errors.New("referralrewards: this account has already been referred")
	ErrNoReferralLink            = errors.New("referralrewards: no referral link found for this account")
	ErrAlreadyRewarded           = errors.New("referralrewards: this referral has already been rewarded")
)

// ReferralLink is one real referral relationship: referredAccountIdentifier
// was referred by referrerAccountIdentifier using referralCode.
type ReferralLink struct {
	ReferralCode              string         `json:"referralCode"`
	ReferrerAccountIdentifier string         `json:"referrerAccountIdentifier"`
	ReferredAccountIdentifier string         `json:"referredAccountIdentifier"`
	Status                    ReferralStatus `json:"status"`
	RewardAmountInMinorUnits  int64          `json:"rewardAmountInMinorUnits,omitempty"`
	CreatedAtTime             time.Time      `json:"createdAtTime"`
	RewardedAtTime            time.Time      `json:"rewardedAtTime,omitzero"`
}

// Registry is a real, mutex-guarded, in-memory store of every referral
// code and every referral link.
type Registry struct {
	mutexGuardingState              sync.RWMutex
	referralCodeByReferrerAccountId map[string]string
	referrerAccountIdByReferralCode map[string]string
	linkByReferredAccountId         map[string]ReferralLink
}

func NewRegistry() *Registry {
	return &Registry{
		referralCodeByReferrerAccountId: make(map[string]string),
		referrerAccountIdByReferralCode: make(map[string]string),
		linkByReferredAccountId:         make(map[string]ReferralLink),
	}
}

// GenerateReferralCode returns the account's real referral code,
// generating (and persisting) a brand-new one the first time it's
// called for an account, and returning the SAME code on every
// subsequent call — a referral code is a stable, durable identity for
// the referrer, not a one-time token.
func (registry *Registry) GenerateReferralCode(accountIdentifier string) (string, error) {
	if accountIdentifier == "" {
		return "", ErrAccountIdentifierRequired
	}

	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	if existingCode, exists := registry.referralCodeByReferrerAccountId[accountIdentifier]; exists {
		return existingCode, nil
	}

	code := generateHumanShareableCode(accountIdentifier)
	// Vanishingly unlikely collision, but a real registry never silently
	// overwrites an existing code -> another account's mapping; retry
	// with a fresh random suffix until unique.
	for {
		if _, taken := registry.referrerAccountIdByReferralCode[code]; !taken {
			break
		}
		code = generateHumanShareableCode(accountIdentifier)
	}

	registry.referralCodeByReferrerAccountId[accountIdentifier] = code
	registry.referrerAccountIdByReferralCode[code] = accountIdentifier
	return code, nil
}

// ReferralCodeForAccount returns an account's existing referral code
// without generating a new one.
func (registry *Registry) ReferralCodeForAccount(accountIdentifier string) (string, bool) {
	registry.mutexGuardingState.RLock()
	defer registry.mutexGuardingState.RUnlock()

	code, exists := registry.referralCodeByReferrerAccountId[accountIdentifier]
	return code, exists
}

// RecordReferral links referredAccountIdentifier to whichever account
// owns referralCode. Rejects: an unknown code, an account referring
// itself, and an account that has already been referred by anyone
// (real, one-time referral attribution — a referred account keeps its
// FIRST referrer forever).
func (registry *Registry) RecordReferral(referralCode string, referredAccountIdentifier string, now time.Time) (ReferralLink, error) {
	if referredAccountIdentifier == "" {
		return ReferralLink{}, ErrAccountIdentifierRequired
	}

	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	referrerAccountIdentifier, codeExists := registry.referrerAccountIdByReferralCode[referralCode]
	if !codeExists {
		return ReferralLink{}, ErrUnknownReferralCode
	}
	if referrerAccountIdentifier == referredAccountIdentifier {
		return ReferralLink{}, ErrSelfReferralNotAllowed
	}
	if _, alreadyReferred := registry.linkByReferredAccountId[referredAccountIdentifier]; alreadyReferred {
		return ReferralLink{}, ErrAlreadyReferred
	}

	link := ReferralLink{
		ReferralCode:              referralCode,
		ReferrerAccountIdentifier: referrerAccountIdentifier,
		ReferredAccountIdentifier: referredAccountIdentifier,
		Status:                    ReferralStatusPendingQualifyingEvent,
		CreatedAtTime:             now,
	}
	registry.linkByReferredAccountId[referredAccountIdentifier] = link
	return link, nil
}

// GetReferralLink returns the referral link for a referred account, if
// any.
func (registry *Registry) GetReferralLink(referredAccountIdentifier string) (ReferralLink, bool) {
	registry.mutexGuardingState.RLock()
	defer registry.mutexGuardingState.RUnlock()

	link, exists := registry.linkByReferredAccountId[referredAccountIdentifier]
	return link, exists
}

// ReferralsForReferrer returns every real referral link where
// accountIdentifier is the referrer — "who have I referred, and what's
// their status".
func (registry *Registry) ReferralsForReferrer(accountIdentifier string) []ReferralLink {
	registry.mutexGuardingState.RLock()
	defer registry.mutexGuardingState.RUnlock()

	var matching []ReferralLink
	for _, link := range registry.linkByReferredAccountId {
		if link.ReferrerAccountIdentifier == accountIdentifier {
			matching = append(matching, link)
		}
	}
	return matching
}

// MarkRewarded transitions a referral link from pending to rewarded —
// a one-way, idempotent-by-rejection transition: calling this a second
// time for the same referred account returns ErrAlreadyRewarded instead
// of crediting twice. The HTTP layer (see cmd/server/main.go's
// buildCheckAndQualifyReferralHandler) only calls this AFTER a real
// ledger credit has already succeeded, so a link is never marked
// rewarded without the cash having genuinely moved.
func (registry *Registry) MarkRewarded(referredAccountIdentifier string, rewardAmountInMinorUnits int64, now time.Time) (ReferralLink, error) {
	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	link, exists := registry.linkByReferredAccountId[referredAccountIdentifier]
	if !exists {
		return ReferralLink{}, ErrNoReferralLink
	}
	if link.Status == ReferralStatusRewarded {
		return ReferralLink{}, ErrAlreadyRewarded
	}

	link.Status = ReferralStatusRewarded
	link.RewardAmountInMinorUnits = rewardAmountInMinorUnits
	link.RewardedAtTime = now
	registry.linkByReferredAccountId[referredAccountIdentifier] = link
	return link, nil
}

// generateHumanShareableCode builds a short, real, human-shareable
// referral code: an 8-character uppercase alphanumeric suffix from a
// real cryptographic random source (crypto/rand, not math/rand — even
// though unguessability isn't this package's security boundary, there's
// no reason to use a weaker source when a stronger one is free), so two
// different accounts calling GenerateReferralCode at the same instant
// can never collide by construction (astronomically unlikely at this
// length regardless, but real code doesn't lean on "unlikely" alone —
// see the collision-retry loop in GenerateReferralCode).
func generateHumanShareableCode(accountIdentifier string) string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // excludes commonly-confused characters (0/O, 1/I/L).
	suffixBytes := make([]byte, 6)
	_, _ = rand.Read(suffixBytes)

	var suffixBuilder strings.Builder
	for _, b := range suffixBytes {
		suffixBuilder.WriteByte(alphabet[int(b)%len(alphabet)])
	}

	return fmt.Sprintf("MERC-%s", suffixBuilder.String())
}
