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
- **Margin Pledge system** (`internal/marginpledge`, FEATURES.md §3):
  `POST /margin-pledge/pledge` pledges quantity of an existing holding
  (looked up server-side from `internal/positions`, not client-asserted)
  as collateral — a real, mutex-guarded state machine, not a stub. A
  successful pledge increases the account's cached available margin
  (`internal/riskengine`, via a new `AdjustAvailableMarginInMinorUnits`
  method) by a haircut-adjusted value, and marks that quantity
  unavailable to sell: `processOrderSubmission`'s pledged-holding gate
  rejects a SELL order with `PLEDGED_QUANTITY_UNAVAILABLE` if it would
  dip into pledged quantity. `POST /margin-pledge/unpledge` reverses
  this — and genuinely refuses (not a fake check) if releasing that
  pledge would drop the account's remaining pledged collateral below
  its currently *utilized* margin for open derivative positions.
  `GET /margin-pledge/holdings?accountId=...` lists an account's active
  pledges. Verified live: pledging 20 DEMO-EQ shares (haircut 15%,
  reference price ₹100.00) contributed exactly 170,000 paise of margin
  — confirmed both directly in the pledge response and indirectly by
  the exact shortfall on a deliberately oversized order shrinking by
  170,000 before vs. after; a SELL of more than the unpledged remainder
  was rejected with `PLEDGED_QUANTITY_UNAVAILABLE`; a partial unpledge
  released the correct proportional margin value; and an unpledge that
  would have dropped collateral below a simulated open position's
  utilized margin was genuinely blocked, then succeeded once that
  utilized figure was cleared.
  **Known, loudly documented gaps**:
  (1) haircut percentages are an ILLUSTRATIVE, made-up table (a 15%
  override for `DEMO-EQ`, a flat 20% default for everything else) — NOT
  a real SEBI/exchange haircut table, which is published per-security
  and changes periodically.
  (2) `referencePriceInMinorUnits` is caller-supplied on every pledge
  call, not looked up from any live price feed — oms-gateway has none
  yet (the same gap the risk check's market-order TODO already
  documents). Treat every resulting margin figure as illustrative, not
  authoritative.
  (3) "Utilized margin for open derivative positions" is set explicitly
  via `POST /margin-pledge/set-utilized-margin` rather than derived
  automatically, because oms-gateway has no structured open-F&O-position
  book yet (`internal/positions` is net equity quantity only) — an
  honest, explicit stand-in, the same pattern `internal/marketsession`
  already uses for a real trading-calendar clock that doesn't exist yet.
  In-memory only, same as every other package in this service.
- **SPAN + exposure margin calculator for F&O** (`internal/marginengine`,
  FEATURES.md §3): `POST /margin/calculate-span-exposure` computes an
  illustrative SPAN margin + exposure margin + total required margin for
  one derivatives position, given its contract notional value. 11 tests
  including a fully hand-worked example (₹5,00,000 notional → SPAN
  ₹50,000 + exposure ₹15,000 = ₹65,000 total), cross-checked line-by-line
  against the package's own rate constants — mirroring
  `chargescalculator`'s worked-example pattern. Verified live: a real
  request with `contractNotionalValueInMinorUnits: 50000000` returned
  exactly `spanMarginInMinorUnits: 5000000`,
  `exposureMarginInMinorUnits: 1500000`,
  `totalRequiredMarginInMinorUnits: 6500000` — the same numbers the unit
  test predicts by hand; a negative notional was rejected with a 400 and
  a plain-language error.
  **LOUD, REPEATED KNOWN GAP**: `illustrativeSpanMarginPercentRate`
  (10%) and `illustrativeExposureMarginPercentRate` (3%) are made-up,
  order-of-magnitude constants — NOT drawn from any real exchange SPAN
  risk-parameter file or current exposure-margin circular. Real SPAN
  margin is the output of a genuinely complex multi-scenario portfolio
  risk simulation run by the clearing corporation, not a flat percentage
  of notional at all. This calculator is NOT exchange-certified, NOT
  SEBI-compliant, and must never size a real margin requirement against
  real capital — it exists purely to prove the request/response shape
  and the illustrative-rate-table pattern this codebase already uses.
