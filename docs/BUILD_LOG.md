# Build Log (append-only)

Chronological record of every build step taken on this repo. Each entry is
appended, never edited or reordered — if something changes later, a new
entry says so and points back at the old one. For a synthesized,
non-chronological reference of what exists, see `DOCUMENTATION.md`.

---

## Entry 1 — Root repo scaffolding

- `git init` in `projects/trading-app` (own repo, matching sibling-project
  convention in this workspace).
- `.gitignore` covering Rust (`target/`), Go (`bin/`), Node
  (`node_modules/`, `.next/`), Tauri, Python (`.venv/`, `__pycache__/`),
  env/secrets, OS/editor cruft, local docker-compose volumes.
- `README.md` — top-level orientation, repo layout tree, local dev
  quickstart, explicit statement that this is a scaffold (services build
  and health-check but business logic is largely stubbed).
- `Makefile` — `infra-up`/`infra-down` (docker-compose), `build`/`build-*`
  per language, `dev-*` per service, `test`, `fmt`, `clean`.
- `infra/docker/docker-compose.yml` — local dev infra: Postgres 16
  (ledger), Redpanda (Kafka-compatible event backbone), ClickHouse (tick
  storage). Commented as dev-only, not representative of the production
  topology in `ARCHITECTURE.md` §7.

## Entry 2 — services/matching-engine (Rust)

Tier 0 skeleton. Files:
- `Cargo.toml` — package `matching_engine`, edition 2024, no external deps
  (std-only, deliberately, to keep the hot-path skeleton dependency-free).
- `src/orderTypes.rs` — domain types: `OrderSide`, `IncomingOrderRequest`,
  `RestingLimitOrder`, `TradeExecutionEvent`. Integer minor-unit pricing
  throughout (no floats for money).
- `src/orderBookCore.rs` — `OrderBookCore`: single-instrument,
  single-threaded, price-time-priority limit order book over two
  `BTreeMap<i64, VecDeque<RestingLimitOrder>>` (bids descending, asks
  ascending). Real matching algorithm: an aggressive order walks price
  levels and FIFO queues at each level until exhausted or no longer
  crossing, partial remainder rests on the book. 3 passing unit tests
  (crossing match, partial fill, non-crossing rest).
- `src/main.rs` — demo driver with a hardcoded 3-order sequence, prints
  trades and final book depth. Explicitly not the real ingress path.
