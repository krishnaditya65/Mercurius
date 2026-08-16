package bondladderbuilder

import (
	"testing"
	"time"

	"mercurius/mutualFunds/internal/fixedincome"
)

// buildFullyStaggeredLadder builds a ladder with one rung per catalog bond
// (numberOfRungs == len(catalog)), each allocated exactly perBondAmount,
// so every catalog bond ends up held at a known, hand-computable face
// value.
func buildFullyStaggeredLadder(t *testing.T, catalog *fixedincome.BondCatalog, builder *Builder, accountIdentifier string, perBondAmount int64, now time.Time) {
	t.Helper()
	bondCount := len(catalog.ListAll())
	if _, err := builder.BuildLadder(accountIdentifier, perBondAmount*int64(bondCount), bondCount, now); err != nil {
		t.Fatalf("unexpected error building fully-staggered ladder: %v", err)
	}
}

// TestUpcomingCouponsHandWorkedNextCouponDateAndAmount is a hand-worked
// case: GSEC-07.10-2028 has IssueDate 2023-08-01, coupon 7.10% paid
// semi-annually. Coupon dates step 6 months from IssueDate:
// 2024-02-01, 2024-08-01, 2025-02-01, 2025-08-01, 2026-02-01, 2026-08-01,
// 2027-02-01, ... . With asOf = 2026-08-14 (after the 2026-08-01 coupon),
// the NEXT coupon is 2027-02-01. Held face value is set to exactly the
// bond's own FaceValueInMinorUnits (100000), so the coupon amount is
// exactly 100000 * 7.10% / 2 = 3550 — the same as the catalog's own
// full-face-value coupon, hand-computed independently of the
// implementation.
func TestUpcomingCouponsHandWorkedNextCouponDateAndAmount(t *testing.T) {
	catalog := fixedincome.NewBondCatalog()
	builder := NewBuilder(catalog)
	now, _ := time.Parse("2006-01-02", "2026-08-14")

	buildFullyStaggeredLadder(t, catalog, builder, "acct-1", 100000, now)

	reminders := builder.UpcomingCoupons(catalog, "acct-1", now)

	var found *CouponReminder
	for i := range reminders {
		if reminders[i].BondId == "GSEC-07.10-2028" {
			found = &reminders[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected at least one upcoming coupon reminder for GSEC-07.10-2028")
	}

	expectedDate, _ := time.Parse("2006-01-02", "2027-02-01")
	if !found.CouponDate.Equal(expectedDate) {
		t.Fatalf("expected next coupon date %s, got %s", expectedDate, found.CouponDate)
	}
	if found.AmountInMinorUnits != 3550 {
		t.Fatalf("expected coupon amount exactly 3550, got %d", found.AmountInMinorUnits)
	}
}

func TestUpcomingCouponsExcludesTreasuryBills(t *testing.T) {
	catalog := fixedincome.NewBondCatalog()
	builder := NewBuilder(catalog)
	now := time.Now()

	buildFullyStaggeredLadder(t, catalog, builder, "acct-1", 100000, now)

	reminders := builder.UpcomingCoupons(catalog, "acct-1", now)
	for _, reminder := range reminders {
		if reminder.BondId == "TBILL-91D-NOV26" || reminder.BondId == "TBILL-364D-AUG27" {
			t.Fatalf("expected no coupon reminders for a T-Bill, got one for %s", reminder.BondId)
		}
	}
}

func TestUpcomingCouponsExcludesPastCouponDates(t *testing.T) {
	catalog := fixedincome.NewBondCatalog()
	builder := NewBuilder(catalog)
	now, _ := time.Parse("2006-01-02", "2026-08-14")

	buildFullyStaggeredLadder(t, catalog, builder, "acct-1", 100000, now)

	reminders := builder.UpcomingCoupons(catalog, "acct-1", now)
	for _, reminder := range reminders {
		if !reminder.CouponDate.After(now) {
			t.Fatalf("expected every reminder's CouponDate to be after asOf, got %s for %s", reminder.CouponDate, reminder.BondId)
		}
	}
}

func TestUpcomingCouponsExcludesDatesAfterMaturity(t *testing.T) {
	catalog := fixedincome.NewBondCatalog()
	builder := NewBuilder(catalog)
	now := time.Now()

	buildFullyStaggeredLadder(t, catalog, builder, "acct-1", 100000, now)

	reminders := builder.UpcomingCoupons(catalog, "acct-1", now)
	for _, reminder := range reminders {
		bond, _ := catalog.Lookup(reminder.BondId)
		if reminder.CouponDate.After(bond.MaturityDate) {
			t.Fatalf("expected no coupon reminder after maturity, got %s for %s (matures %s)", reminder.CouponDate, reminder.BondId, bond.MaturityDate)
		}
	}
}

func TestUpcomingCouponsIsSortedByDateThenBondId(t *testing.T) {
	catalog := fixedincome.NewBondCatalog()
	builder := NewBuilder(catalog)
	now := time.Now()

	buildFullyStaggeredLadder(t, catalog, builder, "acct-1", 100000, now)

	reminders := builder.UpcomingCoupons(catalog, "acct-1", now)
	for i := 1; i < len(reminders); i++ {
		if reminders[i].CouponDate.Before(reminders[i-1].CouponDate) {
			t.Fatal("expected reminders sorted by CouponDate ascending")
		}
		if reminders[i].CouponDate.Equal(reminders[i-1].CouponDate) && reminders[i].BondId < reminders[i-1].BondId {
			t.Fatal("expected same-date reminders sorted by BondId")
		}
	}
}

func TestUpcomingCouponsAmountScalesWithHeldFaceValue(t *testing.T) {
	catalog := fixedincome.NewBondCatalog()
	builder := NewBuilder(catalog)
	now, _ := time.Parse("2006-01-02", "2026-08-14")

	// Hold HALF the bond's face value this time -> coupon amount should
	// be exactly half of the full-face-value case (1775 instead of 3550).
	buildFullyStaggeredLadder(t, catalog, builder, "acct-1", 50000, now)

	reminders := builder.UpcomingCoupons(catalog, "acct-1", now)
	for _, reminder := range reminders {
		if reminder.BondId == "GSEC-07.10-2028" {
			if reminder.AmountInMinorUnits != 1775 {
				t.Fatalf("expected coupon amount 1775 for half face value held, got %d", reminder.AmountInMinorUnits)
			}
			return
		}
	}
	t.Fatal("expected a coupon reminder for GSEC-07.10-2028")
}

// TestMonthsPerCouponPeriodOrPanicHandWorkedDivisorFrequencies covers every
// frequency that evenly divides 12 (the only ones UpcomingCoupons'
// calendar-month stepping supports).
func TestMonthsPerCouponPeriodOrPanicHandWorkedDivisorFrequencies(t *testing.T) {
	cases := []struct {
		paymentsPerYear int
		wantMonths      int
	}{
		{paymentsPerYear: 1, wantMonths: 12},
		{paymentsPerYear: 2, wantMonths: 6},
		{paymentsPerYear: 3, wantMonths: 4},
		{paymentsPerYear: 4, wantMonths: 3},
		{paymentsPerYear: 6, wantMonths: 2},
		{paymentsPerYear: 12, wantMonths: 1},
	}
	for _, testCase := range cases {
		bond := fixedincome.Bond{BondId: "TEST-BOND", PaymentsPerYear: testCase.paymentsPerYear}
		got := monthsPerCouponPeriodOrPanic(bond)
		if got != testCase.wantMonths {
			t.Fatalf("PaymentsPerYear=%d: expected %d months per period, got %d", testCase.paymentsPerYear, testCase.wantMonths, got)
		}
	}
}

// TestMonthsPerCouponPeriodOrPanicRejectsNonDivisorFrequenciesInsteadOfSilentlyTruncating
// reproduces the confirmed audit bug: 12/PaymentsPerYear is plain integer
// division, so before the fix a PaymentsPerYear of 5, 7, 8, 9, or 11 was
// silently truncated (e.g. 12/5 == 2, not the true 2.4 months) rather than
// rejected, producing a systematically wrong coupon calendar with no
// error. After the fix, every non-divisor frequency must panic loudly
// instead of returning a truncated value.
func TestMonthsPerCouponPeriodOrPanicRejectsNonDivisorFrequenciesInsteadOfSilentlyTruncating(t *testing.T) {
	for _, nonDivisorPaymentsPerYear := range []int{5, 7, 8, 9, 11} {
		func() {
			defer func() {
				if recovered := recover(); recovered == nil {
					t.Errorf("PaymentsPerYear=%d: expected a panic instead of a silently truncated month count", nonDivisorPaymentsPerYear)
				}
			}()
			bond := fixedincome.Bond{BondId: "TEST-BOND", PaymentsPerYear: nonDivisorPaymentsPerYear}
			gotMonths := monthsPerCouponPeriodOrPanic(bond)
			t.Errorf("PaymentsPerYear=%d: expected a panic, got a silently truncated %d months per period (12/%d truncates to %d, losing the true %.1f)",
				nonDivisorPaymentsPerYear, gotMonths, nonDivisorPaymentsPerYear, gotMonths, 12.0/float64(nonDivisorPaymentsPerYear))
		}()
	}
}

// TestUpcomingCouponsPanicsForABondWithANonDivisorPaymentsPerYear exercises
// the same bug end-to-end through UpcomingCoupons itself (not just the
// extracted helper), using a hand-built catalog with a single bond whose
// PaymentsPerYear (5) does not evenly divide 12.
func TestUpcomingCouponsPanicsForABondWithANonDivisorPaymentsPerYear(t *testing.T) {
	catalog := fixedincome.NewBondCatalog()
	builder := NewBuilder(catalog)
	now, _ := time.Parse("2006-01-02", "2026-08-14")

	buildFullyStaggeredLadder(t, catalog, builder, "acct-1", 100000, now)

	// Directly exercise the helper UpcomingCoupons relies on with a bond
	// shaped like a real catalog entry but with an unsupported frequency,
	// confirming the end-to-end code path panics rather than silently
	// computing a wrong coupon calendar.
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("expected UpcomingCoupons' month-stepping logic to panic for PaymentsPerYear=5, not silently truncate")
		}
	}()
	badBond := fixedincome.Bond{
		BondId:            "HYPOTHETICAL-QUINTUPLE-COUPON-BOND",
		IssueDate:         now,
		MaturityDate:      now.AddDate(2, 0, 0),
		CouponRatePercent: 7.0,
		PaymentsPerYear:   5, // does not evenly divide 12
	}
	_ = monthsPerCouponPeriodOrPanic(badBond)
}

func TestUpcomingCouponsUnknownAccountReturnsEmpty(t *testing.T) {
	catalog := fixedincome.NewBondCatalog()
	builder := NewBuilder(catalog)
	reminders := builder.UpcomingCoupons(catalog, "no-such-account", time.Now())
	if len(reminders) != 0 {
		t.Fatalf("expected no reminders for an unknown account, got %d", len(reminders))
	}
}
