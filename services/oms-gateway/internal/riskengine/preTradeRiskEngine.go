// Package riskengine implements the pre-trade risk check described in
// ARCHITECTURE.md §4: an in-memory, synchronous check that must never do a
// blocking database round-trip on the request path. The authoritative
// balance data is expected to arrive asynchronously from the ledger
// service and refresh this cache. As of this build, `cmd/server/main.go`
// does a one-shot sync from the ledger at startup
// (RefreshAccountBalanceFromLedger) and applies fill adjustments directly
// (ApplyTradeSettlementToLocalCache) — a real build would instead consume
// a continuous stream of balance-changed events, not call these
// synchronously inline with request handling.
package riskengine

import (
	"fmt"
	"sync"
)

// PreTradeRiskEngine holds an in-memory cache of available margin per
// account. It is intentionally the ONLY thing the order-submission request
// path touches synchronously — never the ledger's database directly.
type PreTradeRiskEngine struct {
	mutexGuardingAvailableMarginCache      sync.RWMutex
	availableMarginInMinorUnitsByAccountId map[string]int64
}

func NewPreTradeRiskEngineWithSeedBalances(seedBalancesByAccountId map[string]int64) *PreTradeRiskEngine {
	copiedSeedBalances := make(map[string]int64, len(seedBalancesByAccountId))
	for accountId, balance := range seedBalancesByAccountId {
		copiedSeedBalances[accountId] = balance
	}
	return &PreTradeRiskEngine{
		availableMarginInMinorUnitsByAccountId: copiedSeedBalances,
	}
}

// RiskCheckOutcome carries both a machine-readable and a human-readable
// explanation, per FEATURES.md §21 — the human-readable reason is what
// actually reaches the client UI.
type RiskCheckOutcome struct {
	IsOrderApproved                bool
	HumanReadableRejectionReason   string
	MachineReadableRejectionReason string
}

// EvaluateOrderAgainstAvailableMargin is the sub-millisecond synchronous
// check every incoming order must pass before it is ever routed to the
// matching engine. It does NOT mutate the cached balance — margin is only
// actually debited once a fill event comes back from the matching engine
// (TODO(real build): wire that reconciliation path).
func (riskEngine *PreTradeRiskEngine) EvaluateOrderAgainstAvailableMargin(
	clientAccountIdentifier string,
	orderNotionalValueInMinorUnits int64,
) RiskCheckOutcome {
	riskEngine.mutexGuardingAvailableMarginCache.RLock()
	defer riskEngine.mutexGuardingAvailableMarginCache.RUnlock()

	availableMargin, accountIsKnown := riskEngine.availableMarginInMinorUnitsByAccountId[clientAccountIdentifier]
	if !accountIsKnown {
		return RiskCheckOutcome{
			IsOrderApproved:                false,
			HumanReadableRejectionReason:   "We couldn't find an account with sufficient onboarding to trade. Please complete KYC first.",
			MachineReadableRejectionReason: "ACCOUNT_NOT_FOUND",
		}
	}

	if orderNotionalValueInMinorUnits > availableMargin {
		shortfallInMinorUnits := orderNotionalValueInMinorUnits - availableMargin
		return RiskCheckOutcome{
			IsOrderApproved: false,
			HumanReadableRejectionReason: fmt.Sprintf(
				"Insufficient margin: this order needs %d more minor units than you have available.",
				shortfallInMinorUnits,
			),
			MachineReadableRejectionReason: "INSUFFICIENT_MARGIN",
		}
	}

	return RiskCheckOutcome{IsOrderApproved: true}
}

// AvailableMarginInMinorUnits returns the account's current cached
// available margin and whether the account is known at all. Read-only —
// exposed so callers like internal/marginpledge's HTTP handlers can show
// a real before/after margin figure without reaching into the cache map
// directly.
func (riskEngine *PreTradeRiskEngine) AvailableMarginInMinorUnits(clientAccountIdentifier string) (int64, bool) {
	riskEngine.mutexGuardingAvailableMarginCache.RLock()
	defer riskEngine.mutexGuardingAvailableMarginCache.RUnlock()

	availableMargin, accountIsKnown := riskEngine.availableMarginInMinorUnitsByAccountId[clientAccountIdentifier]
	return availableMargin, accountIsKnown
}

// RefreshAccountBalanceFromLedger overwrites the cached balance for one
// account with an authoritative value fetched from the ledger service.
// Safe to call for an account not previously known — it adds the account
// to the cache rather than erroring, since a real KYC-driven onboarding
// flow would create the account here for the first time.
func (riskEngine *PreTradeRiskEngine) RefreshAccountBalanceFromLedger(
	clientAccountIdentifier string,
	authoritativeBalanceInMinorUnits int64,
) {
	riskEngine.mutexGuardingAvailableMarginCache.Lock()
	defer riskEngine.mutexGuardingAvailableMarginCache.Unlock()

	riskEngine.availableMarginInMinorUnitsByAccountId[clientAccountIdentifier] = authoritativeBalanceInMinorUnits
}

// AdjustAvailableMarginInMinorUnits applies a signed delta to one
// account's cached available margin — used by internal/marginpledge to
// reflect a pledge (positive delta: pledging collateral increases
// available margin) or an unpledge (negative delta: removing collateral
// decreases it). Safe to call for an account not previously known — the
// delta becomes the account's starting balance, same rationale as
// RefreshAccountBalanceFromLedger.
func (riskEngine *PreTradeRiskEngine) AdjustAvailableMarginInMinorUnits(
	clientAccountIdentifier string,
	deltaInMinorUnits int64,
) {
	riskEngine.mutexGuardingAvailableMarginCache.Lock()
	defer riskEngine.mutexGuardingAvailableMarginCache.Unlock()

	riskEngine.availableMarginInMinorUnitsByAccountId[clientAccountIdentifier] += deltaInMinorUnits
}

// ApplyTradeSettlementToLocalCache adjusts the cached margin for both
// sides of a fill immediately, so a client's very next order reflects the
// trade without waiting on the next ledger sync. This is a pragmatic
// skeleton shortcut, not the real design: ARCHITECTURE.md §4 says this
// cache should be refreshed asynchronously FROM the ledger's event
// stream, not mutated directly by the request handler that also caused
// the trade. Kept here, clearly labeled, until that stream exists.
func (riskEngine *PreTradeRiskEngine) ApplyTradeSettlementToLocalCache(
	buyingClientAccountId string,
	sellingClientAccountId string,
	executedNotionalValueInMinorUnits int64,
) {
	riskEngine.mutexGuardingAvailableMarginCache.Lock()
	defer riskEngine.mutexGuardingAvailableMarginCache.Unlock()

	riskEngine.availableMarginInMinorUnitsByAccountId[buyingClientAccountId] -= executedNotionalValueInMinorUnits
	riskEngine.availableMarginInMinorUnitsByAccountId[sellingClientAccountId] += executedNotionalValueInMinorUnits
}
