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
// This is a binary-only crate (no lib target), so several items exist
// purely for the test/audit surface — e.g. `OrderBookCore::
// fullBookStateSnapshotForTesting`, `deterministicReplayHarness`'s public
// helpers, `printCurrentBookDepth` — and are never called from `main()`
// itself, only from `#[cfg(test)]` modules elsewhere in the crate. `cargo
// build`'s dead-code analysis doesn't see into those, so it would
// otherwise warn about code that is, in fact, actively used by `cargo
// test`. Suppressed crate-wide rather than item-by-item, matching this
// crate's existing style of a small number of deliberate, documented,
// crate-root `#![allow(...)]`s.
#![allow(dead_code)]

mod deterministicReplayHarness;
mod lockFreeSpscRingBuffer;
mod orderBookCore;
mod orderTypes;
mod walBackedOrderBook;
mod wireProtocol;
mod writeAheadLog;

use std::io::{BufRead, BufReader, Write};
use std::net::{TcpListener, TcpStream};
use std::path::Path;
use std::thread;
use std::time::Duration;

use lockFreeSpscRingBuffer::{
    RingBufferConsumerHandle, RingBufferProducerHandle, splitIntoSpscProducerConsumerHandles,
};
use orderTypes::TradeExecutionEvent;
use walBackedOrderBook::WalBackedOrderBook;
use wireProtocol::{
    IncomingOrderWireRequest, OrderSubmissionWireResponse, OutgoingDepthPublishWireMessage,
    TradeExecutionWireEvent,
};

const TRADED_INSTRUMENT_SYMBOL: &str = "DEMO-EQ";
const MATCHING_ENGINE_TCP_LISTEN_ADDRESS: &str = "127.0.0.1:9101";
const MARKET_DATA_TCP_ADDRESS: &str = "127.0.0.1:9102";
/// Default WAL file path, relative to the process's working directory.
/// Overridable via the `MATCHING_ENGINE_WAL_FILE_PATH` env var (used by
/// the live-verification run so it doesn't clobber this default file, and
/// generally useful for running more than one instance side by side).
const DEFAULT_WRITE_AHEAD_LOG_FILE_PATH: &str = "matchingEngineWriteAheadLog.jsonl";
/// Capacity (slots, must be a power of two) of each of the two SPSC ring
/// buffers wired up in `main()`. The network thread never has more than
/// one request genuinely in flight at a time (it blocks on the egress ring
/// buffer for a response before accepting the next connection — see the
/// comment on the accept loop below), so this is generous headroom, not a
/// tightly-tuned value.
const RING_BUFFER_CAPACITY: usize = 1024;

/// One line read off the wire by the network thread, handed to the
/// matching-core thread through the ingress ring buffer.
/// `requestSequenceId` isn't load-bearing for correctness today (the
/// network thread never has more than one request in flight — see
/// `RING_BUFFER_CAPACITY`'s doc comment — so FIFO ordering alone already
/// guarantees the egress item the network thread pops back is the response
/// to the ingress item it just pushed), but is included as a real
/// correlation id and cross-checked with a `debug_assert_eq!` at the pop
/// site, both for defense-in-depth and so a future pipelined network
/// thread (accepting connection N+1 before connection N's response comes
/// back) would already have a correlation mechanism to build on.
struct IngressRingBufferItem {
    requestSequenceId: u64,
    requestLine: String,
}

/// The matching-core thread's response to one `IngressRingBufferItem`,
/// handed back to the network thread through the egress ring buffer.
struct EgressRingBufferItem {
    requestSequenceId: u64,
    /// Already-serialized `OrderSubmissionWireResponse` JSON (without the
    /// trailing newline) to write back to the connection that sent this
    /// request.
    responseJson: String,
    /// Already-serialized `OutgoingDepthPublishWireMessage` JSON to
    /// fire-and-forget at market-data, or `None` if it failed to
    /// serialize (mirrors the previous single-threaded code's `let Ok(..)
    /// = .. else { return }` short-circuit — see
    /// `buildMarketDataPublishJson`). Computed on the matching-core thread
    /// (which owns the order book) so the network thread never needs a
    /// reference back into book state.
    marketDataPublishJson: Option<String>,
}

