package amoqueue

import (
	"testing"

	"mercurius/omsgateway/internal/orders"
)

func TestNewQueueStartsEmpty(t *testing.T) {
	queue := NewAfterMarketOrderQueue()
	if queue.QueuedCount() != 0 {
		t.Fatalf("expected a new queue to start empty, got %d", queue.QueuedCount())
	}
}

func TestEnqueueIncreasesQueuedCount(t *testing.T) {
	queue := NewAfterMarketOrderQueue()
	queue.Enqueue(orders.OrderSubmissionRequest{ClientAccountIdentifier: "acct-001"})
	queue.Enqueue(orders.OrderSubmissionRequest{ClientAccountIdentifier: "acct-002"})

	if queue.QueuedCount() != 2 {
		t.Fatalf("expected 2 queued requests, got %d", queue.QueuedCount())
	}
}

func TestDrainAllReturnsRequestsInFifoOrderAndEmptiesTheQueue(t *testing.T) {
	queue := NewAfterMarketOrderQueue()
	queue.Enqueue(orders.OrderSubmissionRequest{ClientAccountIdentifier: "first"})
	queue.Enqueue(orders.OrderSubmissionRequest{ClientAccountIdentifier: "second"})
	queue.Enqueue(orders.OrderSubmissionRequest{ClientAccountIdentifier: "third"})

	drained := queue.DrainAll()

	if len(drained) != 3 {
		t.Fatalf("expected 3 drained requests, got %d", len(drained))
	}
	if drained[0].ClientAccountIdentifier != "first" || drained[1].ClientAccountIdentifier != "second" || drained[2].ClientAccountIdentifier != "third" {
		t.Fatalf("expected FIFO order first,second,third — got %v", drained)
	}
	if queue.QueuedCount() != 0 {
		t.Fatalf("expected the queue to be empty after DrainAll, got %d remaining", queue.QueuedCount())
	}
}

func TestDrainAllOnAnEmptyQueueReturnsEmptySlice(t *testing.T) {
	queue := NewAfterMarketOrderQueue()
	drained := queue.DrainAll()
	if len(drained) != 0 {
		t.Fatalf("expected an empty slice, got %d items", len(drained))
	}
}
