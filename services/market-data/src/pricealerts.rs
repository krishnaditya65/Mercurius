// Real price alerts — FEATURES.md §9 ("Watchlists, alerts (price/
// technical triggers)"). "Technical" triggers (moving-average crosses,
// RSI thresholds, etc.) are NOT built — only the simplest and most
// common real-world case, a price crossing above or below a threshold.
// Evaluated against the same live trade-tick stream that feeds
// candleAggregator (see main.rs's ingestion loop) — an alert fires the
// moment a real trade prints past its threshold, not on a polling
// interval.
//
// TODO(real build): in-memory only (a restart loses every alert, fired
// or not), no push notification — a client has to poll GET /alerts to
// discover a fired one (same polling-stopgap category as this service's
// HTTP query API generally). No auth. No repeat/reset semantics — once
// fired, an alert stays fired forever; a real build likely wants a
// "re-arm" option for a recurring alert.
#![allow(non_snake_case)]

use std::sync::Mutex;
use std::sync::atomic::{AtomicU64, Ordering};

use serde::Serialize;

use crate::pgBacking::PgBacking;

#[derive(Debug, Clone, Serialize)]
pub struct PriceAlert {
    pub alertId: u64,
    pub accountIdentifier: String,
    pub instrumentSymbol: String,
    pub isAboveNotBelow: bool,
    pub thresholdPriceInMinorUnits: i64,
    pub isTriggered: bool,
    pub triggeredAtEpochSeconds: Option<u64>,
}

/// Real Postgres persistence (docs/BUILD_LOG.md's Postgres-persistence
/// entry): when constructed via `newPostgresBackedStore`, `postgres` is
/// set and every method below reads/writes real Postgres instead of
/// `alerts`/`nextAlertId` — Postgres (via its own `alert_id BIGSERIAL`)
/// becomes the sole source of truth and id allocator in that mode.
/// `newEmptyStore()` (unchanged) behaves exactly as it always did.
pub struct PriceAlertStore {
    alerts: Mutex<Vec<PriceAlert>>,
    nextAlertId: AtomicU64,
    postgres: Option<PgBacking>,
}

impl PriceAlertStore {
    pub fn newEmptyStore() -> Self {
        PriceAlertStore {
            alerts: Mutex::new(Vec::new()),
            nextAlertId: AtomicU64::new(1),
            postgres: None,
        }
    }

    /// Connects to postgresDsn, applies migrations (shared with
    /// watchlist.rs — see migrations/0001_watchlist_and_pricealerts.sql),
    /// and returns a real, durable PriceAlertStore. See pgBacking.rs's
    /// module doc for the block_on-around-a-dedicated-runtime pattern
    /// this uses to keep every method's signature synchronous.
    pub fn newPostgresBackedStore(postgresDsn: &str) -> Result<Self, String> {
        let postgres = PgBacking::connect(postgresDsn)?;
        Ok(PriceAlertStore {
            alerts: Mutex::new(Vec::new()),
            nextAlertId: AtomicU64::new(1),
            postgres: Some(postgres),
        })
    }

    pub fn createAlert(
        &self,
        accountIdentifier: &str,
        instrumentSymbol: &str,
        isAboveNotBelow: bool,
        thresholdPriceInMinorUnits: i64,
    ) -> u64 {
        if let Some(postgres) = &self.postgres {
            return postgres.blockOn(async {
                let row = postgres
                    .client()
                    .query_one(
                        "INSERT INTO price_alerts (account_identifier, instrument_symbol, is_above_not_below, threshold_price_in_minor_units) \
                         VALUES ($1, $2, $3, $4) RETURNING alert_id",
                        &[&accountIdentifier, &instrumentSymbol, &isAboveNotBelow, &thresholdPriceInMinorUnits],
                    )
                    .await
                    .expect("pricealerts: insert failed");
                row.get::<_, i64>(0) as u64
            });
        }

        let alertId = self.nextAlertId.fetch_add(1, Ordering::SeqCst);
        let mut alerts = self.alerts.lock().expect("price alert store mutex poisoned");
        alerts.push(PriceAlert {
            alertId,
            accountIdentifier: accountIdentifier.to_string(),
            instrumentSymbol: instrumentSymbol.to_string(),
            isAboveNotBelow,
            thresholdPriceInMinorUnits,
            isTriggered: false,
            triggeredAtEpochSeconds: None,
        });
        alertId
    }