- **Iceberg, FOK, IOC order-type acceptance and validation**
  (`internal/orders`, FEATURES.md §3): `OrderSubmissionRequest` gained an
  optional `orderExecutionType` field (`MARKET`/`LIMIT`/`SL`/`SL-M` plus
  new `ICEBERG`/`FOK`/`IOC`) and an `icebergVisibleQuantity` sub-field.
  `orders.ValidateOrderExecutionType`, called from
  `buildSubmitOrderHandler` before any gate runs, rejects an unknown
  execution type and enforces that an `ICEBERG` order's visible quantity
  is present, positive, and ≤ the order's total quantity — all with a
  plain-language 400 response. Backward compatible: omitting the field
  entirely (every pre-existing client) is unaffected. Verified live: an
  Iceberg order missing `icebergVisibleQuantity` was rejected with
  `"an ICEBERG order requires icebergVisibleQuantity"`; one with
  `icebergVisibleQuantity` exceeding `orderQuantity` was rejected too; a
  valid Iceberg order and a valid FOK order were both accepted and
  actually reached matching-engine and filled.
  **HONEST, LOAD-BEARING SCOPE BOUNDARY — read this before assuming more
  than what's here**: this is order-type *acceptance and validation
  only*. True ICEBERG/FOK/IOC fill semantics — showing only a visible
  slice of an iceberg order and refreshing it as it fills, atomically
  killing a FOK order that can't fully fill immediately instead of
  resting it, cancelling an IOC order's unfilled remainder immediately
  instead of letting it rest — all require support in the matching
  engine's order book itself, which does not exist (see
  `internal/matchingengineclient`: its wire protocol carries no field
  for any of this). As of this build, an ICEBERG/FOK/IOC order that
  passes validation here is handed off to matching-engine and matched
  using ordinary continuous-matching rules, exactly like a plain
  Limit/Market order — a fact `buildSubmitOrderHandler` logs loudly
  every time one of these three execution types is accepted, so it's
  never silently misleading in the logs either. Closing this gap needs
  real matching-engine work this build doesn't include.
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

