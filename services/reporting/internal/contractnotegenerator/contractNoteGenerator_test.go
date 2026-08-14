package contractnotegenerator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mercurius/reporting/internal/filltrail"
	"mercurius/reporting/internal/omsgatewayclient"
)

func TestGenerateForAccountAndDateWithLiveCalculator(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(omsgatewayclient.ChargesBreakdownWireFormat{
			TurnoverInMinorUnits:     150000,
			TotalChargesInMinorUnits: 150,
			NetAmountInMinorUnits:    150150,
		})
	}))
	defer server.Close()

	tradeDate := time.Date(2025, time.June, 10, 0, 0, 0, 0, time.UTC)
	fills := []filltrail.Fill{
		{InstrumentSymbol: "INFY", Side: filltrail.SideBuy, Quantity: 10, PriceInMinorUnits: 15000, ExecutedAtTime: time.Date(2025, time.June, 10, 9, 30, 0, 0, time.UTC), CounterpartyAccountIdentifier: "acct-002"},
		{InstrumentSymbol: "TCS", Side: filltrail.SideSell, Quantity: 5, PriceInMinorUnits: 30000, ExecutedAtTime: time.Date(2025, time.June, 11, 9, 30, 0, 0, time.UTC), CounterpartyAccountIdentifier: "acct-003"},
	}

	client := omsgatewayclient.NewOmsGatewayClient(server.URL)
	note := GenerateForAccountAndDate("acct-001", tradeDate, fills, false, client, time.Now())

	if len(note.TradeLines) != 1 {
		t.Fatalf("expected only the 2025-06-10 fill in the note, got %d lines", len(note.TradeLines))
	}
	if note.TradeLines[0].ChargesSource != "OMS_GATEWAY_LIVE_CALCULATOR" {
		t.Errorf("expected live calculator source, got %s", note.TradeLines[0].ChargesSource)
	}
	if note.TotalTurnoverInMinorUnits != 150000 || note.TotalChargesInMinorUnits != 150 || note.TotalNetAmountInMinorUnits != 150150 {
		t.Errorf("unexpected note totals: %+v", note)
	}
}

func TestGenerateForAccountAndDateFallsBackWhenOmsGatewayUnreachable(t *testing.T) {
	tradeDate := time.Date(2025, time.June, 10, 0, 0, 0, 0, time.UTC)
	fills := []filltrail.Fill{
		{InstrumentSymbol: "INFY", Side: filltrail.SideBuy, Quantity: 10, PriceInMinorUnits: 15000, ExecutedAtTime: time.Date(2025, time.June, 10, 9, 30, 0, 0, time.UTC)},
	}

	client := omsgatewayclient.NewOmsGatewayClient("http://127.0.0.1:1")
	note := GenerateForAccountAndDate("acct-001", tradeDate, fills, false, client, time.Now())

	if len(note.TradeLines) != 1 {
		t.Fatalf("expected 1 trade line, got %d", len(note.TradeLines))
	}
	if note.TradeLines[0].ChargesSource != "REPORTING_ILLUSTRATIVE_FALLBACK" {
		t.Errorf("expected illustrative fallback source, got %s", note.TradeLines[0].ChargesSource)
	}
	wantTurnover := int64(15000 * 10)
	if note.TotalTurnoverInMinorUnits != wantTurnover {
		t.Errorf("expected fallback turnover %d, got %d", wantTurnover, note.TotalTurnoverInMinorUnits)
	}
}

func TestGenerateForAccountAndDateWithNoFillsIsEmptyNote(t *testing.T) {
	client := omsgatewayclient.NewOmsGatewayClient("http://127.0.0.1:1")
	note := GenerateForAccountAndDate("acct-001", time.Date(2025, time.June, 10, 0, 0, 0, 0, time.UTC), nil, false, client, time.Now())
	if len(note.TradeLines) != 0 || note.TotalTurnoverInMinorUnits != 0 {
		t.Fatalf("expected an empty note, got %+v", note)
	}
}
