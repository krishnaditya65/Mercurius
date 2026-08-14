// Package liquiditybadge implements FEATURES.md §21's "Liquidity/
// fill-probability badge on the order ticket for illiquid instruments":
// given real order book depth (REUSING
// internal/impactcostestimator.OrderBookDepthSnapshot — the exact same
// depth shape that package already walks, not a reimplementation), this
// computes a real liquidity classification (HIGH/MEDIUM/LOW, based on
// real depth-at-price thresholds on the relevant side of the book for a
// hypothetical order) and an expected-time-to-fill estimate.
//
// HONEST, LOUD SCOPE BOUNDARY: the expected-time-to-fill figure is an
// ILLUSTRATIVE, formula-derived estimate — NOT a real ML-fitted model
// trained on actual historical fill-time data (this codebase has none).
// It exists to prove the request/response shape and give a directionally
// sensible number (deeper book relative to order size -> faster
// estimate), not to be relied on as a calibrated prediction. See this
// package's own "LOUD, REPEATED KNOWN GAP" convention, matching
// internal/impactcostestimator's own depth-source gap, which this
// package inherits unchanged (same caller-supplied-snapshot pattern).
package liquiditybadge

import (
	"errors"

	"mercurius/omsgateway/internal/impactcostestimator"
)

// LiquidityClassification is a closed set — HIGH/MEDIUM/LOW — so a
// client-facing badge can render deterministically.
type LiquidityClassification string

const (
	LiquidityHigh   LiquidityClassification = "HIGH"
	LiquidityMedium LiquidityClassification = "MEDIUM"
	LiquidityLow    LiquidityClassification = "LOW"
)

var (
	// ErrZeroHypotheticalQuantity mirrors impactcostestimator's own
	// validation — there's nothing to classify for a zero-size order.
	ErrZeroHypotheticalQuantity = errors.New("hypotheticalQuantity must be greater than zero")

	// ErrNonPositiveThresholds is returned by NewThresholds-style
	// validation when a configured depth threshold isn't a real,
	// positive ordering.
	ErrNonPositiveThresholds = errors.New("liquiditybadge: HighDepthQuantityThreshold must be greater than MediumDepthQuantityThreshold, and both must be positive")
)

// Thresholds configures the real depth-at-price boundaries between
// classifications, and the illustrative time-to-fill formula's base
// seconds per classification.
type Thresholds struct {
	// HighDepthQuantityThreshold: total resting quantity on the relevant
	// side of the book (summed across every supplied level — a real,
	// simple, hand-checkable depth-at-price aggregate) at or above this
	// is classified HIGH.
	HighDepthQuantityThreshold uint64
	// MediumDepthQuantityThreshold: total depth at or above this (but
	// below HighDepthQuantityThreshold) is classified MEDIUM; anything
	// below is LOW.
	MediumDepthQuantityThreshold uint64

	// IllustrativeBaseSecondsByClassification: the base expected-fill-time
	// estimate (in seconds) for an order that's a SMALL fraction of the
	// classified depth — scaled up as the hypothetical order consumes a
	// larger fraction of available depth (see ComputeLiquidityBadge's doc
	// comment for the exact formula).
	IllustrativeBaseSecondsHigh   float64
	IllustrativeBaseSecondsMedium float64
	IllustrativeBaseSecondsLow    float64
}

// DefaultThresholds returns real, illustrative default boundaries.
func DefaultThresholds() Thresholds {
	return Thresholds{
		HighDepthQuantityThreshold:    1000,
		MediumDepthQuantityThreshold:  200,
		IllustrativeBaseSecondsHigh:   1.0,
		IllustrativeBaseSecondsMedium: 5.0,
		IllustrativeBaseSecondsLow:    30.0,
	}
}

func (thresholds Thresholds) validate() error {
	if thresholds.HighDepthQuantityThreshold == 0 || thresholds.MediumDepthQuantityThreshold == 0 {
		return ErrNonPositiveThresholds
	}
	if thresholds.HighDepthQuantityThreshold <= thresholds.MediumDepthQuantityThreshold {
		return ErrNonPositiveThresholds
	}
	return nil
}

