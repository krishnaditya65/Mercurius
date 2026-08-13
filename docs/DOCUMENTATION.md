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

### `src/watchlist.rs` and `src/pricealerts.rs` — new this build

FEATURES.md §9: "Watchlists, alerts (price/technical triggers)". Only
plain price-threshold alerts — no technical (moving average/RSI/etc.)
triggers.

| Item | Purpose |
|---|---|
| `WatchlistStore` | `addSymbol`/`removeSymbol` (both idempotent — a repeat add/remove is a no-op, not an error) / `symbolsForAccount` (sorted, deterministic) — a per-account `HashSet<String>` of instrument symbols. |
| `PriceAlertStore` | `createAlert(accountId, symbol, isAboveNotBelow, thresholdPrice) -> alertId`; `checkAndTriggerAlertsForTrade(symbol, price, now)` — called once per real trade tick from `main.rs`'s ingestion loop, checks every NOT-yet-triggered alert for that instrument and marks any that now qualify, returning the newly-triggered ids; `alertsForAccount`. |

**Tested behavior:** watchlist — 7 tests (empty by default, add/remove,
idempotent double-add, per-account independence, sorted output).
pricealerts — 8 tests (starts untriggered; above/below threshold
crossing logic; an alert only checks its own instrument; once triggered
it's never re-evaluated even by a later qualifying trade; multiple
alerts on one instrument trigger independently; per-account filtering).

