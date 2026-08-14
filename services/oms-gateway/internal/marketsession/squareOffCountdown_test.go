package marketsession

import (
	"sync"
	"testing"
	"time"
)

func TestComputeSquareOffCountdown_StillCountingDown(t *testing.T) {
	cutoff := time.Date(2026, 8, 14, 15, 20, 0, 0, time.UTC)
	now := time.Date(2026, 8, 14, 15, 0, 0, 0, time.UTC)
	countdown := ComputeSquareOffCountdown(cutoff, now)
	if countdown.IsPastCutoff {
		t.Fatalf("expected not past cutoff")
	}
	if countdown.RemainingSeconds != 1200 {
		t.Fatalf("expected 1200 remaining seconds, got %d", countdown.RemainingSeconds)
	}
}

func TestComputeSquareOffCountdown_ExactlyAtCutoff(t *testing.T) {
	cutoff := time.Date(2026, 8, 14, 15, 20, 0, 0, time.UTC)
	countdown := ComputeSquareOffCountdown(cutoff, cutoff)
	if !countdown.IsPastCutoff {
		t.Fatalf("expected IsPastCutoff at exact cutoff")
	}
	if countdown.RemainingSeconds != 0 {
		t.Fatalf("expected 0 remaining seconds, got %d", countdown.RemainingSeconds)
	}
}

func TestComputeSquareOffCountdown_PastCutoffFlooredAtZero(t *testing.T) {
	cutoff := time.Date(2026, 8, 14, 15, 20, 0, 0, time.UTC)
	now := cutoff.Add(10 * time.Minute)
	countdown := ComputeSquareOffCountdown(cutoff, now)
	if !countdown.IsPastCutoff {
		t.Fatalf("expected IsPastCutoff")
	}
	if countdown.RemainingSeconds != 0 {
		t.Fatalf("expected floored at 0, got %d", countdown.RemainingSeconds)
	}
}

func TestSquareOffCutoffConfig_CutoffForDate(t *testing.T) {
	config := SquareOffCutoffConfig{HourUtc: 15, MinuteUtc: 20, SecondUtc: 0}
	now := time.Date(2026, 8, 14, 9, 5, 0, 0, time.UTC)
	cutoff := config.CutoffForDate(now)
	expected := time.Date(2026, 8, 14, 15, 20, 0, 0, time.UTC)
	if !cutoff.Equal(expected) {
		t.Fatalf("expected %v, got %v", expected, cutoff)
	}
}

func TestSquareOffCutoffConfig_UsesSameDayAsNow(t *testing.T) {
	config := DefaultSquareOffCutoffConfig()
	now := time.Date(2026, 12, 25, 3, 0, 0, 0, time.UTC)
	cutoff := config.CutoffForDate(now)
	if cutoff.Day() != 25 || cutoff.Month() != time.December {
		t.Fatalf("expected cutoff on same date as now, got %v", cutoff)
	}
}

func TestNewSquareOffReminderTracker_RejectsEmptyThresholds(t *testing.T) {
	if _, err := NewSquareOffReminderTracker(nil); err == nil {
		t.Fatalf("expected error for empty thresholds")
	}
}

func TestNewSquareOffReminderTracker_RejectsNonPositiveThreshold(t *testing.T) {
	if _, err := NewSquareOffReminderTracker([]time.Duration{30 * time.Minute, 0}); err == nil {
		t.Fatalf("expected error for zero threshold")
	}
}

func TestDueReminders_FiresExactlyAtThreshold(t *testing.T) {
	tracker, _ := NewSquareOffReminderTracker(DefaultSquareOffReminderThresholds)
	cutoff := time.Date(2026, 8, 14, 15, 20, 0, 0, time.UTC)
	now := cutoff.Add(-30 * time.Minute)
	due := tracker.DueReminders("acct-1", cutoff, now)
	if len(due) != 1 || due[0] != 30*time.Minute {
		t.Fatalf("expected exactly [30m] due, got %v", due)
	}
}

