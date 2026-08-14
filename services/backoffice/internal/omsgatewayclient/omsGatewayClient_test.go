package omsgatewayclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchPositionsParsesRealPositionsResponse(t *testing.T) {
	fakeOmsGatewayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/positions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("accountId") != "acct-owner" {
			t.Fatalf("unexpected accountId: %s", r.URL.Query().Get("accountId"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(PositionsWireResponse{
			AccountIdentifier:             "acct-owner",
			NetQuantityByInstrumentSymbol: map[string]int64{"AAPL": 100},
		})
	}))
	defer fakeOmsGatewayServer.Close()

	client := NewOmsGatewayClient(fakeOmsGatewayServer.URL)
	positions, fetchError := client.FetchPositions("acct-owner")

	if fetchError != nil {
		t.Fatalf("expected successful fetch, got: %v", fetchError)
	}
	if positions.NetQuantityByInstrumentSymbol["AAPL"] != 100 {
		t.Fatalf("unexpected positions: %v", positions)
	}
}

func TestFetchPositionsReturnsErrorWhenUnreachable(t *testing.T) {
	client := NewOmsGatewayClient("http://127.0.0.1:1")

	_, fetchError := client.FetchPositions("acct-owner")

	if fetchError == nil {
		t.Fatal("expected an error when oms-gateway is unreachable")
	}
}

func TestFetchPositionsReturnsErrorOnNonOkStatus(t *testing.T) {
	fakeOmsGatewayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer fakeOmsGatewayServer.Close()

	client := NewOmsGatewayClient(fakeOmsGatewayServer.URL)
	_, fetchError := client.FetchPositions("acct-owner")

	if fetchError == nil {
		t.Fatal("expected an error on a non-200 response")
	}
}

func TestFetchVerifiedStrategiesParsesRealListResponse(t *testing.T) {
	fakeOmsGatewayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/strategies" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]VerifiedStrategyWireEntry{
			{StrategyIdentifier: "strat-1", DisplayName: "Momentum", FollowerCount: 5},
			{StrategyIdentifier: "strat-2", DisplayName: "MeanReversion", FollowerCount: 2},
		})
	}))
	defer fakeOmsGatewayServer.Close()

	client := NewOmsGatewayClient(fakeOmsGatewayServer.URL)
	strategies, fetchError := client.FetchVerifiedStrategies()

	if fetchError != nil {
		t.Fatalf("expected successful fetch, got: %v", fetchError)
	}
	if len(strategies) != 2 {
		t.Fatalf("expected 2 strategies, got %d", len(strategies))
	}
}

func TestFetchVerifiedStrategiesReturnsErrorWhenUnreachable(t *testing.T) {
	client := NewOmsGatewayClient("http://127.0.0.1:1")

	_, fetchError := client.FetchVerifiedStrategies()

	if fetchError == nil {
		t.Fatal("expected an error when oms-gateway is unreachable")
	}
}

func TestFetchAlgoLimitsStatusParsesRealNotionalUsed(t *testing.T) {
	fakeOmsGatewayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/algo-limits" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("strategyId") != "strat-1" {
			t.Fatalf("unexpected strategyId: %s", r.URL.Query().Get("strategyId"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(AlgoLimitsStatusWireResponse{
			StrategyIdentifier:            "strat-1",
			NotionalUsedTodayInMinorUnits: 123456,
		})
	}))
	defer fakeOmsGatewayServer.Close()

	client := NewOmsGatewayClient(fakeOmsGatewayServer.URL)
	status, fetchError := client.FetchAlgoLimitsStatus("strat-1")

	if fetchError != nil {
		t.Fatalf("expected successful fetch, got: %v", fetchError)
	}
	if status.NotionalUsedTodayInMinorUnits != 123456 {
		t.Fatalf("unexpected notional used: %d", status.NotionalUsedTodayInMinorUnits)
	}
}

func TestFetchAlgoLimitsStatusReturnsErrorWhenUnreachable(t *testing.T) {
	client := NewOmsGatewayClient("http://127.0.0.1:1")

	_, fetchError := client.FetchAlgoLimitsStatus("strat-1")

	if fetchError == nil {
		t.Fatal("expected an error when oms-gateway is unreachable")
	}
}

func TestFetchAlgoLimitsStatusReturnsErrorOnNonOkStatus(t *testing.T) {
	fakeOmsGatewayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing strategyId", http.StatusBadRequest)
	}))
	defer fakeOmsGatewayServer.Close()

	client := NewOmsGatewayClient(fakeOmsGatewayServer.URL)
	_, fetchError := client.FetchAlgoLimitsStatus("")

	if fetchError == nil {
		t.Fatal("expected an error on a non-200 response")
	}
}
