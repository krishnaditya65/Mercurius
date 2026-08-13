"""Value-at-Risk (VaR) and portfolio stress-testing for the margin/risk
engine. See ARCHITECTURE.md §6 ("Quant Math Engine") and FEATURES.md
§6 — "Value-at-Risk (VaR) and stress-testing for margin/risk engine".

Two real, independent VaR methods, both standard textbook formulas:

1. HISTORICAL VaR (`calculateHistoricalValueAtRisk`): sorts the observed
   return series and reads off the loss at the requested percentile —
   makes no distributional assumption, just empirical order statistics.
2. PARAMETRIC (variance-covariance) VaR
   (`calculateParametricValueAtRisk`): assumes returns are normally
   distributed and uses `mean - z * stddev`, where `z` is the standard
   normal quantile for the requested confidence level. The quantile is
   computed by inverting `calculateStandardNormalCumulativeDistribution`
   from `blackScholesOptionPricer.py` via bisection — reusing that
   module's real normal-CDF implementation rather than adding a second,
   approximate inverse-CDF formula.

Both express VaR as a POSITIVE number representing the magnitude of the
expected loss (i.e. "VaR of 0.03 at 95% confidence" means "there's a 5%
chance of losing more than 3% of the portfolio's value over one period"),
which is the common risk-desk convention.

`calculatePortfolioStressTestPnLImpact` applies a set of NAMED, ILLUSTRATIVE
stress scenarios (e.g. "-20% equity shock") to a set of portfolio
positions and reports the resulting P&L impact per scenario — these
scenario definitions are example inputs a caller supplies, not a
calibrated/regulatory scenario set (no CCAR/Basel FRTB stress library is
implemented or referenced here).

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

from dataclasses import dataclass

from quantengine.blackScholesOptionPricer import calculateStandardNormalCumulativeDistribution


def calculateMeanOfSeries(values: list[float]) -> float:
    if not values:
        raise ValueError("values must contain at least one observation")
    return sum(values) / len(values)


def calculatePopulationStandardDeviationOfSeries(values: list[float]) -> float:
    if not values:
        raise ValueError("values must contain at least one observation")
    meanValue = calculateMeanOfSeries(values)
    return (sum((v - meanValue) ** 2 for v in values) / len(values)) ** 0.5


def calculateHistoricalValueAtRisk(
    periodicReturns: list[float], confidenceLevel: float
) -> float:
    """Historical VaR: sorts `periodicReturns` ascending and reads off the
    return at the `(1 - confidenceLevel)` empirical percentile using the
    NEAREST-RANK method:

        percentileIndex = floor((1 - confidenceLevel) * n)
        VaR = -sortedReturns[percentileIndex]

    e.g. with 10 observations at 90% confidence, `percentileIndex =
    floor(0.10 * 10) = 1` (0-based) — the second-worst observed return
    becomes the VaR estimate. This is the standard simple/nearest-rank
    historical VaR convention (no interpolation between order
    statistics) — a common practitioner simplification, not the only
    valid convention (some desks interpolate between adjacent order
    statistics instead).

    Returns a POSITIVE number (the loss magnitude). Raises `ValueError`
    if `periodicReturns` is empty or `confidenceLevel` is outside
    (0, 1).
    """
    if not periodicReturns:
        raise ValueError("periodicReturns must contain at least one observation")
    if not (0.0 < confidenceLevel < 1.0):
        raise ValueError("confidenceLevel must be strictly between 0 and 1")

    sortedReturns = sorted(periodicReturns)
    # A tiny epsilon guards against floating-point representation error in
    # `(1 - confidenceLevel) * n` landing just under an exact integer
    # (e.g. confidenceLevel=0.90 with n=10 should give exactly 1.0, but
    # binary floating point can compute 0.9999999999999998) — without it,
    # `int(...)` would silently floor to one index below the intended one.
    percentileIndex = int((1.0 - confidenceLevel) * len(sortedReturns) + 1e-9)
    percentileIndex = min(percentileIndex, len(sortedReturns) - 1)
    return -sortedReturns[percentileIndex]


def calculateInverseStandardNormalCumulativeDistribution(
    targetProbability: float,
    searchLowerBound: float = -10.0,
    searchUpperBound: float = 10.0,
    convergenceTolerance: float = 1e-12,
    maximumIterationCount: int = 200,
) -> float:
    """Inverts `calculateStandardNormalCumulativeDistribution` via plain
    bisection over `[searchLowerBound, searchUpperBound]` — reuses
    blackScholesOptionPricer's own real normal-CDF implementation rather
    than adding a second, separately-approximated inverse-CDF formula.
    The standard normal CDF is monotonically increasing everywhere, so
    bisection converges reliably. +/-10 standard deviations comfortably
    brackets any (0, 1) probability that will ever be requested for a
    risk quantile in practice.
    """
    if not (0.0 < targetProbability < 1.0):
        raise ValueError("targetProbability must be strictly between 0 and 1")

    lowerBound = searchLowerBound
    upperBound = searchUpperBound
    for _ in range(maximumIterationCount):
        midpoint = (lowerBound + upperBound) / 2.0
        if (upperBound - lowerBound) < convergenceTolerance:
            break
        if calculateStandardNormalCumulativeDistribution(midpoint) < targetProbability:
            lowerBound = midpoint
        else:
            upperBound = midpoint
    return (lowerBound + upperBound) / 2.0


def calculateParametricValueAtRisk(
    periodicReturns: list[float], confidenceLevel: float
) -> float:
    """Parametric (variance-covariance) VaR, assuming normally
    distributed returns:

        VaR = z(confidenceLevel) * stddev(periodicReturns) - mean(periodicReturns)

    where `z(confidenceLevel)` is the standard normal quantile for
    `confidenceLevel` (e.g. z(0.95) ~= 1.6449, z(0.99) ~= 2.3263),
    computed via `calculateInverseStandardNormalCumulativeDistribution`
    above. Uses the population standard deviation, matching
    `riskStatistics.py`'s convention elsewhere in this codebase.

    Returns a POSITIVE number (the loss magnitude). Raises `ValueError`
    if `periodicReturns` is empty, has zero variance, or
    `confidenceLevel` is outside (0, 1).
    """
    if not periodicReturns:
        raise ValueError("periodicReturns must contain at least one observation")
    standardDeviation = calculatePopulationStandardDeviationOfSeries(periodicReturns)
    if standardDeviation == 0.0:
        raise ValueError("cannot compute parametric VaR: periodicReturns has zero variance")

    meanReturn = calculateMeanOfSeries(periodicReturns)
    zScore = calculateInverseStandardNormalCumulativeDistribution(confidenceLevel)
    return zScore * standardDeviation - meanReturn


@dataclass(frozen=True)
class PortfolioPosition:
    symbol: str
    quantity: float
    currentPrice: float

    def calculateMarketValue(self) -> float:
        return self.quantity * self.currentPrice


@dataclass(frozen=True)
class StressScenario:
    """One ILLUSTRATIVE named stress scenario: a percentage price shock
    applied per-symbol. `shockPercentageBySymbol` values are fractional
    (e.g. -0.20 means "-20%"). A symbol with no entry in
    `shockPercentageBySymbol` is treated as unshocked (0% impact) in that
    scenario — lets a caller define a scenario that only shocks a subset
    of the book (e.g. an equity-only shock that leaves rates/FX
    positions untouched).
    """

    scenarioName: str
    shockPercentageBySymbol: dict[str, float]


def calculatePortfolioStressTestPnLImpact(
    positions: list[PortfolioPosition], scenarios: list[StressScenario]
) -> dict[str, float]:
    """For each scenario, sums `position.quantity * position.currentPrice
    * shockPercentage` across every position — the resulting portfolio
    P&L impact (positive = gain, negative = loss) if that scenario's
    shocks were applied simultaneously and instantaneously to mark-to-
    market every position. A purely linear (no gamma/convexity) repricing
    — real options/convex positions would need their own repriced value
    under the shocked underlying, not this linear approximation; that's
    a documented simplification, not attempted here.

    Returns a dict keyed by `scenarioName`. Raises `ValueError` on an
    empty `positions` or `scenarios` list, or on duplicate scenario
    names (ambiguous result key).
    """
    if not positions:
        raise ValueError("positions must contain at least one position")
    if not scenarios:
        raise ValueError("scenarios must contain at least one scenario")

    scenarioNames = [scenario.scenarioName for scenario in scenarios]
    if len(scenarioNames) != len(set(scenarioNames)):
        raise ValueError("scenarios must have distinct scenarioName values")

    pnlImpactByScenarioName: dict[str, float] = {}
    for scenario in scenarios:
        totalPnlImpact = 0.0
        for position in positions:
            shockPercentage = scenario.shockPercentageBySymbol.get(position.symbol, 0.0)
            totalPnlImpact += position.calculateMarketValue() * shockPercentage
        pnlImpactByScenarioName[scenario.scenarioName] = totalPnlImpact

    return pnlImpactByScenarioName
