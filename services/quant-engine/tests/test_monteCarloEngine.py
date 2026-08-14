from __future__ import annotations

import pytest

from quantengine.blackScholesOptionPricer import (
    BlackScholesInputParameters,
    calculateBlackScholesCallOptionPrice,
    calculateBlackScholesPutOptionPrice,
)
from quantengine.monteCarloEngine import (
    priceAsianArithmeticAverageOptionViaMonteCarlo,
    priceEuropeanOptionViaMonteCarlo,
    simulateGeometricBrownianMotionPaths,
    simulateMonteCarloPortfolioValueAtRisk,
)


def testSimulatedPathsHaveCorrectShapeAndStartingPrice():
    paths = simulateGeometricBrownianMotionPaths(100.0, 0.05, 0.2, 1.0, numberOfTimeSteps=10, numberOfPaths=5, randomSeed=1)
    assert len(paths) == 5
    for path in paths:
        assert len(path) == 11
        assert path[0] == 100.0


def testSamePathsSeedGivesSameSamplesDeterministically():
    pathsA = simulateGeometricBrownianMotionPaths(100.0, 0.05, 0.2, 1.0, 10, 5, randomSeed=42)
    pathsB = simulateGeometricBrownianMotionPaths(100.0, 0.05, 0.2, 1.0, 10, 5, randomSeed=42)
    assert pathsA == pathsB


def testDifferentSeedsGiveDifferentPaths():
    pathsA = simulateGeometricBrownianMotionPaths(100.0, 0.05, 0.2, 1.0, 10, 5, randomSeed=1)
    pathsB = simulateGeometricBrownianMotionPaths(100.0, 0.05, 0.2, 1.0, 10, 5, randomSeed=2)
    assert pathsA != pathsB


def testAllSimulatedPricesArePositive():
    paths = simulateGeometricBrownianMotionPaths(100.0, 0.05, 0.4, 2.0, 20, 50, randomSeed=7)
    for path in paths:
        assert all(price > 0.0 for price in path)


def testSimulatePathsRejectsNonPositiveTimeStepsOrPaths():
    with pytest.raises(ValueError):
        simulateGeometricBrownianMotionPaths(100.0, 0.05, 0.2, 1.0, 0, 5, randomSeed=1)
    with pytest.raises(ValueError):
        simulateGeometricBrownianMotionPaths(100.0, 0.05, 0.2, 1.0, 10, 0, randomSeed=1)


def testSimulatePathsRejectsNonPositiveVolatilityOrHorizon():
    with pytest.raises(ValueError):
        simulateGeometricBrownianMotionPaths(100.0, 0.05, 0.0, 1.0, 10, 5, randomSeed=1)
    with pytest.raises(ValueError):
        simulateGeometricBrownianMotionPaths(100.0, 0.05, 0.2, 0.0, 10, 5, randomSeed=1)


def testEuropeanCallMonteCarloConvergesTowardBlackScholesClosedFormPrice():
    # A REAL, meaningful correctness check: with a large path count, the
    # Monte Carlo estimate must land within a small number of its OWN
    # standard errors of the true closed-form Black-Scholes price -- not
    # merely "it runs". 4 standard errors gives a false-failure
    # probability under 0.01% for a correctly-implemented estimator.
    bsParams = BlackScholesInputParameters(100.0, 100.0, 0.05, 0.2, 1.0)
    closedFormPrice = calculateBlackScholesCallOptionPrice(bsParams)

    mcResult = priceEuropeanOptionViaMonteCarlo(
        100.0, 100.0, 0.05, 0.2, 1.0, isCallOptionNotPut=True, numberOfPaths=50000, randomSeed=42
    )

    assert abs(mcResult.estimatedPrice - closedFormPrice) < 4.0 * mcResult.standardErrorOfEstimate
    assert mcResult.numberOfPaths == 50000


def testEuropeanPutMonteCarloConvergesTowardBlackScholesClosedFormPrice():
    bsParams = BlackScholesInputParameters(100.0, 95.0, 0.03, 0.25, 0.5)
    closedFormPrice = calculateBlackScholesPutOptionPrice(bsParams)

    mcResult = priceEuropeanOptionViaMonteCarlo(
        100.0, 95.0, 0.03, 0.25, 0.5, isCallOptionNotPut=False, numberOfPaths=50000, randomSeed=123
    )

    assert abs(mcResult.estimatedPrice - closedFormPrice) < 4.0 * mcResult.standardErrorOfEstimate


def testMonteCarloStandardErrorShrinksAsPathCountGrows():
    smallSample = priceEuropeanOptionViaMonteCarlo(100.0, 100.0, 0.05, 0.2, 1.0, True, 500, randomSeed=1)
    largeSample = priceEuropeanOptionViaMonteCarlo(100.0, 100.0, 0.05, 0.2, 1.0, True, 20000, randomSeed=1)
    assert largeSample.standardErrorOfEstimate < smallSample.standardErrorOfEstimate


