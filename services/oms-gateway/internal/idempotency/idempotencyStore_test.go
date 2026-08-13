package idempotency

import (
	"sync"
	"testing"
	"time"

	"mercurius/omsgateway/internal/orders"
)

func TestFirstCallerForANewKeyBecomesTheOwner(t *testing.T) {
	store := NewIdempotencyStore()

	_, isThisCallTheOwner := store.ClaimKeyOrAwaitExistingResponse("retry-key-1")
	if !isThisCallTheOwner {
		t.Fatal("expected the first caller for an unclaimed key to become the owner")
	}
}

func TestCompletedResponseIsReturnedForTheSameKeyOnASequentialRetry(t *testing.T) {
	store := NewIdempotencyStore()
	completedResponse := orders.OrderAcknowledgementResponse{
		WasOrderAccepted:                  true,
		AssignedGlobalSequenceNumber:      42,
		MatchingEngineOrderSequenceNumber: 7,
	}

	_, isThisCallTheOwner := store.ClaimKeyOrAwaitExistingResponse("retry-key-1")
	if !isThisCallTheOwner {
		t.Fatal("expected the first call to be the owner")
	}
	store.CompleteClaimedKey("retry-key-1", completedResponse)

	// A sequential retry after the first request already finished.
	returnedResponse, isThisCallTheOwner := store.ClaimKeyOrAwaitExistingResponse("retry-key-1")
	if isThisCallTheOwner {
		t.Fatal("a retry against a completed key must not become a new owner")
	}
	if returnedResponse.AssignedGlobalSequenceNumber != 42 {
		t.Fatalf("expected AssignedGlobalSequenceNumber=42, got %d", returnedResponse.AssignedGlobalSequenceNumber)
	}
	if returnedResponse.MatchingEngineOrderSequenceNumber != 7 {
		t.Fatalf("expected MatchingEngineOrderSequenceNumber=7, got %d", returnedResponse.MatchingEngineOrderSequenceNumber)
	}
}

func TestDifferentKeysDoNotCollide(t *testing.T) {
	store := NewIdempotencyStore()
	store.ClaimKeyOrAwaitExistingResponse("key-a")
	store.CompleteClaimedKey("key-a", orders.OrderAcknowledgementResponse{AssignedGlobalSequenceNumber: 1})
	store.ClaimKeyOrAwaitExistingResponse("key-b")
	store.CompleteClaimedKey("key-b", orders.OrderAcknowledgementResponse{AssignedGlobalSequenceNumber: 2})

	responseA, _ := store.ClaimKeyOrAwaitExistingResponse("key-a")
	responseB, _ := store.ClaimKeyOrAwaitExistingResponse("key-b")

	if responseA.AssignedGlobalSequenceNumber != 1 {
		t.Fatalf("expected key-a to map to sequence 1, got %d", responseA.AssignedGlobalSequenceNumber)
	}
	if responseB.AssignedGlobalSequenceNumber != 2 {
		t.Fatalf("expected key-b to map to sequence 2, got %d", responseB.AssignedGlobalSequenceNumber)
	}
}

func TestEmptyKeyAlwaysReturnsOwnerAndNeverBlocks(t *testing.T) {
	store := NewIdempotencyStore()
	store.CompleteClaimedKey("", orders.OrderAcknowledgementResponse{AssignedGlobalSequenceNumber: 99})

	_, isThisCallTheOwner := store.ClaimKeyOrAwaitExistingResponse("")
	if !isThisCallTheOwner {
		t.Fatal("an empty idempotency key must never block waiting for another owner — it means the client opted out")
	}
}

