package fractionalshares

import (
	"errors"
	"math"
	"testing"
)

func TestValidateMilliShareQuantity_NilIsAlwaysValid(t *testing.T) {
	if err := ValidateMilliShareQuantity(nil, false); err != nil {
		t.Fatalf("expected nil to always be valid, got %v", err)
	}
	if err := ValidateMilliShareQuantity(nil, true); err != nil {
		t.Fatalf("expected nil to always be valid, got %v", err)
	}
}

func TestValidateMilliShareQuantity_ZeroRejected(t *testing.T) {
	zero := uint64(0)
	err := ValidateMilliShareQuantity(&zero, true)
	if !errors.Is(err, ErrMilliShareQuantityMustBePositive) {
		t.Fatalf("expected ErrMilliShareQuantityMustBePositive, got %v", err)
	}
}

func TestValidateMilliShareQuantity_NonPaperOrderRejected(t *testing.T) {
	quantity := uint64(500)
	err := ValidateMilliShareQuantity(&quantity, false)
	if !errors.Is(err, ErrFractionalSharesOnlySupportedForPaperTrading) {
		t.Fatalf("expected ErrFractionalSharesOnlySupportedForPaperTrading, got %v", err)
	}
}

func TestValidateMilliShareQuantity_PaperOrderAccepted(t *testing.T) {
	quantity := uint64(500)
	if err := ValidateMilliShareQuantity(&quantity, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateMilliShareQuantity_OverflowingQuantityRejected(t *testing.T) {
	// A milliShareQuantity near uint64 max wraps negative when converted
	// to int64 in NotionalInMinorUnits, producing a near-zero/negative
	// notional that would sail through margin checks. This MUST be
	// rejected before it ever reaches the conversion.
	huge := uint64(math.MaxInt64) + 1000
	err := ValidateMilliShareQuantity(&huge, true)
	if !errors.Is(err, ErrMilliShareQuantityExceedsMaximum) {
		t.Fatalf("expected ErrMilliShareQuantityExceedsMaximum, got %v", err)
	}
}

func TestValidateMilliShareQuantity_MaxInt64QuantityRejected(t *testing.T) {
	// Exactly math.MaxInt64 does not itself overflow the int64
	// conversion, but it is a wildly unreasonable share quantity (far
	// beyond MaxReasonableMilliShareQuantity) and must still be
	// rejected by the sane ceiling, not just a bare overflow check.
	maxVal := uint64(math.MaxInt64)
	err := ValidateMilliShareQuantity(&maxVal, true)
	if !errors.Is(err, ErrMilliShareQuantityExceedsMaximum) {
		t.Fatalf("expected ErrMilliShareQuantityExceedsMaximum, got %v", err)
	}
}

func TestValidateMilliShareQuantity_AtCeilingAccepted(t *testing.T) {
	ceiling := uint64(MaxReasonableMilliShareQuantity)
	if err := ValidateMilliShareQuantity(&ceiling, true); err != nil {
		t.Fatalf("expected ceiling value to be accepted, got %v", err)
	}
}

func TestValidateMilliShareQuantity_JustAboveCeilingRejected(t *testing.T) {
	overCeiling := uint64(MaxReasonableMilliShareQuantity) + 1
	err := ValidateMilliShareQuantity(&overCeiling, true)
	if !errors.Is(err, ErrMilliShareQuantityExceedsMaximum) {
		t.Fatalf("expected ErrMilliShareQuantityExceedsMaximum, got %v", err)
	}
}

func TestNotionalInMinorUnits_OneWholeShare(t *testing.T) {
	// 1 share (1000 milli-units) @ ₹100.00 (10000 paise) = 10000 paise.
	got := NotionalInMinorUnits(10000, 1000)
	if got != 10000 {
		t.Fatalf("expected 10000, got %d", got)
	}
}

func TestNotionalInMinorUnits_HalfShareHandWorked(t *testing.T) {
	// 0.5 share (500 milli-units) @ ₹100.00 (10000 paise) = 5000 paise.
	got := NotionalInMinorUnits(10000, 500)
	if got != 5000 {
		t.Fatalf("expected 5000, got %d", got)
	}
}

func TestNotionalInMinorUnits_ZeroPointThreeShareHandWorked(t *testing.T) {
	// 0.3 share (300 milli-units) @ ₹100.00 (10000 paise) = 3000 paise
	// exactly.
	got := NotionalInMinorUnits(10000, 300)
	if got != 3000 {
		t.Fatalf("expected 3000, got %d", got)
	}
}

func TestNotionalInMinorUnits_RoundsHalfUp(t *testing.T) {
	// price=333 paise, milliShareQuantity=333 -> numerator=110889,
	// /1000 = 110 remainder 889 (>=500) -> rounds up to 111.
	got := NotionalInMinorUnits(333, 333)
	if got != 111 {
		t.Fatalf("expected 111 (rounded up), got %d", got)
	}
}

func TestNotionalInMinorUnits_RoundsDownWhenRemainderBelowHalf(t *testing.T) {
	// price=100, milliShareQuantity=104 -> numerator=10400, /1000=10
	// remainder 400 (<500) -> stays 10.
	got := NotionalInMinorUnits(100, 104)
	if got != 10 {
		t.Fatalf("expected 10 (rounded down), got %d", got)
	}
}

func TestNotionalInMinorUnits_ZeroPriceIsZero(t *testing.T) {
	if got := NotionalInMinorUnits(0, 1000); got != 0 {
		t.Fatalf("expected 0, got %d", got)
	}
}

func TestNotionalInMinorUnits_ZeroQuantityIsZero(t *testing.T) {
	if got := NotionalInMinorUnits(10000, 0); got != 0 {
		t.Fatalf("expected 0, got %d", got)
	}
}

func TestNotionalInMinorUnits_LargeQuantityExact(t *testing.T) {
	// 1000 whole shares (1,000,000 milli-units) @ ₹50.00 (5000 paise) =
	// 5,000,000 paise.
	got := NotionalInMinorUnits(5000, 1_000_000)
	if got != 5_000_000 {
		t.Fatalf("expected 5000000, got %d", got)
	}
}

func TestFormatWholeAndMilliParts_ExactWholeShare(t *testing.T) {
	whole, remaining := FormatWholeAndMilliParts(2000)
	if whole != 2 || remaining != 0 {
		t.Fatalf("expected (2, 0), got (%d, %d)", whole, remaining)
	}
}

func TestFormatWholeAndMilliParts_OnePointFiveShares(t *testing.T) {
	whole, remaining := FormatWholeAndMilliParts(1500)
	if whole != 1 || remaining != 500 {
		t.Fatalf("expected (1, 500), got (%d, %d)", whole, remaining)
	}
}

func TestFormatWholeAndMilliParts_LessThanOneShare(t *testing.T) {
	whole, remaining := FormatWholeAndMilliParts(300)
	if whole != 0 || remaining != 300 {
		t.Fatalf("expected (0, 300), got (%d, %d)", whole, remaining)
	}
}

func TestFormatWholeAndMilliParts_NegativeQuantity(t *testing.T) {
	whole, remaining := FormatWholeAndMilliParts(-1500)
	if whole != -1 || remaining != 500 {
		t.Fatalf("expected (-1, 500), got (%d, %d)", whole, remaining)
	}
}

func TestFormatWholeAndMilliParts_Zero(t *testing.T) {
	whole, remaining := FormatWholeAndMilliParts(0)
	if whole != 0 || remaining != 0 {
		t.Fatalf("expected (0, 0), got (%d, %d)", whole, remaining)
	}
}
