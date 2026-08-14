# quant-engine

Research-tier component — see `ARCHITECTURE.md` §6 and §8 in the repo root.

## Status: Black-Scholes pricer, risk statistics, arbitrage scanner, a
## deterministic backtest engine, GARCH(1,1) volatility forecasting, a
## correlation matrix engine, VaR/stress-testing, a volatility surface
## builder, a strategy deployment lifecycle state machine, a
## market-making sandbox, an illustrative sentiment trading hook, and an
## ESG scoring/screening engine (illustrative dataset, real scoring math)
## are all real and tested; HTTP service covers pricing/Greeks/IV plus
## risk, arbitrage, GARCH forecast, correlation matrix, VaR,
## market-making sandbox, and ESG screening endpoints. FEATURES.md §22
## ("Deep Quant & Algorithmic Trading Internals") pass 1 adds:
## portfolio-level Greeks aggregation, IV Rank/Percentile, implied-vs-
## realized volatility comparison, a synthetic-position builder,
## delta-hedging threshold monitoring, Kelly-criterion position sizing,
## a strategy-level correlation matrix, Engle-Granger cointegration
## testing (with a real from-scratch OLS + Augmented Dickey-Fuller
## implementation), realistic backtest cost modeling (square-root
## market impact, slippage, partial fills), and a seeded Monte Carlo
## engine (Asian option pricing + portfolio VaR, convergence-tested
## against the closed-form Black-Scholes price) — see the "§22 pass 1"
## section below for exactly what's real vs. illustrative in each.
## FEATURES.md §22 pass 2 (this pass) adds the remaining SIX §22 items,
## completing the section: a walk-forward optimizer with automatic
## overfitting warnings, a factor risk model, a latency benchmarking
## dashboard, a cross-asset macro dashboard, options-aware corporate-
## action handling (stock splits + ex-dividend early-exercise risk), and
## a hand-rolled Gaussian Hidden Markov Model for regime detection (real
## Baum-Welch EM + Viterbi, stdlib-only, no hmmlearn/scikit-learn) — see
## the new "§22 pass 2" section below. HTTP service now additionally
## covers factor-risk, latency-benchmark, and both corporate-action
## endpoints (5 new POST routes); the walk-forward optimizer and the HMM
## regime detector are pytest-verified only this pass (see that section
## for why).
## FEATURES.md §16 ("AI, Data & Research") — this pass adds all SEVEN §16
## items: a real stock/fund screener with a compound AND/OR filter builder
## and saved screens, a real TF-IDF-retrieval research copilot (RAG over a
## small synthetic filings corpus, always cited and non-advisory), an HHI-
## based portfolio health check with genuinely input-derived plain-language
## nudges, a tax-loss harvesting advisor with a real 61-day wash-sale
## window check, an alternative-data module (sentiment aggregation +
## z-score filing-anomaly detection) wired into the existing §7 NLP
## trading hook, a real Brinson-Hood-Beebower P&L attribution engine, and
## a real rules-based custom index constructor + backtester — see the new
## "§16" section below for exactly what's real vs. illustrative in each.
## HTTP service now additionally covers 11 new POST routes across all
## seven items.

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
- `src/quantengine/esgScoringEngine.py` — ESG (Environmental/Social/
  Governance) composite scoring and screening. **Read the module
  docstring in full before treating any score here as real research.**
  `ILLUSTRATIVE_ESG_DATASET_BY_SYMBOL` is a STATIC, HAND-FABRICATED set of
  per-symbol E/S/G sub-scores for six illustrative demo symbols (reusing
  this repo's existing `DEMO-EQ` / `SIM-AAPL` demo-symbol convention from
  `services/oms-gateway` and `services/market-data`, plus two invented
  `SIM-` symbols carrying controversial-sector flags) — it is NOT sourced
  from MSCI, Sustainalytics, ISS ESG, Refinitiv, Bloomberg ESG, or any
  other real rating agency; nobody researched these companies' actual
  ESG practices. What IS real: `calculateCompositeEsgScore` — a real,
  documented weighted average (Environmental 40%, Social 30%, Governance
  30%, weights summing to exactly 1.0) — and
  `screenCandidateSymbolsAgainstEsgCriteria` — real minimum-score-per-
  pillar filtering, real controversial-sector exclusion (a symbol
  carrying ANY excluded flag is rejected regardless of its scores), and
  real descending ranking by composite score, with unknown symbols and
  criteria-failing symbols reported separately from the ranked results.
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
- `src/quantengine/walkForwardOptimizer.py` (§22 pass 2) — real rolling
  train/(immediately-following)test walk-forward optimization reusing
  `backtesting/backtestRunner.py` UNMODIFIED: for each window, grid-
  searches a caller-supplied parameter grid on the in-sample slice,
  re-runs ONLY the best combination on the out-of-sample slice, and rolls
  forward. `evaluateOverfittingWarning` applies two real, documented
  heuristics — Walk-Forward Efficiency (Pardo) below a 0.5 threshold, and
  fewer than 10 in-sample observations per tunable parameter (a standard
  "events per variable" rule of thumb) — either one flags overfitting.
  Pytest-verified only (see "§22 pass 2" below for why).
- `src/quantengine/factorRiskModel.py` (§22 pass 2) — a Fama-French-
  style/Barra-lite factor risk model. Per-holding factor exposures
  (`marketBeta`, `size`, `value`, or any caller-named factor) are
  ILLUSTRATIVE/CALLER-SUPPLIED — no real factor-loading regression
  against real market data is performed anywhere in this module. What's
  real: `computePortfolioFactorExposures` (a real weight-weighted sum
  into portfolio-level exposures) and `computeFactorAttribution` (a real
  linear factor-model return decomposition: `contribution_f = exposure_f
  * factorReturn_f`, with the remainder attributed to
  `idiosyncraticReturn`). Wired to `POST /portfolio/factor-risk`,
  live-verified.
- `src/quantengine/latencyBenchmarkDashboard.py` (§22 pass 2) — real
  order round-trip histograms and percentile stats (p50/p95/p99/max,
  nearest-rank convention shared with `valueAtRiskCalculator.py`), with
  multi-venue side-by-side comparison. `measureRoundTripTimeSamplesOverHttp`
  performs REAL, `time.perf_counter()`-timed HTTP round trips against any
  URL (designed to time `services/oms-gateway`'s real
  `POST /orders/submit`); `services/oms-gateway` was NOT reachable during
  this pass's live verification (it isn't running — no Docker, and it
  depends on `services/matching-engine` being up too), so the live
  transcript below instead times quant-engine's own running `/health`
  endpoint as proof the timing function performs genuine network I/O —
  see "§22 pass 2" for the honest transcript. Wired to
  `POST /latency/benchmark` (caller-supplied samples), live-verified.
- `src/quantengine/crossAssetMacroDashboard.py` (§22 pass 2) — a cross-
  asset macro dashboard (yields/DXY/crude/VIX/equity-index). Every named
  macro series is ILLUSTRATIVE/FIXTURE data a caller supplies — no real
  market-data feed is ingested anywhere in this module. What's real: a
  real aggregation/alignment-validation step plus real pairwise
  correlation across every named series, computed by REUSING
  `correlationMatrixEngine.buildPairwiseCorrelationMatrix` verbatim (zero
  correlation math is reimplemented here). Pytest-verified only.
