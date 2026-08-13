package papertrading

import (
	"errors"
	"testing"

	"mercurius/omsgateway/internal/orders"
)

func TestSimulateFillLimitOrderFillsAtSubmittedLimitPrice(t *testing.T) {
	fill, err := SimulateFill(orders.OrderSubmissionRequest{
		LimitPriceInMinorUnits: 10500,
		OrderQuantity:          25,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fill.ExecutedPriceInMinorUnits != 10500 {
		t.Errorf("expected executed price 10500, got %d", fill.ExecutedPriceInMinorUnits)
	}
	if fill.ExecutedQuantity != 25 {
		t.Errorf("expected executed quantity 25 (fully filled), got %d", fill.ExecutedQuantity)
	}
}

func TestSimulateFillLimitOrderRejectsNonPositivePrice(t *testing.T) {
	_, err := SimulateFill(orders.OrderSubmissionRequest{
		LimitPriceInMinorUnits: 0,
		OrderQuantity:          10,
	})
	if !errors.Is(err, ErrLimitPriceMustBePositive) {
		t.Errorf("expected ErrLimitPriceMustBePositive, got %v", err)
	}
}

func TestSimulateFillLimitOrderRejectsNegativePrice(t *testing.T) {
	_, err := SimulateFill(orders.OrderSubmissionRequest{
		LimitPriceInMinorUnits: -100,
		OrderQuantity:          10,
	})
	if !errors.Is(err, ErrLimitPriceMustBePositive) {
		t.Errorf("expected ErrLimitPriceMustBePositive, got %v", err)
	}
}

func TestSimulateFillMarketOrderFillsAtReferencePrice(t *testing.T) {
	referencePrice := int64(9950)
	fill, err := SimulateFill(orders.OrderSubmissionRequest{
		OrderIsMarketOrderNotLimit:            true,
		PaperMarketReferencePriceInMinorUnits: &referencePrice,
		OrderQuantity:                         40,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fill.ExecutedPriceInMinorUnits != 9950 {
		t.Errorf("expected executed price 9950, got %d", fill.ExecutedPriceInMinorUnits)
	}
	if fill.ExecutedQuantity != 40 {
		t.Errorf("expected executed quantity 40, got %d", fill.ExecutedQuantity)
	}
}

func TestSimulateFillMarketOrderRejectsMissingReferencePrice(t *testing.T) {
	_, err := SimulateFill(orders.OrderSubmissionRequest{
		OrderIsMarketOrderNotLimit: true,
		OrderQuantity:              10,
	})
	if !errors.Is(err, ErrMarketOrderRequiresReferencePrice) {
		t.Errorf("expected ErrMarketOrderRequiresReferencePrice, got %v", err)
	}
}

func TestSimulateFillMarketOrderRejectsZeroReferencePrice(t *testing.T) {
	zero := int64(0)
	_, err := SimulateFill(orders.OrderSubmissionRequest{
		OrderIsMarketOrderNotLimit:            true,
		PaperMarketReferencePriceInMinorUnits: &zero,
		OrderQuantity:                         10,
	})
	if !errors.Is(err, ErrMarketOrderRequiresReferencePrice) {
		t.Errorf("expected ErrMarketOrderRequiresReferencePrice for zero reference price, got %v", err)
	}
}

func TestSimulateFillMarketOrderRejectsNegativeReferencePrice(t *testing.T) {
	negative := int64(-10)
	_, err := SimulateFill(orders.OrderSubmissionRequest{
		OrderIsMarketOrderNotLimit:            true,
		PaperMarketReferencePriceInMinorUnits: &negative,
		OrderQuantity:                         10,
	})
	if !errors.Is(err, ErrMarketOrderRequiresReferencePrice) {
		t.Errorf("expected ErrMarketOrderRequiresReferencePrice for negative reference price, got %v", err)
	}
}

func TestSimulateFillIsAlwaysFullyFilledNeverPartial(t *testing.T) {
	fill, err := SimulateFill(orders.OrderSubmissionRequest{
		LimitPriceInMinorUnits: 100,
		OrderQuantity:          1_000_000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fill.ExecutedQuantity != 1_000_000 {
		t.Errorf("expected full fill of 1000000, got %d", fill.ExecutedQuantity)
	}
}

func TestSyntheticCounterpartyAccountIdentifierIsStable(t *testing.T) {
	if SyntheticCounterpartyAccountIdentifier != "paper-market-maker" {
		t.Errorf("unexpected synthetic counterparty identifier: %s", SyntheticCounterpartyAccountIdentifier)
	}
}