// TestConcurrentDuplicateRequestsCollapseToOneExecution is the actual
// regression test for the gap this build closes: two requests carrying
// the SAME idempotency key, arriving concurrently (not sequentially),
// must not both become owners — exactly one does the real work, and the
// other blocks until that work completes, then receives the identical
// response.
func TestConcurrentDuplicateRequestsCollapseToOneExecution(t *testing.T) {
	store := NewIdempotencyStore()

	firstCallerHasClaimedSignal := make(chan struct{})
	secondCallerHasStartedWaitingSignal := make(chan struct{})

	var ownerCount int
	var ownerCountMutex sync.Mutex
	var secondCallerResponse orders.OrderAcknowledgementResponse

	var waitGroup sync.WaitGroup
	waitGroup.Add(2)

	// First caller: claims the key, signals it has claimed, waits until
	// the second caller confirms it's about to block, THEN completes —
	// this deterministically forces the second caller to observe the key
	// as already-claimed-but-not-yet-completed, exercising the actual
	// concurrent-wait path rather than relying on a lucky race.
	go func() {
		defer waitGroup.Done()
		_, isOwner := store.ClaimKeyOrAwaitExistingResponse("concurrent-key")
		if isOwner {
			ownerCountMutex.Lock()
			ownerCount++
			ownerCountMutex.Unlock()
		}
		close(firstCallerHasClaimedSignal)
		<-secondCallerHasStartedWaitingSignal
		time.Sleep(20 * time.Millisecond) // give the second caller time to actually enter its blocking select
		store.CompleteClaimedKey("concurrent-key", orders.OrderAcknowledgementResponse{
			WasOrderAccepted:             true,
			AssignedGlobalSequenceNumber: 123,
		})
	}()

	// Second caller: waits for the first to have claimed the key, then
	// calls ClaimKeyOrAwaitExistingResponse itself — this call must block
	// (not become a second owner) until CompleteClaimedKey above runs.
	go func() {
		defer waitGroup.Done()
		<-firstCallerHasClaimedSignal
		close(secondCallerHasStartedWaitingSignal)
		response, isOwner := store.ClaimKeyOrAwaitExistingResponse("concurrent-key")
		if isOwner {
			ownerCountMutex.Lock()
			ownerCount++
			ownerCountMutex.Unlock()
		}
		secondCallerResponse = response
	}()

	waitGroupDoneSignal := make(chan struct{})
	go func() {
		waitGroup.Wait()
		close(waitGroupDoneSignal)
	}()
	select {
	case <-waitGroupDoneSignal:
	case <-time.After(5 * time.Second):
		t.Fatal("test deadlocked — concurrent claim/wait logic likely broken")
	}

	if ownerCount != 1 {
		t.Fatalf("expected exactly one owner for a concurrently-claimed key, got %d", ownerCount)
	}
	if secondCallerResponse.AssignedGlobalSequenceNumber != 123 {
		t.Fatalf("expected the waiting caller to see the owner's completed response, got %+v", secondCallerResponse)
	}
}

func TestClaimTimesOutIfOwnerNeverCompletes(t *testing.T) {
	store := NewIdempotencyStoreWithClaimTimeout(50 * time.Millisecond)

	// Owner claims but deliberately never calls CompleteClaimedKey.
	_, isOwner := store.ClaimKeyOrAwaitExistingResponse("never-completed-key")
	if !isOwner {
		t.Fatal("expected the first call to be the owner")
	}

	startTime := time.Now()
	response, isThisCallTheOwner := store.ClaimKeyOrAwaitExistingResponse("never-completed-key")
	elapsed := time.Since(startTime)

	if elapsed < 50*time.Millisecond {
		t.Fatalf("expected the wait to take at least the configured timeout, took %v", elapsed)
	}
	if isThisCallTheOwner {
		t.Fatal("a timed-out waiter must not become the owner — that would risk a THIRD concurrent execution")
	}
	if response.WasOrderAccepted {
		t.Fatal("expected the synthetic timeout response to be a rejection, not an accepted order")
	}
	if response.MachineReadableRejectionReason != TimedOutWaitingForConcurrentRequestRejectionReason {
		t.Fatalf("expected the timeout rejection reason, got %q", response.MachineReadableRejectionReason)
	}
}