- `src/quantengine/optionsCorporateActionHandler.py` (§22 pass 2) —
  options-aware corporate-action handling. `applyStockSplitAdjustmentToOptionPosition`
  implements the real, standard OCC-style split-adjustment formula
  (`newStrike = oldStrike / splitRatio`, `newQuantity = oldQuantity *
  splitRatio`) with a `notionalExposureIsPreserved` invariant check.
  `evaluateEarlyExerciseRiskAroundExDividendDate` implements the real
  textbook necessary condition for early exercise of an American call to
  be worth considering (dividend exceeds remaining call time value) —
  European contracts and puts are always returned unflagged with an
  explanatory reason, never evaluated against a formula that doesn't
  apply to them. Wired to
  `POST /options/corporate-action/split-adjustment` and
  `POST /options/corporate-action/early-exercise-risk`, live-verified.
- `src/quantengine/regimeDetectionHmm.py` (§22 pass 2) — a REAL,
  hand-rolled Gaussian Hidden Markov Model for TRENDING/MEAN_REVERTING/
  HIGH_VOLATILITY regime classification: a real scaled forward algorithm,
  scaled backward algorithm, Baum-Welch EM parameter re-estimation
  (transition matrix + per-state Gaussian mean/variance), and log-space
  Viterbi most-likely-state-sequence decoding — genuine from-scratch
  numerical work, stdlib `math`-only (no numpy, no
  hmmlearn/scikit-learn). Regime labels are assigned from FITTED
  per-state statistics via a real, documented rule (highest variance ->
  HIGH_VOLATILITY; highest mean among the rest -> TRENDING; remainder ->
  MEAN_REVERTING). Pytest-verified only (see "§22 pass 2" below for why).
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
  - `POST /esg/screen` — ESG screening/ranking over a caller-supplied
    `candidateSymbols` list against optional minimum-composite,
    minimum-per-pillar, and controversial-sector-exclusion criteria.
    Returns `rankedResults` (descending by composite score),
    `excludedSymbols` (known symbols that failed a criterion), and
    `unknownSymbols` (candidates with no illustrative ESG profile) —
    **the underlying per-symbol dataset is illustrative/fabricated; the
    scoring formula and screening logic are real** (see
    `esgScoringEngine.py` above).
  - `POST /portfolio/greeks` — real net portfolio Greeks (delta/gamma/
    vega/theta) across a caller-supplied `positions` list of
    pre-computed per-contract Greeks + signed quantity. See
    `portfolioGreeksAggregator.py` below.
  - `POST /volatility/iv-rank` — real IV Rank AND real IV Percentile for
    a `currentImpliedVolatility` against a caller-supplied
    `historicalImpliedVolatilitySeries` (illustrative/fixture — no
    historical-IV ingestion happens here). See `ivRankCalculator.py`
    below.
  - `POST /portfolio/delta-hedge-check` — same `positions` shape as
    `/portfolio/greeks` plus a `deltaThreshold`; returns the real
    `isThresholdBreached` alert boolean and the exact
    `hedgeQuantityInShares` needed to flatten net delta-equivalent
    exposure. NEVER places a real hedge order. See
    `deltaHedgingMonitor.py` below.
  - `POST /sizing/kelly-criterion` — real classic (discrete win/loss)
    Kelly fraction plus a fractional-Kelly (default half-Kelly)
    recommended allocation. Returns a bankroll FRACTION only — no
    account size, no order. See `kellyCriterionSizer.py` below.
  - `POST /portfolio/factor-risk` (§22 pass 2) — real weight-weighted
    portfolio factor exposures plus a real factor-attribution return
    decomposition. Illustrative per-holding factor exposures. See
    `factorRiskModel.py` above.
  - `POST /latency/benchmark` (§22 pass 2) — real per-venue latency
    histogram + p50/p95/p99/max percentile stats over caller-supplied
    round-trip-time samples. See `latencyBenchmarkDashboard.py` above.
  - `POST /options/corporate-action/split-adjustment` and
    `POST /options/corporate-action/early-exercise-risk` (§22 pass 2) —
    real split-adjusted strike/quantity, and the real textbook
    early-exercise-risk flag for an American call near an ex-dividend
    date. See `optionsCorporateActionHandler.py` above.
  - Every response carries a permissive CORS header so `apps/web` can
    call it directly (same "wrong once real auth exists" caveat as
    oms-gateway's and market-data's CORS middleware).
  - The backtest runner, pairs-trading strategy, volatility surface
    builder, strategy lifecycle state machine, sentiment trading hook,
    implied-vs-realized volatility comparison, synthetic position
    builder, strategy correlation matrix, cointegration tester, realistic
    backtest cost model, Monte Carlo engine, walk-forward optimizer,
    cross-asset macro dashboard, and HMM regime detector are NOT exposed
    over HTTP — they're verified via pytest only (see below and the "§22
    pass 1"/"§22 pass 2" sections for exactly why in each case). GARCH
    forecast, correlation matrix, VaR, the market-making sandbox, ESG
    screening, portfolio Greeks aggregation, IV Rank/Percentile, the
    delta-hedging threshold check, the Kelly criterion sizer, the factor
    risk model, the latency benchmark dashboard, and both corporate-
    action endpoints WERE additionally verified with real `curl`
    requests against a live running process (hand-worked values
    round-tripped exactly — see the "§22 pass 1"/"§22 pass 2" sections
    for the exact transcripts).
