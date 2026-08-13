// Package loanagainstsecurities implements FEATURES.md §17's "Loan
// Against Securities (LAS)" — modeled closely on internal/marginfunding
// (same mutex-guarded reservation pattern, same two-phase
// reserve-then-disburse-then-rollback-on-failure flow), but a genuinely
// DISTINCT loan product, not a rename of margin funding:
//
//   - Margin funding is an INSTANT cash advance capped by the FULL pledged
//     collateral value (internal/marginpledge), intended for short-term
//     buying power.
//   - LAS is a LONGER-TENURE loan, capped at only a FRACTION of pledged
//     collateral value via illustrativeLoanToValuePercent (a real LAS
//     product's loan-to-value ratio is deliberately conservative — a
//     lender wants a real cash cushion against collateral price moves
//     over a period of months, not days) — genuinely stricter than margin
//     funding's own cap, not the same number renamed. TenureInMonths is
//     recorded per request (informational — see the gap note below).
//
// Real disbursement happens through internal/ledgerclient's
// PostLoanAgainstSecuritiesDisbursementJournalEntry — a genuine balanced
// journal entry, not a bookkeeping fiction, exactly mirroring
// internal/marginfunding's own disbursement flow.
//
// LOUD, HONEST GAPS, same caliber as internal/marginfunding's own:
//  1. illustrativeLoanToValuePercent (50%) and
//     illustrativeAnnualInterestRatePercent (9%) are MADE-UP,
//     order-of-magnitude figures, NOT sourced from any real lender's LAS
//     rate card — real LTV ratios and rates vary by pledged security's
//     liquidity/volatility class and change by lender policy.
//  2. TenureInMonths is recorded but NOT enforced by any clock-driven
//     maturity/expiry logic — this build has no real trading-calendar
//     clock at all (see internal/marketsession's own admin-toggle
//     placeholder note); a real build would need a scheduled job to flag
//     or auto-recall a matured loan.
//  3. Interest accrual (CalculateIllustrativeAccruedInterest) is a real,
//     tested formula but is NOT wired to run automatically or post any
//     interest journal entry — principal tracking is real, interest is
//     illustrative-and-inert, exactly like internal/marginfunding's own
//     gap §1.
//  4. In-memory only, same as every other package in this service.
package loanagainstsecurities

import (
	"errors"
	"sync"
)

var (
	// ErrLoanAmountMustBePositive is returned when a loan request or
	// repayment is attempted with a zero or negative amount.
	ErrLoanAmountMustBePositive = errors.New("loan amount must be greater than zero")

	// ErrTenureMustBePositive is returned when a loan request specifies a
	// zero tenure.
	ErrTenureMustBePositive = errors.New("tenureInMonths must be greater than zero")

	// ErrInsufficientLoanToValueCapacity is returned when the requested
	// loan amount exceeds the account's remaining LTV-capped capacity
	// (illustrativeLoanToValuePercent * pledged collateral value, minus
	// already-outstanding principal).
	ErrInsufficientLoanToValueCapacity = errors.New("requested amount exceeds loan-to-value-capped capacity against pledged collateral")

	// ErrRepaymentExceedsOutstandingPrincipal is returned when a
	// repayment attempts to pay down more than the account currently owes.
	ErrRepaymentExceedsOutstandingPrincipal = errors.New("repayment amount exceeds outstanding principal owed")
)

// illustrativeLoanToValuePercent / illustrativeAnnualInterestRatePercent
// — see the package doc's loud gap note above.
const (
	illustrativeLoanToValuePercent        = 0.50 // 50% of pledged collateral value
	illustrativeAnnualInterestRatePercent = 9.0  // lower than margin funding's 12%, reflecting a real LAS product's typically longer tenure/lower risk pricing — still illustrative
)

