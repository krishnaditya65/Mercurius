package omsgatewayclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchAuditTrailForAccount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audit-trail" || r.URL.Query().Get("accountId") != "acct-001" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]AuditTrailEntryWireFormat{
			{EventType: "ORDER_FILLED", ClientAccountIdentifier: "acct-001", InstrumentSymbol: "INFY", DetailMessage: "filled 10 @ 150000 (buyer=acct-001 seller=acct-002)"},
		})
	}))
	defer server.Close()

	client := NewOmsGatewayClient(server.URL)
	entries, err := client.FetchAuditTrailForAccount("acct-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 || entries[0].InstrumentSymbol != "INFY" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
}

func TestFetchAllAuditTrailEntriesHasNoAccountIdQueryParam(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audit-trail" || r.URL.Query().Get("accountId") != "" {
			t.Errorf("expected an unfiltered audit-trail request, got %s", r.URL.String())
		}
		_ = json.NewEncoder(w).Encode([]AuditTrailEntryWireFormat{
			{
				EventType:                      "ORDER_FILLED",
				ClientAccountIdentifier:        "acct-002",
				InstrumentSymbol:               "DEMO-EQ",
				BuyingClientAccountIdentifier:  "acct-001",
				SellingClientAccountIdentifier: "acct-002",
				ExecutedPriceInMinorUnits:      10000,
				ExecutedQuantity:               100,
			},
		})
	}))
	defer server.Close()

	client := NewOmsGatewayClient(server.URL)
	entries, err := client.FetchAllAuditTrailEntries()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 || entries[0].BuyingClientAccountIdentifier != "acct-001" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
}

func TestFetchAuditTrailForAccountReturnsErrorOnNonOkStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewOmsGatewayClient(server.URL)
	if _, err := client.FetchAuditTrailForAccount("acct-001"); err == nil {
		t.Fatal("expected an error for a 500 response")
	}
}

func TestFetchPositionsForAccount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(PositionsWireResponse{
			AccountIdentifier:             "acct-001",
			NetQuantityByInstrumentSymbol: map[string]int64{"INFY": 50},
		})
	}))
	defer server.Close()

	client := NewOmsGatewayClient(server.URL)
	positions, err := client.FetchPositionsForAccount("acct-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if positions.NetQuantityByInstrumentSymbol["INFY"] != 50 {
		t.Fatalf("unexpected positions: %+v", positions)
	}
}

func TestFetchProcessedCorporateActionsForAccount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"accountIdentifier": "acct-001",
			"processedActions": []ProcessedActionWireFormat{
				{ActionType: "CASH_DIVIDEND", ClientAccountIdentifier: "acct-001", InstrumentSymbol: "ITC", CashCreditedInMinorUnits: 5000},
			},
		})
	}))
	defer server.Close()

	client := NewOmsGatewayClient(server.URL)
	actions, err := client.FetchProcessedCorporateActionsForAccount("acct-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(actions) != 1 || actions[0].CashCreditedInMinorUnits != 5000 {
		t.Fatalf("unexpected actions: %+v", actions)
	}
}

func TestEstimateChargesLiveCalculator(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		_ = json.NewEncoder(w).Encode(ChargesBreakdownWireFormat{
			TurnoverInMinorUnits:     100000,
			TotalChargesInMinorUnits: 100,
			NetAmountInMinorUnits:    100100,
		})
	}))
	defer server.Close()

	client := NewOmsGatewayClient(server.URL)
	breakdown, isLive := client.EstimateCharges(true, 10000, 10, false)
	if !isLive {
		t.Fatal("expected isChargesCalculatorLive to be true")
	}
	if breakdown.TurnoverInMinorUnits != 100000 {
		t.Fatalf("unexpected breakdown: %+v", breakdown)
	}
}

func TestEstimateChargesFallsBackWhenUnreachable(t *testing.T) {
	client := NewOmsGatewayClient("http://127.0.0.1:1") // nothing listening
	_, isLive := client.EstimateCharges(true, 10000, 10, false)
	if isLive {
		t.Fatal("expected isChargesCalculatorLive to be false when oms-gateway is unreachable")
	}
}
