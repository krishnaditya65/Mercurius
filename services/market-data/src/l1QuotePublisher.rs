// FEATURES.md §8 [P1]/[P2] — L1 (top-of-book) quote tracking + the
// snapshot/delta/resync sequencing contract that both this feed and
// `deltaPublisher.rs`'s book-delta feed are built on.
//
// A "full depth publish" from matching-engine (see
// `ingestionWireProtocol.rs` and the TODO on
// `OrderBookCore::currentBookDepthSnapshot`) carries EVERY currently-
// resting price level on both sides, not an incremental diff — a level
// with zero quantity is simply absent rather than sent as a zeroed
// entry. That means the best bid/ask for one publish can be derived
// directly from that publish's own delta list (max bid price present,
// min ask price present) without market-data needing to reconstruct a
// full price ladder itself. This module does exactly that, then keeps
// the derived L1Quote around per instrument so a newly-connecting WS
// client can be sent a SNAPSHOT of current state before switching to
// DELTA messages — see `l1QuoteWebSocketServer.rs` for the networking
// side of that protocol.
#![allow(non_snake_case)]

use std::collections::HashMap;

use crate::marketDataEventTypes::PriceLevelDeltaUpdate;

/// Top-of-book state for one instrument at a point in time. `None` means
/// "no resting interest on that side right now" (e.g. a thin/empty book
/// on one side), not "unknown" — this skeleton has no notion of a book
/// that hasn't been observed at all yet vs. one that's genuinely empty
/// on a side.
#[derive(Debug, Clone, PartialEq, serde::Serialize)]
pub struct L1Quote {
    pub instrumentSymbol: String,
    pub bestBidPriceInMinorUnits: Option<i64>,
    pub bestBidQuantity: u64,
    pub bestAskPriceInMinorUnits: Option<i64>,
    pub bestAskQuantity: u64,
    pub lastTradePriceInMinorUnits: Option<i64>,
}

impl L1Quote {
    fn emptyQuoteForInstrument(instrumentSymbol: &str) -> Self {
        L1Quote {
            instrumentSymbol: instrumentSymbol.to_string(),
            bestBidPriceInMinorUnits: None,
            bestBidQuantity: 0,
            bestAskPriceInMinorUnits: None,
            bestAskQuantity: 0,
            lastTradePriceInMinorUnits: None,
        }
    }
}

/// One L1 quote update carrying the per-instrument sequence number it
/// was assigned. Whether this is delivered to a client as a SNAPSHOT or
/// a DELTA is a framing decision made by the WS layer
/// (`l1QuoteWebSocketServer.rs`), not something this type encodes itself
/// — the payload shape (a full `L1Quote`) is identical either way, which
/// is the whole point of top-of-book: there's nothing smaller than "the
/// current best bid/ask/last" to send as an incremental delta.
#[derive(Debug, Clone)]
pub struct SequencedL1QuoteUpdate {
    // Deliberately Clone (unlike DeltaPublisher's own
    // SequencedMarketDataMessage) — this type is broadcast to every
    // connected WS client via a `tokio::sync::broadcast` channel, which
    // requires its payload to be Clone since every subscriber gets its
    // own copy.
    pub instrumentSymbol: String,
    pub perInstrumentSequenceNumber: u64,
    pub quote: L1Quote,
}

/// Derives L1 (best bid/ask/last-trade) state from the same real
/// ingestion stream `deltaPublisher.rs` and `candleAggregator.rs`
/// consume, assigns a monotonic per-instrument sequence number to every
/// resulting update (independent from `DeltaPublisher`'s own book-delta
/// sequence counters — these are two different topics per
/// ARCHITECTURE.md §5's "sequence number per symbol/topic" requirement),
/// and fans the update out to every registered downstream sink. Also
/// retains the current quote per instrument so a late-joining subscriber
/// can be caught up with a snapshot instead of only ever seeing deltas
/// from whenever it happened to connect.
/// One registered downstream sink's callback shape — named so
/// `L1QuotePublisher`'s field doesn't need to spell out the raw
/// `Vec<Box<dyn FnMut(...) + Send>>` type inline
/// (clippy::type_complexity). `Send` (unlike `DeltaPublisher`'s own
/// sink type) because this publisher's sink closure captures a
/// `tokio::sync::broadcast::Sender` that gets moved into `main.rs`'s
/// ingestion thread.
type DownstreamSink = Box<dyn FnMut(&SequencedL1QuoteUpdate) + Send>;

