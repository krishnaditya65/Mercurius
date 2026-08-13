// Package papertrading implements FEATURES.md §7's "Paper trading mode
// sharing the exact same OMS code path as live": the ONLY thing this
// package does is compute what a paper order's simulated fill would be.
// Everything else about paper trading — reusing the real pre-trade risk
// check, the real audit trail, and a genuinely separate positions book —
// lives in cmd/server/main.go's processOrderSubmission, which branches on
// orders.OrderSubmissionRequest.IsPaperTradingOrder only at the very last
// step (the hand-off to matching-engine / settlement to ledger), not
// anywhere earlier. That's the whole point: a paper order runs through
// EXACTLY the same KYC/freeze/pledge/risk gates as a live order and can
// be genuinely rejected by any of them — this package never sees an
// order that hasn't already cleared those gates.
//
// SimulateFill deliberately makes the simplest possible assumption: a
// LIMIT order fills immediately and completely at its own submitted
// limit price; a MARKET order fills at a caller-supplied reference price
// (oms-gateway has no live last-traded-price feed to fill a market order
// against — the same documented gap the risk check's market-order TODO
// and internal/marginpledge's reference-price gap already carry). A real
// paper-trading engine would simulate against a live (or realistically
// synthesized) order book, including partial fills and slippage; this is
// an illustrative, always-fully-filled simplification.
package papertrading

import (
	"errors"

	"mercurius/omsgateway/internal/orders"
)

var (
	// ErrLimitPriceMustBePositive is returned when simulating a fill for
	// a LIMIT paper order with a non-positive limit price.
	ErrLimitPriceMustBePositive = errors.New("paper LIMIT order requires a positive limitPriceInMinorUnits")

	// ErrMarketOrderRequiresReferencePrice is returned when simulating a
	// fill for a MARKET paper order without a caller-supplied reference
	// price — see the package doc's "no live price feed" gap.
	ErrMarketOrderRequiresReferencePrice = errors.New("a paper MARKET order requires paperMarketReferencePriceInMinorUnits since oms-gateway has no live price feed")
)

// SyntheticCounterpartyAccountIdentifier is the "other side" of every
// simulated paper fill. Paper trading has no real counterparty — this
// package always fills a paper order in full, immediately, against an
// imaginary market maker, so the client's paper position book still has
// a well-defined buyer/seller pair to apply (positions.PositionBook.
// ApplyFill expects both sides of a trade).
const SyntheticCounterpartyAccountIdentifier = "paper-market-maker"

// SimulatedFill is the outcome of simulating one paper order.
type SimulatedFill struct {
	ExecutedPriceInMinorUnits int64
	ExecutedQuantity          uint64
}

// SimulateFill computes the simulated fill for one already-risk-approved
// paper order. Never touches any shared state — pure function of its
// input — so it's trivially safe to call from any goroutine.
func SimulateFill(request orders.OrderSubmissionRequest) (SimulatedFill, error) {
	if request.OrderIsMarketOrderNotLimit {
		if request.PaperMarketReferencePriceInMinorUnits == nil || *request.PaperMarketReferencePriceInMinorUnits <= 0 {
			return SimulatedFill{}, ErrMarketOrderRequiresReferencePrice
		}
		return SimulatedFill{
			ExecutedPriceInMinorUnits: *request.PaperMarketReferencePriceInMinorUnits,
			ExecutedQuantity:          request.OrderQuantity,
		}, nil
	}

	if request.LimitPriceInMinorUnits <= 0 {
		return SimulatedFill{}, ErrLimitPriceMustBePositive
	}
	return SimulatedFill{
		ExecutedPriceInMinorUnits: request.LimitPriceInMinorUnits,
		ExecutedQuantity:          request.OrderQuantity,
	}, nil
}
