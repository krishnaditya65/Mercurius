// Package marktomarket implements FEATURES.md §12's "Real-time
// Mark-to-Market engine across leveraged positions": given a stream of
// fills (to build a real, weighted-average cost basis per account and
// instrument, mirroring internal/positions' net-quantity tracking but
// additionally remembering the price paid) and a stream of current market
// prices, compute genuine unrealized P&L per position and per account.
//
// DESIGN CHOICE — how current prices arrive: this package accepts prices
// via a real HTTP PUSH endpoint (`POST /mark-to-market/price` in
// cmd/server/main.go), not a pull from quant-engine or market-data.
// Rationale: oms-gateway has no live price feed anywhere yet (the exact
// same documented gap the pre-trade risk check's market-order TODO and
// internal/marginpledge's caller-supplied-reference-price gap already
// call out) — quant-engine computes option theoretical prices/Greeks from
// a caller-supplied spot price, it does not itself observe or publish a
// live underlying price either. A push endpoint is the smallest genuinely
// real integration point until a real market-data service with a
// subscribable feed exists in this repo; the moment one does, wiring it
// in only requires a goroutine that calls SetMarketPrice on every tick —
// nothing about this package's internal model needs to change.
//
// DESIGN CHOICE — "leveraged" positions only: FEATURES.md §12 asks this
// specifically for LEVERAGED positions (margin-funded or pledge-backed
// exposure). This package deliberately stays decoupled from
// internal/marginfunding and internal/marginpledge (the same decoupling
// pattern marginfunding itself uses toward marginpledge) — it computes
// MTM for every account/instrument it has cost-basis data for, and it is
// cmd/server/main.go's HTTP handler that decides which accounts count as
// "leveraged" (outstanding margin-funding principal > 0, or any pledged
// quantity > 0) before exposing the result, exactly the same
// responsibility split marginfunding's own package doc describes for its
// relationship with marginpledge.
//
// Real, tested math: weighted-average cost basis on same-direction
// additions, cost basis held constant through a partial close, cost basis
// RESET to the fill price when a position flips from long to short (or
// vice versa) — the position's economic identity genuinely changes at
// that instant, so continuing to average against the old side's cost
// basis would be wrong. Unrealized P&L uses the single signed formula
// signedQuantity * (marketPrice - averageEntryPrice), which is correct
// for both long (positive signedQuantity) and short (negative
// signedQuantity) positions — see the package's test file for a
// hand-worked short-position case.
//
// In-memory only, same as every other package in this service.
package marktomarket

import (
	"errors"
	"sync"
)

// ErrMarketPriceMustBePositive is returned by SetMarketPrice for a
// zero/negative price.
var ErrMarketPriceMustBePositive = errors.New("market price must be greater than zero")

// costBasisEntry is one account/instrument's current net position and its
// weighted-average entry price.
type costBasisEntry struct {
	netQuantity                   int64
	averageEntryPriceInMinorUnits int64
}

// PositionMTM is one account's mark-to-market snapshot for a single
// instrument.
type PositionMTM struct {
	InstrumentSymbol               string `json:"instrumentSymbol"`
	NetQuantity                    int64  `json:"netQuantity"`
	AverageEntryPriceInMinorUnits  int64  `json:"averageEntryPriceInMinorUnits"`
	CurrentMarketPriceInMinorUnits int64  `json:"currentMarketPriceInMinorUnits"`
	UnrealizedPnLInMinorUnits      int64  `json:"unrealizedPnLInMinorUnits"`
}

// MarkToMarketEngine is the mutex-guarded state machine tracking real
// cost basis per (account, instrument) and the latest pushed market price
// per instrument.
type MarkToMarketEngine struct {
	mutexGuardingState sync.RWMutex

	costBasisByAccountAndInstrument map[string]map[string]costBasisEntry
	marketPriceByInstrument         map[string]int64
}

func NewMarkToMarketEngine() *MarkToMarketEngine {
	return &MarkToMarketEngine{
		costBasisByAccountAndInstrument: make(map[string]map[string]costBasisEntry),
		marketPriceByInstrument:         make(map[string]int64),
	}
}

// ApplyFill folds one executed trade into both sides' cost basis. Mirrors
// internal/positions.PositionBook.ApplyFill's signature exactly (same
// buyer/seller/instrument/quantity shape) plus the executed price this
// package additionally needs to track cost basis — callers pass the same
// matching-engine trade execution event to both.
func (engine *MarkToMarketEngine) ApplyFill(
	buyingClientAccountId string,
	sellingClientAccountId string,
	instrumentSymbol string,
	executedQuantity uint64,
	executedPriceInMinorUnits int64,
) {
	engine.mutexGuardingState.Lock()
	defer engine.mutexGuardingState.Unlock()

	engine.applyFillLocked(buyingClientAccountId, instrumentSymbol, int64(executedQuantity), executedPriceInMinorUnits)
	engine.applyFillLocked(sellingClientAccountId, instrumentSymbol, -int64(executedQuantity), executedPriceInMinorUnits)
}

