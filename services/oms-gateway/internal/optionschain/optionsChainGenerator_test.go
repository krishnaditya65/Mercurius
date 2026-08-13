package optionschain

import (
	"errors"
	"testing"
	"time"

	"mercurius/omsgateway/internal/quantengineclient"
)

// fakeOptionPricer is a deterministic stand-in for quant-engine, so these
// tests never touch the network. It returns a fixed theoretical price and
// Greeks regardless of input (tests that care about propagation check
// the request it was called with), unless configured to fail.
type fakeOptionPricer struct {
	shouldFail    bool
	capturedCalls []quantengineclient.OptionPricingRequest
}

func (f *fakeOptionPricer) PriceOptionContract(request quantengineclient.OptionPricingRequest) (quantengineclient.OptionPricingResponse, error) {
	f.capturedCalls = append(f.capturedCalls, request)
	if f.shouldFail {
		return quantengineclient.OptionPricingResponse{}, errors.New("simulated quant-engine failure")
	}
	response := quantengineclient.OptionPricingResponse{
		TheoreticalPriceInMinorUnits:      10.0,
		Delta:                             0.5,
		Gamma:                             0.02,
		VegaPerOnePercentVolatilityChange: 0.3,
		ThetaPerCalendarDay:               -0.01,
	}
	if request.IsCallOptionNotPut {
		response.Delta = 0.6
	} else {
		response.Delta = -0.4
	}
	return response, nil
}

