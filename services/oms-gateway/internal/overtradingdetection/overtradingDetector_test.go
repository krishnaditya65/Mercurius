package overtradingdetection

import (
	"sync"
	"testing"
	"time"
)

var baseTestTime = time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)

func testThresholds() Thresholds {
	return Thresholds{
		RecentWindowDuration:                     5 * time.Minute,
		BaselineLookbackDuration:                 24 * time.Hour,
		MinimumRecentOrderCountToConsider:        5,
		BaselineMultiplierToTrigger:              3.0,
		RapidFireMinimumGapBelowThisIsSuspicious: 3 * time.Second,
		CooldownDuration:                         15 * time.Minute,
		MaxHistoryPerAccount:                     500,
	}
}

func TestNewDetector_RejectsInvalidThresholds(t *testing.T) {
	cases := []Thresholds{
		{},
		func() Thresholds { th := testThresholds(); th.RecentWindowDuration = 0; return th }(),
		func() Thresholds { th := testThresholds(); th.BaselineLookbackDuration = time.Second; return th }(),
		func() Thresholds { th := testThresholds(); th.MinimumRecentOrderCountToConsider = 0; return th }(),
		func() Thresholds { th := testThresholds(); th.BaselineMultiplierToTrigger = 1.0; return th }(),
		func() Thresholds { th := testThresholds(); th.CooldownDuration = 0; return th }(),
	}
	for i, c := range cases {
		if _, err := NewDetector(c); err == nil {
			t.Errorf("case %d: expected error, got nil", i)
		}
	}
}

