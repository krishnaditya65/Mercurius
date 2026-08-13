// Package marginengine implements FEATURES.md §3's "SPAN + exposure
// margin for F&O" — a real, hand-checkable calculator for the required
// margin on a single derivatives (futures/options) position, given a
// contract's notional value.
//
// LOUD, REPEATED WARNING: the two percentage rates below are an
// ILLUSTRATIVE model only. Real SPAN margin is produced by exchange-
// published SPAN risk-parameter files (a genuinely complex, multi-
// scenario portfolio-risk simulation run by NSE/BSE clearing
// corporations — not a flat percentage of notional at all), and real
// exposure margin slabs vary by underlying, index vs. stock, and change
// by exchange circular on no fixed schedule. This package is NOT
// exchange-certified, NOT SEBI-compliant, and must never be used to size
// a real margin requirement against real capital. It exists to prove the
// oms-gateway request/response shape and the illustrative-rate-table
// pattern this codebase already uses in internal/chargescalculator —
// nothing more.
package marginengine

import "errors"

// ErrContractNotionalValueMustBeNonNegative is returned when the caller
// passes a negative notional value — nonsensical for a margin
// calculation (a short position's notional is still expressed as a
// positive magnitude at this layer; sign/direction is the caller's
// concern, not this package's).
var ErrContractNotionalValueMustBeNonNegative = errors.New("contract notional value must be non-negative")

// Rate constants — see the package doc's warning above. Both are
// expressed as a fraction of the contract's notional value
// (price × lot-adjusted quantity), computed independently of each other
// per exchange convention (SPAN and exposure margin are two genuinely
// separate risk components, not one derived from the other).
const (
	// illustrativeSpanMarginPercentRate stands in for a real SPAN
	// scenario-based margin, which a real build would obtain from the
	// clearing corporation's published SPAN risk-parameter file for the
	// specific contract, not compute here. 10% is a rough, commonly-cited
	// order of magnitude for an index/stock futures SPAN margin — not
	// drawn from any specific current exchange circular.
	illustrativeSpanMarginPercentRate = 0.10

	// illustrativeExposureMarginPercentRate stands in for the additional
	// exposure margin exchanges layer on top of SPAN margin. 3% is
	// likewise a rough, commonly-cited order of magnitude, not a live
	// exchange value.
	illustrativeExposureMarginPercentRate = 0.03
)

// MarginRequirement is the full breakdown for one derivatives position —
// every component individually auditable against the rate constants
// above, mirroring chargescalculator.ChargesBreakdown's "receipt" shape.
type MarginRequirement struct {
	ContractNotionalValueInMinorUnits int64 `json:"contractNotionalValueInMinorUnits"`
	SpanMarginInMinorUnits            int64 `json:"spanMarginInMinorUnits"`
	ExposureMarginInMinorUnits        int64 `json:"exposureMarginInMinorUnits"`
	TotalRequiredMarginInMinorUnits   int64 `json:"totalRequiredMarginInMinorUnits"`
}

// CalculateSpanAndExposureMargin computes the illustrative SPAN +
// exposure margin requirement for one derivatives position given its
// contract notional value (e.g. futures: lot size × quantity × current
// price; options: whatever notional convention the caller has already
// settled on — this package is agnostic to how the notional was
// derived, it only operates on the resulting minor-units figure).
func CalculateSpanAndExposureMargin(contractNotionalValueInMinorUnits int64) (MarginRequirement, error) {
	if contractNotionalValueInMinorUnits < 0 {
		return MarginRequirement{}, ErrContractNotionalValueMustBeNonNegative
	}

	spanMargin := roundToNearestMinorUnit(float64(contractNotionalValueInMinorUnits) * illustrativeSpanMarginPercentRate)
	exposureMargin := roundToNearestMinorUnit(float64(contractNotionalValueInMinorUnits) * illustrativeExposureMarginPercentRate)

	return MarginRequirement{
		ContractNotionalValueInMinorUnits: contractNotionalValueInMinorUnits,
		SpanMarginInMinorUnits:            spanMargin,
		ExposureMarginInMinorUnits:        exposureMargin,
		TotalRequiredMarginInMinorUnits:   spanMargin + exposureMargin,
	}, nil
}

// roundToNearestMinorUnit mirrors chargescalculator's helper of the same
// name — rounds a fractional minor-unit amount to the nearest whole
// minor unit, half-away-from-zero.
func roundToNearestMinorUnit(amount float64) int64 {
	if amount < 0 {
		return int64(amount - 0.5)
	}
	return int64(amount + 0.5)
}