pub struct L1QuotePublisher {
    currentQuoteByInstrument: HashMap<String, L1Quote>,
    lastSequenceNumberByInstrument: HashMap<String, u64>,
    registeredDownstreamSinks: Vec<DownstreamSink>,
}

impl L1QuotePublisher {
    pub fn newPublisherWithNoSinks() -> Self {
        L1QuotePublisher {
            currentQuoteByInstrument: HashMap::new(),
            lastSequenceNumberByInstrument: HashMap::new(),
            registeredDownstreamSinks: Vec::new(),
        }
    }

    /// Registers a downstream sink (in the real build: the WS fan-out
    /// broadcast channel). Every L1 update produced by
    /// `applyDepthPublishForInstrument` is delivered to every registered
    /// sink, same fan-out shape as `DeltaPublisher::registerDownstreamSink`.
    pub fn registerDownstreamSink<SinkFn>(&mut self, downstreamSink: SinkFn)
    where
        SinkFn: FnMut(&SequencedL1QuoteUpdate) + Send + 'static,
    {
        self.registeredDownstreamSinks.push(Box::new(downstreamSink));
    }

    /// Recomputes best bid/ask from one full depth publish's delta list
    /// (see the module doc for why that's valid given matching-engine
    /// sends full depth, not an incremental diff), optionally folds in
    /// the price of a just-executed trade as the new last-traded price,
    /// bumps this instrument's L1 sequence number, and fans the
    /// resulting quote out to every registered sink. Called once per
    /// depth publish from `main.rs`'s ingestion loop — real push the
    /// instant new data arrives, not a polling loop.
    pub fn applyDepthPublishForInstrument(
        &mut self,
        instrumentSymbol: &str,
        deltaUpdatesInThisPublish: &[PriceLevelDeltaUpdate],
        latestTradePriceInMinorUnits: Option<i64>,
    ) -> SequencedL1QuoteUpdate {
        let bestBid = deltaUpdatesInThisPublish
            .iter()
            .filter(|delta| delta.isBidSide && delta.newTotalQuantityAtPrice > 0)
            .max_by_key(|delta| delta.priceInMinorUnits);
        let bestAsk = deltaUpdatesInThisPublish
            .iter()
            .filter(|delta| !delta.isBidSide && delta.newTotalQuantityAtPrice > 0)
            .min_by_key(|delta| delta.priceInMinorUnits);

        let previousQuote = self
            .currentQuoteByInstrument
            .get(instrumentSymbol)
            .cloned()
            .unwrap_or_else(|| L1Quote::emptyQuoteForInstrument(instrumentSymbol));

        let updatedQuote = L1Quote {
            instrumentSymbol: instrumentSymbol.to_string(),
            bestBidPriceInMinorUnits: bestBid.map(|delta| delta.priceInMinorUnits),
            bestBidQuantity: bestBid.map(|delta| delta.newTotalQuantityAtPrice).unwrap_or(0),
            bestAskPriceInMinorUnits: bestAsk.map(|delta| delta.priceInMinorUnits),
            bestAskQuantity: bestAsk.map(|delta| delta.newTotalQuantityAtPrice).unwrap_or(0),
            lastTradePriceInMinorUnits: latestTradePriceInMinorUnits.or(previousQuote.lastTradePriceInMinorUnits),
        };

        self.currentQuoteByInstrument
            .insert(instrumentSymbol.to_string(), updatedQuote.clone());

        let nextSequenceNumberForInstrument = self
            .lastSequenceNumberByInstrument
            .entry(instrumentSymbol.to_string())
            .and_modify(|sequenceNumber| *sequenceNumber += 1)
            .or_insert(1);

        let sequencedUpdate = SequencedL1QuoteUpdate {
            instrumentSymbol: instrumentSymbol.to_string(),
            perInstrumentSequenceNumber: *nextSequenceNumberForInstrument,
            quote: updatedQuote,
        };

        for downstreamSink in self.registeredDownstreamSinks.iter_mut() {
            downstreamSink(&sequencedUpdate);
        }

        sequencedUpdate
    }

