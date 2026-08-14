package filltrail

import (
	"testing"
	"time"

	"mercurius/reporting/internal/omsgatewayclient"
)

func TestParseFillsFromAllAuditTrailEntriesBuySide(t *testing.T) {
	recordedAt := time.Date(2025, time.June, 10, 9, 30, 0, 0, time.UTC)
	entries := []omsgatewayclient.AuditTrailEntryWireFormat{
		{
			RecordedAtTime:                    recordedAt,
			EventType:                         EventTypeOrderFilled,
			ClientAccountIdentifier:           "acct-002", // the TAKER, not acct-001 — this is the real bug pattern
			InstrumentSymbol:                  "INFY",
			MatchingEngineOrderSequenceNumber: 42,
			BuyingClientAccountIdentifier:     "acct-001",
			SellingClientAccountIdentifier:    "acct-002",
			ExecutedPriceInMinorUnits:         150000,
			ExecutedQuantity:                  10,
		},
	}

	fills, errs := ParseFillsFromAllAuditTrailEntries("acct-001", entries)
	if len(errs) != 0 {
		t.Fatalf("unexpected parse errors: %v", errs)
	}
	if len(fills) != 1 {
		t.Fatalf("expected 1 fill for the BUYER even though ClientAccountIdentifier was the seller, got %d", len(fills))
	}

	fill := fills[0]
	if fill.Side != SideBuy {
		t.Errorf("expected BUY side, got %s", fill.Side)
	}
	if fill.Quantity != 10 || fill.PriceInMinorUnits != 150000 {
		t.Errorf("expected qty=10 price=150000, got qty=%d price=%d", fill.Quantity, fill.PriceInMinorUnits)
	}
	if fill.CounterpartyAccountIdentifier != "acct-002" {
		t.Errorf("expected counterparty acct-002, got %s", fill.CounterpartyAccountIdentifier)
	}
	if !fill.ExecutedAtTime.Equal(recordedAt) {
		t.Errorf("expected executedAtTime %v, got %v", recordedAt, fill.ExecutedAtTime)
	}
}

func TestParseFillsFromAllAuditTrailEntriesSellSide(t *testing.T) {
	entries := []omsgatewayclient.AuditTrailEntryWireFormat{
		{
			EventType:                      EventTypeOrderFilled,
			ClientAccountIdentifier:        "acct-002",
			InstrumentSymbol:               "INFY",
			BuyingClientAccountIdentifier:  "acct-001",
			SellingClientAccountIdentifier: "acct-002",
			ExecutedPriceInMinorUnits:      150000,
			ExecutedQuantity:               10,
		},
	}

	fills, errs := ParseFillsFromAllAuditTrailEntries("acct-002", entries)
	if len(errs) != 0 {
		t.Fatalf("unexpected parse errors: %v", errs)
	}
	if len(fills) != 1 || fills[0].Side != SideSell {
		t.Fatalf("expected 1 SELL fill, got %+v", fills)
	}
	if fills[0].CounterpartyAccountIdentifier != "acct-001" {
		t.Errorf("expected counterparty acct-001, got %s", fills[0].CounterpartyAccountIdentifier)
	}
}

func TestParseFillsFromAllAuditTrailEntriesIgnoresFillsNotInvolvingAccount(t *testing.T) {
	entries := []omsgatewayclient.AuditTrailEntryWireFormat{
		{
			EventType:                      EventTypeOrderFilled,
			BuyingClientAccountIdentifier:  "acct-003",
			SellingClientAccountIdentifier: "acct-004",
			ExecutedPriceInMinorUnits:      100,
			ExecutedQuantity:               1,
		},
	}
	fills, errs := ParseFillsFromAllAuditTrailEntries("acct-001", entries)
	if len(fills) != 0 || len(errs) != 0 {
		t.Fatalf("expected no fills/errors for an unrelated fill, got fills=%+v errs=%v", fills, errs)
	}
}

func TestParseFillsFromAllAuditTrailEntriesSkipsNonFillEvents(t *testing.T) {
	entries := []omsgatewayclient.AuditTrailEntryWireFormat{
		{EventType: "ORDER_SUBMITTED", ClientAccountIdentifier: "acct-001", DetailMessage: "order accepted"},
	}
	fills, errs := ParseFillsFromAllAuditTrailEntries("acct-001", entries)
	if len(fills) != 0 || len(errs) != 0 {
		t.Fatalf("expected no fills and no errors for a non-fill event, got fills=%+v errs=%v", fills, errs)
	}
}

func TestParseFillsFromAllAuditTrailEntriesFallsBackToDetailMessage(t *testing.T) {
	entries := []omsgatewayclient.AuditTrailEntryWireFormat{
		{
			EventType:               EventTypeOrderFilled,
			ClientAccountIdentifier: "acct-002",
			DetailMessage:           "filled 10 @ 150000 (buyer=acct-001 seller=acct-002)",
			// deliberately no structured fields, to exercise the fallback
		},
	}
	fills, errs := ParseFillsFromAllAuditTrailEntries("acct-001", entries)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(fills) != 1 || fills[0].Side != SideBuy || fills[0].Quantity != 10 || fills[0].PriceInMinorUnits != 150000 {
		t.Fatalf("fallback parse produced unexpected fill: %+v", fills)
	}
}

func TestParseFillsFromAllAuditTrailEntriesReportsUnparseableMessage(t *testing.T) {
	entries := []omsgatewayclient.AuditTrailEntryWireFormat{
		{EventType: EventTypeOrderFilled, ClientAccountIdentifier: "acct-001", DetailMessage: "some unexpected future format"},
	}
	fills, errs := ParseFillsFromAllAuditTrailEntries("acct-001", entries)
	if len(fills) != 0 {
		t.Fatalf("expected no fills parsed from an unrecognized message, got %+v", fills)
	}
	if len(errs) != 1 {
		t.Fatalf("expected exactly 1 unparseable-entry error, got %d", len(errs))
	}
}

func TestFillsOnDateFiltersToCalendarDate(t *testing.T) {
	fills := []Fill{
		{ExecutedAtTime: time.Date(2025, time.June, 10, 9, 30, 0, 0, time.UTC)},
		{ExecutedAtTime: time.Date(2025, time.June, 10, 15, 0, 0, 0, time.UTC)},
		{ExecutedAtTime: time.Date(2025, time.June, 11, 9, 30, 0, 0, time.UTC)},
	}
	onDate := FillsOnDate(fills, time.Date(2025, time.June, 10, 0, 0, 0, 0, time.UTC))
	if len(onDate) != 2 {
		t.Fatalf("expected 2 fills on 2025-06-10, got %d", len(onDate))
	}
}

func TestFillsInRangeIsInclusiveOfBounds(t *testing.T) {
	fills := []Fill{
		{ExecutedAtTime: time.Date(2025, time.June, 1, 0, 0, 0, 0, time.UTC)},
		{ExecutedAtTime: time.Date(2025, time.June, 15, 0, 0, 0, 0, time.UTC)},
		{ExecutedAtTime: time.Date(2025, time.June, 30, 23, 59, 59, 0, time.UTC)},
		{ExecutedAtTime: time.Date(2025, time.July, 1, 0, 0, 0, 0, time.UTC)},
	}
	inRange := FillsInRange(fills,
		time.Date(2025, time.June, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2025, time.June, 30, 23, 59, 59, 999999999, time.UTC),
	)
	if len(inRange) != 3 {
		t.Fatalf("expected 3 fills within June 2025, got %d", len(inRange))
	}
}
