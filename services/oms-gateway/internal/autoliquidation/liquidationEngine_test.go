package autoliquidation

import (
	"errors"
	"sync"
	"testing"
)

func alwaysAcceptSubmit(callLog *[]string) SubmitReducingOrderFunc {
	return func(accountId string, symbol string, quantity int64) (bool, error) {
		*callLog = append(*callLog, accountId+":"+symbol)
		return true, nil
	}
}

func TestNewLiquidationEngine_RequiresSubmitCallback(t *testing.T) {
	_, err := NewLiquidationEngine(DefaultThresholds(), nil)
	if err != ErrSubmitReducingOrderRequired {
		t.Fatalf("expected ErrSubmitReducingOrderRequired, got %v", err)
	}
}

func TestClassifyUtilization_AllFourStates(t *testing.T) {
	thresholds := DefaultThresholds() // warning 80, urgent 90
	cases := []struct {
		utilization float64
		expected    RiskState
	}{
		{0, RiskStateNormal},
		{79.9, RiskStateNormal},
		{80.0, RiskStateWarning},
		{89.9, RiskStateWarning},
		{90.0, RiskStateUrgent},
		{99.9, RiskStateUrgent},
		{100.0, RiskStateLiquidation},
		{150.0, RiskStateLiquidation},
	}
	for _, testCase := range cases {
		got := ClassifyUtilization(testCase.utilization, thresholds)
		if got != testCase.expected {
			t.Errorf("utilization %.1f: expected %s, got %s", testCase.utilization, testCase.expected, got)
		}
	}
}

func TestEvaluateAndLiquidateIfBreached_WarningDoesNotLiquidate(t *testing.T) {
	var calls []string
	engine, _ := NewLiquidationEngine(DefaultThresholds(), alwaysAcceptSubmit(&calls))

	snapshot := AccountLeverageSnapshot{
		ClientAccountIdentifier:          "acct-1",
		OutstandingPrincipalInMinorUnits: 80000,
		PledgedMarginValueInMinorUnits:   100000, // 80% utilization -> WARNING
		Positions: []PositionForLiquidation{
			{InstrumentSymbol: "DEMO-EQ", NetQuantity: 100, CurrentMarketPriceInMinorUnits: 1000, MarketPriceIsKnown: true},
		},
	}

	outcome := engine.EvaluateAndLiquidateIfBreached(snapshot)
	if outcome.RiskState != RiskStateWarning {
		t.Fatalf("expected WARNING, got %s", outcome.RiskState)
	}
	if len(calls) != 0 || len(outcome.SubmittedReducingOrders) != 0 {
		t.Fatalf("expected NO liquidation orders at WARNING, got calls=%v orders=%v", calls, outcome.SubmittedReducingOrders)
	}
}

func TestEvaluateAndLiquidateIfBreached_UrgentDoesNotLiquidate(t *testing.T) {
	var calls []string
	engine, _ := NewLiquidationEngine(DefaultThresholds(), alwaysAcceptSubmit(&calls))

	snapshot := AccountLeverageSnapshot{
		ClientAccountIdentifier:          "acct-1",
		OutstandingPrincipalInMinorUnits: 95000,
		PledgedMarginValueInMinorUnits:   100000, // 95% -> URGENT
		Positions: []PositionForLiquidation{
			{InstrumentSymbol: "DEMO-EQ", NetQuantity: 100, CurrentMarketPriceInMinorUnits: 1000, MarketPriceIsKnown: true},
		},
	}

	outcome := engine.EvaluateAndLiquidateIfBreached(snapshot)
	if outcome.RiskState != RiskStateUrgent {
		t.Fatalf("expected URGENT, got %s", outcome.RiskState)
	}
	if len(calls) != 0 {
		t.Fatalf("expected NO liquidation orders at URGENT, got %v", calls)
	}
}

func TestEvaluateAndLiquidateIfBreached_NormalDoesNothing(t *testing.T) {
	var calls []string
	engine, _ := NewLiquidationEngine(DefaultThresholds(), alwaysAcceptSubmit(&calls))

	snapshot := AccountLeverageSnapshot{
		ClientAccountIdentifier:          "acct-1",
		OutstandingPrincipalInMinorUnits: 10000,
		PledgedMarginValueInMinorUnits:   100000, // 10% -> NORMAL
	}
	outcome := engine.EvaluateAndLiquidateIfBreached(snapshot)
	if outcome.RiskState != RiskStateNormal {
		t.Fatalf("expected NORMAL, got %s", outcome.RiskState)
	}
	if len(calls) != 0 {
		t.Fatalf("expected no calls, got %v", calls)
	}
}

