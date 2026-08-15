# web (retail app)

Skeleton — see `FEATURES.md` §11 in the repo root for the full retail app
scope (watchlists, MF/SIP flows, options chain, alerts, etc.).

## Status: dashboard page + three FEATURES.md §11 pages + three FEATURES.md §20 pages

`app/page.tsx` is a working order ticket that POSTs directly to
`oms-gateway`'s `/orders/submit` and renders its
`humanReadableRejectionReason` on failure — this proves the FEATURES.md
§21 plain-language-rejection differentiator end-to-end from a real
browser client, not just from `curl`. It now also embeds the
Notifications section (item 2 below) and links to all five other pages.

Everything else (auth dashboard reconciliation, portfolio, MF investing,
SIPs, watchlists UI) does not exist yet.

### 1. Options chain — `app/optionsChain/page.tsx`

Real UI for oms-gateway's real `GET /options/chain?underlyingSpotPrice=
&expiryDate=&symbol=` (`internal/optionschain`), which itself calls
quant-engine's real Black-Scholes HTTP pricer for every contract — the
Greeks and theoretical prices rendered here are genuinely computed by
quant-engine, not fabricated by this component. Renders a 10-strike
ladder (strike / call price+Greeks+OI+Vol / put price+Greeks+OI+Vol) plus
a Put-Call Ratio summary. The spot-price field can be prefilled from
market-data's real last-trade tick or edited by hand; the expiry date is
a plain date picker.

**Honest inherited gap** (from oms-gateway's own README/package doc,
repeated in this page's file header): Open Interest and Volume per
contract are SYNTHETIC/illustrative, not observed market data; "implied
volatility" shown is really the ASSUMED flat volatility fed into the
pricer, not a real solved IV; there's no real bid/ask spread anywhere in
this repo, so the theoretical price stands in as a single bid/ask
equivalent. Only the strike ladder, the Greeks/theoretical prices, and
the PCR arithmetic are real.

**Verified live** against a real running oms-gateway + quant-engine (see
"Live verification transcript" below): a real chain for DEMO-EQ, spot
1000, expiry 2026-09-30 came back with 10 strikes, real per-strike
Greeks, `totalCallOpenInterest: 277567`, `totalPutOpenInterest: 319199`,
`putCallRatio: 1.1499890116620493`; killing quant-engine and retrying
produced a real `502 Bad Gateway` from oms-gateway, which this page
surfaces as an error message (loading/error states both exercised).

### 2. Push notifications — `app/notificationCenter/notificationCenterSection.tsx`

Real Web Notifications API integration (`Notification.requestPermission()`
+ `new Notification(...)`), embedded as a section on the dashboard page.
Browsers can't receive real server push without a backend push service
(no Firebase/APNs/web-push infra anywhere in this repo) — so this is
real client-side polling against backends that genuinely produce the
underlying events, not fabricated data:

- **Order fills**: polls oms-gateway's real `GET /audit-trail?
  accountId=...` and fires a notification for every new `ORDER_FILLED` /
  `PAPER_ORDER_FILLED` entry. **Honest gap**: `audittrail.Append` for a
  fill is only called inside the request handler of the order that
  CROSSED (the aggressor) — see oms-gateway's
  `cmd/server/main.go`'s `processOrderSubmission`. A resting LIMIT order
  filled later by someone *else's* incoming crossing order isn't
  guaranteed to get an `ORDER_FILLED` entry attributed to the resting
  order's own owner. This reliably notifies for orders that filled
  immediately (the common demo case) but isn't a complete "notify me the
  instant ANY of my resting orders fills" guarantee.
- **Price alerts**: uses market-data's real, pre-existing
  `src/pricealerts.rs` feature end to end — this component can create a
  real alert via `POST /alerts/create`, then polls `GET /alerts?
  accountIdentifier=...` and fires a notification the moment a
  previously-untriggered alert flips `isTriggered: true`, which only
  happens off a real trade tick crossing the threshold.
- **Margin calls**: **no real event-driven trigger exists for this
  today.** oms-gateway's margin/risk state
  (`internal/riskengine`/`internal/marginpledge`) is not event-driven and
  exposes no "available margin dropped below maintenance" endpoint —
  margin only ever surfaces as a side effect of a pledge/unpledge/funding
  call, or implicitly via an order getting rejected with
  `INSUFFICIENT_MARGIN`. Rather than fabricate a margin-call event that
  doesn't exist, this ships a clearly-labeled **best-effort** heuristic:
  it polls the same real audit-trail feed and fires a notification,
  explicitly titled "Best-effort margin alert (NOT a real margin call)",
  for any new `ORDER_REJECTED` entry whose real `detailMessage` mentions
  margin. This is a genuine signal (a real order was genuinely rejected
  for real insufficient margin) but it is **not** a real margin call — a
  real margin call fires on an *existing open position's* mark-to-market
  breaching a maintenance threshold, independent of whether you submit
  any new order at all, and nothing in this repo computes or watches
  that continuously yet.

**Verified live**: created a price alert (fire at DEMO-EQ ≥ 100),
submitted a real crossing trade at 100 through oms-gateway →
matching-engine → market-data, and the alert flipped to
`"isTriggered": true, "triggeredAtEpochSeconds": 1786661100` — the exact
signal this component's poll loop watches for. Submitted a real order
for 999,999,999 shares and confirmed a real
`{"eventType":"ORDER_REJECTED","detailMessage":"INSUFFICIENT_MARGIN"}`
audit-trail entry — the exact signal the best-effort margin heuristic
watches for. Submitted a real crossing pair of orders and confirmed real
`ORDER_FILLED` audit-trail entries (`"filled 4 @ 100 (buyer=acct-001
seller=acct-002)"`) — the exact signal the order-fill watch fires on.
Browser-side `Notification.requestPermission()`/`new Notification(...)`
itself could not be exercised in this environment (no interactive
browser available to this agent) — the polling→event plumbing feeding it
is proven real end-to-end above; wiring the last step
(`showBrowserNotification`) to an actual granted-permission browser is a
one-click manual step for a human running this app.

