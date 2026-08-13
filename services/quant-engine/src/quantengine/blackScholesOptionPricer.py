"""Black-Scholes European option pricer, Greeks, and an implied-volatility
solver. See ARCHITECTURE.md §6 ("Quant Math Engine") and §8's tech-stack
note: this Python module is for RESEARCH/CORRECTNESS. Any path that needs
to run on a real-time latency budget (e.g. the arbitrage scanner across
thousands of contracts per second, FEATURES.md §6) must be ported to Rust
and exposed via PyO3/gRPC once the math here is validated — do not put
this module directly on a hot path.

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

import math
from dataclasses import dataclass


def calculateStandardNormalCumulativeDistribution(inputValue: float) -> float:
    """N(x) — the standard normal CDF, via the error function."""
    return 0.5 * (1.0 + math.erf(inputValue / math.sqrt(2.0)))


def calculateStandardNormalProbabilityDensity(inputValue: float) -> float:
    """N'(x) — the standard normal PDF, used by every Greek below."""
    return math.exp(-0.5 * inputValue * inputValue) / math.sqrt(2.0 * math.pi)


@dataclass(frozen=True)
class BlackScholesInputParameters:
    underlyingSpotPrice: float
    optionStrikePrice: float
    annualizedRiskFreeInterestRate: float
    annualizedVolatility: float
    timeToExpiryInYears: float


def calculateD1AndD2(inputParameters: BlackScholesInputParameters) -> tuple[float, float]:
    """The two intermediate terms shared by every Black-Scholes formula
    below. See ARCHITECTURE.md §6 for the underlying math reference.
    """
    spotPrice = inputParameters.underlyingSpotPrice
    strikePrice = inputParameters.optionStrikePrice
    riskFreeRate = inputParameters.annualizedRiskFreeInterestRate
    volatility = inputParameters.annualizedVolatility
    timeToExpiry = inputParameters.timeToExpiryInYears

    if timeToExpiry <= 0 or volatility <= 0:
        raise ValueError(
            "timeToExpiryInYears and annualizedVolatility must both be strictly positive "
            "— an expired or zero-vol contract isn't priceable with this formula"
        )

    d1 = (
        math.log(spotPrice / strikePrice)
        + (riskFreeRate + 0.5 * volatility * volatility) * timeToExpiry
    ) / (volatility * math.sqrt(timeToExpiry))
    d2 = d1 - volatility * math.sqrt(timeToExpiry)

    return d1, d2


def calculateBlackScholesCallOptionPrice(inputParameters: BlackScholesInputParameters) -> float:
    d1, d2 = calculateD1AndD2(inputParameters)
    spotPrice = inputParameters.underlyingSpotPrice
    strikePrice = inputParameters.optionStrikePrice
    riskFreeRate = inputParameters.annualizedRiskFreeInterestRate
    timeToExpiry = inputParameters.timeToExpiryInYears

    discountedStrikePrice = strikePrice * math.exp(-riskFreeRate * timeToExpiry)
    return spotPrice * calculateStandardNormalCumulativeDistribution(
        d1
    ) - discountedStrikePrice * calculateStandardNormalCumulativeDistribution(d2)


def calculateBlackScholesPutOptionPrice(inputParameters: BlackScholesInputParameters) -> float:
    d1, d2 = calculateD1AndD2(inputParameters)
    spotPrice = inputParameters.underlyingSpotPrice
    strikePrice = inputParameters.optionStrikePrice
    riskFreeRate = inputParameters.annualizedRiskFreeInterestRate
    timeToExpiry = inputParameters.timeToExpiryInYears

    discountedStrikePrice = strikePrice * math.exp(-riskFreeRate * timeToExpiry)
    return discountedStrikePrice * calculateStandardNormalCumulativeDistribution(
        -d2
    ) - spotPrice * calculateStandardNormalCumulativeDistribution(-d1)


@dataclass(frozen=True)
class OptionGreeksResult:
    delta: float
    gamma: float
    vegaPerOnePercentVolatilityChange: float
    thetaPerCalendarDay: float


