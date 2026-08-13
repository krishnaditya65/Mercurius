"""ESG (Environmental/Social/Governance) composite scoring and screening.
See FEATURES.md §17 ("Wealth & Product Breadth") — this is the
"ESG/sustainability scoring and screening filters" [P3] item.

============================================================================
READ THIS BEFORE USING ANY NUMBER FROM THIS MODULE IN A REAL DECISION
============================================================================
`ILLUSTRATIVE_ESG_DATASET_BY_SYMBOL` below is a STATIC, HAND-FABRICATED set
of per-symbol Environmental/Social/Governance sub-scores this module ships
with for demonstration and testing purposes ONLY. It is NOT sourced from
MSCI, Sustainalytics, ISS ESG, Refinitiv, Bloomberg ESG, or any other real
ESG rating agency or data vendor — nobody researched these companies'
actual environmental, social, or governance practices. The scores are
illustrative fixture data, exactly like `illustrativeSentimentTradingHook.py`'s
toy lexicon and `valueAtRiskCalculator.py`'s illustrative stress scenarios
elsewhere in this service.

What IS real and correctly implemented here:
  - The composite-score WEIGHTED-AVERAGE FORMULA (`calculateCompositeEsgScore`)
    — a real, documented, testable weighting of the three pillar sub-scores.
  - The SCREENING/FILTERING LOGIC (`screenCandidateSymbolsAgainstEsgCriteria`)
    — real minimum-score and sector-exclusion filtering, and real descending
    ranking by composite score.

What is NOT real: the underlying per-symbol E/S/G sub-scores themselves.
Wiring this module to a real ESG data vendor's per-symbol feed is a
documented future integration, not attempted here.

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

from dataclasses import dataclass, field

# --- Pillar weighting -------------------------------------------------
#
# The composite ESG score is a real weighted average of the three pillar
# sub-scores:
#
#     compositeEsgScore = ENVIRONMENTAL_PILLAR_WEIGHT * environmentalScore
#                        + SOCIAL_PILLAR_WEIGHT        * socialScore
#                        + GOVERNANCE_PILLAR_WEIGHT     * governanceScore
#
# Weights sum to 1.0 and slightly overweight the Environmental pillar
# (0.40) relative to Social and Governance (0.30 each) — a commonly cited
# weighting emphasis in retail-facing ESG composite methodologies (climate/
# environmental impact tends to be the most visible pillar to end
# investors). This is a documented, fixed methodology choice for this
# module, not a tuned/backtested number and not a claim that it matches
# any specific real rating agency's proprietary weighting scheme.
ENVIRONMENTAL_PILLAR_WEIGHT = 0.40
SOCIAL_PILLAR_WEIGHT = 0.30
GOVERNANCE_PILLAR_WEIGHT = 0.30

_PILLAR_WEIGHT_SUM = ENVIRONMENTAL_PILLAR_WEIGHT + SOCIAL_PILLAR_WEIGHT + GOVERNANCE_PILLAR_WEIGHT
assert abs(_PILLAR_WEIGHT_SUM - 1.0) < 1e-12, "ESG pillar weights must sum to exactly 1.0"

# A small, illustrative sector-exclusion vocabulary — the "controversial
# sector" flags a screening caller can ask to exclude. Not an exhaustive or
# regulatory-grade exclusion taxonomy (real ESG exclusion policies also
# cover e.g. controversial weapons, gambling, adult entertainment); this is
# a deliberately small illustrative set sufficient to exercise real
# sector-exclusion screening logic.
KNOWN_CONTROVERSIAL_SECTOR_FLAGS = frozenset({"TOBACCO", "THERMAL_COAL"})


def validateZeroToOneHundredScore(scoreValue: float, scoreFieldName: str) -> None:
    """Raises `ValueError` if `scoreValue` is outside the inclusive [0, 100]
    range every E/S/G sub-score in this module is defined on.
    """
    if not (0.0 <= scoreValue <= 100.0):
        raise ValueError(f"{scoreFieldName} must be within [0, 100], got {scoreValue}")


def calculateCompositeEsgScore(
    environmentalScore: float, socialScore: float, governanceScore: float
) -> float:
    """The real, documented weighted-average composite ESG score:

        composite = 0.40 * environmentalScore
                  + 0.30 * socialScore
                  + 0.30 * governanceScore

    Each sub-score must be within [0, 100] (raises `ValueError` otherwise).
    Since the three weights sum to exactly 1.0, the composite is itself
    always within [0, 100].

    Hand-worked example (see `tests/test_esgScoringEngine.py` for the
    exact assertion): environmentalScore=70, socialScore=60,
    governanceScore=80 ->
        0.40*70 + 0.30*60 + 0.30*80 = 28.0 + 18.0 + 24.0 = 70.0
    """
    validateZeroToOneHundredScore(environmentalScore, "environmentalScore")
    validateZeroToOneHundredScore(socialScore, "socialScore")
    validateZeroToOneHundredScore(governanceScore, "governanceScore")
    return (
        ENVIRONMENTAL_PILLAR_WEIGHT * environmentalScore
        + SOCIAL_PILLAR_WEIGHT * socialScore
        + GOVERNANCE_PILLAR_WEIGHT * governanceScore
    )


@dataclass(frozen=True)
class EsgProfile:
    """One symbol's full ESG profile: the three pillar sub-scores, the
    computed composite score, and any controversial-sector flags that
    apply to it. `controversialSectorFlags` is a frozenset since a symbol
    could in principle carry more than one flag (this illustrative
    dataset only ever assigns zero or one, but the screening logic below
    doesn't assume that).
    """

    symbol: str
    environmentalScore: float
    socialScore: float
    governanceScore: float
    compositeEsgScore: float
    controversialSectorFlags: frozenset[str] = field(default_factory=frozenset)


def buildEsgProfile(
    symbol: str,
    environmentalScore: float,
    socialScore: float,
    governanceScore: float,
    controversialSectorFlags: frozenset[str] = frozenset(),
) -> EsgProfile:
    """Constructs an `EsgProfile`, computing `compositeEsgScore` from the
    three sub-scores via `calculateCompositeEsgScore` rather than letting a
    caller pass an inconsistent composite in directly — the composite is
    always derived, never independently supplied.
    """
    compositeEsgScore = calculateCompositeEsgScore(environmentalScore, socialScore, governanceScore)
    return EsgProfile(
        symbol=symbol,
        environmentalScore=environmentalScore,
        socialScore=socialScore,
        governanceScore=governanceScore,
        compositeEsgScore=compositeEsgScore,
        controversialSectorFlags=frozenset(controversialSectorFlags),
    )


# --- ILLUSTRATIVE, FABRICATED per-symbol ESG dataset -------------------
#
# Symbols reuse this repo's existing illustrative demo-symbol naming
# convention (see `services/oms-gateway` and `services/market-data`, which
# already use "DEMO-EQ" and "SIM-AAPL" as illustrative fixture symbols)
# plus a couple of newly invented "SIM-" symbols for this module's
# controversial-sector screening tests. NONE of these E/S/G sub-scores are
# sourced from a real rating agency — see the module docstring above.
ILLUSTRATIVE_ESG_DATASET_BY_SYMBOL: dict[str, EsgProfile] = {
    profile.symbol: profile
    for profile in (
        buildEsgProfile("DEMO-EQ", environmentalScore=70.0, socialScore=60.0, governanceScore=80.0),
        buildEsgProfile("SIM-AAPL", environmentalScore=82.0, socialScore=75.0, governanceScore=88.0),
        buildEsgProfile("SIM-MSFT", environmentalScore=78.0, socialScore=80.0, governanceScore=90.0),
        buildEsgProfile("SIM-JPM", environmentalScore=60.0, socialScore=55.0, governanceScore=70.0),
        buildEsgProfile(
            "SIM-THERMAL-COAL-CO",
            environmentalScore=20.0,
            socialScore=40.0,
            governanceScore=55.0,
            controversialSectorFlags=frozenset({"THERMAL_COAL"}),
        ),
        buildEsgProfile(
            "SIM-TOBACCO-CO",
            environmentalScore=55.0,
            socialScore=30.0,
            governanceScore=60.0,
            controversialSectorFlags=frozenset({"TOBACCO"}),
        ),
    )
}


def lookupEsgProfileForSymbol(symbol: str) -> EsgProfile:
    """Raises `KeyError` for a symbol not present in the illustrative
    dataset — callers (including the HTTP layer) turn that into a
    "known but unrated" / "unknown symbol" response rather than silently
    fabricating a score for a symbol this module has no data for.
    """
    if symbol not in ILLUSTRATIVE_ESG_DATASET_BY_SYMBOL:
        raise KeyError(f"no illustrative ESG profile for symbol {symbol!r}")
    return ILLUSTRATIVE_ESG_DATASET_BY_SYMBOL[symbol]


@dataclass(frozen=True)
class EsgScreeningCriteria:
    """A real, composable set of screening criteria. Every field is
    optional (`None` / empty means "don't filter on this"); a caller
    combines as many as it wants and every supplied criterion must pass
    (logical AND) for a symbol to survive screening.
    """

    minimumCompositeEsgScore: float | None = None
    minimumEnvironmentalScore: float | None = None
    minimumSocialScore: float | None = None
    minimumGovernanceScore: float | None = None
    excludedControversialSectorFlags: frozenset[str] = field(default_factory=frozenset)


def doesEsgProfilePassScreeningCriteria(profile: EsgProfile, criteria: EsgScreeningCriteria) -> bool:
    """Real, testable AND-combination of every supplied criterion:

      - `minimumCompositeEsgScore` / `minimumEnvironmentalScore` /
        `minimumSocialScore` / `minimumGovernanceScore`: the profile's
        corresponding score must be >= the threshold (inclusive boundary
        — a profile exactly at the minimum passes).
      - `excludedControversialSectorFlags`: the profile FAILS if its
        `controversialSectorFlags` intersects this set at all (i.e. it
        carries ANY excluded flag), regardless of how high its scores are
        — a real exclusionary screen overrides scoring, which is the
        entire point of a sector exclusion list.
    """
    if (
        criteria.minimumCompositeEsgScore is not None
        and profile.compositeEsgScore < criteria.minimumCompositeEsgScore
    ):
        return False
    if (
        criteria.minimumEnvironmentalScore is not None
        and profile.environmentalScore < criteria.minimumEnvironmentalScore
    ):
        return False
    if criteria.minimumSocialScore is not None and profile.socialScore < criteria.minimumSocialScore:
        return False
    if (
        criteria.minimumGovernanceScore is not None
        and profile.governanceScore < criteria.minimumGovernanceScore
    ):
        return False
    if profile.controversialSectorFlags & criteria.excludedControversialSectorFlags:
        return False
    return True


@dataclass(frozen=True)
class EsgScreeningResult:
    """`rankedProfiles` is every candidate symbol that both (a) has a
    known illustrative ESG profile and (b) passes every supplied
    criterion, sorted by `compositeEsgScore` DESCENDING (ties broken
    alphabetically by symbol for a deterministic order). `excludedSymbols`
    is known symbols that failed at least one criterion.
    `unknownSymbols` is candidate symbols with no illustrative ESG profile
    at all — kept separate from `excludedSymbols` since "no data" is a
    different condition from "failed the screen".
    """

    rankedProfiles: list[EsgProfile]
    excludedSymbols: list[str]
    unknownSymbols: list[str]


def rankEsgProfilesDescendingByCompositeScore(profiles: list[EsgProfile]) -> list[EsgProfile]:
    """Sorts a list of `EsgProfile` by `compositeEsgScore` descending, with
    ties broken alphabetically by `symbol` for a deterministic order.
    Returns a new list; does not mutate `profiles`. Extracted as its own
    function so the ranking rule is independently testable, e.g. against
    two profiles with an identical composite score.
    """
    return sorted(profiles, key=lambda profile: (-profile.compositeEsgScore, profile.symbol))


def screenCandidateSymbolsAgainstEsgCriteria(
    candidateSymbols: list[str], criteria: EsgScreeningCriteria
) -> EsgScreeningResult:
    """The real screening/ranking entry point: classifies every candidate
    symbol as unknown (no illustrative ESG profile), excluded (known, but
    failed at least one criterion), or ranked (known and passed every
    criterion — included in `rankedProfiles`, sorted descending by
    `compositeEsgScore`).

    An empty `candidateSymbols` list is valid and simply produces an
    all-empty `EsgScreeningResult` — not an error, since "screen zero
    candidates" is a well-defined (if useless) request.
    """
    rankedProfiles: list[EsgProfile] = []
    excludedSymbols: list[str] = []
    unknownSymbols: list[str] = []

    for symbol in candidateSymbols:
        if symbol not in ILLUSTRATIVE_ESG_DATASET_BY_SYMBOL:
            unknownSymbols.append(symbol)
            continue
        profile = ILLUSTRATIVE_ESG_DATASET_BY_SYMBOL[symbol]
        if doesEsgProfilePassScreeningCriteria(profile, criteria):
            rankedProfiles.append(profile)
        else:
            excludedSymbols.append(symbol)

    return EsgScreeningResult(
        rankedProfiles=rankEsgProfilesDescendingByCompositeScore(rankedProfiles),
        excludedSymbols=excludedSymbols,
        unknownSymbols=unknownSymbols,
    )
