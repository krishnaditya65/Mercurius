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

<!-- Append new entries below this line as work continues. -->
