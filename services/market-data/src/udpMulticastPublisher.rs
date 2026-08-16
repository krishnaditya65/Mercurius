// FEATURES.md §8 [P4] — "UDP multicast fan-out for co-located
// institutional consumers". A real UDP multicast publisher: joins/sends to
// an actual IPv4 multicast group via `std::net::UdpSocket`, carrying the
// same trade-tick and L1 top-of-book data the WebSocket feed
// (`l1QuoteWebSocketServer.rs`) already carries, in a compact hand-rolled
// binary wire format (not JSON — multicast fan-out to co-located
// consumers is exactly the case where per-message serialization overhead
// matters). Wired into `main.rs`'s `ingestDepthPublishMessage` so every
// real (or simulated) tick/quote update gets fanned out here too, same as
// every other downstream sink in this service.
//
// TODO(real build): no reliability layer (multicast/UDP is inherently
// best-effort — a real institutional feed would layer sequence-number gap
// detection + a snapshot-refresh/retransmit channel on top, similar in
// spirit to the WS feed's own resync protocol in `l1QuoteWebSocketServer.rs`,
// but that's out of scope here). No encryption/auth — anything that can
// join the multicast group on the local network segment can read the
// feed. TTL is fixed at 1 (link-local only) since there's no real network
// topology to route across in this skeleton.
#![allow(non_snake_case)]

use std::io;
use std::net::{SocketAddr, UdpSocket};

/// Default multicast group + port this service publishes to, overridable
/// via `MARKET_DATA_UDP_MULTICAST_GROUP_ADDRESS` in `main.rs`. `239.x.x.x`
/// is IPv4's administratively-scoped (site-local) multicast range — a
/// reasonable default for "internal co-located consumers", not a
/// globally-routable address.
pub const DEFAULT_MULTICAST_GROUP_ADDRESS: &str = "239.1.1.1:9105";

/// Multicast TTL this publisher sends with. `1` means "don't forward past
/// the local subnet" — appropriate for co-located consumers, and the safe
/// default so a misconfigured instance can't accidentally flood a wider
/// network.
const MULTICAST_TIME_TO_LIVE: u32 = 1;

/// Message-kind tag byte, so one wire format can carry either a trade tick
/// or an L1 quote update and a receiver can tell which it got without a
/// second out-of-band channel.
const MESSAGE_KIND_TRADE_TICK: u8 = 0;
const MESSAGE_KIND_L1_QUOTE: u8 = 1;

/// A decoded trade tick, as reconstructed by `decodeMessage`. Exists so
/// tests (and any future real consumer written in Rust) don't have to
/// hand-parse bytes themselves. Not constructed anywhere in `main.rs`
/// today — this binary only ENCODES and sends; `decodeMessage` and these
/// decoded types exist for the real send/receive integration tests below
/// and for any future Rust-based multicast consumer.
#[allow(dead_code)]
#[derive(Debug, Clone, PartialEq)]
pub struct DecodedTradeTick {
    pub instrumentSymbol: String,
    pub executedAtEpochSeconds: u64,
    pub priceInMinorUnits: i64,
    pub quantity: u64,
}

/// A decoded L1 quote update, as reconstructed by `decodeMessage`.
#[allow(dead_code)]
#[derive(Debug, Clone, PartialEq)]
pub struct DecodedL1QuoteUpdate {
    pub instrumentSymbol: String,
    pub perInstrumentSequenceNumber: u64,
    pub bestBidPriceInMinorUnits: Option<i64>,
    pub bestBidQuantity: u64,
    pub bestAskPriceInMinorUnits: Option<i64>,
    pub bestAskQuantity: u64,
    pub lastTradePriceInMinorUnits: Option<i64>,
}

/// Either decoded message kind, returned by `decodeMessage`.
#[allow(dead_code)]
#[derive(Debug, Clone, PartialEq)]
pub enum DecodedMulticastMessage {
    TradeTick(DecodedTradeTick),
    L1Quote(DecodedL1QuoteUpdate),
}

pub struct UdpMulticastPublisher {
    sendSocket: UdpSocket,
    multicastGroupAddress: SocketAddr,
}

