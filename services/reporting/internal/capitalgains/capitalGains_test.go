package capitalgains

import (
	"testing"
	"time"

	"mercurius/reporting/internal/filltrail"
)

func mustParseDate(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		t.Fatalf("bad test date %q: %v", value, err)
	}
	return parsed
}

// TestFifoHandWorkedExampleTwoLotsPartialSellStcgLtcgSplit is the
// hand-worked example required for this build:
//
//	Buy lot 1: 2024-01-10, qty 100 @ ₹100.00 (10000 paise)
//	Buy lot 2: 2024-06-15, qty 100 @ ₹120.00 (12000 paise)
//	Sell:      2025-02-01, qty 150 @ ₹150.00 (15000 paise)
//
// FIFO must consume lot 1 fully (100 units) and 50 units of lot 2,
// leaving 50 units of lot 2 still open (a genuine partial sell).
//
// Lot 1 match: acquired 2024-01-10, sold 2025-02-01 — more than 12
// months later (12-month cutoff is 2025-01-10, sold after that) ->
// LTCG. Gain = (15000-10000) * 100 = 500000 paise = ₹5000.00.
//
// Lot 2 partial match: acquired 2024-06-15, sold 2025-02-01 — 12-month
// cutoff is 2025-06-15, sold BEFORE that -> STCG. Gain =
// (15000-12000) * 50 = 150000 paise = ₹1500.00.
//
// Expected FY2024-25 (Apr 2024 - Mar 2025, the sell falls in this FY)
// aggregation: STCG total = 150000 paise, LTCG total = 500000 paise.
func TestFifoHandWorkedExampleTwoLotsPartialSellStcgLtcgSplit(t *testing.T) {
	const account = "acct-001"
	const instrument = "TATASTEEL"

	fills := []filltrail.Fill{
		{
			AccountIdentifier: account,
			InstrumentSymbol:  instrument,
			Side:              filltrail.SideBuy,
			Quantity:          100,
			PriceInMinorUnits: 10000,
			ExecutedAtTime:    mustParseDate(t, "2024-01-10"),
		},
		{
			AccountIdentifier: account,
			InstrumentSymbol:  instrument,
			Side:              filltrail.SideBuy,
			Quantity:          100,
			PriceInMinorUnits: 12000,
			ExecutedAtTime:    mustParseDate(t, "2024-06-15"),
		},
		{
			AccountIdentifier: account,
			InstrumentSymbol:  instrument,
			Side:              filltrail.SideSell,
			Quantity:          150,
			PriceInMinorUnits: 15000,
			ExecutedAtTime:    mustParseDate(t, "2025-02-01"),
		},
	}

	realizedGains, err := ComputeFifoRealizedGains(fills)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(realizedGains) != 2 {
		t.Fatalf("expected 2 realized-gain matches (one per consumed lot), got %d: %+v", len(realizedGains), realizedGains)
	}

	first, second := realizedGains[0], realizedGains[1]

	if first.Quantity != 100 || first.GainType != GainTypeLongTerm || first.RealizedGainInMinorUnits != 500000 {
		t.Errorf("lot 1 match: expected qty=100 LTCG gain=500000, got qty=%d type=%s gain=%d", first.Quantity, first.GainType, first.RealizedGainInMinorUnits)
	}
	if !first.AcquiredAtTime.Equal(mustParseDate(t, "2024-01-10")) {
		t.Errorf("lot 1 match acquiredAtTime wrong: %v", first.AcquiredAtTime)
	}

	if second.Quantity != 50 || second.GainType != GainTypeShortTerm || second.RealizedGainInMinorUnits != 150000 {
		t.Errorf("lot 2 partial match: expected qty=50 STCG gain=150000, got qty=%d type=%s gain=%d", second.Quantity, second.GainType, second.RealizedGainInMinorUnits)
	}
	if !second.AcquiredAtTime.Equal(mustParseDate(t, "2024-06-15")) {
		t.Errorf("lot 2 match acquiredAtTime wrong: %v", second.AcquiredAtTime)
	}

	fyStart, fyEnd, err := IndianFinancialYearRange("2024-25")
	if err != nil {
		t.Fatalf("unexpected FY parse error: %v", err)
	}
	summary := AggregateForFinancialYear(account, "2024-25", realizedGains, fyStart, fyEnd)

	if summary.ShortTermTotalInMinorUnits != 150000 {
		t.Errorf("expected STCG total 150000 paise, got %d", summary.ShortTermTotalInMinorUnits)
	}
	if summary.LongTermTotalInMinorUnits != 500000 {
		t.Errorf("expected LTCG total 500000 paise, got %d", summary.LongTermTotalInMinorUnits)
	}
	if len(summary.RealizedGains) != 2 {
		t.Errorf("expected both matches to fall within FY2024-25, got %d", len(summary.RealizedGains))
	}
}