fn main() {
    let commandLineArguments: Vec<String> = std::env::args().collect();
    if let Some(walFilePathToReplay) = commandLineArguments
        .get(1)
        .filter(|firstArgument| firstArgument.as_str() == "--replay")
        .and(commandLineArguments.get(2))
    {
        runReplayModeAndExit(walFilePathToReplay);
    }

    let writeAheadLogFilePath = std::env::var("MATCHING_ENGINE_WAL_FILE_PATH")
        .unwrap_or_else(|_| DEFAULT_WRITE_AHEAD_LOG_FILE_PATH.to_string());

    println!(
        "matching-engine listening on {MATCHING_ENGINE_TCP_LISTEN_ADDRESS}, trading {TRADED_INSTRUMENT_SYMBOL} \
         (TCP+JSON bridge — see wireProtocol.rs for what's a placeholder here). WAL file: {writeAheadLogFilePath}"
    );

    let orderBookForInstrument = WalBackedOrderBook::openRecoveringIfPresent(
        TRADED_INSTRUMENT_SYMBOL,
        Path::new(&writeAheadLogFilePath),
    )
    .expect(
        "failed to open/recover the write-ahead log — see writeAheadLog.rs/walBackedOrderBook.rs",
    );

    // FEATURES.md §9 "Lock-free ring buffer ingress/egress": two SPSC
    // ring buffers (`lockFreeSpscRingBuffer.rs`) replace what used to be a
    // single-threaded synchronous function call between "read the request
    // off the wire" and "match it against the book" — that whole
    // read/match/write sequence used to run inline in one loop on one
    // thread with no synchronization mechanism at all (there was nothing
    // to synchronize; it was one thread doing everything in order). Now
    // network I/O and order matching genuinely run on two different
    // threads, and `ingressRingBuffer`/`egressRingBuffer` are the ONLY
    // hand-off between them — no `Mutex`, no `std::sync::mpsc` channel.
    let (mut ingressProducer, ingressConsumer) =
        splitIntoSpscProducerConsumerHandles::<IngressRingBufferItem>(RING_BUFFER_CAPACITY);
    let (egressProducer, mut egressConsumer) =
        splitIntoSpscProducerConsumerHandles::<EgressRingBufferItem>(RING_BUFFER_CAPACITY);

    // The matching-core thread: the ONE thread that ever touches
    // `orderBookForInstrument` for the rest of the process's life,
    // preserving the single-writer principle (ARCHITECTURE.md §3.1) — now
    // enforced by the type system (the `WalBackedOrderBook` is moved into
    // this closure and never touched again on the thread that spawned it)
    // rather than by "the accept loop happens to be sequential."
    thread::Builder::new()
        .name("matching-core".to_string())
        .spawn(move || runMatchingCoreLoop(orderBookForInstrument, ingressConsumer, egressProducer))
        .expect("failed to spawn matching-core thread");

    let tcpListener = TcpListener::bind(MATCHING_ENGINE_TCP_LISTEN_ADDRESS)
        .expect("failed to bind matching-engine TCP listener");

    // The network thread (this one, `main`'s own thread): accepts
    // connections and does all socket I/O, but never touches the order
    // book directly any more — every request crosses over to the
    // matching-core thread via `ingressProducer.push`, and every response
    // comes back via `egressConsumer.pop`. Still deliberately a sequential
    // accept loop, not one thread per connection: one request is fully
    // round-tripped through the ring buffers and its response written
    // before the next connection is even accepted, which is what keeps at
    // most one request ever in flight through the ring buffers at a time.
    // (See `runReplayModeAndExit` below for the offline recovery-
    // inspection mode — `cargo run -- --replay <walFilePath>` — which
    // never reaches this loop, or spawns the matching-core thread, at
    // all.)
    let mut nextRequestSequenceId: u64 = 0;
    for incomingConnection in tcpListener.incoming() {
        let Ok(mut connectionStream) = incomingConnection else {
            continue;
        };

        let mut requestLine = String::new();
        let lineReadResult = BufReader::new(&connectionStream).read_line(&mut requestLine);
        if lineReadResult.is_err() || requestLine.trim().is_empty() {
            continue;
        }

        let requestSequenceId = nextRequestSequenceId;
        nextRequestSequenceId = nextRequestSequenceId.wrapping_add(1);

        ingressProducer.push(IngressRingBufferItem {
            requestSequenceId,
            requestLine,
        });

        let egressItem = egressConsumer.pop();
        debug_assert_eq!(
            egressItem.requestSequenceId, requestSequenceId,
            "ring-buffer FIFO order broken: egress item doesn't match the ingress item that \
             should have produced it"
        );

        let mut responseLine = egressItem.responseJson;
        responseLine.push('\n');
        let _ = connectionStream.write_all(responseLine.as_bytes());

        if let Some(marketDataPublishJson) = egressItem.marketDataPublishJson {
            sendMarketDataPublishMessage(&marketDataPublishJson);
        }
    }
}

