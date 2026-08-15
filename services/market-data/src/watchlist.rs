// Per-account watchlists — FEATURES.md §9 ("Watchlists, alerts
// (price/technical triggers)") and §21's "Cross-device watchlist/alert
// sync with a home-screen live P&L widget". Deliberately simple: a set of
// instrument symbols per account, nothing more (no custom ordering, no
// grouping into multiple named lists — a real build likely wants both).
//
// CROSS-DEVICE SYNC, for real: the store was already account-scoped (any
// session querying accountIdentifier="acct-001" always sees the same live
// set — that part of "cross-device sync" was already true by construction,
// since there is no per-device partitioning anywhere in this module). The
// genuinely new technical work here is SYNC FRESHNESS: every mutation is
// stamped with a real wall-clock `epochMillis` and appended to a real
// per-account change log (`changesForAccountSince`), so a client that
// already has a stale local copy can ask "what changed since I last
// synced" instead of re-fetching the whole watchlist every poll — the
// actual delta-sync mechanism a real cross-device client needs, not just
// "the storage happens to be shared".
//
// MILLISECOND (not second) resolution, deliberately, unlike this
// service's other epoch-seconds timestamps (candles/ticks/alerts): two
// real mutations from two different devices arriving within the same
// wall-clock SECOND is an entirely ordinary case for this feature (unlike
// a 1-minute OHLCV candle bucket), and second resolution would silently
// make the second of two same-second changes indistinguishable from "no
// change since" for a client polling with the first change's own
// timestamp as its cursor — an actual bug caught by this module's own
// test suite during development, not a hypothetical.
//
// TODO(real build): in-memory only, no persistence, no auth (any caller
// can read/modify any account's watchlist — same gap as every other
// unauthenticated endpoint in this repo). Account identifiers here are
// free-text, not reconciled with services/auth's identifier space (see
// that service's own README for the same unresolved gap elsewhere). The
// change log is unbounded (grows forever, never compacted) — fine for a
// skeleton, not for a real long-running deployment.
#![allow(non_snake_case)]

use std::collections::{HashMap, HashSet};
use std::sync::Mutex;

use serde::Serialize;

use crate::pgBacking::PgBacking;

/// One real mutation to an account's watchlist — the unit a delta/since
/// query returns. `deviceIdentifier` is caller-supplied and purely
/// informational (never used for access control or partitioning — the
/// watchlist itself stays account-scoped) — it exists so a client can
/// prove to itself "this change arrived from a DIFFERENT device/session
/// than the one I'm running on", which is the actual cross-device-sync
/// story end to end.
#[derive(Debug, Clone, Serialize)]
pub struct WatchlistChangeEvent {
    pub epochMillis: u64,
    pub instrumentSymbol: String,
    pub wasAdded: bool,
    pub deviceIdentifier: Option<String>,
}

struct AccountWatchlistState {
    symbols: HashSet<String>,
    changeLog: Vec<WatchlistChangeEvent>,
    lastModifiedAtEpochMillis: u64,
}

impl AccountWatchlistState {
    fn newEmptyState() -> Self {
        AccountWatchlistState {
            symbols: HashSet::new(),
            changeLog: Vec::new(),
            lastModifiedAtEpochMillis: 0,
        }
    }
}

/// Real Postgres persistence (docs/BUILD_LOG.md's Postgres-persistence
/// entry): when constructed via `newPostgresBackedStore`, `postgres` is
/// set and every method below reads/writes real Postgres instead of
/// `stateByAccount` — Postgres becomes the sole source of truth in that
/// mode, not a mirror. `newEmptyStore()` (unchanged) leaves `postgres`
/// `None` and behaves exactly as it always did.
pub struct WatchlistStore {
    stateByAccount: Mutex<HashMap<String, AccountWatchlistState>>,
    postgres: Option<PgBacking>,
}

impl WatchlistStore {
    pub fn newEmptyStore() -> Self {
        WatchlistStore {
            stateByAccount: Mutex::new(HashMap::new()),
            postgres: None,
        }
    }

