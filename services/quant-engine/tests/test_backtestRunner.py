import math

import pytest

from quantengine.backtesting.backtestRunner import (
    BacktestResult,
    PortfolioState,
    TradeAction,
    TradeDecision,
    applySignedQuantityChangeToPortfolio,
    runDeterministicEventDrivenBacktest,
)
from quantengine.backtesting.tickStore import HistoricalPriceTick

# --- Hand-worked backtest scenario --------------------------------------
# ticks: (t=1, price=100), (t=2, price=110), (t=3, price=90)
# strategy: BUY 10 @ tick 1, HOLD @ tick 2, SELL 10 @ tick 3
# startingCashBalance = 100000
#
# tick 1 (BUY 10 @ 100):
#   opening a new long position: averageEntryPrice = (0 + 10*100) / 10 = 100
#   cashBalance = 100000 - 10*100 = 99000
#   positionQuantity = 10
#   unrealizedP&L = 10 * (100 - 100) = 0
#   totalEquity = 99000 + 10*100 = 100000
#
# tick 2 (HOLD @ 110):
#   cashBalance unchanged = 99000, positionQuantity unchanged = 10
#   unrealizedP&L = 10 * (110 - 100) = 100
#   totalEquity = 99000 + 10*110 = 99000 + 1100 = 100100
#
# tick 3 (SELL 10 @ 90):
#   closing the full long position: closingQuantity = 10
#   realizedP&L += 10 * (90 - 100) * (+1) = -100
#   cashBalance = 99000 - (-10 * 90) = 99000 + 900 = 99900
#   positionQuantity = 0, averageEntryPrice reset to 0
#   unrealizedP&L = 0 (flat)
#   totalEquity = 99900 + 0 = 99900
HAND_WORKED_TICKS = [
    HistoricalPriceTick(timestamp=1.0, price=100.0),
    HistoricalPriceTick(timestamp=2.0, price=110.0),
    HistoricalPriceTick(timestamp=3.0, price=90.0),
]


def buildHandWorkedBuyHoldSellStrategy():
    decisionsByCallIndex = [
        TradeDecision(TradeAction.BUY, 10.0),
        TradeDecision(TradeAction.HOLD),
        TradeDecision(TradeAction.SELL, 10.0),
    ]
    callIndexHolder = {"index": 0}

    def strategy(currentTick, portfolioState):
        decision = decisionsByCallIndex[callIndexHolder["index"]]
        callIndexHolder["index"] += 1
        return decision

    return strategy


def test_handWorkedBuyHoldSellScenarioMatchesHandComputedEquityCurve():
    result = runDeterministicEventDrivenBacktest(
        HAND_WORKED_TICKS, buildHandWorkedBuyHoldSellStrategy(), startingCashBalance=100_000.0
    )
    assert isinstance(result, BacktestResult)
    assert len(result.equityCurve) == 3

    point1, point2, point3 = result.equityCurve

    assert point1.cashBalance == pytest.approx(99000.0)
    assert point1.positionQuantity == pytest.approx(10.0)
    assert point1.unrealizedProfitAndLoss == pytest.approx(0.0)
    assert point1.totalEquity == pytest.approx(100000.0)

    assert point2.cashBalance == pytest.approx(99000.0)
    assert point2.unrealizedProfitAndLoss == pytest.approx(100.0)
    assert point2.totalEquity == pytest.approx(100100.0)

    assert point3.cashBalance == pytest.approx(99900.0)
    assert point3.positionQuantity == pytest.approx(0.0)
    assert point3.realizedProfitAndLoss == pytest.approx(-100.0)
    assert point3.unrealizedProfitAndLoss == pytest.approx(0.0)
    assert point3.totalEquity == pytest.approx(99900.0)

    assert result.finalTotalEquity == pytest.approx(99900.0)
    assert result.finalPortfolioState.realizedProfitAndLoss == pytest.approx(-100.0)


def test_backtestIsDeterministicAcrossRepeatedRuns():
    # Same ticks + same strategy (freshly constructed, no shared mutable
    # state between runs) must produce byte-for-byte identical results.
    firstRunResult = runDeterministicEventDrivenBacktest(
        HAND_WORKED_TICKS, buildHandWorkedBuyHoldSellStrategy(), startingCashBalance=100_000.0
    )
    secondRunResult = runDeterministicEventDrivenBacktest(
        HAND_WORKED_TICKS, buildHandWorkedBuyHoldSellStrategy(), startingCashBalance=100_000.0
    )

    assert firstRunResult.equityCurve == secondRunResult.equityCurve
    assert firstRunResult.finalPortfolioState == secondRunResult.finalPortfolioState
    assert firstRunResult.finalTotalEquity == secondRunResult.finalTotalEquity


