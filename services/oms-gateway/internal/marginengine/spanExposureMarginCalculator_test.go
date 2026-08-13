package marginengine

import "testing"

// TestHandWorkedExampleFiveLakhNotional hand-computes every line item for
// a round-number contract notional against the package's own documented
// rate constants, mirroring chargescalculator's worked-example test
// pattern.
//
// Hand computation:
//
//	contract notional value = ₹5,00,000.00 = 50,000,000 paise
//	SPAN margin       = 10% of 50,000,000 = 5,000,000 paise
//	exposure margin   =  3% of 50,000,000 = 1,500,000 paise
//	total required    = 5,000,000 + 1,500,000 = 6,500,000 paise
func TestHandWorkedExampleFiveLakhNotional(t *testing.T) {
	requirement, err := CalculateSpanAndExposureMargin(50_000_000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requirement.ContractNotionalValueInMinorUnits != 50_000_000 {
		t.Fatalf("expected notional 50000000, got %d", requirement.ContractNotionalValueInMinorUnits)
	}
	if requirement.SpanMarginInMinorUnits != 5_000_000 {
		t.Fatalf("expected SPAN margin 5000000, got %d", requirement.SpanMarginInMinorUnits)
	}
	if requirement.ExposureMarginInMinorUnits != 1_500_000 {
		t.Fatalf("expected exposure margin 1500000, got %d", requirement.ExposureMarginInMinorUnits)
	}
	if requirement.TotalRequiredMarginInMinorUnits != 6_500_000 {
		t.Fatalf("expected total required margin 6500000, got %d", requirement.TotalRequiredMarginInMinorUnits)
	}
}

func TestZeroNotionalProducesZeroMargin(t *testing.T) {
	requirement, err := CalculateSpanAndExposureMargin(0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requirement.SpanMarginInMinorUnits != 0 || requirement.ExposureMarginInMinorUnits != 0 || requirement.TotalRequiredMarginInMinorUnits != 0 {
		t.Fatalf("expected all-zero margin for zero notional, got %+v", requirement)
	}
}

func TestNegativeNotionalReturnsSentinelError(t *testing.T) {
	_, err := CalculateSpanAndExposureMargin(-1)
	if err != ErrContractNotionalValueMustBeNonNegative {
		t.Fatalf("expected ErrContractNotionalValueMustBeNonNegative, got %v", err)
	}
}

func TestSpanMarginIsTenPercentOfNotional(t *testing.T) {
	requirement, err := CalculateSpanAndExposureMargin(1_000_000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requirement.SpanMarginInMinorUnits != 100_000 {
		t.Fatalf("expected SPAN margin 100000 (10%% of 1000000), got %d", requirement.SpanMarginInMinorUnits)
	}
}

func TestExposureMarginIsThreePercentOfNotional(t *testing.T) {
	requirement, err := CalculateSpanAndExposureMargin(1_000_000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requirement.ExposureMarginInMinorUnits != 30_000 {
		t.Fatalf("expected exposure margin 30000 (3%% of 1000000), got %d", requirement.ExposureMarginInMinorUnits)
	}
}

func TestTotalRequiredMarginIsSumOfSpanAndExposure(t *testing.T) {
	requirement, err := CalculateSpanAndExposureMargin(777_777)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requirement.TotalRequiredMarginInMinorUnits != requirement.SpanMarginInMinorUnits+requirement.ExposureMarginInMinorUnits {
		t.Fatalf("expected total to equal span+exposure, got total=%d span=%d exposure=%d",
			requirement.TotalRequiredMarginInMinorUnits, requirement.SpanMarginInMinorUnits, requirement.ExposureMarginInMinorUnits)
	}
}

// TestRoundingHalfUpForFractionalPaise: notional=333 paise.
// SPAN = 10% of 333 = 33.3 -> rounds to 33.
// exposure = 3% of 333 = 9.99 -> rounds to 10.
func TestRoundingHalfUpForFractionalPaise(t *testing.T) {
	requirement, err := CalculateSpanAndExposureMargin(333)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requirement.SpanMarginInMinorUnits != 33 {
		t.Fatalf("expected SPAN margin 33, got %d", requirement.SpanMarginInMinorUnits)
	}
	if requirement.ExposureMarginInMinorUnits != 10 {
		t.Fatalf("expected exposure margin 10, got %d", requirement.ExposureMarginInMinorUnits)
	}
}

func TestLargeNotionalDoesNotOverflowForReasonableRealWorldValues(t *testing.T) {
	// ₹10 crore notional = 10,00,00,000.00 rupees = 100,000,000,00 paise —
	// a large but entirely realistic single-position notional.
	requirement, err := CalculateSpanAndExposureMargin(10_000_000_000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requirement.TotalRequiredMarginInMinorUnits != 1_300_000_000 {
		t.Fatalf("expected total required margin 1300000000, got %d", requirement.TotalRequiredMarginInMinorUnits)
	}
}

func TestSmallNotionalRoundsSpanAndExposureIndependently(t *testing.T) {
	// notional = 7 paise: SPAN = 0.7 -> rounds to 1; exposure = 0.21 -> rounds to 0.
	requirement, err := CalculateSpanAndExposureMargin(7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requirement.SpanMarginInMinorUnits != 1 {
		t.Fatalf("expected SPAN margin 1, got %d", requirement.SpanMarginInMinorUnits)
	}
	if requirement.ExposureMarginInMinorUnits != 0 {
		t.Fatalf("expected exposure margin 0, got %d", requirement.ExposureMarginInMinorUnits)
	}
}

func TestReturnedNotionalEchoesInput(t *testing.T) {
	requirement, err := CalculateSpanAndExposureMargin(123_456)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requirement.ContractNotionalValueInMinorUnits != 123_456 {
		t.Fatalf("expected echoed notional 123456, got %d", requirement.ContractNotionalValueInMinorUnits)
	}
}

func TestConsecutiveCallsAreIndependentWithNoSharedState(t *testing.T) {
	first, err := CalculateSpanAndExposureMargin(1_000_000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, err := CalculateSpanAndExposureMargin(2_000_000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if first.TotalRequiredMarginInMinorUnits == second.TotalRequiredMarginInMinorUnits {
		t.Fatal("expected different notionals to produce different totals")
	}
	// Calling again with the first notional must reproduce the exact same
	// result — the calculator is pure/stateless.
	firstAgain, err := CalculateSpanAndExposureMargin(1_000_000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if firstAgain != first {
		t.Fatalf("expected a pure function to reproduce the same result, got %+v vs %+v", firstAgain, first)
	}
}
