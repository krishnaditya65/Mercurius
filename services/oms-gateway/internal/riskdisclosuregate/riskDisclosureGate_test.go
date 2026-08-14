package riskdisclosuregate

import (
	"errors"
	"sync"
	"testing"
	"time"
)

var baseTestTime = time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)

func TestNewGate_RejectsNonPositiveCoolingOff(t *testing.T) {
	if _, err := NewGate(0); err == nil {
		t.Fatalf("expected error for zero duration")
	}
	if _, err := NewGate(-time.Hour); err == nil {
		t.Fatalf("expected error for negative duration")
	}
}

func TestNewGate_ValidDurationSucceeds(t *testing.T) {
	if _, err := NewGate(24 * time.Hour); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckFirstFnoOrderGate_NonFnoInstrumentAlwaysPasses(t *testing.T) {
	gate, _ := NewGate(24 * time.Hour)
	if err := gate.CheckFirstFnoOrderGate("acct-1", false, baseTestTime); err != nil {
		t.Fatalf("expected non-F&O order to always pass, got %v", err)
	}
}

func TestCheckFirstFnoOrderGate_NeverAcknowledgedRejected(t *testing.T) {
	gate, _ := NewGate(24 * time.Hour)
	err := gate.CheckFirstFnoOrderGate("acct-1", true, baseTestTime)
	if !errors.Is(err, ErrNotYetAcknowledged) {
		t.Fatalf("expected ErrNotYetAcknowledged, got %v", err)
	}
}

func TestCheckFirstFnoOrderGate_AcknowledgedButCoolingOffNotElapsed(t *testing.T) {
	gate, _ := NewGate(24 * time.Hour)
	gate.Acknowledge("acct-1", baseTestTime)
	err := gate.CheckFirstFnoOrderGate("acct-1", true, baseTestTime.Add(23*time.Hour))
	if !errors.Is(err, ErrCoolingOffPeriodNotElapsed) {
		t.Fatalf("expected ErrCoolingOffPeriodNotElapsed, got %v", err)
	}
}

func TestCheckFirstFnoOrderGate_ExactlyAtCoolingOffBoundaryPasses(t *testing.T) {
	gate, _ := NewGate(24 * time.Hour)
	gate.Acknowledge("acct-1", baseTestTime)
	err := gate.CheckFirstFnoOrderGate("acct-1", true, baseTestTime.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("expected pass at exact boundary, got %v", err)
	}
}

func TestCheckFirstFnoOrderGate_OneSecondBeforeBoundaryFails(t *testing.T) {
	gate, _ := NewGate(24 * time.Hour)
	gate.Acknowledge("acct-1", baseTestTime)
	err := gate.CheckFirstFnoOrderGate("acct-1", true, baseTestTime.Add(24*time.Hour-time.Second))
	if !errors.Is(err, ErrCoolingOffPeriodNotElapsed) {
		t.Fatalf("expected ErrCoolingOffPeriodNotElapsed, got %v", err)
	}
}

func TestCheckFirstFnoOrderGate_AfterFirstOrderRecordedAlwaysPasses(t *testing.T) {
	gate, _ := NewGate(24 * time.Hour)
	gate.Acknowledge("acct-1", baseTestTime)
	if err := gate.CheckFirstFnoOrderGate("acct-1", true, baseTestTime.Add(24*time.Hour)); err != nil {
		t.Fatalf("expected pass: %v", err)
	}
	gate.RecordFirstFnoOrderPlaced("acct-1", baseTestTime.Add(24*time.Hour))

	// Even immediately (no cooling off needed), and even if the caller
	// forgot to acknowledge again — permanently exempt now.
	err := gate.CheckFirstFnoOrderGate("acct-1", true, baseTestTime.Add(24*time.Hour+time.Second))
	if err != nil {
		t.Fatalf("expected returning F&O trader to always pass, got %v", err)
	}
}

func TestCheckFirstFnoOrderGate_DoesNotMutateStateOnItsOwn(t *testing.T) {
	gate, _ := NewGate(24 * time.Hour)
	gate.Acknowledge("acct-1", baseTestTime)
	// Passing check without RecordFirstFnoOrderPlaced must not itself
	// mark HasPlacedFirstFnoOrder -- so a later-rejected order doesn't
	// wrongly consume the milestone.
	if err := gate.CheckFirstFnoOrderGate("acct-1", true, baseTestTime.Add(24*time.Hour)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	status := gate.Status("acct-1")
	if status.HasPlacedFirstFnoOrder {
		t.Fatalf("CheckFirstFnoOrderGate must not mutate HasPlacedFirstFnoOrder on its own")
	}
}

func TestAcknowledge_ReAcknowledgeResetsTimestamp(t *testing.T) {
	gate, _ := NewGate(24 * time.Hour)
	gate.Acknowledge("acct-1", baseTestTime)
	gate.Acknowledge("acct-1", baseTestTime.Add(time.Hour))
	status := gate.Status("acct-1")
	if !status.AcknowledgedAtTime.Equal(baseTestTime.Add(time.Hour)) {
		t.Fatalf("expected re-acknowledgement to reset timestamp, got %v", status.AcknowledgedAtTime)
	}
}

func TestStatus_NeverAcknowledgedReturnsZeroValue(t *testing.T) {
	gate, _ := NewGate(24 * time.Hour)
	status := gate.Status("nonexistent")
	if status.HasAcknowledged {
		t.Fatalf("expected HasAcknowledged=false for unknown account")
	}
}

func TestPerAccountIndependence(t *testing.T) {
	gate, _ := NewGate(24 * time.Hour)
	gate.Acknowledge("acct-1", baseTestTime)
	err := gate.CheckFirstFnoOrderGate("acct-2", true, baseTestTime.Add(48*time.Hour))
	if !errors.Is(err, ErrNotYetAcknowledged) {
		t.Fatalf("expected acct-2 to be unaffected by acct-1's acknowledgement, got %v", err)
	}
}

func TestRecordFirstFnoOrderPlaced_IdempotentAndSafeIfNeverAcknowledged(t *testing.T) {
	gate, _ := NewGate(24 * time.Hour)
	gate.RecordFirstFnoOrderPlaced("acct-1", baseTestTime)
	gate.RecordFirstFnoOrderPlaced("acct-1", baseTestTime)
	status := gate.Status("acct-1")
	if !status.HasPlacedFirstFnoOrder {
		t.Fatalf("expected HasPlacedFirstFnoOrder=true")
	}
}

func TestConcurrentAcknowledgeAndCheck(t *testing.T) {
	gate, _ := NewGate(time.Millisecond)
	var waitGroup sync.WaitGroup
	for i := 0; i < 50; i++ {
		waitGroup.Add(2)
		go func() {
			defer waitGroup.Done()
			gate.Acknowledge("acct-1", baseTestTime)
		}()
		go func() {
			defer waitGroup.Done()
			gate.CheckFirstFnoOrderGate("acct-1", true, baseTestTime.Add(time.Hour))
		}()
	}
	waitGroup.Wait()
}
