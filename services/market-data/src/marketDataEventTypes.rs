// Domain types for the market data pipeline. See ARCHITECTURE.md §5.
//
// TODO(real build): these types should be shared with matching-engine via
// `libs/proto` once the wire format is finalized (SBE for Tier 0->1
// hand-off, per ARCHITECTURE.md §3.5) instead of being redefined here.

/// A single price-level change, as broadcast to clients. Only CHANGED
/// levels are sent — never the full book — per the delta-compression
/// requirement in ARCHITECTURE.md §5.
#[derive(Debug, Clone)]
pub struct PriceLevelDeltaUpdate {
    pub instrumentSymbol: String,
    pub isBidSide: bool,
    pub priceInMinorUnits: i64,
    pub newTotalQuantityAtPrice: u64, // 0 means the level was fully removed
}

/// Every message sent to a client carries a monotonic sequence number per
/// instrument. Clients that detect a gap between the last-seen sequence
/// number and an incoming one MUST request a fresh snapshot rather than
/// silently continuing on a now-inconsistent book — see ARCHITECTURE.md §5
/// ("snapshot + delta protocol with sequence numbers").
#[derive(Debug, Clone)]
pub struct SequencedMarketDataMessage {
    pub instrumentSymbol: String,
    pub perInstrumentSequenceNumber: u64,
    pub deltaUpdatesInThisMessage: Vec<PriceLevelDeltaUpdate>,
}

/// A full point-in-time book snapshot, sent when a client (re)subscribes
/// or reports a detected sequence gap.
#[derive(Debug, Clone)]
pub struct FullBookSnapshotMessage {
    pub instrumentSymbol: String,
    pub snapshotSequenceNumber: u64,
    pub bidLevelsBestFirst: Vec<(i64, u64)>,
    pub askLevelsBestFirst: Vec<(i64, u64)>,
}

/// One executed trade, as ingested from matching-engine. This is the raw
/// input to `candleAggregator.rs`'s OHLCV bucketing — see that module for
/// what's built on top of it. `executedAtEpochSeconds` is stamped by
/// market-data on receipt (matching-engine doesn't send a timestamp today
/// — see the TODO on `IncomingTradeTickWireEvent`), so it reflects
/// ingestion time, not true execution time; close enough for a skeleton,
/// wrong for anything that needs exchange-grade timestamp accuracy.
#[derive(Debug, Clone, serde::Serialize)]
pub struct TradeTick {
    pub instrumentSymbol: String,
    pub executedAtEpochSeconds: u64,
    pub priceInMinorUnits: i64,
    pub quantity: u64,
}

/// One OHLCV bar for a fixed-width time bucket
/// (`CandleAggregator::CANDLE_INTERVAL_SECONDS`). `openPriceInMinorUnits`
/// is the price of the first trade tick to land in this bucket,
/// `closePriceInMinorUnits` the most recent one seen so far (a candle for
/// the CURRENT, still-open bucket is mutable — its close keeps moving
/// until the bucket rolls over).
#[derive(Debug, Clone, serde::Serialize)]
pub struct CandleBar {
    pub instrumentSymbol: String,
    pub bucketStartEpochSeconds: u64,
    pub openPriceInMinorUnits: i64,
    pub highPriceInMinorUnits: i64,
    pub lowPriceInMinorUnits: i64,
    pub closePriceInMinorUnits: i64,
    pub totalVolume: u64,
}
