package papertrading

import (
	"errors"
	"testing"

	"mercurius/omsgateway/internal/orders"
)

func TestSimulateFractionalFill_LimitOrderFillsAtLimitPrice(t *testing.T) {
	milliQty := uint64(300) // 0.300 share
	fill, err := SimulateFractionalFill(orders.OrderSubmissionRequest{
		LimitPriceInMinorUnits: 10000,
		MilliShareQuantity:     &milliQty,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fill.ExecutedPriceInMinorUnits != 10000 {
		t.Errorf("expected price 10000, got %d", fill.ExecutedPriceInMinorUnits)
	}
	if fill.ExecutedMilliShareQuantity != 300 {
		t.Errorf("expected milli-share quantity 300, got %d", fill.ExecutedMilliShareQuantity)
	}
}

func TestSimulateFractionalFill_MissingMilliShareQuantityErrors(t *testing.T) {
	_, err := SimulateFractionalFill(orders.OrderSubmissionRequest{
		LimitPriceInMinorUnits: 10000,
	})
	if !errors.Is(err, ErrMissingMilliShareQuantity) {
		t.Errorf("expected ErrMissingMilliShareQuantity, got %v", err)
	}
}

func TestSimulateFractionalFill_LimitOrderRejectsNonPositivePrice(t *testing.T) {
	milliQty := uint64(300)
	_, err := SimulateFractionalFill(orders.OrderSubmissionRequest{
		LimitPriceInMinorUnits: 0,
		MilliShareQuantity:     &milliQty,
	})
	if !errors.Is(err, ErrLimitPriceMustBePositive) {
		t.Errorf("expected ErrLimitPriceMustBePositive, got %v", err)
	}
}

func TestSimulateFractionalFill_MarketOrderFillsAtReferencePrice(t *testing.T) {
	milliQty := uint64(750)
	referencePrice := int64(9950)
	fill, err := SimulateFractionalFill(orders.OrderSubmissionRequest{
		OrderIsMarketOrderNotLimit:            true,
		PaperMarketReferencePriceInMinorUnits: &referencePrice,
		MilliShareQuantity:                    &milliQty,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fill.ExecutedPriceInMinorUnits != 9950 {
		t.Errorf("expected price 9950, got %d", fill.ExecutedPriceInMinorUnits)
	}
	if fill.ExecutedMilliShareQuantity != 750 {
		t.Errorf("expected milli-share quantity 750, got %d", fill.ExecutedMilliShareQuantity)
	}
}

func TestSimulateFractionalFill_MarketOrderRejectsMissingReferencePrice(t *testing.T) {
	milliQty := uint64(750)
	_, err := SimulateFractionalFill(orders.OrderSubmissionRequest{
		OrderIsMarketOrderNotLimit: true,
		MilliShareQuantity:         &milliQty,
	})
	if !errors.Is(err, ErrMarketOrderRequiresReferencePrice) {
		t.Errorf("expected ErrMarketOrderRequiresReferencePrice, got %v", err)
	}
}

func TestSimulateFractionalFill_SubOneShareQuantityFillsExactly(t *testing.T) {
	milliQty := uint64(1) // 0.001 share -- smallest representable unit
	fill, err := SimulateFractionalFill(orders.OrderSubmissionRequest{
		LimitPriceInMinorUnits: 10000,
		MilliShareQuantity:     &milliQty,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fill.ExecutedMilliShareQuantity != 1 {
		t.Errorf("expected exactly 1 milli-share unit filled, got %d", fill.ExecutedMilliShareQuantity)
	}
}

func TestSimulateFractionalFill_LargeMilliShareQuantityFillsFully(t *testing.T) {
	milliQty := uint64(5_000_000) // 5000 whole shares
	fill, err := SimulateFractionalFill(orders.OrderSubmissionRequest{
		LimitPriceInMinorUnits: 100,
		MilliShareQuantity:     &milliQty,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fill.ExecutedMilliShareQuantity != 5_000_000 {
		t.Errorf("expected full fill of 5000000, got %d", fill.ExecutedMilliShareQuantity)
	}
}
