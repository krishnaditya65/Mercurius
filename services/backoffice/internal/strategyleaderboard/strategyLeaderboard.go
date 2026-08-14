// Package strategyleaderboard implements FEATURES.md §19/§11's
// "Verified-track-record social/copy-trading leaderboards with
// disclosed, audited performance (not self-reported returns)". It is a
// real HTTP client-driven read/aggregation layer over oms-gateway's real
// internal/strategyfollowing (verified strategies + live follower
// counts) and internal/algolimits (real per-strategy daily notional
// turnover) — internal/omsgatewayclient does the actual HTTP calls, this
// package ranks and shapes the result.
//
// The key honesty bar this package is built to (from FEATURES.md's own
// wording): NOT self-reported. A strategy owner cannot submit a
// performance number here — there is no method on this package's
// Ranker, or endpoint in cmd/server/main.go, that accepts a
// caller-supplied return/P&L figure at all. Every ranked figure is
// FETCHED from oms-gateway, never posted in.
//
// What's honestly NOT built, stated loudly (this is the "data-limited"
// gap the requirements ask to be explicit about): oms-gateway does not
// expose per-strategy REALIZED P&L or RETURNS anywhere. Its audit trail
// (internal/audittrail) records fills with buyer/seller account IDs and
// price/quantity, but — verified by reading that package and its call
// sites directly — fill entries are NOT tagged with a strategyIdentifier
// at all, only STRATEGY_LIMIT_REJECTED rejection entries are. That
// means there is no way to reconstruct which fills belong to which
// strategy from the audit trail as it exists today, so this package does
// NOT attempt to derive a P&L/returns figure from it (doing so would
// require guessing, which is worse than being explicit about the gap).
//
// What IS real and used instead: internal/algolimits tracks a genuine,
// live, per-strategyId CUMULATIVE NOTIONAL TRADED TODAY (see oms-
// gateway's GET /algo-limits?strategyId=...) — every order tagged with a
// strategyId that passes the strategy-limits gate contributes to this
// figure. This package surfaces that as a real "trading activity" proxy
// alongside the real follower count, while being explicit in the wire
// shape and in this doc comment that TRADING VOLUME/TURNOVER IS NOT
// PERFORMANCE — a strategy could trade a large notional and still lose
// money, or trade small and be highly profitable; this proxy says
// nothing about returns. A real build needs oms-gateway to tag audit-
// trail fill entries (or a dedicated per-strategy P&L ledger) with
// strategyIdentifier before a genuine "audited performance" ranking is
// possible — that's the concrete next step, not built here.
//
// TODO(real build): no caching (queries oms-gateway fresh on every
// ranking request — fine for a skeleton, not for a real leaderboard
// endpoint under load); ranking is a simple descending sort on the one
// available proxy metric, not a real risk-adjusted return calculation
// (Sharpe ratio, drawdown, etc. — all of which need real P&L data this
// package doesn't have); no time-windowing (today's notional only, no
// "this month" / "all-time" views).
package strategyleaderboard

import (
	"sort"

	"mercurius/backoffice/internal/omsgatewayclient"
)

// LeaderboardEntry is one real, ranked strategy — never self-reported.
// DisplayName/Description/FollowerCount come straight from oms-gateway's
// admin-verified internal/strategyfollowing registry.
// NotionalTradedTodayInMinorUnits is the real, live per-strategy figure
// from internal/algolimits — see the package doc's honesty note: this is
// a TRADING-ACTIVITY proxy, explicitly NOT a performance/returns figure.
type LeaderboardEntry struct {
	StrategyIdentifier              string `json:"strategyIdentifier"`
	DisplayName                     string `json:"displayName"`
	Description                     string `json:"description"`
	FollowerCount                   int    `json:"followerCount"`
	NotionalTradedTodayInMinorUnits int64  `json:"notionalTradedTodayInMinorUnits"`
	// IsPerformanceDataAvailable is always false today — present so a
	// real client can render an honest "performance data not yet
	// available" state instead of silently treating the notional figure
	// as if it were returns. Flip this only once a real P&L-per-strategy
	// data source exists to back it.
	IsPerformanceDataAvailable bool `json:"isPerformanceDataAvailable"`
}

// Ranker builds a real leaderboard by querying oms-gateway for verified
// strategies + follower counts, then per strategy for today's notional
// traded, and sorting descending by that notional. No method here
// accepts a caller-supplied performance number for any strategy — that
// is the deliberate, structural enforcement of "not self-reported".
type Ranker struct {
	omsGatewayClient *omsgatewayclient.OmsGatewayClient
}

// NewRanker builds a Ranker over the given oms-gateway client.
func NewRanker(omsGatewayClient *omsgatewayclient.OmsGatewayClient) *Ranker {
	return &Ranker{omsGatewayClient: omsGatewayClient}
}

// BuildLeaderboard fetches the real verified-strategy list (with real
// follower counts) and, for each, the real notional traded today, then
// returns them ranked descending by that notional. A per-strategy
// algo-limits fetch failure does not fail the whole leaderboard — that
// entry is included with a zero notional rather than dropped, so one
// strategy oms-gateway can't answer for doesn't hide every other
// strategy's ranking.
func (ranker *Ranker) BuildLeaderboard() ([]LeaderboardEntry, error) {
	verifiedStrategies, fetchError := ranker.omsGatewayClient.FetchVerifiedStrategies()
	if fetchError != nil {
		return nil, fetchError
	}

	entries := make([]LeaderboardEntry, 0, len(verifiedStrategies))
	for _, strategy := range verifiedStrategies {
		var notionalTradedToday int64
		if algoLimitsStatus, algoLimitsError := ranker.omsGatewayClient.FetchAlgoLimitsStatus(strategy.StrategyIdentifier); algoLimitsError == nil {
			notionalTradedToday = algoLimitsStatus.NotionalUsedTodayInMinorUnits
		}

		entries = append(entries, LeaderboardEntry{
			StrategyIdentifier:              strategy.StrategyIdentifier,
			DisplayName:                     strategy.DisplayName,
			Description:                     strategy.Description,
			FollowerCount:                   strategy.FollowerCount,
			NotionalTradedTodayInMinorUnits: notionalTradedToday,
			IsPerformanceDataAvailable:      false,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].NotionalTradedTodayInMinorUnits != entries[j].NotionalTradedTodayInMinorUnits {
			return entries[i].NotionalTradedTodayInMinorUnits > entries[j].NotionalTradedTodayInMinorUnits
		}
		// Stable, deterministic tiebreak for equal notional (including
		// the common zero/zero case) — never rely on oms-gateway's own
		// response ordering, which strategyfollowing.ListVerifiedStrategies
		// already sorts by strategyIdentifier, but re-asserted here so
		// this package's own output contract doesn't silently depend on
		// an upstream implementation detail.
		return entries[i].StrategyIdentifier < entries[j].StrategyIdentifier
	})

	return entries, nil
}
