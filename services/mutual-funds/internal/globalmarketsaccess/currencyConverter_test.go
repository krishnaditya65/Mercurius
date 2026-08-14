package globalmarketsaccess

import (
	"math"
	"testing"
)

func TestConvertSameCurrencyIsIdentity(t *testing.T) {
	converter := NewCurrencyConverter()
	converted, err := converter.Convert(100000, "USD", "USD")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if converted != 100000 {
		t.Fatalf("expected identity conversion, got %d", converted)
	}
}

func TestConvertInrToUsdHandWorked(t *testing.T) {
	converter := NewCurrencyConverter()
	// Seeded rate: 1 USD = 83.00 INR, so 100 USD (10000 minor units)
	// requires 8300 INR (830000 minor units) -> converting 830000 paise
	// (8300.00 INR) to USD should yield exactly 10000 cents (100.00 USD).
	converted, err := converter.Convert(830000, "INR", "USD")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if converted != 10000 {
		t.Fatalf("expected exactly 10000 (100.00 USD), got %d", converted)
	}
}

func TestConvertUsdToInrHandWorked(t *testing.T) {
	converter := NewCurrencyConverter()
	// 100.00 USD (10000 minor units) * 83.00 = 8300.00 INR (830000 minor
	// units).
	converted, err := converter.Convert(10000, "USD", "INR")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if converted != 830000 {
		t.Fatalf("expected exactly 830000 (8300.00 INR), got %d", converted)
	}
}

func TestConvertUnknownPairReturnsError(t *testing.T) {
	converter := NewCurrencyConverter()
	if _, err := converter.Convert(1000, "INR", "JPY"); err != ErrUnknownCurrencyPair {
		t.Fatalf("expected ErrUnknownCurrencyPair, got %v", err)
	}
}

func TestUpdateRateChangesFutureConversions(t *testing.T) {
	converter := NewCurrencyConverter()
	if err := converter.UpdateRate("INR", "USD", 0.02); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	converted, _ := converter.Convert(100000, "INR", "USD")
	if converted != 2000 {
		t.Fatalf("expected 2000, got %d", converted)
	}
}

func TestUpdateRateRejectsNonPositiveRate(t *testing.T) {
	converter := NewCurrencyConverter()
	if err := converter.UpdateRate("INR", "USD", 0); err != ErrInvalidFxRate {
		t.Fatalf("expected ErrInvalidFxRate, got %v", err)
	}
}

func TestRateRoundTripIsApproximatelyReciprocal(t *testing.T) {
	converter := NewCurrencyConverter()
	inrToUsd, _ := converter.Rate("INR", "USD")
	usdToInr, _ := converter.Rate("USD", "INR")
	product := inrToUsd * usdToInr
	if math.Abs(product-1.0) > 1e-9 {
		t.Fatalf("expected reciprocal rates to multiply to ~1.0, got %v", product)
	}
}
