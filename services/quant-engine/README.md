# quant-engine

Research-tier component — see `ARCHITECTURE.md` §6 and §8 in the repo root.

## Status: Black-Scholes pricer, risk statistics, arbitrage scanner, a
## deterministic backtest engine, GARCH(1,1) volatility forecasting, a
## correlation matrix engine, VaR/stress-testing, a volatility surface
## builder, a strategy deployment lifecycle state machine, a
## market-making sandbox, and an illustrative sentiment trading hook are
## all real and tested; HTTP service covers pricing/Greeks/IV plus risk,
## arbitrage, GARCH forecast, correlation matrix, VaR, and market-making
## sandbox endpoints

What's real:
- `src/quantengine/blackScholesOptionPricer.py` — call/put pricing, all
  four core Greeks (delta, gamma, vega, theta), and a Newton-Raphson
  implied-volatility solver
- `src/quantengine/riskStatistics.py` — annualized Sharpe ratio,
  annualized Sortino ratio (downside-deviation denominator), and max
  drawdown (largest peak-to-trough decline over a compounded equity
  curve). Real formulas (population-stddev convention, configurable
  periods-per-year annualization factor), not tuned against any real
  market data — feed it whatever periodic-return series you have
  (portfolio-level or single-strategy).
- `src/quantengine/arbitrageScanner.py` — theoretical-vs-live price
  deviation scanner: absolute + percentage deviation, a configurable
  alert threshold, and a signed "is live overpriced" flag. Includes
  `calculateCashAndCarryForwardFairPrice` (F = S·e^(rT)) as one real,
  simple example of a "theoretical price" input — the module also
  happily takes a Black-Scholes price from the pricer above, or any
  other theoretical price a caller supplies. The threshold itself is
  fully caller-configurable, not a tuned/backtested number.
- `src/quantengine/backtesting/` — a real, deterministic event-driven
  backtest SDK:
  - `tickStore.py` — `InMemoryHistoricalTickStore`: sorted-by-timestamp
    `(timestamp, price)` ticks per symbol, with `addTick`/`addTicks` and
    an inclusive `queryRange`. In-memory only, no persistence — this is
    explicitly a research-tier store for feeding the backtest runner, not
    a production tick database.
  - `backtestRunner.py` — `runDeterministicEventDrivenBacktest` replays a
    chronological tick list through a strategy callback
    (`(tick, portfolioState) -> TradeDecision`), applying trades with
    standard weighted-average-cost accounting (tracks cash, signed
    position size, realized P&L, and mark-to-market unrealized P&L per
    tick). No randomness, no wall-clock, no I/O — proven deterministic by
    a test that runs the identical backtest twice and asserts identical
    output.
  - `pairsTradingStrategy.py` — `ZScoreMeanReversionPairsTradingStrategy`,
    a real reference strategy: rolling mean/stddev of a price spread,
    z-score entry/exit thresholds, wired as a `backtestRunner` strategy
    callback. Trades the spread as a single synthetic instrument (see the
    module docstring for why) rather than sizing two separate legs —
    a real two-leg implementation is a documented future extension, not
    faked here.