    /// Checks every NOT-YET-triggered alert for instrumentSymbol against
    /// a real trade print at executedPriceInMinorUnits, marking any that
    /// now qualify as triggered. Called once per trade tick from main.rs
    /// — this is what makes alerts fire off REAL market data, not a
    /// polling loop re-checking the latest price on a timer.
    pub fn checkAndTriggerAlertsForTrade(
        &self,
        instrumentSymbol: &str,
        executedPriceInMinorUnits: i64,
        nowEpochSeconds: u64,
    ) -> Vec<u64> {
        if let Some(postgres) = &self.postgres {
            let nowAsI64 = nowEpochSeconds as i64;
            return postgres.blockOn(async {
                // Single UPDATE ... RETURNING: atomically flips every
                // currently-untriggered, qualifying alert for this
                // instrument to triggered and returns their ids — no
                // read-then-write race between two concurrent trade
                // ticks for the same instrument.
                let rows = postgres
                    .client()
                    .query(
                        "UPDATE price_alerts SET is_triggered = true, triggered_at_epoch_seconds = $1 \
                         WHERE instrument_symbol = $2 AND NOT is_triggered \
                         AND ((is_above_not_below AND $3 >= threshold_price_in_minor_units) \
                              OR (NOT is_above_not_below AND $3 <= threshold_price_in_minor_units)) \
                         RETURNING alert_id",
                        &[&nowAsI64, &instrumentSymbol, &executedPriceInMinorUnits],
                    )
                    .await
                    .unwrap_or_default();
                rows.iter().map(|row| row.get::<_, i64>(0) as u64).collect()
            });
        }

        let mut alerts = self.alerts.lock().expect("price alert store mutex poisoned");
        let mut newlyTriggeredAlertIds = Vec::new();

        for alert in alerts.iter_mut() {
            if alert.isTriggered || alert.instrumentSymbol != instrumentSymbol {
                continue;
            }
            let conditionMet = if alert.isAboveNotBelow {
                executedPriceInMinorUnits >= alert.thresholdPriceInMinorUnits
            } else {
                executedPriceInMinorUnits <= alert.thresholdPriceInMinorUnits
            };
            if conditionMet {
                alert.isTriggered = true;
                alert.triggeredAtEpochSeconds = Some(nowEpochSeconds);
                newlyTriggeredAlertIds.push(alert.alertId);
            }
        }

        newlyTriggeredAlertIds
    }

