// This file extends internal/marginengine with FEATURES.md §15's
// "Portfolio margining / cross-margining across correlated asset classes":
// a real netting-benefit calculation for a portfolio of derivatives
// positions spanning multiple asset classes, built on top of
// CalculateSpanAndExposureMargin's per-position standalone margin.
//
// THE REAL IDEA, using an illustrative correlation table: two OFFSETTING
// positions (one long, one short) in two POSITIVELY correlated asset
// classes partially hedge each other's directional risk — a real
// clearing corporation's portfolio margining scheme (e.g. NSE's SPAN-based
// cross-margining between index futures and index options, or CME's SPAN
// 2) grants real margin relief for exactly this reason. This package
// computes that relief as, for every pair of positions in DIFFERENT asset
// classes with OPPOSITE direction (long vs. short) and a POSITIVE
// correlation coefficient:
//
//	nettingBenefit(i, j) = correlation(assetClass_i, assetClass_j) * min(standaloneMargin_i, standaloneMargin_j)
//
// summed across every such pair, then subtracted from the sum of every
// position's standalone (gross) margin — floored at the single LARGEST
// standalone margin in the portfolio (a real portfolio margin scheme
// never nets a multi-position book down below what its single riskiest
// leg alone would require).
//
// LOUD, REPEATED WARNING — same caliber as this package's existing SPAN/
// exposure-margin warning: illustrativeCorrelationByAssetClassPair below
// is a MADE-UP, order-of-magnitude correlation table, NOT sourced from
// any real historical return-correlation study or exchange-published
// cross-margining eligibility list (e.g. NSE's actual index-futures/
// index-options cross-margining benefit table). Same-asset-class netting
// (e.g. two different equity positions) is NOT modeled at all here — a
// real build would need a full covariance-matrix / scenario-based
// portfolio VaR calculation, not a pairwise correlation lookup. This is
// illustrative machinery to prove the request/response shape and the
// netting-benefit ARITHMETIC pattern, exactly like this package's
// existing SPAN calculator — NOT exchange-certified, NOT SEBI-compliant,
// must never size a real margin requirement against real capital.
package marginengine

import "errors"

// AssetClass is an illustrative classification for cross-margining
// correlation lookups — mirrors internal/exposurelimits' own illustrative
// segment taxonomy (this repo has no real instrument-master asset-class
// field).
type AssetClass string

const (
	AssetClassEquity       AssetClass = "EQUITY"
	AssetClassIndexFutures AssetClass = "INDEX_FUTURES"
	AssetClassIndexOptions AssetClass = "INDEX_OPTIONS"
	AssetClassCurrency     AssetClass = "CURRENCY"
	AssetClassCommodity    AssetClass = "COMMODITY"
	AssetClassInterestRate AssetClass = "INTEREST_RATE"
)

// illustrativeCorrelationByAssetClassPair: a made-up, symmetric
// correlation table — see this file's loud warning above. Only positive
// entries can ever contribute a netting benefit (see
// correlationBetween); an unlisted pair defaults to 0 (no assumed
// relationship at all — conservative, not optimistic).
var illustrativeCorrelationByAssetClassPair = map[[2]AssetClass]float64{
	{AssetClassEquity, AssetClassIndexFutures}:       0.85,
	{AssetClassEquity, AssetClassIndexOptions}:       0.80,
	{AssetClassIndexFutures, AssetClassIndexOptions}: 0.95,
	{AssetClassEquity, AssetClassCurrency}:           0.10,
	{AssetClassEquity, AssetClassCommodity}:          0.15,
	{AssetClassCurrency, AssetClassInterestRate}:     0.40,
	{AssetClassCommodity, AssetClassInterestRate}:    0.05,
}

// correlationBetween looks up the illustrative correlation between two
// (possibly identical) asset classes, symmetric regardless of argument
// order. Same-asset-class pairs return 0 here deliberately — see the
// package doc's "same-asset-class netting is NOT modeled" gap; this
// function is only ever consulted for cross-asset-class pairs by
// CalculatePortfolioMargin below.
func correlationBetween(a, b AssetClass) float64 {
	if correlation, ok := illustrativeCorrelationByAssetClassPair[[2]AssetClass{a, b}]; ok {
		return correlation
	}
	if correlation, ok := illustrativeCorrelationByAssetClassPair[[2]AssetClass{b, a}]; ok {
		return correlation
	}
	return 0
}

var (
	// ErrNoPositions is returned when CalculatePortfolioMargin is given an
	// empty position slice.
	ErrNoPositions = errors.New("at least one position is required")

	// ErrPositionNotionalMustBeNonNegative mirrors
	// ErrContractNotionalValueMustBeNonNegative for one leg of a
	// portfolio.
	ErrPositionNotionalMustBeNonNegative = errors.New("position contract notional value must be non-negative")

	// ErrUnknownAssetClass is returned when a position's AssetClass isn't
	// one of the recognized constants above.
	ErrUnknownAssetClass = errors.New("unknown asset class")
)

