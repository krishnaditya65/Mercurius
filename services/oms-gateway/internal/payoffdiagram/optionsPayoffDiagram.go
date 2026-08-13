// Package payoffdiagram implements FEATURES.md §15's "Options strategy
// payoff diagram (max profit/loss, breakevens computed live as legs are
// added)" — real math, not a canned lookup table, for an arbitrary set
// of option legs (any mix of calls/puts, strikes, buy/sell, quantities).
//
// The core insight this package leans on: an at-expiry option payoff is
// PIECEWISE LINEAR in the underlying spot price, with kinks (slope
// changes) occurring EXACTLY at each leg's strike price, and the domain
// is bounded below at spot=0 (a real underlying price can never go
// negative) but unbounded above. That means:
//   - the strategy's overall max profit / max loss, if finite, is
//     always attained at spot=0 or at one of the strikes — never at an
//     interior point of a linear segment (a line has no interior
//     extremum) — so evaluating the payoff at those candidate points
//     and taking min/max is exact, not an approximation;
//   - whether profit/loss is UNBOUNDED is exactly determined by the
//     slope of the single ray beyond the highest strike (the only place
//     spot is genuinely unbounded);
//   - breakevens (zero-crossings) can only occur within one of the
//     finite linear segments between consecutive candidate points, or
//     on the one unbounded ray, and — because each segment is exactly
//     linear — a crossing within a segment is found by exact linear
//     interpolation, not a numerical root-finder.
//
// ComputePayoffDiagram is meant to be called fresh every time a caller's
// leg set changes ("computed live as legs are added") — it is a pure
// function of the leg slice, holding no state of its own, so a caller
// (e.g. cmd/server/main.go's buildComputePayoffDiagramHandler) can just
// re-invoke it on every request.
package payoffdiagram

import (
	"errors"
	"math"
	"sort"
)

// OptionType identifies a leg as a call or a put.
type OptionType string

const (
	OptionTypeCall OptionType = "CALL"
	OptionTypePut  OptionType = "PUT"
)

var (
	// ErrNoLegs is returned when ComputePayoffDiagram is given an empty
	// leg slice — there is no strategy to diagram.
	ErrNoLegs = errors.New("at least one option leg is required")

	// ErrZeroQuantity is returned when a leg's Quantity is zero.
	ErrZeroQuantity = errors.New("leg quantity must be greater than zero")

	// ErrUnknownOptionType is returned when a leg's OptionType is neither
	// OptionTypeCall nor OptionTypePut.
	ErrUnknownOptionType = errors.New("leg optionType must be CALL or PUT")

	// ErrNegativeStrikePrice is returned when a leg's strike is negative.
	ErrNegativeStrikePrice = errors.New("leg strikePriceInMinorUnits must not be negative")

	// ErrNegativePremium is returned when a leg's premium is negative.
	ErrNegativePremium = errors.New("leg premiumInMinorUnits must not be negative")
)

// OptionLeg is one leg of a (possibly multi-leg) options strategy, for
// payoff purposes only — no lot-size/expiry/underlying-symbol fields;
// see internal/multilegoptions for the fuller order-submission-shaped
// leg type this deliberately does not depend on (this package has no
// reason to couple to how a leg gets executed).
type OptionLeg struct {
	OptionType              OptionType `json:"optionType"`
	StrikePriceInMinorUnits int64      `json:"strikePriceInMinorUnits"`
	PremiumInMinorUnits     int64      `json:"premiumInMinorUnits"`
	IsBuyNotSell            bool       `json:"isBuyNotSell"`
	Quantity                uint64     `json:"quantity"`
}

func validateLegs(legs []OptionLeg) error {
	if len(legs) == 0 {
		return ErrNoLegs
	}
	for _, leg := range legs {
		if leg.Quantity == 0 {
			return ErrZeroQuantity
		}
		if leg.OptionType != OptionTypeCall && leg.OptionType != OptionTypePut {
			return ErrUnknownOptionType
		}
		if leg.StrikePriceInMinorUnits < 0 {
			return ErrNegativeStrikePrice
		}
		if leg.PremiumInMinorUnits < 0 {
			return ErrNegativePremium
		}
	}
	return nil
}

