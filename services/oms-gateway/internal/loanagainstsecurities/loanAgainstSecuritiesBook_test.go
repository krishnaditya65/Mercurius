package loanagainstsecurities

import (
	"errors"
	"sync"
	"testing"
)

// TestCalculateLoanToValueCap_HandWorked: pledged collateral 100000, LTV
// 50% -> cap exactly 50000.
func TestCalculateLoanToValueCap_HandWorked(t *testing.T) {
	if cap := CalculateLoanToValueCap(100000); cap != 50000 {
		t.Fatalf("expected LTV cap 50000, got %d", cap)
	}
}

func TestCalculateLoanToValueCap_NonPositiveCollateralIsZero(t *testing.T) {
	if cap := CalculateLoanToValueCap(0); cap != 0 {
		t.Fatalf("expected zero cap for zero collateral, got %d", cap)
	}
	if cap := CalculateLoanToValueCap(-100); cap != 0 {
		t.Fatalf("expected zero cap for negative collateral, got %d", cap)
	}
}

func TestRequestLoan_ApprovesWithinLtvCapacity(t *testing.T) {
	book := NewLoanBook()
	outstanding, err := book.RequestLoan("acct-001", 30000, 100000, 12)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outstanding != 30000 {
		t.Fatalf("expected outstanding 30000, got %d", outstanding)
	}
}

func TestRequestLoan_RejectsExceedingLtvCap(t *testing.T) {
	book := NewLoanBook()
	// LTV cap is 50000 (50% of 100000); request 50001
	_, err := book.RequestLoan("acct-001", 50001, 100000, 12)
	if !errors.Is(err, ErrInsufficientLoanToValueCapacity) {
		t.Fatalf("expected ErrInsufficientLoanToValueCapacity, got %v", err)
	}
}

func TestRequestLoan_ExactCapBoundaryAllowed(t *testing.T) {
	book := NewLoanBook()
	outstanding, err := book.RequestLoan("acct-001", 50000, 100000, 12)
	if err != nil {
		t.Fatalf("unexpected error at exact LTV cap boundary: %v", err)
	}
	if outstanding != 50000 {
		t.Fatalf("expected outstanding 50000, got %d", outstanding)
	}
}

func TestRequestLoan_AccumulatesAcrossCallsAndCapsAtLtv(t *testing.T) {
	book := NewLoanBook()
	if _, err := book.RequestLoan("acct-001", 20000, 100000, 12); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := book.RequestLoan("acct-001", 20000, 100000, 12); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// total now 40000; a further 20000 would total 60000 > 50000 cap
	_, err := book.RequestLoan("acct-001", 20000, 100000, 12)
	if !errors.Is(err, ErrInsufficientLoanToValueCapacity) {
		t.Fatalf("expected ErrInsufficientLoanToValueCapacity, got %v", err)
	}
	if outstanding := book.OutstandingPrincipalInMinorUnits("acct-001"); outstanding != 40000 {
		t.Fatalf("expected outstanding to remain 40000 after the rejected request, got %d", outstanding)
	}
}

func TestRequestLoan_RejectsZeroOrNegativeAmount(t *testing.T) {
	book := NewLoanBook()
	if _, err := book.RequestLoan("acct-001", 0, 100000, 12); !errors.Is(err, ErrLoanAmountMustBePositive) {
		t.Fatalf("expected ErrLoanAmountMustBePositive, got %v", err)
	}
	if _, err := book.RequestLoan("acct-001", -1, 100000, 12); !errors.Is(err, ErrLoanAmountMustBePositive) {
		t.Fatalf("expected ErrLoanAmountMustBePositive for negative, got %v", err)
	}
}

func TestRequestLoan_RejectsZeroTenure(t *testing.T) {
	book := NewLoanBook()
	if _, err := book.RequestLoan("acct-001", 1000, 100000, 0); !errors.Is(err, ErrTenureMustBePositive) {
		t.Fatalf("expected ErrTenureMustBePositive, got %v", err)
	}
}

func TestRequestLoan_RejectsWhenNoPledgedCollateralAtAll(t *testing.T) {
	book := NewLoanBook()
	if _, err := book.RequestLoan("acct-001", 1, 0, 12); !errors.Is(err, ErrInsufficientLoanToValueCapacity) {
		t.Fatalf("expected ErrInsufficientLoanToValueCapacity, got %v", err)
	}
}

func TestRollbackReservation_ReturnsCapacity(t *testing.T) {
	book := NewLoanBook()
	if _, err := book.RequestLoan("acct-001", 30000, 100000, 12); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	book.RollbackReservation("acct-001", 30000)
	if outstanding := book.OutstandingPrincipalInMinorUnits("acct-001"); outstanding != 0 {
		t.Fatalf("expected outstanding 0 after rollback, got %d", outstanding)
	}
	// capacity should now be free again
	if _, err := book.RequestLoan("acct-001", 50000, 100000, 12); err != nil {
		t.Fatalf("expected full capacity available again after rollback, got %v", err)
	}
}

func TestRollbackReservation_NeverGoesNegative(t *testing.T) {
	book := NewLoanBook()
	book.RollbackReservation("acct-001", 5000)
	if outstanding := book.OutstandingPrincipalInMinorUnits("acct-001"); outstanding != 0 {
		t.Fatalf("expected outstanding floored at 0, got %d", outstanding)
	}
}