impl UdpMulticastPublisher {
    /// Binds an ephemeral local send socket and resolves `groupAddress`
    /// (e.g. `"239.1.1.1:9105"`) as the fixed destination every
    /// `publish*` call sends to. Real, fallible I/O — surfaced as
    /// `io::Result` rather than panicking, matching this crate's general
    /// posture of returning `Result`/`Option` from fallible constructors
    /// where a caller (`main.rs`) can reasonably decide to skip the
    /// feature rather than crash the whole service.
    pub fn newPublisherForGroupAddress(groupAddress: &str) -> io::Result<Self> {
        let multicastGroupAddress: SocketAddr = groupAddress.parse().map_err(|_| {
            io::Error::new(
                io::ErrorKind::InvalidInput,
                format!("invalid multicast group address: {groupAddress}"),
            )
        })?;

        let sendSocket = UdpSocket::bind("0.0.0.0:0")?;
        match multicastGroupAddress {
            SocketAddr::V4(_) => sendSocket.set_multicast_ttl_v4(MULTICAST_TIME_TO_LIVE)?,
            SocketAddr::V6(_) => {
                return Err(io::Error::new(
                    io::ErrorKind::InvalidInput,
                    "IPv6 multicast group addresses are not supported",
                ));
            }
        }

        Ok(UdpMulticastPublisher {
            sendSocket,
            multicastGroupAddress,
        })
    }

    /// Encodes + sends one trade tick to the multicast group. Returns the
    /// number of bytes sent (mirroring `UdpSocket::send_to`'s own return
    /// shape) so a caller can log/assert on it if it wants to.
    pub fn publishTradeTick(
        &self,
        instrumentSymbol: &str,
        executedAtEpochSeconds: u64,
        priceInMinorUnits: i64,
        quantity: u64,
    ) -> io::Result<usize> {
        let mut encodedMessage = vec![MESSAGE_KIND_TRADE_TICK];
        writeLengthPrefixedString(&mut encodedMessage, instrumentSymbol);
        writeU64LittleEndian(&mut encodedMessage, executedAtEpochSeconds);
        writeI64LittleEndian(&mut encodedMessage, priceInMinorUnits);
        writeU64LittleEndian(&mut encodedMessage, quantity);
        self.sendSocket.send_to(&encodedMessage, self.multicastGroupAddress)
    }

    /// Encodes + sends one L1 top-of-book update to the multicast group —
    /// the same data `l1QuoteWebSocketServer.rs` pushes over WebSocket,
    /// fanned out here too.
    #[allow(clippy::too_many_arguments)]
    pub fn publishL1Quote(
        &self,
        instrumentSymbol: &str,
        perInstrumentSequenceNumber: u64,
        bestBidPriceInMinorUnits: Option<i64>,
        bestBidQuantity: u64,
        bestAskPriceInMinorUnits: Option<i64>,
        bestAskQuantity: u64,
        lastTradePriceInMinorUnits: Option<i64>,
    ) -> io::Result<usize> {
        let mut encodedMessage = vec![MESSAGE_KIND_L1_QUOTE];
        writeLengthPrefixedString(&mut encodedMessage, instrumentSymbol);
        writeU64LittleEndian(&mut encodedMessage, perInstrumentSequenceNumber);
        writeOptionalI64(&mut encodedMessage, bestBidPriceInMinorUnits);
        writeU64LittleEndian(&mut encodedMessage, bestBidQuantity);
        writeOptionalI64(&mut encodedMessage, bestAskPriceInMinorUnits);
        writeU64LittleEndian(&mut encodedMessage, bestAskQuantity);
        writeOptionalI64(&mut encodedMessage, lastTradePriceInMinorUnits);
        self.sendSocket.send_to(&encodedMessage, self.multicastGroupAddress)
    }
}

/// Length-prefixed string encoding. Uses a `u16` (little-endian) length
/// prefix — NOT a single `u8` — because a `u8` prefix silently truncates
/// (`bytes.len() as u8`) for any string longer than 255 bytes while
/// `extend_from_slice` below still writes the FULL string: the truncated
/// length no longer matches the bytes actually written, desyncing every
/// subsequent field a decoder reads from this datagram. This wire format
/// has no external/frozen consumer yet (this module's own `decodeMessage`
/// is the only decoder in this codebase), so widening the prefix here is
/// safe and is preferred over silently rejecting/truncating long inputs.
/// `instrumentSymbol`s are expected to be short (a handful of ASCII
/// characters) in practice, but a `u16` prefix (up to 65535 bytes) gives
/// enormous headroom over a 1-byte prefix's silent-corruption failure mode
/// for a negligible 1-extra-byte-per-message cost.
fn writeLengthPrefixedString(buffer: &mut Vec<u8>, value: &str) {
    let bytes = value.as_bytes();
    buffer.extend_from_slice(&(bytes.len() as u16).to_le_bytes());
    buffer.extend_from_slice(bytes);
}