**Verified live, through the real running matching-engine + market-data
processes** (`docs/BUILD_LOG.md` entry 37): created a real alert (fire
at price ≥ 100), submitted a real trade at 80 (alert correctly stayed
untriggered), then a real trade at 120 (alert fired, `triggeredAtEpochSeconds`
populated, and market-data's own stdout logged the trigger) — not a unit
test, the actual live trigger path.

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
execution time. Everything (trade tape, candles, book state, watchlists,
alerts) is in-memory only. The HTTP query API is a polling stopgap, not
real WebSocket streaming. Watchlists/alerts have no auth, no technical
triggers, no push notification (poll `GET /alerts` to discover a fired
one), and a fired alert never re-arms.

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
| `NewIdempotencyStore()` | Constructs an empty store. |
| `PreviousResponseForKey(key) (OrderAcknowledgementResponse, bool)` | Looks up the cached response for a client-supplied idempotency key. An empty key always misses — treated as "client opted out," never as a real key. |
| `RecordResponseForKey(key, response)` | Caches whatever response this submission produced (accepted OR any rejection) under the key, so a retry gets the exact same answer. No-op for an empty key. |

**Tested behavior:** no hit before anything's recorded; same key returns
the recorded response; different keys don't collide; an empty key is
never cached or looked up.

**Known limitations:** only guards against SEQUENTIAL retries — two
requests carrying the same key arriving CONCURRENTLY aren't blocked from
both reaching risk-check/matching-engine before either finishes recording
a response; a real build needs a "reservation" (claim-then-release) state
to close that race. Also unbounded, unexpiring, in-memory only — no TTL,
doesn't survive a restart.

### `internal/marketsession/marketSessionState.go` — FEATURES.md §3 AMO

| Function | Purpose |
|---|---|
| `NewMarketSessionState()` | Constructs a session that starts CLOSED. |
| `IsMarketOpen() bool` / `SetMarketOpen(bool)` | RWMutex-guarded boolean — cheap concurrent reads (every order submission checks this), infrequent writes (an admin flips it). |

**Known limitation:** a plain admin-toggled flag, not a real clock-driven
trading calendar (pre-open/continuous/closing-auction/holidays).

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

### `internal/audittrail/auditTrail.go` — FEATURES.md "Audit trail: immutable log of every order, modification, cancellation"

| Item | Purpose |
|---|---|
| `EventType` | Closed set of string constants (`ORDER_SUBMITTED`, `ORDER_REJECTED`, `ORDER_MATCHING_ENGINE_FAILURE`, `ORDER_FILLED`, `ORDER_CANCELLED`, `ORDER_CANCEL_FAILED`, `COVER_PROTECTIVE_LEG_PLACED`, `COVER_PROTECTIVE_LEG_FAILED`, `AFTER_MARKET_ORDER_QUEUED`, `MARKET_SESSION_OPENED`, `MARKET_SESSION_CLOSED`) — not free text, so a compliance query can filter reliably. |
| `Entry` | One immutable record: `RecordedAtTime` (stamped by `Append`, never caller-supplied), `EventType`, `ClientAccountIdentifier`, `InstrumentSymbol`, `MatchingEngineOrderSequenceNumber`, `DetailMessage`. |
| `NewAuditTrail()` | Constructs an empty trail. |
| `Append(entry)` | Stamps `RecordedAtTime` with `time.Now()` and appends. No corresponding update/remove method exists ANYWHERE on this type — that's the actual immutability guarantee, not just a naming convention. |
| `AllEntries() []Entry` | Returns every entry, oldest first, as a COPY (mutating the returned slice doesn't touch the trail's internal state — tested explicitly). |
| `EntriesForAccount(id) []Entry` | Same, filtered to one account; entries with no account (e.g. market-session events) never match. |

**Tested behavior:** starts empty; `Append` stamps a real timestamp and
preserves every field; `AllEntries` preserves append order and returns a
genuine copy; `EntriesForAccount` filters correctly and returns empty for
an unknown account.

**Known limitation:** in-memory only — an oms-gateway restart loses the
entire audit trail, which would be disqualifying for anything actually
regulated. A real build needs an append-only/WORM-backed store that
survives a restart and can't be tampered with even by this service's own
operators.

**Wired into `cmd/server/main.go`** at every consequential decision
point: `processOrderSubmission` logs `ORDER_SUBMITTED`/`ORDER_REJECTED`
(KYC, freeze, and risk rejections all distinguished by `DetailMessage`)/
`ORDER_MATCHING_ENGINE_FAILURE`/`ORDER_FILLED`; the cancel handler logs
`ORDER_CANCELLED`/`ORDER_CANCEL_FAILED`; the cover-order handler logs
`COVER_PROTECTIVE_LEG_PLACED`/`COVER_PROTECTIVE_LEG_FAILED`; AMO queueing
logs `AFTER_MARKET_ORDER_QUEUED`; the market-session handlers log
`MARKET_SESSION_OPENED`/`MARKET_SESSION_CLOSED`. `orderSubmissionDependencies`
carries the `auditTrail` reference so all three callers of
`processOrderSubmission` (plain submission, cover-order entry legs, AMO
drain) log through the exact same code path.

### `internal/positions/positionBook.go` — FEATURES.md §3 "positions / holdings views"

| Function | Purpose |
|---|---|
| `NewPositionBook()` | Constructs an empty book. |
| `ApplyFill(buyerId, sellerId, instrument, qty)` | Increments the buyer's, decrements the seller's net signed quantity for that instrument. |
| `PositionsForAccount(accountId) map[string]int64` | Returns a copy of the account's non-zero positions; net-zero instruments are omitted rather than returned as an explicit 0. |

**Tested behavior** (5 tests): buy/sell increment/decrement correctly;
multiple fills accumulate; a position that nets back to zero is omitted;
positions are tracked independently per instrument; an unknown account
has none.

**Known limitations:** net quantity only — no average cost basis, no
realized/unrealized P&L. In-memory, not persisted; a restart loses every
position (no WAL/event-sourcing to replay from yet).

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

| Route / Function | Behavior |
|---|---|
| `GET /health` | Liveness check. |
| `POST /orders/submit` | Decodes `OrderSubmissionRequest` → **idempotency check** (a hit short-circuits everything below, returning the cached response) → **AMO check** (if `OrderIsAfterMarketOrder` and the market is closed, queues into `afterMarketOrderQueue` and returns `IsQueuedAsAfterMarketOrder:true` WITHOUT touching idempotency-caching, KYC, freeze, or risk) → delegates to `processOrderSubmission` (see below) → caches the result under `IdempotencyKey` and responds. |
| `processOrderSubmission(deps, request) OrderAcknowledgementResponse` | The actual pipeline, extracted out of the HTTP handler so `buildCoverOrderHandler` can reuse it for an entry leg: **KYC gate** → **freeze gate** → computes notional (`price × qty`; always 0 for a market/stop-market order — known gap, see file comment) → risk check → on approve, allocates a sequence number, calls `matchingEngineClient.SubmitOrderAndAwaitMatchResult`, captures `AssignedOrderSequenceNumber` into the response's `MatchingEngineOrderSequenceNumber`, and on a real fill calls `settleTradeAgainstLedgerAndLocalCache` and `positionBook.ApplyFill` for each trade. Takes an `orderSubmissionDependencies` struct instead of a long parameter list. |
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
| `settleTradeAgainstLedgerAndLocalCache` | For one fill: posts the settlement journal entry via `ledgerClient`, then — only on success — applies the same adjustment to the local risk cache via `ApplyTradeSettlementToLocalCache`. A failed settlement post is logged loudly (`SETTLEMENT FAILED`) since there's no reconciliation/retry job yet to catch it silently going missing. |

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

---

## services/ledger (Go) — Tier 2, double-entry core is real

### `internal/doubleentry/doubleEntryLedgerCore.go`

| Item | Purpose |
|---|---|
| `LedgerAccountLine` | One side (debit or credit) of an entry: account id + amount. |
| `JournalEntry` | An atomic, must-balance set of `DebitLines` and `CreditLines` plus a description. |
| `ErrJournalEntryDoesNotBalance` / `ErrUnknownLedgerAccount` | Sentinel errors, checkable via `errors.Is`. |
| `NewInMemoryDoubleEntryLedgerBookWithAccounts(accountIds []string)` | Constructs the book with a fixed set of zero-balance accounts. |
| `PostJournalEntry(entry) error` | **The core invariant**: rejects the entry outright (no partial application) if debit-sum ≠ credit-sum, or if any referenced account doesn't exist. Otherwise, mutex-guarded, applies every line atomically (debit increases the named account's balance, credit decreases it — one uniform convention in this skeleton, not yet real chart-of-accounts semantics) and appends to the post-order history. |
| `CurrentBalanceInMinorUnits(accountId) (int64, error)` | Mutex-guarded balance lookup. |

