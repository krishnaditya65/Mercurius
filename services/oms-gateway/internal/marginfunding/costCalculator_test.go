package marginfunding

import (
	"testing"
	"time"
)

var costCalcBaseTime = time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)

func TestCalculateCostSoFarInMinorUnits_MatchesHandWorkedExample(t *testing.T) {
	// Same hand-worked case as TestCalculateIllustrativeAccruedInterest
	// presumably already covers -- principal 1,00,000 paise for 30 days
	// at 12% p.a.: dailyRate = 0.12/365, accrued = 100000*0.12/365*30 =
	// 986.30... -> rounds to 986.
	disbursed := costCalcBaseTime
	asOf := costCalcBaseTime.Add(30 * 24 * time.Hour)
	got := CalculateCostSoFarInMinorUnits(100000, disbursed, asOf)
	want := CalculateIllustrativeAccruedInterest(100000, 30)
	if got != want {
		t.Fatalf("expected %d (matching the underlying formula), got %d", want, got)
	}
}

func TestCalculateCostSoFarInMinorUnits_ZeroDaysElapsed(t *testing.T) {
	got := CalculateCostSoFarInMinorUnits(100000, costCalcBaseTime, costCalcBaseTime)
	if got != 0 {
		t.Fatalf("expected 0 cost at zero days elapsed, got %d", got)
	}
}

func TestCalculateCostSoFarInMinorUnits_AsOfBeforeDisbursedClampsToZero(t *testing.T) {
	got := CalculateCostSoFarInMinorUnits(100000, costCalcBaseTime, costCalcBaseTime.Add(-24*time.Hour))
	if got != 0 {
		t.Fatalf("expected 0 for asOfTime before disbursedAtTime, got %d", got)
	}
}

func TestCalculateCostSoFarInMinorUnits_NonPositivePrincipalIsZero(t *testing.T) {
	got := CalculateCostSoFarInMinorUnits(0, costCalcBaseTime, costCalcBaseTime.Add(30*24*time.Hour))
	if got != 0 {
		t.Fatalf("expected 0 for zero principal, got %d", got)
	}
}

func TestCalculateCostSoFarInMinorUnits_PartialDayFlooredDown(t *testing.T) {
	// 10 days and 12 hours elapsed -- should floor to 10 whole days, not
	// round up to 11.
	asOf := costCalcBaseTime.Add(10*24*time.Hour + 12*time.Hour)
	got := CalculateCostSoFarInMinorUnits(100000, costCalcBaseTime, asOf)
	want := CalculateIllustrativeAccruedInterest(100000, 10)
	if got != want {
		t.Fatalf("expected floored-to-10-days value %d, got %d", want, got)
	}
}

func TestCalculateProjectedCostInMinorUnits_IncludesCostSoFarPlusProjection(t *testing.T) {
	disbursed := costCalcBaseTime
	asOf := costCalcBaseTime.Add(10 * 24 * time.Hour)
	projected := CalculateProjectedCostInMinorUnits(100000, disbursed, asOf, 20)
	want := CalculateIllustrativeAccruedInterest(100000, 30) // 10 elapsed + 20 projected
	if projected != want {
		t.Fatalf("expected total-30-day cost %d, got %d", want, projected)
	}
}

func TestCalculateProjectedCostInMinorUnits_ZeroAdditionalDaysEqualsSoFar(t *testing.T) {
	disbursed := costCalcBaseTime
	asOf := costCalcBaseTime.Add(15 * 24 * time.Hour)
	projected := CalculateProjectedCostInMinorUnits(100000, disbursed, asOf, 0)
	costSoFar := CalculateCostSoFarInMinorUnits(100000, disbursed, asOf)
	if projected != costSoFar {
		t.Fatalf("expected projected(0 additional days)==costSoFar: got %d vs %d", projected, costSoFar)
	}
}

func TestCalculateProjectedCostInMinorUnits_NegativeAdditionalDaysClampedToZero(t *testing.T) {
	disbursed := costCalcBaseTime
	asOf := costCalcBaseTime.Add(15 * 24 * time.Hour)
	projected := CalculateProjectedCostInMinorUnits(100000, disbursed, asOf, -5)
	costSoFar := CalculateCostSoFarInMinorUnits(100000, disbursed, asOf)
	if projected != costSoFar {
		t.Fatalf("expected negative additional days clamped to 0: got %d vs %d", projected, costSoFar)
	}
}

