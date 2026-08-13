package marginfunding

import (
	"errors"
	"sync"
	"testing"
)

func TestRequestFundingApprovesWithinPledgedCapacity(t *testing.T) {
	fundingBook := NewFundingBook()

	newOutstanding, err := fundingBook.RequestFunding("acct-001", 50_000, 100_000)
	if err != nil {
		t.Fatalf("expected approval, got error: %v", err)
	}
	if newOutstanding != 50_000 {
		t.Errorf("expected outstanding 50000, got %d", newOutstanding)
	}
}

func TestRequestFundingAccumulatesAcrossMultipleCalls(t *testing.T) {
	fundingBook := NewFundingBook()

	if _, err := fundingBook.RequestFunding("acct-001", 40_000, 100_000); err != nil {
		t.Fatalf("first request failed: %v", err)
	}
	newOutstanding, err := fundingBook.RequestFunding("acct-001", 30_000, 100_000)
	if err != nil {
		t.Fatalf("second request failed: %v", err)
	}
	if newOutstanding != 70_000 {
		t.Errorf("expected accumulated outstanding 70000, got %d", newOutstanding)
	}
}

func TestRequestFundingRejectsExceedingUnutilizedCollateral(t *testing.T) {
	fundingBook := NewFundingBook()

	if _, err := fundingBook.RequestFunding("acct-001", 90_000, 100_000); err != nil {
		t.Fatalf("first request failed: %v", err)
	}

	// only 10,000 of unutilized capacity remains
	_, err := fundingBook.RequestFunding("acct-001", 10_001, 100_000)
	if !errors.Is(err, ErrInsufficientUnutilizedPledgedCollateral) {
		t.Fatalf("expected ErrInsufficientUnutilizedPledgedCollateral, got %v", err)
	}
}

func TestRequestFundingExactRemainingCapacityBoundaryAllowed(t *testing.T) {
	fundingBook := NewFundingBook()

	if _, err := fundingBook.RequestFunding("acct-001", 90_000, 100_000); err != nil {
		t.Fatalf("first request failed: %v", err)
	}

	newOutstanding, err := fundingBook.RequestFunding("acct-001", 10_000, 100_000)
	if err != nil {
		t.Fatalf("expected the exact remaining capacity to be approved, got error: %v", err)
	}
	if newOutstanding != 100_000 {
		t.Errorf("expected outstanding 100000, got %d", newOutstanding)
	}
}

func TestRequestFundingRejectsZeroOrNegativeAmount(t *testing.T) {
	fundingBook := NewFundingBook()

	if _, err := fundingBook.RequestFunding("acct-001", 0, 100_000); !errors.Is(err, ErrFundingAmountMustBePositive) {
		t.Errorf("expected ErrFundingAmountMustBePositive for zero, got %v", err)
	}
	if _, err := fundingBook.RequestFunding("acct-001", -5, 100_000); !errors.Is(err, ErrFundingAmountMustBePositive) {
		t.Errorf("expected ErrFundingAmountMustBePositive for negative, got %v", err)
	}
}

func TestRequestFundingRejectsWhenNoPledgedCollateralAtAll(t *testing.T) {
	fundingBook := NewFundingBook()

	_, err := fundingBook.RequestFunding("acct-001", 1, 0)
	if !errors.Is(err, ErrInsufficientUnutilizedPledgedCollateral) {
		t.Fatalf("expected ErrInsufficientUnutilizedPledgedCollateral, got %v", err)
	}
}

func TestRollbackReservationReturnsCapacity(t *testing.T) {
	fundingBook := NewFundingBook()

	if _, err := fundingBook.RequestFunding("acct-001", 100_000, 100_000); err != nil {
		t.Fatalf("request failed: %v", err)
	}
	fundingBook.RollbackReservation("acct-001", 100_000)

	if outstanding := fundingBook.OutstandingPrincipalInMinorUnits("acct-001"); outstanding != 0 {
		t.Errorf("expected outstanding 0 after rollback, got %d", outstanding)
	}

	// capacity should be usable again
	if _, err := fundingBook.RequestFunding("acct-001", 100_000, 100_000); err != nil {
		t.Fatalf("expected re-request to succeed after rollback, got: %v", err)
	}
}

func TestRollbackReservationNeverGoesNegative(t *testing.T) {
	fundingBook := NewFundingBook()

	fundingBook.RollbackReservation("acct-001", 50_000)
	if outstanding := fundingBook.OutstandingPrincipalInMinorUnits("acct-001"); outstanding != 0 {
		t.Errorf("expected outstanding floored at 0, got %d", outstanding)
	}
}