- 471 passing tests total across `tests/` (362 after §22 pass 1 + 109
  added in the FEATURES.md §22 pass 2 below):
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
  - 26 in `test_esgScoringEngine.py` (the hand-worked composite-score
    weighted-average example, sub-score range validation, single- and
    multi-criterion screening including minimum composite/pillar scores
    and sector exclusion overriding a high score, unknown-symbol and
    empty-candidate-list edge cases, tie-break-by-symbol ranking)
  - 49 in `test_httpServer.py` (live HTTP requests against a real
    `ThreadingHTTPServer` on an ephemeral port, including
    `/risk/statistics`, `/arbitrage/scan`, `/volatility/garch-forecast`,
    `/correlation/matrix`, `/risk/value-at-risk`, the three
    `/market-making/*` endpoints, `/esg/screen`, `/portfolio/greeks`,
    `/volatility/iv-rank`, `/portfolio/delta-hedge-check`,
    `/sizing/kelly-criterion`, and (§22 pass 2) `/portfolio/factor-risk`,
    `/latency/benchmark`, `/options/corporate-action/split-adjustment`,
    and `/options/corporate-action/early-exercise-risk`, several with
    hand-worked values)
  - 10 in `test_portfolioGreeksAggregator.py` (hand-worked quantity-
    weighted two-position aggregation, empty-portfolio all-zero result,
    long/short identical positions netting to exactly zero, real
    Black-Scholes-backed positions cross-checked against a manual sum)
  - 15 in `test_ivRankCalculator.py` (hand-worked IV Rank and IV
    Percentile against the same fixture series, rank boundary cases at
    and beyond the historical min/max, percentile's strict-less-than
    convention, flat-series division-by-zero handling for Rank vs.
    Percentile's well-defined behavior in the same case)
  - 10 in `test_impliedVsRealizedVolatility.py` (hand-worked log-return
    and annualized-realized-volatility arithmetic, an end-to-end check
    that solves a known volatility back out via the real IV solver and
    pairs it with a real realized-volatility computation, spread/ratio
    sign checks)
  - 14 in `test_syntheticPositionBuilder.py` (all five real
    put-call-parity synthetic structures validated, wrong-strike and
    wrong-side rejections, scaled-up multi-unit matching, combined-
    Greeks aggregation reusing `portfolioGreeksAggregator`)
  - 11 in `test_deltaHedgingMonitor.py` (hand-worked breach/no-breach
    and signed hedge-quantity cases at the default and custom share
    multiplier, exact-threshold boundary using strict inequality,
    validation of non-positive threshold/multiplier)
  - 15 in `test_kellyCriterionSizer.py` (hand-worked classic Kelly
    fraction and its half-Kelly scaling, zero-edge and negative-edge
    cases, the continuous mean/variance formula's hand-worked value,
    fractional-Kelly multiplier range validation)
  - 9 in `test_strategyCorrelationMatrix.py` (perfect positive/negative
    correlation between synthetic strategy return series, hidden-
    correlation flagging above/below threshold, descending-magnitude
    sort order, reuse of `correlationMatrixEngine`'s real math confirmed
    via exact r=1.0/-1.0 checks)
  - 17 in `test_cointegrationTester.py` (hand-worked 2x2 matrix
    inversion and matrix multiplication, a hand-worked simple-linear-
    regression case, a fully hand-worked Augmented Dickey-Fuller test
    statistic on a small alternating series, a strongly mean-reverting
    series rejecting the unit root at every level, a random-walk-with-
    drift series failing to reject it, and full Engle-Granger end-to-end
    checks on both a constructed-cointegrated and a constructed-non-
    cointegrated series pair)
  - 18 in `test_realisticBacktestCostModel.py` (hand-worked square-root
    market-impact fraction and its scaling with coefficient/order size,
    hand-worked buy/sell slippage adjustment, hand-worked full and
    partial order-book fills across three price levels with exact VWAP
    arithmetic, empty-book and exact-liquidity edge cases)
  - 16 in `test_monteCarloEngine.py` (deterministic seeded-path
    reproducibility, a REAL convergence check asserting the Monte Carlo
    European call/put price lands within 4 standard errors of the
    closed-form Black-Scholes price at 50,000 paths, standard-error
    shrinkage with path count, Asian-option price provably below the
    equivalent European price, and a Monte Carlo VaR result independently
    recomputed from its own returned simulated terminal values)
  - 17 in `test_walkForwardOptimizer.py` (§22 pass 2 — cartesian
    parameter-grid generation, a hand-worked single-window and
    two-rolling-window walk-forward run over a buy-and-hold-quantity
    strategy with exact P&L arithmetic, a custom-step overlapping-window
    case, and hand-worked overfitting-warning threshold boundaries for
    both the Walk-Forward Efficiency rule and the observations-per-
    parameter rule, including the exact-threshold non-flagging boundary)
  - 15 in `test_factorRiskModel.py` (§22 pass 2 — hand-worked two-holding
    weighted factor-exposure aggregation, negative-weight/short-position
    handling, mismatched-factor-set rejection, a hand-worked two-factor
    attribution decomposition with an exact idiosyncratic residual,
    contribution-fraction-of-return arithmetic, and an end-to-end
    exposure-then-attribution round trip)
  - 16 in `test_latencyBenchmarkDashboard.py` (§22 pass 2 — REAL live
    HTTP round-trip timing against an actual `ThreadingHTTPServer` for
    both GET and POST, unreachable-URL error propagation, hand-worked
    nearest-rank p50/p95/p99 percentiles and histogram bucket boundaries
    on a small sorted fixture, all-identical-sample and single-sample
    edge cases, and multi-venue side-by-side comparison including a
    10x-scaled-venue percentile-scaling check)
  - 10 in `test_crossAssetMacroDashboard.py` (§22 pass 2 — reproduces
    `test_correlationMatrixEngine.py`'s own hand-worked Pearson r via
    this module's aggregation to prove real reuse, a five-illustrative-
    macro-series aggregation, perfect +1/-1 correlation fixtures for
    "risk-on"/"risk-off" scenarios, misaligned-series-length rejection
    naming the offending series, and the strongest-pairs filter's
    threshold/sort behavior)
  - 18 in `test_optionsCorporateActionHandler.py` (§22 pass 2 —
    hand-worked 2-for-1, 3-for-2, and 1-for-4-reverse split adjustments
    with a notional-exposure-invariance check across six different
    ratios, hand-worked early-exercise-risk flagging both above and
    below the dividend/time-value boundary including the exact-boundary
    non-flagging case, and European/put never-flagged guarantees)
  - 21 in `test_regimeDetectionHmm.py` (§22 pass 2 — a fully hand-worked
    two-step scaled-forward-algorithm probability trace including the
    exact scaling factors and log-likelihood, forward/backward-product
    state-occupation-probability validation, an obvious-sequence Viterbi
    recovery check, quantile-initialization validity checks, a genuine
    Baum-Welch log-likelihood-monotonicity proof across iterations, a
    hand-worked three-state regime-labeling case, and end-to-end
    3-state and 2-state (reduced-scope) regime-detection pipelines)

## FEATURES.md §22 ("Deep Quant & Algorithmic Trading Internals") — pass 1

Ten of the sixteen listed items were built this pass, each with real math
and real tests, four of them (plus a fifth composite) additionally wired
to and verified over live HTTP. **Six items (walk-forward optimization,
a factor risk model, a latency benchmarking dashboard, a cross-asset
macro dashboard, options-aware corporate-action handling, and HMM regime
detection) were NOT reached this pass** — see "What's not built yet"
below for the honest list.

1. **`portfolioGreeksAggregator.py`** — real net portfolio delta/gamma/
   vega/theta, quantity-weighted sum across positions. Reuses
   `blackScholesOptionPricer.OptionGreeksResult` verbatim; an empty
   portfolio is a legitimate flat book (all-zero result, not an error).
   Wired to `POST /portfolio/greeks`, live-verified.
2. **`ivRankCalculator.py`** — real IV Rank (`(current-min)/(max-min)`)
   and real IV Percentile (fraction of history strictly below current)
   against a caller-supplied, ILLUSTRATIVE/FIXTURE historical IV series
   — no historical-IV data ingestion exists anywhere in this module.
   Wired to `POST /volatility/iv-rank`, live-verified.
3. **`impliedVsRealizedVolatility.py`** — reuses the EXISTING Newton-
   Raphson IV solver unmodified; computes REAL annualized realized
   volatility as the sample-stddev-of-log-returns (documented as the
   sample/N-1 convention, distinct from `riskStatistics.py`'s
   population convention elsewhere in this codebase — different
   statistics, different standard conventions, both documented). Pytest-
   verified only (not wired to HTTP this pass).
4. **`syntheticPositionBuilder.py`** — five real put-call-parity
   synthetic structures (synthetic long/short stock, synthetic long
   call, synthetic long put, synthetic covered call), each validated
   against an exact signed-leg-ratio definition (including N-unit
   scaled matches); combines Greeks via #1 above, not reimplemented.
   Real margin calculation is explicitly OUT of scope (see module
   docstring) — margin is a pass-through caller-supplied number for
   reporting only. Pytest-verified only.
5. **`deltaHedgingMonitor.py`** — reuses #1's aggregation; computes the
   exact signed share quantity to flatten net delta-equivalent exposure
   to ZERO (not merely back within the threshold — see module docstring
   for why "hedge to flat" is the documented convention) and a real
   `isThresholdBreached` boolean. NEVER places a real order. Wired to
   `POST /portfolio/delta-hedge-check`, live-verified.
6. **`kellyCriterionSizer.py`** — TWO real, distinct Kelly formulas: the
   classic discrete win/loss formula (`f* = (bp-q)/b`) and the
   continuous mean/variance approximation (`f* = mean/variance`), each
   documented as correct for a different input shape. Real fractional-
   Kelly scaling (default half-Kelly) with the over-aggressiveness of
   full Kelly documented explicitly. Wired to `POST /sizing/kelly-
   criterion`, live-verified.
7. **`strategyCorrelationMatrix.py`** — a thin, real wrapper reusing
   `correlationMatrixEngine.py`'s Pearson math verbatim over
   STRATEGY-level return series instead of instrument series, plus
   `identifyHiddenlyCorrelatedStrategyPairs` — same real correlation
   number, reframed for concentration-risk reading rather than pairs-
   trading-entry reading. Pytest-verified only.
8. **`cointegrationTester.py`** — a REAL Engle-Granger two-step test,
   more rigorous than #7/`correlationMatrixEngine.py`'s simple
   correlation screen (see module docstring for why). Genuine numerical
   work implemented from scratch, stdlib-only: a general OLS solver via
   Gauss-Jordan matrix inversion (own implementation, no numpy), and a
   real Augmented Dickey-Fuller test statistic (with an optional
   augmented-lag order) computed from that OLS machinery. **Honest
   caveat**: the critical values compared against are standard
   asymptotic Dickey-Fuller values, NOT the more conservative MacKinnon
   (1991) Engle-Granger-adjusted critical values — see the module
   docstring for exactly why plain DF critical values are measurably
   anti-conservative here. The test statistic itself is real and
   correctly computed; only the significance-threshold table is a
   documented simplification. Pytest-verified only (deep numerical item,
   per the task's own HTTP-wiring guidance).
9. **`realisticBacktestCostModel.py`** — extends (without modifying)
   `backtesting/backtestRunner.py`'s documented "no slippage" fill
   simplification: a real Almgren-Chriss-STYLE square-root market-impact
   cost function (impact proportional to `sqrt(orderQuantity/averageDailyVolume)`,
   scaled by volatility and a caller-tunable, ILLUSTRATIVE
   `impactCoefficient` — real math, illustrative calibration constant,
   documented as such), real directional slippage, and a real order-
   book-walking PARTIAL FILL simulator with exact volume-weighted
   average fill price. Pytest-verified only.
10. **`monteCarloEngine.py`** — a real, SEEDED geometric Brownian motion
    path simulator (exact step-wise GBM discretization, stdlib
    `random.Random`, no numpy); prices a genuinely path-dependent Asian
    (arithmetic-average) option (no closed-form Black-Scholes analogue
    exists for it, which is the whole point of using Monte Carlo here)
    plus a European option INCLUDED SPECIFICALLY as a correctness check
    — `tests/test_monteCarloEngine.py` asserts the Monte Carlo European
    price converges within 4 of its own standard errors of the real
    closed-form Black-Scholes price at 50,000 paths, a genuine,
    meaningful validation. Also a Monte Carlo portfolio VaR (real
    empirical nearest-rank percentile loss over simulated terminal
    values, same sign convention as `valueAtRiskCalculator.py`) —
    documented as a single-aggregate-GBM simplification, not a real
    multi-asset correlated simulation. Pytest-verified only.

**Live HTTP verification transcripts** (against a real running
`quant-engine-server` process on port 8085, killed before and after):

```
$ curl -s -X POST http://127.0.0.1:8085/portfolio/greeks -d '{
  "positions": [
    {"identifier": "A", "quantity": 10.0, "delta": 0.5, "gamma": 0.02, "vegaPerOnePercentVolatilityChange": 0.15, "thetaPerCalendarDay": -0.05},
    {"identifier": "B", "quantity": -5.0, "delta": 0.3, "gamma": 0.01, "vegaPerOnePercentVolatilityChange": 0.10, "thetaPerCalendarDay": -0.02}
  ]
}'
{"netDelta": 3.5, "netGamma": 0.15000000000000002, "netVegaPerOnePercentVolatilityChange": 1.0, "netThetaPerCalendarDay": -0.4, "positionCount": 2}
# hand-worked: netDelta = 10*0.5 + (-5)*0.3 = 3.5 -- matches exactly.

$ curl -s -X POST http://127.0.0.1:8085/volatility/iv-rank -d '{
  "currentImpliedVolatility": 0.19,
  "historicalImpliedVolatilitySeries": [0.10, 0.12, 0.14, 0.16, 0.18, 0.20, 0.50]
}'
{"currentImpliedVolatility": 0.19, "historicalMinimumImpliedVolatility": 0.1, "historicalMaximumImpliedVolatility": 0.5, "impliedVolatilityRank": 0.22499999999999998, "impliedVolatilityPercentile": 0.7142857142857143}
# hand-worked: ivRank = (0.19-0.10)/(0.50-0.10) = 0.225; ivPercentile = 5/7 -- matches exactly.

$ curl -s -X POST http://127.0.0.1:8085/portfolio/delta-hedge-check -d '{
  "positions": [
    {"identifier": "A", "quantity": 10.0, "delta": 0.5, "gamma": 0.02, "vegaPerOnePercentVolatilityChange": 0.15, "thetaPerCalendarDay": -0.05},
    {"identifier": "B", "quantity": -5.0, "delta": 0.3, "gamma": 0.01, "vegaPerOnePercentVolatilityChange": 0.10, "thetaPerCalendarDay": -0.02}
  ],
  "deltaThreshold": 2.0
}'
{"netDelta": 3.5, "deltaThreshold": 2.0, "isThresholdBreached": true, "hedgeQuantityInShares": -350.0, "sharesPerContractMultiplierUsed": 100.0}
# hand-worked: |3.5| > 2.0 -> breached; hedgeQuantityInShares = -3.5*100 = -350.0 -- matches exactly.

$ curl -s -X POST http://127.0.0.1:8085/sizing/kelly-criterion -d '{"winProbability": 0.55, "winLossPayoutRatio": 1.5}'
{"fullKellyFraction": 0.25000000000000006, "fractionalMultiplier": 0.5, "recommendedAllocationFraction": 0.12500000000000003}
# hand-worked: f* = (1.5*0.55-0.45)/1.5 = 0.25, half-Kelly = 0.125 -- matches exactly.
```

## FEATURES.md §22 ("Deep Quant & Algorithmic Trading Internals") — pass 2

This pass builds the remaining SIX §22 items, completing all 16 listed
in FEATURES.md §22. Four of the six are wired to and live-verified over
HTTP; the other two (the walk-forward optimizer and the HMM regime
detector) are pytest-verified only — see each item's own note below for
exactly why.

11. **`walkForwardOptimizer.py`** — real rolling train/(immediately-
    following)test walk-forward optimization reusing
    `backtesting/backtestRunner.py` UNMODIFIED, plus a real, two-rule,
    documented overfitting-warning heuristic (Walk-Forward Efficiency
    below 0.5, per Pardo; fewer than 10 in-sample observations per
    tunable parameter, a standard "events per variable" rule of thumb).
    **Not wired to HTTP**: its `strategyFactory` parameter is a Python
    callable (`dict -> StrategyCallback`) — there is no JSON-safe way to
    transmit an arbitrary trading-strategy function over HTTP without
    inventing a strategy-description mini-language first (out of scope
    for this pass), so this stays pytest-verified only, same documented
    reasoning `backtestRunner.py` itself already has for not being
    exposed over HTTP.
12. **`factorRiskModel.py`** — real weight-weighted portfolio factor
    exposure aggregation and a real linear factor-attribution return
    decomposition. Per-holding factor exposures are ILLUSTRATIVE/CALLER-
    SUPPLIED — no real Fama-French/Barra factor-loading regression
    exists anywhere in this module. Wired to `POST /portfolio/factor-risk`,
    live-verified.
13. **`latencyBenchmarkDashboard.py`** — real p50/p95/p99/max percentile
    stats (nearest-rank convention, reused from
    `valueAtRiskCalculator.py`'s own documented convention) and a real
    fixed-width histogram, with multi-venue comparison.
    `measureRoundTripTimeSamplesOverHttp` performs REAL
    `time.perf_counter()`-timed HTTP round trips against any URL — see
    the live-verification transcript below for a genuine timed run
    against quant-engine's own live server (oms-gateway, the intended
    real target per the task, was not reachable — see that transcript
    for the honest reachability check). Wired to `POST /latency/benchmark`
    (accepting caller-supplied samples, since an HTTP handler cannot
    synchronously perform ANOTHER live network-timing measurement as
    part of serving its own request without conflating the two), live-
    verified.
14. **`crossAssetMacroDashboard.py`** — a real data-aggregation/
    alignment-validation step plus real cross-asset correlation, reusing
    `correlationMatrixEngine.buildPairwiseCorrelationMatrix` VERBATIM
    (zero correlation math reimplemented). Every named macro series
    (yields/DXY/crude/VIX/equity-index) is ILLUSTRATIVE/FIXTURE data — no
    real macro data feed exists anywhere in this repo or this module.
    **Not wired to HTTP** this pass — a straightforward omission given
    the remaining time budget, not a technical blocker (its shape is
    HTTP-friendly and a `POST /macro/dashboard`-style endpoint would be a
    reasonable near-term addition); pytest-verified only.
15. **`optionsCorporateActionHandler.py`** — the real, standard OCC-style
    split-adjustment formula (`newStrike = oldStrike / splitRatio`,
    `newQuantity = oldQuantity * splitRatio`) and the real textbook
    early-exercise-risk necessary condition for American calls near an
    ex-dividend date (dividend exceeds remaining call time value).
    Wired to `POST /options/corporate-action/split-adjustment` and
    `POST /options/corporate-action/early-exercise-risk`, live-verified.
16. **`regimeDetectionHmm.py`** — a REAL, hand-rolled Gaussian Hidden
    Markov Model: scaled forward algorithm, scaled backward algorithm,
    Baum-Welch EM (transition matrix + per-state Gaussian mean/variance
    re-estimation), and log-space Viterbi decoding — genuine from-
    scratch numerical work, stdlib `math`-only, no numpy/hmmlearn/
    scikit-learn. Regimes are labeled from FITTED per-state statistics
    via a real, documented rule. **Not wired to HTTP**: fitting a fresh
    HMM per request is a genuinely heavier synchronous computation than
    every other endpoint in this service (Baum-Welch is an iterative
    algorithm, unlike every other endpoint's single closed-form/one-pass
    computation) and the "gate which strategies are allowed to run"
    consumer described in FEATURES.md is naturally an internal/batch
    caller rather than a live request/response client, so this stays
    pytest-verified only this pass, per the task's own explicit
    allowance for this item.

**Live HTTP verification transcripts** (against a real running
`quant-engine-server` process on port 8085, killed before and after):

```
$ curl -s -X POST http://127.0.0.1:8085/portfolio/factor-risk -d '{
  "holdings": [
    {"symbol": "DEMO-EQ", "portfolioWeight": 0.6, "factorExposuresByName": {"marketBeta": 1.2, "size": 0.5}},
    {"symbol": "SIM-AAPL", "portfolioWeight": 0.4, "factorExposuresByName": {"marketBeta": 0.8, "size": -0.3}}
  ],
  "factorReturnsByName": {"marketBeta": 0.02, "size": 0.01},
  "actualOrExpectedPortfolioReturn": 0.03
}'
{"portfolioExposureByFactor": {"size": 0.18, "marketBeta": 1.04}, "totalPortfolioWeight": 1.0, "holdingCount": 2, "contributionByFactor": {"size": 0.0018, "marketBeta": 0.020800000000000003}, "totalFactorContribution": 0.022600000000000002, "idiosyncraticReturn": 0.007399999999999997, "actualOrExpectedPortfolioReturn": 0.03}
# hand-worked: marketBeta exposure = 0.6*1.2+0.4*0.8 = 1.04; size exposure = 0.6*0.5+0.4*-0.3 = 0.18;
# totalFactorContribution = 1.04*0.02 + 0.18*0.01 = 0.0226; idiosyncratic = 0.03-0.0226 = 0.0074 -- matches exactly.

$ curl -s -X POST http://127.0.0.1:8085/latency/benchmark -d '{
  "roundTripTimeSamplesInMillisecondsByVenue": {
    "VENUE-A": [1,2,3,4,5,6,7,8,9,10],
    "VENUE-B": [10,20,30,40,50,60,70,80,90,100]
  },
  "bucketCount": 5
}'
{"VENUE-A": {"sampleCount": 10, "minimumMilliseconds": 1.0, "maximumMilliseconds": 10.0, "p50Milliseconds": 6.0, "p95Milliseconds": 10.0, "p99Milliseconds": 10.0, "histogramBuckets": [...]}, "VENUE-B": {"sampleCount": 10, "minimumMilliseconds": 10.0, "maximumMilliseconds": 100.0, "p50Milliseconds": 60.0, "p95Milliseconds": 100.0, "p99Milliseconds": 100.0, "histogramBuckets": [...]}}
# hand-worked nearest-rank: n=10, p50 index=floor(0.5*10)=5 -> sortedSamples[5] = 6.0 (VENUE-A) / 60.0 (VENUE-B) -- matches exactly.

$ curl -s -X POST http://127.0.0.1:8085/options/corporate-action/split-adjustment -d '{
  "symbol": "DEMO-EQ", "strikePrice": 100.0, "quantity": 10.0,
  "exerciseStyle": "AMERICAN", "contractSide": "CALL", "splitRatio": 2.0
}'
{"splitRatio": 2.0, "originalStrikePrice": 100.0, "originalQuantity": 10.0, "adjustedStrikePrice": 50.0, "adjustedQuantity": 20.0, "notionalExposureIsPreserved": true}
# hand-worked: newStrike = 100/2 = 50.0; newQuantity = 10*2 = 20.0 -- matches exactly.

$ curl -s -X POST http://127.0.0.1:8085/options/corporate-action/early-exercise-risk -d '{
  "symbol": "DEMO-EQ", "strikePrice": 100.0, "quantity": 1.0,
  "exerciseStyle": "AMERICAN", "contractSide": "CALL",
  "underlyingSpotPrice": 110.0, "callMarketPrice": 10.5, "dividendAmount": 1.0
}'
{"intrinsicValue": 10.0, "callTimeValue": 0.5, "dividendAmount": 1.0, "isFlaggedForEarlyExerciseRisk": true, "reason": "dividend (1.0000) exceeds remaining call time value (0.5000) — the textbook necessary condition for early exercise to be worth considering is met"}
# hand-worked: intrinsic = max(110-100,0) = 10.0; timeValue = 10.5-10.0 = 0.5; dividend 1.0 > 0.5 -> flagged -- matches exactly.
```

**Honest oms-gateway latency-timing reachability transcript** (the task
asked for real timing against oms-gateway's real order-submission
endpoint if reachable, caller-supplied samples otherwise — this is
exactly what happened):

```
$ .venv/bin/python3 -c "
from quantengine.latencyBenchmarkDashboard import measureRoundTripTimeSamplesOverHttp, buildLatencyHistogramAndPercentiles, checkHttpEndpointIsReachable
print('oms-gateway reachable:', checkHttpEndpointIsReachable('http://127.0.0.1:8081/health', timeoutSeconds=1.0))
samples = measureRoundTripTimeSamplesOverHttp('http://127.0.0.1:8085/health', sampleCount=20)
print('samples(ms):', [round(s,3) for s in samples])
result = buildLatencyHistogramAndPercentiles(samples, bucketCount=5)
print('p50', result.p50Milliseconds, 'p95', result.p95Milliseconds, 'p99', result.p99Milliseconds, 'max', result.maximumMilliseconds)
"
oms-gateway reachable: False
samples(ms): [1.01, 0.629, 0.491, 0.458, 0.635, 0.504, 0.435, 0.468, 0.521, 0.597, 0.559, 0.362, 0.444, 0.36, 0.362, 0.349, 0.359, 0.371, 0.349, 0.361]
p50 0.4583330010063946 p95 1.010209001833573 p99 1.010209001833573 max 1.010209001833573
```

oms-gateway was NOT reachable on `127.0.0.1:8081` (it isn't running —
this environment has no Docker, and oms-gateway also depends on
`services/matching-engine` being up) — `checkHttpEndpointIsReachable`
correctly reported `False`, and the transcript above falls back to
REAL, genuinely `time.perf_counter()`-timed HTTP round trips against
quant-engine's OWN live `/health` endpoint instead, proving the timing
function performs actual network I/O rather than fabricating numbers.

What's not built yet (see FEATURES.md §6, §7, §22):
- All 16 FEATURES.md §22 items are now built across pass 1 and pass 2 —
  see both "§22" sections above for exactly what's real vs. illustrative
  in each, and which of the pass-2 items are HTTP-wired vs.
  pytest-verified-only (and why).
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
- **Cointegration critical values** (`cointegrationTester.py`) are
  standard asymptotic Dickey-Fuller values, NOT MacKinnon (1991)
  Engle-Granger-adjusted values — measurably anti-conservative (more
  likely to falsely conclude cointegration). The ADF test statistic
  itself is real and correctly computed from a real from-scratch OLS
  implementation; only the significance table is a documented
  simplification. See the module docstring.
- **Market-impact cost model** (`realisticBacktestCostModel.py`) uses a
  real square-root functional form, but `impactCoefficient` is an
  ILLUSTRATIVE, caller-supplied constant — not calibrated against any
  real market microstructure data.
- **Monte Carlo VaR** (`monteCarloEngine.simulateMonteCarloPortfolioValueAtRisk`)
  simulates a single AGGREGATE portfolio GBM process (one blended
  expected-return/volatility pair), not a real multi-asset simulation
  with per-position correlated processes.
- **Synthetic position margin** (`syntheticPositionBuilder.py`) is a
  pass-through caller-supplied number for reporting only — real
  Reg-T/portfolio-margin/broker-specific margin calculation is not
  implemented anywhere in this module.
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
- **The ESG dataset is fabricated, full stop** (`esgScoringEngine.py`).
  `ILLUSTRATIVE_ESG_DATASET_BY_SYMBOL` covers exactly six illustrative
  demo symbols with hand-picked E/S/G sub-scores — it is NOT sourced from
  MSCI, Sustainalytics, ISS ESG, Refinitiv, Bloomberg ESG, or any other
  real ESG data vendor, and a real vendor integration is not attempted
  anywhere in this module. The 40/30/30 pillar weighting is a documented,
  fixed methodology choice, not derived from or claiming to match any
  specific real rating agency's proprietary methodology. The composite
  formula and the screening/ranking logic ARE real and correctly
  implemented — see the module docstring for the exact split of what's
  real versus illustrative.
- **Walk-forward optimizer overfitting thresholds** (`walkForwardOptimizer.py`)
  — the 0.5 Walk-Forward Efficiency threshold (Pardo) and the 10-
  observations-per-parameter rule of thumb are real, standard, NAMED
  quant-literature/statistics conventions used as-is, not empirically
  recalibrated for any specific real strategy or market.
- **Factor risk model exposures** (`factorRiskModel.py`) are ENTIRELY
  illustrative/caller-supplied per-holding numbers — no real Fama-
  French/Barra factor-loading regression against real market data exists
  anywhere in this module or this repo.
- **Latency benchmark oms-gateway timing** (`latencyBenchmarkDashboard.py`)
  — real HTTP timing infrastructure exists and was proven against
  quant-engine's own live server, but oms-gateway itself was not running
  during this pass's verification (no Docker; also depends on
  `services/matching-engine`), so no genuine oms-gateway order-
  submission latency numbers exist yet — only the capability to collect
  them once oms-gateway is reachable.
- **Cross-asset macro dashboard data** (`crossAssetMacroDashboard.py`) is
  ENTIRELY illustrative/fixture named time series — no yields/DXY/crude/
  VIX/equity-index feed is ingested anywhere in this repo. Also not
  wired to HTTP this pass (a time-budget omission, not a technical
  blocker — see the "§22 pass 2" section).
- **Options corporate-action early-exercise check** (`optionsCorporateActionHandler.py`)
  implements only the textbook NECESSARY condition (dividend exceeds
  remaining time value) for American-call early exercise to be worth
  considering — it explicitly does NOT weigh transaction costs, tax
  treatment, or opportunity cost of capital, and does not implement the
  separate (out-of-scope) American-put early-exercise driver.
- **HMM regime detector** (`regimeDetectionHmm.py`) — Baum-Welch is a
  local-optimum hill-climbing algorithm; the quantile-based
  initialization is real and deterministic but not a multi-restart
  global search, so the fitted regimes can depend on that initialization
  for pathological input series. Not wired to HTTP this pass (see the
  "§22 pass 2" section for why).

## FEATURES.md §16 ("AI, Data & Research") — all seven items

All seven items in this section are now real, tested modules with real
HTTP endpoints. As with every other illustrative dataset in this service
(ESG scores, factor exposures), every dataset called out below is
synthetic/fabricated fixture data — no real market data, no real filings,
no internet access. The MATH and LOGIC applied to that data is real.

1. **Stock/fund screener** (`stockScreenerFilterBuilder.py`) — a real
   compound filter-expression engine (arbitrarily nested AND/OR groups
   over `<`, `<=`, `>`, `>=`, `==`, `!=`, `in`, `not_in` comparisons) run
   against an illustrative six-symbol instrument universe. Fundamental
   fields (P/E, market cap, dividend yield, sector) are hand-fabricated;
   technical fields (50-day SMA, 14-day Wilder RSI) are REAL formulas
   computed from each symbol's deterministic (non-random) synthetic price
   series. Saved screens persist via `SavedScreenStore` — IN-MEMORY by
   default (screens don't survive a process restart unless a
   `persistenceFilePath` is supplied, in which case every save/delete is
   also written to a JSON file and reloaded on the next construction —
   see that class's docstring; this is intentionally the simplest
   persistence that satisfies "saved screens survive," not a real
   database).
2. **AI research copilot** (`researchCopilotRetrievalAugmentedGeneration.py`)
   — a real, working RAG pipeline: real paragraph chunking, real TF-IDF
   vectorization (term frequency × inverse document frequency over the
   corpus's actual token statistics — a real, correct retrieval
   algorithm, explicitly NOT a trained neural embedding model, since none
   is available offline), real cosine-similarity top-k retrieval, and
   real extractive (template-based, not generative-LLM) response
   composition that quotes ONLY the retrieved chunks with exact
   `(documentId, chunkIndex)` citations. Runs over a small, hand-authored
   SYNTHETIC corpus of four "SIM-"-prefixed filing/earnings-call/annual-
   report excerpts (`ILLUSTRATIVE_DOCUMENT_CORPUS`) — no real company or
   filing. Every response carries a fixed, unconditional non-advisory
   disclaimer. Retrieval correctness is pytest-verified directly: a
   "revenue growth" query retrieves the chunk discussing revenue growth,
   not an unrelated litigation chunk.
3. **Portfolio health check / diversification analysis**
   (`portfolioHealthCheckDiversificationAnalyzer.py`) — real
   Herfindahl-Hirschman Index concentration math over both individual
   positions and sector-aggregated weights, severity-classified using the
   real DOJ/FTC Merger Guidelines HHI bands (repurposed here for
   portfolio concentration), an optional real factor-exposure summary
   (reusing `factorRiskModel.computePortfolioFactorExposures` when every
   holding supplies exposures), and plain-language nudge strings
   genuinely interpolated from the actual computed numbers — pytest
   directly proves a concentrated portfolio and a diversified portfolio
   produce different nudge text AND different severities.
4. **Tax-loss harvesting** (`taxLossHarvestingAdvisor.py`) — real per-lot
   unrealized-loss identification, the real IRS 61-day wash-sale window
   check (30 days before through 30 days after the proposed sale date,
   inclusive), and the real offset waterfall (harvested losses offset
   realized gains first, then up to the real $3,000/year ordinary-income
   offset cap, with the remainder correctly carried forward). Pytest
   covers both a wash-sale case that MUST be excluded (repurchase 10 days
   after the sale) and one that must NOT be (repurchase 45 days after,
   outside the window). Explicitly NOT tax advice — see the module
   docstring for exact scope limits (no short/long-term rate handling, no
   state tax rules, "substantially identical" approximated as same
   symbol).
5. **Alternative data feeds** (`alternativeDataFeedAggregator.py`) — real
   pooled-count sentiment aggregation (reusing §7's
   `illustrativeSentimentTradingHook.calculateIllustrativeLexiconSentimentScore`
   per snippet, then pooling positive/negative counts across all snippets
   before scoring) over a small illustrative news/social snippet fixture
   set, plus real z-score outlier detection (population-stddev
   convention, same as `riskStatistics.py`) over illustrative multi-period
   filing metrics (e.g. debt-to-equity). Genuinely wired into the §7 NLP
   module: `buildIntegratedAlternativeDataSignal` concatenates the
   aggregated snippets and feeds that text directly into §7's real
   `generateOrderHookSuggestion` (same kill-switch-off-by-default safety
   gate) — pytest proves the aggregated data actually flows through into
   a real BUY/SELL/HOLD suggestion, not a disconnected parallel pipeline.
6. **Factor-based P&L attribution** (`factorBasedPnlAttributionEngine.py`)
   — the real, classic Brinson-Hood-Beebower three-term decomposition
   (allocation + selection + interaction effects per sector) plus a
   separate currency-overlay effect for multi-currency portfolios. Tested
   against a fully hand-worked two-sector example with pre-computed
   expected values for every effect, AND a property-style check that the
   allocation+selection+interaction identity exactly reconstructs total
   active return for arbitrary multi-sector input.
7. **Custom index construction + backtesting**
   (`customIndexConstructionBacktester.py`) — real rules-based index
   construction (top-N by market cap, EQUAL_WEIGHT or CAP_WEIGHT, a
   configurable rebalance frequency) over an illustrative six-symbol
   constituent universe with deterministic synthetic price/shares-
   outstanding history, producing a real computed index level path
   (buy-and-hold share counts between rebalances, weights drift with
   price exactly like a real index fund). Backtested performance stats
   (CAGR via the standard compound formula; annualized Sharpe ratio and
   max drawdown REUSING `riskStatistics.py`) are computed directly from
   that actual constructed price path, not fabricated. "Licensable to
   other institutions" (the FEATURES.md item's product framing) has no
   technical licensing/entitlement system here — that's a commercial
   concern outside this module's scope; what's built is the real
   construction-and-backtest engine such a product would sit on top of.

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

# ESG screening — illustrative fixture dataset, real scoring/screening math.
# DEMO-EQ has E=70,S=60,G=80 -> composite = 0.4*70+0.3*60+0.3*80 = 70.0 exactly.
curl -X POST http://127.0.0.1:8085/esg/screen -d '{
  "candidateSymbols": ["DEMO-EQ", "SIM-AAPL", "SIM-MSFT", "SIM-JPM", "SIM-THERMAL-COAL-CO", "SIM-TOBACCO-CO"],
  "minimumCompositeEsgScore": 60.0,
  "excludedControversialSectorFlags": ["TOBACCO", "THERMAL_COAL"]
}'