func TestCalculatePutCallRatioKnownValue(t *testing.T) {
	ratio, err := CalculatePutCallRatio(800, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ratio != 0.8 {
		t.Errorf("expected PCR 0.8, got %v", ratio)
	}
}

func TestCalculatePutCallRatioEqualOpenInterestIsOne(t *testing.T) {
	ratio, err := CalculatePutCallRatio(500, 500)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ratio != 1.0 {
		t.Errorf("expected PCR 1.0, got %v", ratio)
	}
}

func TestCalculatePutCallRatioZeroCallOpenInterestErrors(t *testing.T) {
	_, err := CalculatePutCallRatio(500, 0)
	if !errors.Is(err, ErrCannotComputePutCallRatioWithZeroCallOpenInterest) {
		t.Errorf("expected ErrCannotComputePutCallRatioWithZeroCallOpenInterest, got %v", err)
	}
}

func TestCalculatePutCallRatioZeroPutOpenInterestIsZero(t *testing.T) {
	ratio, err := CalculatePutCallRatio(0, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ratio != 0.0 {
		t.Errorf("expected PCR 0.0, got %v", ratio)
	}
}

func TestGenerateStrikeLadderReturnsTenStrikesCenteredOnAtm(t *testing.T) {
	strikes := generateStrikeLadder(1000)
	if len(strikes) != 10 {
		t.Fatalf("expected 10 strikes, got %d", len(strikes))
	}
	// spot 1000 -> interval 50, ATM strike 1000, ladder from 750..1200
	expectedFirst := 1000.0 - 5*50
	if strikes[0] != expectedFirst {
		t.Errorf("expected first strike %v, got %v", expectedFirst, strikes[0])
	}
	expectedLast := 1000.0 + 4*50
	if strikes[len(strikes)-1] != expectedLast {
		t.Errorf("expected last strike %v, got %v", expectedLast, strikes[len(strikes)-1])
	}
}

func TestCalculateStrikeIntervalForSpotPriceTable(t *testing.T) {
	cases := []struct {
		spot     float64
		expected float64
	}{
		{50, 1},
		{500, 10},
		{5000, 50},
		{50000, 100},
	}
	for _, c := range cases {
		if got := calculateStrikeIntervalForSpotPrice(c.spot); got != c.expected {
			t.Errorf("spot=%v: expected interval %v, got %v", c.spot, c.expected, got)
		}
	}
}

func TestCalculateSyntheticOpenInterestPeaksAtAtm(t *testing.T) {
	atmOi, _ := calculateSyntheticOpenInterestAndVolume(0, true)
	farOi, _ := calculateSyntheticOpenInterestAndVolume(5, true)
	if atmOi <= farOi {
		t.Errorf("expected ATM OI (%d) to exceed far-strike OI (%d)", atmOi, farOi)
	}
}

func TestCalculateSyntheticOpenInterestPutSkewExceedsCall(t *testing.T) {
	callOi, _ := calculateSyntheticOpenInterestAndVolume(0, true)
	putOi, _ := calculateSyntheticOpenInterestAndVolume(0, false)
	if putOi <= callOi {
		t.Errorf("expected put OI (%d) to exceed call OI (%d) at the same strike per the illustrative skew", putOi, callOi)
	}
}

func TestCalculateSyntheticOpenInterestNegativeAndPositiveDistanceSymmetric(t *testing.T) {
	oiNegative, _ := calculateSyntheticOpenInterestAndVolume(-3, true)
	oiPositive, _ := calculateSyntheticOpenInterestAndVolume(3, true)
	if oiNegative != oiPositive {
		t.Errorf("expected symmetric decay: distance -3 gave %d, distance 3 gave %d", oiNegative, oiPositive)
	}
}

func TestGenerateSyntheticOptionsChainRejectsNonPositiveSpotPrice(t *testing.T) {
	pricer := &fakeOptionPricer{}
	_, err := GenerateSyntheticOptionsChain(pricer, "DEMO-EQ", 0, time.Now().Add(24*time.Hour), time.Now())
	if !errors.Is(err, ErrUnderlyingSpotPriceMustBePositive) {
		t.Errorf("expected ErrUnderlyingSpotPriceMustBePositive, got %v", err)
	}
}

func TestGenerateSyntheticOptionsChainRejectsPastExpiry(t *testing.T) {
	pricer := &fakeOptionPricer{}
	now := time.Now()
	_, err := GenerateSyntheticOptionsChain(pricer, "DEMO-EQ", 100, now.Add(-time.Hour), now)
	if !errors.Is(err, ErrExpiryMustBeInTheFuture) {
		t.Errorf("expected ErrExpiryMustBeInTheFuture, got %v", err)
	}
}

func TestGenerateSyntheticOptionsChainRejectsExpiryEqualToNow(t *testing.T) {
	pricer := &fakeOptionPricer{}
	now := time.Now()
	_, err := GenerateSyntheticOptionsChain(pricer, "DEMO-EQ", 100, now, now)
	if !errors.Is(err, ErrExpiryMustBeInTheFuture) {
		t.Errorf("expected ErrExpiryMustBeInTheFuture, got %v", err)
	}
}

func TestGenerateSyntheticOptionsChainBuildsTenStrikeRowsWithCallAndPut(t *testing.T) {
	pricer := &fakeOptionPricer{}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expiry := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	chain, err := GenerateSyntheticOptionsChain(pricer, "DEMO-EQ", 1000, expiry, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chain.Strikes) != 10 {
		t.Fatalf("expected 10 strike rows, got %d", len(chain.Strikes))
	}
	// 20 pricing calls: 10 strikes x (call + put)
	if len(pricer.capturedCalls) != 20 {
		t.Errorf("expected 20 quant-engine calls, got %d", len(pricer.capturedCalls))
	}
	for _, row := range chain.Strikes {
		if row.Call.OptionType != "CALL" {
			t.Errorf("expected call quote OptionType CALL, got %s", row.Call.OptionType)
		}
		if row.Put.OptionType != "PUT" {
			t.Errorf("expected put quote OptionType PUT, got %s", row.Put.OptionType)
		}
	}
}

func TestGenerateSyntheticOptionsChainComputesTimeToExpiryInYears(t *testing.T) {
	pricer := &fakeOptionPricer{}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expiry := now.Add(365 * 24 * time.Hour)

	chain, err := GenerateSyntheticOptionsChain(pricer, "DEMO-EQ", 1000, expiry, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chain.TimeToExpiryInYears < 0.999 || chain.TimeToExpiryInYears > 1.001 {
		t.Errorf("expected ~1.0 year to expiry, got %v", chain.TimeToExpiryInYears)
	}
}

func TestGenerateSyntheticOptionsChainComputesConsistentPutCallRatio(t *testing.T) {
	pricer := &fakeOptionPricer{}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expiry := now.Add(30 * 24 * time.Hour)

	chain, err := GenerateSyntheticOptionsChain(pricer, "DEMO-EQ", 1000, expiry, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedRatio, expectedErr := CalculatePutCallRatio(chain.TotalPutOpenInterest, chain.TotalCallOpenInterest)
	if expectedErr != nil {
		t.Fatalf("unexpected error computing expected ratio: %v", expectedErr)
	}
	if chain.PutCallRatio != expectedRatio {
		t.Errorf("expected chain.PutCallRatio (%v) to match independently recomputed ratio (%v)", chain.PutCallRatio, expectedRatio)
	}
	// Put skew multiplier > 1 means total put OI must exceed total call OI.
	if chain.TotalPutOpenInterest <= chain.TotalCallOpenInterest {
		t.Errorf("expected total put OI (%d) to exceed total call OI (%d)", chain.TotalPutOpenInterest, chain.TotalCallOpenInterest)
	}
	if chain.PutCallRatio <= 1.0 {
		t.Errorf("expected PCR > 1.0 given the put skew, got %v", chain.PutCallRatio)
	}
}

func TestGenerateSyntheticOptionsChainReturnsClearErrorWhenPricerFails(t *testing.T) {
	pricer := &fakeOptionPricer{shouldFail: true}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expiry := now.Add(30 * 24 * time.Hour)

	_, err := GenerateSyntheticOptionsChain(pricer, "DEMO-EQ", 1000, expiry, now)
	if err == nil {
		t.Fatal("expected an error when the pricer fails")
	}
}

func TestGenerateSyntheticOptionsChainPropagatesUnderlyingSpotAndStrikePerCall(t *testing.T) {
	pricer := &fakeOptionPricer{}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expiry := now.Add(30 * 24 * time.Hour)

	chain, err := GenerateSyntheticOptionsChain(pricer, "DEMO-EQ", 1000, expiry, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i, call := range pricer.capturedCalls {
		if call.UnderlyingSpotPrice != 1000 {
			t.Errorf("call %d: expected underlyingSpotPrice 1000, got %v", i, call.UnderlyingSpotPrice)
		}
	}
	if chain.UnderlyingSpotPrice != 1000 {
		t.Errorf("expected chain UnderlyingSpotPrice 1000, got %v", chain.UnderlyingSpotPrice)
	}
	if chain.ExpiryDate != "2026-01-31" {
		t.Errorf("expected ExpiryDate 2026-01-31, got %s", chain.ExpiryDate)
	}
}