// CalculateLoanToValueCap computes the maximum TOTAL loan capacity
// (illustrativeLoanToValuePercent * pledgedMarginValueInMinorUnits) for a
// given pledged collateral value — exposed so this figure is hand-
// auditable, mirroring marginengine/chargescalculator's receipt-style
// transparency.
func CalculateLoanToValueCap(pledgedMarginValueInMinorUnits int64) int64 {
	if pledgedMarginValueInMinorUnits <= 0 {
		return 0
	}
	return roundToNearestMinorUnit(float64(pledgedMarginValueInMinorUnits) * illustrativeLoanToValuePercent)
}

// LoanRecord is one account's current LAS loan state.
type LoanRecord struct {
	ClientAccountIdentifier          string `json:"clientAccountIdentifier"`
	OutstandingPrincipalInMinorUnits int64  `json:"outstandingPrincipalInMinorUnits"`
	TenureInMonths                   uint32 `json:"tenureInMonths"`
}

// LoanBook is the mutex-guarded state machine tracking every account's
// outstanding LAS principal — the same locking discipline as
// internal/marginfunding.FundingBook.
type LoanBook struct {
	mutexGuardingLoans sync.Mutex

	outstandingPrincipalByAccount map[string]int64
	tenureByAccount               map[string]uint32
}

func NewLoanBook() *LoanBook {
	return &LoanBook{
		outstandingPrincipalByAccount: make(map[string]int64),
		tenureByAccount:               make(map[string]uint32),
	}
}

// RequestLoan reserves requestedAmountInMinorUnits of new principal
// against clientAccountIdentifier, provided doing so would not push the
// account's total outstanding LAS principal above
// CalculateLoanToValueCap(pledgedMarginValueInMinorUnits) — a genuinely
// STRICTER cap than internal/marginfunding's own (which caps at the full
// pledged value, not a fraction of it). On success, returns the account's
// new total outstanding principal and records tenureInMonths (see the
// package doc's gap §2 — informational only, not enforced). The caller is
// responsible for actually disbursing the cash (a real ledger journal
// entry via internal/ledgerclient.PostLoanAgainstSecuritiesDisbursementJournalEntry)
// and must call RollbackReservation with the same amount if that
// disbursement fails.
func (loanBook *LoanBook) RequestLoan(
	clientAccountIdentifier string,
	requestedAmountInMinorUnits int64,
	pledgedMarginValueInMinorUnits int64,
	tenureInMonths uint32,
) (int64, error) {
	if requestedAmountInMinorUnits <= 0 {
		return 0, ErrLoanAmountMustBePositive
	}
	if tenureInMonths == 0 {
		return 0, ErrTenureMustBePositive
	}

	loanBook.mutexGuardingLoans.Lock()
	defer loanBook.mutexGuardingLoans.Unlock()

	loanToValueCap := CalculateLoanToValueCap(pledgedMarginValueInMinorUnits)
	currentOutstanding := loanBook.outstandingPrincipalByAccount[clientAccountIdentifier]
	remainingCapacity := loanToValueCap - currentOutstanding
	if requestedAmountInMinorUnits > remainingCapacity {
		return 0, ErrInsufficientLoanToValueCapacity
	}

	newOutstanding := currentOutstanding + requestedAmountInMinorUnits
	loanBook.outstandingPrincipalByAccount[clientAccountIdentifier] = newOutstanding
	loanBook.tenureByAccount[clientAccountIdentifier] = tenureInMonths
	return newOutstanding, nil
}

// RollbackReservation reverses a RequestLoan reservation whose actual
// cash disbursement subsequently failed — mirrors
// internal/marginfunding.FundingBook.RollbackReservation exactly.
func (loanBook *LoanBook) RollbackReservation(clientAccountIdentifier string, amountInMinorUnits int64) {
	loanBook.mutexGuardingLoans.Lock()
	defer loanBook.mutexGuardingLoans.Unlock()

	loanBook.outstandingPrincipalByAccount[clientAccountIdentifier] -= amountInMinorUnits
	if loanBook.outstandingPrincipalByAccount[clientAccountIdentifier] < 0 {
		loanBook.outstandingPrincipalByAccount[clientAccountIdentifier] = 0
	}
}

