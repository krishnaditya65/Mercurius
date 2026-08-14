package aisreconciliation

import (
	"testing"

	"mercurius/reporting/internal/capitalgains"
)

func TestBuildPlatformAisRecordAggregatesByInstrumentAndCategory(t *testing.T) {
	realizedGains := []capitalgains.RealizedGain{
		{InstrumentSymbol: "INFY", GainType: capitalgains.GainTypeShortTerm, RealizedGainInMinorUnits: 1000},
		{InstrumentSymbol: "INFY", GainType: capitalgains.GainTypeShortTerm, RealizedGainInMinorUnits: 500},
		{InstrumentSymbol: "TCS", GainType: capitalgains.GainTypeLongTerm, RealizedGainInMinorUnits: 20000},
	}
	dividends := map[string]int64{"ITC": 3000}

	record := BuildPlatformAisRecord("acct-001", "2024-25", realizedGains, dividends)

	if record.Source != "MERCURIUS_PLATFORM_COMPUTED" {
		t.Errorf("unexpected source: %s", record.Source)
	}
	if len(record.Entries) != 3 {
		t.Fatalf("expected 3 aggregated entries, got %d: %+v", len(record.Entries), record.Entries)
	}
	for _, entry := range record.Entries {
		if entry.Category == CategoryShortTermCapitalGains && entry.InstrumentSymbol == "INFY" && entry.AmountInMinorUnits != 1500 {
			t.Errorf("expected INFY STCG aggregated to 1500, got %d", entry.AmountInMinorUnits)
		}
	}
}

func TestReconcileFindsNoDiscrepanciesForIdenticalRecords(t *testing.T) {
	record := AisRecord{
		AccountIdentifier: "acct-001",
		FinancialYear:     "2024-25",
		Source:            "MERCURIUS_PLATFORM_COMPUTED",
		Entries: []AisEntry{
			{Category: CategoryShortTermCapitalGains, InstrumentSymbol: "INFY", AmountInMinorUnits: 1500},
		},
	}
	mock := BuildIllustrativeMockAisRecord(record)

	report := Reconcile(record, mock)
	if !report.IsFullyReconciled {
		t.Fatalf("expected full reconciliation, got discrepancies: %+v", report.Discrepancies)
	}
}

func TestReconcileDetectsAmountMismatchMissingInAisAndMissingInPlatform(t *testing.T) {
	platform := AisRecord{
		AccountIdentifier: "acct-001",
		FinancialYear:     "2024-25",
		Source:            "MERCURIUS_PLATFORM_COMPUTED",
		Entries: []AisEntry{
			{Category: CategoryShortTermCapitalGains, InstrumentSymbol: "INFY", AmountInMinorUnits: 1500},
			{Category: CategoryLongTermCapitalGains, InstrumentSymbol: "TCS", AmountInMinorUnits: 20000},
		},
	}

	extraEntry := AisEntry{Category: CategoryDividendIncome, InstrumentSymbol: "WIPRO", AmountInMinorUnits: 750}
	mock := BuildIllustrativeMockAisRecord(platform,
		MockDiscrepancy{PerturbCategoryAndInstrument: [2]string{CategoryShortTermCapitalGains, "INFY"}, PerturbByMinorUnits: 200},
		MockDiscrepancy{DropCategoryAndInstrument: [2]string{CategoryLongTermCapitalGains, "TCS"}},
		MockDiscrepancy{AddExtraEntry: &extraEntry},
	)

	report := Reconcile(platform, mock)
	if report.IsFullyReconciled {
		t.Fatal("expected discrepancies, got none")
	}
	if len(report.Discrepancies) != 3 {
		t.Fatalf("expected 3 discrepancies, got %d: %+v", len(report.Discrepancies), report.Discrepancies)
	}

	var sawMismatch, sawMissingInAis, sawMissingInPlatform bool
	for _, d := range report.Discrepancies {
		switch d.Type {
		case DiscrepancyTypeAmountMismatch:
			sawMismatch = true
			if d.Category != CategoryShortTermCapitalGains || d.InstrumentSymbol != "INFY" {
				t.Errorf("unexpected mismatch entry: %+v", d)
			}
			// platform=1500, ais=1500+200=1700, delta=1500-1700=-200
			if d.DeltaInMinorUnits != -200 {
				t.Errorf("expected delta -200, got %d", d.DeltaInMinorUnits)
			}
		case DiscrepancyTypeMissingInAis:
			sawMissingInAis = true
			if d.Category != CategoryLongTermCapitalGains || d.InstrumentSymbol != "TCS" {
				t.Errorf("unexpected missing-in-ais entry: %+v", d)
			}
		case DiscrepancyTypeMissingInPlatform:
			sawMissingInPlatform = true
			if d.Category != CategoryDividendIncome || d.InstrumentSymbol != "WIPRO" {
				t.Errorf("unexpected missing-in-platform entry: %+v", d)
			}
		}
	}
	if !sawMismatch || !sawMissingInAis || !sawMissingInPlatform {
		t.Fatalf("expected all 3 discrepancy types, got %+v", report.Discrepancies)
	}
}
