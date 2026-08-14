// Package marginfunding implements FEATURES.md §2's "Margin funding /
// instant margin against pledged collateral" — an account with pledged
// collateral (internal/marginpledge) can request a real CASH ADVANCE up
// to whatever of its pledged margin value isn't already drawn against.
//
// This is REAL money movement, not a bookkeeping fiction: a successful
// request disburses cash via a genuine balanced journal entry posted to
// `ledger` (internal/ledgerclient) — the client account is credited
// (increased) and FirmMarginFundingClearingAccountIdentifier is debited
// (decreased) by the same amount, mirroring the pattern
// internal/ledgerclient's PostTradeSettlementJournalEntry already uses
// for trade settlement (see that constant's own doc comment for why it
// currently resolves to the pre-seeded "firm-clearing-acct" rather than a
// brand-new ledger account). The disbursed amount becomes a real, tracked
// outstanding PRINCIPAL balance against that account — every subsequent
// request is capped by (pledged collateral value - principal already
// outstanding), never by the raw pledged value alone.
//
// LOUD, HONEST GAPS:
//  1. Interest accrual is an ILLUSTRATIVE placeholder
//     (CalculateIllustrativeAccruedInterest below) — it is NOT wired to
//     run automatically or to post any real interest journal entry. A
//     real build would accrue interest daily (or continuously) against
//     outstanding principal and actually charge it, likely via its own
//     scheduled ledger posting. This package only tracks principal owed
//     with genuine precision; interest is a documented future extension.
//  2. "Pledged margin value" is read from internal/marginpledge, which
//     itself carries the same illustrative-haircut-table and
//     caller-supplied-reference-price caveats already documented there —
//     every capacity figure this package computes inherits those
//     caveats.
//  3. No repayment endpoint ships in this build — RepayFunding exists so
//     the loan balance can genuinely be paid down and is fully tested,
//     but cmd/server/main.go does not yet expose it over HTTP. A real
//     build needs a POST /margin-funding/repay endpoint posting the
//     reverse journal entry.
//  4. In-memory only, same as every other package in this service — an
//     oms-gateway restart loses every outstanding loan balance.
package marginfunding

import (
	"errors"
	"sync"
	"time"
)

var (
	// ErrFundingAmountMustBePositive is returned when a funding request
	// or repayment is attempted with a zero or negative amount.
	ErrFundingAmountMustBePositive = errors.New("margin funding amount must be greater than zero")

	// ErrInsufficientUnutilizedPledgedCollateral is returned when the
	// requested funding amount exceeds what's actually backed by
	// currently-pledged, not-already-drawn-against collateral — i.e.
	// pledgedMarginValueInMinorUnits - already-outstanding principal.
	ErrInsufficientUnutilizedPledgedCollateral = errors.New("requested amount exceeds unutilized pledged collateral available for margin funding")

	// ErrRepaymentExceedsOutstandingPrincipal is returned when a
	// repayment attempts to pay down more than the account currently owes.
	ErrRepaymentExceedsOutstandingPrincipal = errors.New("repayment amount exceeds outstanding principal owed")
)

// FirmMarginFundingClearingAccountIdentifier is the ledger account
// margin-funding disbursements are drawn from and repayments return to.
//
// HONEST GAP: FEATURES.md's suggested "firm-margin-funding-acct" would be
// a NEW, dedicated ledger account distinct from firm-clearing-acct, so a
// real build could reconcile margin-funding cash flow separately from
// trade-settlement cash flow. `services/ledger`'s account list is
// hardcoded at its own startup (cmd/server/main.go there) and is outside
// this service's boundary to provision — oms-gateway cannot create new
// ledger accounts through any API it's given. This build therefore
// reuses the already-seeded `firm-clearing-acct` as the real clearing
// account for margin-funding cash movement too: the money movement
// itself is 100% real (a genuine balanced journal entry the ledger
// actually posts, verifiable via GET /accounts/balance), only the
// bookkeeping SEPARATION between trade-settlement and margin-funding
// cash flow is not yet a distinct ledger account. A real build should
// add "firm-margin-funding-acct" to ledger's seeded account list.
const FirmMarginFundingClearingAccountIdentifier = "firm-clearing-acct"

// illustrativeAnnualInterestRatePercent is a made-up, order-of-magnitude
// interest rate — NOT sourced from any real published margin-funding rate
// card. See the package doc's "LOUD, HONEST GAPS" §1: this exists purely
// to demonstrate the shape of an interest calculation, and nothing in
// this package applies it automatically.
const illustrativeAnnualInterestRatePercent = 12.0

