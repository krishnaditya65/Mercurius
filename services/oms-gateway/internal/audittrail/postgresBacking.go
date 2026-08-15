// Real Postgres persistence for AuditTrail — see docs/BUILD_LOG.md's
// Postgres-persistence entry and docs/DOCUMENTATION.md's
// services/oms-gateway section. HIGHEST priority of everything touched
// in that pass: this package's own doc comment already called
// in-memory-only "disqualifying for anything actually regulated".
//
// Design: rather than a separate pgstore package + interface (the
// pattern used for services/ledger's doubleentry.LedgerBook, where
// three OTHER packages held a reference to the concrete type), this
// extends AuditTrail itself with an optional Postgres backing, because
// every consumer in this service (cmd/server/main.go's ~15 call sites)
// already holds a *AuditTrail concrete pointer and none of them needed
// to change AT ALL — only main()'s ONE construction call site does
// (NewAuditTrail() -> NewPostgresBackedAuditTrail(ctx, dsn)). This is
// the same "interface-preserving" goal via a different, lower-blast-
// radius mechanism, appropriate here because AuditTrail's method set is
// small and its internal state (a plain mutex-guarded slice) has no
// concurrency primitive (unlike internal/idempotency's channel-based
// claim/await mechanism) that would make a Postgres swap behaviorally
// different.
//
// When postgresPool is set, AuditTrail is Postgres-backed: Append
// writes directly to Postgres (Postgres is the sole source of truth —
// the in-memory `entries` slice is left completely unused in this mode,
// never both-written), and AllEntries/EntriesForAccount read back from
// Postgres. When postgresPool is nil (the original NewAuditTrail()
// constructor), behavior is byte-for-byte identical to before this file
// existed.
package audittrail

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"

	"mercurius/omsgateway/migrations"
)

// postgresPool is nil for an in-memory AuditTrail (NewAuditTrail) and
// set for a Postgres-backed one (NewPostgresBackedAuditTrail) — the
// single flag every method below branches on.
type postgresBacking struct {
	pool *pgxpool.Pool
}

// NewPostgresBackedAuditTrail connects to postgresDsn, applies every
// migration in services/oms-gateway/migrations (in identifier order —
// shared across audittrail/positions/idempotency, all three migrated
// together at startup), and returns a real, durable AuditTrail.
//
// Known limitation (documented, not fixed): no retry/backoff if
// postgresDsn is unreachable at startup — returns an error immediately,
// same choice as services/ledger's pgstore.NewPostgresLedgerBook; see
// cmd/server/main.go's construction site for the fallback behavior this
// enables.
func NewPostgresBackedAuditTrail(ctx context.Context, postgresDsn string) (*AuditTrail, error) {
	pool, connectError := openAndMigrate(ctx, postgresDsn)
	if connectError != nil {
		return nil, connectError
	}
	return &AuditTrail{postgres: &postgresBacking{pool: pool}}, nil
}