def test_alwaysHoldStrategyLeavesCashAndPositionUnchanged():
    def alwaysHoldStrategy(currentTick, portfolioState):
        return TradeDecision(TradeAction.HOLD)

    result = runDeterministicEventDrivenBacktest(HAND_WORKED_TICKS, alwaysHoldStrategy, startingCashBalance=5000.0)
    assert result.finalPortfolioState.cashBalance == pytest.approx(5000.0)
    assert result.finalPortfolioState.positionQuantity == pytest.approx(0.0)
    assert result.finalTotalEquity == pytest.approx(5000.0)


def test_tradeDecisionRejectsNonPositiveQuantityForBuyOrSell():
    with pytest.raises(ValueError):
        TradeDecision(TradeAction.BUY, 0.0)
    with pytest.raises(ValueError):
        TradeDecision(TradeAction.SELL, -5.0)


def test_positionFlipsThroughZeroOpensNewPositionAtTradePrice():
    # Long 5 @ 100, then SELL 8 @ 120 -> closes the 5 long (realizing P&L),
    # then opens a fresh short 3 @ 120.
    state = PortfolioState(cashBalance=0.0)
    applySignedQuantityChangeToPortfolio(state, 5.0, 100.0)
    assert state.positionQuantity == pytest.approx(5.0)
    assert state.averageEntryPrice == pytest.approx(100.0)

    applySignedQuantityChangeToPortfolio(state, -8.0, 120.0)
    assert state.positionQuantity == pytest.approx(-3.0)
    # realized P&L from closing the 5 long units: 5 * (120-100) * 1 = 100
    assert state.realizedProfitAndLoss == pytest.approx(100.0)
    # the new short 3 units opened at the flip price, 120
    assert state.averageEntryPrice == pytest.approx(120.0)


def test_partialCloseLeavesAverageEntryPriceUnchanged():
    state = PortfolioState(cashBalance=0.0)
    applySignedQuantityChangeToPortfolio(state, 10.0, 50.0)
    applySignedQuantityChangeToPortfolio(state, -4.0, 60.0)
    assert state.positionQuantity == pytest.approx(6.0)
    assert state.averageEntryPrice == pytest.approx(50.0)
    assert state.realizedProfitAndLoss == pytest.approx(4 * (60 - 50))


def test_addingToExistingLongPositionBlendsAverageEntryPrice():
    state = PortfolioState(cashBalance=0.0)
    applySignedQuantityChangeToPortfolio(state, 10.0, 100.0)
    applySignedQuantityChangeToPortfolio(state, 10.0, 120.0)
    # blended average: (10*100 + 10*120) / 20 = 2200/20 = 110
    assert state.averageEntryPrice == pytest.approx(110.0)
    assert state.positionQuantity == pytest.approx(20.0)


def test_positionFlatChecksToleratesFloatDustResidualAfterFullRoundTrip():
    # Ten BUYs of 0.1 accumulated via repeated float += land at
    # 0.9999999999999999 (not exactly 1.0); selling exactly that back
    # then lands positionQuantity at a tiny float-dust residual like
    # -1.1102230246251565e-16 (not exactly 0.0) rather than a clean zero.
    # A genuinely fully-closed position must still be treated as FLAT:
    # averageEntryPrice reset to 0.0, and zero unrealized P&L regardless
    # of the current market price.
    portfolioState = PortfolioState(cashBalance=100_000.0)
    for _ in range(10):
        applySignedQuantityChangeToPortfolio(portfolioState, 0.1, 100.0)
    # Sell the economically-correct "1.0" (what ten 0.1 buys SHOULD sum
    # to), not portfolioState.positionQuantity itself — selling the
    # literal accumulated float back against itself would trivially net
    # to exact zero and wouldn't reproduce the accumulation-error dust.
    applySignedQuantityChangeToPortfolio(portfolioState, -1.0, 100.0)

    assert portfolioState.positionQuantity != 0.0  # genuine float dust, not exactly zero
    assert portfolioState.averageEntryPrice == 0.0
    assert portfolioState.calculateUnrealizedProfitAndLoss(500.0) == 0.0

    # Re-opening a brand-new position from this float-dust-flat state
    # must be treated as OPENING (fresh averageEntryPrice at the new
    # trade price), not as "reducing" the residual dust position.
    applySignedQuantityChangeToPortfolio(portfolioState, 5.0, 200.0)
    assert portfolioState.averageEntryPrice == pytest.approx(200.0)