func TestNewDetector_ValidThresholdsSucceed(t *testing.T) {
	if _, err := NewDetector(testThresholds()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEvaluate_NoHistoryNoNudge(t *testing.T) {
	detector, _ := NewDetector(testThresholds())
	nudge, inCooldown := detector.Evaluate("acct-1", baseTestTime)
	if nudge != nil || inCooldown {
		t.Fatalf("expected no nudge, got %+v inCooldown=%v", nudge, inCooldown)
	}
}

func TestEvaluate_RapidFireBurstFires(t *testing.T) {
	detector, _ := NewDetector(testThresholds())
	now := baseTestTime
	// 5 submissions, last two 1 second apart -> rapid fire.
	for i := 0; i < 4; i++ {
		detector.RecordSubmission("acct-1", now.Add(time.Duration(i)*time.Minute))
	}
	detector.RecordSubmission("acct-1", now.Add(3*time.Minute+1*time.Second))

	evalTime := now.Add(3*time.Minute + 1*time.Second)
	nudge, inCooldown := detector.Evaluate("acct-1", evalTime)
	if inCooldown {
		t.Fatalf("should not be in cooldown yet")
	}
	if nudge == nil {
		t.Fatalf("expected a nudge to fire")
	}
	if nudge.PatternDetected != PatternRapidFireBurst {
		t.Fatalf("expected RAPID_FIRE_BURST, got %s", nudge.PatternDetected)
	}
}

func TestEvaluate_BelowMinimumCountNeverFires(t *testing.T) {
	detector, _ := NewDetector(testThresholds())
	now := baseTestTime
	detector.RecordSubmission("acct-1", now)
	detector.RecordSubmission("acct-1", now.Add(time.Millisecond))
	nudge, _ := detector.Evaluate("acct-1", now.Add(2*time.Millisecond))
	if nudge != nil {
		t.Fatalf("expected no nudge below MinimumRecentOrderCountToConsider, got %+v", nudge)
	}
}

func TestEvaluate_ElevatedVelocityVsBaselineFires(t *testing.T) {
	detector, _ := NewDetector(testThresholds())
	now := baseTestTime

	// Establish a low baseline: 1 order every hour for 20 hours.
	for i := 0; i < 20; i++ {
		detector.RecordSubmission("acct-1", now.Add(-time.Duration(i+1)*time.Hour))
	}
	// Now a burst of 6 orders in the last 5 minutes, spaced > 3s apart
	// (so rapid-fire doesn't fire first and mask this test).
	for i := 0; i < 6; i++ {
		detector.RecordSubmission("acct-1", now.Add(-4*time.Minute+time.Duration(i)*30*time.Second))
	}

	nudge, inCooldown := detector.Evaluate("acct-1", now)
	if inCooldown {
		t.Fatalf("should not be in cooldown yet")
	}
	if nudge == nil {
		t.Fatalf("expected an elevated-velocity nudge")
	}
	if nudge.PatternDetected != PatternElevatedOrderVelocity {
		t.Fatalf("expected ELEVATED_ORDER_VELOCITY_VS_BASELINE, got %s", nudge.PatternDetected)
	}
}

func TestEvaluate_NormalVelocityNoNudge(t *testing.T) {
	detector, _ := NewDetector(testThresholds())
	now := baseTestTime
	// Consistent 1-order-per-5-minutes pace for 24 hours -> baseline
	// matches recent window exactly, never triggers.
	for i := 0; i < 288; i++ { // 24h / 5min = 288
		detector.RecordSubmission("acct-1", now.Add(-time.Duration(i)*5*time.Minute))
	}
	nudge, _ := detector.Evaluate("acct-1", now)
	if nudge != nil {
		t.Fatalf("expected no nudge for normal steady pace, got %+v", nudge)
	}
}

func TestEvaluate_CooldownSuppressesRepeatNudge(t *testing.T) {
	detector, _ := NewDetector(testThresholds())
	now := baseTestTime
	for i := 0; i < 6; i++ {
		detector.RecordSubmission("acct-1", now.Add(time.Duration(i)*time.Second))
	}
	evalTime := now.Add(6 * time.Second)
	firstNudge, firstInCooldown := detector.Evaluate("acct-1", evalTime)
	if firstNudge == nil || firstInCooldown {
		t.Fatalf("expected first evaluate to fire a nudge")
	}

	// Immediately re-evaluate: should be suppressed by cooldown.
	secondNudge, secondInCooldown := detector.Evaluate("acct-1", evalTime.Add(time.Second))
	if secondNudge != nil {
		t.Fatalf("expected no nudge while in cooldown, got %+v", secondNudge)
	}
	if !secondInCooldown {
		t.Fatalf("expected inCooldown=true")
	}
}

func TestEvaluate_CooldownExpiresAndCanFireAgain(t *testing.T) {
	detector, _ := NewDetector(testThresholds())
	now := baseTestTime
	for i := 0; i < 6; i++ {
		detector.RecordSubmission("acct-1", now.Add(time.Duration(i)*time.Second))
	}
	evalTime := now.Add(6 * time.Second)
	firstNudge, _ := detector.Evaluate("acct-1", evalTime)
	if firstNudge == nil {
		t.Fatalf("expected first nudge")
	}

	afterCooldown := firstNudge.CooldownExpiresAtTime.Add(time.Second)
	for i := 0; i < 6; i++ {
		detector.RecordSubmission("acct-1", afterCooldown.Add(time.Duration(i)*time.Second))
	}
	secondNudge, secondInCooldown := detector.Evaluate("acct-1", afterCooldown.Add(6*time.Second))
	if secondInCooldown {
		t.Fatalf("cooldown should have expired")
	}
	if secondNudge == nil {
		t.Fatalf("expected a second nudge to be able to fire after cooldown expiry")
	}
}

func TestIsInCooldown(t *testing.T) {
	detector, _ := NewDetector(testThresholds())
	now := baseTestTime
	if detector.IsInCooldown("acct-1", now) {
		t.Fatalf("expected not in cooldown initially")
	}
	for i := 0; i < 6; i++ {
		detector.RecordSubmission("acct-1", now.Add(time.Duration(i)*time.Second))
	}
	detector.Evaluate("acct-1", now.Add(6*time.Second))
	if !detector.IsInCooldown("acct-1", now.Add(7*time.Second)) {
		t.Fatalf("expected in cooldown after nudge fired")
	}
	if detector.IsInCooldown("acct-1", now.Add(16*time.Minute)) {
		t.Fatalf("expected cooldown to have expired after CooldownDuration")
	}
}

func TestStatus_ReflectsRecentCountAndCooldown(t *testing.T) {
	detector, _ := NewDetector(testThresholds())
	now := baseTestTime
	for i := 0; i < 3; i++ {
		detector.RecordSubmission("acct-1", now.Add(time.Duration(i)*time.Second))
	}
	status := detector.Status("acct-1", now.Add(3*time.Second))
	if status.RecentOrderCount != 3 {
		t.Fatalf("expected recentOrderCount 3, got %d", status.RecentOrderCount)
	}
	if status.IsInCooldown {
		t.Fatalf("expected not in cooldown")
	}
}

func TestStatus_DoesNotMutateCooldown(t *testing.T) {
	detector, _ := NewDetector(testThresholds())
	now := baseTestTime
	for i := 0; i < 6; i++ {
		detector.RecordSubmission("acct-1", now.Add(time.Duration(i)*time.Second))
	}
	// Calling Status repeatedly must never itself arm a cooldown -- only
	// Evaluate does that.
	detector.Status("acct-1", now.Add(6*time.Second))
	detector.Status("acct-1", now.Add(7*time.Second))
	if detector.IsInCooldown("acct-1", now.Add(8*time.Second)) {
		t.Fatalf("Status must never arm a cooldown by itself")
	}
}

func TestMaxHistoryPerAccountBoundsMemory(t *testing.T) {
	th := testThresholds()
	th.MaxHistoryPerAccount = 10
	detector, _ := NewDetector(th)
	now := baseTestTime
	for i := 0; i < 100; i++ {
		detector.RecordSubmission("acct-1", now.Add(time.Duration(i)*time.Minute))
	}
	detector.mutexGuardingState.Lock()
	historyLen := len(detector.submissionTimesByAccount["acct-1"])
	detector.mutexGuardingState.Unlock()
	if historyLen != 10 {
		t.Fatalf("expected history bounded to 10, got %d", historyLen)
	}
}

func TestPerAccountIndependence(t *testing.T) {
	detector, _ := NewDetector(testThresholds())
	now := baseTestTime
	for i := 0; i < 6; i++ {
		detector.RecordSubmission("acct-1", now.Add(time.Duration(i)*time.Second))
	}
	// acct-2 has no history at all.
	nudge, _ := detector.Evaluate("acct-2", now.Add(6*time.Second))
	if nudge != nil {
		t.Fatalf("expected acct-2 to be unaffected by acct-1's burst, got %+v", nudge)
	}
}

func TestConcurrentRecordAndEvaluate(t *testing.T) {
	detector, _ := NewDetector(testThresholds())
	now := baseTestTime
	var waitGroup sync.WaitGroup
	for i := 0; i < 50; i++ {
		waitGroup.Add(2)
		idx := i
		go func() {
			defer waitGroup.Done()
			detector.RecordSubmission("acct-1", now.Add(time.Duration(idx)*time.Millisecond))
		}()
		go func() {
			defer waitGroup.Done()
			detector.Evaluate("acct-1", now.Add(time.Duration(idx)*time.Millisecond))
		}()
	}
	waitGroup.Wait()
}
