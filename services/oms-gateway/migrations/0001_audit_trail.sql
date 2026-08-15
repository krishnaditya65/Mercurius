-- Real Postgres schema for internal/audittrail — see
-- docs/BUILD_LOG.md's Postgres-persistence entry. No ORM, no external
-- migration-framework dependency: applied at process startup, in
-- identifier order, by internal/pgstore. Every statement is idempotent
-- (CREATE TABLE/INDEX IF NOT EXISTS) — no schema_migrations tracking
-- table, same convention as services/ledger/migrations.
--
-- Append-only by CONVENTION (application code never issues UPDATE/
-- DELETE against this table — see internal/pgstore/pgAuditTrail.go) —
-- a real WORM guarantee would additionally need a REVOKE UPDATE, DELETE
-- ON audit_trail_entries FROM <app role> at the database-grant level,
-- which this dev-default "trading" superuser-ish role does not have
-- applied. Documented as a known limitation, not silently assumed.
CREATE TABLE IF NOT EXISTS audit_trail_entries (
    id                                       BIGSERIAL PRIMARY KEY,
    recorded_at                              TIMESTAMPTZ NOT NULL,
    event_type                               TEXT NOT NULL,
    client_account_identifier                TEXT NOT NULL DEFAULT '',
    instrument_symbol                        TEXT NOT NULL DEFAULT '',
    matching_engine_order_sequence_number    BIGINT NOT NULL DEFAULT 0,
    detail_message                           TEXT NOT NULL DEFAULT '',
    authenticated_actor_account_identifier   TEXT NOT NULL DEFAULT '',
    order_side_is_buy_not_sell               BOOLEAN,
    order_quantity                           BIGINT NOT NULL DEFAULT 0,
    limit_price_in_minor_units               BIGINT NOT NULL DEFAULT 0,
    order_is_market_order_not_limit          BOOLEAN NOT NULL DEFAULT false,
    buying_client_account_identifier         TEXT NOT NULL DEFAULT '',
    selling_client_account_identifier        TEXT NOT NULL DEFAULT '',
    executed_price_in_minor_units            BIGINT NOT NULL DEFAULT 0,
    executed_quantity                        BIGINT NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_audit_trail_entries_account
    ON audit_trail_entries (client_account_identifier);

CREATE INDEX IF NOT EXISTS idx_audit_trail_entries_recorded_at
    ON audit_trail_entries (recorded_at);
