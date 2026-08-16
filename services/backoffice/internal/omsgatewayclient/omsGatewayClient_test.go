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
	positions, fetchError := client.FetchPositions("acct-owner", "Bearer test-token")

	if fetchError != nil {
		t.Fatalf("expected successful fetch, got: %v", fetchError)
	}
	if positions.NetQuantityByInstrumentSymbol["AAPL"] != 100 {
		t.Fatalf("unexpected positions: %v", positions)
	}
}

// TestFetchPositionsForwardsCallersBearerToken reproduces the
// REGRESSION where FetchPositions never attached a bearer token: it
// asserts oms-gateway actually RECEIVES the exact Authorization header
// value the caller supplied, requiring authentication the same way
// oms-gateway's real /positions route (authmiddleware.RequireAuth) does
// — so before the fix, this test fails with a 401 exactly like the real
// regression, not just an assertion on an unused mock header.
func TestFetchPositionsForwardsCallersBearerToken(t *testing.T) {
	const expectedAuthorizationHeader = "Bearer caller-supplied-jwt"
	var observedAuthorizationHeader string

	fakeOmsGatewayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observedAuthorizationHeader = r.Header.Get("Authorization")
		if observedAuthorizationHeader != expectedAuthorizationHeader {
			// Mirrors oms-gateway's real authmiddleware.RequireAuth
			// behavior: no/wrong bearer token -> 401.
			http.Error(w, `{"errorMessage":"missing or malformed Authorization header"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(PositionsWireResponse{
			AccountIdentifier:             "acct-owner",
			NetQuantityByInstrumentSymbol: map[string]int64{"AAPL": 100},
		})
	}))
	defer fakeOmsGatewayServer.Close()

	client := NewOmsGatewayClient(fakeOmsGatewayServer.URL)
	_, fetchError := client.FetchPositions("acct-owner", expectedAuthorizationHeader)

	if fetchError != nil {
		t.Fatalf("expected successful fetch once the caller's bearer token is forwarded, got: %v", fetchError)
	}
	if observedAuthorizationHeader != expectedAuthorizationHeader {
		t.Fatalf("expected oms-gateway to receive Authorization %q, got %q", expectedAuthorizationHeader, observedAuthorizationHeader)
	}
}

func TestFetchPositionsReturnsErrorWhenUnreachable(t *testing.T) {
	client := NewOmsGatewayClient("http://127.0.0.1:1")

	_, fetchError := client.FetchPositions("acct-owner", "Bearer test-token")

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
	_, fetchError := client.FetchPositions("acct-owner", "Bearer test-token")

	if fetchError == nil {
		t.Fatal("expected an error on a non-200 response")
	}
}

// TestFetchPositionsReturnsErrorWhenNoAuthorizationHeaderIsForwarded
// covers the case backoffice's own handlers hit when the incoming
// request had no Authorization header at all -- FetchPositions must
// not silently invent one, and oms-gateway's real 401 must surface as
// a real Go error, not a successful empty response.
func TestFetchPositionsReturnsErrorWhenNoAuthorizationHeaderIsForwarded(t *testing.T) {
	fakeOmsGatewayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, `{"errorMessage":"missing or malformed Authorization header"}`, http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer fakeOmsGatewayServer.Close()

	client := NewOmsGatewayClient(fakeOmsGatewayServer.URL)
	_, fetchError := client.FetchPositions("acct-owner", "")

	if fetchError == nil {
		t.Fatal("expected an error when no Authorization header is forwarded")
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