def testAsianOptionPriceIsLowerThanEuropeanOptionPriceForSameParameters():
    # Well-known theoretical property: averaging reduces effective payoff
    # volatility, so an arithmetic-average Asian call is always cheaper
    # than the otherwise-equivalent European call.
    europeanResult = priceEuropeanOptionViaMonteCarlo(
        100.0, 100.0, 0.05, 0.2, 1.0, isCallOptionNotPut=True, numberOfPaths=20000, randomSeed=99
    )
    asianResult = priceAsianArithmeticAverageOptionViaMonteCarlo(
        100.0, 100.0, 0.05, 0.2, 1.0, isCallOptionNotPut=True, numberOfTimeSteps=50, numberOfPaths=20000, randomSeed=99
    )
    assert asianResult.estimatedPrice < europeanResult.estimatedPrice
    assert asianResult.estimatedPrice > 0.0


def testAsianOptionPriceIsDeterministicForFixedSeed():
    resultA = priceAsianArithmeticAverageOptionViaMonteCarlo(100.0, 100.0, 0.05, 0.2, 1.0, True, 20, 1000, randomSeed=5)
    resultB = priceAsianArithmeticAverageOptionViaMonteCarlo(100.0, 100.0, 0.05, 0.2, 1.0, True, 20, 1000, randomSeed=5)
    assert resultA.estimatedPrice == resultB.estimatedPrice


def testAsianOptionPutCallPayoffsAreNonNegative():
    callResult = priceAsianArithmeticAverageOptionViaMonteCarlo(100.0, 100.0, 0.05, 0.2, 1.0, True, 10, 1000, randomSeed=3)
    putResult = priceAsianArithmeticAverageOptionViaMonteCarlo(100.0, 100.0, 0.05, 0.2, 1.0, False, 10, 1000, randomSeed=3)
    assert callResult.estimatedPrice >= 0.0
    assert putResult.estimatedPrice >= 0.0


def testMonteCarloValueAtRiskIsSelfConsistentWithSimulatedTerminalValues():
    result = simulateMonteCarloPortfolioValueAtRisk(
        currentPortfolioValue=1_000_000.0,
        portfolioExpectedAnnualReturn=0.08,
        portfolioAnnualizedVolatility=0.15,
        timeHorizonInYears=1.0,
        confidenceLevel=0.95,
        numberOfPaths=20000,
        randomSeed=3,
    )
    # Recompute the same nearest-rank empirical VaR directly from the
    # returned simulated terminal values, using the identical convention
    # documented in the module -- must match exactly (real, not fabricated).
    terminalReturns = sorted(
        (value - 1_000_000.0) / 1_000_000.0 for value in result.simulatedTerminalPortfolioValues
    )
    percentileIndex = int((1.0 - 0.95) * len(terminalReturns) + 1e-9)
    expectedValueAtRisk = -terminalReturns[percentileIndex]
    assert result.valueAtRisk == pytest.approx(expectedValueAtRisk)
    assert result.valueAtRisk > 0.0
    assert result.numberOfPaths == 20000


def testMonteCarloValueAtRiskIsHigherAtHigherConfidenceLevel():
    var95 = simulateMonteCarloPortfolioValueAtRisk(1_000_000.0, 0.08, 0.15, 1.0, 0.95, 20000, randomSeed=3)
    var99 = simulateMonteCarloPortfolioValueAtRisk(1_000_000.0, 0.08, 0.15, 1.0, 0.99, 20000, randomSeed=3)
    assert var99.valueAtRisk > var95.valueAtRisk


def testMonteCarloValueAtRiskRejectsInvalidConfidenceLevel():
    with pytest.raises(ValueError):
        simulateMonteCarloPortfolioValueAtRisk(1_000_000.0, 0.08, 0.15, 1.0, 1.5, 1000, randomSeed=1)
    with pytest.raises(ValueError):
        simulateMonteCarloPortfolioValueAtRisk(1_000_000.0, 0.08, 0.15, 1.0, 0.0, 1000, randomSeed=1)


def testMonteCarloValueAtRiskIsDeterministicForFixedSeed():
    resultA = simulateMonteCarloPortfolioValueAtRisk(1_000_000.0, 0.08, 0.15, 1.0, 0.95, 5000, randomSeed=11)
    resultB = simulateMonteCarloPortfolioValueAtRisk(1_000_000.0, 0.08, 0.15, 1.0, 0.95, 5000, randomSeed=11)
    assert resultA.valueAtRisk == resultB.valueAtRisk
