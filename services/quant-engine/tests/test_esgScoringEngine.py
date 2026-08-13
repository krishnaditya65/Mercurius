"""Tests for esgScoringEngine.py. See the module docstring for the
loud caveat: the underlying illustrative dataset is fabricated fixture
data, NOT sourced from any real ESG rating agency. These tests verify the
composite-score WEIGHTED-AVERAGE FORMULA and the SCREENING/RANKING LOGIC,
both of which are real.
"""

from __future__ import annotations

import math

import pytest

from quantengine.esgScoringEngine import (
    GOVERNANCE_PILLAR_WEIGHT,
    ILLUSTRATIVE_ESG_DATASET_BY_SYMBOL,
    ENVIRONMENTAL_PILLAR_WEIGHT,
    SOCIAL_PILLAR_WEIGHT,
    EsgProfile,
    EsgScreeningCriteria,
    EsgScreeningResult,
    buildEsgProfile,
    calculateCompositeEsgScore,
    doesEsgProfilePassScreeningCriteria,
    lookupEsgProfileForSymbol,
    rankEsgProfilesDescendingByCompositeScore,
    screenCandidateSymbolsAgainstEsgCriteria,
)

# --- Hand-worked composite-score reference case -------------------------
# environmentalScore=70, socialScore=60, governanceScore=80
# weights: E=0.40, S=0.30, G=0.30
#
#   composite = 0.40*70 + 0.30*60 + 0.30*80
#             = 28.0    + 18.0    + 24.0
#             = 70.0
HAND_WORKED_E = 70.0
HAND_WORKED_S = 60.0
HAND_WORKED_G = 80.0
HAND_WORKED_EXPECTED_COMPOSITE = 70.0


def test_pillarWeightsSumToExactlyOne():
    assert math.isclose(
        ENVIRONMENTAL_PILLAR_WEIGHT + SOCIAL_PILLAR_WEIGHT + GOVERNANCE_PILLAR_WEIGHT, 1.0, rel_tol=1e-12
    )


def test_compositeEsgScoreMatchesHandWorkedExample():
    composite = calculateCompositeEsgScore(HAND_WORKED_E, HAND_WORKED_S, HAND_WORKED_G)
    assert math.isclose(composite, HAND_WORKED_EXPECTED_COMPOSITE, rel_tol=1e-12)


def test_compositeEsgScoreOfAllZerosIsZero():
    assert calculateCompositeEsgScore(0.0, 0.0, 0.0) == 0.0


def test_compositeEsgScoreOfAllHundredsIsHundred():
    assert math.isclose(calculateCompositeEsgScore(100.0, 100.0, 100.0), 100.0, rel_tol=1e-12)


def test_compositeEsgScoreRaisesOnOutOfRangeSubScore():
    with pytest.raises(ValueError):
        calculateCompositeEsgScore(-1.0, 60.0, 80.0)
    with pytest.raises(ValueError):
        calculateCompositeEsgScore(70.0, 101.0, 80.0)
    with pytest.raises(ValueError):
        calculateCompositeEsgScore(70.0, 60.0, 200.0)


def test_buildEsgProfileDerivesCompositeRatherThanAcceptingItDirectly():
    profile = buildEsgProfile("TEST-SYM", HAND_WORKED_E, HAND_WORKED_S, HAND_WORKED_G)
    assert isinstance(profile, EsgProfile)
    assert math.isclose(profile.compositeEsgScore, HAND_WORKED_EXPECTED_COMPOSITE, rel_tol=1e-12)
    assert profile.controversialSectorFlags == frozenset()


def test_buildEsgProfileCarriesControversialSectorFlags():
    profile = buildEsgProfile(
        "TEST-COAL", 20.0, 40.0, 55.0, controversialSectorFlags=frozenset({"THERMAL_COAL"})
    )
    assert profile.controversialSectorFlags == frozenset({"THERMAL_COAL"})


# --- Illustrative dataset sanity -----------------------------------------


def test_illustrativeDatasetContainsExpectedDemoSymbols():
    # Reuses this repo's existing "DEMO-EQ" / "SIM-AAPL" illustrative demo
    # symbol convention from oms-gateway / market-data.
    assert "DEMO-EQ" in ILLUSTRATIVE_ESG_DATASET_BY_SYMBOL
    assert "SIM-AAPL" in ILLUSTRATIVE_ESG_DATASET_BY_SYMBOL


def test_lookupEsgProfileForKnownSymbolReturnsProfileWithConsistentComposite():
    profile = lookupEsgProfileForSymbol("DEMO-EQ")
    recomputed = calculateCompositeEsgScore(
        profile.environmentalScore, profile.socialScore, profile.governanceScore
    )
    assert math.isclose(profile.compositeEsgScore, recomputed, rel_tol=1e-12)


def test_lookupEsgProfileForUnknownSymbolRaisesKeyError():
    with pytest.raises(KeyError):
        lookupEsgProfileForSymbol("NOT-A-REAL-SYMBOL")


# --- Screening criteria: single-criterion pass/fail ----------------------


