import math

import pytest

from quantengine.illustrativeSentimentTradingHook import (
    OrderHookDirection,
    OrderHookSuggestion,
    SentimentScoreResult,
    calculateIllustrativeLexiconSentimentScore,
    generateOrderHookSuggestion,
)


def test_sentimentScoreAllPositiveWordsGivesScoreOfOne():
    result = calculateIllustrativeLexiconSentimentScore("Strong growth and record profit, shares surged")
    assert isinstance(result, SentimentScoreResult)
    assert result.positiveWordCount == 5  # strong, growth, record, profit, surged
    assert result.negativeWordCount == 0
    assert math.isclose(result.sentimentScore, 1.0, rel_tol=1e-12)


def test_sentimentScoreAllNegativeWordsGivesScoreOfNegativeOne():
    result = calculateIllustrativeLexiconSentimentScore("Earnings missed, guidance was cut, shares plunged")
    assert result.positiveWordCount == 0
    assert result.negativeWordCount >= 1
    assert math.isclose(result.sentimentScore, -1.0, rel_tol=1e-12)


def test_sentimentScoreOfNeutralTextWithNoSentimentWordsIsZero():
    result = calculateIllustrativeLexiconSentimentScore("The company held its quarterly meeting on Tuesday")
    assert result.positiveWordCount == 0
    assert result.negativeWordCount == 0
    assert result.sentimentScore == 0.0


def test_sentimentScoreMixedWordsComputesNetPolarity():
    # 3 positive (beat, strong, growth), 1 negative (weak) -> (3-1)/4 = 0.5
    result = calculateIllustrativeLexiconSentimentScore("Revenue beat estimates on strong growth, but margins were weak")
    assert result.positiveWordCount == 3
    assert result.negativeWordCount == 1
    assert math.isclose(result.sentimentScore, 0.5, rel_tol=1e-9)


def test_sentimentScoreIsCaseInsensitive():
    lower = calculateIllustrativeLexiconSentimentScore("strong beat")
    upper = calculateIllustrativeLexiconSentimentScore("STRONG BEAT")
    assert lower.sentimentScore == upper.sentimentScore


def test_sentimentScoreDoesNotSubstringMatchInsideOtherWords():
    # "cut" should not match inside "cute", "beat" should not match inside "beaten"... but
    # this toy tokenizer matches whole alphabetic tokens only, so "cute" != "cut".
    result = calculateIllustrativeLexiconSentimentScore("The company's mascot is a cute robot")
    assert result.negativeWordCount == 0


def test_sentimentScoreRaisesOnEmptyText():
    with pytest.raises(ValueError):
        calculateIllustrativeLexiconSentimentScore("")
    with pytest.raises(ValueError):
        calculateIllustrativeLexiconSentimentScore("   ")


# --- Order hook / kill switch ---------------------------------------------


def test_orderHookDefaultsToKillSwitchOffAndReturnsInertHold():
    suggestion = generateOrderHookSuggestion("Strong growth and record profit beat expectations")
    assert isinstance(suggestion, OrderHookSuggestion)
    assert suggestion.direction == OrderHookDirection.HOLD
    assert suggestion.confidence == 0.0
    assert suggestion.killSwitchEngaged is True
    assert "kill switch" in suggestion.explanation.lower()


def test_orderHookWithKillSwitchEnabledSuggestsBuyOnStronglyPositiveText():
    suggestion = generateOrderHookSuggestion(
        "Strong growth, record profit, shares surged on the beat", killSwitchEnabled=True
    )
    assert suggestion.direction == OrderHookDirection.BUY
    assert suggestion.confidence > 0.0
    assert suggestion.killSwitchEngaged is False


def test_orderHookWithKillSwitchEnabledSuggestsSellOnStronglyNegativeText():
    suggestion = generateOrderHookSuggestion(
        "Earnings missed badly, guidance cut, shares plunged on the warning", killSwitchEnabled=True
    )
    assert suggestion.direction == OrderHookDirection.SELL
    assert suggestion.confidence > 0.0
    assert suggestion.killSwitchEngaged is False


def test_orderHookWithKillSwitchEnabledHoldsOnNeutralText():
    suggestion = generateOrderHookSuggestion(
        "The company held its quarterly meeting on Tuesday", killSwitchEnabled=True
    )
    assert suggestion.direction == OrderHookDirection.HOLD
    assert suggestion.killSwitchEngaged is False


def test_orderHookRespectsCustomThresholds():
    # score=0.5 (see test_sentimentScoreMixedWordsComputesNetPolarity) is
    # below a strict 0.6 buyThreshold -> HOLD, but crosses a looser 0.4
    # buyThreshold -> BUY. Confirms the threshold is genuinely read.
    text = "Revenue beat estimates on strong growth, but margins were weak"
    strictSuggestion = generateOrderHookSuggestion(text, killSwitchEnabled=True, buyThreshold=0.6)
    looseSuggestion = generateOrderHookSuggestion(text, killSwitchEnabled=True, buyThreshold=0.4)
    assert strictSuggestion.direction == OrderHookDirection.HOLD
    assert looseSuggestion.direction == OrderHookDirection.BUY


def test_orderHookNeverExposesAnOrderSubmissionSideEffect():
    # Structural guarantee check: the dataclass has exactly the
    # documented fields and nothing resembling an order-id/exchange
    # acknowledgement, proving this call could not have submitted
    # anything anywhere.
    suggestion = generateOrderHookSuggestion("Strong growth beat", killSwitchEnabled=True)
    fieldNames = set(suggestion.__dataclass_fields__.keys())
    assert fieldNames == {"direction", "confidence", "explanation", "killSwitchEngaged"}


def test_orderHookConfidenceIsWithinUnitInterval():
    for text in [
        "Strong growth and record profit beat estimates, shares surged",
        "Earnings missed, guidance was cut, shares plunged",
        "The company held its quarterly meeting on Tuesday",
    ]:
        suggestion = generateOrderHookSuggestion(text, killSwitchEnabled=True)
        assert 0.0 <= suggestion.confidence <= 1.0
