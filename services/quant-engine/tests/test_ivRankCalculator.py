from __future__ import annotations

import pytest

from quantengine.ivRankCalculator import (
    calculateImpliedVolatilityPercentile,
    calculateImpliedVolatilityRank,
    calculateImpliedVolatilityRankAndPercentile,
)

# Shared hand-worked fixture:
# historical = [0.10, 0.12, 0.14, 0.16, 0.18, 0.20, 0.50], current = 0.19
# min = 0.10, max = 0.50, range = 0.40
# ivRank = (0.19 - 0.10) / 0.40 = 0.09 / 0.40 = 0.225
# below-0.19 count = {0.10, 0.12, 0.14, 0.16, 0.18} = 5 of 7 = 0.714285714...
HAND_WORKED_HISTORICAL_SERIES = [0.10, 0.12, 0.14, 0.16, 0.18, 0.20, 0.50]
HAND_WORKED_CURRENT = 0.19


def testHandWorkedImpliedVolatilityRank():
    assert calculateImpliedVolatilityRank(HAND_WORKED_CURRENT, HAND_WORKED_HISTORICAL_SERIES) == pytest.approx(0.225)


def testHandWorkedImpliedVolatilityPercentile():
    assert calculateImpliedVolatilityPercentile(
        HAND_WORKED_CURRENT, HAND_WORKED_HISTORICAL_SERIES
    ) == pytest.approx(5.0 / 7.0)


def testHandWorkedCombinedResult():
    result = calculateImpliedVolatilityRankAndPercentile(HAND_WORKED_CURRENT, HAND_WORKED_HISTORICAL_SERIES)
    assert result.currentImpliedVolatility == HAND_WORKED_CURRENT
    assert result.historicalMinimumImpliedVolatility == 0.10
    assert result.historicalMaximumImpliedVolatility == 0.50
    assert result.impliedVolatilityRank == pytest.approx(0.225)
    assert result.impliedVolatilityPercentile == pytest.approx(5.0 / 7.0)


def testCurrentEqualToHistoricalMinimumGivesZeroRank():
    assert calculateImpliedVolatilityRank(0.10, HAND_WORKED_HISTORICAL_SERIES) == pytest.approx(0.0)


def testCurrentEqualToHistoricalMaximumGivesRankOfOne():
    assert calculateImpliedVolatilityRank(0.50, HAND_WORKED_HISTORICAL_SERIES) == pytest.approx(1.0)


def testCurrentAboveHistoricalMaximumGivesRankAboveOne():
    assert calculateImpliedVolatilityRank(0.90, HAND_WORKED_HISTORICAL_SERIES) == pytest.approx((0.90 - 0.10) / 0.40)


def testCurrentBelowHistoricalMinimumGivesNegativeRank():
    assert calculateImpliedVolatilityRank(0.05, HAND_WORKED_HISTORICAL_SERIES) == pytest.approx((0.05 - 0.10) / 0.40)


def testPercentileIsZeroWhenCurrentIsBelowEveryHistoricalObservation():
    assert calculateImpliedVolatilityPercentile(0.01, HAND_WORKED_HISTORICAL_SERIES) == 0.0


def testPercentileIsOneWhenCurrentExceedsEveryHistoricalObservation():
    assert calculateImpliedVolatilityPercentile(0.99, HAND_WORKED_HISTORICAL_SERIES) == 1.0


def testPercentileUsesStrictlyLessThanNotLessThanOrEqual():
    # current exactly equal to one historical observation (0.20) should NOT
    # count that observation as "below" — only 0.10..0.18 (5 of 7) are.
    assert calculateImpliedVolatilityPercentile(0.20, HAND_WORKED_HISTORICAL_SERIES) == pytest.approx(5.0 / 7.0)


def testEmptyHistoricalSeriesRaisesForRank():
    with pytest.raises(ValueError):
        calculateImpliedVolatilityRank(0.20, [])


def testEmptyHistoricalSeriesRaisesForPercentile():
    with pytest.raises(ValueError):
        calculateImpliedVolatilityPercentile(0.20, [])


def testFlatHistoricalSeriesRaisesForRankDivisionByZero():
    with pytest.raises(ValueError):
        calculateImpliedVolatilityRank(0.20, [0.30, 0.30, 0.30])


def testFlatHistoricalSeriesStillWorksForPercentile():
    # percentile has no divide-by-zero failure mode — current above the
    # flat value gives percentile 1.0, current below gives 0.0.
    assert calculateImpliedVolatilityPercentile(0.35, [0.30, 0.30, 0.30]) == 1.0
    assert calculateImpliedVolatilityPercentile(0.25, [0.30, 0.30, 0.30]) == 0.0


def testSingleObservationHistoricalSeries():
    assert calculateImpliedVolatilityPercentile(0.25, [0.20]) == 1.0
    assert calculateImpliedVolatilityPercentile(0.15, [0.20]) == 0.0
