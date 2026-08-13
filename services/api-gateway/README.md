# Mercurius / api-gateway

A real reverse-proxy / API-management layer (`:8089`) sitting in front
of the platform's other services: `ledger` (`:8082`), `oms-gateway`
(`:8081`), `mutual-funds` (`:8087`), `market-data` (`:9103` HTTP query
API), `quant-engine` (`:8085`). This is the home for FEATURES.md §13/§18's
platform-and-ecosystem items that don't belong inside any single backend
service.

Module: `mercurius/apiGateway`, Go 1.26.3, long descriptive camelCase
naming throughout, matching every other Mercurius service.

## Running it

```
cd services/api-gateway
LEDGER_BASE_URL=http://127.0.0.1:8082 \
OMS_GATEWAY_BASE_URL=http://127.0.0.1:8081 \
MUTUAL_FUNDS_BASE_URL=http://127.0.0.1:8087 \
MARKET_DATA_BASE_URL=http://127.0.0.1:9103 \
QUANT_ENGINE_BASE_URL=http://127.0.0.1:8085 \
go run ./cmd/server
```

Defaults match every backend service's own default port, so
`go run ./cmd/server` with no env vars at all works if every backend is
running locally on its documented default port.

## Endpoints

| Method | Path | What it does |
|---|---|---|
| GET | `/health` | liveness |
| GET | `/alerts` | every SLO alert raised so far (§13 item 1) |
| POST | `/developer/api-keys` | issue a developer API key (§18 item 8) |
| GET | `/developer/api-keys?accountIdentifier=` | list keys for an account |
| POST | `/developer/api-keys/revoke` | revoke a key |
| POST/GET | `/tenants` | register/list white-label tenants (§18 item 10) |
| POST | `/webhooks/subscribe` | register a webhook (§18 item 9) |
| GET | `/webhooks/deliveries` | every real delivery attempt outcome |
| GET | `/tca/report?accountId=` | Transaction Cost Analysis report (§18 item 12) |
| GET | `/account-aggregator/net-worth?accountId=` | illustrative AA merge view (§18 item 13) |
| * | `/ledger/*`, `/oms/*`, `/mutual-funds/*`, `/market-data/*`, `/quant-engine/*` | reverse-proxied to the real backend, prefix stripped |

Every request except `/health` goes through the rate-limiting
middleware (§13 item 6 / §18 item 8 — same mechanism, see below).

## Item-by-item status

### §13 item 1 — Alerting on SLO breach

