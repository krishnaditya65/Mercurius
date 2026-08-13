package payoffdiagram

import (
	"errors"
	"math"
	"testing"
)

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-6
}

func TestLongStraddleBreakevensAreStrikePlusMinusPremium(t *testing.T) {
	// Buy 1 call strike 100 @ premium 5, buy 1 put strike 100 @ premium 5.
	// Textbook: max loss == total premium (10), max profit unbounded,
	// breakevens == strike +/- total premium == 90 and 110.
	legs := []OptionLeg{
		{OptionType: OptionTypeCall, StrikePriceInMinorUnits: 100, PremiumInMinorUnits: 5, IsBuyNotSell: true, Quantity: 1},
		{OptionType: OptionTypePut, StrikePriceInMinorUnits: 100, PremiumInMinorUnits: 5, IsBuyNotSell: true, Quantity: 1},
	}

	result, err := ComputePayoffDiagram(legs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.MaxProfitIsUnbounded {
		t.Error("expected long straddle max profit to be unbounded")
	}
	if result.MaxLossIsUnbounded {
		t.Error("expected long straddle max loss to be bounded")
	}
	if !approxEqual(result.MaxLossInMinorUnits, 10) {
		t.Errorf("expected max loss 10, got %v", result.MaxLossInMinorUnits)
	}
	if len(result.BreakevenPricesInMinorUnits) != 2 {
		t.Fatalf("expected 2 breakevens, got %v", result.BreakevenPricesInMinorUnits)
	}
	if !approxEqual(result.BreakevenPricesInMinorUnits[0], 90) || !approxEqual(result.BreakevenPricesInMinorUnits[1], 110) {
		t.Errorf("expected breakevens [90, 110], got %v", result.BreakevenPricesInMinorUnits)
	}

	// Direct spot-price spot checks corroborate the diagram.
	atStrike, _ := PayoffAtSpotPrice(legs, 100)
	if !approxEqual(atStrike, -10) {
		t.Errorf("expected payoff at strike == -10 (both legs lose their premium), got %v", atStrike)
	}
	atLowerBreakeven, _ := PayoffAtSpotPrice(legs, 90)
	if !approxEqual(atLowerBreakeven, 0) {
		t.Errorf("expected payoff at breakeven 90 == 0, got %v", atLowerBreakeven)
	}
}

func TestLongStrangleBreakevensAndMaxLoss(t *testing.T) {
	// Buy 1 call strike 110 @ premium 3, buy 1 put strike 90 @ premium 3.
	// Textbook: max loss == total premium (6) for any spot in [90,110],
	// breakevens == putStrike - totalPremium (84) and callStrike +
	// totalPremium (116).
	legs := []OptionLeg{
		{OptionType: OptionTypeCall, StrikePriceInMinorUnits: 110, PremiumInMinorUnits: 3, IsBuyNotSell: true, Quantity: 1},
		{OptionType: OptionTypePut, StrikePriceInMinorUnits: 90, PremiumInMinorUnits: 3, IsBuyNotSell: true, Quantity: 1},
	}

	result, err := ComputePayoffDiagram(legs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.MaxProfitIsUnbounded {
		t.Error("expected long strangle max profit unbounded")
	}
	if !approxEqual(result.MaxLossInMinorUnits, 6) {
		t.Errorf("expected max loss 6, got %v", result.MaxLossInMinorUnits)
	}
	if len(result.BreakevenPricesInMinorUnits) != 2 {
		t.Fatalf("expected 2 breakevens, got %v", result.BreakevenPricesInMinorUnits)
	}
	if !approxEqual(result.BreakevenPricesInMinorUnits[0], 84) || !approxEqual(result.BreakevenPricesInMinorUnits[1], 116) {
		t.Errorf("expected breakevens [84, 116], got %v", result.BreakevenPricesInMinorUnits)
	}

	// The flat bottom between the two strikes should be constant at -6.
	between1, _ := PayoffAtSpotPrice(legs, 90)
	between2, _ := PayoffAtSpotPrice(legs, 110)
	if !approxEqual(between1, -6) || !approxEqual(between2, -6) {
		t.Errorf("expected flat -6 between strikes, got %v and %v", between1, between2)
	}
}

func TestBullCallSpreadBoundedProfitAndLoss(t *testing.T) {
	// Buy call strike 100 @ premium 8, sell call strike 110 @ premium 3.
	// Net debit == 5. Textbook: max profit == spread width - net debit ==
	// 10-5=5; max loss == net debit == 5; breakeven == lowerStrike +
	// netDebit == 105.
	legs := []OptionLeg{
		{OptionType: OptionTypeCall, StrikePriceInMinorUnits: 100, PremiumInMinorUnits: 8, IsBuyNotSell: true, Quantity: 1},
		{OptionType: OptionTypeCall, StrikePriceInMinorUnits: 110, PremiumInMinorUnits: 3, IsBuyNotSell: false, Quantity: 1},
	}

	result, err := ComputePayoffDiagram(legs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.MaxProfitIsUnbounded || result.MaxLossIsUnbounded {
		t.Fatalf("expected a fully bounded vertical spread, got %+v", result)
	}
	if !approxEqual(result.MaxProfitInMinorUnits, 5) {
		t.Errorf("expected max profit 5, got %v", result.MaxProfitInMinorUnits)
	}
	if !approxEqual(result.MaxLossInMinorUnits, 5) {
		t.Errorf("expected max loss 5, got %v", result.MaxLossInMinorUnits)
	}
	if len(result.BreakevenPricesInMinorUnits) != 1 || !approxEqual(result.BreakevenPricesInMinorUnits[0], 105) {
		t.Errorf("expected single breakeven at 105, got %v", result.BreakevenPricesInMinorUnits)
	}

	// Beyond the short strike, profit is capped flat at 5.
	farAbove, _ := PayoffAtSpotPrice(legs, 500)
	if !approxEqual(farAbove, 5) {
		t.Errorf("expected capped profit of 5 far above short strike, got %v", farAbove)
	}
}

func TestSingleLongCallUnboundedProfitBoundedLoss(t *testing.T) {
	legs := []OptionLeg{
		{OptionType: OptionTypeCall, StrikePriceInMinorUnits: 100, PremiumInMinorUnits: 4, IsBuyNotSell: true, Quantity: 2},
	}
	result, err := ComputePayoffDiagram(legs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.MaxProfitIsUnbounded {
		t.Error("expected unbounded max profit for a long call")
	}
	if result.MaxLossIsUnbounded {
		t.Error("expected bounded max loss for a long call")
	}
	// 2 contracts * premium 4 == 8 max loss.
	if !approxEqual(result.MaxLossInMinorUnits, 8) {
		t.Errorf("expected max loss 8, got %v", result.MaxLossInMinorUnits)
	}
	if len(result.BreakevenPricesInMinorUnits) != 1 || !approxEqual(result.BreakevenPricesInMinorUnits[0], 104) {
		t.Errorf("expected breakeven at strike+premium=104, got %v", result.BreakevenPricesInMinorUnits)
	}
}

func TestNakedShortCallUnboundedLoss(t *testing.T) {
	legs := []OptionLeg{
		{OptionType: OptionTypeCall, StrikePriceInMinorUnits: 100, PremiumInMinorUnits: 4, IsBuyNotSell: false, Quantity: 1},
	}
	result, err := ComputePayoffDiagram(legs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.MaxProfitIsUnbounded {
		t.Error("expected bounded max profit for a naked short call")
	}
	if !approxEqual(result.MaxProfitInMinorUnits, 4) {
		t.Errorf("expected max profit == premium collected (4), got %v", result.MaxProfitInMinorUnits)
	}
	if !result.MaxLossIsUnbounded {
		t.Error("expected unbounded max loss for a naked short call")
	}
}

func TestCashSecuredShortPutMaxLossBoundedAtSpotZero(t *testing.T) {
	// Sell 1 put strike 100 @ premium 6. Downside is bounded (not
	// unbounded) because spot can't go below 0: worst case is spot==0,
	// loss == strike - premium == 94.
	legs := []OptionLeg{
		{OptionType: OptionTypePut, StrikePriceInMinorUnits: 100, PremiumInMinorUnits: 6, IsBuyNotSell: false, Quantity: 1},
	}
	result, err := ComputePayoffDiagram(legs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.MaxLossIsUnbounded {
		t.Error("expected bounded max loss for a short put (floor at spot=0)")
	}
	if !approxEqual(result.MaxLossInMinorUnits, 94) {
		t.Errorf("expected max loss 94 (=strike-premium at spot=0), got %v", result.MaxLossInMinorUnits)
	}
	if !approxEqual(result.MaxProfitInMinorUnits, 6) {
		t.Errorf("expected max profit == premium (6), got %v", result.MaxProfitInMinorUnits)
	}
}

func TestComputePayoffDiagramValidation(t *testing.T) {
	if _, err := ComputePayoffDiagram(nil); !errors.Is(err, ErrNoLegs) {
		t.Errorf("expected ErrNoLegs, got %v", err)
	}
	if _, err := ComputePayoffDiagram([]OptionLeg{{OptionType: OptionTypeCall, Quantity: 0}}); !errors.Is(err, ErrZeroQuantity) {
		t.Errorf("expected ErrZeroQuantity, got %v", err)
	}
	if _, err := ComputePayoffDiagram([]OptionLeg{{OptionType: "BOGUS", Quantity: 1}}); !errors.Is(err, ErrUnknownOptionType) {
		t.Errorf("expected ErrUnknownOptionType, got %v", err)
	}
	if _, err := ComputePayoffDiagram([]OptionLeg{{OptionType: OptionTypeCall, StrikePriceInMinorUnits: -1, Quantity: 1}}); !errors.Is(err, ErrNegativeStrikePrice) {
		t.Errorf("expected ErrNegativeStrikePrice, got %v", err)
	}
	if _, err := ComputePayoffDiagram([]OptionLeg{{OptionType: OptionTypeCall, PremiumInMinorUnits: -1, Quantity: 1}}); !errors.Is(err, ErrNegativePremium) {
		t.Errorf("expected ErrNegativePremium, got %v", err)
	}
}

func TestPayoffAtSpotPriceValidatesLegsToo(t *testing.T) {
	if _, err := PayoffAtSpotPrice(nil, 100); !errors.Is(err, ErrNoLegs) {
		t.Errorf("expected ErrNoLegs, got %v", err)
	}
}
