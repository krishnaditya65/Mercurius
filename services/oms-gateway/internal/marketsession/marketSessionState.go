// Package marketsession tracks whether the market is currently open —
// FEATURES.md §3's After Market Orders (AMO) need to know this to decide
// whether an order goes straight to matching-engine or waits.
//
// TODO(real build): a real exchange has a clock-driven session calendar
// (pre-open, continuous trading, closing auction, holidays, etc.) per
// ARCHITECTURE.md's broader scope. This skeleton has none of that — it's
// a plain in-memory boolean flipped by an explicit admin call
// (POST /market-session/open, /market-session/close), not by wall-clock
// time. Good enough to prove the AMO queue-and-drain mechanics; not a
// real trading calendar.
package marketsession

import "sync"

// MarketSessionState is a single shared boolean, safe for concurrent
// reads (every order submission checks it) and infrequent writes (an
// admin flips it open/closed).
type MarketSessionState struct {
	mutexGuardingIsOpen sync.RWMutex
	isMarketOpen        bool
}

// NewMarketSessionState starts CLOSED — matches the intuition that a
// freshly started demo environment isn't mid-trading-session by default;
// an operator (or test) opens it explicitly.
func NewMarketSessionState() *MarketSessionState {
	return &MarketSessionState{isMarketOpen: false}
}

func (state *MarketSessionState) IsMarketOpen() bool {
	state.mutexGuardingIsOpen.RLock()
	defer state.mutexGuardingIsOpen.RUnlock()
	return state.isMarketOpen
}

func (state *MarketSessionState) SetMarketOpen(isMarketOpen bool) {
	state.mutexGuardingIsOpen.Lock()
	defer state.mutexGuardingIsOpen.Unlock()
	state.isMarketOpen = isMarketOpen
}