// Badge is the real, structured result an order ticket would render.
type Badge struct {
	Classification                        LiquidityClassification `json:"classification"`
	TotalRelevantSideDepthQuantity        uint64                  `json:"totalRelevantSideDepthQuantity"`
	HypotheticalQuantity                  uint64                  `json:"hypotheticalQuantity"`
	IllustrativeExpectedTimeToFillSeconds float64                 `json:"illustrativeExpectedTimeToFillSeconds"`
	IsIllustrativeEstimate                bool                    `json:"isIllustrativeEstimate"`
}

// ComputeLiquidityBadge classifies liquidity and estimates fill time for
// a hypothetical order against a real depth snapshot's relevant side
// (asks for a buy, bids for a sell — same convention
// internal/impactcostestimator's EstimateImpactCost uses).
//
// Classification: TotalRelevantSideDepthQuantity (the real sum of every
// supplied level's quantity on that side) >= HighDepthQuantityThreshold
// -> HIGH; >= MediumDepthQuantityThreshold -> MEDIUM; otherwise LOW (this
// includes zero depth at all, which is definitionally the least liquid
// case).
//
// Expected time to fill (ILLUSTRATIVE — see package doc): baseSeconds
// for the position's classification, scaled by how large a fraction of
// the available depth the hypothetical order represents:
//
//	consumptionRatio = hypotheticalQuantity / totalDepth   (or, if
//	    totalDepth is zero, a fixed large ratio representing "very hard
//	    to fill")
//	estimatedSeconds = baseSeconds * max(1.0, consumptionRatio)
//
// so an order that's a small sliver of a deep book fills at roughly the
// classification's base estimate, while an order that would consume most
// or all of the visible depth scales proportionally slower.
func ComputeLiquidityBadge(
	snapshot impactcostestimator.OrderBookDepthSnapshot,
	isBuyNotSell bool,
	hypotheticalQuantity uint64,
	thresholds Thresholds,
) (Badge, error) {
	if hypotheticalQuantity == 0 {
		return Badge{}, ErrZeroHypotheticalQuantity
	}
	if validationError := thresholds.validate(); validationError != nil {
		return Badge{}, validationError
	}

	relevantLevels := snapshot.AskLevels
	if !isBuyNotSell {
		relevantLevels = snapshot.BidLevels
	}

	var totalDepth uint64
	for _, level := range relevantLevels {
		totalDepth += level.Quantity
	}

	var classification LiquidityClassification
	var baseSeconds float64
	switch {
	case totalDepth >= thresholds.HighDepthQuantityThreshold:
		classification = LiquidityHigh
		baseSeconds = thresholds.IllustrativeBaseSecondsHigh
	case totalDepth >= thresholds.MediumDepthQuantityThreshold:
		classification = LiquidityMedium
		baseSeconds = thresholds.IllustrativeBaseSecondsMedium
	default:
		classification = LiquidityLow
		baseSeconds = thresholds.IllustrativeBaseSecondsLow
	}

	var consumptionRatio float64
	if totalDepth == 0 {
		// No depth at all on the relevant side -- treat as maximally hard
		// to fill: the ratio is the order size itself relative to a
		// notional "1 unit of depth", which trivially makes
		// consumptionRatio >= hypotheticalQuantity and dominates the max()
		// below for any real order size.
		consumptionRatio = float64(hypotheticalQuantity)
	} else {
		consumptionRatio = float64(hypotheticalQuantity) / float64(totalDepth)
	}
	if consumptionRatio < 1.0 {
		consumptionRatio = 1.0
	}

	return Badge{
		Classification:                        classification,
		TotalRelevantSideDepthQuantity:        totalDepth,
		HypotheticalQuantity:                  hypotheticalQuantity,
		IllustrativeExpectedTimeToFillSeconds: baseSeconds * consumptionRatio,
		IsIllustrativeEstimate:                true,
	}, nil
}
