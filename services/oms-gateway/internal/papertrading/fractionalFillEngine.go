// This file extends internal/papertrading with FEATURES.md §17's
// fractional share investing: a real, milli-share-precision simulated
// fill, additive alongside SimulateFill (simulatedFillEngine.go) which
// is left completely untouched. See internal/fractionalshares' package
// doc for the full design rationale and the honest "paper trading only"
// scope boundary this mirrors.
package papertrading

import (
	"errors"

	"mercurius/omsgateway/internal/orders"
)

// ErrMissingMilliShareQuantity is returned when SimulateFractionalFill
// is called on a request with no MilliShareQuantity at all -- callers
// should never reach this in practice since
// fractionalshares.ValidateMilliShareQuantity is meant to run first, but
// this function fails loudly rather than silently rather than panicking
// on the nil dereference.
var ErrMissingMilliShareQuantity = errors.New("paper fractional fill requires a non-nil milliShareQuantity")

// SimulatedFractionalFill is the outcome of simulating one paper order
// with a milli-share-precision quantity.
type SimulatedFractionalFill struct {
	ExecutedPriceInMinorUnits  int64
	ExecutedMilliShareQuantity uint64
}

// SimulateFractionalFill mirrors SimulateFill's exact fill-price rules
// (LIMIT fills immediately and completely at its own limit price; MARKET
// fills at the caller-supplied reference price) but fills the REAL
// MilliShareQuantity instead of the whole-share OrderQuantity. Callers
// must have already validated (via
// fractionalshares.ValidateMilliShareQuantity) that MilliShareQuantity
// is non-nil, positive, and that this is a paper-trading order — this
// function does not re-validate that contract, only the price rules
// SimulateFill already enforces.
func SimulateFractionalFill(request orders.OrderSubmissionRequest) (SimulatedFractionalFill, error) {
	if request.MilliShareQuantity == nil {
		return SimulatedFractionalFill{}, ErrMissingMilliShareQuantity
	}

	if request.OrderIsMarketOrderNotLimit {
		if request.PaperMarketReferencePriceInMinorUnits == nil || *request.PaperMarketReferencePriceInMinorUnits <= 0 {
			return SimulatedFractionalFill{}, ErrMarketOrderRequiresReferencePrice
		}
		return SimulatedFractionalFill{
			ExecutedPriceInMinorUnits:  *request.PaperMarketReferencePriceInMinorUnits,
			ExecutedMilliShareQuantity: *request.MilliShareQuantity,
		}, nil
	}

	if request.LimitPriceInMinorUnits <= 0 {
		return SimulatedFractionalFill{}, ErrLimitPriceMustBePositive
	}
	return SimulatedFractionalFill{
		ExecutedPriceInMinorUnits:  request.LimitPriceInMinorUnits,
		ExecutedMilliShareQuantity: *request.MilliShareQuantity,
	}, nil
}
