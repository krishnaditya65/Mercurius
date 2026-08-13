// Package marginpledge implements FEATURES.md §3's "Margin pledge
// system (stocks/MF as collateral)": an account can pledge quantity of
// an existing holding (from internal/positions) as collateral, which
// increases the account's available margin (internal/riskengine) by a
// haircut-adjusted value and marks that quantity "pledged" — unavailable
// to sell until it's unpledged again.
//
// LOUD, REPEATED WARNING: the haircut percentages below are an
// ILLUSTRATIVE, made-up model, NOT a real regulatory (SEBI/exchange)
// haircut table. Real haircuts are published per-security by exchanges,
// change periodically, and depend on volatility/liquidity bands this
// package doesn't model at all. Treat every haircut and every resulting
// margin figure here as "illustrative, not authoritative" — exactly the
// same caveat internal/chargescalculator and internal/marginengine carry
// for their own rate tables.
//
// Reference pricing gap: oms-gateway has no live last-traded-price feed
// (see the risk-check TODO in cmd/server/main.go for market orders — the
// same underlying gap). Pledging therefore takes a caller-supplied
// referencePriceInMinorUnits per unit instead of looking one up. A real
// build would price a pledge off a genuine market data feed, not a
// client-asserted number — treat this as one more instance of the same
// documented "no price feed yet" gap, not a new one.
package marginpledge

import (
	"errors"
	"sync"
)

var (
	// ErrPledgeQuantityMustBePositive is returned when a pledge or
	// unpledge is attempted with a zero quantity.
	ErrPledgeQuantityMustBePositive = errors.New("pledge quantity must be greater than zero")

	// ErrReferencePriceMustBePositive is returned when a pledge is
	// attempted with a non-positive reference price — a pledge with a
	// zero or negative price would contribute zero or negative margin,
	// which is never a legitimate collateral value.
	ErrReferencePriceMustBePositive = errors.New("reference price must be greater than zero")

	// ErrInsufficientUnpledgedHoldingQuantity is returned when the
	// requested pledge quantity exceeds what's actually available to
	// pledge: the account's current net holding of that symbol, minus
	// whatever of it is already pledged.
	ErrInsufficientUnpledgedHoldingQuantity = errors.New("insufficient unpledged holding quantity to pledge this amount")

	// ErrNoPledgeFoundForSymbol is returned when an unpledge is attempted
	// against an account/symbol pair with no active pledge record.
	ErrNoPledgeFoundForSymbol = errors.New("no active pledge found for this account and symbol")

	// ErrUnpledgeQuantityExceedsPledgedQuantity is returned when the
	// requested unpledge quantity exceeds the currently pledged quantity
	// for that account/symbol.
	ErrUnpledgeQuantityExceedsPledgedQuantity = errors.New("unpledge quantity exceeds currently pledged quantity")

	// ErrPledgeStillBackingOpenMarginPosition is the real state-machine
	// guard FEATURES.md asked for: an unpledge is refused if removing
	// this pledge's collateral value would drop the account's total
	// pledged collateral below the margin it currently has utilized by
	// open derivative positions (see SetUtilizedMarginInMinorUnits).
	ErrPledgeStillBackingOpenMarginPosition = errors.New("cannot unpledge: remaining pledged collateral would fall below this account's utilized margin for open derivative positions")
)

// defaultHaircutPercent applies to any symbol not explicitly listed in
// illustrativeHaircutPercentBySymbol below — see the package doc's
// warning: this is a made-up flat rate, not a real regulatory value.
const defaultHaircutPercent = 0.20

// illustrativeHaircutPercentBySymbol: a tiny, made-up per-symbol
// override table, purely to demonstrate that a real build would need
// per-security haircuts (large, liquid names typically get a smaller
// haircut than everything else) — NOT sourced from any real exchange
// haircut list.
var illustrativeHaircutPercentBySymbol = map[string]float64{
	"DEMO-EQ": 0.15,
}

func haircutPercentForSymbol(instrumentSymbol string) float64 {
	if percent, hasOverride := illustrativeHaircutPercentBySymbol[instrumentSymbol]; hasOverride {
		return percent
	}
	return defaultHaircutPercent
}

// PledgeRecord is the current state of one account's pledge of one
// symbol. A single record accumulates across multiple PledgeHolding
// calls for the same account/symbol rather than creating separate
// records per call.
type PledgeRecord struct {
	InstrumentSymbol                   string  `json:"instrumentSymbol"`
	PledgedQuantity                    uint64  `json:"pledgedQuantity"`
	ReferencePriceInMinorUnits         int64   `json:"referencePriceInMinorUnits"`
	HaircutPercentApplied              float64 `json:"haircutPercentApplied"`
	MarginValueContributedInMinorUnits int64   `json:"marginValueContributedInMinorUnits"`
}

