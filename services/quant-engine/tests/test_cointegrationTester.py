from __future__ import annotations

import math

import pytest

from quantengine.cointegrationTester import (
    ASYMPTOTIC_DICKEY_FULLER_CRITICAL_VALUES_NO_CONSTANT,
    calculateAugmentedDickeyFullerTestStatistic,
    invertSquareMatrixViaGaussJordanElimination,
    multiplyMatrices,
    performEngleGrangerCointegrationTest,
    runOrdinaryLeastSquaresRegression,
)


def testMatrixInversionHandWorkedTwoByTwo():
    # [[4, 7], [2, 6]] has determinant 4*6 - 7*2 = 10, inverse = (1/10)*[[6,-7],[-2,4]]
    inverse = invertSquareMatrixViaGaussJordanElimination([[4.0, 7.0], [2.0, 6.0]])
    expected = [[0.6, -0.7], [-0.2, 0.4]]
    for i in range(2):
        for j in range(2):
            assert inverse[i][j] == pytest.approx(expected[i][j])


def testMatrixInversionOfIdentityIsIdentity():
    identity = [[1.0, 0.0], [0.0, 1.0]]
    inverse = invertSquareMatrixViaGaussJordanElimination(identity)
    assert [v for row in inverse for v in row] == pytest.approx([v for row in identity for v in row])


def testSingularMatrixRaisesOnInversion():
    with pytest.raises(ValueError):
        invertSquareMatrixViaGaussJordanElimination([[1.0, 2.0], [2.0, 4.0]])


def testMultiplyMatricesHandWorked():
    result = multiplyMatrices([[1.0, 2.0], [3.0, 4.0]], [[5.0, 6.0], [7.0, 8.0]])
    expected = [[19.0, 22.0], [43.0, 50.0]]
    assert [v for row in result for v in row] == pytest.approx([v for row in expected for v in row])


def testHandWorkedSimpleLinearRegression():
    # x = [1,2,3,4,5], y = [2,4,5,4,5]
    # xbar=3, ybar=4
    # Sxy = sum((x-xbar)(y-ybar)) = (-2)(-2)+(-1)(0)+(0)(1)+(1)(0)+(2)(1) = 4+0+0+0+2 = 6
    # Sxx = sum((x-xbar)^2) = 4+1+0+1+4 = 10
    # slope = Sxy/Sxx = 0.6, intercept = ybar - slope*xbar = 4 - 1.8 = 2.2
    designMatrix = [[1.0, x] for x in [1.0, 2.0, 3.0, 4.0, 5.0]]
    result = runOrdinaryLeastSquaresRegression(designMatrix, [2.0, 4.0, 5.0, 4.0, 5.0])
    intercept, slope = result.coefficients
    assert intercept == pytest.approx(2.2)
    assert slope == pytest.approx(0.6)


def testOlsRaisesWhenNotEnoughObservationsForDegreesOfFreedom():
    with pytest.raises(ValueError):
        runOrdinaryLeastSquaresRegression([[1.0, 1.0], [1.0, 2.0]], [2.0, 4.0])


def testHandWorkedAdfTestStatisticAlternatingSeries():
    # series = [0,1,0,1,0,1] (lag=0):
    # firstDifferences = [1,-1,1,-1,1]
    # design rows use series[differenceIndex] for differenceIndex in 1..4 -> [1,0,1,0]
    # targets = firstDifferences[1:5] = [-1,1,-1,1]
    # regression through origin: rho = sum(xy)/sum(x^2) = (1*-1+0*1+1*-1+0*1) / (1+0+1+0) = -2/2 = -1
    # residuals of that regression: y - rho*x = [-1-(-1), 1-0, -1-(-1), 1-0] = [0,1,0,1]
    # SSE = 0+1+0+1 = 2, df = 4-1 = 3, sigma^2 = 2/3
    # Var(rho) = sigma^2 / sum(x^2) = (2/3)/2 = 1/3, SE = sqrt(1/3)
    # t-stat = rho / SE = -1 / sqrt(1/3) = -sqrt(3)
    result = calculateAugmentedDickeyFullerTestStatistic([0.0, 1.0, 0.0, 1.0, 0.0, 1.0], numberOfLagsForAugmentedTerms=0)
    assert result.testStatistic == pytest.approx(-math.sqrt(3.0))
    assert result.unitRootCoefficient == pytest.approx(-1.0)


def testAdfCriticalValueTableHasStandardThreeLevels():
    assert set(ASYMPTOTIC_DICKEY_FULLER_CRITICAL_VALUES_NO_CONSTANT.keys()) == {"1%", "5%", "10%"}
    assert ASYMPTOTIC_DICKEY_FULLER_CRITICAL_VALUES_NO_CONSTANT["1%"] < ASYMPTOTIC_DICKEY_FULLER_CRITICAL_VALUES_NO_CONSTANT["5%"] < ASYMPTOTIC_DICKEY_FULLER_CRITICAL_VALUES_NO_CONSTANT["10%"]


