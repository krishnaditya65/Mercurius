// FEATURES.md §8 [P3] — "Tick-level storage in a columnar time-series
// store for replay/backtest". A real column-oriented (struct-of-arrays)
// in-memory store: each tick FIELD lives in its own contiguous `Vec`
// (`timestampsEpochSeconds`, `pricesInMinorUnits`, `quantities`), one such
// trio of arrays per instrument, rather than one `Vec<TickRow>` of
// interleaved fields (array-of-structs). Wired into the real ingestion
// path in `main.rs`'s `ingestDepthPublishMessage` — every real (or
// simulated, `simulatedExchangeFeedGenerator.rs`) trade tick that flows
// through gets appended here, in addition to `candleAggregator.rs`'s
// separate OHLCV bucketing.
//
// TODO(real build): a real time-series/columnar store (e.g. a Parquet-
// backed or Arrow-backed columnar file format, or a real TSDB) would
// support far more than this: compression per column, multiple retention
// tiers, and durability across restarts. This is in-memory only and capped
// per-instrument, same posture as `candleAggregator.rs`.
#![allow(non_snake_case)]

use std::collections::HashMap;
use std::sync::Mutex;

/// How many ticks to retain per instrument before the oldest is evicted.
/// Bounds memory in this in-memory skeleton, same rationale as
/// `candleAggregator::MAX_RETAINED_TICKS_PER_INSTRUMENT` (a separate,
/// larger constant here since this store's whole purpose is deeper
/// history for replay/backtest, not just a short recent trade tape).
const MAX_RETAINED_TICKS_PER_INSTRUMENT: usize = 50_000;

/// Struct-of-arrays column storage for ONE instrument's tick history.
/// `timestampsEpochSeconds` is always kept sorted ascending — ticks are
/// appended in arrival order, and both the real ingestion path and the
/// simulated feed generator only ever produce non-decreasing timestamps —
/// which is what lets `ColumnarTickStore::rangeQuery` binary-search for
/// its window bounds (`partition_point`, O(log n)) instead of
/// linear-scanning every retained tick the way a naive row-oriented store
/// scanned tick-by-tick would have to.
#[derive(Default)]
struct SymbolColumns {
    timestampsEpochSeconds: Vec<u64>,
    pricesInMinorUnits: Vec<i64>,
    quantities: Vec<u64>,
}

/// One tick as returned by a range query — a row-shaped VIEW reconstructed
/// on the fly from the column arrays. Materializing a "row" only happens
/// at query time; the store itself never holds ticks in this shape.
#[derive(Debug, Clone, PartialEq, serde::Serialize)]
pub struct TickRecord {
    pub instrumentSymbol: String,
    pub executedAtEpochSeconds: u64,
    pub priceInMinorUnits: i64,
    pub quantity: u64,
}

pub struct ColumnarTickStore {
    columnsBySymbol: Mutex<HashMap<String, SymbolColumns>>,
}

impl ColumnarTickStore {
    pub fn newEmptyStore() -> Self {
        ColumnarTickStore {
            columnsBySymbol: Mutex::new(HashMap::new()),
        }
    }

    /// Appends one tick to `instrumentSymbol`'s column arrays. Callers
    /// (`main.rs`'s ingestion path) are expected to call this with
    /// non-decreasing `executedAtEpochSeconds` per symbol — see the
    /// struct doc for why that invariant matters to `rangeQuery`.
    pub fn appendTick(
        &self,
        instrumentSymbol: &str,
        executedAtEpochSeconds: u64,
        priceInMinorUnits: i64,
        quantity: u64,
    ) {
        let mut columnsBySymbol = self.columnsBySymbol.lock().expect("columnar tick store mutex poisoned");
        let columns = columnsBySymbol.entry(instrumentSymbol.to_string()).or_default();

        columns.timestampsEpochSeconds.push(executedAtEpochSeconds);
        columns.pricesInMinorUnits.push(priceInMinorUnits);
        columns.quantities.push(quantity);

        if columns.timestampsEpochSeconds.len() > MAX_RETAINED_TICKS_PER_INSTRUMENT {
            columns.timestampsEpochSeconds.remove(0);
            columns.pricesInMinorUnits.remove(0);
            columns.quantities.remove(0);
        }
    }