// PledgeBook is the mutex-guarded state machine tracking every account's
// pledged holdings and, separately, how much margin each account
// currently has utilized by open derivative positions (the real
// invariant that guards unpledging — see ErrPledgeStillBackingOpenMarginPosition).
//
// KNOWN GAP: utilized margin is set directly via
// SetUtilizedMarginInMinorUnits rather than derived automatically from a
// real open-F&O-position book, because oms-gateway doesn't track open
// derivative positions as a structured, stateful book yet (internal/
// positions is net EQUITY quantity only). This is the same kind of
// explicit, honest placeholder as internal/marketsession's admin toggle
// standing in for a real trading calendar: the STATE and the CHECK it
// guards are both real, only the automatic feed populating that state is
// not built yet.
type PledgeBook struct {
	mutexGuardingPledges sync.Mutex

	pledgesByAccountAndSymbol map[string]map[string]*PledgeRecord
	utilizedMarginByAccount   map[string]int64
}

func NewPledgeBook() *PledgeBook {
	return &PledgeBook{
		pledgesByAccountAndSymbol: make(map[string]map[string]*PledgeRecord),
		utilizedMarginByAccount:   make(map[string]int64),
	}
}

// PledgeHolding pledges `quantity` of `instrumentSymbol` as collateral
// for `accountIdentifier`, given the account's current net holding
// quantity of that symbol (as reported by internal/positions — this
// package deliberately does not import positions itself, so the caller
// supplies the figure, the same decoupled pattern riskengine uses for
// order notional).
//
// Returns the account's new PledgeRecord for that symbol and the margin
// VALUE just contributed by this specific call (the caller applies this
// as a positive delta via riskengine.AdjustAvailableMarginInMinorUnits —
// this package never touches riskengine directly, to stay decoupled the
// same way every other internal package here does).
func (pledgeBook *PledgeBook) PledgeHolding(
	accountIdentifier string,
	instrumentSymbol string,
	quantity uint64,
	referencePriceInMinorUnits int64,
	currentNetHoldingQuantity int64,
) (PledgeRecord, int64, error) {
	if quantity == 0 {
		return PledgeRecord{}, 0, ErrPledgeQuantityMustBePositive
	}
	if referencePriceInMinorUnits <= 0 {
		return PledgeRecord{}, 0, ErrReferencePriceMustBePositive
	}

	pledgeBook.mutexGuardingPledges.Lock()
	defer pledgeBook.mutexGuardingPledges.Unlock()

	existingRecord := pledgeBook.pledgesByAccountAndSymbol[accountIdentifier][instrumentSymbol]
	var alreadyPledgedQuantity uint64
	if existingRecord != nil {
		alreadyPledgedQuantity = existingRecord.PledgedQuantity
	}

	availableToPledge := currentNetHoldingQuantity - int64(alreadyPledgedQuantity)
	if availableToPledge < 0 || quantity > uint64(availableToPledge) {
		return PledgeRecord{}, 0, ErrInsufficientUnpledgedHoldingQuantity
	}

	haircutPercent := haircutPercentForSymbol(instrumentSymbol)
	marginValueContributedByThisCall := roundToNearestMinorUnit(
		float64(referencePriceInMinorUnits) * float64(quantity) * (1 - haircutPercent),
	)

	if pledgeBook.pledgesByAccountAndSymbol[accountIdentifier] == nil {
		pledgeBook.pledgesByAccountAndSymbol[accountIdentifier] = make(map[string]*PledgeRecord)
	}

	if existingRecord == nil {
		existingRecord = &PledgeRecord{
			InstrumentSymbol:           instrumentSymbol,
			ReferencePriceInMinorUnits: referencePriceInMinorUnits,
			HaircutPercentApplied:      haircutPercent,
		}
		pledgeBook.pledgesByAccountAndSymbol[accountIdentifier][instrumentSymbol] = existingRecord
	}
	existingRecord.PledgedQuantity += quantity
	existingRecord.ReferencePriceInMinorUnits = referencePriceInMinorUnits // last-supplied price wins, illustrative only
	existingRecord.MarginValueContributedInMinorUnits += marginValueContributedByThisCall

	return *existingRecord, marginValueContributedByThisCall, nil
}

