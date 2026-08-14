// FEATURES.md §20 "[P4] Historical DOM replay for a chosen instrument/time
// window". A small hand-rolled HTTP/1.1 GET server — same std-library-only
// raw-socket style as market-data's `httpQueryServer.rs` — exposing:
//
//   GET /domReplay?instrumentSymbol=...&startEpochMillis=...&endEpochMillis=...
//
// WHY THIS LIVES IN MATCHING-ENGINE, NOT MARKET-DATA: the write-ahead log
// (`writeAheadLog.rs`) and the deterministic replay machinery
// (`replayWalEventRecordsCollectingDepthSnapshotsInWindow`) both already
// live here, right next to `OrderBookCore` — matching-engine is the
// service that owns the WAL file and the only process with a real
// `OrderBookCore` implementation to replay commands against. market-data
// has neither: it only ever sees post-hoc depth-delta PUBLISHES over TCP,
// never the underlying order/cancel command stream a WAL needs to reissue.
// Duplicating `OrderBookCore` + the WAL format into market-data just to
// answer this one query would mean two independent (and inevitably
// drifting) reimplementations of price-time-priority matching — building
// the endpoint here instead genuinely REUSES the existing WAL + replay
// code (this module adds zero new replay logic of its own, only HTTP
// plumbing) rather than reinventing it, exactly as the task requires.
//
// This is read-only and never touches the live `WalBackedOrderBook` the
// matching-core thread owns — every request re-reads the WAL file fresh
// off disk (`readAllEventRecordsFromWalFile`) and replays it from
// scratch. That's deliberate: the WAL file is fsync'd durable storage
// (see `writeAheadLog.rs`'s module docs), so reading it fresh is always
// at least as up to date as anything the running process could report
// without adding a second consumer of the single-writer `OrderBookCore`
// (ARCHITECTURE.md §3.1) — no shared mutable state, no locking, no risk
// of this diagnostic endpoint ever blocking or racing the hot order-
// processing path.
//
// TODO(real build): re-reading and fully re-replaying the WAL from byte
// zero on every request is O(total WAL size) per call — fine for a
// skeleton/demo WAL (thousands of events), not for a venue that has been
// running for months. A real build would maintain periodic snapshots
// (checkpoints) and only replay the tail since the nearest one.

use std::io::{BufRead, BufReader, Write};
use std::net::TcpListener;
use std::path::{Path, PathBuf};

use crate::writeAheadLog::{
    readAllEventRecordsFromWalFile, replayWalEventRecordsCollectingDepthSnapshotsInWindow,
};

pub fn runDomReplayHttpServer(
    listenAddress: &str,
    walFilePath: PathBuf,
    tradedInstrumentSymbol: &'static str,
) {
    let tcpListener = match TcpListener::bind(listenAddress) {
        Ok(listener) => listener,
        Err(bindError) => {
            eprintln!(
                "matching-engine DOM replay HTTP server failed to bind {listenAddress}: {bindError}"
            );
            return;
        }
    };
    println!(
        "matching-engine DOM replay HTTP server listening on {listenAddress} \
         (GET /domReplay?instrumentSymbol=...&startEpochMillis=...&endEpochMillis=...)"
    );

    for incomingConnection in tcpListener.incoming() {
        let Ok(mut connectionStream) = incomingConnection else {
            continue;
        };

        let mut requestLine = String::new();
        if BufReader::new(&connectionStream)
            .read_line(&mut requestLine)
            .is_err()
        {
            continue;
        }

        let (statusLine, bodyJson) =
            handleOneHttpRequest(&requestLine, &walFilePath, tradedInstrumentSymbol);

        let responseBytes = format!(
            "{statusLine}\r\nContent-Type: application/json\r\nAccess-Control-Allow-Origin: *\r\nAccess-Control-Allow-Methods: GET, OPTIONS\r\nAccess-Control-Allow-Headers: Content-Type\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{bodyJson}",
            bodyJson.len()
        );
        let _ = connectionStream.write_all(responseBytes.as_bytes());
    }
}

