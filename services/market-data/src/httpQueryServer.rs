// A minimal, hand-rolled HTTP/1.1 server exposing the trade tape, OHLCV
// candles (`candleAggregator.rs`), watchlists (`watchlist.rs`), and price
// alerts (`pricealerts.rs`). Deliberately not pulling in an HTTP
// framework crate — this codebase's convention so far (see
// matching-engine's and market-data's own TCP+JSON bridges) is
// std-library-only raw sockets. Originally GET-only; now also parses
// headers + a request body for the POST endpoints watchlists/alerts
// need.
//
// TODO(real build): a real charting/quote API needs WebSocket streaming
// (ARCHITECTURE.md §5), not polling this HTTP endpoint; this is a
// stopgap so `apps/web` has SOMETHING to point a chart at today.

use std::io::{BufRead, BufReader, Read, Write};
use std::net::TcpListener;
use std::sync::{Arc, Mutex};
use std::time::{SystemTime, UNIX_EPOCH};

use serde::Deserialize;

use crate::candleAggregator::CandleAggregator;
use crate::columnarTickStore::ColumnarTickStore;
use crate::livePnlWidget::{self, computeLivePnlSnapshot};
use crate::orderFlowFootprintAggregator::computeOrderFlowFootprint;
use crate::pricealerts::PriceAlertStore;
use crate::volumeProfileAggregator::{computeTpoProfile, computeVolumeProfile};
use crate::watchlist::WatchlistStore;

/// Default host:port for oms-gateway's real HTTP API — overridable via
/// `MARKET_DATA_OMS_GATEWAY_HTTP_ADDRESS`, same env-var-with-a-hardcoded-
/// default convention as this service's other configuration. `main.rs`
/// reads the env var itself and always calls
/// `newEmptyStateWithOmsGatewayAddress` explicitly, so this constant (and
/// the plain `newEmptyState` convenience constructor below) are only
/// exercised by this module's own test suite — same
/// kept-pub-for-tests-only pattern as `candleAggregator.rs`'s
/// `recordTrade`.
#[allow(dead_code)]
const DEFAULT_OMS_GATEWAY_HTTP_ADDRESS: &str = "127.0.0.1:8081";

/// Default price-bucket width (minor units) for `GET /volumeProfile` and
/// `GET /orderFlowFootprint` when the caller doesn't specify one — a
/// round, demo-friendly number, not tuned to any particular instrument's
/// tick size.
const DEFAULT_PRICE_BUCKET_SIZE_IN_MINOR_UNITS: i64 = 100;
/// Default Value Area volume fraction (70%) — the standard Market Profile
/// convention (FEATURES.md §20).
const DEFAULT_VALUE_AREA_VOLUME_FRACTION: f64 = 0.70;
/// Default TPO letter width (seconds) for `GET /volumeProfile`'s
/// `tpoIntervalSeconds` query param.
const DEFAULT_TPO_INTERVAL_SECONDS: u64 = 60;

const DEFAULT_QUERY_LIMIT: usize = 100;

/// Everything the HTTP query server needs, bundled so `main.rs` only has
/// to pass one thing across the thread boundary. Each field is its own
/// `Mutex` (not one mutex around the whole struct) — the ingestion loop
/// only ever touches `candleAggregator`, `columnarTickStore`, and
/// `priceAlerts`, and there's no reason contention on the watchlist store
/// (touched only by this HTTP server) should ever block it.
pub struct SharedMarketDataState {
    pub candleAggregator: Mutex<CandleAggregator>,
    pub columnarTickStore: ColumnarTickStore,
    pub watchlists: WatchlistStore,
    pub priceAlerts: PriceAlertStore,
    /// `host:port` this process calls out to for `GET /pnl/live`'s
    /// real, read-only fetch of oms-gateway's mark-to-market cost basis
    /// (`livePnlWidget.rs`). Not touched by anything else in this
    /// struct — read-only client state, not shared mutable state.
    pub omsGatewayHttpAddress: String,
}

impl SharedMarketDataState {
    #[allow(dead_code)]
    pub fn newEmptyState() -> Self {
        SharedMarketDataState::newEmptyStateWithOmsGatewayAddress(DEFAULT_OMS_GATEWAY_HTTP_ADDRESS)
    }

    pub fn newEmptyStateWithOmsGatewayAddress(omsGatewayHttpAddress: &str) -> Self {
        SharedMarketDataState {
            candleAggregator: Mutex::new(CandleAggregator::newEmptyAggregator()),
            columnarTickStore: ColumnarTickStore::newEmptyStore(),
            watchlists: WatchlistStore::newEmptyStore(),
            priceAlerts: PriceAlertStore::newEmptyStore(),
            omsGatewayHttpAddress: omsGatewayHttpAddress.to_string(),
        }
    }
}

/// Current wall-clock time as epoch MILLISECONDS (not seconds — see
/// watchlist.rs's module doc for why) — used to stamp real watchlist
/// mutations (`POST /watchlist/add`/`/watchlist/remove`) with a real
/// `lastModifiedAt`/change-log timestamp for the sync-freshness mechanism.
fn currentEpochMillis() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|duration| duration.as_millis() as u64)
        .unwrap_or(0)
}