// RepayLoan pays down amountInMinorUnits of outstanding LAS principal.
// Returns the account's new total outstanding principal.
func (loanBook *LoanBook) RepayLoan(clientAccountIdentifier string, amountInMinorUnits int64) (int64, error) {
	if amountInMinorUnits <= 0 {
		return 0, ErrLoanAmountMustBePositive
	}

	loanBook.mutexGuardingLoans.Lock()
	defer loanBook.mutexGuardingLoans.Unlock()

	currentOutstanding := loanBook.outstandingPrincipalByAccount[clientAccountIdentifier]
	if amountInMinorUnits > currentOutstanding {
		return 0, ErrRepaymentExceedsOutstandingPrincipal
	}

	newOutstanding := currentOutstanding - amountInMinorUnits
	loanBook.outstandingPrincipalByAccount[clientAccountIdentifier] = newOutstanding
	return newOutstanding, nil
}

// RestorePrincipalAfterFailedRepaymentLedgerPosting re-adds
// amountInMinorUnits of principal that RepayLoan already subtracted, for
// the case where the REPAYMENT's own ledger posting subsequently fails
// (the real cash never actually left the client's ledger balance, so the
// loan book must not pretend it was repaid) — the repayment-side mirror
// of RollbackReservation's disbursement-side rollback. Deliberately
// bypasses the loan-to-value cap check RequestLoan enforces: this is
// restoring principal that was ALREADY validly outstanding a moment ago,
// not approving new credit, so re-checking capacity here would be wrong
// and could spuriously fail.
func (loanBook *LoanBook) RestorePrincipalAfterFailedRepaymentLedgerPosting(clientAccountIdentifier string, amountInMinorUnits int64) {
	loanBook.mutexGuardingLoans.Lock()
	defer loanBook.mutexGuardingLoans.Unlock()

	loanBook.outstandingPrincipalByAccount[clientAccountIdentifier] += amountInMinorUnits
}

// OutstandingPrincipalInMinorUnits returns the account's current
// outstanding LAS principal (0 if it has never borrowed).
func (loanBook *LoanBook) OutstandingPrincipalInMinorUnits(clientAccountIdentifier string) int64 {
	loanBook.mutexGuardingLoans.Lock()
	defer loanBook.mutexGuardingLoans.Unlock()

	return loanBook.outstandingPrincipalByAccount[clientAccountIdentifier]
}

// LoanRecordForAccount returns a snapshot LoanRecord for one account.
func (loanBook *LoanBook) LoanRecordForAccount(clientAccountIdentifier string) LoanRecord {
	loanBook.mutexGuardingLoans.Lock()
	defer loanBook.mutexGuardingLoans.Unlock()

	return LoanRecord{
		ClientAccountIdentifier:          clientAccountIdentifier,
		OutstandingPrincipalInMinorUnits: loanBook.outstandingPrincipalByAccount[clientAccountIdentifier],
		TenureInMonths:                   loanBook.tenureByAccount[clientAccountIdentifier],
	}
}

// CalculateIllustrativeAccruedInterest computes simple (non-compounding)
// interest owed on principalInMinorUnits over numberOfDaysElapsed days,
// using illustrativeAnnualInterestRatePercent. NOT called anywhere
// automatically — see the package doc's gap §3.
func CalculateIllustrativeAccruedInterest(principalInMinorUnits int64, numberOfDaysElapsed int64) int64 {
	if principalInMinorUnits <= 0 || numberOfDaysElapsed <= 0 {
		return 0
	}
	dailyRate := (illustrativeAnnualInterestRatePercent / 100.0) / 365.0
	accrued := float64(principalInMinorUnits) * dailyRate * float64(numberOfDaysElapsed)
	return roundToNearestMinorUnit(accrued)
}

// roundToNearestMinorUnit mirrors marginfunding's / marginengine's /
// marginpledge's helper of the same name.
func roundToNearestMinorUnit(amount float64) int64 {
	if amount < 0 {
		return int64(amount - 0.5)
	}
	return int64(amount + 0.5)
}