- **Margin funding / instant margin against pledged collateral**
  (`internal/marginfunding`, FEATURES.md §2): `POST /margin-funding/
  request` — a real CASH ADVANCE against an account's pledged collateral
  (`internal/marginpledge`), capped at whatever pledged margin value
  isn't already drawn against. This is REAL money movement: a successful
  request posts a genuine balanced journal entry to `ledger`
  (`internal/ledgerclient`'s new `PostMarginFundingDisbursementJournalEntry`),
  crediting the client's real ledger balance and debiting a clearing
  account, then reflects the same amount into the local risk cache
  (`preTradeRiskEngine.AdjustAvailableMarginInMinorUnits`) so the
  account's very next order sees the extra buying power immediately. The
  disbursed amount becomes a real, mutex-guarded outstanding PRINCIPAL
  balance (`internal/marginfunding.FundingBook`) — every subsequent
  request is capped by `pledgedMarginValue - alreadyOutstandingPrincipal`,
  never by the raw pledged value alone; a two-phase reserve-then-disburse
  flow rolls the reservation back if the ledger posting itself fails, so
  a failed disbursement never permanently eats into the account's real
  funding capacity. `GET /margin-funding?accountId=...` shows outstanding
  principal and remaining capacity. 16 tests in `internal/marginfunding`
  (including a concurrency test proving the mutex genuinely serializes
  requests so total approvals never exceed capacity) plus 3 new tests in
  `internal/ledgerclient` for the new disbursement/repayment journal
  entries. Verified live: pledging 20 DEMO-EQ shares (170,000 paise
  margin), then two funding requests of 100,000 and 70,000 paise each
  moved exactly that much real cash in `ledger` (confirmed via
  `GET /accounts/balance` before/after each call) and updated the
  outstanding-principal figure exactly; a third request past the
  remaining capacity was genuinely rejected.
  **Known, loudly documented gaps**: (1) interest accrual
  (`CalculateIllustrativeAccruedInterest`) is a real, tested formula but
  is NOT wired to run automatically or post any interest journal entry —
  principal tracking is real, interest is illustrative-and-inert; (2) the
  clearing account this reuses is the pre-existing `firm-clearing-acct`,
  not a brand-new dedicated `firm-margin-funding-acct` — `ledger`'s
  account list is hardcoded at its own startup and out of oms-gateway's
  boundary to extend, so the SEPARATION between trade-settlement and
  margin-funding cash flow isn't there yet, even though the money
  movement itself is 100% real; (3) no repayment HTTP endpoint ships yet
  (`FundingBook.RepayFunding` and `LedgerClient.PostMarginFundingRepaymentJournalEntry`
  exist and are tested, just not wired to a route); (4) in-memory only.

- **Real-time Options Chain with live-computed Greeks**
  (`internal/optionschain`, FEATURES.md §3): `GET /options/chain?
  underlyingSpotPrice=&expiryDate=&symbol=` builds a 10-strike ladder
  around the money and, for every strike's call AND put, gets a REAL
  theoretical price and all four Greeks by actually calling
  `quant-engine`'s live Black-Scholes HTTP API
  (`internal/quantengineclient`, `POST /options/price` on `:8085`) — the
  math is genuinely computed by quant-engine, nothing here fakes a
  Greek. Also computes a real Put-Call Ratio
  (`optionschain.CalculatePutCallRatio` — total put OI / total call OI)
  from the chain's Open Interest figures. If quant-engine is unreachable,
  the endpoint returns a clear `502` with a diagnosable message —
  oms-gateway itself never crashes or hangs. 17 tests in
  `internal/optionschain` (strike-ladder math, the synthetic-OI decay
  curve, PCR edge cases including division-by-zero, a fake-pricer-backed
  full-chain test) plus 4 in `internal/quantengineclient`. Verified live
  with a real running quant-engine: a real chain for spot 1000 / expiry
  2026-09-30 came back with real (non-fake) Delta/Gamma/Vega/Theta per
  strike computed live by quant-engine, a real PCR of ~1.15 consistent
  with the illustrative put-OI skew; killing quant-engine and retrying
  produced a clean `502` while oms-gateway's own `/health` kept
  responding.
  **LOUD, REPEATED KNOWN GAP**: Open Interest and Volume per contract
  (`SyntheticOpenInterest`/`SyntheticVolume`) are ILLUSTRATIVE, made-up
  numbers from a deterministic decay formula — NOT observed from any
  real exchange or order book. "Implied Volatility" is really an ASSUMED
  flat volatility fed INTO the pricer (`AssumedAnnualizedVolatility`,
  deliberately not named "impliedVolatility") — NOT solved from an
  observed market price the way quant-engine's own
  `/options/implied-volatility` endpoint genuinely can. The risk-free
  rate is likewise a flat illustrative constant. Only the strike-ladder
  logic, the Greeks/theoretical-price values, and the PCR arithmetic are
  real. There is no real listed-options market data feed anywhere in
  this repo.

- **Direct Market Access (DMA) / FIX-inspired institutional gateway**
  (`internal/dmagateway`, FEATURES.md §3): a genuine, real, session-based
  TCP protocol on `:8088` — a Logon handshake with sequence-number
  enforcement, a NewOrderSingle-equivalent that runs through
  `processOrderSubmission` (the EXACT SAME risk-check/audit-trail/
  matching-engine pipeline HTTP `/orders/submit` uses, via a plain Go
  closure — nothing duplicated), an ExecutionReport-equivalent response,
  and a Logout handshake. Sequence-number tracking is real, mutex-guarded
  per-connection state: an out-of-sequence message (or one that arrives
  before Logon) is genuinely REJECTed and the connection is closed,
  matching real FIX session behavior. 23 tests (protocol parsing, every
  session-state transition table-driven via `SessionHandler.HandleLine`
  with a fake `submitOrder`, plus one real end-to-end test over an actual
  TCP socket). Verified live with a standalone Python TCP client
  (`dma_test_client.py`) against a real running oms-gateway: a full
  Logon→NewOrderSingle→ExecutionReport→Logout happy path; a huge order
  that the REAL risk engine genuinely rejected (`OrdStatus=REJECTED`
  carrying the real `INSUFFICIENT_MARGIN` rejection text) — proving this
  isn't a rubber-stamp path; an out-of-sequence NewOrderSingle that was
  REJECTed and the connection closed by the server; a NewOrderSingle sent
  before any Logon, also rejected. The accepted order also showed up in
  `GET /audit-trail`, confirming genuine pipeline reuse, not a parallel
  stub.
  **============ LOUD, REPEATED, LOAD-BEARING WARNING ============**
  **THIS IS NOT FIX-PROTOCOL-CERTIFIED.** It borrows a few FIX SESSION
  CONCEPTS (Logon, sequence numbers, an ExecutionReport-style response,
  Logout) for illustrative realism only. It is NOT FIX 4.2/4.4 (or any
  version) wire-format compliant, does NOT use real FIX tag numbers
  (human-readable field names like `MsgType`/`ClOrdID` instead — on
  purpose, so nobody mistakes a capture for real FIX traffic), does NOT
  implement FIX's real session-recovery behavior (a sequence gap here is
  just rejected-and-disconnected, not resend/gap-filled), and would
  NEVER be accepted by a real exchange's or institutional counterparty's
  FIX engine. A real DMA/FIX integration needs a certified FIX engine
  (e.g. QuickFIX or a licensed commercial engine), real exchange
  onboarding, and a full FIX conformance test pass. See
  `internal/dmagateway`'s package doc for the same warning, repeated
  again there.
  **================================================================**

