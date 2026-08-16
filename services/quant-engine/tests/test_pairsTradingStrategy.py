import math

import pytest

from quantengine.backtesting.backtestRunner import (
    PortfolioState,
    TradeAction,
    runDeterministicEventDrivenBacktest,
)
from quantengine.backtesting.pairsTradingStrategy import (
    ZScoreMeanReversionPairsTradingStrategy,
    calculateRollingMeanAndStandardDeviation,
    calculateSpreadSeriesBetweenTwoPriceSeries,
)
from quantengine.backtesting.tickStore import HistoricalPriceTick

# Reuses the exact hand-worked series from test_riskStatistics.py's
# HAND_WORKED_RETURN_SERIES: [0.04, -0.02, 0.04, -0.02, 0.04, -0.02]
# mean = 0.01, population stddev = 0.03 (see that file's comment for the
# full worked arithmetic).
HAND_WORKED_SERIES = [0.04, -0.02, 0.04, -0.02, 0.04, -0.02]


def test_rollingMeanAndStandardDeviationMatchesHandWorkedValue():
    mean, standardDeviation = calculateRollingMeanAndStandardDeviation(HAND_WORKED_SERIES, lookbackWindowSize=6)
    assert math.isclose(mean, 0.01, rel_tol=1e-12)
    assert math.isclose(standardDeviation, 0.03, rel_tol=1e-9)


def test_rollingMeanAndStandardDeviationUsesOnlyTrailingWindow():
    # window size 2 over [1, 2, 3, 100] should use only the last 2 values: [3, 100]
    mean, standardDeviation = calculateRollingMeanAndStandardDeviation([1, 2, 3, 100], lookbackWindowSize=2)
    assert mean == pytest.approx(51.5)
    assert standardDeviation == pytest.approx(48.5)


def test_calculateSpreadSeriesSubtractsElementwise():
    spread = calculateSpreadSeriesBetweenTwoPriceSeries([100.0, 102.0, 98.0], [99.0, 100.0, 101.0])
    assert spread == pytest.approx([1.0, 2.0, -3.0])


def test_calculateSpreadSeriesRaisesOnMismatchedLengths():
    with pytest.raises(ValueError):
        calculateSpreadSeriesBetweenTwoPriceSeries([1.0, 2.0], [1.0])


def test_strategyConstructorRejectsInvalidThresholdOrdering():
    with pytest.raises(ValueError):
        ZScoreMeanReversionPairsTradingStrategy(
            lookbackWindowSize=10, entryZScoreThreshold=0.5, exitZScoreThreshold=1.0, tradeQuantity=1.0
        )


def test_strategyConstructorRejectsTooSmallLookbackWindow():
    with pytest.raises(ValueError):
        ZScoreMeanReversionPairsTradingStrategy(
            lookbackWindowSize=1, entryZScoreThreshold=1.0, exitZScoreThreshold=0.3, tradeQuantity=1.0
        )


def test_strategyConstructorRejectsNonPositiveTradeQuantity():
    with pytest.raises(ValueError):
        ZScoreMeanReversionPairsTradingStrategy(
            lookbackWindowSize=10, entryZScoreThreshold=1.0, exitZScoreThreshold=0.3, tradeQuantity=0.0
        )


def buildSyntheticOscillatingSpreadFixtureTicks(numberOfPoints: int = 60) -> list[HistoricalPriceTick]:
    """Fully deterministic synthetic spread series — a sine wave around a
    mean of 100 with amplitude 5 — used as fixture data for the
    end-to-end pairs-trading test below. No randomness: `math.sin` of a
    fixed input sequence is exactly reproducible.
    """
    return [
        HistoricalPriceTick(timestamp=float(index), price=100.0 + 5.0 * math.sin(index * 0.5))
        for index in range(numberOfPoints)
    ]