// intrinsicValueAtSpot is a single leg's intrinsic (exercise) value at
// expiry, for one unit, given the underlying's spot price.
func intrinsicValueAtSpot(leg OptionLeg, spot float64) float64 {
	strike := float64(leg.StrikePriceInMinorUnits)
	if leg.OptionType == OptionTypeCall {
		return math.Max(spot-strike, 0)
	}
	return math.Max(strike-spot, 0)
}

// legPnLAtSpot is one leg's total profit/loss (across its whole
// Quantity) at expiry, given spot: a bought leg paid PremiumInMinorUnits
// per unit up front and nets (intrinsic - premium); a sold leg received
// the premium up front and nets (premium - intrinsic).
func legPnLAtSpot(leg OptionLeg, spot float64) float64 {
	intrinsic := intrinsicValueAtSpot(leg, spot)
	premium := float64(leg.PremiumInMinorUnits)
	quantity := float64(leg.Quantity)
	if leg.IsBuyNotSell {
		return quantity * (intrinsic - premium)
	}
	return quantity * (premium - intrinsic)
}

// PayoffAtSpotPrice returns the combined strategy's total profit/loss at
// expiry for a given hypothetical spot price — the raw "y" value a
// payoff diagram plots at a given "x".
func PayoffAtSpotPrice(legs []OptionLeg, spotPriceInMinorUnits float64) (float64, error) {
	if err := validateLegs(legs); err != nil {
		return 0, err
	}
	total := 0.0
	for _, leg := range legs {
		total += legPnLAtSpot(leg, spotPriceInMinorUnits)
	}
	return total, nil
}

// segmentSlopeAtSpot returns the strategy's payoff slope (d(payoff)/d(spot))
// at a spot price known to fall strictly inside one linear segment (i.e.
// not exactly on a strike) — used both for interior segments and for the
// one unbounded ray beyond the highest strike.
func segmentSlopeAtSpot(legs []OptionLeg, spot float64) float64 {
	slope := 0.0
	for _, leg := range legs {
		sign := -1.0
		if leg.IsBuyNotSell {
			sign = 1.0
		}
		strike := float64(leg.StrikePriceInMinorUnits)
		quantity := float64(leg.Quantity)
		var derivative float64
		if leg.OptionType == OptionTypeCall {
			if spot > strike {
				derivative = 1
			}
		} else {
			if spot < strike {
				derivative = -1
			}
		}
		slope += sign * quantity * derivative
	}
	return slope
}

// PayoffDiagramResult is the full computed shape of a strategy's payoff:
// its max profit, its max loss (reported as a positive magnitude — "you
// can lose up to this much", never a negative number), and every
// breakeven spot price, sorted ascending.
type PayoffDiagramResult struct {
	MaxProfitInMinorUnits float64 `json:"maxProfitInMinorUnits,omitempty"`
	MaxProfitIsUnbounded  bool    `json:"maxProfitIsUnbounded"`

	MaxLossInMinorUnits float64 `json:"maxLossInMinorUnits,omitempty"`
	MaxLossIsUnbounded  bool    `json:"maxLossIsUnbounded"`

	BreakevenPricesInMinorUnits []float64 `json:"breakevenPricesInMinorUnits,omitempty"`
}

const floatEqualityTolerance = 1e-6

