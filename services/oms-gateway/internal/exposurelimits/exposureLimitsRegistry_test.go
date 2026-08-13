package exposurelimits

import (
	"errors"
	"sync"
	"testing"
)

func TestClassifySegment(t *testing.T) {
	cases := map[string]string{
		"DEMO-EQ":      SegmentEquity,
		"NIFTY-FUT":    SegmentFuturesAndOptions,
		"NIFTY-OPT":    SegmentFuturesAndOptions,
		"USDINR-CUR":   SegmentCurrency,
		"WEIRD-THING":  SegmentOther,
		"lowercase-eq": SegmentEquity,
	}
	for symbol, expected := range cases {
		if got := ClassifySegment(symbol); got != expected {
			t.Errorf("ClassifySegment(%q) = %q, want %q", symbol, got, expected)
		}
	}
}

func TestCheckAndReserveExposure_UnconfiguredAccountIsUnconstrained(t *testing.T) {
	registry := NewLimitsRegistry()
	if err := registry.CheckAndReserveExposure("acct-1", SegmentEquity, 999999999); err != nil {
		t.Fatalf("expected no error for unconfigured account, got %v", err)
	}
}

func TestCheckAndReserveExposure_AccountLimitRejectsOverage(t *testing.T) {
	registry := NewLimitsRegistry()
	registry.SetAccountLimit("acct-1", 10000)

	if err := registry.CheckAndReserveExposure("acct-1", SegmentEquity, 5000); err != nil {
		t.Fatalf("expected first order to succeed, got %v", err)
	}
	if err := registry.CheckAndReserveExposure("acct-1", SegmentEquity, 5001); !errors.Is(err, ErrAccountExposureLimitExceeded) {
		t.Fatalf("expected ErrAccountExposureLimitExceeded, got %v", err)
	}
	// exact-at-limit is allowed
	if err := registry.CheckAndReserveExposure("acct-1", SegmentEquity, 5000); err != nil {
		t.Fatalf("expected exact-at-limit order to succeed, got %v", err)
	}
}

func TestCheckAndReserveExposure_SegmentLimitRejectsOverageEvenUnderAccountLimit(t *testing.T) {
	registry := NewLimitsRegistry()
	registry.SetAccountLimit("acct-1", 1000000)              // generous account cap
	registry.SetSegmentLimit("acct-1", SegmentEquity, 10000) // tight segment cap

	if err := registry.CheckAndReserveExposure("acct-1", SegmentEquity, 10001); !errors.Is(err, ErrSegmentExposureLimitExceeded) {
		t.Fatalf("expected ErrSegmentExposureLimitExceeded, got %v", err)
	}
}

func TestCheckAndReserveExposure_RejectionDoesNotMutateEitherRunningTotal(t *testing.T) {
	registry := NewLimitsRegistry()
	registry.SetAccountLimit("acct-1", 10000)

	_ = registry.CheckAndReserveExposure("acct-1", SegmentEquity, 5000)
	beforeAccount := registry.CurrentAccountExposure("acct-1")
	beforeSegment := registry.CurrentSegmentExposure("acct-1", SegmentEquity)

	err := registry.CheckAndReserveExposure("acct-1", SegmentEquity, 99999)
	if err == nil {
		t.Fatalf("expected rejection")
	}
	if registry.CurrentAccountExposure("acct-1") != beforeAccount {
		t.Fatalf("expected account exposure unchanged after rejection")
	}
	if registry.CurrentSegmentExposure("acct-1", SegmentEquity) != beforeSegment {
		t.Fatalf("expected segment exposure unchanged after rejection")
	}
}

func TestCheckAndReserveExposure_DifferentSegmentsTrackedIndependently(t *testing.T) {
	registry := NewLimitsRegistry()
	registry.SetSegmentLimit("acct-1", SegmentEquity, 10000)
	registry.SetSegmentLimit("acct-1", SegmentFuturesAndOptions, 5000)

	if err := registry.CheckAndReserveExposure("acct-1", SegmentEquity, 10000); err != nil {
		t.Fatalf("expected equity order to succeed, got %v", err)
	}
	// F&O segment is independent, should still succeed at its own limit
	if err := registry.CheckAndReserveExposure("acct-1", SegmentFuturesAndOptions, 5000); err != nil {
		t.Fatalf("expected F&O order to succeed under its own segment cap, got %v", err)
	}
	if err := registry.CheckAndReserveExposure("acct-1", SegmentFuturesAndOptions, 1); !errors.Is(err, ErrSegmentExposureLimitExceeded) {
		t.Fatalf("expected F&O segment now exceeded, got %v", err)
	}
}

