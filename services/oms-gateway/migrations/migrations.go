// Package migrations embeds this directory's *.sql files so
// internal/pgstore can apply them at process startup with no external
// migration-framework dependency — same convention as
// services/ledger/migrations. Applied in identifier order; every
// statement must be idempotent (CREATE TABLE/INDEX IF NOT EXISTS).
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
