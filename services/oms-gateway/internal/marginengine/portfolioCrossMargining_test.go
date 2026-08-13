package marginengine

import (
	"errors"
	"testing"
)

// TestCalculatePortfolioMargin_HandWorkedTwoLegOffsetting: long EQUITY
// notional 500,000 (margin = 10%+3% = 13% * 500000 = 65000) and short
// INDEX_FUTURES notional 400,000 (margin = 13% * 400000 = 52000).
// Correlation EQUITY/INDEX_FUTURES = 0.85. Netting benefit =
// 0.85 * min(65000, 52000) = 0.85 * 52000 = 44200.
// Gross = 65000 + 52000 = 117000. Net = 117000 - 44200 = 72800.
// Largest standalone = 65000, net (72800) > that, so no floor kicks in.
func TestCalculatePortfolioMargin_HandWorkedTwoLegOffsetting(t *testing.T) {
	positions := []Position{
		{AssetClass: AssetClassEquity, ContractNotionalValueInMinorUnits: 500000, IsLongNotShort: true},
		{AssetClass: AssetClassIndexFutures, ContractNotionalValueInMinorUnits: 400000, IsLongNotShort: false},
	}
	result, err := CalculatePortfolioMargin(positions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.GrossMarginInMinorUnits != 117000 {
		t.Fatalf("expected gross margin 117000, got %d", result.GrossMarginInMinorUnits)
	}
	if result.TotalNettingBenefitInMinorUnits != 44200 {
		t.Fatalf("expected netting benefit 44200, got %d", result.TotalNettingBenefitInMinorUnits)
	}
	if result.NetPortfolioMarginInMinorUnits != 72800 {
		t.Fatalf("expected net portfolio margin 72800, got %d", result.NetPortfolioMarginInMinorUnits)
	}
	if len(result.NettingBenefits) != 1 {
		t.Fatalf("expected exactly 1 netting benefit entry, got %d", len(result.NettingBenefits))
	}
}

func TestCalculatePortfolioMargin_SameDirectionNoBenefit(t *testing.T) {
	// Both LONG -- no offsetting relationship assumed, zero netting.
	positions := []Position{
		{AssetClass: AssetClassEquity, ContractNotionalValueInMinorUnits: 500000, IsLongNotShort: true},
		{AssetClass: AssetClassIndexFutures, ContractNotionalValueInMinorUnits: 400000, IsLongNotShort: true},
	}
	result, err := CalculatePortfolioMargin(positions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalNettingBenefitInMinorUnits != 0 {
		t.Fatalf("expected zero netting benefit for same-direction positions, got %d", result.TotalNettingBenefitInMinorUnits)
	}
	if result.NetPortfolioMarginInMinorUnits != result.GrossMarginInMinorUnits {
		t.Fatalf("expected net margin == gross margin with no netting, got net=%d gross=%d", result.NetPortfolioMarginInMinorUnits, result.GrossMarginInMinorUnits)
	}
}

func TestCalculatePortfolioMargin_SameAssetClassNoBenefit(t *testing.T) {
	positions := []Position{
		{AssetClass: AssetClassEquity, ContractNotionalValueInMinorUnits: 500000, IsLongNotShort: true},
		{AssetClass: AssetClassEquity, ContractNotionalValueInMinorUnits: 400000, IsLongNotShort: false},
	}
	result, err := CalculatePortfolioMargin(positions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalNettingBenefitInMinorUnits != 0 {
		t.Fatalf("expected zero netting benefit within the same asset class, got %d", result.TotalNettingBenefitInMinorUnits)
	}
}

func TestCalculatePortfolioMargin_UncorrelatedPairNoBenefit(t *testing.T) {
	// COMMODITY/INTEREST_RATE correlation is 0.05 (low but positive) --
	// use a genuinely unlisted (zero-correlation-by-default) pair instead:
	// no entry exists for INDEX_OPTIONS/CURRENCY.
	positions := []Position{
		{AssetClass: AssetClassIndexOptions, ContractNotionalValueInMinorUnits: 500000, IsLongNotShort: true},
		{AssetClass: AssetClassCurrency, ContractNotionalValueInMinorUnits: 400000, IsLongNotShort: false},
	}
	result, err := CalculatePortfolioMargin(positions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalNettingBenefitInMinorUnits != 0 {
		t.Fatalf("expected zero netting benefit for an unlisted (zero-correlation) pair, got %d", result.TotalNettingBenefitInMinorUnits)
	}
}

func TestCalculatePortfolioMargin_FloorAtLargestStandaloneMargin(t *testing.T) {
	// Three highly-correlated offsetting legs could theoretically net
	// below the single largest leg's own standalone margin -- the floor
	// must prevent that.
	positions := []Position{
		{AssetClass: AssetClassIndexFutures, ContractNotionalValueInMinorUnits: 1000000, IsLongNotShort: true}, // margin 130000
		{AssetClass: AssetClassIndexOptions, ContractNotionalValueInMinorUnits: 10000, IsLongNotShort: false},  // margin 1300
		{AssetClass: AssetClassEquity, ContractNotionalValueInMinorUnits: 10000, IsLongNotShort: false},        // margin 1300
	}
	result, err := CalculatePortfolioMargin(positions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NetPortfolioMarginInMinorUnits < 130000 {
		t.Fatalf("expected net margin floored at largest standalone margin 130000, got %d", result.NetPortfolioMarginInMinorUnits)
	}
}

func TestCalculatePortfolioMargin_SinglePositionNoNetting(t *testing.T) {
	positions := []Position{
		{AssetClass: AssetClassEquity, ContractNotionalValueInMinorUnits: 500000, IsLongNotShort: true},
	}
	result, err := CalculatePortfolioMargin(positions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NetPortfolioMarginInMinorUnits != 65000 {
		t.Fatalf("expected net margin 65000 (13%% of 500000), got %d", result.NetPortfolioMarginInMinorUnits)
	}
	if len(result.NettingBenefits) != 0 {
		t.Fatalf("expected zero netting benefits for a single position")
	}
}

func TestCalculatePortfolioMargin_NoPositions(t *testing.T) {
	if _, err := CalculatePortfolioMargin(nil); !errors.Is(err, ErrNoPositions) {
		t.Fatalf("expected ErrNoPositions, got %v", err)
	}
}

func TestCalculatePortfolioMargin_NegativeNotionalRejected(t *testing.T) {
	positions := []Position{
		{AssetClass: AssetClassEquity, ContractNotionalValueInMinorUnits: -1, IsLongNotShort: true},
	}
	if _, err := CalculatePortfolioMargin(positions); !errors.Is(err, ErrPositionNotionalMustBeNonNegative) {
		t.Fatalf("expected ErrPositionNotionalMustBeNonNegative, got %v", err)
	}
}

func TestCalculatePortfolioMargin_UnknownAssetClassRejected(t *testing.T) {
	positions := []Position{
		{AssetClass: "NOT_A_CLASS", ContractNotionalValueInMinorUnits: 1000, IsLongNotShort: true},
	}
	if _, err := CalculatePortfolioMargin(positions); !errors.Is(err, ErrUnknownAssetClass) {
		t.Fatalf("expected ErrUnknownAssetClass, got %v", err)
	}
}

func TestCorrelationBetween_SymmetricRegardlessOfArgOrder(t *testing.T) {
	a := correlationBetween(AssetClassEquity, AssetClassIndexFutures)
	b := correlationBetween(AssetClassIndexFutures, AssetClassEquity)
	if a != b || a != 0.85 {
		t.Fatalf("expected symmetric correlation 0.85, got a=%v b=%v", a, b)
	}
}

func TestCorrelationBetween_UnlistedPairIsZero(t *testing.T) {
	if correlation := correlationBetween(AssetClassCommodity, AssetClassIndexOptions); correlation != 0 {
		t.Fatalf("expected zero correlation for an unlisted pair, got %v", correlation)
	}
}
