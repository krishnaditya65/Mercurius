-- Real Postgres schema for the ledger's double-entry accounting core
-- (internal/doubleentry) — see docs/BUILD_LOG.md for the entry that
-- introduced this. No ORM, no external migration-framework dependency:
-- this file (and any later-numbered file in this directory) is read and
-- applied, in identifier order, by internal/pgstore at process startup.
-- Every statement is idempotent (CREATE TABLE/INDEX IF NOT EXISTS) so
-- re-running this file against an already-migrated database is always
-- safe — there is deliberately no separate schema_migrations tracking
-- table, since idempotent DDL makes one unnecessary for a file set this
-- small.

-- One row per ledger account, holding its current balance as the fast
-- read path (GET /accounts/balance doesn't want to sum the full journal-
-- entry-line history on every request). The journal_entry_lines table
-- below remains the source of TRUTH for how that balance was arrived at
-- — ledger_accounts.balance_minor_units is a maintained projection,
-- always updated in the same transaction as the journal_entry_lines rows
-- that changed it.
CREATE TABLE IF NOT EXISTS ledger_accounts (
    account_identifier   TEXT PRIMARY KEY,
    balance_minor_units  BIGINT NOT NULL DEFAULT 0
);

-- One row per posted journal entry (never updated or deleted after
-- insert — mirrors InMemoryDoubleEntryLedgerBook's own
-- postedJournalEntriesInPostOrder, which also has no update/remove
-- method).
CREATE TABLE IF NOT EXISTS journal_entries (
    id                           BIGSERIAL PRIMARY KEY,
    human_readable_description   TEXT NOT NULL,
    posted_at                    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One row per debit or credit line of a journal entry. line_side +
-- amount_minor_units together mirror doubleentry.LedgerAccountLine;
-- which side a row is on is what convertLedgerAccountLinesToSnapshotLines
-- keys off of when reconstructing DebitLines/CreditLines.
CREATE TABLE IF NOT EXISTS journal_entry_lines (
    id                          BIGSERIAL PRIMARY KEY,
    journal_entry_id            BIGINT NOT NULL REFERENCES journal_entries(id),
    line_side                   TEXT NOT NULL CHECK (line_side IN ('debit', 'credit')),
    ledger_account_identifier   TEXT NOT NULL,
    amount_minor_units          BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_journal_entry_lines_entry_id
    ON journal_entry_lines (journal_entry_id);

CREATE INDEX IF NOT EXISTS idx_journal_entry_lines_account
    ON journal_entry_lines (ledger_account_identifier);
