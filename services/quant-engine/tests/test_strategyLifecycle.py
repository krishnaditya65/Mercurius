import pytest

from quantengine.backtesting.backtestRunner import (
    PortfolioState,
    TradeAction,
    TradeDecision,
    runDeterministicEventDrivenBacktest,
)
from quantengine.backtesting.tickStore import HistoricalPriceTick
from quantengine.strategyLifecycle import (
    PaperTradingTrackRecord,
    PromotionGateEvaluation,
    StrategyLifecycleRecord,
    StrategyLifecycleState,
    createNewStrategyLifecycleRecord,
    evaluateBacktestPromotionGate,
    evaluatePaperTradingPromotionGate,
    promoteStrategyFromBacktestingToPaperTrading,
    promoteStrategyFromPaperTradingToLive,
)


def buildProfitableBacktestResult():
    """buy 10 @ 100 on tick 1, sell 10 @ 110 on tick 2, hold on tick 3 ->
    realized P&L = 10*(110-100) = 100, executed trade count = 2.
    """
    ticks = [
        HistoricalPriceTick(timestamp=1.0, price=100.0),
        HistoricalPriceTick(timestamp=2.0, price=110.0),
        HistoricalPriceTick(timestamp=3.0, price=110.0),
    ]
    decisions = [
        TradeDecision(TradeAction.BUY, 10.0),
        TradeDecision(TradeAction.SELL, 10.0),
        TradeDecision(TradeAction.HOLD),
    ]
    callIndex = {"i": 0}

    def strategy(tick: HistoricalPriceTick, portfolioState: PortfolioState) -> TradeDecision:
        decision = decisions[callIndex["i"]]
        callIndex["i"] += 1
        return decision

    return runDeterministicEventDrivenBacktest(ticks, strategy, startingCashBalance=100_000.0)


def buildUnprofitableBacktestResult():
    """buy 10 @ 100, sell 10 @ 90 -> loss of 100."""
    ticks = [
        HistoricalPriceTick(timestamp=1.0, price=100.0),
        HistoricalPriceTick(timestamp=2.0, price=90.0),
    ]
    decisions = [TradeDecision(TradeAction.BUY, 10.0), TradeDecision(TradeAction.SELL, 10.0)]
    callIndex = {"i": 0}

    def strategy(tick, portfolioState):
        decision = decisions[callIndex["i"]]
        callIndex["i"] += 1
        return decision

    return runDeterministicEventDrivenBacktest(ticks, strategy, startingCashBalance=100_000.0)


def buildAllHoldBacktestResult():
    ticks = [HistoricalPriceTick(timestamp=1.0, price=100.0), HistoricalPriceTick(timestamp=2.0, price=100.0)]

    def strategy(tick, portfolioState):
        return TradeDecision(TradeAction.HOLD)

    return runDeterministicEventDrivenBacktest(ticks, strategy, startingCashBalance=100_000.0)


# --- Backtest promotion gate ---------------------------------------------


def test_backtestGatePassesForProfitableResultWithEnoughTrades():
    result = buildProfitableBacktestResult()
    evaluation = evaluateBacktestPromotionGate(result, minimumTotalProfitAndLoss=0.0, minimumTradeCount=2)
    assert isinstance(evaluation, PromotionGateEvaluation)
    assert evaluation.passed is True


def test_backtestGateFailsForUnprofitableResult():
    result = buildUnprofitableBacktestResult()
    evaluation = evaluateBacktestPromotionGate(result, minimumTotalProfitAndLoss=0.0, minimumTradeCount=1)
    assert evaluation.passed is False
    assert "P&L" in evaluation.reason


def test_backtestGateFailsWhenTradeCountTooLow():
    result = buildProfitableBacktestResult()
    evaluation = evaluateBacktestPromotionGate(result, minimumTotalProfitAndLoss=0.0, minimumTradeCount=10)
    assert evaluation.passed is False
    assert "trade count" in evaluation.reason


def test_backtestGateFailsForAllHoldStrategy():
    result = buildAllHoldBacktestResult()
    evaluation = evaluateBacktestPromotionGate(result, minimumTotalProfitAndLoss=0.0, minimumTradeCount=1)
    assert evaluation.passed is False


# --- Paper-trading promotion gate -----------------------------------------


def test_paperTradingGatePassesForGoodTrackRecord():
    trackRecord = PaperTradingTrackRecord(totalTradeCount=25, totalProfitAndLoss=500.0, winningTradeCount=15)
    evaluation = evaluatePaperTradingPromotionGate(trackRecord)
    assert evaluation.passed is True


