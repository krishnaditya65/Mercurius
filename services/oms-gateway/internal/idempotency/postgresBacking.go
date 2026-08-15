// Real Postgres persistence for IdempotencyStore's COMPLETED response
// cache — see docs/BUILD_LOG.md's Postgres-persistence entry and this
// package's own doc comment for why the in-process claim/await
// concurrency mechanism (claimedKeyEntry/doneChannel) stays exactly
// as-is: it is a single-process primitive, not something a Postgres row
// can replace without a much bigger redesign. What THIS file adds is
// durability of the FINAL answer for an already-completed key, closing
// two previously-documented gaps at once:
//
//  1. Persistence — a replayed request after an oms-gateway restart now
//     gets back the SAME cached response instead of the store having
//     forgotten every key and re-executing the order a second time.
//  2. Unbounded growth — every row carries a real expires_at
//     (idempotencyResponseTtl after CompleteClaimedKey, default 24h,
//     matching this package's pre-existing doc-comment suggestion of
//     "a bounded retry window, e.g. 24h"), and expired rows are
//     filtered out at read time AND opportunistically deleted by
//     DeleteExpiredResponses — see that function's own comment for why
//     this is a "call it from somewhere periodically" design rather
//     than a background goroutine started implicitly by this package.
package idempotency

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"mercurius/omsgateway/internal/orders"
	"mercurius/omsgateway/migrations"
)

// idempotencyResponseTtl is how long a completed response stays valid
// for replay before DeleteExpiredResponses is allowed to reclaim it —
// this package's pre-existing doc comment already named 24h as the
// natural bound for "a client's retry window."
const idempotencyResponseTtl = 24 * time.Hour

type postgresBacking struct {
	pool *pgxpool.Pool
}

// NewPostgresBackedIdempotencyStore connects to postgresDsn, applies
// every migration in services/oms-gateway/migrations, and returns a
// real, durable IdempotencyStore. The in-process claim/await mechanism
// (ClaimKeyOrAwaitExistingResponse's concurrent-duplicate collapsing)
// is unchanged and still purely in-memory; only the COMPLETED-response
// cache gains Postgres durability — see this file's header comment.
func NewPostgresBackedIdempotencyStore(ctx context.Context, postgresDsn string) (*IdempotencyStore, error) {
	pool, connectError := pgxpool.New(ctx, postgresDsn)
	if connectError != nil {
		return nil, fmt.Errorf("idempotency: connect: %w", connectError)
	}
	if pingError := pool.Ping(ctx); pingError != nil {
		pool.Close()
		return nil, fmt.Errorf("idempotency: ping: %w", pingError)
	}
	if migrateError := applyMigrations(ctx, pool); migrateError != nil {
		pool.Close()
		return nil, fmt.Errorf("idempotency: migrate: %w", migrateError)
	}
	store := NewIdempotencyStore()
	store.postgres = &postgresBacking{pool: pool}
	return store, nil
}

func applyMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	entries, readDirError := migrations.FS.ReadDir(".")
	if readDirError != nil {
		return fmt.Errorf("read embedded migrations dir: %w", readDirError)
	}
	fileNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			fileNames = append(fileNames, entry.Name())
		}
	}
	sort.Strings(fileNames)
	for _, fileName := range fileNames {
		contents, readFileError := migrations.FS.ReadFile(fileName)
		if readFileError != nil {
			return fmt.Errorf("read migration %q: %w", fileName, readFileError)
		}
		if _, execError := pool.Exec(ctx, string(contents)); execError != nil {
			return fmt.Errorf("apply migration %q: %w", fileName, execError)
		}
	}
	return nil
}

// responseFromPostgres looks up a NOT-YET-EXPIRED completed response
// for key, consulted by ClaimKeyOrAwaitExistingResponse ONLY when the
// in-memory map has no record of the key at all (e.g. this process just
// restarted and lost every in-flight claim, but a previous process
// instance already completed this exact key durably).
func (store *IdempotencyStore) responseFromPostgres(idempotencyKey string) (orders.OrderAcknowledgementResponse, bool) {
	ctx := context.Background()
	var responseJson []byte
	scanError := store.postgres.pool.QueryRow(
		ctx,
		`SELECT response_json FROM idempotency_responses WHERE idempotency_key = $1 AND expires_at > now()`,
		idempotencyKey,
	).Scan(&responseJson)
	if scanError != nil {
		if scanError != pgx.ErrNoRows {
			log.Printf("idempotency: FAILED to read Postgres cache for key %q: %v", idempotencyKey, scanError)
		}
		return orders.OrderAcknowledgementResponse{}, false
	}

	var response orders.OrderAcknowledgementResponse
	if unmarshalError := json.Unmarshal(responseJson, &response); unmarshalError != nil {
		log.Printf("idempotency: FAILED to unmarshal cached response for key %q: %v", idempotencyKey, unmarshalError)
		return orders.OrderAcknowledgementResponse{}, false
	}
	return response, true
}

// persistCompletedResponse durably records the final response for key,
// called by CompleteClaimedKey right after it wakes up in-memory
// waiters. A write failure is logged, not returned — CompleteClaimedKey
// has no error return (its pre-existing signature — see this package's
// doc comment) and every in-memory waiter has already gotten its answer
// regardless of whether the Postgres write below succeeds.
func (store *IdempotencyStore) persistCompletedResponse(idempotencyKey string, response orders.OrderAcknowledgementResponse) {
	responseJson, marshalError := json.Marshal(response)
	if marshalError != nil {
		log.Printf("idempotency: FAILED to marshal response for key %q: %v", idempotencyKey, marshalError)
		return
	}

	ctx := context.Background()
	_, execError := store.postgres.pool.Exec(ctx, `
		INSERT INTO idempotency_responses (idempotency_key, response_json, expires_at)
		VALUES ($1, $2, now() + $3::interval)
		ON CONFLICT (idempotency_key) DO UPDATE SET response_json = EXCLUDED.response_json, expires_at = EXCLUDED.expires_at`,
		idempotencyKey, responseJson, idempotencyResponseTtl.String(),
	)
	if execError != nil {
		log.Printf("idempotency: FAILED to persist response for key %q: %v", idempotencyKey, execError)
	}
}

// DeleteExpiredResponses removes every idempotency_responses row whose
// expires_at has passed, returning how many rows were deleted. NOT run
// automatically on a timer by this package (matching this repo's
// existing convention for other "sweep due X" operations —
// internal/withdrawalworkflow's ProcessDueWithdrawals,
// internal/paymentmandate's SweepDueMandates — which are all
// operator/scheduled-job-triggered, not self-scheduling); a real
// deployment would call this from a periodic job or a dedicated
// `POST /idempotency/cleanup`-style admin endpoint. A no-op returning 0
// on an in-memory (non-Postgres-backed) store.
func (store *IdempotencyStore) DeleteExpiredResponses() (int64, error) {
	if store.postgres == nil {
		return 0, nil
	}
	ctx := context.Background()
	commandTag, execError := store.postgres.pool.Exec(ctx, `DELETE FROM idempotency_responses WHERE expires_at <= now()`)
	if execError != nil {
		return 0, fmt.Errorf("idempotency: delete expired: %w", execError)
	}
	return commandTag.RowsAffected(), nil
}
