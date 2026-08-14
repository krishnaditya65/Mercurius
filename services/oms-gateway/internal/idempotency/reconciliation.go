// This file extends internal/idempotency with FEATURES.md §21's
// "Idempotent order status with WebSocket-reconnect reconciliation":
// GIVEN idempotency already collapses concurrent/sequential RESUBMISSIONS
// of the same key into one real execution (see idempotencyStore.go), the
// one real gap for a WS-reconnect scenario is that a client shouldn't
// HAVE to resubmit the full order body just to ask "what happened to
// order X while I was disconnected" — it may not even have the original
// request body handy after a reconnect, and resubmitting risks operator
// confusion about whether this is a genuine retry. Reconcile is a pure,
// non-blocking, READ-ONLY query by idempotencyKey alone: it returns the
// exact same real, complete answer ClaimKeyOrAwaitExistingResponse's
// non-owner path would have returned, without claiming anything, without
// blocking, and without requiring the caller to resubmit anything.
package idempotency

import "mercurius/omsgateway/internal/orders"

// ReconciliationStatus is a closed set describing what this service
// currently knows about an idempotency key.
type ReconciliationStatus string

const (
	// ReconciliationStatusUnknown: this key has never been claimed by
	// any submission this service has seen (since its last restart — see
	// the package's own in-memory-only gap).
	ReconciliationStatusUnknown ReconciliationStatus = "UNKNOWN"
	// ReconciliationStatusInProgress: a request carrying this key is
	// still being processed (its owner hasn't called CompleteClaimedKey
	// yet).
	ReconciliationStatusInProgress ReconciliationStatus = "IN_PROGRESS"
	// ReconciliationStatusCompleted: the key's owning request finished —
	// Response is the real, final, authoritative answer.
	ReconciliationStatusCompleted ReconciliationStatus = "COMPLETED"
)

// ReconciliationResult is the real, complete answer to "what happened to
// the order submitted with this idempotency key".
type ReconciliationResult struct {
	Status   ReconciliationStatus                 `json:"status"`
	Response *orders.OrderAcknowledgementResponse `json:"response,omitempty"`
}

// Reconcile is a pure, non-blocking read: it NEVER claims a key, NEVER
// waits on doneChannel, and is safe to call any number of times (e.g.
// on every WS reconnect, or on a polling cadence) without side effects
// or risk of racing with the real owner. Calling it repeatedly for the
// same completed key always returns the identical Response — the same
// idempotency guarantee ClaimKeyOrAwaitExistingResponse's replay path
// already provides, just without requiring a full resubmission.
func (store *IdempotencyStore) Reconcile(idempotencyKey string) ReconciliationResult {
	if idempotencyKey == "" {
		return ReconciliationResult{Status: ReconciliationStatusUnknown}
	}

	store.mutexGuardingEntries.Lock()
	entry, exists := store.entriesByKey[idempotencyKey]
	store.mutexGuardingEntries.Unlock()

	if !exists {
		return ReconciliationResult{Status: ReconciliationStatusUnknown}
	}

	select {
	case <-entry.doneChannel:
		responseCopy := entry.response
		return ReconciliationResult{Status: ReconciliationStatusCompleted, Response: &responseCopy}
	default:
		return ReconciliationResult{Status: ReconciliationStatusInProgress}
	}
}
