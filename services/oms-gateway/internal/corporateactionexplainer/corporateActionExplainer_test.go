package corporateactionexplainer

import (
	"strings"
	"sync"
	"testing"
)

func TestExplain_RejectsUnknownActionType(t *testing.T) {
	_, err := Explain(AdjustmentEvent{
		ActionType: "BOGUS", InstrumentSymbol: "X", QuantityBefore: 10, QuantityAfter: 20,
	})
	if err == nil {
		t.Fatalf("expected error for unknown action type")
	}
}

func TestExplain_RejectsNonPositiveQuantityBefore(t *testing.T) {
	_, err := Explain(AdjustmentEvent{
		ActionType: ActionTypeStockSplit, InstrumentSymbol: "X", QuantityBefore: 0, QuantityAfter: 20,
	})
	if err == nil {
		t.Fatalf("expected error for zero quantityBefore")
	}
}

func TestExplain_RejectsNonPositiveQuantityAfter(t *testing.T) {
	_, err := Explain(AdjustmentEvent{
		ActionType: ActionTypeStockSplit, InstrumentSymbol: "X", QuantityBefore: 10, QuantityAfter: 0,
	})
	if err == nil {
		t.Fatalf("expected error for zero quantityAfter")
	}
}

func TestExplain_StockSplitHandWorked(t *testing.T) {
	// 1:2 split: 10 shares @ avg ₹200.00 (20000 paise) -> 20 shares @ avg
	// ₹100.00 (10000 paise). Ratio 2.00x.
	explanation, err := Explain(AdjustmentEvent{
		ActionType:                     ActionTypeStockSplit,
		InstrumentSymbol:               "DEMO-EQ",
		QuantityBefore:                 10,
		QuantityAfter:                  20,
		AveragePriceBeforeInMinorUnits: 20000,
		AveragePriceAfterInMinorUnits:  10000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(explanation, "stock split") {
		t.Fatalf("expected 'stock split' in explanation, got %q", explanation)
	}
	if !strings.Contains(explanation, "10 shares") || !strings.Contains(explanation, "20 shares") {
		t.Fatalf("expected real before/after quantities in explanation, got %q", explanation)
	}
	if !strings.Contains(explanation, "2.00x") {
		t.Fatalf("expected exact 2.00x ratio in explanation, got %q", explanation)
	}
	if !strings.Contains(explanation, "200.00") || !strings.Contains(explanation, "100.00") {
		t.Fatalf("expected real before/after average prices in explanation, got %q", explanation)
	}
}

func TestExplain_BonusIssueLabel(t *testing.T) {
	explanation, _ := Explain(AdjustmentEvent{
		ActionType: ActionTypeBonusIssue, InstrumentSymbol: "X",
		QuantityBefore: 10, QuantityAfter: 15,
		AveragePriceBeforeInMinorUnits: 10000, AveragePriceAfterInMinorUnits: 10000,
	})
	if !strings.Contains(explanation, "bonus issue") {
		t.Fatalf("expected 'bonus issue' in explanation, got %q", explanation)
	}
}

func TestExplain_MergerLabel(t *testing.T) {
	explanation, _ := Explain(AdjustmentEvent{
		ActionType: ActionTypeMerger, InstrumentSymbol: "X",
		QuantityBefore: 10, QuantityAfter: 7,
		AveragePriceBeforeInMinorUnits: 10000, AveragePriceAfterInMinorUnits: 14285,
	})
	if !strings.Contains(explanation, "merger") {
		t.Fatalf("expected 'merger' in explanation, got %q", explanation)
	}
}

func TestExplain_UnchangedPriceOmitsPriceChangeLanguage(t *testing.T) {
	// Bonus issue with the SAME average price supplied before/after --
	// should use the "your average price ... is unchanged" phrasing, not
	// the two-price phrasing.
	explanation, _ := Explain(AdjustmentEvent{
		ActionType: ActionTypeBonusIssue, InstrumentSymbol: "X",
		QuantityBefore: 10, QuantityAfter: 15,
		AveragePriceBeforeInMinorUnits: 10000, AveragePriceAfterInMinorUnits: 10000,
	})
	if !strings.Contains(explanation, "is unchanged") {
		t.Fatalf("expected 'is unchanged' phrasing when price is identical, got %q", explanation)
	}
}

func TestExplain_RatioComputedExactly(t *testing.T) {
	// 3-for-1: 100 -> 300, ratio 3.00x.
	explanation, _ := Explain(AdjustmentEvent{
		ActionType: ActionTypeStockSplit, InstrumentSymbol: "X",
		QuantityBefore: 100, QuantityAfter: 300,
		AveragePriceBeforeInMinorUnits: 30000, AveragePriceAfterInMinorUnits: 10000,
	})
	if !strings.Contains(explanation, "3.00x") {
		t.Fatalf("expected exact 3.00x ratio, got %q", explanation)
	}
}

func TestLog_RecordAdjustmentAppendsAndReturns(t *testing.T) {
	log := NewLog()
	logged, err := log.RecordAdjustment(AdjustmentEvent{
		ActionType: ActionTypeStockSplit, ClientAccountIdentifier: "acct-1", InstrumentSymbol: "X",
		QuantityBefore: 10, QuantityAfter: 20,
		AveragePriceBeforeInMinorUnits: 20000, AveragePriceAfterInMinorUnits: 10000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if logged.OneLineExplanation == "" {
		t.Fatalf("expected non-empty explanation")
	}
	if logged.RecordedAtTime.IsZero() {
		t.Fatalf("expected RecordedAtTime to be set")
	}
}

func TestLog_RecordAdjustmentRejectsInvalidEvent(t *testing.T) {
	log := NewLog()
	_, err := log.RecordAdjustment(AdjustmentEvent{ActionType: "BOGUS"})
	if err == nil {
		t.Fatalf("expected error for invalid event")
	}
	if len(log.AllEntries()) != 0 {
		t.Fatalf("expected invalid event to not be recorded")
	}
}

func TestLog_EntriesForAccountFiltersCorrectly(t *testing.T) {
	log := NewLog()
	log.RecordAdjustment(AdjustmentEvent{
		ActionType: ActionTypeStockSplit, ClientAccountIdentifier: "acct-1", InstrumentSymbol: "X",
		QuantityBefore: 10, QuantityAfter: 20, AveragePriceBeforeInMinorUnits: 20000, AveragePriceAfterInMinorUnits: 10000,
	})
	log.RecordAdjustment(AdjustmentEvent{
		ActionType: ActionTypeBonusIssue, ClientAccountIdentifier: "acct-2", InstrumentSymbol: "Y",
		QuantityBefore: 5, QuantityAfter: 10, AveragePriceBeforeInMinorUnits: 20000, AveragePriceAfterInMinorUnits: 10000,
	})
	acct1Entries := log.EntriesForAccount("acct-1")
	if len(acct1Entries) != 1 {
		t.Fatalf("expected 1 entry for acct-1, got %d", len(acct1Entries))
	}
	if acct1Entries[0].ClientAccountIdentifier != "acct-1" {
		t.Fatalf("expected entry to belong to acct-1")
	}
}

func TestLog_AllEntriesReturnsEverything(t *testing.T) {
	log := NewLog()
	log.RecordAdjustment(AdjustmentEvent{
		ActionType: ActionTypeStockSplit, ClientAccountIdentifier: "acct-1", InstrumentSymbol: "X",
		QuantityBefore: 10, QuantityAfter: 20, AveragePriceBeforeInMinorUnits: 20000, AveragePriceAfterInMinorUnits: 10000,
	})
	log.RecordAdjustment(AdjustmentEvent{
		ActionType: ActionTypeBonusIssue, ClientAccountIdentifier: "acct-2", InstrumentSymbol: "Y",
		QuantityBefore: 5, QuantityAfter: 10, AveragePriceBeforeInMinorUnits: 20000, AveragePriceAfterInMinorUnits: 10000,
	})
	if len(log.AllEntries()) != 2 {
		t.Fatalf("expected 2 entries total, got %d", len(log.AllEntries()))
	}
}

func TestLog_ReturnsDefensiveCopy(t *testing.T) {
	log := NewLog()
	log.RecordAdjustment(AdjustmentEvent{
		ActionType: ActionTypeStockSplit, ClientAccountIdentifier: "acct-1", InstrumentSymbol: "X",
		QuantityBefore: 10, QuantityAfter: 20, AveragePriceBeforeInMinorUnits: 20000, AveragePriceAfterInMinorUnits: 10000,
	})
	entries := log.AllEntries()
	entries[0].InstrumentSymbol = "MUTATED"
	freshEntries := log.AllEntries()
	if freshEntries[0].InstrumentSymbol == "MUTATED" {
		t.Fatalf("expected AllEntries to return a defensive copy")
	}
}

func TestConcurrentRecordAdjustment(t *testing.T) {
	log := NewLog()
	var waitGroup sync.WaitGroup
	for i := 0; i < 50; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			log.RecordAdjustment(AdjustmentEvent{
				ActionType: ActionTypeStockSplit, ClientAccountIdentifier: "acct-1", InstrumentSymbol: "X",
				QuantityBefore: 10, QuantityAfter: 20, AveragePriceBeforeInMinorUnits: 20000, AveragePriceAfterInMinorUnits: 10000,
			})
		}()
	}
	waitGroup.Wait()
	if len(log.AllEntries()) != 50 {
		t.Fatalf("expected 50 entries after concurrent recording, got %d", len(log.AllEntries()))
	}
}