# FEATURES.md §22 pass 1 — portfolio Greeks aggregation, IV Rank/Percentile,
# delta-hedging threshold check, Kelly-criterion sizer (see "§22 pass 1"
# section above for hand-worked values and live transcripts).
curl -X POST http://127.0.0.1:8085/portfolio/greeks -d '{
  "positions": [
    {"identifier": "A", "quantity": 10.0, "delta": 0.5, "gamma": 0.02, "vegaPerOnePercentVolatilityChange": 0.15, "thetaPerCalendarDay": -0.05},
    {"identifier": "B", "quantity": -5.0, "delta": 0.3, "gamma": 0.01, "vegaPerOnePercentVolatilityChange": 0.10, "thetaPerCalendarDay": -0.02}
  ]
}'
curl -X POST http://127.0.0.1:8085/volatility/iv-rank -d '{
  "currentImpliedVolatility": 0.19,
  "historicalImpliedVolatilitySeries": [0.10, 0.12, 0.14, 0.16, 0.18, 0.20, 0.50]
}'
curl -X POST http://127.0.0.1:8085/portfolio/delta-hedge-check -d '{
  "positions": [
    {"identifier": "A", "quantity": 10.0, "delta": 0.5, "gamma": 0.02, "vegaPerOnePercentVolatilityChange": 0.15, "thetaPerCalendarDay": -0.05},
    {"identifier": "B", "quantity": -5.0, "delta": 0.3, "gamma": 0.01, "vegaPerOnePercentVolatilityChange": 0.10, "thetaPerCalendarDay": -0.02}
  ],
  "deltaThreshold": 2.0
}'
curl -X POST http://127.0.0.1:8085/sizing/kelly-criterion -d '{"winProbability": 0.55, "winLossPayoutRatio": 1.5}'

