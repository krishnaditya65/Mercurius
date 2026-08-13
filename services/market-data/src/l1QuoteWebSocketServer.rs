// Real WebSocket broadcast of L1 (top-of-book) quotes — FEATURES.md §8
// [P1]. Every connected client is caught up with a SNAPSHOT of current
// state (per instrument, with that instrument's current sequence
// number) before switching to DELTA messages, and can send a
// RESYNC_REQUEST at any time to get a fresh SNAPSHOT — see
// `l1QuoteWireProtocol.rs` for the message shapes and
// `l1QuotePublisher.rs` for where the sequence numbers and quote state
// actually come from. Pushed the instant `main.rs`'s ingestion loop
// derives a new L1 quote from a real depth publish — NOT a polling loop.
//
// TODO(real build): one tokio runtime, one OS thread, in-process
// `tokio::sync::broadcast` fan-out — the same "not yet a real Kafka/
// fan-out fleet" caveat as `deltaPublisher.rs`'s println sink applies
// here too (ARCHITECTURE.md §5). A slow/stalled client can lag behind
// the bounded broadcast channel; when that happens this server detects
// it (`broadcast::error::RecvError::Lagged`) and proactively resends a
// full snapshot rather than leaving the client to figure out on its own
// that it missed messages.
#![allow(non_snake_case)]

use std::net::SocketAddr;
use std::sync::{Arc, Mutex};

use futures_util::stream::SplitSink;
use futures_util::{SinkExt, StreamExt};
use tokio::net::{TcpListener, TcpStream};
use tokio::sync::broadcast;
use tokio_tungstenite::WebSocketStream;
use tokio_tungstenite::tungstenite::Message;

use crate::l1QuotePublisher::{L1QuotePublisher, SequencedL1QuoteUpdate};
use crate::l1QuoteWireProtocol::{IncomingL1StreamClientMessage, OutgoingL1StreamMessage};

type OutgoingWebSocketSink = SplitSink<WebSocketStream<TcpStream>, Message>;

/// Binds `listenAddress`, spins up a dedicated tokio runtime on the
/// calling thread, and serves the L1 quote WS feed forever. Meant to be
/// called from its own OS thread (see `main.rs`), same pattern as
/// `httpQueryServer::runHttpQueryServer` — this function blocks.
pub fn runL1QuoteWebSocketServer(
    listenAddress: &str,
    sharedL1QuotePublisher: Arc<Mutex<L1QuotePublisher>>,
    broadcastSender: broadcast::Sender<SequencedL1QuoteUpdate>,
) {
    let tokioRuntime = match tokio::runtime::Runtime::new() {
        Ok(runtime) => runtime,
        Err(runtimeError) => {
            eprintln!("market-data L1 quote WS server failed to start its tokio runtime: {runtimeError}");
            return;
        }
    };

    tokioRuntime.block_on(async move {
        let tcpListener = match TcpListener::bind(listenAddress).await {
            Ok(listener) => listener,
            Err(bindError) => {
                eprintln!("market-data L1 quote WS server failed to bind {listenAddress}: {bindError}");
                return;
            }
        };
        println!("market-data L1 quote WebSocket server listening on {listenAddress}");
        serveConnectionsFromListener(tcpListener, sharedL1QuotePublisher, broadcastSender).await;
    });
}

/// The actual accept loop, factored out from `runL1QuoteWebSocketServer`
/// so tests can drive it against a pre-bound, ephemeral-port
/// `TcpListener` (`127.0.0.1:0`) instead of a fixed real port.
async fn serveConnectionsFromListener(
    tcpListener: TcpListener,
    sharedL1QuotePublisher: Arc<Mutex<L1QuotePublisher>>,
    broadcastSender: broadcast::Sender<SequencedL1QuoteUpdate>,
) {
    loop {
        let (tcpStream, peerAddress) = match tcpListener.accept().await {
            Ok(accepted) => accepted,
            Err(acceptError) => {
                eprintln!("market-data L1 quote WS server accept error: {acceptError}");
                continue;
            }
        };

        let sharedL1QuotePublisherForConnection = Arc::clone(&sharedL1QuotePublisher);
        let broadcastSenderForConnection = broadcastSender.clone();
        tokio::spawn(async move {
            handleOneWebSocketConnection(
                tcpStream,
                peerAddress,
                sharedL1QuotePublisherForConnection,
                broadcastSenderForConnection,
            )
            .await;
        });
    }
}