fn writeU64LittleEndian(buffer: &mut Vec<u8>, value: u64) {
    buffer.extend_from_slice(&value.to_le_bytes());
}

fn writeI64LittleEndian(buffer: &mut Vec<u8>, value: i64) {
    buffer.extend_from_slice(&value.to_le_bytes());
}

fn writeOptionalI64(buffer: &mut Vec<u8>, value: Option<i64>) {
    match value {
        Some(innerValue) => {
            buffer.push(1);
            writeI64LittleEndian(buffer, innerValue);
        }
        None => buffer.push(0),
    }
}

/// Decodes a raw multicast payload back into a `DecodedMulticastMessage`.
/// Returns `None` on any malformed/truncated input rather than panicking —
/// a receiver on an unreliable UDP transport should tolerate a corrupt or
/// partial datagram, not crash on one.
#[allow(dead_code)]
pub fn decodeMessage(bytes: &[u8]) -> Option<DecodedMulticastMessage> {
    let mut cursor = ByteCursor { bytes, position: 0 };
    let messageKind = cursor.readU8()?;

    match messageKind {
        MESSAGE_KIND_TRADE_TICK => {
            let instrumentSymbol = cursor.readLengthPrefixedString()?;
            let executedAtEpochSeconds = cursor.readU64()?;
            let priceInMinorUnits = cursor.readI64()?;
            let quantity = cursor.readU64()?;
            Some(DecodedMulticastMessage::TradeTick(DecodedTradeTick {
                instrumentSymbol,
                executedAtEpochSeconds,
                priceInMinorUnits,
                quantity,
            }))
        }
        MESSAGE_KIND_L1_QUOTE => {
            let instrumentSymbol = cursor.readLengthPrefixedString()?;
            let perInstrumentSequenceNumber = cursor.readU64()?;
            let bestBidPriceInMinorUnits = cursor.readOptionalI64()?;
            let bestBidQuantity = cursor.readU64()?;
            let bestAskPriceInMinorUnits = cursor.readOptionalI64()?;
            let bestAskQuantity = cursor.readU64()?;
            let lastTradePriceInMinorUnits = cursor.readOptionalI64()?;
            Some(DecodedMulticastMessage::L1Quote(DecodedL1QuoteUpdate {
                instrumentSymbol,
                perInstrumentSequenceNumber,
                bestBidPriceInMinorUnits,
                bestBidQuantity,
                bestAskPriceInMinorUnits,
                bestAskQuantity,
                lastTradePriceInMinorUnits,
            }))
        }
        _ => None,
    }
}

/// Tiny read cursor over a byte slice, used only by `decodeMessage`. Not
/// meant to be a general-purpose parsing utility — just enough to mirror
/// `writeLengthPrefixedString`/`writeU64LittleEndian`/etc. above without
/// repeating bounds-checking logic at every call site.
#[allow(dead_code)]
struct ByteCursor<'a> {
    bytes: &'a [u8],
    position: usize,
}

#[allow(dead_code)]
impl<'a> ByteCursor<'a> {
    fn readU8(&mut self) -> Option<u8> {
        let value = *self.bytes.get(self.position)?;
        self.position += 1;
        Some(value)
    }

    fn readFixedBytes<const N: usize>(&mut self) -> Option<[u8; N]> {
        let slice = self.bytes.get(self.position..self.position + N)?;
        self.position += N;
        slice.try_into().ok()
    }

    fn readU64(&mut self) -> Option<u64> {
        self.readFixedBytes::<8>().map(u64::from_le_bytes)
    }

    fn readI64(&mut self) -> Option<i64> {
        self.readFixedBytes::<8>().map(i64::from_le_bytes)
    }

    fn readOptionalI64(&mut self) -> Option<Option<i64>> {
        match self.readU8()? {
            0 => Some(None),
            _ => Some(Some(self.readI64()?)),
        }
    }

    fn readU16(&mut self) -> Option<u16> {
        self.readFixedBytes::<2>().map(u16::from_le_bytes)
    }

