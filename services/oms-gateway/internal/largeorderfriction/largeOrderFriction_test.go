package largeorderfriction

import (
	"sync"
	"testing"
)

func testConfig() Config {
	return Config{
		AccountHistoryMultiplier:            5.0,
		InstrumentVolumeMultiplier:          0.10,
		MinimumHistorySamplesBeforeFlagging: 5,
		MaxHistoryPerAccount:                200,
	}
}

func TestNewTracker_RejectsInvalidConfig(t *testing.T) {
	cases := []Config{
		{},
		func() Config { c := testConfig(); c.AccountHistoryMultiplier = 1.0; return c }(),
		func() Config { c := testConfig(); c.InstrumentVolumeMultiplier = 0; return c }(),
		func() Config { c := testConfig(); c.MinimumHistorySamplesBeforeFlagging = 0; return c }(),
	}
	for i, c := range cases {
		if _, err := NewTracker(c); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}

func TestEvaluateOrder_NoHistoryNeverFlagged(t *testing.T) {
	tracker, _ := NewTracker(testConfig())
	result := tracker.EvaluateOrder("acct-1", 1000000, 0)
	if result.RequiresConfirmation {
		t.Fatalf("expected no confirmation required with zero history, got %+v", result)
	}
}

func TestEvaluateOrder_BelowMinimumSamplesNeverFlagged(t *testing.T) {
	tracker, _ := NewTracker(testConfig())
	for i := 0; i < 4; i++ {
		tracker.RecordOrderNotional("acct-1", 1000)
	}
	result := tracker.EvaluateOrder("acct-1", 1000000, 0)
	if result.RequiresConfirmation {
		t.Fatalf("expected no confirmation below MinimumHistorySamplesBeforeFlagging, got %+v", result)
	}
}

func TestEvaluateOrder_LargeRelativeToAccountHistoryFlagged(t *testing.T) {
	tracker, _ := NewTracker(testConfig())
	for i := 0; i < 5; i++ {
		tracker.RecordOrderNotional("acct-1", 1000)
	}
	// average = 1000, 5x = 5000, order of 6000 exceeds it.
	result := tracker.EvaluateOrder("acct-1", 6000, 0)
	if !result.RequiresConfirmation || !result.IsLargeRelativeToAccountHistory {
		t.Fatalf("expected large-relative-to-history flag, got %+v", result)
	}
	if result.Reason == "" {
		t.Fatalf("expected a non-empty reason")
	}
}

func TestEvaluateOrder_ExactlyAtMultiplierBoundaryNotFlagged(t *testing.T) {
	tracker, _ := NewTracker(testConfig())
	for i := 0; i < 5; i++ {
		tracker.RecordOrderNotional("acct-1", 1000)
	}
	// average = 1000, 5x = 5000 exactly -- must NOT flag (strictly greater than).
	result := tracker.EvaluateOrder("acct-1", 5000, 0)
	if result.IsLargeRelativeToAccountHistory {
		t.Fatalf("expected exact boundary to NOT be flagged, got %+v", result)
	}
}

func TestEvaluateOrder_OneUnitOverBoundaryFlagged(t *testing.T) {
	tracker, _ := NewTracker(testConfig())
	for i := 0; i < 5; i++ {
		tracker.RecordOrderNotional("acct-1", 1000)
	}
	result := tracker.EvaluateOrder("acct-1", 5001, 0)
	if !result.IsLargeRelativeToAccountHistory {
		t.Fatalf("expected one-unit-over to be flagged")
	}
}

func TestEvaluateOrder_NormalOrderNotFlagged(t *testing.T) {
	tracker, _ := NewTracker(testConfig())
	for i := 0; i < 10; i++ {
		tracker.RecordOrderNotional("acct-1", 1000)
	}
	result := tracker.EvaluateOrder("acct-1", 1200, 0)
	if result.RequiresConfirmation {
		t.Fatalf("expected normal-sized order to pass, got %+v", result)
	}
}

func TestEvaluateOrder_LargeRelativeToInstrumentVolumeFlagged(t *testing.T) {
	tracker, _ := NewTracker(testConfig())
	// No account history at all -- purely volume-triggered.
	result := tracker.EvaluateOrder("acct-1", 20000, 100000) // 20% > 10% threshold
	if !result.RequiresConfirmation || !result.IsLargeRelativeToInstrumentVolume {
		t.Fatalf("expected volume-relative flag, got %+v", result)
	}
}

func TestEvaluateOrder_ZeroInstrumentVolumeSkipsThatCheck(t *testing.T) {
	tracker, _ := NewTracker(testConfig())
	result := tracker.EvaluateOrder("acct-1", 1000000000, 0)
	if result.IsLargeRelativeToInstrumentVolume {
		t.Fatalf("expected volume check skipped when averageInstrumentVolumeNotional=0")
	}
}

func TestEvaluateOrder_BothReasonsCombinedMessage(t *testing.T) {
	tracker, _ := NewTracker(testConfig())
	for i := 0; i < 5; i++ {
		tracker.RecordOrderNotional("acct-1", 1000)
	}
	result := tracker.EvaluateOrder("acct-1", 100000, 500000) // large vs both
	if !result.IsLargeRelativeToAccountHistory || !result.IsLargeRelativeToInstrumentVolume {
		t.Fatalf("expected both flags set, got %+v", result)
	}
}

func TestEvaluateOrder_NegativeNotionalTreatedAsAbsolute(t *testing.T) {
	tracker, _ := NewTracker(testConfig())
	for i := 0; i < 5; i++ {
		tracker.RecordOrderNotional("acct-1", 1000)
	}
	result := tracker.EvaluateOrder("acct-1", -6000, 0)
	if !result.IsLargeRelativeToAccountHistory {
		t.Fatalf("expected negative notional to be treated as its absolute value")
	}
}

func TestEvaluateOrder_DoesNotMutateHistory(t *testing.T) {
	tracker, _ := NewTracker(testConfig())
	for i := 0; i < 5; i++ {
		tracker.RecordOrderNotional("acct-1", 1000)
	}
	tracker.EvaluateOrder("acct-1", 999999, 0)
	tracker.EvaluateOrder("acct-1", 999999, 0)
	_, sampleCount := tracker.averageNotionalLocked("acct-1")
	if sampleCount != 5 {
		t.Fatalf("expected EvaluateOrder to never mutate history, got sampleCount=%d", sampleCount)
	}
}

func TestEvaluateOrder_PerAccountIndependence(t *testing.T) {
	tracker, _ := NewTracker(testConfig())
	for i := 0; i < 5; i++ {
		tracker.RecordOrderNotional("acct-1", 1000)
	}
	result := tracker.EvaluateOrder("acct-2", 6000, 0)
	if result.RequiresConfirmation {
		t.Fatalf("expected acct-2 unaffected by acct-1's history, got %+v", result)
	}
}

func TestMaxHistoryPerAccountBoundsMemory(t *testing.T) {
	config := testConfig()
	config.MaxHistoryPerAccount = 10
	tracker, _ := NewTracker(config)
	for i := 0; i < 100; i++ {
		tracker.RecordOrderNotional("acct-1", int64(i))
	}
	tracker.mutexGuardingState.Lock()
	historyLen := len(tracker.notionalHistoryByAccount["acct-1"])
	tracker.mutexGuardingState.Unlock()
	if historyLen != 10 {
		t.Fatalf("expected history bounded to 10, got %d", historyLen)
	}
}

func TestConcurrentRecordAndEvaluate(t *testing.T) {
	tracker, _ := NewTracker(testConfig())
	var waitGroup sync.WaitGroup
	for i := 0; i < 50; i++ {
		waitGroup.Add(2)
		go func() {
			defer waitGroup.Done()
			tracker.RecordOrderNotional("acct-1", 1000)
		}()
		go func() {
			defer waitGroup.Done()
			tracker.EvaluateOrder("acct-1", 5000, 0)
		}()
	}
	waitGroup.Wait()
}
