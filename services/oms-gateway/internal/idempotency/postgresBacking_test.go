// Real tests against a real, locally-running Postgres — no mocks. See
// docs/BUILD_LOG.md's Postgres-persistence entry.
package idempotency

import (
	"context"
	"os"
	"testing"
	"time"

	"mercurius/omsgateway/internal/orders"
)

func testOmsPostgresDsn() string {
	if dsn := os.Getenv("OMS_PGSTORE_TEST_DSN"); dsn != "" {
		return dsn
	}
	return "postgres://trading:trading@localhost:5432/omsgateway"
}

func mustOpenTestIdempotencyStore(t *testing.T) *IdempotencyStore {
	t.Helper()
	store, connectError := NewPostgresBackedIdempotencyStore(context.Background(), testOmsPostgresDsn())
	if connectError != nil {
		t.Skipf("skipping: real Postgres not reachable at %s: %v", testOmsPostgresDsn(), connectError)
	}
	if _, execError := store.postgres.pool.Exec(context.Background(), `TRUNCATE idempotency_responses`); execError != nil {
		t.Fatalf("truncate idempotency_responses: %v", execError)
	}
	t.Cleanup(store.postgres.pool.Close)
	return store
}

func TestPostgresBackedIdempotencyStore_ClaimCompleteAndReplay(t *testing.T) {
	store := mustOpenTestIdempotencyStore(t)

	response, isOwner := store.ClaimKeyOrAwaitExistingResponse("order-key-1")
	if !isOwner {
		t.Fatal("expected first caller to be the owner")
	}
	completedResponse := orders.OrderAcknowledgementResponse{WasOrderAccepted: true, MatchingEngineOrderSequenceNumber: 42}
	store.CompleteClaimedKey("order-key-1", completedResponse)

	// Same-process replay: served from the in-memory map.
	replayedResponse, isOwnerOnReplay := store.ClaimKeyOrAwaitExistingResponse("order-key-1")
	if isOwnerOnReplay {
		t.Fatal("expected replay to NOT be the owner")
	}
	if replayedResponse.MatchingEngineOrderSequenceNumber != 42 {
		t.Fatalf("expected replayed response to match completed response, got %+v", replayedResponse)
	}
	_ = response
}

func TestPostgresBackedIdempotencyStore_PersistsAcrossFreshStore(t *testing.T) {
	firstStore := mustOpenTestIdempotencyStore(t)
	_, isOwner := firstStore.ClaimKeyOrAwaitExistingResponse("restart-survival-key")
	if !isOwner {
		t.Fatal("expected owner")
	}
	firstStore.CompleteClaimedKey("restart-survival-key", orders.OrderAcknowledgementResponse{
		WasOrderAccepted:               false,
		MachineReadableRejectionReason: "INSUFFICIENT_MARGIN",
	})

	// A brand-new IdempotencyStore (simulating a fresh process — its
	// in-memory entriesByKey map is empty) must still find the
	// COMPLETED response durably cached in Postgres.
	secondStore, connectError := NewPostgresBackedIdempotencyStore(context.Background(), testOmsPostgresDsn())
	if connectError != nil {
		t.Fatalf("unexpected error opening second store: %v", connectError)
	}
	defer secondStore.postgres.pool.Close()

	response, isOwner := secondStore.ClaimKeyOrAwaitExistingResponse("restart-survival-key")
	if isOwner {
		t.Fatal("expected a fresh store to find the persisted response, not become owner")
	}
	if response.MachineReadableRejectionReason != "INSUFFICIENT_MARGIN" {
		t.Fatalf("expected persisted rejection reason, got %+v", response)
	}
}

func TestPostgresBackedIdempotencyStore_DeleteExpiredResponses(t *testing.T) {
	store := mustOpenTestIdempotencyStore(t)

	_, isOwner := store.ClaimKeyOrAwaitExistingResponse("expiring-key")
	if !isOwner {
		t.Fatal("expected owner")
	}
	store.CompleteClaimedKey("expiring-key", orders.OrderAcknowledgementResponse{WasOrderAccepted: true})

	// Force the row to already be expired, bypassing the normal TTL —
	// simulates time having passed, without a 24h real sleep.
	if _, execError := store.postgres.pool.Exec(context.Background(),
		`UPDATE idempotency_responses SET expires_at = now() - $1::interval WHERE idempotency_key = 'expiring-key'`,
		(1 * time.Hour).String(),
	); execError != nil {
		t.Fatalf("force-expire row: %v", execError)
	}

	deletedCount, deleteError := store.DeleteExpiredResponses()
	if deleteError != nil {
		t.Fatalf("unexpected error: %v", deleteError)
	}
	if deletedCount != 1 {
		t.Fatalf("expected 1 row deleted, got %d", deletedCount)
	}

	// NOTE: re-claiming "expiring-key" on THIS SAME store/process still
	// finds the in-memory entriesByKey record from the original
	// claim/complete above (in-memory entries are never removed —
	// documented, unchanged, pre-existing behavior; Postgres's
	// expires_at/DeleteExpiredResponses only bounds the DURABLE cache a
	// FRESH process consults, not the current process's own memory). A
	// brand-new store (simulating a fresh process after restart+cleanup)
	// is the correct way to observe the expired-and-deleted row actually
	// being gone.
	freshStore, connectError := NewPostgresBackedIdempotencyStore(context.Background(), testOmsPostgresDsn())
	if connectError != nil {
		t.Fatalf("unexpected error opening fresh store: %v", connectError)
	}
	defer freshStore.postgres.pool.Close()

	_, isOwnerAfterExpiry := freshStore.ClaimKeyOrAwaitExistingResponse("expiring-key")
	if !isOwnerAfterExpiry {
		t.Fatal("expected a fresh store's claim, after the row was deleted, to become the owner (not find a stale cached response)")
	}
}