func TestBuildLiveInterestCostSnapshot_FieldsConsistent(t *testing.T) {
	disbursed := costCalcBaseTime
	asOf := costCalcBaseTime.Add(10 * 24 * time.Hour)
	snapshot := BuildLiveInterestCostSnapshot(100000, disbursed, asOf, 20)

	if snapshot.OutstandingPrincipalInMinorUnits != 100000 {
		t.Fatalf("unexpected principal: %d", snapshot.OutstandingPrincipalInMinorUnits)
	}
	if snapshot.DaysElapsedSoFar != 10 {
		t.Fatalf("expected 10 days elapsed, got %d", snapshot.DaysElapsedSoFar)
	}
	if snapshot.CostSoFarInMinorUnits != CalculateCostSoFarInMinorUnits(100000, disbursed, asOf) {
		t.Fatalf("cost-so-far mismatch")
	}
	if snapshot.ProjectedTotalCostInMinorUnits != CalculateProjectedCostInMinorUnits(100000, disbursed, asOf, 20) {
		t.Fatalf("projected-total mismatch")
	}
	expectedIncremental := snapshot.ProjectedTotalCostInMinorUnits - snapshot.CostSoFarInMinorUnits
	if snapshot.ProjectedIncrementalCostInMinorUnits != expectedIncremental {
		t.Fatalf("expected incremental %d, got %d", expectedIncremental, snapshot.ProjectedIncrementalCostInMinorUnits)
	}
}

func TestBuildLiveInterestCostSnapshot_ZeroPrincipalZeroCosts(t *testing.T) {
	snapshot := BuildLiveInterestCostSnapshot(0, costCalcBaseTime, costCalcBaseTime.Add(30*24*time.Hour), 10)
	if snapshot.CostSoFarInMinorUnits != 0 || snapshot.ProjectedTotalCostInMinorUnits != 0 {
		t.Fatalf("expected zero costs for zero principal, got %+v", snapshot)
	}
}

func TestRecordDisbursementStartTime_FirstDrawSetsTime(t *testing.T) {
	fundingBook := NewFundingBook()
	fundingBook.RecordDisbursementStartTime("acct-1", costCalcBaseTime)
	startTime, exists := fundingBook.DisbursementStartTime("acct-1")
	if !exists || !startTime.Equal(costCalcBaseTime) {
		t.Fatalf("expected start time recorded, got %v exists=%v", startTime, exists)
	}
}

func TestRecordDisbursementStartTime_SecondDrawDoesNotResetClock(t *testing.T) {
	fundingBook := NewFundingBook()
	fundingBook.RecordDisbursementStartTime("acct-1", costCalcBaseTime)
	fundingBook.RecordDisbursementStartTime("acct-1", costCalcBaseTime.Add(10*24*time.Hour))
	startTime, _ := fundingBook.DisbursementStartTime("acct-1")
	if !startTime.Equal(costCalcBaseTime) {
		t.Fatalf("expected original start time preserved, got %v", startTime)
	}
}

func TestDisbursementStartTime_UnknownAccountReturnsFalse(t *testing.T) {
	fundingBook := NewFundingBook()
	_, exists := fundingBook.DisbursementStartTime("nonexistent")
	if exists {
		t.Fatalf("expected exists=false for unknown account")
	}
}

func TestRepayFunding_FullRepaymentClearsDisbursementStartTime(t *testing.T) {
	fundingBook := NewFundingBook()
	if _, err := fundingBook.RequestFunding("acct-1", 50000, 100000); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	fundingBook.RecordDisbursementStartTime("acct-1", costCalcBaseTime)

	if _, err := fundingBook.RepayFunding("acct-1", 50000); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, exists := fundingBook.DisbursementStartTime("acct-1")
	if exists {
		t.Fatalf("expected disbursement start time cleared after full repayment")
	}
}

func TestRepayFunding_PartialRepaymentKeepsDisbursementStartTime(t *testing.T) {
	fundingBook := NewFundingBook()
	if _, err := fundingBook.RequestFunding("acct-1", 50000, 100000); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	fundingBook.RecordDisbursementStartTime("acct-1", costCalcBaseTime)

	if _, err := fundingBook.RepayFunding("acct-1", 20000); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, exists := fundingBook.DisbursementStartTime("acct-1")
	if !exists {
		t.Fatalf("expected disbursement start time preserved after partial repayment")
	}
}

func TestFreshDrawAfterFullRepaymentRestartsClock(t *testing.T) {
	fundingBook := NewFundingBook()
	fundingBook.RequestFunding("acct-1", 50000, 100000)
	fundingBook.RecordDisbursementStartTime("acct-1", costCalcBaseTime)
	fundingBook.RepayFunding("acct-1", 50000)

	fundingBook.RequestFunding("acct-1", 30000, 100000)
	later := costCalcBaseTime.Add(60 * 24 * time.Hour)
	fundingBook.RecordDisbursementStartTime("acct-1", later)

	startTime, exists := fundingBook.DisbursementStartTime("acct-1")
	if !exists || !startTime.Equal(later) {
		t.Fatalf("expected fresh clock starting at %v, got %v exists=%v", later, startTime, exists)
	}
}