// TestEvaluateAndLiquidateIfBreached_HandWorkedLiquidationSizing is the
// hand-worked case: outstanding 120,000 against pledged 100,000 ->
// 120% utilization -> LIQUIDATION. Target is 75% of 100,000 = 75,000.
// Shortfall = 120,000 - 75,000 = 45,000. One position: 100 shares @ 1,000
// each (100,000 notional). ceil(45,000 / 1,000) = 45 shares needed to
// sell -> sells exactly 45 shares, notional 45,000, fully covering the
// shortfall (remaining shortfall = 0).
func TestEvaluateAndLiquidateIfBreached_HandWorkedLiquidationSizing(t *testing.T) {
	var calls []string
	engine, _ := NewLiquidationEngine(DefaultThresholds(), alwaysAcceptSubmit(&calls))

	snapshot := AccountLeverageSnapshot{
		ClientAccountIdentifier:          "acct-1",
		OutstandingPrincipalInMinorUnits: 120000,
		PledgedMarginValueInMinorUnits:   100000,
		Positions: []PositionForLiquidation{
			{InstrumentSymbol: "DEMO-EQ", NetQuantity: 100, CurrentMarketPriceInMinorUnits: 1000, MarketPriceIsKnown: true},
		},
	}

	outcome := engine.EvaluateAndLiquidateIfBreached(snapshot)
	if outcome.RiskState != RiskStateLiquidation {
		t.Fatalf("expected LIQUIDATION, got %s", outcome.RiskState)
	}
	if len(outcome.SubmittedReducingOrders) != 1 {
		t.Fatalf("expected exactly 1 reducing order, got %d", len(outcome.SubmittedReducingOrders))
	}
	order := outcome.SubmittedReducingOrders[0]
	if order.QuantitySold != 45 {
		t.Fatalf("expected 45 shares sold, got %d", order.QuantitySold)
	}
	if order.NotionalReducedInMinorUnits != 45000 {
		t.Fatalf("expected 45000 notional reduced, got %d", order.NotionalReducedInMinorUnits)
	}
	if outcome.RemainingShortfallInMinorUnits != 0 {
		t.Fatalf("expected shortfall fully covered, got remaining %d", outcome.RemainingShortfallInMinorUnits)
	}
	if len(calls) != 1 {
		t.Fatalf("expected exactly one real submission call, got %v", calls)
	}
}

func TestEvaluateAndLiquidateIfBreached_LiquidatesLargestPositionFirst(t *testing.T) {
	var calls []string
	engine, _ := NewLiquidationEngine(DefaultThresholds(), alwaysAcceptSubmit(&calls))

	snapshot := AccountLeverageSnapshot{
		ClientAccountIdentifier:          "acct-1",
		OutstandingPrincipalInMinorUnits: 100000,
		PledgedMarginValueInMinorUnits:   90000, // ~111% -> LIQUIDATION, target 75% of 90000=67500, shortfall=32500
		Positions: []PositionForLiquidation{
			{InstrumentSymbol: "SMALL", NetQuantity: 10, CurrentMarketPriceInMinorUnits: 100, MarketPriceIsKnown: true},   // notional 1000
			{InstrumentSymbol: "LARGE", NetQuantity: 1000, CurrentMarketPriceInMinorUnits: 100, MarketPriceIsKnown: true}, // notional 100000
		},
	}

	outcome := engine.EvaluateAndLiquidateIfBreached(snapshot)
	if len(outcome.SubmittedReducingOrders) == 0 {
		t.Fatalf("expected at least one reducing order")
	}
	if outcome.SubmittedReducingOrders[0].InstrumentSymbol != "LARGE" {
		t.Fatalf("expected LARGE position liquidated first, got %s", outcome.SubmittedReducingOrders[0].InstrumentSymbol)
	}
}

func TestEvaluateAndLiquidateIfBreached_SkipsShortPositions(t *testing.T) {
	var calls []string
	engine, _ := NewLiquidationEngine(DefaultThresholds(), alwaysAcceptSubmit(&calls))

	snapshot := AccountLeverageSnapshot{
		ClientAccountIdentifier:          "acct-1",
		OutstandingPrincipalInMinorUnits: 120000,
		PledgedMarginValueInMinorUnits:   100000,
		Positions: []PositionForLiquidation{
			{InstrumentSymbol: "SHORT-POS", NetQuantity: -100, CurrentMarketPriceInMinorUnits: 1000, MarketPriceIsKnown: true},
		},
	}

	outcome := engine.EvaluateAndLiquidateIfBreached(snapshot)
	if len(outcome.SubmittedReducingOrders) != 0 {
		t.Fatalf("expected short positions never liquidated, got %v", outcome.SubmittedReducingOrders)
	}
	if outcome.RemainingShortfallInMinorUnits <= 0 {
		t.Fatalf("expected a remaining shortfall since nothing could be liquidated")
	}
}

func TestEvaluateAndLiquidateIfBreached_SkipsPositionsWithUnknownPrice(t *testing.T) {
	var calls []string
	engine, _ := NewLiquidationEngine(DefaultThresholds(), alwaysAcceptSubmit(&calls))

	snapshot := AccountLeverageSnapshot{
		ClientAccountIdentifier:          "acct-1",
		OutstandingPrincipalInMinorUnits: 120000,
		PledgedMarginValueInMinorUnits:   100000,
		Positions: []PositionForLiquidation{
			{InstrumentSymbol: "NO-PRICE", NetQuantity: 100, MarketPriceIsKnown: false},
		},
	}

	outcome := engine.EvaluateAndLiquidateIfBreached(snapshot)
	if len(outcome.SubmittedReducingOrders) != 0 {
		t.Fatalf("expected no orders for a position with unknown price, got %v", outcome.SubmittedReducingOrders)
	}
	if len(outcome.SkippedPositionsMissingPrice) != 1 || outcome.SkippedPositionsMissingPrice[0] != "NO-PRICE" {
		t.Fatalf("expected NO-PRICE reported skipped, got %v", outcome.SkippedPositionsMissingPrice)
	}
}

