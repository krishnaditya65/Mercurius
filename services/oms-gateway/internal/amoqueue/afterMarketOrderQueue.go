// Package amoqueue holds After Market Orders (AMOs) submitted while the
// market is closed, until something drains the queue (in this skeleton,
// an explicit "market just opened" admin call — see
// docs/BUILD_LOG.md for why there's no clock-driven trigger yet).
package amoqueue

import (
	"sync"

	"mercurius/omsgateway/internal/orders"
)

// AfterMarketOrderQueue is a plain FIFO of queued order-submission
// requests, safe for concurrent Enqueue calls (many clients can submit
// AMOs while the market is closed) and a single DrainAll call (market
// open).
type AfterMarketOrderQueue struct {
	mutexGuardingQueue sync.Mutex
	queuedRequests     []orders.OrderSubmissionRequest
}

func NewAfterMarketOrderQueue() *AfterMarketOrderQueue {
	return &AfterMarketOrderQueue{}
}

// Enqueue appends a request to the back of the queue.
func (queue *AfterMarketOrderQueue) Enqueue(request orders.OrderSubmissionRequest) {
	queue.mutexGuardingQueue.Lock()
	defer queue.mutexGuardingQueue.Unlock()
	queue.queuedRequests = append(queue.queuedRequests, request)
}

// QueuedCount returns how many AMOs are currently waiting — used by the
// market-session-status endpoint and by tests.
func (queue *AfterMarketOrderQueue) QueuedCount() int {
	queue.mutexGuardingQueue.Lock()
	defer queue.mutexGuardingQueue.Unlock()
	return len(queue.queuedRequests)
}

// DrainAll removes and returns every currently queued request, in the
// order they were enqueued (oldest first — FEATURES.md-adjacent fairness
// expectation: an AMO queued earlier shouldn't be processed after one
// queued later). The queue is empty again once this returns.
//
// TODO(real build): this is an all-at-once, in-memory drain triggered by
// an explicit admin call. A real build needs this triggered by an actual
// market-open event (a clock, or an upstream exchange-calendar service)
// and the queue itself needs to survive a restart (currently: an
// oms-gateway crash between market close and market open silently loses
// every AMO submitted in between).
func (queue *AfterMarketOrderQueue) DrainAll() []orders.OrderSubmissionRequest {
	queue.mutexGuardingQueue.Lock()
	defer queue.mutexGuardingQueue.Unlock()

	drainedRequests := queue.queuedRequests
	queue.queuedRequests = nil
	return drainedRequests
}
