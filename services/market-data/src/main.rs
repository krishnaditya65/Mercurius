// Mercurius / market-data
//
// Tier 1 component per ARCHITECTURE.md §5. Ingests REAL depth AND
// trade-tick publishes from matching-engine over TCP+JSON
// (ingestionWireProtocol.rs), OR a deterministic simulated/sandbox feed
// (simulatedExchangeFeedGenerator.rs, FEATURES.md §8 [P1]) when
// MARKET_DATA_SIMULATED_FEED_ENABLED=true — both flow through the exact
// same `ingestDepthPublishMessage` function below, so nothing downstream
// (candles, L1 quotes, columnar tick storage, UDP multicast) needs to
// know or care which source produced a given message. Fan-out is still an
// in-process println sink, not a real Kafka/Redpanda producer + WS
// fan-out fleet — see deltaPublisher.rs for what that TODO covers. Trade
// ticks additionally feed candleAggregator.rs (queryable over a small
// hand-rolled HTTP server, httpQueryServer.rs), columnarTickStore.rs
// (FEATURES.md §8 [P3], also queryable over HTTP), and pricealerts.rs —
// every real trade tick is checked against every account's
// not-yet-triggered price alerts (FEATURES.md §9). L1 quotes and trade
// ticks are additionally fanned out over real UDP multicast
// (udpMulticastPublisher.rs, FEATURES.md §8 [P4]).
#![allow(non_snake_case)]

mod candleAggregator;
mod columnarTickStore;
mod deltaPublisher;
mod httpQueryServer;
mod ingestionWireProtocol;
mod l1QuotePublisher;
mod l1QuoteWebSocketServer;
mod l1QuoteWireProtocol;
mod marketDataEventTypes;
mod orderFlowFootprintAggregator;
mod pricealerts;
mod simulatedExchangeFeedGenerator;
mod udpMulticastPublisher;
mod volumeProfileAggregator;
mod watchlist;

use std::io::{BufRead, BufReader};
use std::net::TcpListener;
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use deltaPublisher::DeltaPublisher;
use httpQueryServer::SharedMarketDataState;
use ingestionWireProtocol::IncomingDepthPublishWireMessage;
use l1QuotePublisher::L1QuotePublisher;
use simulatedExchangeFeedGenerator::{SimulatedExchangeFeedGenerator, SimulatedSymbolConfig};
use udpMulticastPublisher::UdpMulticastPublisher;

const MARKET_DATA_INGESTION_TCP_LISTEN_ADDRESS: &str = "127.0.0.1:9102";
const MARKET_DATA_HTTP_QUERY_LISTEN_ADDRESS: &str = "127.0.0.1:9103";
const MARKET_DATA_L1_QUOTE_WS_LISTEN_ADDRESS: &str = "127.0.0.1:9104";

/// How many not-yet-delivered updates a slow WS client is allowed to
/// fall behind by before it's told to lag (see
/// `l1QuoteWebSocketServer.rs`'s handling of
/// `broadcast::error::RecvError::Lagged`) and resent a fresh snapshot.
/// Generous for a skeleton — real capacity planning belongs to the real
/// Kafka/fan-out fleet this in-process channel stands in for
/// (ARCHITECTURE.md §5).
const L1_QUOTE_BROADCAST_CHANNEL_CAPACITY: usize = 1024;

/// Default synthetic symbols the simulated feed drives when
/// `MARKET_DATA_SIMULATED_FEED_ENABLED=true` and no more specific
/// configuration is provided. Deliberately small and fixed — this is a
/// Phase 0-1 demo/sandbox feed, not a configurable market simulator.
fn defaultSimulatedSymbolConfigs() -> Vec<SimulatedSymbolConfig> {
    vec![
        SimulatedSymbolConfig {
            instrumentSymbol: "DEMO-EQ".to_string(),
            startingPriceInMinorUnits: 10_000,
            driftInMinorUnitsPerTick: 0,
            volatilityInMinorUnits: 15,
        },
        SimulatedSymbolConfig {
            instrumentSymbol: "SIM-AAPL".to_string(),
            startingPriceInMinorUnits: 19_000,
            driftInMinorUnitsPerTick: 1,
            volatilityInMinorUnits: 25,
        },
    ]
}

