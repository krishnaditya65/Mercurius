# market-data

Tier 1 component — see `ARCHITECTURE.md` §5 in the repo root.

## Status: ingests real depth AND trade-tick publishes from matching-engine; serves a trade tape, OHLCV candles, watchlists, and real price alerts

What's real:
- Sequenced snapshot+delta message contract
  (`src/marketDataEventTypes.rs`) — the actual protocol clients depend on
  to detect gaps and resync
- Per-instrument monotonic sequencing with independent counters
  (`src/deltaPublisher.rs`), tested
- **A real TCP+JSON ingestion server** (`main.rs`, `src/
  ingestionWireProtocol.rs`) on `127.0.0.1:9102` — matching-engine
  connects here after every order it processes and publishes the current
  book depth. Verified end-to-end with real orders flowing through all
  four services (`docs/BUILD_LOG.md` entry 15).
- **Real trade-tick ingestion + OHLCV candle aggregation**
  (`src/candleAggregator.rs`): every executed trade matching-engine
  publishes is folded into a fixed 60-second-bucket OHLCV candle per
  instrument, plus a bounded trade tape (last 500 ticks/candles). Tested
  (bucket-rollover, per-instrument independence, limit/ordering).
- **A small HTTP query API** (`src/httpQueryServer.rs`) on
  `127.0.0.1:9103`: `GET /trades?instrumentSymbol=...&limit=...` and
  `GET /candles?instrumentSymbol=...&limit=...`, both JSON, with a
  permissive CORS header so a browser-based chart can poll directly.
  Hand-rolled HTTP/1.1 GET parsing — no framework dependency, consistent
  with this codebase's raw-TCP-JSON style elsewhere. Verified end-to-end:
  a real cross between two orders showed up correctly in both endpoints
  (`docs/BUILD_LOG.md` entry 26).
- **Watchlists** (`src/watchlist.rs`, FEATURES.md §9): `POST
  /watchlist/add`/`/watchlist/remove`, `GET /watchlist?accountIdentifier=...`
  — a per-account set of instrument symbols, sorted for deterministic
  responses. 7 tests.
- **Real price alerts** (`src/pricealerts.rs`, FEATURES.md §9): `POST
  /alerts/create` (`isAboveNotBelow` + a threshold price), `GET
  /alerts?accountIdentifier=...`. Evaluated against the SAME live trade-
  tick stream that feeds the candle aggregator — `main.rs`'s ingestion
  loop checks every not-yet-triggered alert against every real trade
  print, so an alert fires the instant a real trade crosses its
  threshold, not on a polling interval re-checking the latest price.
  8 tests. **Verified end-to-end live**: created an alert (fire at
  price ≥ 100), submitted real trades at 80 (alert correctly stayed
  untriggered) and then at 120 (alert fired, `triggeredAtEpochSeconds`
  set, logged to market-data's own stdout) — through the actual running
  matching-engine + market-data processes, not a unit test.
- `httpQueryServer.rs` now also parses HTTP headers/body (needed for the
  `POST` endpoints above) and answers `OPTIONS` with `204` for CORS
  preflights, matching the pattern established elsewhere in this repo.

What's a placeholder:
- Fan-out is still an in-process println sink, not a real Kafka/Redpanda
  producer + independently-scaled WS fan-out fleet (ARCHITECTURE.md §5)
- Matching-engine sends its FULL book depth on every order, not an actual
  diff — see the TODO on `OrderBookCore::currentBookDepthSnapshot` in
  matching-engine. Bandwidth-inefficient; internally consistent since
  market-data re-derives its own sequence numbers on receipt regardless.
- No UDP multicast distribution tree for co-located institutional clients
- Candle width is fixed at 60 seconds — not configurable per request
- Trade tick timestamps are ingestion-time (`SystemTime::now()` in
  market-data), not true matching-engine execution time — there's no
  shared clock/NTP discipline set up in this skeleton
- Everything is in-memory only — a restart loses the entire trade tape
  and candle history
- The HTTP query API is a polling stopgap, not the real WebSocket
  streaming ARCHITECTURE.md §5 describes
- Watchlists/alerts: in-memory only, no auth, no "technical" triggers
  (moving averages, RSI, etc. — only a plain price threshold), no push
  notification (a client has to poll `GET /alerts` to discover a fired
  one), a fired alert never re-arms, and `apps/web` has no UI for any of
  this yet (curl-verified only)

## Run it

```bash
cargo run      # starts the TCP ingestion server on 127.0.0.1:9102
               # and the HTTP query server on 127.0.0.1:9103
cargo test
```

To see it receive real data, start `matching-engine` too and submit an
order through `oms-gateway` (or directly over TCP to matching-engine) —
see that service's README. Then:

```bash
curl "http://127.0.0.1:9103/trades?instrumentSymbol=DEMO-EQ"
curl "http://127.0.0.1:9103/candles?instrumentSymbol=DEMO-EQ"

# watchlist
curl -X POST localhost:9103/watchlist/add -d '{"accountIdentifier":"acct-001","instrumentSymbol":"DEMO-EQ"}'
curl "localhost:9103/watchlist?accountIdentifier=acct-001"

# price alert — fires the next time a real trade prints at/above 100
curl -X POST localhost:9103/alerts/create -d '{
  "accountIdentifier": "acct-001",
  "instrumentSymbol": "DEMO-EQ",
  "isAboveNotBelow": true,
  "thresholdPriceInMinorUnits": 100
}'
curl "localhost:9103/alerts?accountIdentifier=acct-001"
```