**Tested behavior:** a balanced entry posts and both accounts update
correctly; an unbalanced entry is rejected and *neither* account is
touched; an entry referencing an unknown account is rejected.

**Known limitations:** in-memory only (no PostgreSQL persistence yet), no
client-fund segregation at the account-structure level (FEATURES.md §1),
no chart-of-accounts semantics.

### `cmd/server/main.go`

| Route | Behavior |
|---|---|
| `GET /health` | Liveness check. |
| `GET /accounts/balance?accountId=...` | Returns the current balance for an account, 404 if unknown. |
| `POST /journal-entries` | **New this build.** Decodes `PostJournalEntryWireRequest` (own wire type, deliberately decoupled from `doubleentry.JournalEntry` which has no JSON tags), converts to the internal domain type, calls `PostJournalEntry`. Returns `422 Unprocessable Entity` with `errorMessage` set on rejection (unbalanced entry or unknown account), `200` with `wasJournalEntryPosted:true` on success. This is what `oms-gateway` posts trade settlements to — see its `internal/ledgerclient`. |

Seed accounts are `acct-001`, `acct-002`, `firm-clearing-acct` —
deliberately matching `oms-gateway`'s demo accounts so the two services
exercise together without extra setup. **Verified end-to-end**
(`docs/BUILD_LOG.md` entry 15): funded via `POST /journal-entries`,
balances moved correctly after a real trade routed through all three of
ledger/matching-engine/oms-gateway.

---

## services/kyc-onboarding (Go) — PAN-format-check AND bank penny-drop verification are both real

### `internal/kycstate/kycVerificationStateMachine.go`

| Item | Purpose |
|---|---|
| `KycVerificationStage` | `NOT_SUBMITTED` / `VERIFIED` / `REJECTED`. |
| `SubmitKycDetails(accountId, panNumber, fullName) KycRecord` | Validates full name is non-empty and PAN matches `^[A-Z]{5}[0-9]{4}[A-Z]$`; marks `VERIFIED` or `REJECTED` with a reason, stores the record. No async review step — see doc comment. |
| `LookupKycStatus(accountId) KycRecord` | Returns the stored record, or a `NOT_SUBMITTED` placeholder if the account never submitted. |
| `KycRecord.IsEligibleToPlaceOrders() bool` | `true` iff stage is `VERIFIED`. |