func TestDueReminders_NotYetDueBeforeThreshold(t *testing.T) {
	tracker, _ := NewSquareOffReminderTracker(DefaultSquareOffReminderThresholds)
	cutoff := time.Date(2026, 8, 14, 15, 20, 0, 0, time.UTC)
	now := cutoff.Add(-31 * time.Minute)
	due := tracker.DueReminders("acct-1", cutoff, now)
	if len(due) != 0 {
		t.Fatalf("expected no reminders due yet, got %v", due)
	}
}

func TestDueReminders_MultipleThresholdsCrossedAtOnceAllFire(t *testing.T) {
	tracker, _ := NewSquareOffReminderTracker(DefaultSquareOffReminderThresholds)
	cutoff := time.Date(2026, 8, 14, 15, 20, 0, 0, time.UTC)
	// Poll for the first time when only 3 minutes remain -- all three
	// thresholds (30/15/5) have technically been crossed already.
	now := cutoff.Add(-3 * time.Minute)
	due := tracker.DueReminders("acct-1", cutoff, now)
	if len(due) != 3 {
		t.Fatalf("expected all 3 thresholds due, got %v", due)
	}
}

func TestDueReminders_NeverRefiresSameThreshold(t *testing.T) {
	tracker, _ := NewSquareOffReminderTracker(DefaultSquareOffReminderThresholds)
	cutoff := time.Date(2026, 8, 14, 15, 20, 0, 0, time.UTC)
	now := cutoff.Add(-30 * time.Minute)
	first := tracker.DueReminders("acct-1", cutoff, now)
	if len(first) != 1 {
		t.Fatalf("expected 1 due on first call, got %v", first)
	}
	second := tracker.DueReminders("acct-1", cutoff, now.Add(time.Second))
	if len(second) != 0 {
		t.Fatalf("expected 0 due on immediate repeat poll, got %v", second)
	}
}

func TestDueReminders_ProgressiveCrossingFiresEachThresholdOnce(t *testing.T) {
	tracker, _ := NewSquareOffReminderTracker(DefaultSquareOffReminderThresholds)
	cutoff := time.Date(2026, 8, 14, 15, 20, 0, 0, time.UTC)

	at30 := tracker.DueReminders("acct-1", cutoff, cutoff.Add(-30*time.Minute))
	if len(at30) != 1 || at30[0] != 30*time.Minute {
		t.Fatalf("expected [30m], got %v", at30)
	}
	at20 := tracker.DueReminders("acct-1", cutoff, cutoff.Add(-20*time.Minute))
	if len(at20) != 0 {
		t.Fatalf("expected nothing new at 20m remaining, got %v", at20)
	}
	at15 := tracker.DueReminders("acct-1", cutoff, cutoff.Add(-15*time.Minute))
	if len(at15) != 1 || at15[0] != 15*time.Minute {
		t.Fatalf("expected [15m], got %v", at15)
	}
	at5 := tracker.DueReminders("acct-1", cutoff, cutoff.Add(-5*time.Minute))
	if len(at5) != 1 || at5[0] != 5*time.Minute {
		t.Fatalf("expected [5m], got %v", at5)
	}
}

func TestDueReminders_PastCutoffFiresNothingNew(t *testing.T) {
	tracker, _ := NewSquareOffReminderTracker(DefaultSquareOffReminderThresholds)
	cutoff := time.Date(2026, 8, 14, 15, 20, 0, 0, time.UTC)
	due := tracker.DueReminders("acct-1", cutoff, cutoff.Add(time.Minute))
	if len(due) != 0 {
		t.Fatalf("expected nothing due after cutoff has passed, got %v", due)
	}
}

