// Package algolimits implements FEATURES.md §7's "Strategy resource
// limits & circuit breakers (max orders/sec, max notional/day per
// algo)": every order submission can optionally carry a strategyId (see
// orders.OrderSubmissionRequest.StrategyIdentifier), and this package
// enforces REAL, configurable per-strategy limits on it —
//   - a genuine token-bucket rate limiter for max orders per second (not
//     a fake sleep or a naive fixed-window counter that lets a burst
//     straddle a window boundary)
//   - a real cumulative notional-per-day cap that resets at an actual
//     calendar-day boundary
//
// Both checks take `now` as an explicit parameter everywhere — never the
// wall clock internally — so every boundary (bucket refill timing, day
// rollover) is exactly reproducible in tests, per FEATURES.md's own
// call for "resetting at a real day boundary you can test
// deterministically".
//
// An order that exceeds EITHER limit is rejected before it reaches the
// risk engine or matching-engine (see cmd/server/main.go's
// processOrderSubmission, which checks this ahead of the KYC gate) — the
// returned error identifies exactly which limit tripped.
package algolimits

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	// ErrOrderRateLimitExceeded is returned when a strategy's token
	// bucket has no tokens available — it's submitting orders faster
	// than its configured MaxOrdersPerSecond allows.
	ErrOrderRateLimitExceeded = errors.New("strategy order rate limit exceeded")

	// ErrDailyNotionalLimitExceeded is returned when accepting this
	// order's notional would push the strategy's cumulative notional for
	// the current calendar day above its configured
	// MaxNotionalPerDayInMinorUnits.
	ErrDailyNotionalLimitExceeded = errors.New("strategy daily notional limit exceeded")
)

// StrategyLimitConfig holds one strategy's configured limits.
// MaxOrdersPerSecond == 0 disables rate limiting for that strategy (an
// explicit, documented "unlimited" — not an accidental always-reject).
// MaxNotionalPerDayInMinorUnits <= 0 disables the daily notional cap the
// same way.
type StrategyLimitConfig struct {
	MaxOrdersPerSecond            float64
	MaxNotionalPerDayInMinorUnits int64
}

// strategyState is the real, mutable, per-strategy tracking state: a
// token bucket for the rate limit and a running total for the daily
// notional cap.
type strategyState struct {
	config StrategyLimitConfig

	// Token bucket: tokensAvailable never exceeds config.MaxOrdersPerSecond
	// (the bucket's capacity), refilling continuously at
	// config.MaxOrdersPerSecond tokens per second based on elapsed time
	// since lastRefillTime.
	tokensAvailable float64
	lastRefillTime  time.Time

	// Daily notional cap: currentDayStart is truncated to a UTC calendar
	// day; notionalUsedTodayInMinorUnits resets to 0 whenever `now`'s
	// calendar day (UTC) no longer matches currentDayStart's.
	currentDayStart               time.Time
	notionalUsedTodayInMinorUnits int64
}

// Registry is the mutex-guarded set of every strategy's limiter state.
type Registry struct {
	mutexGuardingState sync.Mutex

	defaultConfig      StrategyLimitConfig
	configByStrategyId map[string]StrategyLimitConfig
	stateByStrategyId  map[string]*strategyState
}

// NewRegistry builds a Registry. defaultConfig applies to any
// strategyId that's never had SetStrategyLimits called for it —
// callers that want every strategy gated identically can just use this
// and skip SetStrategyLimits entirely.
func NewRegistry(defaultConfig StrategyLimitConfig) *Registry {
	return &Registry{
		defaultConfig:      defaultConfig,
		configByStrategyId: make(map[string]StrategyLimitConfig),
		stateByStrategyId:  make(map[string]*strategyState),
	}
}

