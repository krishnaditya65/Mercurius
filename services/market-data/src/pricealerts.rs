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

use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Mutex;

use serde::Serialize;

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

pub struct PriceAlertStore {
    alerts: Mutex<Vec<PriceAlert>>,
    nextAlertId: AtomicU64,
}

impl PriceAlertStore {
    pub fn newEmptyStore() -> Self {
        PriceAlertStore { alerts: Mutex::new(Vec::new()), nextAlertId: AtomicU64::new(1) }
    }

    pub fn createAlert(
        &self,
        accountIdentifier: &str,
        instrumentSymbol: &str,
        isAboveNotBelow: bool,
        thresholdPriceInMinorUnits: i64,
    ) -> u64 {
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
        let alerts = self.alerts.lock().expect("price alert store mutex poisoned");
        alerts.iter().filter(|alert| alert.accountIdentifier == accountIdentifier).cloned().collect()
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
}