// UnpledgeHolding releases `quantity` of a previously pledged holding,
// provided doing so wouldn't drop the account's remaining pledged
// collateral below its currently utilized margin (see
// ErrPledgeStillBackingOpenMarginPosition — the real, not-fake state
// check FEATURES.md asked for).
//
// Returns the margin value to REMOVE from the account's available margin
// (the caller applies this as a negative delta via
// riskengine.AdjustAvailableMarginInMinorUnits).
func (pledgeBook *PledgeBook) UnpledgeHolding(
	accountIdentifier string,
	instrumentSymbol string,
	quantity uint64,
) (int64, error) {
	if quantity == 0 {
		return 0, ErrPledgeQuantityMustBePositive
	}

	pledgeBook.mutexGuardingPledges.Lock()
	defer pledgeBook.mutexGuardingPledges.Unlock()

	record := pledgeBook.pledgesByAccountAndSymbol[accountIdentifier][instrumentSymbol]
	if record == nil {
		return 0, ErrNoPledgeFoundForSymbol
	}
	if quantity > record.PledgedQuantity {
		return 0, ErrUnpledgeQuantityExceedsPledgedQuantity
	}

	// Proportional share of this record's contributed margin value being
	// released — integer division is fine here: this is an illustrative
	// figure, not a regulated cash movement, and the remainder stays
	// attributed to the record's remaining pledged quantity.
	marginValueToRelease := int64(quantity) * record.MarginValueContributedInMinorUnits / int64(record.PledgedQuantity)

	totalPledgedMarginValueForAccount := pledgeBook.totalPledgedMarginValueForAccountLocked(accountIdentifier)
	remainingMarginValueAfterUnpledge := totalPledgedMarginValueForAccount - marginValueToRelease
	utilizedMargin := pledgeBook.utilizedMarginByAccount[accountIdentifier]

	if remainingMarginValueAfterUnpledge < utilizedMargin {
		return 0, ErrPledgeStillBackingOpenMarginPosition
	}

	record.PledgedQuantity -= quantity
	record.MarginValueContributedInMinorUnits -= marginValueToRelease
	if record.PledgedQuantity == 0 {
		delete(pledgeBook.pledgesByAccountAndSymbol[accountIdentifier], instrumentSymbol)
	}

	return marginValueToRelease, nil
}

// SetUtilizedMarginInMinorUnits records how much margin an account
// currently has utilized by open derivative positions — see the
// PledgeBook doc comment's "KNOWN GAP" note: this is set explicitly
// rather than derived automatically, since oms-gateway has no structured
// open-F&O-position book yet. Once set, UnpledgeHolding genuinely
// enforces that pledged collateral can never drop below this figure.
func (pledgeBook *PledgeBook) SetUtilizedMarginInMinorUnits(accountIdentifier string, utilizedMarginInMinorUnits int64) {
	pledgeBook.mutexGuardingPledges.Lock()
	defer pledgeBook.mutexGuardingPledges.Unlock()

	pledgeBook.utilizedMarginByAccount[accountIdentifier] = utilizedMarginInMinorUnits
}

// PledgedQuantity returns how much of instrumentSymbol accountIdentifier
// currently has pledged (0 if none) — used by the order-submission path
// to enforce that pledged shares aren't sellable (see the SELL-order
// check in cmd/server/main.go).
func (pledgeBook *PledgeBook) PledgedQuantity(accountIdentifier string, instrumentSymbol string) uint64 {
	pledgeBook.mutexGuardingPledges.Lock()
	defer pledgeBook.mutexGuardingPledges.Unlock()

	record := pledgeBook.pledgesByAccountAndSymbol[accountIdentifier][instrumentSymbol]
	if record == nil {
		return 0
	}
	return record.PledgedQuantity
}

// PledgesForAccount returns a copy of every active pledge record for one
// account, keyed by instrument symbol.
func (pledgeBook *PledgeBook) PledgesForAccount(accountIdentifier string) map[string]PledgeRecord {
	pledgeBook.mutexGuardingPledges.Lock()
	defer pledgeBook.mutexGuardingPledges.Unlock()

	pledgesCopy := make(map[string]PledgeRecord)
	for instrumentSymbol, record := range pledgeBook.pledgesByAccountAndSymbol[accountIdentifier] {
		pledgesCopy[instrumentSymbol] = *record
	}
	return pledgesCopy
}

// TotalPledgedMarginValueForAccount returns the account's current total
// pledged collateral value (the sum of MarginValueContributedInMinorUnits
// across every active pledge record) — used by internal/marginfunding to
// cap a cash-advance request at what's actually backed by pledged
// collateral (see that package's RequestFunding).
func (pledgeBook *PledgeBook) TotalPledgedMarginValueForAccount(accountIdentifier string) int64 {
	pledgeBook.mutexGuardingPledges.Lock()
	defer pledgeBook.mutexGuardingPledges.Unlock()

	return pledgeBook.totalPledgedMarginValueForAccountLocked(accountIdentifier)
}

func (pledgeBook *PledgeBook) totalPledgedMarginValueForAccountLocked(accountIdentifier string) int64 {
	var total int64
	for _, record := range pledgeBook.pledgesByAccountAndSymbol[accountIdentifier] {
		total += record.MarginValueContributedInMinorUnits
	}
	return total
}

// roundToNearestMinorUnit mirrors chargescalculator's / marginengine's
// helper of the same name — rounds a fractional minor-unit amount to the
// nearest whole minor unit, half-away-from-zero.
func roundToNearestMinorUnit(amount float64) int64 {
	if amount < 0 {
		return int64(amount - 0.5)
	}
	return int64(amount + 0.5)
}
