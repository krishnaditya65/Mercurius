package impactcostestimator

import (
	"errors"
	"math"
	"testing"
)

func sampleSnapshot() OrderBookDepthSnapshot {
	return OrderBookDepthSnapshot{
		InstrumentSymbol: "DEMO-EQ",
		BidLevels: []DepthLevel{
			{PriceInMinorUnits: 9900, Quantity: 10},
			{PriceInMinorUnits: 9800, Quantity: 20},
			{PriceInMinorUnits: 9700, Quantity: 30},
		},
		AskLevels: []DepthLevel{
			{PriceInMinorUnits: 10000, Quantity: 10},
			{PriceInMinorUnits: 10100, Quantity: 20},
			{PriceInMinorUnits: 10200, Quantity: 30},
		},
	}
}

func floatsClose(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestEstimateImpactCost_BuySingleLevelExactMatch(t *testing.T) {
	estimate, err := EstimateImpactCost(sampleSnapshot(), true, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if estimate.QuantityFillable != 10 {
		t.Fatalf("expected quantityFillable 10, got %d", estimate.QuantityFillable)
	}
	if !floatsClose(estimate.AverageFillPriceInMinorUnits, 10000) {
		t.Fatalf("expected average fill price 10000, got %v", estimate.AverageFillPriceInMinorUnits)
	}
	if !floatsClose(estimate.SlippageInMinorUnits, 0) {
		t.Fatalf("expected zero slippage for an exact best-level fill, got %v", estimate.SlippageInMinorUnits)
	}
	if estimate.LevelsWalked != 1 {
		t.Fatalf("expected 1 level walked, got %d", estimate.LevelsWalked)
	}
}

func TestEstimateImpactCost_BuyWalksMultipleLevels(t *testing.T) {
	// 10 @ 10000 + 20 @ 10100 = 100000 + 202000 = 302000 / 30 = 10066.666...
	estimate, err := EstimateImpactCost(sampleSnapshot(), true, 30)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedAverage := 302000.0 / 30.0
	if !floatsClose(estimate.AverageFillPriceInMinorUnits, expectedAverage) {
		t.Fatalf("expected average fill price %v, got %v", expectedAverage, estimate.AverageFillPriceInMinorUnits)
	}
	if estimate.LevelsWalked != 2 {
		t.Fatalf("expected 2 levels walked, got %d", estimate.LevelsWalked)
	}
	expectedSlippage := expectedAverage - 10000
	if !floatsClose(estimate.SlippageInMinorUnits, expectedSlippage) {
		t.Fatalf("expected slippage %v, got %v", expectedSlippage, estimate.SlippageInMinorUnits)
	}
	if estimate.DepthInsufficientForFullSize {
		t.Fatalf("expected sufficient depth for 30 units (exactly available)")
	}
}

func TestEstimateImpactCost_SellWalksBidsDescending(t *testing.T) {
	// sell 15: 10 @ 9900 + 5 @ 9800 = 99000 + 49000 = 148000 / 15 = 9866.666...
	estimate, err := EstimateImpactCost(sampleSnapshot(), false, 15)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedAverage := 148000.0 / 15.0
	if !floatsClose(estimate.AverageFillPriceInMinorUnits, expectedAverage) {
		t.Fatalf("expected average %v, got %v", expectedAverage, estimate.AverageFillPriceInMinorUnits)
	}
	expectedSlippage := 9900.0 - expectedAverage
	if !floatsClose(estimate.SlippageInMinorUnits, expectedSlippage) {
		t.Fatalf("expected slippage %v, got %v", expectedSlippage, estimate.SlippageInMinorUnits)
	}
	if estimate.SlippageInMinorUnits <= 0 {
		t.Fatalf("expected positive slippage walking down the bid book, got %v", estimate.SlippageInMinorUnits)
	}
}

func TestEstimateImpactCost_DepthInsufficientForFullSize(t *testing.T) {
	// total ask depth is 10+20+30=60; ask for 100
	estimate, err := EstimateImpactCost(sampleSnapshot(), true, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !estimate.DepthInsufficientForFullSize {
		t.Fatalf("expected depth insufficient flag set")
	}
	if estimate.QuantityFillable != 60 {
		t.Fatalf("expected quantityFillable capped at total depth 60, got %d", estimate.QuantityFillable)
	}
	if estimate.LevelsWalked != 3 {
		t.Fatalf("expected all 3 levels walked, got %d", estimate.LevelsWalked)
	}
}

func TestEstimateImpactCost_ZeroQuantityRejected(t *testing.T) {
	_, err := EstimateImpactCost(sampleSnapshot(), true, 0)
	if !errors.Is(err, ErrZeroHypotheticalQuantity) {
		t.Fatalf("expected ErrZeroHypotheticalQuantity, got %v", err)
	}
}

func TestEstimateImpactCost_EmptyInstrumentSymbolRejected(t *testing.T) {
	snapshot := sampleSnapshot()
	snapshot.InstrumentSymbol = ""
	_, err := EstimateImpactCost(snapshot, true, 5)
	if !errors.Is(err, ErrEmptyInstrumentSymbol) {
		t.Fatalf("expected ErrEmptyInstrumentSymbol, got %v", err)
	}
}

func TestEstimateImpactCost_NoAskDepthRejected(t *testing.T) {
	snapshot := sampleSnapshot()
	snapshot.AskLevels = nil
	_, err := EstimateImpactCost(snapshot, true, 5)
	if !errors.Is(err, ErrNoDepthOnRelevantSide) {
		t.Fatalf("expected ErrNoDepthOnRelevantSide, got %v", err)
	}
}

func TestEstimateImpactCost_NoBidDepthRejectedForSell(t *testing.T) {
	snapshot := sampleSnapshot()
	snapshot.BidLevels = nil
	_, err := EstimateImpactCost(snapshot, false, 5)
	if !errors.Is(err, ErrNoDepthOnRelevantSide) {
		t.Fatalf("expected ErrNoDepthOnRelevantSide, got %v", err)
	}
}

func TestEstimateImpactCost_NonPositiveLevelPriceRejected(t *testing.T) {
	snapshot := sampleSnapshot()
	snapshot.AskLevels[0].PriceInMinorUnits = 0
	_, err := EstimateImpactCost(snapshot, true, 5)
	if !errors.Is(err, ErrNonPositiveLevelPrice) {
		t.Fatalf("expected ErrNonPositiveLevelPrice, got %v", err)
	}
}

func TestEstimateImpactCost_ZeroLevelQuantityRejected(t *testing.T) {
	snapshot := sampleSnapshot()
	snapshot.AskLevels[0].Quantity = 0
	_, err := EstimateImpactCost(snapshot, true, 5)
	if !errors.Is(err, ErrZeroLevelQuantity) {
		t.Fatalf("expected ErrZeroLevelQuantity, got %v", err)
	}
}

func TestEstimateImpactCost_AskLevelsNotAscendingRejected(t *testing.T) {
	snapshot := sampleSnapshot()
	snapshot.AskLevels = []DepthLevel{
		{PriceInMinorUnits: 10100, Quantity: 10},
		{PriceInMinorUnits: 10000, Quantity: 10},
	}
	_, err := EstimateImpactCost(snapshot, true, 5)
	if !errors.Is(err, ErrAskLevelsNotAscending) {
		t.Fatalf("expected ErrAskLevelsNotAscending, got %v", err)
	}
}

func TestEstimateImpactCost_BidLevelsNotDescendingRejected(t *testing.T) {
	snapshot := sampleSnapshot()
	snapshot.BidLevels = []DepthLevel{
		{PriceInMinorUnits: 9800, Quantity: 10},
		{PriceInMinorUnits: 9900, Quantity: 10},
	}
	_, err := EstimateImpactCost(snapshot, false, 5)
	if !errors.Is(err, ErrBidLevelsNotDescending) {
		t.Fatalf("expected ErrBidLevelsNotDescending, got %v", err)
	}
}

func TestEstimateImpactCost_SingleLevelBookNoSlippage(t *testing.T) {
	snapshot := OrderBookDepthSnapshot{
		InstrumentSymbol: "DEMO-EQ",
		AskLevels:        []DepthLevel{{PriceInMinorUnits: 5000, Quantity: 1000}},
	}
	estimate, err := EstimateImpactCost(snapshot, true, 500)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if estimate.SlippageInMinorUnits != 0 {
		t.Fatalf("expected zero slippage on a single flat level, got %v", estimate.SlippageInMinorUnits)
	}
	if estimate.SlippagePercent != 0 {
		t.Fatalf("expected zero slippage percent, got %v", estimate.SlippagePercent)
	}
}