// openAndMigrate is shared by every internal/*-package Postgres
// constructor in this service (audittrail, positions, idempotency) —
// each calls it independently with its own DSN (which may all resolve
// to the same physical database, per OMS_POSTGRES_DSN) so each remains
// independently constructible/testable, but the connect+migrate logic
// itself isn't triplicated verbatim... it IS triplicated verbatim
// across three files (this one, internal/positions,
// internal/idempotency) — see docs/BUILD_LOG.md's known-limitations
// list: no shared internal/pgutil package was introduced for this
// three-line helper, matching this repo's own documented convention of
// tolerating small duplication over a premature shared abstraction
// (internal/httplogging is independently duplicated across every Go
// service for the same reason).
func openAndMigrate(ctx context.Context, postgresDsn string) (*pgxpool.Pool, error) {
	pool, connectError := pgxpool.New(ctx, postgresDsn)
	if connectError != nil {
		return nil, fmt.Errorf("audittrail: connect: %w", connectError)
	}
	if pingError := pool.Ping(ctx); pingError != nil {
		pool.Close()
		return nil, fmt.Errorf("audittrail: ping: %w", pingError)
	}
	if migrateError := applyMigrations(ctx, pool); migrateError != nil {
		pool.Close()
		return nil, fmt.Errorf("audittrail: migrate: %w", migrateError)
	}
	return pool, nil
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

// appendToPostgres inserts one entry — called by Append when postgres
// backing is configured, in place of the in-memory slice append.
func (trail *AuditTrail) appendToPostgres(entry Entry) error {
	ctx := context.Background()
	var orderSideIsBuyNotSell any
	if entry.OrderSideIsBuyNotSell != nil {
		orderSideIsBuyNotSell = *entry.OrderSideIsBuyNotSell
	}
	_, execError := trail.postgres.pool.Exec(ctx, `
		INSERT INTO audit_trail_entries (
			recorded_at, event_type, client_account_identifier, instrument_symbol,
			matching_engine_order_sequence_number, detail_message, authenticated_actor_account_identifier,
			order_side_is_buy_not_sell, order_quantity, limit_price_in_minor_units, order_is_market_order_not_limit,
			buying_client_account_identifier, selling_client_account_identifier,
			executed_price_in_minor_units, executed_quantity
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		entry.RecordedAtTime, string(entry.EventType), entry.ClientAccountIdentifier, entry.InstrumentSymbol,
		int64(entry.MatchingEngineOrderSequenceNumber), entry.DetailMessage, entry.AuthenticatedActorAccountIdentifier,
		orderSideIsBuyNotSell, int64(entry.OrderQuantity), entry.LimitPriceInMinorUnits, entry.OrderIsMarketOrderNotLimit,
		entry.BuyingClientAccountIdentifier, entry.SellingClientAccountIdentifier,
		entry.ExecutedPriceInMinorUnits, int64(entry.ExecutedQuantity),
	)
	if execError != nil {
		return fmt.Errorf("audittrail: insert: %w", execError)
	}
	return nil
}

func (trail *AuditTrail) allEntriesFromPostgres() []Entry {
	return trail.queryEntriesFromPostgres(`SELECT recorded_at, event_type, client_account_identifier, instrument_symbol,
		matching_engine_order_sequence_number, detail_message, authenticated_actor_account_identifier,
		order_side_is_buy_not_sell, order_quantity, limit_price_in_minor_units, order_is_market_order_not_limit,
		buying_client_account_identifier, selling_client_account_identifier,
		executed_price_in_minor_units, executed_quantity
		FROM audit_trail_entries ORDER BY id`)
}

func (trail *AuditTrail) entriesForAccountFromPostgres(clientAccountIdentifier string) []Entry {
	return trail.queryEntriesFromPostgres(`SELECT recorded_at, event_type, client_account_identifier, instrument_symbol,
		matching_engine_order_sequence_number, detail_message, authenticated_actor_account_identifier,
		order_side_is_buy_not_sell, order_quantity, limit_price_in_minor_units, order_is_market_order_not_limit,
		buying_client_account_identifier, selling_client_account_identifier,
		executed_price_in_minor_units, executed_quantity
		FROM audit_trail_entries WHERE client_account_identifier = $1 ORDER BY id`, clientAccountIdentifier)
}

func (trail *AuditTrail) queryEntriesFromPostgres(query string, args ...any) []Entry {
	ctx := context.Background()
	rows, queryError := trail.postgres.pool.Query(ctx, query, args...)
	if queryError != nil {
		return nil
	}
	defer rows.Close()

	entries := make([]Entry, 0)
	for rows.Next() {
		var entry Entry
		var eventType string
		var matchingEngineOrderSequenceNumber int64
		var orderQuantity int64
		var executedQuantity int64
		var orderSideIsBuyNotSell *bool

		scanError := rows.Scan(
			&entry.RecordedAtTime, &eventType, &entry.ClientAccountIdentifier, &entry.InstrumentSymbol,
			&matchingEngineOrderSequenceNumber, &entry.DetailMessage, &entry.AuthenticatedActorAccountIdentifier,
			&orderSideIsBuyNotSell, &orderQuantity, &entry.LimitPriceInMinorUnits, &entry.OrderIsMarketOrderNotLimit,
			&entry.BuyingClientAccountIdentifier, &entry.SellingClientAccountIdentifier,
			&entry.ExecutedPriceInMinorUnits, &executedQuantity,
		)
		if scanError != nil {
			return nil
		}
		entry.EventType = EventType(eventType)
		entry.MatchingEngineOrderSequenceNumber = uint64(matchingEngineOrderSequenceNumber)
		entry.OrderQuantity = uint64(orderQuantity)
		entry.ExecutedQuantity = uint64(executedQuantity)
		entry.OrderSideIsBuyNotSell = orderSideIsBuyNotSell
		entries = append(entries, entry)
	}
	return entries
}
