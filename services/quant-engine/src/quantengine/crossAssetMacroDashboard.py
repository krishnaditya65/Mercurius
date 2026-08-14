"""Cross-asset macro dashboard (yields, DXY, crude, VIX vs. equity
indices). See FEATURES.md §22 ("Deep Quant & Algorithmic Trading
Internals") — "Cross-asset macro dashboard (yields, DXY, crude, VIX vs.
equity indices)".

**Read this before treating any number produced by this module as real
macro research.** A real cross-asset macro dashboard needs live feeds for
Treasury yields, the DXY dollar index, WTI/Brent crude, the VIX, and
equity index levels — none of which this repo ingests anywhere. NO
network call, scraper, or market-data vendor integration exists in this
module. Every named time series this module accepts
(`namedMacroSeriesByLabel`, e.g. `"US10Y_YIELD"`, `"DXY"`,
`"WTI_CRUDE"`, `"VIX"`, `"SPX"`) is an ILLUSTRATIVE/FIXTURE series a
caller supplies — exactly like `esgScoringEngine.py`'s fabricated
dataset and `crossAssetMacroDashboard.py`'s sibling illustrative-input
modules elsewhere in this pass.

**What IS real**: a genuine data-aggregation/normalization structure
(`buildMacroDashboardSnapshot`) that takes multiple named series, checks
they're aligned (same observation count — this module does no resampling
of its own, same convention as `correlationMatrixEngine.py`), and
computes REAL cross-asset correlations across every pair by REUSING
`correlationMatrixEngine.buildPairwiseCorrelationMatrix` VERBATIM (not
reimplemented — this module contains zero correlation math of its own).
On top of the reused correlation matrix, this module adds one small,
real, genuinely-computed convenience: `findStrongestMacroCorrelationPairs`,
a thin sort/filter over the SAME reused
`findPairsTradingCandidatePairsAboveCorrelationThreshold` helper, framed
for a "which macro series are currently moving together" dashboard
reading rather than a pairs-trading-entry reading (the same real-vs-
reframed relationship `strategyCorrelationMatrix.py` has with
`correlationMatrixEngine.py` — see that module's docstring for the same
pattern applied to strategy returns instead of macro series).

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

# Illustrative label constants for common macro series a caller might
# supply — purely documentation/convenience, not enforced anywhere below
# (any string label works as a dict key). NONE of these carry real data;
# see the module docstring.
ILLUSTRATIVE_MACRO_SERIES_LABELS = (
    "US10Y_YIELD",
    "DXY",
    "WTI_CRUDE",
    "VIX",
    "SPX",
)


@dataclass(frozen=True)
class MacroDashboardSnapshot:
    seriesLabelsInOrder: list[str]
    observationCount: int
    correlationMatrix: CorrelationMatrixResult

    def toDenseCorrelationGrid(self) -> list[list[float]]:
        return self.correlationMatrix.toDenseGrid()


def buildMacroDashboardSnapshot(namedMacroSeriesByLabel: dict[str, list[float]]) -> MacroDashboardSnapshot:
    """Real aggregation/normalization step: validates every named series
    in `namedMacroSeriesByLabel` has the SAME observation count (raises
    `ValueError` naming the offending label if not — this module does no
    resampling/interpolation of misaligned series, same explicit
    limitation `correlationMatrixEngine.py` documents), then computes the
    real pairwise Pearson correlation matrix across every series by
    reusing `correlationMatrixEngine.buildPairwiseCorrelationMatrix`
    verbatim. Raises `ValueError` if fewer than two series are supplied
    (propagated from the reused correlation engine).
    """
    if not namedMacroSeriesByLabel:
        raise ValueError("namedMacroSeriesByLabel must contain at least one macro series")

    labelsInOrder = list(namedMacroSeriesByLabel.keys())
    referenceLabel = labelsInOrder[0]
    referenceLength = len(namedMacroSeriesByLabel[referenceLabel])
    for label in labelsInOrder[1:]:
        seriesLength = len(namedMacroSeriesByLabel[label])
        if seriesLength != referenceLength:
            raise ValueError(
                f"macro series '{label}' has {seriesLength} observations but '{referenceLabel}' has "
                f"{referenceLength} — all series must be time-aligned by the caller before aggregation "
                "(this module performs no resampling of its own)"
            )

    correlationMatrix = buildPairwiseCorrelationMatrix(namedMacroSeriesByLabel)

    return MacroDashboardSnapshot(
        seriesLabelsInOrder=labelsInOrder,
        observationCount=referenceLength,
        correlationMatrix=correlationMatrix,
    )


def findStrongestMacroCorrelationPairs(
    snapshot: MacroDashboardSnapshot, minimumAbsoluteCorrelationThreshold: float
) -> list[PairsTradingCandidatePair]:
    """Thin, real wrapper reusing
    `correlationMatrixEngine.findPairsTradingCandidatePairsAboveCorrelationThreshold`
    verbatim over `snapshot.correlationMatrix` — same real correlation
    numbers as the dense grid, reframed for a "which macro series are
    currently moving together (or apart)" dashboard reading. Sorted by
    descending absolute correlation, exactly like the reused helper's own
    documented sort order.
    """
    return findPairsTradingCandidatePairsAboveCorrelationThreshold(
        snapshot.correlationMatrix, minimumAbsoluteCorrelationThreshold
    )
