"""GARCH(1,1) volatility forecasting, feeding an "Expected Intraday Range"
widget. See ARCHITECTURE.md §6 ("Quant Math Engine") and FEATURES.md §6 —
"GARCH(1,1) overnight batch job -> 'Expected Intraday Range' widget".

The GARCH(1,1) conditional-variance recursion is the real, textbook
Bollerslev (1986) formula:

    sigma^2_t = omega + alpha * epsilon^2_{t-1} + beta * sigma^2_{t-1}

where `epsilon_t` is the (demeaned) periodic return at time t and
`sigma^2_t` is the conditional variance forecast for period t, formed
using information available through t-1. This module implements that
recursion exactly (`calculateGarchConditionalVarianceSeries` below), and
a fitting routine that estimates (omega, alpha, beta) from a historical
return series.

FITTING METHOD — read this before trusting the numbers in production:
full maximum-likelihood GARCH estimation normally runs a general-purpose
nonlinear optimizer (e.g. BFGS/L-BFGS-B in `scipy.optimize`) over the
full 3-parameter space. This module deliberately does NOT pull in scipy
(quant-engine has stayed stdlib-only, per riskStatistics.py's and
blackScholesOptionPricer.py's own convention). Instead it uses a real,
documented SIMPLIFIED method:

1. "Variance targeting" (a genuine, widely-used practitioner
   simplification, not invented here): for any candidate (alpha, beta)
   with alpha + beta < 1, the unconditional (long-run) variance implied
   by the model is `omega / (1 - alpha - beta)`. Fixing that equal to
   the sample variance of the historical returns lets `omega` be solved
   in closed form from (alpha, beta): `omega = sampleVariance * (1 -
   alpha - beta)`. This removes one free parameter from the search.
2. A coarse grid search over the remaining (alpha, beta) pair,
   maximizing the Gaussian conditional log-likelihood

       LL = -0.5 * sum_t [ log(sigma^2_t) + epsilon^2_t / sigma^2_t ]

   This is a real (if computationally crude) quasi-maximum-likelihood
   fit — every candidate's likelihood is genuinely computed via the
   recursion above, not faked — but it is NOT a production-grade
   continuous optimizer and will not find the global MLE to high
   precision the way `scipy.optimize.minimize` would. Treat fitted
   parameters as a reasonable starting point, not a regulatory-grade
   estimate. See `fitGarchOneOneParametersByGridSearchQuasiMaximumLikelihood`.

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

import math
from dataclasses import dataclass


@dataclass(frozen=True)
class GarchOneOneParameters:
    """(omega, alpha, beta) for the GARCH(1,1) recursion. Stationarity
    requires `alphaArchCoefficient + betaGarchCoefficient < 1` (otherwise
    the unconditional variance is infinite/undefined) — enforced here.
    """

    omega: float
    alphaArchCoefficient: float
    betaGarchCoefficient: float

    def __post_init__(self) -> None:
        if self.omega <= 0:
            raise ValueError("omega must be strictly positive")
        if self.alphaArchCoefficient < 0 or self.betaGarchCoefficient < 0:
            raise ValueError("alphaArchCoefficient and betaGarchCoefficient must both be non-negative")
        if self.alphaArchCoefficient + self.betaGarchCoefficient >= 1:
            raise ValueError(
                "alphaArchCoefficient + betaGarchCoefficient must be < 1 for a stationary "
                "(finite unconditional variance) GARCH(1,1) process"
            )

    def calculateUnconditionalVariance(self) -> float:
        """The model-implied long-run variance: omega / (1 - alpha - beta)."""
        return self.omega / (1.0 - self.alphaArchCoefficient - self.betaGarchCoefficient)


def calculateMeanOfSeries(values: list[float]) -> float:
    if not values:
        raise ValueError("values must contain at least one observation")
    return sum(values) / len(values)


def calculateSampleVarianceOfSeries(values: list[float]) -> float:
    """Population variance (divides by N), matching
    riskStatistics.calculatePopulationStandardDeviationOfReturnSeries's
    convention elsewhere in this module — used both as the GARCH seed
    variance and as the variance-targeting anchor for `omega`.
    """
    if not values:
        raise ValueError("values must contain at least one observation")
    meanValue = calculateMeanOfSeries(values)
    return sum((oneValue - meanValue) ** 2 for oneValue in values) / len(values)


def calculateGarchConditionalVarianceSeries(
    demeanedReturns: list[float],
    parameters: GarchOneOneParameters,
    initialConditionalVariance: float,
) -> list[float]:
    """Runs the GARCH(1,1) recursion forward over `demeanedReturns`.

    Returns a list of length `len(demeanedReturns) + 1`:
    - index 0 is `initialConditionalVariance` (the seed variance, i.e.
      the conditional variance forecast BEFORE any of `demeanedReturns`
      has been observed — in practice the sample variance of a prior
      warm-up window).
    - index i (for i = 1..len(demeanedReturns)) is
      `omega + alpha * demeanedReturns[i-1]**2 + beta * series[i-1]`.

    The LAST element of the returned series is therefore exactly the
    one-step-ahead forecast for the period immediately AFTER the last
    observed return in `demeanedReturns` — i.e. it doubles as the
    "next-period volatility forecast" without a separate function, since
    the recursion step to produce it is identical to every other step.

    Raises `ValueError` if `demeanedReturns` is empty or
    `initialConditionalVariance` is non-positive.
    """
    if not demeanedReturns:
        raise ValueError("demeanedReturns must contain at least one observation")
    if initialConditionalVariance <= 0:
        raise ValueError("initialConditionalVariance must be strictly positive")

    conditionalVarianceSeries = [initialConditionalVariance]
    for oneDemeanedReturn in demeanedReturns:
        previousConditionalVariance = conditionalVarianceSeries[-1]
        nextConditionalVariance = (
            parameters.omega
            + parameters.alphaArchCoefficient * (oneDemeanedReturn**2)
            + parameters.betaGarchCoefficient * previousConditionalVariance
        )
        conditionalVarianceSeries.append(nextConditionalVariance)

    return conditionalVarianceSeries


def calculateGaussianConditionalLogLikelihood(
    demeanedReturns: list[float], conditionalVarianceSeries: list[float]
) -> float:
    """Gaussian conditional log-likelihood (up to the additive constant
    -0.5*n*log(2*pi), which is dropped since it's the same for every
    candidate parameter set and doesn't affect which one maximizes it):

        LL = -0.5 * sum_t [ log(sigma^2_t) + epsilon^2_t / sigma^2_t ]

    `conditionalVarianceSeries` must be the recursion OUTPUT (length
    len(demeanedReturns) + 1) — this function uses
    `conditionalVarianceSeries[1:]`, i.e. the in-sample forecasts that
    line up one-to-one with `demeanedReturns`, and skips the seed value
    at index 0.
    """
    if len(conditionalVarianceSeries) != len(demeanedReturns) + 1:
        raise ValueError(
            "conditionalVarianceSeries must have length len(demeanedReturns) + 1 "
            "(the recursion's own output shape, including the seed at index 0)"
        )
    inSampleVariances = conditionalVarianceSeries[1:]
    logLikelihood = 0.0
    for oneReturn, oneVariance in zip(demeanedReturns, inSampleVariances):
        logLikelihood += math.log(oneVariance) + (oneReturn**2) / oneVariance
    return -0.5 * logLikelihood


@dataclass(frozen=True)
class GarchFitResult:
    parameters: GarchOneOneParameters
    logLikelihoodAtFittedParameters: float
    sampleMeanReturn: float
    conditionalVarianceSeries: list[float]


def fitGarchOneOneParametersByGridSearchQuasiMaximumLikelihood(
    periodicReturns: list[float],
    alphaSearchGrid: list[float] | None = None,
    betaSearchGrid: list[float] | None = None,
) -> GarchFitResult:
    """Fits GARCH(1,1) parameters to `periodicReturns` via the coarse
    variance-targeting grid search documented in this module's docstring.
    NOT a production-grade continuous MLE optimizer — see that docstring
    for exactly what tradeoff this makes and why.

    `periodicReturns` are demeaned internally (the sample mean is
    subtracted before running the recursion) — GARCH models the
    conditional variance of the MEAN-ZERO innovation, not the raw return
    level.

    Default grids: alpha in {0.02, 0.04, ..., 0.28} (14 points), beta in
    {0.50, 0.55, ..., 0.95} (10 points), skipping any (alpha, beta) pair
    with alpha + beta >= 0.999 (non-stationary / numerically unstable).
    140 candidate evaluations total — deliberately small enough to run in
    well under a second on a batch/overnight schedule, per FEATURES.md's
    framing of this as an "overnight batch job".
    """
    if len(periodicReturns) < 3:
        raise ValueError("periodicReturns must contain at least 3 observations to fit GARCH(1,1)")

    sampleMeanReturn = calculateMeanOfSeries(periodicReturns)
    demeanedReturns = [oneReturn - sampleMeanReturn for oneReturn in periodicReturns]
    sampleVariance = calculateSampleVarianceOfSeries(periodicReturns)
    if sampleVariance <= 0:
        raise ValueError("cannot fit GARCH(1,1): periodicReturns has zero variance")

    if alphaSearchGrid is None:
        alphaSearchGrid = [0.02 * stepIndex for stepIndex in range(1, 15)]
    if betaSearchGrid is None:
        betaSearchGrid = [0.50 + 0.05 * stepIndex for stepIndex in range(0, 10)]

    bestLogLikelihood = float("-inf")
    bestParameters: GarchOneOneParameters | None = None
    bestConditionalVarianceSeries: list[float] | None = None

    for alphaCandidate in alphaSearchGrid:
        for betaCandidate in betaSearchGrid:
            if alphaCandidate + betaCandidate >= 0.999:
                continue
            omegaCandidate = sampleVariance * (1.0 - alphaCandidate - betaCandidate)
            candidateParameters = GarchOneOneParameters(
                omega=omegaCandidate,
                alphaArchCoefficient=alphaCandidate,
                betaGarchCoefficient=betaCandidate,
            )
            conditionalVarianceSeries = calculateGarchConditionalVarianceSeries(
                demeanedReturns, candidateParameters, initialConditionalVariance=sampleVariance
            )
            candidateLogLikelihood = calculateGaussianConditionalLogLikelihood(
                demeanedReturns, conditionalVarianceSeries
            )
            if candidateLogLikelihood > bestLogLikelihood:
                bestLogLikelihood = candidateLogLikelihood
                bestParameters = candidateParameters
                bestConditionalVarianceSeries = conditionalVarianceSeries

    if bestParameters is None or bestConditionalVarianceSeries is None:
        raise ValueError("no stationary (alpha, beta) candidate found in the supplied search grids")

    return GarchFitResult(
        parameters=bestParameters,
        logLikelihoodAtFittedParameters=bestLogLikelihood,
        sampleMeanReturn=sampleMeanReturn,
        conditionalVarianceSeries=bestConditionalVarianceSeries,
    )


@dataclass(frozen=True)
class ExpectedIntradayRangeResult:
    forecastNextPeriodVolatility: float
    currentPrice: float
    zScoreMultiple: float
    expectedRangeLowerBound: float
    expectedRangeUpperBound: float


def calculateExpectedIntradayRangeFromForecastVariance(
    forecastNextPeriodVariance: float,
    currentPrice: float,
    zScoreMultiple: float = 1.645,
) -> ExpectedIntradayRangeResult:
    """The "Expected Intraday Range" widget: converts a GARCH one-step-
    ahead variance forecast into a +/- price band around `currentPrice`.

        forecastVolatility = sqrt(forecastNextPeriodVariance)
        range = currentPrice +/- zScoreMultiple * forecastVolatility * currentPrice

    `zScoreMultiple` defaults to 1.645 (the ~90% two-sided normal
    z-score) — an illustrative, caller-overridable default, not a
    calibrated risk parameter. This treats `forecastNextPeriodVariance`
    as the variance of a simple percentage return over the same period
    the GARCH model was fit on (e.g. a daily return series fit ->
    a one-day-ahead expected range).
    """
    if forecastNextPeriodVariance <= 0:
        raise ValueError("forecastNextPeriodVariance must be strictly positive")
    if currentPrice <= 0:
        raise ValueError("currentPrice must be strictly positive")
    if zScoreMultiple <= 0:
        raise ValueError("zScoreMultiple must be strictly positive")

    forecastVolatility = math.sqrt(forecastNextPeriodVariance)
    halfRangeInPrice = zScoreMultiple * forecastVolatility * currentPrice

    return ExpectedIntradayRangeResult(
        forecastNextPeriodVolatility=forecastVolatility,
        currentPrice=currentPrice,
        zScoreMultiple=zScoreMultiple,
        expectedRangeLowerBound=currentPrice - halfRangeInPrice,
        expectedRangeUpperBound=currentPrice + halfRangeInPrice,
    )
