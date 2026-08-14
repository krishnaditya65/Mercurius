// Package largeorderfriction implements FEATURES.md §21's "Large-order
// friction: a brief confirm-with-context step for orders large relative
// to the user's history or the instrument's average volume": a real
// comparison of an incoming order's notional against the account's OWN
// real historical average order size (tracked from real submitted-order
// notionals, the same "fed by the real order-submission path" pattern
// internal/overtradingdetection already established) and, if supplied,
// the instrument's average volume. An order exceeding a configurable
// multiple of either baseline is SOFT-rejected — the client must
// resubmit with an explicit confirmedLargeOrder:true flag — never
// silently blocked forever.
package largeorderfriction

import (
	"fmt"
	"sync"
)

// Config configures the friction check.
type Config struct {
	// AccountHistoryMultiplier: an order notional exceeding the
	// account's own historical average notional by more than this
	// multiple is flagged large-relative-to-history.
	AccountHistoryMultiplier float64

	// InstrumentVolumeMultiplier: an order notional exceeding a
	// caller-supplied average-instrument-volume-notional figure by more
	// than this multiple is flagged large-relative-to-volume. Only
	// applied when the caller actually supplies a positive
	// averageInstrumentVolumeNotional to EvaluateOrder — this codebase
	// has no live instrument volume feed (see
	// internal/impactcostestimator's own "no live feed" gap), so this
	// comparison is opportunistic, not mandatory.
	InstrumentVolumeMultiplier float64

	// MinimumHistorySamplesBeforeFlagging: an account with fewer than
	// this many prior recorded order notionals is NEVER flagged against
	// its own history (there's no meaningful baseline yet) — the same
	// "don't flag a brand-new account's very first orders" convention
	// internal/overtradingdetection's MinimumRecentOrderCountToConsider
	// already established.
	MinimumHistorySamplesBeforeFlagging int

	// MaxHistoryPerAccount bounds memory — only the most recent N order
	// notionals are retained per account.
	MaxHistoryPerAccount int
}

// DefaultConfig returns a real, illustrative default: an order more than
// 5x an account's own historical average (once it has at least 5 prior
// orders to average), or more than 10% of the instrument's average
// volume when that figure is supplied, triggers friction.
func DefaultConfig() Config {
	return Config{
		AccountHistoryMultiplier:            5.0,
		InstrumentVolumeMultiplier:          0.10,
		MinimumHistorySamplesBeforeFlagging: 5,
		MaxHistoryPerAccount:                200,
	}
}

// EvaluationResult is the real, structured outcome of checking one
// order.
type EvaluationResult struct {
	RequiresConfirmation              bool    `json:"requiresConfirmation"`
	Reason                            string  `json:"reason,omitempty"`
	OrderNotional                     int64   `json:"orderNotional"`
	AccountAverageNotional            float64 `json:"accountAverageNotional"`
	AccountHistorySampleCount         int     `json:"accountHistorySampleCount"`
	IsLargeRelativeToAccountHistory   bool    `json:"isLargeRelativeToAccountHistory"`
	IsLargeRelativeToInstrumentVolume bool    `json:"isLargeRelativeToInstrumentVolume"`
}

// Tracker is a real, mutex-guarded per-account order-notional history.
type Tracker struct {
	config Config

	mutexGuardingState       sync.Mutex
	notionalHistoryByAccount map[string][]int64
}

// NewTracker constructs a Tracker, validating config the same
// "fail loudly at construction" way this codebase's other packages do.
func NewTracker(config Config) (*Tracker, error) {
	if config.AccountHistoryMultiplier <= 1.0 {
		return nil, fmt.Errorf("largeorderfriction: AccountHistoryMultiplier must be > 1.0")
	}
	if config.InstrumentVolumeMultiplier <= 0 {
		return nil, fmt.Errorf("largeorderfriction: InstrumentVolumeMultiplier must be positive")
	}
	if config.MinimumHistorySamplesBeforeFlagging <= 0 {
		return nil, fmt.Errorf("largeorderfriction: MinimumHistorySamplesBeforeFlagging must be positive")
	}
	if config.MaxHistoryPerAccount <= 0 {
		config.MaxHistoryPerAccount = 200
	}
	return &Tracker{
		config:                   config,
		notionalHistoryByAccount: make(map[string][]int64),
	}, nil
}