# FEATURES.md §22 pass 2 — factor risk model, latency benchmark dashboard,
# options corporate-action handling (see "§22 pass 2" section above for
# hand-worked values and live transcripts).
curl -X POST http://127.0.0.1:8085/portfolio/factor-risk -d '{
  "holdings": [
    {"symbol": "DEMO-EQ", "portfolioWeight": 0.6, "factorExposuresByName": {"marketBeta": 1.2, "size": 0.5}},
    {"symbol": "SIM-AAPL", "portfolioWeight": 0.4, "factorExposuresByName": {"marketBeta": 0.8, "size": -0.3}}
  ],
  "factorReturnsByName": {"marketBeta": 0.02, "size": 0.01},
  "actualOrExpectedPortfolioReturn": 0.03
}'
curl -X POST http://127.0.0.1:8085/latency/benchmark -d '{
  "roundTripTimeSamplesInMillisecondsByVenue": {"VENUE-A": [1,2,3,4,5,6,7,8,9,10]},
  "bucketCount": 5
}'
curl -X POST http://127.0.0.1:8085/options/corporate-action/split-adjustment -d '{
  "symbol": "DEMO-EQ", "strikePrice": 100.0, "quantity": 10.0,
  "exerciseStyle": "AMERICAN", "contractSide": "CALL", "splitRatio": 2.0
}'
curl -X POST http://127.0.0.1:8085/options/corporate-action/early-exercise-risk -d '{
  "symbol": "DEMO-EQ", "strikePrice": 100.0, "quantity": 1.0,
  "exerciseStyle": "AMERICAN", "contractSide": "CALL",
  "underlyingSpotPrice": 110.0, "callMarketPrice": 10.5, "dividendAmount": 1.0
}'