    /// The current quote + sequence number for one instrument, if
    /// anything has ever been published for it — what a WS server sends
    /// as a SNAPSHOT on connect (or on an explicit RESYNC_REQUEST).
    pub fn currentSnapshotForInstrument(&self, instrumentSymbol: &str) -> Option<SequencedL1QuoteUpdate> {
        let quote = self.currentQuoteByInstrument.get(instrumentSymbol)?.clone();
        let sequenceNumber = *self.lastSequenceNumberByInstrument.get(instrumentSymbol)?;
        Some(SequencedL1QuoteUpdate {
            instrumentSymbol: instrumentSymbol.to_string(),
            perInstrumentSequenceNumber: sequenceNumber,
            quote,
        })
    }

    /// A snapshot for every instrument this publisher has ever seen a
    /// depth publish for — what a freshly-connected WS client (one that
    /// hasn't named a specific instrument yet) is caught up with before
    /// it starts receiving DELTA messages for everything.
    pub fn currentSnapshotsForAllInstruments(&self) -> Vec<SequencedL1QuoteUpdate> {
        let mut instrumentSymbols: Vec<&String> = self.currentQuoteByInstrument.keys().collect();
        instrumentSymbols.sort(); // deterministic ordering, same rationale as WatchlistStore
        instrumentSymbols
            .into_iter()
            .filter_map(|instrumentSymbol| self.currentSnapshotForInstrument(instrumentSymbol))
            .collect()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn bidDelta(priceInMinorUnits: i64, quantity: u64) -> PriceLevelDeltaUpdate {
        PriceLevelDeltaUpdate {
            instrumentSymbol: "DEMO-EQ".to_string(),
            isBidSide: true,
            priceInMinorUnits,
            newTotalQuantityAtPrice: quantity,
        }
    }

    fn askDelta(priceInMinorUnits: i64, quantity: u64) -> PriceLevelDeltaUpdate {
        PriceLevelDeltaUpdate {
            instrumentSymbol: "DEMO-EQ".to_string(),
            isBidSide: false,
            priceInMinorUnits,
            newTotalQuantityAtPrice: quantity,
        }
    }

    #[test]
    fn bestBidIsTheHighestPricedBidLevelAndBestAskTheLowestPricedAskLevel() {
        let mut publisher = L1QuotePublisher::newPublisherWithNoSinks();
        let update = publisher.applyDepthPublishForInstrument(
            "DEMO-EQ",
            &[bidDelta(99, 10), bidDelta(98, 20), askDelta(101, 5), askDelta(102, 7)],
            None,
        );

        assert_eq!(update.quote.bestBidPriceInMinorUnits, Some(99));
        assert_eq!(update.quote.bestBidQuantity, 10);
        assert_eq!(update.quote.bestAskPriceInMinorUnits, Some(101));
        assert_eq!(update.quote.bestAskQuantity, 5);
    }

    #[test]
    fn levelsWithZeroQuantityAreExcludedFromBestBidAskSelection() {
        let mut publisher = L1QuotePublisher::newPublisherWithNoSinks();
        let update = publisher.applyDepthPublishForInstrument("DEMO-EQ", &[bidDelta(99, 0), bidDelta(97, 5)], None);

        assert_eq!(update.quote.bestBidPriceInMinorUnits, Some(97));
    }

    #[test]
    fn lastTradePriceCarriesForwardWhenAPublishHasNoNewTrade() {
        let mut publisher = L1QuotePublisher::newPublisherWithNoSinks();
        publisher.applyDepthPublishForInstrument("DEMO-EQ", &[bidDelta(99, 10)], Some(100));
        let secondUpdate = publisher.applyDepthPublishForInstrument("DEMO-EQ", &[bidDelta(98, 5)], None);

        assert_eq!(secondUpdate.quote.lastTradePriceInMinorUnits, Some(100));
    }

    #[test]
    fn lastTradePriceUpdatesWhenANewTradeIsSupplied() {
        let mut publisher = L1QuotePublisher::newPublisherWithNoSinks();
        publisher.applyDepthPublishForInstrument("DEMO-EQ", &[bidDelta(99, 10)], Some(100));
        let secondUpdate = publisher.applyDepthPublishForInstrument("DEMO-EQ", &[bidDelta(98, 5)], Some(105));

        assert_eq!(secondUpdate.quote.lastTradePriceInMinorUnits, Some(105));
    }

    #[test]
    fn sequenceNumberIncrementsIndependentlyPerInstrument() {
        let mut publisher = L1QuotePublisher::newPublisherWithNoSinks();
        let aaplFirst = publisher.applyDepthPublishForInstrument("AAPL", &[], None);
        let msftFirst = publisher.applyDepthPublishForInstrument("MSFT", &[], None);
        let aaplSecond = publisher.applyDepthPublishForInstrument("AAPL", &[], None);

        assert_eq!(aaplFirst.perInstrumentSequenceNumber, 1);
        assert_eq!(msftFirst.perInstrumentSequenceNumber, 1);
        assert_eq!(aaplSecond.perInstrumentSequenceNumber, 2);
    }

    #[test]
    fn everyRegisteredSinkReceivesEveryUpdate() {
        use std::sync::Arc;
        use std::sync::atomic::{AtomicUsize, Ordering};

        let firstSinkCallCount = Arc::new(AtomicUsize::new(0));
        let secondSinkCallCount = Arc::new(AtomicUsize::new(0));
        let firstSinkCallCountForClosure = Arc::clone(&firstSinkCallCount);
        let secondSinkCallCountForClosure = Arc::clone(&secondSinkCallCount);

        let mut publisher = L1QuotePublisher::newPublisherWithNoSinks();
        publisher.registerDownstreamSink(move |_update| {
            firstSinkCallCountForClosure.fetch_add(1, Ordering::SeqCst);
        });
        publisher.registerDownstreamSink(move |_update| {
            secondSinkCallCountForClosure.fetch_add(1, Ordering::SeqCst);
        });

        publisher.applyDepthPublishForInstrument("DEMO-EQ", &[], None);

        assert_eq!(firstSinkCallCount.load(Ordering::SeqCst), 1);
        assert_eq!(secondSinkCallCount.load(Ordering::SeqCst), 1);
    }

    #[test]
    fn snapshotForUnknownInstrumentIsNone() {
        let publisher = L1QuotePublisher::newPublisherWithNoSinks();
        assert!(publisher.currentSnapshotForInstrument("NOPE").is_none());
    }

    #[test]
    fn snapshotForKnownInstrumentReflectsTheMostRecentUpdate() {
        let mut publisher = L1QuotePublisher::newPublisherWithNoSinks();
        publisher.applyDepthPublishForInstrument("DEMO-EQ", &[bidDelta(99, 10)], Some(100));
        publisher.applyDepthPublishForInstrument("DEMO-EQ", &[bidDelta(97, 3)], None);

        let snapshot = publisher
            .currentSnapshotForInstrument("DEMO-EQ")
            .expect("should have a snapshot by now");
        assert_eq!(snapshot.perInstrumentSequenceNumber, 2);
        assert_eq!(snapshot.quote.bestBidPriceInMinorUnits, Some(97));
        assert_eq!(snapshot.quote.lastTradePriceInMinorUnits, Some(100));
    }

    #[test]
    fn allInstrumentSnapshotsAreSortedBySymbolForDeterministicOrdering() {
        let mut publisher = L1QuotePublisher::newPublisherWithNoSinks();
        publisher.applyDepthPublishForInstrument("MSFT", &[], None);
        publisher.applyDepthPublishForInstrument("AAPL", &[], None);

        let snapshots = publisher.currentSnapshotsForAllInstruments();
        assert_eq!(snapshots.len(), 2);
        assert_eq!(snapshots[0].instrumentSymbol, "AAPL");
        assert_eq!(snapshots[1].instrumentSymbol, "MSFT");
    }
}
