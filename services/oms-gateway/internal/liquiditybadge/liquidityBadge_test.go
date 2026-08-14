package liquiditybadge

import (
	"testing"

	"mercurius/omsgateway/internal/impactcostestimator"
)

func testThresholds() Thresholds {
	return Thresholds{
		HighDepthQuantityThreshold:    1000,
		MediumDepthQuantityThreshold:  200,
		IllustrativeBaseSecondsHigh:   1.0,
		IllustrativeBaseSecondsMedium: 5.0,
		IllustrativeBaseSecondsLow:    30.0,
	}
}

func TestComputeLiquidityBadge_RejectsZeroQuantity(t *testing.T) {
	snapshot := impactcostestimator.OrderBookDepthSnapshot{InstrumentSymbol: "X"}
	_, err := ComputeLiquidityBadge(snapshot, true, 0, testThresholds())
	if err == nil {
		t.Fatalf("expected error for zero hypothetical quantity")
	}
}

func TestComputeLiquidityBadge_RejectsInvalidThresholds(t *testing.T) {
	snapshot := impactcostestimator.OrderBookDepthSnapshot{InstrumentSymbol: "X"}
	badThresholds := Thresholds{HighDepthQuantityThreshold: 100, MediumDepthQuantityThreshold: 200}
	_, err := ComputeLiquidityBadge(snapshot, true, 10, badThresholds)
	if err == nil {
		t.Fatalf("expected error for High <= Medium threshold")
	}
}

func TestComputeLiquidityBadge_HighClassification(t *testing.T) {
	snapshot := impactcostestimator.OrderBookDepthSnapshot{
		InstrumentSymbol: "X",
		AskLevels: []impactcostestimator.DepthLevel{
			{PriceInMinorUnits: 100, Quantity: 600},
			{PriceInMinorUnits: 101, Quantity: 500},
		},
	}
	badge, err := ComputeLiquidityBadge(snapshot, true, 10, testThresholds())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if badge.Classification != LiquidityHigh {
		t.Fatalf("expected HIGH, got %s", badge.Classification)
	}
	if badge.TotalRelevantSideDepthQuantity != 1100 {
		t.Fatalf("expected total depth 1100, got %d", badge.TotalRelevantSideDepthQuantity)
	}
}

func TestComputeLiquidityBadge_MediumClassification(t *testing.T) {
	snapshot := impactcostestimator.OrderBookDepthSnapshot{
		InstrumentSymbol: "X",
		AskLevels:        []impactcostestimator.DepthLevel{{PriceInMinorUnits: 100, Quantity: 500}},
	}
	badge, _ := ComputeLiquidityBadge(snapshot, true, 10, testThresholds())
	if badge.Classification != LiquidityMedium {
		t.Fatalf("expected MEDIUM, got %s", badge.Classification)
	}
}

func TestComputeLiquidityBadge_LowClassification(t *testing.T) {
	snapshot := impactcostestimator.OrderBookDepthSnapshot{
		InstrumentSymbol: "X",
		AskLevels:        []impactcostestimator.DepthLevel{{PriceInMinorUnits: 100, Quantity: 50}},
	}
	badge, _ := ComputeLiquidityBadge(snapshot, true, 10, testThresholds())
	if badge.Classification != LiquidityLow {
		t.Fatalf("expected LOW, got %s", badge.Classification)
	}
}

func TestComputeLiquidityBadge_ZeroDepthIsLow(t *testing.T) {
	snapshot := impactcostestimator.OrderBookDepthSnapshot{InstrumentSymbol: "X"}
	badge, _ := ComputeLiquidityBadge(snapshot, true, 10, testThresholds())
	if badge.Classification != LiquidityLow {
		t.Fatalf("expected LOW for zero depth, got %s", badge.Classification)
	}
	if badge.TotalRelevantSideDepthQuantity != 0 {
		t.Fatalf("expected 0 depth, got %d", badge.TotalRelevantSideDepthQuantity)
	}
}

func TestComputeLiquidityBadge_ExactlyAtHighBoundary(t *testing.T) {
	snapshot := impactcostestimator.OrderBookDepthSnapshot{
		InstrumentSymbol: "X",
		AskLevels:        []impactcostestimator.DepthLevel{{PriceInMinorUnits: 100, Quantity: 1000}},
	}
	badge, _ := ComputeLiquidityBadge(snapshot, true, 10, testThresholds())
	if badge.Classification != LiquidityHigh {
		t.Fatalf("expected exact boundary to classify HIGH, got %s", badge.Classification)
	}
}

