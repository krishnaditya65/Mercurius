# Documentation — Mercurius

This is the **living, synthesized reference** for everything implemented
in this repo: every service, every file, every public function/type, what
it does, and its current known limitations. It is edited in place as code
changes — for the chronological record of *how* it got built, see
`BUILD_LOG.md` (append-only, never edited).

Read `FEATURES.md` for the product backlog and `ARCHITECTURE.md` for the
system design this code is implementing pieces of. This document only
describes code that actually exists.

---

## services/matching-engine (Rust) — Tier 0 skeleton

Single-instrument, single-threaded, price-time-priority limit order book.
Implements the core matching algorithm from ARCHITECTURE.md §3, without
yet implementing the ring-buffer ingress, WAL/recovery, sharding, or
kernel-bypass networking also described there.

### `src/orderTypes.rs`

| Item | Kind | Purpose |
|---|---|---|
| `OrderSide` | enum (`Buy`, `Sell`) | Which side of the book an order is on. |
| `IncomingOrderRequest` | struct | An order arriving from the OMS: `clientAccountId`, `orderSide`, `orderType`, `limitPriceInMinorUnits` (i64, integer money — never float), `stopTriggerPriceInMinorUnits` (`Option<i64>`, only meaningful for `StopLossLimit`/`StopLossMarket`), `orderQuantity` (u64). |
| `RestingLimitOrder` | struct | An order sitting on the book after partial/zero fill: adds `restingOrderSequenceNumber` (time priority) to the above. |
| `TradeExecutionEvent` | struct | One executed trade: `buyingClientAccountId`, `sellingClientAccountId`, `executedPriceInMinorUnits`, `executedQuantity`. |
| `OrderType` | enum (`Limit`, `Market`, `StopLossLimit`, `StopLossMarket`) | FEATURES.md §3's four order types — all real. Stop variants convert to `Limit`/`Market` once triggered (see `orderBookCore.rs`). |

### `src/orderBookCore.rs`

| Function | Purpose |
|---|---|
| `OrderBookCore::newEmptyOrderBook(instrumentSymbol)` | Constructs an empty book for one instrument. |
| `OrderBookCore::submitIncomingOrder(order) -> OrderSubmissionOutcome` | Entry point: assigns the order a local id immediately (`allocateNextOrderSequenceNumber`), then `Limit`/`Market` orders match immediately while `StopLossLimit`/`StopLossMarket` orders are parked in `pendingStopOrders` instead. Either way, re-checks all pending stop orders against the (possibly just-updated) last traded price afterward and triggers any that qualify, looping to support cascades. Returns both the trades produced and the assigned id (needed to `cancelOrder` this order later). |
| `OrderBookCore::cancelOrder(id) -> bool` | Removes a resting `Limit` order or a still-armed stop order by the id `submitIncomingOrder` returned. Linear scan: both resting price-level maps first, then the pending-stop pool. Returns `false` if nothing matched (never existed, already filled, or already cancelled). |
| `OrderBookCore::queryOrderStatus(id) -> OrderStatusQueryResult` | Read-only version of the same lookup: reports `RestingLimit{side, price, remainingQuantity}`, `PendingStop{side, triggerPrice, quantity}`, or `NotFound` (never existed, filled, cancelled, or triggered-and-filled — indistinguishable without history). No side effects. |
| `matchAndRecordLastPrice` (private) | Dispatches to the buy-/sell-side matcher, then updates `lastTradedPriceInMinorUnits` from the most recent trade produced, if any. |
| `triggerAnyEligiblePendingStopOrders` (private) | Loops: finds the first pending stop order whose trigger condition holds at the current last traded price, converts it to a live `Limit`/`Market` order, matches it, repeats until a full scan finds nothing left to trigger. |
| `pendingStopOrderCount` | Returns the number of currently-armed, not-yet-triggered stop orders — used by tests and available for `main.rs`/monitoring. |
| `matchIncomingBuyOrderAgainstRestingSellOrders` (private) | Walks resting asks from best (lowest) price upward while the incoming buy's limit price still crosses; FIFO-fills within each price level; rests any unfilled remainder as a bid. |
| `matchIncomingSellOrderAgainstRestingBuyOrders` (private) | Mirror image: walks resting bids from best (highest) price downward. |
| `restRemainingBuyOrderOnBook` / `restRemainingSellOrderOnBook` (private) | Reuses the id already assigned at intake (rather than allocating a new one) and pushes the remainder onto the appropriate `BTreeMap` price-level queue — this is what lets a triggered stop order stay cancellable under the same id it was armed with. |
| `removeFromRestingMap` (private, static) | Shared by `cancelOrder` for both book sides: scans every price level's `VecDeque` for a matching id, removes it, and drops the price level entirely if it's now empty. |
| `allocateNextOrderSequenceNumber` (private) | Simple incrementing counter — now called once per `submitIncomingOrder` call (every order gets an id, not just ones that end up resting). |
| `printCurrentBookDepth` | Debug/demo helper: prints aggregated quantity at each price level, both sides. |
| `isStopOrderTriggeredAtPrice` (free function) | BUY stops fire once last traded price rises to/through the trigger; SELL stops fire once it falls to/through the trigger — the standard retail stop-loss convention. |

**Trade price convention:** a trade always executes at the *resting*
order's price, not the aggressor's limit price — standard price-time
priority convention, and every downstream P&L/margin calculation in the
rest of the system must assume this.

**Tested behavior** (`#[cfg(test)] mod tests`): crossing match executes at
resting price; partial fill correctly leaves a remainder resting; two
non-crossing orders both rest without trading; a `Market` order crosses a
resting order regardless of its price; an unfilled `Market` order is
dropped, never rested; a stop order stays invisible to depth snapshots
until triggered; a stop order triggers and fills (including a same-call
cascade) once a qualifying trade prints; a stop order correctly stays
pending while price remains on the favorable side; cancelling a resting
order removes it and frees up the liquidity it was blocking; cancelling a
pending stop order removes it before it can ever trigger; cancelling an
unknown id returns `false` (22 tests total).

**Order types** (`OrderType` in `orderTypes.rs`, FEATURES.md §3): all
four — `Limit`, `Market`, `StopLossLimit`, `StopLossMarket` — are real.
`Market` bypasses the price-crossing check entirely and never rests an
unfilled remainder — a documented IOC-like simplification, not
necessarily final semantics. Stop orders trigger off
`lastTradedPriceInMinorUnits` (last-traded convention, not a separate
reference/index price — a real venue might do either); an
armed-but-untriggered stop order is deliberately kept out of
`currentBookDepthSnapshot`, since it isn't a live bid/ask.

### `src/wireProtocol.rs` and `main.rs` — the real network bridge to oms-gateway

`main.rs` is no longer a hardcoded demo driver: it's a real TCP server
(`127.0.0.1:9101`) that accepts one JSON order request per connection,
submits it to `OrderBookCore`, and writes back a JSON response — verified
end-to-end against `oms-gateway`'s real HTTP endpoint (`docs/BUILD_LOG.md`
entry 14).