### 3. Social/copy-trading (follow verified strategies) — `app/strategies/page.tsx`

Opt-in follow/unfollow of admin-verified strategies, backed by a new
real, minimal, in-memory backend piece added to oms-gateway:
`services/oms-gateway/internal/strategyfollowing` (18 tests), wired up as
`GET /strategies`, `POST /strategies/admin/verify`, `POST
/strategies/follow`, `POST /strategies/unfollow`, `GET
/strategies/followers?strategyId=`, `GET /strategies/following?
accountId=`.

**============ SCOPE BOUNDARY ============**
This is opt-in FOLLOW/UNFOLLOW **ONLY**. There is **no order mirroring**,
**no automatic replication** of a followed strategy's trades into your
account, and **no auto-following** of anything — following a strategy
here has zero effect on your own orders. A real copy-trading engine
(mirroring trades, position sizing, risk controls for the copier) is
explicitly out of scope, both here and in the backend package's own doc
comment.
**==========================================**

`Follow` only succeeds against a `strategyIdentifier` that's on the
public, admin-curated verified list (`POST /strategies/admin/verify` —
reusing the spirit of backoffice's admin-approval pattern, a human
decision gates something becoming publicly followable) — an unverified
`strategyIdentifier` is rejected with a real `400`. The page includes a
demo-only "Admin: verify a strategy" panel so this is exercisable
end-to-end without a separate admin tool (unauthenticated, like most
endpoints in this repo today — documented in the page's file header).

**Verified live**: verified `algo-1`/`algo-2` via the admin endpoint,
followed `algo-1` from two different accounts, confirmed `GET
/strategies` showed `"followerCount":2` for `algo-1` and `0` for
`algo-2`, confirmed `GET /strategies/followers?strategyId=algo-1`
returned `["acct-001","acct-002"]` and `GET /strategies/following?
accountId=acct-001` returned `["algo-1"]`, confirmed following an
unverified `strategyIdentifier` was rejected with a real `400`, and
confirmed `unfollow` then correctly emptied `acct-001`'s following list.

### 4. Volume Profile / Market Profile (TPO) — `app/volumeProfile/page.tsx`

FEATURES.md §20 `[P3]`. Calls market-data's real `GET /volumeProfile`
(`services/market-data/src/volumeProfileAggregator.rs`) — real horizontal
volume-by-price bars, with the real Point of Control (amber) and real
Value Area (blue) highlighted, plus a real TPO letter table below it.
Symbol, price-bucket size, Value Area fraction, and an optional time
window are all editable form fields; nothing here is a static mock — bar
widths, the POC price, and the Value Area bounds all come straight from
market-data's JSON response.

**Verified live**: ran the real `matching-engine` + `market-data`
processes, submitted real crossing orders producing real trades at
10000/10100/10200 (minor units), then fetched `GET /volumeProfile?
instrumentSymbol=DEMO-EQ&priceBucketSizeInMinorUnits=100` from this page
and confirmed the rendered bars, POC (`10100`), and Value Area
(`10000`–`10100`) matched the raw JSON response exactly — no client-side
recomputation of what market-data already computed.

### 5. Order-flow footprint — `app/orderFlowFootprint/page.tsx`

FEATURES.md §20 `[P3]`. Calls market-data's real `GET
/orderFlowFootprint` (`services/market-data/src/
orderFlowFootprintAggregator.rs`) — one real per-candle grid per response,
each row a price level with real buy-volume/sell-volume bars, using the
real aggressor-side flag threaded all the way from matching-engine's
matching logic (not a synthetic buy/sell split).

**Verified live**: same real order flow as above produced a real
candle-level footprint (`buyVolume: 20` at `10100`, `sellVolume: 10` at
`10000`, matching which side genuinely aggressed each of the two real
crossing trades) — confirmed the page's rendered grid matched the raw
JSON response.

### 6. Historical DOM replay — `app/domReplay/page.tsx`

FEATURES.md §20 `[P4]`. Calls matching-engine's real `GET /domReplay`
(`services/matching-engine/src/domReplayHttpServer.rs`), which genuinely
replays the real write-ahead log (see that service's README's
"Historical DOM replay" section for the exact mechanism this endpoint
reuses, not reinvents). A real, if simple, playback control — Prev/Next/
Play-Pause/slider — steps a held index through the REAL array of
depth snapshots matching-engine returned; nothing is interpolated or
fabricated between real snapshots, and playback stops itself at the last
real one rather than looping.

**Verified live**: fetched a real replay window from the live
matching-engine process after submitting several real orders across
multiple price levels and two real crossing trades; confirmed the page's
bid/ask tables at each stepped index exactly matched the corresponding
snapshot in the raw JSON array (an ask level appearing then shrinking as
a buy order crossed it; a bid level appearing then shrinking as a sell
order crossed it), and that a `startEpochMillis` bound correctly excluded
earlier snapshots while the book state shown still reflected the full
pre-window history (not a truncated replay).

## Tests

`apps/web` still has no test runner configured (`package.json` defines
only `dev`/`build`/`start`/`lint` — no Jest/Vitest/Playwright anywhere in
this package or its `node_modules`) — still true as of this round too. No
frontend automated tests were added here; rigor for all six frontend
features above instead comes from `npx tsc --noEmit`/`npm run lint`/`npm
run build` all passing clean plus the live-verification transcripts
documented per-feature. The real work this round is in the two Rust
services (`market-data`, `matching-engine`), which DO have real `cargo
test` suites — see their own READMEs.

## Run it

```bash
npm install
npm run dev       # http://localhost:3000

# in other terminals, so every page has something real to talk to:
cd ../../services/ledger && go run ./cmd/server            # :8082
cd ../../services/kyc-onboarding && go run ./cmd/server    # :8083
cd ../../services/backoffice && go run ./cmd/server        # :8084
cd ../../services/matching-engine && cargo run              # :9101 (+ market-data ingestion), :9106 for GET /domReplay
cd ../../services/market-data && cargo run                  # :9102/:9103/:9104 — needed for the price chart, price alerts, options chain's spot-price prefill, volume profile, and order-flow footprint
cd ../../services/quant-engine && python3 -m venv .venv && .venv/bin/pip install -e . && .venv/bin/quant-engine-server   # :8085 — needed for the options chain
cd ../../services/oms-gateway && go run ./cmd/server        # :8081 — needed for almost everything, including the new /options/chain and /strategies/* endpoints
cd ../../services/api-gateway && go run ./cmd/server         # :8089 — needed for the Developer API keys page only
```

Override base URLs via `NEXT_PUBLIC_OMS_GATEWAY_BASE_URL` (default
`http://localhost:8081`), `NEXT_PUBLIC_MARKET_DATA_BASE_URL` (default
`http://localhost:9103`), `NEXT_PUBLIC_MATCHING_ENGINE_DOM_REPLAY_BASE_URL`
(default `http://localhost:9106`, used only by the Historical DOM replay
page), `NEXT_PUBLIC_QUANT_ENGINE_BASE_URL` (default `http://localhost:8085`,
used by the Portfolio Greeks and IV Rank pages among others), and
`NEXT_PUBLIC_API_GATEWAY_BASE_URL` (default `http://localhost:8089`, used
only by the Developer API keys page) if any service isn't at its default
port.

**Known gap in the Developer API keys page:** api-gateway's
`cmd/server/main.go` has no CORS middleware at all (unlike oms-gateway's
`withPermissiveCorsForDevelopment` or quant-engine's permissive CORS
header) — a real browser running this page against a real api-gateway on
a different origin will have every fetch blocked by the browser's CORS
policy. Verified via curl (server-to-server calls are unaffected) but NOT
verified from an actual browser client — see `docs/BUILD_LOG.md` for the
exact verification performed.
