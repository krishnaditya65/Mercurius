package securitieslendingborrowing

import (
	"errors"
	"sync"
	"testing"
)

func TestLendSecurity_Success(t *testing.T) {
	desk := NewDesk()
	record, err := desk.LendSecurity("acct-001", "DEMO-EQ", 20, 10000, 50)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if record.LentQuantity != 20 {
		t.Fatalf("expected lent quantity 20, got %d", record.LentQuantity)
	}
}

func TestLendSecurity_AccumulatesAcrossCalls(t *testing.T) {
	desk := NewDesk()
	if _, err := desk.LendSecurity("acct-001", "DEMO-EQ", 20, 10000, 50); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	record, err := desk.LendSecurity("acct-001", "DEMO-EQ", 10, 10000, 50)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if record.LentQuantity != 30 {
		t.Fatalf("expected accumulated lent quantity 30, got %d", record.LentQuantity)
	}
}

func TestLendSecurity_InsufficientHoldingRejected(t *testing.T) {
	desk := NewDesk()
	_, err := desk.LendSecurity("acct-001", "DEMO-EQ", 60, 10000, 50)
	if !errors.Is(err, ErrInsufficientUnlentHoldingQuantity) {
		t.Fatalf("expected ErrInsufficientUnlentHoldingQuantity, got %v", err)
	}
}

func TestLendSecurity_CannotLendAlreadyLentPortionAgain(t *testing.T) {
	desk := NewDesk()
	if _, err := desk.LendSecurity("acct-001", "DEMO-EQ", 50, 10000, 50); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err := desk.LendSecurity("acct-001", "DEMO-EQ", 1, 10000, 50)
	if !errors.Is(err, ErrInsufficientUnlentHoldingQuantity) {
		t.Fatalf("expected ErrInsufficientUnlentHoldingQuantity, got %v", err)
	}
}

func TestLendSecurity_ZeroQuantityRejected(t *testing.T) {
	desk := NewDesk()
	_, err := desk.LendSecurity("acct-001", "DEMO-EQ", 0, 10000, 50)
	if !errors.Is(err, ErrQuantityMustBePositive) {
		t.Fatalf("expected ErrQuantityMustBePositive, got %v", err)
	}
}