def test_doesEsgProfilePassScreeningCriteriaWithNoCriteriaAlwaysPasses():
    profile = lookupEsgProfileForSymbol("SIM-THERMAL-COAL-CO")
    assert doesEsgProfilePassScreeningCriteria(profile, EsgScreeningCriteria()) is True


def test_minimumCompositeEsgScoreCriterionIsInclusiveAtBoundary():
    profile = buildEsgProfile("BOUNDARY", HAND_WORKED_E, HAND_WORKED_S, HAND_WORKED_G)  # composite=70.0
    assert doesEsgProfilePassScreeningCriteria(
        profile, EsgScreeningCriteria(minimumCompositeEsgScore=70.0)
    )
    assert not doesEsgProfilePassScreeningCriteria(
        profile, EsgScreeningCriteria(minimumCompositeEsgScore=70.0001)
    )


def test_minimumPillarScoreCriterionFiltersOnASinglePillar():
    profile = lookupEsgProfileForSymbol("SIM-AAPL")  # E=82, S=75, G=88
    assert doesEsgProfilePassScreeningCriteria(profile, EsgScreeningCriteria(minimumGovernanceScore=85.0))
    assert not doesEsgProfilePassScreeningCriteria(
        profile, EsgScreeningCriteria(minimumGovernanceScore=90.0)
    )
    assert not doesEsgProfilePassScreeningCriteria(
        profile, EsgScreeningCriteria(minimumEnvironmentalScore=90.0)
    )


def test_sectorExclusionCriterionOverridesHighScores():
    # SIM-TOBACCO-CO has a decent-ish composite (49.0) but carries the
    # TOBACCO flag; a low minimumCompositeEsgScore alone would let it
    # through, but the exclusion flag must still veto it.
    profile = lookupEsgProfileForSymbol("SIM-TOBACCO-CO")
    assert doesEsgProfilePassScreeningCriteria(
        profile, EsgScreeningCriteria(minimumCompositeEsgScore=0.0)
    )
    assert not doesEsgProfilePassScreeningCriteria(
        profile,
        EsgScreeningCriteria(
            minimumCompositeEsgScore=0.0, excludedControversialSectorFlags=frozenset({"TOBACCO"})
        ),
    )


def test_sectorExclusionDoesNotAffectProfilesWithNoFlags():
    profile = lookupEsgProfileForSymbol("SIM-AAPL")
    assert doesEsgProfilePassScreeningCriteria(
        profile, EsgScreeningCriteria(excludedControversialSectorFlags=frozenset({"TOBACCO", "THERMAL_COAL"}))
    )


# --- Full screening/ranking pipeline --------------------------------------


def test_screenCandidateSymbolsRanksDescendingByCompositeScore():
    result = screenCandidateSymbolsAgainstEsgCriteria(
        ["DEMO-EQ", "SIM-AAPL", "SIM-MSFT", "SIM-JPM"], EsgScreeningCriteria()
    )
    assert isinstance(result, EsgScreeningResult)
    rankedSymbolsInOrder = [profile.symbol for profile in result.rankedProfiles]
    # composites: SIM-MSFT=82.2, SIM-AAPL=81.7, DEMO-EQ=70.0, SIM-JPM=61.5
    assert rankedSymbolsInOrder == ["SIM-MSFT", "SIM-AAPL", "DEMO-EQ", "SIM-JPM"]
    assert result.excludedSymbols == []
    assert result.unknownSymbols == []


def test_screenCandidateSymbolsFiltersByMinimumCompositeScore():
    result = screenCandidateSymbolsAgainstEsgCriteria(
        ["DEMO-EQ", "SIM-AAPL", "SIM-MSFT", "SIM-JPM"],
        EsgScreeningCriteria(minimumCompositeEsgScore=70.0),
    )
    rankedSymbols = {profile.symbol for profile in result.rankedProfiles}
    assert rankedSymbols == {"DEMO-EQ", "SIM-AAPL", "SIM-MSFT"}
    assert result.excludedSymbols == ["SIM-JPM"]


def test_screenCandidateSymbolsFiltersByMinimumPillarScore():
    result = screenCandidateSymbolsAgainstEsgCriteria(
        ["DEMO-EQ", "SIM-AAPL", "SIM-MSFT"],
        EsgScreeningCriteria(minimumGovernanceScore=85.0),
    )
    rankedSymbols = {profile.symbol for profile in result.rankedProfiles}
    # DEMO-EQ governance=80 (fails), SIM-AAPL=88 (passes), SIM-MSFT=90 (passes)
    assert rankedSymbols == {"SIM-AAPL", "SIM-MSFT"}
    assert result.excludedSymbols == ["DEMO-EQ"]


def test_screenCandidateSymbolsAppliesSectorExclusionList():
    result = screenCandidateSymbolsAgainstEsgCriteria(
        ["DEMO-EQ", "SIM-THERMAL-COAL-CO", "SIM-TOBACCO-CO"],
        EsgScreeningCriteria(excludedControversialSectorFlags=frozenset({"TOBACCO", "THERMAL_COAL"})),
    )
    rankedSymbols = {profile.symbol for profile in result.rankedProfiles}
    assert rankedSymbols == {"DEMO-EQ"}
    assert set(result.excludedSymbols) == {"SIM-THERMAL-COAL-CO", "SIM-TOBACCO-CO"}


