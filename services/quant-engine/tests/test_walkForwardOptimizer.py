import pytest

from quantengine.backtesting.backtestRunner import TradeAction, TradeDecision
from quantengine.backtesting.tickStore import HistoricalPriceTick
from quantengine.walkForwardOptimizer import (
    OverfittingWarning,
    evaluateOverfittingWarning,
    generateParameterCombinationsFromGrid,
    runWalkForwardOptimization,
)


def makeBuyAndHoldQuantityStrategy(quantity: float):
    """Buys `quantity` units on the very first tick of whatever window
    it's run over (position starts flat), then holds forever after —
    makes the resulting P&L hand-computable as
    `quantity * (finalPrice - firstPrice)` since there's exactly one
    trade and no partial closes.
    """

    def strategy(tick, portfolioState):
        if portfolioState.positionQuantity == 0:
            return TradeDecision(TradeAction.BUY, quantity)
        return TradeDecision(TradeAction.HOLD)

    return strategy


def ticksFromPrices(prices: list[float]) -> list[HistoricalPriceTick]:
    return [HistoricalPriceTick(timestamp=float(i), price=p) for i, p in enumerate(prices)]


class TestGenerateParameterCombinationsFromGrid:
    def testCartesianProductOfTwoParameters(self):
        combinations = generateParameterCombinationsFromGrid({"quantity": [1, 2], "threshold": [0.1, 0.2]})
        assert combinations == [
            {"quantity": 1, "threshold": 0.1},
            {"quantity": 1, "threshold": 0.2},
            {"quantity": 2, "threshold": 0.1},
            {"quantity": 2, "threshold": 0.2},
        ]

    def testSingleParameterGrid(self):
        combinations = generateParameterCombinationsFromGrid({"quantity": [1, 2, 3]})
        assert combinations == [{"quantity": 1}, {"quantity": 2}, {"quantity": 3}]

    def testEmptyGridRaises(self):
        with pytest.raises(ValueError):
            generateParameterCombinationsFromGrid({})

    def testEmptyCandidateListRaises(self):
        with pytest.raises(ValueError):
            generateParameterCombinationsFromGrid({"quantity": []})


class TestRunWalkForwardOptimizationHandWorked:
    def testSingleWindowBestQuantityAndOutOfSamplePnl(self):
        # In-sample window: prices [100, 101, 102] (3 ticks).
        # Out-of-sample window: prices [103, 105] (2 ticks).
        # Price rises monotonically in both windows, so a larger buy-and-
        # hold quantity always yields strictly larger P&L -> the grid
        # search must pick quantity=5 (the largest candidate) every time.
        prices = [100, 101, 102, 103, 105]
        ticks = ticksFromPrices(prices)

        result = runWalkForwardOptimization(
            ticks,
            parameterGrid={"quantity": [1, 2, 5]},
            strategyFactory=lambda params: makeBuyAndHoldQuantityStrategy(params["quantity"]),
            inSampleWindowSizeInTicks=3,
            outOfSampleWindowSizeInTicks=2,
        )

        assert len(result.windowResults) == 1
        window = result.windowResults[0]
        assert window.bestParameters == {"quantity": 5}
        # hand-worked: 5 * (102 - 100) = 10.0
        assert window.inSampleTotalProfitAndLoss == pytest.approx(10.0)
        # hand-worked: 5 * (105 - 103) = 10.0
        assert window.outOfSampleTotalProfitAndLoss == pytest.approx(10.0)
        assert result.averageInSampleProfitAndLoss == pytest.approx(10.0)
        assert result.averageOutOfSampleProfitAndLoss == pytest.approx(10.0)

    def testTwoNonOverlappingRollingWindows(self):
        # Window 1: IS=[100,101,102] OOS=[103,104]; Window 2 rolls forward
        # by stepSize=outOfSampleWindowSizeInTicks=2:
        # IS=[102,103,104] OOS=[105,106] (indices 2..6).
        prices = [100, 101, 102, 103, 104, 105, 106]
        ticks = ticksFromPrices(prices)

        result = runWalkForwardOptimization(
            ticks,
            parameterGrid={"quantity": [1, 3]},
            strategyFactory=lambda params: makeBuyAndHoldQuantityStrategy(params["quantity"]),
            inSampleWindowSizeInTicks=3,
            outOfSampleWindowSizeInTicks=2,
        )

        assert len(result.windowResults) == 2
        firstWindow, secondWindow = result.windowResults
        assert firstWindow.bestParameters == {"quantity": 3}
        # 3 * (102-100) = 6.0 in-sample; 3 * (104-103) = 3.0 out-of-sample
        assert firstWindow.inSampleTotalProfitAndLoss == pytest.approx(6.0)
        assert firstWindow.outOfSampleTotalProfitAndLoss == pytest.approx(3.0)
        # second window starts at tick index 2 (price 102)
        assert secondWindow.inSampleTickCount == 3
        assert secondWindow.outOfSampleTickCount == 2

    def testRaisesWhenTicksTooShortForOneWindow(self):
        ticks = ticksFromPrices([100, 101])
        with pytest.raises(ValueError):
            runWalkForwardOptimization(
                ticks,
                parameterGrid={"quantity": [1]},
                strategyFactory=lambda params: makeBuyAndHoldQuantityStrategy(params["quantity"]),
                inSampleWindowSizeInTicks=3,
                outOfSampleWindowSizeInTicks=2,
            )

    def testRaisesOnNonPositiveWindowSizes(self):
        ticks = ticksFromPrices([100, 101, 102])
        with pytest.raises(ValueError):
            runWalkForwardOptimization(
                ticks,
                parameterGrid={"quantity": [1]},
                strategyFactory=lambda params: makeBuyAndHoldQuantityStrategy(params["quantity"]),
                inSampleWindowSizeInTicks=0,
                outOfSampleWindowSizeInTicks=2,
            )

    def testCustomStepSizeOverlapsOutOfSampleData(self):
        prices = [100, 101, 102, 103, 104, 105]
        ticks = ticksFromPrices(prices)
        result = runWalkForwardOptimization(
            ticks,
            parameterGrid={"quantity": [1]},
            strategyFactory=lambda params: makeBuyAndHoldQuantityStrategy(params["quantity"]),
            inSampleWindowSizeInTicks=2,
            outOfSampleWindowSizeInTicks=2,
            stepSizeInTicks=1,
        )
        # windowSpan=4; ticks=6 -> startIndex can be 0,1,2 -> 3 windows
        assert len(result.windowResults) == 3