func TestRecallLending_PartialAndFull(t *testing.T) {
	desk := NewDesk()
	if _, err := desk.LendSecurity("acct-001", "DEMO-EQ", 20, 10000, 50); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	record, err := desk.RecallLending("acct-001", "DEMO-EQ", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if record.LentQuantity != 15 {
		t.Fatalf("expected 15 remaining lent, got %d", record.LentQuantity)
	}
	if _, err := desk.RecallLending("acct-001", "DEMO-EQ", 15); err != nil {
		t.Fatalf("unexpected error on full recall: %v", err)
	}
	if desk.LentQuantity("acct-001", "DEMO-EQ") != 0 {
		t.Fatalf("expected zero lent quantity after full recall")
	}
}

func TestRecallLending_NoRecordFound(t *testing.T) {
	desk := NewDesk()
	_, err := desk.RecallLending("acct-001", "DEMO-EQ", 5)
	if !errors.Is(err, ErrNoLendingRecordFound) {
		t.Fatalf("expected ErrNoLendingRecordFound, got %v", err)
	}
}

func TestRecallLending_ExceedsLentQuantityRejected(t *testing.T) {
	desk := NewDesk()
	if _, err := desk.LendSecurity("acct-001", "DEMO-EQ", 20, 10000, 50); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err := desk.RecallLending("acct-001", "DEMO-EQ", 21)
	if !errors.Is(err, ErrRecallQuantityExceedsLentQuantity) {
		t.Fatalf("expected ErrRecallQuantityExceedsLentQuantity, got %v", err)
	}
}

func TestBorrowSecurity_NoPriorHoldingRequired(t *testing.T) {
	desk := NewDesk()
	record, err := desk.BorrowSecurity("acct-002", "DEMO-EQ", 30, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if record.BorrowedQuantity != 30 {
		t.Fatalf("expected borrowed quantity 30, got %d", record.BorrowedQuantity)
	}
}

func TestBorrowSecurity_ZeroQuantityRejected(t *testing.T) {
	desk := NewDesk()
	_, err := desk.BorrowSecurity("acct-002", "DEMO-EQ", 0, 10000)
	if !errors.Is(err, ErrQuantityMustBePositive) {
		t.Fatalf("expected ErrQuantityMustBePositive, got %v", err)
	}
}

func TestReturnBorrowing_PartialAndFull(t *testing.T) {
	desk := NewDesk()
	if _, err := desk.BorrowSecurity("acct-002", "DEMO-EQ", 30, 10000); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	record, err := desk.ReturnBorrowing("acct-002", "DEMO-EQ", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if record.BorrowedQuantity != 20 {
		t.Fatalf("expected 20 remaining borrowed, got %d", record.BorrowedQuantity)
	}
	if _, err := desk.ReturnBorrowing("acct-002", "DEMO-EQ", 20); err != nil {
		t.Fatalf("unexpected error on full return: %v", err)
	}
	records := desk.BorrowingRecordsForAccount("acct-002")
	if len(records) != 0 {
		t.Fatalf("expected zero active borrowing records after full return, got %+v", records)
	}
}

func TestReturnBorrowing_NoRecordFound(t *testing.T) {
	desk := NewDesk()
	_, err := desk.ReturnBorrowing("acct-002", "DEMO-EQ", 5)
	if !errors.Is(err, ErrNoBorrowingRecordFound) {
		t.Fatalf("expected ErrNoBorrowingRecordFound, got %v", err)
	}
}

func TestReturnBorrowing_ExceedsBorrowedQuantityRejected(t *testing.T) {
	desk := NewDesk()
	if _, err := desk.BorrowSecurity("acct-002", "DEMO-EQ", 10, 10000); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err := desk.ReturnBorrowing("acct-002", "DEMO-EQ", 11)
	if !errors.Is(err, ErrReturnQuantityExceedsBorrowedQuantity) {
		t.Fatalf("expected ErrReturnQuantityExceedsBorrowedQuantity, got %v", err)
	}
}

func TestLendingAndBorrowingAreIndependentLedgers(t *testing.T) {
	desk := NewDesk()
	if _, err := desk.LendSecurity("acct-001", "DEMO-EQ", 10, 10000, 50); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := desk.BorrowSecurity("acct-001", "DEMO-EQ", 5, 10000); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if desk.LentQuantity("acct-001", "DEMO-EQ") != 10 {
		t.Fatalf("expected lending record unaffected by borrowing on the same account/symbol")
	}
	borrowRecords := desk.BorrowingRecordsForAccount("acct-001")
	if borrowRecords["DEMO-EQ"].BorrowedQuantity != 5 {
		t.Fatalf("expected independent borrowing record of 5, got %+v", borrowRecords)
	}
}

// TestHandWorkedAccruedFee: notional 100000, rate 2% p.a., 365 days ->
// exactly 2000 (a full year's fee equals the flat annual rate).
func TestHandWorkedAccruedFee_FullYear(t *testing.T) {
	fee := CalculateIllustrativeAccruedFee(100000, 0.02, 365)
	if fee != 2000 {
		t.Fatalf("expected fee 2000, got %d", fee)
	}
}

// TestHandWorkedAccruedFee_Half: notional 100000, rate 5% p.a., ~182.5
// days (365/2) -> should be very close to half the annual fee (5000/2=2500).
func TestHandWorkedAccruedFee_HalfYear(t *testing.T) {
	fee := CalculateIllustrativeAccruedFee(100000, 0.05, 182)
	// 100000*0.05*182/365 = 2493.15... -> rounds to 2493
	if fee != 2493 {
		t.Fatalf("expected fee 2493, got %d", fee)
	}
}

func TestCalculateIllustrativeAccruedFee_NonPositiveInputsReturnZero(t *testing.T) {
	if fee := CalculateIllustrativeAccruedFee(0, 0.02, 30); fee != 0 {
		t.Fatalf("expected zero fee for zero notional, got %d", fee)
	}
	if fee := CalculateIllustrativeAccruedFee(100000, 0.02, 0); fee != 0 {
		t.Fatalf("expected zero fee for zero days held, got %d", fee)
	}
	if fee := CalculateIllustrativeAccruedFee(-100, 0.02, 30); fee != 0 {
		t.Fatalf("expected zero fee for negative notional, got %d", fee)
	}
}

func TestIllustrativeRateAccessors(t *testing.T) {
	if IllustrativeAnnualizedLendingFeePercent() != 0.02 {
		t.Fatalf("expected lending rate 0.02, got %v", IllustrativeAnnualizedLendingFeePercent())
	}
	if IllustrativeAnnualizedBorrowingFeePercent() != 0.05 {
		t.Fatalf("expected borrowing rate 0.05, got %v", IllustrativeAnnualizedBorrowingFeePercent())
	}
}

func TestDesk_ConcurrencyLendAndBorrow(t *testing.T) {
	desk := NewDesk()
	var waitGroup sync.WaitGroup
	successCount := 0
	var mutexGuardingCounter sync.Mutex

	for i := 0; i < 100; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, err := desk.LendSecurity("acct-shared", "DEMO-EQ", 1, 10000, 50)
			if err == nil {
				mutexGuardingCounter.Lock()
				successCount++
				mutexGuardingCounter.Unlock()
			}
		}()
	}
	waitGroup.Wait()

	if successCount != 50 {
		t.Fatalf("expected exactly 50 successful lends against a 50-share holding, got %d", successCount)
	}
	if desk.LentQuantity("acct-shared", "DEMO-EQ") != 50 {
		t.Fatalf("expected exactly 50 lent quantity, got %d", desk.LentQuantity("acct-shared", "DEMO-EQ"))
	}
}