fn main() {
    let simulatedFeedEnabled = readBoolEnvVar("MARKET_DATA_SIMULATED_FEED_ENABLED", false);
    let simulatedFeedSeed = readU64EnvVar("MARKET_DATA_SIMULATED_FEED_SEED", 1);
    let simulatedFeedTickIntervalMillis = readU64EnvVar("MARKET_DATA_SIMULATED_FEED_TICK_INTERVAL_MILLIS", 250);
    let udpMulticastEnabled = readBoolEnvVar("MARKET_DATA_UDP_MULTICAST_ENABLED", true);
    let udpMulticastGroupAddress = readStringEnvVar(
        "MARKET_DATA_UDP_MULTICAST_GROUP_ADDRESS",
        udpMulticastPublisher::DEFAULT_MULTICAST_GROUP_ADDRESS,
    );

    println!(
        "market-data listening on {MARKET_DATA_INGESTION_TCP_LISTEN_ADDRESS} for depth publishes from matching-engine"
    );

    let sharedState = Arc::new(SharedMarketDataState::newEmptyState());

    // The HTTP query server runs on its own thread — it only ever touches
    // sharedState behind its own internal mutexes, so it can't violate
    // the ingestion loop's ordering guarantees. Unlike matching-engine's
    // accept loop, there's no single-writer requirement here to protect.
    let httpServerStateHandle = Arc::clone(&sharedState);
    thread::spawn(move || {
        httpQueryServer::runHttpQueryServer(MARKET_DATA_HTTP_QUERY_LISTEN_ADDRESS, httpServerStateHandle);
    });

    // Wrapped in Arc<Mutex<_>> (unlike the earlier plain-owned version)
    // because it can now be driven from up to two producer threads: the
    // real TCP ingestion accept loop below, AND the simulated feed's own
    // thread when MARKET_DATA_SIMULATED_FEED_ENABLED=true — both call the
    // exact same `ingestDepthPublishMessage` function.
    let deltaPublisher = Arc::new(Mutex::new(DeltaPublisher::newPublisherWithNoSinks()));
    // TODO(real build): register a real Kafka/Redpanda producer sink here
    // (ARCHITECTURE.md §5) instead of a println closure. The WS fan-out
    // fleet then consumes from Kafka independently — it should never be a
    // direct sink of the matching engine's output.
    deltaPublisher
        .lock()
        .expect("delta publisher mutex poisoned")
        .registerDownstreamSink(|sequencedMessage| {
            println!(
                "[sink: simulated-ws-fanout] {} seq={} deltas={}",
                sequencedMessage.instrumentSymbol,
                sequencedMessage.perInstrumentSequenceNumber,
                sequencedMessage.deltaUpdatesInThisMessage.len()
            );
        });

    // A REAL UDP multicast publisher (FEATURES.md §8 [P4]) — joins/sends
    // to an actual multicast group via std::net::UdpSocket. Constructing
    // it can fail (e.g. an invalid configured group address, or a
    // sandboxed environment that disallows multicast sockets); that's
    // treated as "feature unavailable, keep running" rather than a fatal
    // error, since UDP fan-out is an additive capability, not something
    // anything else in this service depends on.
    let udpMulticastPublisher: Option<Arc<UdpMulticastPublisher>> = if udpMulticastEnabled {
        match UdpMulticastPublisher::newPublisherForGroupAddress(&udpMulticastGroupAddress) {
            Ok(publisher) => {
                println!("market-data UDP multicast fan-out publishing to {udpMulticastGroupAddress}");
                Some(Arc::new(publisher))
            }
            Err(bindError) => {
                eprintln!("market-data UDP multicast fan-out disabled — failed to construct publisher: {bindError}");
                None
            }
        }
    } else {
        None
    };

    // The L1 (top-of-book) quote feed — a REAL WebSocket push, unlike
    // the depth-delta sink above. `l1QuotePublisher` derives best bid/
    // ask/last-trade from the exact same real depth publishes as
    // `deltaPublisher`, and every update it produces is fanned out to
    // `l1BroadcastSender` (every connected WS client subscribes to it
    // independently, `l1QuoteWebSocketServer.rs`) AND, if enabled, to the
    // UDP multicast publisher above. It's wrapped in `Arc<Mutex<_>>`
    // because the WS server thread also needs read access to it to
    // answer SNAPSHOT/RESYNC_REQUEST queries.
    let mut l1QuotePublisher = L1QuotePublisher::newPublisherWithNoSinks();
    let (l1BroadcastSender, _l1BroadcastReceiverTemplate) =
        tokio::sync::broadcast::channel(L1_QUOTE_BROADCAST_CHANNEL_CAPACITY);
    let l1BroadcastSenderForSink = l1BroadcastSender.clone();
    l1QuotePublisher.registerDownstreamSink(move |sequencedUpdate| {
        // A send error here just means there are currently zero
        // subscribed WS clients — not a failure, since the broadcast
        // channel has no persistent backlog for late joiners (that's
        // exactly why every new connection gets an explicit SNAPSHOT
        // instead of relying on the channel to replay history).
        let _ = l1BroadcastSenderForSink.send(sequencedUpdate.clone());
    });
    if let Some(udpMulticastPublisherForL1Sink) = udpMulticastPublisher.clone() {
        l1QuotePublisher.registerDownstreamSink(move |sequencedUpdate| {
            // Best-effort — a dropped/failed UDP send is normal for an
            // unreliable transport and not worth logging on every quote.
            let _ = udpMulticastPublisherForL1Sink.publishL1Quote(
                &sequencedUpdate.instrumentSymbol,
                sequencedUpdate.perInstrumentSequenceNumber,
                sequencedUpdate.quote.bestBidPriceInMinorUnits,
                sequencedUpdate.quote.bestBidQuantity,
                sequencedUpdate.quote.bestAskPriceInMinorUnits,
                sequencedUpdate.quote.bestAskQuantity,
                sequencedUpdate.quote.lastTradePriceInMinorUnits,
            );
        });
    }
    let sharedL1QuotePublisher = Arc::new(Mutex::new(l1QuotePublisher));

    let l1QuoteWebSocketServerStateHandle = Arc::clone(&sharedL1QuotePublisher);
    let l1BroadcastSenderForWsServer = l1BroadcastSender.clone();
    thread::spawn(move || {
        l1QuoteWebSocketServer::runL1QuoteWebSocketServer(
            MARKET_DATA_L1_QUOTE_WS_LISTEN_ADDRESS,
            l1QuoteWebSocketServerStateHandle,
            l1BroadcastSenderForWsServer,
        );
    });

    // The simulated/sandbox exchange feed (FEATURES.md §8 [P1]) — off by
    // default, opt-in via MARKET_DATA_SIMULATED_FEED_ENABLED=true. Runs on
    // its own thread, generating a deterministic (seeded) synthetic tick
    // stream and feeding it into `ingestDepthPublishMessage`, the EXACT
    // SAME function the real TCP ingestion loop below calls — so this
    // service can run and demo/test fully standalone, without
    // matching-engine running at all, and everything downstream (candles,
    // L1 WS feed, columnar tick storage, UDP multicast) behaves
    // identically either way.
    if simulatedFeedEnabled {
        println!(
            "market-data simulated exchange feed ENABLED (seed={simulatedFeedSeed}, \
             tickIntervalMillis={simulatedFeedTickIntervalMillis}) — matching-engine is NOT required"
        );
        let simulatedFeedSharedState = Arc::clone(&sharedState);
        let simulatedFeedDeltaPublisher = Arc::clone(&deltaPublisher);
        let simulatedFeedL1QuotePublisher = Arc::clone(&sharedL1QuotePublisher);
        let simulatedFeedUdpMulticastPublisher = udpMulticastPublisher.clone();
        thread::spawn(move || {
            let mut generator = SimulatedExchangeFeedGenerator::newGeneratorWithSeed(
                simulatedFeedSeed,
                defaultSimulatedSymbolConfigs(),
            );
            let symbolCount = generator.symbolCount();
            loop {
                for symbolIndex in 0..symbolCount {
                    let simulatedTick = generator.nextTickForSymbolIndex(symbolIndex);
                    ingestDepthPublishMessage(
                        simulatedTick.intoDepthPublishWireMessage(),
                        &simulatedFeedSharedState,
                        &simulatedFeedDeltaPublisher,
                        &simulatedFeedL1QuotePublisher,
                        simulatedFeedUdpMulticastPublisher.as_deref(),
                    );
                }
                thread::sleep(Duration::from_millis(simulatedFeedTickIntervalMillis));
            }
        });
    }

    let tcpListener = TcpListener::bind(MARKET_DATA_INGESTION_TCP_LISTEN_ADDRESS)
        .expect("failed to bind market-data ingestion TCP listener");

    // Sequential accept loop, single thread — same style as
    // matching-engine's bridge, and sufficient since there's only one
    // real publisher (one matching-engine instance) today. Unlike
    // matching-engine, nothing here has a single-writer *requirement* —
    // this could become multi-threaded later without correctness issues,
    // since DeltaPublisher's own per-instrument sequence counters (now
    // behind a Mutex, shared with the simulated feed thread) are what
    // actually needs protecting once there's real concurrency.
    for incomingConnection in tcpListener.incoming() {
        let Ok(connectionStream) = incomingConnection else {
            continue;
        };

        let mut requestLine = String::new();
        if BufReader::new(&connectionStream).read_line(&mut requestLine).is_err() {
            continue;
        }
        if requestLine.trim().is_empty() {
            continue;
        }

        match serde_json::from_str::<IncomingDepthPublishWireMessage>(&requestLine) {
            Ok(depthPublishMessage) => {
                ingestDepthPublishMessage(
                    depthPublishMessage,
                    &sharedState,
                    &deltaPublisher,
                    &sharedL1QuotePublisher,
                    udpMulticastPublisher.as_deref(),
                );
            }
            Err(parseError) => {
                eprintln!("dropped malformed depth publish message: {parseError}");
            }
        }
    }
}