- **Paper trading mode sharing the exact same OMS code path as live**
  (`internal/papertrading`, FEATURES.md §7): set
  `isPaperTradingOrder: true` on any `/orders/submit` (or DMA
  NewOrderSingle) request and it runs through the IDENTICAL KYC/freeze/
  pledged-holding/pre-trade-risk gates and the identical audit trail as
  a live order — `processOrderSubmission` only branches at the very
  final step. Instead of handing off to matching-engine, the order gets
  a simulated fill (`internal/papertrading.SimulateFill`: a LIMIT order
  fills immediately and fully at its own limit price; a MARKET order
  fills at a caller-supplied `paperMarketReferencePriceInMinorUnits`,
  since oms-gateway still has no live price feed). Critically, a paper
  fill posts NO real settlement to `ledger` and never touches
  matching-engine's real order book — it updates a completely SEPARATE
  `positions.PositionBook` instance (`paperPositionBook`), so paper P&L
  can never contaminate real holdings. `GET /paper-positions?
  accountId=...` mirrors `GET /positions` for the paper book. 9 tests in
  `internal/papertrading`. Verified live: a paper LIMIT buy filled
  immediately and showed up ONLY in `/paper-positions` (real
  `/positions` and the real ledger balance were provably unchanged
  before/after); a paper MARKET order without a reference price was
  cleanly rejected by the simulation engine itself (not silently
  filled at a fake price); and — proving this is genuinely risk-checked,
  not a rubber stamp — a paper order for 999,999,999 shares was rejected
  by the REAL pre-trade risk engine with the same `INSUFFICIENT_MARGIN`
  reason a live order would get.
  **Known gap**: the pledged-holding SELL gate still checks the REAL
  position book even for a paper SELL order (there's no separate paper
  pledge/collateral concept yet) — a documented simplification, not a
  security hole, since it can only make a paper SELL more conservative,
  never less.

