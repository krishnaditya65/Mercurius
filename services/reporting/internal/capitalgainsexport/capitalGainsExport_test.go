package capitalgainsexport

import (
	"bytes"
	"encoding/csv"
	"testing"
	"time"

	"mercurius/reporting/internal/capitalgains"
)

func TestWriteCsvProducesCorrectlyParseableRowsAndTotals(t *testing.T) {
	summary := capitalgains.Summary{
		AccountIdentifier: "acct-001",
		FinancialYear:     "2024-25",
		RealizedGains: []capitalgains.RealizedGain{
			{
				InstrumentSymbol:         "TATASTEEL",
				Quantity:                 100,
				AcquiredAtTime:           time.Date(2024, 1, 10, 0, 0, 0, 0, time.UTC),
				SoldAtTime:               time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC),
				BuyPriceInMinorUnits:     10000,
				SellPriceInMinorUnits:    15000,
				HoldingPeriodDays:        388,
				GainType:                 capitalgains.GainTypeLongTerm,
				RealizedGainInMinorUnits: 500000,
			},
			{
				InstrumentSymbol:         "TATASTEEL",
				Quantity:                 50,
				AcquiredAtTime:           time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC),
				SoldAtTime:               time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC),
				BuyPriceInMinorUnits:     12000,
				SellPriceInMinorUnits:    15000,
				HoldingPeriodDays:        231,
				GainType:                 capitalgains.GainTypeShortTerm,
				RealizedGainInMinorUnits: 150000,
			},
		},
		ShortTermTotalInMinorUnits: 150000,
		LongTermTotalInMinorUnits:  500000,
	}

	csvBytes, err := WriteCsv(summary)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	reader := csv.NewReader(bytes.NewReader(csvBytes))
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("produced CSV did not parse: %v", err)
	}

	// header + 2 data rows + 3 totals rows
	if len(records) != 6 {
		t.Fatalf("expected 6 CSV rows, got %d: %+v", len(records), records)
	}

	if len(records[0]) != len(ColumnHeaders) {
		t.Fatalf("header row length mismatch: %v", records[0])
	}
	for i, header := range ColumnHeaders {
		if records[0][i] != header {
			t.Errorf("header[%d] = %q, want %q", i, records[0][i], header)
		}
	}

	if records[1][0] != "TATASTEEL" || records[1][5] != "LTCG" || records[1][8] != "500000" {
		t.Errorf("unexpected first data row: %v", records[1])
	}
	if records[2][0] != "TATASTEEL" || records[2][5] != "STCG" || records[2][8] != "150000" {
		t.Errorf("unexpected second data row: %v", records[2])
	}

	if records[4][0] != "TOTAL_STCG" || records[4][8] != "150000" {
		t.Errorf("unexpected STCG totals row: %v", records[4])
	}
	if records[5][0] != "TOTAL_LTCG" || records[5][8] != "500000" {
		t.Errorf("unexpected LTCG totals row: %v", records[5])
	}
}

func TestWriteCsvWithNoRealizedGainsStillProducesHeaderAndZeroTotals(t *testing.T) {
	summary := capitalgains.Summary{AccountIdentifier: "acct-001", FinancialYear: "2024-25"}
	csvBytes, err := WriteCsv(summary)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	reader := csv.NewReader(bytes.NewReader(csvBytes))
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("produced CSV did not parse: %v", err)
	}
	if len(records) != 4 { // header + total + stcg + ltcg
		t.Fatalf("expected 4 rows, got %d: %+v", len(records), records)
	}
}

func TestSuggestedFilenameIsStableAndDescriptive(t *testing.T) {
	filename := SuggestedFilename("acct-001", "2024-25", time.Date(2025, 8, 14, 0, 0, 0, 0, time.UTC))
	want := "capital-gains-acct-001-FY2024-25-20250814.csv"
	if filename != want {
		t.Errorf("got %q, want %q", filename, want)
	}
}