pub fn runHttpQueryServer(listenAddress: &str, sharedState: Arc<SharedMarketDataState>) {
    let tcpListener = match TcpListener::bind(listenAddress) {
        Ok(listener) => listener,
        Err(bindError) => {
            eprintln!("market-data HTTP query server failed to bind {listenAddress}: {bindError}");
            return;
        }
    };
    println!(
        "market-data HTTP query server listening on {listenAddress} (GET /trades, GET /candles, \
         GET /ticks/range, GET /volumeProfile, GET /orderFlowFootprint, GET/POST /watchlist, \
         GET /watchlist/changes, POST /alerts/create, GET /alerts, GET /pnl/live)"
    );

    for incomingConnection in tcpListener.incoming() {
        let Ok(mut connectionStream) = incomingConnection else {
            continue;
        };

        let Some((requestLine, requestBody)) = readRequestLineAndBody(&connectionStream) else {
            continue;
        };

        let (statusLine, bodyJson) = handleOneHttpRequest(&requestLine, &requestBody, &sharedState);

        let responseBytes = format!(
            "{statusLine}\r\nContent-Type: application/json\r\nAccess-Control-Allow-Origin: *\r\nAccess-Control-Allow-Methods: GET, POST, OPTIONS\r\nAccess-Control-Allow-Headers: Content-Type\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{bodyJson}",
            bodyJson.len()
        );
        let _ = connectionStream.write_all(responseBytes.as_bytes());
    }
}

/// Reads the request line, then headers up to the blank line, then a
/// body of exactly `Content-Length` bytes if present. Returns `None` on
/// any I/O error — the caller just drops the connection, same tolerance
/// as the original GET-only version had for a bad read.
fn readRequestLineAndBody(connectionStream: &std::net::TcpStream) -> Option<(String, String)> {
    let mut bufferedReader = BufReader::new(connectionStream);

    let mut requestLine = String::new();
    bufferedReader.read_line(&mut requestLine).ok()?;

    let mut contentLength: usize = 0;
    loop {
        let mut headerLine = String::new();
        bufferedReader.read_line(&mut headerLine).ok()?;
        let trimmedHeaderLine = headerLine.trim_end();
        if trimmedHeaderLine.is_empty() {
            break; // blank line — end of headers
        }
        if let Some((headerName, headerValue)) = trimmedHeaderLine.split_once(':')
            && headerName.trim().eq_ignore_ascii_case("content-length")
        {
            contentLength = headerValue.trim().parse().unwrap_or(0);
        }
    }

    let mut bodyBytes = vec![0u8; contentLength];
    if contentLength > 0 {
        bufferedReader.read_exact(&mut bodyBytes).ok()?;
    }

    Some((requestLine, String::from_utf8_lossy(&bodyBytes).to_string()))
}

#[derive(Debug, Deserialize)]
struct WatchlistMutationWireRequest {
    accountIdentifier: String,
    instrumentSymbol: String,
    /// Optional caller-supplied identifier for the device/browser session
    /// making this change — purely informational (see `watchlist.rs`'s
    /// module doc), lets a client prove a change came from a different
    /// device than the one it's running on.
    #[serde(default)]
    deviceIdentifier: Option<String>,
}

#[derive(Debug, Deserialize)]
struct CreateAlertWireRequest {
    accountIdentifier: String,
    instrumentSymbol: String,
    isAboveNotBelow: bool,
    thresholdPriceInMinorUnits: i64,
}

