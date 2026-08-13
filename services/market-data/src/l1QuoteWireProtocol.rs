// The JSON message shapes exchanged over the L1 quote WebSocket feed
// (`l1QuoteWebSocketServer.rs`) — FEATURES.md §8 [P1]/[P2]. This is the
// client-facing snapshot+delta+resync contract: a client connects,
// receives a SNAPSHOT (current state + its sequence number) for every
// instrument market-data currently has state for, then receives DELTA
// messages as new quotes arrive. If a client detects a gap — the
// sequence number on an incoming message for an instrument doesn't
// immediately follow the last one it saw for that same instrument — it
// sends a RESYNC_REQUEST and gets a fresh SNAPSHOT back rather than
// silently continuing on a now-possibly-inconsistent view.
#![allow(non_snake_case)]

use serde::{Deserialize, Serialize};

use crate::l1QuotePublisher::L1Quote;

/// Server -> client. `messageType` is the discriminant a client switches
/// on; SNAPSHOT and DELTA carry an identically-shaped payload (see
/// `l1QuotePublisher.rs`'s doc comment on `SequencedL1QuoteUpdate` for
/// why top-of-book has nothing smaller than "the current best bid/ask/
/// last" to send as an incremental delta) — the distinction is purely
/// about when a client may trust it as a fresh baseline (SNAPSHOT) vs.
/// when it must first check for a sequence gap (DELTA).
#[derive(Debug, Clone, Serialize)]
#[serde(tag = "messageType")]
pub enum OutgoingL1StreamMessage {
    #[serde(rename = "SNAPSHOT")]
    Snapshot {
        instrumentSymbol: String,
        sequenceNumber: u64,
        quote: L1Quote,
    },
    #[serde(rename = "DELTA")]
    Delta {
        instrumentSymbol: String,
        sequenceNumber: u64,
        quote: L1Quote,
    },
}

/// Client -> server. `instrumentSymbol: None` asks for a fresh snapshot
/// of every instrument; `Some(symbol)` scopes the resync to just that
/// one instrument, so a client that only detected a gap on one symbol's
/// sequence numbers doesn't have to pay for a full resync of everything
/// else it's tracking too.
#[derive(Debug, Clone, Deserialize)]
#[serde(tag = "messageType")]
pub enum IncomingL1StreamClientMessage {
    #[serde(rename = "RESYNC_REQUEST")]
    ResyncRequest {
        #[serde(default)]
        instrumentSymbol: Option<String>,
    },
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn snapshotMessageSerializesWithMessageTypeDiscriminant() {
        let message = OutgoingL1StreamMessage::Snapshot {
            instrumentSymbol: "DEMO-EQ".to_string(),
            sequenceNumber: 3,
            quote: L1Quote {
                instrumentSymbol: "DEMO-EQ".to_string(),
                bestBidPriceInMinorUnits: Some(99),
                bestBidQuantity: 10,
                bestAskPriceInMinorUnits: Some(101),
                bestAskQuantity: 5,
                lastTradePriceInMinorUnits: Some(100),
            },
        };

        let json = serde_json::to_string(&message).expect("should serialize");
        assert!(json.contains(r#""messageType":"SNAPSHOT""#));
        assert!(json.contains(r#""sequenceNumber":3"#));
        assert!(json.contains(r#""bestBidPriceInMinorUnits":99"#));
    }

    #[test]
    fn resyncRequestWithNoInstrumentSymbolDeserializesToNone() {
        let parsed: IncomingL1StreamClientMessage =
            serde_json::from_str(r#"{"messageType":"RESYNC_REQUEST"}"#).expect("should parse");
        let IncomingL1StreamClientMessage::ResyncRequest { instrumentSymbol } = parsed;
        assert_eq!(instrumentSymbol, None);
    }

    #[test]
    fn resyncRequestScopedToOneInstrumentDeserializesThatSymbol() {
        let parsed: IncomingL1StreamClientMessage =
            serde_json::from_str(r#"{"messageType":"RESYNC_REQUEST","instrumentSymbol":"DEMO-EQ"}"#)
                .expect("should parse");
        let IncomingL1StreamClientMessage::ResyncRequest { instrumentSymbol } = parsed;
        assert_eq!(instrumentSymbol, Some("DEMO-EQ".to_string()));
    }
}
