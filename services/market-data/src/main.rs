// Mercurius / market-data
//
// Tier 1 component per ARCHITECTURE.md §5. Now ingests REAL depth AND
// trade-tick publishes from matching-engine over TCP+JSON
// (ingestionWireProtocol.rs) instead of only driving DeltaPublisher with
// hardcoded demo data. Fan-out is still an in-process println sink, not a
// real Kafka/Redpanda producer + WS fan-out fleet — see deltaPublisher.rs
// for what that TODO covers. Trade ticks additionally feed
// candleAggregator.rs (queryable over a small hand-rolled HTTP server,
// httpQueryServer.rs, so a charting UI has something to poll) AND
// pricealerts.rs — every real trade tick is checked against every
// account's not-yet-triggered price alerts (FEATURES.md §9).
#![allow(non_snake_case)]

mod candleAggregator;
mod deltaPublisher;
mod httpQueryServer;
mod ingestionWireProtocol;
mod marketDataEventTypes;
mod pricealerts;
mod watchlist;

use std::io::{BufRead, BufReader};
use std::net::TcpListener;
use std::sync::Arc;
use std::thread;
use std::time::{SystemTime, UNIX_EPOCH};

use deltaPublisher::DeltaPublisher;
use httpQueryServer::SharedMarketDataState;
use ingestionWireProtocol::IncomingDepthPublishWireMessage;

const MARKET_DATA_INGESTION_TCP_LISTEN_ADDRESS: &str = "127.0.0.1:9102";
const MARKET_DATA_HTTP_QUERY_LISTEN_ADDRESS: &str = "127.0.0.1:9103";

fn main() {
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

    let mut deltaPublisher = DeltaPublisher::newPublisherWithNoSinks();

    // TODO(real build): register a real Kafka/Redpanda producer sink here
    // (ARCHITECTURE.md §5) instead of a println closure. The WS fan-out
    // fleet then consumes from Kafka independently — it should never be a
    // direct sink of the matching engine's output.
    deltaPublisher.registerDownstreamSink(|sequencedMessage| {
        println!(
            "[sink: simulated-ws-fanout] {} seq={} deltas={}",
            sequencedMessage.instrumentSymbol,
            sequencedMessage.perInstrumentSequenceNumber,
            sequencedMessage.deltaUpdatesInThisMessage.len()
        );
    });

    let tcpListener = TcpListener::bind(MARKET_DATA_INGESTION_TCP_LISTEN_ADDRESS)
        .expect("failed to bind market-data ingestion TCP listener");

    // Sequential accept loop, single thread — same style as
    // matching-engine's bridge, and sufficient since there's only one
    // publisher (one matching-engine instance) today. Unlike
    // matching-engine, nothing here has a single-writer *requirement* —
    // this could become multi-threaded later without correctness issues,
    // since DeltaPublisher's own per-instrument sequence counters are
    // what actually needs protecting once there's real concurrency.
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
                let instrumentSymbol = depthPublishMessage.instrumentSymbol.clone();
                let internalDeltaUpdates = depthPublishMessage.intoInternalDeltaUpdates();

                if !depthPublishMessage.tradeTicks.is_empty() {
                    let executedAtEpochSeconds = currentEpochSeconds();
                    {
                        let mut candleAggregator =
                            sharedState.candleAggregator.lock().expect("candle aggregator mutex poisoned");
                        for tradeTick in &depthPublishMessage.tradeTicks {
                            candleAggregator.recordTrade(
                                &instrumentSymbol,
                                tradeTick.executedPriceInMinorUnits,
                                tradeTick.executedQuantity,
                                executedAtEpochSeconds,
                            );
                        }
                    }
                    // Every real trade tick is checked against every
                    // not-yet-triggered price alert for this instrument —
                    // alerts fire off the actual live trade stream, not a
                    // polling loop re-checking the latest price on a timer.
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

                deltaPublisher.publishDeltaBatchForInstrument(&instrumentSymbol, internalDeltaUpdates);
            }
            Err(parseError) => {
                eprintln!("dropped malformed depth publish message: {parseError}");
            }
        }
    }
}

fn currentEpochSeconds() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|duration| duration.as_secs())
        .unwrap_or(0)
}