- `src/quantengine/garchVolatilityForecaster.py` — GARCH(1,1) volatility
  forecasting feeding an "Expected Intraday Range" widget. The
  conditional-variance recursion (`sigma^2_t = omega + alpha*epsilon^2_{t-1}
  + beta*sigma^2_{t-1}`) is the exact textbook Bollerslev formula.
  **Fitting method — read this before trusting the numbers in
  production**: parameters are estimated via "variance targeting" (omega
  solved in closed form from the sample variance and a candidate
  alpha/beta) plus a coarse grid search over alpha/beta maximizing the
  Gaussian conditional log-likelihood. This is a real, genuinely-computed
  quasi-MLE — every candidate's likelihood is actually evaluated via the
  recursion — but it is NOT a production-grade continuous optimizer
  (no `scipy.optimize`, kept stdlib-only per this service's convention);
  treat fitted parameters as a reasonable starting point, not a
  regulatory-grade estimate.
- `src/quantengine/correlationMatrixEngine.py` — real pairwise Pearson
  correlation matrix engine over multiple symbols' aligned return
  series, plus a "candidate pairs" filter (absolute correlation above a
  configurable threshold, sorted descending) for pairs-trading candidate
  discovery. Doesn't call into `backtesting/pairsTradingStrategy.py`
  directly — feeds it conceptually, per FEATURES.md's framing.
- `src/quantengine/valueAtRiskCalculator.py` — real historical VaR
  (nearest-rank empirical percentile of a return series) AND real
  parametric/variance-covariance VaR (`z * stddev - mean`, with the
  z-score computed by bisecting `blackScholesOptionPricer`'s own normal
  CDF rather than a second approximate inverse-CDF formula). Also
  `calculatePortfolioStressTestPnLImpact`: a linear (no gamma/convexity)
  P&L-impact calculator over named, caller-supplied stress scenarios
  (e.g. "-20% equity shock") — those scenario definitions are
  ILLUSTRATIVE EXAMPLE inputs, not a calibrated/regulatory (CCAR/Basel
  FRTB) scenario library.
- `src/quantengine/volatilitySurfaceBuilder.py` — builds a real
  (expiry, strike) -> impliedVolatility surface by solving EVERY input
  quote through the EXISTING `solveImpliedVolatilityFromMarketPrice`
  (reused, not reimplemented), with a query function that linearly
  interpolates across strikes at a fixed expiry (flat-extrapolates
  beyond the observed strike range; does not interpolate across
  expiries).
- `src/quantengine/strategyLifecycle.py` — a real state machine
  (BACKTESTING -> PAPER_TRADING -> LIVE, plus a terminal REJECTED state)
  for promoting a named strategy. Promotion from BACKTESTING requires a
  real `backtesting/backtestRunner.BacktestResult` (reused) meeting an
  illustrative, documented gate (positive total P&L + a minimum executed
  trade count). Promotion from PAPER_TRADING requires a
  `PaperTradingTrackRecord` input (a compatible-shaped structure this
  module accepts but does not produce — `services/oms-gateway` is
  building the real paper-trading execution path in a parallel lane;
  integrating with it is out of scope here) meeting its own illustrative
  gate. A strategy that fails a gate becomes REJECTED with a clear
  `rejectionReason`, never silently stuck.
- `src/quantengine/marketMakingSandbox.py` — a simulated single-level
  two-sided-quote order book per symbol: submit a bid/ask quote, track a
  real inventory position as simulated taker fills cross it
  (`simulateTakerOrderCrossingQuote`), and REJECT a quote update that
  would let inventory exceed a configurable long/short limit if fully
  filled. This is the one module in this pass with shared MUTABLE state
  (inventory persists across calls) — the HTTP layer guards it with an
  explicit lock (see below); every other quant-engine module remains a
  pure, stateless computation.
- `src/quantengine/illustrativeSentimentTradingHook.py` — **read the
  module docstring in full before using any part of this file.** A TOY,
  hand-built positive/negative word-list lexicon scorer — explicitly
  NOT real NLP, NOT a trained model, no negation/context handling. Real
  filings/earnings ingestion is NOT implemented anywhere here; every
  function takes plain fixture `str` text. `generateOrderHookSuggestion`
  NEVER places a real order — it returns a structured
  BUY/SELL/HOLD + confidence suggestion and nothing else, and it is
  gated by a literal kill switch (`killSwitchEnabled`, default `False`)
  that must be explicitly flipped by the caller before any directional
  signal is even computed. Wiring this to a real order-submission path
  is explicitly out of scope and not attempted.
- **A real HTTP service** (`src/quantengine/httpServer.py`), stdlib-only
  (`http.server` + `json`, no framework dependency — same convention as
  matching-engine's/market-data's hand-rolled Rust bridges), on
  `127.0.0.1:8085`:
  - `GET /health`
  - `POST /options/price` — price + all four Greeks for one contract in a
    single response
  - `POST /options/implied-volatility` — Newton-Raphson solve given an
    observed market price
  - `POST /risk/statistics` — annualized Sharpe ratio, annualized Sortino
    ratio, and max drawdown for a `periodicReturns` series
  - `POST /arbitrage/scan` — theoretical-vs-live deviation alert for one
    caller-supplied theoretical price and live price
  - `POST /volatility/garch-forecast` — fits GARCH(1,1) to a
    `periodicReturns` series and returns the fitted parameters plus the
    "Expected Intraday Range" around `currentPrice`
  - `POST /correlation/matrix` — full pairwise Pearson correlation
    matrix plus the pairs-trading candidate-pairs filter for
    `returnSeriesBySymbol`
  - `POST /risk/value-at-risk` — both historical and parametric VaR for
    a `periodicReturns` series at a given `confidenceLevel`
  - `POST /market-making/quote`, `POST /market-making/simulate-fill`,
    `POST /market-making/inventory` — the market-making sandbox's quote
    submission, taker-fill simulation, and inventory query. These are
    the ONLY stateful endpoints in this service — they share one
    in-process `MarketMakingSandbox` instance guarded by an explicit
    `threading.Lock`, unlike every other endpoint above (and below),
    which remains a pure, stateless computation per request.
  - Every response carries a permissive CORS header so `apps/web` can
    call it directly (same "wrong once real auth exists" caveat as
    oms-gateway's and market-data's CORS middleware).
  - The backtest runner, pairs-trading strategy, volatility surface
    builder, strategy lifecycle state machine, and sentiment trading
    hook are NOT exposed over HTTP — they're verified via pytest only
    (see below). GARCH forecast, correlation matrix, VaR, and the
    market-making sandbox WERE additionally verified with real `curl`
    requests against a live running process (hand-worked values
    round-tripped exactly — see git history / build notes for the
    transcript).
- 186 passing tests total across `tests/`:
  - 7 in `test_blackScholesOptionPricer.py` (known reference-value
    checks, put-call parity, Greek sanity bounds, gamma call/put
    equality, an IV solver round-trip)
  - 14 in `test_riskStatistics.py` (hand-worked Sharpe/Sortino/max-
    drawdown values with the arithmetic spelled out in comments, edge
    cases like zero-variance/empty series)
  - 9 in `test_arbitrageScanner.py` (hand-worked deviation values, rich/
    cheap sign convention, exact-threshold boundary, batch scan)
  - 6 in `test_tickStore.py` (sorted insertion, range queries, unknown
    symbols)
  - 7 in `test_backtestRunner.py` (a hand-worked buy/hold/sell scenario
    with the accounting spelled out in comments, a determinism proof
    that runs the same backtest twice, position-flip and partial-close
    accounting edge cases)
  - 9 in `test_pairsTradingStrategy.py` (rolling stats, spread
    computation, an end-to-end backtest over a deterministic synthetic
    oscillating fixture proving at least one entry AND exit occurred,
    plus a determinism proof)
  - 22 in `test_garchVolatilityForecaster.py` (the GARCH(1,1) recursion's
    first several steps and full series checked against hand-worked
    arithmetic, stationarity/validation edge cases, a deterministic
    grid-search fit that beats every other candidate in its own search
    grid on log-likelihood, and an end-to-end Expected Intraday Range
    check)
  - 14 in `test_correlationMatrixEngine.py` (a hand-worked Pearson r,
    perfect +1/-1 correlation cases, matrix symmetry/diagonal checks,
    the candidate-pairs threshold filter and its sort order)
  - 19 in `test_valueAtRiskCalculator.py` (hand-worked historical VaR at
    two confidence levels, the inverse-normal-CDF bisection against
    known standard quantiles, hand-worked parametric VaR reusing
    riskStatistics.py's own mean/stddev fixture, and stress-test P&L
    scenarios)
  - 11 in `test_volatilitySurfaceBuilder.py` (IV solved back out of
    fixture quotes to their known source volatility, and a hand-worked
    linear-interpolation case across three strikes)
  - 17 in `test_strategyLifecycle.py` (the full BACKTESTING ->
    PAPER_TRADING -> LIVE happy path, gate-failure rejections with
    reasons, and illegal state transitions raising)
  - 15 in `test_marketMakingSandbox.py` (quote acceptance/rejection at
    the inventory limit boundary, taker fills increasing/decreasing
    inventory, fill-size capping at quoted quantity)
  - 14 in `test_illustrativeSentimentTradingHook.py` (lexicon scoring
    edge cases including case-insensitivity and no substring-matching,
    the kill switch defaulting OFF, and suggestion generation once
    explicitly enabled)
  - 22 in `test_httpServer.py` (live HTTP requests against a real
    `ThreadingHTTPServer` on an ephemeral port, including
    `/risk/statistics`, `/arbitrage/scan`, `/volatility/garch-forecast`,
    `/correlation/matrix`, `/risk/value-at-risk`, and the three
    `/market-making/*` endpoints, several with hand-worked values)

What's not built yet (see FEATURES.md §6, §7, §22):
- Portfolio-level Greeks aggregation (the HTTP API above is single-
  contract only; nothing sums positions yet)
- A "run a backtest" HTTP endpoint (backtest runner is pytest-verified
  only)
- **Explicitly out of scope for this pass**: "Paper trading mode sharing
  the exact same OMS code path as live" (FEATURES.md §7, P3) — real
  integration work with `services/oms-gateway`'s actual order-management
  code path that is beyond quant-engine's boundary as a standalone
  research-tier module. `strategyLifecycle.py`'s `PaperTradingTrackRecord`
  is a compatible-SHAPED input this module accepts, not a real
  integration with oms-gateway's parallel paper-trading lane.
- Real filings/earnings ingestion for the sentiment trading hook —
  `illustrativeSentimentTradingHook.py` takes fixture text only; there is
  no network call, scraper, or data feed anywhere in that module. Its
  lexicon scorer is a documented TOY, not real NLP.

Known limitations/gaps in this pass's new modules (be aware of these
before treating any of them as production-grade):
- **GARCH(1,1) fitting** (`garchVolatilityForecaster.py`) uses a
  variance-targeting + coarse grid-search quasi-MLE, NOT a
  production-grade continuous optimizer (no `scipy.optimize` — kept
  stdlib-only). The conditional-variance RECURSION itself is exact; the
  PARAMETER FIT is a documented simplification.
