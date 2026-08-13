// Per-account watchlists — FEATURES.md §9 ("Watchlists, alerts
// (price/technical triggers)"). Deliberately simple: a set of instrument
// symbols per account, nothing more (no custom ordering, no grouping
// into multiple named lists — a real build likely wants both).
//
// TODO(real build): in-memory only, no persistence, no auth (any caller
// can read/modify any account's watchlist — same gap as every other
// unauthenticated endpoint in this repo). Account identifiers here are
// free-text, not reconciled with services/auth's identifier space (see
// that service's own README for the same unresolved gap elsewhere).
#![allow(non_snake_case)]

use std::collections::{HashMap, HashSet};
use std::sync::Mutex;

pub struct WatchlistStore {
    symbolsByAccount: Mutex<HashMap<String, HashSet<String>>>,
}

impl WatchlistStore {
    pub fn newEmptyStore() -> Self {
        WatchlistStore { symbolsByAccount: Mutex::new(HashMap::new()) }
    }

    /// Returns true if the symbol was newly added, false if it was
    /// already on the watchlist (idempotent — adding twice isn't an
    /// error, it's a no-op).
    pub fn addSymbol(&self, accountIdentifier: &str, instrumentSymbol: &str) -> bool {
        let mut symbolsByAccount = self.symbolsByAccount.lock().expect("watchlist mutex poisoned");
        symbolsByAccount
            .entry(accountIdentifier.to_string())
            .or_insert_with(HashSet::new)
            .insert(instrumentSymbol.to_string())
    }

    /// Returns true if the symbol was present and removed, false if it
    /// wasn't on the watchlist to begin with.
    pub fn removeSymbol(&self, accountIdentifier: &str, instrumentSymbol: &str) -> bool {
        let mut symbolsByAccount = self.symbolsByAccount.lock().expect("watchlist mutex poisoned");
        match symbolsByAccount.get_mut(accountIdentifier) {
            Some(symbols) => symbols.remove(instrumentSymbol),
            None => false,
        }
    }

    /// Symbols on accountIdentifier's watchlist, sorted for a stable,
    /// deterministic response (a HashSet has no defined iteration order).
    pub fn symbolsForAccount(&self, accountIdentifier: &str) -> Vec<String> {
        let symbolsByAccount = self.symbolsByAccount.lock().expect("watchlist mutex poisoned");
        let mut symbols: Vec<String> = symbolsByAccount
            .get(accountIdentifier)
            .map(|set| set.iter().cloned().collect())
            .unwrap_or_default();
        symbols.sort();
        symbols
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
        store.addSymbol("acct-001", "DEMO-EQ");
        assert_eq!(store.symbolsForAccount("acct-001"), vec!["DEMO-EQ".to_string()]);
    }

    #[test]
    fn addingTheSameSymbolTwiceIsIdempotent() {
        let store = WatchlistStore::newEmptyStore();
        let firstAddWasNew = store.addSymbol("acct-001", "DEMO-EQ");
        let secondAddWasNew = store.addSymbol("acct-001", "DEMO-EQ");

        assert!(firstAddWasNew);
        assert!(!secondAddWasNew);
        assert_eq!(store.symbolsForAccount("acct-001").len(), 1);
    }

    #[test]
    fn removingASymbolTakesItOffTheWatchlist() {
        let store = WatchlistStore::newEmptyStore();
        store.addSymbol("acct-001", "DEMO-EQ");
        let wasRemoved = store.removeSymbol("acct-001", "DEMO-EQ");

        assert!(wasRemoved);
        assert!(store.symbolsForAccount("acct-001").is_empty());
    }

    #[test]
    fn removingASymbolThatWasNeverAddedReturnsFalse() {
        let store = WatchlistStore::newEmptyStore();
        assert!(!store.removeSymbol("acct-001", "NEVER-ADDED"));
    }

    #[test]
    fn twoAccountsHaveIndependentWatchlists() {
        let store = WatchlistStore::newEmptyStore();
        store.addSymbol("acct-001", "AAPL");
        store.addSymbol("acct-002", "MSFT");

        assert_eq!(store.symbolsForAccount("acct-001"), vec!["AAPL".to_string()]);
        assert_eq!(store.symbolsForAccount("acct-002"), vec!["MSFT".to_string()]);
    }

    #[test]
    fn symbolsAreReturnedInSortedOrder() {
        let store = WatchlistStore::newEmptyStore();
        store.addSymbol("acct-001", "MSFT");
        store.addSymbol("acct-001", "AAPL");
        store.addSymbol("acct-001", "GOOG");

        assert_eq!(
            store.symbolsForAccount("acct-001"),
            vec!["AAPL".to_string(), "GOOG".to_string(), "MSFT".to_string()]
        );
    }
}