    pub fn alertsForAccount(&self, accountIdentifier: &str) -> Vec<PriceAlert> {
        if let Some(postgres) = &self.postgres {
            return postgres.blockOn(async {
                postgres
                    .client()
                    .query(
                        "SELECT alert_id, account_identifier, instrument_symbol, is_above_not_below, \
                         threshold_price_in_minor_units, is_triggered, triggered_at_epoch_seconds \
                         FROM price_alerts WHERE account_identifier = $1 ORDER BY alert_id",
                        &[&accountIdentifier],
                    )
                    .await
                    .map(|rows| {
                        rows.iter()
                            .map(|row| PriceAlert {
                                alertId: row.get::<_, i64>(0) as u64,
                                accountIdentifier: row.get(1),
                                instrumentSymbol: row.get(2),
                                isAboveNotBelow: row.get(3),
                                thresholdPriceInMinorUnits: row.get(4),
                                isTriggered: row.get(5),
                                triggeredAtEpochSeconds: row.get::<_, Option<i64>>(6).map(|value| value as u64),
                            })
                            .collect()
                    })
                    .unwrap_or_default()
            });
        }

        let alerts = self.alerts.lock().expect("price alert store mutex poisoned");
        alerts
            .iter()
            .filter(|alert| alert.accountIdentifier == accountIdentifier)
            .cloned()
            .collect()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn newAlertStartsUntriggered() {
        let store = PriceAlertStore::newEmptyStore();
        store.createAlert("acct-001", "DEMO-EQ", true, 100);

        let alerts = store.alertsForAccount("acct-001");
        assert_eq!(alerts.len(), 1);
        assert!(!alerts[0].isTriggered);
        assert!(alerts[0].triggeredAtEpochSeconds.is_none());
    }

    #[test]
    fn aboveAlertTriggersWhenPriceReachesOrExceedsThreshold() {
        let store = PriceAlertStore::newEmptyStore();
        let alertId = store.createAlert("acct-001", "DEMO-EQ", true, 100);

        let triggeredIds = store.checkAndTriggerAlertsForTrade("DEMO-EQ", 100, 1_000);
        assert_eq!(triggeredIds, vec![alertId]);

        let alerts = store.alertsForAccount("acct-001");
        assert!(alerts[0].isTriggered);
        assert_eq!(alerts[0].triggeredAtEpochSeconds, Some(1_000));
    }

    #[test]
    fn aboveAlertDoesNotTriggerBelowThreshold() {
        let store = PriceAlertStore::newEmptyStore();
        store.createAlert("acct-001", "DEMO-EQ", true, 100);

        let triggeredIds = store.checkAndTriggerAlertsForTrade("DEMO-EQ", 99, 1_000);
        assert!(triggeredIds.is_empty());
        assert!(!store.alertsForAccount("acct-001")[0].isTriggered);
    }

    #[test]
    fn belowAlertTriggersWhenPriceReachesOrFallsBelowThreshold() {
        let store = PriceAlertStore::newEmptyStore();
        let alertId = store.createAlert("acct-001", "DEMO-EQ", false, 90);

        let triggeredIds = store.checkAndTriggerAlertsForTrade("DEMO-EQ", 90, 1_000);
        assert_eq!(triggeredIds, vec![alertId]);
    }

    #[test]
    fn alertOnlyChecksItsOwnInstrument() {
        let store = PriceAlertStore::newEmptyStore();
        store.createAlert("acct-001", "AAPL", true, 100);

        let triggeredIds = store.checkAndTriggerAlertsForTrade("MSFT", 500, 1_000);
        assert!(triggeredIds.is_empty());
        assert!(!store.alertsForAccount("acct-001")[0].isTriggered);
    }

    #[test]
    fn onceTriggeredAnAlertStaysTriggeredAndIsNotReEvaluated() {
        let store = PriceAlertStore::newEmptyStore();
        let alertId = store.createAlert("acct-001", "DEMO-EQ", true, 100);

        let firstTrigger = store.checkAndTriggerAlertsForTrade("DEMO-EQ", 150, 1_000);
        assert_eq!(firstTrigger, vec![alertId]);

        // A second qualifying trade must NOT be reported as a fresh
        // trigger — the alert already fired once.
        let secondTrigger = store.checkAndTriggerAlertsForTrade("DEMO-EQ", 200, 2_000);
        assert!(secondTrigger.is_empty());

        let alerts = store.alertsForAccount("acct-001");
        assert_eq!(alerts[0].triggeredAtEpochSeconds, Some(1_000)); // unchanged by the second trade
    }

    #[test]
    fn multipleAlertsOnTheSameInstrumentCanTriggerIndependently() {
        let store = PriceAlertStore::newEmptyStore();
        let lowAlertId = store.createAlert("acct-001", "DEMO-EQ", true, 50);
        let highAlertId = store.createAlert("acct-001", "DEMO-EQ", true, 500);

        let triggeredIds = store.checkAndTriggerAlertsForTrade("DEMO-EQ", 100, 1_000);
        assert_eq!(triggeredIds, vec![lowAlertId]);

        let triggeredIdsSecondTrade = store.checkAndTriggerAlertsForTrade("DEMO-EQ", 600, 2_000);
        assert_eq!(triggeredIdsSecondTrade, vec![highAlertId]);
    }

    #[test]
    fn alertsForAccountOnlyReturnsThatAccountsAlerts() {
        let store = PriceAlertStore::newEmptyStore();
        store.createAlert("acct-001", "DEMO-EQ", true, 100);
        store.createAlert("acct-002", "DEMO-EQ", true, 200);

        assert_eq!(store.alertsForAccount("acct-001").len(), 1);
        assert_eq!(store.alertsForAccount("acct-002").len(), 1);
    }

    // -------------------------------------------------------------
    // Real tests against a real, locally-running Postgres — no mocks.
    // See docs/BUILD_LOG.md's Postgres-persistence entry.
    // -------------------------------------------------------------

    fn testMarketDataPostgresDsn() -> String {
        std::env::var("MARKET_DATA_PGSTORE_TEST_DSN")
            .unwrap_or_else(|_| "postgres://trading:trading@localhost:5432/marketdata".to_string())
    }

    fn openTestPostgresBackedStoreOrSkip() -> Option<PriceAlertStore> {
        match PriceAlertStore::newPostgresBackedStore(&testMarketDataPostgresDsn()) {
            Ok(store) => {
                store.postgres.as_ref().unwrap().blockOn(async {
                    store
                        .postgres
                        .as_ref()
                        .unwrap()
                        .client()
                        .batch_execute("TRUNCATE price_alerts RESTART IDENTITY")
                        .await
                        .expect("truncate price_alerts");
                });
                Some(store)
            }
            Err(connectError) => {
                eprintln!("skipping Postgres-backed pricealerts test: {connectError}");
                None
            }
        }
    }

    #[test]
    fn postgresBacked_createAndTrigger() {
        let Some(store) = openTestPostgresBackedStoreOrSkip() else { return };

        let alertId = store.createAlert("acct-001", "DEMO-EQ", true, 100);
        assert!(!store.alertsForAccount("acct-001")[0].isTriggered);

        let triggeredIds = store.checkAndTriggerAlertsForTrade("DEMO-EQ", 150, 1_000);
        assert_eq!(triggeredIds, vec![alertId]);

        let alerts = store.alertsForAccount("acct-001");
        assert!(alerts[0].isTriggered);
        assert_eq!(alerts[0].triggeredAtEpochSeconds, Some(1_000));

        // A second qualifying trade must NOT re-report the same alert.
        let secondTrigger = store.checkAndTriggerAlertsForTrade("DEMO-EQ", 200, 2_000);
        assert!(secondTrigger.is_empty());
    }

    #[test]
    fn postgresBacked_persistsAcrossFreshConnection() {
        let Some(firstStore) = openTestPostgresBackedStoreOrSkip() else { return };
        let alertId = firstStore.createAlert("acct-restart-test", "DEMO-EQ", false, 90);
        firstStore.checkAndTriggerAlertsForTrade("DEMO-EQ", 80, 500);

        let secondStore =
            PriceAlertStore::newPostgresBackedStore(&testMarketDataPostgresDsn()).expect("second connection should succeed");
        let alerts = secondStore.alertsForAccount("acct-restart-test");
        assert_eq!(alerts.len(), 1);
        assert_eq!(alerts[0].alertId, alertId);
        assert!(alerts[0].isTriggered);
    }
}