// SetStrategyLimits overrides the limits for one specific strategyId,
// independent of the registry's defaultConfig. Safe to call at any time,
// including after that strategy has already submitted orders — it only
// changes the CONFIG; existing token-bucket/notional state carries
// forward unaffected (a strategy that just got a lower daily cap doesn't
// get its already-used-today notional erased).
func (registry *Registry) SetStrategyLimits(strategyId string, config StrategyLimitConfig) {
	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	registry.configByStrategyId[strategyId] = config
	if state, exists := registry.stateByStrategyId[strategyId]; exists {
		state.config = config
	}
}

// CheckAndReserve is the one real gate this package exposes: given a
// strategyId and the notional value of the order it's about to submit,
// it atomically checks BOTH limits against `now` and, if both pass,
// reserves the capacity (consumes one token, adds to today's notional
// total) in the same locked operation — so two concurrent calls can
// never both squeak through a boundary they'd individually have been
// rejected for.
//
// Returns ErrOrderRateLimitExceeded or ErrDailyNotionalLimitExceeded
// (never both — rate is checked first) if either limit trips; nil if the
// order is within both limits (and has now been reserved against them).
func (registry *Registry) CheckAndReserve(strategyId string, orderNotionalInMinorUnits int64, now time.Time) error {
	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	state := registry.stateForStrategyLocked(strategyId, now)

	if rateLimitError := checkAndConsumeRateLimitLocked(state, now); rateLimitError != nil {
		return rateLimitError
	}

	if notionalLimitError := checkAndReserveNotionalLocked(state, orderNotionalInMinorUnits, now); notionalLimitError != nil {
		return notionalLimitError
	}

	return nil
}

// Release reverses a prior CheckAndReserve reservation for an order that
// was ultimately rejected downstream (KYC/freeze/pledge/disclosure/risk/
// matching-engine-handoff failures in cmd/server/main.go's
// processOrderSubmission) AFTER already having been reserved here —
// mirrors internal/marginfunding.FundingBook.RollbackReservation's
// pattern exactly. Without this, a strategy's rate-limit token and
// daily-notional usage would be permanently and incorrectly consumed by
// an order that never actually reached the market. Gives back both the
// consumed rate-limit token (capped at the bucket's own capacity — see
// bucketCapacity) and the reserved notional (floored at zero). A no-op,
// safe call for an unrecognized strategyId (nothing to release).
func (registry *Registry) Release(strategyId string, orderNotionalInMinorUnits int64, now time.Time) {
	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	state, exists := registry.stateByStrategyId[strategyId]
	if !exists {
		return
	}

	if state.config.MaxOrdersPerSecond > 0 {
		state.tokensAvailable += 1.0
		capacity := bucketCapacity(state.config.MaxOrdersPerSecond)
		if state.tokensAvailable > capacity {
			state.tokensAvailable = capacity
		}
	}

	today := truncateToUtcCalendarDay(now)
	if !today.Equal(state.currentDayStart) {
		// The reservation being released was made on a now-past calendar
		// day whose usage has already reset — nothing to give back
		// against today's (already-zero) usage.
		return
	}
	state.notionalUsedTodayInMinorUnits -= orderNotionalInMinorUnits
	if state.notionalUsedTodayInMinorUnits < 0 {
		state.notionalUsedTodayInMinorUnits = 0
	}
}

func (registry *Registry) stateForStrategyLocked(strategyId string, now time.Time) *strategyState {
	if existing, exists := registry.stateByStrategyId[strategyId]; exists {
		return existing
	}

	config, hasOverride := registry.configByStrategyId[strategyId]
	if !hasOverride {
		config = registry.defaultConfig
	}

	newState := &strategyState{
		config:          config,
		tokensAvailable: bucketCapacity(config.MaxOrdersPerSecond),
		lastRefillTime:  now,
		currentDayStart: truncateToUtcCalendarDay(now),
	}
	registry.stateByStrategyId[strategyId] = newState
	return newState
}