/// Parses just enough of an HTTP/1.1 request line (`METHOD /path?query
/// HTTP/1.1`) plus an optional JSON body to route it, and returns
/// `(statusLine, jsonBody)`. Deliberately permissive with unknown routes
/// (404 + empty array/object) rather than strict, since this is mostly a
/// read-only reporting endpoint with a couple of simple mutations, not
/// something worth hardening against malformed input.
fn handleOneHttpRequest(
    requestLine: &str,
    requestBody: &str,
    sharedState: &Arc<SharedMarketDataState>,
) -> (String, String) {
    let mut requestLineParts = requestLine.split_whitespace();
    let httpMethod = requestLineParts.next().unwrap_or("");
    let requestTarget = requestLineParts.next().unwrap_or("");

    if httpMethod == "OPTIONS" {
        return ("HTTP/1.1 204 No Content".to_string(), String::new());
    }

    let (path, queryString) = match requestTarget.split_once('?') {
        Some((pathPart, queryPart)) => (pathPart, queryPart),
        None => (requestTarget, ""),
    };
    let queryParams = parseQueryString(queryString);
    let instrumentSymbol = queryParams.get("instrumentSymbol").cloned().unwrap_or_default();
    let accountIdentifier = queryParams.get("accountIdentifier").cloned().unwrap_or_default();
    let limit = queryParams
        .get("limit")
        .and_then(|value| value.parse::<usize>().ok())
        .unwrap_or(DEFAULT_QUERY_LIMIT);

    match (httpMethod, path) {
        ("GET", "/trades") | ("GET", "/candles") => {
            if instrumentSymbol.is_empty() {
                return (
                    "HTTP/1.1 400 Bad Request".to_string(),
                    r#"{"errorMessage":"instrumentSymbol query parameter is required"}"#.to_string(),
                );
            }
            let aggregator = sharedState
                .candleAggregator
                .lock()
                .expect("candle aggregator mutex poisoned");
            let bodyJson = if path == "/trades" {
                serde_json::to_string(&aggregator.recentTradeTicksForInstrument(&instrumentSymbol, limit))
            } else {
                serde_json::to_string(&aggregator.recentCandlesForInstrument(&instrumentSymbol, limit))
            };
            (
                "HTTP/1.1 200 OK".to_string(),
                bodyJson.unwrap_or_else(|_| "[]".to_string()),
            )
        }

        ("GET", "/ticks/range") => {
            if instrumentSymbol.is_empty() {
                return (
                    "HTTP/1.1 400 Bad Request".to_string(),
                    r#"{"errorMessage":"instrumentSymbol query parameter is required"}"#.to_string(),
                );
            }
            // Defaults to "the whole retained history" when either bound
            // is omitted, so `GET /ticks/range?instrumentSymbol=X` alone
            // is a convenient "give me everything you have" query.
            let startEpochSeconds = queryParams
                .get("startEpochSeconds")
                .and_then(|value| value.parse::<u64>().ok())
                .unwrap_or(u64::MIN);
            let endEpochSeconds = queryParams
                .get("endEpochSeconds")
                .and_then(|value| value.parse::<u64>().ok())
                .unwrap_or(u64::MAX);
            let ticks = sharedState
                .columnarTickStore
                .rangeQuery(&instrumentSymbol, startEpochSeconds, endEpochSeconds);
            (
                "HTTP/1.1 200 OK".to_string(),
                serde_json::to_string(&ticks).unwrap_or_else(|_| "[]".to_string()),
            )
        }

        ("GET", "/volumeProfile") => {
            if instrumentSymbol.is_empty() {
                return (
                    "HTTP/1.1 400 Bad Request".to_string(),
                    r#"{"errorMessage":"instrumentSymbol query parameter is required"}"#.to_string(),
                );
            }
            let startEpochSeconds = queryParams
                .get("startEpochSeconds")
                .and_then(|value| value.parse::<u64>().ok())
                .unwrap_or(u64::MIN);
            let endEpochSeconds = queryParams
                .get("endEpochSeconds")
                .and_then(|value| value.parse::<u64>().ok())
                .unwrap_or(u64::MAX);
            let priceBucketSizeInMinorUnits = queryParams
                .get("priceBucketSizeInMinorUnits")
                .and_then(|value| value.parse::<i64>().ok())
                .unwrap_or(DEFAULT_PRICE_BUCKET_SIZE_IN_MINOR_UNITS);
            let valueAreaVolumeFraction = queryParams
                .get("valueAreaVolumeFraction")
                .and_then(|value| value.parse::<f64>().ok())
                .unwrap_or(DEFAULT_VALUE_AREA_VOLUME_FRACTION);
            let tpoIntervalSeconds = queryParams
                .get("tpoIntervalSeconds")
                .and_then(|value| value.parse::<u64>().ok())
                .unwrap_or(DEFAULT_TPO_INTERVAL_SECONDS);

            let ticks = sharedState
                .columnarTickStore
                .rangeQuery(&instrumentSymbol, startEpochSeconds, endEpochSeconds);
            let volumeProfile = computeVolumeProfile(&ticks, priceBucketSizeInMinorUnits, valueAreaVolumeFraction);
            let tpoProfile = computeTpoProfile(&ticks, priceBucketSizeInMinorUnits, tpoIntervalSeconds);

            let bodyJson = serde_json::json!({
                "instrumentSymbol": instrumentSymbol,
                "tickCount": ticks.len(),
                "volumeProfile": volumeProfile,
                "tpoProfile": tpoProfile,
            });
            ("HTTP/1.1 200 OK".to_string(), bodyJson.to_string())
        }

        ("GET", "/orderFlowFootprint") => {
            if instrumentSymbol.is_empty() {
                return (
                    "HTTP/1.1 400 Bad Request".to_string(),
                    r#"{"errorMessage":"instrumentSymbol query parameter is required"}"#.to_string(),
                );
            }
            let startEpochSeconds = queryParams
                .get("startEpochSeconds")
                .and_then(|value| value.parse::<u64>().ok())
                .unwrap_or(u64::MIN);
            let endEpochSeconds = queryParams
                .get("endEpochSeconds")
                .and_then(|value| value.parse::<u64>().ok())
                .unwrap_or(u64::MAX);
            let priceBucketSizeInMinorUnits = queryParams
                .get("priceBucketSizeInMinorUnits")
                .and_then(|value| value.parse::<i64>().ok())
                .unwrap_or(DEFAULT_PRICE_BUCKET_SIZE_IN_MINOR_UNITS);
            let candleIntervalSeconds = queryParams
                .get("candleIntervalSeconds")
                .and_then(|value| value.parse::<u64>().ok())
                .unwrap_or(crate::candleAggregator::CANDLE_INTERVAL_SECONDS);

            let ticks = sharedState
                .columnarTickStore
                .rangeQuery(&instrumentSymbol, startEpochSeconds, endEpochSeconds);
            let footprint = computeOrderFlowFootprint(&ticks, priceBucketSizeInMinorUnits, candleIntervalSeconds);

            (
                "HTTP/1.1 200 OK".to_string(),
                serde_json::to_string(&footprint).unwrap_or_else(|_| "[]".to_string()),
            )
        }

        ("GET", "/watchlist") => {
            if accountIdentifier.is_empty() {
                return (
                    "HTTP/1.1 400 Bad Request".to_string(),
                    r#"{"errorMessage":"accountIdentifier query parameter is required"}"#.to_string(),
                );
            }
            let symbols = sharedState.watchlists.symbolsForAccount(&accountIdentifier);
            let lastModifiedAtEpochMillis = sharedState
                .watchlists
                .lastModifiedAtEpochMillisForAccount(&accountIdentifier);
            let bodyJson = serde_json::json!({
                "accountIdentifier": accountIdentifier,
                "symbols": symbols,
                "lastModifiedAtEpochMillis": lastModifiedAtEpochMillis,
            });
            ("HTTP/1.1 200 OK".to_string(), bodyJson.to_string())
        }
        ("POST", "/watchlist/add") => match serde_json::from_str::<WatchlistMutationWireRequest>(requestBody) {
            Ok(wireRequest) => {
                let wasAdded = sharedState.watchlists.addSymbol(
                    &wireRequest.accountIdentifier,
                    &wireRequest.instrumentSymbol,
                    wireRequest.deviceIdentifier.as_deref(),
                    currentEpochMillis(),
                );
                ("HTTP/1.1 200 OK".to_string(), format!(r#"{{"wasAdded":{wasAdded}}}"#))
            }
            Err(_) => malformedJsonBodyResponse(),
        },
        ("POST", "/watchlist/remove") => match serde_json::from_str::<WatchlistMutationWireRequest>(requestBody) {
            Ok(wireRequest) => {
                let wasRemoved = sharedState.watchlists.removeSymbol(
                    &wireRequest.accountIdentifier,
                    &wireRequest.instrumentSymbol,
                    wireRequest.deviceIdentifier.as_deref(),
                    currentEpochMillis(),
                );
                (
                    "HTTP/1.1 200 OK".to_string(),
                    format!(r#"{{"wasRemoved":{wasRemoved}}}"#),
                )
            }
            Err(_) => malformedJsonBodyResponse(),
        },

        // Cross-device sync-freshness: "what changed since I last synced"
        // — see watchlist.rs's module doc. sinceEpochMillis defaults to
        // 0 (everything) when omitted.
        ("GET", "/watchlist/changes") => {
            if accountIdentifier.is_empty() {
                return (
                    "HTTP/1.1 400 Bad Request".to_string(),
                    r#"{"errorMessage":"accountIdentifier query parameter is required"}"#.to_string(),
                );
            }
            let sinceEpochMillis = queryParams
                .get("sinceEpochMillis")
                .and_then(|value| value.parse::<u64>().ok())
                .unwrap_or(0);
            let changes = sharedState
                .watchlists
                .changesForAccountSince(&accountIdentifier, sinceEpochMillis);
            let lastModifiedAtEpochMillis = sharedState
                .watchlists
                .lastModifiedAtEpochMillisForAccount(&accountIdentifier);
            let bodyJson = serde_json::json!({
                "accountIdentifier": accountIdentifier,
                "sinceEpochMillis": sinceEpochMillis,
                "changes": changes,
                "lastModifiedAtEpochMillis": lastModifiedAtEpochMillis,
            });
            ("HTTP/1.1 200 OK".to_string(), bodyJson.to_string())
        }

        ("GET", "/alerts") => {
            if accountIdentifier.is_empty() {
                return (
                    "HTTP/1.1 400 Bad Request".to_string(),
                    r#"{"errorMessage":"accountIdentifier query parameter is required"}"#.to_string(),
                );
            }
            let alerts = sharedState.priceAlerts.alertsForAccount(&accountIdentifier);
            (
                "HTTP/1.1 200 OK".to_string(),
                serde_json::to_string(&alerts).unwrap_or_else(|_| "[]".to_string()),
            )
        }
        ("POST", "/alerts/create") => match serde_json::from_str::<CreateAlertWireRequest>(requestBody) {
            Ok(wireRequest) => {
                let alertId = sharedState.priceAlerts.createAlert(
                    &wireRequest.accountIdentifier,
                    &wireRequest.instrumentSymbol,
                    wireRequest.isAboveNotBelow,
                    wireRequest.thresholdPriceInMinorUnits,
                );
                ("HTTP/1.1 200 OK".to_string(), format!(r#"{{"alertId":{alertId}}}"#))
            }
            Err(_) => malformedJsonBodyResponse(),
        },

        // Home-screen live P&L widget (FEATURES.md §21) — a real,
        // read-only fetch of oms-gateway's mark-to-market cost basis
        // combined with market-data's OWN live trade tape. See
        // livePnlWidget.rs's module doc for the full contract and its
        // one documented upstream limitation.
        ("GET", "/pnl/live") => {
            if accountIdentifier.is_empty() {
                return (
                    "HTTP/1.1 400 Bad Request".to_string(),
                    r#"{"errorMessage":"accountIdentifier query parameter is required"}"#.to_string(),
                );
            }
            match livePnlWidget::fetchOmsGatewayCostBasisForAccount(
                &sharedState.omsGatewayHttpAddress,
                &accountIdentifier,
            ) {
                Ok(omsPositions) => {
                    let candleAggregator = sharedState
                        .candleAggregator
                        .lock()
                        .expect("candle aggregator mutex poisoned");
                    let snapshot = computeLivePnlSnapshot(&accountIdentifier, &omsPositions, &candleAggregator);
                    (
                        "HTTP/1.1 200 OK".to_string(),
                        serde_json::to_string(&snapshot).unwrap_or_else(|_| "{}".to_string()),
                    )
                }
                Err(fetchError) => (
                    "HTTP/1.1 502 Bad Gateway".to_string(),
                    serde_json::json!({
                        "errorMessage": format!("failed to fetch cost basis from oms-gateway: {fetchError}"),
                        "omsGatewayHttpAddress": sharedState.omsGatewayHttpAddress,
                    })
                    .to_string(),
                ),
            }
        }

        _ => ("HTTP/1.1 404 Not Found".to_string(), "[]".to_string()),
    }
}

fn malformedJsonBodyResponse() -> (String, String) {
    (
        "HTTP/1.1 400 Bad Request".to_string(),
        r#"{"errorMessage":"malformed JSON request body"}"#.to_string(),
    )
}

/// Tiny `a=1&b=2` parser — deliberately not URL-decoding percent escapes
/// since instrument symbols and integers never need them in this
/// skeleton. A real build should use a proper URL/query parsing crate.
fn parseQueryString(queryString: &str) -> std::collections::HashMap<String, String> {
    queryString
        .split('&')
        .filter_map(|keyValuePair| keyValuePair.split_once('='))
        .map(|(key, value)| (key.to_string(), value.to_string()))
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parsesMultipleQueryParameters() {
        let params = parseQueryString("instrumentSymbol=DEMO-EQ&limit=5");
        assert_eq!(params.get("instrumentSymbol"), Some(&"DEMO-EQ".to_string()));
        assert_eq!(params.get("limit"), Some(&"5".to_string()));
    }

    #[test]
    fn candlesRouteReturnsRecordedCandleAsJson() {
        let sharedState = Arc::new(SharedMarketDataState::newEmptyState());
        sharedState
            .candleAggregator
            .lock()
            .unwrap()
            .recordTrade("DEMO-EQ", 100, 5, 1_000);

        let (statusLine, bodyJson) =
            handleOneHttpRequest("GET /candles?instrumentSymbol=DEMO-EQ HTTP/1.1", "", &sharedState);

        assert_eq!(statusLine, "HTTP/1.1 200 OK");
        assert!(bodyJson.contains("\"openPriceInMinorUnits\":100"));
    }

    #[test]
    fn missingInstrumentSymbolReturns400() {
        let sharedState = Arc::new(SharedMarketDataState::newEmptyState());
        let (statusLine, _bodyJson) = handleOneHttpRequest("GET /trades HTTP/1.1", "", &sharedState);
        assert_eq!(statusLine, "HTTP/1.1 400 Bad Request");
    }

    #[test]
    fn unknownPathReturns404() {
        let sharedState = Arc::new(SharedMarketDataState::newEmptyState());
        let (statusLine, _bodyJson) = handleOneHttpRequest("GET /nope HTTP/1.1", "", &sharedState);
        assert_eq!(statusLine, "HTTP/1.1 404 Not Found");
    }

    #[test]
    fn watchlistAddThenGetRoundTrips() {
        let sharedState = Arc::new(SharedMarketDataState::newEmptyState());
        let (addStatus, addBody) = handleOneHttpRequest(
            "POST /watchlist/add HTTP/1.1",
            r#"{"accountIdentifier":"acct-001","instrumentSymbol":"DEMO-EQ"}"#,
            &sharedState,
        );
        assert_eq!(addStatus, "HTTP/1.1 200 OK");
        assert_eq!(addBody, r#"{"wasAdded":true}"#);

        let (getStatus, getBody) =
            handleOneHttpRequest("GET /watchlist?accountIdentifier=acct-001 HTTP/1.1", "", &sharedState);
        assert_eq!(getStatus, "HTTP/1.1 200 OK");
        assert!(getBody.contains(r#""symbols":["DEMO-EQ"]"#));
        assert!(!getBody.contains("\"lastModifiedAtEpochMillis\":0")); // a real epoch stamp was recorded
    }

    #[test]
    fn watchlistAddWithADeviceIdentifierIsVisibleFromADifferentQueryContextForTheSameAccount() {
        // Proves the cross-device sync story end to end over the real
        // HTTP routing layer: a mutation tagged with one device
        // identifier is immediately visible to a plain GET (standing in
        // for a different device's session) for the same account.
        let sharedState = Arc::new(SharedMarketDataState::newEmptyState());
        let (addStatus, _addBody) = handleOneHttpRequest(
            "POST /watchlist/add HTTP/1.1",
            r#"{"accountIdentifier":"acct-001","instrumentSymbol":"DEMO-EQ","deviceIdentifier":"device-phone"}"#,
            &sharedState,
        );
        assert_eq!(addStatus, "HTTP/1.1 200 OK");

        let (getStatus, getBody) =
            handleOneHttpRequest("GET /watchlist?accountIdentifier=acct-001 HTTP/1.1", "", &sharedState);
        assert_eq!(getStatus, "HTTP/1.1 200 OK");
        assert!(getBody.contains(r#""symbols":["DEMO-EQ"]"#));
    }

    #[test]
    fn watchlistChangesSinceReturnsOnlyTheRealDeltaAfterAGivenTimestamp() {
        let sharedState = Arc::new(SharedMarketDataState::newEmptyState());
        handleOneHttpRequest(
            "POST /watchlist/add HTTP/1.1",
            r#"{"accountIdentifier":"acct-001","instrumentSymbol":"AAPL","deviceIdentifier":"device-A"}"#,
            &sharedState,
        );

        // A full sync: read the current lastModifiedAtEpochMillis as the
        // client's own "synced as of" marker.
        let (_status, firstGetBody) =
            handleOneHttpRequest("GET /watchlist?accountIdentifier=acct-001 HTTP/1.1", "", &sharedState);
        let firstSnapshot: serde_json::Value = serde_json::from_str(&firstGetBody).unwrap();
        let syncedAsOf = firstSnapshot["lastModifiedAtEpochMillis"].as_u64().unwrap();

        // Immediately since its own timestamp: nothing new yet.
        let (_status, noChangesBody) = handleOneHttpRequest(
            &format!("GET /watchlist/changes?accountIdentifier=acct-001&sinceEpochMillis={syncedAsOf} HTTP/1.1"),
            "",
            &sharedState,
        );
        assert!(noChangesBody.contains("\"changes\":[]"));

        // A second, later real change from a DIFFERENT device. A tiny
        // real sleep guarantees a distinct millisecond timestamp from the
        // first change, even on a very fast machine — this test asserts
        // on real wall-clock timestamps, not injected/mocked ones.
        std::thread::sleep(std::time::Duration::from_millis(5));
        handleOneHttpRequest(
            "POST /watchlist/add HTTP/1.1",
            r#"{"accountIdentifier":"acct-001","instrumentSymbol":"MSFT","deviceIdentifier":"device-desktop"}"#,
            &sharedState,
        );

        let (changesStatus, changesBody) = handleOneHttpRequest(
            &format!("GET /watchlist/changes?accountIdentifier=acct-001&sinceEpochMillis={syncedAsOf} HTTP/1.1"),
            "",
            &sharedState,
        );
        assert_eq!(changesStatus, "HTTP/1.1 200 OK");
        assert!(changesBody.contains("\"instrumentSymbol\":\"MSFT\""));
        assert!(changesBody.contains("\"wasAdded\":true"));
        assert!(changesBody.contains("\"deviceIdentifier\":\"device-desktop\""));
        assert!(!changesBody.contains("\"instrumentSymbol\":\"AAPL\"")); // outside the delta window
    }

    #[test]
    fn watchlistChangesRouteWithoutAccountIdentifierReturns400() {
        let sharedState = Arc::new(SharedMarketDataState::newEmptyState());
        let (statusLine, _) = handleOneHttpRequest("GET /watchlist/changes HTTP/1.1", "", &sharedState);
        assert_eq!(statusLine, "HTTP/1.1 400 Bad Request");
    }

    #[test]
    fn alertCreateThenGetRoundTrips() {
        let sharedState = Arc::new(SharedMarketDataState::newEmptyState());
        let (createStatus, createBody) = handleOneHttpRequest(
            "POST /alerts/create HTTP/1.1",
            r#"{"accountIdentifier":"acct-001","instrumentSymbol":"DEMO-EQ","isAboveNotBelow":true,"thresholdPriceInMinorUnits":100}"#,
            &sharedState,
        );
        assert_eq!(createStatus, "HTTP/1.1 200 OK");
        assert!(createBody.contains("\"alertId\":1"));

        let (getStatus, getBody) =
            handleOneHttpRequest("GET /alerts?accountIdentifier=acct-001 HTTP/1.1", "", &sharedState);
        assert_eq!(getStatus, "HTTP/1.1 200 OK");
        assert!(getBody.contains("\"isTriggered\":false"));
    }

    #[test]
    fn volumeProfileRouteWithoutInstrumentSymbolReturns400() {
        let sharedState = Arc::new(SharedMarketDataState::newEmptyState());
        let (statusLine, _) = handleOneHttpRequest("GET /volumeProfile HTTP/1.1", "", &sharedState);
        assert_eq!(statusLine, "HTTP/1.1 400 Bad Request");
    }

    #[test]
    fn volumeProfileRouteReturnsARealComputedProfileWithPocAndValueArea() {
        let sharedState = Arc::new(SharedMarketDataState::newEmptyState());
        // Same hand-worked fixture as volumeProfileAggregator.rs's own
        // tests: bucket 100 gets 10, bucket 110 gets 10 -> POC=100 (tie
        // broken low), value area [100,110] at the default 70% fraction.
        sharedState.columnarTickStore.appendTick("DEMO-EQ", 1_000, 100, 5);
        sharedState.columnarTickStore.appendTick("DEMO-EQ", 1_010, 100, 5);
        sharedState.columnarTickStore.appendTick("DEMO-EQ", 1_020, 110, 3);
        sharedState.columnarTickStore.appendTick("DEMO-EQ", 1_030, 110, 3);
        sharedState.columnarTickStore.appendTick("DEMO-EQ", 1_040, 110, 4);
        sharedState.columnarTickStore.appendTick("DEMO-EQ", 1_050, 120, 2);

        let (statusLine, bodyJson) = handleOneHttpRequest(
            "GET /volumeProfile?instrumentSymbol=DEMO-EQ&priceBucketSizeInMinorUnits=10 HTTP/1.1",
            "",
            &sharedState,
        );

        assert_eq!(statusLine, "HTTP/1.1 200 OK");
        assert!(bodyJson.contains("\"pointOfControlPriceBucketStart\":100"));
        assert!(bodyJson.contains("\"valueAreaLowPriceBucketStart\":100"));
        assert!(bodyJson.contains("\"valueAreaHighPriceBucketStart\":110"));
        assert!(bodyJson.contains("\"tpoProfile\""));
        assert!(bodyJson.contains("\"tickCount\":6"));
    }

    #[test]
    fn orderFlowFootprintRouteWithoutInstrumentSymbolReturns400() {
        let sharedState = Arc::new(SharedMarketDataState::newEmptyState());
        let (statusLine, _) = handleOneHttpRequest("GET /orderFlowFootprint HTTP/1.1", "", &sharedState);
        assert_eq!(statusLine, "HTTP/1.1 400 Bad Request");
    }

    #[test]
    fn orderFlowFootprintRouteReturnsRealBuySellSplitPerPriceLevel() {
        let sharedState = Arc::new(SharedMarketDataState::newEmptyState());
        sharedState
            .columnarTickStore
            .appendTickWithAggressorSide("DEMO-EQ", 1_000, 100, 5, true);
        sharedState
            .columnarTickStore
            .appendTickWithAggressorSide("DEMO-EQ", 1_005, 100, 3, false);

        let (statusLine, bodyJson) = handleOneHttpRequest(
            "GET /orderFlowFootprint?instrumentSymbol=DEMO-EQ&priceBucketSizeInMinorUnits=10&candleIntervalSeconds=60 HTTP/1.1",
            "",
            &sharedState,
        );

        assert_eq!(statusLine, "HTTP/1.1 200 OK");
        assert!(bodyJson.contains("\"buyVolume\":5"));
        assert!(bodyJson.contains("\"sellVolume\":3"));
    }

    #[test]
    fn ticksRangeRouteReturnsAppendedTicksAsJson() {
        let sharedState = Arc::new(SharedMarketDataState::newEmptyState());
        sharedState.columnarTickStore.appendTick("DEMO-EQ", 100, 10_000, 5);

        let (statusLine, bodyJson) =
            handleOneHttpRequest("GET /ticks/range?instrumentSymbol=DEMO-EQ HTTP/1.1", "", &sharedState);

        assert_eq!(statusLine, "HTTP/1.1 200 OK");
        assert!(bodyJson.contains("\"priceInMinorUnits\":10000"));
    }

    #[test]
    fn ticksRangeRouteRespectsExplicitBounds() {
        let sharedState = Arc::new(SharedMarketDataState::newEmptyState());
        sharedState.columnarTickStore.appendTick("DEMO-EQ", 100, 1, 1);
        sharedState.columnarTickStore.appendTick("DEMO-EQ", 500, 2, 1);

        let (statusLine, bodyJson) = handleOneHttpRequest(
            "GET /ticks/range?instrumentSymbol=DEMO-EQ&startEpochSeconds=0&endEpochSeconds=200 HTTP/1.1",
            "",
            &sharedState,
        );

        assert_eq!(statusLine, "HTTP/1.1 200 OK");
        assert!(bodyJson.contains("\"executedAtEpochSeconds\":100"));
        assert!(!bodyJson.contains("\"executedAtEpochSeconds\":500"));
    }

    #[test]
    fn ticksRangeRouteWithoutInstrumentSymbolReturns400() {
        let sharedState = Arc::new(SharedMarketDataState::newEmptyState());
        let (statusLine, _bodyJson) = handleOneHttpRequest("GET /ticks/range HTTP/1.1", "", &sharedState);
        assert_eq!(statusLine, "HTTP/1.1 400 Bad Request");
    }

    #[test]
    fn malformedPostBodyReturns400() {
        let sharedState = Arc::new(SharedMarketDataState::newEmptyState());
        let (statusLine, _bodyJson) = handleOneHttpRequest("POST /watchlist/add HTTP/1.1", "not json", &sharedState);
        assert_eq!(statusLine, "HTTP/1.1 400 Bad Request");
    }

    #[test]
    fn optionsRequestGetsA204WithNoBody() {
        let sharedState = Arc::new(SharedMarketDataState::newEmptyState());
        let (statusLine, bodyJson) = handleOneHttpRequest("OPTIONS /watchlist/add HTTP/1.1", "", &sharedState);
        assert_eq!(statusLine, "HTTP/1.1 204 No Content");
        assert_eq!(bodyJson, "");
    }

    // -------------------------------------------------------------
    // GET /pnl/live — the home-screen live P&L widget endpoint.
    // -------------------------------------------------------------

    #[test]
    fn pnlLiveRouteWithoutAccountIdentifierReturns400() {
        let sharedState = Arc::new(SharedMarketDataState::newEmptyState());
        let (statusLine, _) = handleOneHttpRequest("GET /pnl/live HTTP/1.1", "", &sharedState);
        assert_eq!(statusLine, "HTTP/1.1 400 Bad Request");
    }

    #[test]
    fn pnlLiveRouteReturns502WhenOmsGatewayIsUnreachable() {
        // Port 1 is reserved — nothing should ever be listening there, so
        // this exercises a real failed outbound connection, not a mock.
        let sharedState = Arc::new(SharedMarketDataState::newEmptyStateWithOmsGatewayAddress("127.0.0.1:1"));
        let (statusLine, bodyJson) =
            handleOneHttpRequest("GET /pnl/live?accountIdentifier=acct-001 HTTP/1.1", "", &sharedState);
        assert_eq!(statusLine, "HTTP/1.1 502 Bad Gateway");
        assert!(bodyJson.contains("errorMessage"));
    }

    #[test]
    fn pnlLiveRouteComputesARealSnapshotFromARealLocalStandInForOmsGatewayAndMarketDatasOwnLiveTrades() {
        use std::net::TcpListener;
        use std::thread;

        // A real local HTTP server standing in for oms-gateway's GET
        // /mark-to-market — proves the FULL round trip through this
        // route: a real outbound HTTP call, a real JSON parse, and a
        // real join against market-data's OWN candle aggregator state
        // (seeded below with a real trade), not a hardcoded number.
        let listener = TcpListener::bind("127.0.0.1:0").expect("failed to bind ephemeral test listener");
        let listenAddress = listener.local_addr().expect("failed to read local addr").to_string();

        let serverThread = thread::spawn(move || {
            let (mut connectionStream, _) = listener.accept().expect("failed to accept test connection");
            let mut readBuffer = [0u8; 512];
            let _ = connectionStream.read(&mut readBuffer);

            let responseBody = r#"{"accountIdentifier":"acct-001","isLeveragedAccount":true,"positions":[{"instrumentSymbol":"DEMO-EQ","netQuantity":10,"averageEntryPriceInMinorUnits":10000,"currentMarketPriceInMinorUnits":10000,"unrealizedPnLInMinorUnits":0}],"totalUnrealizedPnLInMinorUnits":0}"#;
            let httpResponse = format!(
                "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{responseBody}",
                responseBody.len()
            );
            let _ = connectionStream.write_all(httpResponse.as_bytes());
        });

        let sharedState = Arc::new(SharedMarketDataState::newEmptyStateWithOmsGatewayAddress(
            &listenAddress,
        ));
        // market-data's OWN real live price for DEMO-EQ, independent of
        // whatever oms-gateway's stub response said.
        sharedState
            .candleAggregator
            .lock()
            .unwrap()
            .recordTrade("DEMO-EQ", 12_000, 1, 1_000);

        let (statusLine, bodyJson) =
            handleOneHttpRequest("GET /pnl/live?accountIdentifier=acct-001 HTTP/1.1", "", &sharedState);
        serverThread.join().expect("test server thread panicked");

        assert_eq!(statusLine, "HTTP/1.1 200 OK");
        // 10 * (12_000 - 10_000) = 20_000 — computed from the real join,
        // not the stub's own (deliberately wrong/zero) unrealizedPnL.
        assert!(bodyJson.contains("\"totalUnrealizedPnLInMinorUnits\":20000"));
        assert!(bodyJson.contains("\"currentMarketPriceInMinorUnits\":12000"));
        assert!(bodyJson.contains("\"currentMarketPriceIsLive\":true"));
    }
}
