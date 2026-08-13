// Package ratelimiter implements FEATURES.md §13/§18's "API gateway
// rate limiting, quota tiers (retail vs. institutional)" — a real,
// in-memory, per-key token-bucket limiter. No external dependency (same
// "hand-roll it" convention as services/oms-gateway/internal/metrics):
// each API key gets its own bucket that refills continuously at its
// tier's configured rate and can burst up to its tier's configured
// capacity.
package ratelimiter

import (
	"sync"
	"time"
)

// RateLimitTier names one quota tier. FEATURES.md explicitly calls out
// "retail vs. institutional" — this package supports an arbitrary named
// set of tiers (not just those two) so internal/tenantconfig can layer
// white-label tenants with their own tiers on top without this package
// needing to know about tenancy at all.
type RateLimitTier string

const (
	RateLimitTierRetail        RateLimitTier = "RETAIL"
	RateLimitTierInstitutional RateLimitTier = "INSTITUTIONAL"
	RateLimitTierSandbox       RateLimitTier = "SANDBOX"
)

// TierLimit is one tier's token-bucket configuration: it refills at
// RequestsPerSecond and can hold at most BurstCapacity tokens at once
// (so a client that's been idle can burst above its steady-state rate
// briefly, then is throttled back down to RequestsPerSecond).
type TierLimit struct {
	RequestsPerSecond float64
	BurstCapacity     float64
}

// DefaultTierLimits is the reference tier configuration FEATURES.md's
// example numbers describe: "RETAIL: 10 req/s, INSTITUTIONAL: 200
// req/s". SANDBOX is deliberately tighter than RETAIL — sandbox traffic
// is for integration testing, not production volume.
var DefaultTierLimits = map[RateLimitTier]TierLimit{
	RateLimitTierRetail:        {RequestsPerSecond: 10, BurstCapacity: 20},
	RateLimitTierInstitutional: {RequestsPerSecond: 200, BurstCapacity: 400},
	RateLimitTierSandbox:       {RequestsPerSecond: 5, BurstCapacity: 10},
}

// tokenBucket is one API key's mutable rate-limit state.
type tokenBucket struct {
	tokensAvailable   float64
	lastRefillAtTime  time.Time
	requestsPerSecond float64
	burstCapacity     float64
}

// TokenBucketRateLimiter tracks one token bucket per rate-limit key
// (typically an API key, but callers may use any string identity). Safe
// for concurrent use.
type TokenBucketRateLimiter struct {
	mutexGuardingBuckets sync.Mutex
	bucketsByKey         map[string]*tokenBucket
	tierLimitsByTier     map[RateLimitTier]TierLimit
	nowFunc              func() time.Time
}

// NewTokenBucketRateLimiter returns a limiter using tierLimits for its
// per-tier configuration. Pass DefaultTierLimits for the reference
// configuration, or a custom map to override it (e.g. per-tenant limits
// — see internal/tenantconfig).
func NewTokenBucketRateLimiter(tierLimits map[RateLimitTier]TierLimit) *TokenBucketRateLimiter {
	return &TokenBucketRateLimiter{
		bucketsByKey:     make(map[string]*tokenBucket),
		tierLimitsByTier: tierLimits,
		nowFunc:          time.Now,
	}
}

// AllowRequest reports whether a request identified by rateLimitKey
// under tier is allowed right now, consuming one token if so. Returns
// false (and consumes nothing) if the bucket has no token available.
// An unknown tier is treated as having zero capacity — deny everything —
// rather than silently allowing unlimited traffic, since a
// misconfigured/unknown tier is exactly the situation where failing
// closed matters most for a paid-API rate limiter.
func (limiter *TokenBucketRateLimiter) AllowRequest(rateLimitKey string, tier RateLimitTier) bool {
	limiter.mutexGuardingBuckets.Lock()
	defer limiter.mutexGuardingBuckets.Unlock()

	tierLimit, tierKnown := limiter.tierLimitsByTier[tier]
	if !tierKnown {
		return false
	}

	now := limiter.nowFunc()
	bucket, bucketExists := limiter.bucketsByKey[rateLimitKey]
	if !bucketExists {
		bucket = &tokenBucket{
			tokensAvailable:   tierLimit.BurstCapacity,
			lastRefillAtTime:  now,
			requestsPerSecond: tierLimit.RequestsPerSecond,
			burstCapacity:     tierLimit.BurstCapacity,
		}
		limiter.bucketsByKey[rateLimitKey] = bucket
	} else {
		// Tier limits can change between requests (e.g. an operator
		// re-tiers an API key) — always apply the CURRENT tier's rate
		// going forward, without resetting tokens already accrued.
		bucket.requestsPerSecond = tierLimit.RequestsPerSecond
		bucket.burstCapacity = tierLimit.BurstCapacity
	}

	elapsedSeconds := now.Sub(bucket.lastRefillAtTime).Seconds()
	if elapsedSeconds > 0 {
		bucket.tokensAvailable += elapsedSeconds * bucket.requestsPerSecond
		if bucket.tokensAvailable > bucket.burstCapacity {
			bucket.tokensAvailable = bucket.burstCapacity
		}
		bucket.lastRefillAtTime = now
	}

	if bucket.tokensAvailable < 1 {
		return false
	}
	bucket.tokensAvailable--
	return true
}

// WithClock overrides the limiter's time source — test-only hook so
// refill behavior can be verified deterministically without sleeping.
func (limiter *TokenBucketRateLimiter) WithClock(nowFunc func() time.Time) *TokenBucketRateLimiter {
	limiter.mutexGuardingBuckets.Lock()
	defer limiter.mutexGuardingBuckets.Unlock()
	limiter.nowFunc = nowFunc
	return limiter
}

// RemainingTokens returns the current token count for rateLimitKey,
// rounded down — 0 if the key has never made a request. Useful for
// exposing a `X-RateLimit-Remaining`-style response header.
func (limiter *TokenBucketRateLimiter) RemainingTokens(rateLimitKey string) float64 {
	limiter.mutexGuardingBuckets.Lock()
	defer limiter.mutexGuardingBuckets.Unlock()

	bucket, exists := limiter.bucketsByKey[rateLimitKey]
	if !exists {
		return 0
	}
	return bucket.tokensAvailable
}