def test_paperTradingGateFailsOnInsufficientTradeCount():
    trackRecord = PaperTradingTrackRecord(totalTradeCount=5, totalProfitAndLoss=500.0, winningTradeCount=4)
    evaluation = evaluatePaperTradingPromotionGate(trackRecord, minimumTradeCount=20)
    assert evaluation.passed is False
    assert "trade count" in evaluation.reason


def test_paperTradingGateFailsOnNonPositivePnl():
    trackRecord = PaperTradingTrackRecord(totalTradeCount=25, totalProfitAndLoss=-10.0, winningTradeCount=10)
    evaluation = evaluatePaperTradingPromotionGate(trackRecord)
    assert evaluation.passed is False
    assert "P&L" in evaluation.reason


def test_paperTradingGateFailsOnLowWinRate():
    trackRecord = PaperTradingTrackRecord(totalTradeCount=25, totalProfitAndLoss=500.0, winningTradeCount=2)
    evaluation = evaluatePaperTradingPromotionGate(trackRecord, minimumWinRate=0.40)
    assert evaluation.passed is False
    assert "win rate" in evaluation.reason


def test_paperTrackRecordWinRateHandlesZeroTrades():
    trackRecord = PaperTradingTrackRecord(totalTradeCount=0, totalProfitAndLoss=0.0, winningTradeCount=0)
    assert trackRecord.calculateWinRate() == 0.0


# --- Full state machine ----------------------------------------------------


def test_newStrategyLifecycleRecordStartsInBacktesting():
    record = createNewStrategyLifecycleRecord("MyStrategy")
    assert record.currentState == StrategyLifecycleState.BACKTESTING
    assert record.rejectionReason is None


def test_newStrategyLifecycleRecordRejectsEmptyName():
    with pytest.raises(ValueError):
        createNewStrategyLifecycleRecord("")


def test_fullHappyPathPromotionFromBacktestingThroughLive():
    record = createNewStrategyLifecycleRecord("MeanReversionV1")
    backtestResult = buildProfitableBacktestResult()

    record = promoteStrategyFromBacktestingToPaperTrading(
        record, backtestResult, minimumTotalProfitAndLoss=0.0, minimumTradeCount=2
    )
    assert record.currentState == StrategyLifecycleState.PAPER_TRADING
    assert record.rejectionReason is None

    paperTrackRecord = PaperTradingTrackRecord(totalTradeCount=30, totalProfitAndLoss=1000.0, winningTradeCount=18)
    record = promoteStrategyFromPaperTradingToLive(record, paperTrackRecord)
    assert record.currentState == StrategyLifecycleState.LIVE
    assert record.rejectionReason is None


def test_backtestingPromotionRejectsAndSetsReasonOnFailedGate():
    record = createNewStrategyLifecycleRecord("BadStrategy")
    unprofitableResult = buildUnprofitableBacktestResult()
    record = promoteStrategyFromBacktestingToPaperTrading(record, unprofitableResult, minimumTradeCount=1)
    assert record.currentState == StrategyLifecycleState.REJECTED
    assert record.rejectionReason is not None
    assert "P&L" in record.rejectionReason


def test_paperTradingPromotionRejectsAndSetsReasonOnFailedGate():
    record = StrategyLifecycleRecord(strategyName="X", currentState=StrategyLifecycleState.PAPER_TRADING)
    weakTrackRecord = PaperTradingTrackRecord(totalTradeCount=25, totalProfitAndLoss=-5.0, winningTradeCount=10)
    record = promoteStrategyFromPaperTradingToLive(record, weakTrackRecord)
    assert record.currentState == StrategyLifecycleState.REJECTED
    assert record.rejectionReason is not None


def test_cannotPromoteToPaperTradingFromNonBacktestingState():
    record = StrategyLifecycleRecord(strategyName="X", currentState=StrategyLifecycleState.LIVE)
    with pytest.raises(ValueError):
        promoteStrategyFromBacktestingToPaperTrading(record, buildProfitableBacktestResult())


def test_cannotPromoteToLiveFromNonPaperTradingState():
    record = StrategyLifecycleRecord(strategyName="X", currentState=StrategyLifecycleState.BACKTESTING)
    trackRecord = PaperTradingTrackRecord(totalTradeCount=25, totalProfitAndLoss=500.0, winningTradeCount=15)
    with pytest.raises(ValueError):
        promoteStrategyFromPaperTradingToLive(record, trackRecord)


def test_cannotPromoteAStrategyThatIsAlreadyRejected():
    record = StrategyLifecycleRecord(
        strategyName="X", currentState=StrategyLifecycleState.REJECTED, rejectionReason="already rejected"
    )
    with pytest.raises(ValueError):
        promoteStrategyFromBacktestingToPaperTrading(record, buildProfitableBacktestResult())