// checkAndConsumeRateLimitLocked implements a real token-bucket rate
// limiter: refill tokens by elapsed-seconds * MaxOrdersPerSecond
// (capped at the bucket's capacity, MaxOrdersPerSecond itself, since
// a bucket that could accumulate unboundedly during an idle period would
// let a strategy burst arbitrarily far above its configured rate the
// moment it resumed), then consume one token if available.
// MaxOrdersPerSecond == 0 means rate limiting is disabled entirely for
// this strategy.
func checkAndConsumeRateLimitLocked(state *strategyState, now time.Time) error {
	if state.config.MaxOrdersPerSecond <= 0 {
		return nil
	}

	capacity := bucketCapacity(state.config.MaxOrdersPerSecond)
	elapsedSeconds := now.Sub(state.lastRefillTime).Seconds()
	if elapsedSeconds > 0 {
		state.tokensAvailable += elapsedSeconds * state.config.MaxOrdersPerSecond
		if state.tokensAvailable > capacity {
			state.tokensAvailable = capacity
		}
		state.lastRefillTime = now
	}

	if state.tokensAvailable < 1.0 {
		return fmt.Errorf("%w: max %.4g orders/sec", ErrOrderRateLimitExceeded, state.config.MaxOrdersPerSecond)
	}

	state.tokensAvailable -= 1.0
	return nil
}

// checkAndReserveNotionalLocked resets the running daily total to zero
// the moment `now` falls on a different UTC calendar day than the
// tracked currentDayStart, then checks whether adding
// orderNotionalInMinorUnits would exceed the configured cap.
// MaxNotionalPerDayInMinorUnits <= 0 means the cap is disabled entirely.
func checkAndReserveNotionalLocked(state *strategyState, orderNotionalInMinorUnits int64, now time.Time) error {
	today := truncateToUtcCalendarDay(now)
	if !today.Equal(state.currentDayStart) {
		state.currentDayStart = today
		state.notionalUsedTodayInMinorUnits = 0
	}

	if state.config.MaxNotionalPerDayInMinorUnits <= 0 {
		state.notionalUsedTodayInMinorUnits += orderNotionalInMinorUnits
		return nil
	}

	prospectiveTotal := state.notionalUsedTodayInMinorUnits + orderNotionalInMinorUnits
	if prospectiveTotal > state.config.MaxNotionalPerDayInMinorUnits {
		return fmt.Errorf(
			"%w: today's usage %d + this order's %d would exceed the daily cap of %d minor units",
			ErrDailyNotionalLimitExceeded, state.notionalUsedTodayInMinorUnits, orderNotionalInMinorUnits, state.config.MaxNotionalPerDayInMinorUnits,
		)
	}

	state.notionalUsedTodayInMinorUnits = prospectiveTotal
	return nil
}

// bucketCapacity is the token bucket's capacity for a given configured
// rate: max(1.0, maxOrdersPerSecond). Capping capacity at
// maxOrdersPerSecond itself (the pre-fix behavior) meant a sub-1 rate
// (e.g. 0.5, "1 order per 2 seconds") could never accumulate a full
// token and would PERMANENTLY block that strategy from ever placing an
// order. Flooring capacity at 1.0 guarantees a sub-1-rate strategy can
// still burst a single order once it's accumulated enough elapsed time,
// while a rate >= 1 keeps its exact configured capacity unchanged.
func bucketCapacity(maxOrdersPerSecond float64) float64 {
	if maxOrdersPerSecond < 1.0 {
		return 1.0
	}
	return maxOrdersPerSecond
}

func truncateToUtcCalendarDay(t time.Time) time.Time {
	utc := t.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
}

// NotionalUsedTodayInMinorUnits returns how much of strategyId's daily
// notional cap has been consumed so far today (0 if the strategy has
// never submitted an order, or an unrecognized strategyId is given) —
// exposed for status/observability endpoints.
func (registry *Registry) NotionalUsedTodayInMinorUnits(strategyId string, now time.Time) int64 {
	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	state, exists := registry.stateByStrategyId[strategyId]
	if !exists {
		return 0
	}
	today := truncateToUtcCalendarDay(now)
	if !today.Equal(state.currentDayStart) {
		return 0
	}
	return state.notionalUsedTodayInMinorUnits
}
