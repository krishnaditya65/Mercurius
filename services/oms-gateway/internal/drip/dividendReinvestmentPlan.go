// Package drip implements FEATURES.md §17's "Dividend Reinvestment Plans
// (DRIP), auto-compounding toggle": a real dividend-event cash credit
// proportional to an account's held quantity, plus a real, mutex-guarded
// per-account auto-reinvestment toggle. When a dividend event is
// processed for an account with the toggle ON, cmd/server/main.go
// re-invests the credited cash via the EXACT SAME order-submission path
// (processOrderSubmission) every other order path in this codebase
// reuses — a real BUY order, not a simulated one.
//
// TWO REAL, INDEPENDENT COMPUTATIONS LIVE HERE:
//
//  1. CalculateDividendCredit: quantityHeld * dividendPerShareInMinorUnits
//     — exact integer arithmetic, no rounding involved at all.
//
//  2. CalculateReinvestmentQuantity: given the credited cash and a
//     reference price, how many WHOLE shares that cash can buy, plus the
//     LEFTOVER cash that's too small to buy one more share. Whole-share
//     only — see the loud gap note below on why this doesn't (yet) use
//     fractional-share precision.
//
// LOUD, REPEATED KNOWN GAP: ReferencePriceInMinorUnits (needed to convert
// a cash credit into a reinvestment quantity) has no live price feed to
// come from — the same "no live last-traded-price feed" gap
// internal/marginpledge, internal/marktomarket, internal/basketorders,
// and the pre-trade risk check's market-order TODO already document
// repeatedly. CalculateReinvestmentQuantity is also deliberately
// WHOLE-SHARE only (integer division, leftover cash reported but NOT
// automatically carried forward to the next dividend event) — a real DRIP
// program often supports fractional-share reinvestment; this package does
// NOT integrate with FEATURES.md §17's separate fractional-share item
// (see internal/orders' milli-share precision note, if/when built) — an
// honest scope boundary, not an oversight. In-memory only, same as every
// other package in this service.
package drip

import (
	"errors"
	"sync"
)

var (
	// ErrQuantityHeldMustBePositive is returned when
	// CalculateDividendCredit is given a zero holding — nothing to credit
	// a dividend against.
	ErrQuantityHeldMustBePositive = errors.New("quantityHeld must be greater than zero")

	// ErrDividendPerShareMustBePositive is returned when the per-share
	// dividend amount is zero or negative.
	ErrDividendPerShareMustBePositive = errors.New("dividendPerShareInMinorUnits must be greater than zero")

	// ErrCashAmountMustBePositive is returned when
	// CalculateReinvestmentQuantity is given a non-positive cash amount.
	ErrCashAmountMustBePositive = errors.New("cashAmountInMinorUnits must be greater than zero")

	// ErrReferencePriceMustBePositive is returned when the reinvestment
	// reference price is zero or negative.
	ErrReferencePriceMustBePositive = errors.New("referencePriceInMinorUnits must be greater than zero")
)

// CalculateDividendCredit computes the exact cash amount a dividend event
// credits an account holding quantityHeld shares at
// dividendPerShareInMinorUnits per share.
func CalculateDividendCredit(quantityHeld uint64, dividendPerShareInMinorUnits int64) (int64, error) {
	if quantityHeld == 0 {
		return 0, ErrQuantityHeldMustBePositive
	}
	if dividendPerShareInMinorUnits <= 0 {
		return 0, ErrDividendPerShareMustBePositive
	}
	return int64(quantityHeld) * dividendPerShareInMinorUnits, nil
}

// ReinvestmentPlan is CalculateReinvestmentQuantity's real result: how
// many whole shares the credited cash can buy, plus the leftover cash too
// small to buy one more share (see the package doc's whole-share-only
// gap note).
type ReinvestmentPlan struct {
	ReinvestmentQuantity     uint64 `json:"reinvestmentQuantity"`
	SpentCashInMinorUnits    int64  `json:"spentCashInMinorUnits"`
	LeftoverCashInMinorUnits int64  `json:"leftoverCashInMinorUnits"`
}

// CalculateReinvestmentQuantity computes how many whole shares
// cashAmountInMinorUnits can buy at referencePriceInMinorUnits per share,
// and the leftover cash. Real integer-division arithmetic, exactly
// hand-checkable: reinvestmentQuantity = cash / price (floor),
// spentCash = reinvestmentQuantity * price, leftoverCash = cash - spentCash.
func CalculateReinvestmentQuantity(cashAmountInMinorUnits int64, referencePriceInMinorUnits int64) (ReinvestmentPlan, error) {
	if cashAmountInMinorUnits <= 0 {
		return ReinvestmentPlan{}, ErrCashAmountMustBePositive
	}
	if referencePriceInMinorUnits <= 0 {
		return ReinvestmentPlan{}, ErrReferencePriceMustBePositive
	}
	quantity := uint64(cashAmountInMinorUnits / referencePriceInMinorUnits)
	spent := int64(quantity) * referencePriceInMinorUnits
	return ReinvestmentPlan{
		ReinvestmentQuantity:     quantity,
		SpentCashInMinorUnits:    spent,
		LeftoverCashInMinorUnits: cashAmountInMinorUnits - spent,
	}, nil
}

// ToggleRegistry is a real, mutex-guarded per-account auto-reinvestment
// toggle — the "auto-compounding toggle" FEATURES.md §17 asks for.
// Default (an account never explicitly toggled) is OFF: a dividend credit
// is real cash paid out, not automatically reinvested, unless the account
// opted in — the same "unconfigured = unaffected/conservative default"
// convention internal/algolimits and internal/exposurelimits already use.
type ToggleRegistry struct {
	mutexGuardingToggles  sync.Mutex
	autoReinvestByAccount map[string]bool
}

func NewToggleRegistry() *ToggleRegistry {
	return &ToggleRegistry{autoReinvestByAccount: make(map[string]bool)}
}

// SetAutoReinvest sets accountIdentifier's auto-reinvestment toggle.
func (registry *ToggleRegistry) SetAutoReinvest(accountIdentifier string, enabled bool) {
	registry.mutexGuardingToggles.Lock()
	defer registry.mutexGuardingToggles.Unlock()
	registry.autoReinvestByAccount[accountIdentifier] = enabled
}

// IsAutoReinvestEnabled reports accountIdentifier's current toggle state
// — false (OFF) for an account that was never explicitly toggled.
func (registry *ToggleRegistry) IsAutoReinvestEnabled(accountIdentifier string) bool {
	registry.mutexGuardingToggles.Lock()
	defer registry.mutexGuardingToggles.Unlock()
	return registry.autoReinvestByAccount[accountIdentifier]
}
