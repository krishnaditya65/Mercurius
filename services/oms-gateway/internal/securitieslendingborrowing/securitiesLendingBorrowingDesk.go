// Package securitieslendingborrowing implements FEATURES.md §15's
// "Securities Lending & Borrowing (SLB) desk": a real state machine for
// lending out a held security (earning a lending fee) or borrowing one
// (e.g. to cover a short sale), with real quantity tracking — the SAME
// PATTERN as internal/marginpledge's PledgeBook: an account's quantity of
// a symbol moves between "free" and "on loan" / "borrowed" states via a
// mutex-guarded, in-memory book, not a stub.
//
// TWO INDEPENDENT LEDGERS, MIRRORING A REAL SLB DESK: LendSecurity moves
// quantity of a symbol the LENDING account actually holds (looked up from
// internal/positions, exactly like PledgeHolding — never client-asserted)
// out of that account's freely-sellable quantity into a LendingRecord,
// crediting an illustrative lending fee. BorrowSecurity is the other side
// of the same market: a BORROWING account receives quantity of a symbol
// it does NOT need to already hold (that's the point of borrowing — e.g.
// to cover a short sale), recorded in a BorrowingRecord that accrues an
// illustrative borrowing fee until RecallLending / ReturnBorrowing closes
// it out. This package does NOT match a specific lender to a specific
// borrower (a real SLB desk runs its own matching/auction) — the two
// ledgers are independent books, exactly as independent as
// internal/marginfunding's FundingBook is from internal/marginpledge's
// PledgeBook, even though conceptually related.
//
// LOUD, REPEATED KNOWN GAP, same caliber as internal/marginpledge's own
// warning: illustrativeAnnualizedLendingFeePercent and
// illustrativeAnnualizedBorrowingFeePercent below are MADE-UP,
// order-of-magnitude rates, NOT sourced from any real securities-lending
// market (real SLB fees are set by supply/demand auction per security,
// vary daily, and are typically materially higher for "hard to borrow"
// names). Fee accrual here is a real, hand-checkable day-count formula,
// but the RATE feeding it is illustrative. In-memory only, same as every
// other package in this service.
package securitieslendingborrowing

import (
	"errors"
	"sync"
)

var (
	ErrQuantityMustBePositive                = errors.New("quantity must be greater than zero")
	ErrInsufficientUnlentHoldingQuantity     = errors.New("insufficient unlent holding quantity to lend this amount")
	ErrNoLendingRecordFound                  = errors.New("no active lending record found for this account and symbol")
	ErrRecallQuantityExceedsLentQuantity     = errors.New("recall quantity exceeds currently lent quantity")
	ErrNoBorrowingRecordFound                = errors.New("no active borrowing record found for this account and symbol")
	ErrReturnQuantityExceedsBorrowedQuantity = errors.New("return quantity exceeds currently borrowed quantity")
)

// illustrativeAnnualizedLendingFeePercent / ...BorrowingFeePercent — see
// the package doc's loud warning above.
const (
	illustrativeAnnualizedLendingFeePercent   = 0.02 // 2% p.a. credited to the lender
	illustrativeAnnualizedBorrowingFeePercent = 0.05 // 5% p.a. charged to the borrower
	daysPerYearForFeeAccrual                  = 365.0
)

// LendingRecord is one account's active loan-out of a symbol.
type LendingRecord struct {
	InstrumentSymbol           string `json:"instrumentSymbol"`
	LentQuantity               uint64 `json:"lentQuantity"`
	ReferencePriceInMinorUnits int64  `json:"referencePriceInMinorUnits"`
	AccruedFeeInMinorUnits     int64  `json:"accruedFeeInMinorUnits"`
}

// BorrowingRecord is one account's active borrow of a symbol.
type BorrowingRecord struct {
	InstrumentSymbol           string `json:"instrumentSymbol"`
	BorrowedQuantity           uint64 `json:"borrowedQuantity"`
	ReferencePriceInMinorUnits int64  `json:"referencePriceInMinorUnits"`
	AccruedFeeInMinorUnits     int64  `json:"accruedFeeInMinorUnits"`
}

// Desk is the mutex-guarded state machine tracking every account's active
// lending and borrowing records, exactly the same locking discipline as
// internal/marginpledge.PledgeBook.
type Desk struct {
	mutexGuardingDesk sync.Mutex

	lendingRecordsByAccountAndSymbol   map[string]map[string]*LendingRecord
	borrowingRecordsByAccountAndSymbol map[string]map[string]*BorrowingRecord
}