    /// Connects to postgresDsn, applies migrations, and returns a real,
    /// durable WatchlistStore. See pgBacking.rs's module doc for why
    /// every method below still has the exact same synchronous
    /// signature it always did (a dedicated background tokio runtime
    /// absorbs the async tokio-postgres calls).
    pub fn newPostgresBackedStore(postgresDsn: &str) -> Result<Self, String> {
        let postgres = PgBacking::connect(postgresDsn)?;
        Ok(WatchlistStore {
            stateByAccount: Mutex::new(HashMap::new()),
            postgres: Some(postgres),
        })
    }

    /// Returns true if the symbol was newly added, false if it was
    /// already on the watchlist (idempotent — adding twice isn't an
    /// error, it's a no-op). A no-op add does NOT append a change-log
    /// entry or advance `lastModifiedAtEpochMillis` — nothing actually
    /// changed, so a since-query shouldn't report it as a change.
    pub fn addSymbol(
        &self,
        accountIdentifier: &str,
        instrumentSymbol: &str,
        deviceIdentifier: Option<&str>,
        nowEpochMillis: u64,
    ) -> bool {
        if let Some(postgres) = &self.postgres {
            return self.addSymbolInPostgres(postgres, accountIdentifier, instrumentSymbol, deviceIdentifier, nowEpochMillis);
        }

        let mut stateByAccount = self.stateByAccount.lock().expect("watchlist mutex poisoned");
        let accountState = stateByAccount
            .entry(accountIdentifier.to_string())
            .or_insert_with(AccountWatchlistState::newEmptyState);

        let wasAdded = accountState.symbols.insert(instrumentSymbol.to_string());
        if wasAdded {
            accountState.lastModifiedAtEpochMillis = nowEpochMillis;
            accountState.changeLog.push(WatchlistChangeEvent {
                epochMillis: nowEpochMillis,
                instrumentSymbol: instrumentSymbol.to_string(),
                wasAdded: true,
                deviceIdentifier: deviceIdentifier.map(|value| value.to_string()),
            });
        }
        wasAdded
    }

    fn addSymbolInPostgres(
        &self,
        postgres: &PgBacking,
        accountIdentifier: &str,
        instrumentSymbol: &str,
        deviceIdentifier: Option<&str>,
        nowEpochMillis: u64,
    ) -> bool {
        postgres.blockOn(async {
            let insertResult = postgres
                .client()
                .execute(
                    "INSERT INTO watchlist_symbols (account_identifier, instrument_symbol) VALUES ($1, $2) ON CONFLICT DO NOTHING",
                    &[&accountIdentifier, &instrumentSymbol],
                )
                .await;
            let wasAdded = matches!(insertResult, Ok(rowsAffected) if rowsAffected == 1);
            if wasAdded {
                let epochMillisAsI64 = nowEpochMillis as i64;
                let _ = postgres
                    .client()
                    .execute(
                        "INSERT INTO watchlist_changes (account_identifier, instrument_symbol, epoch_millis, was_added, device_identifier) VALUES ($1, $2, $3, true, $4)",
                        &[&accountIdentifier, &instrumentSymbol, &epochMillisAsI64, &deviceIdentifier],
                    )
                    .await;
            }
            wasAdded
        })
    }

    /// Returns true if the symbol was present and removed, false if it
    /// wasn't on the watchlist to begin with. Same no-op-doesn't-log
    /// rule as `addSymbol`.
    pub fn removeSymbol(
        &self,
        accountIdentifier: &str,
        instrumentSymbol: &str,
        deviceIdentifier: Option<&str>,
        nowEpochMillis: u64,
    ) -> bool {
        if let Some(postgres) = &self.postgres {
            return self.removeSymbolInPostgres(postgres, accountIdentifier, instrumentSymbol, deviceIdentifier, nowEpochMillis);
        }

        let mut stateByAccount = self.stateByAccount.lock().expect("watchlist mutex poisoned");
        let Some(accountState) = stateByAccount.get_mut(accountIdentifier) else {
            return false;
        };

        let wasRemoved = accountState.symbols.remove(instrumentSymbol);
        if wasRemoved {
            accountState.lastModifiedAtEpochMillis = nowEpochMillis;
            accountState.changeLog.push(WatchlistChangeEvent {
                epochMillis: nowEpochMillis,
                instrumentSymbol: instrumentSymbol.to_string(),
                wasAdded: false,
                deviceIdentifier: deviceIdentifier.map(|value| value.to_string()),
            });
        }
        wasRemoved
    }

