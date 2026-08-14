import pytest

from quantengine.alternativeDataFeedAggregator import (
    ILLUSTRATIVE_FILING_METRIC_HISTORY_BY_SYMBOL,
    ILLUSTRATIVE_NEWS_SNIPPETS_BY_SYMBOL,
    NewsSnippet,
    aggregateSentimentAcrossSnippets,
    buildIntegratedAlternativeDataSignal,
    concatenateSnippetTextForNlpModule,
    detectAnomaliesAcrossFilingMetrics,
    detectFilingMetricAnomaly,
)
from quantengine.illustrativeSentimentTradingHook import (
    OrderHookDirection,
    calculateIllustrativeLexiconSentimentScore,
)


def test_aggregateSentimentAcrossSnippets_rejectsEmptyList():
    with pytest.raises(ValueError):
        aggregateSentimentAcrossSnippets([])


def test_aggregateSentimentAcrossSnippets_handWorkedPooledExample():
    snippets = [
        NewsSnippet("A", "strong growth and record profit"),  # 2 positive words: strong, growth... let's check lexicon
        NewsSnippet("B", "weak decline and losses"),
    ]
    result = aggregateSentimentAcrossSnippets(snippets)
    # cross-check against raw per-snippet lexicon counts
    scoreA = calculateIllustrativeLexiconSentimentScore(snippets[0].text)
    scoreB = calculateIllustrativeLexiconSentimentScore(snippets[1].text)
    expectedPositive = scoreA.positiveWordCount + scoreB.positiveWordCount
    expectedNegative = scoreA.negativeWordCount + scoreB.negativeWordCount
    assert result.totalPositiveWordCount == expectedPositive
    assert result.totalNegativeWordCount == expectedNegative
    expectedPooled = (expectedPositive - expectedNegative) / max(1, expectedPositive + expectedNegative)
    assert result.pooledSentimentScore == pytest.approx(expectedPooled)


def test_aggregateSentimentAcrossSnippets_perSourceBreakdown():
    snippets = [
        NewsSnippet("SOURCE-X", "strong beat and record profit"),
        NewsSnippet("SOURCE-X", "growth improved robust"),
        NewsSnippet("SOURCE-Y", "decline and losses and downgrade"),
    ]
    result = aggregateSentimentAcrossSnippets(snippets)
    assert set(result.meanSentimentScoreBySource.keys()) == {"SOURCE-X", "SOURCE-Y"}
    assert result.meanSentimentScoreBySource["SOURCE-X"] > 0
    assert result.meanSentimentScoreBySource["SOURCE-Y"] < 0


def test_concatenateSnippetTextForNlpModule_joinsInOrder():
    snippets = [NewsSnippet("A", "first snippet"), NewsSnippet("B", "second snippet")]
    combined = concatenateSnippetTextForNlpModule(snippets)
    assert combined == "first snippet. second snippet"


def test_detectFilingMetricAnomaly_flagsRealOutlier():
    historical = [1.0, 1.1, 0.9, 1.05, 0.95, 1.0]
    result = detectFilingMetricAnomaly("expenseRatioPercent", historical, currentValue=3.0, zScoreThreshold=2.0)
    assert result.isAnomalous is True
    assert result.zScore > 2.0


def test_detectFilingMetricAnomaly_inLineValueIsNotFlagged():
    historical = [1.0, 1.1, 0.9, 1.05, 0.95, 1.0]
    result = detectFilingMetricAnomaly("expenseRatioPercent", historical, currentValue=1.02, zScoreThreshold=2.0)
    assert result.isAnomalous is False


def test_detectFilingMetricAnomaly_handWorkedZScore():
    # historical = [2, 4, 4, 4, 5, 5, 7, 9] -> mean=5, population stddev=2
    historical = [2.0, 4.0, 4.0, 4.0, 5.0, 5.0, 7.0, 9.0]
    result = detectFilingMetricAnomaly("metric", historical, currentValue=9.0, zScoreThreshold=2.0)
    assert result.historicalMean == pytest.approx(5.0)
    assert result.historicalPopulationStandardDeviation == pytest.approx(2.0)
    assert result.zScore == pytest.approx(2.0)  # (9-5)/2 == 2.0, right at the threshold
    assert result.isAnomalous is True  # >= threshold


def test_detectFilingMetricAnomaly_rejectsZeroVarianceHistory():
    with pytest.raises(ValueError):
        detectFilingMetricAnomaly("metric", [1.0, 1.0, 1.0], currentValue=1.0)


def test_detectFilingMetricAnomaly_rejectsEmptyHistory():
    with pytest.raises(ValueError):
        detectFilingMetricAnomaly("metric", [], currentValue=1.0)


def test_detectAnomaliesAcrossFilingMetrics_illustrativeDatasetFlagsTheDebtSpike():
    metrics = ILLUSTRATIVE_FILING_METRIC_HISTORY_BY_SYMBOL["SIM-GROWTHCO"]
    results = detectAnomaliesAcrossFilingMetrics(metrics)
    assert results["debtToEquityRatio"].isAnomalous is True
    assert results["expenseRatioPercent"].isAnomalous is False


def test_buildIntegratedAlternativeDataSignal_killSwitchOffProducesHoldRegardlessOfSentiment():
    snippets = ILLUSTRATIVE_NEWS_SNIPPETS_BY_SYMBOL["SIM-GROWTHCO"]
    signal = buildIntegratedAlternativeDataSignal(snippets, killSwitchEnabled=False)
    assert signal.orderHookSuggestion.direction == OrderHookDirection.HOLD
    assert signal.orderHookSuggestion.killSwitchEngaged is True
    # aggregation itself still ran and is genuinely positive for growth snippets
    assert signal.aggregatedSentiment.pooledSentimentScore > 0


def test_buildIntegratedAlternativeDataSignal_positiveSnippetsFlowThroughToBuySuggestion():
    """The required integration proof: real aggregated alternative data
    actually flows through into the §7 NLP module's real output."""
    snippets = ILLUSTRATIVE_NEWS_SNIPPETS_BY_SYMBOL["SIM-GROWTHCO"]
    signal = buildIntegratedAlternativeDataSignal(snippets, killSwitchEnabled=True)

    assert signal.aggregatedSentiment.pooledSentimentScore > 0
    assert signal.orderHookSuggestion.direction == OrderHookDirection.BUY
    assert signal.orderHookSuggestion.killSwitchEngaged is False
    # The combined text is exactly what §7 scored — proving no disconnect
    # between this module's aggregation and the NLP module's own scoring.
    directNlpScore = calculateIllustrativeLexiconSentimentScore(signal.combinedSnippetText)
    assert directNlpScore.sentimentScore > 0


def test_buildIntegratedAlternativeDataSignal_negativeSnippetsFlowThroughToSellSuggestion():
    snippets = ILLUSTRATIVE_NEWS_SNIPPETS_BY_SYMBOL["SIM-DECLINECO"]
    signal = buildIntegratedAlternativeDataSignal(snippets, killSwitchEnabled=True)

    assert signal.aggregatedSentiment.pooledSentimentScore < 0
    assert signal.orderHookSuggestion.direction == OrderHookDirection.SELL
    assert signal.orderHookSuggestion.killSwitchEngaged is False


def test_buildIntegratedAlternativeDataSignal_rejectsEmptySnippets():
    with pytest.raises(ValueError):
        buildIntegratedAlternativeDataSignal([], killSwitchEnabled=True)