| Item | Purpose |
|---|---|
| `IncomingOrderWireRequest` | Deserializes a JSON order request whose field names deliberately match `oms-gateway`'s `OrderSubmissionRequest` JSON shape. `intoInternalOrderRequest()` converts the wire boolean `orderSideIsBuyNotSell` into the internal `OrderSide` enum, and combines `orderIsStopLossVariant` + `orderIsMarketOrderNotLimit` into one of the four `OrderType`s. Both stop-related fields are `#[serde(default)]` for backward compatibility with clients that predate SL/SL-M support. |
| `TradeExecutionWireEvent` | `From<&TradeExecutionEvent>` conversion for serializing a trade back over the wire. |
| `OrderSubmissionWireResponse` | Either `errorMessage` is set (business-level rejection, e.g. wrong instrument, or malformed JSON) or `tradeExecutionEvents` holds whatever trades the order produced (possibly empty, if it fully rested, or if it was a stop order that only just armed and hasn't triggered). `assignedOrderSequenceNumber` is set on a submission response (hold onto it to cancel later); `wasOrderCancelled` is set only on a cancel response — both `skip_serializing_if`, so a client never sees a meaningless null for the field that doesn't apply to the request it made. |
| `IncomingOrderWireRequest::cancelOrderSequenceNumber: Option<u64>` | If set, the whole line is a CANCEL, not a submission — `main.rs` checks this before ever calling `intoInternalOrderRequest()`. `#[serde(default)]`, same backward-compatibility pattern as the stop-loss fields. |
| `handleOneIncomingOrderLine` (in `main.rs`) | Parses one line, checks the instrument symbol against the single hardcoded `DEMO-EQ` book, submits to `OrderBookCore`, builds the response. |

**Design choice worth keeping:** the TCP accept loop is deliberately
*sequential*, not one thread per connection — this preserves
ARCHITECTURE.md §3.1's single-writer principle without needing a `Mutex`
around the order book, even in this placeholder bridge.

**Tested behavior:** buy/sell wire-flag → `OrderSide` conversion in both
directions; JSON deserialization against the exact shape `oms-gateway`
sends; stop-loss flag pair → `StopLossLimit`/`StopLossMarket` conversion
with trigger price carried through; omitted stop-loss fields default to
non-stop for backward compatibility.

**Known limitations:** the bridge is synchronous TCP+JSON, one connection
per order — explicitly not the lock-free ring-buffer ingress with a
zero-copy binary (SBE) encoding ARCHITECTURE.md §3.1/§3.5 describes. No
WAL/crash recovery, no NUMA pinning or kernel-bypass networking, and no
instrument sharding yet (single hardcoded `DEMO-EQ` book). Wire types use
heap-allocated `String`s, which must not survive into a real
allocation-free hot path.

### Publishing to `market-data` — new this build

| Item | Purpose |
|---|---|
| `OrderBookCore::currentBookDepthSnapshot()` | Returns every price level on both sides as `(isBidSide, price, totalQuantity)`. Tested. TODO: this is a full snapshot every call, not a diff — see its doc comment. |
| `OutgoingDepthPublishWireMessage::fromDepthSnapshotAndTrades(symbol, snapshot, tradeEvents)` (in `wireProtocol.rs`) | Wraps a depth snapshot AND whatever trades this order just produced into the JSON shape market-data's `ingestionWireProtocol.rs` expects. `OutgoingTradeTickWireEvent` is deliberately narrower than `TradeExecutionEvent` — just price and quantity, no account ids. |
| `publishBookDepthToMarketData` (in `main.rs`) | Fire-and-forget: connects to market-data at `127.0.0.1:9102` with a 200ms timeout after every processed order and sends the current depth plus any trade ticks. Deliberately swallows connection failures — a Tier 0 component must never let a Tier 1 consumer's availability affect order processing. Verified end-to-end (`docs/BUILD_LOG.md` entries 15, 26): market-data received real sequence numbers and real trade ticks from real orders, not demo data. |
| `handleOneIncomingOrderLine` (in `main.rs`) | Now returns `(OrderSubmissionWireResponse, Vec<TradeExecutionEvent>)` — cancels/status-queries always return an empty trade vec, submissions return whatever `OrderBookCore::submitIncomingOrder` produced, so the caller can forward exactly those trades to market-data as ticks. |

### `src/writeAheadLog.rs`, `src/walBackedOrderBook.rs`, `src/deterministicReplayHarness.rs` — new this build

FEATURES.md §9: "Event sourcing + WAL replay for crash recovery" and
"Deterministic replay test harness". A real fsync'd, append-only NDJSON
write-ahead log — one record per book-mutating event
(`OrderAccepted`/`OrderCancelled`/`TradeExecuted`) — with a reader
tolerant of a torn tail and a replay function that reconstructs a fresh
`OrderBookCore` from a WAL file. `WalBackedOrderBook` wraps
`OrderBookCore` with this logging and withholds acknowledgement on any
WAL write error. `deterministicReplayHarness.rs` runs an arbitrary
operation sequence through both a live book and a WAL-replay-
reconstructed book and asserts exact equality via a new
`fullBookStateSnapshotForTesting()` on `OrderBookCore` — 5 hand-written
scenarios plus a loop over 12 seeded pseudo-random sequences. `main.rs`
gained `MATCHING_ENGINE_WAL_FILE_PATH` and an offline `cargo run --
--replay <path>` recovery-inspection mode. 17 new tests (46 total for
the crate, up from 29, zero regressions).

**Verified live** (`docs/BUILD_LOG.md` entry 52): ran the real server
with a real WAL path, drove 5 real orders over the actual TCP wire
protocol from a standalone Python client, inspected the resulting WAL
file directly with `xxd` (7 real NDJSON lines), then separately replayed
that exact file via `--replay` and confirmed the reconstructed book
state matched what the live sequence should leave resting.

**Known limitations:** no WAL compaction/snapshotting (unbounded
growth); no per-record checksums; the WAL logs commands, not book
internals — "which resting order absorbed this fill" requires a replay
to re-derive, not a direct read.

### `src/lockFreeSpscRingBuffer.rs` — new this build

FEATURES.md §9: "Lock-free ring buffer ingress/egress". A hand-rolled,
genuinely lock-free single-producer/single-consumer ring buffer on
`std::sync::atomic` — no mutex on the enqueue/dequeue hot path. Split
into `RingBufferProducerHandle`/`RingBufferConsumerHandle` so the type
system itself enforces the single-producer/single-consumer invariant
the 4 `unsafe` blocks rely on for soundness: (1) `unsafe impl Sync` —
sound because the Acquire/Release handoff protocol guarantees producer
and consumer never touch the same slot concurrently; (2) the write in
`tryPush` — sound because there's structurally one producer, and the
preceding Acquire-gated full-check proves the consumer already finished
reading whatever was there; (3) `assume_init_read()` in `tryPop` —
sound because there's structurally one consumer, and the preceding
Acquire-gated empty-check proves a completed push initialized and
published the slot; (4) `assume_init_drop()` in `Drop` — sound because
`Drop` gives exclusive access only once the `Arc` refcount hits zero.

`main.rs` now runs two real threads — a network thread (owns the
`TcpListener`) and a matching-core thread (owns `WalBackedOrderBook`) —
connected ONLY by an ingress and an egress ring buffer, replacing the
previous direct synchronous call. `handleOneIncomingOrderLine`'s logic
is reused verbatim on the core thread; every existing behavior (price-
time priority, WAL logging) is unchanged. 6 new tests: single-threaded
FIFO/full/empty/wraparound, plus two real multi-threaded stress tests
(2,000,000 `usize` items; 200,000 heap-allocated `String` items on a
capacity-8 buffer to force contention).

**Verified live** (`docs/BUILD_LOG.md` entry 58): real TCP wire traffic
through the new two-thread path produced correct trades/sequence-
numbers/cancels; the resulting WAL was independently replayed
successfully; two load passes (2,000 sequential requests, then 2,000
requests from 20 concurrent client threads) against the live server
completed with zero errors and the ingress/egress correlation assertion
never fired, confirming FIFO correctness under real concurrent
pressure. 52/52 tests pass — all 46 pre-existing tests (order book, WAL,
deterministic replay) unmodified, zero regressions.

**Known limitations:** at most one request is in flight through the
ring buffers at a time (the network thread blocks on egress before
accepting the next connection) — real and lock-free, but pipelining
isn't exploited yet; no supervision if the matching-core thread panics;
blocking push/pop use a plain spin loop, not spin-then-park.

### `isBuyAggressor` + `domReplayHttpServer.rs` — new this build

FEATURES.md §20: "Order-flow footprint charts" and "Historical DOM
replay" (`docs/BUILD_LOG.md` entry 69). A real, additive
`isBuyAggressor: bool` was threaded through `TradeExecutionEvent` from
both real trade-matching call sites, through the wire protocol and WAL
records, into market-data's ingestion path — zero existing tests broken.
`domReplayHttpServer.rs` (new, port 9106) exposes `GET /domReplay`,
genuinely reusing the existing WAL + deterministic replay capability to
return a real, correctly time-windowed sequence of depth snapshots — 21
new tests (16 WAL, 5 server), 69/69 total for the crate (up from 52).

**Verified live:** a windowed replay query correctly excluded
out-of-window snapshots while preserving full pre-window book state.

**Known limitation:** O(WAL size) per request, no checkpointing yet.

---

## services/market-data (Rust) — Tier 1 skeleton

Sequenced snapshot+delta broadcasting contract per ARCHITECTURE.md §5, now
also a trade tape + OHLCV candle service.

### `src/marketDataEventTypes.rs`

| Item | Purpose |
|---|---|
| `PriceLevelDeltaUpdate` | One changed price level: `instrumentSymbol`, `isBidSide`, `priceInMinorUnits`, `newTotalQuantityAtPrice` (0 = level removed). |
| `SequencedMarketDataMessage` | A batch of deltas for one instrument, tagged with `perInstrumentSequenceNumber`. |
| `FullBookSnapshotMessage` | A complete point-in-time book state, sent on (re)subscribe or detected sequence gap. |
| `TradeTick` | One executed trade as ingested: `instrumentSymbol`, `executedAtEpochSeconds` (stamped by market-data on receipt, not true execution time), `priceInMinorUnits`, `quantity`. |
| `CandleBar` | One OHLCV bar for a fixed-width bucket: `instrumentSymbol`, `bucketStartEpochSeconds`, `openPriceInMinorUnits`, `highPriceInMinorUnits`, `lowPriceInMinorUnits`, `closePriceInMinorUnits`, `totalVolume`. |

### `src/deltaPublisher.rs`

| Function | Purpose |
|---|---|
| `DeltaPublisher::newPublisherWithNoSinks()` | Constructs a publisher with an empty sink list and an empty per-instrument sequence-counter map. |
| `registerDownstreamSink(sinkFn)` | Registers a closure invoked on every published message (stand-in for a real Kafka producer or WS fan-out worker). |
| `publishDeltaBatchForInstrument(symbol, deltas)` | Assigns the next per-instrument sequence number (`HashMap<String,u64>`, independent counter per symbol) and delivers the resulting `SequencedMarketDataMessage` to every registered sink. |

**Tested behavior:** sequence numbers increment independently per
instrument (publishing AAPL, MSFT, AAPL in that order yields sequence
numbers 1, 1, 2 respectively).

### `src/candleAggregator.rs` — trade tape + OHLCV candles, new this build

| Item | Purpose |
|---|---|
| `CandleAggregator::newEmptyAggregator()` | Constructs an aggregator with no history for any instrument. |
| `recordTrade(symbol, price, quantity, epochSeconds)` | Appends to the bounded trade tape (last 500 ticks/instrument) and folds the trade into the current 60-second candle bucket for that instrument — updates H/L/C/volume if the bucket matches the last candle, otherwise opens a fresh candle (a real build should also support configurable interval widths — see the module TODO). |
| `recentTradeTicksForInstrument(symbol, limit)` / `recentCandlesForInstrument(symbol, limit)` | Read-only, oldest-first, capped at `limit`; return an empty `Vec` (never panic) for an unknown instrument. |
| `CANDLE_INTERVAL_SECONDS` | `60` — the one fixed bucket width this build supports. |

**Tested behavior:** a first trade opens a candle with O=H=L=C=that price;
a second trade in the same bucket updates H/L/C and accumulates volume; a
trade landing in a new bucket opens a separate candle; candles/ticks are
tracked independently per instrument; an unknown instrument returns empty
rather than panicking; `recentXForInstrument(_, limit)` returns the last
`limit` entries oldest-first (6 tests).

### `src/ingestionWireProtocol.rs` and `main.rs` — real ingestion from matching-engine

`main.rs` runs a real TCP server on `127.0.0.1:9102` that accepts a
depth-publish message (now including trade ticks) per connection from
matching-engine, folds any ticks into the shared `CandleAggregator`, and
feeds the depth deltas into `DeltaPublisher` — plus a second, independent
HTTP query server (see below) on its own thread.

| Item | Purpose |
|---|---|
| `IncomingPriceLevelDeltaWireUpdate` / `IncomingDepthPublishWireMessage` | Deserializes matching-engine's `OutgoingDepthPublishWireMessage` shape exactly. `intoInternalDeltaUpdates()` (now `&self`, not consuming) converts to `Vec<PriceLevelDeltaUpdate>`, stamping each with the message's `instrumentSymbol`. |
| `IncomingTradeTickWireEvent` | `{executedPriceInMinorUnits, executedQuantity}` — one trade as published by matching-engine, no timestamp (market-data stamps its own on receipt). |
| `IncomingDepthPublishWireMessage::tradeTicks` | `#[serde(default)]` `Vec<IncomingTradeTickWireEvent>` — backward-compatible with any matching-engine build that predates trade-tick publishing. |
| main loop | Sequential accept loop (same style as matching-engine's bridge), reads one line, parses, records any trade ticks into the shared `Arc<Mutex<CandleAggregator>>` (timestamped with `SystemTime::now()`), then calls `publishDeltaBatchForInstrument`. Malformed messages are dropped with an `eprintln!`, not a crash. |

**Tested behavior:** deserialization against the exact JSON shape
matching-engine sends. **Verified end-to-end** (`docs/BUILD_LOG.md` entries
15, 26): received real sequence numbers from real orders, and a real trade
tick correctly surfaced via both `GET /trades` and `GET /candles`.

### `src/watchlist.rs` and `src/pricealerts.rs` — real-Postgres-backed by default (`docs/BUILD_LOG.md` entry 85)

FEATURES.md §9: "Watchlists, alerts (price/technical triggers)". Only
plain price-threshold alerts — no technical (moving average/RSI/etc.)
triggers.

| Item | Purpose |
|---|---|
| `WatchlistStore` | `addSymbol`/`removeSymbol` (both idempotent — a repeat add/remove is a no-op, not an error) / `symbolsForAccount` (sorted, deterministic) — a per-account set of instrument symbols. `newEmptyStore()` is in-memory; `newPostgresBackedStore(dsn) -> Result<Self, String>` (new) connects to real Postgres instead. |
| `PriceAlertStore` | `createAlert(accountId, symbol, isAboveNotBelow, thresholdPrice) -> alertId`; `checkAndTriggerAlertsForTrade(symbol, price, now)` — called once per real trade tick from `main.rs`'s ingestion loop, checks every NOT-yet-triggered alert for that instrument and marks any that now qualify, returning the newly-triggered ids; `alertsForAccount`. Same `newEmptyStore()`/`newPostgresBackedStore(dsn)` pair. |

**Tested behavior:** watchlist — 7 in-memory tests (empty by default,
add/remove, idempotent double-add, per-account independence, sorted
output) + 3 new real-Postgres tests (add/remove round trip,
changes-since + last-modified, persists across a fresh connection).
pricealerts — 8 in-memory tests (starts untriggered; above/below
threshold crossing logic; an alert only checks its own instrument;
once triggered it's never re-evaluated even by a later qualifying
trade; multiple alerts on one instrument trigger independently;
per-account filtering) + 2 new real-Postgres tests (create/trigger
round trip, persists across a fresh connection).

**Verified live, through the real running matching-engine + market-data
processes** (`docs/BUILD_LOG.md` entry 37): created a real alert (fire
at price ≥ 100), submitted a real trade at 80 (alert correctly stayed
untriggered), then a real trade at 120 (alert fired, `triggeredAtEpochSeconds`
populated, and market-data's own stdout logged the trigger) — not a unit
test, the actual live trigger path.

**Real Postgres persistence** (`src/pgBacking.rs`,
`services/market-data/migrations/0001_watchlist_and_pricealerts.sql`):
every existing method above kept its exact synchronous signature — see
`pgBacking.rs`'s own doc comment for the design tension this created
(tokio-postgres is necessarily async, but `httpQueryServer.rs`'s
hand-rolled HTTP server and `main.rs`'s ingestion loop are both plain,
non-async threads) and how it was resolved: each Postgres-backed store
owns a small dedicated `tokio::runtime::Runtime` and every method calls
`runtime.block_on(...)` around the real async query — a real, working
tradeoff (one blocking hop per call), not a permanent design, flagged
explicitly as a known limitation. `checkAndTriggerAlertsForTrade` is a
single atomic `UPDATE ... RETURNING` in Postgres mode — closing a
read-then-write race a naive port would have introduced (the in-memory
version's `for alert in alerts.iter_mut()` loop never had this problem
since it holds the whole `Mutex` for the duration).
`MARKET_DATA_POSTGRES_DSN` defaults to
`postgres://trading:trading@localhost:5432/marketdata`; since this
database does not exist on a fresh compose stack (`POSTGRES_DB` only
provisions `ledger`), `pgBacking.rs`'s `ensureTargetDatabaseExists`
connects to the server's admin `postgres` database first and issues a
real `CREATE DATABASE` if missing — handled for real, not left as a
manual step. `main.rs` falls back to in-memory stores (loudly logged)
if Postgres is unreachable at startup, same fail-open choice as the Go
services.

**Verified live** (`docs/BUILD_LOG.md` entry 85): real `POST
/watchlist/add` and `POST /alerts/create` against the live running
process, confirmed via `GET /watchlist`/`GET /alerts`; the real process
was killed and restarted; the SAME queries returned byte-identical
results (same symbol, same `alertId`, same `lastModifiedAtEpochMillis`).

**Known limitations:** `candleAggregator.rs`'s trade tape/candles and
`columnarTickStore.rs`'s tick store deliberately remain in-memory this
pass — a hot-path performance tradeoff (every real trade tick touches
both on the ingestion thread), not an oversight; ClickHouse is the
intended real backing store per ARCHITECTURE.md §6, separate later
work. Postgres calls block the calling thread (see the design-tension
note above). No auth on watchlists/alerts. No technical triggers, no
push notification (poll `GET /alerts` to discover a fired one), a
fired alert never re-arms.

### `src/httpQueryServer.rs` — HTTP query API, extended this build

A minimal hand-rolled HTTP server (no framework dependency, consistent
with this codebase's raw-TCP-JSON style elsewhere) on `127.0.0.1:9103`,
running on its own thread against a shared `SharedMarketDataState`
(bundling the candle aggregator, watchlist store, and price alert
store — the ingestion loop and this server both hold an `Arc` to the
same instance). Originally GET-only; now also parses headers and a
JSON request body (needed for the watchlist/alert `POST` endpoints) and
answers `OPTIONS` with a bare `204` for CORS preflights.

| Item | Purpose |
|---|---|
| `SharedMarketDataState` | Bundles `candleAggregator`/`watchlists`/`priceAlerts` behind one `Arc` — each field has its OWN internal mutex (not one mutex around the whole struct), so the ingestion loop touching candles/alerts can't be blocked by the HTTP server touching the (server-only) watchlist store. |
| `runHttpQueryServer(listenAddress, sharedState)` | Binds and accepts connections sequentially; `readRequestLineAndBody` now reads headers up to the blank line (extracting `Content-Length`) and then exactly that many body bytes, not just the request line. |
| `GET /trades`, `GET /candles` | Unchanged from before — see prior entry. |
| `GET /watchlist?accountIdentifier=...`, `POST /watchlist/add`, `POST /watchlist/remove` | Thin wrappers over `WatchlistStore`. |
| `GET /alerts?accountIdentifier=...`, `POST /alerts/create` | Thin wrappers over `PriceAlertStore`. |
| `handleOneHttpRequest` | Now matches on `(method, path)` instead of path alone. Missing a required query param → 400; malformed JSON body → 400; unknown route → 404; `OPTIONS` → `204`. |

**Tested behavior** (11 tests, up from 4): the original 4 (query-string
parsing, candle round-trip, missing-param 400, unknown-path 404) plus a
watchlist add→get round trip, an alert create→get round trip, a
malformed-POST-body 400, and an `OPTIONS` preflight returning `204` with
no body.

**Known limitations:** fan-out is still in-process closures, not a real
Kafka/Redpanda producer feeding an independently-scaled WS fleet. No
actual book-diffing on market-data's side either — it publishes whatever
(full-depth) snapshot matching-engine sends. No UDP multicast path. Candle
width is fixed at 60s. Trade timestamps are ingestion-time, not true
execution time. Trade tape/candles/book state remain in-memory only
(deliberate hot-path tradeoff, `docs/BUILD_LOG.md` entry 85); watchlists
and price alerts are real-Postgres-backed by default as of that same
entry — see `src/watchlist.rs`/`src/pricealerts.rs`'s section above.
The HTTP query API is a polling stopgap, not real WebSocket streaming.
Watchlists/alerts have no auth, no technical
triggers, no push notification (poll `GET /alerts` to discover a fired
one), and a fired alert never re-arms.

**Correctness fix, `docs/BUILD_LOG.md` entry 87 — unbounded-allocation
DoS:** `readRequestLineAndBody` allocated `vec![0u8; contentLength]`
directly off a client-supplied `Content-Length` header with no cap,
letting one request drive an arbitrarily large allocation. Fixed with
`MAX_REQUEST_BODY_SIZE_BYTES = 1024 * 1024`, rejecting an oversized
`Content-Length` with a 413 BEFORE allocating or blocking on the body.

### `src/l1QuotePublisher.rs`, `src/l1QuoteWireProtocol.rs`, `src/l1QuoteWebSocketServer.rs` — new this build

FEATURES.md §8: "WebSocket broadcast for L1 quotes to web/mobile
clients" and "Client-side reconnect/resync protocol". `l1QuotePublisher`
derives L1 state (best bid/ask/last trade) from the service's real depth
publish stream, with monotonic per-instrument sequence numbers.
`l1QuoteWebSocketServer` is a real `tokio`/`tokio-tungstenite` WS server
on `127.0.0.1:9104`: every new connection gets a SNAPSHOT (current state
+ its sequence number) before switching to DELTA messages, so a client
can detect a sequence gap and know to resync rather than silently
corrupting its view; a `RESYNC_REQUEST` client message triggers a fresh
SNAPSHOT. 16 new tests (47 total for the crate, up from 33), including 4
real integration tests against an actual bound listener and a real
`tokio-tungstenite` client, not mocks.

**Verified live** (`docs/BUILD_LOG.md` entry 51): real matching-engine +
market-data processes, real crossing orders driven over TCP, a real
Python `websockets` client received correctly-sequenced DELTA messages
reflecting the real fills; a later-connecting client's SNAPSHOT
correctly caught it up; a RESYNC_REQUEST correctly returned a fresh
SNAPSHOT at the same sequence number.

**Known limitations:** fan-out is in-process only (no Kafka); no
per-symbol WS subscription filtering (broadcasts every instrument); no
WS auth; only L1 got real WS push this build — depth-delta and
trade-tick feeds still don't.

### `src/simulatedExchangeFeedGenerator.rs`, `src/columnarTickStore.rs`, `src/udpMulticastPublisher.rs` — new this build

FEATURES.md §8: "Exchange feed ingestion (simulated/sandbox feed for
Phase 0–1)", "Tick-level storage in a columnar time-series store", "UDP
multicast fan-out". The simulated feed generator produces a
deterministic (seeded) per-symbol random-walk tick stream feeding the
EXACT SAME ingestion pipeline real matching-engine ticks use (the
shared `ingestDepthPublishMessage` function was extracted specifically
to guarantee this) — this service now runs fully standalone with zero
matching-engine dependency. The columnar tick store is a real
struct-of-arrays (not array-of-structs) store with binary-search range
queries, exposed via `GET /ticks/range`. The UDP multicast publisher is
a real `IP_ADD_MEMBERSHIP` group join broadcasting trade ticks and L1
quotes in a compact binary format. 33 new tests (83 total, up from 47).

**Bug found and fixed:** `DeltaPublisher`'s sink closure type lacked a
`Send` bound — fine single-threaded, broke once shared behind
`Arc<Mutex<_>>` with the new feed-generator thread; fixed by adding the
bound and updating its test from `Rc<RefCell<_>>` to `Arc<Mutex<_>>`.

**Verified live** (`docs/BUILD_LOG.md` entry 57) with NO matching-engine
process running at all (confirmed via `ps aux`): real sequenced depth
publishes purely from the simulated feed, real candle/trade/tick-range
query results, a real WebSocket client receiving correctly-sequenced
messages, and a real Python UDP client joining the multicast group and
decoding 5 real datagrams from the live process.

**Known limitations:** UDP multicast is a single fixed group/TTL 1, no
reliability/gap-detection layer, no auth/encryption; simulated feed's
symbol list/drift/volatility are hardcoded, not fully configurable;
columnar tick store duplicates trade-tick storage alongside the
existing candle aggregator's shorter trade tape (different retention
purposes, real duplication nonetheless); everything in-memory only.

**Correctness fix, `docs/BUILD_LOG.md` entry 87 — wire-format desync:**
`udpMulticastPublisher.rs`'s `writeLengthPrefixedString` wrote a `u8`
length prefix (max 255) while still writing the FULL string bytes for
anything longer, so a symbol/field over 255 bytes desynced every field
after it in the datagram (decoded length diverged from actual bytes
written). Fixed by widening the prefix to little-endian `u16` on both
the encode (`writeLengthPrefixedString`) and decode
(`ByteCursor::readU16`/`readLengthPrefixedString`) side.

### `volumeProfileAggregator.rs`, `orderFlowFootprintAggregator.rs` — new this build

FEATURES.md §20 (`docs/BUILD_LOG.md` entry 69). Real Volume Profile
computation over the columnar tick store — real Point of Control and
real Value Area (configurable %, default 70%) — plus a real TPO
(Time Price Opportunity) letter-based profile. Real per-price-level
buy-vs-sell footprint per candle, powered by the new `isBuyAggressor`
field threaded from matching-engine (see its section above). New
routes `GET /volumeProfile`, `GET /orderFlowFootprint`. 37 new tests,
115 total (up from 83).

**Verified live:** hand-worked fixture matched exactly over HTTP — POC
at the lower of two tied price levels, Value Area correctly extending
to cover the 70% target; a footprint split of buy=5/sell=3 at one price
level and buy=2/sell=7 at another reproduced exactly.

**Known limitations:** inherits the columnar store's 50k-tick retention
cap (older data silently drops); TPO's "letter A" starts at the
earliest tick in a query result, not a real session-open time.

**Correctness fix, `docs/BUILD_LOG.md` entry 87 — Value Area
calculation bug, actually two compounding bugs in
`computeValueAreaRange`:** (1) `bucketStep` used to be inferred from
the minimum gap between OCCUPIED bucket keys, understating the real
step whenever an intervening bucket has zero volume (and is therefore
absent from the map) — now threaded through explicitly from the
caller's real `priceBucketSizeInMinorUnits`. (2) the expansion loop
only checked the single immediate neighbor bucket on each side and
gave up on that side entirely if it was empty, even when a farther
occupied bucket existed — new `nextOccupiedBucketOutward` walks past
empty buckets to find the next real one. A regression test reproduces
the exact scenario (ticks at 100/130/140, empty buckets at 110/120)
and asserts the value area correctly reaches down to 100 instead of
stopping short at the POC (130).

### `watchlist.rs` sync extension, `livePnlWidget.rs` — new this build

FEATURES.md §21 "Cross-device watchlist/alert sync with a home-screen
live P&L widget" (`docs/BUILD_LOG.md` entry 80). The existing
per-account `WatchlistStore` gained a real millisecond-resolution
change log — `lastModifiedAtEpochMillisForAccount`,
`changesForAccountSince(sinceEpochMillis)`, and an optional
informational `deviceIdentifier` tag per change — the real technical
substance of cross-device sync beyond shared account-scoped storage,
letting a client ask "what changed since my last sync" instead of
re-fetching everything. **A real bug was caught and fixed**:
second-resolution timestamps let two same-second mutations collide
and silently drop from a "since" query; moved to millisecond
resolution. New `livePnlWidget.rs`: `computeLivePnlSnapshot` joins
oms-gateway's real per-position cost basis (`GET /mark-to-market`,
read-only) with market-data's own live last-trade price, computing
genuine unrealized P&L per position and a per-account total. New
routes: `GET /watchlist` (now includes `lastModifiedAtEpochMillis`),
`GET /watchlist/changes?accountIdentifier=...&sinceEpochMillis=...`,
`GET /pnl/live?accountIdentifier=...`. 19 new tests, 135 total (up
from 116).

**Verified live:** a real crossing trade produced a live P&L of
-24,800 against a live market price, matching the manual calculation
exactly; a `device-desktop`-tagged watchlist change was correctly and
exclusively returned by a delta query against a prior `device-phone`
sync point.

**A real gap in oms-gateway was found (documented, not fixed — out of
scope for this lane):** its mark-to-market endpoint only reveals a
position's cost basis after a price has ever been explicitly pushed
for that instrument, even though the cost basis exists internally from
the fill immediately.

**Known limitations:** still polling-based on both sides (no WebSocket
push for watchlist changes or P&L); the change log is unbounded and
unpersisted; a single blocking HTTP round-trip per P&L refresh with no
retry/backoff.

**Correctness fix, `docs/BUILD_LOG.md` entry 87 — P&L overflow:**
`computeLivePnlSnapshot` now returns
`Result<LivePnlSnapshot, LivePnlOverflowError>` — the raw
`netQuantity * (currentPrice - averageEntryPrice)` and the running
per-account total previously used plain `i64` arithmetic that could
silently wrap on an extreme (but constructible) input; now uses
`checked_sub`/`checked_mul`/`checked_add` throughout, returning a
clean error (mapped to HTTP 500 by the one caller in
`httpQueryServer.rs`) instead of wrapping or panicking.

---

## services/oms-gateway (Go) — Tier 1, risk-check path AND matching-engine hand-off are real

### `internal/orders/orderTypes.go`

| Type | Purpose |
|---|---|
| `OrderSubmissionRequest` | Client-facing order payload (JSON): account id, instrument, side, order type flags (`OrderIsMarketOrderNotLimit`, `OrderIsStopLossVariant`), limit price, `StopTriggerPriceInMinorUnits` (`*int64`, nil = not a stop order), quantity. |
| `TradeExecutionSummary` | Client-facing trade summary, deliberately its own type rather than reusing `matchingengineclient`'s wire type — the `orders` package shouldn't couple to one specific downstream client's shape. |
| `OrderAcknowledgementResponse` | Response to every order submission. Always carries `WasOrderAccepted`; on rejection, **always** carries a non-empty `HumanReadableRejectionReason` alongside the `MachineReadableRejectionReason` — this is the direct implementation of the FEATURES.md §21 "plain-language rejection reasons" differentiator. Also carries `TradeExecutionEvents` (populated on a successful match) and `MatchingEngineHandoffError` (populated if the matching engine couldn't be reached or rejected the order) — these are orthogonal to `WasOrderAccepted`: an order can be validly accepted (passed risk, was sequenced) while its matching-engine hand-off separately failed. |

### `internal/matchingengineclient/matchingEngineClient.go` — the real network hand-off

| Function | Purpose |
|---|---|
| `NewMatchingEngineClient(tcpAddress)` | Constructs a client pointed at matching-engine's TCP address (default `127.0.0.1:9101`, overridable via the `MATCHING_ENGINE_TCP_ADDRESS` env var). |
| `SubmitOrderAndAwaitMatchResult(wireRequest) (*OrderSubmissionWireResponse, error)` | Dials a fresh TCP connection, writes one JSON line, reads one JSON line back, closes the connection. Returns a Go `error` only for transport-level failures (unreachable, timeout, malformed response) — a business-level rejection (e.g. wrong instrument) comes back as a successfully-parsed response with `ErrorMessage` populated, not a Go error. |
| `CancelOrderAndAwaitResult(instrumentSymbol, orderSequenceNumberToCancel) (*OrderSubmissionWireResponse, error)` | Same transport-vs-business-error split as submission — "no such order" comes back as `WasOrderCancelled=false` on a successfully-parsed response, not a Go error. Shares the round-trip logic with `SubmitOrderAndAwaitMatchResult` via the private `sendOneLineAndAwaitResponse` helper. |
| `QueryOrderStatusAndAwaitResult(instrumentSymbol, orderSequenceNumberToQuery) (*OrderSubmissionWireResponse, error)` | Read-only status lookup, same shared round-trip helper. |

**Tested behavior** (with a fake in-test TCP server, no dependency on the
real Rust binary): successful submission parses trade events correctly;
a business-level rejection surfaces via `ErrorMessage`, not a Go error; an
unreachable matching engine does return a Go error.

**Known limitation:** one TCP connection per order, no pooling — a
placeholder bridge, not the lock-free ring buffer / SBE encoding
ARCHITECTURE.md §3.1/§3.5 describes. See this file's header comment.

### `internal/ledgerclient/ledgerClient.go` — the real network hand-off to ledger

| Function | Purpose |
|---|---|
| `NewLedgerClient(baseUrl)` | Constructs a client pointed at ledger's HTTP address (default `http://127.0.0.1:8082`, overridable via `LEDGER_BASE_URL`). |
| `FetchAccountBalance(accountId) (int64, error)` | `GET /accounts/balance` — used once at startup per demo account to seed the risk engine's cache from the real ledger instead of a hardcoded map. |
| `PostTradeSettlementJournalEntry(buyerId, sellerId, notional, seqNum) error` | Builds a balanced journal entry (buyer credited... i.e. debited by this ledger's convention, seller credited, routed through `firm-clearing-acct` as a net-zero pass-through so 2 debit lines sum to 2 credit lines) and posts it to `POST /journal-entries`. |

**Tested behavior** (against a real `httptest.Server`, no dependency on
the real ledger binary): balance fetch parses correctly; a settlement
post always sends a request where debit-sum equals credit-sum; a ledger-
side rejection surfaces as a Go error.

**Known limitation:** synchronous, called inline from the order-
submission request path — ARCHITECTURE.md says settlement belongs off
the hot path in an async consumer, not inside the same handler that
risk-checked and routed the order. See this file's header comment.

### `internal/riskengine/preTradeRiskEngine.go`

| Function | Purpose |
|---|---|
| `NewPreTradeRiskEngineWithSeedBalances(balances map[string]int64)` | Constructs the engine with a copied-in starting balance cache. |
| `EvaluateOrderAgainstAvailableMargin(accountId, orderNotionalValue) RiskCheckOutcome` | The synchronous, sub-millisecond pre-trade check. `RLock`-guarded read of the in-memory balance map — no DB round-trip. Returns a rejection with a specific plain-language message for two cases: account not found ("complete KYC first") and insufficient margin (states the exact shortfall). |
| `RefreshAccountBalanceFromLedger(accountId, balance)` | **New this build.** Overwrites (or adds) a cached balance with an authoritative value from the ledger — called once per demo account at startup in `cmd/server/main.go`. |
| `ApplyTradeSettlementToLocalCache(buyerId, sellerId, notional)` | **New this build.** Debits the buyer's and credits the seller's cached balance immediately after a fill, so the very next order from either account reflects the trade without waiting for another ledger round-trip. Explicitly labeled in the doc comment as a pragmatic shortcut — the real design refreshes this cache from an async ledger event stream, not a direct call from the same request handler that caused the trade. |

**Tested behavior:** in addition to the original 3 tests, 2 new ones
cover `RefreshAccountBalanceFromLedger` (overwrites an existing balance,
adds a previously-unknown account) and `ApplyTradeSettlementToLocalCache`
(buyer's margin decreases, seller's increases, by exactly the notional).

**Known limitation:** the startup sync is a one-shot fetch for a fixed
demo account list (`cmd/server/main.go`'s `demoTrackedAccountIdentifiers`)
— a balance change from any other path (a deposit, a second OMS
instance) is invisible to this cache until the process restarts.

**Correctness fix, `docs/BUILD_LOG.md` entry 87:** `EvaluateOrderAgainstAvailableMargin`
was `RLock`-guarded READ-ONLY, letting two concurrent orders from the
same account both read the same available margin and both pass — a
genuine reservation race, not just a cache-staleness concern. Now
`Lock`-guarded and atomically debits `availableMargin` within the same
call it approves in, with a new `ReleaseReservedMargin` for the
rejection path (wired from `cmd/server/main.go`'s deferred-release
block — see `internal/algolimits`/`internal/marginpledge` below for
the same pattern applied to two other reservation types).
Race-tested with 200 concurrent goroutines against a shared balance,
race-clean.

### `internal/kycclient/kycClient.go` and `internal/backofficeclient/backofficeClient.go` — the two new gates

Same shape as `ledgerclient`/`matchingengineclient`: `FetchKycStatus(accountId)`
and `FetchFreezeStatus(accountId)`, each returning a Go `error` only for
transport failures — an explicit "not eligible"/"frozen" answer is a
normal, successfully-parsed response. 3 tests each, against real
`httptest.Server`s.

### `internal/sequencing/sequenceNumberAllocator.go`

| Function | Purpose |
|---|---|
| `NewGlobalSequenceNumberAllocatorStartingAtOne()` | Constructs the allocator. |
| `AllocateNextSequenceNumber() uint64` | Returns a strictly increasing sequence number via `atomic.AddUint64` — deliberately lock-free since this is called on every order submission. |

**Known limitation:** one global counter for every instrument; needs to
become per-shard once the matching engine shards by instrument
(ARCHITECTURE.md §3.2).

### `internal/idempotency/idempotencyStore.go` — FEATURES.md §2 "idempotent transactions"

| Function | Purpose |
|---|---|
| `NewIdempotencyStore()` | Constructs an empty, in-memory-only store. |
| `NewPostgresBackedIdempotencyStore(ctx, postgresDsn) (*IdempotencyStore, error)` — new (`docs/BUILD_LOG.md` entry 85) | Connects to real Postgres, applies migrations, returns a store whose COMPLETED responses are durably cached. See "Real Postgres persistence" below for exactly what is and isn't Postgres-backed. |
| `ClaimKeyOrAwaitExistingResponse(key) (response, isThisCallTheOwner bool)` | The real entry point for a request carrying an idempotency key. Exactly one caller per key becomes the owner (does the real work); every other concurrent/later caller BLOCKS (up to a bounded timeout) and gets the owner's completed response instead of redoing the work — genuine concurrent-duplicate collapsing via a channel handoff, not just sequential-retry caching. An empty key always claims nothing and returns owner=true immediately (client opted out). |
| `CompleteClaimedKey(key, response)` | Called by the owner once real work finishes — records the response and wakes every blocked waiter. |
| `DeleteExpiredResponses() (int64, error)` — new | Postgres-backed mode only (a no-op returning 0 otherwise): deletes every row whose TTL has passed. NOT run on any timer by this package — an operator/scheduled job must call it, same "sweep due X is explicit" convention as `withdrawalworkflow.ProcessDueWithdrawals`. |

**Tested behavior:** first caller for a new key becomes the owner; a
sequential retry with the same key returns the recorded response
without redoing the work; different keys never collide; an empty key
never claims or blocks; concurrent duplicate requests collapse into
exactly one execution (a dedicated `-race`-clean test); a claim that's
never completed times out with a specific machine-readable reason
rather than hanging forever.

**Real Postgres persistence** (`docs/BUILD_LOG.md` entry 85): the
in-process claim/await mechanism above (`entriesByKey`/
`claimedKeyEntry`/`doneChannel`) is deliberately UNCHANGED and stays
purely in-memory — it is a single-process Go channel primitive that
cannot be meaningfully distributed across processes via a Postgres row
without a much larger redesign, explicitly out of scope. What Postgres
adds (`internal/idempotency/postgresBacking.go`,
`services/oms-gateway/migrations/0003_idempotency.sql`'s
`idempotency_responses` table, a real `expires_at` TTL column
defaulting to 24h) is durability of the FINAL answer for an
already-completed key: on a same-process cache miss,
`ClaimKeyOrAwaitExistingResponse` also checks the durable Postgres
cache (releasing the in-memory mutex before the network round-trip)
before deciding this caller must claim ownership and redo the work —
closing both the "doesn't survive a restart" AND "unbounded,
unexpiring" halves of this package's original TODO at once.

**Tested behavior, Postgres-backed** (3 new tests, real local Postgres):
claim → complete → a same-process replay is served from the in-memory
map with the recorded response; a brand-new store (simulating a fresh
process after a restart) still finds and serves the durably-cached
response for an already-completed key without becoming the owner;
`DeleteExpiredResponses` genuinely removes an expired row, after which
a FRESH store's claim for that key correctly becomes owner again (the
SAME process's in-memory map, once it has completed a key, never
re-arms it even after the underlying Postgres row is deleted — a real,
documented distinction found while writing this test, not assumed).

**Known limitations:** concurrent-duplicate-collapsing is still
single-process-only — a second oms-gateway instance behind a load
balancer would not see another instance's in-flight (not-yet-completed)
claim, only its completed responses once durably persisted.
`DeleteExpiredResponses` is not run on any timer by this package.

### `internal/marketsession/marketSessionState.go` — FEATURES.md §3 AMO

| Function | Purpose |
|---|---|
| `NewMarketSessionState()` | Constructs a session that starts CLOSED. |
| `IsMarketOpen() bool` / `SetMarketOpen(bool)` | RWMutex-guarded boolean — cheap concurrent reads (every order submission checks this), infrequent writes (an admin flips it). |

**Known limitation:** a plain admin-toggled flag, not a real clock-driven
trading calendar (pre-open/continuous/closing-auction/holidays).

**Correctness fixes, `docs/BUILD_LOG.md` entry 87:**
`cmd/server/main.go`'s `buildMarketSessionCloseHandler`/
`buildMarketSessionOpenHandler` previously accepted ANY HTTP method —
a stray `GET` could silently open or close the market. Both now
reject non-`POST` with 405. Separately, the AMO-drain response (fired
from the open handler) now reports
`totalDrainedAfterMarketOrders`/`acceptedAfterMarketOrders`/
`rejectedAfterMarketOrders` with a per-rejection log line — the drain
already executed correctly, but previously gave an operator zero
visibility into how many queued after-market orders silently failed
re-submission at market open.

### `internal/amoqueue/afterMarketOrderQueue.go` — FEATURES.md §3 AMO

| Function | Purpose |
|---|---|
| `NewAfterMarketOrderQueue()` | Constructs an empty queue. |
| `Enqueue(request)` | Appends an `orders.OrderSubmissionRequest` to the back. |
| `QueuedCount() int` | Current queue length. |
| `DrainAll() []orders.OrderSubmissionRequest` | Removes and returns everything queued, oldest first, leaving the queue empty. |

**Tested behavior:** starts empty; `Enqueue` increments count;
`DrainAll` returns requests in FIFO order and empties the queue;
draining an already-empty queue returns an empty slice.

**Known limitations:** in-memory only (an oms-gateway restart between
market close and open silently loses every queued AMO); drained
all-at-once by an explicit admin call, not a real market-open event.

### `internal/audittrail/auditTrail.go` — FEATURES.md "Audit trail: immutable log of every order, modification, cancellation" — real-Postgres-backed by default (`docs/BUILD_LOG.md` entry 85)

| Item | Purpose |
|---|---|
| `EventType` | Closed set of string constants (`ORDER_SUBMITTED`, `ORDER_REJECTED`, `ORDER_MATCHING_ENGINE_FAILURE`, `ORDER_FILLED`, `ORDER_CANCELLED`, `ORDER_CANCEL_FAILED`, `COVER_PROTECTIVE_LEG_PLACED`, `COVER_PROTECTIVE_LEG_FAILED`, `AFTER_MARKET_ORDER_QUEUED`, `MARKET_SESSION_OPENED`, `MARKET_SESSION_CLOSED`, plus several later-added event types — paper-trading fills, strategy-limit rejections, matching-engine hand-off) — not free text, so a compliance query can filter reliably. |
| `Entry` | One immutable record: `RecordedAtTime` (stamped by `Append`, never caller-supplied), `EventType`, `ClientAccountIdentifier`, `InstrumentSymbol`, `MatchingEngineOrderSequenceNumber`, `DetailMessage`, plus structured fill/order-shape fields for surveillance. **New this build:** `AuthenticatedActorAccountIdentifier` — the REAL authenticated caller (`authmiddleware.AuthenticatedAccountIdentifier(request)`), distinct from `ClientAccountIdentifier` (the account the action is FOR). Empty for the two internal, non-per-request-authenticated callers (the DMA/FIX-inspired gateway, the auto-liquidation engine) — honestly empty, never guessed. |
| `NewAuditTrail()` | Constructs an empty, in-memory-only trail. |
| `NewPostgresBackedAuditTrail(ctx, postgresDsn) (*AuditTrail, error)` — new | Connects to real Postgres, applies migrations, returns a trail whose `Append`/`AllEntries`/`EntriesForAccount` all read/write real Postgres — Postgres becomes the sole source of truth in this mode, not a mirror of the in-memory slice. Same struct type as `NewAuditTrail()` returns; every existing caller across `cmd/server/main.go` needed zero changes. |
| `Append(entry)` | Stamps `RecordedAtTime` with `time.Now()` and appends (or, Postgres-backed, `INSERT`s). No corresponding update/remove method exists ANYWHERE on this type — that's the actual immutability guarantee, not just a naming convention. A Postgres write failure is logged, not panicked (this method's signature has no error return). |
| `AllEntries() []Entry` | Returns every entry, oldest first. |
| `EntriesForAccount(id) []Entry` | Same, filtered to one account; entries with no account (e.g. market-session events) never match. |

**Tested behavior:** starts empty; `Append` stamps a real timestamp and
preserves every field; `AllEntries` preserves append order and returns a
genuine copy; `EntriesForAccount` filters correctly and returns empty for
an unknown account.

**Real Postgres persistence** (`internal/audittrail/postgresBacking.go`,
`services/oms-gateway/migrations/0001_audit_trail.sql`'s
`audit_trail_entries` table, one row per `Entry` field 1:1): 4 new
tests against a real local Postgres — append + read-back, per-account
filtering, a nil vs. populated `OrderSideIsBuyNotSell` round-trips
correctly through Postgres's nullable boolean column, and data posted
through one connection is visible from a brand-new connection (the
persistence proof at the unit level).

**Verified live** (`docs/BUILD_LOG.md` entry 85): a real submitted
order's audit rows, read directly from Postgres, carried the correct
`authenticated_actor_account_identifier`; the real oms-gateway process
was killed and restarted, and the row count plus every field (including
the new actor field) matched exactly before and after. **A real bug was
caught and fixed during this verification**: a first pass at threading
the new actor field through all 31 `auditTrail.Append` call sites
silently missed 9 of them (`gofmt`'s struct-literal column alignment
defeated a naive text-substitution match) — caught by writing a small
completeness checker before declaring the work done, not by trusting
the initial pass.

**Known limitation, still real but narrower than before:** WORM
enforcement is by application-code convention only (this codebase's
code never issues `UPDATE`/`DELETE` against `audit_trail_entries`) —
a real deployment additionally needs a database-level `REVOKE UPDATE,
DELETE` grant on the table for the application's own role, which this
dev-default Postgres user does not have applied.

**Wired into `cmd/server/main.go`** at every consequential decision
point: `processOrderSubmission` logs `ORDER_SUBMITTED`/`ORDER_REJECTED`
(KYC, freeze, and risk rejections all distinguished by `DetailMessage`)/
`ORDER_MATCHING_ENGINE_FAILURE`/`ORDER_FILLED`; the cancel handler logs
`ORDER_CANCELLED`/`ORDER_CANCEL_FAILED`; the cover-order handler logs
`COVER_PROTECTIVE_LEG_PLACED`/`COVER_PROTECTIVE_LEG_FAILED`; AMO queueing
logs `AFTER_MARKET_ORDER_QUEUED`; the market-session handlers log
`MARKET_SESSION_OPENED`/`MARKET_SESSION_CLOSED`. `orderSubmissionDependencies`
carries the `auditTrail` reference so every caller of
`processOrderSubmission` (plain submission, DMA gateway,
auto-liquidation, cover-order entry legs, AMO drain, conversational-
order confirm, multi-leg options, basket orders, dividend reinvestment)
logs through the exact same code path.

### `internal/positions/positionBook.go` — FEATURES.md §3 "positions / holdings views" — real-Postgres-backed by default for the REAL position book (`docs/BUILD_LOG.md` entry 85)

| Function | Purpose |
|---|---|
| `NewPositionBook()` | Constructs an empty, in-memory-only book. Still used directly for `paperPositionBook` (paper trading) and `milliSharePaperPositionBook` (fractional shares) — deliberately NOT Postgres-backed, since a simulated position was never real money/holdings; see Known limitations. |
| `NewPostgresBackedPositionBook(ctx, postgresDsn) (*PositionBook, error)` — new | Connects to real Postgres, applies migrations, returns a book whose `ApplyFill`/`SetPositionDirectly`/`PositionsForAccount` all read/write real Postgres. |
| `ApplyFill(buyerId, sellerId, instrument, qty)` | Increments the buyer's, decrements the seller's net signed quantity for that instrument. Postgres-backed: a real atomic `INSERT ... ON CONFLICT DO UPDATE SET net_quantity = positions.net_quantity + EXCLUDED.net_quantity` — no read-then-write race. |
| `SetPositionDirectly(accountId, instrument, newQuantity)` | Overwrites an absolute quantity (corporate-action adjustments) — Postgres-backed via the same atomic-upsert pattern. |
| `PositionsForAccount(accountId) map[string]int64` | Returns a copy of the account's non-zero positions; net-zero instruments are omitted rather than returned as an explicit 0. |

**Tested behavior** (5 in-memory tests): buy/sell increment/decrement
correctly; multiple fills accumulate; a position that nets back to zero
is omitted; positions are tracked independently per instrument; an
unknown account has none.

**Real Postgres persistence** (`internal/positions/postgresBacking.go`,
`services/oms-gateway/migrations/0002_positions.sql`'s `positions`
table, primary key `(account_identifier, instrument_symbol)`): 6 new
tests against a real local Postgres, covering the same behaviors as
the in-memory tests plus `SetPositionDirectly` overwriting an existing
quantity and persistence across a fresh connection.

**Verified live** (`docs/BUILD_LOG.md` entry 85): a real crossing trade
between two real accounts through the real running oms-gateway process
(`acct-001` buys 3 from `acct-002`, both risk-checked and settled
against a real ledger) produced `{"DEMO-EQ": 3}`/`{"DEMO-EQ": -3}` via
`GET /positions`; the process was killed and restarted; the SAME query
returned the SAME positions (later accumulated to 5/-5 after a second
trade + restart cycle, confirming accumulation itself survives a
restart, not just a single snapshot).

**Known limitations:** net quantity only — no average cost basis, no
realized/unrealized P&L. Only the REAL `positionBook` is Postgres-
backed; `paperPositionBook` and `milliSharePaperPositionBook` remain
in-memory, unchanged, deliberately out of scope for this pass.

### `internal/metrics` — new this build

Real latency histograms, FEATURES.md §13's "Metrics/tracing (latency
histograms on the execution path especially)". Stdlib only, no
Prometheus client library — but the OUTPUT is genuine Prometheus text
exposition format.

| Item | Purpose |
|---|---|
| `Histogram` (`histogram.go`) | Prometheus-style CUMULATIVE bucket counts (11 buckets, 1ms-5000ms) + observation count/sum. `Observe(valueMs)` increments every bucket at or above the value. `Snapshot()` returns an independent, lock-free-to-read copy. |
| `Registry` (`registry.go`) | One `Histogram` per (method, path) pair, created lazily. `WritePrometheusText(writer)` formats every histogram in real Prometheus text exposition format, routes sorted for deterministic output. |
| `WithRequestTiming(registry, handler)` (`middleware.go`) | Wraps a handler, records each request's wall-clock duration into `registry` keyed by `(request.Method, request.URL.Path)`. No response-wrapping needed (histograms aren't keyed by status code). |
| `BuildMetricsHandler(registry)` | `GET /metrics` handler serving the current Prometheus text. |

**Tested behavior** (14 tests across `histogram_test.go`,
`registry_test.go`, `middleware_test.go`): bucket cumulative-counting
semantics (a value only increments buckets at/above it); count/sum
accumulate correctly; a value exceeding every finite bucket still counts
toward the total; a snapshot is a frozen copy unaffected by later
observations; different routes get independent histograms; the same
route observed twice accumulates on one histogram; output contains
valid Prometheus HELP/TYPE headers and bucket/sum/count lines even for
an empty registry; the timing middleware preserves the wrapped handler's
response untouched; the `/metrics` handler rejects non-GET.

**Verified live**: started the full stack, hit `/health` and
`/orders/submit` for real, and confirmed `GET /metrics` returned
correct, independent histograms for each route with accurate bucket
counts, sum, and count against the actual observed traffic (e.g.
`/orders/submit`'s `_count` was exactly 3 after 3 real submissions,
`_sum` matched the real elapsed milliseconds).

**Known gap**: only wired into oms-gateway. Matching-engine — the
service this FEATURES.md item's "execution path especially" most
directly means — has no metrics at all, since it has no HTTP listener
to expose them on; giving it one (the way market-data grew a query API
listener) is real, separate work not done here.

### `internal/chargescalculator` — new this build

Real pre-order charges breakdown, FEATURES.md §21's "Full charges
breakdown *before* order confirmation: brokerage, STT/CTT, stamp duty,
GST, exchange transaction charges, DP charges."

| Item | Purpose |
|---|---|
| `CalculateCharges(isBuy, priceInMinorUnits, quantity, isIntraday) -> ChargesBreakdown` | Computes turnover and every charge line item independently off it (GST is the one exception — it depends on brokerage + exchange transaction charge + SEBI fee), returning a full receipt-shaped struct including `NetAmountInMinorUnits` (turnover + charges for a buy, turnover − charges for a sell). |
| Rate rules (package-level constants, all documented "illustrative, not authoritative") | Brokerage: 0 for delivery, `min(₹20, 0.03%×turnover)` for intraday. STT: 0.1% BOTH sides for delivery, 0.025% SELL-side-only for intraday. Stamp duty: BUY-side-only, 0.015% delivery / 0.003% intraday. DP charge: flat ₹15, delivery SELL only. Exchange transaction charge and SEBI turnover fee: small fixed rates, both sides/segments. GST: 18% of (brokerage + exchange transaction charge + SEBI fee) only — not of STT or stamp duty, which are themselves taxes. |

**Tested behavior** (8 tests): a fully hand-worked example (₹100.00
delivery buy) checked line-by-line against the package's own rate
constants — the strongest kind of test here, since it would catch a
wrong formula even if the code were internally self-consistent; delivery
sell incurs the DP charge but not stamp duty; intraday buy has no STT
but does have stamp duty (the buy/sell asymmetries the opposite way from
delivery); intraday sell has STT but no stamp duty; brokerage correctly
switches between percentage-based and the flat cap depending on order
size; a zero-quantity order produces all-zero charges; net amount is
above turnover for a buy and below it for a sell.

**Verified live**: `POST /orders/estimate-charges` on the real running
service returned exactly the same 119-paise total charges for a real
₹100.00 delivery buy that the hand-worked unit test predicts; confirmed
malformed input 400s; confirmed this new route automatically appears in
`GET /metrics` (the metrics middleware from the prior increment applies
to every route, including new ones, with no per-route wiring needed).