func isKnownAssetClass(assetClass AssetClass) bool {
	switch assetClass {
	case AssetClassEquity, AssetClassIndexFutures, AssetClassIndexOptions, AssetClassCurrency, AssetClassCommodity, AssetClassInterestRate:
		return true
	default:
		return false
	}
}

// Position is one leg of a cross-margined portfolio.
type Position struct {
	AssetClass                        AssetClass `json:"assetClass"`
	ContractNotionalValueInMinorUnits int64      `json:"contractNotionalValueInMinorUnits"`
	IsLongNotShort                    bool       `json:"isLongNotShort"`
}

// PositionMarginDetail is one position's standalone margin, echoed back
// for auditability (mirrors MarginRequirement's "receipt" style).
type PositionMarginDetail struct {
	Position                     Position `json:"position"`
	StandaloneMarginInMinorUnits int64    `json:"standaloneMarginInMinorUnits"`
}

// NettingBenefitDetail is one pair's real netting-benefit contribution —
// every pair that actually contributed is listed individually so the
// total is hand-auditable, mirroring chargescalculator's receipt-style
// transparency.
type NettingBenefitDetail struct {
	FirstPositionIndex         int     `json:"firstPositionIndex"`
	SecondPositionIndex        int     `json:"secondPositionIndex"`
	CorrelationApplied         float64 `json:"correlationApplied"`
	NettingBenefitInMinorUnits int64   `json:"nettingBenefitInMinorUnits"`
}

// PortfolioMarginResult is CalculatePortfolioMargin's full, auditable
// output.
type PortfolioMarginResult struct {
	PositionMarginDetails           []PositionMarginDetail `json:"positionMarginDetails"`
	GrossMarginInMinorUnits         int64                  `json:"grossMarginInMinorUnits"`
	NettingBenefits                 []NettingBenefitDetail `json:"nettingBenefits,omitempty"`
	TotalNettingBenefitInMinorUnits int64                  `json:"totalNettingBenefitInMinorUnits"`
	NetPortfolioMarginInMinorUnits  int64                  `json:"netPortfolioMarginInMinorUnits"`
}

// CalculatePortfolioMargin computes each position's standalone SPAN+
// exposure margin, then a real cross-asset-class netting benefit for
// every offsetting (opposite-direction), positively-correlated pair —
// see this file's package doc for the exact formula and its honest
// scope/accuracy caveats.
func CalculatePortfolioMargin(positions []Position) (PortfolioMarginResult, error) {
	if len(positions) == 0 {
		return PortfolioMarginResult{}, ErrNoPositions
	}

	result := PortfolioMarginResult{}
	standaloneMargins := make([]int64, len(positions))

	for i, position := range positions {
		if position.ContractNotionalValueInMinorUnits < 0 {
			return PortfolioMarginResult{}, ErrPositionNotionalMustBeNonNegative
		}
		if !isKnownAssetClass(position.AssetClass) {
			return PortfolioMarginResult{}, ErrUnknownAssetClass
		}
		margin, err := CalculateSpanAndExposureMargin(position.ContractNotionalValueInMinorUnits)
		if err != nil {
			return PortfolioMarginResult{}, err
		}
		standaloneMargins[i] = margin.TotalRequiredMarginInMinorUnits
		result.GrossMarginInMinorUnits += margin.TotalRequiredMarginInMinorUnits
		result.PositionMarginDetails = append(result.PositionMarginDetails, PositionMarginDetail{
			Position:                     position,
			StandaloneMarginInMinorUnits: margin.TotalRequiredMarginInMinorUnits,
		})
	}

	var largestStandaloneMargin int64
	for _, margin := range standaloneMargins {
		if margin > largestStandaloneMargin {
			largestStandaloneMargin = margin
		}
	}

	for i := 0; i < len(positions); i++ {
		for j := i + 1; j < len(positions); j++ {
			if positions[i].AssetClass == positions[j].AssetClass {
				continue // same-asset-class netting not modeled — see package doc
			}
			if positions[i].IsLongNotShort == positions[j].IsLongNotShort {
				continue // not offsetting — both same direction, no hedge relationship assumed
			}
			correlation := correlationBetween(positions[i].AssetClass, positions[j].AssetClass)
			if correlation <= 0 {
				continue
			}
			smallerMargin := standaloneMargins[i]
			if standaloneMargins[j] < smallerMargin {
				smallerMargin = standaloneMargins[j]
			}
			benefit := roundToNearestMinorUnit(correlation * float64(smallerMargin))
			if benefit <= 0 {
				continue
			}
			result.NettingBenefits = append(result.NettingBenefits, NettingBenefitDetail{
				FirstPositionIndex:         i,
				SecondPositionIndex:        j,
				CorrelationApplied:         correlation,
				NettingBenefitInMinorUnits: benefit,
			})
			result.TotalNettingBenefitInMinorUnits += benefit
		}
	}

	netMargin := result.GrossMarginInMinorUnits - result.TotalNettingBenefitInMinorUnits
	if netMargin < largestStandaloneMargin {
		// A real portfolio margin scheme never nets a multi-position book
		// down below what its single riskiest leg alone would require.
		netMargin = largestStandaloneMargin
	}
	result.NetPortfolioMarginInMinorUnits = netMargin

	return result, nil
}
