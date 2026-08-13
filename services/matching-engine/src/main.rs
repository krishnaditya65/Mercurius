// Mercurius / matching-engine
//
// Tier 0 component per ARCHITECTURE.md §3. Real single-instrument
// price-time-priority order book (orderBookCore.rs), now reachable over a
// TCP+JSON bridge (wireProtocol.rs) from oms-gateway — see that module's
// header comment for exactly what's a placeholder here vs. the real
// lock-free ring-buffer / SBE design ARCHITECTURE.md describes.
//
// Naming convention: long, descriptive camelCase identifiers throughout —
// this intentionally overrides Rust's default snake_case style, per
// project convention (see the mercurius-naming-convention memory).
#![allow(non_snake_case)]

mod orderBookCore;
mod orderTypes;
mod wireProtocol;

use std::io::{BufRead, BufReader, Write};
use std::net::{TcpListener, TcpStream};
use std::time::Duration;

use orderBookCore::OrderBookCore;
use orderTypes::TradeExecutionEvent;
use wireProtocol::{
    IncomingOrderWireRequest, OrderSubmissionWireResponse, OutgoingDepthPublishWireMessage, TradeExecutionWireEvent,
};

const TRADED_INSTRUMENT_SYMBOL: &str = "DEMO-EQ";
const MATCHING_ENGINE_TCP_LISTEN_ADDRESS: &str = "127.0.0.1:9101";
const MARKET_DATA_TCP_ADDRESS: &str = "127.0.0.1:9102";

fn main() {
    println!(
        "matching-engine listening on {MATCHING_ENGINE_TCP_LISTEN_ADDRESS}, trading {TRADED_INSTRUMENT_SYMBOL} \
         (TCP+JSON bridge — see wireProtocol.rs for what's a placeholder here)"
    );

    let mut orderBookForInstrument = OrderBookCore::newEmptyOrderBook(TRADED_INSTRUMENT_SYMBOL);

    let tcpListener = TcpListener::bind(MATCHING_ENGINE_TCP_LISTEN_ADDRESS)
        .expect("failed to bind matching-engine TCP listener");

    // Deliberately a sequential accept loop, not one thread per connection.
    // This is what actually preserves the single-writer principle
    // (ARCHITECTURE.md §3.1) here without needing a Mutex around the order
    // book: one order is fully processed, response written, and the
    // connection closed before the next connection is even accepted.
    for incomingConnection in tcpListener.incoming() {
        let Ok(mut connectionStream) = incomingConnection else {
            continue;
        };

        let mut requestLine = String::new();
        let lineReadResult = BufReader::new(&connectionStream).read_line(&mut requestLine);
        if lineReadResult.is_err() || requestLine.trim().is_empty() {
            continue;
        }

        let (responseToSend, tradeExecutionEventsForPublish) =
            handleOneIncomingOrderLine(&requestLine, &mut orderBookForInstrument);

        let mut responseJson =
            serde_json::to_string(&responseToSend).expect("OrderSubmissionWireResponse is always serializable");
        responseJson.push('\n');
        let _ = connectionStream.write_all(responseJson.as_bytes());

        publishBookDepthToMarketData(&orderBookForInstrument, &tradeExecutionEventsForPublish);
    }
}

/// Fire-and-forget publish of the current book depth (plus any trade ticks
/// this order just produced) to market-data. Deliberately tolerant of
/// market-data being unreachable — a Tier 0 component must never let a
/// Tier 1 consumer's availability affect order processing
/// (ARCHITECTURE.md §1's tenet on decoupling hot/cold paths). A short
/// connect timeout keeps a hung/unreachable market-data from stalling the
/// (already not-truly-hot-path-fast) accept loop for long.
fn publishBookDepthToMarketData(orderBookForInstrument: &OrderBookCore, tradeExecutionEvents: &[TradeExecutionEvent]) {
    let depthPublishMessage = OutgoingDepthPublishWireMessage::fromDepthSnapshotAndTrades(
        TRADED_INSTRUMENT_SYMBOL,
        orderBookForInstrument.currentBookDepthSnapshot(),
        tradeExecutionEvents,
    );

    let Ok(messageJson) = serde_json::to_string(&depthPublishMessage) else {
        return;
    };

    let connectAddress = MARKET_DATA_TCP_ADDRESS
        .parse()
        .expect("MARKET_DATA_TCP_ADDRESS is a valid socket address literal");
    let Ok(mut marketDataConnection) = TcpStream::connect_timeout(&connectAddress, Duration::from_millis(200)) else {
        // market-data isn't up — that's fine, orders keep processing.
        return;
    };

    let mut messageLine = messageJson;
    messageLine.push('\n');
    let _ = marketDataConnection.write_all(messageLine.as_bytes());
}

/// Returns both the response to send back to the caller AND the raw trade
/// events (if any) so the caller can also forward them to market-data as
/// trade ticks — cancels and status queries never produce trades, so they
/// return an empty `Vec` here.
fn handleOneIncomingOrderLine(
    requestLine: &str,
    orderBookForInstrument: &mut OrderBookCore,
) -> (OrderSubmissionWireResponse, Vec<TradeExecutionEvent>) {
    let parsedWireRequest: IncomingOrderWireRequest = match serde_json::from_str(requestLine) {
        Ok(wireRequest) => wireRequest,
        Err(parseError) => {
            return (
                OrderSubmissionWireResponse::errorResponse(format!("malformed order request: {parseError}")),
                Vec::new(),
            );
        }
    };

    if parsedWireRequest.instrumentSymbol != TRADED_INSTRUMENT_SYMBOL {
        return (
            OrderSubmissionWireResponse::errorResponse(format!(
                "matching-engine (skeleton) only trades {TRADED_INSTRUMENT_SYMBOL}, got {}",
                parsedWireRequest.instrumentSymbol
            )),
            Vec::new(),
        );
    }

    // A cancel request never reaches intoInternalOrderRequest() — it isn't
    // an order at all, it's an instruction to remove one that (maybe)
    // already exists.
    if let Some(orderSequenceNumberToCancel) = parsedWireRequest.cancelOrderSequenceNumber {
        let wasOrderCancelled = orderBookForInstrument.cancelOrder(orderSequenceNumberToCancel);
        return (OrderSubmissionWireResponse::cancelResponse(wasOrderCancelled), Vec::new());
    }

    // Same shape: a status query is read-only and never becomes an
    // internal order either.
    if let Some(orderSequenceNumberToQuery) = parsedWireRequest.queryOrderStatusSequenceNumber {
        let queryResult = orderBookForInstrument.queryOrderStatus(orderSequenceNumberToQuery);
        return (OrderSubmissionWireResponse::statusResponse(queryResult), Vec::new());
    }

    let submissionOutcome = orderBookForInstrument.submitIncomingOrder(parsedWireRequest.intoInternalOrderRequest());
    let responseToSend = OrderSubmissionWireResponse::successResponse(
        submissionOutcome
            .tradeExecutionEvents
            .iter()
            .map(TradeExecutionWireEvent::from)
            .collect(),
        submissionOutcome.assignedOrderSequenceNumber,
    );
    (responseToSend, submissionOutcome.tradeExecutionEvents)
}
