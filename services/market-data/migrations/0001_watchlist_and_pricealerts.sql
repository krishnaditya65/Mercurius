-- Real Postgres schema for watchlist.rs's WatchlistStore and
-- pricealerts.rs's PriceAlertStore — see docs/BUILD_LOG.md's
-- Postgres-persistence entry. No ORM, no external migration-framework
-- dependency: applied at process startup, in identifier order, by
-- pgBacking.rs. Every statement is idempotent (CREATE TABLE/INDEX IF
-- NOT EXISTS) — same convention as services/ledger/migrations and
-- services/oms-gateway/migrations, no schema_migrations tracking table.
--
-- Deliberately NOT included here: candleAggregator.rs's trade tape/
-- candles or columnarTickStore.rs's tick store — both stay in-memory
-- for this pass, a deliberate hot-path performance tradeoff, not an
-- oversight. See docs/DOCUMENTATION.md's services/market-data section.

CREATE TABLE IF NOT EXISTS watchlist_symbols (
    account_identifier   TEXT NOT NULL,
    instrument_symbol    TEXT NOT NULL,
    PRIMARY KEY (account_identifier, instrument_symbol)
);

-- Append-only change log — the real delta-sync primitive
-- changesForAccountSince reads from. Never updated/deleted, mirroring
-- the in-memory Vec<WatchlistChangeEvent>'s own append-only shape.
CREATE TABLE IF NOT EXISTS watchlist_changes (
    id                    BIGSERIAL PRIMARY KEY,
    account_identifier    TEXT NOT NULL,
    instrument_symbol     TEXT NOT NULL,
    epoch_millis          BIGINT NOT NULL,
    was_added             BOOLEAN NOT NULL,
    device_identifier     TEXT
);

CREATE INDEX IF NOT EXISTS idx_watchlist_changes_account_epoch
    ON watchlist_changes (account_identifier, epoch_millis);

CREATE TABLE IF NOT EXISTS price_alerts (
    alert_id                       BIGSERIAL PRIMARY KEY,
    account_identifier             TEXT NOT NULL,
    instrument_symbol              TEXT NOT NULL,
    is_above_not_below             BOOLEAN NOT NULL,
    threshold_price_in_minor_units BIGINT NOT NULL,
    is_triggered                   BOOLEAN NOT NULL DEFAULT false,
    triggered_at_epoch_seconds     BIGINT
);

CREATE INDEX IF NOT EXISTS idx_price_alerts_account
    ON price_alerts (account_identifier);

CREATE INDEX IF NOT EXISTS idx_price_alerts_instrument_untriggered
    ON price_alerts (instrument_symbol) WHERE NOT is_triggered;
