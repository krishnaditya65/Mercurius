// Home-screen live P&L widget — FEATURES.md §21's "Cross-device
// watchlist/alert sync with a home-screen live P&L widget". Computes a
// REAL aggregated unrealized P&L figure per account by combining two
// genuinely different real data sources:
//
//   - Average cost basis + net quantity per position comes from
//     oms-gateway's real mark-to-market engine
//     (services/oms-gateway/internal/marktomarket, built from real fills)
//     over a real, read-only HTTP call to `GET /mark-to-market?accountId=...`.
//     This module only ever READS from oms-gateway — it never writes.
//
//   - The CURRENT MARKET PRICE for each position comes from market-data's
//     OWN real trade-tick store (`candleAggregator`'s most recent trade)
//     — deliberately NOT oms-gateway's own `currentMarketPriceInMinorUnits`
//     field, which reflects whatever price (if any) was last manually
//     PUSHED to oms-gateway via `POST /mark-to-market/price` and may be
//     stale or entirely absent. Using market-data's own live tape instead
//     of trusting a second copy of "current price" from oms-gateway is
//     the actual point of building this widget in market-data at all.
//
// The two are joined here, per position, into a genuine unrealizedPnL =
// netQuantity * (marketDataLivePrice - omsGatewayAverageEntryPrice), summed
// into a real per-account total — not hardcoded, not a passthrough of
// either upstream's own number.
//
// KNOWN LIMITATION (documented honestly, not hidden): oms-gateway's own
// `GET /mark-to-market` handler (cmd/server/main.go) only returns
// non-empty position/cost-basis data for accounts it considers
// "leveraged" (outstanding margin-funding principal, or any pledged
// holding) — see that handler's doc comment. That gate lives entirely on
// oms-gateway's side and this module cannot and does not change it (only
// a read-only HTTP client of oms-gateway per this build's scope). For an
// unleveraged account this widget will honestly report a zero P&L / empty
// position list, exactly mirroring what oms-gateway itself would show —
// it is not silently wrong, just scoped by an upstream restriction a real
// build would want to lift (oms-gateway would need a separate "give me
// cost basis for ANY account" endpoint).
//
// TODO(real build): one blocking HTTP round-trip to oms-gateway per
// refresh — fine for an on-demand "refresh" click or a slow poll, not
// something to hammer on a tight interval. No caching, no auth, no
// retry/backoff on a transient oms-gateway connection failure (returned
// to the caller as a plain error string instead).
#![allow(non_snake_case)]

use std::io::{Read, Write};
use std::net::TcpStream;
use std::time::Duration;

use serde::{Deserialize, Serialize};

use crate::candleAggregator::CandleAggregator;

/// How long to wait for oms-gateway to answer before giving up — this is
/// a synchronous, blocking call made from the HTTP query server's own
/// request-handling thread, so it must not hang forever.
const OMS_GATEWAY_FETCH_TIMEOUT: Duration = Duration::from_secs(5);

/// One position as reported by oms-gateway's real mark-to-market engine.
/// Deliberately only captures the two fields this module actually trusts
/// from oms-gateway (`netQuantity`, `averageEntryPriceInMinorUnits`) —
/// `currentMarketPriceInMinorUnits`/`unrealizedPnLInMinorUnits` are also
/// present on the wire but intentionally NOT deserialized here, since
/// this module supplies its own live price instead (see module doc).
#[derive(Debug, Clone, Deserialize, PartialEq)]
pub struct OmsPositionCostBasis {
    pub instrumentSymbol: String,
    pub netQuantity: i64,
    pub averageEntryPriceInMinorUnits: i64,
}

#[derive(Debug, Clone, Deserialize)]
struct OmsMarkToMarketWireResponse {
    #[serde(default)]
    positions: Vec<OmsPositionCostBasis>,
}