- **VaR stress scenarios** (`valueAtRiskCalculator.calculatePortfolioStressTestPnLImpact`)
  are illustrative example inputs a caller supplies (e.g. "-20% equity
  shock"), not a calibrated/regulatory (CCAR/Basel FRTB) scenario
  library, and the repricing is purely linear (no gamma/convexity).
- **Volatility surface interpolation** is linear across strikes at a
  fixed expiry only; it does not interpolate or extrapolate across
  expiries, and extrapolates flat beyond the observed strike range.
- **Strategy lifecycle promotion gates** (`strategyLifecycle.py`) use
  illustrative default thresholds (e.g. "at least 5 backtest trades and
  positive P&L", "at least 20 paper trades, positive P&L, and a 40% win
  rate") — reasonable-sounding, documented, but not derived from any
  real risk/compliance policy.
- **Market-making sandbox** (`marketMakingSandbox.py`) is a single
  price-level-per-side simulation with caller-driven taker fills, not a
  real matching-engine order book.
- **The sentiment trading hook is a toy lexicon, full stop** — see its
  module docstring. It never places a real order under any
  configuration; the kill switch defaults OFF.

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
curl -X POST http://127.0.0.1:8085/risk/statistics -d '{
  "periodicReturns": [0.04, -0.02, 0.04, -0.02, 0.04, -0.02],
  "periodicRiskFreeRate": 0.0,
  "periodsPerYear": 252
}'
curl -X POST http://127.0.0.1:8085/arbitrage/scan -d '{
  "theoreticalFairPrice": 100.0,
  "liveMarketPrice": 102.0,
  "deviationThresholdPercentage": 1.0
}'
curl -X POST http://127.0.0.1:8085/volatility/garch-forecast -d '{
  "periodicReturns": [0.01, -0.015, 0.02, -0.01, 0.005, -0.02, 0.015, -0.005, 0.01, -0.01,
                       0.025, -0.02, 0.01, -0.015, 0.02, -0.01, 0.005, -0.025, 0.015, -0.01],
  "currentPrice": 100.0
}'
curl -X POST http://127.0.0.1:8085/correlation/matrix -d '{
  "returnSeriesBySymbol": {"X": [1, 2, 3, 4], "Y": [1, 3, 2, 5]},
  "minimumAbsoluteCorrelationThreshold": 0.5
}'
curl -X POST http://127.0.0.1:8085/risk/value-at-risk -d '{
  "periodicReturns": [-0.05, -0.03, -0.02, -0.01, 0.0, 0.01, 0.02, 0.03, 0.04, 0.05],
  "confidenceLevel": 0.90
}'
curl -X POST http://127.0.0.1:8085/market-making/quote -d '{
  "symbol": "AAPL", "maximumAbsoluteInventory": 100.0,
  "bidPrice": 99.0, "bidQuantity": 50.0, "askPrice": 101.0, "askQuantity": 50.0
}'
curl -X POST http://127.0.0.1:8085/market-making/simulate-fill -d '{
  "symbol": "AAPL", "takerSide": "BID", "quantity": 20.0
}'
curl -X POST http://127.0.0.1:8085/market-making/inventory -d '{"symbol": "AAPL"}'
```
