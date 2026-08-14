// This file extends internal/fractionalshares with a real, mutex-guarded
// milli-share-precision position book — the fractional-share counterpart
// to internal/positions.PositionBook, mirroring that package's exact
// signed-net-quantity design (positive = long, negative = short, zero/
// absent = flat) but tracking milli-share units (see
// MilliShareUnitsPerWholeShare) instead of whole shares.
//
// SCOPE: per this package's own doc comment, only paper-trading fills
// can genuinely be milli-share-precise in this build (matching-engine
// has no fractional field), so this book is fed exclusively by
// cmd/server/main.go's paper-trading branch — never by a real
// matching-engine fill.
package fractionalshares

import "sync"

// MilliSharePositionBook tracks net signed milli-share quantity per
// (account, instrument).
type MilliSharePositionBook struct {
	mutexGuardingPositions                      sync.RWMutex
	netMilliShareQuantityByAccountAndInstrument map[string]map[string]int64
}

func NewMilliSharePositionBook() *MilliSharePositionBook {
	return &MilliSharePositionBook{
		netMilliShareQuantityByAccountAndInstrument: make(map[string]map[string]int64),
	}
}

// ApplyFractionalFill adjusts both sides of one simulated fractional
// fill, mirroring positions.PositionBook.ApplyFill exactly, but in
// milli-share units.
func (book *MilliSharePositionBook) ApplyFractionalFill(
	buyingClientAccountId string,
	sellingClientAccountId string,
	instrumentSymbol string,
	executedMilliShareQuantity uint64,
) {
	book.mutexGuardingPositions.Lock()
	defer book.mutexGuardingPositions.Unlock()

	book.adjustPositionLocked(buyingClientAccountId, instrumentSymbol, int64(executedMilliShareQuantity))
	book.adjustPositionLocked(sellingClientAccountId, instrumentSymbol, -int64(executedMilliShareQuantity))
}

func (book *MilliSharePositionBook) adjustPositionLocked(accountIdentifier string, instrumentSymbol string, signedMilliShareDelta int64) {
	if book.netMilliShareQuantityByAccountAndInstrument[accountIdentifier] == nil {
		book.netMilliShareQuantityByAccountAndInstrument[accountIdentifier] = make(map[string]int64)
	}
	book.netMilliShareQuantityByAccountAndInstrument[accountIdentifier][instrumentSymbol] += signedMilliShareDelta
}

// PositionsForAccount returns a copy of the account's current
// milli-share positions (instrument -> net signed milli-share
// quantity). Instruments with a net-zero position are omitted, same
// convention as positions.PositionBook.PositionsForAccount.
func (book *MilliSharePositionBook) PositionsForAccount(accountIdentifier string) map[string]int64 {
	book.mutexGuardingPositions.RLock()
	defer book.mutexGuardingPositions.RUnlock()

	positionsCopy := make(map[string]int64)
	for instrumentSymbol, netMilliShareQuantity := range book.netMilliShareQuantityByAccountAndInstrument[accountIdentifier] {
		if netMilliShareQuantity != 0 {
			positionsCopy[instrumentSymbol] = netMilliShareQuantity
		}
	}
	return positionsCopy
}
