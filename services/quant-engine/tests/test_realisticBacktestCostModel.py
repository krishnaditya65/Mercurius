from __future__ import annotations

import math

import pytest

from quantengine.realisticBacktestCostModel import (
    applySlippageToReferencePrice,
    calculateSquareRootMarketImpactCostFraction,
    simulatePartialFillAgainstOrderBookLevels,
    simulateRealisticMarketOrderFill,
)


def testHandWorkedMarketImpactCostFraction():
    # orderQuantity=10000, ADV=1,000,000, dailyVolatility=0.02, impactCoefficient=1.0
    # fraction = 1.0 * 0.02 * sqrt(10000/1000000) = 0.02 * sqrt(0.01) = 0.02 * 0.1 = 0.002
    fraction = calculateSquareRootMarketImpactCostFraction(
        orderQuantity=10000.0, averageDailyVolume=1_000_000.0, dailyVolatility=0.02, impactCoefficient=1.0
    )
    assert fraction == pytest.approx(0.002)


def testMarketImpactScalesWithImpactCoefficient():
    baseline = calculateSquareRootMarketImpactCostFraction(10000.0, 1_000_000.0, 0.02, impactCoefficient=1.0)
    doubled = calculateSquareRootMarketImpactCostFraction(10000.0, 1_000_000.0, 0.02, impactCoefficient=2.0)
    assert doubled == pytest.approx(baseline * 2.0)


def testMarketImpactGrowsWithSquareRootOfOrderSize():
    # 4x the order size should give exactly 2x the impact (sqrt law)
    small = calculateSquareRootMarketImpactCostFraction(10000.0, 1_000_000.0, 0.02)
    large = calculateSquareRootMarketImpactCostFraction(40000.0, 1_000_000.0, 0.02)
    assert large == pytest.approx(small * 2.0)


def testZeroOrderQuantityGivesZeroImpact():
    assert calculateSquareRootMarketImpactCostFraction(0.0, 1_000_000.0, 0.02) == 0.0


def testMarketImpactRejectsNegativeOrderQuantity():
    with pytest.raises(ValueError):
        calculateSquareRootMarketImpactCostFraction(-1.0, 1_000_000.0, 0.02)


def testMarketImpactRejectsNonPositiveAdvOrVolatilityOrCoefficient():
    with pytest.raises(ValueError):
        calculateSquareRootMarketImpactCostFraction(1000.0, 0.0, 0.02)
    with pytest.raises(ValueError):
        calculateSquareRootMarketImpactCostFraction(1000.0, 1_000_000.0, 0.0)
    with pytest.raises(ValueError):
        calculateSquareRootMarketImpactCostFraction(1000.0, 1_000_000.0, 0.02, impactCoefficient=0.0)


def testHandWorkedSlippageForBuyOrder():
    # referencePrice=100, slippageFraction=0.002 -> 100 * 1.002 = 100.2
    price = applySlippageToReferencePrice(100.0, isBuyOrder=True, slippageFraction=0.002)
    assert price == pytest.approx(100.2)


def testHandWorkedSlippageForSellOrder():
    # referencePrice=100, slippageFraction=0.002 -> 100 * 0.998 = 99.8
    price = applySlippageToReferencePrice(100.0, isBuyOrder=False, slippageFraction=0.002)
    assert price == pytest.approx(99.8)


def testZeroSlippageLeavesPriceUnchanged():
    assert applySlippageToReferencePrice(100.0, True, 0.0) == 100.0
    assert applySlippageToReferencePrice(100.0, False, 0.0) == 100.0


def testSlippageRejectsNonPositiveReferencePrice():
    with pytest.raises(ValueError):
        applySlippageToReferencePrice(0.0, True, 0.001)


def testSlippageRejectsNegativeFraction():
    with pytest.raises(ValueError):
        applySlippageToReferencePrice(100.0, True, -0.001)


def testHandWorkedFullPartialFillAcrossThreeLevels():
    # priceLevels = [(100.0,50), (100.5,30), (101.0,20)], orderQuantity=90
    # fills: 50@100 + 30@100.5 + 10@101 = 90 total (last level partially consumed)
    # VWAP = (50*100 + 30*100.5 + 10*101) / 90 = (5000 + 3015 + 1010) / 90 = 9025/90
    result = simulatePartialFillAgainstOrderBookLevels(90.0, [(100.0, 50.0), (100.5, 30.0), (101.0, 20.0)])
    assert result.filledQuantity == pytest.approx(90.0)
    assert result.unfilledQuantity == pytest.approx(0.0)
    assert result.isFullyFilled is True
    assert result.volumeWeightedAverageFillPrice == pytest.approx(9025.0 / 90.0)
    assert result.fillsByLevel == [(100.0, 50.0), (100.5, 30.0), (101.0, 10.0)]


def testHandWorkedPartialFillWhenLiquidityInsufficient():
    # priceLevels total = 50+30+20 = 100, orderQuantity=120 -> only 100 fillable, 20 unfilled
    # VWAP = (50*100 + 30*100.5 + 20*101) / 100 = (5000+3015+2020)/100 = 10035/100
    result = simulatePartialFillAgainstOrderBookLevels(120.0, [(100.0, 50.0), (100.5, 30.0), (101.0, 20.0)])
    assert result.filledQuantity == pytest.approx(100.0)
    assert result.unfilledQuantity == pytest.approx(20.0)
    assert result.isFullyFilled is False
    assert result.volumeWeightedAverageFillPrice == pytest.approx(10035.0 / 100.0)


def testEmptyOrderBookGivesZeroFillAndNoneVwap():
    result = simulatePartialFillAgainstOrderBookLevels(50.0, [])
    assert result.filledQuantity == 0.0
    assert result.unfilledQuantity == 50.0
    assert result.volumeWeightedAverageFillPrice is None
    assert result.isFullyFilled is False


def testPartialFillRejectsNonPositiveOrderQuantity():
    with pytest.raises(ValueError):
        simulatePartialFillAgainstOrderBookLevels(0.0, [(100.0, 10.0)])


def testPartialFillRejectsNegativeLevelQuantity():
    with pytest.raises(ValueError):
        simulatePartialFillAgainstOrderBookLevels(10.0, [(100.0, -5.0)])


def testExactLiquidityMatchFillsCompletelyWithNoRemainder():
    result = simulatePartialFillAgainstOrderBookLevels(80.0, [(100.0, 50.0), (100.5, 30.0)])
    assert result.isFullyFilled is True
    assert result.unfilledQuantity == 0.0


def testSimulateRealisticMarketOrderFillComposesAllThreePieces():
    result = simulateRealisticMarketOrderFill(
        orderQuantity=90.0,
        isBuyOrder=True,
        referencePrice=100.0,
        averageDailyVolume=1_000_000.0,
        dailyVolatility=0.02,
        priceLevels=[(100.0, 50.0), (100.5, 30.0), (101.0, 20.0)],
        impactCoefficient=1.0,
    )
    expectedImpact = calculateSquareRootMarketImpactCostFraction(90.0, 1_000_000.0, 0.02, 1.0)
    assert result.marketImpactCostFraction == pytest.approx(expectedImpact)
    assert result.slippageAdjustedReferencePrice == pytest.approx(100.0 * (1.0 + expectedImpact))
    assert result.partialFillResult.filledQuantity == pytest.approx(90.0)
    assert result.partialFillResult.isFullyFilled is True
