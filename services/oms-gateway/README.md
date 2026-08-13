# oms-gateway

Tier 1 component — see `ARCHITECTURE.md` §4 in the repo root.

## Status: the risk-check path, and all four downstream hand-offs, are real

What's real:
- HTTP order submission endpoint (`/orders/submit`)
- In-memory pre-trade risk check (`internal/riskengine`) — sub-millisecond,
  no synchronous DB round-trip, tested
- Plain-language + machine-readable rejection reasons on every reject,
  implementing the FEATURES.md §21 differentiator from day one
- Global sequence number assignment (`internal/sequencing`), atomic,
  contention-free
- **A real network hand-off to `matching-engine`**
  (`internal/matchingengineclient`) over TCP+JSON — a risk-approved order
  genuinely reaches the order book and any resulting fills come back in
  the HTTP response's `tradeExecutionEvents`.
- **Real balance sync and settlement with `ledger`** (`internal/
  ledgerclient`): balances are fetched from the ledger at startup instead
  of hardcoded, and every fill posts a real balanced journal entry back to
  the ledger, then updates the local risk cache immediately.
- **Real KYC gating with `kyc-onboarding`** (`internal/kycclient`): an
  account that hasn't submitted valid KYC details is rejected with
  `KYC_NOT_VERIFIED` before its margin is ever evaluated.
- **Real freeze enforcement with `backoffice`** (`internal/
  backofficeclient`): a frozen account is rejected with `ACCOUNT_FROZEN`
  and the recorded reason.
- **Market orders** — `OrderIsMarketOrderNotLimit` passes through to
  matching-engine. **Known gap**: the risk check can't estimate a market
  order's notional yet (no last-price feed in oms-gateway), so it always
  computes 0 and trivially passes — see the loud comment above the risk
  check in `cmd/server/main.go`.
- **Positions tracking** (`internal/positions`, FEATURES.md §3): net
  signed quantity per account/instrument, updated on every fill.
  `GET /positions?accountId=...`.
- **Stop-loss orders (SL/SL-M)** — `OrderIsStopLossVariant` +
  `StopTriggerPriceInMinorUnits` pass through to matching-engine, where
  the trigger logic actually lives (see that service's README). Completes
  FEATURES.md §3's "Order entry: Market, Limit, SL, SL-M" — all four
  types are now real end-to-end. **Same known gap** as market orders:
  `StopLossMarket`'s notional is always 0 in the risk check, and even
  `StopLossLimit`'s is only an estimate of the fill-if-triggered price.
- **Order cancellation** — `POST /orders/cancel`, pass-through to
  matching-engine's `cancelOrder`. Not risk/KYC/freeze-gated (cancelling
  can only reduce exposure). Every failure path now returns a genuine
  plain-language `errorMessage` on the response itself (FEATURES.md §21)
  — fixed a real gap where the "order not found" case had a good
  plain-language string that only ever reached the audit trail, never
  the actual client-facing response; verified live with a cancel against
  a never-existent order id. **Known gap**: doesn't verify the caller
  actually owns the order being cancelled — no auth anywhere in this
  skeleton yet.
- **Idempotency keys on order submission, including CONCURRENT duplicates**
  (`internal/idempotency`, FEATURES.md §2): pass `idempotencyKey` in the
  submit request and a retry with the same key returns the exact same
  response instead of being re-processed — whether that retry arrives
  after the original finished (a sequential retry) OR at nearly the same
  instant (a concurrent duplicate, e.g. a client's own timeout-triggered
  auto-retry firing while the original request is still in flight). The
  first request to see an unclaimed key becomes its "owner" and does the
  real work; every other request sharing that key blocks (bounded at 30s)
  until the owner completes, then gets back the identical response —
  proven live with two genuinely concurrent `curl` requests sharing one
  key: both came back with the SAME `assignedGlobalSequenceNumber`, order
  id 2 never existed, and the audit trail shows exactly one
  `ORDER_SUBMITTED` event, not two. Checked before every gate (KYC/
  freeze/risk), so a replay never re-touches any of them. **Known gaps**:
  in-memory only, no TTL (a key is claimed forever); After Market Orders
  are explicitly NOT covered (queuing isn't a final answer yet — see the
  package/handler comments); if the owner's request handler crashes
  without completing the claim, waiters block for the full 30s timeout
  before getting a synthetic rejection, rather than failing fast.
- **Cover Orders (CO)** — `POST /orders/cover-submit`. Entry order +
  mandatory protective stop-loss leg, placed automatically once (and
  only once) the entry actually fills, for exactly the filled quantity.
  Simpler than a full Bracket Order (no target leg, so no
  one-cancels-other race to manage). See matching-engine's README for
  why BO isn't built yet. **Known gap**: idempotency keys don't extend
  to this endpoint; a failed protective-leg placement leaves the client
  with an open, unprotected position and only an error string to notice
  it by (no retry/alerting).