func TestRepayLoan_ReducesOutstandingPrincipal(t *testing.T) {
	book := NewLoanBook()
	if _, err := book.RequestLoan("acct-001", 30000, 100000, 12); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	outstanding, err := book.RepayLoan("acct-001", 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outstanding != 20000 {
		t.Fatalf("expected outstanding 20000 after repayment, got %d", outstanding)
	}
}

func TestRepayLoan_RejectsExceedingOutstanding(t *testing.T) {
	book := NewLoanBook()
	if _, err := book.RequestLoan("acct-001", 10000, 100000, 12); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err := book.RepayLoan("acct-001", 10001)
	if !errors.Is(err, ErrRepaymentExceedsOutstandingPrincipal) {
		t.Fatalf("expected ErrRepaymentExceedsOutstandingPrincipal, got %v", err)
	}
}

func TestRepayLoan_RejectsZeroOrNegativeAmount(t *testing.T) {
	book := NewLoanBook()
	if _, err := book.RepayLoan("acct-001", 0); !errors.Is(err, ErrLoanAmountMustBePositive) {
		t.Fatalf("expected ErrLoanAmountMustBePositive, got %v", err)
	}
}

func TestLoanRecordForAccount_ReflectsCurrentStateIncludingTenure(t *testing.T) {
	book := NewLoanBook()
	if _, err := book.RequestLoan("acct-001", 30000, 100000, 24); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	record := book.LoanRecordForAccount("acct-001")
	if record.OutstandingPrincipalInMinorUnits != 30000 {
		t.Fatalf("expected outstanding 30000, got %d", record.OutstandingPrincipalInMinorUnits)
	}
	if record.TenureInMonths != 24 {
		t.Fatalf("expected tenure 24 months, got %d", record.TenureInMonths)
	}
}

func TestLoanRecordForUnknownAccountIsZero(t *testing.T) {
	book := NewLoanBook()
	record := book.LoanRecordForAccount("never-seen")
	if record.OutstandingPrincipalInMinorUnits != 0 || record.TenureInMonths != 0 {
		t.Fatalf("expected zero-value record for unknown account, got %+v", record)
	}
}

func TestConcurrentRequestLoan_NeverExceedsLtvCapacity(t *testing.T) {
	book := NewLoanBook()
	var waitGroup sync.WaitGroup
	successCount := 0
	var mutexGuardingCounter sync.Mutex

	// LTV cap = 50% of 1,000,000 = 500,000; 100 concurrent requests of
	// 10,000 each -> exactly 50 should succeed.
	for i := 0; i < 100; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, err := book.RequestLoan("acct-shared", 10000, 1000000, 12)
			if err == nil {
				mutexGuardingCounter.Lock()
				successCount++
				mutexGuardingCounter.Unlock()
			}
		}()
	}
	waitGroup.Wait()

	if successCount != 50 {
		t.Fatalf("expected exactly 50 successful requests, got %d", successCount)
	}
	if outstanding := book.OutstandingPrincipalInMinorUnits("acct-shared"); outstanding != 500000 {
		t.Fatalf("expected outstanding exactly at LTV cap 500000, got %d", outstanding)
	}
}

// TestCalculateIllustrativeAccruedInterest_HandWorked: principal 100000,
// rate 9% p.a., 365 days -> exactly 9000.
func TestCalculateIllustrativeAccruedInterest_HandWorked(t *testing.T) {
	interest := CalculateIllustrativeAccruedInterest(100000, 365)
	if interest != 9000 {
		t.Fatalf("expected interest 9000, got %d", interest)
	}
}

func TestCalculateIllustrativeAccruedInterest_ZeroForNonPositiveInputs(t *testing.T) {
	if interest := CalculateIllustrativeAccruedInterest(0, 30); interest != 0 {
		t.Fatalf("expected zero interest for zero principal, got %d", interest)
	}
	if interest := CalculateIllustrativeAccruedInterest(100000, 0); interest != 0 {
		t.Fatalf("expected zero interest for zero days, got %d", interest)
	}
	if interest := CalculateIllustrativeAccruedInterest(-100, 30); interest != 0 {
		t.Fatalf("expected zero interest for negative principal, got %d", interest)
	}
}

func TestRestorePrincipalAfterFailedRepaymentLedgerPosting_ReAddsPrincipal(t *testing.T) {
	book := NewLoanBook()
	if _, err := book.RequestLoan("acct-001", 30000, 100000, 12); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := book.RepayLoan("acct-001", 10000); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outstanding := book.OutstandingPrincipalInMinorUnits("acct-001"); outstanding != 20000 {
		t.Fatalf("expected 20000 after repayment, got %d", outstanding)
	}
	book.RestorePrincipalAfterFailedRepaymentLedgerPosting("acct-001", 10000)
	if outstanding := book.OutstandingPrincipalInMinorUnits("acct-001"); outstanding != 30000 {
		t.Fatalf("expected principal restored to 30000, got %d", outstanding)
	}
}

// TestLoanToValueIsStricterThanFullPledgedValue documents the real,
// load-bearing distinction from internal/marginfunding: LAS caps at a
// FRACTION of pledged value, not the full value.
func TestLoanToValueIsStricterThanFullPledgedValue(t *testing.T) {
	book := NewLoanBook()
	pledgedValue := int64(100000)
	// A request for the FULL pledged value must be rejected under LAS's
	// LTV cap, even though internal/marginfunding would allow it.
	_, err := book.RequestLoan("acct-001", pledgedValue, pledgedValue, 12)
	if !errors.Is(err, ErrInsufficientLoanToValueCapacity) {
		t.Fatalf("expected the full pledged value to exceed LAS's stricter LTV cap, got %v", err)
	}
}
