# SETUP — Running Mercurius locally, via Docker, and (conceptually) deployed

This is the one consolidated run guide for the repo. It replaces having to
piece commands together from each service's own `README.md` and a
partially-stale root `Makefile`. Every port, env var, and default below was
verified directly against the source in this repo (file:line citations
throughout) and, where marked **Verified live**, by actually running the
service and hitting it — not assumed.

Read `docs/DOCUMENTATION.md` for what each service actually does,
`docs/ARCHITECTURE.md` for the system design, and `DR_RUNBOOK.md` (repo
root) for backup/restore and RTO/RPO. This doc is about *running* the
system, not what it does.

---

## 1. Prerequisites

| Toolchain | Version required | Source of truth |
|---|---|---|
| Go | `1.26.3` (every service's `go.mod` pins `go 1.26.3`) | `services/*/go.mod` |
| Rust | edition `2024` (matching-engine, market-data) | `services/matching-engine/Cargo.toml`, `services/market-data/Cargo.toml` |
| Node.js / npm | no `engines` field is pinned in `apps/web/package.json` or `apps/terminal/package.json` — this guide was verified against Node `v24.12.0`/npm `11.6.2` | `apps/web/package.json`, `apps/terminal/package.json` |
| Python | `>=3.10` (verified against Python 3.14.2) | `services/quant-engine/pyproject.toml` |
| Docker Desktop | any recent version — verified against Docker `29.1.3` | for the infra compose stack only (Postgres/Redpanda/ClickHouse) |

No per-service `Dockerfile` exists anywhere in this repo (confirmed — `find . -iname "Dockerfile*"` returns nothing). Every application service runs as a native process (`cargo run`, `go run`, a Python console script, `npm run dev`), never as a container. Only three pieces of **infrastructure** (Postgres, Redpanda, ClickHouse) run in Docker, via `infra/docker/docker-compose.yml`.

---

## 2. Quickest path: Docker Compose infra + native service processes

This is genuinely how this repo is meant to run day-to-day — confirmed against `docs/DOCUMENTATION.md`'s own framing and `infra/docker/docker-compose.yml`'s header comment ("Local dev infra only... this is for Tier 1/2 development only").

### 2.1 Bring up infra

```bash
cd /path/to/Mercurius
make infra-up
```

This runs `docker compose -f infra/docker/docker-compose.yml up -d`, starting:

| Container | Image | Host port(s) | Purpose |
|---|---|---|---|
| `postgres` | `postgres:16-alpine` | **`5433`→container `5432`** | ledger / oms-gateway / market-data persistence |
| `redpanda` | `redpandadata/redpanda:latest` | `9092`, `9644` (admin) | event backbone — **provisioned but not connected to any service's code yet** |
| `clickhouse` | `clickhouse/clickhouse-server:latest` | `8123` (HTTP), `9000` (native) | tick storage — **provisioned but not connected to any service's code yet** |

**The Postgres host port is 5433, not 5432, and this is a permanent line in the committed compose file, not a one-off workaround note.** `infra/docker/docker-compose.yml`'s `postgres.ports` mapping is `"5433:5432"` (container-internal port is untouched). The file's own comment explains why: on at least one dev machine this project was built on, a pre-existing system-level PostgreSQL 18 `launchd` daemon already owns port 5432 and can't be stopped without an interactive sudo password, so `make infra-up` would otherwise fail to bind. **This is not hypothetical** — verified again during this doc's own write-up: with the compose Postgres torn down, attempting a raw connection to `localhost:5432` on this machine hits a real, different Postgres instance that responds with a TLS-then-SASL handshake and then rejects the `trading`/`trading` credentials (`FATAL: password authentication failed for user "trading" (SQLSTATE 28P01)`) — i.e. port 5432 is live but is NOT this project's database.

Every service's own Postgres DSN env var still *defaults* to the conventional port `5432` in code (see §4's table) — that default is correct for a machine with no such conflict. On a machine that does have this conflict (or simply to match this repo's own compose file as shipped), override the DSN's port to `5433` when starting each Postgres-backed service, as shown throughout this guide.

Confirm Postgres is actually reachable before moving on:

```bash
docker exec docker-postgres-1 pg_isready -U trading
# expect: /var/run/postgresql:5432 - accepting connections
```

(The container name is `docker-postgres-1` — Compose derives it from the compose project's directory name, `docker`, plus the service name and index.)

### 2.2 Bring up services, in real dependency order

None of these are hard blocking dependencies except Postgres for the three persistence-backed services — every downstream HTTP/TCP client in this codebase **fails open** (logs a warning, keeps running with reduced functionality) rather than crashing if a dependency isn't up yet. The order below is the order that lets you *verify* things as you go, not a strict requirement.

Open one terminal per service (or see §4's suggested layout):

```bash
# 1. Infra must be reachable first only for these three (else they silently
#    fall back to in-memory — see §3):
POSTGRES_DSN="postgres://trading:trading@localhost:5433/ledger" \
  make dev-ledger

MARKET_DATA_POSTGRES_DSN="postgres://trading:trading@localhost:5433/marketdata" \
  make dev-market-data

# 2. matching-engine has no Postgres dependency, but publishes book-depth/
#    trade-tick updates fire-and-forget to market-data's TCP port — start it
#    any time, order doesn't matter for correctness, only for whether early
#    publishes are missed (harmless, matching-engine never blocks on this):
make dev-matching-engine

# 3. oms-gateway depends on ledger (settlement), matching-engine (order
#    hand-off), and optionally kyc-onboarding/backoffice/quant-engine (all
#    fail OPEN if unreachable — see §3):
OMS_POSTGRES_DSN="postgres://trading:trading@localhost:5433/omsgateway" \
  make dev-oms-gateway

# 4. auth has no dependency on anything else in the platform (see the
#    caveat in §3.2 about who actually re-verifies its tokens):
make dev-auth

# 5. Everything else, any order:
make dev-kyc-onboarding
make dev-backoffice
make dev-api-gateway
make dev-reporting
make dev-mutual-funds
make dev-quant-engine
```

**What this build actually persists to Postgres vs. what stays in-memory** (verified against `docs/DOCUMENTATION.md`'s cross-cutting summary and confirmed live in §5's smoke test with real kill/restart):

| Persisted to Postgres (survives a restart) | Stays in-memory only (lost on restart), even with Postgres up |
|---|---|
| `ledger`'s entire double-entry ledger (`internal/doubleentry` via `internal/pgstore`) | matching-engine (has its own **separate, already-durable** mechanism — a write-ahead log + deterministic replay, not Postgres) |
| `oms-gateway`'s audit trail (`internal/audittrail`) | `oms-gateway`'s `paperPositionBook` and `milliSharePaperPositionBook` (paper-trading/fractional-share positions — deliberately kept separate from real money) |
| `oms-gateway`'s **real** position book (`internal/positions`) — the paper/fractional books below are NOT this | `oms-gateway`'s ~40 other packages: pre-trade risk engine's balance cache, margin/pledge/funding books, execution algos, strategy following, corporate-actions processing, exposure limits, connectivity kill-switch, and more |
| `oms-gateway`'s idempotency store's *completed* responses (`internal/idempotency`, 24h TTL) — the in-flight claim/await mechanism itself stays in-process only, by design | `market-data`'s `candleAggregator.rs` (OHLCV candles, trade tape) and `columnarTickStore.rs` (tick history) — a deliberate hot-path performance tradeoff |
| `market-data`'s watchlists and price alerts (`watchlist.rs`/`pricealerts.rs` via `pgBacking.rs`) | `auth`, `backoffice`, `kyc-onboarding`, `api-gateway`, `reporting`, `mutual-funds`, `quant-engine` — entirely in-memory, no Postgres wiring exists in any of them |
| — | Redpanda and ClickHouse — both provisioned by the compose file but **not connected to any service's code** as of this writing |

---

## 3. Fully in-memory / no-Docker path

**Yes, the whole core order-flow stack (matching-engine, market-data, oms-gateway, ledger, auth) can run with zero Docker at all** — every Postgres-backed store in this codebase fails open to its in-memory equivalent if Postgres is unreachable at startup, not fatal. Verified directly (not assumed) by starting `ledger` with no compose stack running at all:

```
WARNING: could not connect ledger to Postgres (pgstore: ping: failed to connect...) 
  — falling back to IN-MEMORY ledger, balances will NOT survive a restart.
ledger listening on :8082 — postgresBacked=false (withdrawal settlement hold: T+2 days)
```

The process starts and serves traffic normally; it just doesn't persist. The same fail-open pattern was confirmed by direct code inspection for the other two Postgres-backed services:

- `services/oms-gateway/cmd/server/main.go` — `ensureTargetDatabaseExists` failing only logs a `WARNING` and sets `omsPostgresAvailable = false`; `positions`/`idempotency`/`audittrail` each independently fall back if their own connect attempt fails, all via `log.Printf("WARNING: ...")`, none via `log.Fatalf`.
- `services/market-data/src/main.rs` / `pgBacking.rs` — same pattern, confirmed live in §5's own test run history (before this doc's final pass, `market-data` was accidentally started once without `MARKET_DATA_POSTGRES_DSN` pointed at the right port and it logged `WARNING: could not connect watchlists/priceAlerts to Postgres ... falling back to IN-MEMORY stores` and kept serving).

**None of the three fails closed.** If you skip `make infra-up` entirely, every `make dev-*` command in §2.2 still works — you just lose ledger balances, oms-gateway's audit trail/real-position-book/idempotency cache, and market-data's watchlists/alerts across a restart. Everything else in the platform (auth, kyc-onboarding, backoffice, api-gateway, reporting, mutual-funds, quant-engine, matching-engine's own WAL-backed durability) is unaffected either way — those either have no Postgres dependency at all, or (matching-engine) have a completely separate durability mechanism that doesn't touch Postgres.

---

## 4. Bringing up the full stack — reference table

All Go/Rust ports below are **hardcoded constants unless a "Port override env var" is listed** — several services have no override at all; don't assume one exists.

| Service | Command | Port | Port override env var | Key dependency env vars (default) | Purpose |
|---|---|---|---|---|---|
| matching-engine | `make dev-matching-engine` | TCP `127.0.0.1:9101` (orders); HTTP `127.0.0.1:9106` (DOM replay) | `MATCHING_ENGINE_TCP_LISTEN_ADDRESS`; `MATCHING_ENGINE_DOM_REPLAY_HTTP_LISTEN_ADDRESS` | dials market-data at a **hardcoded, non-overridable** `127.0.0.1:9102`; `MATCHING_ENGINE_WAL_FILE_PATH` (default `matchingEngineWriteAheadLog.jsonl`, relative to the dir it's run from) | Tier 0 order book / matching core |
| market-data | `make dev-market-data` | TCP `127.0.0.1:9102` (ingestion); HTTP `127.0.0.1:9103` (query API); WS `127.0.0.1:9104` (L1 quotes) | **none for any of the three** — all hardcoded consts | `MARKET_DATA_POSTGRES_DSN` (`postgres://trading:trading@localhost:5432/marketdata`); `MARKET_DATA_SIMULATED_FEED_ENABLED` (`false`); `MARKET_DATA_OMS_GATEWAY_HTTP_ADDRESS` (`127.0.0.1:8081`) | Tier 1 depth/trade-tick/candle/watchlist/alerts service |
| oms-gateway | `make dev-oms-gateway` | HTTP `:8081`; DMA/FIX-style TCP `:8088` | none for `:8081`; `DMA_GATEWAY_LISTEN_ADDRESS` for `:8088` | `OMS_POSTGRES_DSN` (`postgres://trading:trading@localhost:5432/omsgateway`); `MATCHING_ENGINE_TCP_ADDRESS` (`127.0.0.1:9101`); `LEDGER_BASE_URL` (`http://127.0.0.1:8082`); `KYC_ONBOARDING_BASE_URL` (`http://127.0.0.1:8083`); `BACKOFFICE_BASE_URL` (`http://127.0.0.1:8084`); `QUANT_ENGINE_BASE_URL` (`http://127.0.0.1:8085`); `CORS_ALLOWED_ORIGINS` (`http://localhost:3000,http://localhost:3100`); `AUTH_JWT_SIGNING_SECRET` (shared dev default, see §3/§6) | Order entry, risk, positions, ~65 gated routes |
| ledger | `make dev-ledger` | HTTP `:8082` | none | `POSTGRES_DSN` (`postgres://trading:trading@localhost:5432/ledger`); `WITHDRAWAL_SETTLEMENT_HOLD_DAYS` | Double-entry ledger / system of record |
| auth | `make dev-auth` | HTTP `:8086` | `AUTH_LISTEN_ADDRESS` | `AUTH_JWT_SIGNING_SECRET` (dev default `dev-only-insecure-default-signing-secret-do-not-use-in-production`); `AUTH_DEMO_ACCOUNT_PASSWORD` (dev default `dev-only-insecure-demo-account-password`) | Login/register, JWT issuance, MFA |
| backoffice | `make dev-backoffice` | HTTP `:8084` | none | `OMS_GATEWAY_BASE_URL` (`http://127.0.0.1:8081`); `LEDGER_BASE_URL` (`http://127.0.0.1:8082`); `CORS_ALLOWED_ORIGINS` | Account freeze/unfreeze, support tickets, nominee flows |
| kyc-onboarding | `make dev-kyc-onboarding` | HTTP `:8083` | none | `CORS_ALLOWED_ORIGINS` | KYC verification state machine |
| api-gateway | `make dev-api-gateway` | HTTP `:8089` | `API_GATEWAY_LISTEN_ADDRESS` | `LEDGER_BASE_URL`, `OMS_GATEWAY_BASE_URL`, `MUTUAL_FUNDS_BASE_URL` (`http://127.0.0.1:8087`), `MARKET_DATA_BASE_URL` (`http://127.0.0.1:9103`), `QUANT_ENGINE_BASE_URL`, `MATCHING_ENGINE_TCP_ADDRESS`, `CORS_ALLOWED_ORIGINS`, `SLO_POLL_INTERVAL_SECONDS` (`10`) | Reverse proxy + developer API keys/webhooks/TCA |
| reporting | `make dev-reporting` | HTTP `:8090` | `REPORTING_LISTEN_ADDRESS` | `OMS_GATEWAY_BASE_URL`, `LEDGER_BASE_URL` | Contract notes, statements, capital gains, AIS reconciliation |
| mutual-funds | `make dev-mutual-funds` | HTTP `:8087` | none | `QUANT_ENGINE_BASE_URL`; `MUTUAL_FUND_ORDER_CONFIRMATION_DELAY_HOURS` | MF/bond/structured-product desk (no Postgres, no auth wiring — see §8) |
| quant-engine | `make dev-quant-engine` | HTTP `127.0.0.1:8085` | **none — no env var reading exists in this service at all** | — | Black-Scholes pricer, Greeks, IV solver, backtesting |
| apps/web | `make dev-web` (or `npm run dev` in `apps/web`) | `3000` | — (no `-p` flag in the `dev` script; standard Next.js default) | see §6 for the full `NEXT_PUBLIC_*` list | Retail web dashboard |
| apps/terminal | `make dev-terminal` | `1420` (Vite, `strictPort: true` — Tauri requires this fixed) | not env-overridable (hardcoded in `vite.config.ts`) | see §7 | Pro desktop terminal (Tauri) |

**Suggested terminal layout**: one pane per row above (tmux/iTerm split, or five-plus plain terminal tabs) — this repo has no aggregated dev-process runner (no `dev-all` target and none is added by this pass; see the Makefile comment for why one wasn't invented). `make infra-logs` aggregates the three *Docker* containers' logs (`docker compose logs -f`) but has no equivalent for the native `go run`/`cargo run` processes — each of those prints to its own terminal's stdout.

---

## 5. First real smoke test

This mirrors the kind of live, curl-verified check `docs/BUILD_LOG.md`'s own entries use to prove a change actually works, not just that a process started. It was run for real while writing this document; the exact output shown below (JWT signatures aside) is real.

**Before running this**, note that `matching-engine`'s default WAL file (`services/matching-engine/matchingEngineWriteAheadLog.jsonl`, tracked in git) is replayed on every startup, so a stock checkout's order book is **not empty** — it already has whatever orders were committed into that file. For a deterministic smoke test, point matching-engine at a scratch WAL path instead:

```bash
cd services/matching-engine
MATCHING_ENGINE_WAL_FILE_PATH=/tmp/mercurius-smoketest.wal.jsonl cargo run
```

With `auth`, `ledger` (Postgres-backed), `oms-gateway` (Postgres-backed), `matching-engine` (fresh WAL), and `market-data` all up:

**1. Log in as `acct-001` (the seed account — see `services/auth/cmd/server/main.go:113`, `demoSeedAccountIdentifiers = []string{"acct-001", "acct-002"}`), get a real JWT:**

```bash
curl -s -X POST http://localhost:8086/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"acct-001@demo.mercurius.local","password":"dev-only-insecure-demo-account-password"}'
```

Real response (fields present, values from an actual run):
```json
{"accountIdentifier":"acct-001","accessToken":"eyJhbGciOiJIUzI1NiIs...","refreshToken":"CWptokU0_...","expiresInSeconds":900}
```

Repeat for `acct-002@demo.mercurius.local` with the same password. (If `AUTH_DEMO_ACCOUNT_PASSWORD` was set when `auth` started, use that value instead of the dev default shown here.)

**2. Submit a real resting sell from acct-002, then a real crossing buy from acct-001:**

```bash
TOKEN2=<acct-002's accessToken>
TOKEN1=<acct-001's accessToken>

curl -s -X POST http://localhost:8081/orders/submit -H "Authorization: Bearer $TOKEN2" \
  -H 'Content-Type: application/json' -d '{
    "clientAccountIdentifier":"acct-002","instrumentSymbol":"DEMO-EQ",
    "orderSideIsBuyNotSell":false,"orderIsMarketOrderNotLimit":false,
    "limitPriceInMinorUnits":5000,"orderQuantity":5,
    "idempotencyKey":"setup-doc-smoketest-sell-1"}'
# real response: {"wasOrderAccepted":true,"assignedGlobalSequenceNumber":3,"matchingEngineOrderSequenceNumber":1}

curl -s -X POST http://localhost:8081/orders/submit -H "Authorization: Bearer $TOKEN1" \
  -H 'Content-Type: application/json' -d '{
    "clientAccountIdentifier":"acct-001","instrumentSymbol":"DEMO-EQ",
    "orderSideIsBuyNotSell":true,"orderIsMarketOrderNotLimit":false,
    "limitPriceInMinorUnits":5000,"orderQuantity":5,
    "idempotencyKey":"setup-doc-smoketest-buy-2"}'
# real response:
# {"wasOrderAccepted":true,"assignedGlobalSequenceNumber":4,
#  "tradeExecutionEvents":[{"buyingClientAccountId":"acct-001","sellingClientAccountId":"acct-002",
#                            "executedPriceInMinorUnits":5000,"executedQuantity":5}],
#  "matchingEngineOrderSequenceNumber":2}
```

Note `clientAccountIdentifier` in the body must match the authenticated account in the bearer token — `oms-gateway` enforces this (`authenticatedAccountMatches`, `services/oms-gateway/cmd/server/main.go:690`); a mismatched token/body pair gets rejected before the order is even parsed further.

**3. Confirm the fill actually landed in positions:**

```bash
curl -s "http://localhost:8081/positions?accountId=acct-001" -H "Authorization: Bearer $TOKEN1"
# {"accountIdentifier":"acct-001","netQuantityByInstrumentSymbol":{"DEMO-EQ":5}}
curl -s "http://localhost:8081/positions?accountId=acct-002" -H "Authorization: Bearer $TOKEN2"
# {"accountIdentifier":"acct-002","netQuantityByInstrumentSymbol":{"DEMO-EQ":-5}}
```

**4. Confirm the trade actually settled in the ledger (real money movement, not just an in-memory position bump):**

```bash
curl -s "http://localhost:8082/accounts/balance?accountId=acct-001"
curl -s "http://localhost:8082/accounts/balance?accountId=acct-002"
```

acct-001's balance drops by exactly `5000 × 5 = 25000` minor units, acct-002's rises by the same — this was confirmed with real before/after balance reads during this doc's verification.

**5. Prove Postgres persistence for real — kill and restart oms-gateway, confirm the position survived:**

```bash
# Ctrl-C the oms-gateway terminal, then:
OMS_POSTGRES_DSN="postgres://trading:trading@localhost:5433/omsgateway" make dev-oms-gateway
# ... re-login (auth's sessions are in-memory and don't survive auth's own restart,
#     but oms-gateway restarting doesn't invalidate an already-issued, unexpired JWT) ...
curl -s "http://localhost:8081/positions?accountId=acct-001" -H "Authorization: Bearer $TOKEN1"
# {"accountIdentifier":"acct-001","netQuantityByInstrumentSymbol":{"DEMO-EQ":5}}   <- same, survived the restart
```

This exact sequence (fresh WAL → resting sell → crossing buy at the expected price → position check → ledger balance check → kill/restart → position check again) was run live while writing this document; all values above are real, not illustrative.

---

## 6. apps/web (Next.js)

```bash
cd apps/web
npm install   # if not already done
npm run dev   # http://localhost:3000
```

Verified: `next dev` boots in well under a second (Turbopack), `curl http://localhost:3000/` returns `200` and the rendered HTML contains the app's branding.

Every backend base URL is `NEXT_PUBLIC_*`-configurable, each with an in-code fallback matching the corresponding backend's own default port (so an unconfigured `apps/web` talks to the exact stack §4 brings up):

| Env var | Default | Backend it points at |
|---|---|---|
| `NEXT_PUBLIC_OMS_GATEWAY_BASE_URL` | `http://localhost:8081` | oms-gateway |
| `NEXT_PUBLIC_MARKET_DATA_BASE_URL` | `http://localhost:9103` | market-data (HTTP query API) |
| `NEXT_PUBLIC_AUTH_BASE_URL` | `http://localhost:8086` | auth |
| `NEXT_PUBLIC_BACKOFFICE_BASE_URL` | `http://localhost:8084` | backoffice |
| `NEXT_PUBLIC_QUANT_ENGINE_BASE_URL` | `http://localhost:8085` | quant-engine |
| `NEXT_PUBLIC_MATCHING_ENGINE_DOM_REPLAY_BASE_URL` | `http://localhost:9106` | matching-engine's DOM-replay HTTP endpoint |
| `NEXT_PUBLIC_API_GATEWAY_BASE_URL` | `http://localhost:8089` | api-gateway |

There is no committed `.env.local`/`.env.example` in `apps/web` — the defaults above live as inline `?? "..."` fallbacks in each page's source (e.g. `apps/web/app/page.tsx:33-35`), so the app works out of the box against §4's default ports with zero env file needed; only override these if you've moved a backend to a non-default port.

**Log in through the UI's Account panel (`apps/web/app/page.tsx`) before any gated page will load real data** — most pages call oms-gateway/backoffice/api-gateway routes that now require a bearer token (see §8's RBAC note); an unauthenticated page load will show 401s from those calls. Alternatively, run the §5 curl login flow and paste the resulting `accessToken` wherever the UI's session storage expects it, if you need to skip the UI login step.

---

## 7. apps/terminal (Tauri)

```bash
cd apps/terminal
npm install
npm run tauri dev
```

This needs the Tauri desktop toolchain (a Rust toolchain plus your OS's native webview — see [Tauri's prerequisites](https://tauri.app/start/prerequisites/) for the current per-OS list; not re-verified as part of this pass since it requires a GUI environment this session doesn't have). Vite serves on a **fixed** port `1420` (`apps/terminal/vite.config.ts:17`, `strictPort: true` — Tauri's `tauri.conf.json` `devUrl` expects exactly this), not configurable via env var.

Backend base URLs it reads (`apps/terminal/src/workspace/widgetRegistry.tsx`):

| Env var | Default |
|---|---|
| `VITE_OMS_GATEWAY_BASE_URL` | `http://127.0.0.1:8081` |
| `VITE_MATCHING_ENGINE_DOM_REPLAY_BASE_URL` | `http://127.0.0.1:9106` |
| `VITE_MARKET_DATA_BASE_URL` | `http://127.0.0.1:9103` |

It genuinely needs a running backend (oms-gateway/matching-engine/market-data) for any of its live widgets to show real data — it is not a standalone/offline app. (`docs/DOCUMENTATION.md`'s own apps/terminal section is the authority on which widgets exist; this guide only covers how to boot it.)

---

## 8. Running tests

```bash
make test
```

runs, per the corrected Makefile (see the "Makefile fixes" note at the bottom of this doc): `cargo test` for matching-engine and market-data; `go test ./...` for oms-gateway, ledger, auth, backoffice, kyc-onboarding, api-gateway, reporting, mutual-funds; `pytest` for quant-engine; `npm run test` (`vitest run`) for apps/terminal.

**Known gap, honestly stated**: `apps/web/package.json` has no `"test"` script (only `dev`/`build`/`start`/`lint`) — there is no automated test suite for the web app in this repo as of this writing, and `make test` deliberately does not try to invoke one (it would just fail with "missing script"). `apps/web`'s own verification story is `npx tsc --noEmit` + `npm run build`, not a test runner.

Also note (per `docs/DOCUMENTATION.md`'s RBAC section): none of the newly-added `go test` targets above depend on a running Postgres or any other live service — they use `httptest.Server` fakes and in-process fixtures, same pattern as the pre-existing oms-gateway/ledger tests. The *Postgres-backed* unit tests inside `internal/idempotency`, `internal/positions`, `internal/audittrail` (oms-gateway) and `pgBacking.rs` (market-data) do need a real reachable Postgres and will fail/skip without one — point `POSTGRES_DSN`/`OMS_POSTGRES_DSN`/`MARKET_DATA_POSTGRES_DSN` at `localhost:5433` (after `make infra-up`) before running `go test ./...`/`cargo test` if you want those specific tests to run rather than fail on connection.

---

## 9. "Deployed environment" — conceptual sketch, not a tested procedure

**This repo has no real cloud/production deployment today.** Verified: no `infra/k8s/`, no `infra/terraform/` directory exists anywhere in the repo, and no per-service `Dockerfile` exists either — `docs/ARCHITECTURE.md` §7's multi-AZ Kubernetes / bare-metal / multi-region / PTP content is architecture *prose*, not backed by a single deployable artifact in this codebase. Everything below is a sketch of what it would take, explicitly distinguished from the tested sections above — do not follow it expecting it to work as-is.

**(a) Containerizing each service.** None of the 11 backend services has a `Dockerfile`. Each would need one — straightforward for the Go services (multi-stage build, `go build`, scratch/distroless final image) and matching-engine/market-data (multi-stage `cargo build --release`), a little more involved for quant-engine (needs a Python base image plus `pip install -e .`) and apps/web (Next.js's own multi-stage Docker pattern, standalone output mode). None of this exists yet.

**(b) How `DR_RUNBOOK.md` relates.** That document already gives real, tiered RTO/RPO targets (Tier 1 execution path: RTO <60s/RPO 0 via matching-engine's WAL; Tier 2 ledger: RTO <5min; Tier 2 identity/compliance: RTO <15min; Tier 3 analytics: RTO <30min/best-effort) and a *real*, actually-run failover drill — but that drill is explicitly scoped to a single-machine environment (kill a process, prove fail-open behavior; ledger snapshot/restore via `GET /admin/snapshot`/`POST /admin/restore`). It explicitly states it does **not** prove a real cross-region failover: no second region, no cloud account, no DNS/traffic-manager, no cross-region replication were ever exercised. A real deployment would need to build toward those RTO/RPO numbers with actual infrastructure, not just process-level fail-open behavior.

**(c) The real env-var/secrets surface that would need replacing.** Every "insecure dev default" fallback in this codebase is a real, working security hole if it ever reached a genuine deployment:
- `AUTH_JWT_SIGNING_SECRET` — falls back to the literal string `dev-only-insecure-default-signing-secret-do-not-use-in-production`, duplicated byte-for-byte across `auth`, `oms-gateway`, `backoffice`, `kyc-onboarding`, `api-gateway`'s own copies of `internal/authmiddleware`. Anyone who reads this repo's source (which is public once cloned) can forge a valid JWT for any account/role against an unconfigured deployment.
- `AUTH_DEMO_ACCOUNT_PASSWORD` — falls back to `dev-only-insecure-demo-account-password` for the seeded `acct-001`/`acct-002` accounts.
- `POSTGRES_DSN` / `OMS_POSTGRES_DSN` / `MARKET_DATA_POSTGRES_DSN` — all default to the literal credential pair `trading`/`trading`.
- `CORS_ALLOWED_ORIGINS` defaults to localhost origins (safe by itself), but `auth` still runs fully permissive wildcard CORS (`Access-Control-Allow-Origin: *`) with no allow-list option at all — the one service that issues credentials is the one still wide open.
- No TLS anywhere in this codebase — every listed port above is plain HTTP/TCP.
- A real deployment needs every one of these sourced from an actual secrets manager (Vault, cloud KMS-backed secret store, etc.), TLS termination in front of every service, and `auth` migrated onto the same CORS allow-list pattern as everything else.

**(d) A realistic minimal target**, sized to what this repo actually is (a skeleton, not a scaled trading venue) rather than over-engineering: one small VM or container per service (11 backend services + apps/web, ~12 compute units) behind a single reverse proxy/load balancer for TLS termination and routing, one managed Postgres instance (e.g. RDS/Cloud SQL) replacing the local compose Postgres, one managed Kafka-compatible service (e.g. MSK/Confluent Cloud) replacing Redpanda whenever something actually starts consuming it, and a managed ClickHouse (e.g. ClickHouse Cloud) whenever tick storage gets wired up. No Kubernetes needed at this scale — `ARCHITECTURE.md`'s multi-AZ K8s vision is a later-scale concern, not a prerequisite for a first real deployment of what exists today.

---

## 10. Troubleshooting

- **`docker compose up` fails to bind Postgres to port 5432.** Expected and already handled — the compose file maps host `5433:5432` specifically because of this (see §2.1). If you still see a bind conflict on `5433` itself, something else on your machine has taken that port; check with `lsof -iTCP:5433 -sTCP:LISTEN`.
- **A service logs `WARNING: could not connect ... to Postgres` and keeps running.** This is the documented fail-open behavior (§3), not a crash — the service is now running in-memory-only mode. Check that you passed the right `*_POSTGRES_DSN`/`POSTGRES_DSN` env var with port `5433` (not the code's own default of `5432`), and that `make infra-up` actually succeeded.
- **Connecting to `localhost:5432` gives a real, confusing Postgres auth error instead of "connection refused."** Confirmed real on at least one dev machine (see §2.1): a pre-existing system-level Postgres answers on 5432 and rejects the `trading`/`trading` credentials with a SASL auth failure. This is not this project's database — use port `5433` for the compose Postgres.
- **matching-engine's order book isn't empty on a fresh checkout.** `services/matching-engine/matchingEngineWriteAheadLog.jsonl` is checked into git and replayed on every startup (this is the WAL/crash-recovery feature working as designed, not a bug) — the default `MATCHING_ENGINE_WAL_FILE_PATH` resolves relative to the directory `cargo run` is invoked from, so running from `services/matching-engine` reads/appends that committed file. For a clean/deterministic smoke test, point `MATCHING_ENGINE_WAL_FILE_PATH` at a scratch path instead (§5). Also worth knowing: running the matching-engine at all, even briefly, will grow this tracked file — a real dev workflow likely wants a `.gitignore` entry for it or a documented reset step, neither of which exists yet.
- **oms-gateway `:8081` refuses to start with `bind: address already in use`.** A previous `go run ./cmd/server` (or its compiled child process) is still bound — `go run` spawns a child binary that a plain `kill` on the `go run` PID doesn't always reach. Find and kill the actual listener: `lsof -tiTCP:8081 -sTCP:LISTEN | xargs kill -9`.
- **A gated oms-gateway/backoffice/kyc-onboarding/api-gateway route returns 401.** You need a real bearer token from `POST /auth/login` (§5, step 1) — none of these routes accept unauthenticated requests as of the auth/RBAC pass (`docs/BUILD_LOG.md` entry 84).
- **A gated route returns 403 even with a valid token.** Two distinct causes, both by design: (1) the token's account doesn't match the `clientAccountIdentifier`/`accountId` in the request — every service enforces "you can only act on your own account"; (2) the route requires a specific role (`RoleAdmin`/`RoleSupport`/`RoleCompliance`) and your token's role doesn't match exactly — role checks are exact-match, not hierarchical, so even an admin token fails a compliance-only check. The seeded `acct-001`/`acct-002` demo accounts are both `RoleRetail` — **there is no seeded admin/support/compliance account in this build**, so any admin-gated route (e.g. oms-gateway's `/market-session/open`, `/audit-trail`) cannot currently be exercised end-to-end through the real login flow; you'd need to mint a token for one of those roles directly via `jwtauth.IssueAccessToken` in a short Go snippet, or extend `seedDemoAccounts`.
- **apps/web shows 401s / broken data despite the backend being up.** You're not logged in through the UI — see §6's note. A page loading before login is expected to fail its authenticated fetches.
- **`market-data`'s three ports (9102/9103/9104) can't be moved.** They're hardcoded constants with no env-var override (§4) — if you need different ports, that requires a source change, not a run-time flag.
- **`quant-engine` doesn't respond to any port/host override.** Confirmed: zero `os.environ`/`os.getenv` calls anywhere in the service — it always binds `127.0.0.1:8085`.

---

## Makefile fixes made alongside this doc

The root `Makefile` was missing targets for several services that have existed in the codebase for a while — this was fixed so every command referenced above is real and `make -n <target>`-verified:

- Added `dev-auth`, `dev-backoffice`, `dev-kyc-onboarding`, `dev-api-gateway`, `dev-reporting`, `dev-mutual-funds`, `dev-quant-engine` (mirroring the pre-existing `dev-oms-gateway`/`dev-ledger` pattern).
- `build-go` now also builds `auth`, `api-gateway`, `reporting`, `mutual-funds` (kyc-onboarding and backoffice were already there).
- `test` now also runs `go test ./...` for `auth`, `backoffice`, `kyc-onboarding`, `api-gateway`, `reporting`, `mutual-funds`, plus `apps/terminal`'s `npm run test` (`vitest run`). `apps/web` was deliberately NOT added — it has no `test` script (see §8).
- `fmt` now also runs `gofmt -w .` for the same newly-added Go services.
- No `dev-all`/tmux-aggregation target was added — no such convenience existed in this Makefile before, and none of this repo's existing scripts assume one; §4 documents a manual multi-pane layout instead rather than inventing a new pattern un-asked-for.
