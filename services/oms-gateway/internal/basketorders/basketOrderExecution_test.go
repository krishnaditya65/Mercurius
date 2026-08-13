package basketorders

import (
	"errors"
	"sync"
	"testing"
)

func quantityModeRequest() BasketOrderRequest {
	return BasketOrderRequest{
		BasketIdentifier:              "basket-1",
		ClientAccountIdentifier:       "acct-001",
		NetCashConstraintInMinorUnits: 1_000_000,
		Constituents: []Constituent{
			{InstrumentSymbol: "AAA", IsBuyNotSell: true, Quantity: 10, ReferencePriceInMinorUnits: 10000},
			{InstrumentSymbol: "BBB", IsBuyNotSell: true, Quantity: 5, ReferencePriceInMinorUnits: 20000},
		},
	}
}

func TestValidateAndResolveBasket_QuantityMode(t *testing.T) {
	resolved, netCash, err := ValidateAndResolveBasket(quantityModeRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 10*10000 + 5*20000 = 100000 + 100000 = 200000
	if netCash != 200000 {
		t.Fatalf("expected netCash 200000, got %d", netCash)
	}
	if len(resolved) != 2 {
		t.Fatalf("expected 2 resolved constituents, got %d", len(resolved))
	}
	if resolved[0].resolvedQuantity != 10 || resolved[1].resolvedQuantity != 5 {
		t.Fatalf("expected explicit quantities preserved, got %+v", resolved)
	}
}

func TestValidateAndResolveBasket_NetCashConstraintExceeded(t *testing.T) {
	request := quantityModeRequest()
	request.NetCashConstraintInMinorUnits = 100000 // less than the real 200000 net cash
	_, _, err := ValidateAndResolveBasket(request)
	if !errors.Is(err, ErrNetCashConstraintExceeded) {
		t.Fatalf("expected ErrNetCashConstraintExceeded, got %v", err)
	}
}

func TestValidateAndResolveBasket_NetCashExactAtLimitAllowed(t *testing.T) {
	request := quantityModeRequest()
	request.NetCashConstraintInMinorUnits = 200000 // exactly the real net cash
	_, netCash, err := ValidateAndResolveBasket(request)
	if err != nil {
		t.Fatalf("unexpected error at exact limit: %v", err)
	}
	if netCash != 200000 {
		t.Fatalf("expected netCash 200000, got %d", netCash)
	}
}

func TestValidateAndResolveBasket_BuyAndSellNetOffsettingCash(t *testing.T) {
	request := BasketOrderRequest{
		BasketIdentifier:              "basket-2",
		ClientAccountIdentifier:       "acct-001",
		NetCashConstraintInMinorUnits: 50000,
		Constituents: []Constituent{
			{InstrumentSymbol: "AAA", IsBuyNotSell: true, Quantity: 10, ReferencePriceInMinorUnits: 10000}, // +100000
			{InstrumentSymbol: "BBB", IsBuyNotSell: false, Quantity: 5, ReferencePriceInMinorUnits: 10000}, // -50000
		},
	}
	_, netCash, err := ValidateAndResolveBasket(request)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if netCash != 50000 {
		t.Fatalf("expected netCash 50000 (100000-50000), got %d", netCash)
	}
}

func TestValidateAndResolveBasket_WeightMode(t *testing.T) {
	request := BasketOrderRequest{
		BasketIdentifier:              "basket-3",
		ClientAccountIdentifier:       "acct-001",
		NetCashConstraintInMinorUnits: 100000,
		Constituents: []Constituent{
			{InstrumentSymbol: "AAA", IsBuyNotSell: true, WeightPercent: 60, ReferencePriceInMinorUnits: 100},
			{InstrumentSymbol: "BBB", IsBuyNotSell: true, WeightPercent: 40, ReferencePriceInMinorUnits: 200},
		},
	}
	resolved, netCash, err := ValidateAndResolveBasket(request)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// AAA: 60% of 100000 = 60000 / 100 = 600 shares -> notional 60000
	// BBB: 40% of 100000 = 40000 / 200 = 200 shares -> notional 40000
	if resolved[0].resolvedQuantity != 600 {
		t.Fatalf("expected AAA quantity 600, got %d", resolved[0].resolvedQuantity)
	}
	if resolved[1].resolvedQuantity != 200 {
		t.Fatalf("expected BBB quantity 200, got %d", resolved[1].resolvedQuantity)
	}
	if netCash != 100000 {
		t.Fatalf("expected netCash 100000, got %d", netCash)
	}
}

func TestValidateAndResolveBasket_WeightResolvingToZeroRejected(t *testing.T) {
	request := BasketOrderRequest{
		BasketIdentifier:              "basket-4",
		ClientAccountIdentifier:       "acct-001",
		NetCashConstraintInMinorUnits: 100,
		Constituents: []Constituent{
			{InstrumentSymbol: "AAA", IsBuyNotSell: true, WeightPercent: 1, ReferencePriceInMinorUnits: 100000},
		},
	}
	_, _, err := ValidateAndResolveBasket(request)
	if !errors.Is(err, ErrZeroQuantityOrWeight) {
		t.Fatalf("expected ErrZeroQuantityOrWeight, got %v", err)
	}
}

func TestValidateAndResolveBasket_MixedModesRejected(t *testing.T) {
	request := BasketOrderRequest{
		BasketIdentifier:              "basket-5",
		ClientAccountIdentifier:       "acct-001",
		NetCashConstraintInMinorUnits: 100000,
		Constituents: []Constituent{
			{InstrumentSymbol: "AAA", IsBuyNotSell: true, Quantity: 10, ReferencePriceInMinorUnits: 100},
			{InstrumentSymbol: "BBB", IsBuyNotSell: true, WeightPercent: 50, ReferencePriceInMinorUnits: 100},
		},
	}
	_, _, err := ValidateAndResolveBasket(request)
	if !errors.Is(err, ErrMixedQuantityModes) {
		t.Fatalf("expected ErrMixedQuantityModes, got %v", err)
	}
}

func TestValidateAndResolveBasket_EmptyBasketIdentifier(t *testing.T) {
	request := quantityModeRequest()
	request.BasketIdentifier = ""
	if _, _, err := ValidateAndResolveBasket(request); !errors.Is(err, ErrEmptyBasketIdentifier) {
		t.Fatalf("expected ErrEmptyBasketIdentifier, got %v", err)
	}
}

func TestValidateAndResolveBasket_EmptyClientAccountIdentifier(t *testing.T) {
	request := quantityModeRequest()
	request.ClientAccountIdentifier = ""
	if _, _, err := ValidateAndResolveBasket(request); !errors.Is(err, ErrEmptyClientAccountIdentifier) {
		t.Fatalf("expected ErrEmptyClientAccountIdentifier, got %v", err)
	}
}

func TestValidateAndResolveBasket_NoConstituents(t *testing.T) {
	request := quantityModeRequest()
	request.Constituents = nil
	if _, _, err := ValidateAndResolveBasket(request); !errors.Is(err, ErrNoConstituents) {
		t.Fatalf("expected ErrNoConstituents, got %v", err)
	}
}

func TestValidateAndResolveBasket_NonPositiveReferencePrice(t *testing.T) {
	request := quantityModeRequest()
	request.Constituents[0].ReferencePriceInMinorUnits = 0
	if _, _, err := ValidateAndResolveBasket(request); !errors.Is(err, ErrNonPositiveReferencePrice) {
		t.Fatalf("expected ErrNonPositiveReferencePrice, got %v", err)
	}
}

func TestValidateAndResolveBasket_EmptyInstrumentSymbol(t *testing.T) {
	request := quantityModeRequest()
	request.Constituents[0].InstrumentSymbol = ""
	if _, _, err := ValidateAndResolveBasket(request); !errors.Is(err, ErrEmptyInstrumentSymbol) {
		t.Fatalf("expected ErrEmptyInstrumentSymbol, got %v", err)
	}
}

// --- ExecuteBasket tests ---

func TestExecuteBasket_AllAccepted(t *testing.T) {
	submitFunc := func(symbol string, isBuy bool, quantity uint64) (bool, uint64, string, error) {
		return true, quantity, "", nil
	}
	result, err := ExecuteBasket(quantityModeRequest(), submitFunc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.AggregateStatus != AggregateStatusAllAccepted {
		t.Fatalf("expected ALL_ACCEPTED, got %s", result.AggregateStatus)
	}
	if len(result.ConstituentOutcomes) != 2 {
		t.Fatalf("expected 2 outcomes, got %d", len(result.ConstituentOutcomes))
	}
}

func TestExecuteBasket_PartiallyAccepted(t *testing.T) {
	callCount := 0
	submitFunc := func(symbol string, isBuy bool, quantity uint64) (bool, uint64, string, error) {
		callCount++
		if callCount == 1 {
			return true, quantity, "", nil
		}
		return false, 0, "INSUFFICIENT_MARGIN", nil
	}
	result, err := ExecuteBasket(quantityModeRequest(), submitFunc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.AggregateStatus != AggregateStatusPartialAccepted {
		t.Fatalf("expected PARTIALLY_ACCEPTED, got %s", result.AggregateStatus)
	}
	if result.ConstituentOutcomes[1].RejectionReason != "INSUFFICIENT_MARGIN" {
		t.Fatalf("expected rejection reason propagated, got %+v", result.ConstituentOutcomes[1])
	}
}

func TestExecuteBasket_NoneAccepted(t *testing.T) {
	submitFunc := func(symbol string, isBuy bool, quantity uint64) (bool, uint64, string, error) {
		return false, 0, "KYC_NOT_VERIFIED", nil
	}
	result, err := ExecuteBasket(quantityModeRequest(), submitFunc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.AggregateStatus != AggregateStatusNoneAccepted {
		t.Fatalf("expected NONE_ACCEPTED, got %s", result.AggregateStatus)
	}
}

func TestExecuteBasket_SubmitsEveryConstituentEvenAfterAnEarlierRejection(t *testing.T) {
	// Deliberately NOT atomic: unlike multilegoptions, a rejection does
	// NOT stop later constituents from being submitted.
	var submittedSymbols []string
	submitFunc := func(symbol string, isBuy bool, quantity uint64) (bool, uint64, string, error) {
		submittedSymbols = append(submittedSymbols, symbol)
		return symbol != "AAA", quantity, "REJECTED", nil
	}
	_, err := ExecuteBasket(quantityModeRequest(), submitFunc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(submittedSymbols) != 2 {
		t.Fatalf("expected both constituents submitted regardless of the first's rejection, got %v", submittedSymbols)
	}
}

func TestExecuteBasket_ConstraintBreachRejectsWholesaleWithZeroSubmissions(t *testing.T) {
	callCount := 0
	submitFunc := func(symbol string, isBuy bool, quantity uint64) (bool, uint64, string, error) {
		callCount++
		return true, quantity, "", nil
	}
	request := quantityModeRequest()
	request.NetCashConstraintInMinorUnits = 1 // far below the real 200000 net cash
	_, err := ExecuteBasket(request, submitFunc)
	if !errors.Is(err, ErrNetCashConstraintExceeded) {
		t.Fatalf("expected ErrNetCashConstraintExceeded, got %v", err)
	}
	if callCount != 0 {
		t.Fatalf("expected zero submissions for a constraint-breaching basket, got %d", callCount)
	}
}

func TestExecuteBasket_NilSubmitFunc(t *testing.T) {
	_, err := ExecuteBasket(quantityModeRequest(), nil)
	if err == nil {
		t.Fatalf("expected error for nil submitConstituent func")
	}
}

func TestExecuteBasket_FilledQuantityTracked(t *testing.T) {
	submitFunc := func(symbol string, isBuy bool, quantity uint64) (bool, uint64, string, error) {
		// simulate a partial fill: only half the requested quantity fills
		return true, quantity / 2, "", nil
	}
	result, err := ExecuteBasket(quantityModeRequest(), submitFunc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ConstituentOutcomes[0].FilledQuantity != 5 { // 10/2
		t.Fatalf("expected filled quantity 5, got %d", result.ConstituentOutcomes[0].FilledQuantity)
	}
}

func TestExecuteBasket_Concurrency(t *testing.T) {
	var waitGroup sync.WaitGroup
	for i := 0; i < 50; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			submitFunc := func(symbol string, isBuy bool, quantity uint64) (bool, uint64, string, error) {
				return true, quantity, "", nil
			}
			result, err := ExecuteBasket(quantityModeRequest(), submitFunc)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if result.AggregateStatus != AggregateStatusAllAccepted {
				t.Errorf("expected ALL_ACCEPTED, got %s", result.AggregateStatus)
			}
		}()
	}
	waitGroup.Wait()
}
