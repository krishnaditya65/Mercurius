// This file extends internal/marketsession with FEATURES.md §15's
// "Pre-market / post-market / extended-hours session support with
// distinct matching rules": a real SessionPhase (in addition to the
// pre-existing plain isMarketOpen boolean, left completely untouched —
// see below) with genuinely DIFFERENT order-acceptance rules than
// regular trading hours, enforced by ValidateOrderAgainstSessionPhase.
//
// WHY A SEPARATE FIELD INSTEAD OF REPLACING isMarketOpen: isMarketOpen
// already drives FEATURES.md §3's AMO queue-and-drain mechanics (see
// IsMarketOpen/SetMarketOpen above and cmd/server/main.go's AMO branch)
// and has existing tests. SessionPhase is ADDITIVE, independently
// settable state — an operator wanting realistic pre-market/regular/
// post-market behavior sets BOTH (SetSessionPhase for the new
// order-acceptance rules below, SetMarketOpen for the pre-existing AMO
// gate) — documented explicitly here and in the README rather than
// silently coupling two independent booleans together in a way that
// could surprise an existing caller of SetMarketOpen alone.
//
// REAL, ENFORCED RULES (not just labels): PRE_MARKET and POST_MARKET
// both only accept plain LIMIT orders — no MARKET, no SL/SL-M, no
// ICEBERG/FOK/IOC — mirroring a real exchange's pre-open call-auction
// session and after-hours closing session, both of which are
// order-collection-and-single-match windows, not continuous markets where
// a market order or a stop-trigger genuinely makes sense.
//
// REGULAR and CLOSED are BOTH treated as "no additional restriction from
// THIS gate" — deliberately, not an oversight. REGULAR is today's status
// quo (everything allowed). CLOSED's real order-rejection behavior is
// ALREADY fully owned by the pre-existing isMarketOpen/AMO-queueing
// mechanism in cmd/server/main.go — this new gate staying silent for
// CLOSED avoids a second, competing "is the market closed" check that
// could disagree with the first one (isMarketOpen defaults to false and
// sessionPhase defaults to CLOSED independently — see the "WHY A SEPARATE
// FIELD" note above), which would be a real regression, not an
// improvement. In other words: this file's contribution is PURELY the
// PRE_MARKET/POST_MARKET rules; closed-market handling remains exactly
// where it already was.
package marketsession

import "errors"

// SessionPhase names one of the four real trading-session phases this
// package now distinguishes.
type SessionPhase string

const (
	SessionPhaseClosed     SessionPhase = "CLOSED"
	SessionPhasePreMarket  SessionPhase = "PRE_MARKET"
	SessionPhaseRegular    SessionPhase = "REGULAR"
	SessionPhasePostMarket SessionPhase = "POST_MARKET"
)

var (
	// ErrOnlyLimitOrdersInPreMarket is returned for a non-plain-LIMIT
	// order submitted during PRE_MARKET.
	ErrOnlyLimitOrdersInPreMarket = errors.New("only plain LIMIT orders are accepted during the pre-market session — no MARKET, SL/SL-M, or ICEBERG/FOK/IOC")

	// ErrOnlyLimitOrdersInPostMarket is returned for a non-plain-LIMIT
	// order submitted during POST_MARKET.
	ErrOnlyLimitOrdersInPostMarket = errors.New("only plain LIMIT orders are accepted during the post-market session — no MARKET, SL/SL-M, or ICEBERG/FOK/IOC")

	// ErrUnknownSessionPhase is returned by SetSessionPhase for a value
	// other than the four constants above.
	ErrUnknownSessionPhase = errors.New("unknown session phase")
)

// OrderShapeForSessionRules is the minimal set of an order submission's
// fields this package's rules need to see — a small, decoupled shape
// (this package deliberately does not import internal/orders, the same
// decoupling internal/riskengine and internal/marginpledge already
// practice for their own inputs) rather than depending on the full
// OrderSubmissionRequest type.
type OrderShapeForSessionRules struct {
	OrderIsMarketOrderNotLimit bool
	OrderIsStopLossVariant     bool
	OrderExecutionType         string // "" / "MARKET" / "LIMIT" / "SL" / "SL-M" / "ICEBERG" / "FOK" / "IOC"
}

func isPlainLimitOrder(order OrderShapeForSessionRules) bool {
	if order.OrderIsMarketOrderNotLimit || order.OrderIsStopLossVariant {
		return false
	}
	switch order.OrderExecutionType {
	case "", "LIMIT":
		return true
	default:
		return false
	}
}

// ValidateOrderAgainstSessionPhase enforces the real, distinct rules
// described in this file's doc comment for the given phase. Returns nil
// for REGULAR (unrestricted, today's status quo) and for a CLOSED-phase
// order that's flagged OrderIsAfterMarketOrder (handled by the pre-
// existing AMO queue, not this function).
func ValidateOrderAgainstSessionPhase(phase SessionPhase, order OrderShapeForSessionRules, isAfterMarketOrder bool) error {
	switch phase {
	case SessionPhaseRegular, SessionPhaseClosed:
		// CLOSED's real rejection behavior belongs to the pre-existing
		// isMarketOpen/AMO mechanism — see this file's package doc.
		return nil
	case SessionPhasePreMarket:
		if isPlainLimitOrder(order) {
			return nil
		}
		return ErrOnlyLimitOrdersInPreMarket
	case SessionPhasePostMarket:
		if isPlainLimitOrder(order) {
			return nil
		}
		return ErrOnlyLimitOrdersInPostMarket
	default:
		return ErrUnknownSessionPhase
	}
}

// SetSessionPhase sets the current session phase — validated against the
// four known constants so a typo/garbage value can never silently become
// the active phase.
func (state *MarketSessionState) SetSessionPhase(phase SessionPhase) error {
	switch phase {
	case SessionPhaseClosed, SessionPhasePreMarket, SessionPhaseRegular, SessionPhasePostMarket:
	default:
		return ErrUnknownSessionPhase
	}
	state.mutexGuardingIsOpen.Lock()
	defer state.mutexGuardingIsOpen.Unlock()
	state.sessionPhase = phase
	return nil
}

// CurrentSessionPhase returns the current session phase — defaults to
// SessionPhaseClosed on a freshly constructed MarketSessionState, mirroring
// IsMarketOpen's own "starts closed" default.
func (state *MarketSessionState) CurrentSessionPhase() SessionPhase {
	state.mutexGuardingIsOpen.RLock()
	defer state.mutexGuardingIsOpen.RUnlock()
	if state.sessionPhase == "" {
		return SessionPhaseClosed
	}
	return state.sessionPhase
}
