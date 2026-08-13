// Package idempotency guards POST /orders/submit against a client's retry
// producing a second, distinct order — FEATURES.md §2's "idempotent
// transactions": a network timeout on the client side must never leave it
// unsure whether an order actually went through, and retrying with the
// same key must never place it twice.
package idempotency

import (
	"sync"
	"time"

	"mercurius/omsgateway/internal/orders"
)

// defaultMaximumClaimWaitDuration bounds how long a concurrent duplicate
// request will wait for the request that's actually doing the work
// before giving up. Without a bound, a bug that leaves the owning
// request's goroutine never calling CompleteClaimedKey (e.g. a panic
// that skips the deferred completion — see buildSubmitOrderHandler)
// would hang every subsequent request carrying that key forever.
const defaultMaximumClaimWaitDuration = 30 * time.Second

// TimedOutWaitingForConcurrentRequestRejectionReason is the machine-
// readable rejection reason recorded on the synthetic response a waiter
// gets back if it times out — see ClaimKeyOrAwaitExistingResponse.
const TimedOutWaitingForConcurrentRequestRejectionReason = "IDEMPOTENCY_KEY_TIMED_OUT_WAITING_FOR_CONCURRENT_REQUEST"

// claimedKeyEntry tracks one idempotency key's in-flight/completed state.
// doneChannel is closed exactly once, by whichever goroutine completes
// the claim — every waiter blocks on receiving from it. Go guarantees a
// channel close happens-before any receive that observes it, so reading
// response after <-doneChannel is safe without re-acquiring the mutex.
type claimedKeyEntry struct {
	response    orders.OrderAcknowledgementResponse
	doneChannel chan struct{}
}

// IdempotencyStore caches the OrderAcknowledgementResponse produced for
// each client-supplied idempotency key, so a retried request with the
// same key gets back the SAME response instead of being risk-checked and
// routed to matching-engine a second time.
//
// Concurrent duplicates (two requests carrying the same key arriving at
// roughly the same time — see the TODO this replaces) are now genuinely
// collapsed into one execution: the first caller to see an unclaimed key
// becomes the "owner" and does the real work; every other caller with the
// same key blocks on ClaimKeyOrAwaitExistingResponse until the owner
// calls CompleteClaimedKey, then receives the identical response. Bounded
// by maximumClaimWaitDuration so an owner that never completes can't hang
// waiters forever.
//
// TODO(real build): in-memory only, unbounded, never expires. A real
// build needs a TTL (idempotency keys are only meaningful for a bounded
// retry window, e.g. 24h) and persistence that survives a restart.
type IdempotencyStore struct {
	mutexGuardingEntries     sync.Mutex
	entriesByKey             map[string]*claimedKeyEntry
	maximumClaimWaitDuration time.Duration
}

func NewIdempotencyStore() *IdempotencyStore {
	return NewIdempotencyStoreWithClaimTimeout(defaultMaximumClaimWaitDuration)
}

// NewIdempotencyStoreWithClaimTimeout is the same as NewIdempotencyStore
// but with an overridable wait bound — exists so tests can exercise the
// timeout path without waiting 30 real seconds.
func NewIdempotencyStoreWithClaimTimeout(maximumClaimWaitDuration time.Duration) *IdempotencyStore {
	return &IdempotencyStore{
		entriesByKey:             make(map[string]*claimedKeyEntry),
		maximumClaimWaitDuration: maximumClaimWaitDuration,
	}
}

// ClaimKeyOrAwaitExistingResponse is the entry point for a request
// carrying an idempotency key, replacing the old PreviousResponseForKey.
// Exactly one caller per key becomes the owner (isThisCallTheOwner ==
// true, response is the zero value — go do the real work and call
// CompleteClaimedKey with the result); every other concurrent or later
// caller blocks (up to maximumClaimWaitDuration) and gets back
// (isThisCallTheOwner: false, response: <the owner's completed response,
// or a synthetic timeout rejection if the owner never completed in
// time>) — either way, a non-owner must return `response` directly to
// its client rather than doing the work itself.
//
// An empty key never claims anything and always returns
// (isThisCallTheOwner: true) immediately — callers should treat an
// empty/absent idempotency key as "the client opted out," processing
// normally without ever calling CompleteClaimedKey for it (mirrors the
// old package's documented empty-key opt-out behavior).
func (store *IdempotencyStore) ClaimKeyOrAwaitExistingResponse(
	idempotencyKey string,
) (response orders.OrderAcknowledgementResponse, isThisCallTheOwner bool) {
	if idempotencyKey == "" {
		return orders.OrderAcknowledgementResponse{}, true
	}

	store.mutexGuardingEntries.Lock()
	existingEntry, alreadyClaimed := store.entriesByKey[idempotencyKey]
	if !alreadyClaimed {
		store.entriesByKey[idempotencyKey] = &claimedKeyEntry{doneChannel: make(chan struct{})}
		store.mutexGuardingEntries.Unlock()
		return orders.OrderAcknowledgementResponse{}, true
	}
	store.mutexGuardingEntries.Unlock()

	select {
	case <-existingEntry.doneChannel:
		return existingEntry.response, false
	case <-time.After(store.maximumClaimWaitDuration):
		return orders.OrderAcknowledgementResponse{
			WasOrderAccepted:               false,
			MachineReadableRejectionReason: TimedOutWaitingForConcurrentRequestRejectionReason,
			HumanReadableRejectionReason: "Another request with this idempotency key is still being processed " +
				"and did not complete in time. Do not blindly retry — check order status before resubmitting.",
		}, false
	}
}

// CompleteClaimedKey records the final response for a key this caller
// owns (per ClaimKeyOrAwaitExistingResponse returning
// isThisCallTheOwner: true) and wakes up every goroutine currently
// blocked waiting on it. A no-op for an empty key — there is never a
// claim to complete for one.
func (store *IdempotencyStore) CompleteClaimedKey(idempotencyKey string, response orders.OrderAcknowledgementResponse) {
	if idempotencyKey == "" {
		return
	}

	store.mutexGuardingEntries.Lock()
	entry, wasClaimed := store.entriesByKey[idempotencyKey]
	store.mutexGuardingEntries.Unlock()

	if !wasClaimed {
		// Should never happen if callers follow the claim/complete
		// contract — defensive no-op rather than a panic.
		return
	}

	entry.response = response
	close(entry.doneChannel)
}
