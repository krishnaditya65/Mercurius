package idempotency

import (
	"sync"
	"testing"
	"time"

	"mercurius/omsgateway/internal/orders"
)

func TestReconcile_EmptyKeyIsUnknown(t *testing.T) {
	store := NewIdempotencyStore()
	result := store.Reconcile("")
	if result.Status != ReconciliationStatusUnknown {
		t.Fatalf("expected UNKNOWN for empty key, got %s", result.Status)
	}
}

func TestReconcile_NeverClaimedKeyIsUnknown(t *testing.T) {
	store := NewIdempotencyStore()
	result := store.Reconcile("never-seen")
	if result.Status != ReconciliationStatusUnknown {
		t.Fatalf("expected UNKNOWN, got %s", result.Status)
	}
	if result.Response != nil {
		t.Fatalf("expected nil response for unknown key")
	}
}

func TestReconcile_ClaimedButNotCompletedIsInProgress(t *testing.T) {
	store := NewIdempotencyStore()
	store.ClaimKeyOrAwaitExistingResponse("key-1") // becomes owner, never completes

	result := store.Reconcile("key-1")
	if result.Status != ReconciliationStatusInProgress {
		t.Fatalf("expected IN_PROGRESS, got %s", result.Status)
	}
	if result.Response != nil {
		t.Fatalf("expected nil response while in progress")
	}
}

func TestReconcile_CompletedReturnsRealResponse(t *testing.T) {
	store := NewIdempotencyStore()
	store.ClaimKeyOrAwaitExistingResponse("key-1")
	expected := orders.OrderAcknowledgementResponse{
		WasOrderAccepted:             true,
		AssignedGlobalSequenceNumber: 42,
	}
	store.CompleteClaimedKey("key-1", expected)

	result := store.Reconcile("key-1")
	if result.Status != ReconciliationStatusCompleted {
		t.Fatalf("expected COMPLETED, got %s", result.Status)
	}
	if result.Response == nil {
		t.Fatalf("expected non-nil response")
	}
	if result.Response.AssignedGlobalSequenceNumber != 42 {
		t.Fatalf("expected sequence 42, got %d", result.Response.AssignedGlobalSequenceNumber)
	}
}

func TestReconcile_RepeatedCallsReturnIdenticalResponse(t *testing.T) {
	store := NewIdempotencyStore()
	store.ClaimKeyOrAwaitExistingResponse("key-1")
	expected := orders.OrderAcknowledgementResponse{WasOrderAccepted: true, AssignedGlobalSequenceNumber: 7}
	store.CompleteClaimedKey("key-1", expected)

	first := store.Reconcile("key-1")
	second := store.Reconcile("key-1")
	third := store.Reconcile("key-1")

	if first.Response.AssignedGlobalSequenceNumber != second.Response.AssignedGlobalSequenceNumber ||
		second.Response.AssignedGlobalSequenceNumber != third.Response.AssignedGlobalSequenceNumber {
		t.Fatalf("expected identical response across repeated reconcile calls")
	}
}

func TestReconcile_NeverBlocksWhileInProgress(t *testing.T) {
	store := NewIdempotencyStore()
	store.ClaimKeyOrAwaitExistingResponse("key-1") // owner never completes

	done := make(chan struct{})
	go func() {
		store.Reconcile("key-1")
		close(done)
	}()
	select {
	case <-done:
		// good -- returned promptly
	case <-time.After(2 * time.Second):
		t.Fatalf("Reconcile blocked -- it must be a non-blocking read")
	}
}

func TestReconcile_NeverClaimsAKey(t *testing.T) {
	store := NewIdempotencyStore()
	// Reconcile on a never-seen key must NOT itself claim it -- a real
	// submission with that key afterward should still become the owner.
	store.Reconcile("key-1")

	_, isOwner := store.ClaimKeyOrAwaitExistingResponse("key-1")
	if !isOwner {
		t.Fatalf("expected the real submission to still become the owner after Reconcile was called on an unseen key")
	}
}

func TestReconcile_RejectedOrderResponsePreservedVerbatim(t *testing.T) {
	store := NewIdempotencyStore()
	store.ClaimKeyOrAwaitExistingResponse("key-1")
	rejected := orders.OrderAcknowledgementResponse{
		WasOrderAccepted:               false,
		HumanReadableRejectionReason:   "insufficient margin",
		MachineReadableRejectionReason: "INSUFFICIENT_MARGIN",
	}
	store.CompleteClaimedKey("key-1", rejected)

	result := store.Reconcile("key-1")
	if result.Response.MachineReadableRejectionReason != "INSUFFICIENT_MARGIN" {
		t.Fatalf("expected rejection reason preserved, got %+v", result.Response)
	}
}

func TestReconcile_DistinctKeysIndependent(t *testing.T) {
	store := NewIdempotencyStore()
	store.ClaimKeyOrAwaitExistingResponse("key-1")
	store.CompleteClaimedKey("key-1", orders.OrderAcknowledgementResponse{WasOrderAccepted: true})

	result := store.Reconcile("key-2")
	if result.Status != ReconciliationStatusUnknown {
		t.Fatalf("expected key-2 unaffected by key-1's completion, got %s", result.Status)
	}
}

func TestReconcile_TransitionsFromInProgressToCompleted(t *testing.T) {
	store := NewIdempotencyStore()
	store.ClaimKeyOrAwaitExistingResponse("key-1")

	before := store.Reconcile("key-1")
	if before.Status != ReconciliationStatusInProgress {
		t.Fatalf("expected IN_PROGRESS before completion, got %s", before.Status)
	}

	store.CompleteClaimedKey("key-1", orders.OrderAcknowledgementResponse{WasOrderAccepted: true})

	after := store.Reconcile("key-1")
	if after.Status != ReconciliationStatusCompleted {
		t.Fatalf("expected COMPLETED after completion, got %s", after.Status)
	}
}

func TestReconcile_ConcurrentReconcileAndComplete(t *testing.T) {
	store := NewIdempotencyStore()
	store.ClaimKeyOrAwaitExistingResponse("key-1")

	var waitGroup sync.WaitGroup
	for i := 0; i < 50; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			store.Reconcile("key-1")
		}()
	}
	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		store.CompleteClaimedKey("key-1", orders.OrderAcknowledgementResponse{WasOrderAccepted: true})
	}()
	waitGroup.Wait()

	final := store.Reconcile("key-1")
	if final.Status != ReconciliationStatusCompleted {
		t.Fatalf("expected COMPLETED after all goroutines settle, got %s", final.Status)
	}
}
