-- Real Postgres schema for internal/positions — net signed quantity per
-- (account, instrument). See docs/BUILD_LOG.md's Postgres-persistence
-- entry. Idempotent DDL, applied in identifier order alongside
-- 0001_audit_trail.sql — see that file's header comment for the shared
-- migration convention.
CREATE TABLE IF NOT EXISTS positions (
    account_identifier   TEXT NOT NULL,
    instrument_symbol    TEXT NOT NULL,
    net_quantity          BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (account_identifier, instrument_symbol)
);