func TestEvaluateAndLiquidateIfBreached_PartialCoverageLeavesRemainingShortfall(t *testing.T) {
	var calls []string
	engine, _ := NewLiquidationEngine(DefaultThresholds(), alwaysAcceptSubmit(&calls))

	// shortfall will be large but the position is small: can't fully cover.
	snapshot := AccountLeverageSnapshot{
		ClientAccountIdentifier:          "acct-1",
		OutstandingPrincipalInMinorUnits: 500000,
		PledgedMarginValueInMinorUnits:   100000,
		Positions: []PositionForLiquidation{
			{InstrumentSymbol: "DEMO-EQ", NetQuantity: 5, CurrentMarketPriceInMinorUnits: 1000, MarketPriceIsKnown: true}, // only 5000 notional available
		},
	}

	outcome := engine.EvaluateAndLiquidateIfBreached(snapshot)
	if len(outcome.SubmittedReducingOrders) != 1 {
		t.Fatalf("expected one order selling the entire small position, got %v", outcome.SubmittedReducingOrders)
	}
	if outcome.SubmittedReducingOrders[0].QuantitySold != 5 {
		t.Fatalf("expected entire 5-share position sold, got %d", outcome.SubmittedReducingOrders[0].QuantitySold)
	}
	if outcome.RemainingShortfallInMinorUnits <= 0 {
		t.Fatalf("expected a remaining shortfall since the position couldn't cover it all")
	}
}

func TestEvaluateAndLiquidateIfBreached_RejectedSubmissionDoesNotReduceShortfall(t *testing.T) {
	rejectingSubmit := func(accountId string, symbol string, quantity int64) (bool, error) {
		return false, errors.New("simulated downstream rejection")
	}
	engine, _ := NewLiquidationEngine(DefaultThresholds(), rejectingSubmit)

	snapshot := AccountLeverageSnapshot{
		ClientAccountIdentifier:          "acct-1",
		OutstandingPrincipalInMinorUnits: 120000,
		PledgedMarginValueInMinorUnits:   100000,
		Positions: []PositionForLiquidation{
			{InstrumentSymbol: "DEMO-EQ", NetQuantity: 100, CurrentMarketPriceInMinorUnits: 1000, MarketPriceIsKnown: true},
		},
	}

	outcome := engine.EvaluateAndLiquidateIfBreached(snapshot)
	if len(outcome.SubmittedReducingOrders) != 1 {
		t.Fatalf("expected the attempt to be recorded even though rejected")
	}
	if outcome.SubmittedReducingOrders[0].WasAccepted {
		t.Fatalf("expected WasAccepted=false")
	}
	if outcome.SubmittedReducingOrders[0].SubmissionError == "" {
		t.Fatalf("expected a recorded submission error")
	}
	if outcome.RemainingShortfallInMinorUnits <= 0 {
		t.Fatalf("expected shortfall to remain uncovered since the order was rejected")
	}
}

func TestUtilizationPercent_ZeroPledgedZeroOutstandingIsZero(t *testing.T) {
	snapshot := AccountLeverageSnapshot{}
	if got := snapshot.UtilizationPercent(); got != 0.0 {
		t.Fatalf("expected 0.0, got %f", got)
	}
}

func TestUtilizationPercent_ZeroPledgedWithOutstandingIsMaximallyBreached(t *testing.T) {
	snapshot := AccountLeverageSnapshot{OutstandingPrincipalInMinorUnits: 1}
	if got := snapshot.UtilizationPercent(); got < 100.0 {
		t.Fatalf("expected a utilization well past 100, got %f", got)
	}
}

func TestConcurrentEvaluateAndLiquidateIfBreached_NoRace(t *testing.T) {
	var mutex sync.Mutex
	var totalCalls int
	submit := func(accountId string, symbol string, quantity int64) (bool, error) {
		mutex.Lock()
		totalCalls++
		mutex.Unlock()
		return true, nil
	}
	engine, _ := NewLiquidationEngine(DefaultThresholds(), submit)

	snapshot := AccountLeverageSnapshot{
		ClientAccountIdentifier:          "acct-1",
		OutstandingPrincipalInMinorUnits: 120000,
		PledgedMarginValueInMinorUnits:   100000,
		Positions: []PositionForLiquidation{
			{InstrumentSymbol: "DEMO-EQ", NetQuantity: 1000, CurrentMarketPriceInMinorUnits: 1000, MarketPriceIsKnown: true},
		},
	}

	var waitGroup sync.WaitGroup
	for i := 0; i < 20; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			engine.EvaluateAndLiquidateIfBreached(snapshot)
		}()
	}
	waitGroup.Wait()

	if totalCalls == 0 {
		t.Fatalf("expected at least some liquidation calls")
	}
}
