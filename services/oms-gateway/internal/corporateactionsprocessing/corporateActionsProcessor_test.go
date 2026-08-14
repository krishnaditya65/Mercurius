package corporateactionsprocessing

import (
	"errors"
	"testing"
	"time"
)

func TestStockSplitDoublesQuantityAndPreservesTotalCostBasis(t *testing.T) {
	book := NewHoldingsBook()
	// 10 shares bought for a total of ₹10,000.00 (avg ₹1000.00/share).
	if _, err := book.SeedHolding("acct-001", "RELIANCE", 10, 1_000_000); err != nil {
		t.Fatalf("unexpected seed error: %v", err)
	}

	processed, err := book.ApplyStockSplit("acct-001", "RELIANCE", 2, 1, time.Now())
	if err != nil {
		t.Fatalf("unexpected split error: %v", err)
	}

	if processed.HoldingAfter.QuantityHeld != 20 {
		t.Fatalf("expected 20 shares after a 2:1 split, got %d", processed.HoldingAfter.QuantityHeld)
	}
	if processed.HoldingAfter.TotalCostBasisInMinorUnits != 1_000_000 {
		t.Fatalf("expected total cost basis unchanged at 1,000,000 minor units, got %d", processed.HoldingAfter.TotalCostBasisInMinorUnits)
	}
	// avg cost per share must exactly halve: 1000.00 -> 500.00.
	if avg := processed.HoldingAfter.AverageCostPerShareInMinorUnits(); avg != 50_000 {
		t.Fatalf("expected avg cost per share to halve to 50,000 minor units, got %d", avg)
	}

	stored, exists := book.GetHolding("acct-001", "RELIANCE")
	if !exists || stored.QuantityHeld != 20 || stored.TotalCostBasisInMinorUnits != 1_000_000 {
		t.Fatalf("expected stored holding to reflect the split, got %+v exists=%v", stored, exists)
	}
}