def test_screenCandidateSymbolsCombinesMultipleCriteria():
    result = screenCandidateSymbolsAgainstEsgCriteria(
        ["DEMO-EQ", "SIM-AAPL", "SIM-MSFT", "SIM-JPM", "SIM-THERMAL-COAL-CO", "SIM-TOBACCO-CO"],
        EsgScreeningCriteria(
            minimumCompositeEsgScore=60.0,
            minimumSocialScore=55.0,
            excludedControversialSectorFlags=frozenset({"TOBACCO", "THERMAL_COAL"}),
        ),
    )
    rankedSymbols = [profile.symbol for profile in result.rankedProfiles]
    # SIM-JPM: composite 61.5>=60, social 55>=55, no flags -> passes
    # DEMO-EQ: composite 70>=60, social 60>=55, no flags -> passes
    # SIM-AAPL/SIM-MSFT: pass both numeric criteria, no flags -> passes
    # SIM-THERMAL-COAL-CO / SIM-TOBACCO-CO: flagged -> excluded regardless of scores
    assert set(rankedSymbols) == {"SIM-MSFT", "SIM-AAPL", "DEMO-EQ", "SIM-JPM"}
    assert set(result.excludedSymbols) == {"SIM-THERMAL-COAL-CO", "SIM-TOBACCO-CO"}


def test_screenCandidateSymbolsHandlesUnknownSymbolsSeparatelyFromExcluded():
    result = screenCandidateSymbolsAgainstEsgCriteria(
        ["DEMO-EQ", "NOT-A-REAL-SYMBOL", "ALSO-UNKNOWN"], EsgScreeningCriteria()
    )
    assert [profile.symbol for profile in result.rankedProfiles] == ["DEMO-EQ"]
    assert result.excludedSymbols == []
    assert set(result.unknownSymbols) == {"NOT-A-REAL-SYMBOL", "ALSO-UNKNOWN"}


def test_screenCandidateSymbolsWithEmptyCandidateListReturnsAllEmptyResult():
    result = screenCandidateSymbolsAgainstEsgCriteria([], EsgScreeningCriteria())
    assert result.rankedProfiles == []
    assert result.excludedSymbols == []
    assert result.unknownSymbols == []


def test_screenCandidateSymbolsWithCriteriaSoStrictNothingPassesReturnsEmptyRankedList():
    result = screenCandidateSymbolsAgainstEsgCriteria(
        ["DEMO-EQ", "SIM-AAPL", "SIM-MSFT", "SIM-JPM"],
        EsgScreeningCriteria(minimumCompositeEsgScore=99.0),
    )
    assert result.rankedProfiles == []
    assert set(result.excludedSymbols) == {"DEMO-EQ", "SIM-AAPL", "SIM-MSFT", "SIM-JPM"}


def test_rankEsgProfilesTieBreaksAlphabeticallyOnEqualComposite():
    # Two synthetic profiles with an identical composite score (both
    # 70.0, via different E/S/G combinations) must be ordered
    # alphabetically by symbol.
    profileZ = buildEsgProfile("Z-SYM", 70.0, 70.0, 70.0)  # composite=70.0
    profileA = buildEsgProfile("A-SYM", 60.0, 80.0, 70.0)  # 0.4*60+0.3*80+0.3*70=24+24+21=69.0... adjust
    # Recompute to guarantee an exact tie: choose sub-scores that also
    # yield composite=70.0 for profileA.
    profileA = buildEsgProfile("A-SYM", 100.0, 40.0, 60.0)  # 0.4*100+0.3*40+0.3*60=40+12+18=70.0
    assert math.isclose(profileZ.compositeEsgScore, profileA.compositeEsgScore, rel_tol=1e-12)

    ranked = rankEsgProfilesDescendingByCompositeScore([profileZ, profileA])
    assert [profile.symbol for profile in ranked] == ["A-SYM", "Z-SYM"]


def test_rankEsgProfilesDoesNotMutateInputList():
    profiles = [
        lookupEsgProfileForSymbol("DEMO-EQ"),
        lookupEsgProfileForSymbol("SIM-AAPL"),
    ]
    originalOrder = list(profiles)
    rankEsgProfilesDescendingByCompositeScore(profiles)
    assert profiles == originalOrder


def test_screenCandidateSymbolsHandlesDuplicateCandidateSymbols():
    result = screenCandidateSymbolsAgainstEsgCriteria(["DEMO-EQ", "DEMO-EQ"], EsgScreeningCriteria())
    # Duplicates are processed independently (no implicit dedup) — a
    # caller who passes the same symbol twice gets it ranked twice. This
    # documents the actual (simple, predictable) behavior rather than
    # silently deduping.
    assert [profile.symbol for profile in result.rankedProfiles] == ["DEMO-EQ", "DEMO-EQ"]
