package algolimits

import (
	"errors"
	"testing"
	"time"
)

var baseTime = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

func TestCheckAndReserveAllowsOrdersWithinRateLimit(t *testing.T) {
	registry := NewRegistry(StrategyLimitConfig{MaxOrdersPerSecond: 5, MaxNotionalPerDayInMinorUnits: 1_000_000})

	if err := registry.CheckAndReserve("algo-1", 100, baseTime); err != nil {
		t.Fatalf("expected first order to be allowed, got: %v", err)
	}
}

func TestCheckAndReserveTokenBucketAllowsExactCapacityBurst(t *testing.T) {
	registry := NewRegistry(StrategyLimitConfig{MaxOrdersPerSecond: 3, MaxNotionalPerDayInMinorUnits: 0})

	// Bucket starts full at capacity 3 — exactly 3 immediate orders
	// (same instant) must all succeed.
	for i := 0; i < 3; i++ {
		if err := registry.CheckAndReserve("algo-1", 10, baseTime); err != nil {
			t.Fatalf("order %d: expected allowed within initial burst capacity, got: %v", i+1, err)
		}
	}
	// The 4th, at the same instant (no refill time elapsed), must be rejected.
	err := registry.CheckAndReserve("algo-1", 10, baseTime)
	if !errors.Is(err, ErrOrderRateLimitExceeded) {
		t.Fatalf("expected ErrOrderRateLimitExceeded on the 4th immediate order, got: %v", err)
	}
}

func TestCheckAndReserveTokenBucketRefillsOverTime(t *testing.T) {
	registry := NewRegistry(StrategyLimitConfig{MaxOrdersPerSecond: 2, MaxNotionalPerDayInMinorUnits: 0})

	// Consume both initial tokens.
	if err := registry.CheckAndReserve("algo-1", 10, baseTime); err != nil {
		t.Fatalf("order 1 unexpectedly rejected: %v", err)
	}
	if err := registry.CheckAndReserve("algo-1", 10, baseTime); err != nil {
		t.Fatalf("order 2 unexpectedly rejected: %v", err)
	}
	// Immediately, a 3rd should fail.
	if err := registry.CheckAndReserve("algo-1", 10, baseTime); !errors.Is(err, ErrOrderRateLimitExceeded) {
		t.Fatalf("expected rate limit rejection, got: %v", err)
	}
	// After exactly 0.5s at 2/sec, exactly 1 token has refilled.
	halfSecondLater := baseTime.Add(500 * time.Millisecond)
	if err := registry.CheckAndReserve("algo-1", 10, halfSecondLater); err != nil {
		t.Fatalf("expected 1 refilled token to allow this order, got: %v", err)
	}
	// Immediately after, no more tokens.
	if err := registry.CheckAndReserve("algo-1", 10, halfSecondLater); !errors.Is(err, ErrOrderRateLimitExceeded) {
		t.Fatalf("expected rate limit rejection right after consuming the refilled token, got: %v", err)
	}
}

func TestCheckAndReserveTokenBucketNeverExceedsCapacityAfterLongIdle(t *testing.T) {
	registry := NewRegistry(StrategyLimitConfig{MaxOrdersPerSecond: 2, MaxNotionalPerDayInMinorUnits: 0})

	if err := registry.CheckAndReserve("algo-1", 10, baseTime); err != nil {
		t.Fatalf("unexpected rejection: %v", err)
	}
	// A huge idle gap must not let the bucket accumulate beyond capacity 2.
	muchLater := baseTime.Add(1 * time.Hour)
	for i := 0; i < 2; i++ {
		if err := registry.CheckAndReserve("algo-1", 10, muchLater); err != nil {
			t.Fatalf("order %d after long idle: expected allowed (capacity 2), got: %v", i+1, err)
		}
	}
	if err := registry.CheckAndReserve("algo-1", 10, muchLater); !errors.Is(err, ErrOrderRateLimitExceeded) {
		t.Fatalf("expected the 3rd order after a long idle to still be capped at capacity, got: %v", err)
	}
}

func TestCheckAndReserveZeroMaxOrdersPerSecondDisablesRateLimit(t *testing.T) {
	registry := NewRegistry(StrategyLimitConfig{MaxOrdersPerSecond: 0, MaxNotionalPerDayInMinorUnits: 0})

	for i := 0; i < 100; i++ {
		if err := registry.CheckAndReserve("algo-1", 10, baseTime); err != nil {
			t.Fatalf("order %d: expected unlimited rate (MaxOrdersPerSecond=0), got: %v", i, err)
		}
	}
}