func (engine *MarkToMarketEngine) applyFillLocked(
	accountIdentifier string,
	instrumentSymbol string,
	signedQuantityDelta int64,
	fillPriceInMinorUnits int64,
) {
	if engine.costBasisByAccountAndInstrument[accountIdentifier] == nil {
		engine.costBasisByAccountAndInstrument[accountIdentifier] = make(map[string]costBasisEntry)
	}
	existing := engine.costBasisByAccountAndInstrument[accountIdentifier][instrumentSymbol]

	newQuantity := existing.netQuantity + signedQuantityDelta
	newAveragePrice := existing.averageEntryPriceInMinorUnits

	switch {
	case existing.netQuantity == 0:
		// Starting fresh (or re-starting after a prior full close): the
		// average entry price is simply this fill's price.
		newAveragePrice = fillPriceInMinorUnits

	case sameSign(existing.netQuantity, signedQuantityDelta):
		// Adding to an existing position in the same direction: real
		// weighted-average cost basis across the old and new quantity.
		existingAbsQuantity := absInt64(existing.netQuantity)
		deltaAbsQuantity := absInt64(signedQuantityDelta)
		totalAbsQuantity := existingAbsQuantity + deltaAbsQuantity
		weightedSum := existing.averageEntryPriceInMinorUnits*existingAbsQuantity + fillPriceInMinorUnits*deltaAbsQuantity
		newAveragePrice = weightedSum / totalAbsQuantity

	case newQuantity == 0:
		// Fully closed: no position left, so no meaningful average price
		// until the next fill starts a fresh one.
		newAveragePrice = 0

	case sameSign(newQuantity, existing.netQuantity):
		// Partial close (reducing but not flipping direction): the
		// remaining quantity's cost basis is unchanged — you don't
		// re-average a partial sell into your average buy price.
		newAveragePrice = existing.averageEntryPriceInMinorUnits

	default:
		// Reversed direction (e.g. long flips to short): the position's
		// economic identity restarts at this fill's price for the new,
		// opposite-signed remainder.
		newAveragePrice = fillPriceInMinorUnits
	}

	engine.costBasisByAccountAndInstrument[accountIdentifier][instrumentSymbol] = costBasisEntry{
		netQuantity:                   newQuantity,
		averageEntryPriceInMinorUnits: newAveragePrice,
	}
}

// SetMarketPrice records the latest known market price for an instrument.
// See the package doc's "push endpoint" design-choice note.
func (engine *MarkToMarketEngine) SetMarketPrice(instrumentSymbol string, priceInMinorUnits int64) error {
	if priceInMinorUnits <= 0 {
		return ErrMarketPriceMustBePositive
	}

	engine.mutexGuardingState.Lock()
	defer engine.mutexGuardingState.Unlock()

	engine.marketPriceByInstrument[instrumentSymbol] = priceInMinorUnits
	return nil
}

// MarketPrice returns the latest pushed price for an instrument, and
// whether one has ever been pushed.
func (engine *MarkToMarketEngine) MarketPrice(instrumentSymbol string) (int64, bool) {
	engine.mutexGuardingState.RLock()
	defer engine.mutexGuardingState.RUnlock()

	price, known := engine.marketPriceByInstrument[instrumentSymbol]
	return price, known
}

// PositionsMTMForAccount returns a real mark-to-market snapshot for every
// instrument the account holds a non-zero position in AND for which a
// market price has been pushed. An instrument with a non-zero position
// but no known market price yet is deliberately omitted rather than
// reported with a fabricated zero price — callers can tell the
// difference by comparing against internal/positions' full position list
// if they need to surface "MTM unavailable" explicitly.
func (engine *MarkToMarketEngine) PositionsMTMForAccount(accountIdentifier string) []PositionMTM {
	engine.mutexGuardingState.RLock()
	defer engine.mutexGuardingState.RUnlock()

	var snapshots []PositionMTM
	for instrumentSymbol, entry := range engine.costBasisByAccountAndInstrument[accountIdentifier] {
		if entry.netQuantity == 0 {
			continue
		}
		marketPrice, priceKnown := engine.marketPriceByInstrument[instrumentSymbol]
		if !priceKnown {
			continue
		}
		unrealizedPnL := entry.netQuantity * (marketPrice - entry.averageEntryPriceInMinorUnits)
		snapshots = append(snapshots, PositionMTM{
			InstrumentSymbol:               instrumentSymbol,
			NetQuantity:                    entry.netQuantity,
			AverageEntryPriceInMinorUnits:  entry.averageEntryPriceInMinorUnits,
			CurrentMarketPriceInMinorUnits: marketPrice,
			UnrealizedPnLInMinorUnits:      unrealizedPnL,
		})
	}
	return snapshots
}

// AccountLevelUnrealizedPnL sums UnrealizedPnLInMinorUnits across every
// position PositionsMTMForAccount would return for this account.
func (engine *MarkToMarketEngine) AccountLevelUnrealizedPnL(accountIdentifier string) (int64, []PositionMTM) {
	snapshots := engine.PositionsMTMForAccount(accountIdentifier)
	var total int64
	for _, snapshot := range snapshots {
		total += snapshot.UnrealizedPnLInMinorUnits
	}
	return total, snapshots
}

func sameSign(a int64, b int64) bool {
	return (a > 0 && b > 0) || (a < 0 && b < 0)
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