**Tested behavior** (5 tests): valid PAN → verified + eligible; malformed
PAN → rejected with a reason; missing name → rejected; unknown account
lookup → `NOT_SUBMITTED`; lookup after submit round-trips the stored data.

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
or manual review queue. In-memory only. No auth on any endpoint. Bank
verification isn't wired into oms-gateway's order-gating the way KYC is
— nothing currently requires a verified bank account for anything. No
real payment rail (see above). The risk-profile category feeds nothing
downstream yet (no Robo-Advisory feature exists) and gates nothing
(e.g. F&O eligibility) either.

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
| `IssueAccessToken(accountId, secret, lifetime, issuedAt) -> string` | Builds and signs a token; claims are `sub`/`iat`/`exp` (mirroring standard JWT claim names, unlike this repo's usual long-name convention, since JWT interop depends on the exact key names). |
| `ParseAndVerifyAccessToken(token, secret, now) -> (AccessTokenClaims, error)` | Constant-time signature check, then expiry check. Returns `ErrTokenSignatureInvalid`, `ErrTokenExpired`, or `ErrTokenMalformed` as distinct sentinel errors. |

**Tested behavior** (7 tests): valid token round-trips its subject;
expired token rejected (including exactly-at-expiry-instant); wrong
secret rejected; a forged claims segment swapped onto a legitimately-
signed header/signature is rejected (signature no longer matches);
malformed tokens (empty, no dots, wrong segment count) rejected without
panicking.

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
| `RegisterAccount(email, password) -> (accountId, error)` | Case-insensitive/whitespace-trimmed email matching; `ErrEmailAlreadyRegistered` on a duplicate. |
| `AuthenticateWithPassword(email, password) -> (accountId, error)` | Returns the SAME `ErrInvalidCredentials` whether the email doesn't exist or the password is wrong — an unknown-email branch still runs a dummy password verification for rough timing parity, to avoid a distinguishable-response account-enumeration side channel. |

**Tested behavior** (6 tests): register+authenticate round-trip; wrong
password / unknown email both rejected identically; duplicate
registration rejected; case-insensitive/whitespace-trimmed email
matching; two accounts get distinct identifiers.

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
identical to oms-gateway's middleware of the same name — so a browser
can call `/auth/*` directly.

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

**Known limitations:** login/register/logout are now reachable from
`apps/web`'s `AccountSection` (entry 32), but the resulting JWT/account
identifier are NOT yet wired into anything that actually gates access —
oms-gateway still accepts every request unauthenticated, and the order
ticket's account field is a free-text input, not derived from the logged-
in session. Mints its own `acct-<random hex>` identifier space,
disconnected from oms-gateway's/ledger's seeded demo accounts (documented
TODO — a real build needs one canonical account identifier). No MFA, no
phone auth, no email verification, no password-strength policy, no rate
limiting on login/register. In-memory only. HS256 means every verifying
service needs the shared secret (a real build would prefer RS256/ES256).

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

---

## apps/web (Next.js) — one page, now covering most of oms-gateway's surface plus a live price chart

Scaffolded with `create-next-app` (TypeScript, Tailwind, ESLint, App
Router). Real dependency tree, not hand-rolled.

| File | Purpose |
|---|---|
| `app/page.tsx` — `RetailTradingDashboardPage` | Top-level page, composes the six sections below. |
| `app/page.tsx` — `AccountSection` | Register/login/logout against the NEW `services/auth` (`NEXT_PUBLIC_AUTH_BASE_URL`, default `127.0.0.1:8086`) — real JWT access token + refresh token displayed on login, logout calls `/auth/logout`. Deliberately NOT wired into `OrderTicketSection`'s account field: auth mints its own `acct-<random hex>` identifier space, disconnected from oms-gateway's/ledger's seeded demo accounts — see `services/auth/README.md`. |
| `app/page.tsx` — `OrderTicketSection` | Submits to `/orders/submit` or (if the Cover Order toggle is on) `/orders/cover-submit`. Order-type dropdown selects Limit/Market/SL/SL-M, mapping to the right `orderIsMarketOrderNotLimit`/`orderIsStopLossVariant`/`stopTriggerPriceInMinorUnits` combination. Idempotency key auto-generated via `crypto.randomUUID()` (editable, regeneratable) so a real double-click exercises the idempotency guard rather than defeating it. AMO checkbox and Cover Order toggle are mutually exclusive in the UI (an AMO can't also be a cover order in this skeleton). |
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

## apps/terminal — not yet scaffolded

Deliberately deferred; see the service's own `README.md` for the
reasoning (avoids building shared UI components twice before `apps/web`
has any to share) and the scaffold command to run when it's picked up
(`FEATURES.md` §0 Phase 2).

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
- No service is backed by the real infrastructure in
  `infra/docker/docker-compose.yml` (Postgres, Redpanda, ClickHouse) —
  they all run fully in-memory today. (Not for lack of trying this round:
  Docker isn't available in this build environment, so Postgres
  persistence work was deliberately skipped rather than shipped
  unverified — see `docs/BUILD_LOG.md` entry 16.)
- No auth, no TLS, no rate limiting anywhere. The two new admin-ish
  endpoints (`kyc-onboarding`'s `/kyc/submit`, `backoffice`'s
  `/accounts/freeze`) are especially exposed — anyone reaching them can
  act on any account.

This is the expected state for the "repo scaffold" build phase. See
`FEATURES.md` §0 for the phasing plan this scaffold is set up to support.