func TestCheckAndReserveRejectsExceedingDailyNotionalCap(t *testing.T) {
	registry := NewRegistry(StrategyLimitConfig{MaxOrdersPerSecond: 0, MaxNotionalPerDayInMinorUnits: 1000})

	if err := registry.CheckAndReserve("algo-1", 600, baseTime); err != nil {
		t.Fatalf("first order within cap unexpectedly rejected: %v", err)
	}
	err := registry.CheckAndReserve("algo-1", 500, baseTime)
	if !errors.Is(err, ErrDailyNotionalLimitExceeded) {
		t.Fatalf("expected ErrDailyNotionalLimitExceeded (600+500=1100 > 1000), got: %v", err)
	}
}

func TestCheckAndReserveAllowsExactDailyNotionalCapBoundary(t *testing.T) {
	registry := NewRegistry(StrategyLimitConfig{MaxOrdersPerSecond: 0, MaxNotionalPerDayInMinorUnits: 1000})

	if err := registry.CheckAndReserve("algo-1", 600, baseTime); err != nil {
		t.Fatalf("unexpected rejection: %v", err)
	}
	// exactly at the cap: 600 + 400 == 1000, must be allowed
	if err := registry.CheckAndReserve("algo-1", 400, baseTime); err != nil {
		t.Fatalf("expected exact-boundary order (reaching cap exactly) to be allowed, got: %v", err)
	}
	// one more minor unit tips it over
	if err := registry.CheckAndReserve("algo-1", 1, baseTime); !errors.Is(err, ErrDailyNotionalLimitExceeded) {
		t.Fatalf("expected rejection for exceeding the cap by 1, got: %v", err)
	}
}

func TestCheckAndReserveDailyNotionalResetsAtCalendarDayBoundary(t *testing.T) {
	registry := NewRegistry(StrategyLimitConfig{MaxOrdersPerSecond: 0, MaxNotionalPerDayInMinorUnits: 1000})

	if err := registry.CheckAndReserve("algo-1", 900, baseTime); err != nil {
		t.Fatalf("unexpected rejection: %v", err)
	}
	// Same day, would exceed:
	if err := registry.CheckAndReserve("algo-1", 200, baseTime); !errors.Is(err, ErrDailyNotionalLimitExceeded) {
		t.Fatalf("expected same-day rejection, got: %v", err)
	}

	// Next calendar day (UTC) — even a moment after midnight — resets usage.
	nextDay := time.Date(2026, 1, 16, 0, 0, 1, 0, time.UTC)
	if err := registry.CheckAndReserve("algo-1", 900, nextDay); err != nil {
		t.Fatalf("expected next-day order to be allowed after reset, got: %v", err)
	}
}

func TestCheckAndReserveDailyNotionalDoesNotResetWithinSameDay(t *testing.T) {
	registry := NewRegistry(StrategyLimitConfig{MaxOrdersPerSecond: 0, MaxNotionalPerDayInMinorUnits: 1000})

	morning := time.Date(2026, 1, 15, 1, 0, 0, 0, time.UTC)
	night := time.Date(2026, 1, 15, 23, 59, 0, 0, time.UTC)

	if err := registry.CheckAndReserve("algo-1", 900, morning); err != nil {
		t.Fatalf("unexpected rejection: %v", err)
	}
	if err := registry.CheckAndReserve("algo-1", 200, night); !errors.Is(err, ErrDailyNotionalLimitExceeded) {
		t.Fatalf("expected still-same-UTC-day usage to accumulate and reject, got: %v", err)
	}
}

func TestCheckAndReserveZeroOrNegativeMaxNotionalDisablesCap(t *testing.T) {
	registry := NewRegistry(StrategyLimitConfig{MaxOrdersPerSecond: 0, MaxNotionalPerDayInMinorUnits: 0})

	if err := registry.CheckAndReserve("algo-1", 10_000_000_000, baseTime); err != nil {
		t.Fatalf("expected unlimited notional (cap disabled), got: %v", err)
	}

	registryNegative := NewRegistry(StrategyLimitConfig{MaxOrdersPerSecond: 0, MaxNotionalPerDayInMinorUnits: -1})
	if err := registryNegative.CheckAndReserve("algo-1", 10_000_000_000, baseTime); err != nil {
		t.Fatalf("expected unlimited notional for negative cap too, got: %v", err)
	}
}

func TestCheckAndReserveTracksIndependentStatePerStrategy(t *testing.T) {
	registry := NewRegistry(StrategyLimitConfig{MaxOrdersPerSecond: 1, MaxNotionalPerDayInMinorUnits: 1000})

	if err := registry.CheckAndReserve("algo-1", 900, baseTime); err != nil {
		t.Fatalf("unexpected rejection for algo-1: %v", err)
	}
	// algo-2 has its own independent 1000-unit cap and rate bucket —
	// algo-1's usage must not affect it.
	if err := registry.CheckAndReserve("algo-2", 900, baseTime); err != nil {
		t.Fatalf("expected algo-2 to have independent capacity, got: %v", err)
	}
}

