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
  matching real FIX session behavior. 25 top-level test functions
  (protocol parsing, every session-state transition table-driven via
  `SessionHandler.HandleLine` with a fake `submitOrder`, plus real
  end-to-end tests over an actual TCP socket) — including
  `conformanceSuite_test.go`, a real, executable **conformance test
  suite** for FEATURES.md §18's "FIX protocol certification suite"
  line item: 11 distinct, named checklist behaviors (Logon at the
  correct starting sequence, double-Logon refusal, sequence gaps both
  ahead and behind expected, pre-Logon message rejection, accept/reject
  execution-report paths including 7 malformed-field sub-cases, Logout,
  malformed wire input, and unknown-MsgType-doesn't-close-session), a
  subset re-run against a real live `Server` TCP listener for extra
  evidence, and a captured pass/fail run written to
  `internal/dmagateway/CONFORMANCE_REPORT.md`. Verified live with a standalone Python TCP client
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

- **Real-time Mark-to-Market engine across leveraged positions**
  (`internal/marktomarket`, FEATURES.md §12): a real, weighted-average
  cost-basis tracker fed by the exact same fill events
  `internal/positions` consumes (plus the executed price), and a real
  HTTP PUSH endpoint (`POST /mark-to-market/price`) for the current
  market price of an instrument — see the package doc's design-choice
  note on why push, not pull: oms-gateway has no live price feed anywhere
  yet (the same documented gap the pre-trade risk check's market-order
  TODO already calls out). `GET /mark-to-market?accountId=...` returns
  real unrealized P&L per position and per account — but ONLY for
  accounts this service considers LEVERAGED (outstanding margin-funding
  principal from `internal/marginfunding`, or any pledged quantity at all
  from `internal/marginpledge`); an unleveraged account gets a clear
  `isLeveragedAccount:false` with an empty position list rather than a
  potentially-misleading P&L for exposure this endpoint isn't scoped to
  report. Cost basis is real weighted-average math: adding to a position
  blends the new fill into the existing average, a partial close leaves
  the average untouched, and a full direction reversal (long flips short
  or vice versa) restarts the average at the reversing fill's price — the
  position's economic identity genuinely changed. The single signed
  formula `netQuantity * (marketPrice - averageEntryPrice)` is correct
  for both long and short positions, hand-verified in tests for both
  signs. 13 tests including the weighted-average, partial-close,
  direction-reversal, and short-position-profits-on-price-drop cases, plus
  a concurrency test. Verified live: buying 20 DEMO-EQ @ ₹100.00 (a REAL
  fill against a real running matching-engine), pledging 10 of them
  (making the account leveraged), then pushing a market price of ₹110.00
  produced exactly `unrealizedPnLInMinorUnits: 20000` (20 × ₹10.00) —
  matching the hand-worked math exactly — while a second, unleveraged
  account correctly showed `isLeveragedAccount:false` and no P&L at all
  despite also holding a position.
  **Known gaps**: no continuous/subscribed price feed (push-only, see
  above); positions with a live quantity but no market price ever pushed
  are silently omitted from the response rather than reported as
  "MTM unavailable"; in-memory only.

- **Auto-liquidation on margin breach, with graduated warnings first**
  (`internal/autoliquidation`, FEATURES.md §12): given an account's real
  margin utilization — `outstandingMarginFundingPrincipal /
  totalPledgedCollateralValue`, assembled from real
  `internal/marginfunding` and `internal/marginpledge` state — this
  emits graduated states: `NORMAL` below 80%, `WARNING` at 80%, `URGENT`
  at 90%, and `LIQUIDATION` at 100%+ (all configurable via
  `autoliquidation.Thresholds`). `GET /auto-liquidation/status?
  accountId=...` is a PURE read (`autoliquidation.ClassifyUtilization`)
  that never acts, even at `LIQUIDATION` — only `POST
  /auto-liquidation/evaluate` can actually liquidate, and it only does so
  when genuinely breached: at `WARNING`/`URGENT` it is provably a no-op
  (dedicated tests assert zero calls to the order-submission callback).
  At `LIQUIDATION`, it computes exactly how much notional must be sold to
  bring the account back down to a configurable target (75% by default),
  walks the account's LONG positions largest-notional-first, and submits
  REAL reducing MARKET SELL orders through the EXACT SAME
  `processOrderSubmission` pipeline every other order path (HTTP submit,
  DMA gateway) reuses — via a plain Go closure, nothing duplicated, so a
  liquidation order is genuinely risk-checked, audited, and reaches
  matching-engine like any other order. 15 tests including a fully
  hand-worked liquidation-sizing case (₹1,20,000 outstanding against
  ₹1,00,000 pledged → 120% utilization → target 75% of ₹1,00,000 =
  ₹75,000 → shortfall ₹45,000 → sells exactly 45 shares @ ₹1,000 to fully
  cover it), a largest-position-first ordering test, a
  rejected-submission-doesn't-reduce-shortfall test, and a concurrency
  test. Verified live: pledging 100 DEMO-EQ shares then borrowing 82% of
  capacity showed `riskState:"WARNING"` and a `POST
  /auto-liquidation/evaluate` call genuinely submitted ZERO orders
  (position unchanged before/after); unpledging 30 of those shares (not
  blocked — utilized-derivative-margin was never set for this account,
  see `internal/marginpledge`'s own documented gap) dropped the real
  pledged collateral value and pushed utilization to a genuine 117.6%,
  showing `riskState:"LIQUIDATION"`; the subsequent evaluate call
  genuinely submitted a real 26-share MARKET SELL through the real order
  pipeline (`wasAccepted:true`, sequenced, logged in the audit trail) —
  proving this is real order submission with real consequences, not a
  simulation.
  **Known gaps**: (1) short positions are never liquidated (only long
  positions are sold down — covering a short needs a BUY order, an
  intentionally excluded scope boundary); (2) no automatic
  scheduler/poller triggers evaluation — this build only evaluates on an
  explicit `POST /auto-liquidation/evaluate` call, the same
  admin-toggle-not-a-real-clock pattern `internal/marketsession` already
  uses; (3) a liquidation order that's accepted by the pipeline can still
  fail to actually fill if there's no real resting liquidity on the other
  side of the book at that instant — exactly like any other real market
  order, not a guarantee unique to this feature (this is why the live
  verification above shows the order accepted but doesn't claim a fill:
  there was no counterparty resting an opposite-side order at that
  moment); (4) a position with no pushed `internal/marktomarket` price is
  skipped entirely during liquidation sizing (reported in
  `skippedPositionsMissingPrice`), which can leave an account
  under-liquidated if that was its only holding.

- **Per-user, per-segment exposure limits (configurable by risk team)**
  (`internal/exposurelimits`, FEATURES.md §12): a real pre-trade check,
  run right alongside `internal/riskengine`'s own margin check (actually
  BEFORE it in the gate order, right after the strategy-limits gate — no
  reason to touch KYC/freeze/margin for an order the risk team has
  already capped out on exposure grounds). `POST /exposure-limits/
  configure` sets an account's total notional exposure cap and/or its cap
  within one instrument "segment" (`EQUITY`/`FUTURES_AND_OPTIONS`/
  `CURRENCY`/`OTHER` — an ILLUSTRATIVE, symbol-suffix-derived
  classification since this repo has no real instrument-master segment
  taxonomy, see the package doc). An order that would push either the
  account-wide running total OR the segment-specific running total past
  its CONFIGURED limit (an unconfigured account/segment is completely
  unconstrained, same convention `internal/algolimits` already uses) is
  rejected with a clear, specific machine-readable reason —
  `ACCOUNT_EXPOSURE_LIMIT_EXCEEDED` or `SEGMENT_EXPOSURE_LIMIT_EXCEEDED`
  — identifying exactly which limit was breached, never an ambiguous
  generic rejection. `GET /exposure-limits?accountId=...&segment=...`
  shows configured limits and current usage. 12 tests including
  exact-at-limit-allowed, over-by-one-rejected, independent-per-segment,
  independent-per-account, rejection-doesn't-mutate-running-totals, and a
  concurrency test proving 200 simultaneous 10-unit reservations against
  a 1000-unit limit yield exactly 100 successes. Verified live:
  configuring `acct-001` with a 50,000-unit account cap AND a
  30,000-unit `EQUITY` segment cap, a 20,000-unit order succeeded and
  the status endpoint showed exactly 20,000 used in both totals; a
  follow-up 15,000-unit order (would total 35,000 against the 30,000
  segment cap) was genuinely rejected with `SEGMENT_EXPOSURE_LIMIT_EXCEEDED`
  and the exact shortfall numbers in the message; a separate account
  configured with ONLY a 25,000-unit account-wide cap (no segment cap)
  was rejected on a second order specifically with
  `ACCOUNT_EXPOSURE_LIMIT_EXCEEDED` once its running total would have
  hit 30,000 — proving the two limit types are independently enforced
  and independently identified.
  **Known gaps**: (1) "exposure" here is a cumulative RESERVATION model
  (every accepted order's notional accumulates and is never
  automatically released — the same simplification
  `internal/algolimits` already uses for its own daily notional cap),
  NOT a live mark-to-market net position value — `ReleaseExposure` exists
  and is tested but nothing calls it automatically yet; (2) segment
  classification is ILLUSTRATIVE (a symbol-suffix convention), not a
  real exchange segment taxonomy; (3) the reservation happens BEFORE
  KYC/freeze/margin/matching-engine, so an order that's later rejected by
  any of those gates (or that reaches matching-engine but never fills)
  still permanently consumes exposure capacity — a documented
  conservative-but-imprecise tradeoff, the same one
  `internal/algolimits` already accepts for its own reservation.

- **Circuit breaker / kill-switch at the exchange-connectivity layer**
  (`internal/connectivitykillswitch`, FEATURES.md §12): a real,
  mutex-guarded kill switch, checked FIRST — before even the strategy
  resource-limits gate — on every single order submission
  (`processOrderSubmission`). When engaged (either way below), EVERY new
  order submission is rejected immediately with a clear
  `TRADING_HALTED` error; `buildCancelOrderHandler` deliberately never
  consults it at all, so existing resting orders can always still be
  cancelled, per FEATURES.md §12. Two independent engagement paths, both
  real: (1) a manual admin toggle — `POST
  /connectivity-kill-switch/engage` (body `{"reason":"..."}`) and `POST
  /connectivity-kill-switch/disengage`; (2) a real, health-check-driven
  AUTO trigger that engages the SAME switch after 3 consecutive
  matching-engine connectivity FAILURES (genuine transport/dial errors,
  deliberately distinguished from a legitimate business rejection
  matching-engine itself returns, which never counts against the
  streak) — fed from TWO real sources: every live order's real
  matching-engine hand-off attempt, AND a genuinely independent
  background goroutine (`main()`) that polls matching-engine every 2
  seconds via its existing `QueryOrderStatusAndAwaitResult` method,
  regardless of order flow. That second source is load-bearing, not
  decorative: without an independent prober, once trading is halted
  every new order is rejected BEFORE it ever reaches
  `matchingEngineClient`, so the very act of halting would make
  self-recovery impossible to ever observe. A manual engagement and an
  auto engagement are tracked as fully independent flags —
  `DisengageManually` only clears the manual one; if matching-engine is
  still genuinely failing, the AUTO flag alone keeps trading halted
  regardless, so an admin can't accidentally re-open trading into a
  still-broken downstream. The AUTO flag clears itself automatically the
  moment a connectivity check succeeds again. `GET
  /connectivity-kill-switch/status` shows the full real state. 12 tests
  including an exact-threshold-transition test (only the 3rd consecutive
  failure trips it, proven via the transition-return-value), a
  success-resets-and-clears test, a manual-disengage-does-NOT-clear-
  active-auto-engagement test, and a concurrency test. Verified live
  with a real running matching-engine killed mid-session: three
  consecutive real connectivity failures via order submission
  auto-engaged the switch (`isAutoEngaged:true`) and the very next order
  submission was rejected with `TRADING_HALTED` without even attempting
  matching-engine; a manual engage/disengage cycle was independently
  verified to halt and resume order submission while a genuine
  `orders/cancel` call against a non-existent order kept returning its
  ordinary (non-`TRADING_HALTED`) error throughout, proving cancellation
  is never gated. Separately, with NO order traffic at all, killing
  matching-engine and waiting ~8 seconds showed the background prober
  alone auto-engage the switch (`consecutiveFailureCount:6,
  isAutoEngaged:true`); restarting matching-engine and waiting a few
  more seconds showed the very same switch self-clear
  (`isTradingHalted:false, consecutiveFailureCount:0`) with zero admin
  intervention — proving the self-heal path genuinely works, not just
  the halt path.
  **Known gaps**: (1) the failure threshold (3) and probe interval (2s)
  are hardcoded in `cmd/server/main.go`, not configurable via any
  endpoint; (2) the background prober queries a hardcoded `DEMO-EQ`/seq
  `0` — a real build would probe something that can never legitimately
  fail to distinguish "matching-engine down" from "this specific
  instrument/order genuinely doesn't exist", though in practice
  `QueryOrderStatusAndAwaitResult`'s Go error is already scoped to
  transport failures only, so this is a low-risk simplification, not a
  correctness bug; (3) in-memory only — an oms-gateway restart always
  starts fully disengaged, even if the operator meant to leave trading
  halted.

- **Execution algos: TWAP / VWAP / POV** (`internal/executionalgos`,
  FEATURES.md §15): real algorithmic slicing of one large parent order
  into many smaller child orders, released over time. Three real
  strategies, all deterministically testable (every clock/volume input
  is an explicit parameter — nothing sleeps or reads the wall clock
  internally, same discipline as `internal/algolimits`' token bucket):
  (1) **TWAP** — `BuildTwapSchedule` splits the parent quantity into N
  equal-sized slices (remainder distributed by the largest-remainder/
  Hamilton apportionment method so slices always sum EXACTLY to the
  parent quantity — no rounding drift) at N equally-spaced points in
  time from `startTime` to `endTime` inclusive; (2) **VWAP** —
  `BuildVwapSchedule` splits the parent quantity across a caller-supplied
  historical/assumed intraday volume curve, proportionally to each
  bucket's `historicalVolumeWeight` (weights need not pre-sum to 1 — 
  normalized internally), again via largest-remainder apportionment, and
  releases each slice at its bucket's own time; (3) **POV** — a
  `PovScheduler` has no pre-built time grid at all: it consumes real-time
  observed CUMULATIVE market volume readings via
  `OnVolumeObservation(now, cumulativeVolume)` and, for each observation,
  releases a child slice sized at `participationRate * (volume traded
  since the previous observation)`, hard-capped by both a configured
  `maxClipSizeQuantity` and whatever of the parent order remains
  unsliced — the first observation only establishes a baseline (no slice
  yet, since there's no "volume since last observation"). A
  `Scheduler` wraps a TWAP/VWAP plan with real, mutex-guarded
  release-tracking: `PollDueSlices(now)` returns only NEWLY due slices on
  each call, so polling on any cadence (or twice at the same `now`) never
  double-releases a slice. 13 tests including hand-worked numeric cases
  (1000 qty / 4 TWAP slices = exactly 250 each; 1001 qty / 4 slices =
  one slice of 251 and three of 250, proving the remainder never gets
  lost or duplicated; VWAP weights 1:4:3:2 over 1000 qty = exactly
  100/400/300/200; POV with 10% participation and a 50-unit cap turns a
  300-unit volume delta into a 30-unit slice and an 800-unit delta into
  a 50-unit slice capped by `maxClipSizeQuantity`, and separately proves
  a POV slice additionally caps at the parent's remaining quantity so
  the very last slice can complete the order exactly instead of
  overshooting it) plus a concurrency-safe `Scheduler` double-poll test.
  Wired into `cmd/server/main.go` via `executionAlgoOrderRegistry` (an
  in-memory, mutex-guarded map from a generated `algoOrderId` to its live
  scheduler) and six real endpoints:
  `POST /execution-algos/twap/create`, `POST /execution-algos/vwap/create`,
  `POST /execution-algos/pov/create`, `POST /execution-algos/poll`
  (TWAP/VWAP — drives `PollDueSlices` forward with a caller-supplied
  `now`), `POST /execution-algos/pov/observe-volume` (feeds one
  cumulative-volume reading), and `GET /execution-algos/status`. Verified
  live against a real running server: created a 1000-share TWAP order
  over a 30-minute window in 4 slices (got back exactly 250/250/250/250
  at 10:00/10:10/10:20/10:30), polled at 10:15 and got back exactly the
  two due slices (index 0 and 1, `remainingQuantity:500`), polled again
  at the same `now` and got back `dueSlices:null` (no double-release);
  created a VWAP order with curve weights 1:4:3:2 and got back exactly
  100/400/200/300 in bucket-time order; created a POV order (10%
  participation, 50-unit cap), fed volume observations of 10000 (baseline,
  `newSlice:null`), then 10300 (delta 300 → `newSlice.quantity:30`,
  `remainingQuantity:970`), then 11100 (delta 800 → capped at
  `newSlice.quantity:50`, `remainingQuantity:920`) — every live number
  matched the hand-worked unit-test expectations exactly; a status query
  against an unknown `algoOrderId` correctly returned 404.
  **Known gaps**: (1) this package computes WHAT to release and WHEN —
  it does NOT itself submit the resulting `ChildOrderSlice` values as
  real `orders.OrderSubmissionRequest` calls; wiring a background
  scheduler loop that actually polls and submits is future work; (2) no
  persistence — an oms-gateway restart loses every in-flight algo
  order's schedule/state; (3) VWAP's volume curve is entirely
  caller-supplied (illustrative), not sourced from any real historical
  volume feed; (4) POV has no real-time market-volume feed wired in
  either — a caller has to supply `cumulativeMarketVolume` readings
  itself (e.g. from `matchingengineclient`'s depth/trade data, which this
  build doesn't automatically bridge in).

- **Options strategy payoff diagram** (`internal/payoffdiagram`,
  FEATURES.md §15): real math — max profit, max loss, and every
  breakeven spot price — for an arbitrary set of option legs (any mix of
  calls/puts, strikes, buy/sell, quantities), computed EXACTLY, not by
  sampling. The approach leans on a real structural fact about option
  payoffs: at expiry, total payoff is piecewise LINEAR in the underlying
  spot price with kinks only at each leg's strike, and the domain is
  bounded below at spot=0 (a real price can't go negative) but unbounded
  above — so (1) any finite max/min is provably attained at spot=0 or at
  one of the strikes (a line has no interior extremum), never needing to
  scan every price; (2) whether profit/loss is unbounded is exactly
  determined by the slope of the one ray beyond the highest strike; (3)
  breakevens are found by exact linear interpolation within whichever
  segment changes sign, not a numerical root-finder. `ComputePayoffDiagram`
  is a pure, stateless function of the leg slice — safe to call fresh on
  every request as legs are added, per FEATURES.md's own "computed live"
  framing. 15 tests, most against textbook hand-worked numbers: a long
  straddle (buy call+put, same strike 100, premium 5 each) — max loss
  exactly 10 (=combined premium), max profit unbounded, breakevens
  exactly 90 and 110 (=strike ± total premium); a long strangle (call
  strike 110 @ 3, put strike 90 @ 3) — max loss exactly 6 over the flat
  region between strikes, breakevens exactly 84 and 116; a bull call
  spread (buy 100 @ 8, sell 110 @ 3) — max profit exactly 5 (=spread
  width 10 − net debit 5), max loss exactly 5 (=net debit), single
  breakeven exactly 105 (=lower strike + net debit); plus a naked short
  call (unbounded loss), a cash-secured short put (loss BOUNDED at
  spot=0, not unbounded — max loss exactly 94 = strike − premium), and
  full input-validation coverage. Wired into `cmd/server/main.go` as a
  single stateless `POST /payoff-diagram/compute` endpoint (post the
  full current leg set, get back `maxProfitInMinorUnits`/
  `maxProfitIsUnbounded`/`maxLossInMinorUnits`/`maxLossIsUnbounded`/
  `breakevenPricesInMinorUnits`). Verified live against a real running
  server: the long straddle example returned exactly
  `maxLossInMinorUnits:10, maxProfitIsUnbounded:true,
  breakevenPricesInMinorUnits:[90,110]`; the bull call spread example
  returned exactly `maxProfitInMinorUnits:5, maxLossInMinorUnits:5,
  breakevenPricesInMinorUnits:[105]` — both matching the unit tests'
  hand-worked numbers exactly; an empty-legs request correctly returned
  400.
  **Known gaps**: (1) at-expiry payoff only — no time-value/theta-decay
  curve for an intermediate date before expiry (a real "payoff diagram
  as of today" needs an options-pricing model, which this package
  deliberately doesn't attempt); (2) no direct coupling to
  `internal/multilegoptions`' leg type or `internal/optionschain`'s
  synthetic chain — a caller has to translate either into this package's
  `OptionLeg` shape itself.

- **Multi-leg options strategy builder with atomic all-or-nothing
  execution** (`internal/multilegoptions`, FEATURES.md §15): `POST
  /multileg-options/execute` accepts a named strategy shape
  (`STRADDLE`/`STRANGLE`/`BULL_CALL_SPREAD`/`BEAR_PUT_SPREAD`/
  `IRON_CONDOR`/`BUTTERFLY`) and a leg set, and `multilegoptions.
  ValidateStrategyShape` genuinely checks the legs match that strategy's
  real textbook definition — leg count, call/put mix, buy/sell direction,
  relative strike ordering, and quantity relationships (e.g. a straddle
  must be one CALL + one PUT at the SAME strike, same quantity, same
  direction; a butterfly's three strikes must be EQUALLY spaced with the
  body quantity exactly double each wing's) — before any leg is submitted.
  Execution is genuinely atomic: each leg is submitted through the EXACT
  SAME `processOrderSubmission` pipeline every other order path reuses
  (via a plain Go closure — nothing duplicated); if any leg is rejected,
  every previously-accepted leg is rolled back with a REAL, genuinely-
  submitted opposite-side compensating order for the same quantity — not
  a no-op. 27 tests including full shape-validation coverage for all six
  strategies and atomic-execution tests (all-accepted, a rejected-second-
  leg genuinely rolling back the first, a rollback-that-itself-fails
  surfacing loudly, a shape-invalid request submitting zero legs). Verified
  live: a valid long straddle had both legs accepted; an invalid two-CALL
  "straddle" was rejected before any submission; a straddle where the
  second leg breached a configured exposure limit showed the first leg
  genuinely accepted then genuinely rolled back — including the
  rollback's OWN attempt being independently exposure-limited and that
  failure surfacing in `rollbackErrorMessage`, and the account's real
  position correctly settling back to empty.
  **HONEST SCOPE BOUNDARY**: matching-engine has no real listed OPTIONS
  instrument — each leg submits as an ordinary LIMIT order (at the leg's
  premium as price) against its own caller-supplied `instrumentSymbol`,
  using the option-shape fields purely for validation math (borrowed
  conceptually from `internal/payoffdiagram`'s leg idea, not its type).
  Rollback is best-effort (a real compensating order, not a database
  transaction) — see the package doc for the full contract.

- **Basket/program order execution** (`internal/basketorders`,
  FEATURES.md §15): `POST /basket-orders/execute` accepts (symbol, side,
  quantity-or-weight) constituent tuples plus a
  `netCashConstraintInMinorUnits`, computes the basket's real net cash
  (buys' notional minus sells' notional, weight-mode quantities derived
  from the constraint), and — if within the constraint — submits every
  constituent through the exact same order-submission path, tracking real
  aggregate fill status (`ALL_ACCEPTED`/`PARTIALLY_ACCEPTED`/
  `NONE_ACCEPTED`) and per-constituent filled quantity. Deliberately NOT
  atomic (contrast `internal/multilegoptions` above) — a rejected
  constituent doesn't stop the rest of the basket from being submitted, a
  real program-trade tolerance FEATURES.md itself only asks "tracking
  aggregate fill status" for. A basket that would breach its OWN stated
  cash constraint is rejected wholesale, with ZERO submissions, before
  ever touching the order pipeline. 20 tests including quantity-mode and
  weight-mode resolution (hand-worked: 60%/40% weights over a 100,000
  budget → exactly 600/200 shares at 100/200 reference prices), exact-
  at-constraint-allowed, buy+sell net-offsetting cash, and a
  partially-accepted aggregate-status test. Verified live: a 5-share
  quantity-mode basket genuinely filled against the real order book; a
  basket breaching its net cash constraint was rejected with zero
  submissions; a 100%-weight basket resolved to the same real fill.
  **Known gap**: `ReferencePriceInMinorUnits` is caller-supplied (no live
  price feed — the same documented gap this codebase repeats everywhere).

- **Pre-trade impact-cost / slippage estimator**
  (`internal/impactcostestimator`, FEATURES.md §15): `POST
  /impact-cost/estimate` takes a hypothetical order size and a real order
  book depth snapshot (bid/ask price levels), and walks the book — best
  price first, consuming each level's quantity before moving to the next,
  exactly like a real matching engine crossing a marketable order — to
  compute the real, quantity-weighted average fill price and the
  resulting slippage against the best available price. 13 tests including
  hand-worked multi-level walks (10 @ 10000 + 20 @ 10100 = exactly
  10066.67 average for a 30-unit buy), a sell walking bids descending,
  depth-insufficient-for-full-size correctly capping the fillable
  quantity, and strict bid-descending/ask-ascending ordering validation.
  Verified live: a 30-unit buy against a real 3-level book returned
  exactly the hand-worked `10033.33` average (for the smaller 15-unit
  case tested) and `33.33` slippage; a 100-unit buy against 60 total ask
  depth correctly capped `quantityFillable` at 60 with
  `depthInsufficientForFullSize:true`; malformed (unsorted) depth was
  rejected with a specific error.
  **LOUD, REPEATED KNOWN GAP**: oms-gateway has NO way to query a real,
  live order book depth snapshot — matching-engine only PUSHES its full
  depth fire-and-forget to `market-data` (verified by reading both
  services' code before writing this package: `internal/
  matchingengineclient` has no depth-query method at all, and
  market-data's own HTTP API has no full-depth-ladder endpoint either,
  only trades/candles/an L1-only WS feed). The depth snapshot is therefore
  an explicit, CALLER-SUPPLIED request field — the same "the real
  computation exists, the live feed to source its input doesn't" pattern
  `internal/executionalgos`' VWAP/POV inputs already established.

- **Portfolio margining / cross-margining across correlated asset
  classes** (`internal/marginengine`'s `portfolioCrossMargining.go`,
  FEATURES.md §15): `POST /margin/calculate-portfolio-margin` extends the
  existing SPAN+exposure calculator with a real netting-benefit
  computation for a multi-position portfolio: for every pair of positions
  in DIFFERENT asset classes with OPPOSITE direction (long vs. short) and
  a POSITIVE illustrative correlation, `nettingBenefit = correlation *
  min(standaloneMarginA, standaloneMarginB)`, summed and subtracted from
  the portfolio's gross standalone margin — floored at the single largest
  standalone margin in the book (a real portfolio scheme never nets below
  what its riskiest single leg alone would require). 21 tests including a
  fully hand-worked two-leg example (long EQUITY ₹5,00,000 notional +
  short INDEX_FUTURES ₹4,00,000 notional, correlation 0.85 → gross
  margin ₹1,17,000, netting benefit exactly ₹44,200, net margin exactly
  ₹72,800), same-direction/same-asset-class/uncorrelated-pair zero-benefit
  cases, and the largest-standalone-margin floor. Verified live: the exact
  same hand-worked example returned `grossMarginInMinorUnits:117000,
  totalNettingBenefitInMinorUnits:44200,
  netPortfolioMarginInMinorUnits:72800` from the real running server.
  **LOUD, REPEATED KNOWN GAP**, same caliber as the pre-existing SPAN
  calculator's own warning: the correlation table is a MADE-UP,
  order-of-magnitude illustration, NOT a real historical-correlation
  study or exchange cross-margining eligibility list; same-asset-class
  netting isn't modeled at all (would need a real covariance-matrix / VaR
  calculation). NOT exchange-certified, NOT SEBI-compliant.

- **Securities Lending & Borrowing (SLB) desk**
  (`internal/securitieslendingborrowing`, FEATURES.md §15): a real,
  mutex-guarded state machine — the same pattern as `internal/
  marginpledge`'s `PledgeBook` — with TWO independent ledgers. `POST
  /securities-lending/lend` moves quantity of a symbol the lending
  account actually holds (server-looked-up from `internal/positions`,
  never client-asserted) out of its freely-sellable holding into a real
  `LendingRecord`; `POST /securities-lending/recall` reverses it
  (partially or fully). `POST /securities-lending/borrow` records a
  borrow — deliberately requiring NO prior holding (the entire point of
  borrowing, e.g. to cover a short); `POST /securities-lending/return`
  reverses it. `GET /securities-lending?accountId=...` shows both ledgers.
  A real, hand-checkable day-count fee-accrual formula
  (`CalculateIllustrativeAccruedFee`) mirrors `internal/marginfunding`'s
  own interest formula. 20 tests including accumulation across calls,
  insufficient-holding rejection, independent lending/borrowing ledgers on
  the SAME account/symbol, hand-worked full-year and half-year fee
  accrual, and a concurrency test proving exactly 50 of 100 simultaneous
  1-share lends succeed against a 50-share holding. Verified live: lending
  1 DEMO-EQ share (a real held position) succeeded and showed up in the
  status endpoint; lending more than held was genuinely rejected; a
  separate account borrowed 5 shares with zero prior holding; recall and
  return both correctly zeroed out their records.
  **Known gaps**: (1) illustrative lending/borrowing fee rates (2%/5%
  p.a.), not sourced from any real securities-lending market; (2) not
  wired into the order-submission SELL gate the way `internal/
  marginpledge`'s pledged quantity is (a lent-out holding can still be
  sold in this build — a real build would add the same
  `PLEDGED_QUANTITY_UNAVAILABLE`-style gate); (3) no lender-to-borrower
  matching/auction (two independent books, not a matched market).

- **Pre-market / post-market session support with distinct matching
  rules** (`internal/marketsession`'s `sessionPhaseRules.go`, FEATURES.md
  §15): a new, additive `SessionPhase` (`CLOSED`/`PRE_MARKET`/`REGULAR`/
  `POST_MARKET`, independent of the pre-existing `isMarketOpen` boolean
  that already drives AMO queueing — see the file's doc comment for why
  they're deliberately separate state) with REAL, enforced rules: `POST
  /market-session/set-phase` sets it, and `processOrderSubmission` (so
  EVERY order path — HTTP submit, DMA gateway, cover orders, multi-leg,
  baskets, auto-liquidation — inherits it for free) genuinely rejects any
  non-plain-LIMIT order (MARKET, SL/SL-M, ICEBERG/FOK/IOC) during
  `PRE_MARKET` or `POST_MARKET` with `SESSION_PHASE_RULE_VIOLATION` —
  mirroring a real exchange's pre-open call-auction and after-hours
  closing sessions, both order-collection-and-single-match windows where a
  market/stop order genuinely doesn't make sense. `REGULAR` and `CLOSED`
  are both no-ops for this NEW gate (`CLOSED`'s real rejection stays owned
  by the pre-existing `isMarketOpen`/AMO mechanism, avoiding two competing
  "is it closed" checks). 13 tests including phase-transition validation,
  full rule coverage per phase (every order shape allowed in REGULAR,
  only plain LIMIT in PRE_MARKET/POST_MARKET, CLOSED as a no-op for this
  gate), and independence from `isMarketOpen`. Verified live: setting
  `PRE_MARKET` genuinely rejected a MARKET order
  (`SESSION_PHASE_RULE_VIOLATION`) while a plain LIMIT order in the same
  phase was accepted and genuinely filled; switching to `REGULAR` then
  let the identical MARKET order through and fill.
  **Known gap**: an operator wanting fully realistic session behavior must
  set BOTH `isMarketOpen` (for AMO queueing) and `sessionPhase` (for these
  new rules) — they are not automatically coupled, a deliberate choice to
  avoid silently changing `isMarketOpen`'s pre-existing, already-tested
  behavior.

- **Dividend Reinvestment Plan (DRIP), auto-compounding toggle**
  (`internal/drip`, FEATURES.md §17): `POST /drip/toggle` sets a real,
  mutex-guarded per-account auto-reinvestment toggle (default OFF —
  unconfigured/untoggled accounts are unaffected, the same convention
  `internal/algolimits`/`internal/exposurelimits` already use). `POST
  /drip/process-dividend` looks up the account's REAL held quantity
  (`internal/positions`, never client-asserted), computes the exact
  dividend cash credit (`quantityHeld * dividendPerShareInMinorUnits`),
  posts it through a REAL balanced ledger journal entry
  (`internal/ledgerclient`'s new `PostDividendCreditJournalEntry`,
  mechanically mirroring the margin-funding disbursement pattern) and
  updates the local risk cache immediately — then, if auto-reinvest is
  ON and a reinvestment reference price was supplied, computes a real
  whole-share reinvestment quantity (`drip.CalculateReinvestmentQuantity`
  — exact integer division, leftover cash reported) and submits a REAL BUY
  order through the EXACT SAME `processOrderSubmission` pipeline every
  other order path reuses. 12 tests (in `internal/drip`) plus 2 new tests
  in `internal/ledgerclient` for the dividend journal entry, including
  hand-worked credit/reinvestment-quantity math and per-account toggle
  independence. Verified live: crediting a real ₹5.00/share dividend on a
  real 10-share holding credited exactly 500 minor units with auto-
  reinvest OFF; toggling ON and re-crediting the same dividend computed
  `reinvestmentQuantity:5` at a supplied ₹1.00 reference price
  (500/100 = 5 exactly) and genuinely submitted a real BUY order through
  the real pipeline.
  **Known gaps**: (1) no live price feed — the reinvestment reference
  price is caller-supplied, the same documented gap this codebase repeats
  everywhere; (2) whole-share reinvestment only — leftover cash isn't
  automatically carried forward to the next dividend event, and this
  package deliberately does NOT integrate with any fractional-share
  feature; (3) in-memory only.

- **Loan Against Securities (LAS)** (`internal/loanagainstsecurities`,
  FEATURES.md §17): modeled closely on `internal/marginfunding` (the same
  mutex-guarded two-phase reserve-then-disburse-then-rollback-on-failure
  flow), but a genuinely DISTINCT, longer-tenure loan product: capped at
  only `illustrativeLoanToValuePercent` (50%) of pledged collateral value
  — deliberately STRICTER than margin funding's own cap at the FULL
  pledged value — with a recorded (informational)
  `tenureInMonths`. `POST /loan-against-securities/request` reserves
  principal against the account's real pledged collateral
  (`internal/marginpledge`), then disburses REAL cash via a new
  `internal/ledgerclient.PostLoanAgainstSecuritiesDisbursementJournalEntry`
  balanced journal entry, rolling the reservation back if disbursement
  fails. `POST /loan-against-securities/repay` — unlike margin funding's
  own still-unwired repayment gap — posts a REAL repayment journal entry
  and pays down real outstanding principal, itself rolling back (via the
  new `RestorePrincipalAfterFailedRepaymentLedgerPosting`) if that ledger
  posting fails. `GET /loan-against-securities?accountId=...` shows
  outstanding principal, tenure, LTV cap, and remaining capacity. 21 tests
  including a hand-worked LTV cap (₹1,00,000 pledged × 50% = exactly
  ₹50,000 cap), exact-boundary-allowed, accumulation-caps-correctly,
  repayment, restore-after-failed-repayment, and a concurrency test
  proving exactly 50 of 100 simultaneous ₹10,000 requests succeed against
  a ₹5,00,000 LTV cap — plus 3 new `internal/ledgerclient` tests for the
  LAS disbursement/repayment journal entries. Verified live: pledging 5
  DEMO-EQ shares (₹42,500 real margin value) showed an exact
  `loanToValueCapInMinorUnits:21250` (50% of 42500); a ₹10,000 loan was
  approved (real disbursement, real outstanding-principal figure); a
  ₹9,00,000 request was genuinely rejected against the real cap; a
  ₹5,000 repayment correctly reduced outstanding principal to exactly
  ₹5,000.
  **Known gaps**: same caliber as `internal/marginfunding`'s own — (1)
  illustrative LTV (50%) and interest rate (9% p.a.) are made-up, not from
  any real lender's rate card; (2) `tenureInMonths` is recorded but not
  enforced by any clock-driven maturity/expiry logic (no real trading
  calendar exists in this build); (3) interest accrual is a real, tested
  formula but not wired to run automatically or post any interest journal
  entry; (4) reuses `firm-clearing-acct` rather than a dedicated LAS
  clearing account, the same ledger-account-provisioning boundary
  `internal/marginfunding` already documents; (5) in-memory only.

**Not attempted this round**: FEATURES.md §17's fractional share
investing (milli-share integer precision through order submission +
positions) — explicitly time-boxed out given its risk profile (touches
`internal/orders`' core `OrderQuantity uint64` field and every consumer of
it, with an explicit "do not break any existing integer-quantity test"
constraint) rather than attempted partially and left in an inconsistent
state. A future pass should introduce a `MilliShareQuantity` (or similar)
field alongside the existing integer `OrderQuantity` — additive, backward
compatible — and thread it through `internal/positions`' net-quantity
tracking and matching-engine's wire protocol, exactly the same additive
pattern `internal/orders`' `OrderExecutionType` field already used for
Iceberg/FOK/IOC.

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

# Mark-to-Market: push a current price, then check leveraged unrealized P&L
curl -X POST localhost:8081/mark-to-market/price -d '{
  "instrumentSymbol": "DEMO-EQ",
  "priceInMinorUnits": 11000
}'
curl "localhost:8081/mark-to-market?accountId=acct-001"

# Auto-liquidation: pure status read never acts, even at LIQUIDATION
curl "localhost:8081/auto-liquidation/status?accountId=acct-001"
# admin/scheduler-triggerable evaluate — only actually liquidates (real
# reducing MARKET SELL orders through the real order path) if genuinely
# at LIQUIDATION (100%+ utilization); a no-op at WARNING/URGENT/NORMAL
curl -X POST localhost:8081/auto-liquidation/evaluate -d '{
  "accountIdentifier": "acct-001"
}'

# Exposure limits: configure a real per-account + per-segment cap, then
# watch a breaching order get rejected with a specific, identifying reason
curl -X POST localhost:8081/exposure-limits/configure -d '{
  "accountIdentifier": "acct-001",
  "segment": "EQUITY",
  "accountLimitInMinorUnits": 50000,
  "segmentLimitInMinorUnits": 30000
}'
curl "localhost:8081/exposure-limits?accountId=acct-001&segment=EQUITY"

# Connectivity kill switch: manual admin engage/disengage halts/resumes
# ALL new order submissions (cancellation is never gated); an independent
# background prober also auto-engages/self-heals on real matching-engine
# connectivity — see internal/connectivitykillswitch's package doc.
curl "localhost:8081/connectivity-kill-switch/status"
curl -X POST localhost:8081/connectivity-kill-switch/engage -d '{
  "reason": "admin-initiated trading halt"
}'
curl -X POST localhost:8081/connectivity-kill-switch/disengage

# Execution algos — TWAP: 1000 shares over a 30-minute window in 4 slices
curl -X POST localhost:8081/execution-algos/twap/create -d '{
  "instrumentSymbol": "RELIANCE",
  "orderSideIsBuyNotSell": true,
  "totalQuantity": 1000,
  "startTime": "2026-08-14T10:00:00Z",
  "endTime": "2026-08-14T10:30:00Z",
  "numberOfSlices": 4
}'
# -> note "algoOrderId" in the response, then poll it forward:
curl -X POST localhost:8081/execution-algos/poll -d '{
  "algoOrderId": "TWAP-1",
  "now": "2026-08-14T10:15:00Z"
}'

# Execution algos — VWAP: weighted by a supplied historical volume curve
curl -X POST localhost:8081/execution-algos/vwap/create -d '{
  "instrumentSymbol": "INFY",
  "orderSideIsBuyNotSell": true,
  "totalQuantity": 1000,
  "volumeCurve": [
    {"bucketReleaseTime": "2026-08-14T09:15:00Z", "historicalVolumeWeight": 1},
    {"bucketReleaseTime": "2026-08-14T10:15:00Z", "historicalVolumeWeight": 4},
    {"bucketReleaseTime": "2026-08-14T11:15:00Z", "historicalVolumeWeight": 2},
    {"bucketReleaseTime": "2026-08-14T12:15:00Z", "historicalVolumeWeight": 3}
  ]
}'

# Execution algos — POV: 10% participation, capped at 50 units/slice
curl -X POST localhost:8081/execution-algos/pov/create -d '{
  "instrumentSymbol": "SBIN",
  "orderSideIsBuyNotSell": true,
  "totalQuantity": 1000,
  "participationRate": 0.1,
  "maxClipSizeQuantity": 50
}'
# -> note "algoOrderId", then feed real-time cumulative volume readings:
curl -X POST localhost:8081/execution-algos/pov/observe-volume -d '{
  "algoOrderId": "POV-3",
  "now": "2026-08-14T10:01:00Z",
  "cumulativeMarketVolume": 10300
}'
curl "localhost:8081/execution-algos/status?algoOrderId=POV-3"

# Options payoff diagram — long straddle: max loss 10, unbounded profit,
# breakevens at 90 and 110
curl -X POST localhost:8081/payoff-diagram/compute -d '{
  "legs": [
    {"optionType": "CALL", "strikePriceInMinorUnits": 100, "premiumInMinorUnits": 5, "isBuyNotSell": true, "quantity": 1},
    {"optionType": "PUT", "strikePriceInMinorUnits": 100, "premiumInMinorUnits": 5, "isBuyNotSell": true, "quantity": 1}
  ]
}'
# bull call spread: max profit 5, max loss 5, breakeven 105
curl -X POST localhost:8081/payoff-diagram/compute -d '{
  "legs": [
    {"optionType": "CALL", "strikePriceInMinorUnits": 100, "premiumInMinorUnits": 8, "isBuyNotSell": true, "quantity": 1},
    {"optionType": "CALL", "strikePriceInMinorUnits": 110, "premiumInMinorUnits": 3, "isBuyNotSell": false, "quantity": 1}
  ]
}'

# Multi-leg options: a valid long straddle -- both legs submitted
# atomically through the real order-submission path
curl -X POST localhost:8081/multileg-options/execute -d '{
  "clientAccountIdentifier": "acct-001",
  "strategy": "STRADDLE",
  "legs": [
    {"instrumentSymbol":"DEMO-EQ","optionType":"CALL","strikePriceInMinorUnits":100,"premiumInMinorUnits":50,"isBuyNotSell":true,"quantity":2},
    {"instrumentSymbol":"DEMO-EQ","optionType":"PUT","strikePriceInMinorUnits":100,"premiumInMinorUnits":50,"isBuyNotSell":true,"quantity":2}
  ]
}'

# Basket/program order: buy multiple constituents as one logical unit,
# capped by a net cash constraint
curl -X POST localhost:8081/basket-orders/execute -d '{
  "basketIdentifier": "basket-1",
  "clientAccountIdentifier": "acct-001",
  "netCashConstraintInMinorUnits": 100000,
  "constituents": [
    {"instrumentSymbol":"DEMO-EQ","isBuyNotSell":true,"quantity":5,"referencePriceInMinorUnits":10000}
  ]
}'

# Pre-trade impact-cost / slippage estimator: walk-the-book against a
# caller-supplied depth snapshot (see the honest "no live depth feed"
# gap in internal/impactcostestimator's package doc)
curl -X POST localhost:8081/impact-cost/estimate -d '{
  "snapshot": {
    "instrumentSymbol": "DEMO-EQ",
    "bidLevels": [{"priceInMinorUnits":9900,"quantity":10},{"priceInMinorUnits":9800,"quantity":20}],
    "askLevels": [{"priceInMinorUnits":10000,"quantity":10},{"priceInMinorUnits":10100,"quantity":20}]
  },
  "isBuyNotSell": true,
  "hypotheticalQuantity": 15
}'

# Portfolio margining / cross-margining -- illustrative correlation table,
# NOT exchange-certified
curl -X POST localhost:8081/margin/calculate-portfolio-margin -d '{
  "positions": [
    {"assetClass":"EQUITY","contractNotionalValueInMinorUnits":500000,"isLongNotShort":true},
    {"assetClass":"INDEX_FUTURES","contractNotionalValueInMinorUnits":400000,"isLongNotShort":false}
  ]
}'

# Securities Lending & Borrowing desk
curl -X POST localhost:8081/securities-lending/lend -d '{
  "clientAccountIdentifier":"acct-001","instrumentSymbol":"DEMO-EQ","quantity":1,"referencePriceInMinorUnits":10000
}'
curl -X POST localhost:8081/securities-lending/borrow -d '{
  "clientAccountIdentifier":"acct-003","instrumentSymbol":"DEMO-EQ","quantity":5,"referencePriceInMinorUnits":10000
}'
curl "localhost:8081/securities-lending?accountId=acct-001"

# Pre-market/post-market session rules: only plain LIMIT orders accepted
curl -X POST localhost:8081/market-session/set-phase -d '{"phase":"PRE_MARKET"}'
curl -X POST localhost:8081/orders/submit -d '{
  "clientAccountIdentifier":"acct-001","instrumentSymbol":"DEMO-EQ","orderSideIsBuyNotSell":true,
  "orderIsMarketOrderNotLimit":true,"orderQuantity":1
}'
# -> rejected with SESSION_PHASE_RULE_VIOLATION; a plain LIMIT order in
# the same phase is accepted
curl -X POST localhost:8081/market-session/set-phase -d '{"phase":"REGULAR"}'

# DRIP: toggle auto-reinvest, then process a real dividend event
curl -X POST localhost:8081/drip/toggle -d '{"clientAccountIdentifier":"acct-001","enabled":true}'
curl -X POST localhost:8081/drip/process-dividend -d '{
  "clientAccountIdentifier":"acct-001","instrumentSymbol":"DEMO-EQ","dividendPerShareInMinorUnits":50,
  "reinvestmentReferencePriceInMinorUnits":100
}'

# Loan Against Securities: pledge collateral first (see the margin-pledge
# example above), then draw a longer-tenure loan capped at a stricter
# loan-to-value ratio than margin funding's own cap
curl -X POST localhost:8081/loan-against-securities/request -d '{
  "clientAccountIdentifier":"acct-001","requestedAmountInMinorUnits":10000,"tenureInMonths":12
}'
curl "localhost:8081/loan-against-securities?accountId=acct-001"
curl -X POST localhost:8081/loan-against-securities/repay -d '{
  "clientAccountIdentifier":"acct-001","amountInMinorUnits":5000
}'

## Round N additions: behavioral nudges, F&O disclosure gating, square-off countdown, live interest cost, stress test, large-order friction, liquidity badge, idempotent reconciliation, corporate-action explainer, and fractional shares

Ten more FEATURES.md items, built and verified against a real running
server (and, where money genuinely moved, a real running `ledger` and
`matching-engine` too). Every package below follows the existing
convention: sentinel errors, mutex-guarded state, explicit `now`
parameters (never an internal wall-clock read except at the one real
call site in `cmd/server/main.go`), and a loud, honest "known gaps"
section.

- **Fractional share investing** (`internal/fractionalshares`,
  FEATURES.md §17 — previously explicitly deferred for its blast
  radius): a documented MILLI-SHARE INTEGER precision scheme (1000 =
  1.000 share), NOT float — exact integer arithmetic throughout,
  including round-half-up notional computation
  (`NotionalInMinorUnits`). Per the previous round's own recommendation,
  this is purely ADDITIVE: a new, optional `MilliShareQuantity *uint64`
  field on `orders.OrderSubmissionRequest`, layered alongside the
  pre-existing `OrderQuantity uint64` (whole-share) field, which is
  completely untouched — every pre-existing test, consumer, and client
  that never sets the new field is 100% unaffected (verified: the full
  `internal/orders` and `internal/positions` test suites, and the whole
  service's `go test ./...`, stayed green throughout this change).
  **HONEST, LOUD, LOAD-BEARING SCOPE BOUNDARY**: matching-engine's wire
  protocol (`internal/matchingengineclient`) has NO milli-share-precision
  field — extending it is out of this service's boundary (a different,
  Rust-written service). A fractional order is therefore only genuinely
  FILLABLE through paper trading (`internal/papertrading`'s new
  `SimulateFractionalFill`, additive alongside the pre-existing
  `SimulateFill`) — `fractionalshares.ValidateMilliShareQuantity`, called
  from `buildSubmitOrderHandler` before any gate runs, REJECTS a live
  (non-paper) order that sets `MilliShareQuantity` with a clear 400 and a
  plain-language reason, rather than silently rounding/truncating it into
  a whole-share live order (which would be a real financial-correctness
  bug: ask for 0.3 shares, get charged for 1). A fractional paper fill
  updates a brand-new, completely separate `fractionalshares.
  MilliSharePositionBook` (mirroring `internal/positions.PositionBook`'s
  own signed-net-quantity design exactly) — never the existing whole-share
  `paperPositionBook` — via `POST /orders/submit` (with `isPaperTradingOrder:
  true` and `milliShareQuantity` set) and read via `GET /paper-positions/
  fractional?accountId=...`. The pre-trade risk check's notional is
  computed with the milli-share-aware formula whenever `MilliShareQuantity`
  is set (real integer math, not the whole-share formula). 21 tests
  across `internal/fractionalshares` (hand-worked notional cases including
  round-half-up/round-half-down boundaries, whole/milli-part formatting,
  position-book accumulation) plus 6 more in `internal/papertrading` for
  `SimulateFractionalFill` and 3 more in `internal/positions` for the new
  additive `SetPositionDirectly` method (used by item 10 below, not this
  one). Verified live: a live (non-paper) order with `milliShareQuantity`
  set was rejected with the exact documented error; a zero
  `milliShareQuantity` was rejected even for a paper order; a paper BUY of
  0.300 share then another of 0.250 share against a real running server
  left `netMilliShareQuantity:550` (exactly 0.550 share, integer-exact,
  no float drift) in `/paper-positions/fractional`, while `/paper-positions`
  (the whole-share book) stayed completely empty throughout — proving the
  two books never cross-contaminate.
  **Known, loudly documented gaps**: (1) `internal/chargescalculator` and
  `internal/marginengine`'s SPAN/exposure calculators still operate on
  whole-share `OrderQuantity` only — a fractional order's charges/margin
  estimate is not yet fractional-aware, out of this round's time budget;
  (2) live (matching-engine-routed) fractional orders remain unsupported
  by design until matching-engine itself gains a milli-share field; (3)
  in-memory only, same as every other package in this service.

- **Overtrading / revenge-trading pattern detection with cool-down
  nudges** (`internal/overtradingdetection`, FEATURES.md §19): a real
  pattern detector over an account's own real recent order-submission
  history — RAPID-FIRE BURST (a configurable minimum order count within
  a recent window with an unusually small gap between the two most
  recent submissions) and ELEVATED ORDER VELOCITY (recent-window order
  count exceeding a multiple of the account's OWN longer-lookback
  baseline rate, never a cross-account population baseline). This is a
  real, NON-BLOCKING behavioral nudge — `POST /orders/submit`'s handler
  records every real submission attempt (accepted or rejected) and
  evaluates the detector AFTER `processOrderSubmission` runs, attaching
  an `overtradingNudge` field to the response without ever touching
  `wasOrderAccepted`. A real, queryable, mutex-guarded cool-down period
  (15 minutes, illustrative default) is armed the instant a nudge fires
  and suppresses repeat nudges until it expires — `GET
  /overtrading-detection/status?accountId=...` is a pure read. **Honest
  scope boundary**: a textbook "revenge trading" definition ties the
  burst specifically to a REALIZED LOSS, which this codebase has no
  event feed for (no realized-P&L/trade-journal stream exists anywhere
  in oms-gateway) — this detector uses order-submission VELOCITY as the
  practical, real-data-driven proxy instead, loudly documented in the
  package doc as not equivalent to a true loss-triggered detector. 15
  tests including exact-boundary tests for both patterns, a
  cooldown-suppresses-then-expires-and-refires test, and a concurrency
  test. Verified live: 7 rapid order submissions to a real running
  server armed a real cool-down (`isInCooldown:true,
  recentOrderCount:7`), with the 5th response itself carrying a
  `RAPID_FIRE_BURST` nudge inline.

- **Mandatory F&O risk disclosure + cooling-off gate**
  (`internal/riskdisclosuregate`, FEATURES.md §19): real per-account
  acknowledgement state (`POST /risk-disclosure/acknowledge`) and a real
  gate, run in `processOrderSubmission` right after the pledged-holding
  gate, that REJECTS an account's FIRST-EVER F&O order
  (`FNO_RISK_DISCLOSURE_REQUIRED`) until it has acknowledged AND at
  least a configurable cooling-off duration (24 hours, illustrative
  default) has genuinely elapsed since that acknowledgement — enforced
  with an explicit `now` parameter throughout, exactly reproducible in
  tests, never the wall clock internally except at the one real call
  site. F&O classification reuses `internal/exposurelimits.
  ClassifySegment` verbatim (not reimplemented) — an equity order is
  never gated by this package at all. Once an account's first F&O order
  is genuinely accepted, it's permanently exempted from the gate going
  forward (a one-time onboarding friction, not a check re-run on every
  order) — the exemption is recorded only after the order clears every
  earlier gate, so a check for an order that's later rejected downstream
  never wrongly consumes the milestone. 14 tests including an
  exact-cooling-off-boundary pair (23h59m59s still rejected, exactly 24h
  passes) and a permanently-exempt-after-first-order test. Verified
  live against a real running server: an F&O order for a
  never-acknowledged account was rejected with the disclosure-required
  reason; immediately after acknowledging, the SAME order was still
  rejected (cooling-off not yet elapsed, since the test used the real
  wall clock); a plain equity order for a different never-acknowledged
  account was never touched by this gate at all (it failed only on the
  unrelated, pre-existing KYC check).

- **Intraday auto square-off countdown timer + reminder-eligibility
  check** (`internal/marketsession/squareOffCountdown.go`, FEATURES.md
  §21): extends `internal/marketsession` — real countdown math
  (`ComputeSquareOffCountdown`, given a real configured cutoff
  time-of-day and the current time, computes real remaining seconds,
  floored at 0 once cutoff has passed) and a real, mutex-guarded,
  DEDUP'D reminder-eligibility check
  (`SquareOffReminderTracker.DueReminders`): each configured threshold
  (30/15/5 minutes before cutoff, illustrative default) fires AT MOST
  ONCE per (account, trading-day cutoff instant) pair, so repeated
  polling (the real shape a frontend push-notification poller would use)
  never double-fires the same reminder — proven by a dedicated
  never-refires test and a concurrency test (50 simultaneous pollers at
  the same instant produce exactly 3 total firings, not 150).
  `GET /market-session/square-off/countdown[?now=RFC3339]` and `GET
  /market-session/square-off/reminders?accountId=...[&now=RFC3339]` are
  the two new endpoints; both accept an optional `now` override (falling
  back to the real wall clock) so they're exactly testable live too. 16
  tests including exact-threshold-boundary cases, a
  multiple-thresholds-crossed-at-once-all-fire-together case (a client
  polling for the first time with only 3 minutes left correctly gets all
  three reminders in one response), and per-account/per-trading-day
  independence. Verified live: a pinned `now` 25 minutes before the
  default 15:20 UTC cutoff correctly fired the 30-minute reminder (25 <
  30); a pinned `now` with 4 minutes left fired all three thresholds at
  once on first poll; an immediate repeat poll for the same account/time
  returned zero NEW reminders; a `now` past cutoff showed
  `isPastCutoff:true, remainingSeconds:0`. **Scope boundary, stated
  loudly**: this is the backend countdown/reminder-eligibility SIGNAL
  only — it does not itself force a closure (that's
  `internal/autoliquidation`'s different, margin-breach-triggered
  concern) and does not deliver an actual push notification (out of
  oms-gateway's boundary per the task's own framing).

- **Margin/leverage interest cost calculator shown live**
  (`internal/marginfunding/costCalculator.go`, FEATURES.md §21): extends
  `internal/marginfunding` with a real "cost so far" figure
  (`CalculateCostSoFarInMinorUnits`, interest genuinely accrued from a
  new, real, mutex-guarded `disbursementStartTimeByAccount` — recorded
  once per account's first outstanding draw, cleared automatically the
  moment `RepayFunding` brings principal back to exactly zero so a
  future fresh draw restarts the clock) AND a real "projected cost if
  held N more days" figure (`CalculateProjectedCostInMinorUnits`) — both
  reusing the EXACT SAME `CalculateIllustrativeAccruedInterest`
  simple-interest formula this package already had; no new rate, no new
  model. `GET /margin-funding/interest-cost?accountId=...[&now=RFC3339]
  [&projectDays=N]` returns both figures plus the incremental projected
  cost (`ProjectedTotalCostInMinorUnits - CostSoFarInMinorUnits`). 18
  tests including a floored-partial-day case and full disbursement-
  start-time lifecycle coverage (first draw sets it, a second draw on
  top doesn't reset it — a documented blended-principal simplification —
  full repayment clears it, partial repayment doesn't, a fresh draw
  after full repayment genuinely restarts the clock). Verified live end-
  to-end with REAL money movement through a real running `ledger`: seeded
  ₹10,000.00 into `acct-001` via a real journal entry, bought and matched
  50 DEMO-EQ shares against a real running `matching-engine`, pledged 20
  of them (₹1,700.00 margin), drew ₹1,000.00 margin funding (a real
  ledger disbursement), then queried interest cost at a pinned `now` 30
  days later projecting 30 more days — got back exactly
  `costSoFarInMinorUnits:986` and `projectedTotalCostInMinorUnits:1973`,
  matching the hand-worked 12%-p.a.-simple-interest formula to the paisa
  (₹9.86 at 30 days, ₹19.73 at 60 days).
  **Known gap**: same blended-single-start-time-per-account
  simplification the struct field's own doc comment states loudly — a
  real build would track each disbursement tranche's own start date.

- **Portfolio stress test** (`internal/portfoliostresstest`, FEATURES.md
  §21): given an account's real positions (equity from
  `internal/positions`, options supplied by the caller in the same shape
  `internal/optionschain`'s live Greeks already produce) and a
  hypothetical market-wide shock percentage, computes a real estimated
  P&L impact. Equity math is EXACT (`netQuantity * currentPrice *
  shockPercent` — a position's value genuinely does move linearly with
  its own price); option math is an explicit, LOUDLY documented
  FIRST-ORDER DELTA APPROXIMATION (`netContracts * contractMultiplier *
  deltaPerContract * (underlyingPrice * shockPercent)`) — NOT a full
  Black-Scholes repricing at the shocked price (ignores gamma/convexity/
  IV changes a real large shock would also cause), with every
  per-position result carrying an explicit `isFirstOrderApproximation`
  flag so a client can distinguish exact equity impact from
  approximated option impact. `POST /portfolio-stress-test/compute`
  looks up the account's REAL equity positions server-side from
  `internal/positions` (never client-asserted quantities) — the caller
  only supplies each held instrument's current price (no live price feed
  exists, the same documented gap this codebase repeats everywhere) and
  any option legs directly (no server-side options-position book exists
  in this build). 13 tests including hand-worked long/short equity and
  option cases and a mixed-portfolio summation case. Verified live: a
  real 50-share DEMO-EQ position (bought and matched against a real
  running matching-engine) plus a synthetic 10-contract delta-0.5 option
  leg, under a -10% shock, returned exactly `-50000` (equity) and
  `-5000` (option) for a total of `-55000` paise (₹550.00) — matching
  the hand-worked numbers exactly.

- **Large-order friction** (`internal/largeorderfriction`, FEATURES.md
  §21): a real comparison of an incoming order's notional against the
  account's OWN real historical average order size — a new, real,
  mutex-guarded `Tracker` fed by every order that actually proceeds past
  the gate (the same "fed by the real order-submission path" pattern
  `internal/overtradingdetection` already established), not audit-trail
  parsing. An order exceeding a configurable multiple (5x, illustrative
  default) of the account's own average — once it has enough history to
  have a meaningful baseline — OR a configurable fraction (10%,
  illustrative default) of a caller-supplied average-instrument-volume
  figure, is SOFT-rejected with `LARGE_ORDER_CONFIRMATION_REQUIRED` and
  a clear, specific plain-language reason. This is genuinely NOT a
  permanent block: the client resubmits the EXACT SAME order with a new,
  additive `confirmedLargeOrder: true` field on
  `orders.OrderSubmissionRequest` (mirroring the `IsPaperTradingOrder`
  precedent) and the friction gate steps aside. Checked early in
  `processOrderSubmission`, right after the exposure-limits gate and
  before KYC. 15 tests including exact-multiplier-boundary cases,
  independent account/volume triggers, a combined-both-reasons message
  test, and a proof that `EvaluateOrder` never itself mutates history
  (only a genuinely-proceeding order does, via a separate
  `RecordOrderNotional` call). Verified live against a real running
  server: 5 baseline orders of 100 paise notional each, then a
  100,000x-larger order was soft-rejected with the exact multiplier and
  average called out in the message; the identical order resubmitted
  with `confirmedLargeOrder:true` cleared the friction gate (it was then
  rejected only by the unrelated, pre-existing KYC check, proving the
  friction gate genuinely stepped aside rather than blocking forever).

- **Liquidity/fill-probability badge** (`internal/liquiditybadge`,
  FEATURES.md §21): given real order book depth — REUSING
  `internal/impactcostestimator.OrderBookDepthSnapshot` verbatim, not
  reimplementing it — computes a real liquidity classification
  (HIGH/MEDIUM/LOW, based on real summed depth-at-price on the order's
  relevant side against configurable thresholds) and an
  ILLUSTRATIVE expected-time-to-fill estimate, explicitly flagged
  `isIllustrativeEstimate:true` on every response — loudly documented as
  a directionally-sensible formula (deeper book relative to order size
  scales the estimate down), NOT a real ML-fitted model trained on
  actual historical fill data, which this codebase has none of. `POST
  /liquidity-badge/compute` takes the same snapshot/side/quantity shape
  `POST /impact-cost/estimate` already uses. 13 tests including
  exact-classification-boundary cases, a hand-worked time-to-fill
  scaling case (a 100-unit order against 50 units of LOW-classified
  depth scales the 30s base estimate to exactly 60s), and a
  zero-depth-is-maximally-illiquid case. Verified live: a real 30-unit
  depth book classified LOW with a 30s estimate; a thin 5-unit book
  under a 50-unit hypothetical order classified LOW with a 300s
  estimate (10x the base, matching the 10x depth-consumption ratio
  exactly).

- **Idempotent order status with reconnect reconciliation** — verified
  and extended, not rebuilt: `internal/idempotency` already substantially
  covered this via replay-by-resubmission. The real, genuine gap this
  round closed: a client shouldn't have to resubmit the FULL order body
  just to ask "what happened while I was disconnected" — it may not even
  have the original request handy after a reconnect. `Reconcile`
  (`internal/idempotency/reconciliation.go`) is a new, PURE, NON-BLOCKING
  read by idempotency key alone — never claims a key, never waits on the
  owner's completion channel, safe to call any number of times (e.g. on
  every WS reconnect) — returning one of three real states: `UNKNOWN`
  (never claimed), `IN_PROGRESS` (owner still working), or `COMPLETED`
  (the real, final `OrderAcknowledgementResponse`, identical across every
  repeated call — the same idempotency guarantee a full resubmission
  already provided, without requiring one). `GET /orders/reconcile?
  idempotencyKey=...` is the new endpoint. 12 new tests (16 total in the
  package) including a never-blocks-while-in-progress test (a goroutine
  proving `Reconcile` returns promptly even while the real owner is still
  working), a repeated-calls-return-identical-response test, and a
  concurrent-reconcile-and-complete race test. Verified live: an unknown
  key returned `{"status":"UNKNOWN"}`; submitting a real order with a key
  then reconciling that key returned `{"status":"COMPLETED","response":
  {...}}` with the EXACT SAME response body the original submission got,
  reproduced identically on a repeat query.

- **Corporate-action explainer** (`internal/corporateactionexplainer`,
  FEATURES.md §21 — explicitly NOT §14 corporate-actions processing;
  **update from a later build**: §14 corporate-actions processing DOES
  now exist, in `internal/corporateactionsprocessing` — see that
  package's own section further below. This explainer package is
  unchanged and still does exactly what this paragraph originally
  described: narrate a caller-supplied outcome, never compute one):
  given a (today always manual/synthetic, since there's no real
  corporate-actions feed — loudly documented) quantity/average-price
  adjustment event on a position, generates a real, accurate,
  human-readable one-line explanation reflecting the ACTUAL supplied
  before/after numbers — not a canned template. `Explain` computes an
  exact quantity ratio (e.g. a genuine 1:2 split reads "quantity ratio
  2.00x", real numbers, not a vague "your quantity changed") and adapts
  its phrasing depending on whether the average price also changed. A
  new, real, additive `positions.PositionBook.SetPositionDirectly` method
  (mirroring `ApplyFill`'s existing style, but an absolute overwrite
  instead of a delta — used ONLY by this feature, never by ordinary
  trading flow) lets `POST /positions/corporate-action-adjustments/apply`
  genuinely update the account's real position AND record the real
  explanation together, exposed via the positions API as the task
  specified; `GET /positions/corporate-action-adjustments[?accountId=...]`
  is a pure read of the real, append-only explainer log (mirrors
  `internal/audittrail`'s own no-update/no-delete convention). 15 tests
  in the new package plus 3 more in `internal/positions` for
  `SetPositionDirectly`. Verified live: applying a 1:2 stock split
  adjustment (10→20 shares, avg ₹200.00→₹100.00) to a real, empty
  `acct-ca` position produced the exact explanation `"A stock split
  changed your DEMO-EQ holding from 10 shares @ avg ₹200.00 to 20 shares
  @ avg ₹100.00 (quantity ratio 2.00x) -- your total invested value is
  unchanged, only how it's split across shares."`, and `GET /positions`
  immediately reflected the real new quantity of 20 — both the real
  position mutation and the real explanation are genuinely linked, not
  two independent stubs.

### Curl examples for this round's ten additions

```bash
# Fractional shares: a live (non-paper) order with milliShareQuantity is
# genuinely REJECTED (matching-engine has no milli-share field yet)
curl -X POST localhost:8081/orders/submit -d '{
  "clientAccountIdentifier":"acct-001","instrumentSymbol":"DEMO-EQ",
  "orderSideIsBuyNotSell":true,"limitPriceInMinorUnits":10000,
  "orderQuantity":1,"milliShareQuantity":300
}'
# -> 400: "fractional share orders ... only supported for paper trading"

# A paper order buying 0.300 share, then 0.250 more -- exact integer sum
curl -X POST localhost:8081/orders/submit -d '{
  "clientAccountIdentifier":"acct-002","instrumentSymbol":"DEMO-EQ",
  "orderSideIsBuyNotSell":true,"isPaperTradingOrder":true,
  "limitPriceInMinorUnits":10000,"orderQuantity":1,"milliShareQuantity":300
}'
curl "localhost:8081/paper-positions/fractional?accountId=acct-002"

# Overtrading nudge status (non-blocking; never rejects an order itself)
curl "localhost:8081/overtrading-detection/status?accountId=acct-001"

# F&O risk disclosure: acknowledge, then check status
curl -X POST localhost:8081/risk-disclosure/acknowledge -d '{
  "clientAccountIdentifier":"acct-001"
}'
curl "localhost:8081/risk-disclosure/status?accountId=acct-001"

# Square-off countdown + reminder-eligibility check (optional pinned `now`)
curl "localhost:8081/market-session/square-off/countdown?now=2026-08-14T14:55:00Z"
curl "localhost:8081/market-session/square-off/reminders?accountId=acct-001&now=2026-08-14T15:16:00Z"

# Live margin-funding interest cost: cost so far + projected cost
curl "localhost:8081/margin-funding/interest-cost?accountId=acct-001&projectDays=30"

# Portfolio stress test: -10% shock on real equity + supplied option legs
curl -X POST localhost:8081/portfolio-stress-test/compute -d '{
  "clientAccountIdentifier":"acct-001",
  "shockPercent":-0.10,
  "equityCurrentPricesInMinorUnits":{"DEMO-EQ":10000},
  "optionPositions":[
    {"instrumentSymbol":"DEMO-EQ-CALL","positionType":"OPTION","netContracts":10,"contractMultiplier":1,"deltaPerContract":0.5,"underlyingCurrentPriceInMinorUnits":10000}
  ]
}'

# Large-order friction: soft-reject, then resubmit with confirmation
curl -X POST localhost:8081/orders/submit -d '{
  "clientAccountIdentifier":"acct-001","instrumentSymbol":"DEMO-EQ",
  "orderSideIsBuyNotSell":true,"limitPriceInMinorUnits":100,"orderQuantity":100000
}'
curl -X POST localhost:8081/orders/submit -d '{
  "clientAccountIdentifier":"acct-001","instrumentSymbol":"DEMO-EQ",
  "orderSideIsBuyNotSell":true,"limitPriceInMinorUnits":100,"orderQuantity":100000,
  "confirmedLargeOrder":true
}'

# Liquidity badge for an illiquid instrument
curl -X POST localhost:8081/liquidity-badge/compute -d '{
  "snapshot": {"instrumentSymbol":"THIN-STOCK",
    "bidLevels":[{"priceInMinorUnits":9900,"quantity":5}],
    "askLevels":[{"priceInMinorUnits":10000,"quantity":5}]},
  "isBuyNotSell": true,
  "hypotheticalQuantity": 50
}'

# Idempotent reconciliation: what happened to order X while disconnected
curl -X POST localhost:8081/orders/submit -d '{
  "clientAccountIdentifier":"acct-001","instrumentSymbol":"DEMO-EQ",
  "orderSideIsBuyNotSell":true,"limitPriceInMinorUnits":100,"orderQuantity":1,
  "idempotencyKey":"reconnect-test-1"
}'
curl "localhost:8081/orders/reconcile?idempotencyKey=reconnect-test-1"

# Corporate-action explainer: apply a synthetic 1:2 split, get the real
# one-line explanation, see the real position reflect it immediately
curl -X POST localhost:8081/positions/corporate-action-adjustments/apply -d '{
  "clientAccountIdentifier":"acct-001","instrumentSymbol":"DEMO-EQ",
  "actionType":"STOCK_SPLIT","quantityBefore":10,"quantityAfter":20,
  "averagePriceBeforeInMinorUnits":20000,"averagePriceAfterInMinorUnits":10000
}'
curl "localhost:8081/positions/corporate-action-adjustments?accountId=acct-001"
```

## Real FEATURES.md §14 corporate actions processing (`internal/corporateactionsprocessing`)

The REAL implementation the item above's explainer explicitly punted
on: given a real corporate-action input (a split ratio, a bonus ratio, a
merger exchange ratio, or a cash-dividend-per-share amount), this
package COMPUTES the correct new quantity and total cost basis itself
using real accounting rules, rather than narrating a caller-supplied
outcome. 13 tests in `internal/corporateactionsprocessing`, asserting
exact post-action position/cost-basis numbers (not just "it changed").

Accounting rules implemented, all with `HoldingsBook`'s own cost-basis-
aware store (`positions.PositionBook` has no cost-basis field at all —
see that package's doc — so this feature owns its own `Holding{
QuantityHeld, TotalCostBasisInMinorUnits }` store, syncing quantity into
`positions.PositionBook.SetPositionDirectly` after every mutation so
ordinary `GET /positions` immediately reflects it):

- **Stock split** (e.g. 2:1): quantity multiplies by the ratio, total
  cost basis is UNCHANGED. Verified: 10 shares @ ₹10,000.00 total cost
  basis, 2:1 split -> 20 shares, cost basis still ₹10,000.00, average
  cost per share exactly halves (₹1,000.00 -> ₹500.00).
- **Bonus issue** (e.g. 1:1): additional free shares are added
  (quantity * (1 + ratio)), total cost basis UNCHANGED (the same
  dilution effect as a split, different issuer mechanism). Verified: 10
  shares @ ₹10,000.00, 1:1 bonus -> 20 shares, cost basis still
  ₹10,000.00.
- **Merger / share exchange** (e.g. 2 old shares -> 1 new share): the
  quantity converts by the exchange ratio into the acquirer instrument
  and the ENTIRE prior cost basis carries over unchanged; if the account
  already independently holds some of the acquirer, the converted
  position is ADDED onto it (not overwritten), and the old instrument's
  holding is removed entirely (from both `HoldingsBook` and
  `positions.PositionBook`, the latter zeroed via `SetPositionDirectly`).
  A merger ratio that doesn't divide the held quantity evenly is
  REJECTED (`ErrExchangeRatioProducesFractionalShares`), never silently
  truncated — fractional-share cash-in-lieu is an honest, documented
  gap, not implemented.
- **Cash dividend** (e.g. ₹5.00/share): holding quantity and cost basis
  are COMPLETELY UNCHANGED. The total dividend
  (quantity * perShare) is credited to the account's REAL ledger balance
  via a genuine HTTP call — `ledgerclient.PostDividendCreditJournalEntry`
  posting a real balanced journal entry to ledger's `/journal-entries`
  endpoint — never a local bookkeeping fiction. If the ledger call fails,
  the handler returns `502` (nothing was mutated locally for a dividend
  to roll back).

Endpoints:

```
POST /corporate-actions/holdings/seed   {clientAccountIdentifier, instrumentSymbol, quantity, totalCostBasisInMinorUnits}
GET  /corporate-actions/holdings?accountId=...&instrument=...
POST /corporate-actions/process         {actionType, clientAccountIdentifier, instrumentSymbol,
                                          ratioNumerator, ratioDenominator,      // split / bonus / merger
                                          mergerTargetInstrumentSymbol,          // merger only
                                          dividendPerShareInMinorUnits}          // cash dividend only
GET  /corporate-actions/processed-actions?accountId=...
```

**Verified live** against a real running `ledger` + `oms-gateway`:
seeded `acct-001` 10 RELIANCE shares @ ₹10,000.00 total cost basis;
applied a 2:1 split via `POST /corporate-actions/process` and confirmed
both `GET /corporate-actions/holdings` (20 shares, ₹10,000.00 cost
basis, ₹500.00 avg) AND `GET /positions` (20 shares) reflected it
immediately; applied a ₹5.00/share cash dividend on the resulting 20
shares and confirmed `ledger`'s real `GET /accounts/balance` rose by
exactly 10,000 minor units (0 -> 10,000) while the holding itself stayed
completely untouched (still 20 shares, ₹10,000.00 cost basis).

```bash
# Seed a starting holding
curl -X POST localhost:8081/corporate-actions/holdings/seed -d '{
  "clientAccountIdentifier":"acct-001","instrumentSymbol":"RELIANCE",
  "quantity":10,"totalCostBasisInMinorUnits":1000000
}'

# Real 2:1 stock split
curl -X POST localhost:8081/corporate-actions/process -d '{
  "actionType":"STOCK_SPLIT","clientAccountIdentifier":"acct-001",
  "instrumentSymbol":"RELIANCE","ratioNumerator":2,"ratioDenominator":1
}'
curl "localhost:8081/corporate-actions/holdings?accountId=acct-001&instrument=RELIANCE"
curl "localhost:8081/positions?accountId=acct-001"

# Real cash dividend -- genuinely credits ledger
curl -X POST localhost:8081/corporate-actions/process -d '{
  "actionType":"CASH_DIVIDEND","clientAccountIdentifier":"acct-001",
  "instrumentSymbol":"RELIANCE","dividendPerShareInMinorUnits":500
}'
curl "http://127.0.0.1:8082/accounts/balance?accountId=acct-001"

# Real merger: 2 old shares -> 1 new share, full cost basis carries over
curl -X POST localhost:8081/corporate-actions/holdings/seed -d '{
  "clientAccountIdentifier":"acct-001","instrumentSymbol":"OLDCO",
  "quantity":10,"totalCostBasisInMinorUnits":1000000
}'
curl -X POST localhost:8081/corporate-actions/process -d '{
  "actionType":"MERGER","clientAccountIdentifier":"acct-001",
  "instrumentSymbol":"OLDCO","mergerTargetInstrumentSymbol":"NEWCO",
  "ratioNumerator":1,"ratioDenominator":2
}'
curl "localhost:8081/corporate-actions/processed-actions?accountId=acct-001"
```
