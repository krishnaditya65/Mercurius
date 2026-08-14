package strategyleaderboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"mercurius/backoffice/internal/omsgatewayclient"
)

// fakeOmsGateway builds an httptest server serving GET /strategies from
// strategies and GET /algo-limits?strategyId=... from
// notionalByStrategy, mirroring oms-gateway's real response shapes
// exactly (see internal/omsgatewayclient's wire types).
func fakeOmsGateway(t *testing.T, strategies []omsgatewayclient.VerifiedStrategyWireEntry, notionalByStrategy map[string]int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/strategies":
			_ = json.NewEncoder(w).Encode(strategies)
		case "/algo-limits":
			strategyId := r.URL.Query().Get("strategyId")
			notional, exists := notionalByStrategy[strategyId]
			if !exists {
				http.Error(w, "unknown strategy", http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(omsgatewayclient.AlgoLimitsStatusWireResponse{
				StrategyIdentifier:            strategyId,
				NotionalUsedTodayInMinorUnits: notional,
			})
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestBuildLeaderboardRanksDescendingByNotionalTradedToday(t *testing.T) {
	server := fakeOmsGateway(t,
		[]omsgatewayclient.VerifiedStrategyWireEntry{
			{StrategyIdentifier: "strat-low", DisplayName: "Low Volume", FollowerCount: 10},
			{StrategyIdentifier: "strat-high", DisplayName: "High Volume", FollowerCount: 3},
		},
		map[string]int64{
			"strat-low":  1000,
			"strat-high": 999999,
		},
	)
	defer server.Close()

	ranker := NewRanker(omsgatewayclient.NewOmsGatewayClient(server.URL))
	leaderboard, err := ranker.BuildLeaderboard()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(leaderboard) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(leaderboard))
	}
	if leaderboard[0].StrategyIdentifier != "strat-high" {
		t.Fatalf("expected strat-high ranked first (higher notional), got %q", leaderboard[0].StrategyIdentifier)
	}
	if leaderboard[1].StrategyIdentifier != "strat-low" {
		t.Fatalf("expected strat-low ranked second, got %q", leaderboard[1].StrategyIdentifier)
	}
}

func TestBuildLeaderboardIncludesRealFollowerCounts(t *testing.T) {
	server := fakeOmsGateway(t,
		[]omsgatewayclient.VerifiedStrategyWireEntry{
			{StrategyIdentifier: "strat-1", DisplayName: "Momentum", FollowerCount: 42},
		},
		map[string]int64{"strat-1": 500},
	)
	defer server.Close()

	ranker := NewRanker(omsgatewayclient.NewOmsGatewayClient(server.URL))
	leaderboard, err := ranker.BuildLeaderboard()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if leaderboard[0].FollowerCount != 42 {
		t.Fatalf("expected real follower count 42, got %d", leaderboard[0].FollowerCount)
	}
}

func TestBuildLeaderboardNeverMarksPerformanceDataAvailable(t *testing.T) {
	server := fakeOmsGateway(t,
		[]omsgatewayclient.VerifiedStrategyWireEntry{
			{StrategyIdentifier: "strat-1", DisplayName: "Momentum", FollowerCount: 1},
		},
		map[string]int64{"strat-1": 500},
	)
	defer server.Close()

	ranker := NewRanker(omsgatewayclient.NewOmsGatewayClient(server.URL))
	leaderboard, _ := ranker.BuildLeaderboard()
	if leaderboard[0].IsPerformanceDataAvailable {
		t.Fatal("expected IsPerformanceDataAvailable to be false — no real P&L data source exists yet")
	}
}

func TestBuildLeaderboardWithNoVerifiedStrategiesReturnsEmpty(t *testing.T) {
	server := fakeOmsGateway(t, []omsgatewayclient.VerifiedStrategyWireEntry{}, map[string]int64{})
	defer server.Close()

	ranker := NewRanker(omsgatewayclient.NewOmsGatewayClient(server.URL))
	leaderboard, err := ranker.BuildLeaderboard()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(leaderboard) != 0 {
		t.Fatalf("expected an empty leaderboard, got %v", leaderboard)
	}
}

func TestBuildLeaderboardReturnsErrorWhenOmsGatewayIsUnreachable(t *testing.T) {
	ranker := NewRanker(omsgatewayclient.NewOmsGatewayClient("http://127.0.0.1:1"))
	_, err := ranker.BuildLeaderboard()
	if err == nil {
		t.Fatal("expected an error when oms-gateway is unreachable")
	}
}

func TestBuildLeaderboardToleratesOneStrategysAlgoLimitsFetchFailing(t *testing.T) {
	// strat-broken has no entry in notionalByStrategy, so the fake
	// server 404s for its algo-limits fetch — the leaderboard should
	// still include it (with zero notional), not fail the whole request.
	server := fakeOmsGateway(t,
		[]omsgatewayclient.VerifiedStrategyWireEntry{
			{StrategyIdentifier: "strat-broken", DisplayName: "Broken", FollowerCount: 1},
			{StrategyIdentifier: "strat-ok", DisplayName: "OK", FollowerCount: 1},
		},
		map[string]int64{"strat-ok": 500},
	)
	defer server.Close()

	ranker := NewRanker(omsgatewayclient.NewOmsGatewayClient(server.URL))
	leaderboard, err := ranker.BuildLeaderboard()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(leaderboard) != 2 {
		t.Fatalf("expected both strategies present despite one algo-limits fetch failing, got %d", len(leaderboard))
	}
	var brokenEntry LeaderboardEntry
	for _, entry := range leaderboard {
		if entry.StrategyIdentifier == "strat-broken" {
			brokenEntry = entry
		}
	}
	if brokenEntry.NotionalTradedTodayInMinorUnits != 0 {
		t.Fatalf("expected zero notional for the strategy whose fetch failed, got %d", brokenEntry.NotionalTradedTodayInMinorUnits)
	}
}

func TestBuildLeaderboardTiebreaksDeterministicallyByStrategyIdentifier(t *testing.T) {
	server := fakeOmsGateway(t,
		[]omsgatewayclient.VerifiedStrategyWireEntry{
			{StrategyIdentifier: "strat-z", DisplayName: "Z", FollowerCount: 1},
			{StrategyIdentifier: "strat-a", DisplayName: "A", FollowerCount: 1},
		},
		map[string]int64{"strat-z": 100, "strat-a": 100},
	)
	defer server.Close()

	ranker := NewRanker(omsgatewayclient.NewOmsGatewayClient(server.URL))
	leaderboard, err := ranker.BuildLeaderboard()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if leaderboard[0].StrategyIdentifier != "strat-a" || leaderboard[1].StrategyIdentifier != "strat-z" {
		t.Fatalf("expected deterministic tiebreak by strategyIdentifier, got %v", leaderboard)
	}
}