/// The matching-core thread's entire body: drain the ingress ring buffer,
/// process each request against `orderBookForInstrument` exactly as the
/// single-threaded code used to (same `handleOneIncomingOrderLine`, same
/// WAL-before-acknowledge ordering, same price-time-priority matching —
/// none of that changed, only which thread it runs on), and push the
/// result onto the egress ring buffer. Never returns — this is the
/// process's entire order-processing lifetime once the network thread
/// starts feeding it.
fn runMatchingCoreLoop(
    mut orderBookForInstrument: WalBackedOrderBook,
    mut ingressConsumer: RingBufferConsumerHandle<IngressRingBufferItem>,
    mut egressProducer: RingBufferProducerHandle<EgressRingBufferItem>,
) -> ! {
    loop {
        let ingressItem = ingressConsumer.pop();

        let (responseToSend, tradeExecutionEventsForPublish) =
            handleOneIncomingOrderLine(&ingressItem.requestLine, &mut orderBookForInstrument);

        let responseJson = serde_json::to_string(&responseToSend)
            .expect("OrderSubmissionWireResponse is always serializable");

        // Mirrors the previous code's unconditional
        // `publishBookDepthToMarketData` call — a depth-plus-trade-ticks
        // publish message is built for EVERY request (cancels/queries/
        // errors just carry an empty trade list), not only successful
        // order submissions.
        let marketDataPublishJson =
            buildMarketDataPublishJson(&orderBookForInstrument, &tradeExecutionEventsForPublish);

        egressProducer.push(EgressRingBufferItem {
            requestSequenceId: ingressItem.requestSequenceId,
            responseJson,
            marketDataPublishJson,
        });
    }
}

/// Serializes the depth-plus-trade-ticks message that used to be built and
/// sent directly by `publishBookDepthToMarketData`. Now split from the
/// actual network send: this runs on the matching-core thread (which has
/// the book), the send itself runs on the network thread (via
/// `sendMarketDataPublishMessage`) — book access and socket I/O never
/// happen on the same thread any more.
fn buildMarketDataPublishJson(
    orderBookForInstrument: &WalBackedOrderBook,
    tradeExecutionEvents: &[TradeExecutionEvent],
) -> Option<String> {
    let depthPublishMessage = OutgoingDepthPublishWireMessage::fromDepthSnapshotAndTrades(
        TRADED_INSTRUMENT_SYMBOL,
        orderBookForInstrument.currentBookDepthSnapshot(),
        tradeExecutionEvents,
    );
    serde_json::to_string(&depthPublishMessage).ok()
}

/// Fire-and-forget publish of an already-serialized depth/trade-tick
/// message to market-data. Deliberately tolerant of market-data being
/// unreachable — a Tier 0 component must never let a Tier 1 consumer's
/// availability affect order processing (ARCHITECTURE.md §1's tenet on
/// decoupling hot/cold paths). A short connect timeout keeps a hung/
/// unreachable market-data from stalling the network thread for long. Runs
/// on the network thread, never the matching-core thread — see
/// `buildMarketDataPublishJson`.
fn sendMarketDataPublishMessage(messageJson: &str) {
    let connectAddress = MARKET_DATA_TCP_ADDRESS
        .parse()
        .expect("MARKET_DATA_TCP_ADDRESS is a valid socket address literal");
    let Ok(mut marketDataConnection) =
        TcpStream::connect_timeout(&connectAddress, Duration::from_millis(200))
    else {
        // market-data isn't up — that's fine, orders keep processing.
        return;
    };

    let mut messageLine = messageJson.to_string();
    messageLine.push('\n');
    let _ = marketDataConnection.write_all(messageLine.as_bytes());
}