def testStronglyMeanRevertingSeriesRejectsUnitRootAtAllLevels():
    # A damped oscillation (AR(1) with rho=-0.5, i.e. strongly mean-reverting)
    # plus a tiny perturbation (to avoid a degenerate zero-residual fit)
    # should produce a very negative ADF statistic, rejecting the unit
    # root at every standard significance level.
    series = [10.0 * ((-0.5) ** i) + 0.01 * math.sin(i) for i in range(30)]
    result = calculateAugmentedDickeyFullerTestStatistic(series, numberOfLagsForAugmentedTerms=0)
    assert result.testStatistic < ASYMPTOTIC_DICKEY_FULLER_CRITICAL_VALUES_NO_CONSTANT["1%"]
    assert result.isStationaryAtOnePercent is True
    assert result.isStationaryAtFivePercent is True
    assert result.isStationaryAtTenPercent is True


def testRandomWalkWithDriftFailsToRejectUnitRoot():
    # A cumulative-sum ("random walk with drift") series has a genuine
    # unit root -- the ADF test statistic should be nowhere near the
    # negative critical values (here it's even positive), so the test
    # correctly fails to reject the unit root at every level.
    increments = [1, 2, -1, 3, -2, 4, 1, -1, 2, 3, -2, 1, 2, -1, 3, 1, 2, -1, 2, 3, -2, 1, 2, -1, 3, 1, 2, -1, 2]
    series = [0.0]
    for increment in increments:
        series.append(series[-1] + increment)
    result = calculateAugmentedDickeyFullerTestStatistic(series, numberOfLagsForAugmentedTerms=0)
    assert result.isStationaryAtOnePercent is False
    assert result.isStationaryAtFivePercent is False
    assert result.isStationaryAtTenPercent is False


def testNegativeLagCountRaises():
    with pytest.raises(ValueError):
        calculateAugmentedDickeyFullerTestStatistic([1.0, 2.0, 3.0], numberOfLagsForAugmentedTerms=-1)


def testSeriesTooShortForRequestedLagsRaises():
    with pytest.raises(ValueError):
        calculateAugmentedDickeyFullerTestStatistic([1.0, 2.0], numberOfLagsForAugmentedTerms=5)


def testEngleGrangerDetectsCointegratedPairByConstruction():
    # secondSeries is EXACTLY 3*firstSeries plus a small BOUNDED
    # (non-growing) oscillating perturbation -- a textbook cointegrated
    # pair (shared stochastic trend via firstSeries, bounded spread).
    firstSeries = [100.0 + 0.5 * t for t in range(40)]
    secondSeries = [3.0 * firstSeries[t] + 5.0 * ((-1) ** t) + 0.01 * (t % 3) for t in range(40)]
    result = performEngleGrangerCointegrationTest(firstSeries, secondSeries, numberOfLagsForAugmentedTerms=0)
    assert result.regressionSlope == pytest.approx(3.0, abs=0.05)
    assert result.isLikelyCointegratedAtFivePercent is True


def testEngleGrangerDoesNotDetectCointegrationForUnrelatedTrendingSeries():
    # secondSeries has an UNBOUNDED, super-linearly-growing residual
    # relative to firstSeries -- the two series are NOT cointegrated,
    # and the test correctly fails to reject the unit root.
    firstSeries = [100.0 + 0.5 * t for t in range(40)]
    secondSeries = [200.0 + 0.9 * t + 0.02 * (t ** 1.6) for t in range(40)]
    result = performEngleGrangerCointegrationTest(firstSeries, secondSeries, numberOfLagsForAugmentedTerms=0)
    assert result.isLikelyCointegratedAtFivePercent is False
    assert result.adfTestResult.testStatistic > ASYMPTOTIC_DICKEY_FULLER_CRITICAL_VALUES_NO_CONSTANT["10%"]


def testEngleGrangerRaisesOnMismatchedSeriesLengths():
    with pytest.raises(ValueError):
        performEngleGrangerCointegrationTest([1.0, 2.0, 3.0], [1.0, 2.0])


def testEngleGrangerResidualSeriesHasExpectedLength():
    firstSeries = [100.0 + 0.5 * t for t in range(20)]
    secondSeries = [3.0 * firstSeries[t] + 2.0 * ((-1) ** t) for t in range(20)]
    result = performEngleGrangerCointegrationTest(firstSeries, secondSeries, numberOfLagsForAugmentedTerms=0)
    assert len(result.residualSeries) == len(firstSeries)


def testAugmentedDickeyFullerWithOneLagRunsAndReturnsStructuredResult():
    series = [10.0 * ((-0.5) ** i) + 0.01 * math.sin(i) for i in range(30)]
    result = calculateAugmentedDickeyFullerTestStatistic(series, numberOfLagsForAugmentedTerms=1)
    assert result.numberOfLagsUsed == 1
    assert isinstance(result.testStatistic, float)