func TestFifoSellExceedingHeldQuantityIsRejected(t *testing.T) {
	fills := []filltrail.Fill{
		{InstrumentSymbol: "INFY", Side: filltrail.SideBuy, Quantity: 10, PriceInMinorUnits: 1000, ExecutedAtTime: mustParseDate(t, "2024-01-01")},
		{InstrumentSymbol: "INFY", Side: filltrail.SideSell, Quantity: 20, PriceInMinorUnits: 1100, ExecutedAtTime: mustParseDate(t, "2024-02-01")},
	}
	_, err := ComputeFifoRealizedGains(fills)
	if err == nil {
		t.Fatal("expected an error for a sell exceeding open lot quantity, got nil")
	}
}

func TestFifoExactlyTwelveMonthsIsLongTerm(t *testing.T) {
	fills := []filltrail.Fill{
		{InstrumentSymbol: "WIPRO", Side: filltrail.SideBuy, Quantity: 10, PriceInMinorUnits: 1000, ExecutedAtTime: mustParseDate(t, "2024-03-01")},
		{InstrumentSymbol: "WIPRO", Side: filltrail.SideSell, Quantity: 10, PriceInMinorUnits: 1200, ExecutedAtTime: mustParseDate(t, "2025-03-01")},
	}
	realizedGains, err := ComputeFifoRealizedGains(fills)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(realizedGains) != 1 || realizedGains[0].GainType != GainTypeLongTerm {
		t.Fatalf("expected exactly-12-months-later sale to be LTCG, got %+v", realizedGains)
	}
}

func TestFifoOneDayBeforeTwelveMonthsIsShortTerm(t *testing.T) {
	fills := []filltrail.Fill{
		{InstrumentSymbol: "WIPRO", Side: filltrail.SideBuy, Quantity: 10, PriceInMinorUnits: 1000, ExecutedAtTime: mustParseDate(t, "2024-03-01")},
		{InstrumentSymbol: "WIPRO", Side: filltrail.SideSell, Quantity: 10, PriceInMinorUnits: 1200, ExecutedAtTime: mustParseDate(t, "2025-02-28")},
	}
	realizedGains, err := ComputeFifoRealizedGains(fills)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(realizedGains) != 1 || realizedGains[0].GainType != GainTypeShortTerm {
		t.Fatalf("expected one-day-short-of-12-months sale to be STCG, got %+v", realizedGains)
	}
}

func TestFifoMultipleInstrumentsDoNotCrossMatch(t *testing.T) {
	fills := []filltrail.Fill{
		{InstrumentSymbol: "INFY", Side: filltrail.SideBuy, Quantity: 10, PriceInMinorUnits: 1000, ExecutedAtTime: mustParseDate(t, "2024-01-01")},
		{InstrumentSymbol: "TCS", Side: filltrail.SideBuy, Quantity: 5, PriceInMinorUnits: 3000, ExecutedAtTime: mustParseDate(t, "2024-01-01")},
		{InstrumentSymbol: "INFY", Side: filltrail.SideSell, Quantity: 10, PriceInMinorUnits: 1100, ExecutedAtTime: mustParseDate(t, "2024-02-01")},
	}
	realizedGains, err := ComputeFifoRealizedGains(fills)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(realizedGains) != 1 || realizedGains[0].InstrumentSymbol != "INFY" {
		t.Fatalf("expected only the INFY sell to produce a match, got %+v", realizedGains)
	}
}

func TestIndianFinancialYearRangeParsing(t *testing.T) {
	start, end, err := IndianFinancialYearRange("2025-26")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantStart := time.Date(2025, time.April, 1, 0, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, time.March, 31, 23, 59, 59, 999999999, time.UTC)
	if !start.Equal(wantStart) {
		t.Errorf("expected start %v, got %v", wantStart, start)
	}
	if !end.Equal(wantEnd) {
		t.Errorf("expected end %v, got %v", wantEnd, end)
	}
}

func TestIndianFinancialYearRangeRejectsNonConsecutiveYears(t *testing.T) {
	if _, _, err := IndianFinancialYearRange("2025-28"); err == nil {
		t.Fatal("expected an error for a non-consecutive financial year label")
	}
}

func TestIndianFinancialYearRangeRejectsMalformedLabel(t *testing.T) {
	if _, _, err := IndianFinancialYearRange("garbage"); err == nil {
		t.Fatal("expected an error for a malformed financial year label")
	}
}
