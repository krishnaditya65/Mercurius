"""Strategy correlation matrix across a user's/desk's live strategies. See
FEATURES.md §22 ("Deep Quant & Algorithmic Trading Internals").

This module does NOT reimplement Pearson correlation — it reuses
`correlationMatrixEngine.buildPairwiseCorrelationMatrix` and
`findPairsTradingCandidatePairsAboveCorrelationThreshold` (the EXISTING,
already-tested real Pearson math) verbatim, applied to STRATEGY-LEVEL
periodic RETURN series (e.g. each strategy's daily P&L-return series from
its own backtest/live-trading equity curve) instead of single-instrument
PRICE/return series.

The value-add here over the raw correlation engine is purely semantic and
one small piece of real logic: `identifyHiddenlyCorrelatedStrategyPairs`
surfaces strategy pairs whose correlation is high enough to represent a
"secretly correlated bet" — two strategies that LOOK diversified (e.g.
different names, different instruments, different signal logic) but
whose P&L streams actually move together, meaning the desk is carrying
more concentrated risk than position-count or notional-count alone would
suggest. This is real, computed directly from the correlation matrix
above; it does not use any qualitative/strategy-metadata signal (this
module has no concept of "different signal logic" — it operates purely
on the numbers).

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

from dataclasses import dataclass

from quantengine.correlationMatrixEngine import (
    CorrelationMatrixResult,
    PairsTradingCandidatePair,
    buildPairwiseCorrelationMatrix,
    findPairsTradingCandidatePairsAboveCorrelationThreshold,
)


def buildStrategyCorrelationMatrix(
    periodicReturnSeriesByStrategyName: dict[str, list[float]]
) -> CorrelationMatrixResult:
    """Thin, semantically-named wrapper around
    `correlationMatrixEngine.buildPairwiseCorrelationMatrix` — identical
    real math, applied to strategy-level return series. Raises whatever
    the underlying function raises (fewer than two strategies, mismatched
    series lengths, zero-variance series).
    """
    return buildPairwiseCorrelationMatrix(periodicReturnSeriesByStrategyName)


@dataclass(frozen=True)
class HiddenlyCorrelatedStrategyPair:
    firstStrategyName: str
    secondStrategyName: str
    correlationCoefficient: float


def identifyHiddenlyCorrelatedStrategyPairs(
    strategyCorrelationMatrix: CorrelationMatrixResult,
    concentrationRiskCorrelationThreshold: float = 0.7,
) -> list[HiddenlyCorrelatedStrategyPair]:
    """Reuses `findPairsTradingCandidatePairsAboveCorrelationThreshold`
    (identical absolute-correlation-magnitude filter, real math, not
    reimplemented) and relabels the result with strategy-desk-appropriate
    field names — "these two strategies are secretly correlated bets"
    rather than "these two symbols are pairs-trading candidates", which
    is a materially different READING of the same real correlation
    number (high correlation between STRATEGIES is a concentration-risk
    warning; high correlation between INSTRUMENT price series is a
    pairs-trading entry signal — same math, opposite portfolio-
    construction implication).

    Default threshold of 0.7 matches `correlationMatrixEngine`'s own
    default — fully caller-configurable, not a tuned/backtested number.
    """
    rawCandidates: list[PairsTradingCandidatePair] = findPairsTradingCandidatePairsAboveCorrelationThreshold(
        strategyCorrelationMatrix, concentrationRiskCorrelationThreshold
    )
    return [
        HiddenlyCorrelatedStrategyPair(
            firstStrategyName=candidate.firstSymbol,
            secondStrategyName=candidate.secondSymbol,
            correlationCoefficient=candidate.correlationCoefficient,
        )
        for candidate in rawCandidates
    ]
