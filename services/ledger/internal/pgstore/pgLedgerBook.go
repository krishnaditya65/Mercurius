// Package pgstore is the real Postgres-backed implementation of
// doubleentry.LedgerBook — see docs/BUILD_LOG.md's Postgres-persistence
// entry and docs/DOCUMENTATION.md's services/ledger section for the
// full story of why and how this was added.
//
// Design: PostgresLedgerBook implements the EXACT same public method
// set as doubleentry.InMemoryDoubleEntryLedgerBook (via the
// doubleentry.LedgerBook interface both now satisfy), so
// cmd/server/main.go's every OTHER consumer (internal/fundsegregation,
// internal/withdrawalworkflow, internal/multicurrencywallet) needed no
// logic changes at all — only their field/parameter type widened from
// the concrete in-memory struct to the interface, which is
// implementation-detail-shaped, not behavior-shaped. Only
// cmd/server/main.go's ONE construction call site actually changes.
//
// No ORM, no golang-migrate: schema is a handful of hand-written,
// idempotent (CREATE TABLE/INDEX IF NOT EXISTS) *.sql files in
// services/ledger/migrations, embedded via that package's go:embed
// directive and applied here, in identifier order, on every startup —
// see that package's doc comment for why no schema_migrations tracking
// table is needed.
package pgstore

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"mercurius/ledger/internal/doubleentry"
	"mercurius/ledger/migrations"
)

// PostgresLedgerBook is the real, durable implementation of
// doubleentry.LedgerBook. Every mutating method runs inside one
// Postgres transaction — a journal entry and the account-balance
// projection it produces are never left half-applied, the same
// all-or-nothing guarantee InMemoryDoubleEntryLedgerBook gives via its
// mutex, now enforced by the database instead of a Go mutex.
type PostgresLedgerBook struct {
	pool *pgxpool.Pool
}

var _ doubleentry.LedgerBook = (*PostgresLedgerBook)(nil)

// NewPostgresLedgerBook connects to postgresDsn, applies every
// migration in services/ledger/migrations (in identifier order), then
// registers every account in initialAccountIdentifiers (idempotent —
// safe to call against an already-seeded database; matches
// doubleentry.NewInMemoryDoubleEntryLedgerBookWithAccounts's own
// "start with these accounts" contract exactly).
//
// Known limitation (documented, not fixed in this pass): no connection
// pool tuning (pgxpool defaults), no retry/backoff if postgresDsn is
// unreachable at startup — this returns an error immediately and lets
// the caller (cmd/server/main.go) decide whether to fall back to an
// in-memory book or fail hard. See docs/BUILD_LOG.md.
func NewPostgresLedgerBook(ctx context.Context, postgresDsn string, initialAccountIdentifiers []string) (*PostgresLedgerBook, error) {
	pool, connectError := pgxpool.New(ctx, postgresDsn)
	if connectError != nil {
		return nil, fmt.Errorf("pgstore: connect: %w", connectError)
	}
	if pingError := pool.Ping(ctx); pingError != nil {
		pool.Close()
		return nil, fmt.Errorf("pgstore: ping: %w", pingError)
	}

	if migrateError := applyMigrations(ctx, pool); migrateError != nil {
		pool.Close()
		return nil, fmt.Errorf("pgstore: migrate: %w", migrateError)
	}

	book := &PostgresLedgerBook{pool: pool}
	for _, accountIdentifier := range initialAccountIdentifiers {
		if _, registerError := book.pool.Exec(
			ctx,
			`INSERT INTO ledger_accounts (account_identifier, balance_minor_units) VALUES ($1, 0)
			 ON CONFLICT (account_identifier) DO NOTHING`,
			accountIdentifier,
		); registerError != nil {
			pool.Close()
			return nil, fmt.Errorf("pgstore: seed account %q: %w", accountIdentifier, registerError)
		}
	}

	return book, nil
}

// Close releases the underlying connection pool — cmd/server/main.go
// calls this on graceful shutdown paths; tests call it between runs.
func (book *PostgresLedgerBook) Close() {
	book.pool.Close()
}

// applyMigrations reads every embedded *.sql file, sorted by filename
// (hence the 0001_, 0002_, ... naming convention), and executes its
// full contents as one statement batch. Every statement inside those
// files is itself idempotent, so this is safe to run on every process
// startup with no tracking table.
func applyMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	entries, readDirError := migrations.FS.ReadDir(".")
	if readDirError != nil {
		return fmt.Errorf("read embedded migrations dir: %w", readDirError)
	}

	fileNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		fileNames = append(fileNames, entry.Name())
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