- **Strategy resource limits & circuit breakers**
  (`internal/algolimits`, FEATURES.md §7): tag any order with
  `strategyIdentifier` and it's checked against that strategy's
  configured limits — set via `POST /algo-limits/configure`
  (`maxOrdersPerSecond`, `maxNotionalPerDayInMinorUnits`) — BEFORE it
  ever reaches KYC/freeze/risk/matching-engine. Max orders/sec is a real
  token-bucket rate limiter (continuous refill by elapsed time, capped
  at capacity — not a fake `sleep`, not a naive fixed-window counter that
  lets a burst straddle a boundary). Max notional/day is a real
  cumulative running total that resets at an actual UTC calendar-day
  boundary; both checks take `now` as an explicit parameter throughout
  (never the wall clock internally), so every boundary is exactly
  reproducible in tests — the wall clock is only ever read once, in
  `cmd/server/main.go`'s call site. An order tripping either limit is
  rejected with `STRATEGY_RATE_LIMIT_EXCEEDED` or
  `STRATEGY_DAILY_NOTIONAL_LIMIT_EXCEEDED` and never reaches any later
  gate. `GET /algo-limits?strategyId=...` shows today's usage. An order
  with no `strategyIdentifier` at all is completely unaffected — fully
  backward compatible. 18 tests in `internal/algolimits`, including
  exact-boundary tests for both the token bucket (capacity burst, partial
  refill, long-idle-doesn't-over-accumulate) and the notional cap
  (exact-cap-allowed, one-unit-over-rejected, same-day-accumulates,
  next-day-resets), plus a concurrency test proving 50 simultaneous
  requests against a 10/sec bucket yield exactly 10 successes. Verified
  live: configuring `algo-1` at 2 orders/sec + 50,000 paise/day, then
  firing orders showed the 3rd immediate order genuinely rate-limited,
  a wait-and-retry succeeding once a token refilled, the daily cap
  being hit at exactly 50,000 and the next order rejected, and an
  untagged order sailing through unaffected — every rejection also
  landed in `GET /audit-trail` as `STRATEGY_LIMIT_REJECTED`.

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
- No real FIX or WebSocket session handling — `internal/dmagateway` is a
  real TCP session gateway inspired by FIX session CONCEPTS, explicitly
  and repeatedly NOT FIX-protocol-certified (see its own section above
  and its package doc). No WebSocket support at all.
- No per-client-session backpressure/throttling for ordinary HTTP
  clients (ARCHITECTURE.md §4) — `internal/algolimits` only throttles
  orders that opt into a `strategyIdentifier`.
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

# Margin Pledge: pledge 20 DEMO-EQ shares from a real existing position
# (acct-001 must actually hold at least 20 — buy some first via
# /orders/submit if starting fresh) as collateral. Response shows the
# haircut-adjusted margin value contributed and the new available margin.
curl -X POST localhost:8081/margin-pledge/pledge -d '{
  "clientAccountIdentifier": "acct-001",
  "instrumentSymbol": "DEMO-EQ",
  "quantity": 20,
  "referencePriceInMinorUnits": 10000
}'
curl "localhost:8081/margin-pledge/holdings?accountId=acct-001"
# selling more than the UNPLEDGED remainder is rejected:
curl -X POST localhost:8081/orders/submit -d '{
  "clientAccountIdentifier": "acct-001",
  "instrumentSymbol": "DEMO-EQ",
  "orderSideIsBuyNotSell": false,
  "limitPriceInMinorUnits": 10000,
  "orderQuantity": 999
}'
# unpledge releases the proportional margin value back:
curl -X POST localhost:8081/margin-pledge/unpledge -d '{
  "clientAccountIdentifier": "acct-001",
  "instrumentSymbol": "DEMO-EQ",
  "quantity": 20
}'
# simulate an open derivative position utilizing margin, then prove
# unpledge is genuinely blocked while it would drop collateral below
# that utilized figure:
curl -X POST localhost:8081/margin-pledge/set-utilized-margin -d '{
  "clientAccountIdentifier": "acct-001",
  "utilizedMarginInMinorUnits": 1
}'

# SPAN + exposure margin calculator (F&O) — illustrative, NOT
# exchange-certified, see the package doc's warning.
curl -X POST localhost:8081/margin/calculate-span-exposure -d '{
  "contractNotionalValueInMinorUnits": 50000000
}'