`internal/sloalerting`. `SloAlertEvaluator` is a pure, deterministic
state machine (Prometheus `for:`-duration semantics: a metric must
breach its threshold CONTINUOUSLY for `MinimumBreachWindow` before an
alert fires, fires once per continuous breach episode) — fully unit
tested with injected synthetic samples, no live services required
(15 tests). `MetricsPoller` is the live half: polls oms-gateway's
`GET /audit-trail` for order-reject rate, market-data's `GET /trades`
for feed staleness, and TCP-dials matching-engine for a connect-latency
proxy (matching-engine has no HTTP metrics endpoint — a pre-existing gap
documented in oms-gateway's own `internal/metrics` package). Alerts are
logged loudly (`🚨 SLO BREACH ALERT`) and queryable via `GET /alerts`.
Live-verified: ran api-gateway against real (and real-but-absent)
backends and confirmed the poller runs on its real interval and fails
soft when a backend is down (see live-verification transcript in the
final report).

**Gap**: matching-engine's "latency" signal is TCP-connect latency, not
a true order-matching round-trip latency histogram — matching-engine
has no HTTP surface to expose one.

### §13 item 2 — Secrets management, least-privilege IAM

`internal/secretsprovider`: a real `SecretsProvider` interface
(`GetSecret(key string) (string, error)`), a real
`EnvironmentVariableSecretsProvider` (env-var-backed, prefix-scoped) and
a `StaticInMemorySecretsProvider` for tests. `config/secretsAccessMatrix.yaml`
is the real, concrete least-privilege access matrix (which service reads
which secret) — documentation, not enforced code, because there is no
real cloud IAM system in this environment to bind it to; see that file's
own header for why faking an enforcer would be dishonest.

### §13 item 3 — Ledger backup/restore

Lives in `services/ledger`, not here (the natural home for ledger's own
data durability): `GET /admin/snapshot` / `POST /admin/restore` on
ledger, `services/ledger/scripts/backupLedgerSnapshot.sh`,
`services/ledger/internal/doubleentry/snapshotRestore.go` +
`snapshotRestore_test.go`. See the top-level `DR_RUNBOOK.md` for how
this integrates into disaster recovery.

### §13 item 4 — Disaster recovery

`DR_RUNBOOK.md` at the repo root: concrete per-tier RTO/RPO targets and
a real failover drill actually run against real processes. See that
document for the honest boundary on what a real multi-region DR drill
would additionally need.

### §13 item 5 — Chaos/load testing

`services/oms-gateway/scripts/chaosLoadTesting/`: real concurrent load
generator + real chaos test (kills a live dependency mid-run). Run for
real; see the final report for real latency percentiles and chaos
observations.

### §13 item 6 / §18 item 8 — Rate limiting, quota tiers, developer API + sandbox

`internal/ratelimiter` (real token-bucket limiter, `RETAIL`=10req/s/
burst20, `INSTITUTIONAL`=200req/s/burst400, `SANDBOX`=5req/s/burst10) +
`internal/apikeymanager` (real key issuance/validation/revocation).
`cmd/server/main.go`'s `buildRateLimitingMiddleware` enforces this on
every proxied request: a valid `X-Api-Key` header is rate-limited under
its own issued tier; no key at all uses a shared anonymous RETAIL-tier
bucket (not unlimited); an invalid/revoked key is rejected with 401
(fail closed). A sandbox key gets `X-Sandbox: true` injected onto the
proxied request — backend services can inspect and safely ignore it
today (no backend currently branches on it; this is the documented,
minimal sandbox mechanism this task allows: "requests tagged as sandbox
get routed to... real backend services with an X-Sandbox header they
can safely ignore").

Live-verified: issued a retail key (burst 20), fired 25 requests — got
20×200 then 429s with intermittent refill 200s exactly matching
token-bucket math; issued an institutional key, fired 60 requests, all
200 (burst 400 never exhausted); invalid key → 401; revoked key → 401.

### §13 item 7 — Blue/green deploy for matching-engine

Lives in `services/matching-engine/scripts/blueGreenDeployDrill.py` +
`services/matching-engine/BLUE_GREEN_DRILL.md`. Proves WAL-replay state
parity between a "blue" and "green" instance for real, using
matching-engine's existing WAL + deterministic replay mechanism. See
that doc for the explicit real-infrastructure boundary.

### §18 item 9 — Webhooks

`internal/webhookdelivery`: real subscription registry
(`RegisterSubscription`/`RemoveSubscription`), real delivery
(`DeliverEvent` does a real `http.Client.Do` POST, with real
retry-on-failure — `DefaultRetryPolicy` = 3 attempts, 200ms apart —
every attempt recorded in `DeliveryHistory`). Tested against a REAL
local `httptest.Server` receiver, including a receiver that fails N
times then succeeds (proves retry actually recovers) and one that
always fails (proves retries exhaust and every attempt is recorded, not
silently dropped). `AuditTrailEventSource` is the real event source:
polls oms-gateway's real `GET /audit-trail` on a real interval, forwards
only NEW entries since the last poll, maps `ORDER_SUBMITTED`/
`ORDER_FILLED`/`ORDER_REJECTED`/`ORDER_CANCELLED` audit events to
webhook events.

Live-verified end-to-end: registered a webhook against a real local
Python HTTP receiver, submitted a real order through oms-gateway (which
appended a real `ORDER_SUBMITTED` audit-trail entry), waited one real
poll interval, and confirmed via `GET /webhooks/deliveries` that
api-gateway POSTed the event and got a real HTTP 200 back from the real
receiver.

### §18 item 10 — White-label / Broker-as-a-Service primitive

`internal/tenantconfig`: `TenantRegistry` with per-tenant
`BrandingMetadata` (name/logo/color) and a genuinely independent
`ratelimiter.TokenBucketRateLimiter` instance per tenant via
`RateLimiterForTenant` — proven isolated in tests (exhausting one
tenant's bucket for a key never affects another tenant's bucket for the
identical key). Live-verified: registered `acme-fintech` via
`POST /tenants`, listed it back via `GET /tenants`.

**Explicit scope boundary** (per the task and the package's own doc
comment): this is the gateway-layer multi-tenancy PRIMITIVE a
white-label offering needs — not a complete commercial BaaS product.
Separate legal/compliance entities per tenant, data-residency
guarantees, and per-tenant billing are out of scope and do not exist
here.

### §18 item 11 — FIX conformance suite

Lives in `services/oms-gateway/internal/dmagateway/conformanceSuite_test.go`
+ `CONFORMANCE_REPORT.md`. Real pass/fail conformance suite against
oms-gateway's existing illustrative FIX-inspired `dmagateway` session
protocol. Explicitly NOT real FIX 4.2/4.4 certification — see that
report for the full scope statement.

### §18 item 12 — TCA

`internal/tca`: `ComputeMetricsForOrder` computes implementation
shortfall and arrival-price slippage (signed so positive always means
"cost the client money" regardless of buy/sell side) from a
`FilledOrder`'s arrival price vs. average fill price; `BuildAccountReport`
aggregates across an account's orders. **Honest data-source gap**:
oms-gateway's real running HTTP API has no endpoint returning both a
historical arrival price and fill price per order (`GET /audit-trail`
carries no price fields; `GET /orders/status` returns only a CURRENT
price by order id) — `internal/tca/fillDataSource.go` documents exactly
what a real build needs to add to oms-gateway, and
`FetchLiveFilledOrders` deliberately always returns
`ErrLiveFillHistoryNotYetAvailable` rather than silently faking "live"
data. `GET /tca/report` therefore serves realistic FIXTURE order data
today (explicitly labeled `"dataSourceIsLive": false` in the response,
with the caveat message included) — the MATH is real, the input data is
fixtured, exactly as this task's own instructions explicitly allow.

### §18 item 13 — Account Aggregator (illustrative)

`internal/accountaggregator`. **LOUD WARNING, repeated from the package
doc comment**: this does NOT connect to any real Account Aggregator
network (India's AA ecosystem or any equivalent). `MockedExternalInstitutionHoldingsSource`
returns a small, deterministic fixture set standing in for what a real
AA consent flow would return. `BuildUnifiedNetWorthView` does REAL
merge/aggregation math combining that fixture data with real platform
holdings pulled live via HTTP from oms-gateway's `GET /positions` and
mutual-funds' `GET /holdings` (`GET /account-aggregator/net-worth`
handler in `cmd/server/main.go`). The response's
`isExternalDataFromRealAaNetwork` field is hardcoded `false` — always,
on purpose, so no caller can mistake fixture data for a real network
connection.

## Testing

```
cd services/api-gateway
gofmt -l .            # clean
go vet ./...           # clean
go build ./...          # clean
go test ./... -race     # clean, ~90+ tests across 9 internal packages
```

## Known cross-cutting gaps (honest, repo-wide)

- Every piece of state in this service (issued API keys, webhook
  subscriptions, tenant config, SLO alert history) is **in-memory
  only** — an api-gateway restart loses all of it. Same convention as
  every other skeleton service in this repository; a real build
  persists all of this durably.
- The rate limiter and API key store are single-process — a real
  multi-instance deployment of api-gateway needs a shared backing store
  (Redis for the rate limiter, a real database for API keys) so limits
  and keys are consistent across instances, not per-instance.