# FEATURES.md §16 ("AI, Data & Research") — screener, research copilot,
# portfolio health check, tax-loss harvesting, alternative data, P&L
# attribution, and custom index construction (see the "§16" section above
# for exactly what's real vs. illustrative in each).
curl -X POST http://127.0.0.1:8085/screener/run -d '{
  "filterExpression": {"logic": "AND", "conditions": [
    {"field": "priceToEarningsRatio", "operator": "<", "value": 20.0},
    {"field": "dividendYieldPercent", "operator": ">=", "value": 4.0}
  ]}
}'
curl -X POST http://127.0.0.1:8085/screener/saved-screens/save -d '{
  "screenName": "cheap-div", "filterExpression": {"field": "dividendYieldPercent", "operator": ">=", "value": 4.0}
}'
curl -X POST http://127.0.0.1:8085/screener/saved-screens/list -d '{}'
curl -X POST http://127.0.0.1:8085/research/copilot/ask -d '{"query": "revenue growth", "topK": 2}'
curl -X POST http://127.0.0.1:8085/portfolio/health-check -d '{
  "holdings": [
    {"symbol": "MEGA", "sector": "TECH", "portfolioWeight": 0.85},
    {"symbol": "SMALL1", "sector": "FINANCIALS", "portfolioWeight": 0.10},
    {"symbol": "SMALL2", "sector": "FINANCIALS", "portfolioWeight": 0.05}
  ]
}'
curl -X POST http://127.0.0.1:8085/tax/loss-harvesting-plan -d '{
  "lots": [
    {"lotId": "L1", "symbol": "SIM-AAPL", "quantity": 10, "buyPricePerShare": 200.0, "buyDate": "2024-01-01", "currentPricePerShare": 100.0},
    {"lotId": "L2", "symbol": "SIM-AAPL", "quantity": 5, "buyPricePerShare": 105.0, "buyDate": "2026-06-20", "currentPricePerShare": 110.0}
  ],
  "realizedGainsYtd": 2000.0,
  "proposedSaleDate": "2026-06-15"
}'
curl -X POST http://127.0.0.1:8085/alternative-data/sentiment-signal -d '{
  "snippets": [{"source": "NEWSWIRE-A", "text": "strong revenue growth and record profit"}],
  "killSwitchEnabled": true
}'
curl -X POST http://127.0.0.1:8085/alternative-data/filing-anomaly -d '{
  "metrics": {"debtToEquityRatio": {"historicalValues": [0.4, 0.42, 0.39, 0.41, 0.40, 0.43], "currentValue": 0.95}}
}'
curl -X POST http://127.0.0.1:8085/pnl-attribution/brinson -d '{
  "sectors": [
    {"sectorName": "TECH", "portfolioWeight": 0.6, "portfolioLocalReturn": 0.10, "benchmarkWeight": 0.5, "benchmarkLocalReturn": 0.08},
    {"sectorName": "FINANCIALS", "portfolioWeight": 0.4, "portfolioLocalReturn": 0.03, "benchmarkWeight": 0.5, "benchmarkLocalReturn": 0.05}
  ]
}'
curl -X POST http://127.0.0.1:8085/index/construct-and-backtest -d '{
  "constituentCount": 3, "weightingScheme": "CAP_WEIGHT", "rebalanceFrequencyInBars": 20
}'
```
