// Package portfoliostresstest implements FEATURES.md §21's portfolio
// stress test: "if Nifty drops 10% tomorrow, your portfolio loses ~₹X".
// Given an account's real positions (equity + options) and a
// hypothetical market-wide shock percentage, this computes a real
// estimated P&L impact.
//
// HONEST, LOAD-BEARING SCOPE BOUNDARY:
//   - Equity positions are scaled EXACTLY (linearly) by the shock
//     percentage — this is exact, not an approximation: a position's
//     mark-to-market value genuinely does move linearly with its own
//     price.
//   - Option positions use their DELTA as a FIRST-ORDER (linear)
//     approximation of the option's own price change in response to the
//     underlying moving by the shock percentage — this is NOT a full
//     repricing (it ignores gamma/convexity, theta decay over the
//     horizon, and any change in implied volatility a real 10% shock
//     would likely also cause). For a small shock this is a reasonable
//     estimate; for a LARGE shock (like the 10% example FEATURES.md
//     itself uses) delta-only materially understates the true P&L swing
//     for positions with meaningful gamma. A real build would re-run the
//     full Black-Scholes repricing (see internal/quantengineclient,
//     which oms-gateway already calls for internal/optionschain) at the
//     shocked underlying price instead of using delta alone. This
//     package deliberately doesn't do that — it's real, honest,
//     first-order math, not a full scenario repricing engine.
package portfoliostresstest

import "fmt"

// PositionType distinguishes the two supported position shapes.
type PositionType string

const (
	PositionTypeEquity PositionType = "EQUITY"
	PositionTypeOption PositionType = "OPTION"
)

// StressTestPositionInput is one position's real current state, as
// looked up from internal/positions (equity) or internal/optionschain /
// internal/payoffdiagram (options) by the caller — this package itself
// stays decoupled from those packages' own types, the same pattern
// internal/marketsession's OrderShapeForSessionRules already
// establishes.
type StressTestPositionInput struct {
	InstrumentSymbol string       `json:"instrumentSymbol"`
	PositionType     PositionType `json:"positionType"`

	// --- EQUITY fields (ignored for PositionTypeOption) ---

	// NetQuantity is the signed held quantity — positive for long,
	// negative for short.
	NetQuantity int64 `json:"netQuantity,omitempty"`
	// CurrentPriceInMinorUnits is the position's current market price.
	CurrentPriceInMinorUnits int64 `json:"currentPriceInMinorUnits,omitempty"`

	// --- OPTION fields (ignored for PositionTypeEquity) ---

	// NetContracts is the signed held contract count — positive for a
	// net-long delta exposure orientation, negative for net-short. The
	// SIGN of DeltaPerContract should already reflect a long call/short
	// put being positive delta and a long put/short call being negative
	// delta (the same convention internal/optionschain's live Greeks
	// already use) — this package does not itself flip signs based on
	// call/put, it trusts the caller's DeltaPerContract.
	NetContracts int64 `json:"netContracts,omitempty"`
	// ContractMultiplier is the lot size / contract multiplier (e.g. 1
	// for a simple 1-share-equivalent option, or a real exchange lot
	// size like 50 for NIFTY).
	ContractMultiplier int64 `json:"contractMultiplier,omitempty"`
	// DeltaPerContract is the option's current delta (from
	// internal/optionschain's live Black-Scholes call, or supplied
	// directly) — the first-order sensitivity of the option's price to a
	// 1-unit move in the underlying.
	DeltaPerContract float64 `json:"deltaPerContract,omitempty"`
	// UnderlyingCurrentPriceInMinorUnits is the current spot price of
	// this option's underlying instrument, used to compute the absolute
	// price move implied by the shock percentage.
	UnderlyingCurrentPriceInMinorUnits int64 `json:"underlyingCurrentPriceInMinorUnits,omitempty"`
}

// PositionImpact is one position's estimated stress-test P&L impact.
type PositionImpact struct {
	InstrumentSymbol               string       `json:"instrumentSymbol"`
	PositionType                   PositionType `json:"positionType"`
	EstimatedPnLImpactInMinorUnits int64        `json:"estimatedPnLImpactInMinorUnits"`
	IsFirstOrderApproximation      bool         `json:"isFirstOrderApproximation"`
}

// StressTestResult is the full portfolio-level result.
type StressTestResult struct {
	ShockPercent                        float64          `json:"shockPercent"`
	TotalEstimatedPnLImpactInMinorUnits int64            `json:"totalEstimatedPnLImpactInMinorUnits"`
	PerPositionImpacts                  []PositionImpact `json:"perPositionImpacts"`
}

// ComputeStressTest applies shockPercent (e.g. -0.10 for "market drops
// 10%") to every position and returns the real, summed estimated P&L
// impact. shockPercent is applied uniformly to every equity position's
// own price and every option's underlying price — a real market-wide
// shock scenario, not a per-instrument idiosyncratic move.
//
// Equity math is EXACT: impact = netQuantity * currentPrice * shockPercent.
// Option math is a FIRST-ORDER DELTA APPROXIMATION: impact = netContracts
// * contractMultiplier * deltaPerContract * (underlyingPrice * shockPercent)
// — see the package doc for the honest limitation this implies for large
// shocks.
func ComputeStressTest(positions []StressTestPositionInput, shockPercent float64) (StressTestResult, error) {
	if len(positions) == 0 {
		return StressTestResult{}, fmt.Errorf("portfoliostresstest: at least one position is required")
	}

	result := StressTestResult{
		ShockPercent:       shockPercent,
		PerPositionImpacts: make([]PositionImpact, 0, len(positions)),
	}

	for _, position := range positions {
		switch position.PositionType {
		case PositionTypeEquity:
			impact := roundToNearestMinorUnit(float64(position.NetQuantity) * float64(position.CurrentPriceInMinorUnits) * shockPercent)
			result.PerPositionImpacts = append(result.PerPositionImpacts, PositionImpact{
				InstrumentSymbol:               position.InstrumentSymbol,
				PositionType:                   position.PositionType,
				EstimatedPnLImpactInMinorUnits: impact,
				IsFirstOrderApproximation:      false,
			})
			result.TotalEstimatedPnLImpactInMinorUnits += impact

		case PositionTypeOption:
			underlyingPriceMove := float64(position.UnderlyingCurrentPriceInMinorUnits) * shockPercent
			impact := roundToNearestMinorUnit(
				float64(position.NetContracts) * float64(position.ContractMultiplier) * position.DeltaPerContract * underlyingPriceMove,
			)
			result.PerPositionImpacts = append(result.PerPositionImpacts, PositionImpact{
				InstrumentSymbol:               position.InstrumentSymbol,
				PositionType:                   position.PositionType,
				EstimatedPnLImpactInMinorUnits: impact,
				IsFirstOrderApproximation:      true,
			})
			result.TotalEstimatedPnLImpactInMinorUnits += impact

		default:
			return StressTestResult{}, fmt.Errorf("portfoliostresstest: unknown positionType %q for %s", position.PositionType, position.InstrumentSymbol)
		}
	}

	return result, nil
}

// roundToNearestMinorUnit mirrors this codebase's other packages'
// helper of the same name (chargescalculator, marginengine,
// marginpledge, marginfunding).
func roundToNearestMinorUnit(amount float64) int64 {
	if amount < 0 {
		return int64(amount - 0.5)
	}
	return int64(amount + 0.5)
}
