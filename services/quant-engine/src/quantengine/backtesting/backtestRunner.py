"""A minimal but REAL, deterministic, event-driven backtest runner. See
ARCHITECTURE.md §7 ("Algorithmic Trading & Backtesting") and
FEATURES.md §7 — "Historical tick data store + backtest runner (Python
strategy SDK)".

The runner replays a chronologically-ordered tick series one tick at a
time, calling a caller-supplied strategy callback with the current tick
and a read-by-convention `PortfolioState` snapshot. The callback returns
a `TradeDecision` (BUY/SELL/HOLD + quantity); the runner applies that
trade using a standard weighted-average-cost accounting rule, tracks
cash/position/realized-P&L/unrealized-P&L deterministically, and records
one `BacktestEquityCurvePoint` per tick.

Determinism: this loop does no I/O, no wall-clock reads, no randomness,
and no dict-ordering-dependent iteration (ticks are processed as a plain
list, in the order given) — replaying the exact same tick list through
the exact same strategy callback MUST produce byte-for-byte identical
output every time. `tests/test_backtestRunner.py` proves this directly by
running the same backtest twice and asserting the results are equal.

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum
from typing import Callable

from quantengine.backtesting.tickStore import HistoricalPriceTick


class TradeAction(Enum):
    BUY = "BUY"
    SELL = "SELL"
    HOLD = "HOLD"


@dataclass(frozen=True)
class TradeDecision:
    action: TradeAction
    quantity: float = 0.0

    def __post_init__(self) -> None:
        if self.action != TradeAction.HOLD and self.quantity <= 0:
            raise ValueError("a BUY or SELL TradeDecision must have a strictly positive quantity")


@dataclass
class PortfolioState:
    """Mutable portfolio state threaded through the backtest loop.
    Strategies receive this object read-only BY CONVENTION (the runner is
    the sole mutator, via `applySignedQuantityChangeToPortfolio` below) —
    nothing enforces immutability at the language level, but a strategy
    that mutates it directly is not using the SDK as intended.
    """

    cashBalance: float
    positionQuantity: float = 0.0
    averageEntryPrice: float = 0.0
    realizedProfitAndLoss: float = 0.0

    def calculateUnrealizedProfitAndLoss(self, currentPrice: float) -> float:
        if self.positionQuantity == 0:
            return 0.0
        return self.positionQuantity * (currentPrice - self.averageEntryPrice)

    def calculateTotalEquity(self, currentPrice: float) -> float:
        return self.cashBalance + self.positionQuantity * currentPrice


def applySignedQuantityChangeToPortfolio(
    portfolioState: PortfolioState, signedQuantityChange: float, tradePrice: float
) -> None:
    """Applies one trade (positive `signedQuantityChange` = BUY, negative
    = SELL) to `portfolioState` in place, using standard weighted-average-
    cost accounting:

    - Opening a new position, or adding to an existing position in the
      SAME direction: `averageEntryPrice` becomes the quantity-weighted
      blend of the old cost basis and the new trade.
    - Reducing (but not flipping) an existing position: `averageEntryPrice`
      is UNCHANGED (the cost basis of the remaining shares/units doesn't
      move just because some were sold), and the closed portion's P&L is
      realized into `realizedProfitAndLoss`.
    - Flipping a position through zero (e.g. long 5 -> sell 8 -> short 3):
      the portion up to zero realizes P&L against the old cost basis, and
      the remainder opens a brand-new position at `tradePrice`.

    Cash always moves by `-signedQuantityChange * tradePrice` — buying
    spends cash, selling (including opening a short) receives cash. This
    holds uniformly across all three cases above.
    """
    if signedQuantityChange == 0:
        return

    oldPositionQuantity = portfolioState.positionQuantity
    newPositionQuantity = oldPositionQuantity + signedQuantityChange

    isOpeningOrAddingInSameDirection = (
        oldPositionQuantity == 0
        or (oldPositionQuantity > 0 and signedQuantityChange > 0)
        or (oldPositionQuantity < 0 and signedQuantityChange < 0)
    )

    if isOpeningOrAddingInSameDirection:
        totalCostBasisBefore = oldPositionQuantity * portfolioState.averageEntryPrice
        totalCostBasisAdded = signedQuantityChange * tradePrice
        portfolioState.averageEntryPrice = (totalCostBasisBefore + totalCostBasisAdded) / newPositionQuantity
    else:
        closingQuantityMagnitude = min(abs(signedQuantityChange), abs(oldPositionQuantity))
        positionDirectionSign = 1.0 if oldPositionQuantity > 0 else -1.0
        portfolioState.realizedProfitAndLoss += (
            closingQuantityMagnitude * (tradePrice - portfolioState.averageEntryPrice) * positionDirectionSign
        )

        if newPositionQuantity == 0:
            portfolioState.averageEntryPrice = 0.0
        elif (newPositionQuantity > 0) != (oldPositionQuantity > 0):
            # Flipped through zero — the remainder is a brand-new position
            # opened at the current trade price.
            portfolioState.averageEntryPrice = tradePrice
        # else: partial close in the same direction — cost basis unchanged.

    portfolioState.cashBalance -= signedQuantityChange * tradePrice
    portfolioState.positionQuantity = newPositionQuantity


@dataclass(frozen=True)
class BacktestEquityCurvePoint:
    timestamp: float
    price: float
    actionTaken: TradeAction
    quantityTraded: float
    cashBalance: float
    positionQuantity: float
    realizedProfitAndLoss: float
    unrealizedProfitAndLoss: float
    totalEquity: float


@dataclass(frozen=True)
class BacktestResult:
    equityCurve: list[BacktestEquityCurvePoint]
    finalPortfolioState: PortfolioState

    @property
    def finalTotalEquity(self) -> float:
        if not self.equityCurve:
            return self.finalPortfolioState.cashBalance
        return self.equityCurve[-1].totalEquity


StrategyCallback = Callable[[HistoricalPriceTick, PortfolioState], TradeDecision]


def runDeterministicEventDrivenBacktest(
    orderedTicks: list[HistoricalPriceTick],
    strategyCallback: StrategyCallback,
    startingCashBalance: float = 100_000.0,
) -> BacktestResult:
    """Replays `orderedTicks` (must already be in ascending-timestamp
    order — this function does not re-sort, to keep the loop's behavior
    transparent and O(n)) one at a time. For each tick:

    1. Calls `strategyCallback(currentTick, portfolioState)` to get a
       `TradeDecision`.
    2. Applies the trade (if BUY/SELL) via
       `applySignedQuantityChangeToPortfolio`, using the tick's price as
       the fill price — a simplification (no slippage/spread modeling),
       explicitly research-tier like the rest of quant-engine.
    3. Records one `BacktestEquityCurvePoint` marking the portfolio to
       market at the tick's price.

    Purely deterministic: no randomness, no wall-clock, no I/O — replaying
    the same `orderedTicks` through the same `strategyCallback` always
    produces an identical `BacktestResult`.
    """
    portfolioState = PortfolioState(cashBalance=startingCashBalance)
    equityCurve: list[BacktestEquityCurvePoint] = []

    for currentTick in orderedTicks:
        decision = strategyCallback(currentTick, portfolioState)

        if decision.action == TradeAction.BUY:
            signedQuantityChange = decision.quantity
        elif decision.action == TradeAction.SELL:
            signedQuantityChange = -decision.quantity
        else:
            signedQuantityChange = 0.0

        if signedQuantityChange != 0.0:
            applySignedQuantityChangeToPortfolio(portfolioState, signedQuantityChange, currentTick.price)

        equityCurve.append(
            BacktestEquityCurvePoint(
                timestamp=currentTick.timestamp,
                price=currentTick.price,
                actionTaken=decision.action,
                quantityTraded=decision.quantity if decision.action != TradeAction.HOLD else 0.0,
                cashBalance=portfolioState.cashBalance,
                positionQuantity=portfolioState.positionQuantity,
                realizedProfitAndLoss=portfolioState.realizedProfitAndLoss,
                unrealizedProfitAndLoss=portfolioState.calculateUnrealizedProfitAndLoss(currentTick.price),
                totalEquity=portfolioState.calculateTotalEquity(currentTick.price),
            )
        )

    return BacktestResult(equityCurve=equityCurve, finalPortfolioState=portfolioState)