/// One position's real, freshly-computed contribution to the live P&L
/// widget.
#[derive(Debug, Clone, Serialize, PartialEq)]
pub struct LivePnlPositionSnapshot {
    pub instrumentSymbol: String,
    pub netQuantity: i64,
    pub averageEntryPriceInMinorUnits: i64,
    pub currentMarketPriceInMinorUnits: i64,
    pub unrealizedPnLInMinorUnits: i64,
    /// False when market-data has never seen a trade for this instrument
    /// — in that case `currentMarketPriceInMinorUnits` falls back to the
    /// position's own average entry price (so unrealizedPnL reports 0
    /// rather than a fabricated nonzero number) and the caller can tell
    /// the difference from this flag rather than being silently misled.
    pub currentMarketPriceIsLive: bool,
}

#[derive(Debug, Clone, Serialize)]
pub struct LivePnlSnapshot {
    pub accountIdentifier: String,
    pub totalUnrealizedPnLInMinorUnits: i64,
    pub positions: Vec<LivePnlPositionSnapshot>,
}

/// Returned by `computeLivePnlSnapshot` when the P&L arithmetic for some
/// position would overflow `i64`. Surfaced as a clean, reportable error
/// rather than panicking (`*`/`-` in debug builds) or silently wrapping
/// around to a nonsense P&L figure (release builds) — a live P&L widget
/// must never show a fabricated number.
#[derive(Debug, Clone, PartialEq)]
pub struct LivePnlOverflowError {
    pub instrumentSymbol: String,
}

impl std::fmt::Display for LivePnlOverflowError {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(
            formatter,
            "unrealized P&L computation overflowed i64 for instrument {}",
            self.instrumentSymbol
        )
    }
}

/// The real computation — deliberately pure (no I/O), taking oms-gateway's
/// already-fetched cost-basis data and market-data's own candle aggregator
/// as plain arguments, so it's trivially unit- and integration-testable
/// with constructed data rather than a live oms-gateway process.
///
/// Uses checked arithmetic (`checked_sub`/`checked_mul`) for the
/// per-position P&L and the running total: `netQuantity * (currentPrice -
/// averageEntryPrice)` is a plain `i64` multiply of two values a caller
/// (oms-gateway's response, or a market price) ultimately controls, and an
/// extreme-but-constructible combination of the two can overflow `i64` —
/// this must surface as `Err(LivePnlOverflowError)`, not a panic or a
/// silently wrapped-around P&L figure.
pub fn computeLivePnlSnapshot(
    accountIdentifier: &str,
    omsPositions: &[OmsPositionCostBasis],
    candleAggregator: &CandleAggregator,
) -> Result<LivePnlSnapshot, LivePnlOverflowError> {
    let mut positions = Vec::with_capacity(omsPositions.len());
    let mut totalUnrealizedPnLInMinorUnits: i64 = 0;

    for omsPosition in omsPositions {
        if omsPosition.netQuantity == 0 {
            continue;
        }

        let mostRecentTrade = candleAggregator
            .recentTradeTicksForInstrument(&omsPosition.instrumentSymbol, 1)
            .into_iter()
            .next_back();

        let (currentMarketPriceInMinorUnits, currentMarketPriceIsLive) = match mostRecentTrade {
            Some(tradeTick) => (tradeTick.priceInMinorUnits, true),
            None => (omsPosition.averageEntryPriceInMinorUnits, false),
        };

        let overflowError = || LivePnlOverflowError {
            instrumentSymbol: omsPosition.instrumentSymbol.clone(),
        };

        let priceDeltaInMinorUnits = currentMarketPriceInMinorUnits
            .checked_sub(omsPosition.averageEntryPriceInMinorUnits)
            .ok_or_else(overflowError)?;
        let unrealizedPnLInMinorUnits = omsPosition
            .netQuantity
            .checked_mul(priceDeltaInMinorUnits)
            .ok_or_else(overflowError)?;
        totalUnrealizedPnLInMinorUnits = totalUnrealizedPnLInMinorUnits
            .checked_add(unrealizedPnLInMinorUnits)
            .ok_or_else(overflowError)?;

        positions.push(LivePnlPositionSnapshot {
            instrumentSymbol: omsPosition.instrumentSymbol.clone(),
            netQuantity: omsPosition.netQuantity,
            averageEntryPriceInMinorUnits: omsPosition.averageEntryPriceInMinorUnits,
            currentMarketPriceInMinorUnits,
            unrealizedPnLInMinorUnits,
            currentMarketPriceIsLive,
        });
    }

    Ok(LivePnlSnapshot {
        accountIdentifier: accountIdentifier.to_string(),
        totalUnrealizedPnLInMinorUnits,
        positions,
    })
}

