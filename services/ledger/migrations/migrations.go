// Package migrations embeds this directory's *.sql files so
// internal/pgstore can apply them at process startup without any
// external migration-framework dependency (no golang-migrate, no ORM —
// see docs/DOCUMENTATION.md's services/ledger section for why). Files
// are applied in identifier order (0001_, 0002_, ...); every statement
// in every file must be idempotent (CREATE TABLE/INDEX IF NOT EXISTS)
// since there is no schema_migrations tracking table — re-applying an
// already-migrated file is always safe by construction, not by
// tracking what's already run.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