// FundingRecord is one account's current margin-funding loan state.
type FundingRecord struct {
	ClientAccountIdentifier          string `json:"clientAccountIdentifier"`
	OutstandingPrincipalInMinorUnits int64  `json:"outstandingPrincipalInMinorUnits"`
}

// FundingBook is the mutex-guarded state machine tracking every account's
// outstanding margin-funding principal.
type FundingBook struct {
	mutexGuardingFunding sync.Mutex

	outstandingPrincipalByAccount map[string]int64

	// disbursementStartTimeByAccount: FEATURES.md §21's live interest
	// cost calculator (see costCalculator.go) needs a real "since when
	// has this account owed interest" instant to compute days-elapsed
	// from. Set via RecordDisbursementStartTime (called by the HTTP
	// handler with the real wall clock right after a successful
	// RequestFunding — this package itself never reads the wall clock
	// internally, same discipline as internal/algolimits). Cleared
	// automatically the moment RepayFunding brings an account's
	// principal back to exactly zero, so a LATER fresh draw restarts the
	// interest clock rather than inheriting a stale start time.
	//
	// KNOWN SIMPLIFICATION: if an account draws MORE funding while it
	// already has outstanding principal, this start time is NOT reset —
	// the whole (old + new) principal accrues from the ORIGINAL draw
	// date. A real build would track each individual disbursement
	// tranche's own start date and sum per-tranche interest; this
	// package tracks one blended start time per account, the same
	// "illustrative but real, hand-checkable" caliber as this package's
	// other simplifications.
	disbursementStartTimeByAccount map[string]time.Time
}

func NewFundingBook() *FundingBook {
	return &FundingBook{
		outstandingPrincipalByAccount:  make(map[string]int64),
		disbursementStartTimeByAccount: make(map[string]time.Time),
	}
}

// RecordDisbursementStartTime marks `now` as the real instant an
// account's interest clock starts running — ONLY if it doesn't already
// have one recorded (a second draw while principal is already
// outstanding does not push the clock forward; see the struct field's
// doc comment for the honest blended-principal simplification this
// implies). Idempotent and safe to call on every successful funding
// request.
func (fundingBook *FundingBook) RecordDisbursementStartTime(clientAccountIdentifier string, now time.Time) {
	fundingBook.mutexGuardingFunding.Lock()
	defer fundingBook.mutexGuardingFunding.Unlock()

	if _, alreadyRecorded := fundingBook.disbursementStartTimeByAccount[clientAccountIdentifier]; !alreadyRecorded {
		fundingBook.disbursementStartTimeByAccount[clientAccountIdentifier] = now
	}
}

// DisbursementStartTime returns the account's currently recorded
// interest-clock start time, and whether one exists at all (false if the
// account has never drawn funding, or fully repaid it and hasn't drawn
// again since).
func (fundingBook *FundingBook) DisbursementStartTime(clientAccountIdentifier string) (time.Time, bool) {
	fundingBook.mutexGuardingFunding.Lock()
	defer fundingBook.mutexGuardingFunding.Unlock()

	startTime, exists := fundingBook.disbursementStartTimeByAccount[clientAccountIdentifier]
	return startTime, exists
}

// RequestFunding reserves `requestedAmountInMinorUnits` of new principal
// against `clientAccountIdentifier`, provided doing so would not push the
// account's total outstanding principal above
// `pledgedMarginValueInMinorUnits` (the account's current total pledged
// collateral value, as reported by internal/marginpledge — this package
// deliberately does not import marginpledge itself, the same decoupled
// pattern marginpledge uses for riskengine and positions).
//
// On success, returns the account's new total outstanding principal. The
// caller is responsible for actually disbursing the cash (a real ledger
// journal entry) and must call RollbackReservation with the same amount
// if that disbursement fails, so the reservation doesn't leak.
func (fundingBook *FundingBook) RequestFunding(
	clientAccountIdentifier string,
	requestedAmountInMinorUnits int64,
	pledgedMarginValueInMinorUnits int64,
) (int64, error) {
	if requestedAmountInMinorUnits <= 0 {
		return 0, ErrFundingAmountMustBePositive
	}

	fundingBook.mutexGuardingFunding.Lock()
	defer fundingBook.mutexGuardingFunding.Unlock()

	currentOutstanding := fundingBook.outstandingPrincipalByAccount[clientAccountIdentifier]
	unutilizedPledgedCollateral := pledgedMarginValueInMinorUnits - currentOutstanding
	if requestedAmountInMinorUnits > unutilizedPledgedCollateral {
		return 0, ErrInsufficientUnutilizedPledgedCollateral
	}

	newOutstanding := currentOutstanding + requestedAmountInMinorUnits
	fundingBook.outstandingPrincipalByAccount[clientAccountIdentifier] = newOutstanding
	return newOutstanding, nil
}