// PostJournalEntry mirrors InMemoryDoubleEntryLedgerBook.PostJournalEntry
// exactly: rejects an unbalanced entry or one referencing an unknown
// account (checking every debit AND credit line's account existence
// before applying anything), and applies debit-increases/credit-
// decreases atomically. Uses context.Background() internally since the
// doubleentry.LedgerBook interface (matching the pre-existing in-memory
// method signature) takes no context — see the file-level doc comment's
// note on why that signature was preserved rather than widened.
func (book *PostgresLedgerBook) PostJournalEntry(journalEntry doubleentry.JournalEntry) error {
	ctx := context.Background()

	debitSum := sumLines(journalEntry.DebitLines)
	creditSum := sumLines(journalEntry.CreditLines)
	if debitSum != creditSum {
		return fmt.Errorf(
			"%w: debits=%d credits=%d entry=%q",
			doubleentry.ErrJournalEntryDoesNotBalance, debitSum, creditSum, journalEntry.HumanReadableDescription,
		)
	}

	transaction, beginError := book.pool.Begin(ctx)
	if beginError != nil {
		return fmt.Errorf("pgstore: begin: %w", beginError)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	for _, line := range journalEntry.DebitLines {
		if existsError := requireAccountExistsForUpdate(ctx, transaction, line.LedgerAccountIdentifier); existsError != nil {
			return existsError
		}
	}
	for _, line := range journalEntry.CreditLines {
		if existsError := requireAccountExistsForUpdate(ctx, transaction, line.LedgerAccountIdentifier); existsError != nil {
			return existsError
		}
	}

	var journalEntryId int64
	insertEntryError := transaction.QueryRow(
		ctx,
		`INSERT INTO journal_entries (human_readable_description) VALUES ($1) RETURNING id`,
		journalEntry.HumanReadableDescription,
	).Scan(&journalEntryId)
	if insertEntryError != nil {
		return fmt.Errorf("pgstore: insert journal_entries: %w", insertEntryError)
	}

	for _, line := range journalEntry.DebitLines {
		if lineError := insertLineAndApplyBalance(ctx, transaction, journalEntryId, "debit", line, +1); lineError != nil {
			return lineError
		}
	}
	for _, line := range journalEntry.CreditLines {
		if lineError := insertLineAndApplyBalance(ctx, transaction, journalEntryId, "credit", line, -1); lineError != nil {
			return lineError
		}
	}

	if commitError := transaction.Commit(ctx); commitError != nil {
		return fmt.Errorf("pgstore: commit: %w", commitError)
	}
	return nil
}

func requireAccountExistsForUpdate(ctx context.Context, transaction pgx.Tx, accountIdentifier string) error {
	var discard int64
	scanError := transaction.QueryRow(
		ctx,
		`SELECT balance_minor_units FROM ledger_accounts WHERE account_identifier = $1 FOR UPDATE`,
		accountIdentifier,
	).Scan(&discard)
	if errors.Is(scanError, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s", doubleentry.ErrUnknownLedgerAccount, accountIdentifier)
	}
	if scanError != nil {
		return fmt.Errorf("pgstore: check account %q: %w", accountIdentifier, scanError)
	}
	return nil
}

func insertLineAndApplyBalance(ctx context.Context, transaction pgx.Tx, journalEntryId int64, side string, line doubleentry.LedgerAccountLine, balanceSign int64) error {
	if _, insertLineError := transaction.Exec(
		ctx,
		`INSERT INTO journal_entry_lines (journal_entry_id, line_side, ledger_account_identifier, amount_minor_units)
		 VALUES ($1, $2, $3, $4)`,
		journalEntryId, side, line.LedgerAccountIdentifier, line.AmountInMinorUnits,
	); insertLineError != nil {
		return fmt.Errorf("pgstore: insert journal_entry_lines: %w", insertLineError)
	}

	if _, updateBalanceError := transaction.Exec(
		ctx,
		`UPDATE ledger_accounts SET balance_minor_units = balance_minor_units + $1 WHERE account_identifier = $2`,
		balanceSign*line.AmountInMinorUnits, line.LedgerAccountIdentifier,
	); updateBalanceError != nil {
		return fmt.Errorf("pgstore: update balance for %q: %w", line.LedgerAccountIdentifier, updateBalanceError)
	}
	return nil
}

func sumLines(lines []doubleentry.LedgerAccountLine) int64 {
	total := int64(0)
	for _, line := range lines {
		total += line.AmountInMinorUnits
	}
	return total
}

// RegisterAccountIfAbsent mirrors
// InMemoryDoubleEntryLedgerBook.RegisterAccountIfAbsent's exact
// contract: idempotent, returns true only if this call actually created
// the account.
func (book *PostgresLedgerBook) RegisterAccountIfAbsent(accountIdentifier string) bool {
	ctx := context.Background()
	commandTag, execError := book.pool.Exec(
		ctx,
		`INSERT INTO ledger_accounts (account_identifier, balance_minor_units) VALUES ($1, 0)
		 ON CONFLICT (account_identifier) DO NOTHING`,
		accountIdentifier,
	)
	if execError != nil {
		// The in-memory implementation's signature has no error return
		// for this method — a real transport failure here is a genuine
		// gap in what this interface can report, documented in
		// docs/BUILD_LOG.md's known-limitations list. Treated as "did
		// not newly create" rather than panicking.
		return false
	}
	return commandTag.RowsAffected() == 1
}

// CurrentBalanceInMinorUnits mirrors
// InMemoryDoubleEntryLedgerBook.CurrentBalanceInMinorUnits exactly,
// including returning doubleentry.ErrUnknownLedgerAccount (not a
// pgstore-specific error type) for an unknown account, so callers that
// do errors.Is(err, doubleentry.ErrUnknownLedgerAccount) behave
// identically regardless of which LedgerBook implementation is wired
// in.
func (book *PostgresLedgerBook) CurrentBalanceInMinorUnits(accountIdentifier string) (int64, error) {
	ctx := context.Background()
	var balance int64
	scanError := book.pool.QueryRow(
		ctx,
		`SELECT balance_minor_units FROM ledger_accounts WHERE account_identifier = $1`,
		accountIdentifier,
	).Scan(&balance)
	if errors.Is(scanError, pgx.ErrNoRows) {
		return 0, fmt.Errorf("%w: %s", doubleentry.ErrUnknownLedgerAccount, accountIdentifier)
	}
	if scanError != nil {
		return 0, fmt.Errorf("pgstore: query balance for %q: %w", accountIdentifier, scanError)
	}
	return balance, nil
}

// CaptureSnapshot mirrors InMemoryDoubleEntryLedgerBook.CaptureSnapshot's
// exact output shape (doubleentry.LedgerBookSnapshot), reading every
// account balance and every posted journal entry back out of Postgres.
// Kept for interface-completeness (cmd/server/main.go's /admin/snapshot
// and /admin/restore handlers are written against doubleentry.LedgerBook,
// not the concrete in-memory type) — but for a real Postgres-backed
// deployment, `pg_dump`/point-in-time recovery is the actual backup
// mechanism; this JSON snapshot is a convenience/dev tool here, same
// caveat internal/doubleentry/snapshotRestore.go's own doc comment
// already made for the in-memory case.
func (book *PostgresLedgerBook) CaptureSnapshot() doubleentry.LedgerBookSnapshot {
	ctx := context.Background()

	accountRows, queryAccountsError := book.pool.Query(ctx, `SELECT account_identifier, balance_minor_units FROM ledger_accounts ORDER BY account_identifier`)
	if queryAccountsError != nil {
		return doubleentry.LedgerBookSnapshot{}
	}
	defer accountRows.Close()

	accountBalances := make([]doubleentry.LedgerAccountBalanceSnapshot, 0)
	for accountRows.Next() {
		var accountBalance doubleentry.LedgerAccountBalanceSnapshot
		if scanError := accountRows.Scan(&accountBalance.LedgerAccountIdentifier, &accountBalance.BalanceInMinorUnits); scanError != nil {
			return doubleentry.LedgerBookSnapshot{}
		}
		accountBalances = append(accountBalances, accountBalance)
	}

	entryRows, queryEntriesError := book.pool.Query(ctx, `SELECT id, human_readable_description FROM journal_entries ORDER BY id`)
	if queryEntriesError != nil {
		return doubleentry.LedgerBookSnapshot{}
	}
	defer entryRows.Close()

	type entryWithId struct {
		id    int64
		entry doubleentry.JournalEntrySnapshot
	}
	entries := make([]entryWithId, 0)
	for entryRows.Next() {
		var e entryWithId
		if scanError := entryRows.Scan(&e.id, &e.entry.HumanReadableDescription); scanError != nil {
			return doubleentry.LedgerBookSnapshot{}
		}
		entries = append(entries, e)
	}

	for index := range entries {
		lineRows, queryLinesError := book.pool.Query(
			ctx,
			`SELECT line_side, ledger_account_identifier, amount_minor_units FROM journal_entry_lines WHERE journal_entry_id = $1 ORDER BY id`,
			entries[index].id,
		)
		if queryLinesError != nil {
			return doubleentry.LedgerBookSnapshot{}
		}
		for lineRows.Next() {
			var side string
			var line doubleentry.LedgerAccountLineSnapshot
			if scanError := lineRows.Scan(&side, &line.LedgerAccountIdentifier, &line.AmountInMinorUnits); scanError != nil {
				lineRows.Close()
				return doubleentry.LedgerBookSnapshot{}
			}
			if side == "debit" {
				entries[index].entry.DebitLines = append(entries[index].entry.DebitLines, line)
			} else {
				entries[index].entry.CreditLines = append(entries[index].entry.CreditLines, line)
			}
		}
		lineRows.Close()
	}

	journalEntries := make([]doubleentry.JournalEntrySnapshot, 0, len(entries))
	for _, e := range entries {
		journalEntries = append(journalEntries, e.entry)
	}

	return doubleentry.LedgerBookSnapshot{
		SnapshotFormatVersion:           1,
		AccountBalances:                 accountBalances,
		PostedJournalEntriesInPostOrder: journalEntries,
	}
}

// RestoreFromSnapshot mirrors
// InMemoryDoubleEntryLedgerBook.RestoreFromSnapshot's "hard replace, not
// a merge" contract: truncates every ledger table and re-inserts
// exactly what snapshot contains, inside one transaction, so a
// concurrent read either sees the full pre-restore or full post-restore
// state, never a partial mix. Errors are swallowed to match the
// in-memory method's signature (no error return) — a real caller can
// verify success afterward via CaptureSnapshot/CurrentBalanceInMinorUnits.
func (book *PostgresLedgerBook) RestoreFromSnapshot(snapshot doubleentry.LedgerBookSnapshot) {
	ctx := context.Background()
	transaction, beginError := book.pool.Begin(ctx)
	if beginError != nil {
		return
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	if _, execError := transaction.Exec(ctx, `TRUNCATE journal_entry_lines, journal_entries, ledger_accounts RESTART IDENTITY`); execError != nil {
		return
	}

	for _, accountBalance := range snapshot.AccountBalances {
		if _, execError := transaction.Exec(
			ctx,
			`INSERT INTO ledger_accounts (account_identifier, balance_minor_units) VALUES ($1, $2)`,
			accountBalance.LedgerAccountIdentifier, accountBalance.BalanceInMinorUnits,
		); execError != nil {
			return
		}
	}

	for _, journalEntry := range snapshot.PostedJournalEntriesInPostOrder {
		var journalEntryId int64
		insertError := transaction.QueryRow(
			ctx,
			`INSERT INTO journal_entries (human_readable_description) VALUES ($1) RETURNING id`,
			journalEntry.HumanReadableDescription,
		).Scan(&journalEntryId)
		if insertError != nil {
			return
		}
		for _, line := range journalEntry.DebitLines {
			if _, execError := transaction.Exec(
				ctx,
				`INSERT INTO journal_entry_lines (journal_entry_id, line_side, ledger_account_identifier, amount_minor_units) VALUES ($1, 'debit', $2, $3)`,
				journalEntryId, line.LedgerAccountIdentifier, line.AmountInMinorUnits,
			); execError != nil {
				return
			}
		}
		for _, line := range journalEntry.CreditLines {
			if _, execError := transaction.Exec(
				ctx,
				`INSERT INTO journal_entry_lines (journal_entry_id, line_side, ledger_account_identifier, amount_minor_units) VALUES ($1, 'credit', $2, $3)`,
				journalEntryId, line.LedgerAccountIdentifier, line.AmountInMinorUnits,
			); execError != nil {
				return
			}
		}
	}

	_ = transaction.Commit(ctx)
}