func TestSetStrategyLimitsOverridesDefaultForOneStrategy(t *testing.T) {
	registry := NewRegistry(StrategyLimitConfig{MaxOrdersPerSecond: 0, MaxNotionalPerDayInMinorUnits: 0})
	registry.SetStrategyLimits("tight-algo", StrategyLimitConfig{MaxOrdersPerSecond: 0, MaxNotionalPerDayInMinorUnits: 100})

	if err := registry.CheckAndReserve("tight-algo", 100, baseTime); err != nil {
		t.Fatalf("unexpected rejection at exact cap: %v", err)
	}
	if err := registry.CheckAndReserve("tight-algo", 1, baseTime); !errors.Is(err, ErrDailyNotionalLimitExceeded) {
		t.Fatalf("expected the overridden 100-unit cap to apply, got: %v", err)
	}
	// A DIFFERENT strategy (never overridden) still uses the unlimited default.
	if err := registry.CheckAndReserve("other-algo", 10_000_000, baseTime); err != nil {
		t.Fatalf("expected other-algo to use the unlimited default config, got: %v", err)
	}
}

func TestSetStrategyLimitsPreservesAlreadyAccumulatedUsage(t *testing.T) {
	registry := NewRegistry(StrategyLimitConfig{MaxOrdersPerSecond: 0, MaxNotionalPerDayInMinorUnits: 10_000})

	if err := registry.CheckAndReserve("algo-1", 5_000, baseTime); err != nil {
		t.Fatalf("unexpected rejection: %v", err)
	}
	// Tighten the cap after 5,000 has already been used.
	registry.SetStrategyLimits("algo-1", StrategyLimitConfig{MaxOrdersPerSecond: 0, MaxNotionalPerDayInMinorUnits: 6_000})
	// Only 1,000 of headroom should remain (6000 cap - 5000 already used).
	if err := registry.CheckAndReserve("algo-1", 1_000, baseTime); err != nil {
		t.Fatalf("expected exactly the remaining 1000 headroom to be usable, got: %v", err)
	}
	if err := registry.CheckAndReserve("algo-1", 1, baseTime); !errors.Is(err, ErrDailyNotionalLimitExceeded) {
		t.Fatalf("expected the tightened cap to now reject, got: %v", err)
	}
}

func TestNotionalUsedTodayReflectsCurrentAccumulation(t *testing.T) {
	registry := NewRegistry(StrategyLimitConfig{MaxOrdersPerSecond: 0, MaxNotionalPerDayInMinorUnits: 100_000})

	_ = registry.CheckAndReserve("algo-1", 3_000, baseTime)
	_ = registry.CheckAndReserve("algo-1", 2_000, baseTime)

	if used := registry.NotionalUsedTodayInMinorUnits("algo-1", baseTime); used != 5_000 {
		t.Errorf("expected 5000 used today, got %d", used)
	}
}

func TestNotionalUsedTodayIsZeroForUnknownStrategy(t *testing.T) {
	registry := NewRegistry(StrategyLimitConfig{MaxOrdersPerSecond: 1, MaxNotionalPerDayInMinorUnits: 1000})
	if used := registry.NotionalUsedTodayInMinorUnits("never-seen", baseTime); used != 0 {
		t.Errorf("expected 0 for unknown strategy, got %d", used)
	}
}

func TestNotionalUsedTodayReflectsZeroAfterDayRollsOver(t *testing.T) {
	registry := NewRegistry(StrategyLimitConfig{MaxOrdersPerSecond: 0, MaxNotionalPerDayInMinorUnits: 100_000})
	_ = registry.CheckAndReserve("algo-1", 3_000, baseTime)

	nextDay := baseTime.Add(24 * time.Hour)
	if used := registry.NotionalUsedTodayInMinorUnits("algo-1", nextDay); used != 0 {
		t.Errorf("expected 0 used on a new calendar day, got %d", used)
	}
}

func TestCheckAndReserveRateLimitCheckedBeforeNotionalLimit(t *testing.T) {
	// Both limits are already violated; the returned error must be the
	// rate-limit one, per CheckAndReserve's documented "rate is checked
	// first" order.
	registry := NewRegistry(StrategyLimitConfig{MaxOrdersPerSecond: 1, MaxNotionalPerDayInMinorUnits: 100})
	if err := registry.CheckAndReserve("algo-1", 100, baseTime); err != nil {
		t.Fatalf("unexpected rejection on first order: %v", err)
	}
	// Second order: rate bucket empty AND notional already at cap
	// (100 used, cap 100) — 1 more minor unit would also exceed notional.
	err := registry.CheckAndReserve("algo-1", 1, baseTime)
	if !errors.Is(err, ErrOrderRateLimitExceeded) {
		t.Fatalf("expected rate limit error to take precedence, got: %v", err)
	}
}