    /// Returns every retained tick for `instrumentSymbol` with
    /// `startEpochSeconds <= executedAtEpochSeconds <= endEpochSeconds`
    /// (inclusive both ends), oldest first. Finds the window's start/end
    /// indices with two binary searches (`partition_point`) over the
    /// sorted timestamp column — O(log n) to locate the window plus O(k)
    /// to materialize the `k` matching rows, rather than an O(n) linear
    /// scan of every retained tick.
    pub fn rangeQuery(&self, instrumentSymbol: &str, startEpochSeconds: u64, endEpochSeconds: u64) -> Vec<TickRecord> {
        let columnsBySymbol = self.columnsBySymbol.lock().expect("columnar tick store mutex poisoned");
        let Some(columns) = columnsBySymbol.get(instrumentSymbol) else {
            return Vec::new();
        };

        if startEpochSeconds > endEpochSeconds {
            return Vec::new();
        }

        let startIndex = columns
            .timestampsEpochSeconds
            .partition_point(|&timestamp| timestamp < startEpochSeconds);
        let endIndex = columns
            .timestampsEpochSeconds
            .partition_point(|&timestamp| timestamp <= endEpochSeconds);

        (startIndex..endIndex)
            .map(|index| TickRecord {
                instrumentSymbol: instrumentSymbol.to_string(),
                executedAtEpochSeconds: columns.timestampsEpochSeconds[index],
                priceInMinorUnits: columns.pricesInMinorUnits[index],
                quantity: columns.quantities[index],
            })
            .collect()
    }

