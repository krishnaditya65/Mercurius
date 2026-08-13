"""Pairwise Pearson correlation matrix engine for pairs-trading candidate
discovery. See ARCHITECTURE.md §6 ("Quant Math Engine") and FEATURES.md
§6 — "Correlation matrix engine for pairs-trading candidate discovery".

Given multiple symbols' aligned return series, this module computes a
real pairwise Pearson correlation matrix, and a "candidate pairs" filter
returning symbol pairs whose correlation exceeds a configurable
threshold — a standard first screening step for pairs-trading candidate
selection (see backtesting/pairsTradingStrategy.py's module docstring
for the strategy this conceptually feeds; this module deliberately does
NOT import or call into that one — building the correlation engine
itself, real and tested, is the whole of this pass).

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

import math
from dataclasses import dataclass


def calculateMeanOfSeries(values: list[float]) -> float:
    if not values:
        raise ValueError("values must contain at least one observation")
    return sum(values) / len(values)


def calculatePearsonCorrelationCoefficient(
    firstSeries: list[float], secondSeries: list[float]
) -> float:
    """Pearson product-moment correlation coefficient between two
    equal-length, index-aligned return series:

        r = sum((x_i - mean(x)) * (y_i - mean(y)))
            / sqrt(sum((x_i - mean(x))^2) * sum((y_i - mean(y))^2))

    Raises `ValueError` if the series have different lengths, either is
    empty, or either series has zero variance (a perfectly flat series
    has an undefined correlation with anything — division by zero).
    """
    if len(firstSeries) != len(secondSeries):
        raise ValueError("firstSeries and secondSeries must be the same length")
    if not firstSeries:
        raise ValueError("firstSeries/secondSeries must contain at least one observation")

    firstMean = calculateMeanOfSeries(firstSeries)
    secondMean = calculateMeanOfSeries(secondSeries)

    firstDeviations = [value - firstMean for value in firstSeries]
    secondDeviations = [value - secondMean for value in secondSeries]

    covarianceSum = sum(fd * sd for fd, sd in zip(firstDeviations, secondDeviations))
    firstSumOfSquares = sum(fd * fd for fd in firstDeviations)
    secondSumOfSquares = sum(sd * sd for sd in secondDeviations)

    if firstSumOfSquares == 0.0 or secondSumOfSquares == 0.0:
        raise ValueError(
            "cannot compute Pearson correlation: at least one series has zero variance "
            "(a perfectly flat series has an undefined correlation)"
        )

    return covarianceSum / math.sqrt(firstSumOfSquares * secondSumOfSquares)


@dataclass(frozen=True)
class CorrelationMatrixResult:
    symbolsInOrder: list[str]
    correlationBySymbolPair: dict[tuple[str, str], float]

    def getCorrelation(self, firstSymbol: str, secondSymbol: str) -> float:
        """Symmetric lookup — works regardless of argument order, and
        returns 1.0 for a symbol correlated with itself without needing a
        stored diagonal entry.
        """
        if firstSymbol == secondSymbol:
            return 1.0
        if (firstSymbol, secondSymbol) in self.correlationBySymbolPair:
            return self.correlationBySymbolPair[(firstSymbol, secondSymbol)]
        return self.correlationBySymbolPair[(secondSymbol, firstSymbol)]

    def toDenseGrid(self) -> list[list[float]]:
        """Full symmetric matrix (with 1.0 on the diagonal), ordered per
        `symbolsInOrder` — the shape most UIs/notebooks expect for
        rendering a correlation heatmap.
        """
        return [
            [self.getCorrelation(rowSymbol, columnSymbol) for columnSymbol in self.symbolsInOrder]
            for rowSymbol in self.symbolsInOrder
        ]


def buildPairwiseCorrelationMatrix(
    returnSeriesBySymbol: dict[str, list[float]]
) -> CorrelationMatrixResult:
    """Computes the Pearson correlation for every distinct unordered pair
    of symbols in `returnSeriesBySymbol`. All series must be the same
    length (i.e. already time-aligned by the caller — this module does
    no resampling/alignment of its own). Raises `ValueError` if fewer
    than two symbols are supplied.

    `symbolsInOrder` preserves `returnSeriesBySymbol`'s insertion order
    (Python dicts are order-preserving) rather than sorting — callers
    that want a stable/sorted axis order can sort their input dict first.
    """
    symbolsInOrder = list(returnSeriesBySymbol.keys())
    if len(symbolsInOrder) < 2:
        raise ValueError("returnSeriesBySymbol must contain at least two symbols")

    correlationBySymbolPair: dict[tuple[str, str], float] = {}
    for firstIndex in range(len(symbolsInOrder)):
        for secondIndex in range(firstIndex + 1, len(symbolsInOrder)):
            firstSymbol = symbolsInOrder[firstIndex]
            secondSymbol = symbolsInOrder[secondIndex]
            correlationBySymbolPair[(firstSymbol, secondSymbol)] = calculatePearsonCorrelationCoefficient(
                returnSeriesBySymbol[firstSymbol], returnSeriesBySymbol[secondSymbol]
            )

    return CorrelationMatrixResult(symbolsInOrder=symbolsInOrder, correlationBySymbolPair=correlationBySymbolPair)


@dataclass(frozen=True)
class PairsTradingCandidatePair:
    firstSymbol: str
    secondSymbol: str
    correlationCoefficient: float


def findPairsTradingCandidatePairsAboveCorrelationThreshold(
    correlationMatrix: CorrelationMatrixResult,
    minimumAbsoluteCorrelationThreshold: float,
) -> list[PairsTradingCandidatePair]:
    """Filters `correlationMatrix` down to symbol pairs whose ABSOLUTE
    correlation is at or above `minimumAbsoluteCorrelationThreshold` —
    the standard pairs-trading candidate screen (strong positive
    correlation is the classic case, but strong negative correlation is
    also a legitimate mean-reversion-of-the-spread candidate under a
    sign-adjusted hedge ratio, so this filters on magnitude, not sign).

    Results are sorted by descending absolute correlation, so the
    strongest candidates come first. Raises `ValueError` if the
    threshold is outside [0, 1].
    """
    if not (0.0 <= minimumAbsoluteCorrelationThreshold <= 1.0):
        raise ValueError("minimumAbsoluteCorrelationThreshold must be within [0, 1]")

    candidates = [
        PairsTradingCandidatePair(firstSymbol=firstSymbol, secondSymbol=secondSymbol, correlationCoefficient=correlation)
        for (firstSymbol, secondSymbol), correlation in correlationMatrix.correlationBySymbolPair.items()
        if abs(correlation) >= minimumAbsoluteCorrelationThreshold
    ]
    candidates.sort(key=lambda candidate: abs(candidate.correlationCoefficient), reverse=True)
    return candidates