# Iceberg order missing icebergVisibleQuantity — rejected with a
# plain-language 400
curl -X POST localhost:8081/orders/submit -d '{
  "clientAccountIdentifier": "acct-001",
  "instrumentSymbol": "DEMO-EQ",
  "orderSideIsBuyNotSell": true,
  "limitPriceInMinorUnits": 10000,
  "orderQuantity": 100,
  "orderExecutionType": "ICEBERG"
}'
# a VALID Iceberg order — accepted and handed to matching-engine like an
# ordinary Limit order (see the honest scope-boundary note above)
curl -X POST localhost:8081/orders/submit -d '{
  "clientAccountIdentifier": "acct-001",
  "instrumentSymbol": "DEMO-EQ",
  "orderSideIsBuyNotSell": true,
  "limitPriceInMinorUnits": 10000,
  "orderQuantity": 100,
  "orderExecutionType": "ICEBERG",
  "icebergVisibleQuantity": 10
}'
# FOK / IOC — accepted and validated, but matched with ordinary
# continuous-matching rules (matching-engine doesn't implement true
# fill-or-kill / immediate-or-cancel semantics yet)
curl -X POST localhost:8081/orders/submit -d '{
  "clientAccountIdentifier": "acct-001",
  "instrumentSymbol": "DEMO-EQ",
  "orderSideIsBuyNotSell": true,
  "limitPriceInMinorUnits": 10500,
  "orderQuantity": 5,
  "orderExecutionType": "FOK"
}'

# Margin funding: pledge collateral first (see the margin-pledge example
# above), then draw a real cash advance against it
curl -X POST localhost:8081/margin-funding/request -d '{
  "clientAccountIdentifier": "acct-001",
  "requestedAmountInMinorUnits": 100000
}'
curl "localhost:8081/margin-funding?accountId=acct-001"

# Options chain — needs quant-engine running on :8085 (see that
# service's own README to start it). Real Greeks, synthetic OI/Volume.
curl "localhost:8081/options/chain?underlyingSpotPrice=1000&expiryDate=2026-09-30&symbol=DEMO-EQ"

# Paper trading: same risk/audit path as live, simulated fill, separate
# position book — never touches the real ledger or matching-engine
curl -X POST localhost:8081/orders/submit -d '{
  "clientAccountIdentifier": "acct-001",
  "instrumentSymbol": "DEMO-EQ",
  "orderSideIsBuyNotSell": true,
  "limitPriceInMinorUnits": 10000,
  "orderQuantity": 10,
  "isPaperTradingOrder": true
}'
curl "localhost:8081/paper-positions?accountId=acct-001"

# Strategy resource limits: configure, then tag an order with strategyIdentifier
curl -X POST localhost:8081/algo-limits/configure -d '{
  "strategyIdentifier": "algo-1",
  "maxOrdersPerSecond": 5,
  "maxNotionalPerDayInMinorUnits": 5000000
}'
curl -X POST localhost:8081/orders/submit -d '{
  "clientAccountIdentifier": "acct-001",
  "instrumentSymbol": "DEMO-EQ",
  "orderSideIsBuyNotSell": true,
  "limitPriceInMinorUnits": 10000,
  "orderQuantity": 5,
  "strategyIdentifier": "algo-1"
}'
curl "localhost:8081/algo-limits?strategyId=algo-1"

# DMA/FIX-inspired gateway — NOT FIX-certified, see internal/dmagateway's
# package doc. Plain TCP on :8088, one message per line:
#   MsgType=LOGON|MsgSeqNum=1|SenderCompID=CLIENT1|TargetCompID=MERCURIUS
#   MsgType=NEW_ORDER_SINGLE|MsgSeqNum=2|ClOrdID=ord-1|Account=acct-001|Symbol=DEMO-EQ|Side=BUY|OrderQty=10|OrdType=LIMIT|Price=10000
#   MsgType=LOGOUT|MsgSeqNum=3
# e.g. with netcat: nc localhost 8088

go test ./...
```
