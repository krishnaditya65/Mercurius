"""Implied vs. realized volatility comparison. See FEATURES.md §22
("Deep Quant & Algorithmic Trading Internals").

Two real, independent numbers this module brings together:

1. IMPLIED volatility (IV) — NOT recomputed here; this module reuses
   `blackScholesOptionPricer.solveImpliedVolatilityFromMarketPrice` (the
   EXISTING Newton-Raphson IV solver) exactly as-is, taking an observed
   market option price and backing out the volatility the market is
   pricing in.
2. REALIZED (a.k.a. historical) volatility — computed FOR REAL here, from
   an underlying's historical price series, as the ANNUALIZED standard
   deviation of daily (or whatever periodicity the caller supplies)
   LOG returns:

       logReturn_t = ln(price_t / price_{t-1})
       realizedVolatility = stddev(logReturns) * sqrt(periodsPerYear)

   Uses the SAMPLE standard deviation (divides by N-1, not N) for the
   log-return series, which is the standard convention for realized-
   volatility estimation (as opposed to `riskStatistics.py`'s population
   convention for Sharpe/Sortino — these are different statistics with
   different standard conventions in practice, and this module documents
   which one it uses rather than silently picking one).

The IV-RV spread (`impliedVolatility - realizedVolatility`) and ratio
(`impliedVolatility / realizedVolatility`) are simple, real derived
quantities: a positive spread / ratio above 1.0 means the market is
pricing in MORE future volatility than the underlying has recently
realized (often read as options being "rich"), and vice versa. This is a
real, common volatility-trading signal input — this module does NOT
generate a trading signal or recommendation from it, only the two
underlying numbers and their comparison.

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

import math
from dataclasses import dataclass

from quantengine.blackScholesOptionPricer import (
    BlackScholesInputParameters,
    solveImpliedVolatilityFromMarketPrice,
)


def calculateLogReturnsFromPriceSeries(priceSeries: list[float]) -> list[float]:
    """`ln(price_t / price_{t-1})` for each consecutive pair in
    `priceSeries`. Raises `ValueError` if `priceSeries` has fewer than 2
    observations (no return is computable from a single price) or
    contains any non-positive price (log is undefined).
    """
    if len(priceSeries) < 2:
        raise ValueError("priceSeries must contain at least 2 observations to compute a log return")
    if any(price <= 0.0 for price in priceSeries):
        raise ValueError("priceSeries must contain only strictly positive prices")

    return [math.log(priceSeries[i] / priceSeries[i - 1]) for i in range(1, len(priceSeries))]


def calculateAnnualizedRealizedVolatilityFromPriceSeries(
    priceSeries: list[float], periodsPerYear: float
) -> float:
    """Annualized realized volatility: the SAMPLE standard deviation
    (N-1 denominator) of log returns derived from `priceSeries`, scaled
    by `sqrt(periodsPerYear)` (252 for daily trading-day prices, 12 for
    monthly, 52 for weekly, etc. — fully caller-configurable, matching
    this codebase's convention elsewhere of not hardcoding an
    annualization factor).

    Raises `ValueError` if fewer than 3 prices are supplied (need at
    least 2 log returns for a sample standard deviation to be defined —
    with the N-1 denominator, 1 return gives division by zero) or if the
    log returns have zero variance.
    """
    if len(priceSeries) < 3:
        raise ValueError(
            "priceSeries must contain at least 3 observations (>= 2 log returns) "
            "to compute a sample standard deviation with an N-1 denominator"
        )

    logReturns = calculateLogReturnsFromPriceSeries(priceSeries)
    meanLogReturn = sum(logReturns) / len(logReturns)
    sumOfSquaredDeviations = sum((r - meanLogReturn) ** 2 for r in logReturns)
    sampleVariance = sumOfSquaredDeviations / (len(logReturns) - 1)
    if sampleVariance == 0.0:
        raise ValueError("cannot compute realized volatility: log returns have zero variance")

    return math.sqrt(sampleVariance) * math.sqrt(periodsPerYear)


@dataclass(frozen=True)
class ImpliedVersusRealizedVolatilityResult:
    impliedVolatility: float
    realizedVolatility: float
    impliedMinusRealizedSpread: float
    impliedOverRealizedRatio: float


def compareImpliedVersusRealizedVolatility(
    observedMarketOptionPrice: float,
    blackScholesInputParametersWithoutVolatility: BlackScholesInputParameters,
    isCallOptionNotPut: bool,
    underlyingHistoricalPriceSeries: list[float],
    periodsPerYear: float = 252.0,
) -> ImpliedVersusRealizedVolatilityResult:
    """Solves implied volatility from `observedMarketOptionPrice` (via the
    EXISTING `solveImpliedVolatilityFromMarketPrice` — not reimplemented
    here), computes realized volatility from
    `underlyingHistoricalPriceSeries` (real annualized-stddev-of-log-
    returns math above), and returns both plus their spread and ratio.

    Raises whatever `solveImpliedVolatilityFromMarketPrice` or
    `calculateAnnualizedRealizedVolatilityFromPriceSeries` raises (e.g.
    non-convergence, a too-short price series, or zero-variance
    returns) — this function does no additional validation of its own
    beyond composing the two.
    """
    impliedVolatility = solveImpliedVolatilityFromMarketPrice(
        observedMarketOptionPrice, blackScholesInputParametersWithoutVolatility, isCallOptionNotPut
    )
    realizedVolatility = calculateAnnualizedRealizedVolatilityFromPriceSeries(
        underlyingHistoricalPriceSeries, periodsPerYear
    )

    return ImpliedVersusRealizedVolatilityResult(
        impliedVolatility=impliedVolatility,
        realizedVolatility=realizedVolatility,
        impliedMinusRealizedSpread=impliedVolatility - realizedVolatility,
        impliedOverRealizedRatio=impliedVolatility / realizedVolatility,
    )