func NewDesk() *Desk {
	return &Desk{
		lendingRecordsByAccountAndSymbol:   make(map[string]map[string]*LendingRecord),
		borrowingRecordsByAccountAndSymbol: make(map[string]map[string]*BorrowingRecord),
	}
}

// LendSecurity lends quantity of instrumentSymbol from
// lendingAccountIdentifier's holding — currentNetHoldingQuantity is the
// account's real position (from internal/positions), server-looked-up by
// the caller, never client-asserted, mirroring PledgeHolding. Returns the
// account's updated LendingRecord.
func (desk *Desk) LendSecurity(
	lendingAccountIdentifier string,
	instrumentSymbol string,
	quantity uint64,
	referencePriceInMinorUnits int64,
	currentNetHoldingQuantity int64,
) (LendingRecord, error) {
	if quantity == 0 {
		return LendingRecord{}, ErrQuantityMustBePositive
	}

	desk.mutexGuardingDesk.Lock()
	defer desk.mutexGuardingDesk.Unlock()

	existing := desk.lendingRecordsByAccountAndSymbol[lendingAccountIdentifier][instrumentSymbol]
	var alreadyLentQuantity uint64
	if existing != nil {
		alreadyLentQuantity = existing.LentQuantity
	}

	availableToLend := currentNetHoldingQuantity - int64(alreadyLentQuantity)
	if availableToLend < 0 || quantity > uint64(availableToLend) {
		return LendingRecord{}, ErrInsufficientUnlentHoldingQuantity
	}

	if desk.lendingRecordsByAccountAndSymbol[lendingAccountIdentifier] == nil {
		desk.lendingRecordsByAccountAndSymbol[lendingAccountIdentifier] = make(map[string]*LendingRecord)
	}
	if existing == nil {
		existing = &LendingRecord{InstrumentSymbol: instrumentSymbol, ReferencePriceInMinorUnits: referencePriceInMinorUnits}
		desk.lendingRecordsByAccountAndSymbol[lendingAccountIdentifier][instrumentSymbol] = existing
	}
	existing.LentQuantity += quantity
	existing.ReferencePriceInMinorUnits = referencePriceInMinorUnits

	return *existing, nil
}

// RecallLending reverses (partially or fully) a lending position,
// returning the requested quantity to the lender's freely-sellable
// holding — no fee is retroactively removed (a real lending fee, once
// earned for the period it was on loan, is not clawed back).
func (desk *Desk) RecallLending(lendingAccountIdentifier string, instrumentSymbol string, quantity uint64) (LendingRecord, error) {
	if quantity == 0 {
		return LendingRecord{}, ErrQuantityMustBePositive
	}

	desk.mutexGuardingDesk.Lock()
	defer desk.mutexGuardingDesk.Unlock()

	record := desk.lendingRecordsByAccountAndSymbol[lendingAccountIdentifier][instrumentSymbol]
	if record == nil {
		return LendingRecord{}, ErrNoLendingRecordFound
	}
	if quantity > record.LentQuantity {
		return LendingRecord{}, ErrRecallQuantityExceedsLentQuantity
	}

	record.LentQuantity -= quantity
	result := *record
	if record.LentQuantity == 0 {
		delete(desk.lendingRecordsByAccountAndSymbol[lendingAccountIdentifier], instrumentSymbol)
	}
	return result, nil
}

// BorrowSecurity records a borrow of quantity of instrumentSymbol for
// borrowingAccountIdentifier. Unlike LendSecurity, this does NOT require
// the borrower to already hold anything — that's the entire point of
// borrowing (e.g. to cover a short sale).
func (desk *Desk) BorrowSecurity(
	borrowingAccountIdentifier string,
	instrumentSymbol string,
	quantity uint64,
	referencePriceInMinorUnits int64,
) (BorrowingRecord, error) {
	if quantity == 0 {
		return BorrowingRecord{}, ErrQuantityMustBePositive
	}

	desk.mutexGuardingDesk.Lock()
	defer desk.mutexGuardingDesk.Unlock()

	if desk.borrowingRecordsByAccountAndSymbol[borrowingAccountIdentifier] == nil {
		desk.borrowingRecordsByAccountAndSymbol[borrowingAccountIdentifier] = make(map[string]*BorrowingRecord)
	}
	existing := desk.borrowingRecordsByAccountAndSymbol[borrowingAccountIdentifier][instrumentSymbol]
	if existing == nil {
		existing = &BorrowingRecord{InstrumentSymbol: instrumentSymbol, ReferencePriceInMinorUnits: referencePriceInMinorUnits}
		desk.borrowingRecordsByAccountAndSymbol[borrowingAccountIdentifier][instrumentSymbol] = existing
	}
	existing.BorrowedQuantity += quantity
	existing.ReferencePriceInMinorUnits = referencePriceInMinorUnits

	return *existing, nil
}