**Known, loudly documented gap**: every rate is an ILLUSTRATIVE model —
not fetched from any live regulatory/exchange source, will drift out of
date (STT/stamp-duty rates change by government notification on no
predictable schedule), and doesn't capture state-by-state stamp-duty
variation or F&O/currency/commodity segment differences. A real build
needs a maintained, versioned, centrally-updated rate table, not
hardcoded Go constants.

### `cmd/server/main.go`

**`cmd/server/main_test.go` — new (`docs/BUILD_LOG.md` entry 87):** a
real integration harness exercising `processOrderSubmission` and the
market-session handlers end-to-end against FOUR fakes — real
`httptest.Server`s standing in for KYC and backoffice, a configurable
fake ledger server (toggleable success/failure, used to prove the
settlement-rollback fix below), and a real TCP listener speaking
oms-gateway's actual matching-engine wire protocol (matching-engine
isn't HTTP, so this is a hand-rolled fake socket server, not
`httptest`). 10 tests, several directly reproducing one of this
entry's specific findings (concurrent margin over-approval, algo-
limits/exposure-limits reservation leaks on rejection, settlement
failure leaving the position book untouched, non-`POST` market-session
requests).

| Route / Function | Behavior |
|---|---|
| `GET /health` | Liveness check. |
| `POST /orders/submit` | Decodes `OrderSubmissionRequest` → **idempotency check** (a hit short-circuits everything below, returning the cached response) → **AMO check** (if `OrderIsAfterMarketOrder` and the market is closed, queues into `afterMarketOrderQueue` and returns `IsQueuedAsAfterMarketOrder:true` WITHOUT touching idempotency-caching, KYC, freeze, or risk) → delegates to `processOrderSubmission` (see below) → caches the result under `IdempotencyKey` and responds. |
| `processOrderSubmission(deps, request) OrderAcknowledgementResponse` | The actual pipeline, extracted out of the HTTP handler so `buildCoverOrderHandler` can reuse it for an entry leg: **KYC gate** → **freeze gate** → computes notional (`price × qty`; always 0 for a market/stop-market order — known gap, see file comment) → risk check → on approve, allocates a sequence number, calls `matchingEngineClient.SubmitOrderAndAwaitMatchResult`, captures `AssignedOrderSequenceNumber` into the response's `MatchingEngineOrderSequenceNumber`, and on a real fill calls `settleTradeAgainstLedgerAndLocalCache` for each trade — `positionBook.ApplyFill` now only runs when settlement SUCCEEDED (`docs/BUILD_LOG.md` entry 87 fix, see `settleTradeAgainstLedgerAndLocalCache` below); a failed settlement is surfaced on the response via `SettlementFailures` instead of silently mutating the position book as if the trade had cleared. Also `defer`s a release of any provisional algo-limits/exposure-limits/pledge-book reservation the order took, if it never reached acceptance (same entry 87 fix — closes the reservation-leak half of the risk-engine/pledge-book/algo-limits races documented in their own sections above). Takes an `orderSubmissionDependencies` struct instead of a long parameter list. |
| `GET /positions?accountId=...` | Calls `positionBook.PositionsForAccount`, returns net quantity per instrument. |
| `POST /orders/cancel` | Decodes `CancelOrderRequest` → `matchingEngineClient.CancelOrderAndAwaitResult` → `CancelOrderResponse`. Deliberately NOT risk/KYC/freeze-gated (cancelling can only reduce exposure). Every failure path now populates a genuine plain-language `ErrorMessage` on the response itself (`docs/BUILD_LOG.md` entry 41 fixed a real gap where the not-found case's reason only ever reached the audit trail, never the client). Known gap: doesn't verify the caller owns the order being cancelled (no auth anywhere yet). |
| `POST /orders/cover-submit` | FEATURES.md §3 Cover Orders: decodes `CoverOrderRequest` → runs the entry leg through `processOrderSubmission` → if it filled (sums `ExecutedQuantity` across its trades), places a `StopLossMarket` order on the opposite side for the filled quantity via a direct `matchingEngineClient` call — bypassing KYC/freeze/risk (same "can only reduce exposure" rationale as cancellation). If the entry didn't fill at all, no protective leg is placed. Returns `CoverOrderResponse` with the entry's ack plus the protective leg's id or, if placing it failed, a loudly-logged `ProtectiveStopOrderError`. |
| `GET /orders/status?instrumentSymbol=...&matchingEngineOrderSequenceNumber=...` | Calls `matchingEngineClient.QueryOrderStatusAndAwaitResult`, returns `orders.OrderStatusResponse`. Read-only, not gated. |
| `GET /market-session/status` | Returns `isMarketOpen` + `queuedAfterMarketOrders` (from `afterMarketOrderQueue.QueuedCount()`). |
| `POST /market-session/close` | Sets `marketSession` closed. |
| `POST /market-session/open` | Sets `marketSession` open, then SYNCHRONOUSLY drains every queued AMO through `processOrderSubmission` — the exact same real pipeline any other order goes through, run fresh (KYC/freeze/risk all re-evaluated at drain time, not at queue time). Response includes every drained order's real `OrderAcknowledgementResponse`. Known gap: blocks the caller on however long the drain takes; a real build would ack immediately and drain asynchronously with a way for clients to observe each AMO's outcome later. |
| `withPermissiveCorsForDevelopment(handler)` | Wraps the whole mux — sets `Access-Control-Allow-Origin: *` (+ Methods/Headers) on every response and short-circuits `OPTIONS` preflights with `204`. Without this, `apps/web` (a different origin/port in dev) couldn't call any oms-gateway endpoint from a real browser at all. Known gap, explicitly flagged in its own doc comment: `*` is fine for a no-auth demo, wrong the moment cookies/bearer tokens exist — a real build must echo back an allow-listed origin instead. |
| `GET /audit-trail` (optionally `?accountId=...`) | Calls `auditTrail.AllEntries()` or `EntriesForAccount`, returns the raw `[]audittrail.Entry`. |
| `GET /metrics` | Serves `internal/metrics`'s registry as real Prometheus text exposition format — a genuine external tool could scrape this. |
| `POST /orders/estimate-charges` | Decodes side/price/quantity/segment, calls `chargescalculator.CalculateCharges`, returns the full `ChargesBreakdown`. Read-only — no KYC/freeze/risk/idempotency gating, purely a pre-order quote. |
| KYC gate | Calls `kycClient.FetchKycStatus`. An explicit `IsEligibleToPlaceOrders=false` rejects with `KYC_NOT_VERIFIED` (fails **closed**). A transport error logs a warning and lets the order proceed (fails **open**) — see the extensive doc comment in `main.go` on why, and the explicit caveat that real capital would likely want the opposite. |
| Freeze gate | Same shape, calls `backofficeClient.FetchFreezeStatus`, rejects with `ACCOUNT_FROZEN` (+ the recorded reason) on an explicit frozen answer, fails open on transport error. |
| `syncRiskEngineBalancesFromLedger` | Runs once at startup: fetches each account in `demoTrackedAccountIdentifiers` from `ledgerClient` and seeds the risk engine's cache. Logs and continues (doesn't crash) if the ledger isn't up yet. |
| `settleTradeAgainstLedgerAndLocalCache` | For one fill: posts the settlement journal entry via `ledgerClient`, then — only on success — applies the same adjustment to the local risk cache via `ApplyTradeSettlementToLocalCache`. A failed settlement post is logged loudly (`SETTLEMENT FAILED`) AND (as of `docs/BUILD_LOG.md` entry 87) now returns a real `error` instead of only logging — the caller (`processOrderSubmission`) uses that to skip `positionBook.ApplyFill` and record the failure in the response's `SettlementFailures` plus a new `audittrail.EventSettlementFailedPositionNotApplied` entry, rather than the pre-fix behavior of applying the fill to the position book regardless of whether settlement actually succeeded. There's still no reconciliation/retry job to automatically resolve a settlement failure once surfaced. |

**Verified end-to-end** (`docs/BUILD_LOG.md` entries 14–16, all five
services running together): a resting buy produces no trades; a crossing
sell against it returns the real fill in `tradeExecutionEvents` and moves
real ledger balances (checked via `GET /accounts/balance` before/after);
an over-margin order is rejected by the risk engine and never reaches the
matching engine at all; a follow-up order is rejected using the
just-updated (not stale) local cache, confirming the settlement loop
closes without a restart; an order is rejected before KYC submission,
accepted after; an order is rejected while frozen (with the recorded
reason), accepted after unfreezing; and both gates were confirmed to fail
open (with a logged warning) when their upstream service was stopped.

**Known limitation:** no FIX/WS session handling, no per-client
backpressure/throttling. The matching-engine hand-off and the ledger
settlement post are both synchronous placeholders, not the real
ring-buffer / async-event-consumer design ARCHITECTURE.md describes.

### `internal/marginpledge`, `internal/marginengine`, `internal/orders` execution types — new this build

FEATURES.md §3: "Margin Pledge system", "SPAN + Exposure margin
calculator for F&O", "Iceberg, FOK, IOC order types". `marginpledge`
lets an account pledge held stock quantity as collateral — increases
`riskengine`'s `AvailableMarginInMinorUnits` by (quantity × price ×
1-haircut), marks that quantity unavailable for sale, refuses to
unpledge quantity currently backing open utilized margin. `marginengine`
computes an illustrative SPAN + Exposure margin (explicitly not an
exchange-certified SPAN file). `orders.OrderExecutionType` gained
ICEBERG/FOK/IOC — this is ACCEPTANCE and VALIDATION only (an Iceberg
order without a visible-quantity sub-field, or one exceeding total
quantity, is rejected); true fill semantics need matching-engine support
that doesn't exist, and every accepted order of these types logs a loud
boundary warning saying so. New routes: `/margin-pledge/pledge`,
`/unpledge`, `/set-utilized-margin`, `/margin-pledge?accountId=`,
`/margin/calculate-span-exposure`. 38 new tests (17 pledge + 11 margin
engine + 10 order-type validation), including a `-race`-targeted
concurrent pledge/unpledge test and a fully hand-worked SPAN example.

**Verified live** (`docs/BUILD_LOG.md` entry 48) across a real 5-service
run: pledging increased available margin by the exact hand-computed
amount and reduced an oversized order's shortfall by that same amount;
selling into pledged quantity was blocked, selling the unpledged
remainder succeeded; unpledging released a proportional amount exactly;
utilized margin above the safe remainder genuinely blocked a further
unpledge; the SPAN calculator matched its hand-worked test over HTTP; a
valid Iceberg/FOK order was accepted and filled under ordinary
continuous-matching rules (the documented boundary), invalid ones were
rejected with clear errors.