def calculateOptionGreeks(
    inputParameters: BlackScholesInputParameters, isCallOptionNotPut: bool
) -> OptionGreeksResult:
    """Portfolio-level aggregation of these (net delta/gamma/theta/vega
    across all positions) is the FEATURES.md §22 differentiator — this
    function is the per-contract building block that aggregation sums
    over, it does not do the aggregation itself.
    """
    d1, d2 = calculateD1AndD2(inputParameters)
    spotPrice = inputParameters.underlyingSpotPrice
    strikePrice = inputParameters.optionStrikePrice
    riskFreeRate = inputParameters.annualizedRiskFreeInterestRate
    volatility = inputParameters.annualizedVolatility
    timeToExpiry = inputParameters.timeToExpiryInYears

    standardNormalPdfAtD1 = calculateStandardNormalProbabilityDensity(d1)
    discountedStrikePrice = strikePrice * math.exp(-riskFreeRate * timeToExpiry)

    if isCallOptionNotPut:
        delta = calculateStandardNormalCumulativeDistribution(d1)
        thetaPerYear = (
            -(spotPrice * standardNormalPdfAtD1 * volatility) / (2 * math.sqrt(timeToExpiry))
            - riskFreeRate * discountedStrikePrice * calculateStandardNormalCumulativeDistribution(d2)
        )
    else:
        delta = calculateStandardNormalCumulativeDistribution(d1) - 1.0
        thetaPerYear = (
            -(spotPrice * standardNormalPdfAtD1 * volatility) / (2 * math.sqrt(timeToExpiry))
            + riskFreeRate * discountedStrikePrice * calculateStandardNormalCumulativeDistribution(-d2)
        )

    gamma = standardNormalPdfAtD1 / (spotPrice * volatility * math.sqrt(timeToExpiry))
    vegaPerFullUnitVolatilityChange = spotPrice * standardNormalPdfAtD1 * math.sqrt(timeToExpiry)

    return OptionGreeksResult(
        delta=delta,
        gamma=gamma,
        vegaPerOnePercentVolatilityChange=vegaPerFullUnitVolatilityChange / 100.0,
        thetaPerCalendarDay=thetaPerYear / 365.0,
    )


def solveImpliedVolatilityFromMarketPrice(
    observedMarketPrice: float,
    inputParametersWithoutVolatility: BlackScholesInputParameters,
    isCallOptionNotPut: bool,
    initialVolatilityGuess: float = 0.3,
    maximumIterationCount: int = 100,
    convergenceTolerance: float = 1e-8,
) -> float:
    """Newton-Raphson solve for implied volatility, using vega as the
    derivative. This is what feeds the arbitrage scanner (FEATURES.md §6)
    and the IV Rank/Percentile differentiator (FEATURES.md §22) — both
    depend on being able to invert the pricer against a live market quote.

    TODO(real build): add a bisection fallback for the low-vega region
    (deep ITM/OTM contracts near expiry) where Newton-Raphson can diverge;
    this skeleton raises instead of silently returning a bad estimate.
    """
    currentVolatilityEstimate = initialVolatilityGuess

    for _ in range(maximumIterationCount):
        candidateParameters = BlackScholesInputParameters(
            underlyingSpotPrice=inputParametersWithoutVolatility.underlyingSpotPrice,
            optionStrikePrice=inputParametersWithoutVolatility.optionStrikePrice,
            annualizedRiskFreeInterestRate=inputParametersWithoutVolatility.annualizedRiskFreeInterestRate,
            annualizedVolatility=currentVolatilityEstimate,
            timeToExpiryInYears=inputParametersWithoutVolatility.timeToExpiryInYears,
        )

        theoreticalPriceAtCurrentVolatility = (
            calculateBlackScholesCallOptionPrice(candidateParameters)
            if isCallOptionNotPut
            else calculateBlackScholesPutOptionPrice(candidateParameters)
        )

        priceDifferenceFromMarket = theoreticalPriceAtCurrentVolatility - observedMarketPrice
        if abs(priceDifferenceFromMarket) < convergenceTolerance:
            return currentVolatilityEstimate

        greeksAtCurrentVolatility = calculateOptionGreeks(candidateParameters, isCallOptionNotPut)
        vegaPerFullUnitVolatilityChange = greeksAtCurrentVolatility.vegaPerOnePercentVolatilityChange * 100.0

        if abs(vegaPerFullUnitVolatilityChange) < 1e-12:
            raise ValueError(
                "vega too small to continue Newton-Raphson — contract is likely deep ITM/OTM "
                "near expiry; needs the bisection fallback noted in the TODO above"
            )

        currentVolatilityEstimate -= priceDifferenceFromMarket / vegaPerFullUnitVolatilityChange

    raise ValueError(
        f"implied volatility solve did not converge within {maximumIterationCount} iterations"
    )