/// Serves one client connection for its whole lifetime: handshake, an
/// initial full snapshot, then a loop that both forwards broadcast
/// DELTA updates and services RESYNC_REQUEST messages from the client,
/// until the client disconnects or a send fails.
async fn handleOneWebSocketConnection(
    tcpStream: TcpStream,
    peerAddress: SocketAddr,
    sharedL1QuotePublisher: Arc<Mutex<L1QuotePublisher>>,
    broadcastSender: broadcast::Sender<SequencedL1QuoteUpdate>,
) {
    let webSocketStream = match tokio_tungstenite::accept_async(tcpStream).await {
        Ok(stream) => stream,
        Err(handshakeError) => {
            eprintln!("market-data L1 quote WS handshake with {peerAddress} failed: {handshakeError}");
            return;
        }
    };

    let (mut webSocketSink, mut webSocketSource) = webSocketStream.split();

    // Subscribe to the broadcast BEFORE sending the initial snapshot, so
    // there's no window between "read current state for the snapshot"
    // and "start receiving future updates" during which an update could
    // be silently missed.
    let mut broadcastReceiver = broadcastSender.subscribe();

    if !sendSnapshotsForAllInstruments(&mut webSocketSink, &sharedL1QuotePublisher).await {
        return;
    }

    loop {
        tokio::select! {
            broadcastResult = broadcastReceiver.recv() => {
                match broadcastResult {
                    Ok(update) => {
                        let sent = sendOutgoingMessage(&mut webSocketSink, OutgoingL1StreamMessage::Delta {
                            instrumentSymbol: update.instrumentSymbol.clone(),
                            sequenceNumber: update.perInstrumentSequenceNumber,
                            quote: update.quote.clone(),
                        }).await;
                        if !sent {
                            return;
                        }
                    }
                    Err(broadcast::error::RecvError::Lagged(skippedUpdateCount)) => {
                        // This client's receiver fell behind the
                        // broadcast channel's bounded buffer and missed
                        // skippedUpdateCount updates — precisely the gap
                        // the sequence-number contract exists to catch.
                        // Proactively resend a full snapshot instead of
                        // waiting on the client to notice.
                        eprintln!(
                            "market-data L1 quote WS client {peerAddress} lagged by {skippedUpdateCount} update(s); resending snapshot"
                        );
                        if !sendSnapshotsForAllInstruments(&mut webSocketSink, &sharedL1QuotePublisher).await {
                            return;
                        }
                    }
                    Err(broadcast::error::RecvError::Closed) => return,
                }
            }
            incomingMessage = webSocketSource.next() => {
                match incomingMessage {
                    Some(Ok(Message::Text(messageText))) => {
                        if let Ok(IncomingL1StreamClientMessage::ResyncRequest { instrumentSymbol }) =
                            serde_json::from_str::<IncomingL1StreamClientMessage>(&messageText)
                        {
                            let resendSucceeded = match instrumentSymbol {
                                Some(symbol) => {
                                    sendSnapshotForInstrument(&mut webSocketSink, &sharedL1QuotePublisher, &symbol).await
                                }
                                None => sendSnapshotsForAllInstruments(&mut webSocketSink, &sharedL1QuotePublisher).await,
                            };
                            if !resendSucceeded {
                                return;
                            }
                        }
                        // Malformed/unrecognized client messages are
                        // silently ignored — this feed is read-mostly,
                        // not worth hardening against bad client input.
                    }
                    Some(Ok(Message::Close(_))) | None => return,
                    Some(Ok(_)) => {} // ping/pong/binary frames — nothing to do
                    Some(Err(_)) => return,
                }
            }
        }
    }
}

async fn sendOutgoingMessage(webSocketSink: &mut OutgoingWebSocketSink, message: OutgoingL1StreamMessage) -> bool {
    let Ok(messageJson) = serde_json::to_string(&message) else {
        return false;
    };
    webSocketSink.send(Message::Text(messageJson)).await.is_ok()
}

/// Sends a SNAPSHOT for one instrument if market-data has ever recorded
/// a quote for it; if not (e.g. a resync request for a symbol that has
/// never traded), there's simply nothing to send — that's not a
/// connection-level failure, so this still returns `true`.
async fn sendSnapshotForInstrument(
    webSocketSink: &mut OutgoingWebSocketSink,
    sharedL1QuotePublisher: &Arc<Mutex<L1QuotePublisher>>,
    instrumentSymbol: &str,
) -> bool {
    let maybeSnapshot = {
        let l1QuotePublisher = sharedL1QuotePublisher
            .lock()
            .expect("l1 quote publisher mutex poisoned");
        l1QuotePublisher.currentSnapshotForInstrument(instrumentSymbol)
    };
    let Some(snapshot) = maybeSnapshot else {
        return true;
    };
    sendOutgoingMessage(
        webSocketSink,
        OutgoingL1StreamMessage::Snapshot {
            instrumentSymbol: snapshot.instrumentSymbol,
            sequenceNumber: snapshot.perInstrumentSequenceNumber,
            quote: snapshot.quote,
        },
    )
    .await
}