// RollbackReservation reverses a RequestFunding reservation that was
// approved here but whose actual cash disbursement subsequently failed
// (e.g. the ledger was unreachable) — without this, a failed disbursement
// would permanently and incorrectly consume the account's funding
// capacity for a cash advance that never actually happened.
func (fundingBook *FundingBook) RollbackReservation(clientAccountIdentifier string, amountInMinorUnits int64) {
	fundingBook.mutexGuardingFunding.Lock()
	defer fundingBook.mutexGuardingFunding.Unlock()

	fundingBook.outstandingPrincipalByAccount[clientAccountIdentifier] -= amountInMinorUnits
	if fundingBook.outstandingPrincipalByAccount[clientAccountIdentifier] < 0 {
		fundingBook.outstandingPrincipalByAccount[clientAccountIdentifier] = 0
	}
}

// RepayFunding pays down `amountInMinorUnits` of outstanding principal for
// `clientAccountIdentifier`. Returns the account's new total outstanding
// principal. See the package doc's gap §3: not yet wired to an HTTP
// endpoint, but fully real and tested.
func (fundingBook *FundingBook) RepayFunding(clientAccountIdentifier string, amountInMinorUnits int64) (int64, error) {
	if amountInMinorUnits <= 0 {
		return 0, ErrFundingAmountMustBePositive
	}

	fundingBook.mutexGuardingFunding.Lock()
	defer fundingBook.mutexGuardingFunding.Unlock()

	currentOutstanding := fundingBook.outstandingPrincipalByAccount[clientAccountIdentifier]
	if amountInMinorUnits > currentOutstanding {
		return 0, ErrRepaymentExceedsOutstandingPrincipal
	}

	newOutstanding := currentOutstanding - amountInMinorUnits
	fundingBook.outstandingPrincipalByAccount[clientAccountIdentifier] = newOutstanding
	if newOutstanding == 0 {
		// Fully repaid — clear the interest-clock start time so a future
		// fresh draw restarts it instead of inheriting this repaid loan's
		// stale start date. See disbursementStartTimeByAccount's doc
		// comment.
		delete(fundingBook.disbursementStartTimeByAccount, clientAccountIdentifier)
	}
	return newOutstanding, nil
}

// OutstandingPrincipalInMinorUnits returns the account's current
// outstanding margin-funding principal (0 if it has never borrowed).
func (fundingBook *FundingBook) OutstandingPrincipalInMinorUnits(clientAccountIdentifier string) int64 {
	fundingBook.mutexGuardingFunding.Lock()
	defer fundingBook.mutexGuardingFunding.Unlock()

	return fundingBook.outstandingPrincipalByAccount[clientAccountIdentifier]
}

// FundingRecordForAccount returns a snapshot FundingRecord for one account.
func (fundingBook *FundingBook) FundingRecordForAccount(clientAccountIdentifier string) FundingRecord {
	fundingBook.mutexGuardingFunding.Lock()
	defer fundingBook.mutexGuardingFunding.Unlock()

	return FundingRecord{
		ClientAccountIdentifier:          clientAccountIdentifier,
		OutstandingPrincipalInMinorUnits: fundingBook.outstandingPrincipalByAccount[clientAccountIdentifier],
	}
}

// CalculateIllustrativeAccruedInterest computes simple (non-compounding)
// interest owed on `principalInMinorUnits` over `numberOfDaysElapsed`
// days, using illustrativeAnnualInterestRatePercent. NOT called anywhere
// automatically — see the package doc's gap §1. Exposed purely so the
// illustrative rate/formula is at least real, tested code rather than a
// number quoted only in a comment.
func CalculateIllustrativeAccruedInterest(principalInMinorUnits int64, numberOfDaysElapsed int64) int64 {
	if principalInMinorUnits <= 0 || numberOfDaysElapsed <= 0 {
		return 0
	}
	dailyRate := (illustrativeAnnualInterestRatePercent / 100.0) / 365.0
	accrued := float64(principalInMinorUnits) * dailyRate * float64(numberOfDaysElapsed)
	return roundToNearestMinorUnit(accrued)
}

// roundToNearestMinorUnit mirrors chargescalculator's / marginengine's /
// marginpledge's helper of the same name.
func roundToNearestMinorUnit(amount float64) int64 {
	if amount < 0 {
		return int64(amount - 0.5)
	}
	return int64(amount + 0.5)
}
