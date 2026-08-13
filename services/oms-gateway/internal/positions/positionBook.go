// Package positions implements FEATURES.md §3's "positions / holdings
// views": tracking each account's net position per instrument as fills
// happen, so a client can ask "what do I currently hold?" instead of
// replaying every trade themselves.
//
// TODO(real build): this is net quantity only — no average cost basis,
// no realized/unrealized P&L, no corporate-action adjustments. In-memory,
// not persisted; a restart loses every position (the real source of
// truth would be reconstructible by replaying the trade event log, which
// doesn't exist yet either — see ARCHITECTURE.md §3.4 for the WAL/event-
// sourcing design this eventually needs).
package positions

import "sync"

// PositionBook tracks net signed quantity per (account, instrument).
// Positive = long, negative = short, zero/absent = flat.
type PositionBook struct {
	mutexGuardingPositions            sync.RWMutex
	netQuantityByAccountAndInstrument map[string]map[string]int64
}

func NewPositionBook() *PositionBook {
	return &PositionBook{
		netQuantityByAccountAndInstrument: make(map[string]map[string]int64),
	}
}

// ApplyFill adjusts both sides of one trade: the buyer's position
// increases by the executed quantity, the seller's decreases by it —
// mirroring the ledger's debit/credit settlement in ledgerclient, but for
// share/contract quantity instead of cash.
func (positionBook *PositionBook) ApplyFill(
	buyingClientAccountId string,
	sellingClientAccountId string,
	instrumentSymbol string,
	executedQuantity uint64,
) {
	positionBook.mutexGuardingPositions.Lock()
	defer positionBook.mutexGuardingPositions.Unlock()

	positionBook.adjustPositionLocked(buyingClientAccountId, instrumentSymbol, int64(executedQuantity))
	positionBook.adjustPositionLocked(sellingClientAccountId, instrumentSymbol, -int64(executedQuantity))
}

func (positionBook *PositionBook) adjustPositionLocked(accountIdentifier string, instrumentSymbol string, signedQuantityDelta int64) {
	if positionBook.netQuantityByAccountAndInstrument[accountIdentifier] == nil {
		positionBook.netQuantityByAccountAndInstrument[accountIdentifier] = make(map[string]int64)
	}
	positionBook.netQuantityByAccountAndInstrument[accountIdentifier][instrumentSymbol] += signedQuantityDelta
}

// PositionsForAccount returns a copy of the account's current positions
// (instrument -> net signed quantity). Instruments with a net-zero
// position are omitted rather than returned as an explicit zero.
func (positionBook *PositionBook) PositionsForAccount(accountIdentifier string) map[string]int64 {
	positionBook.mutexGuardingPositions.RLock()
	defer positionBook.mutexGuardingPositions.RUnlock()

	positionsCopy := make(map[string]int64)
	for instrumentSymbol, netQuantity := range positionBook.netQuantityByAccountAndInstrument[accountIdentifier] {
		if netQuantity != 0 {
			positionsCopy[instrumentSymbol] = netQuantity
		}
	}
	return positionsCopy
}
