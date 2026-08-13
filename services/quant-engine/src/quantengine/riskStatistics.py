"""Portfolio/strategy risk statistics: Sharpe ratio, Sortino ratio, and
maximum drawdown, computed from a series of periodic returns. See
ARCHITECTURE.md §6 ("Quant Math Engine") and FEATURES.md §6 — this is the
"Sharpe Ratio / Sortino Ratio / max drawdown per portfolio & per strategy"
item.

This module is deliberately independent of blackScholesOptionPricer.py —
it operates on a caller-supplied return series (e.g. daily/periodic P&L
returns for a portfolio or a single strategy's backtest equity curve), not
on option pricing inputs. It doesn't care where the returns came from.

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

import math
from dataclasses import dataclass


def calculateMeanOfReturnSeries(periodicReturns: list[float]) -> float:
    """Arithmetic mean of a list of periodic returns. Raises `ValueError`
    on an empty series — there is no meaningful mean of zero observations.
    """
    if not periodicReturns:
        raise ValueError("periodicReturns must contain at least one observation")
    return sum(periodicReturns) / len(periodicReturns)


def calculatePopulationStandardDeviationOfReturnSeries(periodicReturns: list[float]) -> float:
    """Population standard deviation (divides by N, not N-1) of a list of
    periodic returns. This module uses the population convention
    throughout for both Sharpe's denominator and Sortino's downside
    deviation, which is the most common convention in practitioner Sharpe
    ratio calculations (as opposed to the sample/N-1 convention used in
    some academic treatments) — see the hand-worked test case in
    `tests/test_riskStatistics.py` for the exact arithmetic this implies.
    """
    if not periodicReturns:
        raise ValueError("periodicReturns must contain at least one observation")
    meanReturn = calculateMeanOfReturnSeries(periodicReturns)
    sumOfSquaredDeviations = sum((oneReturn - meanReturn) ** 2 for oneReturn in periodicReturns)
    return math.sqrt(sumOfSquaredDeviations / len(periodicReturns))


def calculateDownsideDeviationOfReturnSeries(
    periodicReturns: list[float], minimumAcceptableReturnPerPeriod: float
) -> float:
    """The Sortino ratio's denominator: like a standard deviation, but
    computed only over returns that fall BELOW
    `minimumAcceptableReturnPerPeriod` (typically the periodic risk-free
    rate, sometimes a separate target return), and squared deviations from
    that threshold rather than from the mean. Returns at or above the
    threshold contribute zero to the sum — upside volatility is not
    penalized, which is the entire point of Sortino over Sharpe.

    Divides by the FULL count of periods (not just the count of
    below-threshold periods), which is the standard Sortino convention:
    a period with no downside deviation still "counts" toward diluting
    the average.
    """
    if not periodicReturns:
        raise ValueError("periodicReturns must contain at least one observation")
    sumOfSquaredDownsideDeviations = sum(
        min(0.0, oneReturn - minimumAcceptableReturnPerPeriod) ** 2 for oneReturn in periodicReturns
    )
    return math.sqrt(sumOfSquaredDownsideDeviations / len(periodicReturns))


def calculateAnnualizedSharpeRatio(
    periodicReturns: list[float],
    periodicRiskFreeRate: float,
    periodsPerYear: float,
) -> float:
    """Annualized Sharpe ratio:

        Sharpe = (mean(periodicReturns) - periodicRiskFreeRate)
                 / stddev(periodicReturns)
                 * sqrt(periodsPerYear)

    `periodicRiskFreeRate` must already be expressed in the SAME period
    as `periodicReturns` (e.g. a daily risk-free rate for daily returns)
    — this function does no rate conversion of its own. `periodsPerYear`
    is the standard annualization factor (252 for daily trading-day
    returns, 12 for monthly, 52 for weekly, etc.) and is left fully
    configurable rather than hardcoded, since quant-engine has no fixed
    return-series cadence yet.

    Raises `ValueError` if the return series has zero variance (a flat/
    constant series), since the ratio is undefined (division by zero)
    in that case — this mirrors calculateD1AndD2's pattern in
    blackScholesOptionPricer.py of raising rather than returning NaN/inf.
    """
    standardDeviationOfReturns = calculatePopulationStandardDeviationOfReturnSeries(periodicReturns)
    if standardDeviationOfReturns == 0.0:
        raise ValueError(
            "cannot compute Sharpe ratio: periodicReturns has zero variance "
            "(a perfectly flat return series has an undefined risk-adjusted ratio)"
        )
    meanExcessReturn = calculateMeanOfReturnSeries(periodicReturns) - periodicRiskFreeRate
    return (meanExcessReturn / standardDeviationOfReturns) * math.sqrt(periodsPerYear)


def calculateAnnualizedSortinoRatio(
    periodicReturns: list[float],
    periodicRiskFreeRate: float,
    periodsPerYear: float,
    minimumAcceptableReturnPerPeriod: float | None = None,
) -> float:
    """Annualized Sortino ratio — identical numerator convention to
    Sharpe (mean excess return over the periodic risk-free rate), but the
    denominator is `calculateDownsideDeviationOfReturnSeries` instead of
    the full-sample standard deviation:

        Sortino = (mean(periodicReturns) - periodicRiskFreeRate)
                  / downsideDeviation(periodicReturns, minimumAcceptableReturnPerPeriod)
                  * sqrt(periodsPerYear)

    `minimumAcceptableReturnPerPeriod` defaults to `periodicRiskFreeRate`
    when not supplied — the common case where the downside threshold and
    the excess-return baseline are the same rate — but can be set
    independently (some practitioners target a fixed hurdle rate that
    differs from the risk-free rate).

    Raises `ValueError` if downside deviation is zero (no period fell
    below the threshold, or all sub-threshold periods were exactly at the
    threshold) — the ratio is undefined in that case.
    """
    effectiveMinimumAcceptableReturn = (
        periodicRiskFreeRate
        if minimumAcceptableReturnPerPeriod is None
        else minimumAcceptableReturnPerPeriod
    )
    downsideDeviation = calculateDownsideDeviationOfReturnSeries(
        periodicReturns, effectiveMinimumAcceptableReturn
    )
    if downsideDeviation == 0.0:
        raise ValueError(
            "cannot compute Sortino ratio: downside deviation is zero "
            "(no period fell below minimumAcceptableReturnPerPeriod)"
        )
    meanExcessReturn = calculateMeanOfReturnSeries(periodicReturns) - periodicRiskFreeRate
    return (meanExcessReturn / downsideDeviation) * math.sqrt(periodsPerYear)


@dataclass(frozen=True)
class MaximumDrawdownResult:
    maximumDrawdownFraction: float
    peakEquityValue: float
    troughEquityValue: float
    peakIndex: int
    troughIndex: int


def buildCumulativeEquityCurveFromReturns(
    periodicReturns: list[float], startingEquityValue: float = 1.0
) -> list[float]:
    """Compounds a periodic-return series into a cumulative equity curve,
    starting from `startingEquityValue` (default 1.0, i.e. a unit-equity
    curve). Equity curve has length len(periodicReturns) + 1: index 0 is
    the starting value, index i+1 reflects having compounded through
    periodicReturns[0..i].
    """
    if not periodicReturns:
        raise ValueError("periodicReturns must contain at least one observation")
    equityCurve = [startingEquityValue]
    for oneReturn in periodicReturns:
        equityCurve.append(equityCurve[-1] * (1.0 + oneReturn))
    return equityCurve


def calculateMaximumDrawdownFromEquityCurve(equityCurveValues: list[float]) -> MaximumDrawdownResult:
    """Largest peak-to-trough decline (as a fraction of the peak) across a
    cumulative equity curve — the standard max-drawdown definition:

        drawdown(t) = (runningPeak(t) - equity(t)) / runningPeak(t)
        maxDrawdown = max over all t of drawdown(t)

    where `runningPeak(t)` is the highest equity value seen at or before
    index t. Returns 0.0 (with peak == trough == the curve's own values)
    if the curve is non-decreasing throughout (no drawdown ever occurred).

    Takes the equity curve directly (not a return series) so it composes
    with `buildCumulativeEquityCurveFromReturns` above but can also be fed
    a real portfolio equity curve directly, which is the more common
    real-world input for this specific statistic.
    """
    if not equityCurveValues:
        raise ValueError("equityCurveValues must contain at least one observation")

    runningPeakValue = equityCurveValues[0]
    runningPeakIndex = 0
    largestDrawdownFraction = 0.0
    largestDrawdownPeakValue = equityCurveValues[0]
    largestDrawdownTroughValue = equityCurveValues[0]
    largestDrawdownPeakIndex = 0
    largestDrawdownTroughIndex = 0

    for currentIndex, currentEquityValue in enumerate(equityCurveValues):
        if currentEquityValue > runningPeakValue:
            runningPeakValue = currentEquityValue
            runningPeakIndex = currentIndex

        currentDrawdownFraction = (runningPeakValue - currentEquityValue) / runningPeakValue
        if currentDrawdownFraction > largestDrawdownFraction:
            largestDrawdownFraction = currentDrawdownFraction
            largestDrawdownPeakValue = runningPeakValue
            largestDrawdownTroughValue = currentEquityValue
            largestDrawdownPeakIndex = runningPeakIndex
            largestDrawdownTroughIndex = currentIndex

    return MaximumDrawdownResult(
        maximumDrawdownFraction=largestDrawdownFraction,
        peakEquityValue=largestDrawdownPeakValue,
        troughEquityValue=largestDrawdownTroughValue,
        peakIndex=largestDrawdownPeakIndex,
        troughIndex=largestDrawdownTroughIndex,
    )


def calculateMaximumDrawdownFromReturns(
    periodicReturns: list[float], startingEquityValue: float = 1.0
) -> MaximumDrawdownResult:
    """Convenience wrapper: builds the cumulative equity curve from a
    periodic-return series, then computes max drawdown over it.
    """
    equityCurve = buildCumulativeEquityCurveFromReturns(periodicReturns, startingEquityValue)
    return calculateMaximumDrawdownFromEquityCurve(equityCurve)
