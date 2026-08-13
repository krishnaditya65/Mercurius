package connectivitykillswitch

import (
	"sync"
	"testing"
)

func TestNewKillSwitch_StartsNotHalted(t *testing.T) {
	killSwitch := NewKillSwitch(3)
	if killSwitch.IsTradingHalted() {
		t.Fatalf("expected not halted at construction")
	}
}

func TestEngageManually_HaltsTrading(t *testing.T) {
	killSwitch := NewKillSwitch(3)
	killSwitch.EngageManually("admin decided to halt trading")
	if !killSwitch.IsTradingHalted() {
		t.Fatalf("expected halted after manual engage")
	}
	status := killSwitch.CurrentStatus()
	if !status.IsManuallyEngaged || status.ManualEngagementReason != "admin decided to halt trading" {
		t.Fatalf("unexpected status: %+v", status)
	}
	if status.ManualEngagedAtTime == nil {
		t.Fatalf("expected a recorded engagement time")
	}
}

func TestDisengageManually_ClearsManualFlagOnly(t *testing.T) {
	killSwitch := NewKillSwitch(3)
	killSwitch.EngageManually("reason")
	killSwitch.DisengageManually()
	if killSwitch.IsTradingHalted() {
		t.Fatalf("expected not halted after disengage (no auto engagement pending)")
	}
	status := killSwitch.CurrentStatus()
	if status.IsManuallyEngaged || status.ManualEngagementReason != "" || status.ManualEngagedAtTime != nil {
		t.Fatalf("expected manual state fully cleared, got %+v", status)
	}
}

func TestDisengageManually_DoesNotClearActiveAutoEngagement(t *testing.T) {
	killSwitch := NewKillSwitch(2)
	killSwitch.EngageManually("admin halt")
	killSwitch.RecordConnectivityCheckResult(false)
	killSwitch.RecordConnectivityCheckResult(false) // trips auto engagement too

	killSwitch.DisengageManually()
	if !killSwitch.IsTradingHalted() {
		t.Fatalf("expected STILL halted: auto engagement is independent of manual disengage")
	}
	status := killSwitch.CurrentStatus()
	if !status.IsAutoEngaged {
		t.Fatalf("expected auto engagement still set")
	}
}

func TestRecordConnectivityCheckResult_AutoEngagesAtThreshold(t *testing.T) {
	killSwitch := NewKillSwitch(3)

	if killSwitch.RecordConnectivityCheckResult(false) {
		t.Fatalf("expected no auto-engagement yet (1/3)")
	}
	if killSwitch.IsTradingHalted() {
		t.Fatalf("expected not halted yet (1/3 failures)")
	}
	if killSwitch.RecordConnectivityCheckResult(false) {
		t.Fatalf("expected no auto-engagement yet (2/3)")
	}
	if killSwitch.IsTradingHalted() {
		t.Fatalf("expected not halted yet (2/3 failures)")
	}
	if !killSwitch.RecordConnectivityCheckResult(false) {
		t.Fatalf("expected auto-engagement to trip on the 3rd consecutive failure")
	}
	if !killSwitch.IsTradingHalted() {
		t.Fatalf("expected halted after threshold reached")
	}
}

func TestRecordConnectivityCheckResult_SuccessResetsCounterAndClearsAutoFlag(t *testing.T) {
	killSwitch := NewKillSwitch(3)
	killSwitch.RecordConnectivityCheckResult(false)
	killSwitch.RecordConnectivityCheckResult(false)
	killSwitch.RecordConnectivityCheckResult(false) // now auto-engaged
	if !killSwitch.IsTradingHalted() {
		t.Fatalf("expected halted before recovery")
	}

	killSwitch.RecordConnectivityCheckResult(true)
	if killSwitch.IsTradingHalted() {
		t.Fatalf("expected auto engagement cleared by a successful check")
	}
	status := killSwitch.CurrentStatus()
	if status.ConsecutiveFailureCount != 0 {
		t.Fatalf("expected failure count reset to 0, got %d", status.ConsecutiveFailureCount)
	}
}

func TestRecordConnectivityCheckResult_NonPositiveThresholdNeverAutoEngages(t *testing.T) {
	killSwitch := NewKillSwitch(0)
	for i := 0; i < 100; i++ {
		killSwitch.RecordConnectivityCheckResult(false)
	}
	if killSwitch.IsTradingHalted() {
		t.Fatalf("expected a non-positive threshold to disable auto-engagement entirely")
	}
}

func TestRecordConnectivityCheckResult_ReturnsTrueOnlyOnTheTransitionCall(t *testing.T) {
	killSwitch := NewKillSwitch(2)
	killSwitch.RecordConnectivityCheckResult(false)
	if transitioned := killSwitch.RecordConnectivityCheckResult(false); !transitioned {
		t.Fatalf("expected transition=true on the call that trips the threshold")
	}
	if transitioned := killSwitch.RecordConnectivityCheckResult(false); transitioned {
		t.Fatalf("expected transition=false on a subsequent failure while already auto-engaged")
	}
}

func TestEngageManually_Idempotent_OverwritesReason(t *testing.T) {
	killSwitch := NewKillSwitch(3)
	killSwitch.EngageManually("first reason")
	killSwitch.EngageManually("second reason")
	status := killSwitch.CurrentStatus()
	if status.ManualEngagementReason != "second reason" {
		t.Fatalf("expected latest reason to win, got %q", status.ManualEngagementReason)
	}
}

func TestCurrentStatus_ReflectsThresholdConfigured(t *testing.T) {
	killSwitch := NewKillSwitch(5)
	status := killSwitch.CurrentStatus()
	if status.FailureThreshold != 5 {
		t.Fatalf("expected threshold 5, got %d", status.FailureThreshold)
	}
}

func TestConcurrentEngageAndRecord_NoRace(t *testing.T) {
	killSwitch := NewKillSwitch(10)
	var waitGroup sync.WaitGroup
	for i := 0; i < 50; i++ {
		waitGroup.Add(3)
		go func() {
			defer waitGroup.Done()
			killSwitch.RecordConnectivityCheckResult(false)
		}()
		go func() {
			defer waitGroup.Done()
			_ = killSwitch.IsTradingHalted()
		}()
		go func() {
			defer waitGroup.Done()
			_ = killSwitch.CurrentStatus()
		}()
	}
	waitGroup.Wait()
}