    /// Total retained ticks for an instrument (post-eviction). Useful for
    /// tests/diagnostics without materializing a full range query. Not
    /// called from `main.rs` today (only from this module's own tests) —
    /// kept `pub` since it's a reasonable thing for an HTTP diagnostics
    /// route to expose later.
    #[allow(dead_code)]
    pub fn tickCountForInstrument(&self, instrumentSymbol: &str) -> usize {
        let columnsBySymbol = self.columnsBySymbol.lock().expect("columnar tick store mutex poisoned");
        columnsBySymbol
            .get(instrumentSymbol)
            .map(|columns| columns.timestampsEpochSeconds.len())
            .unwrap_or(0)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rangeQueryOnUnknownInstrumentReturnsEmpty() {
        let store = ColumnarTickStore::newEmptyStore();
        assert!(store.rangeQuery("NOPE", 0, 1000).is_empty());
    }

    #[test]
    fn singleAppendedTickIsReturnedByARangeThatContainsIt() {
        let store = ColumnarTickStore::newEmptyStore();
        store.appendTick("DEMO-EQ", 100, 10_000, 5);

        let results = store.rangeQuery("DEMO-EQ", 0, 200);
        assert_eq!(results.len(), 1);
        assert_eq!(results[0].executedAtEpochSeconds, 100);
        assert_eq!(results[0].priceInMinorUnits, 10_000);
        assert_eq!(results[0].quantity, 5);
    }

    #[test]
    fn tickOutsideTheRangeIsExcluded() {
        let store = ColumnarTickStore::newEmptyStore();
        store.appendTick("DEMO-EQ", 500, 10_000, 5);

        assert!(store.rangeQuery("DEMO-EQ", 0, 100).is_empty());
        assert!(store.rangeQuery("DEMO-EQ", 600, 1000).is_empty());
    }

    #[test]
    fn rangeBoundsAreInclusiveOnBothEnds() {
        let store = ColumnarTickStore::newEmptyStore();
        store.appendTick("DEMO-EQ", 100, 1, 1);
        store.appendTick("DEMO-EQ", 200, 2, 1);
        store.appendTick("DEMO-EQ", 300, 3, 1);

        let exactBoundaryResults = store.rangeQuery("DEMO-EQ", 100, 300);
        assert_eq!(exactBoundaryResults.len(), 3);

        let exclusiveOfEndpointsResults = store.rangeQuery("DEMO-EQ", 101, 299);
        assert_eq!(exclusiveOfEndpointsResults.len(), 1);
        assert_eq!(exclusiveOfEndpointsResults[0].executedAtEpochSeconds, 200);
    }

    #[test]
    fn resultsAreOrderedOldestFirst() {
        let store = ColumnarTickStore::newEmptyStore();
        for timestamp in [100u64, 200, 300] {
            store.appendTick("DEMO-EQ", timestamp, timestamp as i64, 1);
        }

        let results = store.rangeQuery("DEMO-EQ", 0, 1000);
        assert_eq!(
            results
                .iter()
                .map(|tick| tick.executedAtEpochSeconds)
                .collect::<Vec<_>>(),
            vec![100, 200, 300]
        );
    }

    #[test]
    fn multipleTicksWithTheSameTimestampAreAllIncluded() {
        let store = ColumnarTickStore::newEmptyStore();
        store.appendTick("DEMO-EQ", 100, 10, 1);
        store.appendTick("DEMO-EQ", 100, 20, 2);
        store.appendTick("DEMO-EQ", 100, 30, 3);

        let results = store.rangeQuery("DEMO-EQ", 100, 100);
        assert_eq!(results.len(), 3);
        assert_eq!(
            results.iter().map(|tick| tick.priceInMinorUnits).collect::<Vec<_>>(),
            vec![10, 20, 30]
        );
    }

    #[test]
    fn instrumentsAreStoredIndependently() {
        let store = ColumnarTickStore::newEmptyStore();
        store.appendTick("AAPL", 100, 15_000, 10);
        store.appendTick("MSFT", 100, 30_000, 20);

        let aaplResults = store.rangeQuery("AAPL", 0, 1000);
        let msftResults = store.rangeQuery("MSFT", 0, 1000);

        assert_eq!(aaplResults.len(), 1);
        assert_eq!(aaplResults[0].priceInMinorUnits, 15_000);
        assert_eq!(msftResults.len(), 1);
        assert_eq!(msftResults[0].priceInMinorUnits, 30_000);
    }

    #[test]
    fn invertedRangeReturnsEmptyRatherThanPanicking() {
        let store = ColumnarTickStore::newEmptyStore();
        store.appendTick("DEMO-EQ", 100, 1, 1);
        assert!(store.rangeQuery("DEMO-EQ", 500, 100).is_empty());
    }

    #[test]
    fn tickCountReflectsAppendedTicks() {
        let store = ColumnarTickStore::newEmptyStore();
        assert_eq!(store.tickCountForInstrument("DEMO-EQ"), 0);
        store.appendTick("DEMO-EQ", 1, 1, 1);
        store.appendTick("DEMO-EQ", 2, 1, 1);
        assert_eq!(store.tickCountForInstrument("DEMO-EQ"), 2);
    }

    #[test]
    fn oldestTicksAreEvictedOnceRetentionCapIsExceeded() {
        let store = ColumnarTickStore::newEmptyStore();
        for timestamp in 0..(MAX_RETAINED_TICKS_PER_INSTRUMENT as u64 + 10) {
            store.appendTick("DEMO-EQ", timestamp, timestamp as i64, 1);
        }

        assert_eq!(
            store.tickCountForInstrument("DEMO-EQ"),
            MAX_RETAINED_TICKS_PER_INSTRUMENT
        );
        // The oldest 10 timestamps (0..10) should have been evicted, so a
        // range query covering them should find nothing left there.
        assert!(store.rangeQuery("DEMO-EQ", 0, 9).is_empty());
        // But something near the tail should still be present.
        let tailResults = store.rangeQuery(
            "DEMO-EQ",
            MAX_RETAINED_TICKS_PER_INSTRUMENT as u64 + 9,
            MAX_RETAINED_TICKS_PER_INSTRUMENT as u64 + 9,
        );
        assert_eq!(tailResults.len(), 1);
    }

    #[test]
    fn instrumentSymbolIsStampedOntoEveryReturnedRecord() {
        let store = ColumnarTickStore::newEmptyStore();
        store.appendTick("DEMO-EQ", 1, 1, 1);
        let results = store.rangeQuery("DEMO-EQ", 0, 10);
        assert_eq!(results[0].instrumentSymbol, "DEMO-EQ");
    }

    #[test]
    fn wholeHistoryRangeQueryReturnsEverythingRetained() {
        let store = ColumnarTickStore::newEmptyStore();
        for timestamp in 0..25u64 {
            store.appendTick("DEMO-EQ", timestamp, timestamp as i64, 1);
        }
        let results = store.rangeQuery("DEMO-EQ", u64::MIN, u64::MAX);
        assert_eq!(results.len(), 25);
    }
}