func TestRepayFundingReducesOutstandingPrincipal(t *testing.T) {
	fundingBook := NewFundingBook()

	if _, err := fundingBook.RequestFunding("acct-001", 60_000, 100_000); err != nil {
		t.Fatalf("request failed: %v", err)
	}
	newOutstanding, err := fundingBook.RepayFunding("acct-001", 20_000)
	if err != nil {
		t.Fatalf("repay failed: %v", err)
	}
	if newOutstanding != 40_000 {
		t.Errorf("expected outstanding 40000 after repayment, got %d", newOutstanding)
	}
}

func TestRepayFundingRejectsExceedingOutstanding(t *testing.T) {
	fundingBook := NewFundingBook()

	if _, err := fundingBook.RequestFunding("acct-001", 10_000, 100_000); err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if _, err := fundingBook.RepayFunding("acct-001", 10_001); !errors.Is(err, ErrRepaymentExceedsOutstandingPrincipal) {
		t.Errorf("expected ErrRepaymentExceedsOutstandingPrincipal, got %v", err)
	}
}

func TestRepayFundingRejectsZeroOrNegativeAmount(t *testing.T) {
	fundingBook := NewFundingBook()
	if _, err := fundingBook.RepayFunding("acct-001", 0); !errors.Is(err, ErrFundingAmountMustBePositive) {
		t.Errorf("expected ErrFundingAmountMustBePositive, got %v", err)
	}
}

func TestFundingRecordForAccountReflectsCurrentState(t *testing.T) {
	fundingBook := NewFundingBook()
	if _, err := fundingBook.RequestFunding("acct-001", 25_000, 100_000); err != nil {
		t.Fatalf("request failed: %v", err)
	}

	record := fundingBook.FundingRecordForAccount("acct-001")
	if record.ClientAccountIdentifier != "acct-001" || record.OutstandingPrincipalInMinorUnits != 25_000 {
		t.Errorf("unexpected record: %+v", record)
	}
}

func TestFundingRecordForUnknownAccountIsZero(t *testing.T) {
	fundingBook := NewFundingBook()
	record := fundingBook.FundingRecordForAccount("never-seen")
	if record.OutstandingPrincipalInMinorUnits != 0 {
		t.Errorf("expected zero outstanding for unknown account, got %d", record.OutstandingPrincipalInMinorUnits)
	}
}

// TestConcurrentRequestFundingNeverExceedsCapacity proves the mutex
// actually serializes requests: firing many concurrent requests that
// together would exceed capacity if raced must result in exactly the
// capacity being consumed, no more.
func TestConcurrentRequestFundingNeverExceedsCapacity(t *testing.T) {
	fundingBook := NewFundingBook()
	const pledgedCapacity = 100_000
	const perRequestAmount = 10_000
	const numberOfConcurrentRequests = 30 // 300,000 requested against 100,000 capacity

	var waitGroup sync.WaitGroup
	var successCount int
	var mutexGuardingCounter sync.Mutex

	for i := 0; i < numberOfConcurrentRequests; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			if _, err := fundingBook.RequestFunding("acct-001", perRequestAmount, pledgedCapacity); err == nil {
				mutexGuardingCounter.Lock()
				successCount++
				mutexGuardingCounter.Unlock()
			}
		}()
	}
	waitGroup.Wait()

	if successCount != pledgedCapacity/perRequestAmount {
		t.Errorf("expected exactly %d successful requests, got %d", pledgedCapacity/perRequestAmount, successCount)
	}
	if outstanding := fundingBook.OutstandingPrincipalInMinorUnits("acct-001"); outstanding != pledgedCapacity {
		t.Errorf("expected outstanding to be exactly capacity %d, got %d", pledgedCapacity, outstanding)
	}
}

func TestCalculateIllustrativeAccruedInterestKnownValue(t *testing.T) {
	// principal 1,000,000 minor units, 12% annual rate, 30 days:
	// dailyRate = 0.12/365 = 0.00032876712...
	// accrued = 1,000,000 * 0.00032876712... * 30 = 9863.0136... -> rounds to 9863
	accrued := CalculateIllustrativeAccruedInterest(1_000_000, 30)
	if accrued != 9863 {
		t.Errorf("expected 9863, got %d", accrued)
	}
}

func TestCalculateIllustrativeAccruedInterestZeroForNonPositiveInputs(t *testing.T) {
	if got := CalculateIllustrativeAccruedInterest(0, 30); got != 0 {
		t.Errorf("expected 0 for zero principal, got %d", got)
	}
	if got := CalculateIllustrativeAccruedInterest(1000, 0); got != 0 {
		t.Errorf("expected 0 for zero days, got %d", got)
	}
	if got := CalculateIllustrativeAccruedInterest(-1000, 30); got != 0 {
		t.Errorf("expected 0 for negative principal, got %d", got)
	}
}
