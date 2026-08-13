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

use serde::Deserialize;

use crate::candleAggregator::CandleAggregator;
use crate::columnarTickStore::ColumnarTickStore;
use crate::pricealerts::PriceAlertStore;
use crate::watchlist::WatchlistStore;

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
}

impl SharedMarketDataState {
    pub fn newEmptyState() -> Self {
        SharedMarketDataState {
            candleAggregator: Mutex::new(CandleAggregator::newEmptyAggregator()),
            columnarTickStore: ColumnarTickStore::newEmptyStore(),
            watchlists: WatchlistStore::newEmptyStore(),
            priceAlerts: PriceAlertStore::newEmptyStore(),
        }
    }
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
         GET /ticks/range, GET/POST /watchlist, POST /alerts/create, GET /alerts)"
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

        ("GET", "/watchlist") => {
            if accountIdentifier.is_empty() {
                return (
                    "HTTP/1.1 400 Bad Request".to_string(),
                    r#"{"errorMessage":"accountIdentifier query parameter is required"}"#.to_string(),
                );
            }
            let symbols = sharedState.watchlists.symbolsForAccount(&accountIdentifier);
            (
                "HTTP/1.1 200 OK".to_string(),
                serde_json::to_string(&symbols).unwrap_or_else(|_| "[]".to_string()),
            )
        }
        ("POST", "/watchlist/add") => match serde_json::from_str::<WatchlistMutationWireRequest>(requestBody) {
            Ok(wireRequest) => {
                let wasAdded = sharedState
                    .watchlists
                    .addSymbol(&wireRequest.accountIdentifier, &wireRequest.instrumentSymbol);
                ("HTTP/1.1 200 OK".to_string(), format!(r#"{{"wasAdded":{wasAdded}}}"#))
            }
            Err(_) => malformedJsonBodyResponse(),
        },
        ("POST", "/watchlist/remove") => match serde_json::from_str::<WatchlistMutationWireRequest>(requestBody) {
            Ok(wireRequest) => {
                let wasRemoved = sharedState
                    .watchlists
                    .removeSymbol(&wireRequest.accountIdentifier, &wireRequest.instrumentSymbol);
                (
                    "HTTP/1.1 200 OK".to_string(),
                    format!(r#"{{"wasRemoved":{wasRemoved}}}"#),
                )
            }
            Err(_) => malformedJsonBodyResponse(),
        },

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
        assert_eq!(getBody, r#"["DEMO-EQ"]"#);
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
}