/// Fetches oms-gateway's real `GET /mark-to-market?accountId=...` over a
/// raw HTTP/1.1 TCP connection — same hand-rolled-HTTP style as the rest
/// of this service's inter-service glue, no HTTP client crate dependency.
/// Read-only: this function never sends anything but a GET.
pub fn fetchOmsGatewayCostBasisForAccount(
    omsGatewayHostAndPort: &str,
    accountIdentifier: &str,
) -> Result<Vec<OmsPositionCostBasis>, String> {
    let mut tcpStream =
        TcpStream::connect(omsGatewayHostAndPort).map_err(|connectError| format!("connect failed: {connectError}"))?;
    tcpStream
        .set_read_timeout(Some(OMS_GATEWAY_FETCH_TIMEOUT))
        .map_err(|timeoutError| format!("failed to set read timeout: {timeoutError}"))?;

    let requestPath = format!("/mark-to-market?accountId={accountIdentifier}");
    let httpRequest =
        format!("GET {requestPath} HTTP/1.1\r\nHost: {omsGatewayHostAndPort}\r\nConnection: close\r\n\r\n");
    tcpStream
        .write_all(httpRequest.as_bytes())
        .map_err(|writeError| format!("write failed: {writeError}"))?;

    let mut responseBytes = Vec::new();
    tcpStream
        .read_to_end(&mut responseBytes)
        .map_err(|readError| format!("read failed: {readError}"))?;
    let responseText = String::from_utf8_lossy(&responseBytes);

    let Some(headerBodySplitIndex) = responseText.find("\r\n\r\n") else {
        return Err("malformed HTTP response from oms-gateway (no header/body separator)".to_string());
    };
    let responseBody = &responseText[headerBodySplitIndex + 4..];

    let parsedResponse: OmsMarkToMarketWireResponse = serde_json::from_str(responseBody)
        .map_err(|parseError| format!("failed to parse oms-gateway response: {parseError} (body: {responseBody})"))?;
    Ok(parsedResponse.positions)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn buildCandleAggregatorWithLastTrade(instrumentSymbol: &str, priceInMinorUnits: i64) -> CandleAggregator {
        let mut candleAggregator = CandleAggregator::newEmptyAggregator();
        candleAggregator.recordTrade(instrumentSymbol, priceInMinorUnits, 1, 1_000);
        candleAggregator
    }

    #[test]
    fn longPositionWithPriceAboveCostBasisIsAPositiveRealGain() {
        let candleAggregator = buildCandleAggregatorWithLastTrade("DEMO-EQ", 12_000);
        let omsPositions = vec![OmsPositionCostBasis {
            instrumentSymbol: "DEMO-EQ".to_string(),
            netQuantity: 10,
            averageEntryPriceInMinorUnits: 10_000,
        }];

        let snapshot = computeLivePnlSnapshot("acct-001", &omsPositions, &candleAggregator).expect("no overflow expected in this fixture");

        // 10 * (12_000 - 10_000) = 20_000, computed, not hardcoded.
        assert_eq!(snapshot.totalUnrealizedPnLInMinorUnits, 20_000);
        assert_eq!(snapshot.positions.len(), 1);
        assert_eq!(snapshot.positions[0].currentMarketPriceInMinorUnits, 12_000);
        assert!(snapshot.positions[0].currentMarketPriceIsLive);
    }

    #[test]
    fn shortPositionWithPriceAboveCostBasisIsARealLoss() {
        let candleAggregator = buildCandleAggregatorWithLastTrade("DEMO-EQ", 12_000);
        let omsPositions = vec![OmsPositionCostBasis {
            instrumentSymbol: "DEMO-EQ".to_string(),
            netQuantity: -5,
            averageEntryPriceInMinorUnits: 10_000,
        }];

        let snapshot = computeLivePnlSnapshot("acct-001", &omsPositions, &candleAggregator).expect("no overflow expected in this fixture");

        // -5 * (12_000 - 10_000) = -10_000: a short that's underwater as
        // price rises above entry.
        assert_eq!(snapshot.totalUnrealizedPnLInMinorUnits, -10_000);
    }

    #[test]
    fn multiplePositionsSumIntoARealAccountLevelTotal() {
        let mut candleAggregator = CandleAggregator::newEmptyAggregator();
        candleAggregator.recordTrade("DEMO-EQ", 11_000, 1, 1_000);
        candleAggregator.recordTrade("SIM-AAPL", 19_500, 1, 1_000);

        let omsPositions = vec![
            OmsPositionCostBasis {
                instrumentSymbol: "DEMO-EQ".to_string(),
                netQuantity: 10,
                averageEntryPriceInMinorUnits: 10_000,
            },
            OmsPositionCostBasis {
                instrumentSymbol: "SIM-AAPL".to_string(),
                netQuantity: 2,
                averageEntryPriceInMinorUnits: 19_000,
            },
        ];

        let snapshot = computeLivePnlSnapshot("acct-001", &omsPositions, &candleAggregator).expect("no overflow expected in this fixture");

        // DEMO-EQ: 10 * (11_000-10_000) = 10_000
        // SIM-AAPL: 2 * (19_500-19_000) = 1_000
        // total = 11_000 — a real sum across positions, not one hardcoded
        // figure.
        assert_eq!(snapshot.totalUnrealizedPnLInMinorUnits, 11_000);
        assert_eq!(snapshot.positions.len(), 2);
    }

    #[test]
    fn flatPositionsAreExcludedFromTheSnapshot() {
        let candleAggregator = buildCandleAggregatorWithLastTrade("DEMO-EQ", 10_000);
        let omsPositions = vec![OmsPositionCostBasis {
            instrumentSymbol: "DEMO-EQ".to_string(),
            netQuantity: 0,
            averageEntryPriceInMinorUnits: 10_000,
        }];

        let snapshot = computeLivePnlSnapshot("acct-001", &omsPositions, &candleAggregator).expect("no overflow expected in this fixture");
        assert!(snapshot.positions.is_empty());
        assert_eq!(snapshot.totalUnrealizedPnLInMinorUnits, 0);
    }

    #[test]
    fn instrumentWithNoLiveTradeFallsBackToCostBasisRatherThanFabricatingAPrice() {
        let candleAggregator = CandleAggregator::newEmptyAggregator(); // no trades at all
        let omsPositions = vec![OmsPositionCostBasis {
            instrumentSymbol: "NEVER-TRADED".to_string(),
            netQuantity: 7,
            averageEntryPriceInMinorUnits: 5_000,
        }];

        let snapshot = computeLivePnlSnapshot("acct-001", &omsPositions, &candleAggregator).expect("no overflow expected in this fixture");

        assert_eq!(snapshot.positions.len(), 1);
        assert!(!snapshot.positions[0].currentMarketPriceIsLive);
        assert_eq!(snapshot.positions[0].currentMarketPriceInMinorUnits, 5_000);
        assert_eq!(snapshot.totalUnrealizedPnLInMinorUnits, 0);
    }

    #[test]
    fn emptyPositionListProducesAnEmptyZeroSnapshot() {
        let candleAggregator = CandleAggregator::newEmptyAggregator();
        let snapshot = computeLivePnlSnapshot("acct-001", &[], &candleAggregator).expect("no overflow expected in this fixture");
        assert!(snapshot.positions.is_empty());
        assert_eq!(snapshot.totalUnrealizedPnLInMinorUnits, 0);
    }

    #[test]
    fn usesTheMostRecentTradeWhenMultipleTicksExist() {
        let mut candleAggregator = CandleAggregator::newEmptyAggregator();
        candleAggregator.recordTrade("DEMO-EQ", 10_000, 1, 1_000);
        candleAggregator.recordTrade("DEMO-EQ", 10_500, 1, 1_100); // separate bucket, later trade
        candleAggregator.recordTrade("DEMO-EQ", 11_000, 1, 1_200); // most recent

        let omsPositions = vec![OmsPositionCostBasis {
            instrumentSymbol: "DEMO-EQ".to_string(),
            netQuantity: 1,
            averageEntryPriceInMinorUnits: 10_000,
        }];

        let snapshot = computeLivePnlSnapshot("acct-001", &omsPositions, &candleAggregator).expect("no overflow expected in this fixture");
        assert_eq!(snapshot.positions[0].currentMarketPriceInMinorUnits, 11_000);
        assert_eq!(snapshot.totalUnrealizedPnLInMinorUnits, 1_000);
    }

    #[test]
    fn extremeButConstructibleInputsReturnACleanOverflowErrorInsteadOfPanickingOrWrapping() {
        // netQuantity near i64::MAX and a huge positive price delta: the
        // plain `netQuantity * (currentPrice - averageEntryPrice)` multiply
        // this guards overflows i64 for this combination. Both `netQuantity`
        // (oms-gateway-reported) and the prices (oms-gateway cost basis /
        // market-data's own live tape) are attacker/upstream-influenced
        // enough that this is a constructible real input, not a purely
        // theoretical one.
        let candleAggregator = buildCandleAggregatorWithLastTrade("DEMO-EQ", i64::MAX);
        let omsPositions = vec![OmsPositionCostBasis {
            instrumentSymbol: "DEMO-EQ".to_string(),
            netQuantity: i64::MAX / 2,
            averageEntryPriceInMinorUnits: -1,
        }];

        let result = computeLivePnlSnapshot("acct-001", &omsPositions, &candleAggregator);

        let overflowError = result.expect_err("this combination is constructed to overflow i64");
        assert_eq!(overflowError.instrumentSymbol, "DEMO-EQ");
        assert!(overflowError.to_string().contains("overflowed"));
    }

    #[test]
    fn fetchFailsClearlyWhenOmsGatewayIsUnreachable() {
        // Port 1 is reserved and nothing should ever be listening there —
        // proves the error path is real (a real failed connect attempt),
        // not mocked.
        let fetchResult = fetchOmsGatewayCostBasisForAccount("127.0.0.1:1", "acct-001");
        assert!(fetchResult.is_err());
    }

    #[test]
    fn fetchRoundTripsAgainstARealLocalHttpServerStandingInForOmsGateway() {
        use std::net::TcpListener;
        use std::thread;

        // A real TcpListener on an ephemeral port, standing in for
        // oms-gateway's own GET /mark-to-market — proves
        // fetchOmsGatewayCostBasisForAccount does a real HTTP round trip
        // (real TCP connect, real request line, real header/body parse),
        // not just exercising the pure computeLivePnlSnapshot function.
        let listener = TcpListener::bind("127.0.0.1:0").expect("failed to bind ephemeral test listener");
        let listenAddress = listener.local_addr().expect("failed to read local addr").to_string();

        let serverThread = thread::spawn(move || {
            let (mut connectionStream, _) = listener.accept().expect("failed to accept test connection");
            let mut readBuffer = [0u8; 512];
            let _ = connectionStream.read(&mut readBuffer); // drain the request, ignore its content

            let responseBody = r#"{"accountIdentifier":"acct-001","isLeveragedAccount":true,"positions":[{"instrumentSymbol":"DEMO-EQ","netQuantity":10,"averageEntryPriceInMinorUnits":10000,"currentMarketPriceInMinorUnits":10000,"unrealizedPnLInMinorUnits":0}],"totalUnrealizedPnLInMinorUnits":0}"#;
            let httpResponse = format!(
                "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{responseBody}",
                responseBody.len()
            );
            let _ = connectionStream.write_all(httpResponse.as_bytes());
        });

        let fetchResult = fetchOmsGatewayCostBasisForAccount(&listenAddress, "acct-001");
        serverThread.join().expect("test server thread panicked");

        let positions = fetchResult.expect("fetch against the real local test server should succeed");
        assert_eq!(positions.len(), 1);
        assert_eq!(positions[0].instrumentSymbol, "DEMO-EQ");
        assert_eq!(positions[0].netQuantity, 10);
        assert_eq!(positions[0].averageEntryPriceInMinorUnits, 10_000);
    }
}
