# quant-engine

Research-tier component — see `ARCHITECTURE.md` §6 and §8 in the repo root.

## Status: Black-Scholes module is real and tested; now a real HTTP service too

What's real:
- `src/quantengine/blackScholesOptionPricer.py` — call/put pricing, all
  four core Greeks (delta, gamma, vega, theta), and a Newton-Raphson
  implied-volatility solver
- **A real HTTP service** (`src/quantengine/httpServer.py`), stdlib-only
  (`http.server` + `json`, no framework dependency — same convention as
  matching-engine's/market-data's hand-rolled Rust bridges), on
  `127.0.0.1:8085`:
  - `GET /health`
  - `POST /options/price` — price + all four Greeks for one contract in a
    single response
  - `POST /options/implied-volatility` — Newton-Raphson solve given an
    observed market price
  - Every response carries a permissive CORS header so `apps/web` can
    call it directly (same "wrong once real auth exists" caveat as
    oms-gateway's and market-data's CORS middleware).
- 15 passing tests total: 7 in `tests/test_blackScholesOptionPricer.py`
  (known reference-value checks, put-call parity, Greek sanity bounds,
  gamma call/put equality, an IV solver round-trip) + 8 in
  `tests/test_httpServer.py` (live HTTP requests against a real
  `ThreadingHTTPServer` on an ephemeral port: health check, a full
  price+Greeks response, a 400 for a missing field, a 422 for a
  business-level rejection like zero time-to-expiry, an IV round-trip
  through both endpoints, a 404 for an unknown route, a 400 for
  malformed JSON, and the CORS header).

What's not built yet (see FEATURES.md §6, §22):
- GARCH(1,1) volatility forecasting
- Sharpe/Sortino/VaR portfolio risk metrics
- The arbitrage scanner itself (this module is the pricer it would call)
- Portfolio-level Greeks aggregation (the HTTP API above is single-
  contract only; nothing sums positions yet)

## Important constraint

This is explicitly a **research-tier** module (ARCHITECTURE.md §8): fine
for backtesting and for computing Greeks at human-perceptible latency, but
NOT for the real-time arbitrage scanner running across thousands of
contracts per second — that hot path needs a Rust port exposed via PyO3
or gRPC once this pricer's correctness is trusted. Don't put this module
directly behind a latency-sensitive request path. The HTTP service above
is stateless (one pure computation per request), which is exactly why
`ThreadingHTTPServer` is safe here with zero locking — that would NOT
generalize to a future version of this service that holds any shared
mutable state.

## Run it

```bash
python3 -m venv .venv
.venv/bin/pip install -e ".[dev]"
.venv/bin/pytest

# start the HTTP service
.venv/bin/quant-engine-server
# or: .venv/bin/python -m quantengine.httpServer

# in another terminal
curl http://127.0.0.1:8085/health
curl -X POST http://127.0.0.1:8085/options/price -d '{
  "underlyingSpotPrice": 100.0,
  "optionStrikePrice": 100.0,
  "annualizedRiskFreeInterestRate": 0.05,
  "annualizedVolatility": 0.2,
  "timeToExpiryInYears": 1.0,
  "isCallOptionNotPut": true
}'
curl -X POST http://127.0.0.1:8085/options/implied-volatility -d '{
  "observedMarketPrice": 10.4506,
  "underlyingSpotPrice": 100.0,
  "optionStrikePrice": 100.0,
  "annualizedRiskFreeInterestRate": 0.05,
  "timeToExpiryInYears": 1.0,
  "isCallOptionNotPut": true
}'
```
