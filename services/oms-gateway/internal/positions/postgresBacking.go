// Real Postgres persistence for PositionBook — see
// docs/BUILD_LOG.md's Postgres-persistence entry. Same
// "extend-the-concrete-struct" design as internal/audittrail's
// postgresBacking.go — see that file's header comment for the full
// rationale (every consumer already holds a *PositionBook concrete
// pointer; only cmd/server/main.go's construction site changes).
//
// Only the REAL positionBook is Postgres-backed — paperPositionBook
// (paper trading, FEATURES.md §7) and milliSharePaperPositionBook
// (fractional shares) stay in-memory-only, unchanged, out of scope for
// this pass: a simulated position was never real money/holdings to
// begin with, so persisting it has materially lower value than the
// three stores this pass actually targets (audittrail, positions,
// idempotency).
package positions

import (
	"context"
	"fmt"
	"log"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"

	"mercurius/omsgateway/migrations"
)

type postgresBacking struct {
	pool *pgxpool.Pool
}

// NewPostgresBackedPositionBook connects to postgresDsn, applies every
// migration in services/oms-gateway/migrations, and returns a real,
// durable PositionBook. See internal/audittrail's
// NewPostgresBackedAuditTrail for the shared no-retry-on-startup
// caveat.
func NewPostgresBackedPositionBook(ctx context.Context, postgresDsn string) (*PositionBook, error) {
	pool, connectError := pgxpool.New(ctx, postgresDsn)
	if connectError != nil {
		return nil, fmt.Errorf("positions: connect: %w", connectError)
	}
	if pingError := pool.Ping(ctx); pingError != nil {
		pool.Close()
		return nil, fmt.Errorf("positions: ping: %w", pingError)
	}
	if migrateError := applyMigrations(ctx, pool); migrateError != nil {
		pool.Close()
		return nil, fmt.Errorf("positions: migrate: %w", migrateError)
	}
	return &PositionBook{postgres: &postgresBacking{pool: pool}}, nil
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

// adjustPositionInPostgres applies signedQuantityDelta to one
// (account, instrument) row, creating the row (starting from 0) first
// if absent — a real, atomic UPSERT, not a read-then-write race.
func (positionBook *PositionBook) adjustPositionInPostgres(accountIdentifier string, instrumentSymbol string, signedQuantityDelta int64) {
	ctx := context.Background()
	_, execError := positionBook.postgres.pool.Exec(ctx, `
		INSERT INTO positions (account_identifier, instrument_symbol, net_quantity)
		VALUES ($1, $2, $3)
		ON CONFLICT (account_identifier, instrument_symbol)
		DO UPDATE SET net_quantity = positions.net_quantity + EXCLUDED.net_quantity`,
		accountIdentifier, instrumentSymbol, signedQuantityDelta,
	)
	if execError != nil {
		// PositionBook's pre-existing methods have no error return —
		// logged, not silently dropped, not panicked. See
		// docs/BUILD_LOG.md's known-limitations list.
		logPositionsPersistenceError("adjust", accountIdentifier, instrumentSymbol, execError)
	}
}

func (positionBook *PositionBook) setPositionInPostgres(accountIdentifier string, instrumentSymbol string, newQuantity int64) {
	ctx := context.Background()
	_, execError := positionBook.postgres.pool.Exec(ctx, `
		INSERT INTO positions (account_identifier, instrument_symbol, net_quantity)
		VALUES ($1, $2, $3)
		ON CONFLICT (account_identifier, instrument_symbol)
		DO UPDATE SET net_quantity = EXCLUDED.net_quantity`,
		accountIdentifier, instrumentSymbol, newQuantity,
	)
	if execError != nil {
		logPositionsPersistenceError("set", accountIdentifier, instrumentSymbol, execError)
	}
}

func (positionBook *PositionBook) positionsForAccountFromPostgres(accountIdentifier string) map[string]int64 {
	ctx := context.Background()
	rows, queryError := positionBook.postgres.pool.Query(ctx,
		`SELECT instrument_symbol, net_quantity FROM positions WHERE account_identifier = $1 AND net_quantity != 0`,
		accountIdentifier,
	)
	if queryError != nil {
		return map[string]int64{}
	}
	defer rows.Close()

	result := make(map[string]int64)
	for rows.Next() {
		var instrumentSymbol string
		var netQuantity int64
		if scanError := rows.Scan(&instrumentSymbol, &netQuantity); scanError != nil {
			return map[string]int64{}
		}
		result[instrumentSymbol] = netQuantity
	}
	return result
}

func logPositionsPersistenceError(operation string, accountIdentifier string, instrumentSymbol string, execError error) {
	log.Printf("positions: FAILED to %s (account=%s instrument=%s) in Postgres: %v", operation, accountIdentifier, instrumentSymbol, execError)
}