func TestComputeLiquidityBadge_UsesBidsForSell(t *testing.T) {
	snapshot := impactcostestimator.OrderBookDepthSnapshot{
		InstrumentSymbol: "X",
		BidLevels:        []impactcostestimator.DepthLevel{{PriceInMinorUnits: 99, Quantity: 1500}},
		AskLevels:        []impactcostestimator.DepthLevel{{PriceInMinorUnits: 100, Quantity: 10}},
	}
	badge, _ := ComputeLiquidityBadge(snapshot, false, 10, testThresholds())
	if badge.Classification != LiquidityHigh {
		t.Fatalf("expected sell side to use bid depth (HIGH), got %s classification with depth %d", badge.Classification, badge.TotalRelevantSideDepthQuantity)
	}
}

func TestComputeLiquidityBadge_TimeToFillHandWorked_SmallOrderVsDeepBook(t *testing.T) {
	// HIGH classification, order is 1% of depth -> consumptionRatio
	// clamped to 1.0 (order is small relative to depth) -> estimated
	// seconds = baseSecondsHigh * 1.0 = 1.0.
	snapshot := impactcostestimator.OrderBookDepthSnapshot{
		InstrumentSymbol: "X",
		AskLevels:        []impactcostestimator.DepthLevel{{PriceInMinorUnits: 100, Quantity: 1000}},
	}
	badge, _ := ComputeLiquidityBadge(snapshot, true, 10, testThresholds())
	if badge.IllustrativeExpectedTimeToFillSeconds != 1.0 {
		t.Fatalf("expected 1.0s, got %v", badge.IllustrativeExpectedTimeToFillSeconds)
	}
}

func TestComputeLiquidityBadge_TimeToFillHandWorked_LargeOrderConsumingDepth(t *testing.T) {
	// LOW classification (depth=50, below Medium threshold 200), order
	// size 100 -> consumptionRatio = 100/50 = 2.0 -> estimated seconds =
	// baseSecondsLow(30) * 2.0 = 60.0.
	snapshot := impactcostestimator.OrderBookDepthSnapshot{
		InstrumentSymbol: "X",
		AskLevels:        []impactcostestimator.DepthLevel{{PriceInMinorUnits: 100, Quantity: 50}},
	}
	badge, _ := ComputeLiquidityBadge(snapshot, true, 100, testThresholds())
	if badge.IllustrativeExpectedTimeToFillSeconds != 60.0 {
		t.Fatalf("expected 60.0s, got %v", badge.IllustrativeExpectedTimeToFillSeconds)
	}
}

func TestComputeLiquidityBadge_IsIllustrativeEstimateAlwaysTrue(t *testing.T) {
	snapshot := impactcostestimator.OrderBookDepthSnapshot{
		InstrumentSymbol: "X",
		AskLevels:        []impactcostestimator.DepthLevel{{PriceInMinorUnits: 100, Quantity: 1000}},
	}
	badge, _ := ComputeLiquidityBadge(snapshot, true, 10, testThresholds())
	if !badge.IsIllustrativeEstimate {
		t.Fatalf("expected IsIllustrativeEstimate=true")
	}
}

func TestComputeLiquidityBadge_EmptyRelevantSideEvenWithOtherSideDeep(t *testing.T) {
	snapshot := impactcostestimator.OrderBookDepthSnapshot{
		InstrumentSymbol: "X",
		BidLevels:        []impactcostestimator.DepthLevel{{PriceInMinorUnits: 99, Quantity: 10000}},
	}
	badge, _ := ComputeLiquidityBadge(snapshot, true, 10, testThresholds())
	if badge.Classification != LiquidityLow || badge.TotalRelevantSideDepthQuantity != 0 {
		t.Fatalf("expected LOW/0 depth using ask side only for a buy, got %+v", badge)
	}
}

func TestComputeLiquidityBadge_ZeroDepthLargeOrderVeryLargeTimeToFill(t *testing.T) {
	snapshot := impactcostestimator.OrderBookDepthSnapshot{InstrumentSymbol: "X"}
	badge, _ := ComputeLiquidityBadge(snapshot, true, 500, testThresholds())
	// consumptionRatio = 500 (hypotheticalQuantity, since totalDepth==0)
	// -> estimated seconds = 30 * 500 = 15000.
	if badge.IllustrativeExpectedTimeToFillSeconds != 15000 {
		t.Fatalf("expected 15000s, got %v", badge.IllustrativeExpectedTimeToFillSeconds)
	}
}