- **Order status queries** — `GET /orders/status?instrumentSymbol=...&
  matchingEngineOrderSequenceNumber=...`, pass-through to
  matching-engine's `queryOrderStatus`. Read-only, no gating.
- **After Market Orders (AMO)** — an order submitted with
  `orderIsAfterMarketOrder: true` while the market is closed
  (`internal/marketsession`) is queued (`internal/amoqueue`) instead of
  processed. `POST /market-session/open` drains the whole queue through
  the real pipeline; `POST /market-session/close` and
  `GET /market-session/status` round it out. **Known gap**: the market
  session is an explicit admin toggle, not a real clock-driven trading
  calendar; the queue is in-memory only (an oms-gateway restart between
  close and open silently loses every queued AMO).
- **Audit trail** (`internal/audittrail`) — an append-only, immutable
  (no update/delete method exists at all) log of every consequential
  decision: submissions, rejections (KYC/freeze/risk, distinguished),
  fills, cancellations, cover-order protective-leg outcomes, AMO
  queueing, and market session toggles. `GET /audit-trail` (optionally
  `?accountId=...`). **Known gap**: in-memory only — a restart loses the
  entire trail, disqualifying for anything actually regulated.
- **Structured (JSON) logging** (`internal/httplogging`): every HTTP
  request logs a machine-parseable `http_request` line (wrapped inside
  the CORS middleware, so it sees every request that actually reaches
  application logic); `slog.SetDefault` also upgrades every pre-existing
  `log.Printf` line (settlement failures, fail-open warnings, balance
  syncs, etc.) to structured JSON, for free. Stdout only — not yet
  shipped to a real log aggregation backend.
- **Real latency histograms** (`internal/metrics`, FEATURES.md §13):
  `GET /metrics` serves genuine Prometheus text exposition format — a
  real Prometheus server could scrape this today, no adapter needed.
  Every request is timed per (method, path) into its own histogram
  (`internal/metrics/middleware.go`'s `WithRequestTiming`, layered
  alongside the request-logging middleware). Verified live: real
  histograms for `/health` and `/orders/submit` with correct bucket
  counts, sum, and count against actual traffic through the running
  service. **Known gap**: only oms-gateway has this — matching-engine
  (the actual hottest path this FEATURES.md item calls out) has no
  metrics yet, since it has no HTTP listener to expose them on.
- **Full pre-order charges breakdown** (`internal/chargescalculator`,
  FEATURES.md §21): `POST /orders/estimate-charges` — brokerage, STT,
  exchange transaction charges, SEBI turnover fees, stamp duty, GST, and
  DP charges, computed as a receipt-style breakdown BEFORE order
  confirmation, not discovered after the fact. Delivery vs. intraday and
  buy vs. sell each have real, different rate rules (e.g. STT is both-
  sides on delivery but sell-side-only on intraday; stamp duty is buy-
  side-only; DP charges only apply to a delivery sell). Read-only, not
  gated by KYC/freeze/risk/idempotency — purely a quote. 8 tests
  including a fully hand-worked example cross-checked line-by-line
  against the package's own rate constants. Verified live: a real
  ₹100.00-delivery-buy request returned exactly the same 119-paise total
  the hand-worked unit test predicts. **Known, loudly documented gap**:
  every rate is an ILLUSTRATIVE model based on common discount-broker
  rate cards, hardcoded as of this build — not fetched from any live
  regulatory/exchange source, and real STT/stamp-duty rates change by
  government notification on no predictable schedule. Treat every number
  as "illustrative, not authoritative."
- Both new gates share a deliberate, documented tradeoff: an *explicit*
  ineligible/frozen answer fails **closed** (order rejected); a
  *transport* failure (that service is unreachable) fails **open** (logs
  a warning, order proceeds) — see the KYC gate's comment in
  `cmd/server/main.go` for the reasoning and the explicit caveat that a
  real production system would likely want to fail closed instead.
- **Verified end-to-end with all five services running** (`docs/
  BUILD_LOG.md` entries 14–16): the full gate order — KYC → freeze → risk
  → match → settle — holds together, including both gates' reject/allow
  transitions and the fail-open behavior when a dependency is down.

What's a placeholder:
- The matching-engine hand-off is synchronous TCP+JSON, one connection
  per order — not the lock-free ring buffer / SBE binary encoding
  ARCHITECTURE.md §3.1/§3.5 describes.
- Settlement, KYC check, and freeze check are all posted/fetched
  synchronously, inline in the same request handler that risk-checked and
  routed the order — ARCHITECTURE.md §4 says all of this belongs off the
  hot path.
