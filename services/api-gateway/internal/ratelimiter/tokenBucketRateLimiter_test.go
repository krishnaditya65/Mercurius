package ratelimiter

import (
	"testing"
	"time"
)

func testTierLimits() map[RateLimitTier]TierLimit {
	return map[RateLimitTier]TierLimit{
		RateLimitTierRetail:        {RequestsPerSecond: 10, BurstCapacity: 5},
		RateLimitTierInstitutional: {RequestsPerSecond: 200, BurstCapacity: 50},
	}
}

func TestFirstRequestIsAllowedAndConsumesOneToken(t *testing.T) {
	limiter := NewTokenBucketRateLimiter(testTierLimits())
	if !limiter.AllowRequest("key-1", RateLimitTierRetail) {
		t.Fatalf("expected first request to be allowed")
	}
	if remaining := limiter.RemainingTokens("key-1"); remaining != 4 {
		t.Fatalf("expected 4 remaining tokens after one consumed from a burst of 5, got %v", remaining)
	}
}

func TestBurstCapacityIsExhaustedThenDenied(t *testing.T) {
	limiter := NewTokenBucketRateLimiter(testTierLimits())
	fixedNow := time.Now()
	limiter.WithClock(func() time.Time { return fixedNow })

	for i := 0; i < 5; i++ {
		if !limiter.AllowRequest("key-1", RateLimitTierRetail) {
			t.Fatalf("expected request %d within burst capacity to be allowed", i)
		}
	}
	if limiter.AllowRequest("key-1", RateLimitTierRetail) {
		t.Fatalf("expected the 6th request beyond burst capacity 5 to be denied")
	}
}

func TestTokensRefillOverTime(t *testing.T) {
	limiter := NewTokenBucketRateLimiter(testTierLimits())
	currentTime := time.Now()
	limiter.WithClock(func() time.Time { return currentTime })

	for i := 0; i < 5; i++ {
		limiter.AllowRequest("key-1", RateLimitTierRetail)
	}
	if limiter.AllowRequest("key-1", RateLimitTierRetail) {
		t.Fatalf("expected bucket to be exhausted")
	}

	// Retail refills at 10/s; advance 200ms => +2 tokens.
	currentTime = currentTime.Add(200 * time.Millisecond)
	if !limiter.AllowRequest("key-1", RateLimitTierRetail) {
		t.Fatalf("expected a refilled token to allow the request")
	}
}

func TestRefillNeverExceedsBurstCapacity(t *testing.T) {
	limiter := NewTokenBucketRateLimiter(testTierLimits())
	currentTime := time.Now()
	limiter.WithClock(func() time.Time { return currentTime })

	limiter.AllowRequest("key-1", RateLimitTierRetail) // 5 -> 4
	currentTime = currentTime.Add(10 * time.Hour)      // would refill far beyond capacity
	limiter.AllowRequest("key-1", RateLimitTierRetail) // clamps to burst capacity, then -1

	if remaining := limiter.RemainingTokens("key-1"); remaining != 4 {
		t.Fatalf("expected remaining tokens clamped at burstCapacity-1=4, got %v", remaining)
	}
}

func TestDifferentKeysHaveIndependentBuckets(t *testing.T) {
	limiter := NewTokenBucketRateLimiter(testTierLimits())
	fixedNow := time.Now()
	limiter.WithClock(func() time.Time { return fixedNow })

	for i := 0; i < 5; i++ {
		limiter.AllowRequest("key-1", RateLimitTierRetail)
	}
	if !limiter.AllowRequest("key-2", RateLimitTierRetail) {
		t.Fatalf("expected an independent key to have its own untouched bucket")
	}
}

func TestInstitutionalTierAllowsFarMoreThroughputThanRetail(t *testing.T) {
	limiter := NewTokenBucketRateLimiter(testTierLimits())
	fixedNow := time.Now()
	limiter.WithClock(func() time.Time { return fixedNow })

	allowedCount := 0
	for i := 0; i < 50; i++ {
		if limiter.AllowRequest("institutional-key", RateLimitTierInstitutional) {
			allowedCount++
		}
	}
	if allowedCount != 50 {
		t.Fatalf("expected institutional tier's burst capacity of 50 to allow all 50 requests, got %d", allowedCount)
	}
}

func TestUnknownTierIsDeniedNotUnlimited(t *testing.T) {
	limiter := NewTokenBucketRateLimiter(testTierLimits())
	if limiter.AllowRequest("key-1", RateLimitTier("NOT_A_REAL_TIER")) {
		t.Fatalf("expected an unknown tier to fail closed (deny), not allow unlimited traffic")
	}
}

func TestRemainingTokensForUnknownKeyIsZero(t *testing.T) {
	limiter := NewTokenBucketRateLimiter(testTierLimits())
	if remaining := limiter.RemainingTokens("never-seen"); remaining != 0 {
		t.Fatalf("expected 0 for a never-seen key, got %v", remaining)
	}
}

func TestBoundaryExactlyOneTokenAvailableIsAllowed(t *testing.T) {
	limiter := NewTokenBucketRateLimiter(testTierLimits())
	fixedNow := time.Now()
	limiter.WithClock(func() time.Time { return fixedNow })

	for i := 0; i < 4; i++ {
		limiter.AllowRequest("key-1", RateLimitTierRetail)
	}
	if remaining := limiter.RemainingTokens("key-1"); remaining != 1 {
		t.Fatalf("expected exactly 1 token remaining, got %v", remaining)
	}
	if !limiter.AllowRequest("key-1", RateLimitTierRetail) {
		t.Fatalf("expected the exact boundary of 1 available token to allow the request")
	}
	if limiter.AllowRequest("key-1", RateLimitTierRetail) {
		t.Fatalf("expected the immediately following request with 0 tokens to be denied")
	}
}

func TestRetierAppliesNewRateWithoutResettingAccruedTokens(t *testing.T) {
	limiter := NewTokenBucketRateLimiter(testTierLimits())
	fixedNow := time.Now()
	limiter.WithClock(func() time.Time { return fixedNow })

	limiter.AllowRequest("key-1", RateLimitTierRetail) // 5 -> 4 tokens
	if !limiter.AllowRequest("key-1", RateLimitTierInstitutional) {
		t.Fatalf("expected the retiered request to still be allowed from the 4 accrued tokens")
	}
	if remaining := limiter.RemainingTokens("key-1"); remaining != 3 {
		t.Fatalf("expected 3 remaining tokens carried over across a tier change, got %v", remaining)
	}
}

func TestDefaultTierLimitsMatchFeaturesDotMdExampleNumbers(t *testing.T) {
	if DefaultTierLimits[RateLimitTierRetail].RequestsPerSecond != 10 {
		t.Fatalf("expected RETAIL default of 10 req/s per FEATURES.md, got %v", DefaultTierLimits[RateLimitTierRetail].RequestsPerSecond)
	}
	if DefaultTierLimits[RateLimitTierInstitutional].RequestsPerSecond != 200 {
		t.Fatalf("expected INSTITUTIONAL default of 200 req/s per FEATURES.md, got %v", DefaultTierLimits[RateLimitTierInstitutional].RequestsPerSecond)
	}
}
