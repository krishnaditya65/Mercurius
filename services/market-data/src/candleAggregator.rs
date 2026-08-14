// Builds a trade tape + OHLCV candles from the trade ticks matching-engine
// publishes alongside book depth. Complements `deltaPublisher.rs` (which
// handles book state); this module handles trade/print state.
//
// TODO(real build): candles here are computed incrementally, in-memory,
// per instrument, capped at a bounded ring buffer — fine for a skeleton,
// but a real build would compute these from the durable Kafka trade-tick
// topic (so a restart doesn't lose history) and likely support multiple
// interval widths (1m/5m/1h/1d), not just one fixed width.

use std::collections::HashMap;

use crate::marketDataEventTypes::{CandleBar, TradeTick};

/// How wide each candle bucket is. One minute — a reasonable default for
/// a retail charting UI; see the module TODO for why this isn't
/// configurable yet.
pub const CANDLE_INTERVAL_SECONDS: u64 = 60;

/// How many raw trade ticks / candles to retain per instrument before the
/// oldest is dropped. Bounds memory in this in-memory skeleton — a real
/// build would page older history out of Kafka/a time-series store
/// instead of just discarding it.
const MAX_RETAINED_TICKS_PER_INSTRUMENT: usize = 500;
const MAX_RETAINED_CANDLES_PER_INSTRUMENT: usize = 500;

pub struct CandleAggregator {
    recentTicksByInstrument: HashMap<String, Vec<TradeTick>>,
    candlesByInstrument: HashMap<String, Vec<CandleBar>>,
}

impl CandleAggregator {
    pub fn newEmptyAggregator() -> Self {
        CandleAggregator {
            recentTicksByInstrument: HashMap::new(),
            candlesByInstrument: HashMap::new(),
        }
    }

    /// Records one executed trade, appending it to the trade tape and
    /// folding it into the current (or a newly-opened) candle bucket for
    /// this instrument.
    /// Not called from `main.rs` any more (the real ingestion path always
    /// has an aggressor flag to pass, via `recordTradeWithAggressorSide`)
    /// — kept `pub` and exercised by this module's own pre-existing test
    /// suite, which predates the aggressor-side field and shouldn't need
    /// to widen every call site just to keep compiling.
    #[allow(dead_code)]
    pub fn recordTrade(
        &mut self,
        instrumentSymbol: &str,
        priceInMinorUnits: i64,
        quantity: u64,
        executedAtEpochSeconds: u64,
    ) {
        // Aggressor side isn't needed for OHLCV candle bucketing itself —
        // only the columnar tick store (`columnarTickStore.rs`) and the
        // order-flow footprint aggregator built on top of it care — so
        // this trade tape still defaults it to `false` rather than
        // widening every call site here too. See `main.rs`'s
        // `ingestDepthPublishMessage`, which passes the REAL aggressor
        // flag on to `columnarTickStore.appendTick` instead.
        self.recordTradeWithAggressorSide(
            instrumentSymbol,
            priceInMinorUnits,
            quantity,
            executedAtEpochSeconds,
            false,
        );
    }

    /// Same as `recordTrade`, but also stamps the real aggressor side onto
    /// the trade-tape entry (FEATURES.md §20 "Order-flow footprint
    /// charts"). Kept as a separate method (rather than widening
    /// `recordTrade`'s signature) purely to avoid touching every existing
    /// `recordTrade` call site across this module's own test suite for a
    /// field the OHLCV candle itself never reads.
    pub fn recordTradeWithAggressorSide(
        &mut self,
        instrumentSymbol: &str,
        priceInMinorUnits: i64,
        quantity: u64,
        executedAtEpochSeconds: u64,
        isBuyAggressor: bool,
    ) {
        let tickHistory = self
            .recentTicksByInstrument
            .entry(instrumentSymbol.to_string())
            .or_default();
        tickHistory.push(TradeTick {
            instrumentSymbol: instrumentSymbol.to_string(),
            executedAtEpochSeconds,
            priceInMinorUnits,
            quantity,
            isBuyAggressor,
        });
        if tickHistory.len() > MAX_RETAINED_TICKS_PER_INSTRUMENT {
            tickHistory.remove(0);
        }

        let bucketStartEpochSeconds = (executedAtEpochSeconds / CANDLE_INTERVAL_SECONDS) * CANDLE_INTERVAL_SECONDS;
        let candleHistory = self
            .candlesByInstrument
            .entry(instrumentSymbol.to_string())
            .or_default();

        match candleHistory.last_mut() {
            Some(currentCandle) if currentCandle.bucketStartEpochSeconds == bucketStartEpochSeconds => {
                currentCandle.highPriceInMinorUnits = currentCandle.highPriceInMinorUnits.max(priceInMinorUnits);
                currentCandle.lowPriceInMinorUnits = currentCandle.lowPriceInMinorUnits.min(priceInMinorUnits);
                currentCandle.closePriceInMinorUnits = priceInMinorUnits;
                currentCandle.totalVolume += quantity;
            }
            _ => {
                // Either the first trade ever for this instrument, or the
                // bucket rolled over — open a fresh candle. Note this
                // means a bucket with zero trades is simply absent from
                // the series rather than carried forward flat — a real
                // charting client should fill gaps itself if it wants a
                // continuous x-axis.
                candleHistory.push(CandleBar {
                    instrumentSymbol: instrumentSymbol.to_string(),
                    bucketStartEpochSeconds,
                    openPriceInMinorUnits: priceInMinorUnits,
                    highPriceInMinorUnits: priceInMinorUnits,
                    lowPriceInMinorUnits: priceInMinorUnits,
                    closePriceInMinorUnits: priceInMinorUnits,
                    totalVolume: quantity,
                });
                if candleHistory.len() > MAX_RETAINED_CANDLES_PER_INSTRUMENT {
                    candleHistory.remove(0);
                }
            }
        }
    }