**Known limitations:** pledge haircuts and SPAN/exposure rates are
illustrative, not real regulatory tables; pledge reference price is
caller-supplied (no live price feed here yet); utilized margin for open
derivative positions is set via an admin-style endpoint, not derived
from a structured F&O position book (doesn't exist yet).

**Correctness fix, `docs/BUILD_LOG.md` entry 87:** the pledged-holding
sell-reservation check used to read `PledgedQuantity()` and
`PositionsForAccount()` as two INDEPENDENT reads, letting two
concurrent sell attempts against the same unpledged remainder both
pass — a genuine reservation race, same family as `riskengine`'s fix
above. A new `reservedForPendingSellByAccountAndSymbol` map plus an
atomic `ReserveUnpledgedQuantityForSell`/`ReleaseSellReservation` pair
closes it. Race-tested: 3 concurrent goroutines each trying to sell 60
of a 100-share holding correctly leave exactly 1 winner.

### `internal/marginfunding`, `internal/optionschain`, `internal/dmagateway`, `internal/papertrading`, `internal/algolimits` — new this build

Five FEATURES.md items built sequentially in one pass (§2 P2, §3 P2/P4,
§7 P3/P4).

**`marginfunding`** ("Margin funding / instant margin against pledged
collateral payout"): real cash disbursement up to unpledged collateral
value via a real journal entry through `internal/ledgerclient`
(`PostMarginFundingDisbursementJournalEntry` /
`...RepaymentJournalEntry`), tracked as a real outstanding loan balance.
16 tests. **Bug caught via live verification**: an early draft credited
the client account for a disbursement, which under `doubleentry`'s real
"debit increases" convention actually decreased it — caught when the
balance moved the wrong direction live, fixed by swapping the debit/
credit lines.

**`optionschain`** + **`quantengineclient`** ("Real-time Options Chain"
+ "Greeks computed live per contract"): a synthetic strike ladder with
illustrative OI/Volume, but REAL Greeks/IV per contract via a real HTTP
call to quant-engine's live Black-Scholes endpoint, and a real PCR
computed from the synthetic OI. Degrades to a clean 502 (not a crash) if
quant-engine is down — verified live by killing it mid-run. 21 tests.

**`dmagateway`** ("Direct Market Access (DMA) / FIX gateway"): a real
session-based TCP protocol on port 8088 — Logon/NewOrderSingle/
ExecutionReport/Logout with real sequence-number enforcement (an
out-of-sequence message disconnects the session) — **explicitly,
repeatedly NOT FIX-certified**: no real FIX tag numbers, no
SOH/BeginString/CheckSum framing, no ResendRequest/SequenceReset
gap-fill. Reuses the same internal order-submission path as the HTTP
API, so a DMA order is genuinely risk-checked. 23 tests. Verified live
with a standalone Python TCP client: full session lifecycle, a
genuinely risk-rejected order, an out-of-sequence rejection, a
pre-Logon rejection, and confirmation the accepted order hit the real
audit trail.

**`papertrading`** ("Paper trading mode sharing the exact same OMS code
path as live"): a paper order goes through the identical risk engine
and audit trail as a live order, but the matching-engine hand-off is
replaced with a simulated fill and nothing posts to ledger. 9 tests.
Verified live: paper fills left the real ledger balance and real
`/positions` untouched while `/paper-positions` accumulated separately;
an oversized paper order was genuinely rejected by the real risk
engine, not rubber-stamped.

**`algolimits`** ("Strategy resource limits & circuit breakers"): real
per-`strategyId` rate limiting (orders/sec) and daily notional caps,
enforced before an order reaches the risk engine or matching-engine. 18
tests. Verified live: rate-limit rejection, refill-then-succeed, the
exact daily-cap boundary, post-cap rejection, untagged orders unaffected.
A sub-1 orders/sec rate (e.g. 0.5/sec) and a missing rejection-path
`Release()` were both real bugs, fixed in `docs/BUILD_LOG.md` entry
87 — see this file's own `internal/algolimits` entry above.

**Known limitations per item:** margin funding's interest formula isn't
auto-applied and reuses the existing clearing account; options chain
OI/Volume are illustrative and "IV" is an assumed flat input, not solved
from real market prices (needs a real listed-options feed to be truly
complete); the DMA/FIX gateway needs a certified FIX engine and real
exchange onboarding to be genuinely complete; paper trading's SELL gate
still checks real positions (conservative simplification); algo limits
are per-strategy only, no global circuit breaker, config in-memory.

**Correctness fixes, `docs/BUILD_LOG.md` entry 87:** `algolimits`'s
`strategyLimiter.go` gained a `Release()` (reverses both the rate-limit
token and daily-notional consumption a rejected order had
provisionally reserved) and a sub-1 orders/sec rate-limit floor fix —
`bucketCapacity = max(1.0, rate)` — since a sub-1 rate (e.g. 0.5/sec)
previously capped its own token refill below 1.0 and could never
accumulate a full token, permanently blocking every order tagged with
that strategy. The actual bug enabling both `Release()`s (this one and
`exposurelimits.ReleaseExposure`, which already existed) to matter was
missing WIRING in `cmd/server/main.go`'s `processOrderSubmission` — it
took a reservation on submission but never released it on a downstream
rejection; a `defer`-based release block now covers both.

### `internal/strategyfollowing`, §12 risk packages, §15 execution/derivatives packages, §17 DRIP/LAS — new this build

FEATURES.md §§11,12,15,17 (`docs/BUILD_LOG.md` entries 59–62).

**`strategyfollowing`** (§11): a real opt-in follow/unfollow relationship
graph + admin-verified strategy registry — explicitly NO order mirroring
or auto-copying, disclosed. 18 tests.

**§12 "Risk, Margin & Surveillance" — 4 packages:** `marktomarket` (real
weighted-average-cost-basis unrealized P&L for leveraged accounts only,
13 tests); `autoliquidation` (real WARNING/URGENT/LIQUIDATION graduated
thresholds, with real reducing MARKET SELL orders submitted through the
live order pipeline ONLY at breach, 15 tests — **fill-vs-accept
accounting bug fixed in `docs/BUILD_LOG.md` entry 87**:
`SubmitReducingOrderFunc` used to let the caller ASSUME
`quantityToSell * price` was the real proceeds; it now returns the
ACTUAL executed notional from `TradeExecutionEvents`, so a partially-
filled or differently-priced liquidation order no longer understates
remaining shortfall); `exposurelimits` (real
pre-trade per-account AND per-segment notional caps, 12 tests including
a 200-goroutine concurrency test); `connectivitykillswitch` (real
MANUAL + AUTO trading halt on 3 consecutive matching-engine failures —
**a self-healing bug was caught and fixed**: the initial design had no
way to recover once auto-engaged since the halt blocked its own only
connectivity signal, fixed with an independent background prober, 12
tests). All four verified live against real running services.

**§15 "Advanced Execution & Trading Sophistication" — 8 items, all
built:** `executionalgos` (real TWAP/VWAP/POV parent-order slicing,
largest-remainder apportionment, deterministic time-parameterized
scheduler, 13 tests); `payoffdiagram` (exact piecewise-linear options
payoff math — not sampling — real max profit/loss/breakevens, 15 tests,
textbook cases matched exactly); `multilegoptions` (real named-strategy
leg validation + atomic execution with real compensating-order rollback
— **a bug was found and fixed**: iron condor's buy/sell direction
checks were inverted, 27 tests); `basketorders` (net-cash-constrained
aggregate multi-instrument execution, 20 tests); `impactcostestimator`
(real walk-the-book slippage estimate over a caller-supplied depth
snapshot, 13 tests); `marginengine`'s new portfolio/cross-margining
(illustrative-correlation-table netting benefit, 21 tests);
`securitieslendingborrowing` (real lend/borrow state machine, real
day-count fee formula, 20 tests); `marketsession`'s new PRE_MARKET/
POST_MARKET phases (real LIMIT-only enforcement, 13 tests). Every item
live-verified against a real running server, several matching
hand-worked numbers exactly.

**§17 "Wealth & Product Breadth" — DRIP and LAS built at the time:**
`drip` (real dividend-credit + toggleable auto-reinvestment through the
live order path, plus new `ledgerclient` journal-entry tests, 12
tests); `loanagainstsecurities` (a distinct, stricter-LTV, longer-tenure
loan product against pledged securities, real disbursement/repayment
via ledger, 21 tests + 3 `ledgerclient` tests). Fractional share
investing was deferred at the time (see below — it was subsequently
built in a later pass).

**Concurrent-edit note:** this round's changes to `cmd/server/main.go`
landed alongside a separate lane's chaos-testing/FIX-conformance
additions to the same file — the orchestrating session independently
re-verified (not just trusted the agents) that all changes coexist
correctly with zero lost work (`docs/BUILD_LOG.md` entry 65).

### `internal/fractionalshares` + 9 more §19/§21 packages — new this build

FEATURES.md §17 (the previously-deferred fractional shares, now built),
§19, §21 (`docs/BUILD_LOG.md` entry 67) — all 10 items completed.

**Fractional share investing**, finally tackled after being explicitly
deferred twice: a milli-share integer precision scheme (1000 = 1.000
share, not float), added as an ADDITIVE `MilliShareQuantity` field
alongside the existing whole-share quantity. Live (non-paper) fractional
orders are correctly rejected (matching-engine has no fractional wire
field yet); paper trading fills fractionally for real via a new, fully
separate `MilliSharePositionBook`. 30 tests, zero existing
integer-quantity tests broken. **Overflow fix, `docs/BUILD_LOG.md`
entry 87**: `ValidateMilliShareQuantity` gained
`MaxReasonableMilliShareQuantity`/`ErrMilliShareQuantityExceedsMaximum`,
checked before `NotionalInMinorUnits`'s uint64→int64 conversion, which
previously silently wrapped negative for an unreasonably large
milli-share quantity.

**§19:** `overtradingdetection` (real rapid-fire/elevated-velocity
pattern detection, a non-blocking nudge with a real cooldown, 15 tests);
`riskdisclosuregate` (real per-account F&O acknowledgement + a real
24-hour cooling-off before the first F&O order, deterministically
tested via an explicit `now`, 14 tests).

**§21 (8 items):** real square-off countdown + deduplicated threshold
reminders (extends `marketsession`, 16 tests); a live interest cost
calculator extending `marginfunding` (cost-so-far + projected, 18
tests, live-verified with real ledger money movement); a portfolio
stress test (exact equity scaling + a documented first-order delta
approximation for options, 13 tests); large-order friction (soft-reject
against an account's own historical average, resubmit-to-confirm, 15
tests); a liquidity badge (real HIGH/MEDIUM/LOW from real order-book
depth + an explicitly-illustrative time-to-fill estimate, 13 tests); an
extended `idempotency` reconciliation function answering "what happened
while I was disconnected" (12 new tests); and a corporate-action
explainer generating a real one-line explanation from real before/after
position numbers (explicitly scoped as ONLY the explainer surface —
real corporate-actions detection/application itself, §14, was built in
a later round — see `internal/corporateactionsprocessing` below).

**Verified live** across every item against real running services.

**Known gaps:** fractional-aware charges/margin calculators not yet
built; liquidity badge and stress test are explicitly illustrative/
first-order, not ML-fitted or full repricing. §21's capital-gains
statement export and cross-device watchlist/P&L widget remain unbuilt.

### `internal/corporateactionsprocessing` — new this build

FEATURES.md §14's "corporate actions processing" (`docs/BUILD_LOG.md`
entry 73) — distinct from `internal/corporateactionexplainer` above
(which only surfaces upcoming actions), this is the real application
logic. Given a `STOCK_SPLIT`/`BONUS_ISSUE`/`MERGER`/`CASH_DIVIDEND`
event, it computes and applies the correct holdings/cost-basis update:
a 2:1 split doubles quantity and halves average cost per share while
total cost basis is unchanged; a bonus issue has the same net effect
via free shares; a merger carries the acquired position's full cost
basis into the acquirer (adding onto any existing acquirer holding)
and rejects non-exact exchange ratios rather than silently truncating;
a cash dividend leaves the holding untouched and credits the account
for real via a genuine `ledgerclient.PostDividendCreditJournalEntry`
call. Endpoints under `/corporate-actions/*`
(`GET /corporate-actions/holdings` requires both `accountId` and
`instrument` query params). 13 tests. Verified live: a split correctly
reflected in both this package's own holdings book and the existing
`internal/positions.PositionBook`; a dividend moved a real ledger
balance from 0 to 10,000 minor units.

### `internal/tradesurveillance`, `internal/conversationalorderparser` — new this build

FEATURES.md §1's surveillance system and §21's conversational order
placement (`docs/BUILD_LOG.md` entry 78).

`tradesurveillance`: real heuristic spoofing detection (a large LIMIT
order cancelled before material execution risk — priced away from a
reference price, or cancelled faster than a plausible fill-latency
threshold), layering detection (a same-side order cluster across
successive price levels, cancelled after an opposite-side fill), exact
same-account wash-trade detection, and an audit-trail replay function
for a flagged incident — honestly scoped as compliance-review
heuristics, not certified surveillance (no linked-account concept
exists in this repo, so wash-trade detection is same-account only; no
tick-to-trade latency concept exists anywhere to reuse, documented
rather than faked). `GET /compliance/surveillance?accountId=...&windowStartTime=...&windowEndTime=...`.
**A real bug was found and fixed**: `EntriesForAccount` silently
dropped every `ORDER_CANCELLED` entry (no account identifier on
cancellations), which would have made spoofing/layering permanently
blind to cancellations — fixed with a new `ScopeEntriesToAccount`
helper.

`conversationalorderparser`: a real rule-based grammar/slot extractor
(side, quantity, instrument including options CE/PE strike, order
type/price) for chat-style input like "buy 10 shares of RELIANCE at
market" — no external LLM call. Never submits directly: returns a
parsed intent + human-readable confirmation summary only; a separate
`POST /conversational-order/confirm-and-submit` (requiring
`explicitConfirmation:true`) submits through the exact same
`processOrderSubmission` path as the regular order ticket, so every
existing gate applies. Ambiguous/incomplete input is rejected with
specific errors, never guessed. Voice/speech-to-text explicitly out of
scope — no audio pipeline available in this sandbox, documented loudly
in the package doc comment.

32 new tests (603 → 635 for the service). Verified live: a constructed
spoofing sequence against a live matching-engine was genuinely flagged
(and incidentally also caught a real wash trade); a parsed-and-confirmed
conversational order produced a real fill with a real matching-engine
sequence number.

### Auth/RBAC wiring — new this build (`docs/BUILD_LOG.md` entry 84)

Every route in `cmd/server/main.go`'s mux (~65) is now gated except
`GET /health` and `POST /orders/estimate-charges` (still an explicit,
un-gated pre-order quote). A new, duplicated `internal/authmiddleware`
package (verify-only HS256 JWT parsing — see its own doc comment for
why it doesn't import `services/auth`'s `internal/jwtauth`, and this
entry's "known limitations" for the maintenance tradeoff of copying it)
wraps each handler at its `httpRequestMultiplexer.HandleFunc` call site
with one of: `authmiddleware.RequireAuth` (any valid token — the large
majority of routes) or `authmiddleware.RequireRole(secret,
authmiddleware.RoleAdmin, ...)` for operationally-privileged routes
(`/market-session/open`/`close`/`set-phase`, `/audit-trail`,
`/compliance/surveillance`, `/connectivity-kill-switch/*`,
`/algo-limits/configure`, `/strategies/admin/verify`,
`/strategies/followers`, `/exposure-limits/configure`,
`/positions/corporate-action-adjustments/apply` and its sibling
`/corporate-actions/holdings/seed`/`/process`, `/mark-to-market/price`,
`/auto-liquidation/evaluate`, `/drip/process-dividend`). A new shared
helper, `authenticatedAccountMatches` (39 call sites), additionally
verifies that any route carrying a `clientAccountIdentifier`/
`accountId`-style field in its body or query string matches the
authenticated caller's own account id, rejecting a mismatch with `403
{"errorMessage":"you can only act on your own account"}` — the real
security property this pass cares about, not just "logged in".
`withPermissiveCorsForDevelopment` was replaced with
`withAllowListedCorsOrigin`: reads `CORS_ALLOWED_ORIGINS` (default
`http://localhost:3000,http://localhost:3100`), echoes the exact
`Origin` back plus `Access-Control-Allow-Credentials: true` only when
it's on the allow-list, omits CORS headers entirely otherwise — no more
wildcard `*`, which the CORS spec forbids alongside credentialed
requests. Existing tests calling now-gated handlers directly were
updated to attach a real, hand-constructed HS256 test JWT (same
pattern as `internal/authmiddleware`'s own test file's
`issueTestToken` helper), not skipped or weakened.
`gofmt`/`go vet`/`go build`/`go test -race` all clean, zero regressions
— independently re-run and confirmed, not just self-reported.

**Verified live**: a real JWT from `services/auth`'s seeded `acct-001`
hit `GET /positions` successfully (200); no token on the same route →
401; `acct-002`'s real token against `acct-001`'s positions → 403; a
retail-role token against `POST /market-session/open` → 403
(admin-only), and the same route with no token at all → 401 (not 403 —
confirming auth is checked before role); a real `POST /orders/submit`
with a valid token passed auth and reached real downstream business
logic (rejected by the connectivity kill switch, unrelated to this
pass — matching-engine wasn't running for this round).

**Known limitation:** `/strategies/followers` was classified
admin-only rather than owner-scoped, because it lists EVERY follower of
a strategy (exposing other accounts' identities) and no "strategy
leader" ownership concept exists yet to scope it to a caller — flagged
for product review (see `docs/BUILD_LOG.md` entry 84's consolidated
judgment-call list). `internal/idempotency`'s pre-existing concurrent-
claim race mitigation and every other known limitation in this section
above are unchanged by this pass — auth answers "who is this and do
they own this account", not "is this request otherwise safe under
concurrency".

---

## services/ledger (Go) — Tier 2, double-entry core is real AND real-Postgres-backed by default

### `internal/doubleentry/doubleEntryLedgerCore.go`

| Item | Purpose |
|---|---|
| `LedgerBook` (interface, new — `docs/BUILD_LOG.md` entry 85) | The public surface every consumer (`fundsegregation`, `withdrawalworkflow`, `multicurrencywallet`, `cmd/server/main.go`'s admin snapshot/restore handlers) actually depends on: `PostJournalEntry`, `RegisterAccountIfAbsent`, `CurrentBalanceInMinorUnits`, `CaptureSnapshot`, `RestoreFromSnapshot`. Both `InMemoryDoubleEntryLedgerBook` (below) and `internal/pgstore.PostgresLedgerBook` satisfy it — this is what lets `cmd/server/main.go` construct either one and hand it to every other package unchanged. |
| `LedgerAccountLine` | One side (debit or credit) of an entry: account id + amount. |
| `JournalEntry` | An atomic, must-balance set of `DebitLines` and `CreditLines` plus a description. |
| `ErrJournalEntryDoesNotBalance` / `ErrUnknownLedgerAccount` | Sentinel errors, checkable via `errors.Is` — returned identically by BOTH `LedgerBook` implementations. |
| `NewInMemoryDoubleEntryLedgerBookWithAccounts(accountIds []string)` | Constructs the IN-MEMORY book with a fixed set of zero-balance accounts. Still used directly by tests and as the fallback if Postgres is unreachable at startup — see `cmd/server/main.go`. |
| `PostJournalEntry(entry) error` | **The core invariant**: rejects the entry outright (no partial application) if debit-sum ≠ credit-sum, or if any referenced account doesn't exist. Otherwise, mutex-guarded, applies every line atomically (debit increases the named account's balance, credit decreases it — one uniform convention in this skeleton, not yet real chart-of-accounts semantics) and appends to the post-order history. |
| `CurrentBalanceInMinorUnits(accountId) (int64, error)` | Mutex-guarded balance lookup. |

**Tested behavior:** a balanced entry posts and both accounts update
correctly; an unbalanced entry is rejected and *neither* account is
touched; an entry referencing an unknown account is rejected.

### `internal/pgstore/pgLedgerBook.go` — new (`docs/BUILD_LOG.md` entry 85), real Postgres persistence

`PostgresLedgerBook` implements `doubleentry.LedgerBook` against real
Postgres — no ORM, no migration framework: `services/ledger/migrations/
0001_journal_entries.sql` (idempotent `CREATE TABLE IF NOT EXISTS`,
applied at every startup via the `migrations` package's `embed.FS`)
defines `ledger_accounts` (a maintained balance projection),
append-only `journal_entries`/`journal_entry_lines`.

| Item | Purpose |
|---|---|
| `NewPostgresLedgerBook(ctx, postgresDsn, initialAccountIdentifiers) (*PostgresLedgerBook, error)` | Connects, applies migrations, seeds the given accounts (idempotent — `ON CONFLICT DO NOTHING`). |
| `PostJournalEntry` | Real Postgres transaction: every referenced account is `SELECT ... FOR UPDATE`-locked before any line applies (a concurrent post against the same accounts serializes correctly, not a race), inserts the journal entry + lines, updates both accounts' balance projections, commits — or rolls back entirely on any failure, mirroring the in-memory version's all-or-nothing guarantee via the database instead of a Go mutex. |
| `RegisterAccountIfAbsent` / `CurrentBalanceInMinorUnits` | Direct SQL equivalents of the in-memory methods, same return contracts (including `errors.Is(err, doubleentry.ErrUnknownLedgerAccount)` working identically). |
| `CaptureSnapshot` / `RestoreFromSnapshot` | Kept for interface-completeness (the admin snapshot/restore HTTP handlers are written against `doubleentry.LedgerBook`) — for a real Postgres deployment, `pg_dump`/point-in-time recovery is the ACTUAL backup mechanism; this JSON snapshot is a dev/convenience tool here, same caveat `internal/doubleentry/snapshotRestore.go` already documented for the in-memory case. |

**Wired into `cmd/server/main.go`**: constructs `pgstore.NewPostgresLedgerBook`
first (`POSTGRES_DSN`, defaulting to
`postgres://trading:trading@localhost:5432/ledger` — the compose
Postgres's `POSTGRES_DB` already provisions this database); on any
connection failure, falls back to the in-memory constructor with a
loud warning log rather than crashing — a deliberate fail-OPEN choice
(documented inline in `main.go`), matching this repo's existing
"degrade, don't crash" convention for other optional startup
dependencies. `internal/fundsegregation`, `internal/withdrawalworkflow`,
and `internal/multicurrencywallet` each had their `ledgerBook` field/
parameter type widened from the concrete `*InMemoryDoubleEntryLedgerBook`
to the `doubleentry.LedgerBook` interface — a pure type-signature change,
zero logic touched, confirmed by their own pre-existing test suites
passing completely unmodified against both backends.

`internal/fundsegregation.SegregationGuard` was checked, not assumed,
for state needing its own persistence: it holds no balances of its own
(`internal/doubleentry` remains the one system of record — see that
package's own pre-existing doc comment) and its account
classification map is rebuilt fresh from `cmd/server/main.go`'s
hardcoded constructor arguments on every startup — there is genuinely
nothing to persist there.

**Tested behavior** (7 new tests, `internal/pgstore`, run against a
real local Postgres, not mocks): register/idempotent-register, unknown-
account balance lookup, a balanced entry updates both accounts, an
unbalanced entry is rejected with no partial application, an entry
referencing an unknown account is rejected, data posted through one
connection is visible from a brand-new connection (the actual
persistence proof at the unit level), and a capture/restore snapshot
round trip.

**Verified live** (`docs/BUILD_LOG.md` entry 85): a real journal entry
posted through the real running ledger process credited `acct-001` by
50000 minor units, confirmed via `GET /accounts/balance`; the real
process was killed (`kill -9`, confirmed via `lsof` that nothing was
listening) and restarted fresh; the SAME balance query returned the
SAME 50000, read back through the live HTTP API — not inspected via
`psql`, the actual restart-survival proof this whole persistence pass
exists for.

**Known limitations:** no connection-pool tuning, no read replicas,
single-node Postgres; no retry/backoff if Postgres is down at
startup (fails open to in-memory instead — a real production
deployment for money likely wants the opposite); no backup/restore
drill run in this pass (see `DR_RUNBOOK.md` and the pre-existing
JSON-snapshot admin endpoints, unaffected by this change); no
client-fund segregation at the account-structure level beyond what
`internal/fundsegregation` already layers on top (FEATURES.md §1); no
chart-of-accounts semantics.

**Correctness fixes, `docs/BUILD_LOG.md` entry 87:** a real
custody-pool-overflow bug in `internal/fundsegregation/clientFundSegregation.go`'s
`PostClientMoneyMovement` (a near-`math.MaxInt64` amount overflowed on
its internal doubling for the external-cash-suspense leg — now bounded
by `maxMovementAmountInMinorUnits` and `ErrMovementAmountTooLarge`); a
genuine TOCTOU withdrawal race in `internal/withdrawalworkflow/withdrawalWorkflow.go`'s
`RequestWithdrawal` (the balance lookup and the hold-insert used to be
two separate critical sections — now one, via a lock-already-held
`heldAmountInMinorUnitsLocked` helper); a matching TOCTOU race in
`internal/multicurrencywallet/multiCurrencyWallet.go`'s
`WithdrawFromCurrencyWallet`/`ConvertBetweenCurrencyWallets`, closed by
a new `mutexGuardingBalanceMutations sync.Mutex`, plus that same
file's FX rate table moving from `float64` to exact `math/big`
round-half-up integer arithmetic (`convertMinorUnitsRoundHalfUp`,
`fxRateScale`); and a SIP-mandate silent-failure fix in
`internal/paymentmandate/paymentMandate.go` — `SweepDueMandates` used
to advance `NextDebitDate` even when the debit failed, so a
permanently-undebitable mandate looked like it was running forever
without ever moving money; it now suspends after 3 consecutive
failures (`MandateStatusSuspended`) instead.

### `internal/withdrawalworkflow/withdrawalWorkflow.go` — new this build

Real withdrawal workflow with T+N settlement holds (FEATURES.md §2).
Shares the SAME `doubleentry` ledger book the rest of this service uses
— a completed withdrawal is a real, balanced journal entry, not a
separate bookkeeping system.

| Item | Purpose |
|---|---|
| `WithdrawalStatus` | `PENDING_HOLD` / `COMPLETED` / `CANCELLED`. |
| `AvailableBalanceInMinorUnits(accountId) -> (int64, error)` | Raw ledger balance MINUS every currently-`PENDING_HOLD` amount for that account — what a client should actually be allowed to withdraw or trade against. |
| `RequestWithdrawal(accountId, amount, now) -> (*WithdrawalRequest, error)` | Places a hold (`EligibleForPayoutAt = now + settlementHoldDuration`) if `amount` doesn't exceed the AVAILABLE (not raw) balance — holds stack, so a second request is checked against what's left after the first, not the full ledger balance. |
| `ProcessDueWithdrawals(now) -> (completed, failedIds)` | Sweeps every `PENDING_HOLD` request whose hold has elapsed and posts a REAL journal entry (credits the account, debits the firm withdrawal clearing account) — money genuinely leaves the ledger balance here, not just a status flip. A post failure leaves the request `PENDING_HOLD` and is reported, not silently dropped. |
| `CancelWithdrawal(withdrawalId) -> (*WithdrawalRequest, error)` | Only valid while `PENDING_HOLD` — releases the hold, restoring the available balance. |
| `RequestsForAccount` / `LookupRequest` | Read-only history/status. |

**Tested behavior** (13 tests): available balance equals raw balance
with no holds; a request reduces available but NOT raw balance; a
request exceeding available balance is rejected; a second request
can't double-spend what an earlier one already holds (proves holds
stack, aren't independently checked against the full balance); non-
positive amounts rejected; `ProcessDueWithdrawals` does nothing before
the hold elapses; **after** it elapses, the money genuinely moves — the
account's ledger balance actually drops and the clearing account
actually receives it (the core load-bearing assertion of the whole
feature); cancellation releases the hold and restores available
balance; cancelling an already-completed or unknown withdrawal fails;
requests list in requested order.

**Known limitations:** payout is a ledger-internal journal entry, not a
real bank transfer (no real payment rail anywhere in this repo, same
category of gap as kyc-onboarding's bank-verification penny-drop).
`ProcessDueWithdrawals` is externally triggered (an endpoint), not run
on a real scheduled job. In-memory only. No auth.

### `internal/fundsegregation/clientFundSegregation.go` — new this build

Client fund segregation (FEATURES.md §1: "Segregation of client funds
vs. firm funds ... client money must be ring-fenced"). Classifies
accounts as CLIENT or FIRM and enforces one real invariant on top of
`doubleentry`: a dedicated custody-pool account's balance must always
equal the sum of every CLIENT account's balance.

| Item | Purpose |
|---|---|
| `AccountKind` | `CLIENT` or `FIRM`. |
| `NewSegregationGuard(ledgerBook, custodyPoolAccountId, externalCashSuspenseAccountId, clientAccountIds, firmAccountIds)` | Builds the guard; both named accounts must already exist in `ledgerBook`. |
| `PostClientMoneyMovement(clientAccountId, amount, description) -> error` | Ring-fenced deposit/payout. Posts one balanced THREE-leg entry: client account and custody pool move together (same direction), `externalCashSuspenseAccountId` is the real-world counterparty absorbing double the amount — the same clearing-account role `firm-clearing-acct` already plays elsewhere, dedicated to genuinely-external client cash. Positive = money arriving, negative = payout. |
| `PostInterClientTransfer(from, to, amount, description) -> error` | Client-to-client only — rejects outright if either side isn't classified CLIENT. Doesn't touch custody at all, since no client money enters/leaves custody in aggregate. |
| `CheckSegregationInvariant() -> (SegregationInvariantReport, error)` | The compliance-facing report: custody pool balance, aggregate client balance, computed discrepancy, `IsSegregationIntact` bool. |
| `ValidateEntryPreservesSegregation(entry) -> error` | Dry-run: would posting this arbitrary journal entry break the invariant? Never posts anything. |
| `AccountKindOf(accountId) -> (AccountKind, bool)` | Classification lookup. |

**Tested behavior** (13 tests): deposit/payout correctly moves both
client and custody balances together; zero-amount and negative-payout
edge cases; unclassified-account rejection; invariant stays intact
across several movements; inter-client transfer preserves the
invariant and is rejected for a non-client destination or non-positive
amount; dry-run validation accepts a properly-segregated deposit,
client-to-client transfer, and firm-only entry, and rejects an entry
that moves client money without touching custody; account
classification lookups.

**Verified live** (`docs/BUILD_LOG.md` entry 45): funded two client
accounts via `POST /client-funds/deposit` (report stayed intact),
transferred between them via `POST /client-funds/transfer` (report
stayed intact, no custody movement needed), a transfer attempt to
`firm-clearing-acct` was rejected, a payout (negative deposit) updated
both balances and the report correctly, a dry-run against the OLD
funding pattern (debit client / credit `firm-clearing-acct`) was
flagged without posting, and — to prove the report isn't just always
green — a real entry posted through the pre-existing `/journal-entries`
endpoint using that old pattern produced a genuine, correctly-computed
nonzero discrepancy in the report.

**Known limitation:** segregation is enforced ONLY on the new
`/client-funds/*` endpoints — `/journal-entries` (used by oms-gateway
for trade settlement) is not migrated and can still produce a real
discrepancy, exactly as demonstrated above. Custody pool and suspense
accounts are just more in-memory ledger accounts, not backed by a real
segregated bank account.

### `internal/amlmonitoring/amlMonitor.go` — new this build

AML transaction monitoring (FEATURES.md §1: "unusual pattern flags, PEP
screening"). Rule-based, evaluated synchronously against every
transaction explicitly reported to it — decoupled from any specific
money-movement mechanism, same design principle as `fundsegregation`.

| Item | Purpose |
|---|---|
| `MonitorConfig` | Explicit thresholds: `LargeTransactionThresholdInMinorUnits`, `StructuringReportThresholdInMinorUnits` + `StructuringWindow`, `VelocityMaxTransactionsInWindow` + `VelocityWindow`. All illustrative constants, not real regulatory limits. |
| `NewMonitor(config, pepNames)` | Builds the monitor with a static PEP watch list (matched case-insensitively). |
| `RecordTransaction(accountId, amount, occurredAt) -> []Alert` | Logs the transaction and evaluates all three rules, returning whatever new alerts fired. |
| `ScreenName(accountId, fullName, occurredAt) -> (Alert, bool)` | Case-insensitive PEP watch-list check; a match raises and stores a `PEP_MATCH` alert (doesn't block anything — it's a signal, not a hard stop). |
| `AlertsForAccount(accountId) -> []Alert` | Every alert for one account, oldest first. |
| `AllAlerts() -> []Alert` | Every alert across every account, chronologically sorted — the actual compliance review-queue view. |

Rules: `LARGE_TRANSACTION` fires immediately on any single transaction
at/above the threshold; `VELOCITY` fires when an account's transaction
count within the rolling window exceeds the limit; `STRUCTURING` fires
when several individually-sub-threshold transactions within the window
sum over the reporting threshold — deliberately does NOT fire for a
single transaction alone crossing the threshold (that's
`LARGE_TRANSACTION`'s job).

**Tested behavior** (15 tests): no alert for an ordinary small
transaction; large-transaction alert on a single big transaction
(positive or negative amount); velocity alert on the transaction that
crosses the limit, not before, and not for transactions outside the
window; structuring alert once sub-threshold transactions sum over the
limit within the window, not for a single large transaction or a single
sub-threshold transaction alone, not across a wider time gap; PEP
name-match case-insensitivity and non-match; per-account and
cross-account alert aggregation, both empty-not-nil when nothing's been
raised.

**Verified live**: a large deposit triggered `LARGE_TRANSACTION`
immediately; three sub-threshold deposits summing over the reporting
threshold triggered `STRUCTURING` on exactly the third one; a
PEP-listed name matched, an ordinary name didn't; six withdrawal
requests within an hour (limit 5) triggered real `VELOCITY` alerts on
the 6th and 7th; the aggregate `/aml/alerts` view correctly showed every
alert across every account.

**Known limitation:** only `/client-funds/deposit` and
`/withdrawals/request` report to the monitor — `/journal-entries` (trade
settlement) and `/withdrawals/process-due` (the actual money-leaving
moment) do not, so trade flow isn't monitored yet. No case-management
lifecycle for a raised alert (no assign/investigate/close/escalate).
Static PEP list, not a real sanctions database.

### `cmd/server/main.go`

| Route | Behavior |
|---|---|
| `GET /health` | Liveness check. |
| `GET /accounts/balance?accountId=...` | Returns the current balance for an account, 404 if unknown. Now ALSO returns `availableBalanceInMinorUnits` alongside the raw `currentBalanceInMinorUnits` — genuinely different numbers once any withdrawal hold exists. |
| `POST /journal-entries` | Decodes `PostJournalEntryWireRequest` (own wire type, deliberately decoupled from `doubleentry.JournalEntry` which has no JSON tags), converts to the internal domain type, calls `PostJournalEntry`. Returns `422 Unprocessable Entity` with `errorMessage` set on rejection (unbalanced entry or unknown account), `200` with `wasJournalEntryPosted:true` on success. This is what `oms-gateway` posts trade settlements to — see its `internal/ledgerclient`. NOT routed through the segregation guard or AML monitor — see their known-limitation notes above. |
| `POST /withdrawals/request` | `{accountIdentifier, amountInMinorUnits}` → the new `WithdrawalRequest` (`PENDING_HOLD`) or a 400 with the validation error. Also reports the transaction to the AML monitor on success. |
| `POST /withdrawals/cancel` | `{withdrawalId}` → the now-`CANCELLED` request or a 400. |
| `GET /withdrawals?accountId=...` | Full history for an account, any status. |
| `POST /withdrawals/process-due` | Sweeps and actually pays out every elapsed hold — `{completedWithdrawalIds, failedWithdrawalIds}`. |
| `POST /client-funds/deposit` | `{accountIdentifier, amountInMinorUnits}` → ring-fenced deposit/payout via `PostClientMoneyMovement`. `{wasApplied, errorMessage?}`. Also reports the transaction to the AML monitor on success. |
| `POST /client-funds/transfer` | `{fromAccountIdentifier, toAccountIdentifier, amountInMinorUnits}` → client-to-client transfer via `PostInterClientTransfer`. |
| `GET /client-funds/segregation-report` | Live `SegregationInvariantReport` — custody pool balance, aggregate client balance, discrepancy, `isSegregationIntact`. |
| `POST /client-funds/validate-entry` | Same wire shape as `/journal-entries` — dry-run only, never posts. `{wasApplied:false, errorMessage}` if it would break segregation. |
| `GET /aml/alerts` (optional `?accountId=...`) | The compliance review queue — every alert, or every alert for one account. |
| `POST /aml/screen-name` | `{accountIdentifier, fullName}` → `{isMatch, alert?}` PEP check via `ScreenName`. |

Seed accounts are `acct-001`, `acct-002`, `firm-clearing-acct`,
`client-money-custody-pool`, `external-cash-suspense` — the first three
deliberately match `oms-gateway`'s demo accounts so the two services
exercise together without extra setup; the last two back the
segregation guard (`acct-001`/`acct-002` classified CLIENT,
`firm-clearing-acct` classified FIRM). `WITHDRAWAL_SETTLEMENT_HOLD_DAYS`
env var (default 2) overrides the settlement hold duration — set to 0 for
live testing without waiting real days. **Verified end-to-end**
(`docs/BUILD_LOG.md` entries 15, 42, 45, 46): funded via `POST
/journal-entries`, balances moved correctly after a real trade routed
through all three of ledger/matching-engine/oms-gateway; a full
withdrawal lifecycle verified live — request (available balance
dropped, raw balance didn't) → a second over-limit request correctly
rejected → `process-due` (raw balance ACTUALLY dropped this time) → a
separate request-then-cancel round trip fully restored the balance;
client fund segregation verified live across deposit/transfer/report/
validate-entry, including a genuine caught discrepancy from the
unmigrated `/journal-entries` path.

### `internal/depositrail/simulatedDepositRail.go`, `internal/paymentmandate/paymentMandate.go` — new this build

FEATURES.md §2: "UPI / NEFT / IMPS / net-banking deposit integration"
and "Auto-payment mandates for SIPs (eNACH/standing instructions)". Both
are loudly documented as NOT real bank integrations — no actual
UPI/NEFT/IMPS/eNACH network call happens anywhere in this codebase.

`depositrail`: a two-phase state machine — `POST /deposits/initiate`
starts a deposit PENDING (no money moves yet); `POST /deposits/confirm`
stands in for the bank's webhook and is the ONLY place real money moves,
posting through `fundsegregation.SegregationGuard.PostClientMoneyMovement`
(same ring-fenced path `/client-funds/deposit` uses) and reporting to
`amlmonitoring.Monitor`. Confirming an already-confirmed or unknown
deposit is rejected — no double-post (a genuine TOCTOU race in an early
draft was caught and fixed before this shipped — see
`docs/BUILD_LOG.md` entry 47). `GET /deposits?accountId=` for history.
13 tests.

`paymentmandate`: register a standing instruction (account, amount,
frequency, next debit date), pause/resume/cancel, and `POST
/payment-mandates/sweep-due` (same "process-due" sweep pattern as
`withdrawalworkflow`) that debits the account via the segregation guard
on every mandate whose date has arrived and advances its next due date.
17 tests.

**Verified live** (`docs/BUILD_LOG.md` entry 47): a deposit's balance
stayed unchanged right after initiate, jumped only after confirm,
segregation stayed intact throughout, a second confirm was rejected; a
mandate swept and debited the account correctly, a paused mandate's
sweep did nothing, a cancelled mandate's resume attempt correctly
failed.

**Known limitations:** sweep endpoints are manually/externally
triggered, not on a real schedule; `/journal-entries`,
`/withdrawals/process-due`, and `/payment-mandates/sweep-due` aren't
reported to AML monitoring (only `/client-funds/deposit`,
`/withdrawals/request`, and `/deposits/confirm` are); swept SIP money
has no real investment destination; no auth on any new endpoint.

### `internal/multicurrencywallet/multiCurrencyWallet.go` — new this build

FEATURES.md §2: "Multi-currency wallet (for platforms offering global/US
stocks)". Each (accountIdentifier, currencyCode) pair is tracked as its
own real `doubleentry` ledger account — the native currency (INR) is an
explicit alias for the account's existing raw ledger account, so nothing
about the pre-existing balance/segregation path changed. Real deposit/
withdraw per currency wallet; real conversion between two currency
wallets of the same account through a static illustrative FX rate table
(e.g. USD/INR=83.0), posted as a real balanced journal entry through a
new `fx-conversion-clearing-acct`. `GET /wallets?accountId=` lists every
currency balance. One new additive method on `doubleentry`,
`RegisterAccountIfAbsent` — no existing signature touched. 18 tests.

**Design bug caught and fixed before implementation, not live:** an
early draft would have decoupled the native-currency wallet's balance
from the raw account it's supposed to alias — fixed by making the
aliasing consistent everywhere before any code ran.

**Verified live** (`docs/BUILD_LOG.md` entry 53): depositing into a USD
wallet left the INR balance and segregation report untouched; converting
100.00 USD → INR at the static rate moved exactly 830000 minor units, no
rounding; withdrawing from an empty currency wallet was rejected even
with a large balance in a different currency (per-currency isolation);
an unconfigured currency pair was rejected with a clear error.

**Known limitations:** FX rate table is static/hardcoded, no live feed,
no bid/ask spread; no real foreign-currency custody/settlement rail; no
LRS limit tracking; non-native currency wallets are deliberately NOT
included in `fundsegregation`'s CLIENT/FIRM classification — mixing
foreign-currency minor units into the INR custody-pool sum would be
numerically meaningless, so the segregation invariant only covers INR
today.

---

## services/mutual-funds (Go) — new this build: AMC routing, SIP/lumpsum, step-up SIPs

FEATURES.md §4. A brand-new Go service (module `mercurius/mutualFunds`,
port :8087), following the same house style as every other Go service
in this repo (long descriptive camelCase names, sentinel errors,
mutex-guarded in-memory state, real HTTP handlers).

### `internal/fundcatalog`

A small static catalog of 5 illustrative mutual fund schemes
(schemeId, name, category — EQUITY/DEBT/HYBRID — and a NAV that can be
moved for testing). 6 tests.

### `internal/amcrouting`

Models routing a purchase/redemption order to an AMC/RTA as a real
PENDING→CONFIRMED state machine — units are allocated as
`amountInvested / navAtConfirmation` only once a (simulated) T+N
confirmation sweep runs, not instantly at order time. **This is NOT a
real AMC/RTA integration** — no BSE StAR MF / NSE NMF II / CAMS /
KFintech API call happens anywhere; it's a state machine standing in for
one, same honesty pattern as `kyc-onboarding`'s bank-verification
placeholder. 11 tests.

### `internal/sipscheduler`

Register a SIP (account, scheme, amount, MONTHLY frequency, start
date), pause/resume/cancel, and a `POST /sips/sweep-due` sweep (same
"process-due" pattern as `ledger`'s `withdrawalworkflow`) that executes
every installment whose due date has arrived by routing a purchase
through `amcrouting`, then advances the next due date. Step-up SIPs: an
optional annual step-up percentage increases the installment amount at
each anniversary of the SIP's start date. Every scheduling function
takes an explicit `now`/`asOf time.Time` parameter (not real-clock
`time.Now()`), so time-dependent behavior — including the step-up
boundary — is testable deterministically without sleeping. 19 tests.

**Tested behavior:** happy-path purchase/redemption, pause/cancel
semantics, sweep idempotency (sweeping twice before the next due date
does nothing the second time), and a hand-worked step-up boundary case:
a ₹5,000/month SIP with 10% step-up stayed at ₹5,000 for installments
#1–#12 and became exactly ₹5,500 at installment #13 (the first
anniversary).

**Verified live** (`docs/BUILD_LOG.md` entry 49, real running process on
:8087): a lumpsum purchase allocated exactly `amount / navAtConfirmation`
units after a simulated confirmation sweep; a SIP's first due sweep
executed and advanced its next due date, a repeat sweep at the same
instant did nothing; pausing froze the schedule, resuming let exactly
one installment execute (no backfilled catch-up); invalid inputs (zero
amount, unknown scheme) were rejected with clear errors; the step-up
boundary was demonstrated by driving `?asOf=` forward through 13
installments without sleeping real time.

### `cmd/server/main.go`

`GET /health`, plus routes for registering/pausing/resuming/cancelling a
SIP, listing SIPs for an account, sweeping due SIPs and due AMC orders,
lumpsum purchase, redemption, and viewing per-account holdings/units.

**Known limitations:** no real AMC/RTA integration anywhere; sweep
endpoints are manually triggered, not scheduled; only MONTHLY frequency
implemented; no KYC gating; **no ledger integration** — purchases don't
debit any account and redemptions don't credit one, this service and
`ledger` aren't wired together yet; no persistence, no auth; the
`?asOf=` time-override is a testing convenience with no access control.

### `internal/basketrebalancing`, `internal/roboadvisory`, `internal/goalinvesting` — new this build

FEATURES.md §4: "Index/thematic rebalancing baskets", "Robo-Advisory:
risk-profile → Efficient Frontier allocation", "Goal-based investing".

**`basketrebalancing`**: named target-weight baskets across
`fundcatalog` schemes (weights validated to sum to exactly 100%),
lumpsum subscription routes proportional purchases through `amcrouting`,
and a real one-click rebalance comparing current holding value per
scheme against target weights to compute exact buy/sell orders. 18
tests, hand-worked NAV-move example verified live: a BLUECHIP NAV move
produced an exact SELL of 2500 (12.5 units) funding a BUY of 1500
MIDCAP + 1000 LIQUID.

**`roboadvisory`**: risk category (matching kyc-onboarding's categories)
→ illustrative model allocation across EQUITY/DEBT/HYBRID, with a REAL
call to quant-engine's live `/risk/statistics` endpoint surfacing real
Sharpe/Sortino/max-drawdown alongside the recommendation — degrades
cleanly when quant-engine is down. 15 tests. Verified live: killed
quant-engine mid-request (clean degradation), restarted it and got real
numbers back (Sharpe 0.7283, Sortino 1.3522).

**`goalinvesting`**: named goals (RETIREMENT/EDUCATION) linked to SIPs/
baskets, real progress tracking, and an on-track projection under an
illustrative assumed growth rate. 19 tests. Hand-worked and live-
verified exactly: 10000 current value, 12 months remaining, 1000/month
contribution → projected value 23346, matched to the integer.

**Known limitations:** Robo-Advisory's allocation table is explicitly
illustrative, NOT a real mean-variance/Efficient Frontier optimization —
this repo has no historical return/covariance data for the fictitious
catalog schemes to optimize over; goal projection's assumed growth rate
is illustrative, not a forecast, and doesn't vary by the goal's actual
linked-scheme risk mix; no cross-referencing between goals/baskets and
actual SIP records — a caller supplies linked scheme IDs by hand.

### `internal/fixedincome` and 7 more §5/§17 packages — new this build

FEATURES.md §5 "Fixed Income" (all 3 items) and §17's P4 remainder
(all 4 items) — `docs/BUILD_LOG.md` entry 66.

**§5:** `fixedincome` + `primarymarketbidding` (an illustrative G-Sec/
T-Bill/SGB catalog with a real price-priority auction-allotment engine
— NOT connected to any real RBI/E-Kuber system, 22 tests; live: a
7.05% bid fully allotted, a 7.20% bid partially allotted exactly the
remainder); `secondarymarketbonds` (a real iterative YTM solver — a
bond re-priced to its own face value correctly returns YTM equal to its
own coupon rate, a genuine correctness proof, 17 tests);
`bondladderbuilder` (real staggered-maturity ladder construction + a
real coupon calendar computed from each bond's actual schedule — a
truncation-vs-rounding bug was caught and fixed here, 18 tests).

**Two further correctness fixes, `docs/BUILD_LOG.md` entry 87:**
`primarymarketbidding/primaryAuctionEngine.go`'s `CloseAuction`
computed pro-rata allotment as
`int64(float64(bid.Qty)/float64(groupTotal)*float64(remaining))` —
float64 precision loss once values exceed its 52-bit mantissa (real
G-Sec auctions run into the trillions of paise); replaced with
`proRataAllotmentInMinorUnits`, an exact `math/big.Int`
multiply-then-divide. `bondladderbuilder/couponCalendar.go`'s
`monthsPerPeriod := 12 / bond.PaymentsPerYear` silently truncated for
any non-divisor payment frequency (5, 7, 8, 9, 11/year) — the new
`monthsPerCouponPeriodOrPanic` does not make those frequencies compute
correctly (the month-stepping design needs a real rewrite for that);
it converts the previous silent wrong output into a loud panic
instead, which is a real improvement but not a complete fix — those
frequencies still don't work.

**§17 P4 remainder:** `globalmarketsaccess` (illustrative ADR/GDR-style
routing state machine, real currency conversion math), `retirementaccounts`
(real contribution-limit + lock-in rules engine, exact-boundary tested),
`structuredproducts` (real capital-protected-note payoff calculator —
150% participation + 20% index return → capped payout exactly 120000),
`insurancecrosssell` (an honest, thin illustrative insurance-partner-
quote stub — explicitly NOT an underwriting engine). 81 tests across
the four.

**Verified live** for every item; full sweep: 224 tests total (136 new
+ 88 pre-existing), all green. All 7 items completed, none deferred.

**Known limitations:** every catalog (bonds, global-market symbols,
structured notes) is static/fictitious; no real RBI, CCIL/NDS-OM,
GDR/ADR/partner-brokerage, PFRDA, or insurer integration anywhere;
`globalmarketsaccess` doesn't call `ledger`'s real
`multicurrencywallet` yet (self-contained, documented as the real
integration point for a future pass); none of the seven new packages
have cash-side/ledger integration.

---

## services/api-gateway (Go) — new this build: rate-limited reverse proxy, API keys, webhooks, TCA, SLO alerting

FEATURES.md §13 "Platform, DevOps & Observability" and §18 "Platform,
Ecosystem & Institutional Tooling" (`docs/BUILD_LOG.md` entry 63). A
brand-new Go service (module `mercurius/apiGateway`, port :8089) sitting
in front of ledger/oms-gateway/mutual-funds/market-data/quant-engine —
9 internal packages, 113 tests, following this repo's established
house style.

| Package | What it does |
|---|---|
| `internal/sloalerting` | Continuous-breach SLO evaluator (unit-testable with synthetic samples) + a live `MetricsPoller` against oms-gateway's audit trail, market-data's `/trades`, and a real matching-engine TCP dial. `GET /alerts`. |
| `internal/secretsprovider` | A real, working env-var-backed `SecretsProvider` behind a real interface — swappable for Vault/AWS Secrets Manager later, documented as such — plus `config/secretsAccessMatrix.yaml`, a concrete real per-service access matrix (not fabricated cloud-IAM enforcement). |
| `internal/ratelimiter` | Real token-bucket rate limiting with RETAIL/INSTITUTIONAL/SANDBOX tiers. |
| `internal/apikeymanager` | Real API key issuance/revocation/validation — backs both the rate-limit tiers and the public developer API. |
| `internal/webhookdelivery` | Real webhook URL registration + real retry-on-failure delivery. |
| `internal/tenantconfig` | Real multi-tenant branding + isolated per-tenant rate limiters — the platform primitive white-labeling needs. |
| `internal/tca` | Real implementation-shortfall and arrival-price-slippage math over order-execution data. |
| `internal/accountaggregator` | Real merge math combining a mocked "external institution" holdings fixture with this platform's real holdings into one unified view. |
| `internal/reverseproxy` | The actual proxy layer routing to backend services. |

Additive changes made elsewhere to support this: `ledger`'s
`internal/doubleentry` gained `GET /admin/snapshot` / `POST
/admin/restore` (`snapshotRestore.go`) for real backup/restore; a real
restore DRILL test proves it (snapshot → mutate further → restore →
state matches the snapshot exactly, not the further mutation) —
verified live against a real running ledger process, not just unit-
tested. `oms-gateway`'s `internal/dmagateway` gained a 15-subtest FIX
conformance suite producing a real pass/fail report against THIS repo's
own illustrative FIX-inspired protocol (explicitly not real FIX 4.2/4.4
certification). `oms-gateway/scripts/chaosLoadTesting/` gained real
load-test (150 concurrent workers, real p50/p95/p99) and chaos-test
(a real kyc-onboarding kill mid-load, confirming documented fail-open
behavior under genuine concurrent pressure) scripts.

`DR_RUNBOOK.md` (repo root): concrete per-tier RTO/RPO targets and a
real runnable failover drill exercising what's actually exercisable
here — explicit boundary that a true multi-region DR failover needs
cloud infrastructure this environment doesn't have. A real WAL-copy +
replay parity proof (byte-identical reconstructed matching-engine
state) demonstrates the core primitive a blue/green deploy for a
stateful service needs, with an explicit boundary that real
traffic-cutover infrastructure (load balancer reconfiguration) doesn't
exist here either.

**Verified live:** a 20-burst retail API key hit real 429s exactly on
schedule while an institutional key handled 60/60; webhook delivery
proven with both `httptest` receivers and a real end-to-end Python
receiver triggered by a real oms-gateway order; API keys genuinely
401 when invalid/revoked.

**Known limitations:** all api-gateway state is in-memory only (restart
loses it); TCA pulls fixture data pending an oms-gateway price-history
endpoint (`dataSourceIsLive: false` flagged in the response); Account
Aggregator and the FIX conformance suite are explicitly illustrative,
not real integrations (`isExternalDataFromRealAaNetwork` hardcoded
false); white-labeling here is the rate-limit/branding primitive only,
not a complete commercial BaaS product (needs separate legal/compliance
entities per tenant); the DR runbook proves recovery primitives, not
true multi-region failover.

**Correctness fixes, `docs/BUILD_LOG.md` entry 87 — three real bugs,
two of them regressions from this repo's own entry-84 auth pass:**
(1) `internal/accountaggregator`'s handler called oms-gateway's (now
`RequireAuth`-gated as of entry 84) `/positions` with a bare,
unauthenticated `httpClient.Get(...)` — meaning the account-aggregator
had been silently broken (401s) since entry 84 shipped. New
`fetchOmsGatewayPositions`/`fetchMutualFundsHoldings` forward the
caller's own `Authorization` header. (2) Found as a side effect of
fixing (1): `omsPositionsWireResponse` was typed as
`[]struct{InstrumentSymbol string; MarketValueInMinorUnits int64}` — a
shape oms-gateway's real `/positions` (`{AccountIdentifier,
NetQuantityByInstrumentSymbol}`) has never actually returned; decoding
silently zero-valued instead of erroring, so this data was WRONG even
before the auth bug — a genuinely pre-existing bug, not invented by
this pass. The equivalent mutual-funds `/holdings` wire-shape bug was
fixed the same way. (3) The catch-all reverse-proxy route
(`httpRequestMultiplexer.Handle("/", backendProxy)`) had NO
authentication at all — every other route got `RequireAuth`/
`RequireRole` in entry 84, this one was missed. New
`buildProxyAccessGate` closes it (accepts a validated `X-Api-Key` OR a
bearer token). Also fixed: `buildRateLimitingMiddleware` keyed every
anonymous request under one shared `"anonymous"` bucket — any single
anonymous client could exhaust it for everyone — now keyed per caller
IP via `remoteAddressKey` (same `X-Forwarded-For` caveat as
`services/auth`'s equivalent helper, not solved here either).

### Auth/RBAC wiring + first-ever CORS — new this build (`docs/BUILD_LOG.md` entry 84)

This closes a real, previously-documented gap (`docs/BUILD_LOG.md`
entry 83): api-gateway had **no CORS middleware at all**. It now has
`withAllowListedCorsForDevelopment`, built directly in the allow-listed
form (no wildcard-first version, since credentialed bearer-token
requests were already the point) — `CORS_ALLOWED_ORIGINS`, default
`http://localhost:3000,http://localhost:3100`, exact-origin echo +
`Access-Control-Allow-Credentials: true` when allow-listed, no headers
otherwise. Every explicitly-registered route now runs through the same
duplicated `internal/authmiddleware` package the other four services
use: `GET /health` stays public; `/developer/api-keys` (both issue and
list), `/developer/api-keys/revoke`, `/webhooks/subscribe`, `/tca/
report`, and `/account-aggregator/net-worth` are `RequireAuth` +
account-match (the revoke handler didn't have an account id on its own
request body, so it looks the key record up FIRST via
`manager.ValidateApiKey` and checks the record's own account before
revoking — real ownership enforcement, not skipped for lack of a
convenient field); `/alerts`, `/tenants`, and `/webhooks/deliveries`
are `RequireRole(..., RoleAdmin)` (`/webhooks/deliveries` specifically
because `internal/webhookdelivery`'s `DeliveryHistory()` returns EVERY
account's history with no per-account filter yet — admin-only until
that filtering exists, not a permanent design choice; see
`docs/BUILD_LOG.md` entry 84's judgment-call list). The catch-all
reverse-proxy route (`httpRequestMultiplexer.Handle("/", backendProxy)`)
is intentionally UNCHANGED — it already has its own, separate
API-key + rate-limit gate (`buildRateLimitingMiddleware`), a different
auth model than JWT bearer tokens, out of scope for this pass.
`gofmt`/`go vet`/`go build`/`go test -race` clean.

**Verified live**: a real OPTIONS preflight from `Origin:
http://localhost:3000` against `/developer/api-keys` returned `204`
with the correct allow-listed CORS headers (confirming the entry-83 gap
is closed); the same preflight from a disallowed origin returned `204`
with NO CORS headers; a real `acct-001` JWT successfully listed that
account's API keys; the same route with no token → 401; the same
route with `acct-002`'s token and `accountIdentifier=acct-001` in the
query → 403.

---

## services/reporting (Go) — new service this build: contract notes, statements, FIFO tax P&L, AIS reconciliation

FEATURES.md §1 "Regulatory reporting" + §21 "One-click capital gains
statement export" (`docs/BUILD_LOG.md` entry 79). Listens on `:8090`
(the next free port after the repo's existing 8081-8089 allocations).
Reads real data read-only from live `oms-gateway` (:8081) and `ledger`
(:8082) HTTP APIs — no fabricated transactions.

| Package | Purpose |
|---|---|
| `internal/omsgatewayclient`, `internal/ledgerclient` | Read-only HTTP clients to the two real upstream services. |
| `internal/filltrail` | Parses oms-gateway's real audit trail into structured fills. |
| `internal/contractnotegenerator` | Real per-trade-day contract-note generator: instrument/side/quantity/price/charges/net-amount breakdown. |
| `internal/ledgerstatement` | Real running-balance account statement over a date range, derived from ledger cash movements plus oms-gateway fills (ledger exposes no journal-history endpoint, so trade-settlement rows are derived, with an honest reconciliation-delta note against ledger's real balance). |
| `internal/capitalgains` | Real FIFO lot-matching STCG/LTCG computation using India's 12-month equity threshold. |
| `internal/aisreconciliation` | Real discrepancy-detection logic (mismatched amounts, missing transactions) against an honestly-labeled MOCK Annual Information Statement — no real government AIS feed exists in this sandbox. |
| `internal/capitalgainsexport` | Real CSV writer for the one-click capital-gains export. |

`GET /health`, `GET /contract-notes/generate?accountId&date`,
`GET /ledger-statements/generate?accountId&startDate&endDate`,
`GET /capital-gains/compute?accountId&financialYear`,
`GET /capital-gains/export?accountId&financialYear` (real CSV),
`GET /ais-reconciliation/run?accountId&financialYear`. 41 tests,
including a hand-worked FIFO example (buy 100@₹100.00 + buy
100@₹120.00, partial sell 150@₹150.00 → exactly LTCG ₹5000.00 + STCG
₹1500.00, verified to the minor unit).

**A real bug was found and worked around** (oms-gateway was out of
scope to edit in this lane): `GET /audit-trail?accountId=X` only
returns `ORDER_FILLED` entries under the *taker* account, never the
resting/maker counterparty — verified live with a real crossing trade.
Worked around by fetching the full unfiltered trail and filtering on
the entry's own structured buyer/seller fields instead.

**Verified live** end-to-end against real running matching-engine/
ledger/oms-gateway processes.

**Known limitations:** illustrative charges fallback if oms-gateway's
real calculator is unreachable; no PDF generation (JSON/CSV only);
withdrawal timestamps use `eligibleForPayoutAt` as a proxy (ledger
exposes no completion timestamp); the AIS mock is loudly labeled as
not a real government feed. **Data race fixed, `docs/BUILD_LOG.md`
entry 87:** `internal/omsgatewayclient.OmsGatewayClient`'s
`chargesCalculatorReachable`/`chargesCalculatorProbed` fields were
plain, unsynchronized `bool`s written from concurrently-called
`EstimateCharges` — now guarded by a new `mutexGuardingChargesCalculatorState`,
written only via `recordChargesCalculatorProbeResult` and read only via
the new `ChargesCalculatorProbeState()`. **That reader is still dead
code, not fixed in this pass:** nothing anywhere in this service
actually calls `ChargesCalculatorProbeState()` to short-circuit a
retry or skip a call known to be doomed — the race is closed, but the
probe result still isn't consulted for anything.

---

## services/kyc-onboarding (Go) — PAN-format-check AND bank penny-drop verification are both real

### `internal/kycstate/kycVerificationStateMachine.go`

| Item | Purpose |
|---|---|
| `KycVerificationStage` | `NOT_SUBMITTED` / `VERIFIED` / `REJECTED`. |
| `SubmitKycDetails(accountId, panNumber, fullName) KycRecord` | Validates full name is non-empty and PAN matches `^[A-Z]{5}[0-9]{4}[A-Z]$`; marks `VERIFIED` or `REJECTED` with a reason, stores the record. No async review step in the AUTOMATED path — see doc comment. |
| `LookupKycStatus(accountId) KycRecord` | Returns the stored record, or a `NOT_SUBMITTED` placeholder if the account never submitted. |
| `KycRecord.IsEligibleToPlaceOrders() bool` | `true` iff stage is `VERIFIED`. |
| `ListRecordsByStage(stage) []KycRecord` — new this build | The actual "KYC review queue" (FEATURES.md §14): every record currently in `stage`, sorted by account id. `GET /kyc/review-queue` defaults to `StageRejected` — the accounts actually worth a human looking at. |
| `OverrideStage(accountId, newStage, reason) (KycRecord, error)` — new this build | The admin DECISION a review-queue entry resolves to: force an account to `VERIFIED` or `REJECTED` (never back to `NOT_SUBMITTED`), overturning or retroactively reversing the automated result. Requires the account to have submitted at least once. |

**Tested behavior** (11 tests, up from 5): valid PAN → verified +
eligible; malformed PAN → rejected with a reason; missing name →
rejected; unknown account lookup → `NOT_SUBMITTED`; lookup after submit
round-trips the stored data; `ListRecordsByStage` returns only matching
records sorted by account id (and an empty slice, not nil, for no
matches — correct `[]` not `null` JSON); `OverrideStage` can overturn an
automated rejection (clearing the rejection reason) AND retroactively
reject a previously-verified account (storing the override reason);
overriding an account that never submitted, or overriding to an invalid
target stage (`NOT_SUBMITTED`), both fail with distinct sentinel errors.

**Verified live** (`docs/BUILD_LOG.md` entry 39.5 — see below): the
normal auto-verify flow is completely unaffected by this addition (a
valid PAN still auto-verifies); a malformed PAN auto-rejects and
immediately shows up in `GET /kyc/review-queue`; overriding it to
`VERIFIED` removes it from the queue and `GET /kyc/status` reflects the
override; overriding an account that never submitted correctly fails.

**Known limitation:** as of this build's auth pass (`docs/BUILD_LOG.md`
entry 84), the override endpoint requires a `RoleCompliance` token —
no longer reachable by anyone unauthenticated, but still no audit trail
entry recorded for the override action itself (unlike oms-gateway's
`audittrail` package) — a real build needs one for compliance.

### `internal/bankverification/bankAccountVerifier.go` — new this build

Real penny-drop / micro-deposit bank account verification (FEATURES.md
§1). No real payment rail behind it — see the loud package-doc caveat.

| Item | Purpose |
|---|---|
| `VerificationStatus` | `PENDING` / `VERIFIED` / `FAILED_LOCKED` / `NOT_FOUND`. |
| `InitiateVerification(accountId, bankAccountNumber, ifscCode) -> (verificationId, error)` | Generates a random micro-deposit amount in `[1, 99]` minor units (`crypto/rand`) that is NOT returned to the caller — exactly like a real penny-drop, where the amount is only discoverable by checking the actual bank statement. |
| `ConfirmMicroDepositAmount(verificationId, claimedAmount) -> VerificationStatus` | Checks the claim; a match marks `VERIFIED`. A wrong guess consumes one of 3 attempts; exhausting them marks `FAILED_LOCKED` — permanently, even a subsequent CORRECT guess against a locked verification stays locked (a fresh `InitiateVerification` with a new amount is required, not a reset of the same one). |
| `QueryVerificationStatus(verificationId) -> VerificationStatus` | Read-only, doesn't consume an attempt. |
| `LatestVerificationIdForAccount(accountId) -> (verificationId, bool)` | Looks up the most recent verification for an account without the caller needing to have persisted the id itself. |
| `PeekAtMicroDepositAmountForTesting(verificationId) -> (amount, bool)` | **Test/demo-only stand-in** for "the account holder checks their real bank statement" — this repo has no real payment rail to actually deposit anything, so this function (wired to a clearly-named `/bank-verification/debug-peek` endpoint) is how a live `curl` session can complete the flow. A real build deletes this function and the endpoint entirely. |

**Tested behavior** (7 tests): fresh verification starts `PENDING`;
correct amount verifies; a single wrong guess stays `PENDING` (attempts
remain); exhausting all 3 attempts locks it, and the correct amount no
longer works against a locked verification; unknown verification id
returns `NOT_FOUND` for both status and confirm; `LatestVerificationIdForAccount`
returns the most recent of multiple verifications for one account;
generated amounts stay within the documented range with real variation
across runs.

### `internal/riskprofiling/riskProfiler.go` — new this build

Risk-tolerance questionnaire → investor risk category (FEATURES.md §1,
feeds a NOT-built Robo-Advisory feature — nothing downstream reads this
yet).

| Item | Purpose |
|---|---|
| `StandardQuestionnaire` | 6 fixed questions (investment horizon, drawdown reaction, income stability, investment goal, investing experience, emergency-fund coverage), 5 options each worth 1-5 points — mirrors the shape of real Vanguard/Fidelity-style questionnaires without claiming to legally BE one. |
| `RiskCategory` | 5 values, `CONSERVATIVE` through `AGGRESSIVE`. |
| `SubmitAnswers(accountId, answerPointValuesByQuestionId) -> (RiskProfile, error)` | Requires EXACTLY one entry per `StandardQuestionnaire` question (no more, no less), each a real option's point value — rejects a made-up score, only validates real selections. Sums to a 6-30 total, classified via `classifyScore` into explicit (not integer-division-derived) bands. |
| `LookupProfile(accountId) -> (RiskProfile, bool)` | Read-only; a later submission overwrites the earlier one entirely. |

**Tested behavior** (11 tests): lowest possible score (6) classifies
Conservative, highest (30) classifies Aggressive, all 5 category bands
verified reachable by sweeping uniform answer point values; a missing
question, an unrecognized question id, and an invalid point value are
all rejected with distinct sentinel errors; lookup before any submission
returns not-found; a second submission overwrites the first; two
accounts have independent profiles; the questionnaire shape itself
(6 questions × 5 options) is asserted directly.

### `internal/nomineedesignation`, `internal/jointholding` — new this build

FEATURES.md §1 "Nominee management, joint holding support"
(`docs/BUILD_LOG.md` entry 77). Split into two packages deliberately —
joint holding answers "who legally co-owns the account," nominee
designation answers "who receives assets on death" — distinct from
`services/backoffice`'s `nomineesuccession`, which is the later
death-claim workflow that would reference this designation record.
`nomineedesignation`: percentage allocations across all nominees for
an account must sum to exactly 100% or the account must explicitly
opt out; a minor nominee (computed age < 18) requires guardian
details. `jointholding`: INDIVIDUAL vs. JOINT accounts with three real
standard modes — JOINTLY (requires every holder's consent),
EITHER_OR_SURVIVOR (exactly 2 holders, any one's consent), ANYONE_OR_SURVIVOR
(3+ holders, any one's consent) — plus a primary-holder designation.
38 tests. Verified live on a running server. Known gap: nothing
downstream (e.g. order submission) currently gates on a complete
nomination or registered joint-holding structure — this service only
records the designations.

### `cmd/server/main.go`

| Route | Behavior |
|---|---|
| `GET /health` | Liveness check. |
| `POST /kyc/submit` | Decodes account id/PAN/name, calls `SubmitKycDetails`, returns the resulting stage. |
| `GET /kyc/status?accountId=...` | Calls `LookupKycStatus`, returns stage + eligibility + rejection reason. |
| `POST /bank-verification/initiate` | `{accountIdentifier, bankAccountNumber, ifscCode}` → `{verificationId}` — no amount included. |
| `POST /bank-verification/confirm` | `{verificationId, claimedAmountMinorUnits}` → `{verificationId, status}`. |
| `GET /bank-verification/status?verificationId=...` | Read-only status lookup. |
| `GET /bank-verification/debug-peek?verificationId=...` | **Test/demo-only** — returns the real deposited amount; see the caveat above. |
| `GET /risk-profile/questionnaire` | Serves the static `StandardQuestionnaire` — no account context needed. |
| `POST /risk-profile/submit` | Decodes account id + answers, calls `SubmitAnswers`, returns the scored profile or a 400 with the validation error. |
| `GET /risk-profile?accountId=...` | Calls `LookupProfile`; 404 if never submitted. |
| `GET /kyc/review-queue` (optionally `?stage=...`) | Calls `ListRecordsByStage`, defaulting to `StageRejected`. |
| `POST /kyc/review-queue/override` | `{accountIdentifier, newStage, overrideReason?}` → the updated record or a 400 with the validation error. |

**Verified end-to-end** (`docs/BUILD_LOG.md` entries 16, 34, 39): `oms-gateway`
genuinely rejects orders for un-KYC'd accounts and accepts them once
verified — see its `internal/kycclient`. Bank verification separately
verified live: initiate → status `PENDING` → debug-peek the real amount
→ 2 wrong guesses (still `PENDING`) → 3rd wrong guess locks
(`FAILED_LOCKED`) → the correct amount against the now-locked
verification is STILL rejected → a fresh verification with the correct
amount on the first try returns `VERIFIED` → an unknown verification id
returns `NOT_FOUND`. Risk profiling separately verified live: all-1
answers → `{totalScore: 6, riskCategory: "CONSERVATIVE"}`, all-5 answers
for a different account → `{totalScore: 30, riskCategory: "AGGRESSIVE"}`,
an invalid point value correctly rejected with the validation error.

**Known limitations:** "verification" is a format check only, not a real
provider call (FEATURES.md §1's actual scope). No `PENDING_REVIEW` stage
or manual review queue. In-memory only. Bank
verification isn't wired into oms-gateway's order-gating the way KYC is
— nothing currently requires a verified bank account for anything. No
real payment rail (see above). The risk-profile category feeds nothing
downstream yet (no Robo-Advisory feature exists) and gates nothing
(e.g. F&O eligibility) either.

### Auth/RBAC wiring — new this build (`docs/BUILD_LOG.md` entry 84)

Every route is now gated except `GET /health` and `GET
/risk-profile/questionnaire` (a static template, not account data).
Owner-only self-service routes (`/kyc/status`, `/kyc/submit`, `/bank-
verification/initiate`/`confirm`/`status`, `/risk-profile/submit`,
`/risk-profile`, `/nominees/*`, `/joint-holding/*`) use two small new
wrapper helpers, `requireOwnAccountFromJsonBody`/
`requireOwnAccountFromQueryParam`, both built on the duplicated
`internal/authmiddleware` package's `RequireAuth` plus the same
"caller must equal the account the request names" check the other
services use. `/bank-verification/debug-peek` is deliberately
`RequireRole(..., RoleAdmin)` rather than owner-self-service — it
exposes the internal micro-deposit amount a real bank-verification
challenge exists specifically to keep from the account holder.
`/kyc/review-queue` and `/kyc/review-queue/override` (the compliance
review-queue workflow) use `RequireRole(..., RoleCompliance)` — a
deliberate choice of the dedicated `RoleCompliance` constant over
`RoleAdmin`, with a real consequence: `authmiddleware.RequireRole` does
an exact single-role match, so a `RoleAdmin` token is currently
REJECTED from these two routes (no role-hierarchy/"any of" support
exists) — flagged for product review, see `docs/BUILD_LOG.md` entry
84's consolidated judgment-call list. CORS replaced the old wildcard
with the same allow-listed-origin-echo pattern
(`CORS_ALLOWED_ORIGINS`) every other gated service in this build uses.
`gofmt`/`go vet`/`go build`/`go test -race` clean, zero regressions.

**A real, exploitable ownership-check bypass was found and fixed
(`docs/BUILD_LOG.md` entry 87):** `requireOwnAccountFromJsonBody`
fell through to the wrapped handler whenever its own `json.Unmarshal`
of the request body failed, on the assumption the wrapped handler's
own decode would also fail closed. That assumption was wrong — most
handlers decode via `json.NewDecoder(...).Decode(...)`, which reads
only the first JSON value and silently ignores trailing bytes, so a
body shaped like `{"accountIdentifier":"someone-elses-account",...}<garbage>`
would fail `Unmarshal` (skipping the ownership check, fail-OPEN) while
still decoding successfully in the wrapped handler — a real
authorization bypass on every owner-only self-service route, not a
theoretical one. Fixed by returning `400` instead of falling through
to `next` on a body-decode failure.

**Verified live**: a real `acct-001` JWT successfully fetched `GET
/kyc/status?accountId=acct-001` (200); the same route with no token →
401; the same route with `acct-002`'s token and `accountId=acct-001` →
403.

## services/backoffice (Go) — account freeze/unfreeze is real

### `internal/accountcontrol/accountFreezeStateMachine.go`

| Function | Purpose |
|---|---|
| `FreezeAccount(accountId, reason)` | Adds the account to the frozen-accounts map with a required reason. |
| `UnfreezeAccount(accountId)` | Removes it from the map. |
| `CheckFreezeStatus(accountId) AccountFreezeStatus` | Absence from the map means not frozen — no need to pre-register every account. |

**Tested behavior** (4 tests): not-frozen by default; freeze stores the
reason; unfreeze clears it; freezing one account doesn't affect another.

### `cmd/server/main.go`

| Route | Behavior |
|---|---|
| `GET /health` | Liveness check. |
| `POST /accounts/freeze` | Requires a non-empty `freezeReason` (rejects the request otherwise — freezing without a recorded reason isn't acceptable) — calls `FreezeAccount`. |
| `POST /accounts/unfreeze` | Calls `UnfreezeAccount`. |
| `GET /accounts/freeze-status?accountId=...` | Calls `CheckFreezeStatus`. |

**Verified end-to-end** (`docs/BUILD_LOG.md` entry 16): `oms-gateway`
genuinely rejects orders for a frozen account (with the recorded reason
in the response) and accepts them again after unfreezing — see its
`internal/backofficeclient`.

**Known limitations:** no auth/RBAC (anyone reaching these endpoints can
freeze any account), no audit trail of who froze what and when, in-memory
only. Every other FEATURES.md §14 feature (KYC review queue, manual order
intervention, corporate actions, support tickets) is still just a TODO.

### `internal/strategyleaderboard`, `internal/familyaccountaccess`, `internal/nomineesuccession` — new this build

FEATURES.md §19's copy-trading leaderboard and §21's family/joint
account views + nominee succession (`docs/BUILD_LOG.md` entry 70).

**Correctness fix, `docs/BUILD_LOG.md` entry 87 (regression from this
repo's own entry-84 auth pass):** `main.go`'s family-access and
referral-qualification handlers both called `omsgatewayclient.FetchPositions`
with no `Authorization` header — silently broken (401s from
oms-gateway) since entry 84 gated oms-gateway's `/positions` behind
`RequireAuth`. `FetchPositions` gained a `callerAuthorizationHeaderValue`
parameter, threaded through from both call sites, forwarding the
caller's own bearer token.

`strategyleaderboard` reads real data from `oms-gateway`'s
`internal/strategyfollowing` and `internal/algolimits` via a new
read-only `internal/omsgatewayclient` — explicitly NOT self-reported,
though honestly scoped down to a real activity-turnover proxy rather
than true audited P&L (oms-gateway's audit trail doesn't yet tag fills
with a strategy identifier). `familyaccountaccess` provides real
account-linking with a genuinely enforced VIEW_ONLY boundary — proven
by a reflection-based test asserting the exposed capability set
contains no order-submission-shaped method. `nomineesuccession` is a
real, auditable SUBMITTED→UNDER_REVIEW→APPROVED→TRANSFERRED (or
REJECTED) state machine modeled on the existing account-freeze
pattern, with a real immutable transition log — explicitly a
workflow/paperwork state machine, not identity verification (document
references are opaque, never verified). 62 total tests for the
service.

**Verified live:** a linked viewer got real owner positions, an
unlinked stranger got a real 403; a full nominee-succession state walk
completed on a running server, a premature approval correctly rejected.

### `internal/supportticketing`, `internal/referralrewards`, `internal/localizationcatalog` — new this build

FEATURES.md §14's remaining backend items (`docs/BUILD_LOG.md` entry
73). `supportticketing` is a real ticket lifecycle (open →
in-progress → resolved → closed, with auto-reopen on customer
follow-up), threaded per-ticket messages, and auto-assignment on first
agent reply, under `/support/tickets/*`. `referralrewards` (plus a new
`internal/ledgerclient`) generates stable per-account referral codes,
blocks self- and double-referral, and credits a real ₹100.00 cash
reward via a genuine ledger HTTP call once the referred account
completes its first trade (detected via a real `FetchPositions` call
to oms-gateway) — verified idempotent (no double-credit) on a repeat
check. `localizationcatalog` serves 39 real UI-string keys (harvested
from `apps/web`'s actual pages) translated into English, Hindi, and
Tamil via `GET /localization/languages` and `GET
/localization/{lang}`. 11 + 7 tests. Honest limitations: all
in-memory (no persistence); auth/RBAC gap closed for the rest of this
service's routes as of this build (below) — `/localization/*`
specifically stays intentionally public, see below; referral
qualification is pull-based (no webhook).

### Auth/RBAC wiring — new this build (`docs/BUILD_LOG.md` entry 84)

Every route is now gated except `GET /health`, `GET
/localization/languages`, and `GET /localization/{lang}` (a public
reference catalog, not account data — `apps/web`'s language switcher
calls it before any login). `/accounts/freeze`/`/unfreeze` and
`/nominee-succession/approve`/`/mark-transferred`/`/reject` are
`RequireRole(..., RoleAdmin)`; `/accounts/freeze-status`,
`/nominee-succession/move-to-under-review`/`/status`/`/audit-trail`,
and the agent-facing `/support/tickets/agent-reply`/`/assign`/
`/status`/`/by-agent`/`/queue` are `RequireRole(..., RoleSupport)`.
Self-service routes (`/family-access/*`, `/nominee-succession/
register-nominee`/`/nominee`/`/submit`, `/support/tickets/create`/
`/customer-message`, `/referral-rewards/*`, `/strategy-leaderboard`)
use `RequireAuth` plus the shared `requireOwnAccount` helper.

**A real, pre-existing ownership bug was found and fixed while wiring
this**: `buildGetTicketHandler` (`GET /support/tickets/get`) and
`buildGetMessageThreadHandler` (`GET /support/tickets/thread`) only
ever took a `ticketId` query parameter, never an account id — so there
was previously no per-request field to check ownership against at all,
meaning ANY caller could read any ticket's contents/thread by
guessing/enumerating ids. Fixed by looking the ticket up FIRST
(`registry.GetTicket`) and checking `requireOwnAccount` against the
fetched ticket's OWN `AccountIdentifier` field, not a caller-supplied
one — the ticket, not the request, is the source of truth for who owns
it. CORS replaced the old wildcard with the same allow-listed-
origin-echo pattern (`CORS_ALLOWED_ORIGINS`) every other gated service
in this build uses — backoffice had none before this pass either way
(a wildcard `*`, same gap as the others).
`gofmt`/`go vet`/`go build`/`go test -race` clean, zero regressions.

**Verified live**: a real `acct-001` JWT successfully fetched `GET
/family-access/links?ownerAccountId=acct-001` (200); the same route
with no token → 401; the same route with `acct-002`'s token and
`ownerAccountId=acct-001` → 403; a retail token against `POST
/accounts/freeze` → 403 (admin-only); the same route with no token at
all → 401 (confirming auth is checked before role, not the other way
around).

**Known ambiguity flagged for product review:**
`/nominee-succession/submit` requires the AUTHENTICATED caller's own
account id to equal the request's `accountIdentifier` field — but that
field names the DECEASED account holder whose succession is being
filed, and the real-world actor filing a death claim is typically a
NOMINEE, not the deceased person logging in as themselves. This
ownership model needs a product decision (should the nominee's own
account be checked against a registered nominee record instead, once
one exists?) rather than the mechanical "caller == subject account"
check every other self-service route uses correctly. See
`docs/BUILD_LOG.md` entry 84's consolidated judgment-call list.

---

## services/auth (Go) — new service: real register/login, JWT, refresh-token rotation with reuse detection

New this build. FEATURES.md §1: "Email/phone auth, session management,
JWT + refresh token rotation." Real end-to-end, but not yet integrated
with any other service — see Known limitations.

### `internal/passwordhashing/passwordHasher.go`

| Function | Purpose |
|---|---|
| `HashPassword(plaintext) -> string` | PBKDF2-HMAC-SHA256 (`crypto/pbkdf2`, Go stdlib as of Go 1.24) with a fresh random 16-byte salt, 100,000 iterations. Returns a self-describing string (`pbkdf2-sha256$<iterations>$<saltB64>$<hashB64>`) — `VerifyPassword` never needs the caller to track parameters separately. |
| `VerifyPassword(candidate, encodedHash) -> (bool, error)` | Re-derives the hash using the embedded salt/iteration count and compares via `crypto/subtle.ConstantTimeCompare` — not a plain `==`, which would leak timing information. |

**Tested behavior** (5 tests): correct/wrong password round-trip; two
hashes of the same password differ (independent random salts) but both
still verify; malformed stored hash returns an error, not a panic; empty
password is internally consistent (policy rejection belongs elsewhere).

### `internal/jwtauth/jwtToken.go`

Hand-rolled HS256 JWT (header.payload.signature, `crypto/hmac`/
`crypto/sha256` only — no external JWT library).

| Function | Purpose |
|---|---|
| `IssueAccessToken(accountId, role, secret, lifetime, issuedAt) -> string` | Builds and signs a token; claims are `sub`/`role`/`iat`/`exp` (`sub`/`iat`/`exp` mirror standard JWT claim names, unlike this repo's usual long-name convention, since JWT interop depends on the exact key names; `role` is this build's own addition, new this round). |
| `ParseAndVerifyAccessToken(token, secret, now) -> (AccessTokenClaims, error)` | Constant-time signature check, then expiry check. Returns `ErrTokenSignatureInvalid`, `ErrTokenExpired`, or `ErrTokenMalformed` as distinct sentinel errors. |
| `RoleRetail`/`RoleAdmin`/`RoleSupport`/`RoleCompliance` | **New this build** (`docs/BUILD_LOG.md` entry 84). A small closed set of role strings carried in the `role` claim — the actual RBAC vocabulary `internal/authmiddleware` (duplicated into oms-gateway, backoffice, kyc-onboarding, api-gateway) checks against. Assignment beyond the default `RoleRetail` has no admin-facing workflow in this build — nothing mints an admin/support/compliance account except by direct code (see `accountstore.RegisterAccount`'s `role` parameter, seeded demo accounts stay `RoleRetail`). |

**Tested behavior** (8 tests, up from 7 this build): valid token
round-trips its subject AND its role; expired token rejected (including
exactly-at-expiry-instant); wrong secret rejected; a forged claims
segment swapped onto a legitimately-signed header/signature is rejected
(signature no longer matches); malformed tokens (empty, no dots, wrong
segment count) rejected without panicking.

### `internal/sessionstore/sessionStore.go`

Refresh-token rotation with reuse detection — the real substance behind
"session management" in FEATURES.md §1.

| Function | Purpose |
|---|---|
| `IssueNewSessionFamily(accountId, issuedAt) -> refreshToken` | Starts a brand new login session (a fresh, random 32-byte family id) and returns its first refresh token. |
| `RotateRefreshToken(presentedToken, now) -> (RotationResult, error)` | Consumes the presented token and issues a new one in the SAME family. If the presented token was already consumed by an earlier rotation (reuse — the signature of a stolen token), revokes the ENTIRE family and returns `RotationResult{WasReuseDetected: true}` rather than an error, since the caller needs the account id to act on it (force logout, alert). |
| `RevokeRefreshToken(token)` | Explicit logout — revokes the token's whole family. |

**Tested behavior** (9 tests): fresh issuance; a valid rotation returns a
distinct new token for the same account; reusing an already-consumed
token is detected; reuse detection revokes the WHOLE family (the
legitimate rotated token also stops working, not just the reused one);
unknown/expired tokens rejected with distinct sentinel errors; explicit
logout revokes; two independent session families (e.g. two devices) for
the same account don't interfere with each other's reuse detection.
`go test -race` clean.

### `internal/accountstore/accountStore.go`

| Function | Purpose |
|---|---|
| `RegisterAccount(email, password, requestedAccountIdentifier, role) -> (accountId, error)` | Case-insensitive/whitespace-trimmed email matching; `ErrEmailAlreadyRegistered` on a duplicate email. **New this build** (`docs/BUILD_LOG.md` entry 84): `requestedAccountIdentifier` is OPTIONAL — pass `""` for the normal real-user path (mints `acct-<8-byte-hex>` exactly as before); pass a specific identifier to register under that EXACT id instead (still validated for uniqueness via a new `accountsByIdentifier` map, `ErrAccountIdentifierAlreadyExists` on a collision) — this is what `cmd/server/main.go`'s `seedDemoAccounts` uses to register `acct-001`/`acct-002` under the SAME identifiers oms-gateway/ledger already seed, reconciling (for these two accounts specifically) the account-identifier-namespace split the "Known limitations" below used to describe as fully open. `role` defaults to `jwtauth.RoleRetail` when `""`. |
| `AuthenticateWithPassword(email, password) -> (accountId, role, error)` | Returns the SAME `ErrInvalidCredentials` whether the email doesn't exist or the password is wrong — an unknown-email branch still runs a dummy password verification for rough timing parity, to avoid a distinguishable-response account-enumeration side channel. Now also returns the account's role (new this build), so `cmd/server/main.go`'s login handler can issue a JWT carrying the account's REAL role instead of a hardcoded one. |
| `RoleForAccountIdentifier(accountId) -> (role, found)` | **New this build.** Looks up a registered account's role by identifier (not email) — used on the refresh-token path, where only the account id (not the original email/password) is available, so a rotated access token still carries the account's real role. |

**Tested behavior** (10 tests, up from 6 this build): register+
authenticate round-trip; wrong password / unknown email both rejected
identically; duplicate registration rejected; case-insensitive/
whitespace-trimmed email matching; two accounts get distinct
identifiers; a caller-supplied identifier is honored and later resolves
correctly on login; a duplicate caller-supplied identifier is rejected
even under a different email; an omitted identifier still
auto-generates; an explicit role is honored and defaults to
`RoleRetail` when omitted.

### `internal/ratelimiter/rateLimiter.go`

A per-key SLIDING-window limiter (not a naive fixed/calendar window —
see the module doc and its dedicated regression test for exactly the
double-burst-across-a-boundary failure mode a fixed window has and this
doesn't).

| Function | Purpose |
|---|---|
| `NewRateLimiter(maxAttemptsPerWindow, windowDuration)` | Constructs a limiter allowing at most N attempts per key in any rolling window of that width. |
| `Allow(key, now) -> bool` | Records one attempt for `key` (whether allowed or rejected — a rejected attempt still counts, so an attacker gets no free retry) and reports whether it's within the limit, pruning expired timestamps first. |
| `AttemptCountInCurrentWindow(key, now) -> int` | Read-only count, for diagnostics/tests. |

**Tested behavior** (6 tests): within-limit attempts allowed, the next
one rejected; rejected attempts still count; attempts age out of the
window and free up capacity again; different keys have independent
limits; the boundary regression test described above.

### `internal/totp/totp.go` and `internal/mfastate/mfaState.go` — new this build

Real RFC 6238 TOTP MFA. Hand-rolled (stdlib `crypto/hmac`/`crypto/sha1`
only — SHA-1 because that's what the RFC and every real authenticator
app expect; HMAC's security doesn't depend on SHA-1's collision
resistance, so this isn't a meaningful downgrade for this specific use).

| Item | Purpose |
|---|---|
| `GenerateRandomSecret() -> string` | 160-bit random secret, base32-encoded (`crypto/rand`). |
| `GenerateCode(secretBase32, atTime) -> string` | RFC 4226 HOTP algorithm (HMAC-SHA1 + dynamic truncation) over the 30-second time-step counter, reduced mod 10^6. |
| `VerifyCode(secretBase32, candidate, atTime, allowedSkewSteps) -> bool` | Checks the current step plus `allowedSkewSteps` steps either side, tolerating real clock drift between the server and the user's device. |
| `BuildOtpAuthUri(secret, accountLabel, issuer) -> string` | The standard `otpauth://totp/...` URI a real client renders as a QR code. |
| `mfastate.MfaState` | `BeginEnrollment` (generates + stores a secret, NOT yet enabled) → `ConfirmEnrollment` (activates only once a valid code proves the enrollment actually worked) → `IsMfaEnabled`/`VerifyLoginCode` (what login gates on) → `DisableMfa` (removes the secret entirely). |

**Tested behavior** (17 tests: 8 totp + 9 mfastate): **cross-checked
against RFC 6238 Appendix B's published test vectors** (the standard
20-byte ASCII test seed, at four documented timestamps, converted from
the RFC's 8-digit vectors to this implementation's 6-digit output) —
not just self-consistency, a real correctness check against an external
reference. Also: 6-digit output format; current code accepted; wrong
code rejected; one step of clock skew tolerated, three steps rejected;
two random secrets differ; the otpauth URI contains the expected
parameters; enrollment doesn't enable MFA until confirmed; a wrong
confirmation code doesn't enable it; confirming with no enrollment in
progress fails; a valid code verifies at login once enabled; an
unenrolled account's `VerifyLoginCode` always fails; `DisableMfa`
removes the enrollment (the old code stops working); two accounts have
independent MFA state.

### `cmd/server/main.go`

| Route | Behavior |
|---|---|
| `GET /health` | Liveness check. |
| `POST /auth/register` | `{email, password}` → `{accountIdentifier}` or 409 on duplicate. Rate-limited: 3/minute per source address (`net.SplitHostPort(request.RemoteAddr)`, not real `X-Forwarded-For`-aware — see Known limitations), 429 with an error message beyond that. |
| `POST /auth/login` | `{email, password, totpCode?}` → `{accountIdentifier, accessToken, refreshToken, expiresInSeconds}`, OR `{mfaRequired: true}` with no tokens if the account has MFA enabled and `totpCode` is missing/wrong (401 for wrong, 200 for missing — the client uses `mfaRequired` to tell "need a code" apart from "wrong password" either way), OR 401 for a bad password. Rate-limited: 5/minute per normalized email (not per address — an attacker spreading attempts across many source IPs still can't out-pace this), 429 beyond that, checked before the password is ever verified. |
| `POST /auth/refresh` | `{refreshToken}` → a new token pair, OR 401 with a reuse-detected error message (also logged as a `SECURITY:` line) if the token was already consumed. |
| `POST /auth/logout` | `{refreshToken}` → revokes the whole session family. |
| `GET /auth/verify` | `Authorization: Bearer <token>` → `{isValid, accountIdentifier}` or 401 — an introspection convenience; a real hot-path caller should verify locally with `jwtauth.ParseAndVerifyAccessToken` instead of a network round-trip per request. |
| `POST /auth/mfa/enroll` | `Authorization: Bearer <token>` (identifies the account — no request-body account field) → `{secretBase32, otpAuthUri}`. Doesn't enable MFA yet. |
| `POST /auth/mfa/confirm-enrollment` | `Authorization: Bearer <token>` + `{totpCode}` → `{mfaEnabled: true}` on a correct code, 400 otherwise. This is what actually flips MFA on. |
| `POST /auth/mfa/disable` | `Authorization: Bearer <token>` → `{mfaEnabled: false}`. **Known gap**: only requires a valid access token, not a fresh password/MFA re-confirmation — see the handler's own comment. |

`requireValidAccessToken` is the shared helper all three `/auth/mfa/*`
handlers use to resolve `accountIdentifier` from the bearer token rather
than trusting a request-body field — a stricter pattern than this
skeleton's earlier endpoints (register/login/refresh/logout) use, since
those don't have a token to check against yet at the point they're
called.

Listens on `:8086` (`AUTH_LISTEN_ADDRESS` overridable — chosen to avoid
colliding with quant-engine's `:8085`). `AUTH_JWT_SIGNING_SECRET` falls
back to a loudly-logged insecure development default if unset.
Structured JSON logging via the same `internal/httplogging` package
every other Go service uses. `withPermissiveCorsForDevelopment` (added
alongside `apps/web`'s `AccountSection`, `docs/BUILD_LOG.md` entry 32) —
so a browser can call `/auth/*` directly. **Not** updated to the
allow-listed-origin form the other four services adopted this build
(`docs/BUILD_LOG.md` entry 84) — `services/auth` itself was out of
scope for that pass, so it's now the ONE service in this build still
serving `Access-Control-Allow-Origin: *`, which is fine for its own
unauthenticated register/login/refresh endpoints (no credentialed
request needs it) but is a real inconsistency worth closing in a future
pass for uniformity.

New this build (`docs/BUILD_LOG.md` entry 84): `main()` now also calls
`seedDemoAccounts(accounts, demoAccountPassword)` at startup, which
registers `acct-001`/`acct-002` (`demoSeedAccountIdentifiers`) under
`<accountId>@demo.mercurius.local` with `AUTH_DEMO_ACCOUNT_PASSWORD`
(falls back to a loudly-logged insecure dev default, same pattern as
`AUTH_JWT_SIGNING_SECRET`) — both stay `RoleRetail`. `buildLoginHandler`
and `buildRefreshHandler` now thread the account's real role (from
`accountstore.AuthenticateWithPassword`/`RoleForAccountIdentifier`
respectively) into `jwtauth.IssueAccessToken`, instead of not carrying
a role at all.

**Verified live** (`docs/BUILD_LOG.md` entries 31, 32): the full
register → login → verify → refresh (rotation) → reuse-attempt
(detected, family revoked, logged) → confirm the legitimate rotated
token is ALSO now dead → fresh login → logout → confirm
refresh-after-logout fails sequence, all against a real running process
with real `curl` requests; separately, a real cross-origin `OPTIONS`
preflight and `POST /auth/register` from `Origin: http://localhost:3000`
both returned the correct CORS headers; separately (`docs/BUILD_LOG.md`
entry 33), 6 rapid wrong-password attempts against one account returned
401×5 then 429, a different account was unaffected, and 4 rapid
registrations from one address returned 201×2 then 429×2; separately
(`docs/BUILD_LOG.md` entry 35), the full MFA flow: enroll → confirm with
a wrong code (rejected) → confirm with a code from an INDEPENDENTLY
written Python RFC 6238 implementation (not this Go code — a genuine
second implementation agreeing, not self-consistency) → enabled → login
without a code (`mfaRequired`) → login with a wrong code (401) → login
with the correct (Python-computed) code → real tokens → disable → login
without a code succeeds again → enroll without a valid bearer token
(401).

**Known limitations (updated, `docs/BUILD_LOG.md` entry 84):**
login/register/logout are reachable from `apps/web`'s `AccountSection`
(entry 32), and as of this build the resulting JWT genuinely DOES gate
access — oms-gateway, backoffice, kyc-onboarding, and api-gateway all
require a valid bearer token on most routes (see each service's own
section above), and `apps/web` stores the token (`localStorage`, see
its own section) and attaches it to every fetch hitting one of those
four services. The account-identifier-namespace split is now only
PARTIALLY closed: `acct-001`/`acct-002` are seeded here under the SAME
identifiers oms-gateway/ledger use (`accountstore.RegisterAccount`'s
new optional-identifier parameter, `AUTH_DEMO_ACCOUNT_PASSWORD` env
var with an insecure dev default), so the two demo accounts can log in
for real — but any NEWLY self-registered account still mints its own
`acct-<random hex>` disconnected from anything oms-gateway/ledger might
independently know about that identifier (a real build still needs one
canonical account identifier minted in exactly one place). No phone
auth, no email verification, no password-strength policy. In-memory
only, so a restart loses every registered/seeded account and every
session — including the seeded demo accounts, which are re-seeded
fresh on every process start (idempotent by construction, since they're
seeded before any real traffic). HS256 means every verifying service
needs the shared secret (`AUTH_JWT_SIGNING_SECRET`, same insecure dev
default duplicated into every service's copy of `internal/
authmiddleware` — see e.g. `services/oms-gateway`'s section above) — a
real build would prefer RS256/ES256 so the signing secret never leaves
this service, AND would need a real secret-rotation story, which
doesn't exist in any form here (rotating the secret today means
restarting every service simultaneously with the new value, invalidating
every live session with no graceful overlap window). `RoleAdmin`/
`RoleSupport`/`RoleCompliance` accounts have no self-service or
admin-facing creation path — only `RegisterAccount`'s `role` parameter,
called directly from Go code (as `seedDemoAccounts` does for
`RoleRetail`), can mint one, meaning the admin/support/compliance-only
routes gated across the other four services in this build are currently
only reachable by an operator who edits `cmd/server/main.go` to seed
one, not through any real provisioning flow.

### `internal/anomalouslogindetection` — new this build

FEATURES.md §19's anomalous-login detection (`docs/BUILD_LOG.md` entry
70) — explicitly re-scoped honestly: real RULE-BASED detection, NOT a
trained ML model (no labeled account-takeover dataset exists anywhere
in this repo to train one, despite the FEATURES.md item's "ML-based"
framing). Real new-device/new-fingerprint flagging, a real impossible-
travel heuristic (distance/time-implied-speed math over illustrative
location tags), and rapid-failed-then-success pattern detection. 18
tests. **A subtle bug was caught during design, before it ever
shipped**: the login handler would have keyed anomaly detection
inconsistently between success (account identifier) and failure
(normalized email, since a failed auth never reveals a real account
identifier) — fixed to key consistently on normalized email throughout,
preserving the rapid-failure-then-success correlation.

**Verified live:** a real NYC→Tokyo-in-5-minutes login pair correctly
flagged both new-device and impossible-travel together.

---

## services/quant-engine (Python) — Black-Scholes module AND a real HTTP service

Now a real running service with a port, like every other `services/*`.
Explicitly research-tier per ARCHITECTURE.md §8: not to be placed directly
on a latency-sensitive path without a Rust port.

### `src/quantengine/blackScholesOptionPricer.py`

| Function / Type | Purpose |
|---|---|
| `calculateStandardNormalCumulativeDistribution(x)` | N(x), via `math.erf`. |
| `calculateStandardNormalProbabilityDensity(x)` | N'(x), the standard normal PDF. |
| `BlackScholesInputParameters` | Frozen dataclass: spot, strike, risk-free rate, volatility, time-to-expiry (years). |
| `calculateD1AndD2(params) -> (d1, d2)` | Shared intermediate terms; raises `ValueError` on non-positive time-to-expiry or volatility. |
| `calculateBlackScholesCallOptionPrice(params)` | $C = S\Phi(d_1) - Ke^{-rT}\Phi(d_2)$ |
| `calculateBlackScholesPutOptionPrice(params)` | $P = Ke^{-rT}\Phi(-d_2) - S\Phi(-d_1)$ |
| `OptionGreeksResult` | Frozen dataclass: `delta`, `gamma`, `vegaPerOnePercentVolatilityChange`, `thetaPerCalendarDay`. |
| `calculateOptionGreeks(params, isCallOptionNotPut)` | Computes all four Greeks for one contract. This is the per-contract building block that a future portfolio-level Greeks aggregator (FEATURES.md §22) would sum across positions — the aggregation itself is not implemented here. |
| `solveImpliedVolatilityFromMarketPrice(marketPrice, paramsWithoutVol, isCall, ...)` | Newton-Raphson solve for implied volatility using vega as the derivative. Raises `ValueError` if vega becomes too small to continue (deep ITM/OTM near expiry) rather than returning a bad estimate, and if it fails to converge within `maximumIterationCount`. |

**Tested behavior** (7 tests): call/put price against a known textbook
reference (S=K=100, r=5%, σ=20%, T=1y); put-call parity as an exact
arbitrage-free invariant; call/put delta sign and range bounds; gamma
equality between call and put at the same strike (regression guard); IV
solver round-trip (price at known σ, solve backward, recover it).

**Known limitations:** no GARCH(1,1), no Sharpe/Sortino/VaR, no arbitrage
scanner (this module is the pricer such a scanner would call), no
portfolio-level Greeks aggregation, no bisection fallback in the IV solver
for the low-vega region.

### `src/quantengine/httpServer.py` — real HTTP service, new this build

Stdlib-only (`http.server` + `json`, no framework dependency — same
"hand-roll it" convention as matching-engine's/market-data's Rust TCP/HTTP
bridges) on `127.0.0.1:8085`. Stateless: every request is one pure
computation, so `ThreadingHTTPServer` needs no locking here — that would
NOT generalize to a future version of this service holding shared state.

| Item | Purpose |
|---|---|
| `QuantEngineRequestHandler` | `BaseHTTPRequestHandler` subclass; `do_GET`/`do_POST` route by `self.path`. Silences the default per-request stderr log line (`log_message` overridden to a no-op), matching this repo's other services. |
| `GET /health` | Liveness check, `{"status": "ok"}`. |
| `POST /options/price` | Body: `BlackScholesInputParameters` fields + `isCallOptionNotPut`. Returns theoretical price AND all four Greeks in one response (a caller pricing a contract almost always wants both). 400 on a missing/malformed field, 422 on a business-level rejection from the pricer itself (e.g. non-positive `timeToExpiryInYears`). |
| `POST /options/implied-volatility` | Body: `observedMarketPrice` + the same parameter shape MINUS `annualizedVolatility` (that's what's being solved for). Delegates to `solveImpliedVolatilityFromMarketPrice`; 422 if it fails to converge or vega collapses. |
| `buildInputParametersFromRequestBody(body)` | Shared JSON→`BlackScholesInputParameters` conversion; raises `KeyError`/`TypeError`/`ValueError` on a bad body, caught by both handlers and turned into a 400. |
| `_writeJsonResponse(status, body)` | Every response carries `Access-Control-Allow-Origin: *` (permissive CORS for `apps/web` — same "wrong once real auth exists" caveat as oms-gateway's and market-data's CORS middleware). |
| `runQuantEngineHttpServer()` | Entry point, also wired as the `quant-engine-server` console script in `pyproject.toml`. |

**Tested behavior** (8 tests in `tests/test_httpServer.py`, live HTTP
requests via `urllib` against a real `ThreadingHTTPServer` bound to an
OS-assigned ephemeral port, not the hardcoded 8085): health check; a full
price+Greeks response for a known reference contract; 400 for a missing
field; 422 for non-positive time-to-expiry; an implied-volatility round
trip (price at a known σ via `/options/price`, solve backward via
`/options/implied-volatility`, recover the same σ); 404 for an unknown
route; 400 for malformed JSON; the CORS header's presence.

**Verified live** (`docs/BUILD_LOG.md` entry 27): started the service for
real, confirmed `/health`, a real priced contract matching the same
textbook reference value the unit tests check (S=K=100, r=5%, σ=20%,
T=1y → 10.4506), the IV solver recovering σ≈0.2 from that same price, the
400/422 error paths, and the CORS header — all via real `curl` requests,
not just `pytest`.

**Correctness fixes, `docs/BUILD_LOG.md` entry 87:** a `bool("false")`
gotcha at both `_handlePriceAndGreeksRequest` and
`_handleImpliedVolatilityRequest` — `bool(requestBody["isCallOptionNotPut"])`
turned the JSON string `"false"` into Python `True` silently; both now
require an actual JSON boolean via `isinstance` and raise otherwise.
Separately, a new `_requireDictField` helper closes a systemic
null-field crash: 6 call sites across 5 endpoint handlers
(`_handleCorrelationMatrixRequest`, `_handleFactorRiskRequest` — 2
call sites, `_handleLatencyBenchmarkRequest`,
`_handlePortfolioHealthCheckRequest`,
`_handleAlternativeDataFilingAnomalyRequest`) previously let a `None`/
missing dict-shaped request field raise an uncaught `AttributeError`
(→ opaque 500) instead of a clean, caught `ValueError` (→ 422).

### `riskStatistics.py`, `arbitrageScanner.py`, `backtesting/` — new this build

FEATURES.md §6: "Sharpe Ratio / Sortino Ratio / max drawdown" and
"Arbitrage scanner"; §7: "Historical tick data store + backtest runner"
and "Pairs trading template (z-score mean reversion)".
`riskStatistics.py` computes annualized Sharpe ratio, annualized Sortino
ratio (downside-deviation denominator), and max drawdown from an equity
curve. `arbitrageScanner.py` compares a theoretical fair price against a
live price and flags a deviation-threshold breach. `backtesting/`
(new subpackage) has `tickStore.py` (in-memory per-symbol tick series),
`backtestRunner.py` (a real event-driven backtest loop, deterministic —
same input twice produces byte-identical output, tested), and
`pairsTradingStrategy.py` (real z-score mean-reversion over a price
spread, wired as a `backtestRunner` strategy callback). 45 new tests (65
total for the service, up from 20).

`POST /risk/statistics` and `POST /arbitrage/scan` are wired into the
existing HTTP server; the backtest runner and pairs strategy are
pytest-verified only, not yet HTTP-exposed (a documented time-budget
tradeoff, not an oversight).

**Verified live** (`docs/BUILD_LOG.md` entry 50): both new HTTP
endpoints returned hand-worked values exactly (Sortino differed by 2e-15
float noise); zero-variance and non-positive-price inputs correctly
422'd; pre-existing `/options/price` re-verified unaffected.

**Known limitations:** pairs strategy trades the spread as one synthetic
instrument rather than sizing two real hedge-ratio legs; formulas are
real math, not tuned against live market data. (Paper trading was
subsequently built in `oms-gateway` — see its section above — and
strategy deployment gates were subsequently built here, below.)

**Correctness fixes, `docs/BUILD_LOG.md` entry 87:**
`arbitrageScanner.py`'s `scanManyLivePricesForDeviationAlerts` used to
let one symbol's bad (non-positive) theoretical price raise and abort
the ENTIRE batch scan; now wraps each symbol in
`try/except ValueError: continue`, so one bad input no longer takes
down every other symbol's result. New shared module
`backtesting/positionTolerance.py` (`POSITION_FLAT_EPSILON = 1e-9`,
`isPositionFlat(quantity)`) replaces exact `== 0` float-equality checks
at 3 sites in `backtestRunner.py` (`PortfolioState.calculateUnrealizedProfitAndLoss`,
`applySignedQuantityChangeToPortfolio`'s same-direction check and its
`newPositionQuantity == 0` check) and 1 site in
`pairsTradingStrategy.py` (the `isFlat`/`isLong`/`isShort` entry-rule
logic). A third, related float-equality fix in
`realisticBacktestCostModel.py` (`isFullyFilled=(totalFilledQuantity
== orderQuantity)` → an epsilon comparison) fixes the same class of
bug but does NOT import `positionTolerance` — it defines its own
local, duplicate `_FULLY_FILLED_QUANTITY_EPSILON = 1e-9` instead; a
real follow-up should route it through the shared module.

### `garchVolatilityForecaster.py`, `correlationMatrixEngine.py`, `valueAtRiskCalculator.py`, `volatilitySurfaceBuilder.py`, `strategyLifecycle.py`, `marketMakingSandbox.py`, `illustrativeSentimentTradingHook.py` — new this build

Seven FEATURES.md items (§6 remaining P3/P4, §7 remaining P3/P4).

- **GARCH(1,1)**: a real recursion (`σ²_t = ω + α·ε²_{t-1} + β·σ²_{t-1}`),
  fit via a documented simplified variance-targeting + grid-search
  quasi-MLE (not a production optimizer), producing a next-period
  volatility forecast and an Expected Intraday Range. 22 tests,
  hand-worked recursion matched exactly.
- **Correlation matrix**: real pairwise Pearson correlation +
  configurable-threshold candidate-pairs filter for pairs-trading
  discovery. 14 tests, hand-worked r=0.8315218406202999.
- **VaR**: real historical (percentile) and real parametric
  (mean/stddev + z-score, reusing the existing normal-CDF solver) VaR,
  plus an illustrative-scenario stress test. 19 tests. **Bug found and
  fixed**: the historical VaR percentile index landed one index low due
  to float representation error — fixed with an epsilon.
- **Volatility surface**: solves real IV per (strike, expiry) quote by
  reusing the existing Black-Scholes IV solver, assembles a surface,
  linear-interpolates across strikes. 11 tests, hand-worked
  interpolation matched (strikes 90/100/110 → IVs 0.25/0.20/0.22, strike
  95 → 0.225).
- **Strategy lifecycle**: a real BACKTESTING→PAPER_TRADING→LIVE (or
  REJECTED) promotion state machine, gated on real backtest-runner and
  paper-trading-track-record results against configurable illustrative
  bars. 17 tests.
- **Market-making sandbox**: real two-sided quote tracking, simulated
  taker fills, and a real inventory risk limit rejecting a quote update
  that would breach the configured long/short limit. 15 tests, wired to
  HTTP and verified live including the exact rejection message.
- **Illustrative sentiment hook**: an explicitly-toy lexicon-based
  sentiment scorer over fixture text, producing a structured BUY/SELL/
  HOLD suggestion ONLY when a kill-switch flag is explicitly enabled
  (default OFF) — never places a real order, never wired to any
  order-submission path. 14 tests.

GARCH, correlation, VaR, and market-making are wired into the real HTTP
server and verified live via curl matching hand-worked values exactly;
volatility surface, strategy lifecycle, and the sentiment hook are
pytest-verified only (documented time-budget choice, not an oversight).
Full suite: 186 tests total (up from 65), green throughout, re-verified
after every one of the seven items.

**Known limitations:** GARCH fit is a simplified quasi-MLE, not
production-grade; VaR stress scenarios and strategy-lifecycle promotion
gates are illustrative, not regulatory-calibrated; volatility surface
interpolates only across strikes, not expiries; market-making sandbox is
single-price-level; the sentiment hook never ingests real filings/
earnings data and structurally cannot place a real order.

### `esgScoringEngine.py` — new this build

FEATURES.md §17: "ESG/sustainability scoring and screening filters"
(`docs/BUILD_LOG.md` entry 64). A real, documented weighted-average
composite formula (Environmental 0.40 / Social 0.30 / Governance 0.30,
summing to exactly 1.0) over an illustrative 6-symbol dataset, plus real
screening/ranking/sector-exclusion logic. `POST /esg/screen` wired into
the existing HTTP server. 30 new tests (216 total for the service, up
from 186).

**Verified live:** E=70/S=60/G=80 → composite exactly 70.0, reproduced
over HTTP; combined criteria (minimum composite + sector exclusion + an
unknown symbol) correctly ranked, excluded, and flagged-unknown in one
response.

**Known limitation:** the ESG dataset itself is entirely fabricated/
illustrative — NOT sourced from any real rating agency (MSCI,
Sustainalytics, ISS ESG, Refinitiv/Bloomberg ESG). The scoring math and
screening logic are real and correctly implemented; the underlying data
is not.

### FEATURES.md §22 "Deep Quant & Algorithmic Trading Internals" — all 16 items, new this build

`docs/BUILD_LOG.md` entries 68 (10 items) and continuation (6 items).
471 tests total (up from 216), across two passes, zero regressions at
any step.

**Pass 1 (10 modules):** `portfolioGreeksAggregator.py` (net delta/
gamma/theta/vega across all positions); `ivRankCalculator.py` (real
IV Rank/Percentile against a historical series); `impliedVsRealizedVolatility.py`
(real annualized log-return realized-vol formula); `syntheticPositionBuilder.py`
(named synthetic structures with combined Greeks); `deltaHedgingMonitor.py`
(real hedge-quantity computation from net delta vs. a threshold);
`kellyCriterionSizer.py` (real f*=(bp-q)/b plus a documented half-Kelly
option); `strategyCorrelationMatrix.py` (the existing Pearson engine
applied at strategy-return-series level); `cointegrationTester.py` (a
genuine Engle-Granger two-step test with a real hand-rolled Augmented
Dickey-Fuller t-statistic — not a faked p-value); `realisticBacktestCostModel.py`
(sqrt-impact market-impact function + real partial-fill simulation);
`monteCarloEngine.py` (a seeded GBM path simulator whose European
option price was validated to converge toward closed-form Black-Scholes
within 4 standard errors at 50,000 paths — a real, meaningful
correctness test).

**Pass 2 (6 modules):** `walkForwardOptimizer.py` (real rolling in-
sample/out-of-sample windows + a documented overfitting-ratio and
observations-per-parameter heuristic); `factorRiskModel.py` (real
weighted factor-exposure aggregation and return attribution over
illustrative factor data); `latencyBenchmarkDashboard.py` (capable of
REAL HTTP round-trip timing — demonstrated live against quant-engine's
own `/health`; a check against oms-gateway found it unreachable in that
run and reported that honestly rather than fabricating a number);
`crossAssetMacroDashboard.py` (real cross-correlation math, reusing
`correlationMatrixEngine.py`, over illustrative fixture macro series);
`optionsCorporateActionHandler.py` (the real split-adjustment formula —
strike/ratio, quantity×ratio, notional-invariant — and the real
textbook early-exercise-risk condition for American calls near an
ex-dividend date); `regimeDetectionHmm.py` (a genuine hand-rolled 2-3-
state Gaussian Hidden Markov Model with real forward-backward/Viterbi
inference — explicitly not a threshold rule dressed up as an HMM, and
no ML dependency added, per this repo's stdlib-preferring convention).
**A real bug was found and fixed**: the HMM's Viterbi step crashed when
a converged, very-tight-variance state's Gaussian emission density
underflowed to exactly `0.0` for a distant observation — fixed with a
genuine log-space density helper instead of composing `log(exp(...))`.

**A second, related bug in the FORWARD algorithm (not Viterbi) was
found and fixed, `docs/BUILD_LOG.md` entry 87:** `runForwardAlgorithm`
materialized raw probabilities and divided to normalize at each step —
the same converged/tight-variance + distant-observation scenario above
could underflow that normalization to `ZeroDivisionError`/`math domain
error`. Rewritten to genuine log-space accumulation (new `_logSumExp`
helper, log-space transition/emission throughout) — the underflow is
now structurally impossible, not just less likely.

**Verified live over HTTP:** factor risk, latency benchmark, and both
corporate-action endpoints all matched hand-worked values exactly.
Walk-forward, the macro dashboard, and the HMM itself are pytest-
verified only (explicit, documented choice) — the HMM's own
forward-probabilities were verified separately: α̂₁=[0.9999975,
0.0000025], logLikelihood ≈ -3.55266183.

**Known limitations:** factor exposures and macro-dashboard data are
illustrative fixtures, not real market data; walk-forward's overfitting
thresholds are real literature conventions, not empirically
recalibrated; the HMM uses single-restart quantile initialization (a
real local-optimum risk); the early-exercise check omits transaction
costs/tax weighting and American put early-exercise.

**Correctness fix, `docs/BUILD_LOG.md` entry 87:**
`syntheticPositionBuilder.py`'s `validateProposedCombinationAgainstSyntheticStructure`
validated each leg's ratio independently, so two DUPLICATE legs of the
same type could each individually pass a per-leg check while the
combined exposure was actually double the structure's definition —
rewritten to aggregate quantity BY leg type (`aggregatedQuantityByLegType`)
before comparing against the expected ratio.

### FEATURES.md §16 "AI, Data & Research" — all 7 items, new this build

`docs/BUILD_LOG.md` entry 74. 574 tests total (up from 471), zero
regressions.

`stockScreenerFilterBuilder.py` (real recursive AND/OR/comparison
filter engine over real SMA/Wilder-RSI technicals plus illustrative
fundamentals, with saved-screen persistence); `researchCopilotRetrievalAugmentedGeneration.py`
(a real RAG pipeline — chunking, TF-IDF vectors, cosine-similarity
top-k retrieval, extractive citation-backed answer composition — over
a small hand-authored synthetic filing/earnings-call corpus, honestly
documented as TF-IDF retrieval rather than a trained neural embedding
model since no internet access is available offline; every response
carries a fixed non-advisory disclaimer); `portfolioHealthCheckDiversificationAnalyzer.py`
(real Herfindahl-Hirschman Index at both position and sector level
with DOJ/FTC severity bands, reuses `factorRiskModel.py`, nudge text
genuinely varies with the computed numbers — tested); `taxLossHarvestingAdvisor.py`
(real per-lot unrealized-loss identification, a real IRS 61-day
wash-sale window check, and a real gain-offset → $3,000-ordinary-cap →
carryforward waterfall, tested with both an excluded and a
non-excluded wash-sale case); `alternativeDataFeedAggregator.py`
(real pooled-count sentiment aggregation reusing §7's
`illustrativeSentimentTradingHook.py` lexicon scorer, and real
z-score filing-anomaly detection, genuinely wired into §7's NLP hook
with an integration test proving values flow through to a real
BUY/SELL suggestion); `factorBasedPnlAttributionEngine.py` (a real
Brinson-Hood-Beebower allocation/selection/interaction/currency
decomposition, tested against a fully hand-worked example);
`customIndexConstructionBacktester.py` (real top-N/cap-weight/
equal-weight/rebalance-frequency index construction over synthetic
history, real CAGR/Sharpe/max-drawdown computed from the actual
constructed price path, reusing `riskStatistics.py`).

**Two real bugs caught while building:** a test-fixture bug where an
all-TECH-sector "diversified" portfolio fixture tripped a HIGH
concentration nudge (the fixture was wrong, fixed by varying sectors);
a wash-sale test's expected value double-counted the repurchase lot's
own small loss (fixed by making that lot a gain, isolating what was
actually being tested).

**Verified live over HTTP:** all 11 new routes curled against a real
running server, including a Brinson decomposition matching the
hand-worked 0.007 active return exactly.

**Known limitations:** every dataset (screener universe, filings
corpus, alt-data snippets/metrics, index constituent history) is
synthetic/illustrative, documented as such in each module's header;
the RAG "embedding" is TF-IDF, not a trained model; tax-loss
harvesting is explicitly not tax advice and approximates "substantially
identical security" as same-symbol only; the custom-index item's
"licensable to other institutions" framing has no actual
licensing/entitlement system — only the real construction-and-backtest
engine was in scope.

**Correctness fix, `docs/BUILD_LOG.md` entry 87:**
`portfolioHealthCheckDiversificationAnalyzer.py`'s `performPortfolioHealthCheck`
crashed on an all-zero-weight portfolio — `calculateHerfindahlHirschmanIndex`
correctly computed exactly `0`, which then broke a downstream
precondition in `calculateEffectiveNumberOfHoldings`. A new explicit
`if positionHhi == 0: raise ValueError(...)` guard turns that into a
clean 422 instead of an opaque 500.

---

## apps/web (Next.js) — one page, now covering most of oms-gateway's surface plus a live price chart

Scaffolded with `create-next-app` (TypeScript, Tailwind, ESLint, App
Router). Real dependency tree, not hand-rolled.

| File | Purpose |
|---|---|
| `app/page.tsx` — `RetailTradingDashboardPage` | Top-level page, composes the six sections below. |
| `app/page.tsx` — `AccountSection` | Register/login/logout against the NEW `services/auth` (`NEXT_PUBLIC_AUTH_BASE_URL`, default `127.0.0.1:8086`) — real JWT access token + refresh token displayed on login, logout calls `/auth/logout`. Deliberately NOT wired into `OrderTicketSection`'s account field: auth mints its own `acct-<random hex>` identifier space, disconnected from oms-gateway's/ledger's seeded demo accounts — see `services/auth/README.md`. |
| `app/page.tsx` — `OrderTicketSection` | Submits to `/orders/submit` or (if the Cover Order toggle is on) `/orders/cover-submit`. Order-type dropdown selects Limit/Market/SL/SL-M, mapping to the right `orderIsMarketOrderNotLimit`/`orderIsStopLossVariant`/`stopTriggerPriceInMinorUnits` combination. Idempotency key auto-generated via `crypto.randomUUID()` (editable, regeneratable) so a real double-click exercises the idempotency guard rather than defeating it — **and, as of `docs/BUILD_LOG.md` entry 87, auto-REGENERATED on every successful submission** (`setIdempotencyKey(generateIdempotencyKey())` right after a successful ack), closing a real bug where a second, genuinely distinct order submitted before the user manually clicked "generate a new key" would reuse the already-consumed key and silently get back oms-gateway's cached response for the FIRST order instead of actually submitting. AMO checkbox and Cover Order toggle are mutually exclusive in the UI (an AMO can't also be a cover order in this skeleton). |
| `app/page.tsx` — `OrderAcknowledgementCard` | Renders one `OrderAcknowledgementResponse`: a distinct amber state for `isQueuedAsAfterMarketOrder`, green/red for accepted/rejected, surfaces `matchingEngineOrderSequenceNumber`, `matchingEngineHandoffError`, and any `tradeExecutionEvents`. |
| `app/page.tsx` — `PriceChartSection` / `CandlestickChart` | Polls `market-data`'s HTTP query API directly (`NEXT_PUBLIC_MARKET_DATA_BASE_URL`, default `127.0.0.1:9103`) every 5s (toggleable) for `GET /candles` and the latest `GET /trades` tick, and renders hand-rolled SVG candlesticks — no charting library dependency, same "don't reach for a framework" convention as elsewhere in this repo. |
| `app/page.tsx` — `MarketSessionSection` | Status/open/close against `/market-session/*`, shows queued-AMO count. |
| `app/page.tsx` — `PositionsSection` | Fetches `/positions?accountId=...`. |
| `app/page.tsx` — `OrderLookupSection` | Fetches `/orders/status` and posts to `/orders/cancel`, both by `instrumentSymbol` + `matchingEngineOrderSequenceNumber`. |
| `app/page.tsx` — `LabeledTextField`, `LabeledNumberField` | Small reusable form-field components, shared across sections. |
| `app/layout.tsx` | Root layout; metadata title/description updated from the create-next-app defaults. |

**Verified:** `npx tsc --noEmit` and `eslint` both clean (including this
build's `react-hooks/set-state-in-effect` rule — the chart's polling
effect carries a scoped, justified `eslint-disable` since its setState
calls all happen after an `await`, not synchronously); `next build`
succeeds (Turbopack, static-prerenders `/` and `/_not-found`); `next dev`
serves `200` with all section headings (including "Price chart") present
in the rendered HTML; with matching-engine + market-data running, a real
trade submitted directly to matching-engine showed up correctly via
`market-data`'s `GET /candles`, confirming the chart's data path end-to-
end (the SVG rendering itself is client-side post-hydration, verified by
code review + `eslint`/`tsc`, not a browser screenshot in this
environment); with all five backend services running, a real cross-
origin `OPTIONS` preflight from `Origin: http://localhost:3100` returns
`204` with correct `Access-Control-Allow-*` headers, and a real cross-
origin `POST /orders/submit` succeeds end-to-end with those headers on
the actual response. `AccountSection` additionally verified: `services/
auth` gained CORS (`withPermissiveCorsForDevelopment`, identical to
oms-gateway's) as part of this change — a real cross-origin `OPTIONS`
preflight against `/auth/register` returns `204` with correct headers,
and a real cross-origin `POST /auth/register` succeeds with those
headers on the response; `next dev` serves `200` with "Account
(services/auth" present in the rendered HTML.

**Known limitations:** no auth, no portfolio/watchlist/MF/SIP views — see
FEATURES.md §11 for the intended scope. Still one page, not a real
multi-route retail app. Bracket Orders and GTT have no UI (not built
anywhere yet — see oms-gateway's cross-cutting notes on why). The chart
polls on a 5s interval rather than subscribing to a WebSocket — same
polling-stopgap caveat as market-data's HTTP query API itself; no
technical-indicator overlays (MACD/RSI/BB/Fib per FEATURES.md §10) and
no zoom/pan.

### `app/optionsChain/`, `app/notificationCenter/`, `app/strategies/` — new this build

FEATURES.md §11 remaining items (`docs/BUILD_LOG.md` entry 59): a real
options-chain page calling oms-gateway's real `GET /options/chain`
(live Greeks, real PCR — verified live, including a clean 502 rather
than a crash when quant-engine was killed mid-session); a real Web
Notifications integration driven by real polling against oms-gateway's
audit trail (order fills) and market-data's existing price-alerts
feature — the data-plumbing proven live end-to-end, though the actual
browser permission-prompt/popup step couldn't be exercised by the build
agent (no interactive browser available) and is documented as such
rather than claimed; a real strategies page wired to oms-gateway's new
`internal/strategyfollowing` for opt-in follow/unfollow (no order
mirroring).

**Known limitations:** margin-call notifications have no real event-
driven backend trigger (oms-gateway's margin state isn't event-driven)
— shipped as an explicitly-labeled best-effort heuristic, not a real
margin call. `apps/web` has no test runner (no Jest/Vitest/Playwright in
`package.json`) — `tsc --noEmit`/`lint`/`build` all clean, live
verification substitutes for automated frontend tests. Still true as
of `docs/BUILD_LOG.md` entry 87 — that pass's `apps/web` fixes were
also verified by `tsc`/`lint`/`build` plus logic review, not an
automated test.

**Correctness fix, `docs/BUILD_LOG.md` entry 87:**
`notificationCenterSection.tsx`'s `recentNotificationLog` (a
prepended, truncated-to-20 `string[]`) was rendered via `key={logIndex}`
— since every entry's index shifts on each new arrival, this was a
textbook stale-key bug (React reusing/misapplying DOM nodes across
reorders). Fixed with a real monotonic `logEntryId` per entry, keyed
on that instead of the array index.

### `app/volumeProfile/`, `app/orderFlowFootprint/`, `app/domReplay/` — new this build

FEATURES.md §20 (`docs/BUILD_LOG.md` entry 69). Three new pages
consuming market-data's real Volume Profile/TPO endpoint, real
order-flow footprint endpoint, and matching-engine's real historical
DOM replay endpoint (port 9106) respectively — a real horizontal
volume-by-price view with POC/Value Area highlighted, a real per-candle
buy/sell-by-price-level grid, and a real playback control stepping
through genuinely replayed depth snapshots. `tsc --noEmit`/`lint`/
`build` all clean; still no automated test runner, same honest note as
the rest of this section.

### `app/support/`, `app/corporateActions/`, `app/referrals/`, `app/screener/`, `app/researchCopilot/`, `app/portfolioHealth/`, `app/taxLossHarvesting/`, `app/quantResearchTools/`, and real i18n — new this build

FEATURES.md §14 + §16 frontend wiring (`docs/BUILD_LOG.md` entry 75).
Eight new pages wiring all 11 of this round's new backend endpoints
(`backoffice` :8084, `oms-gateway` :8081, `quant-engine` :8085). Before
writing any page, every endpoint's real request/response shape was
curl-verified against a running backend rather than assumed — this
caught three real spec/backend mismatches: `GET
/corporate-actions/holdings` requires both `accountId` and
`instrument` query params (a plain-text 400 without `instrument`);
`actionType` is the enum `STOCK_SPLIT`/`BONUS_ISSUE`/`MERGER`/
`CASH_DIVIDEND`, not free text; and `GET /referral-rewards/status`
returns a plain 404 (not a 200 with an error field) for an account
with no referral link.

`app/support/` — ticket list/create/reply. `app/corporateActions/` —
seed a holding, process a corporate action, view the processed log
(honestly notes there's no "upcoming actions" feed anywhere in the
repo — that's the separate `corporateactionexplainer` surface, not
yet linked here). `app/referrals/` — code generation, referral
recording, qualify-check. `app/screener/` — AND/OR filter builder,
results table, saved-screen save/get/list/delete (its `"in"`/`"not_in"`
list-membership operators were selectable but had no way to actually
enter a list until `docs/BUILD_LOG.md` entry 87 added a
comma/newline-separated value input, `parseListMembershipValues`, that
swaps in when one of those operators is selected).
`app/researchCopilot/`
— question box, cited answer, non-advisory disclaimer. `app/portfolioHealth/`
— holdings editor, HHI metrics, severity-colored nudges.
`app/taxLossHarvesting/` — lot editor, eligible/excluded (wash-sale)
lots, offset waterfall. `app/quantResearchTools/` — combines
alternative data (sentiment + filing anomaly), Brinson P&L
attribution, and custom index construction+backtest (with an inline
SVG sparkline) into one page.

**Real i18n**: `app/localization/localizationContext.tsx`
(`LocalizationProvider`/`useLocalization`) fetches `GET
/localization/languages` once and lazily fetches+caches `GET
/localization/{lang}` per language; `languageSwitcher.tsx` is a
dropdown wired into it and into `app/layout.tsx`. Real substitution
proven on the existing dashboard/order-ticket page across 15 real
strings (dashboard title, order-ticket heading/labels/order-type
options/submit-button states, positions heading + empty state, price
chart heading, market-session-admin heading, order-status heading) for
`en`/`hi`/`ta`.

**Verified:** `tsc --noEmit`, `eslint`, and `next build` all clean —
15 total routes. `next dev` booted on :3100 against the still-running
real backends; every new route returned HTTP 200 with no server-log
errors, and the homepage HTML contained the real translated default
string plus the language switcher. Ports 8081/8082/8084/8085/8088
confirmed clear after cleanup.

### `app/watchlist/`, `app/homeScreenLivePnlWidget.tsx` — new this build

FEATURES.md §21 "Cross-device watchlist/alert sync with a home-screen
live P&L widget" (`docs/BUILD_LOG.md` entry 80). `app/watchlist/`:
add/remove symbols against market-data's `WatchlistStore`, a real
per-browser `deviceIdentifier` persisted in `localStorage` (genuinely
different across two browser profiles/incognito sessions), live
polling display, and a delta-sync panel hitting
`GET /watchlist/changes?...&sinceEpochMillis=...`.
`homeScreenLivePnlWidget.tsx` is embedded on the dashboard (`app/page.tsx`),
polling market-data's real `GET /pnl/live` every 7s and honestly
showing a "no live cost basis yet" state rather than a fake number
when oms-gateway hasn't had a price pushed for that instrument yet.
`tsc --noEmit`/`eslint`/`next build` all clean — 16 total routes.

**Stale-response race fixed, `docs/BUILD_LOG.md` entry 87:** a new
shared `app/hooks/useSequencedFetch.ts` hook — a pure incrementing-
sequence-counter (`startNextRequest()`/`isStillMostRecentRequest()`,
not an `AbortController` wrapper) — closes 3 real stale-response races
where a slower, older in-flight poll response could overwrite state a
newer response already set: `homeScreenLivePnlWidget.tsx`'s
`refreshPnl` (a stale account's P&L overwriting the current one),
`page.tsx`'s `PriceChartSection.refreshChartData` (a stale
instrument's chart data), and `watchlist/page.tsx`'s
`refreshFullWatchlist` (a stale account's watchlist). These are the
only 3 sites using the hook; `notificationCenterSection.tsx` and
`screener/page.tsx` were not migrated to it.

### `app/portfolioGreeks/`, `app/ivRank/`, `app/developerApi/` — new this build

FEATURES.md §22 (Portfolio Greeks aggregation, IV Rank/Percentile) and
§18 item 8 (developer API-key management) frontend wiring
(`docs/BUILD_LOG.md` entry 83). Three new pages, following the exact
same conventions as every other page in this app (inlined `fetch`,
module-scope `const xBaseUrl = process.env.NEXT_PUBLIC_X_BASE_URL ??
"http://localhost:PORT"`, hand-rolled Tailwind, no component kit).

`app/portfolioGreeks/` calls quant-engine's real `POST /portfolio/greeks`
(`_handlePortfolioGreeksRequest` in `httpServer.py`, backed by
`portfolioGreeksAggregator.py`) — an add/remove-row position editor
(identifier + quantity + the four per-contract Greeks) and a results
panel showing the real quantity-weighted net Greeks
(`netDelta`/`netGamma`/`netVegaPerOnePercentVolatilityChange`/
`netThetaPerCalendarDay`/`positionCount`) after submit. Submitting zero
positions is allowed and legitimately returns an all-zero result (not a
422), per the handler's own doc comment.

`app/ivRank/` calls quant-engine's real `POST /volatility/iv-rank`
(`_handleIvRankRequest`, backed by `ivRankCalculator.py`) — a current-IV
field plus a comma-or-newline-separated textarea for the historical
series (parsed client-side to `number[]`; this series is an
illustrative/fixture input the caller supplies, quant-engine does no
historical IV ingestion of its own), showing the real IV Rank/Percentile
and historical min/max after submit. The documented 422-on-`ValueError`
case (a degenerate series with zero range) is surfaced distinctly from a
network-failure error, showing quant-engine's real error message.

`app/developerApi/` calls **api-gateway** (a service `apps/web` had no
existing base-URL env var for) — new `NEXT_PUBLIC_API_GATEWAY_BASE_URL`
(default `http://localhost:8089`, matching api-gateway's own
`envOrDefault("API_GATEWAY_LISTEN_ADDRESS", ":8089")`), added following
the exact same pattern as every other `NEXT_PUBLIC_*_BASE_URL` in this
app. Issues developer API keys (`POST /developer/api-keys` — rate-limit
tier selector, sandbox checkbox), lists an account's keys (`GET
/developer/api-keys?accountIdentifier=`, defaulting to `acct-001` per
this app's existing convention), and revokes them (`POST
/developer/api-keys/revoke`) against api-gateway's real
`internal/apikeymanager`. The newly-issued raw key value is shown
prominently once, in the familiar "copy this now" API-key UX pattern —
**explicitly labeled on-page and in the source as a UI-only
convention**: `apiKeyManager.go` keeps the full raw value retrievable
via `GET /developer/api-keys` indefinitely (already flagged as a TODO in
that file's own doc comment — a real build stores only a hash), so this
page does not misrepresent server-side one-time-display as a real
guarantee.

**A real gap found and documented, not fixed (out of this task's
frontend-only scope):** `services/api-gateway/cmd/server/main.go` has no
CORS middleware anywhere — confirmed by grepping the whole service tree
for `Access-Control`/`Cors`/`CORS` (zero matches), unlike oms-gateway's
`withPermissiveCorsForDevelopment` or quant-engine's permissive
`Access-Control-Allow-Origin: *` header (both already real and
documented elsewhere in this file). Verified live: a real `OPTIONS
/developer/api-keys` preflight against the running api-gateway returned
`405 Method Not Allowed`, and a real cross-origin `GET
/developer/api-keys?accountIdentifier=...` with an `Origin` header set
came back with no `Access-Control-Allow-Origin` header at all. A real
browser running this page against a real api-gateway on a different
origin (the normal `apps/web` dev setup) will therefore have every
fetch on this page blocked by the browser's own CORS policy, even
though every endpoint is fully reachable and correct via curl/
server-to-server calls — see this page's own file-header comment and
`apps/web/README.md` for the same note.

**Verified:** `npx tsc --noEmit`, `npm run lint`, and `npm run build`
all clean — all three new routes (`/portfolioGreeks`, `/ivRank`,
`/developerApi`) appear as static-prerendered routes. Real live
verification against actually-running backends: started quant-engine
(`.venv/bin/python -m quantengine.httpServer`, :8085) and api-gateway
(`go run ./cmd/server`, :8089) for real, `curl`'d the exact
endpoints/payloads each page sends and confirmed the responses match
what each page parses (including the 422 degenerate-IV-series case and
the 404 revoke-unknown-key case), then started `next dev` on :3100
against those two real backends and confirmed all three new routes
return `200` with the real page heading present in the rendered HTML,
plus the three new homepage nav links (`href="/portfolioGreeks"`,
`href="/ivRank"`, `href="/developerApi"`) present in the rendered
homepage HTML. All backend/dev-server processes were stopped afterward.
**Not verified:** an actual browser click-through of `app/developerApi/`
against api-gateway (see the CORS gap above — this would demonstrably
fail in a real browser); `oms-gateway`/`ledger`/`market-data`/
`matching-engine` were not started this round, since none of the three
new pages call them.

### JWT wiring — new this build (`docs/BUILD_LOG.md` entry 84)

`app/session/authSession.ts` is a new, small, deliberately-NOT-a-fetch-
client shared module (`saveSession`/`loadSession`/`clearSession`) that
persists `{accountIdentifier, accessToken, refreshToken,
expiresInSeconds}` to `localStorage` under one key
(`mercuriusAuthSession`) — mirroring `app/watchlist/page.tsx`'s
existing device-identifier localStorage convention (same `typeof
window === "undefined"` SSR guard, same long-descriptive-camelCase key
naming). `AccountSection` (`app/page.tsx`) now calls `saveSession`
after a real login and rehydrates from `loadSession()` on mount, so a
page refresh no longer silently drops the logged-in state, and
`clearSession` on logout. Every fetch across `apps/web` that hits
oms-gateway, backoffice, kyc-onboarding, or api-gateway (the four
services gated this build) now attaches `Authorization: Bearer
<token>` when a session exists — added per-call-site, matching this
app's existing no-shared-fetch-client convention exactly (this repo's
one deliberate exception is the shared localStorage accessor itself,
which is not a fetch abstraction). Touched: `app/page.tsx`,
`app/corporateActions/page.tsx`, `app/developerApi/page.tsx`,
`app/referrals/page.tsx`, `app/strategies/page.tsx`,
`app/support/page.tsx`, `app/optionsChain/page.tsx`, and
`app/notificationCenter/notificationCenterSection.tsx`. Fetches hitting
market-data, quant-engine, ledger, mutual-funds, reporting, or
`services/auth` itself were deliberately left untouched — none of
those five were gated by this pass — confirmed by checking every
`NEXT_PUBLIC_*_BASE_URL` file for whether it belongs to one of the four
in-scope services (the one exception found, `app/localization/
localizationContext.tsx`'s calls to backoffice's `/localization/*`,
correctly needed no header — those two routes were deliberately left
public on the backend, see backoffice's section above). Logged-out
handling: gated actions check for a session before firing and show an
inline "log in first" message (matching each page's existing
error-message UI style) rather than firing an unauthenticated request
and surfacing a raw 401 — pages still render fully without a session.

**Verified**: `npx tsc --noEmit`, `npm run lint`, and `npm run build`
all clean (independently re-run, not just self-reported) — 19 page
routes present in the build output (this was a wiring-only change, no
new pages added).

**Known limitation:** no refresh-token auto-renewal wired into the
frontend — once the 15-minute access token stored in `localStorage`
expires, gated fetches start failing with the page's generic
"log in first"/401-handling path rather than transparently calling
`POST /auth/refresh` and retrying; a real build would want a fetch
wrapper (or the shared abstraction this pass deliberately avoided
introducing) to do that silently.

## apps/terminal (Tauri v2 + React 19 + TypeScript) — FEATURES.md §10, new this build

Scaffolded for real via `create-tauri-app` and built out into a real
Pro Desktop Terminal:

- **Tiling workspace**: the `golden-layout` npm package drives a real
  tiling workspace; React roots are mounted into its component-factory
  DOM nodes. Layouts persist to `localStorage` per user
  (`src/workspace/TilingWorkspace.tsx`, `workspaceLayoutPersistence.ts`
  — a Tauri fs-plugin-backed file was considered and explicitly not
  used, documented in that file).
- **Command bar**: `src/commandBar/commandBarParser.ts` implements a
  real grammar parser for Bloomberg-terminal-style input like
  `AAPL DES <GO>`. `GP`/`DOM`/`NEWS` verbs actually open new tiles;
  `DES`/`MOD`/`BLOTTER` parse correctly but are honestly not yet wired
  to a tile type.
- **Candlestick chart**: `CandlestickChartCanvas.tsx` is a hand-rolled
  Canvas 2D renderer (no WebGL library dependency) plotting real
  MACD/RSI/Bollinger Bands/Fibonacci retracement math from
  `src/indicators/*.ts`, each formula unit-tested against hand-derived
  exact values.
- **DOM ladder**: polls matching-engine's `GET /domReplay` (:9106) and
  submits real LIMIT orders to oms-gateway's `POST /orders/submit`
  (:8081) using the same field shapes as `apps/web`'s order ticket.
- **Multi-monitor detachment**: real Tauri v2 `WebviewWindow` API
  (`@tauri-apps/api/window`). Config-building logic is unit-tested;
  actual OS window placement is honestly documented as unverifiable in
  this headless sandbox (no display server).
- **Python hook sandbox**: `src-tauri/src/pythonHookSandbox.rs` is a
  real Tauri command spawning an isolated Python subprocess with real
  `setrlimit(RLIMIT_CPU, RLIMIT_AS)` caps, a wall-clock watchdog, and
  macOS `sandbox-exec` network denial. Proven by integration tests
  (`src-tauri/tests/pythonHookSandboxIntegrationTest.rs`) that spawn
  real Python subprocesses and confirm the kernel actually kills a
  CPU-bound busy loop via `SIGXCPU`. Honest limitation: the
  `RLIMIT_AS` memory cap is a real syscall but not reliably enforced
  by macOS's kernel — CPU-time capping is the verified primary
  defense, and `sandbox-exec` is Apple-deprecated and macOS-only.
- **News ticker**: a CSS-animated scrolling widget over explicitly
  illustrative fixture data (reusing quant-engine's toy sentiment
  lexicon) — not a live feed, documented as such.

Verified: 64 Vitest tests (parser, indicator math, layout persistence,
window-detach config) + 6 Rust tests (2 unit + 4 integration), all
passing. `cargo fmt --check` / `cargo clippy --all-targets -D
warnings` / `cargo build` clean; `npm install` from a clean
`node_modules` + `npm run build` (tsc + vite) clean.

**Four correctness fixes, `docs/BUILD_LOG.md` entry 87 — now 73 Vitest
tests (up from 64), plus the same 6 Rust tests unchanged:**
`src-tauri/src/lib.rs`'s `.run(tauri::generate_context!()).expect(...)`
was a bare panic on any Tauri startup failure (missing WebView2/
webkit2gtk); now matches on the `Result`, prints an actionable message,
and calls `std::process::exit(1)` instead of panicking. New
`src/shared/minorUnitsPriceFormatting.ts` (`formatMinorUnitsAsDisplayPrice`)
fixes `src/domLadder/DomLadderWidget.tsx`, which had been rendering
raw minor-units integers directly in both the price-ladder cells and
the order-acknowledgement toast. `CandlestickChartCanvas.tsx` gained
an exported `shouldRenderFibonacciOverlay` guard, checked before
`calculateFibonacciRetracementLevels` (which throws on a flat/inverted
`swingHigh <= swingLow` window) — a flat price range now shows an
on-canvas note instead of crashing the chart. Two poll races —
`ChartTileContainer.tsx`'s `refreshChartData` and
`DomLadderWidget.tsx`'s `refreshLadder` — each gained a
`latestRequestSequenceNumberRef`, so a slower/older in-flight response
can no longer overwrite state a newer response already set (on top of
the pre-existing unmount guard). **Honest limitation on the poll-race
fixes specifically:** this app still has no component-rendering test
infrastructure (no `@testing-library/react` or equivalent — all 9
`*.test.ts` files are pure-logic unit tests, none mount a component
tree), so these two fixes were verified by logic trace plus a clean
`tsc`/`vite build`, not by a test that actually races two responses
against a mounted component.

## infra/docker — local dev infra, now resource-capped

`infra/docker/docker-compose.yml`: Postgres 16, Redpanda, and
ClickHouse — provisioned for ARCHITECTURE.md §6/§7's persistence layer,
but (see the cross-cutting section below) **not yet connected to any
application service** — every service in this repo still runs fully
in-memory. This build added resource caps only, to stop the compose
stack from being able to exhaust the host machine — a real problem
that happened once already, not a hypothetical.

| Cap | Postgres | Redpanda | ClickHouse | Applies to all three |
|---|---|---|---|---|
| `mem_limit` | 1g | 1g | 2g (highest — ClickHouse caches aggressively and is otherwise happy to use all available RAM) | — |
| `mem_reservation` | 256m | 512m | 512m | — |
| `cpus` | 1.0 | 1.0 | 2.0 | — |
| `pids_limit` | 512 (shared `&pid-cap` YAML anchor) | 512 | 1024 | — |
| log rotation | — | — | — | `json-file` driver, `max-size: 10m`, `max-file: 3` |

**Known limitation, documented in the file itself:** these are plain
Compose v2 keys (`mem_limit`/`cpus`/`pids_limit`), no `deploy:`/Swarm
stanza needed — they apply directly to `docker compose up`. They do
NOT cap the *data-volume* disk growth under `./volumes/*` — a portable
per-container disk quota needs `storage_opt: {size: ...}`, which only
works on specific storage-driver/filesystem combinations most Docker
Desktop installs don't have. The real disk backstop on macOS is
Docker Desktop's own VM disk-image-size setting (Settings → Resources
→ Disk usage limit) — outside this repo's reach, a one-time manual
setting.

Validated with `docker compose config` (parses clean, all three
services' caps present) — not validated against a live `docker
compose up` in this build environment (see the cross-cutting section's
long-standing Docker-unavailability note).

### `Makefile` guardrails around the compose stack

`infra-up`/`infra-down`/`infra-logs` already existed; this build added:

| Target | Purpose |
|---|---|
| `infra-up` | Unchanged behavior, but now prints a warning first if `docker system df` already reports ≥20GB in use (`DISK_WARN_THRESHOLD_GB`), so growth gets flagged before adding more, not after. Warns, never blocks. |
| `infra-down-clean` | New. `docker compose down -v` — also removes the bind-mounted `infra/docker/volumes/{postgres,redpanda,clickhouse}` data, which plain `infra-down` leaves on disk indefinitely. |
| `infra-status` | New. `docker stats --no-stream` scoped to just this compose project's containers — lets actual usage be checked against the `mem_limit`/`cpus` caps above. |
| `infra-disk` | New. `docker system df -v` plus `du -sh` on the bind-mounted volume directories specifically. |

**Known limitation:** the `infra-up` disk-usage warning parses `docker
system df`'s human-readable size column with a `sed`/`cut` one-liner —
brittle to a Docker CLI output-format change across versions, not a
robust parser. Good enough for a dev-loop warning, not something to
depend on for anything automated.

## CI

`.github/workflows/ci.yml` — one job per service. See
`infra/ci/README.md` for the full breakdown and what's not yet covered
(still no integration/e2e job in CI itself — the oms-gateway ↔
matching-engine flow below has only been verified manually, not as an
automated CI step yet).

---

## Cross-cutting: what "skeleton" means everywhere in this repo

Every service above builds, runs, and passes its own tests — that part is
real. What's consistently *not* real yet, across all of them:

- **Six real inter-service hand-offs now exist**, all verified live with
  every binary actually running (not mocked): `oms-gateway` →
  `matching-engine` (order submission, TCP+JSON), `matching-engine` →
  `market-data` (depth publish, TCP+JSON, fire-and-forget), `oms-gateway`
  → `ledger` (balance fetch + settlement post, HTTP+JSON), `oms-gateway`
  → `kyc-onboarding` (eligibility gate, HTTP+JSON), `oms-gateway` →
  `backoffice` (freeze gate, HTTP+JSON). A full order — KYC gate → freeze
  gate → risk check → match → settle → local cache update → next order
  sees the updated state — closes correctly across all five backend
  services (`docs/BUILD_LOG.md` entries 14–16), including both new gates'
  reject/allow transitions and their fail-open behavior when the
  dependency they check is down. What's still missing: any of this
  running over the real lock-free ring buffer / SBE binary encoding
  ARCHITECTURE.md specifies (everything above is TCP+JSON or HTTP+JSON —
  proves the boundary, is not the real design), all of these checks
  happening asynchronously off the request path instead of inline, and
  Postgres/Kafka actually backing any of this instead of in-memory state.
- **All four order types now exist end-to-end** (Limit, Market,
  StopLossLimit, StopLossMarket — FEATURES.md §3), verified live including
  a same-call stop-order trigger cascade through all five backend
  services (`docs/BUILD_LOG.md` entry 18). Both market and stop-market
  orders share one known, deliberately-unfixed gap: `oms-gateway`'s
  pre-trade risk check can't estimate their notional (no last-price feed
  wired into that service yet), so it always computes 0 and trivially
  passes for them — flagged loudly in `cmd/server/main.go`, not fixed.
- **Order cancellation now exists end-to-end** (`docs/BUILD_LOG.md` entry
  19): every order gets a matching-engine-local id at submission time, a
  resting `Limit` remainder or a still-armed stop order can be pulled off
  the book by that id via `POST /orders/cancel`, verified live for both
  cases (including confirming a cancelled stop order does not resurrect
  when its trigger price is later crossed). Known gap: the cancel
  endpoint doesn't verify the caller owns the order — no auth anywhere in
  this skeleton yet, same category as every other gap here.
- **Idempotency keys on order submission — now including CONCURRENT
  duplicates** (`docs/BUILD_LOG.md` entries 20, 30): a client-supplied
  `idempotencyKey` makes a retried `POST /orders/submit` return the exact
  same response instead of being processed a second time, checked before
  every gate. Originally only guarded sequential retries; entry 30
  replaced the plain cache with a claim/complete protocol
  (`ClaimKeyOrAwaitExistingResponse`/`CompleteClaimedKey`, a
  `chan struct{}` closed exactly once per key to wake every waiter) so a
  concurrent duplicate — two requests sharing a key arriving at nearly
  the same instant — genuinely collapses to one execution instead of a
  race where both could slip past the old check. Bounded by a 30s claim
  timeout (configurable via `NewIdempotencyStoreWithClaimTimeout` for
  tests) so an owner that never completes can't hang waiters forever.
  Verified live with two genuinely concurrent `curl` requests sharing one
  key: both returned the identical `assignedGlobalSequenceNumber`, order
  id 2 never existed (`orderStatus: NOT_FOUND`), and the audit trail
  showed exactly one `ORDER_SUBMITTED` entry. `go test -race` clean.
  Known gaps: still in-memory/no TTL, and AMOs remain explicitly outside
  idempotency's coverage (see the handler's own comment on why).
- **Cover Orders (CO) are real** (`docs/BUILD_LOG.md` entry 21): entry +
  protective stop-loss leg, the leg placed automatically only once the
  entry actually fills. Verified live including the full cascade — a
  triggering trade's response showed both the trade that triggered it AND
  the protective leg's own fill in the same round-trip, position
  returning cleanly to net zero. Bracket Orders (BO, with a second
  target/take-profit leg needing one-cancels-other logic) still NOT built:
  order-status queries now exist (entry 22, below) but push fill
  notifications don't — BO would need to either poll both legs' status
  after every unrelated order (wasteful and racy) or matching-engine
  gaining a push/subscribe mechanism, neither built yet.
- **Order status queries are real** (`docs/BUILD_LOG.md` entry 22):
  `GET /orders/status` reports whether a given order id is still resting
  (with live remaining quantity), still armed as a pending stop, or gone
  — verified live for all three states plus the cancelled→NOT_FOUND
  transition. Read-only, no side effects, not gated.
- **After Market Orders (AMO) are real** (`docs/BUILD_LOG.md` entry 23):
  an order submitted while `internal/marketsession` reports the market
  closed gets queued (`internal/amoqueue`) instead of processed, and
  genuinely drains through the real pipeline only when
  `POST /market-session/open` is called. Verified live: confirmed the
  position did NOT change while queued, confirmed the queue count via
  status, then confirmed the drain's response carried a real, fresh
  matching-engine order id and that the order was genuinely resting
  (via `GET /orders/status`) before finally crossing it. Market session
  is an explicit admin toggle, not a real trading calendar — documented,
  not fixed. GTT and Bracket Orders remain the two unbuilt items in
  FEATURES.md §3's CO/BO/GTT/AMO line — both need matching-engine push
  notifications this build doesn't have.
- **`apps/web` now actually reaches oms-gateway's real surface**
  (`docs/BUILD_LOG.md` entry 24): the dashboard covers order submission
  (all four order types), Cover Orders, After Market Orders, market
  session admin, positions, and status/cancel lookups — everything built
  in entries 17–23, none of which the old single-form page knew about.
  Along the way, found and fixed a genuine bug (not a "known gap"): oms-
  gateway had zero CORS headers, so no browser page on a different
  origin/port could have called it at all. Verified live with a real
  cross-origin preflight + POST against all five running backend
  services, headers present on both.
- **Audit trail is real** (`docs/BUILD_LOG.md` entry 25): every
  consequential decision in `oms-gateway` — submissions, rejections
  (KYC/freeze/risk distinguished), fills, cancellations, cover-order
  protective-leg outcomes, AMO queueing, market session toggles — is
  logged to an append-only `internal/audittrail.AuditTrail`, exposed via
  `GET /audit-trail`. Verified live: confirmed the exact sequence of
  entries for a rejection, a resting order, a cancel, an incidentally-
  discovered risk rejection, and a real two-account fill (with correct
  quantity/price/counterparties in the detail message); confirmed the
  `?accountId=` filter scopes correctly. In-memory only — a restart
  loses the whole trail, disqualifying for anything actually regulated,
  same category of gap as everything else not yet Postgres-backed.
- **Real trade-tick publishing + OHLCV candles** (`docs/BUILD_LOG.md`
  entry 26): matching-engine now publishes what actually PRINTED, not
  just book depth; market-data folds every tick into a 60-second-bucket
  candle per instrument (`src/candleAggregator.rs`) and serves both the
  raw trade tape and the candle series over a small hand-rolled HTTP
  query API (`GET /trades`, `GET /candles` on `127.0.0.1:9103`, CORS-
  enabled for `apps/web`). Verified live: a real cross between two
  orders showed up correctly in both endpoints with correct OHLC and
  volume. Known gaps: fixed 60s candle width, ingestion-time (not true
  execution-time) timestamps, everything in-memory only, and the HTTP
  API is a polling stopgap for the real WebSocket streaming
  ARCHITECTURE.md §5 calls for.
- **quant-engine is now a real HTTP service** (`docs/BUILD_LOG.md` entry
  27): stdlib-only `GET /health`/`POST /options/price`/
  `POST /options/implied-volatility` on `127.0.0.1:8085`, wrapping the
  existing Black-Scholes pricer/Greeks/IV-solver module. Verified live
  against the same reference values the unit tests check. Still
  single-contract only (no portfolio aggregation) and explicitly
  research-tier, not for the latency-sensitive arbitrage-scanner path.
- **New `services/auth`** (`docs/BUILD_LOG.md` entry 31): real password
  register/login (PBKDF2-HMAC-SHA256, stdlib only), real HS256 JWT
  access tokens (hand-rolled, stdlib only), and real refresh-token
  rotation WITH reuse detection (a stolen/replayed refresh token burns
  its entire session family, not just itself) — verified live end-to-end
  including the reuse-detection path. 30 tests, `go test -race` clean.
  **apps/web now has a real `AccountSection`** (`docs/BUILD_LOG.md` entry
  32, needed adding CORS to `services/auth` first) that can register/
  login/logout against it for real, live-verified including a real
  cross-origin request — but the resulting JWT/account identifier isn't
  wired into anything that actually GATES access yet: oms-gateway still
  accepts every request unauthenticated, and mints a completely separate
  account-identifier space from oms-gateway's/ledger's seeded demo
  accounts — a real build needs these to converge. **Login/register are
  now rate-limited** (`internal/ratelimiter`, `docs/BUILD_LOG.md` entry
  33) — a sliding-window limiter (tested against exactly the fixed-
  window double-burst failure mode), 5 login attempts/minute per
  account, 3 registrations/minute per source address, both verified live
  end-to-end (401×5 then 429; 201×2 then 429×2). **Real MFA (RFC 6238
  TOTP)** (`internal/totp`, `internal/mfastate`, `docs/BUILD_LOG.md`
  entry 35): hand-rolled, cross-checked against RFC 6238's own published
  test vectors AND against an independently-written Python
  implementation computing the same code from the same secret live —
  `/auth/mfa/enroll`/`confirm-enrollment`/`disable`, and `/auth/login`
  now genuinely requires a valid TOTP code once an account has enrolled.
  17 tests. Known gaps: in-memory (a restart locks out every enrolled
  user), no backup codes, `disable` only needs a valid access token (no
  re-auth), no `apps/web` UI for any of this yet (curl-verified only).
- **Structured (JSON) logging across all four Go services**
  (`docs/BUILD_LOG.md` entry 28): a duplicated `internal/httplogging`
  package (`ledger`, `kyc-onboarding`, `backoffice`, `oms-gateway` — no
  shared Go module exists yet) provides `WithRequestLogging`, wired
  around each service's top-level handler, logging one JSON
  `"http_request"` line per request. `slog.SetDefault(...)` in each
  `main()` also transparently upgrades every PRE-EXISTING `log.Printf`
  business-event line to structured JSON, for free — a documented
  `log/slog` behavior, verified live (e.g. `ledger`'s "journal entry
  rejected" line came out as JSON without being touched). 12 new tests
  (3 per package copy). Known gap: still stdout-only, not shipped to a
  real centralized log aggregation backend (Loki/ELK) — that's the other
  half of FEATURES.md §13's item, out of reach without Docker.
- **Real latency histograms** (`oms-gateway`'s `internal/metrics`,
  `docs/BUILD_LOG.md` entry 36): `GET /metrics` serves genuine
  Prometheus text exposition format (a real Prometheus could scrape it
  today) — per-route histograms via a `WithRequestTiming` middleware, no
  client-library dependency. 14 tests, verified live with real
  bucket/sum/count against actual traffic through the running service.
  Known gap: only oms-gateway has this — matching-engine, the service
  this FEATURES.md item's "execution path especially" most directly
  means, has no HTTP listener to expose metrics on yet.
- **Full pre-order charges breakdown** (`oms-gateway`'s
  `internal/chargescalculator`, `docs/BUILD_LOG.md` entry 38): real
  brokerage/STT/exchange-charge/SEBI-fee/stamp-duty/GST/DP-charge
  computation via `POST /orders/estimate-charges`, with real
  buy/sell/delivery/intraday rate asymmetries (e.g. STT both-sides on
  delivery vs. sell-side-only intraday). 8 tests including a fully
  hand-worked example. Verified live matching the hand-worked test
  exactly. Every rate is explicitly flagged as an illustrative model,
  not fetched from any live regulatory source.
- **Risk profiling questionnaire → investor risk category**
  (`kyc-onboarding`'s `internal/riskprofiling`, `docs/BUILD_LOG.md`
  entry 39): a real 6-question scored questionnaire classifying into 5
  risk categories, feeding a NOT-built Robo-Advisory feature. 11 tests
  sweeping every category band. Verified live: conservative answers →
  CONSERVATIVE, aggressive answers → AGGRESSIVE, invalid input rejected.
  Feeds nothing downstream yet and gates nothing.
- **Plain-language cancel-rejection reasons, a real gap closed**
  (`oms-gateway`, `docs/BUILD_LOG.md` entry 41): an independent
  verification pass over every oms-gateway rejection path confirmed
  KYC/freeze/risk rejections were genuinely plain-language already, but
  found `POST /orders/cancel`'s "order not found" case had a good
  plain-language string that only ever reached the audit trail, never
  the client-facing response — fixed, verified live. The transport-
  failure cancel path also now returns a genuinely plain-language
  message instead of a raw wrapped Go error.
- **Withdrawal workflow with T+N settlement holds** (`ledger`'s
  `internal/withdrawalworkflow`, `docs/BUILD_LOG.md` entry 42): real
  holds that reduce available balance without touching the raw ledger
  balance, holds that correctly stack against each other (no double-
  spend), and a real payout sweep that posts an actual balanced journal
  entry through the shared `doubleentry` core once a hold's settlement
  period elapses — money genuinely leaves the account, not a status
  flip. 13 tests, verified live end-to-end including the full request →
  reject-when-over-limit → process-due → cancel round trip against a
  real running service. No real payment rail behind the payout (same
  category of gap as bank-verification's penny-drop); the sweep is
  externally triggered, not on a real schedule.
- **Bank account verification — penny-drop / micro-deposit**
  (`kyc-onboarding`'s `internal/bankverification`, `docs/BUILD_LOG.md`
  entry 34): real random micro-deposit amount, real 3-attempt limit with
  permanent lockout (not a reset), verified live including the lockout
  path and confirming a locked verification rejects even the correct
  amount. No real payment rail — a debug-only endpoint stands in for
  "check your bank statement", loudly documented for deletion once a
  real banking API is wired in. Not yet gating anything in oms-gateway.
- **Watchlists + real price alerts** (`market-data`'s `src/watchlist.rs`
  / `src/pricealerts.rs`, `docs/BUILD_LOG.md` entry 37): per-account
  watchlists, and price alerts evaluated against the SAME live trade-
  tick stream that feeds the candle aggregator — an alert fires the
  instant a real trade crosses its threshold, not on a polling
  interval. Verified live through the actual running matching-engine +
  market-data processes: a trade below threshold left an alert
  untriggered, a trade crossing it fired the alert for real. 15 new
  tests. No technical (moving-average/RSI) triggers, no push
  notification, no `apps/web` UI yet.
- **Client fund segregation** (`ledger`'s `internal/fundsegregation`,
  `docs/BUILD_LOG.md` entry 45): CLIENT/FIRM account classification plus
  one real, checkable invariant — a custody-pool account's balance must
  always equal the sum of every CLIENT account's balance. A dedicated
  ring-fenced deposit/payout path and client-to-client transfer path
  keep the invariant true by construction; a live compliance-facing
  report and a dry-run entry validator make it checkable and
  pre-checkable respectively. 13 tests, verified live including a real,
  correctly-computed discrepancy surfaced when money was deliberately
  moved through the pre-existing, unmigrated `/journal-entries` path —
  proving the check catches genuine violations, not just always passing.
  Only the new `/client-funds/*` endpoints are ring-fenced; trade
  settlement and withdrawals still bypass the guard.
- **AML transaction monitoring** (`ledger`'s `internal/amlmonitoring`,
  `docs/BUILD_LOG.md` entry 46): real rule-based detection —
  large-transaction, velocity, and structuring rules — plus a static
  PEP name screen. `/client-funds/deposit` and `/withdrawals/request`
  report every real transaction to it; `/aml/alerts` is the compliance
  review queue. 15 tests, verified live including structuring firing on
  exactly the transaction that crosses the threshold, not before, and
  velocity firing on real repeated withdrawal requests against a running
  service. Trade settlement (`/journal-entries`) isn't monitored yet;
  thresholds are illustrative, not real regulatory limits; no
  case-management lifecycle for a raised alert.
- **§2/§3/§4/§6/§7/§8/§9 parallel build** (`docs/BUILD_LOG.md` entries
  47–52): a simulated UPI/NEFT/IMPS deposit rail and eNACH-style SIP
  payment mandates in `ledger`; a margin pledge system, SPAN/exposure
  margin calculator, and Iceberg/FOK/IOC order-type acceptance in
  `oms-gateway`; a brand-new `mutual-funds` service (AMC routing,
  SIP/lumpsum, step-up SIPs); Sharpe/Sortino/max-drawdown, an arbitrage
  scanner, a deterministic backtest runner, and a pairs-trading strategy
  in `quant-engine`; a real WebSocket L1 quote feed with a
  sequence-numbered snapshot/resync protocol in `market-data`; and a
  real fsync'd write-ahead log with deterministic crash-replay in
  `matching-engine`. All six built in parallel by independent agents
  scoped to non-overlapping service directories, each with real tests
  (182 new tests total across the six) and real live verification
  against actually-running processes — see each service's own section
  above and `docs/BUILD_LOG.md` for the full verification transcripts.
  Every lane documents its own honest gaps (illustrative
  rates/thresholds, no real bank/AMC/exchange network calls, several
  cross-service wiring gaps like mutual-funds not yet debiting `ledger`)
  rather than papering over them.
- **§2/§3/§4/§6/§7/§8/§9 completion round** (`docs/BUILD_LOG.md` entries
  53–58): every item remaining unchecked in these seven sections after
  the round above was built, finishing them completely per explicit user
  instruction. A multi-currency wallet in `ledger`; margin funding, a
  synthetic-but-real-Greeks options chain, an illustrative (explicitly
  NOT FIX-certified) DMA/FIX-style institutional gateway, paper trading
  sharing the live OMS code path, and per-strategy circuit breakers in
  `oms-gateway`; rebalancing baskets, robo-advisory (genuinely calling
  quant-engine's live Sharpe/Sortino endpoint), and goal-based investing
  in `mutual-funds`; GARCH, a correlation matrix engine, VaR +
  stress-testing, a volatility surface builder, a strategy promotion
  pipeline, a market-making sandbox, and an explicitly-toy sentiment
  hook that structurally cannot place a real order, all in `quant-engine`;
  a standalone deterministic simulated exchange feed, a columnar tick
  store, and real UDP multicast in `market-data`; and a genuinely
  lock-free SPSC ring buffer replacing the matching-engine's ingress/
  egress synchronization, with every one of its `unsafe` blocks carrying
  a written soundness argument. Six more independent agents, six more
  non-overlapping service directories, 400+ further tests, real live
  verification throughout (including two real concurrent-load passes
  against the matching-engine and a real UDP multicast receive). Two
  more real bugs caught and fixed along the way (a ledger debit/credit
  sign error found via live verification, a float-epsilon VaR percentile
  bug). Every item that fundamentally cannot be "complete" without
  infrastructure this environment doesn't have — a certified FIX/
  exchange connection, real co-located multicast consumers, real
  historical-return covariance data for genuine Efficient Frontier
  optimization, a real filings/NLP data feed — says so explicitly in its
  own section rather than being quietly faked.
- **§11/§12/§13/§15/§17/§18 completion round** (`docs/BUILD_LOG.md`
  entries 59–65): a real options-chain UI and notification/strategy-
  following plumbing in `apps/web`; a real mark-to-market engine,
  auto-liquidation, exposure limits, and a self-healing connectivity
  kill-switch in `oms-gateway` (§12, all 4 items); every one of §15's 8
  execution/derivatives items — TWAP/VWAP/POV execution algos, an exact
  options payoff-diagram calculator, atomic multi-leg options execution,
  basket orders, a walk-the-book impact-cost estimator, portfolio
  cross-margining, a securities lending/borrowing desk, and extended-
  hours session rules; DRIP and Loan Against Securities from §17 (with
  fractional share investing explicitly and honestly deferred as too
  structurally invasive for this pass, not silently skipped); a brand-
  new `api-gateway` service covering all 13 remaining §13+§18 items —
  SLO alerting, a secrets-provider abstraction, a real ledger backup/
  restore with a live-proven drill, a DR runbook, real chaos/load
  testing, tiered rate limiting, a public developer API with keys,
  webhooks, white-label tenancy, a FIX conformance suite, TCA, and an
  illustrative account aggregator; and real ESG scoring/screening in
  `quant-engine`. Four initial parallel lanes plus two follow-up passes
  to work through oms-gateway's long tail. Three of those lanes edited
  `oms-gateway/cmd/server/main.go` concurrently at one point — the
  orchestrating session independently re-verified (not just trusted the
  agents' reports) that all changes coexist correctly with zero lost
  work before proceeding (`docs/BUILD_LOG.md` entry 65). Three more real
  bugs caught and fixed along the way (a kill-switch self-healing gap,
  an inverted iron-condor leg-direction check, plus the earlier round's
  bugs). Every item genuinely deferred (fractional shares) or
  fundamentally bounded by missing real-world infrastructure (real
  cloud IAM, a real DR region, real FIX certification, a real Account
  Aggregator network) says so explicitly rather than being faked.
- **§5/§17/§19/§20/§21/§22 completion round** (`docs/BUILD_LOG.md`
  entries 66–71): fixed income (real auction allotment + a genuine
  iterative YTM solver) and the last four §17 P4 items in
  `mutual-funds`; fractional share investing (finally built, having
  been deferred twice) plus overtrading detection, F&O cooling-off, and
  eight §21 customer-pain-point items in `oms-gateway`; all 16 §22 deep-
  quant items in `quant-engine` — including a genuine Engle-Granger/ADF
  cointegration test and a real hand-rolled Hidden Markov Model with
  forward-backward/Viterbi inference, not a threshold rule in
  disguise; Volume Profile, order-flow footprint, and historical DOM
  replay across `market-data`/`matching-engine`/`apps/web`; and
  anomalous-login detection, a copy-trading leaderboard, family account
  access, and nominee succession across `auth`/`backoffice`. Five
  lanes, each isolated to non-overlapping services, ~600 new tests, real
  live verification throughout. Four more real bugs caught and fixed (a
  coupon-calendar rounding bug, an HMM log-space underflow, plus two
  caught during design before they ever ran: an inconsistent anomaly-
  detection keying scheme and a wallet-aliasing inconsistency from an
  earlier round). The two items explicitly NOT covered this round —
  one-click capital gains statement export and cross-device watchlist/
  P&L sync — are left unmarked in FEATURES.md rather than claimed.
- **§10/§14/§16 completion round** (`docs/BUILD_LOG.md` entries 72–76):
  a real Tauri v2 + React Pro Desktop Terminal scaffolded from scratch
  in `apps/terminal` (tiling workspace, command bar, Canvas candlestick
  chart with real MACD/RSI/BB/Fib math, DOM ladder, multi-monitor
  detachment, a real OS-resource-capped Python hook sandbox, news
  ticker — all 7 §10 items); the remaining four §14 items (support
  ticketing, real corporate-actions cost-basis processing, referral/
  rewards, an English/Hindi/Tamil localization catalog) across
  `backoffice`/`oms-gateway`; all 7 §16 items (screener, a real TF-IDF
  RAG research copilot, portfolio health/HHI check, tax-loss
  harvesting with a real wash-sale window, alternative-data sentiment/
  filing-anomaly feeds wired into §7's NLP hook, Brinson P&L
  attribution, custom index construction+backtest) in `quant-engine`;
  and a final frontend pass wiring all 11 new backend endpoints into 8
  new `apps/web` pages plus a real i18n language switcher. Four lanes,
  each isolated to non-overlapping directories, ~750 new
  tests/assertions across TS/Rust/Go/Python, real live verification
  throughout (including three real endpoint-shape mismatches the
  frontend pass caught by curling before coding, rather than assuming).
- **§1/§21 closeout round** (`docs/BUILD_LOG.md` entries 77–81) — every
  remaining unchecked item anywhere in FEATURES.md sections 1-22: real
  nominee designation + joint-holding registration in `kyc-onboarding`;
  real heuristic trade surveillance (spoofing/layering/wash-trade
  detection + replay) and a real text-based conversational order
  parser (explicit-confirmation-gated, voice honestly out of scope) in
  `oms-gateway`; a brand-new `services/reporting` (port :8090) doing
  real contract notes, ledger statements, FIFO STCG/LTCG tax P&L
  against a hand-worked example, mock-AIS reconciliation, and one-click
  CSV capital-gains export, all computed from live oms-gateway/ledger
  data; and real cross-device watchlist delta-sync plus a live P&L
  widget across `market-data`/`apps/web`. Four lanes, each isolated to
  non-overlapping directories, ~130 new tests, real live verification
  throughout. Three more real bugs caught and fixed or honestly worked
  around (an audit-trail cancellation-scoping blind spot in
  surveillance, a millisecond-resolution watchlist-sync race, and an
  oms-gateway audit-trail maker-side filtering gap worked around from
  the read-only reporting service). The ~250-item FEATURES.md backlog
  is now fully marked 🚧 end to end.
- **Corrected as of `docs/BUILD_LOG.md` entry 85 — no longer true that
  "no service is backed by the real infrastructure in
  `infra/docker/docker-compose.yml`."** Postgres is now genuinely wired
  in (Docker WAS available in the entry-85 build environment — entry
  16's earlier "Docker unavailable" finding was specific to that
  session, not a permanent constraint). Real, Postgres-backed (verified
  live, including a real kill-and-restart survival test — see each
  service's own section above and `docs/BUILD_LOG.md` entry 85 for
  exact evidence): `ledger`'s `internal/doubleentry` (via
  `internal/pgstore.PostgresLedgerBook`); `oms-gateway`'s
  `internal/audittrail`, `internal/positions` (the REAL position book
  only — see below), and `internal/idempotency`'s completed-response
  cache; `market-data`'s `watchlist.rs`/`pricealerts.rs`. Still fully
  in-memory, unchanged, deliberately out of scope for this pass:
  everything else in `oms-gateway`'s ~40 other internal packages
  (`paperPositionBook`, `milliSharePaperPositionBook`, risk engine,
  margin/pledge/funding, execution algos, strategy following,
  corporate actions, and more), `matching-engine` (its WAL is already
  durable — a different, already-real persistence mechanism, explicitly
  out of scope for this pass), `market-data`'s `candleAggregator.rs`/
  `columnarTickStore.rs` (deliberate hot-path performance tradeoff),
  `quant-engine`, `mutual-funds`, `reporting`, `backoffice`,
  `kyc-onboarding`, `api-gateway`, `auth`. Redpanda and ClickHouse
  remain completely unconnected to anything — this pass was Postgres-
  only, per its own explicit scope. The compose file itself also got
  real, live-validated attention (resource caps, `Makefile` guardrails)
  in an earlier entry — see this doc's `infra/docker` section and
  `docs/BUILD_LOG.md` entry 82 — and, as of entry 85, a real,
  documented, machine-specific host-port remap (`5432`→`5433`) worked
  around a pre-existing system Postgres conflict on the one dev machine
  this pass was built on.
- **Real auth/RBAC now exists for five services** (`docs/BUILD_LOG.md`
  entry 84, corrected from this line's prior "no auth anywhere" —
  that was accurate through entry 83 and is not anymore).
  `services/auth` issues real HS256 JWTs carrying a `role` claim
  (`retail`/`admin`/`support`/`compliance`); `oms-gateway`,
  `backoffice`, `kyc-onboarding`, and `api-gateway` each gate their
  routes on a valid bearer token via a small, duplicated
  `internal/authmiddleware` package (one copy per service — a real
  maintenance burden, not a shared Go module; see each service's own
  section above and `docs/BUILD_LOG.md` entry 84 for the tradeoff),
  enforce "you can only act on your own account" on every route
  carrying an account identifier, and gate operationally-privileged
  routes on specific roles. `kyc-onboarding`'s `/kyc/submit` and
  `backoffice`'s `/accounts/freeze` — called out by name in this line
  before this build — are no longer reachable unauthenticated
  (`/kyc/submit` requires the caller to own the account it submits
  for; `/accounts/freeze` requires `RoleAdmin`). What's still real and
  unresolved: `services/auth`'s own remaining endpoints (e.g.
  `/auth/security/login-alerts`) are unauthenticated by the auth
  service's own section above; `ledger`, `matching-engine`,
  `market-data`, `mutual-funds`, `reporting`, `quant-engine`, and
  `apps/terminal` were explicitly out of scope for this pass and remain
  fully open; the HS256 signing secret is a shared, hardcoded-dev-
  default env var with no rotation story; `RequireRole` does an exact
  single-role match with no hierarchy (a `RoleAdmin` token cannot reach
  a `RoleCompliance`-gated route, e.g. `kyc-onboarding`'s review
  queue — see that service's section); and no real user has ever been
  granted a non-`RoleRetail` role through any provisioning flow (only
  direct code can mint one).
  **TLS**: still none anywhere — every service listens plain HTTP,
  and every inter-service call in this build (oms-gateway→
  matching-engine, oms-gateway→ledger, etc.) is unencrypted TCP/HTTP.
  **Rate limiting**: NOT uniformly absent as this line previously
  implied — `api-gateway` has real tiered token-bucket rate limiting
  (`internal/ratelimiter`, retail/institutional tiers keyed off issued
  API keys) predating this build, and `services/auth` has real
  sliding-window rate limiting on `/auth/login` (5/minute per email)
  and `/auth/register` (3/minute per address) predating this build too
  — but oms-gateway, backoffice, kyc-onboarding, and matching-engine
  still have none.

This is the expected state for the "repo scaffold" build phase. See
`FEATURES.md` §0 for the phasing plan this scaffold is set up to support.