func TestDueReminders_IndependentPerAccount(t *testing.T) {
	tracker, _ := NewSquareOffReminderTracker(DefaultSquareOffReminderThresholds)
	cutoff := time.Date(2026, 8, 14, 15, 20, 0, 0, time.UTC)
	now := cutoff.Add(-30 * time.Minute)
	tracker.DueReminders("acct-1", cutoff, now)
	due := tracker.DueReminders("acct-2", cutoff, now)
	if len(due) != 1 {
		t.Fatalf("expected acct-2 unaffected by acct-1's fired reminders, got %v", due)
	}
}

func TestDueReminders_IndependentPerCutoffInstant(t *testing.T) {
	tracker, _ := NewSquareOffReminderTracker(DefaultSquareOffReminderThresholds)
	cutoffDay1 := time.Date(2026, 8, 14, 15, 20, 0, 0, time.UTC)
	cutoffDay2 := time.Date(2026, 8, 15, 15, 20, 0, 0, time.UTC)
	tracker.DueReminders("acct-1", cutoffDay1, cutoffDay1.Add(-30*time.Minute))
	due := tracker.DueReminders("acct-1", cutoffDay2, cutoffDay2.Add(-30*time.Minute))
	if len(due) != 1 {
		t.Fatalf("expected a new trading day's cutoff to fire fresh reminders, got %v", due)
	}
}

func TestHasFired(t *testing.T) {
	tracker, _ := NewSquareOffReminderTracker(DefaultSquareOffReminderThresholds)
	cutoff := time.Date(2026, 8, 14, 15, 20, 0, 0, time.UTC)
	if tracker.HasFired("acct-1", cutoff, 30*time.Minute) {
		t.Fatalf("expected not fired yet")
	}
	tracker.DueReminders("acct-1", cutoff, cutoff.Add(-30*time.Minute))
	if !tracker.HasFired("acct-1", cutoff, 30*time.Minute) {
		t.Fatalf("expected fired after DueReminders crossed it")
	}
	if tracker.HasFired("acct-1", cutoff, 15*time.Minute) {
		t.Fatalf("expected 15m threshold not yet fired")
	}
}

func TestHasFired_DoesNotMutateState(t *testing.T) {
	tracker, _ := NewSquareOffReminderTracker(DefaultSquareOffReminderThresholds)
	cutoff := time.Date(2026, 8, 14, 15, 20, 0, 0, time.UTC)
	tracker.HasFired("acct-1", cutoff, 30*time.Minute)
	tracker.HasFired("acct-1", cutoff, 30*time.Minute)
	due := tracker.DueReminders("acct-1", cutoff, cutoff.Add(-30*time.Minute))
	if len(due) != 1 {
		t.Fatalf("HasFired must never itself mark a threshold fired, got %v", due)
	}
}

func TestThresholds_ReturnsCopy(t *testing.T) {
	tracker, _ := NewSquareOffReminderTracker(DefaultSquareOffReminderThresholds)
	thresholds := tracker.Thresholds()
	thresholds[0] = time.Hour
	freshCopy := tracker.Thresholds()
	if freshCopy[0] == time.Hour {
		t.Fatalf("expected Thresholds() to return a defensive copy")
	}
}

func TestConcurrentDueReminders(t *testing.T) {
	tracker, _ := NewSquareOffReminderTracker(DefaultSquareOffReminderThresholds)
	cutoff := time.Date(2026, 8, 14, 15, 20, 0, 0, time.UTC)
	now := cutoff.Add(-5 * time.Minute)
	var waitGroup sync.WaitGroup
	totalFired := make([]int, 50)
	for i := 0; i < 50; i++ {
		waitGroup.Add(1)
		idx := i
		go func() {
			defer waitGroup.Done()
			due := tracker.DueReminders("acct-1", cutoff, now)
			totalFired[idx] = len(due)
		}()
	}
	waitGroup.Wait()
	sum := 0
	for _, n := range totalFired {
		sum += n
	}
	if sum != 3 {
		t.Fatalf("expected exactly 3 total reminder firings across all concurrent callers (30m/15m/5m each exactly once), got %d", sum)
	}
}
