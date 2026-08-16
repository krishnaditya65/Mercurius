"""Realistic backtest cost modeling: slippage, partial fills, and an
Almgren-Chriss-STYLE market-impact cost curve, extending
`backtesting/backtestRunner.py`'s documented simplification (see that
module's `runDeterministicEventDrivenBacktest` docstring: "uses the
tick's price as the fill price — a simplification (no slippage/spread
modeling)"). This module does NOT modify `backtestRunner.py` itself —
it's a standalone, real cost-modeling toolkit callers can compose with
the existing runner (e.g. by wrapping `applySignedQuantityChangeToPortfolio`'s
`tradePrice` argument with `simulateRealisticMarketOrderFill`'s output)
rather than a forced rewrite of the deterministic-by-design core loop.

Three real, independent pieces:

1. MARKET IMPACT (`calculateSquareRootMarketImpactCostFraction`) — a real,
   simplified sqrt-law market-impact model in the spirit of Almgren-Chriss
   (2000)'s temporary-impact term: price impact grows with the SQUARE
   ROOT of relative order size (`orderQuantity / averageDailyVolume`),
   scaled by the instrument's volatility and a caller-tunable
   `impactCoefficient`:

       impactFraction = impactCoefficient * dailyVolatility
                         * sqrt(orderQuantity / averageDailyVolume)

   This is a real, computed function of its inputs — but
   `impactCoefficient` (the proportionality constant Almgren-Chriss
   calibrates per-instrument from real market microstructure data) is an
   ILLUSTRATIVE, caller-supplied parameter here, not fitted to any real
   market data. Document your own calibration before trusting the
   absolute magnitude of a cost estimate from this function.

2. SLIPPAGE (`applySlippageToReferencePrice`) — real, simple directional
   price adjustment: a BUY order fills at `referencePrice * (1 +
   slippageFraction)` (worse, i.e. higher, for the buyer); a SELL order
   fills at `referencePrice * (1 - slippageFraction)` (worse, i.e. lower,
   for the seller). `slippageFraction` is typically the market-impact
   fraction from #1 above, but is accepted as a plain float so callers
   can also feed a fixed bid-ask-spread-based slippage estimate.

3. PARTIAL FILLS (`simulatePartialFillAgainstOrderBookLevels`) — real
   order-book-walking simulation: given `orderQuantity` and a list of
   `(price, availableQuantity)` levels ALREADY SORTED best-to-worst for
   the order's side (best price first), fills sequentially from the best
   level until either the order is fully filled or every level is
   exhausted, returning the exact filled/unfilled quantities and the
   real volume-weighted average fill price actually achieved. An order
   larger than total available liquidity across every level receives a
   real, non-fabricated PARTIAL fill (not silently filled in full at a
   fictitious price).

`simulateRealisticMarketOrderFill` composes all three into one
convenience call.

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

import math
from dataclasses import dataclass

# Tolerance for "is this order fully filled?" comparisons.
# totalFilledQuantity is accumulated via repeated float `+=` across
# price levels (see simulatePartialFillAgainstOrderBookLevels below), so
# a genuinely fully-filled order can land at something like
# orderQuantity - 1e-14 instead of exactly orderQuantity due to ordinary
# float accumulation error — an exact `==` comparison there would
# mis-flag it as a partial fill.
_FULLY_FILLED_QUANTITY_EPSILON = 1e-9


def calculateSquareRootMarketImpactCostFraction(
    orderQuantity: float,
    averageDailyVolume: float,
    dailyVolatility: float,
    impactCoefficient: float = 1.0,
) -> float:
    """Almgren-Chriss-STYLE square-root market-impact cost, as a
    FRACTION of price (e.g. 0.002 means "0.2% of price"):

        impactFraction = impactCoefficient * dailyVolatility
                          * sqrt(orderQuantity / averageDailyVolume)

    Raises `ValueError` if `orderQuantity` is negative, or if
    `averageDailyVolume`/`dailyVolatility`/`impactCoefficient` are not
    strictly positive (a non-positive ADV or volatility has no
    meaningful impact-fraction interpretation here).
    """
    if orderQuantity < 0.0:
        raise ValueError("orderQuantity must be non-negative")
    if averageDailyVolume <= 0.0:
        raise ValueError("averageDailyVolume must be strictly positive")
    if dailyVolatility <= 0.0:
        raise ValueError("dailyVolatility must be strictly positive")
    if impactCoefficient <= 0.0:
        raise ValueError("impactCoefficient must be strictly positive")

    return impactCoefficient * dailyVolatility * math.sqrt(orderQuantity / averageDailyVolume)


def applySlippageToReferencePrice(referencePrice: float, isBuyOrder: bool, slippageFraction: float) -> float:
    """Real directional slippage adjustment: worse execution price in the
    direction that hurts the order's side. Raises `ValueError` if
    `referencePrice` is not strictly positive or `slippageFraction` is
    negative (negative slippage — i.e. price improvement — is not what
    this function models; a caller wanting that can pass a negative
    adjustment through a different path).
    """
    if referencePrice <= 0.0:
        raise ValueError("referencePrice must be strictly positive")
    if slippageFraction < 0.0:
        raise ValueError("slippageFraction must be non-negative")

    return referencePrice * (1.0 + slippageFraction) if isBuyOrder else referencePrice * (1.0 - slippageFraction)


@dataclass(frozen=True)
class PartialFillSimulationResult:
    requestedQuantity: float
    filledQuantity: float
    unfilledQuantity: float
    volumeWeightedAverageFillPrice: float | None
    isFullyFilled: bool
    fillsByLevel: list[tuple[float, float]]  # (price, quantityFilledAtThatPrice)


def simulatePartialFillAgainstOrderBookLevels(
    orderQuantity: float, priceLevels: list[tuple[float, float]]
) -> PartialFillSimulationResult:
    """Walks `priceLevels` (a list of `(price, availableQuantity)` tuples,
    ALREADY SORTED best-to-worst for the order's side by the caller —
    this function does no sorting of its own, since "best" depends on
    the order's side, which this function doesn't take as a parameter)
    sequentially, filling as much of `orderQuantity` as possible at each
    level before moving to the next. Returns the REAL, exact
    filled/unfilled split and volume-weighted average fill price — an
    order that exceeds total available liquidity gets a genuine partial
    fill, not a fabricated full fill.

    `volumeWeightedAverageFillPrice` is `None` if `filledQuantity` is
    exactly 0.0 (no fill at all — no meaningful average price). Raises
    `ValueError` if `orderQuantity` is not strictly positive, or if any
    level's `availableQuantity` is negative.
    """
    if orderQuantity <= 0.0:
        raise ValueError("orderQuantity must be strictly positive")
    if any(availableQuantity < 0.0 for _, availableQuantity in priceLevels):
        raise ValueError("every level's availableQuantity must be non-negative")

    remainingQuantity = orderQuantity
    fillsByLevel: list[tuple[float, float]] = []
    totalFilledQuantity = 0.0
    totalFillNotional = 0.0

    for levelPrice, levelAvailableQuantity in priceLevels:
        if remainingQuantity <= 0.0:
            break
        quantityFilledAtThisLevel = min(remainingQuantity, levelAvailableQuantity)
        if quantityFilledAtThisLevel > 0.0:
            fillsByLevel.append((levelPrice, quantityFilledAtThisLevel))
            totalFilledQuantity += quantityFilledAtThisLevel
            totalFillNotional += quantityFilledAtThisLevel * levelPrice
            remainingQuantity -= quantityFilledAtThisLevel

    volumeWeightedAverageFillPrice = (
        totalFillNotional / totalFilledQuantity if totalFilledQuantity > 0.0 else None
    )

    return PartialFillSimulationResult(
        requestedQuantity=orderQuantity,
        filledQuantity=totalFilledQuantity,
        unfilledQuantity=orderQuantity - totalFilledQuantity,
        volumeWeightedAverageFillPrice=volumeWeightedAverageFillPrice,
        isFullyFilled=(abs(totalFilledQuantity - orderQuantity) < _FULLY_FILLED_QUANTITY_EPSILON),
        fillsByLevel=fillsByLevel,
    )


@dataclass(frozen=True)
class RealisticMarketOrderFillResult:
    marketImpactCostFraction: float
    slippageAdjustedReferencePrice: float
    partialFillResult: PartialFillSimulationResult


def simulateRealisticMarketOrderFill(
    orderQuantity: float,
    isBuyOrder: bool,
    referencePrice: float,
    averageDailyVolume: float,
    dailyVolatility: float,
    priceLevels: list[tuple[float, float]],
    impactCoefficient: float = 1.0,
) -> RealisticMarketOrderFillResult:
    """Composes all three real cost pieces above into one call: computes
    the square-root market-impact fraction (#1), uses it as the
    slippage fraction to adjust `referencePrice` (#2, reported for
    reference/comparison — NOT substituted into `priceLevels`, since a
    real order book's own quoted levels already reflect the
    market's current price discovery), and simulates the partial fill
    against `priceLevels` (#3). Raises whatever the three underlying
    functions raise.
    """
    marketImpactCostFraction = calculateSquareRootMarketImpactCostFraction(
        orderQuantity, averageDailyVolume, dailyVolatility, impactCoefficient
    )
    slippageAdjustedReferencePrice = applySlippageToReferencePrice(
        referencePrice, isBuyOrder, marketImpactCostFraction
    )
    partialFillResult = simulatePartialFillAgainstOrderBookLevels(orderQuantity, priceLevels)

    return RealisticMarketOrderFillResult(
        marketImpactCostFraction=marketImpactCostFraction,
        slippageAdjustedReferencePrice=slippageAdjustedReferencePrice,
        partialFillResult=partialFillResult,
    )
