package portfoliostresstest

import "testing"

func TestComputeStressTest_RejectsEmptyPositions(t *testing.T) {
	_, err := ComputeStressTest(nil, -0.10)
	if err == nil {
		t.Fatalf("expected error for empty positions")
	}
}

func TestComputeStressTest_RejectsUnknownPositionType(t *testing.T) {
	_, err := ComputeStressTest([]StressTestPositionInput{
		{InstrumentSymbol: "X", PositionType: "BOGUS"},
	}, -0.10)
	if err == nil {
		t.Fatalf("expected error for unknown position type")
	}
}

func TestComputeStressTest_SingleLongEquityHandWorked(t *testing.T) {
	// 100 shares @ ₹100.00 (10000 paise), -10% shock -> impact =
	// 100 * 10000 * -0.10 = -100000 paise = -₹1000.00.
	positions := []StressTestPositionInput{
		{InstrumentSymbol: "DEMO-EQ", PositionType: PositionTypeEquity, NetQuantity: 100, CurrentPriceInMinorUnits: 10000},
	}
	result, err := ComputeStressTest(positions, -0.10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalEstimatedPnLImpactInMinorUnits != -100000 {
		t.Fatalf("expected -100000, got %d", result.TotalEstimatedPnLImpactInMinorUnits)
	}
	if result.PerPositionImpacts[0].IsFirstOrderApproximation {
		t.Fatalf("equity impact must not be flagged as an approximation -- it's exact")
	}
}

func TestComputeStressTest_ShortEquityGainsOnDrop(t *testing.T) {
	// Short 100 shares @ ₹100.00, -10% shock -> impact = -100 * 10000 *
	// -0.10 = +100000 paise -- a short position PROFITS when price drops.
	positions := []StressTestPositionInput{
		{InstrumentSymbol: "DEMO-EQ", PositionType: PositionTypeEquity, NetQuantity: -100, CurrentPriceInMinorUnits: 10000},
	}
	result, _ := ComputeStressTest(positions, -0.10)
	if result.TotalEstimatedPnLImpactInMinorUnits != 100000 {
		t.Fatalf("expected +100000 for a short position on a down move, got %d", result.TotalEstimatedPnLImpactInMinorUnits)
	}
}

func TestComputeStressTest_SingleOptionDeltaApproxHandWorked(t *testing.T) {
	// 10 long contracts, multiplier 1, delta 0.5, underlying ₹100.00
	// (10000 paise), -10% shock:
	// underlyingPriceMove = 10000 * -0.10 = -1000
	// impact = 10 * 1 * 0.5 * -1000 = -5000 paise.
	positions := []StressTestPositionInput{
		{
			InstrumentSymbol: "DEMO-EQ-CALL", PositionType: PositionTypeOption,
			NetContracts: 10, ContractMultiplier: 1, DeltaPerContract: 0.5,
			UnderlyingCurrentPriceInMinorUnits: 10000,
		},
	}
	result, err := ComputeStressTest(positions, -0.10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalEstimatedPnLImpactInMinorUnits != -5000 {
		t.Fatalf("expected -5000, got %d", result.TotalEstimatedPnLImpactInMinorUnits)
	}
	if !result.PerPositionImpacts[0].IsFirstOrderApproximation {
		t.Fatalf("option impact must be flagged as a first-order approximation")
	}
}

func TestComputeStressTest_ShortPutOptionNegativeDeltaGainsOnDrop(t *testing.T) {
	// Short 10 puts (short = negative NetContracts by convention here),
	// with the put's OWN delta already negative (-0.4) per the package
	// doc's "trust the caller's signed delta" contract. Underlying
	// ₹100.00, -10% shock:
	// underlyingPriceMove = -1000
	// impact = (-10) * 1 * (-0.4) * (-1000) = -4000
	positions := []StressTestPositionInput{
		{
			InstrumentSymbol: "DEMO-EQ-PUT", PositionType: PositionTypeOption,
			NetContracts: -10, ContractMultiplier: 1, DeltaPerContract: -0.4,
			UnderlyingCurrentPriceInMinorUnits: 10000,
		},
	}
	result, _ := ComputeStressTest(positions, -0.10)
	if result.TotalEstimatedPnLImpactInMinorUnits != -4000 {
		t.Fatalf("expected -4000, got %d", result.TotalEstimatedPnLImpactInMinorUnits)
	}
}

func TestComputeStressTest_ContractMultiplierScalesImpact(t *testing.T) {
	positions := []StressTestPositionInput{
		{
			InstrumentSymbol: "NIFTY-CALL", PositionType: PositionTypeOption,
			NetContracts: 1, ContractMultiplier: 50, DeltaPerContract: 0.5,
			UnderlyingCurrentPriceInMinorUnits: 10000,
		},
	}
	result, _ := ComputeStressTest(positions, -0.10)
	// 1 * 50 * 0.5 * (10000*-0.10) = 50*0.5*-1000 = -25000
	if result.TotalEstimatedPnLImpactInMinorUnits != -25000 {
		t.Fatalf("expected -25000, got %d", result.TotalEstimatedPnLImpactInMinorUnits)
	}
}

func TestComputeStressTest_MixedPortfolioSumsCorrectly(t *testing.T) {
	positions := []StressTestPositionInput{
		{InstrumentSymbol: "DEMO-EQ", PositionType: PositionTypeEquity, NetQuantity: 100, CurrentPriceInMinorUnits: 10000},
		{
			InstrumentSymbol: "DEMO-EQ-CALL", PositionType: PositionTypeOption,
			NetContracts: 10, ContractMultiplier: 1, DeltaPerContract: 0.5,
			UnderlyingCurrentPriceInMinorUnits: 10000,
		},
	}
	result, _ := ComputeStressTest(positions, -0.10)
	expected := int64(-100000) + int64(-5000)
	if result.TotalEstimatedPnLImpactInMinorUnits != expected {
		t.Fatalf("expected %d, got %d", expected, result.TotalEstimatedPnLImpactInMinorUnits)
	}
	if len(result.PerPositionImpacts) != 2 {
		t.Fatalf("expected 2 per-position impacts, got %d", len(result.PerPositionImpacts))
	}
}

func TestComputeStressTest_PositiveShockGainsOnLongEquity(t *testing.T) {
	positions := []StressTestPositionInput{
		{InstrumentSymbol: "DEMO-EQ", PositionType: PositionTypeEquity, NetQuantity: 100, CurrentPriceInMinorUnits: 10000},
	}
	result, _ := ComputeStressTest(positions, 0.10)
	if result.TotalEstimatedPnLImpactInMinorUnits != 100000 {
		t.Fatalf("expected +100000 for a +10%% shock on a long position, got %d", result.TotalEstimatedPnLImpactInMinorUnits)
	}
}

func TestComputeStressTest_ZeroShockZeroImpact(t *testing.T) {
	positions := []StressTestPositionInput{
		{InstrumentSymbol: "DEMO-EQ", PositionType: PositionTypeEquity, NetQuantity: 100, CurrentPriceInMinorUnits: 10000},
	}
	result, _ := ComputeStressTest(positions, 0)
	if result.TotalEstimatedPnLImpactInMinorUnits != 0 {
		t.Fatalf("expected 0 impact for a 0%% shock, got %d", result.TotalEstimatedPnLImpactInMinorUnits)
	}
}

func TestComputeStressTest_ZeroDeltaOptionZeroImpact(t *testing.T) {
	positions := []StressTestPositionInput{
		{
			InstrumentSymbol: "DEEP-OTM-CALL", PositionType: PositionTypeOption,
			NetContracts: 10, ContractMultiplier: 1, DeltaPerContract: 0,
			UnderlyingCurrentPriceInMinorUnits: 10000,
		},
	}
	result, _ := ComputeStressTest(positions, -0.10)
	if result.TotalEstimatedPnLImpactInMinorUnits != 0 {
		t.Fatalf("expected 0 impact for zero-delta option, got %d", result.TotalEstimatedPnLImpactInMinorUnits)
	}
}

func TestComputeStressTest_ShockPercentEchoedInResult(t *testing.T) {
	positions := []StressTestPositionInput{
		{InstrumentSymbol: "DEMO-EQ", PositionType: PositionTypeEquity, NetQuantity: 1, CurrentPriceInMinorUnits: 100},
	}
	result, _ := ComputeStressTest(positions, -0.10)
	if result.ShockPercent != -0.10 {
		t.Fatalf("expected shockPercent echoed back, got %v", result.ShockPercent)
	}
}