func TestThreeForOneSplitExactMath(t *testing.T) {
	book := NewHoldingsBook()
	// 9 shares, total cost basis ₹9,900.00 (avg ₹1,100.00/share).
	book.SeedHolding("acct-002", "TCS", 9, 990_000)

	processed, err := book.ApplyStockSplit("acct-002", "TCS", 3, 1, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed.HoldingAfter.QuantityHeld != 27 {
		t.Fatalf("expected 27 shares after 3:1 split, got %d", processed.HoldingAfter.QuantityHeld)
	}
	if processed.HoldingAfter.TotalCostBasisInMinorUnits != 990_000 {
		t.Fatalf("expected cost basis unchanged at 990,000, got %d", processed.HoldingAfter.TotalCostBasisInMinorUnits)
	}
	if avg := processed.HoldingAfter.AverageCostPerShareInMinorUnits(); avg != 36_666 {
		t.Fatalf("expected avg 36,666 (990000/27 integer division), got %d", avg)
	}
}

func TestBonusIssueOneForOneDoublesQuantityKeepsCostBasis(t *testing.T) {
	book := NewHoldingsBook()
	// 10 shares, total cost basis ₹10,000.00.
	book.SeedHolding("acct-001", "INFY", 10, 1_000_000)

	processed, err := book.ApplyBonusIssue("acct-001", "INFY", 1, 1, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if processed.HoldingAfter.QuantityHeld != 20 {
		t.Fatalf("expected 20 shares after a 1:1 bonus issue (10 bonus + 10 original), got %d", processed.HoldingAfter.QuantityHeld)
	}
	if processed.HoldingAfter.TotalCostBasisInMinorUnits != 1_000_000 {
		t.Fatalf("expected total cost basis unchanged at 1,000,000, got %d", processed.HoldingAfter.TotalCostBasisInMinorUnits)
	}
	if avg := processed.HoldingAfter.AverageCostPerShareInMinorUnits(); avg != 50_000 {
		t.Fatalf("expected avg cost to halve to 50,000, got %d", avg)
	}
}

func TestBonusIssueTwoForOneTriplesQuantity(t *testing.T) {
	book := NewHoldingsBook()
	// 5 shares, total cost basis ₹5,000.00. A 2:1 bonus means 2 bonus
	// shares per share held -- 10 bonus shares added to the 5 original,
	// tripling total quantity to 15.
	book.SeedHolding("acct-003", "HDFC", 5, 500_000)

	processed, err := book.ApplyBonusIssue("acct-003", "HDFC", 2, 1, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed.HoldingAfter.QuantityHeld != 15 {
		t.Fatalf("expected 15 shares after a 2:1 bonus issue, got %d", processed.HoldingAfter.QuantityHeld)
	}
	if processed.HoldingAfter.TotalCostBasisInMinorUnits != 500_000 {
		t.Fatalf("expected cost basis unchanged at 500,000, got %d", processed.HoldingAfter.TotalCostBasisInMinorUnits)
	}
}

func TestCashDividendComputesAmountAndLeavesHoldingUntouched(t *testing.T) {
	book := NewHoldingsBook()
	// 10 shares, total cost basis ₹10,000.00.
	book.SeedHolding("acct-001", "ITC", 10, 1_000_000)

	// ₹5.00/share dividend = 500 minor units/share.
	processed, err := book.ComputeCashDividendAmount("acct-001", "ITC", 500, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if processed.CashCreditedInMinorUnits != 5_000 {
		t.Fatalf("expected total dividend of 5,000 minor units (10 shares * 500), got %d", processed.CashCreditedInMinorUnits)
	}
	if processed.HoldingAfter.QuantityHeld != 10 {
		t.Fatalf("expected quantity unchanged at 10 after a cash dividend, got %d", processed.HoldingAfter.QuantityHeld)
	}
	if processed.HoldingAfter.TotalCostBasisInMinorUnits != 1_000_000 {
		t.Fatalf("expected cost basis unchanged at 1,000,000 after a cash dividend, got %d", processed.HoldingAfter.TotalCostBasisInMinorUnits)
	}

	stored, _ := book.GetHolding("acct-001", "ITC")
	if stored.QuantityHeld != 10 || stored.TotalCostBasisInMinorUnits != 1_000_000 {
		t.Fatalf("expected stored holding to be completely untouched by dividend, got %+v", stored)
	}
}

func TestMergerConvertsQuantityAndCarriesTotalCostBasis(t *testing.T) {
	book := NewHoldingsBook()
	// 10 shares of OLDCO, total cost basis ₹10,000.00. Merger ratio: 2
	// old shares -> 1 new NEWCO share.
	book.SeedHolding("acct-001", "OLDCO", 10, 1_000_000)

	processed, err := book.ApplyMerger("acct-001", "OLDCO", "NEWCO", 1, 2, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if processed.HoldingAfter.QuantityHeld != 5 {
		t.Fatalf("expected 5 NEWCO shares after a 1:2 exchange of 10 OLDCO shares, got %d", processed.HoldingAfter.QuantityHeld)
	}
	if processed.HoldingAfter.TotalCostBasisInMinorUnits != 1_000_000 {
		t.Fatalf("expected the full 1,000,000 cost basis to carry over into NEWCO, got %d", processed.HoldingAfter.TotalCostBasisInMinorUnits)
	}
	if processed.HoldingAfter.InstrumentSymbol != "NEWCO" {
		t.Fatalf("expected holding after to be for NEWCO, got %s", processed.HoldingAfter.InstrumentSymbol)
	}

	// The old holding must be gone entirely.
	if _, exists := book.GetHolding("acct-001", "OLDCO"); exists {
		t.Fatalf("expected OLDCO holding to be removed after the merger")
	}
	newHolding, exists := book.GetHolding("acct-001", "NEWCO")
	if !exists || newHolding.QuantityHeld != 5 || newHolding.TotalCostBasisInMinorUnits != 1_000_000 {
		t.Fatalf("expected stored NEWCO holding to reflect the merger, got %+v exists=%v", newHolding, exists)
	}
}

func TestMergerAddsOntoExistingTargetHoldingRatherThanOverwriting(t *testing.T) {
	book := NewHoldingsBook()
	book.SeedHolding("acct-001", "OLDCO", 10, 1_000_000) // 10 shares, ₹10,000 cost basis.
	book.SeedHolding("acct-001", "NEWCO", 3, 600_000)    // account already independently owns 3 NEWCO, ₹6,000 cost basis.

	processed, err := book.ApplyMerger("acct-001", "OLDCO", "NEWCO", 1, 2, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 10 OLDCO -> 5 NEWCO, added to the existing 3 NEWCO = 8 total.
	if processed.HoldingAfter.QuantityHeld != 8 {
		t.Fatalf("expected merged quantity of 8 (3 existing + 5 converted), got %d", processed.HoldingAfter.QuantityHeld)
	}
	// 600,000 existing + 1,000,000 carried over = 1,600,000.
	if processed.HoldingAfter.TotalCostBasisInMinorUnits != 1_600_000 {
		t.Fatalf("expected merged cost basis of 1,600,000, got %d", processed.HoldingAfter.TotalCostBasisInMinorUnits)
	}
}

func TestFractionalExchangeRatioIsRejectedNotSilentlyTruncated(t *testing.T) {
	book := NewHoldingsBook()
	book.SeedHolding("acct-001", "OLDCO", 7, 700_000) // 7 shares does not divide evenly by 2.

	_, err := book.ApplyMerger("acct-001", "OLDCO", "NEWCO", 1, 2, time.Now())
	if !errors.Is(err, ErrExchangeRatioProducesFractionalShares) {
		t.Fatalf("expected ErrExchangeRatioProducesFractionalShares, got %v", err)
	}

	// The source holding must be completely untouched by the rejected merger.
	stored, exists := book.GetHolding("acct-001", "OLDCO")
	if !exists || stored.QuantityHeld != 7 {
		t.Fatalf("expected OLDCO holding untouched after a rejected merger, got %+v exists=%v", stored, exists)
	}
}

func TestApplyingActionWithNoExistingHoldingFails(t *testing.T) {
	book := NewHoldingsBook()

	if _, err := book.ApplyStockSplit("acct-999", "GHOST", 2, 1, time.Now()); !errors.Is(err, ErrNoExistingHolding) {
		t.Fatalf("expected ErrNoExistingHolding for split, got %v", err)
	}
	if _, err := book.ApplyBonusIssue("acct-999", "GHOST", 1, 1, time.Now()); !errors.Is(err, ErrNoExistingHolding) {
		t.Fatalf("expected ErrNoExistingHolding for bonus, got %v", err)
	}
	if _, err := book.ApplyMerger("acct-999", "GHOST", "NEWCO", 1, 1, time.Now()); !errors.Is(err, ErrNoExistingHolding) {
		t.Fatalf("expected ErrNoExistingHolding for merger, got %v", err)
	}
	if _, err := book.ComputeCashDividendAmount("acct-999", "GHOST", 100, time.Now()); !errors.Is(err, ErrNoExistingHolding) {
		t.Fatalf("expected ErrNoExistingHolding for dividend, got %v", err)
	}
}

func TestNonPositiveInputsAreRejected(t *testing.T) {
	book := NewHoldingsBook()
	book.SeedHolding("acct-001", "X", 10, 1000)

	if _, err := book.ApplyStockSplit("acct-001", "X", 0, 1, time.Now()); !errors.Is(err, ErrNonPositiveRatio) {
		t.Fatalf("expected ErrNonPositiveRatio, got %v", err)
	}
	if _, err := book.ApplyStockSplit("acct-001", "X", 2, 0, time.Now()); !errors.Is(err, ErrNonPositiveRatio) {
		t.Fatalf("expected ErrNonPositiveRatio, got %v", err)
	}
	if _, err := book.ComputeCashDividendAmount("acct-001", "X", 0, time.Now()); !errors.Is(err, ErrNonPositiveDividendPerShare) {
		t.Fatalf("expected ErrNonPositiveDividendPerShare, got %v", err)
	}
	if _, err := book.ApplyMerger("acct-001", "X", "", 1, 1, time.Now()); !errors.Is(err, ErrEmptyTargetInstrument) {
		t.Fatalf("expected ErrEmptyTargetInstrument, got %v", err)
	}
	if _, err := book.SeedHolding("acct-001", "Y", 0, 100); !errors.Is(err, ErrNonPositiveSeedQuantity) {
		t.Fatalf("expected ErrNonPositiveSeedQuantity, got %v", err)
	}
}

func TestValidateActionType(t *testing.T) {
	for _, valid := range []ActionType{ActionTypeStockSplit, ActionTypeBonusIssue, ActionTypeMerger, ActionTypeCashDividend} {
		if err := ValidateActionType(valid); err != nil {
			t.Fatalf("expected %s to be valid, got %v", valid, err)
		}
	}
	if err := ValidateActionType("NOT_REAL"); !errors.Is(err, ErrUnknownActionType) {
		t.Fatalf("expected ErrUnknownActionType, got %v", err)
	}
}

func TestProcessedActionsForAccountReturnsFullAuditTrail(t *testing.T) {
	book := NewHoldingsBook()
	book.SeedHolding("acct-001", "WIPRO", 10, 1_000_000)

	book.ApplyStockSplit("acct-001", "WIPRO", 2, 1, time.Now())
	book.ComputeCashDividendAmount("acct-001", "WIPRO", 100, time.Now())

	trail := book.ProcessedActionsForAccount("acct-001")
	if len(trail) != 2 {
		t.Fatalf("expected 2 processed actions in the audit trail, got %d", len(trail))
	}
	if trail[0].ActionType != ActionTypeStockSplit || trail[1].ActionType != ActionTypeCashDividend {
		t.Fatalf("expected audit trail in order [split, dividend], got %v then %v", trail[0].ActionType, trail[1].ActionType)
	}

	otherAccountTrail := book.ProcessedActionsForAccount("acct-002")
	if len(otherAccountTrail) != 0 {
		t.Fatalf("expected no audit entries for an unrelated account, got %d", len(otherAccountTrail))
	}
}