// ReturnBorrowing reverses (partially or fully) a borrow.
func (desk *Desk) ReturnBorrowing(borrowingAccountIdentifier string, instrumentSymbol string, quantity uint64) (BorrowingRecord, error) {
	if quantity == 0 {
		return BorrowingRecord{}, ErrQuantityMustBePositive
	}

	desk.mutexGuardingDesk.Lock()
	defer desk.mutexGuardingDesk.Unlock()

	record := desk.borrowingRecordsByAccountAndSymbol[borrowingAccountIdentifier][instrumentSymbol]
	if record == nil {
		return BorrowingRecord{}, ErrNoBorrowingRecordFound
	}
	if quantity > record.BorrowedQuantity {
		return BorrowingRecord{}, ErrReturnQuantityExceedsBorrowedQuantity
	}

	record.BorrowedQuantity -= quantity
	result := *record
	if record.BorrowedQuantity == 0 {
		delete(desk.borrowingRecordsByAccountAndSymbol[borrowingAccountIdentifier], instrumentSymbol)
	}
	return result, nil
}

// LentQuantity returns how much of instrumentSymbol accountIdentifier
// currently has out on loan (0 if none) — a real build would wire this
// into an order-submission SELL gate exactly like
// internal/marginpledge.PledgedQuantity does, so a lent-out holding can't
// also be sold; not wired into cmd/server/main.go's order path in this
// build (documented gap below).
func (desk *Desk) LentQuantity(accountIdentifier string, instrumentSymbol string) uint64 {
	desk.mutexGuardingDesk.Lock()
	defer desk.mutexGuardingDesk.Unlock()

	record := desk.lendingRecordsByAccountAndSymbol[accountIdentifier][instrumentSymbol]
	if record == nil {
		return 0
	}
	return record.LentQuantity
}

// LendingRecordsForAccount returns a copy of every active lending record
// for one account, keyed by instrument symbol.
func (desk *Desk) LendingRecordsForAccount(accountIdentifier string) map[string]LendingRecord {
	desk.mutexGuardingDesk.Lock()
	defer desk.mutexGuardingDesk.Unlock()

	recordsCopy := make(map[string]LendingRecord)
	for symbol, record := range desk.lendingRecordsByAccountAndSymbol[accountIdentifier] {
		recordsCopy[symbol] = *record
	}
	return recordsCopy
}

// BorrowingRecordsForAccount returns a copy of every active borrowing
// record for one account, keyed by instrument symbol.
func (desk *Desk) BorrowingRecordsForAccount(accountIdentifier string) map[string]BorrowingRecord {
	desk.mutexGuardingDesk.Lock()
	defer desk.mutexGuardingDesk.Unlock()

	recordsCopy := make(map[string]BorrowingRecord)
	for symbol, record := range desk.borrowingRecordsByAccountAndSymbol[accountIdentifier] {
		recordsCopy[symbol] = *record
	}
	return recordsCopy
}

// CalculateIllustrativeAccruedFee computes a real, hand-checkable
// day-count fee accrual — see the package doc's loud warning: the RATE is
// illustrative, the day-count ARITHMETIC is real (annualRatePercent *
// notional * daysHeld / 365, mirroring
// internal/marginfunding.CalculateIllustrativeAccruedInterest's own
// pattern and its own "real formula, illustrative rate" framing).
func CalculateIllustrativeAccruedFee(notionalInMinorUnits int64, annualRatePercent float64, daysHeld int) int64 {
	if notionalInMinorUnits <= 0 || daysHeld <= 0 {
		return 0
	}
	fee := float64(notionalInMinorUnits) * annualRatePercent * float64(daysHeld) / daysPerYearForFeeAccrual
	return roundToNearestMinorUnit(fee)
}

// IllustrativeAnnualizedLendingFeePercent / BorrowingFeePercent expose
// the package's illustrative rate constants for callers (e.g.
// cmd/server/main.go) that want to compute a fee quote without
// duplicating the rate.
func IllustrativeAnnualizedLendingFeePercent() float64 {
	return illustrativeAnnualizedLendingFeePercent
}
func IllustrativeAnnualizedBorrowingFeePercent() float64 {
	return illustrativeAnnualizedBorrowingFeePercent
}

func roundToNearestMinorUnit(amount float64) int64 {
	if amount < 0 {
		return int64(amount - 0.5)
	}
	return int64(amount + 0.5)
}
