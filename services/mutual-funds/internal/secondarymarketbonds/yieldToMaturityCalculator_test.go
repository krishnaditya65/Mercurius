package secondarymarketbonds

import (
	"math"
	"testing"
	"time"
)

func mustParseDate(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, parseError := time.Parse("2006-01-02", value)
	if parseError != nil {
		t.Fatalf("bad fixture date %q: %v", value, parseError)
	}
	return parsed
}

// TestCalculateYieldToMaturityParBondEqualsCouponRate is a hand-worked case
// derived from an independent mathematical identity (NOT from running this
// package's own solver in reverse): for ANY coupon-bearing bond, if the
// price exactly equals face value (a "par bond"), the yield to maturity is
// ALWAYS exactly the coupon rate — because at y = couponRate every
// discounted coupon-plus-principal cash flow sums back to exactly face
// value, independent of n or paymentsPerYear. This is bond-math 101, not
// this package's implementation, so recovering it end-to-end is a genuine
// correctness check.
func TestCalculateYieldToMaturityParBondEqualsCouponRate(t *testing.T) {
	asOf := mustParseDate(t, "2026-08-14")
	maturity := mustParseDate(t, "2036-08-14") // exactly 10 years out

	ytm, periods, err := CalculateYieldToMaturity(100000, 7.10, 2, 100000, maturity, asOf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if periods != 20 {
		t.Fatalf("expected 20 semi-annual periods over 10 years, got %d", periods)
	}
	if math.Abs(ytm-7.10) > 1e-6 {
		t.Fatalf("expected YTM exactly 7.10%% for a par bond, got %v", ytm)
	}
}

// TestCalculateYieldToMaturityZeroCouponClosedForm is a hand-worked,
// EXACT case using the closed-form zero-coupon identity
// price = faceValue / (1+y)^years, computed independently of the
// package's Newton-Raphson path: face 1000, price 826.4463..., 2 years
// -> 1000/826.4463 = 1.21 = 1.10^2, so y = 10% exactly.
func TestCalculateYieldToMaturityZeroCouponClosedForm(t *testing.T) {
	asOf := mustParseDate(t, "2026-08-14")
	maturity := mustParseDate(t, "2028-08-13") // ~2 years (accounts for leap day in range)

	faceValue := int64(100000000)                // 1000.00 in minor units (paise-of-rupee style, x100000)
	price := int64(math.Round(100000000 / 1.21)) // 826446281 minor units == 8264.4628...

	ytm, _, err := CalculateYieldToMaturity(faceValue, 0, 0, price, maturity, asOf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if math.Abs(ytm-10.0) > 0.05 {
		t.Fatalf("expected YTM ~10%% for the closed-form zero-coupon case, got %v", ytm)
	}
}

// TestCalculateYieldToMaturityZeroCouponOneYearExact avoids the
// years-to-maturity day-count approximation entirely by using EXACTLY one
// 365.25-day "year" of elapsed time, so the closed-form
// y = faceValue/price - 1 for n=1 holds to very high precision:
// face 1000, price 900 -> y = 1000/900 - 1 = 0.11111... = 11.1111%.
func TestCalculateYieldToMaturityZeroCouponOneYearExact(t *testing.T) {
	asOf := mustParseDate(t, "2026-08-14")
	maturity := asOf.Add(time.Duration(365.25*24) * time.Hour)

	ytm, _, err := CalculateYieldToMaturity(1000, 0, 0, 900, maturity, asOf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := (1000.0/900.0 - 1) * 100
	if math.Abs(ytm-expected) > 1e-4 {
		t.Fatalf("expected YTM %.6f%%, got %.6f%%", expected, ytm)
	}
}

// TestCalculateYieldToMaturityRoundTripsThroughPriceGivenYield is a
// broader (not hand-worked, but real) round-trip check: price a
// coupon-bearing bond at a known target yield using the independent
// pricing formula, then recover that same yield via Newton-Raphson.
func TestCalculateYieldToMaturityRoundTripsThroughPriceGivenYield(t *testing.T) {
	asOf := mustParseDate(t, "2026-08-14")
	maturity := mustParseDate(t, "2031-08-14") // 5 years, 10 semi-annual periods

	targetYield := 8.25
	price := PriceGivenYield(100000, 7.10, 2, 10, targetYield)

	recoveredYield, periods, err := CalculateYieldToMaturity(100000, 7.10, 2, price, maturity, asOf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if periods != 10 {
		t.Fatalf("expected 10 periods, got %d", periods)
	}
	if math.Abs(recoveredYield-targetYield) > 1e-4 {
		t.Fatalf("expected to recover yield %.6f%%, got %.6f%%", targetYield, recoveredYield)
	}
}

func TestCalculateYieldToMaturityDiscountBondHasHigherYieldThanCoupon(t *testing.T) {
	asOf := mustParseDate(t, "2026-08-14")
	maturity := mustParseDate(t, "2036-08-14")

	// Priced BELOW par -> YTM must exceed the coupon rate.
	ytm, _, err := CalculateYieldToMaturity(100000, 7.10, 2, 95000, maturity, asOf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ytm <= 7.10 {
		t.Fatalf("expected discount-bond YTM > coupon rate 7.10%%, got %v", ytm)
	}
}

func TestCalculateYieldToMaturityPremiumBondHasLowerYieldThanCoupon(t *testing.T) {
	asOf := mustParseDate(t, "2026-08-14")
	maturity := mustParseDate(t, "2036-08-14")

	// Priced ABOVE par -> YTM must be below the coupon rate.
	ytm, _, err := CalculateYieldToMaturity(100000, 7.10, 2, 105000, maturity, asOf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ytm >= 7.10 {
		t.Fatalf("expected premium-bond YTM < coupon rate 7.10%%, got %v", ytm)
	}
}

func TestCalculateYieldToMaturityRejectsNonPositivePrice(t *testing.T) {
	asOf := mustParseDate(t, "2026-08-14")
	maturity := mustParseDate(t, "2036-08-14")
	if _, _, err := CalculateYieldToMaturity(100000, 7.10, 2, 0, maturity, asOf); err != ErrInvalidPrice {
		t.Fatalf("expected ErrInvalidPrice, got %v", err)
	}
}

func TestCalculateYieldToMaturityRejectsNonPositiveFaceValue(t *testing.T) {
	asOf := mustParseDate(t, "2026-08-14")
	maturity := mustParseDate(t, "2036-08-14")
	if _, _, err := CalculateYieldToMaturity(0, 7.10, 2, 95000, maturity, asOf); err != ErrInvalidFaceValue {
		t.Fatalf("expected ErrInvalidFaceValue, got %v", err)
	}
}

func TestCalculateYieldToMaturityRejectsMaturityNotAfterAsOf(t *testing.T) {
	asOf := mustParseDate(t, "2026-08-14")
	if _, _, err := CalculateYieldToMaturity(100000, 7.10, 2, 95000, asOf, asOf); err != ErrInvalidMaturity {
		t.Fatalf("expected ErrInvalidMaturity, got %v", err)
	}
	pastMaturity := mustParseDate(t, "2020-01-01")
	if _, _, err := CalculateYieldToMaturity(100000, 7.10, 2, 95000, pastMaturity, asOf); err != ErrInvalidMaturity {
		t.Fatalf("expected ErrInvalidMaturity for a past maturity, got %v", err)
	}
}

func TestCalculateYieldToMaturityRejectsZeroPaymentsPerYearForCouponBond(t *testing.T) {
	asOf := mustParseDate(t, "2026-08-14")
	maturity := mustParseDate(t, "2036-08-14")
	if _, _, err := CalculateYieldToMaturity(100000, 7.10, 0, 95000, maturity, asOf); err != ErrInvalidPaymentsPerYear {
		t.Fatalf("expected ErrInvalidPaymentsPerYear, got %v", err)
	}
}
