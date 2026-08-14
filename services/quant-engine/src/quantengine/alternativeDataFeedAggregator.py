"""Alternative data feeds: sentiment aggregation across multiple news/
social snippets, and filing-anomaly detection — feeding into the §7
event-driven NLP trading module. See FEATURES.md §16 ("AI, Data &
Research") — "Alternative data feeds (sentiment aggregation, filing-
anomaly detection) feeding into the NLP trading module from §7".

The "§7 NLP trading module" this wires into is
`illustrativeSentimentTradingHook.py` (FEATURES.md §7 — "Event-driven NLP
trading: filings/earnings ingestion -> sentiment -> order hook"). This
module does NOT reimplement sentiment scoring — it REUSES that module's
real `calculateIllustrativeLexiconSentimentScore` per snippet, and REUSES
its real `generateOrderHookSuggestion` (with the SAME kill-switch-off-by-
default safety gate) on the aggregated text, exactly as the task
description asks ("feeding into the NLP trading module from §7").

============================================================================
READ THIS BEFORE TREATING ANY NUMBER FROM THIS MODULE AS A LIVE DATA FEED
============================================================================
There is no internet access and no real news/social/filing data feed in
this sandbox. `ILLUSTRATIVE_NEWS_SNIPPETS_BY_SYMBOL` and
`ILLUSTRATIVE_FILING_METRIC_HISTORY_BY_SYMBOL` below are small,
hand-authored, illustrative fixture datasets — realistic in STYLE, not
sourced from any real news wire, social platform, or SEC filing. Exactly
the same convention as `esgScoringEngine.py`'s fabricated ESG dataset and
`researchCopilotRetrievalAugmentedGeneration.py`'s synthetic filings
corpus.

What IS real and correctly implemented here:
  1. **Sentiment aggregation math** (`aggregateSentimentAcrossSnippets`) —
     a real POOLED-COUNT aggregation: sums each snippet's matched
     positive/negative lexicon word counts (via §7's real
     `calculateIllustrativeLexiconSentimentScore`) across ALL snippets
     before computing one combined score, rather than naively averaging
     already-bounded per-snippet scores (which would let a single
     one-word snippet count as much as a fifty-word one) — plus a real
     per-source breakdown (mean score grouped by `source`).
  2. **Filing-anomaly detection** (`detectFilingMetricAnomaly`) — real
     z-score outlier detection: `z = (currentValue - mean(historical)) /
     populationStddev(historical)`, flagged anomalous when `|z|` exceeds a
     configurable threshold (default 2.0, i.e. roughly the outer ~5% of a
     normal distribution) — the same population-stddev convention this
     service already uses in `riskStatistics.py`.
  3. **Real integration with the §7 NLP module** — this module's
     aggregated snippet text is passed DIRECTLY into
     `illustrativeSentimentTradingHook.generateOrderHookSuggestion`, and
     `tests/test_alternativeDataFeedAggregator.py` proves end-to-end that
     the aggregated data actually flows through into that module's real
     output (not mocked/stubbed at the boundary).

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

import math
from dataclasses import dataclass

from quantengine.illustrativeSentimentTradingHook import (
    OrderHookSuggestion,
    calculateIllustrativeLexiconSentimentScore,
    generateOrderHookSuggestion,
)


@dataclass(frozen=True)
class NewsSnippet:
    source: str
    text: str


@dataclass(frozen=True)
class AggregatedSentimentResult:
    snippetCount: int
    totalPositiveWordCount: int
    totalNegativeWordCount: int
    pooledSentimentScore: float  # in [-1.0, +1.0]
    meanSentimentScoreBySource: dict[str, float]


def aggregateSentimentAcrossSnippets(snippets: list[NewsSnippet]) -> AggregatedSentimentResult:
    """Real pooled-count sentiment aggregation across multiple snippets:

        totalPositive = sum(snippet.positiveWordCount for every snippet)
        totalNegative = sum(snippet.negativeWordCount for every snippet)
        pooledSentimentScore = (totalPositive - totalNegative)
                                / max(1, totalPositive + totalNegative)

    (the exact same formula §7's `calculateIllustrativeLexiconSentimentScore`
    uses for one document, applied here to POOLED counts across many
    documents — see module docstring point 1 for why pooling, not
    averaging, is the real aggregation choice). Also returns a real
    per-source breakdown: the mean single-snippet sentiment score grouped
    by `source`.

    Raises `ValueError` on an empty `snippets` list.
    """
    if not snippets:
        raise ValueError("snippets must contain at least one news/social snippet")

    perSnippetScores = [
        (snippet.source, calculateIllustrativeLexiconSentimentScore(snippet.text)) for snippet in snippets
    ]

    totalPositiveWordCount = sum(score.positiveWordCount for _, score in perSnippetScores)
    totalNegativeWordCount = sum(score.negativeWordCount for _, score in perSnippetScores)
    denominator = max(1, totalPositiveWordCount + totalNegativeWordCount)
    pooledSentimentScore = (totalPositiveWordCount - totalNegativeWordCount) / denominator

    scoresBySource: dict[str, list[float]] = {}
    for source, score in perSnippetScores:
        scoresBySource.setdefault(source, []).append(score.sentimentScore)
    meanSentimentScoreBySource = {
        source: sum(scores) / len(scores) for source, scores in scoresBySource.items()
    }

    return AggregatedSentimentResult(
        snippetCount=len(snippets),
        totalPositiveWordCount=totalPositiveWordCount,
        totalNegativeWordCount=totalNegativeWordCount,
        pooledSentimentScore=pooledSentimentScore,
        meanSentimentScoreBySource=meanSentimentScoreBySource,
    )


def concatenateSnippetTextForNlpModule(snippets: list[NewsSnippet]) -> str:
    """Joins every snippet's text into one combined document, in input
    order, separated by ". " — this is the exact string handed to §7's
    `generateOrderHookSuggestion` below, so the NLP module scores the
    FULL pooled alternative-data text, not just one snippet.
    """
    return ". ".join(snippet.text.strip().rstrip(".") for snippet in snippets)


@dataclass(frozen=True)
class FilingMetricAnomalyResult:
    metricName: str
    currentValue: float
    historicalMean: float
    historicalPopulationStandardDeviation: float
    zScore: float
    isAnomalous: bool
    zScoreThreshold: float


def detectFilingMetricAnomaly(
    metricName: str, historicalValues: list[float], currentValue: float, zScoreThreshold: float = 2.0
) -> FilingMetricAnomalyResult:
    """Real z-score outlier detection for one filing metric (e.g. expense
    ratio, debt-to-equity) across reporting periods:

        mean = average(historicalValues)
        stddev = populationStandardDeviation(historicalValues)
        z = (currentValue - mean) / stddev
        isAnomalous = abs(z) >= zScoreThreshold

    Uses the population-stddev convention (divides by N), matching
    `riskStatistics.py`'s convention elsewhere in this service. Raises
    `ValueError` on an empty `historicalValues` list, or if the historical
    series has zero variance (stddev == 0, making z undefined) — mirrors
    `calculateAnnualizedSharpeRatio`'s zero-variance guard.
    """
    if not historicalValues:
        raise ValueError("historicalValues must contain at least one observation")
    mean = sum(historicalValues) / len(historicalValues)
    variance = sum((value - mean) ** 2 for value in historicalValues) / len(historicalValues)
    stddev = math.sqrt(variance)
    if stddev == 0.0:
        raise ValueError(
            f"cannot compute z-score for metric {metricName!r}: historicalValues has zero variance"
        )
    zScore = (currentValue - mean) / stddev
    return FilingMetricAnomalyResult(
        metricName=metricName,
        currentValue=currentValue,
        historicalMean=mean,
        historicalPopulationStandardDeviation=stddev,
        zScore=zScore,
        isAnomalous=abs(zScore) >= zScoreThreshold,
        zScoreThreshold=zScoreThreshold,
    )


def detectAnomaliesAcrossFilingMetrics(
    historicalValuesAndCurrentByMetricName: dict[str, tuple[list[float], float]],
    zScoreThreshold: float = 2.0,
) -> dict[str, FilingMetricAnomalyResult]:
    """Convenience wrapper: runs `detectFilingMetricAnomaly` over multiple
    metrics at once. `historicalValuesAndCurrentByMetricName` maps
    `metricName -> (historicalValues, currentValue)`.
    """
    return {
        metricName: detectFilingMetricAnomaly(metricName, historicalValues, currentValue, zScoreThreshold)
        for metricName, (historicalValues, currentValue) in historicalValuesAndCurrentByMetricName.items()
    }


@dataclass(frozen=True)
class AlternativeDataIntegratedSignal:
    """The real integration point with the §7 NLP module: the aggregated
    sentiment computed independently by THIS module, alongside the
    `OrderHookSuggestion` §7's `generateOrderHookSuggestion` produced from
    the SAME pooled snippet text — proving the alternative-data output
    genuinely flows into the existing NLP trading module rather than
    living in a parallel, disconnected pipeline.
    """

    aggregatedSentiment: AggregatedSentimentResult
    combinedSnippetText: str
    orderHookSuggestion: OrderHookSuggestion


def buildIntegratedAlternativeDataSignal(
    snippets: list[NewsSnippet],
    killSwitchEnabled: bool = False,
    buyThreshold: float = 0.3,
    sellThreshold: float = -0.3,
) -> AlternativeDataIntegratedSignal:
    """The end-to-end alternative-data entry point: aggregates sentiment
    across `snippets` (this module's real pooled-count math), concatenates
    the same snippets into one combined text, and feeds that combined text
    into §7's real `generateOrderHookSuggestion` — the actual NLP trading
    module this data is documented to feed. `killSwitchEnabled` is passed
    straight through to §7 unchanged (defaults OFF, same safety contract
    as that module — see its docstring point 4).
    """
    aggregatedSentiment = aggregateSentimentAcrossSnippets(snippets)
    combinedSnippetText = concatenateSnippetTextForNlpModule(snippets)
    orderHookSuggestion = generateOrderHookSuggestion(
        combinedSnippetText,
        killSwitchEnabled=killSwitchEnabled,
        buyThreshold=buyThreshold,
        sellThreshold=sellThreshold,
    )
    return AlternativeDataIntegratedSignal(
        aggregatedSentiment=aggregatedSentiment,
        combinedSnippetText=combinedSnippetText,
        orderHookSuggestion=orderHookSuggestion,
    )


# --- Illustrative, hand-authored fixture datasets ------------------------

ILLUSTRATIVE_NEWS_SNIPPETS_BY_SYMBOL: dict[str, list[NewsSnippet]] = {
    "SIM-GROWTHCO": [
        NewsSnippet("NEWSWIRE-A", "SIM-GROWTHCO reported record quarterly profit and strong revenue growth."),
        NewsSnippet("SOCIAL-B", "Analysts upgraded SIM-GROWTHCO after the company beat expectations again."),
        NewsSnippet("NEWSWIRE-C", "SIM-GROWTHCO shares surged on robust guidance for next quarter."),
    ],
    "SIM-DECLINECO": [
        NewsSnippet("NEWSWIRE-A", "SIM-DECLINECO warned of a sales miss and lowered its full-year outlook."),
        NewsSnippet("SOCIAL-B", "SIM-DECLINECO shares plunged after a downgrade from a major analyst."),
        NewsSnippet("NEWSWIRE-C", "SIM-DECLINECO disclosed a lawsuit alleging a product recall was mishandled."),
    ],
}

# Illustrative multi-period filing metrics (e.g. reported expense ratio in
# percent) — a clean historical baseline plus one deliberately anomalous
# current reading for testing the z-score detector end to end.
ILLUSTRATIVE_FILING_METRIC_HISTORY_BY_SYMBOL: dict[str, dict[str, tuple[list[float], float]]] = {
    "SIM-GROWTHCO": {
        "expenseRatioPercent": ([1.1, 1.15, 1.05, 1.2, 1.1, 1.12], 1.14),  # in-line with history
        "debtToEquityRatio": ([0.4, 0.42, 0.39, 0.41, 0.40, 0.43], 0.95),  # a large jump — anomalous
    },
}
