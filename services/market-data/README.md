# market-data

Tier 1 component — see `ARCHITECTURE.md` §5 in the repo root.

## Status: ingests real depth AND trade-tick publishes from matching-engine (or a deterministic simulated feed, standalone); serves a trade tape, OHLCV candles, a columnar tick store, watchlists, real price alerts, an L1 WS feed, and UDP multicast fan-out

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
- **A real WebSocket L1 (top-of-book) quote feed** (`src/
  l1QuotePublisher.rs`, `src/l1QuoteWebSocketServer.rs`,
  `src/l1QuoteWireProtocol.rs`) on `ws://127.0.0.1:9104` — FEATURES.md
  §8's "WebSocket broadcast for L1 quotes" `[P1]` and "client-side
  reconnect/resync protocol (sequence numbers, snapshot+delta)" `[P2]`.
  Built on `tokio` + `tokio-tungstenite`, run on its own OS thread with
  its own tokio runtime (same "own thread" pattern as
  `httpQueryServer.rs`, just async instead of blocking sync I/O).
  Real push, not polling: `main.rs`'s ingestion loop derives best bid/
  ask/last-trade directly from the SAME real depth publishes
  `deltaPublisher.rs` consumes (matching-engine sends full book depth
  per publish, so best bid/ask for one publish is just its own
  max-priced bid level / min-priced ask level — see
  `l1QuotePublisher.rs`'s module doc), and every resulting update is
  fanned out over an in-process `tokio::sync::broadcast` channel to
  every connected client the instant it's derived.
  - **Snapshot+delta+resync protocol, for real**: every update carries
    a monotonic per-instrument sequence number, independent from
    `DeltaPublisher`'s own book-delta sequence counters (two different
    topics). A newly-connecting client is sent a `SNAPSHOT` (current
    state + its sequence number) for every instrument market-data has
    ever seen, THEN switches to `DELTA` messages — so a client can
    detect a gap (an incoming sequence number that doesn't immediately
    follow the last one it saw) and knows to resync rather than
    silently corrupting its view. A client can send a
    `{"messageType":"RESYNC_REQUEST","instrumentSymbol":"..."}` (or
    omit `instrumentSymbol` to resync everything) at any time to force
    a fresh `SNAPSHOT`. The server also self-detects a slow/lagging
    client (`tokio::sync::broadcast`'s bounded-channel `Lagged` error)
    and proactively resends a snapshot rather than leaving it to notice
    on its own.
  - 14 unit tests (`l1QuotePublisher.rs`, `l1QuoteWireProtocol.rs`) plus
    4 real integration tests (`l1QuoteWebSocketServer.rs`) that bind a
    real ephemeral-port `TcpListener`, connect a real
    `tokio-tungstenite` client, and assert message ordering
    (SNAPSHOT-then-DELTA, sequence numbers, resync behavior) end-to-end
    over an actual WS handshake — not mocked.
  - **Verified live** against the real running `matching-engine` +
    `market-data` binaries (not just unit/integration tests): a Python
    `websockets` client connected to `ws://127.0.0.1:9104` while two
    real crossing order pairs were submitted directly over TCP to
    matching-engine on `127.0.0.1:9101`. Received `DELTA` messages with
    increasing sequence numbers (1, then 2 reflecting the real trade
    print at 10100), then a second, later-connecting client received a
    `SNAPSHOT` at sequence 4 reflecting all state so far (proving the
    catch-up path works for a client that missed earlier updates), and
    a `RESYNC_REQUEST` correctly returned a fresh `SNAPSHOT` at the same
    sequence number. See the transcript in this build's `docs/
    BUILD_LOG.md` entry.
- **A real deterministic simulated/sandbox exchange feed**
  (`src/simulatedExchangeFeedGenerator.rs`, FEATURES.md §8 `[P1]`) — a
  seeded, per-symbol random-walk price generator (configurable drift +
  volatility) that produces wire-shaped messages identical to a real
  matching-engine depth publish and feeds them into the EXACT SAME
  ingestion pipeline real ticks flow through (`main.rs`'s new
  `ingestDepthPublishMessage` function — extracted from the old inline
  TCP-loop body specifically so both sources share it; there is no
  parallel code path). Enabled via
  `MARKET_DATA_SIMULATED_FEED_ENABLED=true`, seeded via
  `MARKET_DATA_SIMULATED_FEED_SEED=<u64>`, paced via
  `MARKET_DATA_SIMULATED_FEED_TICK_INTERVAL_MILLIS=<u64>` (default
  `250`). Drives two demo symbols (`DEMO-EQ`, `SIM-AAPL`) by default.
  Deterministic for real: two generators built with the same seed
  produce byte-identical tick sequences forever, since the only source
  of randomness is a pure SplitMix64 PRNG with no wall-clock/thread/OS
  entropy dependence (`sameSeedProducesIdenticalSequence` test). This
  makes the whole service runnable standalone — no matching-engine
  process required at all — for Phase 0-1 demos and tests. **Verified
  live**: ran `market_data` with the simulated feed enabled and
  `matching-engine` NOT running at all; confirmed real OHLCV candles via
  `GET /candles`, a real trade tape via `GET /trades`, and real
  `SNAPSHOT`/`DELTA` messages over the L1 WebSocket feed, all produced
  purely from the synthetic tick stream.
- **A real columnar (struct-of-arrays) tick store for replay/backtest**
  (`src/columnarTickStore.rs`, FEATURES.md §8 `[P3]`) — genuinely
  column-oriented: each instrument's tick history is three separate
  parallel `Vec`s (timestamps, prices, quantities), not one
  `Vec` of row structs. `rangeQuery(symbol, start, end)` binary-searches
  (`partition_point`) the sorted timestamp column for its window bounds
  — O(log n) to locate the range plus O(k) to materialize the k matching
  rows — instead of a linear scan across every retained tick. Wired into
  the same `ingestDepthPublishMessage` ingestion path as the candle
  aggregator, so every real or simulated trade tick gets appended.
  Capped at 50,000 ticks/instrument (oldest evicted first), in-memory
  only. Exposed over HTTP: `GET /ticks/range?instrumentSymbol=...&startEpochSeconds=...&endEpochSeconds=...`
  (both bounds optional, inclusive, default to the full retained
  history). 12 tests (boundary inclusivity, eviction, multi-symbol
  independence, empty/inverted ranges, etc). **Verified live**: queried
  `GET /ticks/range?instrumentSymbol=SIM-AAPL` against the live running
  service (simulated feed only, no matching-engine) and got back real
  tick data.
- **Real UDP multicast fan-out for co-located institutional consumers**
  (`src/udpMulticastPublisher.rs`, FEATURES.md §8 `[P4]`) — a real
  `std::net::UdpSocket` publisher that joins/sends to an actual IPv4
  multicast group (default `239.1.1.1:9105`, overridable via
  `MARKET_DATA_UDP_MULTICAST_GROUP_ADDRESS`; disable entirely with
  `MARKET_DATA_UDP_MULTICAST_ENABLED=false`), carrying both trade ticks
  and L1 top-of-book updates — the same data the WS feed carries — in a
  compact hand-rolled binary format (tag byte + length-prefixed symbol +
  little-endian numeric fields), not JSON. TTL fixed at 1 (link-local
  only). Wired into `ingestDepthPublishMessage` (trade ticks) and as a
  second `L1QuotePublisher` downstream sink (L1 quotes) — same fan-out
  points as everything else, real or simulated feed alike. 10 tests,
  including two REAL send/receive integration tests: a receiving
  `UdpSocket` in the same test process actually joins the multicast
  group (`join_multicast_v4`) and asserts it receives and correctly
  decodes real datagrams sent by `UdpMulticastPublisher`. **Verified
  live, not just in tests**: with the real service running, a Python
  socket joined `239.1.1.1:9105` and received real trade-tick and
  L1-quote datagrams (`kind=0`/`kind=1`) sent by the live process, both
  decodable and symbol-tagged correctly.

What's a placeholder:
- Fan-out (both the depth-delta println sink AND the L1 WS feed) is
  still in-process — a real `tokio::sync::broadcast` channel inside one
  OS process, not a real Kafka/Redpanda producer + independently-scaled
  WS fan-out fleet (ARCHITECTURE.md §5). A restart drops every connected
  WS client and all in-memory L1 state.
- The L1 WS feed broadcasts every instrument to every client — there's
  no per-symbol subscribe/unsubscribe filtering, so a client only
  interested in one instrument still receives (and has to discard)
  updates for every other one market-data has state for.
- No WS auth — any TCP client that can reach `127.0.0.1:9104` can
  connect and read the feed.
- Matching-engine sends its FULL book depth on every order, not an actual
  diff — see the TODO on `OrderBookCore::currentBookDepthSnapshot` in
  matching-engine. Bandwidth-inefficient; internally consistent since
  market-data re-derives its own sequence numbers on receipt regardless
  (both `DeltaPublisher`'s and `L1QuotePublisher`'s).
- UDP multicast is a single fixed group address, TTL 1, no reliability
  layer (no sequence-gap detection/retransmit like the WS feed's own
  resync protocol has), no encryption/auth — anything on the local
  network segment that can join the group can read the feed. There's no
  real "distribution tree" (fan-out to further downstream relays), just
  one publisher socket sending to one group.
- The simulated feed's symbol list (`DEMO-EQ`, `SIM-AAPL`) and their
  drift/volatility are hardcoded in `main.rs`'s
  `defaultSimulatedSymbolConfigs()` — not configurable via env var
  beyond enable/seed/interval. A real build would want per-symbol
  config (at minimum via a file), not a fixed Rust literal.
- The columnar tick store is a second, separate retention/eviction
  policy from `candleAggregator`'s own trade tape (500 ticks) — by
  design (FEATURES.md §8 `[P3]` asks for deeper replay/backtest history
  than the short-term tape `[P1]`/`[P2]` need), but it does mean the
  same trade tick is stored twice in two different in-memory structures.
  Neither persists across a restart.
- Candle width is fixed at 60 seconds — not configurable per request
- Trade tick timestamps are ingestion-time (`SystemTime::now()` in
  market-data), not true matching-engine execution time — there's no
  shared clock/NTP discipline set up in this skeleton
- Everything is in-memory only — a restart loses the entire trade tape
  and candle history
- The HTTP query API (`GET /trades`, `GET /candles`, watchlists, alerts)
  is still a polling stopgap for everything OTHER than L1 quotes — only
  the L1 feed got a real WS push in this build. A real build would also
  want book-depth deltas and trade prints pushed over WS, not just L1.
- Watchlists/alerts: in-memory only, no auth, no "technical" triggers
  (moving averages, RSI, etc. — only a plain price threshold), no push
  notification (a client has to poll `GET /alerts` to discover a fired
  one), a fired alert never re-arms, and `apps/web` has no UI for any of
  this yet (curl-verified only)

## Run it

```bash
cargo run      # starts the TCP ingestion server on 127.0.0.1:9102,
               # the HTTP query server on 127.0.0.1:9103, and the L1
               # quote WebSocket server on 127.0.0.1:9104
cargo test
```

### Standalone, with no matching-engine at all (simulated feed)

```bash
MARKET_DATA_SIMULATED_FEED_ENABLED=true \
MARKET_DATA_SIMULATED_FEED_SEED=42 \
MARKET_DATA_SIMULATED_FEED_TICK_INTERVAL_MILLIS=250 \
cargo run
```

Drives two demo symbols (`DEMO-EQ`, `SIM-AAPL`) with a deterministic
random-walk price series — same seed always produces the same tick
sequence. Everything downstream works exactly as it does off a real
matching-engine feed: `GET /candles`, `GET /trades`, `GET /ticks/range`,
the L1 WS feed, and UDP multicast fan-out all populate from it. Other
env vars: `MARKET_DATA_UDP_MULTICAST_ENABLED` (default `true`),
`MARKET_DATA_UDP_MULTICAST_GROUP_ADDRESS` (default `239.1.1.1:9105`).

To see it receive real data instead, start `matching-engine` too and
submit an order through `oms-gateway` (or directly over TCP to
matching-engine) — see that service's README. Then:

```bash
# L1 quote WS feed — SNAPSHOT of current state, then DELTA per update.
# (using the `websockets` Python package; websocat/wscat work too)
python3 -c '
import asyncio, websockets
async def main():
    async with websockets.connect("ws://127.0.0.1:9104") as ws:
        for _ in range(5):
            print(await ws.recv())
asyncio.run(main())
'

# force a resync (server replies with a fresh SNAPSHOT for the symbol)
python3 -c '
import asyncio, json, websockets
async def main():
    async with websockets.connect("ws://127.0.0.1:9104") as ws:
        await ws.send(json.dumps({"messageType": "RESYNC_REQUEST", "instrumentSymbol": "DEMO-EQ"}))
        print(await ws.recv())
asyncio.run(main())
'

# UDP multicast fan-out (FEATURES.md §8 [P4]) — join the group and
# receive real trade-tick (kind=0) / L1-quote (kind=1) datagrams
python3 -c '
import socket, struct
GRP, PORT = "239.1.1.1", 9105
sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM, socket.IPPROTO_UDP)
sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
sock.bind(("0.0.0.0", PORT))
sock.setsockopt(socket.IPPROTO_IP, socket.IP_ADD_MEMBERSHIP,
                 struct.pack("4s4s", socket.inet_aton(GRP), socket.inet_aton("0.0.0.0")))
for _ in range(5):
    data, _ = sock.recvfrom(1024)
    print("kind=", data[0], "symbol=", data[2:2+data[1]].decode())
'
```

```bash
curl "http://127.0.0.1:9103/trades?instrumentSymbol=DEMO-EQ"
curl "http://127.0.0.1:9103/candles?instrumentSymbol=DEMO-EQ"

# columnar tick store range query (FEATURES.md §8 [P3]) — both bounds
# optional, default to the full retained history (up to 50,000 ticks)
curl "http://127.0.0.1:9103/ticks/range?instrumentSymbol=DEMO-EQ"
curl "http://127.0.0.1:9103/ticks/range?instrumentSymbol=DEMO-EQ&startEpochSeconds=1700000000&endEpochSeconds=1700003600"

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
