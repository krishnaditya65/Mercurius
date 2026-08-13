"""Event-driven NLP trading: filings/earnings ingestion -> sentiment ->
order hook. See ARCHITECTURE.md §7 ("Algorithmic Trading & Backtesting")
and FEATURES.md §7 — "Event-driven NLP trading: filings/earnings
ingestion -> sentiment -> order hook".

READ THIS BEFORE USING ANY PART OF THIS MODULE:

1. `calculateIllustrativeLexiconSentimentScore` is a TOY, lexicon-based
   word-counting scorer — a small hand-built positive/negative word
   list, nothing more. It is NOT real NLP, NOT a trained model, NOT
   context-aware (it can't detect negation, sarcasm, or domain-specific
   meaning — "shares fell sharply" scores as containing a negative word
   coincidentally, not because it understood the sentence), and it is
   explicitly documented here as a placeholder for a real sentiment
   pipeline that quant-engine does not build in this pass.
2. Real filings/earnings-release INGESTION (pulling actual SEC filings,
   earnings call transcripts, or news feeds) is NOT implemented anywhere
   in this module. Every function here takes a plain `str` of text as
   input — that text is assumed to already be fixture/test data or
   something a caller sourced elsewhere. There is no network call, no
   data feed, no scraper in this file.
3. `generateOrderHookSuggestion` NEVER places a real order. It returns a
   plain, structured `OrderHookSuggestion` dataclass (direction +
   confidence) and nothing else — no side effects, no I/O, no call into
   any order-submission path anywhere in this codebase. Wiring this
   suggestion to a real order-submission path is explicitly OUT OF SCOPE
   and must not be attempted from this module or by extending it.
4. The suggestion is ONLY EVER generated if `killSwitchEnabled=True` is
   passed EXPLICITLY by the caller — the default is `False`, and when
   `False` the function returns a `HOLD` suggestion with a `killSwitchEngaged`
   explanation and skips even computing a directional signal. This is
   the literal "kill switch" the task description asked for: an
   easy-to-audit single boolean gate that defaults OFF.

This module exists to demonstrate the SHAPE of an event-driven sentiment
hook (text in -> score -> gated suggestion out) for future real-NLP work
to slot into, not to be anyone's actual trading signal.

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

import re
from dataclasses import dataclass
from enum import Enum

# --- TOY lexicon -----------------------------------------------------------
# Deliberately tiny and deliberately naive. Real sentiment/NLP systems use
# trained models (or at minimum much larger, domain-tuned lexicons with
# negation handling); this list exists only to make the "text in -> score
# out -> gated suggestion" plumbing demonstrably real and testable, not to
# be a credible sentiment engine.
ILLUSTRATIVE_POSITIVE_WORDS = frozenset(
    {
        "beat", "beats", "strong", "growth", "record", "profit", "profitable",
        "surge", "surged", "outperform", "outperformed", "upgrade", "upgraded",
        "exceeded", "raised", "raise", "bullish", "gain", "gains", "improved",
        "robust", "expand", "expansion", "positive",
    }
)
ILLUSTRATIVE_NEGATIVE_WORDS = frozenset(
    {
        "miss", "missed", "weak", "decline", "declined", "loss", "losses",
        "plunge", "plunged", "underperform", "underperformed", "downgrade",
        "downgraded", "cut", "lowered", "bearish", "drop", "dropped",
        "warning", "recall", "lawsuit", "bankruptcy", "layoffs", "negative",
        "fell", "falling",
    }
)

_WORD_TOKEN_PATTERN = re.compile(r"[a-zA-Z]+")


@dataclass(frozen=True)
class SentimentScoreResult:
    positiveWordCount: int
    negativeWordCount: int
    totalWordCount: int
    sentimentScore: float  # in [-1.0, +1.0]; see calculateIllustrativeLexiconSentimentScore


def calculateIllustrativeLexiconSentimentScore(text: str) -> SentimentScoreResult:
    """TOY lexicon-based sentiment score — see this module's docstring
    for exactly what "toy" means here. Scoring method:

        sentimentScore = (positiveWordCount - negativeWordCount)
                          / max(1, positiveWordCount + negativeWordCount)

    i.e. it's the net polarity AMONG THE MATCHED SENTIMENT WORDS ONLY
    (not divided by total word count — a short sentence that's 100%
    matched positive words scores +1.0 same as a long one), clamped
    implicitly to [-1.0, +1.0] by construction. Case-insensitive,
    whole-word matching only (substring matches are NOT counted — "cut"
    in "cute" doesn't match, because tokens are split on word
    boundaries via a regex, not a naive substring search).

    Raises `ValueError` on empty/whitespace-only text.
    """
    if not text or not text.strip():
        raise ValueError("text must be non-empty")

    tokens = [token.lower() for token in _WORD_TOKEN_PATTERN.findall(text)]
    positiveWordCount = sum(1 for token in tokens if token in ILLUSTRATIVE_POSITIVE_WORDS)
    negativeWordCount = sum(1 for token in tokens if token in ILLUSTRATIVE_NEGATIVE_WORDS)
    denominator = max(1, positiveWordCount + negativeWordCount)
    sentimentScore = (positiveWordCount - negativeWordCount) / denominator

    return SentimentScoreResult(
        positiveWordCount=positiveWordCount,
        negativeWordCount=negativeWordCount,
        totalWordCount=len(tokens),
        sentimentScore=sentimentScore,
    )


class OrderHookDirection(Enum):
    BUY = "BUY"
    SELL = "SELL"
    HOLD = "HOLD"


@dataclass(frozen=True)
class OrderHookSuggestion:
    """A plain, structured SUGGESTION only. This dataclass carries no
    behavior — it is never passed to any order-submission function
    anywhere in this codebase, and this module contains no such
    function. See the module docstring's point 3.
    """

    direction: OrderHookDirection
    confidence: float  # in [0.0, 1.0]
    explanation: str
    killSwitchEngaged: bool


def generateOrderHookSuggestion(
    text: str,
    killSwitchEnabled: bool = False,
    buyThreshold: float = 0.3,
    sellThreshold: float = -0.3,
) -> OrderHookSuggestion:
    """Scores `text` via `calculateIllustrativeLexiconSentimentScore` and,
    ONLY IF `killSwitchEnabled=True` is passed explicitly, maps the score
    to a directional suggestion:

    - `sentimentScore >= buyThreshold` -> BUY, confidence = sentimentScore
    - `sentimentScore <= sellThreshold` -> SELL, confidence = abs(sentimentScore)
    - otherwise -> HOLD, confidence = 1.0 - abs(sentimentScore) (i.e.
      "how close to neutral" the score is)

    When `killSwitchEnabled=False` (the default), this function does NOT
    even compute a directional signal from the score — it immediately
    returns a HOLD suggestion with `killSwitchEngaged=True` and
    `confidence=0.0`, and `explanation` says exactly why. This is the
    literal kill switch: a single boolean, defaulting OFF, that a caller
    must deliberately flip to get anything other than an inert HOLD out
    of this function.

    Regardless of the kill switch or the computed direction, THIS
    FUNCTION NEVER PLACES AN ORDER — it only ever returns this
    dataclass. See the module docstring's point 3.
    """
    if not killSwitchEnabled:
        return OrderHookSuggestion(
            direction=OrderHookDirection.HOLD,
            confidence=0.0,
            explanation=(
                "kill switch is OFF (killSwitchEnabled=False, the default) — no sentiment "
                "score was computed and no directional suggestion was generated"
            ),
            killSwitchEngaged=True,
        )

    scoreResult = calculateIllustrativeLexiconSentimentScore(text)
    sentimentScore = scoreResult.sentimentScore

    if sentimentScore >= buyThreshold:
        return OrderHookSuggestion(
            direction=OrderHookDirection.BUY,
            confidence=min(1.0, sentimentScore),
            explanation=(
                f"illustrative sentiment score {sentimentScore:.3f} >= buyThreshold {buyThreshold} "
                f"({scoreResult.positiveWordCount} positive / {scoreResult.negativeWordCount} negative "
                "toy-lexicon word matches) — SUGGESTION ONLY, no order was placed"
            ),
            killSwitchEngaged=False,
        )
    if sentimentScore <= sellThreshold:
        return OrderHookSuggestion(
            direction=OrderHookDirection.SELL,
            confidence=min(1.0, abs(sentimentScore)),
            explanation=(
                f"illustrative sentiment score {sentimentScore:.3f} <= sellThreshold {sellThreshold} "
                f"({scoreResult.positiveWordCount} positive / {scoreResult.negativeWordCount} negative "
                "toy-lexicon word matches) — SUGGESTION ONLY, no order was placed"
            ),
            killSwitchEngaged=False,
        )
    return OrderHookSuggestion(
        direction=OrderHookDirection.HOLD,
        confidence=1.0 - abs(sentimentScore),
        explanation=(
            f"illustrative sentiment score {sentimentScore:.3f} is within "
            f"[{sellThreshold}, {buyThreshold}] — no directional suggestion, SUGGESTION ONLY"
        ),
        killSwitchEngaged=False,
    )