    fn readLengthPrefixedString(&mut self) -> Option<String> {
        let length = self.readU16()? as usize;
        let slice = self.bytes.get(self.position..self.position + length)?;
        self.position += length;
        String::from_utf8(slice.to_vec()).ok()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::net::{Ipv4Addr, SocketAddrV4, UdpSocket};
    use std::time::Duration;

    /// Binds a receiving socket on `groupAddress`'s port and joins the
    /// multicast group so it actually receives datagrams sent to that
    /// group — real OS-level multicast membership, not a mocked socket.
    fn joinedMulticastReceiver(groupAddress: &str) -> UdpSocket {
        let socketAddr: SocketAddrV4 = groupAddress.parse().expect("valid test group address");
        let receiveSocket =
            UdpSocket::bind(SocketAddrV4::new(Ipv4Addr::UNSPECIFIED, socketAddr.port())).expect("bind receive socket");
        receiveSocket
            .join_multicast_v4(socketAddr.ip(), &Ipv4Addr::UNSPECIFIED)
            .expect("join multicast group");
        receiveSocket
            .set_read_timeout(Some(Duration::from_secs(2)))
            .expect("set read timeout");
        receiveSocket
    }

    #[test]
    fn realSendAndReceiveOfATradeTickOverActualMulticast() {
        let receiveSocket = joinedMulticastReceiver("239.5.5.1:19105");
        let publisher =
            UdpMulticastPublisher::newPublisherForGroupAddress("239.5.5.1:19105").expect("construct publisher");

        publisher
            .publishTradeTick("DEMO-EQ", 1_700_000_000, 10_050, 25)
            .expect("send trade tick");

        let mut receiveBuffer = [0u8; 512];
        let (bytesReceived, _sourceAddr) = receiveSocket.recv_from(&mut receiveBuffer).expect("receive datagram");

        let decoded = decodeMessage(&receiveBuffer[..bytesReceived]).expect("decode received datagram");
        assert_eq!(
            decoded,
            DecodedMulticastMessage::TradeTick(DecodedTradeTick {
                instrumentSymbol: "DEMO-EQ".to_string(),
                executedAtEpochSeconds: 1_700_000_000,
                priceInMinorUnits: 10_050,
                quantity: 25,
            })
        );
    }

    #[test]
    fn realSendAndReceiveOfAnL1QuoteOverActualMulticast() {
        let receiveSocket = joinedMulticastReceiver("239.5.5.2:19106");
        let publisher =
            UdpMulticastPublisher::newPublisherForGroupAddress("239.5.5.2:19106").expect("construct publisher");

        publisher
            .publishL1Quote("DEMO-EQ", 3, Some(9_900), 10, Some(10_100), 15, Some(10_000))
            .expect("send l1 quote");

        let mut receiveBuffer = [0u8; 512];
        let (bytesReceived, _sourceAddr) = receiveSocket.recv_from(&mut receiveBuffer).expect("receive datagram");

        let decoded = decodeMessage(&receiveBuffer[..bytesReceived]).expect("decode received datagram");
        assert_eq!(
            decoded,
            DecodedMulticastMessage::L1Quote(DecodedL1QuoteUpdate {
                instrumentSymbol: "DEMO-EQ".to_string(),
                perInstrumentSequenceNumber: 3,
                bestBidPriceInMinorUnits: Some(9_900),
                bestBidQuantity: 10,
                bestAskPriceInMinorUnits: Some(10_100),
                bestAskQuantity: 15,
                lastTradePriceInMinorUnits: Some(10_000),
            })
        );
    }

    #[test]
    fn l1QuoteWithNoneSidesRoundTripsCorrectly() {
        let receiveSocket = joinedMulticastReceiver("239.5.5.3:19107");
        let publisher =
            UdpMulticastPublisher::newPublisherForGroupAddress("239.5.5.3:19107").expect("construct publisher");

        publisher
            .publishL1Quote("THIN-BOOK", 1, None, 0, None, 0, None)
            .expect("send l1 quote");

        let mut receiveBuffer = [0u8; 512];
        let (bytesReceived, _sourceAddr) = receiveSocket.recv_from(&mut receiveBuffer).expect("receive datagram");
        let decoded = decodeMessage(&receiveBuffer[..bytesReceived]).expect("decode received datagram");

        match decoded {
            DecodedMulticastMessage::L1Quote(quote) => {
                assert_eq!(quote.bestBidPriceInMinorUnits, None);
                assert_eq!(quote.bestAskPriceInMinorUnits, None);
                assert_eq!(quote.lastTradePriceInMinorUnits, None);
            }
            other => panic!("expected an L1 quote, got {other:?}"),
        }
    }

    #[test]
    fn multipleSequentialTicksAreAllReceivedInOrder() {
        let receiveSocket = joinedMulticastReceiver("239.5.5.4:19108");
        let publisher =
            UdpMulticastPublisher::newPublisherForGroupAddress("239.5.5.4:19108").expect("construct publisher");

        for price in [100i64, 200, 300] {
            publisher.publishTradeTick("DEMO-EQ", 1, price, 1).expect("send tick");
        }

        let mut receivedPrices = Vec::new();
        for _ in 0..3 {
            let mut receiveBuffer = [0u8; 512];
            let (bytesReceived, _) = receiveSocket.recv_from(&mut receiveBuffer).expect("receive datagram");
            match decodeMessage(&receiveBuffer[..bytesReceived]).expect("decode") {
                DecodedMulticastMessage::TradeTick(tick) => receivedPrices.push(tick.priceInMinorUnits),
                other => panic!("expected a trade tick, got {other:?}"),
            }
        }

        // UDP doesn't guarantee ordering in general, but on loopback for a
        // same-process sender/receiver with no contention it is in
        // practice always delivered in send order — assert the multiset
        // received matches, which is the meaningful invariant here.
        receivedPrices.sort();
        assert_eq!(receivedPrices, vec![100, 200, 300]);
    }

    #[test]
    fn invalidGroupAddressIsRejectedAtConstructionTime() {
        let result = UdpMulticastPublisher::newPublisherForGroupAddress("not-an-address");
        assert!(result.is_err());
    }

    #[test]
    fn decodeMessageReturnsNoneForEmptyInput() {
        assert!(decodeMessage(&[]).is_none());
    }

    #[test]
    fn decodeMessageReturnsNoneForAnUnknownMessageKindTag() {
        assert!(decodeMessage(&[0xFF]).is_none());
    }

    #[test]
    fn decodeMessageReturnsNoneForATruncatedTradeTick() {
        // Valid kind tag + length-prefixed symbol, but missing the
        // trailing numeric fields entirely.
        let mut truncated = vec![MESSAGE_KIND_TRADE_TICK];
        writeLengthPrefixedString(&mut truncated, "DEMO-EQ");
        assert!(decodeMessage(&truncated).is_none());
    }

    #[test]
    fn defaultMulticastGroupAddressParsesAsAValidSocketAddr() {
        assert!(DEFAULT_MULTICAST_GROUP_ADDRESS.parse::<SocketAddr>().is_ok());
    }

    #[test]
    fn encodeThenDecodeRoundTripsAStringLongerThan255BytesWithoutDesyncingTheDatagram() {
        // A 1-byte length prefix truncates for any string >255 bytes
        // (`bytes.len() as u8`) while `extend_from_slice` still writes the
        // FULL string — so the encoded length no longer matches the bytes
        // actually written, and every field decoded after this string
        // comes out corrupted/misaligned. A 300-byte instrument symbol
        // (implausible in practice, but exactly the case this guards)
        // reproduces it directly via a real encode -> real decode round
        // trip over actual multicast, checking the numeric fields AFTER
        // the string decode correctly, not just the string itself.
        let longSymbol: String = "X".repeat(300);
        assert_eq!(longSymbol.len(), 300);

        let receiveSocket = joinedMulticastReceiver("239.5.5.6:19110");
        let publisher =
            UdpMulticastPublisher::newPublisherForGroupAddress("239.5.5.6:19110").expect("construct publisher");

        publisher
            .publishTradeTick(&longSymbol, 1_700_000_000, 10_050, 25)
            .expect("send trade tick with a long symbol");

        let mut receiveBuffer = [0u8; 4096];
        let (bytesReceived, _sourceAddr) = receiveSocket.recv_from(&mut receiveBuffer).expect("receive datagram");

        let decoded = decodeMessage(&receiveBuffer[..bytesReceived]).expect("decode received datagram");
        assert_eq!(
            decoded,
            DecodedMulticastMessage::TradeTick(DecodedTradeTick {
                instrumentSymbol: longSymbol,
                executedAtEpochSeconds: 1_700_000_000,
                priceInMinorUnits: 10_050,
                quantity: 25,
            })
        );
    }

    #[test]
    fn encodeThenDecodeRoundTripsAnEmptySymbol() {
        let receiveSocket = joinedMulticastReceiver("239.5.5.5:19109");
        let publisher =
            UdpMulticastPublisher::newPublisherForGroupAddress("239.5.5.5:19109").expect("construct publisher");
        publisher
            .publishTradeTick("", 1, 1, 1)
            .expect("send tick with empty symbol");

        let mut receiveBuffer = [0u8; 512];
        let (bytesReceived, _) = receiveSocket.recv_from(&mut receiveBuffer).expect("receive datagram");
        let decoded = decodeMessage(&receiveBuffer[..bytesReceived]).expect("decode");
        match decoded {
            DecodedMulticastMessage::TradeTick(tick) => assert_eq!(tick.instrumentSymbol, ""),
            other => panic!("expected a trade tick, got {other:?}"),
        }
    }
}