fn handleOneHttpRequest(
    requestLine: &str,
    walFilePath: &Path,
    tradedInstrumentSymbol: &str,
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

    if (httpMethod, path) != ("GET", "/domReplay") {
        return ("HTTP/1.1 404 Not Found".to_string(), "[]".to_string());
    }

    let queryParams = parseQueryString(queryString);
    let instrumentSymbol = queryParams
        .get("instrumentSymbol")
        .cloned()
        .unwrap_or_default();
    if instrumentSymbol.is_empty() {
        return (
            "HTTP/1.1 400 Bad Request".to_string(),
            r#"{"errorMessage":"instrumentSymbol query parameter is required"}"#.to_string(),
        );
    }
    if instrumentSymbol != tradedInstrumentSymbol {
        return (
            "HTTP/1.1 400 Bad Request".to_string(),
            format!(
                r#"{{"errorMessage":"matching-engine (skeleton) only trades {tradedInstrumentSymbol}, got {instrumentSymbol}"}}"#
            ),
        );
    }

    let startEpochMillis = queryParams
        .get("startEpochMillis")
        .and_then(|value| value.parse::<u64>().ok())
        .unwrap_or(u64::MIN);
    let endEpochMillis = queryParams
        .get("endEpochMillis")
        .and_then(|value| value.parse::<u64>().ok())
        .unwrap_or(u64::MAX);

    let eventRecords = match readAllEventRecordsFromWalFile(walFilePath) {
        Ok(records) => records,
        Err(readError) => {
            return (
                "HTTP/1.1 500 Internal Server Error".to_string(),
                format!(r#"{{"errorMessage":"failed to read WAL file: {readError}"}}"#),
            );
        }
    };

    let snapshots = replayWalEventRecordsCollectingDepthSnapshotsInWindow(
        &instrumentSymbol,
        &eventRecords,
        startEpochMillis,
        endEpochMillis,
    );

    (
        "HTTP/1.1 200 OK".to_string(),
        serde_json::to_string(&snapshots).unwrap_or_else(|_| "[]".to_string()),
    )
}

/// Same tiny `a=1&b=2` parser as market-data's `httpQueryServer.rs` —
/// deliberately not URL-decoding percent escapes, same rationale as that
/// module's copy.
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
    use crate::orderTypes::{IncomingOrderRequest, OrderSide, OrderType};
    use crate::walBackedOrderBook::WalBackedOrderBook;
    use std::fs;

    fn freshTempWalFilePathForTest(testName: &str) -> PathBuf {
        let mut walFilePath = std::env::temp_dir();
        walFilePath.push(format!(
            "matchingEngineDomReplayHttpTest-{testName}-{}.jsonl",
            std::process::id()
        ));
        let _ = fs::remove_file(&walFilePath);
        walFilePath
    }

    fn sampleLimitOrder(
        clientAccountId: &str,
        orderSide: OrderSide,
        price: i64,
        quantity: u64,
    ) -> IncomingOrderRequest {
        IncomingOrderRequest {
            clientAccountId: clientAccountId.to_string(),
            orderSide,
            orderType: OrderType::Limit,
            limitPriceInMinorUnits: price,
            stopTriggerPriceInMinorUnits: None,
            orderQuantity: quantity,
            orderSequenceNumber: 0,
        }
    }

    #[test]
    fn missingInstrumentSymbolReturns400() {
        let walFilePath = freshTempWalFilePathForTest("missingSymbol");
        let (statusLine, _) =
            handleOneHttpRequest("GET /domReplay HTTP/1.1", &walFilePath, "DEMO-EQ");
        assert_eq!(statusLine, "HTTP/1.1 400 Bad Request");
    }

    #[test]
    fn unknownInstrumentSymbolReturns400() {
        let walFilePath = freshTempWalFilePathForTest("unknownSymbol");
        let (statusLine, _) = handleOneHttpRequest(
            "GET /domReplay?instrumentSymbol=NOPE HTTP/1.1",
            &walFilePath,
            "DEMO-EQ",
        );
        assert_eq!(statusLine, "HTTP/1.1 400 Bad Request");
    }

    #[test]
    fn unknownPathReturns404() {
        let walFilePath = freshTempWalFilePathForTest("unknownPath");
        let (statusLine, _) = handleOneHttpRequest("GET /nope HTTP/1.1", &walFilePath, "DEMO-EQ");
        assert_eq!(statusLine, "HTTP/1.1 404 Not Found");
    }

    #[test]
    fn missingWalFileReturns500RatherThanPanicking() {
        let walFilePath = freshTempWalFilePathForTest("missingFile");
        let (statusLine, bodyJson) = handleOneHttpRequest(
            "GET /domReplay?instrumentSymbol=DEMO-EQ HTTP/1.1",
            &walFilePath,
            "DEMO-EQ",
        );
        assert_eq!(statusLine, "HTTP/1.1 500 Internal Server Error");
        assert!(bodyJson.contains("errorMessage"));
    }

    #[test]
    fn realWalFileProducesRealReplayedDepthSnapshotsOverHttp() {
        let walFilePath = freshTempWalFilePathForTest("realReplay");
        {
            let mut walBackedBook =
                WalBackedOrderBook::createFreshWithNewWalFile("DEMO-EQ", &walFilePath).unwrap();
            walBackedBook
                .submitIncomingOrder(sampleLimitOrder("buyer", OrderSide::Buy, 100, 5))
                .unwrap();
        }

        let (statusLine, bodyJson) = handleOneHttpRequest(
            "GET /domReplay?instrumentSymbol=DEMO-EQ HTTP/1.1",
            &walFilePath,
            "DEMO-EQ",
        );
        assert_eq!(statusLine, "HTTP/1.1 200 OK");
        assert!(bodyJson.contains("\"bidLevelsBestFirst\":[[100,5]]"));

        fs::remove_file(&walFilePath).ok();
    }

    #[test]
    fn optionsRequestGetsA204WithNoBody() {
        let walFilePath = freshTempWalFilePathForTest("options");
        let (statusLine, bodyJson) =
            handleOneHttpRequest("OPTIONS /domReplay HTTP/1.1", &walFilePath, "DEMO-EQ");
        assert_eq!(statusLine, "HTTP/1.1 204 No Content");
        assert_eq!(bodyJson, "");
    }
}