func TestCheckAndReserveSubOneRateAllowsPeriodicSingleOrderBurst(t *testing.T) {
	// A strategy configured with MaxOrdersPerSecond: 0.5 ("1 order per 2
	// seconds") must genuinely be able to place an order every 2 seconds
	// -- not be permanently blocked because the token bucket's capacity
	// was capped at 0.5, meaning tokensAvailable could never reach 1.0.
	registry := NewRegistry(StrategyLimitConfig{MaxOrdersPerSecond: 0.5, MaxNotionalPerDayInMinorUnits: 0})

	if err := registry.CheckAndReserve("algo-1", 10, baseTime); err != nil {
		t.Fatalf("expected the very first order to be allowed (bucket starts at capacity), got: %v", err)
	}
	// Immediately after, no tokens left -- correctly rejected.
	if err := registry.CheckAndReserve("algo-1", 10, baseTime); !errors.Is(err, ErrOrderRateLimitExceeded) {
		t.Fatalf("expected immediate second order to be rejected, got: %v", err)
	}
	// After 2 full seconds (1 token's worth at 0.5/sec), a single order
	// must be allowed again -- this is the exact bug: with capacity
	// wrongly capped at 0.5, tokensAvailable would plateau at 0.5 and
	// this would ALWAYS reject.
	twoSecondsLater := baseTime.Add(2 * time.Second)
	if err := registry.CheckAndReserve("algo-1", 10, twoSecondsLater); err != nil {
		t.Fatalf("expected an order 2 seconds later to be allowed for a 0.5/sec strategy, got: %v", err)
	}
}

func TestCheckAndReserveSubOneRateNeverAccumulatesMoreThanOneTokenCapacity(t *testing.T) {
	// The capacity floor must not let a sub-1 rate strategy burst beyond
	// 1 order even after a very long idle period.
	registry := NewRegistry(StrategyLimitConfig{MaxOrdersPerSecond: 0.1, MaxNotionalPerDayInMinorUnits: 0})

	muchLater := baseTime.Add(1 * time.Hour)
	if err := registry.CheckAndReserve("algo-1", 10, muchLater); err != nil {
		t.Fatalf("expected first order after long idle to be allowed, got: %v", err)
	}
	if err := registry.CheckAndReserve("algo-1", 10, muchLater); !errors.Is(err, ErrOrderRateLimitExceeded) {
		t.Fatalf("expected the immediate second order to still be rejected (capacity floor is 1, not unbounded), got: %v", err)
	}
}

func TestReleaseReturnsRateTokenAndNotionalCapacity(t *testing.T) {
	registry := NewRegistry(StrategyLimitConfig{MaxOrdersPerSecond: 1, MaxNotionalPerDayInMinorUnits: 1000})

	if err := registry.CheckAndReserve("algo-1", 900, baseTime); err != nil {
		t.Fatalf("unexpected rejection: %v", err)
	}
	// Bucket and notional cap are both now exhausted.
	if err := registry.CheckAndReserve("algo-1", 900, baseTime); err == nil {
		t.Fatalf("expected rejection before Release")
	}

	registry.Release("algo-1", 900, baseTime)

	if used := registry.NotionalUsedTodayInMinorUnits("algo-1", baseTime); used != 0 {
		t.Errorf("expected notional usage to be floored back to 0 after Release, got %d", used)
	}
	if err := registry.CheckAndReserve("algo-1", 900, baseTime); err != nil {
		t.Fatalf("expected order to succeed after Release gave back both rate token and notional capacity, got: %v", err)
	}
}

func TestReleaseFlooredAtZeroNeverGoesNegative(t *testing.T) {
	registry := NewRegistry(StrategyLimitConfig{MaxOrdersPerSecond: 1, MaxNotionalPerDayInMinorUnits: 1000})

	// Release without any prior reservation must not panic or underflow.
	registry.Release("never-reserved", 500, baseTime)

	if used := registry.NotionalUsedTodayInMinorUnits("never-reserved", baseTime); used != 0 {
		t.Errorf("expected 0, got %d", used)
	}
}

func TestConcurrentCheckAndReserveNeverExceedsRateLimitCapacity(t *testing.T) {
	registry := NewRegistry(StrategyLimitConfig{MaxOrdersPerSecond: 10, MaxNotionalPerDayInMinorUnits: 0})

	const numberOfConcurrentAttempts = 50
	results := make(chan error, numberOfConcurrentAttempts)
	for i := 0; i < numberOfConcurrentAttempts; i++ {
		go func() {
			results <- registry.CheckAndReserve("algo-1", 10, baseTime)
		}()
	}

	successCount := 0
	for i := 0; i < numberOfConcurrentAttempts; i++ {
		if err := <-results; err == nil {
			successCount++
		}
	}
	if successCount != 10 {
		t.Errorf("expected exactly 10 successful reservations (bucket capacity), got %d", successCount)
	}
}
