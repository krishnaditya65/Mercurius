"""Pairs trading template: z-score mean-reversion reference strategy. See
ARCHITECTURE.md §7 and FEATURES.md §7 — "Pairs trading template (z-score
mean reversion) as reference strategy".

The classic pairs-trading setup trades TWO correlated instruments (long
one leg, short the other) against their price SPREAD. This module treats
the spread itself as the tradable series fed into
`backtestRunner.runDeterministicEventDrivenBacktest` — i.e.
`positionQuantity > 0` in the resulting `PortfolioState` means "long the
spread" (long the cheap leg / short the rich leg in a real two-instrument
implementation), and `positionQuantity < 0` means the opposite. This is a
deliberate simplification consistent with quant-engine's research-tier
scope: it keeps the reference strategy directly compatible with the
single-instrument backtest runner without inventing a second, more
complex multi-instrument portfolio model. A production pairs-trading
system would size the two legs individually (typically hedge-ratio
weighted); that leg-sizing logic is intentionally out of scope here.

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

import math

from quantengine.backtesting.backtestRunner import PortfolioState, TradeAction, TradeDecision
from quantengine.backtesting.tickStore import HistoricalPriceTick


def calculateRollingMeanAndStandardDeviation(
    priceHistory: list[float], lookbackWindowSize: int
) -> tuple[float, float]:
    """Mean and population standard deviation of the last
    `lookbackWindowSize` values in `priceHistory` (or all of it, if
    shorter). Raises `ValueError` on an empty `priceHistory`.
    """
    if not priceHistory:
        raise ValueError("priceHistory must contain at least one observation")
    windowValues = priceHistory[-lookbackWindowSize:]
    windowMean = sum(windowValues) / len(windowValues)
    sumOfSquaredDeviations = sum((oneValue - windowMean) ** 2 for oneValue in windowValues)
    windowStandardDeviation = math.sqrt(sumOfSquaredDeviations / len(windowValues))
    return windowMean, windowStandardDeviation


def calculateSpreadSeriesBetweenTwoPriceSeries(
    firstInstrumentPrices: list[float], secondInstrumentPrices: list[float]
) -> list[float]:
    """Simple price spread (first minus second) between two equal-length
    synchronized price series — the input a real two-leg pairs-trading
    strategy would feed into the z-score logic below. A production
    version would typically use a hedge-ratio-weighted spread (e.g. from
    a rolling OLS regression) rather than a raw 1:1 difference; the raw
    difference is the honest, simple baseline this module implements.
    """
    if len(firstInstrumentPrices) != len(secondInstrumentPrices):
        raise ValueError("firstInstrumentPrices and secondInstrumentPrices must be the same length")
    return [first - second for first, second in zip(firstInstrumentPrices, secondInstrumentPrices)]


class ZScoreMeanReversionPairsTradingStrategy:
    """A stateful strategy callback compatible with
    `backtestRunner.StrategyCallback` — instances are callable as
    `strategy(currentTick, portfolioState) -> TradeDecision`.

    Maintains its own rolling price history internally (appended once per
    call, in tick order — the SAME source of determinism the backtest
    runner itself relies on: no randomness, no wall-clock, pure function
    of the ticks seen so far).

    Rules, evaluated once per tick, using the rolling z-score of the
    current spread price against its own trailing `lookbackWindowSize`
    history:

    - Flat and z-score > `entryZScoreThreshold`: spread is abnormally
      HIGH -> expect reversion DOWN -> SELL (short the spread).
    - Flat and z-score < -`entryZScoreThreshold`: spread is abnormally
      LOW -> expect reversion UP -> BUY (long the spread).
    - Long a spread position and z-score has reverted back above
      `-exitZScoreThreshold`: close the long (SELL the full position).
    - Short a spread position and z-score has reverted back below
      `exitZScoreThreshold`: close the short (BUY back the full position).
    - Otherwise: HOLD.

    `exitZScoreThreshold` must be smaller than `entryZScoreThreshold` —
    otherwise a position could never legitimately exit before another
    entry signal fires (this is enforced in `__init__`).
    """

    def __init__(
        self,
        lookbackWindowSize: int,
        entryZScoreThreshold: float,
        exitZScoreThreshold: float,
        tradeQuantity: float,
    ) -> None:
        if lookbackWindowSize < 2:
            raise ValueError("lookbackWindowSize must be at least 2 to compute a standard deviation")
        if entryZScoreThreshold <= exitZScoreThreshold:
            raise ValueError("entryZScoreThreshold must be strictly greater than exitZScoreThreshold")
        if tradeQuantity <= 0:
            raise ValueError("tradeQuantity must be strictly positive")

        self.lookbackWindowSize = lookbackWindowSize
        self.entryZScoreThreshold = entryZScoreThreshold
        self.exitZScoreThreshold = exitZScoreThreshold
        self.tradeQuantity = tradeQuantity
        self._observedSpreadPriceHistory: list[float] = []

    def __call__(self, currentTick: HistoricalPriceTick, portfolioState: PortfolioState) -> TradeDecision:
        self._observedSpreadPriceHistory.append(currentTick.price)

        if len(self._observedSpreadPriceHistory) < self.lookbackWindowSize:
            return TradeDecision(TradeAction.HOLD)

        windowMean, windowStandardDeviation = calculateRollingMeanAndStandardDeviation(
            self._observedSpreadPriceHistory, self.lookbackWindowSize
        )
        if windowStandardDeviation == 0.0:
            return TradeDecision(TradeAction.HOLD)

        currentZScore = (currentTick.price - windowMean) / windowStandardDeviation

        isFlat = portfolioState.positionQuantity == 0
        isLong = portfolioState.positionQuantity > 0
        isShort = portfolioState.positionQuantity < 0

        if isFlat and currentZScore > self.entryZScoreThreshold:
            return TradeDecision(TradeAction.SELL, self.tradeQuantity)
        if isFlat and currentZScore < -self.entryZScoreThreshold:
            return TradeDecision(TradeAction.BUY, self.tradeQuantity)
        if isLong and currentZScore > -self.exitZScoreThreshold:
            return TradeDecision(TradeAction.SELL, abs(portfolioState.positionQuantity))
        if isShort and currentZScore < self.exitZScoreThreshold:
            return TradeDecision(TradeAction.BUY, abs(portfolioState.positionQuantity))

        return TradeDecision(TradeAction.HOLD)