// RecordOrderNotional appends a real submitted order's notional to an
// account's history — call this for every order that actually proceeds
// (see cmd/server/main.go's call site), so the baseline reflects genuine
// trading behavior, not just orders that happened to pass this check.
func (tracker *Tracker) RecordOrderNotional(accountIdentifier string, orderNotional int64) {
	if orderNotional < 0 {
		orderNotional = -orderNotional
	}
	tracker.mutexGuardingState.Lock()
	defer tracker.mutexGuardingState.Unlock()

	history := append(tracker.notionalHistoryByAccount[accountIdentifier], orderNotional)
	if len(history) > tracker.config.MaxHistoryPerAccount {
		history = history[len(history)-tracker.config.MaxHistoryPerAccount:]
	}
	tracker.notionalHistoryByAccount[accountIdentifier] = history
}

// averageNotionalLocked computes the account's current average and
// sample count. Caller must hold mutexGuardingState.
func (tracker *Tracker) averageNotionalLocked(accountIdentifier string) (float64, int) {
	history := tracker.notionalHistoryByAccount[accountIdentifier]
	if len(history) == 0 {
		return 0, 0
	}
	var sum int64
	for _, notional := range history {
		sum += notional
	}
	return float64(sum) / float64(len(history)), len(history)
}

// EvaluateOrder checks orderNotional (absolute value — sign is
// irrelevant to "how large is this order") against the account's own
// historical average AND, if averageInstrumentVolumeNotional > 0,
// against that instrument-volume baseline too. Pure read — does NOT
// itself record orderNotional into history (see RecordOrderNotional,
// called separately by the caller once the order is genuinely allowed
// to proceed, so a soft-rejected order that never resubmits doesn't
// pollute the baseline with a notional that was never actually
// accepted).
func (tracker *Tracker) EvaluateOrder(accountIdentifier string, orderNotional int64, averageInstrumentVolumeNotional int64) EvaluationResult {
	if orderNotional < 0 {
		orderNotional = -orderNotional
	}

	tracker.mutexGuardingState.Lock()
	averageNotional, sampleCount := tracker.averageNotionalLocked(accountIdentifier)
	tracker.mutexGuardingState.Unlock()

	result := EvaluationResult{
		OrderNotional:             orderNotional,
		AccountAverageNotional:    averageNotional,
		AccountHistorySampleCount: sampleCount,
	}

	if sampleCount >= tracker.config.MinimumHistorySamplesBeforeFlagging && averageNotional > 0 &&
		float64(orderNotional) > averageNotional*tracker.config.AccountHistoryMultiplier {
		result.IsLargeRelativeToAccountHistory = true
	}

	if averageInstrumentVolumeNotional > 0 &&
		float64(orderNotional) > float64(averageInstrumentVolumeNotional)*tracker.config.InstrumentVolumeMultiplier {
		result.IsLargeRelativeToInstrumentVolume = true
	}

	if result.IsLargeRelativeToAccountHistory || result.IsLargeRelativeToInstrumentVolume {
		result.RequiresConfirmation = true
		switch {
		case result.IsLargeRelativeToAccountHistory && result.IsLargeRelativeToInstrumentVolume:
			result.Reason = fmt.Sprintf(
				"this order's notional (%d) is unusually large -- more than %.1fx your own recent average order size (%.0f) AND more than %.0f%% of this instrument's typical volume",
				orderNotional, tracker.config.AccountHistoryMultiplier, averageNotional, tracker.config.InstrumentVolumeMultiplier*100,
			)
		case result.IsLargeRelativeToAccountHistory:
			result.Reason = fmt.Sprintf(
				"this order's notional (%d) is more than %.1fx your own recent average order size (%.0f)",
				orderNotional, tracker.config.AccountHistoryMultiplier, averageNotional,
			)
		default:
			result.Reason = fmt.Sprintf(
				"this order's notional (%d) is more than %.0f%% of this instrument's typical average volume",
				orderNotional, tracker.config.InstrumentVolumeMultiplier*100,
			)
		}
	}

	return result
}