    /// Most recent trade ticks for an instrument, oldest first, capped at
    /// `limit`.
    pub fn recentTradeTicksForInstrument(&self, instrumentSymbol: &str, limit: usize) -> Vec<TradeTick> {
        let Some(tickHistory) = self.recentTicksByInstrument.get(instrumentSymbol) else {
            return Vec::new();
        };
        let startIndex = tickHistory.len().saturating_sub(limit);
        tickHistory[startIndex..].to_vec()
    }

    /// Most recent candles for an instrument, oldest first, capped at
    /// `limit`. The last element may still be an "open" (in-progress)
    /// candle for the current bucket.
    pub fn recentCandlesForInstrument(&self, instrumentSymbol: &str, limit: usize) -> Vec<CandleBar> {
        let Some(candleHistory) = self.candlesByInstrument.get(instrumentSymbol) else {
            return Vec::new();
        };
        let startIndex = candleHistory.len().saturating_sub(limit);
        candleHistory[startIndex..].to_vec()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn firstTradeOpensANewCandleWithOhlcAllEqualToItsPrice() {
        let mut aggregator = CandleAggregator::newEmptyAggregator();
        aggregator.recordTrade("DEMO-EQ", 100, 5, 1_000);

        let candles = aggregator.recentCandlesForInstrument("DEMO-EQ", 10);
        assert_eq!(candles.len(), 1);
        assert_eq!(candles[0].openPriceInMinorUnits, 100);
        assert_eq!(candles[0].highPriceInMinorUnits, 100);
        assert_eq!(candles[0].lowPriceInMinorUnits, 100);
        assert_eq!(candles[0].closePriceInMinorUnits, 100);
        assert_eq!(candles[0].totalVolume, 5);
    }

    #[test]
    fn secondTradeInSameBucketUpdatesHighLowCloseAndAccumulatesVolume() {
        let mut aggregator = CandleAggregator::newEmptyAggregator();
        aggregator.recordTrade("DEMO-EQ", 100, 5, 1_000);
        aggregator.recordTrade("DEMO-EQ", 110, 3, 1_005); // still within the same 60s bucket [960,1020)
        aggregator.recordTrade("DEMO-EQ", 90, 2, 1_015);

        let candles = aggregator.recentCandlesForInstrument("DEMO-EQ", 10);
        assert_eq!(candles.len(), 1); // same bucket, still one candle
        assert_eq!(candles[0].openPriceInMinorUnits, 100);
        assert_eq!(candles[0].highPriceInMinorUnits, 110);
        assert_eq!(candles[0].lowPriceInMinorUnits, 90);
        assert_eq!(candles[0].closePriceInMinorUnits, 90);
        assert_eq!(candles[0].totalVolume, 10);
    }

    #[test]
    fn tradeInANewBucketOpensASeparateCandle() {
        let mut aggregator = CandleAggregator::newEmptyAggregator();
        aggregator.recordTrade("DEMO-EQ", 100, 5, 1_000); // bucket 960 (1000/60*60)
        aggregator.recordTrade("DEMO-EQ", 200, 5, 1_100); // bucket 1080 — new bucket

        let candles = aggregator.recentCandlesForInstrument("DEMO-EQ", 10);
        assert_eq!(candles.len(), 2);
        assert_eq!(candles[0].closePriceInMinorUnits, 100);
        assert_eq!(candles[1].openPriceInMinorUnits, 200);
    }

    #[test]
    fn candlesAndTicksAreTrackedIndependentlyPerInstrument() {
        let mut aggregator = CandleAggregator::newEmptyAggregator();
        aggregator.recordTrade("AAPL", 100, 5, 1_000);
        aggregator.recordTrade("MSFT", 200, 1, 1_000);

        assert_eq!(aggregator.recentCandlesForInstrument("AAPL", 10).len(), 1);
        assert_eq!(aggregator.recentCandlesForInstrument("MSFT", 10).len(), 1);
        assert_eq!(
            aggregator.recentTradeTicksForInstrument("AAPL", 10)[0].priceInMinorUnits,
            100
        );
    }

    #[test]
    fn unknownInstrumentReturnsEmptyRatherThanPanicking() {
        let aggregator = CandleAggregator::newEmptyAggregator();
        assert!(aggregator.recentCandlesForInstrument("NOPE", 10).is_empty());
        assert!(aggregator.recentTradeTicksForInstrument("NOPE", 10).is_empty());
    }

    #[test]
    fn recentQueriesRespectTheLimitAndReturnOldestFirst() {
        let mut aggregator = CandleAggregator::newEmptyAggregator();
        for tickIndex in 0..5u64 {
            aggregator.recordTrade("DEMO-EQ", 100 + tickIndex as i64, 1, 1_000 + tickIndex * 100); // separate buckets
        }

        let candles = aggregator.recentCandlesForInstrument("DEMO-EQ", 2);
        assert_eq!(candles.len(), 2);
        // Oldest-first among the last two: opens 103 then 104.
        assert_eq!(candles[0].openPriceInMinorUnits, 103);
        assert_eq!(candles[1].openPriceInMinorUnits, 104);
    }
}