def test_endToEndPairsTradingStrategyEntersAndExitsAtLeastOncePositionAcrossFixtureSeries():
    fixtureTicks = buildSyntheticOscillatingSpreadFixtureTicks(numberOfPoints=60)
    strategy = ZScoreMeanReversionPairsTradingStrategy(
        lookbackWindowSize=10, entryZScoreThreshold=1.0, exitZScoreThreshold=0.3, tradeQuantity=1.0
    )

    result = runDeterministicEventDrivenBacktest(fixtureTicks, strategy, startingCashBalance=10_000.0)

    buyActionCount = sum(1 for point in result.equityCurve if point.actionTaken == TradeAction.BUY)
    sellActionCount = sum(1 for point in result.equityCurve if point.actionTaken == TradeAction.SELL)

    # The oscillating fixture must cross the entry threshold and revert
    # back across the (smaller) exit threshold at least once each
    # direction — i.e. the strategy actually trades, not just holds.
    assert buyActionCount >= 1
    assert sellActionCount >= 1

    # Sane P&L behavior: no NaN/inf equity anywhere in the curve, and the
    # final total equity is a finite real number.
    for point in result.equityCurve:
        assert math.isfinite(point.totalEquity)
    assert math.isfinite(result.finalTotalEquity)


def test_endToEndPairsTradingStrategyIsDeterministicAcrossRepeatedRuns():
    fixtureTicks = buildSyntheticOscillatingSpreadFixtureTicks(numberOfPoints=60)

    firstResult = runDeterministicEventDrivenBacktest(
        fixtureTicks,
        ZScoreMeanReversionPairsTradingStrategy(
            lookbackWindowSize=10, entryZScoreThreshold=1.0, exitZScoreThreshold=0.3, tradeQuantity=1.0
        ),
        startingCashBalance=10_000.0,
    )
    secondResult = runDeterministicEventDrivenBacktest(
        fixtureTicks,
        ZScoreMeanReversionPairsTradingStrategy(
            lookbackWindowSize=10, entryZScoreThreshold=1.0, exitZScoreThreshold=0.3, tradeQuantity=1.0
        ),
        startingCashBalance=10_000.0,
    )

    assert firstResult.equityCurve == secondResult.equityCurve
    assert firstResult.finalTotalEquity == secondResult.finalTotalEquity


def test_strategyTreatsFloatDustResidualPositionAsFlatInsteadOfPermanentlyHolding():
    # A float-dust residual positionQuantity (e.g. left over from a
    # backtest's earlier float accumulation error — see
    # test_backtestRunner.py's matching regression test) must still be
    # treated as FLAT by the strategy's entry rule. An exact
    # `positionQuantity == 0` check would misclassify this tiny non-zero
    # residual as "still long"/"still short" and, since this strategy
    # only ever opens a new position while flat, would wedge it into
    # perpetual HOLD forever even when the z-score clearly signals entry.
    strategy = ZScoreMeanReversionPairsTradingStrategy(
        lookbackWindowSize=5, entryZScoreThreshold=1.0, exitZScoreThreshold=0.3, tradeQuantity=1.0
    )
    # Warm up the rolling window with flat prices, then a sharp spike so
    # the z-score clearly exceeds entryZScoreThreshold on the final tick.
    warmupTicks = [
        HistoricalPriceTick(timestamp=float(i), price=100.0) for i in range(4)
    ]
    spikeTick = HistoricalPriceTick(timestamp=4.0, price=200.0)

    portfolioState = PortfolioState(cashBalance=100_000.0, positionQuantity=1e-16)

    decisions = [strategy(tick, portfolioState) for tick in warmupTicks]
    finalDecision = strategy(spikeTick, portfolioState)

    # A correct FRESH ENTRY trades the full tradeQuantity (1.0). A buggy
    # exact `== 0` flat-check instead misreads the 1e-16 dust as an
    # already-open long position (1e-16 > 0 is True in Python) and
    # "exits" it — a SELL of only abs(positionQuantity) ~= 1e-16, not a
    # real entry — which this assertion catches even though both are
    # nominally TradeAction.SELL.
    assert finalDecision.action == TradeAction.SELL
    assert finalDecision.quantity == pytest.approx(strategy.tradeQuantity)