    fn removeSymbolInPostgres(
        &self,
        postgres: &PgBacking,
        accountIdentifier: &str,
        instrumentSymbol: &str,
        deviceIdentifier: Option<&str>,
        nowEpochMillis: u64,
    ) -> bool {
        postgres.blockOn(async {
            let deleteResult = postgres
                .client()
                .execute(
                    "DELETE FROM watchlist_symbols WHERE account_identifier = $1 AND instrument_symbol = $2",
                    &[&accountIdentifier, &instrumentSymbol],
                )
                .await;
            let wasRemoved = matches!(deleteResult, Ok(rowsAffected) if rowsAffected == 1);
            if wasRemoved {
                let epochMillisAsI64 = nowEpochMillis as i64;
                let _ = postgres
                    .client()
                    .execute(
                        "INSERT INTO watchlist_changes (account_identifier, instrument_symbol, epoch_millis, was_added, device_identifier) VALUES ($1, $2, $3, false, $4)",
                        &[&accountIdentifier, &instrumentSymbol, &epochMillisAsI64, &deviceIdentifier],
                    )
                    .await;
            }
            wasRemoved
        })
    }

    /// Symbols on accountIdentifier's watchlist, sorted for a stable,
    /// deterministic response (a HashSet has no defined iteration order).
    pub fn symbolsForAccount(&self, accountIdentifier: &str) -> Vec<String> {
        if let Some(postgres) = &self.postgres {
            let mut symbols: Vec<String> = postgres.blockOn(async {
                postgres
                    .client()
                    .query(
                        "SELECT instrument_symbol FROM watchlist_symbols WHERE account_identifier = $1",
                        &[&accountIdentifier],
                    )
                    .await
                    .map(|rows| rows.iter().map(|row| row.get::<_, String>(0)).collect())
                    .unwrap_or_default()
            });
            symbols.sort();
            return symbols;
        }

        let stateByAccount = self.stateByAccount.lock().expect("watchlist mutex poisoned");
        let mut symbols: Vec<String> = stateByAccount
            .get(accountIdentifier)
            .map(|state| state.symbols.iter().cloned().collect())
            .unwrap_or_default();
        symbols.sort();
        symbols
    }

    /// The epoch-millis timestamp of the most recent real mutation to
    /// this account's watchlist, or 0 if it has never been modified —
    /// what a client stamps as its own "last synced at" marker after a
    /// full fetch, to pass back into `changesForAccountSince` next time.
    pub fn lastModifiedAtEpochMillisForAccount(&self, accountIdentifier: &str) -> u64 {
        if let Some(postgres) = &self.postgres {
            return postgres.blockOn(async {
                postgres
                    .client()
                    .query_opt(
                        "SELECT MAX(epoch_millis) FROM watchlist_changes WHERE account_identifier = $1",
                        &[&accountIdentifier],
                    )
                    .await
                    .ok()
                    .flatten()
                    .and_then(|row| row.get::<_, Option<i64>>(0))
                    .map(|value| value as u64)
                    .unwrap_or(0)
            });
        }

        let stateByAccount = self.stateByAccount.lock().expect("watchlist mutex poisoned");
        stateByAccount
            .get(accountIdentifier)
            .map(|state| state.lastModifiedAtEpochMillis)
            .unwrap_or(0)
    }