/// Returns both the response to send back to the caller AND the raw trade
/// events (if any) so the caller can also forward them to market-data as
/// trade ticks — cancels and status queries never produce trades, so they
/// return an empty `Vec` here.
fn handleOneIncomingOrderLine(
    requestLine: &str,
    orderBookForInstrument: &mut WalBackedOrderBook,
) -> (OrderSubmissionWireResponse, Vec<TradeExecutionEvent>) {
    let parsedWireRequest: IncomingOrderWireRequest = match serde_json::from_str(requestLine) {
        Ok(wireRequest) => wireRequest,
        Err(parseError) => {
            return (
                OrderSubmissionWireResponse::errorResponse(format!(
                    "malformed order request: {parseError}"
                )),
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
        return match orderBookForInstrument.cancelOrder(orderSequenceNumberToCancel) {
            Ok(wasOrderCancelled) => (
                OrderSubmissionWireResponse::cancelResponse(wasOrderCancelled),
                Vec::new(),
            ),
            // The cancel already applied in memory, but the WAL write
            // that must durably record it before we acknowledge anything
            // did NOT complete — see walBackedOrderBook.rs's module docs.
            // Report failure rather than acknowledge a cancel the WAL
            // can't reconstruct on recovery.
            Err(walWriteError) => (
                OrderSubmissionWireResponse::errorResponse(format!(
                    "WAL write failed for cancel: {walWriteError}"
                )),
                Vec::new(),
            ),
        };
    }

    // Same shape: a status query is read-only and never becomes an
    // internal order, and never touches the WAL either.
    if let Some(orderSequenceNumberToQuery) = parsedWireRequest.queryOrderStatusSequenceNumber {
        let queryResult = orderBookForInstrument.queryOrderStatus(orderSequenceNumberToQuery);
        return (
            OrderSubmissionWireResponse::statusResponse(queryResult),
            Vec::new(),
        );
    }

    match orderBookForInstrument.submitIncomingOrder(parsedWireRequest.intoInternalOrderRequest()) {
        Ok(submissionOutcome) => {
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
        Err(walWriteError) => (
            OrderSubmissionWireResponse::errorResponse(format!(
                "WAL write failed for order submission: {walWriteError}"
            )),
            Vec::new(),
        ),
    }
}

/// Offline recovery-inspection tool: `cargo run -- --replay <walFilePath>`.
/// Reads every event out of `walFilePath`, replays it into a brand-new
/// `OrderBookCore` (exactly the same replay path `WalBackedOrderBook::
/// openRecoveringIfPresent` uses at real startup), prints the resulting
/// book depth and pending-stop count, then exits — it never binds the TCP
/// listener or touches the live WAL file the running server (if any) is
/// using. This is the tool used for this build's live-verification run:
/// point it at a real WAL file written by a real `cargo run` session and
/// see the reconstructed state for yourself, independent of the server
/// process that wrote it.
fn runReplayModeAndExit(walFilePathArgument: &str) -> ! {
    let walFilePath = Path::new(walFilePathArgument);
    println!("--replay: reading WAL events from {walFilePath:?}");

    let eventRecords = writeAheadLog::readAllEventRecordsFromWalFile(walFilePath)
        .unwrap_or_else(|readError| panic!("failed to read WAL file {walFilePath:?}: {readError}"));
    println!("--replay: read {} event record(s)", eventRecords.len());

    let (reconstructedOrderBook, tradeExecutionEventsProducedDuringReplay) =
        writeAheadLog::replayWalEventRecordsIntoFreshOrderBook(
            TRADED_INSTRUMENT_SYMBOL,
            &eventRecords,
        );
    println!(
        "--replay: replay re-derived {} trade(s) as a byproduct of the replayed commands",
        tradeExecutionEventsProducedDuringReplay.len()
    );

    reconstructedOrderBook.printCurrentBookDepth();
    println!(
        "--replay: {} pending stop order(s) still armed after replay",
        reconstructedOrderBook.pendingStopOrderCount()
    );

    std::process::exit(0);
}