func TestCheckAndReserveExposure_DifferentAccountsIndependent(t *testing.T) {
	registry := NewLimitsRegistry()
	registry.SetAccountLimit("acct-1", 1000)
	registry.SetAccountLimit("acct-2", 1000)

	if err := registry.CheckAndReserveExposure("acct-1", SegmentEquity, 1000); err != nil {
		t.Fatalf("expected acct-1 order to succeed, got %v", err)
	}
	if err := registry.CheckAndReserveExposure("acct-2", SegmentEquity, 1000); err != nil {
		t.Fatalf("expected acct-2 order to succeed independently, got %v", err)
	}
}

func TestReleaseExposure_ReducesRunningTotalsFlooredAtZero(t *testing.T) {
	registry := NewLimitsRegistry()
	registry.SetAccountLimit("acct-1", 10000)
	_ = registry.CheckAndReserveExposure("acct-1", SegmentEquity, 5000)

	registry.ReleaseExposure("acct-1", SegmentEquity, 2000)
	if got := registry.CurrentAccountExposure("acct-1"); got != 3000 {
		t.Fatalf("expected 3000 after release, got %d", got)
	}
	if got := registry.CurrentSegmentExposure("acct-1", SegmentEquity); got != 3000 {
		t.Fatalf("expected segment 3000 after release, got %d", got)
	}

	registry.ReleaseExposure("acct-1", SegmentEquity, 99999)
	if got := registry.CurrentAccountExposure("acct-1"); got != 0 {
		t.Fatalf("expected floor at 0, got %d", got)
	}
}

func TestCheckAndReserveExposure_ZeroOrNegativeNotionalNeverRejected(t *testing.T) {
	registry := NewLimitsRegistry()
	registry.SetAccountLimit("acct-1", 100)
	if err := registry.CheckAndReserveExposure("acct-1", SegmentEquity, 0); err != nil {
		t.Fatalf("expected no error for zero notional, got %v", err)
	}
	if err := registry.CheckAndReserveExposure("acct-1", SegmentEquity, -5); err != nil {
		t.Fatalf("expected no error for negative notional, got %v", err)
	}
}

func TestAccountLimit_ReturnsConfiguredFlag(t *testing.T) {
	registry := NewLimitsRegistry()
	if _, configured := registry.AccountLimit("acct-1"); configured {
		t.Fatalf("expected not configured before SetAccountLimit")
	}
	registry.SetAccountLimit("acct-1", 500)
	limit, configured := registry.AccountLimit("acct-1")
	if !configured || limit != 500 {
		t.Fatalf("expected configured=true limit=500, got configured=%v limit=%d", configured, limit)
	}
}

func TestSegmentLimit_ReturnsConfiguredFlag(t *testing.T) {
	registry := NewLimitsRegistry()
	if _, configured := registry.SegmentLimit("acct-1", SegmentEquity); configured {
		t.Fatalf("expected not configured before SetSegmentLimit")
	}
	registry.SetSegmentLimit("acct-1", SegmentEquity, 700)
	limit, configured := registry.SegmentLimit("acct-1", SegmentEquity)
	if !configured || limit != 700 {
		t.Fatalf("expected configured=true limit=700, got configured=%v limit=%d", configured, limit)
	}
}

func TestConcurrentCheckAndReserveExposure_NeverExceedsLimit(t *testing.T) {
	registry := NewLimitsRegistry()
	registry.SetAccountLimit("acct-1", 1000)

	var waitGroup sync.WaitGroup
	var mutex sync.Mutex
	successCount := 0
	for i := 0; i < 200; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			if err := registry.CheckAndReserveExposure("acct-1", SegmentEquity, 10); err == nil {
				mutex.Lock()
				successCount++
				mutex.Unlock()
			}
		}()
	}
	waitGroup.Wait()

	if successCount != 100 {
		t.Fatalf("expected exactly 100 successes (100*10=1000=limit), got %d", successCount)
	}
	if registry.CurrentAccountExposure("acct-1") != 1000 {
		t.Fatalf("expected final exposure exactly at limit 1000, got %d", registry.CurrentAccountExposure("acct-1"))
	}
}