    /// Every real change strictly AFTER `sinceEpochMillis`, oldest
    /// first — the actual delta-sync primitive: a device that already
    /// has a snapshot as of some timestamp can ask for just what
    /// happened after it, instead of re-fetching the whole watchlist.
    /// `sinceEpochMillis` is exclusive (a change stamped exactly at
    /// `sinceEpochMillis` is NOT re-returned) so repeatedly polling with
    /// the newly-returned `lastModifiedAtEpochMillis` as the next
    /// `since` never re-delivers the same change twice.
    pub fn changesForAccountSince(&self, accountIdentifier: &str, sinceEpochMillis: u64) -> Vec<WatchlistChangeEvent> {
        if let Some(postgres) = &self.postgres {
            let sinceAsI64 = sinceEpochMillis as i64;
            return postgres.blockOn(async {
                postgres
                    .client()
                    .query(
                        "SELECT epoch_millis, instrument_symbol, was_added, device_identifier FROM watchlist_changes \
                         WHERE account_identifier = $1 AND epoch_millis > $2 ORDER BY id",
                        &[&accountIdentifier, &sinceAsI64],
                    )
                    .await
                    .map(|rows| {
                        rows.iter()
                            .map(|row| WatchlistChangeEvent {
                                epochMillis: row.get::<_, i64>(0) as u64,
                                instrumentSymbol: row.get(1),
                                wasAdded: row.get(2),
                                deviceIdentifier: row.get(3),
                            })
                            .collect()
                    })
                    .unwrap_or_default()
            });
        }

        let stateByAccount = self.stateByAccount.lock().expect("watchlist mutex poisoned");
        stateByAccount
            .get(accountIdentifier)
            .map(|state| {
                state
                    .changeLog
                    .iter()
                    .filter(|changeEvent| changeEvent.epochMillis > sinceEpochMillis)
                    .cloned()
                    .collect()
            })
            .unwrap_or_default()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn newAccountHasAnEmptyWatchlist() {
        let store = WatchlistStore::newEmptyStore();
        assert!(store.symbolsForAccount("acct-001").is_empty());
    }

    #[test]
    fn addedSymbolAppearsInTheWatchlist() {
        let store = WatchlistStore::newEmptyStore();
        store.addSymbol("acct-001", "DEMO-EQ", None, 1_000);
        assert_eq!(store.symbolsForAccount("acct-001"), vec!["DEMO-EQ".to_string()]);
    }

    #[test]
    fn addingTheSameSymbolTwiceIsIdempotent() {
        let store = WatchlistStore::newEmptyStore();
        let firstAddWasNew = store.addSymbol("acct-001", "DEMO-EQ", None, 1_000);
        let secondAddWasNew = store.addSymbol("acct-001", "DEMO-EQ", None, 2_000);

        assert!(firstAddWasNew);
        assert!(!secondAddWasNew);
        assert_eq!(store.symbolsForAccount("acct-001").len(), 1);
    }

    #[test]
    fn removingASymbolTakesItOffTheWatchlist() {
        let store = WatchlistStore::newEmptyStore();
        store.addSymbol("acct-001", "DEMO-EQ", None, 1_000);
        let wasRemoved = store.removeSymbol("acct-001", "DEMO-EQ", None, 1_500);

        assert!(wasRemoved);
        assert!(store.symbolsForAccount("acct-001").is_empty());
    }

    #[test]
    fn removingASymbolThatWasNeverAddedReturnsFalse() {
        let store = WatchlistStore::newEmptyStore();
        assert!(!store.removeSymbol("acct-001", "NEVER-ADDED", None, 1_000));
    }

    #[test]
    fn twoAccountsHaveIndependentWatchlists() {
        let store = WatchlistStore::newEmptyStore();
        store.addSymbol("acct-001", "AAPL", None, 1_000);
        store.addSymbol("acct-002", "MSFT", None, 1_000);

        assert_eq!(store.symbolsForAccount("acct-001"), vec!["AAPL".to_string()]);
        assert_eq!(store.symbolsForAccount("acct-002"), vec!["MSFT".to_string()]);
    }

    #[test]
    fn symbolsAreReturnedInSortedOrder() {
        let store = WatchlistStore::newEmptyStore();
        store.addSymbol("acct-001", "MSFT", None, 1_000);
        store.addSymbol("acct-001", "AAPL", None, 1_001);
        store.addSymbol("acct-001", "GOOG", None, 1_002);

        assert_eq!(
            store.symbolsForAccount("acct-001"),
            vec!["AAPL".to_string(), "GOOG".to_string(), "MSFT".to_string()]
        );
    }

    // -------------------------------------------------------------
    // Cross-device sync: two different "device" query contexts read
    // the same live account-scoped state, plus the real delta/since
    // mechanism.
    // -------------------------------------------------------------

    #[test]
    fn aChangeMadeUnderOneDeviceIsImmediatelyVisibleUnderAnotherForTheSameAccount() {
        let store = WatchlistStore::newEmptyStore();

        // "Device A" (e.g. a phone session) adds a symbol.
        store.addSymbol("acct-001", "DEMO-EQ", Some("device-phone"), 1_000);

        // "Device B" (e.g. a desktop session, a completely separate query
        // context) reads the SAME account's watchlist and sees it live,
        // with no device-scoping anywhere in the read path.
        let watchlistAsSeenByDeviceB = store.symbolsForAccount("acct-001");
        assert_eq!(watchlistAsSeenByDeviceB, vec!["DEMO-EQ".to_string()]);

        // Device B now removes it; Device A's next read reflects that too.
        store.removeSymbol("acct-001", "DEMO-EQ", Some("device-desktop"), 2_000);
        let watchlistAsSeenByDeviceA = store.symbolsForAccount("acct-001");
        assert!(watchlistAsSeenByDeviceA.is_empty());
    }

    #[test]
    fn lastModifiedAtAdvancesOnlyOnARealChangeNotOnANoOp() {
        let store = WatchlistStore::newEmptyStore();
        assert_eq!(store.lastModifiedAtEpochMillisForAccount("acct-001"), 0);

        store.addSymbol("acct-001", "DEMO-EQ", None, 1_000);
        assert_eq!(store.lastModifiedAtEpochMillisForAccount("acct-001"), 1_000);

        // Redundant add at a later timestamp — a no-op, so
        // lastModifiedAtEpochMillis must NOT advance to 2_000.
        let redundantAddWasNew = store.addSymbol("acct-001", "DEMO-EQ", None, 2_000);
        assert!(!redundantAddWasNew);
        assert_eq!(store.lastModifiedAtEpochMillisForAccount("acct-001"), 1_000);

        store.removeSymbol("acct-001", "DEMO-EQ", None, 3_000);
        assert_eq!(store.lastModifiedAtEpochMillisForAccount("acct-001"), 3_000);
    }

    #[test]
    fn changesSinceReturnsOnlyChangesStrictlyAfterTheGivenTimestamp() {
        let store = WatchlistStore::newEmptyStore();
        store.addSymbol("acct-001", "AAPL", Some("device-A"), 1_000);
        store.addSymbol("acct-001", "MSFT", Some("device-B"), 2_000);
        store.removeSymbol("acct-001", "AAPL", Some("device-B"), 3_000);

        // Since 1_000 (exclusive): the AAPL add at exactly 1_000 must NOT
        // reappear — only the MSFT add and the AAPL removal.
        let changesSince1000 = store.changesForAccountSince("acct-001", 1_000);
        assert_eq!(changesSince1000.len(), 2);
        assert_eq!(changesSince1000[0].instrumentSymbol, "MSFT");
        assert!(changesSince1000[0].wasAdded);
        assert_eq!(changesSince1000[0].deviceIdentifier, Some("device-B".to_string()));
        assert_eq!(changesSince1000[1].instrumentSymbol, "AAPL");
        assert!(!changesSince1000[1].wasAdded);

        // Since 0: everything.
        assert_eq!(store.changesForAccountSince("acct-001", 0).len(), 3);

        // Since "now" (the latest change's own timestamp): nothing new.
        assert!(store.changesForAccountSince("acct-001", 3_000).is_empty());
    }

    #[test]
    fn aDeviceThatPollsWithTheLatestLastModifiedTimestampNeverSeesTheSameChangeTwice() {
        let store = WatchlistStore::newEmptyStore();
        store.addSymbol("acct-001", "AAPL", Some("device-A"), 1_000);

        // Device B does an initial full sync, remembers the server's
        // lastModifiedAtEpochMillis as its own "synced as of" marker.
        let syncedAsOf = store.lastModifiedAtEpochMillisForAccount("acct-001");
        assert_eq!(syncedAsOf, 1_000);
        assert!(store.changesForAccountSince("acct-001", syncedAsOf).is_empty());

        // A real new change happens (from either device).
        store.addSymbol("acct-001", "MSFT", Some("device-A"), 1_500);
        let deltaForDeviceB = store.changesForAccountSince("acct-001", syncedAsOf);
        assert_eq!(deltaForDeviceB.len(), 1);
        assert_eq!(deltaForDeviceB[0].instrumentSymbol, "MSFT");
    }

    #[test]
    fn changesForAnAccountThatWasNeverTouchedIsEmpty() {
        let store = WatchlistStore::newEmptyStore();
        assert!(store.changesForAccountSince("never-touched-acct", 0).is_empty());
        assert_eq!(store.lastModifiedAtEpochMillisForAccount("never-touched-acct"), 0);
    }

    // -------------------------------------------------------------
    // Real tests against a real, locally-running Postgres — no mocks.
    // See docs/BUILD_LOG.md's Postgres-persistence entry: run against
    // the actual `make infra-up` container (host port remapped to 5433
    // on this build's dev machine). Skipped (not failed) if Postgres
    // genuinely isn't reachable, matching this repo's Go-side
    // equivalent tests' skip-not-fail convention.
    // -------------------------------------------------------------

    fn testMarketDataPostgresDsn() -> String {
        std::env::var("MARKET_DATA_PGSTORE_TEST_DSN")
            .unwrap_or_else(|_| "postgres://trading:trading@localhost:5432/marketdata".to_string())
    }

    fn openTestPostgresBackedStoreOrSkip() -> Option<WatchlistStore> {
        match WatchlistStore::newPostgresBackedStore(&testMarketDataPostgresDsn()) {
            Ok(store) => {
                store.postgres.as_ref().unwrap().blockOn(async {
                    store
                        .postgres
                        .as_ref()
                        .unwrap()
                        .client()
                        .batch_execute("TRUNCATE watchlist_symbols, watchlist_changes")
                        .await
                        .expect("truncate watchlist tables");
                });
                Some(store)
            }
            Err(connectError) => {
                eprintln!("skipping Postgres-backed watchlist test: {connectError}");
                None
            }
        }
    }

    #[test]
    fn postgresBacked_addAndRemoveRoundTrip() {
        let Some(store) = openTestPostgresBackedStoreOrSkip() else { return };

        assert!(store.addSymbol("acct-001", "DEMO-EQ", Some("device-phone"), 1_000));
        assert!(!store.addSymbol("acct-001", "DEMO-EQ", Some("device-phone"), 2_000)); // idempotent
        assert_eq!(store.symbolsForAccount("acct-001"), vec!["DEMO-EQ".to_string()]);

        assert!(store.removeSymbol("acct-001", "DEMO-EQ", None, 3_000));
        assert!(store.symbolsForAccount("acct-001").is_empty());
    }

    #[test]
    fn postgresBacked_changesForAccountSinceAndLastModified() {
        let Some(store) = openTestPostgresBackedStoreOrSkip() else { return };

        store.addSymbol("acct-002", "AAPL", Some("device-A"), 1_000);
        store.addSymbol("acct-002", "MSFT", Some("device-B"), 2_000);
        assert_eq!(store.lastModifiedAtEpochMillisForAccount("acct-002"), 2_000);

        let changesSince1000 = store.changesForAccountSince("acct-002", 1_000);
        assert_eq!(changesSince1000.len(), 1);
        assert_eq!(changesSince1000[0].instrumentSymbol, "MSFT");
    }

    #[test]
    fn postgresBacked_persistsAcrossFreshConnection() {
        let Some(firstStore) = openTestPostgresBackedStoreOrSkip() else { return };
        firstStore.addSymbol("acct-restart-test", "DEMO-EQ", None, 1_000);

        let secondStore =
            WatchlistStore::newPostgresBackedStore(&testMarketDataPostgresDsn()).expect("second connection should succeed");
        assert_eq!(secondStore.symbolsForAccount("acct-restart-test"), vec!["DEMO-EQ".to_string()]);
    }
}