// ComputePayoffDiagram computes the real max profit, max loss, and every
// breakeven price for the given set of option legs, using the exact
// piecewise-linear analysis described in this package's doc comment —
// see that comment for why evaluating only at spot=0 and each strike is
// provably sufficient, not a sampling approximation.
func ComputePayoffDiagram(legs []OptionLeg) (PayoffDiagramResult, error) {
	if err := validateLegs(legs); err != nil {
		return PayoffDiagramResult{}, err
	}

	candidates := candidateSpotPrices(legs)
	payoffs := make([]float64, len(candidates))
	for i, spot := range candidates {
		payoffs[i], _ = PayoffAtSpotPrice(legs, spot)
	}

	highestStrike := candidates[len(candidates)-1]
	rightRaySlope := segmentSlopeAtSpot(legs, highestStrike+1)

	result := PayoffDiagramResult{}

	maxCandidate, minCandidate := payoffs[0], payoffs[0]
	for _, p := range payoffs {
		if p > maxCandidate {
			maxCandidate = p
		}
		if p < minCandidate {
			minCandidate = p
		}
	}

	if rightRaySlope > floatEqualityTolerance {
		result.MaxProfitIsUnbounded = true
	} else {
		result.MaxProfitInMinorUnits = maxCandidate
	}

	if rightRaySlope < -floatEqualityTolerance {
		result.MaxLossIsUnbounded = true
	} else {
		result.MaxLossInMinorUnits = -minCandidate
		if result.MaxLossInMinorUnits < 0 {
			// A strategy that never loses money (min payoff > 0) has a
			// "max loss" of 0, not a negative number.
			result.MaxLossInMinorUnits = 0
		}
	}

	result.BreakevenPricesInMinorUnits = findBreakevens(candidates, payoffs, rightRaySlope)

	return result, nil
}

// candidateSpotPrices returns 0 plus every distinct strike price, sorted
// ascending — the only spot prices where an extremum or a segment
// boundary can occur, per this package's doc comment.
func candidateSpotPrices(legs []OptionLeg) []float64 {
	seen := map[float64]bool{0: true}
	candidates := []float64{0}
	for _, leg := range legs {
		strike := float64(leg.StrikePriceInMinorUnits)
		if !seen[strike] {
			seen[strike] = true
			candidates = append(candidates, strike)
		}
	}
	sort.Float64s(candidates)
	return candidates
}

// findBreakevens walks each finite linear segment between consecutive
// candidate spot prices, plus the one unbounded ray beyond the highest
// candidate (if it isn't flat), looking for exact zero-crossings.
func findBreakevens(candidates []float64, payoffs []float64, rightRaySlope float64) []float64 {
	var breakevens []float64

	appendIfNew := func(price float64) {
		for _, existing := range breakevens {
			if math.Abs(existing-price) < floatEqualityTolerance {
				return
			}
		}
		breakevens = append(breakevens, price)
	}

	for i := 0; i < len(candidates)-1; i++ {
		x0, x1 := candidates[i], candidates[i+1]
		y0, y1 := payoffs[i], payoffs[i+1]

		if math.Abs(y0) < floatEqualityTolerance {
			appendIfNew(x0)
		}
		if math.Abs(y1) < floatEqualityTolerance {
			appendIfNew(x1)
		}
		if y0*y1 < 0 {
			// Opposite signs, neither exactly zero: exact linear
			// interpolation for the crossing point.
			t := y0 / (y0 - y1)
			appendIfNew(x0 + t*(x1-x0))
		}
	}

	// The unbounded ray beyond the highest candidate: only relevant if
	// it isn't flat (a flat ray either never crosses, or is already
	// captured as a breakeven at the highest candidate itself above).
	lastIndex := len(candidates) - 1
	lastX, lastY := candidates[lastIndex], payoffs[lastIndex]
	if math.Abs(rightRaySlope) > floatEqualityTolerance && math.Abs(lastY) >= floatEqualityTolerance {
		limitSign := 1.0
		if rightRaySlope < 0 {
			limitSign = -1.0
		}
		if (lastY > 0 && limitSign < 0) || (lastY < 0 && limitSign > 0) {
			crossing := lastX - lastY/rightRaySlope
			appendIfNew(crossing)
		}
	}

	sort.Float64s(breakevens)
	return breakevens
}
