from __future__ import annotations

import pytest

from quantengine.kellyCriterionSizer import (
    applyFractionalKelly,
    calculateKellyFractionFromPeriodicReturnSeries,
    calculateKellyFractionFromReturnDistributionStatistics,
    calculateKellyFractionFromWinLossStatistics,
)


def testHandWorkedClassicKellyFraction():
    # p = 0.55, b = 1.5, q = 0.45
    # f* = (b*p - q) / b = (1.5*0.55 - 0.45) / 1.5 = (0.825 - 0.45) / 1.5 = 0.375 / 1.5 = 0.25
    fraction = calculateKellyFractionFromWinLossStatistics(winProbability=0.55, winLossPayoutRatio=1.5)
    assert fraction == pytest.approx(0.25)


def testHandWorkedHalfKellyFromClassicFraction():
    fullKelly = calculateKellyFractionFromWinLossStatistics(0.55, 1.5)
    result = applyFractionalKelly(fullKelly, fractionalMultiplier=0.5)
    assert result.fullKellyFraction == pytest.approx(0.25)
    assert result.recommendedAllocationFraction == pytest.approx(0.125)


def testFiftyPercentWinRateEvenOddsGivesZeroEdge():
    # p = 0.5, b = 1.0 -> f* = (1*0.5 - 0.5)/1 = 0.0 (no edge, no bet)
    fraction = calculateKellyFractionFromWinLossStatistics(0.5, 1.0)
    assert fraction == pytest.approx(0.0)


def testNegativeEdgeGivesNegativeKellyFraction():
    # p = 0.3, b = 1.0 -> f* = (0.3 - 0.7)/1.0 = -0.4 (don't bet; a losing proposition)
    fraction = calculateKellyFractionFromWinLossStatistics(0.3, 1.0)
    assert fraction == pytest.approx(-0.4)


def testWinProbabilityMustBeStrictlyBetweenZeroAndOne():
    with pytest.raises(ValueError):
        calculateKellyFractionFromWinLossStatistics(0.0, 1.0)
    with pytest.raises(ValueError):
        calculateKellyFractionFromWinLossStatistics(1.0, 1.0)


def testWinLossPayoutRatioMustBePositive():
    with pytest.raises(ValueError):
        calculateKellyFractionFromWinLossStatistics(0.5, 0.0)
    with pytest.raises(ValueError):
        calculateKellyFractionFromWinLossStatistics(0.5, -1.0)


def testHandWorkedContinuousKellyFromMeanVariance():
    # mean = 0.02, variance = 0.10 -> f* = 0.02 / 0.10 = 0.2
    fraction = calculateKellyFractionFromReturnDistributionStatistics(meanPeriodicReturn=0.02, periodicReturnVariance=0.10)
    assert fraction == pytest.approx(0.2)


def testContinuousKellyRequiresPositiveVariance():
    with pytest.raises(ValueError):
        calculateKellyFractionFromReturnDistributionStatistics(0.02, 0.0)
    with pytest.raises(ValueError):
        calculateKellyFractionFromReturnDistributionStatistics(0.02, -0.01)


def testKellyFromPeriodicReturnSeriesHandWorked():
    # returns = [0.10, -0.10] -> mean = 0.0, population variance = ((0.1)^2+(0.1)^2)/2 = 0.01
    # f* = 0.0 / 0.01 = 0.0
    fraction = calculateKellyFractionFromPeriodicReturnSeries([0.10, -0.10])
    assert fraction == pytest.approx(0.0)


def testKellyFromPeriodicReturnSeriesWithPositiveDrift():
    # returns = [0.04, 0.02] -> mean = 0.03, population variance = ((0.01)^2+(0.01)^2)/2 = 0.0001
    # f* = 0.03 / 0.0001 = 300.0 (illustrates why raw continuous Kelly can be extreme
    # for very-low-variance/high-mean synthetic inputs -- fractional Kelly is the mitigation)
    fraction = calculateKellyFractionFromPeriodicReturnSeries([0.04, 0.02])
    assert fraction == pytest.approx(300.0)


def testKellyFromEmptySeriesRaises():
    with pytest.raises(ValueError):
        calculateKellyFractionFromPeriodicReturnSeries([])


def testFractionalKellyMultiplierMustBeInRangeZeroExclusiveToOneInclusive():
    with pytest.raises(ValueError):
        applyFractionalKelly(0.25, fractionalMultiplier=0.0)
    with pytest.raises(ValueError):
        applyFractionalKelly(0.25, fractionalMultiplier=1.5)


def testFractionalKellyAtFullMultiplierEqualsFullKelly():
    result = applyFractionalKelly(0.4, fractionalMultiplier=1.0)
    assert result.recommendedAllocationFraction == pytest.approx(0.4)


def testFractionalKellyDefaultsToHalfKelly():
    result = applyFractionalKelly(0.4)
    assert result.fractionalMultiplier == 0.5
    assert result.recommendedAllocationFraction == pytest.approx(0.2)


def testFractionalKellyPreservesSignOfNegativeFullKelly():
    result = applyFractionalKelly(-0.3, fractionalMultiplier=0.5)
    assert result.recommendedAllocationFraction == pytest.approx(-0.15)
