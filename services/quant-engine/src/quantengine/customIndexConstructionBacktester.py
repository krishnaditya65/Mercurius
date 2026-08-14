"""Custom index construction + backtested historical performance,
licensable to other institutions. See FEATURES.md §16 ("AI, Data &
Research") — "Custom index construction + backtested historical
performance, licensable to other institutions".

============================================================================
READ THIS BEFORE TREATING ANY NUMBER FROM THIS MODULE AS A REAL INDEX
============================================================================
`ILLUSTRATIVE_INDEX_CONSTITUENT_UNIVERSE` below is a small, hand-authored,
deterministic (not `random`) synthetic price + shares-outstanding dataset —
same illustrative-fixture convention as `stockScreenerFilterBuilder.py`'s
instrument universe elsewhere in this service. No real market-cap or price
data.

What IS real and correctly implemented here:
  1. **Real rules-based index construction** (`constructCustomIndex`):
     given a rule — top-N constituents BY MARKET CAP, a weighting scheme
     (EQUAL_WEIGHT or CAP_WEIGHT), and a rebalance frequency in bars — this
     walks the illustrative price/market-cap history bar by bar, holding a
     real fixed SHARE COUNT per constituent between rebalance dates (a real
     buy-and-hold index mechanic: weights drift with price between
     rebalances, exactly like a real cap-weighted or equal-weighted index
     fund), and re-selecting constituents + re-deriving share counts from
     target weights at each rebalance date. The resulting index LEVEL
     series is a genuine, computed price path — `indexLevel(t) =
     sum(sharesHeld_i * price_i(t))`, rescaled to start at 100.0 — not a
     fabricated performance number.
  2. **Real backtest statistics over the constructed index's own price
     path** (`backtestConstructedIndex`): CAGR (`(finalLevel /
     initialLevel) ** (periodsPerYear / barCount) - 1`, the standard
     compound-annual-growth-rate formula), and REUSES this service's real
     `riskStatistics.py` for annualized Sharpe ratio and max drawdown —
     computed directly from the constructed index's own periodic returns/
     equity curve, not invented numbers laid on top.

"Licensable to other institutions" in the FEATURES.md item description is
a PRODUCT/BUSINESS framing, not a technical requirement — there is no
licensing/entitlement system implemented here (that would be a commercial
contracts and access-control concern well outside quant-engine's scope);
what this module provides is the real, reusable index-construction-and-
backtest ENGINE such a product would be built on top of.

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum

from quantengine.riskStatistics import (
    calculateAnnualizedSharpeRatio,
    calculateMaximumDrawdownFromEquityCurve,
)


class IndexWeightingScheme(Enum):
    EQUAL_WEIGHT = "EQUAL_WEIGHT"
    CAP_WEIGHT = "CAP_WEIGHT"


@dataclass(frozen=True)
class IndexConstituentHistory:
    symbol: str
    closingPrices: list[float]  # oldest-first, one value per bar
    sharesOutstandingBillions: float  # fixed, illustrative — real indices use a float-adjusted count

    def marketCapitalizationAtBar(self, barIndex: int) -> float:
        return self.closingPrices[barIndex] * self.sharesOutstandingBillions


@dataclass(frozen=True)
class IndexConstructionRule:
    constituentCount: int  # top-N by market cap
    weightingScheme: IndexWeightingScheme
    rebalanceFrequencyInBars: int

    def __post_init__(self) -> None:
        if self.constituentCount <= 0:
            raise ValueError("constituentCount must be a positive integer")
        if self.rebalanceFrequencyInBars <= 0:
            raise ValueError("rebalanceFrequencyInBars must be a positive integer")


def selectTopNConstituentsByMarketCap(
    universe: list[IndexConstituentHistory], barIndex: int, constituentCount: int
) -> list[IndexConstituentHistory]:
    """Real top-N-by-market-cap selection at one point in time (`barIndex`),
    sorted descending by market cap, ties broken alphabetically by symbol
    for determinism. Raises `ValueError` if `constituentCount` exceeds the
    universe size.
    """
    if constituentCount > len(universe):
        raise ValueError(
            f"constituentCount={constituentCount} exceeds universe size ({len(universe)})"
        )
    ranked = sorted(
        universe,
        key=lambda constituent: (-constituent.marketCapitalizationAtBar(barIndex), constituent.symbol),
    )
    return ranked[:constituentCount]


def calculateTargetWeightsForConstituents(
    constituents: list[IndexConstituentHistory], barIndex: int, weightingScheme: IndexWeightingScheme
) -> dict[str, float]:
    """Real target-weight calculation for one rebalance date:

    - `CAP_WEIGHT`: `weight_i = marketCap_i / sum(marketCap for all selected constituents)`
      — the standard cap-weighted index construction (e.g. S&P 500-style).
    - `EQUAL_WEIGHT`: `weight_i = 1 / constituentCount` for every selected
      constituent — the standard equal-weighted index construction.

    Weights always sum to exactly 1.0 (fully invested, no cash) by
    construction.
    """
    if weightingScheme == IndexWeightingScheme.EQUAL_WEIGHT:
        equalWeight = 1.0 / len(constituents)
        return {constituent.symbol: equalWeight for constituent in constituents}

    totalMarketCap = sum(constituent.marketCapitalizationAtBar(barIndex) for constituent in constituents)
    return {
        constituent.symbol: constituent.marketCapitalizationAtBar(barIndex) / totalMarketCap
        for constituent in constituents
    }


@dataclass(frozen=True)
class IndexRebalanceEvent:
    barIndex: int
    constituentSymbols: list[str]
    targetWeights: dict[str, float]


@dataclass(frozen=True)
class ConstructedIndexResult:
    indexLevelSeries: list[float]  # rescaled to start at 100.0
    rebalanceEvents: list[IndexRebalanceEvent]


def constructCustomIndex(
    universe: list[IndexConstituentHistory], rule: IndexConstructionRule, barCount: int | None = None
) -> ConstructedIndexResult:
    """The real index-construction engine. Walks bars `0..barCount-1`
    (defaults to the shortest constituent history's length) computing the
    index's own price level at every bar:

    - At bar 0 and at every subsequent bar that is a multiple of
      `rule.rebalanceFrequencyInBars`, RE-SELECTS the top-N constituents by
      market cap at that bar (`selectTopNConstituentsByMarketCap`),
      computes real target weights (`calculateTargetWeightsForConstituents`),
      and converts those target weights into an ACTUAL SHARE COUNT per
      constituent given the current index level and current prices —
      `sharesHeld_i = (currentIndexLevel * targetWeight_i) / price_i(bar)`.
    - Between rebalances, share counts are held FIXED (real buy-and-hold
      mechanics — a constituent's effective weight drifts with its price
      until the next rebalance, exactly like a real index fund between
      reconstitution dates), so `indexLevel(t) = sum(sharesHeld_i *
      price_i(t))` for every constituent currently held.

    The index level series is rescaled so `indexLevelSeries[0] == 100.0`
    (the standard "index = 100 at inception" convention). Raises
    `ValueError` if `universe` is empty or `barCount` exceeds any
    constituent's available history length.
    """
    if not universe:
        raise ValueError("universe must contain at least one constituent")

    effectiveBarCount = barCount if barCount is not None else min(len(c.closingPrices) for c in universe)
    for constituent in universe:
        if len(constituent.closingPrices) < effectiveBarCount:
            raise ValueError(
                f"constituent '{constituent.symbol}' has only {len(constituent.closingPrices)} bars of "
                f"history, need at least {effectiveBarCount}"
            )

    indexLevelSeries: list[float] = []
    rebalanceEvents: list[IndexRebalanceEvent] = []
    sharesHeldBySymbol: dict[str, float] = {}
    universeBySymbol = {constituent.symbol: constituent for constituent in universe}
    currentIndexLevel = 100.0

    for barIndex in range(effectiveBarCount):
        isRebalanceBar = barIndex % rule.rebalanceFrequencyInBars == 0
        if isRebalanceBar:
            selectedConstituents = selectTopNConstituentsByMarketCap(universe, barIndex, rule.constituentCount)
            targetWeights = calculateTargetWeightsForConstituents(
                selectedConstituents, barIndex, rule.weightingScheme
            )
            sharesHeldBySymbol = {
                symbol: (currentIndexLevel * weight) / universeBySymbol[symbol].closingPrices[barIndex]
                for symbol, weight in targetWeights.items()
            }
            rebalanceEvents.append(
                IndexRebalanceEvent(
                    barIndex=barIndex,
                    constituentSymbols=sorted(targetWeights.keys()),
                    targetWeights=targetWeights,
                )
            )

        currentIndexLevel = sum(
            sharesHeld * universeBySymbol[symbol].closingPrices[barIndex]
            for symbol, sharesHeld in sharesHeldBySymbol.items()
        )
        indexLevelSeries.append(currentIndexLevel)

    return ConstructedIndexResult(indexLevelSeries=indexLevelSeries, rebalanceEvents=rebalanceEvents)


@dataclass(frozen=True)
class IndexBacktestPerformanceResult:
    startingIndexLevel: float
    endingIndexLevel: float
    compoundAnnualGrowthRate: float
    annualizedSharpeRatio: float | None
    maximumDrawdownFraction: float
    barCount: int
    periodsPerYear: float


def calculateCompoundAnnualGrowthRate(
    startingValue: float, endingValue: float, barCount: int, periodsPerYear: float
) -> float:
    """Standard CAGR formula:

        CAGR = (endingValue / startingValue) ** (periodsPerYear / barCount) - 1

    Raises `ValueError` if `startingValue` is not strictly positive
    (undefined base for the exponentiation) or `barCount` is not a
    positive integer.
    """
    if startingValue <= 0:
        raise ValueError("startingValue must be strictly positive")
    if barCount <= 0:
        raise ValueError("barCount must be a positive integer")
    return (endingValue / startingValue) ** (periodsPerYear / barCount) - 1.0


def backtestConstructedIndex(
    constructedIndex: ConstructedIndexResult,
    periodsPerYear: float = 252.0,
    periodicRiskFreeRate: float = 0.0,
) -> IndexBacktestPerformanceResult:
    """Real backtested historical performance statistics computed directly
    from `constructedIndex.indexLevelSeries` — the ACTUAL price path
    `constructCustomIndex` produced, not a separately fabricated number:

    - `compoundAnnualGrowthRate`: via `calculateCompoundAnnualGrowthRate`.
    - `annualizedSharpeRatio`: reuses `riskStatistics.calculateAnnualizedSharpeRatio`
      over the index's own periodic returns; `None` (not an error) if the
      return series has zero variance (e.g. a degenerate one-bar
      backtest), since a Sharpe ratio is genuinely undefined there.
    - `maximumDrawdownFraction`: reuses
      `riskStatistics.calculateMaximumDrawdownFromEquityCurve` directly
      over the index level series.

    Raises `ValueError` if the index level series has fewer than 2 bars
    (no return can be computed from a single price point).
    """
    levelSeries = constructedIndex.indexLevelSeries
    if len(levelSeries) < 2:
        raise ValueError("indexLevelSeries must contain at least 2 bars to backtest")

    periodicReturns = [
        levelSeries[i] / levelSeries[i - 1] - 1.0 for i in range(1, len(levelSeries))
    ]

    cagr = calculateCompoundAnnualGrowthRate(
        levelSeries[0], levelSeries[-1], len(levelSeries) - 1, periodsPerYear
    )

    try:
        sharpeRatio: float | None = calculateAnnualizedSharpeRatio(
            periodicReturns, periodicRiskFreeRate, periodsPerYear
        )
    except ValueError:
        sharpeRatio = None

    maxDrawdown = calculateMaximumDrawdownFromEquityCurve(levelSeries)

    return IndexBacktestPerformanceResult(
        startingIndexLevel=levelSeries[0],
        endingIndexLevel=levelSeries[-1],
        compoundAnnualGrowthRate=cagr,
        annualizedSharpeRatio=sharpeRatio,
        maximumDrawdownFraction=maxDrawdown.maximumDrawdownFraction,
        barCount=len(levelSeries),
        periodsPerYear=periodsPerYear,
    )


# --- Illustrative, hand-authored synthetic constituent universe ---------


def _buildDeterministicPriceSeries(startingPrice: float, barCount: int, perBarDrift: float, oscillationAmplitude: float) -> list[float]:
    """Deterministic (non-random) synthetic series: a linear drift plus a
    small alternating oscillation, so the series is neither perfectly
    monotonic (unrealistic) nor random (non-reproducible).
    """
    prices = []
    for barIndex in range(barCount):
        oscillation = oscillationAmplitude if barIndex % 2 == 0 else -oscillationAmplitude
        prices.append(max(0.01, startingPrice + perBarDrift * barIndex + oscillation))
    return prices


ILLUSTRATIVE_INDEX_CONSTITUENT_UNIVERSE: list[IndexConstituentHistory] = [
    IndexConstituentHistory("SIM-ALPHA", _buildDeterministicPriceSeries(200.0, 120, 0.40, 1.0), 5.0),
    IndexConstituentHistory("SIM-BETA", _buildDeterministicPriceSeries(150.0, 120, 0.15, 0.8), 3.0),
    IndexConstituentHistory("SIM-GAMMA", _buildDeterministicPriceSeries(80.0, 120, 0.05, 0.5), 8.0),
    IndexConstituentHistory("SIM-DELTA", _buildDeterministicPriceSeries(60.0, 120, -0.10, 0.4), 4.0),
    IndexConstituentHistory("SIM-EPSILON", _buildDeterministicPriceSeries(40.0, 120, 0.02, 0.3), 12.0),
    IndexConstituentHistory("SIM-ZETA", _buildDeterministicPriceSeries(300.0, 120, 0.60, 1.5), 1.5),
]
