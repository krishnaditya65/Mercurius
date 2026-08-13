// Package ratelimiter implements a per-key sliding-window rate limiter —
// closes the "no rate limiting on /auth/login (brute-force protection)"
// gap flagged in this service's own README/BUILD_LOG since it was first
// built. Applied per-email on login (stop brute-forcing ONE account's
// password) and per-remote-address on register (stop registration spam
// from one source) — see cmd/server/main.go for the wiring.
package ratelimiter

import (
	"sync"
	"time"
)

// RateLimiter is safe for concurrent use.
type RateLimiter struct {
	mutexGuardingAttempts  sync.Mutex
	attemptTimestampsByKey map[string][]time.Time
	maxAttemptsPerWindow   int
	windowDuration         time.Duration
}

// NewRateLimiter allows at most maxAttemptsPerWindow calls to Allow for
// the same key within any windowDuration-wide sliding window.
func NewRateLimiter(maxAttemptsPerWindow int, windowDuration time.Duration) *RateLimiter {
	return &RateLimiter{
		attemptTimestampsByKey: make(map[string][]time.Time),
		maxAttemptsPerWindow:   maxAttemptsPerWindow,
		windowDuration:         windowDuration,
	}
}

// Allow records one attempt for key at time now and reports whether it
// should be permitted. A SLIDING window, not a fixed one: prunes every
// timestamp for this key older than `now - windowDuration` before
// counting, so the limit is enforced continuously rather than resetting
// all-at-once on a fixed boundary (which would let a burst of
// maxAttemptsPerWindow land right before AND right after a boundary,
// briefly doubling the effective rate).
//
// The attempt is always recorded, including when it's rejected — a
// rejected call still counts toward future windows, so an attacker can't
// get a free extra try by having some requests denied.
func (limiter *RateLimiter) Allow(key string, now time.Time) bool {
	limiter.mutexGuardingAttempts.Lock()
	defer limiter.mutexGuardingAttempts.Unlock()

	windowStart := now.Add(-limiter.windowDuration)
	existingTimestamps := limiter.attemptTimestampsByKey[key]

	prunedTimestamps := existingTimestamps[:0]
	for _, timestamp := range existingTimestamps {
		if timestamp.After(windowStart) {
			prunedTimestamps = append(prunedTimestamps, timestamp)
		}
	}

	isAllowed := len(prunedTimestamps) < limiter.maxAttemptsPerWindow
	prunedTimestamps = append(prunedTimestamps, now)
	limiter.attemptTimestampsByKey[key] = prunedTimestamps

	return isAllowed
}

// AttemptCountInCurrentWindow reports how many attempts key has made
// within the current sliding window, without recording a new one — used
// by the HTTP handler to compute a `Retry-After`-style hint. Exported
// mainly for tests and diagnostics.
func (limiter *RateLimiter) AttemptCountInCurrentWindow(key string, now time.Time) int {
	limiter.mutexGuardingAttempts.Lock()
	defer limiter.mutexGuardingAttempts.Unlock()

	windowStart := now.Add(-limiter.windowDuration)
	count := 0
	for _, timestamp := range limiter.attemptTimestampsByKey[key] {
		if timestamp.After(windowStart) {
			count++
		}
	}
	return count
}
