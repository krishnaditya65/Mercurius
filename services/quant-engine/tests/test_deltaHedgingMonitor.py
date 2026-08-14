from __future__ import annotations

import pytest

from quantengine.deltaHedgingMonitor import evaluateDeltaHedgingThreshold
from quantengine.portfolioGreeksAggregator import PortfolioGreeksAggregationResult


def buildGreeks(netDelta: float) -> PortfolioGreeksAggregationResult:
    return PortfolioGreeksAggregationResult(
        netDelta=netDelta, netGamma=0.0, netVegaPerOnePercentVolatilityChange=0.0, netThetaPerCalendarDay=0.0, positionCount=1
    )


def testHandWorkedBreachAndHedgeQuantity():
    # netDelta = 12.0, threshold = 5.0 -> breached (|12| > 5)
    # hedgeQuantityInShares = -12.0 * 100 = -1200.0 (sell/short 1200 shares to flatten)
    result = evaluateDeltaHedgingThreshold(buildGreeks(12.0), deltaThreshold=5.0)
    assert result.isThresholdBreached is True
    assert result.hedgeQuantityInShares == pytest.approx(-1200.0)


def testHandWorkedNegativeNetDeltaHedgeQuantity():
    # netDelta = -8.0, threshold = 5.0 -> breached (|-8| > 5)
    # hedgeQuantityInShares = -(-8.0) * 100 = 800.0 (buy 800 shares to flatten)
    result = evaluateDeltaHedgingThreshold(buildGreeks(-8.0), deltaThreshold=5.0)
    assert result.isThresholdBreached is True
    assert result.hedgeQuantityInShares == pytest.approx(800.0)


def testWithinThresholdIsNotBreached():
    result = evaluateDeltaHedgingThreshold(buildGreeks(3.0), deltaThreshold=5.0)
    assert result.isThresholdBreached is False
    assert result.hedgeQuantityInShares == pytest.approx(-300.0)


def testExactlyAtThresholdIsNotBreached():
    # strict inequality: abs(netDelta) > threshold, so equality does not breach
    result = evaluateDeltaHedgingThreshold(buildGreeks(5.0), deltaThreshold=5.0)
    assert result.isThresholdBreached is False


def testJustAboveThresholdIsBreached():
    result = evaluateDeltaHedgingThreshold(buildGreeks(5.0001), deltaThreshold=5.0)
    assert result.isThresholdBreached is True


def testZeroNetDeltaNeverBreachesAndNeedsNoHedge():
    result = evaluateDeltaHedgingThreshold(buildGreeks(0.0), deltaThreshold=1.0)
    assert result.isThresholdBreached is False
    assert result.hedgeQuantityInShares == 0.0


def testCustomSharesPerContractMultiplier():
    # netDelta = 2.0, multiplier = 50 -> hedgeQuantity = -100.0
    result = evaluateDeltaHedgingThreshold(buildGreeks(2.0), deltaThreshold=1.0, sharesPerContractMultiplier=50.0)
    assert result.hedgeQuantityInShares == pytest.approx(-100.0)
    assert result.sharesPerContractMultiplierUsed == 50.0


def testZeroThresholdRaises():
    with pytest.raises(ValueError):
        evaluateDeltaHedgingThreshold(buildGreeks(1.0), deltaThreshold=0.0)


def testNegativeThresholdRaises():
    with pytest.raises(ValueError):
        evaluateDeltaHedgingThreshold(buildGreeks(1.0), deltaThreshold=-2.0)


def testNonPositiveMultiplierRaises():
    with pytest.raises(ValueError):
        evaluateDeltaHedgingThreshold(buildGreeks(1.0), deltaThreshold=1.0, sharesPerContractMultiplier=0.0)


def testResultCarriesThroughInputDeltaAndThreshold():
    result = evaluateDeltaHedgingThreshold(buildGreeks(7.5), deltaThreshold=10.0)
    assert result.netDelta == 7.5
    assert result.deltaThreshold == 10.0