async fn sendSnapshotsForAllInstruments(
    webSocketSink: &mut OutgoingWebSocketSink,
    sharedL1QuotePublisher: &Arc<Mutex<L1QuotePublisher>>,
) -> bool {
    let snapshots = {
        let l1QuotePublisher = sharedL1QuotePublisher
            .lock()
            .expect("l1 quote publisher mutex poisoned");
        l1QuotePublisher.currentSnapshotsForAllInstruments()
    };
    for snapshot in snapshots {
        let sent = sendOutgoingMessage(
            webSocketSink,
            OutgoingL1StreamMessage::Snapshot {
                instrumentSymbol: snapshot.instrumentSymbol,
                sequenceNumber: snapshot.perInstrumentSequenceNumber,
                quote: snapshot.quote,
            },
        )
        .await;
        if !sent {
            return false;
        }
    }
    true
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::marketDataEventTypes::PriceLevelDeltaUpdate;
    use serde_json::Value;
    use std::time::Duration;

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

    /// Binds an ephemeral port, spawns the real accept loop against it
    /// (same function `runL1QuoteWebSocketServer` uses in production —
    /// nothing test-only about the server side here), and returns the
    /// `ws://` URL a `tokio-tungstenite` client can connect to plus the
    /// shared publisher/sender the test drives directly to simulate real
    /// depth publishes arriving from `main.rs`'s ingestion loop.
    async fn spawnServerOnEphemeralPort() -> (
        String,
        Arc<Mutex<L1QuotePublisher>>,
        broadcast::Sender<SequencedL1QuoteUpdate>,
    ) {
        let mut publisher = L1QuotePublisher::newPublisherWithNoSinks();
        let (broadcastSender, _receiverTemplate) = broadcast::channel(64);
        let broadcastSenderForSink = broadcastSender.clone();
        publisher.registerDownstreamSink(move |sequencedUpdate| {
            let _ = broadcastSenderForSink.send(sequencedUpdate.clone());
        });
        let sharedPublisher = Arc::new(Mutex::new(publisher));

        let tcpListener = TcpListener::bind("127.0.0.1:0")
            .await
            .expect("should bind an ephemeral port");
        let boundAddress = tcpListener.local_addr().expect("bound listener has a local address");

        let serverPublisherHandle = Arc::clone(&sharedPublisher);
        let serverBroadcastSender = broadcastSender.clone();
        tokio::spawn(serveConnectionsFromListener(
            tcpListener,
            serverPublisherHandle,
            serverBroadcastSender,
        ));

        (format!("ws://{boundAddress}"), sharedPublisher, broadcastSender)
    }

    /// Reads and JSON-parses the next WS text message with a generous
    /// timeout, so a protocol bug (e.g. no message ever sent) fails the
    /// test instead of hanging the suite forever.
    async fn nextJsonMessage<S>(webSocketSource: &mut S) -> Value
    where
        S: futures_util::Stream<Item = Result<Message, tokio_tungstenite::tungstenite::Error>> + Unpin,
    {
        let received = tokio::time::timeout(Duration::from_secs(5), webSocketSource.next())
            .await
            .expect("timed out waiting for a WS message")
            .expect("stream ended before a message arrived")
            .expect("WS transport error");
        let Message::Text(text) = received else {
            panic!("expected a text message, got {received:?}");
        };
        serde_json::from_str(&text).expect("message should be valid JSON")
    }

    #[tokio::test]
    async fn newClientReceivesASnapshotForAlreadyKnownStateBeforeAnyDeltas() {
        let (wsUrl, sharedPublisher, _broadcastSender) = spawnServerOnEphemeralPort().await;

        // Seed known state for DEMO-EQ BEFORE the client ever connects —
        // this is what the client's SNAPSHOT should reflect.
        {
            let mut publisher = sharedPublisher.lock().unwrap();
            publisher.applyDepthPublishForInstrument("DEMO-EQ", &[bidDelta(99, 10), askDelta(101, 5)], Some(100));
        }

        let (webSocketStream, _handshakeResponse) = tokio_tungstenite::connect_async(&wsUrl)
            .await
            .expect("client should connect");
        let (_sink, mut source) = webSocketStream.split();

        let snapshotMessage = nextJsonMessage(&mut source).await;
        assert_eq!(snapshotMessage["messageType"], "SNAPSHOT");
        assert_eq!(snapshotMessage["instrumentSymbol"], "DEMO-EQ");
        assert_eq!(snapshotMessage["sequenceNumber"], 1);
        assert_eq!(snapshotMessage["quote"]["bestBidPriceInMinorUnits"], 99);
        assert_eq!(snapshotMessage["quote"]["bestAskPriceInMinorUnits"], 101);
        assert_eq!(snapshotMessage["quote"]["lastTradePriceInMinorUnits"], 100);
    }

    #[tokio::test]
    async fn snapshotIsFollowedByDeltasInIncreasingSequenceOrder() {
        let (wsUrl, sharedPublisher, _broadcastSender) = spawnServerOnEphemeralPort().await;

        {
            let mut publisher = sharedPublisher.lock().unwrap();
            publisher.applyDepthPublishForInstrument("DEMO-EQ", &[bidDelta(99, 10)], Some(100));
        }

        let (webSocketStream, _handshakeResponse) = tokio_tungstenite::connect_async(&wsUrl)
            .await
            .expect("client should connect");
        let (_sink, mut source) = webSocketStream.split();

        let snapshotMessage = nextJsonMessage(&mut source).await;
        assert_eq!(snapshotMessage["messageType"], "SNAPSHOT");
        assert_eq!(snapshotMessage["sequenceNumber"], 1);

        // Simulate two more real depth publishes arriving from
        // main.rs's ingestion loop — a real push, not a poll, so this
        // must be reflected on the WS client without it asking again.
        {
            let mut publisher = sharedPublisher.lock().unwrap();
            publisher.applyDepthPublishForInstrument("DEMO-EQ", &[bidDelta(98, 4)], Some(102));
        }
        let firstDelta = nextJsonMessage(&mut source).await;
        assert_eq!(firstDelta["messageType"], "DELTA");
        assert_eq!(firstDelta["sequenceNumber"], 2);
        assert_eq!(firstDelta["quote"]["lastTradePriceInMinorUnits"], 102);

        {
            let mut publisher = sharedPublisher.lock().unwrap();
            publisher.applyDepthPublishForInstrument("DEMO-EQ", &[bidDelta(97, 2)], Some(103));
        }
        let secondDelta = nextJsonMessage(&mut source).await;
        assert_eq!(secondDelta["messageType"], "DELTA");
        assert_eq!(secondDelta["sequenceNumber"], 3);
        assert_eq!(secondDelta["quote"]["lastTradePriceInMinorUnits"], 103);
    }

    #[tokio::test]
    async fn resyncRequestReturnsAFreshSnapshotOnDemand() {
        let (wsUrl, sharedPublisher, _broadcastSender) = spawnServerOnEphemeralPort().await;

        {
            let mut publisher = sharedPublisher.lock().unwrap();
            publisher.applyDepthPublishForInstrument("DEMO-EQ", &[bidDelta(99, 10)], Some(100));
        }

        let (webSocketStream, _handshakeResponse) = tokio_tungstenite::connect_async(&wsUrl)
            .await
            .expect("client should connect");
        let (mut sink, mut source) = webSocketStream.split();

        let initialSnapshot = nextJsonMessage(&mut source).await;
        assert_eq!(initialSnapshot["sequenceNumber"], 1);

        // A client that detected a sequence gap explicitly asks for a
        // fresh snapshot rather than silently continuing.
        sink.send(Message::Text(
            r#"{"messageType":"RESYNC_REQUEST","instrumentSymbol":"DEMO-EQ"}"#.to_string(),
        ))
        .await
        .expect("resync request should send");

        let resyncedSnapshot = nextJsonMessage(&mut source).await;
        assert_eq!(resyncedSnapshot["messageType"], "SNAPSHOT");
        assert_eq!(resyncedSnapshot["instrumentSymbol"], "DEMO-EQ");
        assert_eq!(resyncedSnapshot["sequenceNumber"], 1); // unchanged — nothing new was published
    }

    #[tokio::test]
    async fn clientConnectingBeforeAnyPublishGetsNoSnapshotThenGetsTheFirstDelta() {
        let (wsUrl, sharedPublisher, _broadcastSender) = spawnServerOnEphemeralPort().await;

        let (webSocketStream, _handshakeResponse) = tokio_tungstenite::connect_async(&wsUrl)
            .await
            .expect("client should connect");
        let (_sink, mut source) = webSocketStream.split();

        // Nothing known yet for any instrument — the very first message
        // this client sees should be a DELTA once something is
        // published, not an empty/placeholder SNAPSHOT.
        {
            let mut publisher = sharedPublisher.lock().unwrap();
            publisher.applyDepthPublishForInstrument("DEMO-EQ", &[bidDelta(50, 1)], Some(50));
        }

        let onlyMessage = nextJsonMessage(&mut source).await;
        assert_eq!(onlyMessage["messageType"], "DELTA");
        assert_eq!(onlyMessage["sequenceNumber"], 1);
    }
}
