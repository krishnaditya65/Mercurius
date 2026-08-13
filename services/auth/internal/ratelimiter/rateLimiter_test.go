package ratelimiter

import (
	"testing"
	"time"
)

var testNow = time.Unix(1_700_000_000, 0)

func TestFirstAttemptWithinLimitIsAllowed(t *testing.T) {
	limiter := NewRateLimiter(3, time.Minute)
	if !limiter.Allow("jane@example.com", testNow) {
		t.Fatal("expected the first attempt to be allowed")
	}
}

func TestAttemptsUpToTheLimitAreAllowedThenTheNextIsRejected(t *testing.T) {
	limiter := NewRateLimiter(3, time.Minute)
	key := "jane@example.com"

	for attemptIndex := 0; attemptIndex < 3; attemptIndex++ {
		if !limiter.Allow(key, testNow.Add(time.Duration(attemptIndex)*time.Second)) {
			t.Fatalf("expected attempt %d (within the limit of 3) to be allowed", attemptIndex+1)
		}
	}

	if limiter.Allow(key, testNow.Add(4*time.Second)) {
		t.Fatal("expected the 4th attempt within the window to be rejected")
	}
}

func TestRejectedAttemptsStillCountTowardTheLimit(t *testing.T) {
	limiter := NewRateLimiter(2, time.Minute)
	key := "jane@example.com"

	limiter.Allow(key, testNow)
	limiter.Allow(key, testNow.Add(time.Second))
	// Both slots used. This third call is rejected AND must itself be
	// recorded — an attacker shouldn't get a "free" retry because a
	// previous attempt was denied.
	limiter.Allow(key, testNow.Add(2*time.Second))

	count := limiter.AttemptCountInCurrentWindow(key, testNow.Add(2*time.Second))
	if count != 3 {
		t.Fatalf("expected the rejected attempt to still be counted, got count=%d", count)
	}
}

func TestAttemptsOutsideTheWindowExpireAndAreAllowedAgain(t *testing.T) {
	limiter := NewRateLimiter(2, time.Minute)
	key := "jane@example.com"

	limiter.Allow(key, testNow)
	limiter.Allow(key, testNow.Add(time.Second))
	if limiter.Allow(key, testNow.Add(2*time.Second)) {
		t.Fatal("expected the limit to be hit within the window")
	}

	// Well past the 1-minute window — the earlier attempts should have
	// aged out, freeing up capacity again.
	if !limiter.Allow(key, testNow.Add(2*time.Minute)) {
		t.Fatal("expected an attempt after the window has fully elapsed to be allowed")
	}
}

func TestDifferentKeysHaveIndependentLimits(t *testing.T) {
	limiter := NewRateLimiter(1, time.Minute)

	if !limiter.Allow("jane@example.com", testNow) {
		t.Fatal("expected jane's first attempt to be allowed")
	}
	if limiter.Allow("jane@example.com", testNow.Add(time.Second)) {
		t.Fatal("expected jane's second attempt to be rejected")
	}
	if !limiter.Allow("bob@example.com", testNow.Add(time.Second)) {
		t.Fatal("expected bob's first attempt to be allowed independently of jane's limit")
	}
}

func TestSlidingWindowDoesNotAllowADoubleBurstAcrossAFixedBoundary(t *testing.T) {
	// Regression test for exactly the failure mode a naive CALENDAR-
	// ALIGNED fixed window (e.g. bucket = floor(now/60s), reset whenever
	// the bucket changes) has: two attempts just before a bucket
	// boundary plus two more just after it would land in two different
	// buckets and all four would be allowed — a 2x burst in a fraction
	// of a second. A true sliding window must still count the
	// still-recent earlier attempts and reject the later ones.
	limiter := NewRateLimiter(2, time.Minute)
	key := "jane@example.com"

	limiter.Allow(key, testNow.Add(59*time.Second))         // uses slot 1, just before the 60s boundary
	limiter.Allow(key, testNow.Add(59500*time.Millisecond)) // uses slot 2, still just before the boundary

	// Just AFTER the nominal 60s boundary — a naive fixed window keyed
	// on minute buckets would treat this as a fresh window with 2 free
	// slots. The sliding window must instead see both prior attempts are
	// still within the last 60 seconds (only ~1-1.5s old) and reject.
	if limiter.Allow(key, testNow.Add(60500*time.Millisecond)) {
		t.Fatal("expected the sliding window to still count attempts from just over a second ago and reject the burst")
	}
}