- Balance sync from the ledger is a one-shot fetch at startup for a fixed
  demo account list, not a continuous subscription.
- No FIX or WebSocket session handling
- No per-client-session backpressure/throttling (ARCHITECTURE.md §4)
- Sequencing is one global counter; needs to become per-shard once the
  matching engine shards by instrument (ARCHITECTURE.md §3.2)

## Run it

```bash
# terminal 1
cd ../ledger && go run ./cmd/server

# terminal 2
cd ../matching-engine && cargo run

# terminal 3
cd ../kyc-onboarding && go run ./cmd/server

# terminal 4
cd ../backoffice && go run ./cmd/server

# terminal 5 — fund + KYC-verify a demo account before starting oms-gateway
curl -X POST localhost:8082/journal-entries -d '{
  "humanReadableDescription": "demo funding",
  "debitLines": [{"ledgerAccountIdentifier":"acct-001","amountInMinorUnits":1000000}],
  "creditLines": [{"ledgerAccountIdentifier":"firm-clearing-acct","amountInMinorUnits":1000000}]
}'
curl -X POST localhost:8083/kyc/submit -d '{
  "accountIdentifier": "acct-001",
  "panNumber": "ABCDE1234F",
  "fullName": "Jane Trader"
}'

# terminal 5 (continued)
go run ./cmd/server
# MATCHING_ENGINE_TCP_ADDRESS, LEDGER_BASE_URL, KYC_ONBOARDING_BASE_URL,
# and BACKOFFICE_BASE_URL are all overridable; defaults point at the
# ports above (9101, 8082, 8083, 8084 respectively)

# terminal 6
curl -X POST localhost:8081/orders/submit -d '{
  "clientAccountIdentifier": "acct-001",
  "instrumentSymbol": "DEMO-EQ",
  "orderSideIsBuyNotSell": true,
  "limitPriceInMinorUnits": 10000,
  "orderQuantity": 5
}'

# a stop-loss-market sell, arming at trigger price 9000 — sits pending
# until a trade prints at or below 9000, then converts to a live market
# sell
curl -X POST localhost:8081/orders/submit -d '{
  "clientAccountIdentifier": "acct-001",
  "instrumentSymbol": "DEMO-EQ",
  "orderSideIsBuyNotSell": false,
  "orderIsStopLossVariant": true,
  "orderIsMarketOrderNotLimit": true,
  "stopTriggerPriceInMinorUnits": 9000,
  "limitPriceInMinorUnits": 0,
  "orderQuantity": 5
}'
# -> note "matchingEngineOrderSequenceNumber" in the response

# cancel it (or any resting order) by that id
curl -X POST localhost:8081/orders/cancel -d '{
  "instrumentSymbol": "DEMO-EQ",
  "matchingEngineOrderSequenceNumber": 1
}'

# idempotent retry: submit with an idempotencyKey, then submit the exact
# same request again with the same key — the second call returns the
# SAME assignedGlobalSequenceNumber, proving it wasn't processed twice
curl -X POST localhost:8081/orders/submit -d '{
  "clientAccountIdentifier": "acct-001",
  "instrumentSymbol": "DEMO-EQ",
  "orderSideIsBuyNotSell": true,
  "limitPriceInMinorUnits": 10000,
  "orderQuantity": 5,
  "idempotencyKey": "some-client-generated-uuid"
}'

# a Cover Order: entry + protective stop-loss leg placed automatically
# once the entry fills
curl -X POST localhost:8081/orders/cover-submit -d '{
  "clientAccountIdentifier": "acct-001",
  "instrumentSymbol": "DEMO-EQ",
  "orderSideIsBuyNotSell": true,
  "limitPriceInMinorUnits": 100,
  "orderQuantity": 5,
  "stopLossTriggerPriceInMinorUnits": 90
}'

# check on an order by the matchingEngineOrderSequenceNumber any of the
# above responses returned
curl "localhost:8081/orders/status?instrumentSymbol=DEMO-EQ&matchingEngineOrderSequenceNumber=1"

# After Market Order: with the market closed, this gets QUEUED, not
# processed — POST /market-session/open later drains it for real
curl -X POST localhost:8081/market-session/close
curl -X POST localhost:8081/orders/submit -d '{
  "clientAccountIdentifier": "acct-001",
  "instrumentSymbol": "DEMO-EQ",
  "orderSideIsBuyNotSell": true,
  "limitPriceInMinorUnits": 100,
  "orderQuantity": 5,
  "orderIsAfterMarketOrder": true
}'
curl "localhost:8081/market-session/status"
curl -X POST localhost:8081/market-session/open

# audit trail — every consequential decision made above, in order
curl "localhost:8081/audit-trail"
curl "localhost:8081/audit-trail?accountId=acct-001"

go test ./...
```
