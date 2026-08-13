"""Strategy deployment pipeline: backtest -> paper -> live promotion
gates. See ARCHITECTURE.md §7 ("Algorithmic Trading & Backtesting") and
FEATURES.md §7 — "Strategy deployment pipeline: backtest -> paper -> live
promotion gates".

A real state machine with four states:

    BACKTESTING -> PAPER_TRADING -> LIVE
         \\                \\
          -> REJECTED  <---'

`BACKTESTING -> PAPER_TRADING` requires a `backtestRunner.BacktestResult`
(the existing, real `backtesting/backtestRunner.py` — reused, not
reimplemented) meeting a configurable minimum bar: positive total P&L and
a minimum number of executed trades. `PAPER_TRADING -> LIVE` requires a
separate `PaperTradingTrackRecord` input meeting its own bar. Both sets
of default thresholds are ILLUSTRATIVE, documented gates — not
regulatory/compliance-calibrated numbers.

`services/oms-gateway` is building the REAL paper-trading execution path
in a parallel lane (per this module's task description) — this module
does not call into it. `PaperTradingTrackRecord` here is a plain,
caller-supplied result shape "compatible" with whatever oms-gateway
eventually produces; integrating the two is explicitly out of scope for
this pass.

A strategy that fails a gate transitions to the terminal REJECTED state
with a clear, structured reason — never silently stuck in its prior
state.

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum

from quantengine.backtesting.backtestRunner import BacktestResult, TradeAction


class StrategyLifecycleState(Enum):
    BACKTESTING = "BACKTESTING"
    PAPER_TRADING = "PAPER_TRADING"
    LIVE = "LIVE"
    REJECTED = "REJECTED"


@dataclass(frozen=True)
class PaperTradingTrackRecord:
    """Compatible-shaped input for a paper-trading result — NOT produced
    by this module. A caller (eventually oms-gateway's real paper-trading
    path) supplies this after running the strategy against paper/
    simulated fills for some track-record window.
    """

    totalTradeCount: int
    totalProfitAndLoss: float
    winningTradeCount: int

    def calculateWinRate(self) -> float:
        if self.totalTradeCount == 0:
            return 0.0
        return self.winningTradeCount / self.totalTradeCount


@dataclass(frozen=True)
class PromotionGateEvaluation:
    passed: bool
    reason: str


def evaluateBacktestPromotionGate(
    backtestResult: BacktestResult,
    minimumTotalProfitAndLoss: float = 0.0,
    minimumTradeCount: int = 5,
) -> PromotionGateEvaluation:
    """Illustrative, documented promotion gate from BACKTESTING to
    PAPER_TRADING: the backtest's final total P&L (final total equity
    minus the starting cash balance implied by the FIRST equity-curve
    point's cash-plus-position value before any trade) must exceed
    `minimumTotalProfitAndLoss`, AND the number of executed (non-HOLD)
    trades must be at least `minimumTradeCount` — a strategy that never
    actually traded, or only traded once or twice, hasn't demonstrated
    enough of a track record to promote, regardless of P&L.
    """
    executedTradeCount = sum(
        1 for point in backtestResult.equityCurve if point.actionTaken != TradeAction.HOLD
    )

    startingCashBalance = (
        backtestResult.equityCurve[0].cashBalance + backtestResult.equityCurve[0].realizedProfitAndLoss
        if backtestResult.equityCurve
        else backtestResult.finalPortfolioState.cashBalance
    )
    totalProfitAndLoss = backtestResult.finalTotalEquity - _inferStartingEquityFromBacktestResult(backtestResult)

    if executedTradeCount < minimumTradeCount:
        return PromotionGateEvaluation(
            passed=False,
            reason=(
                f"executed trade count {executedTradeCount} is below the minimum required "
                f"{minimumTradeCount} for promotion to PAPER_TRADING"
            ),
        )
    if totalProfitAndLoss <= minimumTotalProfitAndLoss:
        return PromotionGateEvaluation(
            passed=False,
            reason=(
                f"backtest total P&L {totalProfitAndLoss:.4f} does not exceed the minimum required "
                f"{minimumTotalProfitAndLoss:.4f} for promotion to PAPER_TRADING"
            ),
        )
    return PromotionGateEvaluation(
        passed=True,
        reason=(
            f"backtest P&L {totalProfitAndLoss:.4f} > {minimumTotalProfitAndLoss:.4f} and "
            f"{executedTradeCount} trades >= minimum {minimumTradeCount}"
        ),
    )


def _inferStartingEquityFromBacktestResult(backtestResult: BacktestResult) -> float:
    """The backtest runner doesn't store `startingCashBalance` directly on
    `BacktestResult`, so it's inferred from the first equity-curve point:
    before any trade is applied, `totalEquity == startingCashBalance`
    (position is still zero). Falls back to the final portfolio's cash
    balance for an equity curve with zero points (an edge case the
    runner's own tests don't produce, but handled defensively here).
    """
    if not backtestResult.equityCurve:
        return backtestResult.finalPortfolioState.cashBalance

    firstPoint = backtestResult.equityCurve[0]
    if firstPoint.actionTaken == TradeAction.HOLD:
        return firstPoint.totalEquity

    # The very first tick already traded — reconstruct starting equity as
    # cash-before-trade plus zero position value: cashBalance + trade
    # notional (since cash moved by -signedQuantityChange*price on entry).
    signedQuantityChange = (
        firstPoint.quantityTraded if firstPoint.actionTaken == TradeAction.BUY else -firstPoint.quantityTraded
    )
    return firstPoint.cashBalance + signedQuantityChange * firstPoint.price


def evaluatePaperTradingPromotionGate(
    paperTrackRecord: PaperTradingTrackRecord,
    minimumTotalProfitAndLoss: float = 0.0,
    minimumTradeCount: int = 20,
    minimumWinRate: float = 0.40,
) -> PromotionGateEvaluation:
    """Illustrative, documented promotion gate from PAPER_TRADING to
    LIVE: requires a LARGER trade count than the backtest gate (paper
    trading is meant to validate real-world execution behavior over a
    longer track record before risking live capital), positive total
    P&L, and a minimum win rate.
    """
    if paperTrackRecord.totalTradeCount < minimumTradeCount:
        return PromotionGateEvaluation(
            passed=False,
            reason=(
                f"paper-trading trade count {paperTrackRecord.totalTradeCount} is below the minimum "
                f"required {minimumTradeCount} for promotion to LIVE"
            ),
        )
    if paperTrackRecord.totalProfitAndLoss <= minimumTotalProfitAndLoss:
        return PromotionGateEvaluation(
            passed=False,
            reason=(
                f"paper-trading total P&L {paperTrackRecord.totalProfitAndLoss:.4f} does not exceed the "
                f"minimum required {minimumTotalProfitAndLoss:.4f} for promotion to LIVE"
            ),
        )
    winRate = paperTrackRecord.calculateWinRate()
    if winRate < minimumWinRate:
        return PromotionGateEvaluation(
            passed=False,
            reason=(
                f"paper-trading win rate {winRate:.2%} is below the minimum required "
                f"{minimumWinRate:.2%} for promotion to LIVE"
            ),
        )
    return PromotionGateEvaluation(
        passed=True,
        reason=(
            f"paper-trading P&L {paperTrackRecord.totalProfitAndLoss:.4f} > "
            f"{minimumTotalProfitAndLoss:.4f}, {paperTrackRecord.totalTradeCount} trades >= "
            f"minimum {minimumTradeCount}, win rate {winRate:.2%} >= minimum {minimumWinRate:.2%}"
        ),
    )


@dataclass(frozen=True)
class StrategyLifecycleRecord:
    strategyName: str
    currentState: StrategyLifecycleState
    rejectionReason: str | None = None


def createNewStrategyLifecycleRecord(strategyName: str) -> StrategyLifecycleRecord:
    """Every strategy starts its life in BACKTESTING — there is no
    lifecycle path that skips it."""
    if not strategyName:
        raise ValueError("strategyName must be non-empty")
    return StrategyLifecycleRecord(strategyName=strategyName, currentState=StrategyLifecycleState.BACKTESTING)


def promoteStrategyFromBacktestingToPaperTrading(
    record: StrategyLifecycleRecord,
    backtestResult: BacktestResult,
    minimumTotalProfitAndLoss: float = 0.0,
    minimumTradeCount: int = 5,
) -> StrategyLifecycleRecord:
    """Raises `ValueError` if `record.currentState` isn't BACKTESTING —
    this state machine does not allow re-entering BACKTESTING from
    elsewhere or skipping straight to PAPER_TRADING from LIVE/REJECTED.
    Otherwise ALWAYS returns a new record: either PAPER_TRADING (gate
    passed) or REJECTED with `rejectionReason` set (gate failed) — never
    silently stuck in BACKTESTING.
    """
    if record.currentState != StrategyLifecycleState.BACKTESTING:
        raise ValueError(
            f"cannot promote to PAPER_TRADING from state {record.currentState.value} "
            "(must currently be BACKTESTING)"
        )

    gateEvaluation = evaluateBacktestPromotionGate(backtestResult, minimumTotalProfitAndLoss, minimumTradeCount)
    if gateEvaluation.passed:
        return StrategyLifecycleRecord(
            strategyName=record.strategyName, currentState=StrategyLifecycleState.PAPER_TRADING
        )
    return StrategyLifecycleRecord(
        strategyName=record.strategyName,
        currentState=StrategyLifecycleState.REJECTED,
        rejectionReason=gateEvaluation.reason,
    )


def promoteStrategyFromPaperTradingToLive(
    record: StrategyLifecycleRecord,
    paperTrackRecord: PaperTradingTrackRecord,
    minimumTotalProfitAndLoss: float = 0.0,
    minimumTradeCount: int = 20,
    minimumWinRate: float = 0.40,
) -> StrategyLifecycleRecord:
    """Raises `ValueError` if `record.currentState` isn't PAPER_TRADING.
    Otherwise ALWAYS returns a new record: either LIVE (gate passed) or
    REJECTED with `rejectionReason` set (gate failed).
    """
    if record.currentState != StrategyLifecycleState.PAPER_TRADING:
        raise ValueError(
            f"cannot promote to LIVE from state {record.currentState.value} "
            "(must currently be PAPER_TRADING)"
        )

    gateEvaluation = evaluatePaperTradingPromotionGate(
        paperTrackRecord, minimumTotalProfitAndLoss, minimumTradeCount, minimumWinRate
    )
    if gateEvaluation.passed:
        return StrategyLifecycleRecord(strategyName=record.strategyName, currentState=StrategyLifecycleState.LIVE)
    return StrategyLifecycleRecord(
        strategyName=record.strategyName,
        currentState=StrategyLifecycleState.REJECTED,
        rejectionReason=gateEvaluation.reason,
    )