- `README.md` — status (what's real vs. placeholder), run instructions.
- Verified: `cargo build`, `cargo test` (3/3 pass), `cargo run` (correct
  demo output — buy 10×@10000 crosses sell 4×@9950 at price 10000,
  remaining 6 rests as bid; sell 6×@10050 doesn't cross, rests as ask).

## Entry 3 — services/market-data (Rust)

Tier 1 skeleton. Files:
- `Cargo.toml` — package `market_data`, std-only.
- `src/marketDataEventTypes.rs` — `PriceLevelDeltaUpdate`,
  `SequencedMarketDataMessage`, `FullBookSnapshotMessage`.
- `src/deltaPublisher.rs` — `DeltaPublisher`: assigns per-instrument
  monotonic sequence numbers (independent counter per instrument symbol,
  via `HashMap<String, u64>`) and fans each published message out to every
  registered sink closure. 1 passing unit test proving per-instrument
  sequence independence.
- `src/main.rs` — demo driver registering one println sink, publishing two
  delta batches for `DEMO-EQ`.
- `README.md` — status, run instructions.
- Verified: `cargo test` (1/1 pass), `cargo run` (sequence numbers 1, 2 in
  order for the same instrument).

## Entry 4 — services/oms-gateway (Go)

Tier 1 skeleton, risk-check path is real. Files:
- `go.mod` — module `tradingapp/omsgateway`.
- `internal/orders/orderTypes.go` — `OrderSubmissionRequest`,
  `OrderAcknowledgementResponse` (carries both
  `HumanReadableRejectionReason` and `MachineReadableRejectionReason` on
  every reject — implements the FEATURES.md §21 differentiator).
- `internal/riskengine/preTradeRiskEngine.go` —
  `PreTradeRiskEngine.EvaluateOrderAgainstAvailableMargin`: in-memory,
  `sync.RWMutex`-guarded balance cache, no DB round-trip. Returns
  `RiskCheckOutcome` with plain-language rejection text for both the
  unknown-account and insufficient-margin cases.
- `internal/sequencing/sequenceNumberAllocator.go` —
  `GlobalSequenceNumberAllocator.AllocateNextSequenceNumber`: lock-free via
  `atomic.AddUint64`, avoids a mutex on the per-order hot path.
- `cmd/server/main.go` — HTTP server (`net/http`, stdlib only):
  `GET /health`, `POST /orders/submit` wiring risk check → sequencing →
  log-only "would route to matching engine" (no real network hand-off
  yet).
- `internal/riskengine/preTradeRiskEngine_test.go` — 3 tests: approve
  within margin, reject over margin (asserts non-empty human-readable
  reason), reject unknown account.
- `README.md` — status, curl example, run instructions.
- Verified: `gofmt -w .`, `go vet ./...`, `go build ./...`, `go test ./...`
  (3/3 pass).

## Entry 5 — services/ledger (Go)

Tier 2 skeleton, double-entry accounting core is real. Files:
- `go.mod` — module `tradingapp/ledger`.
- `internal/doubleentry/doubleEntryLedgerCore.go` —
  `InMemoryDoubleEntryLedgerBook.PostJournalEntry`: rejects any
  `JournalEntry` whose debit lines don't sum to its credit lines
  (`ErrJournalEntryDoesNotBalance`) or that references an unknown account
  (`ErrUnknownLedgerAccount`), atomically applies all lines otherwise —
  never partially applied. `CurrentBalanceInMinorUnits` for lookups.
- `internal/doubleentry/doubleEntryLedgerCore_test.go` — 3 tests: balanced
  entry posts and updates both accounts, unbalanced entry rejected with
  neither account touched, unknown-account entry rejected.
- `cmd/server/main.go` — HTTP server: `GET /health`,
  `GET /accounts/balance?accountId=...`.
- `README.md` — status, curl example, run instructions.
- Verified: `gofmt -w .`, `go build ./...`, `go test ./...` (3/3 pass).

## Entry 6 — services/kyc-onboarding, services/backoffice (Go)

Thin stubs (health check + one placeholder endpoint each), explicitly not
implementing the FEATURES.md §1 / §14 scope yet — exist only to establish
the service boundary early, since oms-gateway's risk engine and ledger
account creation will eventually depend on "is this account KYC-complete?"
- `kyc-onboarding/cmd/server/main.go` — `GET /health`,
  `GET /kyc/status?accountId=...` (returns `NOT_IMPLEMENTED` stage).
- `backoffice/cmd/server/main.go` — `GET /health` only.
- Verified: both `gofmt -w .` and `go build ./...` clean. (Caught and
  fixed a shell-`cd`-didn't-apply-to-second-command bug during this step —
  backoffice's build was accidentally run from the kyc-onboarding
  directory on the first attempt; re-ran correctly from
  `services/backoffice`.)

## Entry 7 — services/quant-engine (Python), in progress

- `pyproject.toml` — package `quant-engine`, `src/` layout, no runtime
  deps (stdlib `math` only), `pytest` as a dev extra.
- `src/quantengine/blackScholesOptionPricer.py` — real Black-Scholes
  implementation:
  - `calculateStandardNormalCumulativeDistribution` / `...ProbabilityDensity`
    — N(x) and N'(x) via `math.erf`.
  - `BlackScholesInputParameters` — frozen dataclass of the 5 standard
    inputs (spot, strike, rate, vol, time-to-expiry).
  - `calculateD1AndD2` — shared d1/d2 terms, raises on non-positive
    time-to-expiry or volatility.
  - `calculateBlackScholesCallOptionPrice` /
    `calculateBlackScholesPutOptionPrice` — the two pricing formulas from
    ARCHITECTURE.md §6.
  - `calculateOptionGreeks` → `OptionGreeksResult` (delta, gamma, vega per
    1% vol change, theta per calendar day) for calls and puts.
  - `solveImpliedVolatilityFromMarketPrice` — Newton-Raphson IV solver
    using vega as the derivative; raises (rather than silently returning a
    bad estimate) when vega is too small to continue, with a TODO for a
    bisection fallback.
- Test suite and README not yet written as of this entry — next entry
  will cover verification.

## Entry 8 — services/quant-engine, verified

- `tests/test_blackScholesOptionPricer.py` — 7 tests: call/put price
  against a known textbook reference value (S=K=100, r=5%, σ=20%, T=1y →
  call≈10.4506, put≈5.5735), put-call parity as an exact invariant check
  (`C - P == S - K·e^(-rT)`), call delta bounds, put delta bounds, gamma
  equality between call and put at the same strike (regression guard
  against an accidental branch-on-side bug), and an implied-volatility
  round-trip (price at a known σ, solve backward, recover that σ).
- `README.md` — status, explicit "don't put this on a hot path yet"
  constraint per ARCHITECTURE.md §8's Python-for-research /
  Rust-for-hot-path split.
- Verified: created `.venv`, `pip install -e ".[dev]"`, `pytest -q` — 7/7
  pass.

## Entry 9 — documentation infrastructure

- Created this file (`BUILD_LOG.md`) as the append-only chronological
  record (retroactively covering Entries 1–8 at time of creation).
- Created `DOCUMENTATION.md` as the synthesized, non-chronological
  function-by-function reference — see that file for the current
  authoritative description of every implemented piece. This log stays
  append-only going forward; `DOCUMENTATION.md` is the one that gets
  edited in place as code changes.

## Entry 10 — apps/web (Next.js)

- Scaffolded via `create-next-app@latest` (TypeScript, Tailwind, ESLint,
  App Router, no `src/` dir) — real dependency install (365 packages), not
  hand-rolled.
- Replaced the default homepage with `app/page.tsx`: a real order-ticket
  form (`RetailOrderTicketPage`) that POSTs to
  `oms-gateway`'s `/orders/submit` and renders
  `humanReadableRejectionReason` on rejection — proves the FEATURES.md §21
  differentiator end-to-end from an actual browser client, not just curl.
  Base URL configurable via `NEXT_PUBLIC_OMS_GATEWAY_BASE_URL`
  (default `http://localhost:8081`).
- Updated `app/layout.tsx` metadata (title/description) from the
  create-next-app defaults.
- `README.md` — status, run instructions.
- Verified: `npx tsc --noEmit` clean, `npm run build` succeeds
  (Turbopack, static prerender of `/` and `/_not-found`).

## Entry 11 — apps/terminal (deliberately deferred)

- Did not scaffold a Tauri app this pass. Wrote `README.md` explaining why:
  scaffolding it now, before `apps/web` has any reusable
  components/state, means either throwing that work away or building the
  order-ticket/watchlist/chart components twice. This is a `FEATURES.md`
  §0 Phase 2 item (arrives with the options chain and rest of the Pro
  Terminal feature set), not a Phase 0 scaffold item. Left the scaffold
  command and the intended shared-package approach documented for when
  it's picked up.

## Entry 12 — CI

- `.github/workflows/ci.yml` — one job per service (not a shared matrix),
  so a Rust build break never blocks feedback on an unrelated Go test
  failure: `matching-engine`/`market-data` (cargo build+test),
  `oms-gateway`/`ledger` (gofmt check, go vet, go build, go test),
  `kyc-onboarding`/`backoffice` (go build only — no tests exist),
  `quant-engine` (pip install + pytest), `web` (tsc + next build).
- `infra/ci/README.md` — explains why the functional workflow file has to
  live at `.github/workflows/ci.yml` rather than under `infra/ci/`
  directly (GitHub Actions only executes from that exact path), and lists
  what's not yet covered (terminal app, integration/e2e tests, CD).

## Entry 13 — renamed the project to Mercurius, cleaned up, prepped for push

- Project renamed from the working codename "trading-app" to
  **Mercurius** (Roman god of trade, commerce, communication, and speed —
  chosen deliberately for the double meaning: commerce + low latency).
- Renamed the Go module paths and their imports:
  `tradingapp/omsgateway` → `mercurius/omsgateway`,
  `tradingapp/ledger` → `mercurius/ledger`,
  `tradingapp/kycOnboarding` → `mercurius/kycOnboarding`,
  `tradingapp/backoffice` → `mercurius/backoffice`. All four services
  rebuild clean under the new module names.
- Swept "trading-app" → "Mercurius" through source-file header comments,
  the web app's page title, `DOCUMENTATION.md`'s title, and the CI
  workflow comment. Left this file's (`BUILD_LOG.md`) own historical
  entries untouched, per its append-only rule — Entries 1 and 4–5 still
  say "trading-app"/"tradingapp" because that was accurate when written.
- Renamed the naming-convention memory
  (`trading-app-naming-convention` → `mercurius-naming-convention`) and
  updated its content to reference the new name/paths.
- Renamed the local project directory itself:
  `projects/trading-app` → `projects/Mercurius`.
- Cleanup, independent of the rename: found and removed two accidentally
  committed build artifacts (`services/backoffice/server`,
  `services/kyc-onboarding/server` — stray binaries `go build ./...`
  drops in a service's root directory when run without `-o`). Hardened
  `.gitignore` with a `services/*/server` rule so this can't recur.
  Verified no other build artifacts (`.venv/`, `node_modules/`,
  `target/`, `__pycache__/`) were ever tracked — the only Cargo.lock
  files tracked are the two intentional ones for the binary crates
  (matching-engine, market-data), which is correct Rust practice for
  applications, not an artifact leak.
- Verified full rebuild after all of the above (all four Go services, both
  Rust crates, quant-engine's pytest suite, and the web app's
  `tsc --noEmit`/`next build`) before committing.
- **Also observed:** at some point during this pass, the four top-level
  docs (`ARCHITECTURE.md`, `BUILD_LOG.md`, `DOCUMENTATION.md`,
  `FEATURES.md`) ended up moved from the repo root into a new `docs/`
  subdirectory (this file included — hence why this entry is being
  appended at `docs/BUILD_LOG.md`, not `./BUILD_LOG.md`). Content is
  unchanged, only location. Updated `README.md`'s links to point at
  `docs/...` accordingly.

## Entry 14 — first real inter-service wiring: oms-gateway → matching-engine

Closed the single biggest gap called out in `DOCUMENTATION.md`'s
cross-cutting section: until now, every service was an island.

- `services/matching-engine`: added `serde`/`serde_json` (verified
  fetchable from crates.io first). New `src/wireProtocol.rs`:
  `IncomingOrderWireRequest` (deserializes JSON matching oms-gateway's
  `OrderSubmissionRequest` shape exactly, converts to the internal
  `IncomingOrderRequest`), `TradeExecutionWireEvent` (serializes a trade
  back out), `OrderSubmissionWireResponse` (either a populated
  `errorMessage` or a `tradeExecutionEvents` list). 3 new unit tests
  (buy/sell wire-flag conversion both directions, JSON deserialization
  against the exact oms-gateway-shaped payload).
- Rewrote `main.rs` from the old hardcoded-demo driver into a real TCP
  server on `127.0.0.1:9101`: a **sequential** accept loop (not
  thread-per-connection) that reads one JSON line, submits to
  `OrderBookCore`, writes one JSON line back, closes the connection. The
  sequential-not-threaded choice is deliberate — it preserves
  ARCHITECTURE.md §3.1's single-writer principle without a `Mutex`, even
  in this placeholder bridge. Manually verified live with `nc`: a resting
  buy produces no trade, a crossing sell fills at the resting price, an
  unknown-instrument request returns a clean error response.
- `services/oms-gateway`: new `internal/matchingengineclient` package —
  `MatchingEngineClient.SubmitOrderAndAwaitMatchResult` dials, writes,
  reads, closes; returns a Go `error` only for transport failures, never
  for a business-level rejection (that comes back as a populated
  `ErrorMessage` on a successfully-parsed response — tested with a fake
  in-process TCP server in `matchingEngineClient_test.go`, no dependency
  on the real Rust binary: successful-submission parsing, business
  rejection not surfaced as a Go error, unreachable-engine does return a
  Go error — 3 tests).
- Extended `orders.OrderAcknowledgementResponse` with
  `TradeExecutionEvents` and `MatchingEngineHandoffError`, and a new
  `orders.TradeExecutionSummary` type (deliberately decoupled from
  `matchingengineclient`'s wire type — the `orders` package shouldn't
  couple to one downstream client's shape). Documented explicitly in a
  doc comment that `WasOrderAccepted=true` with a non-empty
  `MatchingEngineHandoffError` is a valid, expected state, not a bug.
- Wired it into `cmd/server/main.go`: after risk approval + sequencing,
  calls the matching-engine client and folds the result into the
  response. Matching-engine address configurable via
  `MATCHING_ENGINE_TCP_ADDRESS` env var, default `127.0.0.1:9101`.
- **Verified end-to-end with both real binaries running** (not mocked):
  started `matching-engine` and `oms-gateway`, then via `curl` against
  `oms-gateway`'s actual HTTP API: (1) a resting buy order → accepted, no
  trades; (2) a crossing sell against it → accepted, `tradeExecutionEvents`
  shows the real fill at the resting price, quantity 4; (3) an
  over-margin order → rejected by the risk engine with the plain-language
  reason, confirmed it never reached the matching engine at all (no log
  line, no TCP connection). This is the first real proof that the
  documented architecture's service boundaries actually hold together in
  running code, not just in tests against a single service in isolation.
- Updated `matching-engine/README.md`, `oms-gateway/README.md`, and
  `DOCUMENTATION.md`'s sections for both services plus its cross-cutting
  summary to reflect this — the "no service talks to another" claim is
  now specifically scoped to "every hand-off *except* this one."
- Cleaned up test-run artifacts (stray `matching_engine`/`oms-gateway`
  processes, stray `services/*/server` binaries) before committing.

## Entry 15 — ledger settlement loop + matching-engine → market-data wiring

Continued closing inter-service gaps, per the user's explicit direction
to keep generating real code across the feature list rather than pausing
to ask before each increment.

**ledger gets a write path:**
- New `POST /journal-entries` HTTP endpoint in `cmd/server/main.go`, with
  its own wire types (`JournalEntryLineWireFormat`,
  `PostJournalEntryWireRequest/Response`) deliberately decoupled from the
  untagged internal `doubleentry.JournalEntry` domain type. Returns `422`
  with an error message on rejection (unbalanced entry / unknown
  account), `200` on success.
- Changed ledger's seed accounts from `user-cash-acct-001` to `acct-001`
  / `acct-002` (+ `firm-clearing-acct`) to match oms-gateway's demo
  accounts exactly, so the two services exercise together without extra
  mapping.

**oms-gateway settles trades and syncs balances for real:**
- `riskengine`: two new methods —
  `RefreshAccountBalanceFromLedger` (overwrite/add a cached balance) and
  `ApplyTradeSettlementToLocalCache` (debit buyer, credit seller,
  immediately) — 2 new tests, doc comments explicit that this bypasses
  the "cache refreshed asynchronously from an event stream" design in
  favor of a direct call, as a labeled shortcut.
- New `internal/ledgerclient` package: `FetchAccountBalance` and
  `PostTradeSettlementJournalEntry` (builds a balanced entry routed
  through `firm-clearing-acct` as a net-zero pass-through). 3 tests
  against a real `httptest.Server`.
- `cmd/server/main.go`: removed the hardcoded balance map entirely —
  `syncRiskEngineBalancesFromLedger` fetches real balances from the
  ledger at startup (logs and continues if the ledger isn't up yet,
  doesn't crash). Every fill now calls
  `settleTradeAgainstLedgerAndLocalCache`, which posts the settlement
  entry and only updates the local cache on success; a failed settlement
  is logged loudly (`SETTLEMENT FAILED`) since there's no
  reconciliation job yet.

**matching-engine publishes real depth to market-data:**
- `OrderBookCore::currentBookDepthSnapshot()` — returns every price level
  both sides, tested.
- `wireProtocol.rs`: `OutgoingDepthPublishWireMessage` mirrors
  market-data's incoming shape field-for-field.
- `main.rs`: `publishBookDepthToMarketData` — fire-and-forget TCP publish
  after every processed order, 200ms connect timeout, silently tolerant
  of market-data being down. Explicitly documented as preserving Tier
  0/Tier 1 decoupling (ARCHITECTURE.md tenet: a hot-path component must
  never depend on a downstream consumer's availability).
- `market-data`: new `src/ingestionWireProtocol.rs` +
  a real TCP server in `main.rs` on `127.0.0.1:9102`, replacing the old
  hardcoded-demo-data driver — accepts depth publishes and feeds
  `DeltaPublisher` for real.

**Verified live, all four services running simultaneously** (ledger,
matching-engine, market-data, oms-gateway):
1. Funded `acct-001`/`acct-002` via real `POST /journal-entries` calls,
   confirmed via `GET /accounts/balance`.
2. Started oms-gateway, confirmed startup log shows real synced balances
   (`1000000`, `50000`), not hardcoded values.
3. Placed a resting buy, then a crossing sell — got the real fill back
   in the HTTP response, AND confirmed via `GET /accounts/balance` that
   acct-001 dropped by exactly 40,000 and acct-002 rose by exactly
   40,000, with `firm-clearing-acct` net-unchanged by the trade (as
   designed — it only reflects the earlier funding).
4. Confirmed the LOCAL cache reflects the trade immediately, without a
   restart: a follow-up order from acct-001 was rejected using the
   post-trade balance (960,000), not the stale pre-trade one (1,000,000).
5. Checked `market-data`'s log: received real sequence numbers 1 and 2,
   directly correlated to the two real orders — not the old demo data.
6. Confirmed a risk-rejected order (over margin) never produced any log
   line or connection from matching-engine or ledger — the rejection
   genuinely happens before either downstream service is touched.

Updated `matching-engine/README.md`, `market-data/README.md`,
`oms-gateway/README.md`, `ledger/README.md`, and every affected section
of `DOCUMENTATION.md` (including its cross-cutting summary, which now
lists four real hand-offs instead of one). Cleaned up all test-run
processes and stray `services/*/server` binaries before committing.

## Entry 16 — KYC gating + account freeze/unfreeze, both real gates in oms-gateway

Continued per the user's direction to keep generating real code across
the feature list without pausing to ask before each increment. Checked
Docker availability first (`docker info`) — unavailable in this build
environment, so Postgres persistence for the ledger (the other obvious
next increment) was deliberately skipped rather than shipped unverified;
picked two increments that don't need external infra instead.

**kyc-onboarding gets real (if simplified) verification logic:**
- New `internal/kycstate` package: `KycVerificationStateMachine` —
  `SubmitKycDetails` validates PAN format
  (`^[A-Z]{5}[0-9]{4}[A-Z]$`) and non-empty full name, marks
  `VERIFIED`/`REJECTED` with a reason; `LookupKycStatus` answers
  `NOT_SUBMITTED` for an account that never submitted. 5 tests.
- `cmd/server/main.go`: replaced the hardcoded `NOT_IMPLEMENTED` stub
  with real `POST /kyc/submit` and `GET /kyc/status` handlers over that
  state machine.

**backoffice gets real account freeze/unfreeze:**
- New `internal/accountcontrol` package: `AccountFreezeStateMachine` —
  `FreezeAccount`/`UnfreezeAccount`/`CheckFreezeStatus`, absence from the
  map means not frozen. 4 tests.
- `cmd/server/main.go`: `POST /accounts/freeze` (rejects a request with
  no `freezeReason` — freezing without a recorded reason isn't
  acceptable), `POST /accounts/unfreeze`, `GET /accounts/freeze-status`.

**oms-gateway gates order submission on both, for real:**
- New `internal/kycclient` (`FetchKycStatus`, 3 tests) and
  `internal/backofficeclient` (`FetchFreezeStatus`, 3 tests) — same
  transport-error-vs-explicit-answer contract as the existing
  `ledgerclient`/`matchingengineclient`.
- `cmd/server/main.go`: KYC gate runs first (before risk check — an
  unonboarded account shouldn't have its margin evaluated at all), then
  the freeze gate, both before the existing risk check. Both share one
  deliberate, explicitly-documented tradeoff: an EXPLICIT ineligible/
  frozen answer fails CLOSED (order rejected, `KYC_NOT_VERIFIED` /
  `ACCOUNT_FROZEN`); a TRANSPORT failure (that service unreachable) fails
  OPEN (warning logged, order proceeds) rather than one dependency's
  uptime blocking all trading platform-wide. Flagged explicitly in the
  code comment that real capital would likely want the opposite, and
  that this skeleton chose availability over that safety margin.

**Verified live, all five backend services running simultaneously**
(ledger, matching-engine, kyc-onboarding, backoffice, oms-gateway):
1. Order attempt before KYC submission → rejected, `KYC_NOT_VERIFIED`.
2. KYC submit with a malformed PAN (`"not-a-pan"`) → `REJECTED` with a
   reason.
3. KYC submit with a valid PAN → `VERIFIED`.
4. Same order retried → now accepted, sequence assigned.
5. Stopped kyc-onboarding, retried an order for a *different*,
   never-verified account → confirmed the fail-open path: a warning was
   logged (`KYC check unreachable ... failing OPEN`) and the order
   proceeded to (and passed) the risk check, rather than being blocked.
6. Order accepted for a funded, KYC-verified account.
7. Froze that account via `POST /accounts/freeze` with a reason.
8. Same order retried → rejected, `ACCOUNT_FROZEN`, reason echoed back.
9. Unfroze the account.
10. Same order retried → accepted again, sequence assigned.

Updated `kyc-onboarding/README.md` and `backoffice/README.md` (both
formerly "thin stub" → now document the real slice + limitations),
`oms-gateway/README.md` (full 5-terminal run instructions), and every
affected section of `DOCUMENTATION.md` including the cross-cutting
summary (six real hand-offs now). Also noted in the cross-cutting summary
that the Docker-unavailability finding is itself worth recording, so a
future session doesn't waste time rediscovering it. Cleaned up all
test-run processes and stray `services/*/server` binaries before
committing.

## Entry 17 — Market orders + positions tracking; progress markers added to FEATURES.md

Per the user's request to pick up pace: two increments this round,
lighter documentation per increment (still verified live, just less
prose per step).

- Added `🚧` progress markers to `docs/FEATURES.md` next to the ~8 items
  with real code behind them (KYC, ledger core, order entry, L2 depth,
  matching engine core, retail order ticket, pre-trade risk, CI/CD) —
  checkboxes stay unchecked since none are production-complete.
- **Market orders**: `matching-engine` gets `OrderType::Limit/Market` —
  a market order crosses regardless of resting price and never rests an
  unfilled remainder (IOC-like, documented simplification). 4 new tests
  (2 order-book, 2 wire-protocol, including a backward-compat test for
  the new wire field defaulting to Limit when omitted). Wired through
  `oms-gateway`'s wire types end-to-end. Flagged a real gap in a code
  comment: market-order notional is currently always 0 in the risk check
  (no last-price feed to estimate it from yet) — the risk check trivially
  passes for market orders, not fixed this round.
- **Positions tracking** (FEATURES.md §3 "positions/holdings views"): new
  `internal/positions` package in oms-gateway — net signed quantity per
  (account, instrument), updated on every fill alongside the existing
  ledger settlement. 5 tests. New `GET /positions?accountId=...` endpoint.
- Verified live: a market order crossed a resting limit sell regardless
  of price; a market order with zero available liquidity was accepted
  with zero trades and did not rest; positions were empty before any
  trade and showed the correct +3/-3 split after a partial fill between
  two accounts.

## Entry 18 — Stop-loss order types (SL / SL-M) with live-price-triggered activation

Completes FEATURES.md §3's "Order entry: Market, Limit, SL, SL-M" — all
four order types are now real.

- `matching-engine`: `OrderType` gains `StopLossLimit`/`StopLossMarket`.
  `IncomingOrderRequest` gains `stopTriggerPriceInMinorUnits: Option<i64>`.
  `OrderBookCore` gains a `pendingStopOrders` pool (deliberately separate
  from the resting-order maps — an armed-but-untriggered stop order must
  be invisible to `currentBookDepthSnapshot`, it isn't a live bid/ask yet)
  and `lastTradedPriceInMinorUnits`, updated after every trade. Standard
  convention implemented: a BUY stop fires once last price rises to/
  through its trigger, a SELL stop fires once last price falls to/through
  its trigger. `submitIncomingOrder` now re-scans pending stops after
  every order (original or triggered) and loops, so a triggered stop's
  own trade can cascade-trigger another stop behind it. 4 new tests: stop
  order invisible to depth pre-trigger, triggers and fills on a
  qualifying trade, single-round cascade, and a stop that correctly stays
  pending while price remains on the favorable side. 16 matching-engine
  tests total.
- `wireProtocol.rs`: `IncomingOrderWireRequest` gains
  `orderIsStopLossVariant: bool` + `stopTriggerPriceInMinorUnits:
  Option<i64>`, both `#[serde(default)]` for backward compatibility.
  `(stop, market)` flag pair selects one of the four `OrderType`s. 2 new
  tests (stop-flag conversion, omitted-fields backward compatibility).
- `oms-gateway`: `orders.OrderSubmissionRequest` and
  `matchingengineclient.OrderSubmissionWireRequest` both gain the same
  two fields (trigger price as `*int64` so "not a stop order" is
  distinguishable from "triggers at 0"). Extended the existing market-
  order risk-check gap comment in `cmd/server/main.go` to cover
  `StopLossMarket` (same always-0-notional problem) and note that even
  `StopLossLimit`'s notional is only an estimate of the fill-if-triggered
  price, not a current one — not fixed this round, same known gap family
  as market orders.
- Verified live across all five services: armed a `StopLossMarket` sell
  before any trade had printed (confirmed it does NOT fire prematurely),
  then a separate crossing trade printed at/through its trigger price in
  the same order-submission call — the stop cascade-triggered within that
  single TCP round-trip and its market sell filled into a resting buy,
  confirmed via `GET /positions` showing the correct net position change
  (acct-001 net -5, acct-002 net +5 DEMO-EQ) beyond what the manually
  submitted order alone would have produced.
- Full verification pass: 16 Rust tests (matching-engine) + 2
  (market-data), 4 Go test packages + 3 no-test-file packages across
  oms-gateway/ledger/kyc-onboarding/backoffice, all green; gofmt/vet
  clean.

## Entry 19 — Order cancellation, end-to-end

Closes a real gap: until this round, once an order rested on the book (a
`Limit` remainder) or armed as a pending stop order, there was no way for
anyone to remove it — it sat there indefinitely. Now there is.

- `matching-engine`: `submitIncomingOrder`'s return type changed from a
  bare `Vec<TradeExecutionEvent>` to a new `OrderSubmissionOutcome`
  struct carrying both the trades AND the local order id this order was
  assigned. Every order (not just ones that end up resting) now gets an
  id at intake time — `restRemainingBuyOrderOnBook`/
  `restRemainingSellOrderOnBook` reuse that id instead of allocating a
  fresh one, so a triggered stop order keeps the same id it was armed
  under. New `OrderBookCore::cancelOrder(id) -> bool`: linear-scans both
  resting price-level maps first, then the pending-stop pool; removes and
  returns true on a hit, false otherwise (documented as an O(n) skeleton
  choice — a real build would index by id directly). 3 new tests: cancel
  a resting order and confirm it no longer matches, cancel a pending stop
  order and confirm it never resurrects on a later qualifying trade,
  cancel an unknown id returns false. 22 matching-engine tests total (up
  from 16).
- `wireProtocol.rs`: reused the existing flat-JSON extension pattern
  instead of introducing a new message shape — `IncomingOrderWireRequest`
  gains `cancelOrderSequenceNumber: Option<u64>` (`#[serde(default)]`);
  when set, `main.rs` treats the whole line as a cancel instruction
  instead of an order submission. `OrderSubmissionWireResponse` gains
  `assignedOrderSequenceNumber` (on submission) and `wasOrderCancelled`
  (on cancel), both `skip_serializing_if` so responses to the OTHER kind
  of request don't carry a meaningless null. 4 new wire-protocol tests.
- `oms-gateway`: `matchingengineclient` gained `CancelOrderWireRequest`
  and `CancelOrderAndAwaitResult`, refactored the TCP round-trip into a
  shared `sendOneLineAndAwaitResponse` helper so submission and
  cancellation share one code path. `orders.OrderAcknowledgementResponse`
  gained `MatchingEngineOrderSequenceNumber` (a DIFFERENT sequence space
  from the OMS's own `AssignedGlobalSequenceNumber` — documented
  explicitly to avoid confusing the two). New `POST /orders/cancel`
  endpoint, deliberately NOT risk/KYC/freeze-gated (cancelling can only
  reduce exposure, never increase it) but flagged with a TODO: it doesn't
  verify the caller owns the order being cancelled — no auth anywhere
  yet, consistent with every other gap in that category. 2 new
  matchingengineclient tests.
- Verified live across all five services: submitted a resting limit
  order, captured its id from the response, cancelled it, confirmed a
  second cancel of the same id returns false, and confirmed via a
  crossing sell that the cancelled order really was gone (zero trades).
  Separately: armed a stop order, cancelled it while still pending,
  then printed a qualifying trigger-crossing trade and confirmed via
  `GET /positions` that the cancelled stop did NOT resurrect and fire.
- Marked FEATURES.md §3's "Order book / trade book / positions /
  holdings views" 🚧 — positions + cancel together are a real (if
  backend-only, no UI yet) skeleton slice of that item.
- Full verification pass: 22 Rust tests (matching-engine) + 2
  (market-data), all Go packages across oms-gateway/ledger/
  kyc-onboarding/backoffice green, gofmt/vet clean, no stray artifacts.

## Entry 20 — Idempotency keys on order submission

FEATURES.md §2's "idempotent transactions," extended from the ledger side
(already real) to the order-submission side: a client retrying a timed-out
`POST /orders/submit` must never risk placing the same order twice.

- New `internal/idempotency` package in `oms-gateway`: `IdempotencyStore`
  caches the `OrderAcknowledgementResponse` produced for each
  client-supplied `IdempotencyKey`. A retry with the same key gets back
  the exact same cached response — success, rejection, whatever it
  was — instead of being re-risk-checked and re-routed to matching-engine.
  4 tests: no hit before anything's recorded, same key returns the
  recorded response, different keys don't collide, an empty key is never
  cached or looked up (opt-out semantics — a client that sends no key
  gets zero idempotency protection, exactly like before this existed).
- `orders.OrderSubmissionRequest` gained `IdempotencyKey string`
  (`omitempty`). The check happens before every other gate (KYC, freeze,
  risk) so a replay never re-touches any of them — a
  `respondAndRecord` closure replaces every `respondWithJson` call in the
  submit handler so whatever this submission's outcome is (accepted or
  any flavor of rejected) gets cached under the key, if one was given.
- **Explicitly documented, not fixed**: this only protects against
  *sequential* retries. Two requests carrying the same key arriving
  *concurrently* aren't blocked from both reaching risk-check/matching-
  engine — a real build needs a "reservation" state (claim the key with
  an in-flight marker before doing any work) to close that race. Also
  unbounded/unexpiring in-memory storage, no TTL, doesn't survive a
  restart — same category of gap as everything else in this skeleton.
- Verified live: submitted an order with `idempotencyKey: "retry-abc"`,
  got back `assignedGlobalSequenceNumber: 1`; retried the identical
  request with the same key and got back the exact same
  `assignedGlobalSequenceNumber: 1` (not a new one); a genuinely new
  submission with a different key got a fresh sequence number; then
  drained the book with a crossing sell sized for exactly the two real
  orders (not three) and confirmed a further 1-unit sell found nothing
  left to cross — proof the retried request never actually reached the
  matching engine a second time.
- Full verification pass: all Go packages across all four Go services
  green (new `idempotency` package included), gofmt/vet clean.

## Entry 21 — Cover Orders (CO)

FEATURES.md §3's "Cover Orders (CO), Bracket Orders (BO), GTT, AMO" —
first of the four, and the simplest: an entry order plus one mandatory
protective stop-loss leg. Deliberately scoped narrower than a full
Bracket Order (which also has a target/take-profit leg): with only one
protective order, there's no one-cancels-other race between two live
orders to manage. BO needs matching-engine to expose order-status
queries or push fill notifications so the OMS can cancel a sibling leg
the instant the other fires — that capability doesn't exist yet, which
is exactly why CO ships first.

- `oms-gateway`: extracted the entire gate → risk → matching-engine →
  settlement pipeline out of `buildSubmitOrderHandler`'s closure into a
  standalone `processOrderSubmission` function (taking a new
  `orderSubmissionDependencies` struct instead of a long parameter list),
  so the new Cover Order handler can drive an entry leg through the
  EXACT same real checks instead of reimplementing them. Idempotency
  stays in the HTTP-handler layer, wrapping `processOrderSubmission` —
  not extended to cover orders this round (documented as future work,
  same idempotency gap category).
- New `POST /orders/cover-submit`: submits the entry via
  `processOrderSubmission`; if (and only if) it actually filled —
  summing `ExecutedQuantity` across however many trades it produced — a
  `StopLossMarket` order is placed on the OPPOSITE side for the filled
  quantity. The protective leg deliberately bypasses KYC/freeze/risk
  (same rationale as cancellation: it can only reduce exposure). If the
  entry never fills at all, no protective leg is placed — documented
  simplification versus a real CO, which would likely pre-stage the stop
  leg deactivated until the entry fills.
- New `orders.CoverOrderRequest`/`CoverOrderResponse` types.
  `CoverOrderResponse` surfaces `ProtectiveStopOrderError` distinctly
  from a normal rejection: if the entry filled but placing the
  protective leg itself failed, the client now has a genuinely
  unprotected open position — logged loudly
  (`COVER ORDER PROTECTIVE LEG FAILED`), not silently swallowed, though
  there's no retry loop or alerting yet (flagged as a known gap).
- Verified live: rested a buy at 70 (liquidity for the eventual triggered
  stop), rested + crossed a sell at 100 for the entry, submitted a cover
  order that filled 5 units and placed a protective stop at trigger 90;
  confirmed the entry alone left the position at +5; printed a
  crossing trade at 85 (below trigger) and confirmed, in that single
  response, BOTH the initiating trade AND the cascaded protective-stop
  fill into the resting 70-buy — position returned to net 0. Also
  confirmed a cover order with no liquidity to cross rests unfilled and
  places no protective leg at all.
- Full verification pass: 22 Rust tests (matching-engine, unchanged this
  round — no engine-side code touched), all Go packages across all four
  Go services green, gofmt/vet clean.

## Entry 22 — Order status queries

Read-only "what's happening with order N?" capability — the missing
piece the summary at the end of entry 21 flagged as the natural next
step (needed for Bracket Orders/GTT, and useful right now for any client
that wants to poll a resting or pending order instead of just tracking
state itself).

- `matching-engine`: `OrderStatusQueryResult` enum (`RestingLimit`,
  `PendingStop`, `NotFound`) in `orderTypes.rs`. New
  `OrderBookCore::queryOrderStatus(id)`, read-only, same linear-scan
  approach as `cancelOrder` (both maps, then the pending-stop pool). 4
  new tests: not-found for an unknown id, a resting order reports its
  LIVE remaining quantity (proven by partially filling it first, so the
  reported quantity differs from the original), a pending stop reports
  its side/trigger/quantity, and a cancelled order goes back to
  not-found. 29 matching-engine tests total (up from 22).
- `wireProtocol.rs`: same flat-JSON extension pattern as cancel —
  `IncomingOrderWireRequest` gains `queryOrderStatusSequenceNumber`
  (`Option<u64>`, serde default); response gains `orderStatus` (one of
  `"RESTING_LIMIT"`/`"PENDING_STOP"`/`"NOT_FOUND"`) plus
  side/price/quantity fields, all `skip_serializing_if`. `main.rs`
  checks this before falling through to a normal submission (after the
  cancel check). 4 new wire-protocol tests.
- `oms-gateway`: `matchingengineclient` gained
  `QueryOrderStatusAndAwaitResult`, sharing the TCP round-trip helper
  with submission and cancellation. New `GET /orders/status?
  instrumentSymbol=...&matchingEngineOrderSequenceNumber=...` — read-only,
  no gating (same "can't increase exposure" rationale as cancel/
  positions). New `orders.OrderStatusResponse` type. 2 new
  matchingengineclient tests.
- Verified live: unknown id → `NOT_FOUND`; submitted a resting order,
  status showed `RESTING_LIMIT` with the correct price/quantity;
  cancelled it, status flipped back to `NOT_FOUND`; armed a stop order,
  status showed `PENDING_STOP` with its trigger price and quantity.
- Full verification pass: 29 Rust tests (matching-engine) + 2
  (market-data), all Go packages across all four Go services green,
  gofmt/vet clean.

## Entry 23 — After Market Orders (AMO)

Third of FEATURES.md §3's "CO, BO, GTT, AMO" line — Bracket Orders and
GTT are still deliberately out (they need matching-engine push
notifications, not just the status queries entry 22 added).

- New `internal/marketsession` package: `MarketSessionState`, a plain
  mutex-guarded boolean (starts CLOSED), flipped by an explicit admin
  call rather than a real clock-driven trading calendar — documented as
  the honest gap versus a real exchange session. 2 tests.
- New `internal/amoqueue` package: `AfterMarketOrderQueue`, a FIFO of
  queued `orders.OrderSubmissionRequest`s, safe for concurrent
  `Enqueue`, single `DrainAll`. 4 tests including FIFO-order
  verification and empty-drain behavior.
- `orders.OrderSubmissionRequest` gained `OrderIsAfterMarketOrder`;
  `OrderAcknowledgementResponse` gained `IsQueuedAsAfterMarketOrder`.
  In `buildSubmitOrderHandler`: an AMO arriving while the market is
  closed is queued (no KYC/freeze/risk yet — that all reruns fresh at
  drain time) instead of processed; the idempotency store deliberately
  does NOT cache the "queued" response, since a later retry with the
  same key needs to see the REAL outcome once the market opens, not
  "queued" forever.
- Three new admin endpoints: `GET /market-session/status`,
  `POST /market-session/close`, `POST /market-session/open` — the last
  one synchronously drains the entire AMO queue through
  `processOrderSubmission` (the exact same real pipeline every other
  order goes through) before responding, so the response itself proves
  what happened to every queued order. Documented gap: synchronous
  drain blocks the caller on a large backlog — fine for a skeleton, not
  for real scale.
- Verified live: closed the market, submitted an AMO — confirmed
  `isQueuedAsAfterMarketOrder:true` and that positions showed NO change
  (proving it genuinely wasn't processed yet); confirmed
  `queuedAfterMarketOrders:1` via status; opened the market and
  confirmed the response itself showed the AMO's real submission result
  (a fresh `matchingEngineOrderSequenceNumber`); confirmed via
  `GET /orders/status` it was genuinely resting on the book; crossed it
  with a real sell and confirmed the position finally updated.
- Full verification pass: 29 Rust tests (matching-engine, unchanged),
  all Go packages across all four Go services green (2 new packages),
  gofmt/vet clean.

## Entry 24 — Web dashboard catches up to the backend; oms-gateway gets real CORS

Five backend rounds in a row (stop orders, cancellation, idempotency,
Cover Orders, order status, AMO) had left `apps/web` on a single
order-ticket page that only knew about plain Limit/Market submission.
This round makes the browser client actually reachable and able to
exercise everything built since.

- Rewrote `apps/web/app/page.tsx` from one form into a dashboard with
  four real sections, all talking to oms-gateway's actual endpoints:
  - **Order ticket**: order type dropdown (Limit/Market/SL/SL-M) with
    the right fields enabled/disabled per type, an idempotency-key field
    (auto-generated via `crypto.randomUUID()`, editable/regeneratable so
    a real click-twice actually exercises the idempotency guard), an AMO
    checkbox, and a Cover Order toggle that switches the submit target
    to `/orders/cover-submit` and shows a separate protective-leg result
    card (including a loud red state if the protective leg failed).
  - **Market session (admin)**: status/open/close buttons against
    `/market-session/*`, showing queued-AMO count.
  - **Positions**: fetches `/positions?accountId=...`.
  - **Order status / cancel**: fetches `/orders/status` and posts to
    `/orders/cancel` by matching-engine order id.
  - Order-acknowledgement rendering now understands
    `isQueuedAsAfterMarketOrder`, `matchingEngineOrderSequenceNumber`,
    and `matchingEngineHandoffError` — none of which the old page knew
    existed.
  - Kept the same long-camelCase naming convention and existing
    `LabeledTextField`/`LabeledNumberField` components rather than
    introducing a component library.
- **Found and fixed a real bug while verifying this live**: oms-gateway
  had NO CORS headers at all — every fetch from a browser page served on
  a different port (the whole point of a separate `apps/web` dev server)
  would have failed silently with a CORS error before reaching any
  handler. New `withPermissiveCorsForDevelopment` middleware wraps the
  whole mux. Documented as a real, must-fix-before-real-auth gap:
  `Access-Control-Allow-Origin: *` is fine for a no-auth demo, wrong the
  moment cookies/bearer tokens exist.
- Verified live: `tsc --noEmit` and `eslint` both clean, `next build`
  succeeds, `next dev` serves 200 with all four section headings present
  in the rendered HTML; separately, with all five backend services
  running, confirmed a real `Origin: http://localhost:3100` preflight
  OPTIONS gets `204` with the right `Access-Control-Allow-*` headers and
  a real cross-origin POST to `/orders/submit` succeeds end-to-end with
  those headers present on the actual response (not just the preflight).
- Full verification pass: 29 Rust tests (matching-engine, unchanged),
  all Go packages across all four Go services green, gofmt/vet clean,
  no stray `.next/` build output committed (already gitignored).

## Entry 25 — Audit trail

FEATURES.md's "Audit trail: immutable log of every order, modification,
cancellation" — a compliance-facing record independent of whatever the
client-facing HTTP response said (which can be lost to a dropped
connection; the audit entry, once appended, never is).

- New `internal/audittrail` package: `AuditTrail`, an append-only,
  mutex-guarded, in-memory log. "Immutable" is a real API-level
  guarantee here, not just a naming convention — the type exposes
  `Append` and two read methods (`AllEntries`, `EntriesForAccount`) and
  nothing else; there is no update or delete method to call. 6 tests
  including one that specifically proves `AllEntries` returns a copy
  (mutating the returned slice must not affect the trail's internal
  state).
- Wired an `EventType` at every consequential decision point in
  `oms-gateway`: `ORDER_SUBMITTED`/`ORDER_REJECTED` (KYC, freeze, and
  risk rejections all distinguished by `DetailMessage`)/
  `ORDER_MATCHING_ENGINE_FAILURE`/`ORDER_FILLED` in
  `processOrderSubmission`; `ORDER_CANCELLED`/`ORDER_CANCEL_FAILED` in
  the cancel handler; `COVER_PROTECTIVE_LEG_PLACED`/
  `COVER_PROTECTIVE_LEG_FAILED` in the cover-order handler;
  `AFTER_MARKET_ORDER_QUEUED` when an AMO queues;
  `MARKET_SESSION_OPENED`/`MARKET_SESSION_CLOSED` on the admin toggles.
  `orderSubmissionDependencies` gained an `auditTrail` field so
  `processOrderSubmission` (shared by plain submission, cover orders, and
  AMO drain) logs once, correctly, no matter which caller it's driven by.
- New `GET /audit-trail` (optionally `?accountId=...` to filter).
- Verified live: confirmed the trail starts empty; a KYC rejection
  logged `ORDER_REJECTED` with the right detail; a resting order logged
  `ORDER_SUBMITTED`; a cancel logged `ORDER_CANCELLED`; a risk rejection
  (insufficient margin, caught incidentally while testing) correctly
  logged `ORDER_REJECTED` with `INSUFFICIENT_MARGIN`; a real cross
  between two accounts logged both `ORDER_SUBMITTED` entries plus one
  `ORDER_FILLED` with the exact fill detail (quantity, price, both
  counterparties); the `?accountId=` filter correctly scoped to just
  that account's entries.
- Full verification pass: 29 Rust tests (matching-engine, unchanged),
  all Go packages across all four Go services green (1 new package),
  gofmt/vet clean.

## Entry 26 — market-data: real trade-tick publishing + OHLCV candle aggregation

matching-engine only ever told market-data about book depth (resting
orders); it never said anything about what actually PRINTED. That meant
market-data had no trade tape and no way to build a chart — every FEATURES
item touching charting/price history was blocked on this.

- `matching-engine`: `wireProtocol.rs`'s `OutgoingDepthPublishWireMessage`
  gained a `tradeTicks: Vec<OutgoingTradeTickWireEvent>` field
  (`{executedPriceInMinorUnits, executedQuantity}` — deliberately narrower
  than the internal `TradeExecutionEvent`; market-data has no business
  need for either counterparty's account id). `handleOneIncomingOrderLine`
  in `main.rs` now returns `(response, tradeExecutionEvents)` so
  `publishBookDepthToMarketData` can forward whatever trades this specific
  order just produced, alongside the (unchanged) full depth snapshot.
  Cancels and status queries always produce an empty trade list.
- `market-data`: new `src/candleAggregator.rs` — `CandleAggregator`
  bucketing every trade tick into a fixed 60-second OHLCV candle per
  instrument (`open`/`high`/`low`/`close`/`totalVolume`), plus a bounded
  trade tape (last 500 ticks/candles per instrument). Incremental, not
  recomputed from scratch per query. 6 tests: new-bucket-opens-fresh-
  candle, same-bucket-updates-H/L/C/volume, bucket-rollover, per-
  instrument independence, unknown-instrument returns empty (not a
  panic), and limit/ordering on the recent-N queries.
- `market-data`: `ingestionWireProtocol.rs`'s
  `IncomingDepthPublishWireMessage` gained `tradeTicks`
  (`#[serde(default)]`, backward-compatible with any older
  matching-engine build). `main.rs` timestamps each tick with
  `SystemTime::now()` on receipt (matching-engine doesn't stamp one
  itself yet — documented as a TODO) and folds it into the shared
  `CandleAggregator` before handing the depth deltas to `DeltaPublisher`
  as before.
- New `src/httpQueryServer.rs`: a small hand-rolled HTTP/1.1 GET server
  (no framework dependency, consistent with this codebase's existing
  raw-TCP-JSON style) on `127.0.0.1:9103`, exposing `GET /trades` and
  `GET /candles` (both take `?instrumentSymbol=...&limit=...`), with a
  permissive CORS header so `apps/web` can poll it directly from a
  browser. Runs on its own thread against a `Arc<Mutex<CandleAggregator>>`
  shared with the ingestion loop — read-only, so no ordering hazard.
  4 tests: query-string parsing, a real candle round-trip, the 400 for a
  missing `instrumentSymbol`, the 404 for an unknown path.
- Verified live end-to-end: started matching-engine + market-data,
  submitted a resting sell (qty 10 @ 100) then a crossing buy (qty 4 @
  100) directly over TCP — confirmed the real trade (qty 4 @ 100) showed
  up via `GET /trades?instrumentSymbol=DEMO-EQ` and was folded into
  exactly one candle via `GET /candles?instrumentSymbol=DEMO-EQ` with
  correct OHLC (all 100) and volume 4. Confirmed an unknown instrument
  returns `[]`, a missing `instrumentSymbol` returns 400, and the CORS
  header is present on responses.
- Full verification pass: matching-engine 29 tests (unchanged, still
  green), market-data 12 tests (6 new: candleAggregator + httpQueryServer
  test modules), both crates build clean with no new warnings.
- **Known gaps, documented in code comments and READMEs**: candle width
  is fixed at 60s (not configurable per request yet); trade timestamps
  are ingestion-time, not true matching-engine execution time (no shared
  clock/NTP discipline set up in this skeleton); everything is in-memory
  only — a market-data restart loses the entire trade tape and candle
  history; this HTTP endpoint is a stopgap for polling, not the real
  WebSocket streaming ARCHITECTURE.md §5 calls for.

## Entry 27 — quant-engine: real HTTP service wrapper around the Black-Scholes pricer

quant-engine was library-only — every other `services/*` runs as a real
process with a port; this one couldn't be called by anything except a
Python import. Closed that gap.

- New `src/quantengine/httpServer.py`: stdlib-only (`http.server` +
  `json`, no framework dependency, same "hand-roll it" convention as
  matching-engine's/market-data's Rust bridges) on `127.0.0.1:8085`.
  `GET /health`, `POST /options/price` (price + all four Greeks in one
  response), `POST /options/implied-volatility` (Newton-Raphson solve).
  Every response carries a permissive CORS header for `apps/web`.
  Stateless — one pure computation per request — so `ThreadingHTTPServer`
  needs no locking.
- New `[project.scripts]` entry point (`quant-engine-server`) in
  `pyproject.toml`.
- New `tests/test_httpServer.py`: 8 tests, all making REAL HTTP requests
  via `urllib` against a real `ThreadingHTTPServer` on an OS-assigned
  ephemeral port — health check, a full price+Greeks response, 400 for a
  missing field, 422 for a non-positive time-to-expiry, an
  implied-volatility round trip through both endpoints, 404 for an
  unknown route, 400 for malformed JSON, and the CORS header. 15 tests
  total in the package now (7 pre-existing + 8 new).
- Verified live: started the actual service (not just pytest), confirmed
  `/health`, priced a known reference contract (S=K=100, r=5%, σ=20%,
  T=1y) and got the same 10.4506 the unit tests check, confirmed the IV
  solver recovers σ≈0.2 from that price, confirmed the 400/422 error
  paths and the CORS header via real `curl` requests.
- **Known gaps, documented**: no portfolio-level Greeks aggregation (the
  API is single-contract only), no GARCH/Sharpe/Sortino/VaR, no
  arbitrage scanner (this is the pricer such a scanner would call), and
  this remains explicitly research-tier — not to be placed on any
  latency-sensitive path (the real-time arbitrage scanner would need a
  Rust port, per the existing module docstring).

## Entry 28 — structured (JSON) logging across all four Go services

FEATURES.md §13 (P0): "Structured logging + centralized log aggregation."
Every Go service was logging plain interpolated strings via the stdlib
`log` package — not machine-parseable, no consistent shape.

- New `internal/httplogging` package, identical in all four Go services
  (`ledger`, `kyc-onboarding`, `backoffice`, `oms-gateway` — no shared Go
  module/workspace exists yet, so this is deliberately duplicated code,
  same reality as everything else not yet consolidated into `libs/`):
  `WithRequestLogging(handler)` wraps an `http.Handler`, capturing the
  actual status code written (via a small wrapping `ResponseWriter`) and
  logging one structured line per request — `"http_request"` with
  `method`/`path`/`statusCode`/`durationMs` — via `slog.Default()` after
  the handler completes. 3 tests per package (response passthrough
  unchanged, correct method/path/status in the logged JSON, defaults to
  200 when a handler never calls `WriteHeader` explicitly) — 12 tests
  total across the four copies.
- Each service's `main()` now calls
  `slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))` before
  anything else, and wraps its top-level handler with
  `httplogging.WithRequestLogging` (in `oms-gateway`, layered inside the
  existing CORS middleware, so a CORS-short-circuited OPTIONS preflight
  doesn't get an access-log line but every request that actually reaches
  application logic does).
- **Bonus discovered live, not something I set out to build**:
  `slog.SetDefault` also redirects the whole stdlib `log` package's
  `Printf`/`Fatalf`/etc. through the same JSON handler — a documented
  `log/slog` behavior. That means every PRE-EXISTING `log.Printf` call
  scattered through each service's `cmd/server/main.go` (settlement
  failures, fail-open warnings, freeze/unfreeze events, journal-entry
  rejections, balance-sync results, startup messages) is ALSO now
  structured JSON, for free, with zero individual call sites rewritten.
  Verified live: `ledger`'s "journal entry rejected" line and
  `oms-gateway`'s "synced acct-001 balance from ledger" startup line both
  came out as proper JSON without being touched.
- Verified live end-to-end: started all four services, confirmed every
  startup line is JSON; hit `GET /health` on all four and confirmed a
  matching `http_request` JSON line for each; posted an unbalanced
  journal entry to `ledger` and confirmed both the rejection line AND
  the access-log line came out structured; submitted a real order
  through `oms-gateway` (rejected on insufficient margin — a normal 200
  with a rejection payload, not an HTTP error) and confirmed the access
  log correctly showed `statusCode:200`.
- Full verification pass: all four Go services build clean, `go vet`
  clean, `gofmt -l` clean, all existing test suites still green (nothing
  broken by the wrapping), 12 new `httplogging` tests green.
- **Known gap, documented in the package doc comment**: only the
  "structured" half of FEATURES.md §13's item is done — these JSON lines
  still just go to stdout, not a real centralized aggregation backend
  (Loki/ELK/etc.), which is an infra concern out of reach without Docker.

## Entry 29 — apps/web: a real live price chart against market-data's new candle API

Closes the loop on entry 26 (market-data trade-tick publishing + OHLCV
candles): now something actually renders that data.

- New `PriceChartSection` in `app/page.tsx`: polls `market-data`'s HTTP
  query API directly (`NEXT_PUBLIC_MARKET_DATA_BASE_URL`, default
  `127.0.0.1:9103`, NOT oms-gateway — this is the first thing in
  `apps/web` to talk to a service other than oms-gateway) every 5
  seconds (toggleable), fetching `GET /candles` and the latest
  `GET /trades` tick for a chosen instrument.
- New `CandlestickChart`: hand-rolled SVG rendering (wick lines + OHLC
  body rects, green/red by bullish/bearish) — no charting library
  dependency, matching this repo's "hand-roll it until there's a real
  reason not to" convention elsewhere (matching-engine/market-data's own
  TCP/HTTP bridges, quant-engine's stdlib HTTP server).
- Hit (and fixed, not suppressed blindly) a real lint error from this
  Next.js/React version's newer `react-hooks/set-state-in-effect` rule
  on the polling effect — investigated the rule's actual source/rationale
  before deciding a scoped, commented `eslint-disable` was the correct
  call here (the effect's setState calls all happen after an `await`,
  not synchronously in the effect's call frame, which is exactly the
  pattern react.dev's own "you might not need an effect" guide
  documents as a valid, not-a-smell effect use).
- Verified live: started matching-engine + market-data, submitted a real
  resting sell then a crossing buy directly to matching-engine over TCP,
  confirmed the resulting trade showed up correctly via
  `GET /candles?instrumentSymbol=DEMO-EQ` (the exact same query the new
  chart component makes) — confirming the chart's data path end-to-end.
  Also ran `npx tsc --noEmit` (clean, aside from one confirmed
  PRE-EXISTING unrelated error in `app/layout.tsx` — verified via
  `git stash` that it exists on `main` before this change too), `eslint`
  (clean after the fix above), and `next build` (succeeds, Turbopack,
  static-prerenders `/`); started `next dev` and confirmed `GET /` returns
  200 with "Price chart" / "1m candles" present in the server-rendered
  HTML.
- **Known gaps, documented**: polls on an interval rather than
  subscribing to a WebSocket (same polling-stopgap caveat as
  market-data's HTTP query API itself); no technical-indicator overlays;
  no zoom/pan; the actual client-side SVG rendering wasn't verified via
  a browser screenshot in this environment (no browser tool available),
  only via the data path + build/lint/SSR checks above — flagged
  explicitly rather than silently treated as fully browser-verified.

## Entry 30 — closing the idempotency concurrency gap: concurrent duplicates now genuinely collapse to one execution

Documented since entry 20: the idempotency store only guarded SEQUENTIAL
retries. Two requests carrying the same `idempotencyKey` arriving at
nearly the same instant (e.g. a client's own timeout-triggered auto-retry
firing while the original is still in flight) could both slip past the
old `PreviousResponseForKey` check and both get fully processed —
defeating the entire point of the guard for exactly the case that
matters most (a flaky network causing a genuine double-send).

- Replaced `internal/idempotency`'s plain
  `map[string]OrderAcknowledgementResponse` cache with a claim/complete
  protocol: `ClaimKeyOrAwaitExistingResponse(key)` — the first caller for
  an unclaimed key becomes its owner (`isThisCallTheOwner: true`) and
  must do the real work then call `CompleteClaimedKey(key, response)`;
  every other caller sharing that key (concurrent OR later) blocks on a
  `chan struct{}` closed exactly once by the owner, then receives the
  identical response. Bounded by a 30s claim timeout (`
  NewIdempotencyStoreWithClaimTimeout` lets tests override it) — an owner
  that never completes (e.g. a crash) can't hang waiters forever; a timed-
  out waiter gets a synthetic rejection
  (`IDEMPOTENCY_KEY_TIMED_OUT_WAITING_FOR_CONCURRENT_REQUEST`), never a
  third owner.
- Reordered `buildSubmitOrderHandler` in `cmd/server/main.go`: the AMO
  queue-while-closed branch now runs BEFORE the idempotency claim
  (previously after) — an AMO's real outcome doesn't exist yet at queue
  time, so claiming the key there would make a later legitimate retry
  incorrectly replay "queued" forever instead of the real post-drain
  result. Documented as an intentional, still-accepted gap: two AMO
  submissions sharing a key both get queued, not deduplicated.
- 8 tests (up from 4): the claim/complete happy path, sequential retry
  after completion, different-keys-don't-collide, empty-key opt-out,
  the actual regression test — two goroutines racing for the same key
  with a signal-channel barrier that deterministically forces the second
  to observe the key as claimed-but-incomplete (not a lucky race),
  asserting exactly one owner and the waiter receiving the owner's exact
  response — and a timeout test using the overridable-timeout
  constructor. `go test -race ./...` clean across all of oms-gateway,
  including this new concurrency-heavy package.
- Verified live against real running services (not just the race-
  detector test): funded and KYC-verified `acct-001`, fired two
  genuinely concurrent `curl` requests sharing one `idempotencyKey` at
  `POST /orders/submit`, and confirmed both responses carried the
  IDENTICAL `assignedGlobalSequenceNumber`/`matchingEngineOrderSequenceNumber`
  — proving only one order was actually placed. Confirmed order id 2
  never existed via `GET /orders/status` (`NOT_FOUND`) and that the audit
  trail recorded exactly one `ORDER_SUBMITTED` event, not two.
- **Known gaps, still documented**: in-memory only, no TTL (a claimed key
  is held forever — a real build needs the bounded retry-window
  expiry noted since entry 20); AMOs remain outside idempotency's
  coverage by design (see above); a crashed owner's waiters still pay
  the full 30s timeout before getting an answer, rather than failing
  fast on a detectable crash signal.

## Entry 31 — new services/auth: real register/login, JWT, refresh-token rotation with reuse detection

FEATURES.md §1 (P0): "Email/phone auth, session management, JWT +
refresh token rotation" — the biggest unaddressed P0 gap all session.
Scoped this increment to build the service for real, standalone and
fully tested, matching how kyc-onboarding/backoffice were originally
built before oms-gateway integrated with them one increment later —
integration with oms-gateway/apps/web is explicitly the next step, not
done here.

- New `internal/passwordhashing`: PBKDF2-HMAC-SHA256 via `crypto/pbkdf2`
  (Go stdlib as of Go 1.24 — confirmed available given this repo's
  `go 1.26.3`, so no external dependency needed, keeping this repo's
  zero-Go-external-dependency streak intact), random salt, constant-time
  verification. 5 tests.
- New `internal/jwtauth`: hand-rolled HS256 JWT (header.payload.signature,
  `crypto/hmac`/`crypto/sha256` only — no JWT library). 7 tests including
  a forged-claims-segment-with-legitimate-signature attack scenario
  (correctly rejected).
- New `internal/sessionstore`: refresh-token rotation with REUSE
  DETECTION — every refresh consumes the presented token and issues a
  new one in the same "family"; presenting an already-consumed token
  (the signature of theft) revokes the entire family, the same pattern
  real providers like Auth0/Okta use. 9 tests, `go test -race` clean,
  including two independent session families not interfering.
- New `internal/accountstore`: register/login with case-insensitive email
  matching, duplicate rejection, and — deliberately — the SAME error for
  "unknown email" and "wrong password" (plus a dummy-hash timing-parity
  comparison on the unknown-email path) to avoid an account-enumeration
  side channel. 6 tests.
- New `cmd/server/main.go`: `POST /auth/register`, `POST /auth/login`,
  `POST /auth/refresh`, `POST /auth/logout`, `GET /auth/verify`
  (Bearer-token introspection), `GET /health`. Listens on `:8086`
  (deliberately not `:8085` — that's already quant-engine's port, caught
  and fixed during live verification, not shipped as a collision).
  Structured JSON logging via a copy of the same `internal/httplogging`
  package every other Go service uses.
- Verified live against a real running process (not just `go test`):
  registered a real account, confirmed a duplicate registration 409s,
  confirmed a wrong password 401s, logged in and got a real JWT +
  refresh token, verified the access token via `GET /auth/verify`
  (confirmed a garbage/missing token 401s), rotated the refresh token
  and got a new pair, then REUSED the original now-consumed refresh
  token — confirmed it was rejected AND logged as a `SECURITY:` line,
  AND confirmed the legitimate rotated token was ALSO now dead (whole
  family burned, not just the reused one) — then did a fresh login,
  logged out, and confirmed refreshing after logout fails.
- Full verification pass: `gofmt`/`go vet`/`go build` clean, `go test
  -race ./...` clean (30 tests across 5 packages).
- Also fixed real, pre-existing CI staleness while touching
  `.github/workflows/ci.yml`: `kyc-onboarding`/`backoffice`'s jobs only
  ran `go build`, never `go test`, despite both services having real
  test suites (`internal/kycstate`, `internal/accountcontrol`) — added
  the missing `gofmt`/`go vet`/`go test` steps to match every other Go
  service's job, and added the new `auth` job (with `-race`, given its
  concurrency-heavy `sessionstore` package).
- **Known gaps, documented loudly**: NOT integrated with any other
  service — oms-gateway/apps/web still have zero auth; this service
  proves the auth primitives work, nothing else requires them yet. Mints
  its own `acct-<random hex>` identifier space, completely disconnected
  from oms-gateway's/ledger's seeded demo accounts — a real build needs
  one canonical account identifier space, not two independent ones. No
  MFA/phone-auth/email-verification/password-policy/rate-limiting.
  In-memory only. HS256 requires a shared secret across every verifying
  service (documented as a real build should probably use RS256/ES256
  instead).

## Entry 32 — apps/web: a real login UI against services/auth

Immediate follow-up to entry 31: gave the new auth service a real caller
from a browser, same pattern as every other cross-service integration
this session (build the service standalone, verify it works in
isolation, then connect it to something real).

- `services/auth`: added `withPermissiveCorsForDevelopment` (byte-for-
  byte the same middleware oms-gateway already has, just copied over) —
  without it nothing in a browser could reach `/auth/*` at all, exactly
  the same missing-CORS gap that was caught live when the web dashboard
  was first rebuilt earlier this session.
- `apps/web`: new `AccountSection` in `app/page.tsx` — email/password
  register and login forms; on successful login, displays the real
  `accountIdentifier`, the full JWT access token, and its
  `expiresInSeconds`; a logout button that calls `POST /auth/logout`.
  New `NEXT_PUBLIC_AUTH_BASE_URL` env var (default `127.0.0.1:8086`).
- Deliberately NOT wired into `OrderTicketSection`'s account field —
  `services/auth` mints its own `acct-<random hex>` identifier space,
  completely disconnected from oms-gateway's/ledger's seeded demo
  accounts (`acct-001`/`acct-002`). Reconciling those into one canonical
  account identifier space is real, nontrivial work (touches every
  service's account model) and explicitly out of scope for this
  increment — documented loudly in three places (file-header comment,
  `services/auth/README.md`, `docs/DOCUMENTATION.md`) rather than
  silently left as a surprise.
- Verified live: started `services/auth` for real, confirmed a real
  cross-origin `OPTIONS` preflight against `/auth/register` from
  `Origin: http://localhost:3000` returns `204` with the correct CORS
  headers, and a real cross-origin `POST /auth/register` succeeds with
  those headers on the response. `npx eslint`/`tsc --noEmit`/`next
  build` all clean; `next dev` serves `200` with "Account
  (services/auth" present in the rendered HTML.
- **Known gap, unchanged from entry 31**: this is UI for the auth
  primitives, not enforcement — no service anywhere actually requires a
  valid token to do anything yet.

## Entry 33 — services/auth: rate limiting on login and register

Closes a gap documented since entry 31's very first commit: "No rate
limiting on `/auth/login` (brute-force protection) or `/auth/register`
(registration spam)."

- New `internal/ratelimiter`: a per-key SLIDING-window limiter, not a
  naive fixed/calendar window. `Allow(key, now)` prunes timestamps older
  than `now - windowDuration` before counting, records the attempt
  regardless of outcome (a rejected attempt still counts — no free retry
  for hitting the limit), and reports whether the (now-recorded) attempt
  is within the limit. 6 tests, including a specific regression test for
  the failure mode a naive fixed window has: two attempts just before a
  minute boundary plus two more just after would land in different
  calendar buckets and all four would be allowed under a fixed window;
  the sliding window correctly still counts the recent-but-technically-
  "previous-bucket" attempts and rejects the burst.
- Wired into `cmd/server/main.go`: `POST /auth/login` is limited to 5
  attempts/minute keyed by NORMALIZED EMAIL (not source address — an
  attacker spreading a brute-force across many IPs, or a shared office
  NAT with many legitimate users, shouldn't change the limit on one
  account), checked before the password is ever verified. `POST
  /auth/register` is limited to 3/minute keyed by source address
  (`net.SplitHostPort(request.RemoteAddr)` — deliberately per-address
  here since an attacker choosing a new email every time would trivially
  bypass an email-keyed limit on registration). Both return 429 with a
  plain-language error beyond the limit.
- Verified live: fired 7 rapid wrong-password login attempts against one
  freshly-registered account — got 401×5 then 429×2, confirmed a
  DIFFERENT account was completely unaffected by the first account's
  limit; fired 4 rapid registrations from the same source address — got
  201×2 then 429×2 (correctly counting an earlier registration from the
  same `curl` session in this same test run, not a fresh count).
- Full verification: `gofmt`/`go vet`/`go build` clean, `go test -race
  ./...` clean (36 tests across 6 packages, up from 30/5).
- **Known gaps, documented**: the registration limiter's address key is
  `request.RemoteAddr` directly, not real `X-Forwarded-For`-aware client-
  IP resolution — behind any real load balancer/proxy this would key on
  the proxy, not the actual client. Rate-limiter memory is unbounded (no
  eviction/TTL on stale keys — a real build needs one, same category of
  gap as the idempotency store's still-open TTL TODO from entry 20).

## Entry 34 — kyc-onboarding: real bank account verification (penny-drop / micro-deposit)

FEATURES.md §1 (P1): "Bank account verification (penny-drop /
micro-deposit)."

- New `internal/bankverification`: `InitiateVerification` generates a
  real random micro-deposit amount (`crypto/rand`, 1-99 minor units)
  that's never returned to the caller — exactly like a real penny-drop,
  where the amount is only discoverable by checking the actual bank
  statement. `ConfirmMicroDepositAmount` checks a claimed amount against
  it; a match verifies, a wrong guess consumes one of 3 attempts, and
  exhausting them PERMANENTLY locks the verification
  (`FAILED_LOCKED`) — even the correct amount no longer works against a
  locked verification; a fresh `InitiateVerification` (new amount) is
  required, not a reset of the same one. 7 tests, including the full
  lockout path and confirming a locked verification rejects the correct
  answer too.
- New `cmd/server/main.go` routes: `POST /bank-verification/initiate`,
  `POST /bank-verification/confirm`, `GET /bank-verification/status`,
  and `GET /bank-verification/debug-peek` — the last one loudly
  documented (in the package, the handler, and the README) as existing
  ONLY because this repo has no real payment rail to actually deposit
  anything into an external bank account; it's a test/demo stand-in for
  "the account holder checks their bank statement," not something a
  real build ships.
- Verified live: initiated a real verification, confirmed status
  `PENDING`, peeked the real amount (test-only), submitted 2 wrong
  guesses (stayed `PENDING`, attempts remaining), a 3rd wrong guess
  locked it (`FAILED_LOCKED`), confirmed the CORRECT amount against the
  now-locked verification was STILL rejected, then ran a fresh
  verification through the happy path (correct amount on the first try
  → `VERIFIED`), and confirmed an unknown verification id returns
  `NOT_FOUND` for both status and confirm.
- Full verification: `gofmt`/`go vet`/`go build` clean, `go test -race
  ./...` clean across all of kyc-onboarding including the new package.
- **Known gaps, documented**: no real payment rail (see above); not
  wired into oms-gateway's order-gating the way KYC is — nothing
  currently requires a verified bank account for anything; in-memory
  only; no auth on any endpoint.

## Entry 35 — services/auth: real MFA (RFC 6238 TOTP), gating login

FEATURES.md §1 (P0): "MFA (TOTP + SMS fallback), device binding,
biometric unlock (mobile)". Covers the TOTP half — no SMS, no device
binding, no biometric (mobile-only anyway, deprioritized per
[[mercurius-platform-scope]]).

- New `internal/totp`: hand-rolled RFC 6238 TOTP (built on RFC 4226
  HOTP) — stdlib `crypto/hmac`/`crypto/sha1` only, no external
  dependency. `GenerateRandomSecret`, `GenerateCode`, `VerifyCode` (with
  configurable clock-skew tolerance), `BuildOtpAuthUri` (the standard
  `otpauth://totp/...` URI a real client renders as a QR code). 8 tests
  — critically including a cross-check against RFC 6238 Appendix B's
  PUBLISHED test vectors (the standard 20-byte ASCII test seed, at four
  documented timestamps, converted from the RFC's 8-digit vectors to
  this implementation's 6-digit output) — a real correctness check
  against an external reference, not just self-consistency.
- New `internal/mfastate`: per-account enrollment state.
  `BeginEnrollment` generates a secret but does NOT enable MFA yet;
  `ConfirmEnrollment` only activates it once a real code proves the
  enrollment actually worked (protects against locking out an account
  from a botched QR scan); `VerifyLoginCode`/`IsMfaEnabled` are what
  login gates on; `DisableMfa` removes the secret entirely. 9 tests.
- Wired into `cmd/server/main.go`: `POST /auth/login` gained an optional
  `totpCode` field — if the account has MFA enabled and it's missing,
  the response is `{mfaRequired: true}` with NO tokens (200, so the
  client can distinguish "need a code" from a hard failure); a wrong
  code 401s; a correct code proceeds to issue tokens as normal. New
  `POST /auth/mfa/enroll`, `POST /auth/mfa/confirm-enrollment`, `POST
  /auth/mfa/disable` — all THREE resolve the acting account from a
  verified bearer token (new `requireValidAccessToken` helper), not a
  request-body field, a stricter pattern than this service's
  register/login/refresh/logout endpoints use (which don't have a token
  to check against at the point they're called).
- Verified live with something stronger than a self-consistency check:
  wrote a SEPARATE, independent Python RFC 6238 implementation from
  scratch (not copied from the Go code), computed a code from the same
  secret the Go server generated during a real enrollment, and
  successfully used that Python-computed code to confirm enrollment and
  later log in through the real running Go service — two independent
  implementations agreeing is meaningfully stronger evidence of
  correctness than one implementation's own tests passing. Also
  confirmed: wrong code rejected at both confirm-enrollment and login;
  login without a code returns `mfaRequired` with no tokens; disabling
  MFA makes login succeed without a code again; enrolling without a
  valid bearer token 401s.
- Full verification: `gofmt`/`go vet`/`go build` clean, `go test -race
  ./...` clean (53 tests across 8 packages, up from 36/6).
- **Known gaps, documented loudly**: MFA enrollment is in-memory only —
  a restart LOCKS OUT every enrolled user (the account still
  conceptually needs MFA, but the secret proving a code is gone); no
  backup/recovery codes; `POST /auth/mfa/disable` only requires a valid
  access token, not a fresh password/MFA re-confirmation (a stolen live
  access token could turn MFA off) — flagged as unacceptable for a real
  build, not silently shipped as fine; `apps/web` has no MFA UI yet,
  this entire flow is `curl`-verified only.

## Entry 36 — oms-gateway: real latency histograms (Prometheus text exposition)

FEATURES.md §13 (P0): "Metrics/tracing (latency histograms on the
execution path especially)."

- New `internal/metrics`: `Histogram` (Prometheus-style cumulative
  buckets, 11 buckets spanning 1ms-5000ms, plus observation count/sum),
  `Registry` (one histogram per (method, path) pair, created lazily),
  `WithRequestTiming` middleware (records every request's wall-clock
  duration), `BuildMetricsHandler` (`GET /metrics`). Stdlib only — no
  Prometheus client library dependency — but `WritePrometheusText`
  produces genuine Prometheus text exposition format, so a real
  Prometheus server could scrape this today without any adapter.
- Wired into `cmd/server/main.go`: `metrics.WithRequestTiming` layered
  alongside the existing request-logging middleware (order doesn't
  matter between them — independent concerns both wrapping the same
  requests), new `GET /metrics` route.
- 14 tests across `histogram_test.go`/`registry_test.go`/
  `middleware_test.go`: cumulative-bucket semantics (a value only
  increments buckets at/above it, not just its own slice); count/sum
  accumulation; a value exceeding every finite bucket still counts
  toward the total; a snapshot is a frozen copy; different routes get
  independent histograms; repeated observations on one route accumulate
  correctly; valid Prometheus HELP/TYPE headers even with zero
  observations; the timing middleware preserves the wrapped handler's
  response untouched; `/metrics` rejects non-GET.
- Verified live: started the full five-service stack, hit `/health` and
  `/orders/submit` for real (3 real order submissions), then confirmed
  `GET /metrics` returned two independent, correctly-bucketed
  histograms with exact bucket counts, `_sum` matching real elapsed
  milliseconds, and `_count` exactly matching the number of real
  requests made to each route.
- Full verification: `gofmt`/`go vet`/`go build` clean, `go test -race
  ./...` clean across all of oms-gateway including the new package.
- **Known gap, documented loudly**: only oms-gateway has this.
  Matching-engine — the service this FEATURES.md item's "execution path
  especially" most directly refers to — has no metrics at all, since it
  has no HTTP listener to expose them on; giving it one (the way
  market-data grew a query-API listener alongside its TCP ingestion) is
  real, separate work not done in this increment. No tracing (spans
  across service hops) either — histograms only, per this item's own
  "latency histograms" framing.

## Entry 37 — market-data: real watchlists + price alerts, evaluated against the live trade stream

FEATURES.md §9 (P1): "Watchlists, alerts (price/technical triggers)".
Only plain price-threshold alerts — no technical (moving-average/RSI)
triggers.

- New `src/watchlist.rs`: `WatchlistStore` — per-account `HashSet` of
  instrument symbols, idempotent add/remove, sorted output for
  deterministic responses. 7 tests.
- New `src/pricealerts.rs`: `PriceAlertStore` — `createAlert` +
  `checkAndTriggerAlertsForTrade` (called once per real trade tick from
  `main.rs`'s ingestion loop — this is what makes alerts fire off REAL
  market data instead of a polling loop re-checking the latest price on
  a timer) + `alertsForAccount`. Once triggered, an alert is never
  re-evaluated by a later trade. 8 tests.
- `src/httpQueryServer.rs` significantly extended: was GET-only,
  candles/trades-only, single-`Arc<Mutex<CandleAggregator>>`-only.
  Now: new `SharedMarketDataState` bundles candles/watchlists/alerts
  behind one `Arc` (each field its own mutex, so the ingestion loop
  touching candles/alerts can't be blocked by the HTTP server touching
  the server-only watchlist store); the server now parses HTTP headers
  and a JSON request body (needed for the new `POST` endpoints), not
  just the request line; `OPTIONS` now gets a bare `204` for CORS
  preflights. New routes: `GET/POST /watchlist(/add|/remove)`, `GET
  /alerts`, `POST /alerts/create`. 11 tests total for this file (up
  from 4).
- `main.rs`'s ingestion loop: after folding a trade tick into the
  candle aggregator, now also calls
  `priceAlerts.checkAndTriggerAlertsForTrade` for it, logging any newly
  triggered alert ids to stdout.
- Verified live through the ACTUAL running matching-engine +
  market-data processes, not a unit test: added `DEMO-EQ` to a
  watchlist, created a real alert (fire at price ≥ 100), submitted a
  real trade at 80 over TCP to matching-engine and confirmed the alert
  correctly stayed untriggered, then submitted a real trade at 120 and
  confirmed the alert fired (`isTriggered: true`,
  `triggeredAtEpochSeconds` populated) — plus confirmed market-data's
  own stdout logged the trigger. Caught and worked around a real
  matching mechanic along the way: a resting order at a better (lower)
  price fills before a newer, worse-priced resting order does, so the
  live-test sequence had to fully drain the cheap resting liquidity
  before a trade would actually print at the higher price needed to
  cross the alert's threshold.
- Full verification: matching-engine unaffected (still 29 tests, still
  green), market-data 31 tests (up from 12), `cargo build`/`cargo test`
  clean, no new warnings.
- **Known gaps, documented**: in-memory only (a restart loses every
  watchlist and alert, fired or not); no auth; no technical triggers;
  no push notification (a client has to poll `GET /alerts` to discover
  a fired one); a fired alert never re-arms; no `apps/web` UI yet — this
  entire flow is `curl`-verified only.

## Entry 38 — oms-gateway: full pre-order charges breakdown

FEATURES.md §21 (P1): "Full charges breakdown *before* order
confirmation: brokerage, STT/CTT, stamp duty, GST, exchange transaction
charges, DP charges — shown as a receipt, not discovered after the
fact." Called out in FEATURES.md's own prioritization table as the #2
differentiator ("'Hidden charges' is the #1 trust complaint in broking
app reviews... Low [effort]").

- New `internal/chargescalculator`: `CalculateCharges(isBuy, price,
  quantity, isIntraday) -> ChargesBreakdown` computes turnover and every
  charge line item (brokerage, STT, exchange transaction charge, SEBI
  turnover fee, stamp duty, GST, DP charge) independently off it, plus
  `NetAmountInMinorUnits` (what actually settles). Real, documented
  buy/sell/delivery/intraday rate asymmetries: STT is both-sides on
  delivery but sell-side-only on intraday; stamp duty is buy-side-only;
  DP charges only apply to a delivery sell; GST applies only to
  (brokerage + exchange charge + SEBI fee), not to STT/stamp duty
  (themselves taxes, not GST-able services). Every rate constant is
  documented as an ILLUSTRATIVE model based on common discount-broker
  rate cards — not fetched from any live source, will drift out of date.
- New `POST /orders/estimate-charges` in `cmd/server/main.go`: read-only,
  no KYC/freeze/risk/idempotency gating — purely a pre-order quote a
  client calls before submitting, reusing the same
  price/quantity/side shape `/orders/submit` takes.
- 8 tests, most notably a FULLY HAND-WORKED example (₹100.00 delivery
  buy) checked line-by-line against the package's own rate constants —
  a stronger check than round-trip/self-consistency tests, since it
  would catch a wrong formula even if the code were internally
  consistent with itself. Plus: delivery-sell DP-charge-yes/stamp-duty-
  no; intraday-buy STT-no/stamp-duty-yes; intraday-sell STT-yes/stamp-
  duty-no; brokerage correctly switches between percentage-based and
  the flat ₹20 cap by order size; zero-quantity produces all-zero
  charges; net amount is above turnover for a buy, below for a sell.
- Verified live: `POST /orders/estimate-charges` on the real running
  service returned exactly the 119-paise total the hand-worked test
  predicts for a real ₹100.00 delivery buy; confirmed malformed input
  400s; confirmed this brand-new route automatically showed up in `GET
  /metrics` with zero extra wiring — proof the metrics middleware from
  entry 36 genuinely applies to every route, not just the ones it was
  written against.
- Full verification: `gofmt`/`go vet`/`go build` clean, `go test -race
  ./...` clean across all of oms-gateway including the new package.
- **Known, loudly documented gap**: every rate is illustrative, not
  fetched from any live regulatory/exchange source — a real build needs
  a maintained, versioned, centrally-updated rate table, not hardcoded
  Go constants. No state-by-state stamp-duty variation, no F&O/
  currency/commodity segment differences (equity cash market only).

## Entry 39 — kyc-onboarding: risk profiling questionnaire → investor risk category

FEATURES.md §1 (P1): "Risk profiling questionnaire → investor risk
category (feeds Robo-Advisory)." Robo-Advisory itself is NOT built (see
FEATURES.md's own phasing) — this produces the classification a future
Robo-Advisory feature would consume; nothing downstream reads it yet.

- New `internal/riskprofiling`: a fixed 6-question, 5-option-per-
  question Likert-style questionnaire (investment horizon, drawdown
  reaction, income stability, investment goal, investing experience,
  emergency-fund coverage) mirroring the shape of real investor risk-
  tolerance questionnaires (Vanguard/Fidelity-style) without claiming to
  legally BE one — documented as needing real compliance review before
  being treated as sufficient. `SubmitAnswers` requires exactly one
  entry per question, each a real option's point value (rejects a made-
  up score, only validates genuine selections), sums to a 6-30 range,
  classified into 5 categories via explicit (not integer-division-
  derived) bands. `LookupProfile` for read-only status. 11 tests: lowest/
  highest score classify correctly; all 5 category bands verified
  reachable; missing question, unknown question id, and invalid point
  value all rejected with distinct errors; overwrite-on-resubmit; per-
  account independence; questionnaire shape itself asserted.
- New routes in `cmd/server/main.go`: `GET /risk-profile/questionnaire`
  (static, no account context), `POST /risk-profile/submit`, `GET
  /risk-profile?accountId=...`.
- Verified live: fetched the real questionnaire; submitted all-1 answers
  for one account → `{totalScore: 6, riskCategory: "CONSERVATIVE"}`;
  submitted all-5 answers for a different account → `{totalScore: 30,
  riskCategory: "AGGRESSIVE"}`; submitted an invalid point value for a
  third account and confirmed it was rejected with the validation error,
  not silently accepted.
- Full verification: `gofmt`/`go vet`/`go build` clean, `go test -race
  ./...` clean across all of kyc-onboarding including the new package.
- **Known gaps, documented**: in-memory only; no auth; feeds nothing
  downstream (no Robo-Advisory feature exists yet) and gates nothing
  (e.g. F&O segment eligibility, which real brokers DO gate on investor
  risk profile) either; questionnaire design not reviewed by an actual
  compliance/suitability professional.

## Entry 40 — mid-session: git history rewritten to remove Claude co-author trailer, then git removed from the project entirely at the user's request

Two explicit user-directed changes to how this project is version-
controlled, unrelated to any FEATURES.md item:

1. Every commit this session (and every prior commit in the repo's
   history) carried a `Co-Authored-By: Claude Sonnet 5
   <noreply@anthropic.com>` trailer. The user asked for it removed so
   only their name shows as a contributor on GitHub. Commit AUTHORSHIP
   was already correctly set to the user (`Krishnaditya
   <krishnaditya65@gmail.com>`) — the trailer was the only thing adding
   Claude as a visible co-author. Rewrote all 29 commits' messages via
   `git filter-branch --msg-filter` to strip the trailer, verified 0
   occurrences remained and all 29 commits were still present with
   correct authorship, then force-pushed the rewritten history to
   `origin/main`.
2. Shortly after, the user said they'd rather delete this repo entirely
   and create/push a fresh one themselves. Per their explicit
   instruction ("Stop everything, remove git from this project I will
   delete the git repo"), removed the local `.git` directory entirely —
   this working tree is no longer a git repository. All source files,
   docs, and this session's in-progress work are untouched on disk;
   only version-control tracking was removed. No further commits or
   pushes will happen unless/until the user sets up a new repo and asks
   for it to be wired up.

This entry itself is being appended to `docs/BUILD_LOG.md` on disk per
the project's normal per-increment documentation habit, even though
there's no git commit to accompany it right now.

## Entry 41 — oms-gateway: closed a real gap in plain-language cancel-rejection reasons

FEATURES.md §21 (P1): "Plain-language order rejection reasons." Spawned
an independent verification pass over every rejection path in
oms-gateway to check how genuinely this item was already done (KYC/
freeze/risk rejections have been plain-language since day one — see
entry 1's original build). It found the core order-submission rejection
paths ARE genuinely plain-language (and the insufficient-margin one
includes the actual computed shortfall amount), but surfaced two real
gaps in `POST /orders/cancel`:

- The "order not found" case had a perfectly good plain-language string
  (`"no matching order found (already filled, cancelled, or never
  existed)"`) — but it was ONLY ever written to the audit trail. The
  actual `CancelOrderResponse` sent back to the caller had
  `ErrorMessage` left empty in that branch, so a client had no way to
  show the user WHY their cancel failed.
- The matching-engine handoff transport-failure case put the raw,
  engineer-facing wrapped Go error (host:port, dial errors, etc.)
  directly into the client-facing `ErrorMessage` — technically present,
  but not genuinely "plain-language."

Fixed both in `buildCancelOrderHandler`: the not-found case now puts the
same reason on both the audit entry AND the client response; the
transport-failure case now logs/audits the raw diagnostic error (still
useful for debugging) but returns a separate, genuinely plain-language
sentence to the client instead.

Verified live: cancelled a never-existent order id against the real
running service and confirmed the response now actually carries
`"No matching order was found to cancel — it may have already fully
filled, already been cancelled, or never existed."` — previously this
would have been an empty string.

Full verification: `gofmt`/`go vet`/`go build` clean, `go test -race
./...` clean across all of oms-gateway (no test needed updating — this
was a response-field-population fix, not a behavior-changing one at the
package level).

Marked FEATURES.md's "Plain-language order rejection reasons" item 🚧
(not fully ✅) — the matching-engine's own explicit `ErrorMessage` for a
rejected order SUBMISSION (as opposed to a cancel) is still an opaque
pass-through from matching-engine's side, not verified as plain-language
from oms-gateway's two files alone; a similar audit of matching-engine
itself would be needed to close that out fully.

## Entry 42 — kyc-onboarding: real KYC review queue + admin override

FEATURES.md §14 (P1): "Admin/backoffice panel: KYC review queue, manual
order intervention, account freeze/unfreeze." Account freeze/unfreeze
was already real (early in the session). This closes the "KYC review
queue" third: a real, additive capability that does NOT change the
existing synchronous auto-verify path (avoiding regression risk to
every already-verified flow this session built on top of it).

- `internal/kycstate`: new `ListRecordsByStage(stage) []KycRecord` — the
  actual review queue, sorted by account id, empty slice (not nil) for
  no matches. New `OverrideStage(accountId, newStage, reason)` — the
  admin decision a queue entry resolves to: force VERIFIED or REJECTED
  (never back to NOT_SUBMITTED), clearing the rejection reason on an
  override-to-VERIFIED, storing a new one on override-to-REJECTED.
  Requires the account to have submitted at least once. 6 new tests (11
  total, up from 5).
- New routes: `GET /kyc/review-queue` (defaults to REJECTED — the
  accounts actually worth a human looking at), `POST
  /kyc/review-queue/override`.
- Verified live: confirmed the normal auto-verify flow is completely
  unaffected (a valid PAN still auto-verifies exactly as before); a
  malformed PAN auto-rejects and immediately appears in the review
  queue; overriding it to VERIFIED removes it from the queue and `GET
  /kyc/status` reflects the override; overriding a never-submitted
  account correctly fails.
- Full verification: `gofmt`/`go vet`/`go build` clean, `go test -race
  ./...` clean across all of kyc-onboarding.
- **Known gaps, documented**: no auth on the override endpoint — anyone
  who can reach it can flip any account's trading eligibility. No audit
  trail entry for the override action itself (unlike oms-gateway's
  `audittrail` package) — a real build needs one for compliance.

## Entry 43 — ledger: real withdrawal workflow with T+N settlement holds

FEATURES.md §2 (P1): "Withdrawal workflow with T+N settlement holds."

- New `internal/withdrawalworkflow`: shares the SAME `doubleentry`
  ledger book the rest of the service uses. `RequestWithdrawal` places a
  HOLD (checked against `AvailableBalanceInMinorUnits` — raw balance
  minus every currently-pending hold, so holds correctly stack and can't
  double-spend each other) without touching the raw ledger balance yet.
  `ProcessDueWithdrawals` sweeps every hold whose settlement period has
  elapsed and posts a REAL, balanced journal entry through the same core
  every other mutation uses — money genuinely leaves the account at that
  point, not a status-field flip. `CancelWithdrawal` releases a still-
  pending hold. 13 tests, including the actual load-bearing assertion
  (the account's ledger balance genuinely drops and the clearing account
  genuinely receives it once `ProcessDueWithdrawals` runs, not before).
- New routes: `POST /withdrawals/request`, `POST /withdrawals/cancel`,
  `GET /withdrawals?accountId=...`, `POST /withdrawals/process-due`.
  `GET /accounts/balance` now returns both `currentBalanceInMinorUnits`
  (raw) and `availableBalanceInMinorUnits` (raw minus pending holds) —
  genuinely different numbers once any hold exists. New
  `WITHDRAWAL_SETTLEMENT_HOLD_DAYS` env var (default 2, T+2) for
  overriding the hold duration — set to 0 for live testing without
  waiting real days.
- Verified live end-to-end against a real running process
  (`WITHDRAWAL_SETTLEMENT_HOLD_DAYS=0`): funded an account, requested a
  withdrawal (available balance dropped by the hold amount, raw balance
  untouched), confirmed a second request exceeding what was left was
  correctly rejected (proving holds stack), ran `process-due` and
  confirmed the raw balance ACTUALLY dropped this time (matching
  available), then separately requested and cancelled another
  withdrawal and confirmed the balance was fully restored.
- Full verification: `gofmt`/`go vet`/`go build` clean, `go test -race
  ./...` clean across all of ledger including the new package.
- **Known gaps, documented loudly**: payout is a ledger-internal journal
  entry, not a real bank transfer — no real payment rail anywhere in
  this repo (same category of gap as kyc-onboarding's bank-verification
  penny-drop). `POST /withdrawals/process-due` is externally triggered,
  not run on a real scheduled job. In-memory only. No auth.

## Entry 44 — housekeeping: eliminated the strict phase-gating rule in FEATURES.md

Per explicit user instruction: FEATURES.md's phasing note previously
read "Build nothing tagged P2+ before its P0/P1 dependencies exist and
are tested" — a hard gate. Changed to make the `[P0]`–`[P4]` tags a
rough sequencing guide, not a blocker: a P2+ item can be picked up any
time, building any missing P0/P1 dependencies alongside it in the same
pass rather than treating the tag as something that must be fully
satisfied first. No code changes — a single doc-policy edit.

## Entry 45 — ledger: client fund segregation (`internal/fundsegregation`)

FEATURES.md §1: "Segregation of client funds vs. firm funds (regulatory
requirement in most jurisdictions — client money must be ring-fenced)".

- New package `internal/fundsegregation` classifies every ledger account
  as CLIENT or FIRM and enforces one real, checkable invariant on top of
  `internal/doubleentry`: a dedicated `client-money-custody-pool`
  account's balance must always equal the sum of every CLIENT account's
  balance.
- `PostClientMoneyMovement(clientAccountId, amount, description)` — the
  ring-fenced way to fund or pay out a client account. Posts a single
  balanced three-leg journal entry: client account and custody pool move
  together (same direction, same amount), with a new
  `external-cash-suspense` account as the real-world counterparty — the
  same clearing-account role `firm-clearing-acct` already plays
  elsewhere in this ledger, just dedicated to genuinely-external client
  cash instead of firm capital.
- `PostInterClientTransfer(from, to, amount, description)` — moves money
  between two CLIENT accounts without touching custody at all (correct:
  no client money enters or leaves custody in aggregate). Rejects
  outright if either side isn't classified CLIENT, so it can't be used
  to leak client money into a firm account.
- `CheckSegregationInvariant()` — the actual compliance-facing report:
  custody pool balance vs. aggregate client balance, live, with a
  computed discrepancy and an `IsSegregationIntact` bool.
- `ValidateEntryPreservesSegregation(entry)` — a dry-run check for an
  arbitrary journal entry (e.g. one about to be posted through the raw
  `/journal-entries` endpoint): would it move client money without a
  matching custody movement? Never posts anything itself.
- Wired into `services/ledger/cmd/server/main.go`: new seed accounts
  `client-money-custody-pool` and `external-cash-suspense`; new routes
  `POST /client-funds/deposit`, `POST /client-funds/transfer`,
  `GET /client-funds/segregation-report`, `POST
  /client-funds/validate-entry`.
- 13 new tests in `internal/fundsegregation` (deposit/payout balance
  correctness, zero/negative-amount rejection, unclassified-account
  rejection, invariant intact after several movements, inter-client
  transfer preserves the invariant and rejects non-client destinations,
  dry-run validation accepts/rejects the right shapes, account
  classification lookups).
- **Bug caught and fixed during live verification, not by a user
  report**: the guard's "not classified as CLIENT" error originally read
  "account is not classified as CLIENT or FIRM" — misleading when the
  account genuinely IS classified, just as FIRM (e.g. rejecting a
  transfer to `firm-clearing-acct`). Reworded to "account is not
  classified as a CLIENT account", which is accurate in both the
  genuinely-unclassified and classified-as-FIRM cases. Caught by reading
  the actual live HTTP response during verification, not by a test
  assertion (the tests only checked `errors.Is`, not message text) —
  worth remembering the value of exercising the real wire response, not
  just the Go error type.
- Verified live end-to-end against a real running process: deposited
  into two client accounts via `/client-funds/deposit` (segregation
  report stayed intact throughout), transferred between them via
  `/client-funds/transfer` (report stayed intact — no custody movement
  needed for a purely client-to-client move), attempted to transfer
  client money to `firm-clearing-acct` (correctly rejected, corrected
  error message confirmed on the wire), ran a payout (negative-amount
  deposit) and confirmed both balances and the report updated correctly,
  used `/client-funds/validate-entry` as a dry-run against the *old*
  funding pattern (debit client / credit `firm-clearing-acct`) and
  confirmed it was flagged without being posted, then — to prove the
  report isn't just always green — posted a real entry through the
  pre-existing, unmigrated `/journal-entries` endpoint using that exact
  old pattern and confirmed the segregation report correctly computed a
  genuine, nonzero discrepancy. Also confirmed the pre-existing
  `/journal-entries` and `/withdrawals/*` flows are completely
  unaffected by this change (posted an old-style funding entry, ran a
  full withdrawal request → process-due cycle, both worked exactly as
  before).
- Full verification: `gofmt`/`go vet`/`go build` clean, `go test -race
  ./...` clean across all of ledger including the new package.
- **Known gaps, documented loudly**: segregation is enforced ONLY on the
  new `/client-funds/*` endpoints — the pre-existing `/journal-entries`
  endpoint (used by oms-gateway for trade settlement and by this
  service's own README demo funding example) is NOT migrated, and can
  still produce a real discrepancy, as demonstrated above. The custody
  pool and suspense accounts are just more in-memory ledger accounts, no
  real segregated bank account behind them. Real build needs every
  client-money-touching path (deposits, withdrawals, trade settlement)
  migrated onto this package.

## Entry 46 — ledger: AML transaction monitoring (`internal/amlmonitoring`)

FEATURES.md §1: "AML transaction monitoring (unusual pattern flags, PEP
screening)".

- New package `internal/amlmonitoring`, decoupled from any specific
  money-movement mechanism — callers explicitly report transactions to
  it via `RecordTransaction`, same design principle used for
  `fundsegregation`.
- Three real rules evaluated on every reported transaction:
  `LARGE_TRANSACTION` (single transaction at/above a threshold),
  `VELOCITY` (too many transactions for one account within a rolling
  window), `STRUCTURING` (several individually-sub-threshold
  transactions within a window whose sum crosses the reporting
  threshold — the classic technique of splitting a large transaction to
  dodge a reporting requirement). Structuring deliberately does not fire
  for a single large transaction alone — that's `LARGE_TRANSACTION`'s
  job, not structuring.
- `ScreenName` — a static, case-insensitive PEP watch-list check, raises
  and stores a `PEP_MATCH` alert on a hit.
- `AlertsForAccount` / `AllAlerts` — the compliance review-queue views,
  chronologically sorted, empty-not-nil.
- Wired into `services/ledger/cmd/server/main.go`: `POST
  /client-funds/deposit` and `POST /withdrawals/request` both report
  every successfully-posted transaction to the monitor (additive only —
  neither handler's existing behavior changed). New routes `GET
  /aml/alerts` (optional `?accountId=`) and `POST /aml/screen-name`.
- 15 new tests: no alert for ordinary transactions; large-transaction
  alert fires on a single big transaction (positive or negative amount);
  velocity fires exactly on the transaction that crosses the limit, not
  before, not for transactions outside the window; structuring fires
  once sub-threshold transactions sum over the limit within the window
  (not for a single large or single sub-threshold transaction alone, not
  across a wider time gap); PEP name-match case-insensitivity and
  non-match; per-account and cross-account alert aggregation.
- Verified live end-to-end against a real running process: a single
  large deposit (₹15,00,000) triggered `LARGE_TRANSACTION` immediately;
  three deposits of ₹4,00,000 each (each individually under the
  ₹10,00,000 reporting threshold) into a second account triggered
  `STRUCTURING` on exactly the third deposit, once the running sum
  (₹12,00,000) crossed the threshold; a PEP-listed name ("Corrupt
  Official") matched case-insensitively, an ordinary name didn't; six
  withdrawal requests within an hour against a velocity limit of 5
  triggered real `VELOCITY` alerts on the 6th and 7th; the aggregate
  `GET /aml/alerts` view correctly showed all 5 alerts across both
  accounts, chronologically sorted; confirmed the pre-existing
  `/withdrawals/process-due` and balance-lookup flows are completely
  unaffected (a real process-due sweep still correctly paid out all six
  pending holds).
- Full verification: `gofmt`/`go vet`/`go build` clean, `go test -race
  ./...` clean across all of ledger including the new package.
- **Known gaps, documented loudly**: thresholds are illustrative
  constants, not derived from any real regulatory reporting limit (e.g.
  India's PMLA cash-transaction threshold) or tuned against real data.
  PEP screening is a static hardcoded name list, not a real
  sanctions/PEP database with fuzzy matching. No case-management
  lifecycle for a raised alert (no assign/investigate/close/escalate-to-
  STR workflow). `/journal-entries` (trade settlement) and
  `/withdrawals/process-due` are NOT reported to the monitor — only the
  deposit and withdrawal-*request* paths are, so trade flow isn't
  monitored yet. In-memory only, no persistence, no auth on the new
  endpoints.

## Entry 47 — ledger: simulated deposit rail + SIP payment mandates (FEATURES.md §2)

Built via a parallel background agent per explicit user instruction to
finish FEATURES.md §§2,3,4,6,7,8,9 in one pass; consolidated into the
shared docs by the orchestrating session afterward.

- New `internal/depositrail`: a two-phase UPI/NEFT/IMPS/net-banking
  deposit state machine — `POST /deposits/initiate` starts a deposit
  `PENDING` (no money moves), `POST /deposits/confirm` stands in for the
  bank webhook and is the ONLY place real money moves, posting through
  the existing `fundsegregation.SegregationGuard.PostClientMoneyMovement`
  (ring-fenced, same path `/client-funds/deposit` uses) and reporting to
  `amlmonitoring.Monitor`. Confirming twice is rejected — no double-post.
  Loudly documented as NOT a real bank integration (no UPI/NEFT/IMPS
  network call anywhere). 13 tests.
- New `internal/paymentmandate`: eNACH-style standing instructions for
  SIPs — register (account, amount, frequency, next debit date),
  pause/resume/cancel, and `POST /payment-mandates/sweep-due` (same
  "process-due" sweep pattern as `withdrawalworkflow`) that debits the
  account via the segregation guard and advances the next due date on
  every mandate whose date has arrived. Loudly documented as NOT real
  eNACH (no bank mandate registration happens anywhere). 17 tests.
- New routes wired into `cmd/server/main.go`: `/deposits/initiate`,
  `/deposits/confirm`, `/deposits`, `/payment-mandates/register`,
  `/pause`, `/resume`, `/cancel`, `/sweep-due`, `/payment-mandates`.
- **Bug caught and fixed before verification**: the first
  `ConfirmDeposit` draft had a TOCTOU race — checked status, released
  the lock, posted the ledger entry, then re-locked to set status,
  meaning two concurrent confirms on the same deposit ID could both post
  money. Fixed by claiming the deposit (flipping to CONFIRMED) while
  still holding the lock before posting, rolling back on a post failure.
  Confirmed via `TestDoubleConfirmDoesNotDoublePostMoney` and `-race`.
- Verified live end-to-end on a real running process (port 8082,
  `WITHDRAWAL_SETTLEMENT_HOLD_DAYS=0`): a UPI deposit's balance stayed
  unchanged right after `/deposits/initiate`, jumped only after
  `/deposits/confirm`, segregation report stayed intact throughout, a
  second confirm was rejected; a payment mandate swept, debited the
  account, and advanced its next due date, while a paused mandate's
  sweep correctly did nothing and a cancelled mandate's resume attempt
  correctly failed.
- Full sweep: `gofmt`/`go vet`/`go build` clean, `go test -race ./...`
  clean across the entire `services/ledger` module (7 packages).
- **Known gaps**: no real bank/NPCI network calls anywhere; sweep
  endpoints are manually/externally triggered, not on a real schedule;
  `/journal-entries`, `/withdrawals/process-due`, and
  `/payment-mandates/sweep-due` are not reported to AML monitoring
  (only `/client-funds/deposit`, `/withdrawals/request`, and
  `/deposits/confirm` are); swept SIP money has no real investment
  destination; no auth on any new endpoint.

## Entry 48 — oms-gateway: margin pledge, SPAN/exposure margin, Iceberg/FOK/IOC order types (FEATURES.md §3)

- New `internal/marginpledge`: pledge a held stock quantity as
  collateral — increases available margin by (quantity × price ×
  1-haircut), marks the quantity unavailable for sale, blocks a SELL
  that would dip into pledged (not-yet-unpledged) quantity, and refuses
  to unpledge quantity currently backing open utilized margin
  (`ErrPledgeStillBackingOpenMarginPosition`). 17 tests including a
  `-race`-targeted concurrent pledge/unpledge test.
- New `internal/marginengine`: an illustrative SPAN + Exposure margin
  calculator (`spanMargin + exposureMargin = totalRequiredMargin` off
  configurable illustrative rate constants, explicitly NOT an
  exchange-certified SPAN file). 11 tests including a fully hand-worked
  ₹5,00,000-notional example.
- `internal/riskengine`: added `AdjustAvailableMarginInMinorUnits`
  (signed delta) and `AvailableMarginInMinorUnits` (getter), used by the
  new pledge handlers to actually move the number the pre-trade risk
  engine checks against — pledging isn't cosmetic, it changes what a
  subsequent order is allowed to do.
- `internal/orders`: new `OrderExecutionType` (MARKET/LIMIT/SL/SL-M plus
  ICEBERG/FOK/IOC), `IcebergVisibleQuantity` validation. This is
  ACCEPTANCE and VALIDATION only — true Iceberg/FOK/IOC fill semantics
  need matching-engine order-book support that doesn't exist yet; every
  accepted order of these types logs a loud boundary warning saying so.
  10 tests.
- New routes: `/margin-pledge/pledge`, `/unpledge`,
  `/set-utilized-margin`, `/margin-pledge?accountId=`,
  `/margin/calculate-span-exposure`.
- Verified live end-to-end across a real 5-service run (oms-gateway +
  ledger + kyc-onboarding + backoffice + matching-engine, no Docker):
  pledged 20 shares → available margin rose by exactly the hand-computed
  170000; an oversized order's shortfall dropped by exactly that amount;
  selling more than the unpledged remainder was blocked
  (`PLEDGED_QUANTITY_UNAVAILABLE`), selling exactly the unpledged amount
  succeeded; unpledging released a proportional amount exactly; setting
  utilized margin above the safe remainder genuinely blocked a further
  unpledge, clearing it unblocked the same call; the SPAN/exposure
  calculator matched its hand-worked test exactly over HTTP; a
  ICEBERG order missing `icebergVisibleQuantity` and one exceeding total
  quantity were both rejected with clear errors, valid Iceberg and FOK
  orders were accepted and actually filled by matching-engine under
  ordinary continuous-matching rules (the documented boundary).
- Full sweep: `gofmt`/`go vet`/`go build` clean, `go test -race ./...`
  clean across the entire `services/oms-gateway` module.
- **Known gaps**: pledge haircuts and SPAN/exposure rates are
  illustrative constants, not real regulatory tables; pledge reference
  price is caller-supplied (no live price feed in oms-gateway yet);
  utilized margin for open derivative positions is set explicitly via
  an admin-style endpoint rather than derived from a structured F&O
  position book (doesn't exist yet); Iceberg/FOK/IOC fill semantics are
  not enforced by matching-engine.

## Entry 49 — new `services/mutual-funds`: AMC routing, SIP/lumpsum, step-up SIPs (FEATURES.md §4)

- New Go service (module `mercurius/mutualFunds`, port :8087) with three
  packages: `internal/fundcatalog` (5 illustrative static schemes with
  NAVs), `internal/amcrouting` (a PENDING→CONFIRMED purchase/redemption
  state machine standing in for a real AMC/RTA — units allocated at
  confirmation-time NAV, not fabricated instantly; loudly documented as
  NOT talking to any real AMC/BSE-StAR-MF/CAMS/KFintech API), and
  `internal/sipscheduler` (register/pause/resume/cancel a SIP, a
  "process-due" sweep matching ledger's withdrawal-workflow pattern,
  and step-up SIPs — the installment amount increases by a configured
  percentage on each anniversary of the SIP's start date).
- 36 tests across the three packages, including a hand-worked step-up
  boundary case: a ₹5,000/month SIP with 10% step-up stayed at ₹5,000
  for installments #1–#12 and became exactly ₹5,500 at installment #13
  (the first anniversary), driven deterministically via an explicit
  `now`/`asOf` parameter rather than sleeping real time.
- Verified live on a real running process (:8087): a lumpsum purchase
  allocated exactly `amount / navAtConfirmation` units after a
  simulated T+N confirmation sweep; a SIP's first due sweep executed
  and advanced its next due date, a second sweep at the same instant
  did nothing (idempotency proven); pausing froze the schedule, resuming
  let exactly one (not a backfilled catch-up) installment execute;
  invalid inputs (zero amount, unknown scheme) were rejected with clear
  errors.
- Full sweep: `gofmt`/`go vet`/`go build` clean, `go test -race ./...`
  clean, 36/36 passing.
- **Known gaps**: no real AMC/RTA integration anywhere; sweep endpoints
  are manually triggered, not scheduled; only MONTHLY frequency
  implemented; no KYC gating; no ledger/cash-side integration (purchases
  don't debit any account yet — this service and `ledger` aren't wired
  together); no persistence, no auth; the `?asOf=` time-override used
  for deterministic testing has no access control and isn't meant for
  production.

## Entry 50 — quant-engine: risk statistics, arbitrage scanner, backtest runner, pairs-trading strategy (FEATURES.md §6, §7)

- New `riskStatistics.py`: annualized Sharpe ratio, annualized Sortino
  ratio (downside-deviation denominator), and max drawdown from an
  equity curve. Hand-worked test case: series `[0.04,-0.02,0.04,-0.02,
  0.04,-0.02]` → Sharpe = 5.291502622129181 at rf=0/252
  periods-per-year; equity curve `[100,120,90,150,60,130]` → max
  drawdown = 0.6 exactly (peak 150 @idx3, trough 60 @idx4). 14 tests.
- New `arbitrageScanner.py`: theoretical-vs-live price deviation check
  against a configurable threshold (absolute + percentage deviation,
  triggered bool, overpriced/underpriced direction). Hand-worked:
  theoretical=100, live=102 → deviation=2, 2%, triggered at a 1%
  threshold. 9 tests.
- New `backtesting/` subpackage: `tickStore.py` (in-memory per-symbol
  tick series with range queries, 6 tests), `backtestRunner.py` (a real
  event-driven backtest loop — replays ticks in order, tracks cash/
  position/realized+unrealized P&L deterministically; a test runs the
  same input twice and asserts byte-identical output, 7 tests),
  `pairsTradingStrategy.py` (real z-score mean-reversion over a price
  spread with configurable entry/exit thresholds, wired as a
  `backtestRunner` strategy callback and proven to actually enter and
  exit at least one position against fixture data, 9 tests).
- Wired `POST /risk/statistics` and `POST /arbitrage/scan` into the
  existing `httpServer.py` (port 8085). Backtest runner and pairs
  strategy verified via pytest only (not naturally HTTP-shaped) — an
  explicit, documented time-budget tradeoff, not an oversight.
- Verified live over HTTP on a real running process: both new endpoints
  returned the exact hand-worked numbers (Sortino differed by 2e-15
  float noise, immaterial); zero-variance and non-positive-price inputs
  correctly returned 422; pre-existing `/options/price` re-verified
  unaffected.
- Full sweep: 65 tests total (45 new + the pre-existing 20 all still
  green), pytest clean across the whole service.
- **Explicitly out of scope, documented not faked**: "Paper trading mode
  sharing the exact same OMS code path as live" and "Strategy
  deployment pipeline: backtest → paper → live promotion gates" — both
  need `oms-gateway` integration beyond quant-engine's boundary.
- **Known gaps**: pairs strategy trades the spread as one synthetic
  instrument rather than sizing two real hedge-ratio legs; formulas are
  real math, not tuned against live market data; backtest runner/pairs
  strategy have no HTTP exposure yet.

## Entry 51 — market-data: real-time L1 WebSocket broadcast + sequence-numbered resync protocol (FEATURES.md §8)

- New `l1QuotePublisher.rs`: derives L1 state (best bid/ask/last trade)
  from the service's real depth-publish stream, with monotonic
  per-instrument sequence numbers and current-snapshot retention. 9
  tests.
- New `l1QuoteWireProtocol.rs`: tagged-enum JSON wire types —
  `OutgoingL1StreamMessage` (SNAPSHOT/DELTA) and
  `IncomingL1StreamClientMessage` (RESYNC_REQUEST). 3 tests.
- New `l1QuoteWebSocketServer.rs`: a real `tokio`/`tokio-tungstenite` WS
  server on `127.0.0.1:9104` — every new connection gets a SNAPSHOT
  (current state + its sequence number) before switching to DELTA
  messages, so a client can detect a sequence gap and know to resync
  rather than silently corrupting its view; a RESYNC_REQUEST triggers a
  fresh SNAPSHOT. 4 real integration tests spinning an actual bound
  listener and a real `tokio-tungstenite` client, not mocks.
- Also cleaned up pre-existing clippy/rustc warnings across
  `marketDataEventTypes.rs`, `candleAggregator.rs`, `deltaPublisher.rs`,
  `watchlist.rs`, and `httpQueryServer.rs` so the WHOLE crate — not just
  new code — is warning-clean; added `rustfmt.toml` (`max_width = 120`)
  since this repo's mandated long camelCase names exceed rustfmt's
  default 100-column width on files nobody touched this build.
- Verified live: ran real `matching-engine` (9101) and `market-data`
  (9102/9103/9104) processes, submitted real crossing orders over TCP,
  connected a real Python `websockets` client to `ws://127.0.0.1:9104`
  and received correctly-sequenced DELTA messages reflecting the real
  fills; a second, later-connecting client received a SNAPSHOT that
  correctly caught it up to the current state; a RESYNC_REQUEST
  correctly returned a fresh SNAPSHOT at the same sequence number;
  pre-existing `GET /trades`/`GET /candles` re-verified unaffected.
- Full sweep: `cargo fmt --check`/`cargo clippy`/`cargo build` clean,
  `cargo test` 47/47 passing (was 33 before this build) across the
  whole crate.
- **Known gaps**: fan-out is in-process only (no Kafka); no per-symbol
  WS subscription filtering (broadcasts all instruments); no WS auth;
  depth-delta and trade-tick feeds still aren't pushed over WS, only L1
  got real push this build; everything in-memory, restart loses state.

## Entry 52 — matching-engine: write-ahead log + deterministic crash-replay harness (FEATURES.md §9)

- New `writeAheadLog.rs`: fsync'd, append-only NDJSON WAL — one record
  per book-mutating event (OrderAccepted, OrderCancelled,
  TradeExecuted) — with a reader that tolerates a torn tail and a
  replay function reconstructing a fresh `OrderBookCore` from a WAL
  file. 8 tests.
- New `walBackedOrderBook.rs`: wraps `OrderBookCore` with WAL logging;
  withholds acknowledgement on any WAL write error so a client never
  sees a "success" the log doesn't back. 3 tests.
- New `deterministicReplayHarness.rs`: runs an arbitrary operation
  sequence through a live book and through a WAL-replay-reconstructed
  book and asserts they match exactly (order-for-order, price-level-
  for-price-level, via a new `fullBookStateSnapshotForTesting()` added
  to `orderBookCore.rs`) — 5 hand-written scenarios plus a loop over 12
  seeded pseudo-random sequences. 6 tests.
- `main.rs`: wired `WalBackedOrderBook` into the live server loop
  (`MATCHING_ENGINE_WAL_FILE_PATH` env override) and added an offline
  `cargo run -- --replay <path>` recovery-inspection mode.
- Verified live with REAL infrastructure, not just tests: ran the
  matching-engine server for real with a real WAL file path, drove 5
  real orders over the actual TCP+JSON wire protocol from a standalone
  Python socket client (resting sell, non-crossing buy, crossing buy,
  a cancel, another crossing buy), inspected the resulting WAL file
  directly with `cat`/`xxd` (7 real NDJSON lines: 4 OrderAccepted, 1
  OrderCancelled, 2 TradeExecuted), killed the server, then ran the
  separate `--replay` process against that exact file and confirmed the
  reconstructed book (`ASKS: 100 x 4`, no bids, 0 pending stops) matched
  what the live sequence should leave resting.
- Full sweep: `cargo fmt --check`/`cargo clippy --all-targets -- -D
  warnings`/`cargo build` clean, `cargo test` 46/46 passing (was 29
  before this build, zero regressions) across the whole crate.
- **Known gaps**: no WAL compaction/snapshotting (log grows unbounded);
  no per-record checksums (relies on JSON parse failure alone to catch
  corruption); WAL logs commands, not book internals, so "which resting
  order absorbed this fill" needs replay to re-derive, not a direct WAL
  read.

## Entry 53 — ledger: multi-currency wallet (FEATURES.md §2)

Second parallel-agent round, finishing every remaining item in FEATURES.md
§§2,3,4,6,7,8,9 per explicit user instruction. Six lanes, each scoped to
one service, consolidated into the shared docs by the orchestrating
session afterward.

- New `internal/multicurrencywallet`: each (accountIdentifier,
  currencyCode) pair is tracked as its own real `doubleentry` ledger
  account (native currency is an alias for the existing raw account, so
  nothing about the pre-existing INR balance/segregation path changed).
  Real deposit/withdraw per currency wallet; real conversion between two
  currency wallets of the same account via a static illustrative FX rate
  table (e.g. USD/INR=83.0), posted as a real balanced journal entry
  through an `fx-conversion-clearing-acct`. `GET /wallets?accountId=`
  lists every currency balance. 18 tests.
- Additive-only change to `internal/doubleentry`: one new method,
  `RegisterAccountIfAbsent` — no existing signature touched.
- **Design bug caught and fixed before implementation, not after**: an
  early draft would have decoupled the native-currency wallet balance
  from the raw account it's supposed to alias (deposits would move one,
  reads would come from the other). Fixed before any live verification
  by making the native currency an explicit alias consistently
  everywhere, not a separate sub-account.
- Verified live: depositing into acct-001's USD wallet left its INR
  balance and the segregation report completely untouched; converting
  100.00 USD → INR at the static rate moved exactly 830000 minor units,
  no rounding; withdrawing from an empty currency wallet was rejected
  even with a large balance sitting in a different currency (per-currency
  isolation, not per-account); an unconfigured currency pair (GBP→JPY)
  was rejected with a clear error.
- Full sweep: `gofmt`/`go vet`/`go build` clean, `go test -race ./...`
  clean across all 8 packages in services/ledger, including
  `internal/fundsegregation`'s pre-existing INR invariant tests
  unchanged.
- **Known gaps**: FX rate table is static/hardcoded, no live feed, no
  bid/ask spread; no real foreign-currency custody/settlement rail (a
  real global-stocks broker needs an actual USD account somewhere real,
  not an internal ledger sub-account); no LRS (Liberalised Remittance
  Scheme) limit tracking; non-native currency wallets are deliberately
  NOT included in `fundsegregation`'s CLIENT/FIRM classification —
  mixing foreign-currency minor units into the INR custody-pool sum
  would be numerically meaningless, so today's segregation invariant
  only covers INR.

## Entry 54 — oms-gateway: margin funding, options chain + live Greeks, illustrative DMA/FIX gateway, paper trading, algo circuit breakers (FEATURES.md §2, §3, §7)

Five items built sequentially inside one agent run to avoid concurrent
edits to `cmd/server/main.go`.

- **`internal/marginfunding`** (§2 P2, margin funding / instant margin
  against pledged collateral): real cash disbursement up to unpledged
  collateral value, via a real journal entry posted through
  `internal/ledgerclient` (new `PostMarginFundingDisbursementJournalEntry`
  / `...RepaymentJournalEntry`), tracked as a real outstanding loan
  balance. **Bug caught via live verification, not a test**: an early
  draft credited the client account for a disbursement, which under
  `doubleentry`'s real "debit increases" convention actually *decreased*
  it — the balance moved the wrong direction live, caught immediately,
  fixed by swapping the journal entry's debit/credit lines. 16 tests.
- **`internal/optionschain` + `internal/quantengineclient`** (§3 P2,
  real-time Options Chain + live Greeks): a synthetic strike ladder with
  illustrative OI/Volume, but REAL Greeks/IV per contract via a real HTTP
  call to quant-engine's live Black-Scholes endpoint, and a real
  Put-Call-Ratio computed from the (synthetic) OI numbers. Degrades
  cleanly (502, not a crash) if quant-engine is down — verified live by
  killing it mid-run. 21 tests.
- **`internal/dmagateway`** (§3 P4, DMA/FIX gateway): a real session-based
  TCP protocol on port 8088 — Logon/NewOrderSingle/ExecutionReport/Logout
  with real sequence-number enforcement (an out-of-sequence message is
  rejected and the connection closed) — explicitly, repeatedly documented
  as NOT FIX-certified (no real FIX tag numbers, no SOH/BeginString
  framing, no ResendRequest gap-fill). Reuses the same internal
  order-submission path as the HTTP API, so it's genuinely risk-checked,
  not a side door. Verified live with a standalone Python TCP client:
  full session lifecycle, a genuinely risk-rejected order, an
  out-of-sequence rejection, a pre-Logon rejection, and confirmation the
  accepted order hit the real audit trail. 23 tests.
- **`internal/papertrading`** (§7 P3, paper trading mode sharing the same
  OMS code path): a paper order goes through the exact same risk engine
  and audit trail as a live order, but the matching-engine hand-off is
  replaced with a simulated fill and nothing is posted to ledger. Proven
  live: paper fills left the real ledger balance and real `/positions`
  completely untouched while `/paper-positions` accumulated separately;
  an oversized paper order was genuinely rejected by the real risk
  engine, not rubber-stamped. 9 tests.
- **`internal/algolimits`** (§7 P4, strategy resource limits & circuit
  breakers): real per-`strategyId` rate limiting (orders/sec) and daily
  notional caps, enforced before an order reaches the risk engine or
  matching-engine. Verified live: rate-limit rejection, refill-then-
  succeed, the exact daily-cap boundary, post-cap rejection, and
  confirmation untagged orders are unaffected. 18 tests.
- Full sweep: `gofmt`/`go vet`/`go build` clean, `go test -race ./...`
  clean across the entire `services/oms-gateway` tree (90 new tests, zero
  regressions to the pre-existing suite).
- **Known gaps per item**: margin funding's interest formula isn't
  auto-applied and reuses the existing clearing account rather than a
  dedicated one; options chain OI/Volume are illustrative and "IV" is
  really an assumed flat input, not solved from real market prices — a
  truly complete version needs a real listed-options feed; the DMA/FIX
  gateway needs a certified FIX engine and real exchange onboarding to
  be genuinely complete; paper trading's SELL gate still checks real
  positions (conservative, not a hole); algo limits are per-strategy
  only, no global circuit breaker yet, config is in-memory.

## Entry 55 — mutual-funds: rebalancing baskets, robo-advisory, goal-based investing (FEATURES.md §4)

- **`internal/basketrebalancing`**: named target-weight baskets across
  fundcatalog schemes (weights validated to sum to exactly 100%),
  lumpsum subscription routes proportional purchases through
  `amcrouting`, and a real one-click rebalance that compares current
  holding value per scheme against target weights and computes exact
  buy/sell orders to correct drift. 18 tests, hand-worked NAV-move
  example verified live: a BLUECHIP NAV move produced an exact SELL of
  2500 (12.5 units) funding a BUY of 1500 MIDCAP + 1000 LIQUID.
- **`internal/roboadvisory`**: risk category → illustrative model
  allocation across EQUITY/DEBT/HYBRID (matching kyc-onboarding's risk
  categories), with a REAL call to quant-engine's live `/risk/statistics`
  endpoint surfacing real Sharpe/Sortino/max-drawdown alongside the
  recommendation — degrades cleanly when quant-engine is down (verified
  live by killing it mid-request, then confirming real numbers return
  once it's back up: Sharpe 0.7283, Sortino 1.3522). 15 tests.
- **`internal/goalinvesting`**: named goals (RETIREMENT/EDUCATION) linked
  to SIPs/baskets, real progress tracking, and an on-track projection
  under an illustrative assumed growth rate. Hand-worked and live-verified
  exactly: 10000 current value, 12 months remaining, 1000/month
  contribution → projected value 23346 (`10000×1.007¹² +
  1000×((1.007¹²−1)/0.007)`), matched to the integer. 19 tests.
- Full sweep: `gofmt`/`go vet`/`go build` clean, `go test -race ./...`
  clean, 88 tests total across the whole service.
- **Known gaps**: Robo-Advisory's allocation table is explicitly
  illustrative, not a real mean-variance/Efficient Frontier optimization
  — this repo has no historical return/covariance data for the
  fictitious catalog schemes; goal projection's assumed growth rate is
  illustrative, not a forecast, and doesn't vary by the goal's actual
  linked-scheme risk mix; no cross-referencing between goals/baskets and
  actual SIP records — a caller supplies linked scheme IDs by hand.

## Entry 56 — quant-engine: GARCH, correlation matrix, VaR, volatility surface, strategy lifecycle, market-making sandbox, illustrative sentiment hook (FEATURES.md §6, §7)

Seven items built sequentially inside one agent run.

- `garchVolatilityForecaster.py`: a real GARCH(1,1) recursion (variance-
  targeting + grid-search quasi-MLE fit, documented as a simplified
  method, not a production optimizer) producing a next-period volatility
  forecast and an Expected Intraday Range. 22 tests, hand-worked
  recursion matched exactly.
- `correlationMatrixEngine.py`: real pairwise Pearson correlation matrix
  + a configurable-threshold candidate-pairs filter for pairs-trading
  discovery. 14 tests, hand-worked r=0.8315218406202999.
- `valueAtRiskCalculator.py`: real historical VaR (percentile-based) and
  real parametric VaR (mean/stddev + z-score, reusing the existing
  normal-CDF solver), plus an illustrative-scenario stress test. 19
  tests. **Bug found and fixed**: the historical VaR percentile index was
  landing one index low due to float representation error (`(1-0.90)*10`
  evaluating to `0.9999999999999998`, not `1.0`) — fixed with an epsilon.
- `volatilitySurfaceBuilder.py`: solves real IV per (strike, expiry) quote
  by reusing the existing Black-Scholes IV solver, assembles a surface,
  linear-interpolates across strikes. 11 tests, hand-worked interpolation
  (strikes 90/100/110 → IVs 0.25/0.20/0.22, strike 95 → 0.225) matched.
- `strategyLifecycle.py`: a real BACKTESTING→PAPER_TRADING→LIVE (or
  REJECTED) promotion state machine gated on real backtest-runner and
  paper-trading-track-record results against configurable, documented-
  illustrative minimum bars. 17 tests.
- `marketMakingSandbox.py`: real two-sided quote tracking, simulated
  taker fills against those quotes, and a real inventory risk limit that
  rejects a quote update that would push simulated inventory past the
  configured long/short limit. 15 tests. Verified live over HTTP,
  including the exact rejection message for a limit-breaching fill.
- `illustrativeSentimentTradingHook.py`: an explicitly-toy lexicon-based
  sentiment scorer over fixture text, producing a structured BUY/SELL/
  HOLD suggestion ONLY when a kill-switch flag is explicitly enabled
  (default OFF) — never places a real order itself, wiring to a real
  order path was explicitly and deliberately not attempted. 14 tests.
- GARCH, correlation, VaR, and market-making were wired into the real
  HTTP server and verified live via curl matching hand-worked values
  exactly; volatility surface, strategy lifecycle, and the sentiment hook
  are pytest-verified only (explicit, documented time-budget choice).
- Full sweep: 186 tests total (up from 65), all green throughout,
  re-verified after every one of the seven items, not just at the end.
- **Known gaps**: GARCH fit is a simplified quasi-MLE, not production-
  grade; VaR stress scenarios and strategy-lifecycle promotion gates are
  illustrative, not regulatory-calibrated; volatility surface
  interpolates only across strikes, not across expiries; market-making
  sandbox is single-price-level; the sentiment hook never ingests real
  filings/earnings data and never self-triggers a real order.

## Entry 57 — market-data: standalone simulated exchange feed, columnar tick store, real UDP multicast (FEATURES.md §8)

- `simulatedExchangeFeedGenerator.rs`: a deterministic (seeded) per-symbol
  random-walk tick generator feeding the EXACT SAME ingestion pipeline
  real matching-engine ticks use (the shared `ingestDepthPublishMessage`
  function was extracted specifically to guarantee this) — runs this
  entire service fully standalone for Phase 0-1 demos/tests with zero
  dependency on matching-engine being up. 11 tests including same-seed-
  twice-is-identical determinism.
- `columnarTickStore.rs`: a real struct-of-arrays (not array-of-structs)
  columnar tick store with binary-search range queries, wired into the
  real ingestion path and exposed via a new `GET /ticks/range` endpoint.
  12 tests.
- `udpMulticastPublisher.rs`: a real UDP multicast publisher (actual
  `IP_ADD_MEMBERSHIP` group join, not a simulation) broadcasting trade
  ticks and L1 quotes in a compact binary format. 10 tests including 2
  real send/receive multicast integration tests.
- **Bug found and fixed**: `DeltaPublisher`'s sink closure type lacked a
  `Send` bound — compiled fine when single-threaded, broke once it needed
  to move behind `Arc<Mutex<_>>` to be shared with the new simulated-feed
  producer thread. Fixed by adding the bound and updating its existing
  test from `Rc<RefCell<_>>` to `Arc<Mutex<_>>`.
- Verified live with NO matching-engine process running at all (confirmed
  via `ps aux`): real sequenced depth publishes purely from the simulated
  feed, real candles/trades/tick-range query results, a real Python
  `websockets` client receiving correctly-sequenced SNAPSHOT/DELTA
  messages, and a real Python UDP client joining the multicast group and
  decoding 5 real datagrams from the live process.
- Full sweep: `cargo fmt --check`/`cargo clippy --all-targets -- -D
  warnings`/`cargo build` clean, `cargo test` 83/83 passing (up from 47),
  stable across 3 repeated runs.
- **Known gaps**: UDP multicast is a single fixed group/TTL 1, no
  reliability/gap-detection layer, no auth/encryption; simulated feed's
  symbol list/drift/volatility are hardcoded, not fully env-configurable;
  columnar tick store duplicates trade-tick storage alongside the
  existing candle aggregator's shorter trade tape (different retention
  purposes, but real duplication); everything remains in-memory only.

## Entry 58 — matching-engine: lock-free SPSC ring buffer ingress/egress (FEATURES.md §9)

- `lockFreeSpscRingBuffer.rs`: a hand-rolled, genuinely lock-free
  single-producer/single-consumer ring buffer built on `std::sync::atomic`
  (no mutex anywhere on the enqueue/dequeue hot path), split into
  `RingBufferProducerHandle`/`RingBufferConsumerHandle` so the type
  system enforces the single-producer/single-consumer invariant the
  `unsafe` code relies on for soundness.
- `main.rs` now runs two real threads — a network thread (owns the
  `TcpListener`, reads/writes) and a matching-core thread (owns the
  `WalBackedOrderBook`) — connected ONLY by two ring buffers (ingress and
  egress), replacing whatever direct synchronous call previously
  connected them. `handleOneIncomingOrderLine`'s logic is reused
  verbatim on the core thread — price-time priority, WAL logging, and
  every other existing behavior is unchanged.
- 4 `unsafe` blocks, each with a documented soundness argument tied to
  the Acquire/Release handoff protocol and the structural single-
  producer/single-consumer guarantee (see `docs/DOCUMENTATION.md`'s
  matching-engine section for the full reasoning).
- 6 new tests: single-threaded FIFO/full/empty/wraparound correctness,
  plus two REAL multi-threaded stress tests — 2,000,000 `usize` items on
  one buffer, and 200,000 heap-allocated `String` items on a
  capacity-8 buffer specifically to force frequent full/empty contention.
- Verified live: real TCP wire-protocol traffic through the new two-
  thread path (same 5-order + status-query sequence as the WAL
  verification, correct trades/sequence-numbers/cancel/NOT_FOUND
  throughout), the resulting WAL inspected and independently replayed
  successfully via a separate process; then two real load passes against
  the live server — 2,000 sequential requests and 2,000 requests from 20
  concurrent client threads — zero errors, and the correlation
  `debug_assert_eq!` between ingress and egress items never fired,
  confirming FIFO correctness under genuine concurrent pressure.
- Full sweep: `cargo fmt --check`/`cargo clippy --all-targets -- -D
  warnings`/`cargo build` clean, `cargo test` 52/52 passing — all 46
  pre-existing tests (order book, WAL, deterministic replay) still pass
  completely unmodified, zero regressions.
- **Known gaps**: at most one request is in flight through the ring
  buffers at a time (the network thread blocks on egress before
  accepting the next connection) — the ring buffers are real and
  lock-free but pipelining isn't exploited yet; no supervision if the
  matching-core thread panics; blocking push/pop use a plain spin loop
  rather than spin-then-park.

## Entry 59 — apps/web + oms-gateway: options chain UI, notifications, opt-in strategy following (FEATURES.md §11)

Third parallel-agent round, finishing FEATURES.md §§11,12,13,15,17,18 per
explicit user instruction ("whatever can be written and done, finish
them" — 14/16 deliberately excluded, not requested). Four initial lanes,
plus two follow-up passes needed to work through oms-gateway's large
§15/§17 backlog to near-completion.

- Real `apps/web` options-chain page calling oms-gateway's real
  `GET /options/chain` (live Greeks, real PCR) — verified live, including
  a clean 502 (not a crash) when quant-engine was killed mid-session.
- Real Web Notifications API integration for order-fill and price-alert
  events, driven by real polling against oms-gateway's audit trail and
  market-data's existing price-alerts feature — the entire data-plumbing
  path proven live end-to-end; the actual `Notification.requestPermission()`
  / browser-popup step itself couldn't be exercised (no interactive
  browser available to the build agent), documented honestly rather than
  claimed. Margin-call notifications have no real event-driven backend
  trigger (oms-gateway's margin state isn't event-driven) — shipped as an
  explicitly-labeled best-effort heuristic, not a real margin call.
- New `internal/strategyfollowing` in oms-gateway: real opt-in
  follow/unfollow relationship graph + an admin-verified strategy
  registry — explicitly NO order mirroring/copying, disclosed and
  documented as follow/unfollow only. 18 tests. Verified live: follower
  counts, unfollow, and rejection of following an unverified strategy id.
- `apps/web` has no test runner (`package.json` has no Jest/Vitest/
  Playwright) — documented honestly rather than fabricated; rigor
  substituted with `tsc --noEmit`/`lint`/`build` all clean plus live
  verification.
- **This lane's edits to `oms-gateway/cmd/server/main.go` landed
  concurrently with the §12 lane and the api-gateway lane's
  chaos-testing/FIX-conformance additions to the same service — verified
  independently by the orchestrating session afterward that all three
  coexist correctly with zero lost work** (see the wrap-up note at the
  end of this entry group).

## Entry 60 — oms-gateway: mark-to-market, auto-liquidation, exposure limits, connectivity kill-switch (FEATURES.md §12)

- `internal/marktomarket`: real weighted-average cost-basis P&L tracking
  fed by real fills, scoped to leveraged accounts only (outstanding
  margin-funding principal or pledged quantity). 13 tests. Live: bought
  20 shares @₹100, pledged 10, pushed price ₹110 → exactly ₹20,000
  unrealized P&L; an unleveraged account correctly showed none.
- `internal/autoliquidation`: real graduated WARNING(80%)/URGENT(90%)/
  LIQUIDATION(100%+) utilization classification, with real reducing
  MARKET SELL orders submitted through the exact live order-submission
  pipeline ONLY at the LIQUIDATION threshold. 15 tests, hand-worked
  sizing verified. Live: WARNING correctly submitted zero orders; a real
  breach to 117.6% utilization genuinely submitted and filled a real
  26-share reducing sell.
- `internal/exposurelimits`: real pre-trade per-account AND per-segment
  notional caps, rejecting with a machine-readable code identifying
  which limit tripped. 12 tests including a 200-goroutine concurrency
  test (exactly 100 of 200 succeed against a 1000-unit cap). Live:
  exact-shortfall rejection message verified.
- `internal/connectivitykillswitch`: real MANUAL + AUTO-triggered
  (3-consecutive-failures) trading halt — blocks new submissions,
  deliberately never blocks cancellation. 12 tests. **Bug found and
  fixed**: the initial design had no way to self-heal once auto-engaged,
  since the halt itself blocked the only connectivity signal (the order
  path) — fixed by adding an independent background prober untouched by
  the halt gate. Live: 3 consecutive matching-engine failures auto-halted
  trading; a separate zero-traffic run showed the background prober
  alone both engaging AND self-healing the switch.
- Full sweep: `gofmt`/`go vet`/`go build` clean, `go test -race ./...`
  green across the entire service (52 new tests, zero regressions).

## Entry 61 — oms-gateway: execution algos, options payoff diagram (FEATURES.md §15, first pass)

- `internal/executionalgos`: real TWAP/VWAP/POV parent-order slicing.
  Largest-remainder apportionment (never loses/duplicates a share to
  rounding); deterministic `PollDueSlices(now)` scheduler (no sleeping in
  tests); POV participates in real observed volume with a max-clip cap.
  13 tests. Live: TWAP 1000/4-slice → exactly 250×4; VWAP weights 1:4:3:2
  → exactly 100/400/200/300; POV 10%/50-cap → 30 then a capped 50 —
  every number matched the unit tests exactly.
- `internal/payoffdiagram`: exact piecewise-linear options payoff
  analysis (not sampling) — max/min payoff provably attained at spot=0
  or a strike, breakevens via exact linear interpolation. 15 tests,
  textbook cases matched exactly: long straddle (breakevens 90/110,
  unbounded profit), long strangle (breakevens 84/116), bull call spread
  (max profit/loss both 5, breakeven 105), naked short call (unbounded
  loss), cash-secured short put (loss capped at 94 at spot=0).
- Full sweep: green across the whole service, confirmed after both items.

## Entry 62 — oms-gateway: multi-leg options, basket orders, impact-cost estimator, portfolio margining, SLB desk, extended-hours sessions, DRIP, loan against securities (FEATURES.md §15+§17, second pass)

- `internal/multilegoptions`: real named-strategy leg-shape validation
  (STRADDLE/STRANGLE/BULL_CALL_SPREAD/BEAR_PUT_SPREAD/IRON_CONDOR/
  BUTTERFLY) with atomic execution — real compensating-order rollback if
  a later leg is rejected. 27 tests. **Bug found and fixed**: iron
  condor's buy/sell direction checks were inverted. Live: a valid
  straddle's both legs accepted; an invalid shape rejected pre-
  submission; an exposure-limit-triggered mid-execution rollback
  demonstrated live, including surfacing the rollback's own failure mode.
- `internal/basketorders`: net-cash-constrained, non-atomic aggregate
  execution across N instruments, quantity- or weight-mode constituents.
  20 tests.
- `internal/impactcostestimator`: real walk-the-book slippage estimate
  over a caller-supplied depth snapshot (matching-engine/market-data
  have no queryable depth endpoint yet — documented honestly). 13 tests,
  hand-worked numbers matched exactly.
- `internal/marginengine` extended with real portfolio/cross-margining —
  illustrative correlation-table netting benefit atop the existing SPAN+
  exposure calculator. 21 tests. Live hand-worked: ₹72,800 net margin
  from ₹1,17,000 gross.
- `internal/securitieslendingborrowing`: real lend/borrow state machine,
  two independent mutex-guarded ledgers, real day-count fee formula. 20
  tests.
- `internal/marketsession` extended with real PRE_MARKET/POST_MARKET
  phases enforcing LIMIT-only order acceptance for real in
  `processOrderSubmission`. 13 tests. Live: a market order rejected
  pre-market, a limit order accepted and filled.
- `internal/drip`: real dividend-credit + toggleable auto-reinvestment
  through the real order path; 2 new `ledgerclient` tests for the
  dividend-credit journal entry. 12 tests. Live: exact math verified.
- `internal/loanagainstsecurities`: a distinct, stricter-LTV, longer-
  tenure loan product against pledged securities (vs. margin funding's
  trading-advance model) — real disbursement/repayment via ledger. 21
  tests plus 3 new `ledgerclient` tests. Live: 50% LTV cap of ₹42,500 →
  ₹21,250 disbursement, exact.
- **Fractional share investing (§17) explicitly NOT attempted** — its
  blast radius (`internal/orders.OrderQuantity uint64` and every
  consumer) was judged too structurally invasive for this pass without
  risking the "don't break any existing integer-quantity test"
  constraint. README documents a recommended additive approach (a
  parallel `MilliShareQuantity` field, mirroring the precedent set by
  `OrderExecutionType`) for a future pass.
- Full sweep confirmed green after every item across both passes; all 8
  completed items plus the 2 from Entry 61 bring §15 to 8/8 and add DRIP
  + LAS to §17.

## Entry 63 — new `services/api-gateway`: SLO alerting, secrets abstraction, ledger backup/restore, DR runbook, chaos/load testing, tiered rate limiting, public dev API, webhooks, white-label tenancy, FIX conformance suite, TCA, illustrative account aggregator (FEATURES.md §13, §18)

- New Go service (module `mercurius/apiGateway`, port :8089), 9 internal
  packages, real reverse proxy in front of ledger/oms-gateway/mutual-
  funds/market-data/quant-engine. 113 tests.
- `internal/sloalerting`: continuous-breach evaluator (unit-testable
  with synthetic samples) + a live `MetricsPoller` against oms-gateway's
  audit trail, market-data's `/trades`, and a real matching-engine TCP
  dial — `GET /alerts`.
- `internal/secretsprovider` + `config/secretsAccessMatrix.yaml`: a real
  working env-var-backed provider behind a real interface (swappable for
  Vault/AWS Secrets Manager later, documented as such) plus a concrete,
  real per-service access matrix — not fabricated cloud-IAM enforcement
  that doesn't exist here.
- **Ledger backup/restore**: additive `GET /admin/snapshot` /
  `POST /admin/restore` in `ledger`'s `internal/doubleentry`
  (`snapshotRestore.go`), a real backup script, and a real restore DRILL
  test — snapshot, mutate further, restore, assert state matches the
  snapshot exactly (not the further-mutated state) — proven live against
  a real running ledger process, not just unit-tested.
- `DR_RUNBOOK.md` (repo root): concrete per-tier RTO/RPO targets, a real
  runnable failover drill exercising what's actually exercisable here
  (kill a service, confirm documented fail-open behavior, restore via
  the backup capability), explicit boundary that a true multi-region DR
  failover needs cloud infrastructure this environment doesn't have.
- Real chaos/load testing scripts in `oms-gateway/scripts/
  chaosLoadTesting/`: 150 concurrent workers producing real p50/p95/p99
  latencies, plus a real kyc-onboarding kill mid-load confirming the
  documented fail-open behavior holds under genuine concurrent pressure,
  not just a single request.
- `internal/ratelimiter`: real token-bucket rate limiting, RETAIL/
  INSTITUTIONAL/SANDBOX tiers. Live: a 20-burst retail key hit real 429s
  exactly on schedule; an institutional key handled 60/60 successfully.
- `internal/apikeymanager`: real key issuance/revocation, real 401 on
  invalid/revoked keys — the same mechanism backs the public developer
  API (§18) and the rate-limit tiers (§13) together.
- `internal/webhookdelivery`: real URL registration + real retry-on-
  failure delivery, tested against real `httptest` receivers AND a real
  end-to-end proof (a real Python receiver, a real oms-gateway order
  triggering real delivery).
- `internal/tenantconfig`: real multi-tenant branding + isolated
  per-tenant rate limiters — the platform primitive a white-label
  offering needs, explicitly not a complete commercial BaaS product
  (separate legal/compliance entities per tenant is out of scope).
- FIX conformance suite added to `oms-gateway`'s existing
  `internal/dmagateway`: 15 real subtests producing a real pass/fail
  report against this repo's own illustrative FIX-inspired protocol —
  explicitly NOT a real FIX 4.2/4.4 certification.
- `internal/tca`: real implementation-shortfall and arrival-price-
  slippage math; honest gap noted (oms-gateway has no price-history
  endpoint yet) with fixture-sourced data clearly flagged
  `dataSourceIsLive: false` in the response.
- `internal/accountaggregator`: real merge math combining a mocked
  "external institution" holdings fixture with this platform's real
  holdings into one unified view — `isExternalDataFromRealAaNetwork`
  hardcoded false, no real Account Aggregator network reachable here.
- Blue/green matching-engine primitive: a real WAL-copy + replay parity
  proof (byte-identical reconstructed state), with an explicit,
  documented boundary that real traffic-cutover infrastructure (load
  balancer reconfiguration) doesn't exist here.
- Full sweep: `services/api-gateway`, `services/ledger`, and
  `services/oms-gateway` all `gofmt`/`go vet`/`go build`/`go test -race`
  clean; `services/matching-engine` `cargo build --release`/`cargo test
  --release` clean (52/52).

## Entry 64 — quant-engine: ESG/sustainability scoring and screening (FEATURES.md §17)

- New `esgScoringEngine.py`: a real documented weighted-average
  composite formula (Environmental 0.40 / Social 0.30 / Governance
  0.30, summing to exactly 1.0) over an illustrative 6-symbol dataset,
  plus real screening/ranking/sector-exclusion logic. 26 tests + 4 new
  live-HTTP tests (30 new, 216 total for the service, up from 186).
  Hand-worked: E=70/S=60/G=80 → composite exactly 70.0.
- `POST /esg/screen` wired into the existing HTTP server. Verified live:
  the hand-worked composite reproduced exactly over HTTP; combined
  criteria (minimum composite + sector exclusion + an unknown symbol)
  correctly ranked, excluded, and flagged-unknown in one response; 400
  on a missing required field.
- **Known gap, documented loudly**: the ESG dataset itself is entirely
  fabricated/illustrative — NOT sourced from any real rating agency
  (MSCI, Sustainalytics, ISS ESG, Refinitiv/Bloomberg ESG). The SCORING
  MATH and SCREENING LOGIC are real and correctly implemented; the
  underlying data is not.
- Full sweep: 216/216 passing, zero regressions.

## Entry 65 — housekeeping: verified three concurrent oms-gateway lanes coexist correctly

Entries 59, 60, and 63 all independently edited `services/oms-gateway`
(strategy-following routes, the four §12 risk packages, and the chaos-
testing script tree + FIX conformance suite respectively) in the same
window. Before proceeding to the follow-up §15/§17 passes (entries 61,
62), the orchestrating session independently re-verified — not just
trusted the agents' own reports — that all three sets of changes coexist
in `cmd/server/main.go` with no lost work: confirmed every new package
import, every new route registration, and a full green `gofmt`/`go vet`/
`go build`/`go test ./... -race` sweep across the whole service, plus
the same for `services/ledger` (touched by entry 63's backup/restore
subagent) and `services/matching-engine` (touched by entry 63's
blue/green subagent). No conflicts found; no corrective action was
needed beyond this verification pass itself.

<!-- Append new entries below this line as work continues. -->
