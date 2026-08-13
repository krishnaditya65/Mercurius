package executionalgos

import (
	"errors"
	"testing"
	"time"
)

func mustTime(t *testing.T, layout, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(layout, value)
	if err != nil {
		t.Fatalf("bad test time %q: %v", value, err)
	}
	return parsed
}

func TestBuildTwapScheduleEvenSplit(t *testing.T) {
	start := mustTime(t, time.RFC3339, "2026-08-14T10:00:00Z")
	end := mustTime(t, time.RFC3339, "2026-08-14T10:30:00Z")
	parent := ParentOrder{InstrumentSymbol: "RELIANCE", TotalQuantity: 1000, StartTime: start, EndTime: end}

	slices, err := BuildTwapSchedule(parent, 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(slices) != 4 {
		t.Fatalf("expected 4 slices, got %d", len(slices))
	}

	var sum uint64
	for _, s := range slices {
		if s.Quantity != 250 {
			t.Errorf("expected each slice == 250, got %d at index %d", s.Quantity, s.SliceIndex)
		}
		sum += s.Quantity
	}
	if sum != 1000 {
		t.Errorf("expected slices to sum to 1000, got %d", sum)
	}

	// interval = 30min / 3 = 10min: releases at 10:00, 10:10, 10:20, 10:30.
	wantTimes := []string{"2026-08-14T10:00:00Z", "2026-08-14T10:10:00Z", "2026-08-14T10:20:00Z", "2026-08-14T10:30:00Z"}
	for i, want := range wantTimes {
		wantTime := mustTime(t, time.RFC3339, want)
		if !slices[i].ScheduledReleaseTime.Equal(wantTime) {
			t.Errorf("slice %d: want release time %v, got %v", i, wantTime, slices[i].ScheduledReleaseTime)
		}
	}
	if slices[0].AlgoType != AlgoTypeTwap {
		t.Errorf("expected AlgoTypeTwap, got %v", slices[0].AlgoType)
	}
}

func TestBuildTwapScheduleRemainderDistribution(t *testing.T) {
	start := mustTime(t, time.RFC3339, "2026-08-14T10:00:00Z")
	end := mustTime(t, time.RFC3339, "2026-08-14T10:30:00Z")
	parent := ParentOrder{InstrumentSymbol: "TCS", TotalQuantity: 1001, StartTime: start, EndTime: end}

	slices, err := BuildTwapSchedule(parent, 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 1001 / 4 = 250 remainder 1: exactly one slice gets 251, rest 250.
	countOf251 := 0
	var sum uint64
	for _, s := range slices {
		sum += s.Quantity
		if s.Quantity == 251 {
			countOf251++
		} else if s.Quantity != 250 {
			t.Errorf("unexpected slice quantity %d", s.Quantity)
		}
	}
	if countOf251 != 1 {
		t.Errorf("expected exactly one slice of 251, got %d", countOf251)
	}
	if sum != 1001 {
		t.Errorf("expected sum 1001, got %d", sum)
	}
}

func TestBuildTwapScheduleSingleSlice(t *testing.T) {
	start := mustTime(t, time.RFC3339, "2026-08-14T10:00:00Z")
	end := mustTime(t, time.RFC3339, "2026-08-14T10:30:00Z")
	parent := ParentOrder{InstrumentSymbol: "TCS", TotalQuantity: 500, StartTime: start, EndTime: end}

	slices, err := BuildTwapSchedule(parent, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(slices) != 1 || slices[0].Quantity != 500 || !slices[0].ScheduledReleaseTime.Equal(start) {
		t.Fatalf("unexpected single-slice schedule: %+v", slices)
	}
}

func TestBuildTwapScheduleValidation(t *testing.T) {
	start := mustTime(t, time.RFC3339, "2026-08-14T10:00:00Z")
	end := mustTime(t, time.RFC3339, "2026-08-14T10:30:00Z")

	if _, err := BuildTwapSchedule(ParentOrder{TotalQuantity: 0, StartTime: start, EndTime: end}, 4); !errors.Is(err, ErrZeroTotalQuantity) {
		t.Errorf("expected ErrZeroTotalQuantity, got %v", err)
	}
	if _, err := BuildTwapSchedule(ParentOrder{TotalQuantity: 100, StartTime: start, EndTime: end}, 0); !errors.Is(err, ErrInvalidNumberOfSlices) {
		t.Errorf("expected ErrInvalidNumberOfSlices, got %v", err)
	}
	if _, err := BuildTwapSchedule(ParentOrder{TotalQuantity: 100, StartTime: end, EndTime: start}, 4); !errors.Is(err, ErrEndTimeNotAfterStartTime) {
		t.Errorf("expected ErrEndTimeNotAfterStartTime, got %v", err)
	}
}

func TestBuildVwapScheduleProportionalSplit(t *testing.T) {
	start := mustTime(t, time.RFC3339, "2026-08-14T09:15:00Z")
	parent := ParentOrder{InstrumentSymbol: "INFY", TotalQuantity: 1000, StartTime: start, EndTime: start.Add(4 * time.Hour)}

	// Weights 1:4:3:2 (sum 10) -> fractions 0.1/0.4/0.3/0.2 -> 100/400/300/200.
	curve := []VolumeCurvePoint{
		{BucketReleaseTime: start.Add(3 * time.Hour), HistoricalVolumeWeight: 3},
		{BucketReleaseTime: start, HistoricalVolumeWeight: 1},
		{BucketReleaseTime: start.Add(1 * time.Hour), HistoricalVolumeWeight: 4},
		{BucketReleaseTime: start.Add(2 * time.Hour), HistoricalVolumeWeight: 2},
	}

	slices, err := BuildVwapSchedule(parent, curve)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(slices) != 4 {
		t.Fatalf("expected 4 slices, got %d", len(slices))
	}

	// Sorted by release time ascending: start(w=1)->100, +1h(w=4)->400, +2h(w=2)->200, +3h(w=3)->300.
	wantQuantities := []uint64{100, 400, 200, 300}
	var sum uint64
	for i, s := range slices {
		if s.Quantity != wantQuantities[i] {
			t.Errorf("slice %d: want quantity %d, got %d", i, wantQuantities[i], s.Quantity)
		}
		if s.SliceIndex != i {
			t.Errorf("slice %d: expected re-indexed SliceIndex %d, got %d", i, i, s.SliceIndex)
		}
		sum += s.Quantity
	}
	if sum != 1000 {
		t.Errorf("expected sum 1000, got %d", sum)
	}
	// Confirm sorted ascending by time.
	for i := 1; i < len(slices); i++ {
		if slices[i].ScheduledReleaseTime.Before(slices[i-1].ScheduledReleaseTime) {
			t.Errorf("slices not sorted ascending by release time at index %d", i)
		}
	}
}

func TestBuildVwapScheduleValidation(t *testing.T) {
	start := mustTime(t, time.RFC3339, "2026-08-14T09:15:00Z")
	parent := ParentOrder{TotalQuantity: 100}

	if _, err := BuildVwapSchedule(parent, nil); !errors.Is(err, ErrEmptyVolumeCurve) {
		t.Errorf("expected ErrEmptyVolumeCurve, got %v", err)
	}
	if _, err := BuildVwapSchedule(parent, []VolumeCurvePoint{{BucketReleaseTime: start, HistoricalVolumeWeight: -1}}); !errors.Is(err, ErrNegativeVolumeCurveWeight) {
		t.Errorf("expected ErrNegativeVolumeCurveWeight, got %v", err)
	}
	if _, err := BuildVwapSchedule(parent, []VolumeCurvePoint{{BucketReleaseTime: start, HistoricalVolumeWeight: 0}}); !errors.Is(err, ErrNonPositiveVolumeCurveWeightSum) {
		t.Errorf("expected ErrNonPositiveVolumeCurveWeightSum, got %v", err)
	}
}

func TestSchedulerPollDueSlicesNeverDoubleReleases(t *testing.T) {
	start := mustTime(t, time.RFC3339, "2026-08-14T10:00:00Z")
	end := mustTime(t, time.RFC3339, "2026-08-14T10:30:00Z")
	parent := ParentOrder{InstrumentSymbol: "HDFC", TotalQuantity: 1000, StartTime: start, EndTime: end}
	slices, err := BuildTwapSchedule(parent, 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	scheduler := NewScheduler(parent, slices)

	// Before start: nothing due.
	if due := scheduler.PollDueSlices(start.Add(-time.Minute)); len(due) != 0 {
		t.Errorf("expected no slices due before start, got %d", len(due))
	}
	if scheduler.RemainingQuantity() != 1000 {
		t.Errorf("expected remaining 1000, got %d", scheduler.RemainingQuantity())
	}

	// At 10:00: slice 0 due.
	due := scheduler.PollDueSlices(start)
	if len(due) != 1 || due[0].SliceIndex != 0 {
		t.Fatalf("expected exactly slice 0 due at start, got %+v", due)
	}
	// Poll again at same time: nothing new.
	if due := scheduler.PollDueSlices(start); len(due) != 0 {
		t.Errorf("expected no NEW slices on repeat poll at same time, got %d", len(due))
	}

	// Jump to 10:25: slices 1 and 2 become due together (10:10, 10:20).
	due = scheduler.PollDueSlices(start.Add(25 * time.Minute))
	if len(due) != 2 || due[0].SliceIndex != 1 || due[1].SliceIndex != 2 {
		t.Fatalf("expected slices 1,2 due at 10:25, got %+v", due)
	}

	if scheduler.IsComplete() {
		t.Error("expected scheduler not yet complete")
	}
	if scheduler.RemainingQuantity() != 250 {
		t.Errorf("expected remaining 250, got %d", scheduler.RemainingQuantity())
	}

	// At end: last slice due.
	due = scheduler.PollDueSlices(end)
	if len(due) != 1 || due[0].SliceIndex != 3 {
		t.Fatalf("expected slice 3 due at end, got %+v", due)
	}
	if !scheduler.IsComplete() {
		t.Error("expected scheduler complete after all slices released")
	}
	if scheduler.RemainingQuantity() != 0 {
		t.Errorf("expected remaining 0, got %d", scheduler.RemainingQuantity())
	}
}

func TestPovSchedulerFirstObservationEstablishesBaselineOnly(t *testing.T) {
	parent := ParentOrder{InstrumentSymbol: "SBIN", TotalQuantity: 1000}
	pov, err := NewPovScheduler(parent, PovConfig{ParticipationRate: 0.1, MaxClipSizeQuantity: 50})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	slice, err := pov.OnVolumeObservation(time.Now(), 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if slice != nil {
		t.Fatalf("expected no slice on first observation (baseline only), got %+v", slice)
	}
	if pov.RemainingQuantity() != 1000 {
		t.Errorf("expected remaining unchanged at 1000, got %d", pov.RemainingQuantity())
	}
}

func TestPovSchedulerParticipationRateAndCap(t *testing.T) {
	now := time.Now()
	parent := ParentOrder{InstrumentSymbol: "SBIN", TotalQuantity: 1000}
	pov, err := NewPovScheduler(parent, PovConfig{ParticipationRate: 0.1, MaxClipSizeQuantity: 50})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := pov.OnVolumeObservation(now, 10000); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Volume delta 300 -> 10% == 30, under the 50 cap -> slice of 30.
	slice, err := pov.OnVolumeObservation(now.Add(time.Second), 10300)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if slice == nil || slice.Quantity != 30 {
		t.Fatalf("expected slice of 30, got %+v", slice)
	}
	if slice.AlgoType != AlgoTypePov {
		t.Errorf("expected AlgoTypePov, got %v", slice.AlgoType)
	}
	if pov.RemainingQuantity() != 970 {
		t.Errorf("expected remaining 970, got %d", pov.RemainingQuantity())
	}

	// Volume delta 800 -> 10% == 80, capped at 50 -> slice of 50.
	slice, err = pov.OnVolumeObservation(now.Add(2*time.Second), 11100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if slice == nil || slice.Quantity != 50 {
		t.Fatalf("expected slice capped at 50, got %+v", slice)
	}
	if pov.RemainingQuantity() != 920 {
		t.Errorf("expected remaining 920, got %d", pov.RemainingQuantity())
	}
}

func TestPovSchedulerCapsAtRemainingQuantityAndCompletes(t *testing.T) {
	now := time.Now()
	parent := ParentOrder{InstrumentSymbol: "SBIN", TotalQuantity: 40}
	pov, err := NewPovScheduler(parent, PovConfig{ParticipationRate: 0.1, MaxClipSizeQuantity: 50})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := pov.OnVolumeObservation(now, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Volume delta 800 -> raw 80, capped by MaxClipSize to 50, capped further
	// by remaining (40) to 40 -> exactly completes the parent order.
	slice, err := pov.OnVolumeObservation(now.Add(time.Second), 800)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if slice == nil || slice.Quantity != 40 {
		t.Fatalf("expected final slice of 40, got %+v", slice)
	}
	if !pov.IsComplete() {
		t.Error("expected pov scheduler complete")
	}

	// Further observations produce no more slices.
	slice, err = pov.OnVolumeObservation(now.Add(2*time.Second), 2000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if slice != nil {
		t.Errorf("expected no slice once complete, got %+v", slice)
	}
}

func TestPovSchedulerSmallVolumeDeltaProducesNoSlice(t *testing.T) {
	now := time.Now()
	parent := ParentOrder{InstrumentSymbol: "SBIN", TotalQuantity: 1000}
	pov, err := NewPovScheduler(parent, PovConfig{ParticipationRate: 0.1, MaxClipSizeQuantity: 50})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := pov.OnVolumeObservation(now, 1000); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Delta of 5 -> 10% == 0 (integer truncation) -> no slice.
	slice, err := pov.OnVolumeObservation(now.Add(time.Second), 1005)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if slice != nil {
		t.Errorf("expected no slice for sub-unit participation amount, got %+v", slice)
	}
}

func TestPovSchedulerRejectsVolumeGoingBackwards(t *testing.T) {
	now := time.Now()
	parent := ParentOrder{InstrumentSymbol: "SBIN", TotalQuantity: 1000}
	pov, err := NewPovScheduler(parent, PovConfig{ParticipationRate: 0.1, MaxClipSizeQuantity: 50})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := pov.OnVolumeObservation(now, 1000); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := pov.OnVolumeObservation(now.Add(time.Second), 900); !errors.Is(err, ErrCumulativeVolumeWentBackwards) {
		t.Errorf("expected ErrCumulativeVolumeWentBackwards, got %v", err)
	}
}

func TestNewPovSchedulerValidation(t *testing.T) {
	parent := ParentOrder{InstrumentSymbol: "SBIN", TotalQuantity: 1000}
	if _, err := NewPovScheduler(parent, PovConfig{ParticipationRate: 0, MaxClipSizeQuantity: 50}); !errors.Is(err, ErrNonPositiveParticipationRate) {
		t.Errorf("expected ErrNonPositiveParticipationRate, got %v", err)
	}
	if _, err := NewPovScheduler(parent, PovConfig{ParticipationRate: 0.1, MaxClipSizeQuantity: 0}); !errors.Is(err, ErrZeroMaxClipSize) {
		t.Errorf("expected ErrZeroMaxClipSize, got %v", err)
	}
	if _, err := NewPovScheduler(ParentOrder{TotalQuantity: 0}, PovConfig{ParticipationRate: 0.1, MaxClipSizeQuantity: 50}); !errors.Is(err, ErrZeroTotalQuantity) {
		t.Errorf("expected ErrZeroTotalQuantity, got %v", err)
	}
}
