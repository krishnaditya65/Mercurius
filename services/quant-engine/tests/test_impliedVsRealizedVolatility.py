from __future__ import annotations

import math

import pytest

from quantengine.blackScholesOptionPricer import (
    BlackScholesInputParameters,
    calculateBlackScholesCallOptionPrice,
)
from quantengine.impliedVsRealizedVolatility import (
    calculateAnnualizedRealizedVolatilityFromPriceSeries,
    calculateLogReturnsFromPriceSeries,
    compareImpliedVersusRealizedVolatility,
)


def testLogReturnsHandWorkedTwoPriceSeries():
    # ln(105/100) = ln(1.05), ln(100/105) = ln(0.952380952...)
    logReturns = calculateLogReturnsFromPriceSeries([100.0, 105.0, 100.0])
    assert logReturns == pytest.approx([math.log(1.05), math.log(100.0 / 105.0)])


def testLogReturnsRequiresAtLeastTwoPrices():
    with pytest.raises(ValueError):
        calculateLogReturnsFromPriceSeries([100.0])


def testLogReturnsRejectsNonPositivePrice():
    with pytest.raises(ValueError):
        calculateLogReturnsFromPriceSeries([100.0, -5.0])


def testHandWorkedAnnualizedRealizedVolatility():
    # prices = [100, 105, 100]
    # r1 = ln(1.05) = 0.04879016417...
    # r2 = ln(100/105) = -0.04879016417... (exactly -r1, since 100->105->100)
    # mean = 0.0
    # sumOfSquaredDeviations = r1^2 + r2^2 = 2 * r1^2
    # sampleVariance (N-1=1 denominator) = 2 * r1^2 / 1 = 2 * r1^2
    # stddev = r1 * sqrt(2)
    # annualized (periodsPerYear=252) = r1 * sqrt(2) * sqrt(252)
    r1 = math.log(1.05)
    expectedRealizedVolatility = r1 * math.sqrt(2.0) * math.sqrt(252.0)
    actual = calculateAnnualizedRealizedVolatilityFromPriceSeries([100.0, 105.0, 100.0], periodsPerYear=252.0)
    assert actual == pytest.approx(expectedRealizedVolatility)


def testRealizedVolatilityRequiresAtLeastThreePrices():
    with pytest.raises(ValueError):
        calculateAnnualizedRealizedVolatilityFromPriceSeries([100.0, 105.0], periodsPerYear=252.0)


def testRealizedVolatilityRaisesOnZeroVarianceLogReturns():
    # Constant price series -> every log return is exactly 0.0 -> zero variance.
    with pytest.raises(ValueError):
        calculateAnnualizedRealizedVolatilityFromPriceSeries([100.0, 100.0, 100.0, 100.0], periodsPerYear=252.0)


def testRealizedVolatilityScalesWithSquareRootOfPeriodsPerYear():
    prices = [100.0, 102.0, 99.0, 103.0, 101.0]
    dailyScaled = calculateAnnualizedRealizedVolatilityFromPriceSeries(prices, periodsPerYear=252.0)
    weeklyScaled = calculateAnnualizedRealizedVolatilityFromPriceSeries(prices, periodsPerYear=52.0)
    assert dailyScaled / weeklyScaled == pytest.approx(math.sqrt(252.0 / 52.0))


def testCompareImpliedVersusRealizedVolatilityEndToEnd():
    # Build a market price using a KNOWN volatility (0.30) via the real
    # pricer, feed that back through the real IV solver, and pair it with
    # a real realized-volatility computation from a synthetic price series.
    trueVolatility = 0.30
    inputParametersWithKnownVol = BlackScholesInputParameters(
        underlyingSpotPrice=100.0,
        optionStrikePrice=100.0,
        annualizedRiskFreeInterestRate=0.05,
        annualizedVolatility=trueVolatility,
        timeToExpiryInYears=1.0,
    )
    marketPrice = calculateBlackScholesCallOptionPrice(inputParametersWithKnownVol)

    inputParametersWithoutVolatility = BlackScholesInputParameters(
        underlyingSpotPrice=100.0,
        optionStrikePrice=100.0,
        annualizedRiskFreeInterestRate=0.05,
        annualizedVolatility=1.0,  # ignored by the solver, present only to satisfy the dataclass shape
        timeToExpiryInYears=1.0,
    )

    priceSeries = [100.0, 102.0, 99.0, 103.0, 101.0, 104.0, 100.0]

    result = compareImpliedVersusRealizedVolatility(
        marketPrice, inputParametersWithoutVolatility, True, priceSeries, periodsPerYear=252.0
    )

    assert result.impliedVolatility == pytest.approx(trueVolatility, abs=1e-6)
    expectedRealized = calculateAnnualizedRealizedVolatilityFromPriceSeries(priceSeries, periodsPerYear=252.0)
    assert result.realizedVolatility == pytest.approx(expectedRealized)
    assert result.impliedMinusRealizedSpread == pytest.approx(result.impliedVolatility - result.realizedVolatility)
    assert result.impliedOverRealizedRatio == pytest.approx(result.impliedVolatility / result.realizedVolatility)


def testSpreadIsPositiveWhenImpliedExceedsRealized():
    result = ImpliedVersusRealizedVolatilityResultBuilder(implied=0.40, realized=0.20)
    assert result.impliedMinusRealizedSpread == pytest.approx(0.20)
    assert result.impliedOverRealizedRatio == pytest.approx(2.0)


def testSpreadIsNegativeWhenRealizedExceedsImplied():
    result = ImpliedVersusRealizedVolatilityResultBuilder(implied=0.15, realized=0.25)
    assert result.impliedMinusRealizedSpread == pytest.approx(-0.10)
    assert result.impliedOverRealizedRatio == pytest.approx(0.6)


def ImpliedVersusRealizedVolatilityResultBuilder(implied: float, realized: float):
    from quantengine.impliedVsRealizedVolatility import ImpliedVersusRealizedVolatilityResult

    return ImpliedVersusRealizedVolatilityResult(
        impliedVolatility=implied,
        realizedVolatility=realized,
        impliedMinusRealizedSpread=implied - realized,
        impliedOverRealizedRatio=implied / realized,
    )