class TestEvaluateOverfittingWarningHandWorked:
    def testWalkForwardEfficiencyBelowThresholdFlagsOverfitting(self):
        # WFE = 20 / 100 = 0.2, below the default 0.5 threshold.
        # observationsPerParameter = 50 / 1 = 50.0, well above the
        # default 10.0 rule of thumb -> only the WFE reason should fire.
        warning = evaluateOverfittingWarning(
            averageInSampleProfitAndLoss=100.0,
            averageOutOfSampleProfitAndLoss=20.0,
            numberOfTunableParameters=1,
            inSampleWindowSizeInTicks=50,
        )
        assert warning.isFlagged is True
        assert warning.walkForwardEfficiencyRatio == pytest.approx(0.2)
        assert warning.observationsPerParameter == pytest.approx(50.0)
        assert len(warning.reasons) == 1
        assert "walk-forward efficiency" in warning.reasons[0]

    def testHighWalkForwardEfficiencyDoesNotFlag(self):
        # WFE = 90 / 100 = 0.9 >= 0.5 threshold;
        # observationsPerParameter = 100 / 2 = 50.0 >= 10.0 -> no flags.
        warning = evaluateOverfittingWarning(
            averageInSampleProfitAndLoss=100.0,
            averageOutOfSampleProfitAndLoss=90.0,
            numberOfTunableParameters=2,
            inSampleWindowSizeInTicks=100,
        )
        assert warning.isFlagged is False
        assert warning.reasons == []
        assert warning.walkForwardEfficiencyRatio == pytest.approx(0.9)

    def testTooFewObservationsPerParameterFlags(self):
        # observationsPerParameter = 5 / 3 = 1.6667, below the default
        # 10.0 rule of thumb.
        warning = evaluateOverfittingWarning(
            averageInSampleProfitAndLoss=100.0,
            averageOutOfSampleProfitAndLoss=100.0,
            numberOfTunableParameters=3,
            inSampleWindowSizeInTicks=5,
        )
        assert warning.isFlagged is True
        assert warning.observationsPerParameter == pytest.approx(5 / 3)
        assert any("observations per tunable parameter" in reason for reason in warning.reasons)

    def testNonPositiveInSamplePerformanceFlagsAndNullsRatio(self):
        warning = evaluateOverfittingWarning(
            averageInSampleProfitAndLoss=-5.0,
            averageOutOfSampleProfitAndLoss=10.0,
            numberOfTunableParameters=1,
            inSampleWindowSizeInTicks=1000,
        )
        assert warning.isFlagged is True
        assert warning.walkForwardEfficiencyRatio is None

    def testExactThresholdBoundaryIsNotFlaggedByWfeRule(self):
        # WFE = exactly 0.5 == threshold -> strict-less-than convention
        # means the boundary itself does NOT trigger the WFE reason.
        warning = evaluateOverfittingWarning(
            averageInSampleProfitAndLoss=100.0,
            averageOutOfSampleProfitAndLoss=50.0,
            numberOfTunableParameters=1,
            inSampleWindowSizeInTicks=100,
        )
        assert warning.walkForwardEfficiencyRatio == pytest.approx(0.5)
        assert not any("walk-forward efficiency" in reason for reason in warning.reasons)

    def testCustomThresholdsAreRespected(self):
        warning = evaluateOverfittingWarning(
            averageInSampleProfitAndLoss=100.0,
            averageOutOfSampleProfitAndLoss=80.0,
            numberOfTunableParameters=1,
            inSampleWindowSizeInTicks=100,
            walkForwardEfficiencyWarningThreshold=0.9,
        )
        # WFE = 0.8 < custom 0.9 threshold -> flagged, though it would
        # NOT have been flagged under the default 0.5 threshold.
        assert warning.isFlagged is True

    def testRaisesOnNonPositiveParameterCountOrWindowSize(self):
        with pytest.raises(ValueError):
            evaluateOverfittingWarning(100.0, 90.0, numberOfTunableParameters=0, inSampleWindowSizeInTicks=10)
        with pytest.raises(ValueError):
            evaluateOverfittingWarning(100.0, 90.0, numberOfTunableParameters=1, inSampleWindowSizeInTicks=0)

    def testReturnsOverfittingWarningDataclassShape(self):
        warning = evaluateOverfittingWarning(100.0, 90.0, numberOfTunableParameters=1, inSampleWindowSizeInTicks=100)
        assert isinstance(warning, OverfittingWarning)