/// The single ingestion pipeline both the real matching-engine TCP feed
/// AND the simulated/sandbox feed (`simulatedExchangeFeedGenerator.rs`,
/// FEATURES.md §8 [P1]) funnel every message through. Deliberately a
/// free function (not a method on some "ingestion" struct) so it's
/// trivially callable from either the TCP accept loop or the simulated
/// feed's own thread with the exact same behavior — folding trade ticks
/// into `candleAggregator` and `columnarTickStore`, checking price
/// alerts, deriving the L1 quote, fanning both trade ticks and L1 quotes
/// out over UDP multicast (if enabled), and publishing book deltas.
fn ingestDepthPublishMessage(
    depthPublishMessage: IncomingDepthPublishWireMessage,
    sharedState: &Arc<SharedMarketDataState>,
    deltaPublisher: &Arc<Mutex<DeltaPublisher>>,
    sharedL1QuotePublisher: &Arc<Mutex<L1QuotePublisher>>,
    udpMulticastPublisher: Option<&UdpMulticastPublisher>,
) {
    let instrumentSymbol = depthPublishMessage.instrumentSymbol.clone();
    let internalDeltaUpdates = depthPublishMessage.intoInternalDeltaUpdates();

    if !depthPublishMessage.tradeTicks.is_empty() {
        let executedAtEpochSeconds = currentEpochSeconds();
        {
            let mut candleAggregator = sharedState
                .candleAggregator
                .lock()
                .expect("candle aggregator mutex poisoned");
            for tradeTick in &depthPublishMessage.tradeTicks {
                candleAggregator.recordTradeWithAggressorSide(
                    &instrumentSymbol,
                    tradeTick.executedPriceInMinorUnits,
                    tradeTick.executedQuantity,
                    executedAtEpochSeconds,
                    tradeTick.isBuyAggressor,
                );
            }
        }
        // Every real (or simulated) trade tick also gets appended to the
        // columnar (struct-of-arrays) tick store for replay/backtest
        // range queries — FEATURES.md §8 [P3] — including the real
        // aggressor side (FEATURES.md §20 "Order-flow footprint charts"),
        // which `volumeProfileAggregator.rs`/`orderFlowFootprintAggregator.rs`
        // read straight back off this same store. Same ingestion call
        // site as the candle aggregator above; no separate/parallel path.
        for tradeTick in &depthPublishMessage.tradeTicks {
            sharedState.columnarTickStore.appendTickWithAggressorSide(
                &instrumentSymbol,
                executedAtEpochSeconds,
                tradeTick.executedPriceInMinorUnits,
                tradeTick.executedQuantity,
                tradeTick.isBuyAggressor,
            );
            if let Some(udpMulticastPublisher) = udpMulticastPublisher {
                let _ = udpMulticastPublisher.publishTradeTick(
                    &instrumentSymbol,
                    executedAtEpochSeconds,
                    tradeTick.executedPriceInMinorUnits,
                    tradeTick.executedQuantity,
                );
            }
        }
        // Every real trade tick is checked against every not-yet-
        // triggered price alert for this instrument — alerts fire off
        // the actual live trade stream, not a polling loop re-checking
        // the latest price on a timer.
        for tradeTick in &depthPublishMessage.tradeTicks {
            let newlyTriggeredAlertIds = sharedState.priceAlerts.checkAndTriggerAlertsForTrade(
                &instrumentSymbol,
                tradeTick.executedPriceInMinorUnits,
                executedAtEpochSeconds,
            );
            if !newlyTriggeredAlertIds.is_empty() {
                println!(
                    "price alert(s) triggered for {instrumentSymbol} at {}: {:?}",
                    tradeTick.executedPriceInMinorUnits, newlyTriggeredAlertIds
                );
            }
        }
    }

    // The most recent trade price in THIS publish, if any — what
    // l1QuotePublisher folds in as the new last-traded price.
    // executedAtEpochSeconds was already stamped above for the candle
    // aggregator/price alerts/columnar store; the L1 feed only needs the
    // price itself.
    let latestTradePriceInThisPublish = depthPublishMessage
        .tradeTicks
        .last()
        .map(|tradeTick| tradeTick.executedPriceInMinorUnits);
    {
        let mut l1QuotePublisher = sharedL1QuotePublisher
            .lock()
            .expect("l1 quote publisher mutex poisoned");
        l1QuotePublisher.applyDepthPublishForInstrument(
            &instrumentSymbol,
            &internalDeltaUpdates,
            latestTradePriceInThisPublish,
        );
    }

    deltaPublisher
        .lock()
        .expect("delta publisher mutex poisoned")
        .publishDeltaBatchForInstrument(&instrumentSymbol, internalDeltaUpdates);
}

fn currentEpochSeconds() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|duration| duration.as_secs())
        .unwrap_or(0)
}

fn readBoolEnvVar(name: &str, defaultValue: bool) -> bool {
    match std::env::var(name) {
        Ok(value) => matches!(value.trim().to_ascii_lowercase().as_str(), "1" | "true" | "yes"),
        Err(_) => defaultValue,
    }
}

fn readU64EnvVar(name: &str, defaultValue: u64) -> u64 {
    std::env::var(name)
        .ok()
        .and_then(|value| value.trim().parse().ok())
        .unwrap_or(defaultValue)
}

fn readStringEnvVar(name: &str, defaultValue: &str) -> String {
    std::env::var(name).unwrap_or_else(|_| defaultValue.to_string())
}
