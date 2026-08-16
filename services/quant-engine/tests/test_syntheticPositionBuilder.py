from __future__ import annotations

import pytest

from quantengine.blackScholesOptionPricer import OptionGreeksResult
from quantengine.portfolioGreeksAggregator import PortfolioPosition
from quantengine.syntheticPositionBuilder import (
    OptionLegType,
    SyntheticPositionLeg,
    SyntheticStructureName,
    buildSyntheticPositionSummary,
    identifySyntheticStructure,
    validateProposedCombinationAgainstSyntheticStructure,
)


def testSyntheticLongStockValidCombination():
    legs = [
        SyntheticPositionLeg(OptionLegType.CALL, 1.0, strikePrice=100.0),
        SyntheticPositionLeg(OptionLegType.PUT, -1.0, strikePrice=100.0),
    ]
    result = validateProposedCombinationAgainstSyntheticStructure(legs, SyntheticStructureName.SYNTHETIC_LONG_STOCK)
    assert result.isValidMatch is True
    assert result.matchedStructureName == SyntheticStructureName.SYNTHETIC_LONG_STOCK


def testSyntheticShortStockValidCombination():
    legs = [
        SyntheticPositionLeg(OptionLegType.CALL, -1.0, strikePrice=50.0),
        SyntheticPositionLeg(OptionLegType.PUT, 1.0, strikePrice=50.0),
    ]
    result = validateProposedCombinationAgainstSyntheticStructure(legs, SyntheticStructureName.SYNTHETIC_SHORT_STOCK)
    assert result.isValidMatch is True


def testDifferentStrikesRejectsSyntheticLongStock():
    legs = [
        SyntheticPositionLeg(OptionLegType.CALL, 1.0, strikePrice=100.0),
        SyntheticPositionLeg(OptionLegType.PUT, -1.0, strikePrice=105.0),
    ]
    result = validateProposedCombinationAgainstSyntheticStructure(legs, SyntheticStructureName.SYNTHETIC_LONG_STOCK)
    assert result.isValidMatch is False
    assert "strike" in result.explanation


def testWrongSideRejectsSyntheticLongStock():
    # both long -> not put-call-parity synthetic stock
    legs = [
        SyntheticPositionLeg(OptionLegType.CALL, 1.0, strikePrice=100.0),
        SyntheticPositionLeg(OptionLegType.PUT, 1.0, strikePrice=100.0),
    ]
    result = validateProposedCombinationAgainstSyntheticStructure(legs, SyntheticStructureName.SYNTHETIC_LONG_STOCK)
    assert result.isValidMatch is False


def testScaledUpCombinationStillMatchesAsMultipleUnits():
    legs = [
        SyntheticPositionLeg(OptionLegType.CALL, 3.0, strikePrice=100.0),
        SyntheticPositionLeg(OptionLegType.PUT, -3.0, strikePrice=100.0),
    ]
    result = validateProposedCombinationAgainstSyntheticStructure(legs, SyntheticStructureName.SYNTHETIC_LONG_STOCK)
    assert result.isValidMatch is True
    assert "3" in result.explanation


def testSyntheticLongCallValidCombination():
    legs = [
        SyntheticPositionLeg(OptionLegType.UNDERLYING_SHARE, 1.0),
        SyntheticPositionLeg(OptionLegType.PUT, 1.0, strikePrice=100.0),
    ]
    result = validateProposedCombinationAgainstSyntheticStructure(legs, SyntheticStructureName.SYNTHETIC_LONG_CALL)
    assert result.isValidMatch is True


def testSyntheticLongPutValidCombination():
    legs = [
        SyntheticPositionLeg(OptionLegType.UNDERLYING_SHARE, -1.0),
        SyntheticPositionLeg(OptionLegType.CALL, 1.0, strikePrice=100.0),
    ]
    result = validateProposedCombinationAgainstSyntheticStructure(legs, SyntheticStructureName.SYNTHETIC_LONG_PUT)
    assert result.isValidMatch is True


def testSyntheticCoveredCallValidCombination():
    legs = [
        SyntheticPositionLeg(OptionLegType.UNDERLYING_SHARE, 1.0),
        SyntheticPositionLeg(OptionLegType.CALL, -1.0, strikePrice=100.0),
    ]
    result = validateProposedCombinationAgainstSyntheticStructure(
        legs, SyntheticStructureName.SYNTHETIC_COVERED_CALL
    )
    assert result.isValidMatch is True


def testIdentifySyntheticStructureFindsTheRightOneAmongAll():
    legs = [
        SyntheticPositionLeg(OptionLegType.CALL, 1.0, strikePrice=100.0),
        SyntheticPositionLeg(OptionLegType.PUT, -1.0, strikePrice=100.0),
    ]
    result = identifySyntheticStructure(legs)
    assert result.matchedStructureName == SyntheticStructureName.SYNTHETIC_LONG_STOCK


def testIdentifySyntheticStructureReturnsNoMatchForRandomLegs():
    legs = [
        SyntheticPositionLeg(OptionLegType.CALL, 1.0, strikePrice=100.0),
        SyntheticPositionLeg(OptionLegType.CALL, 1.0, strikePrice=105.0),
    ]
    result = identifySyntheticStructure(legs)
    assert result.isValidMatch is False
    assert result.matchedStructureName is None


def testEmptyLegsReturnsNoMatch():
    result = validateProposedCombinationAgainstSyntheticStructure([], SyntheticStructureName.SYNTHETIC_LONG_STOCK)
    assert result.isValidMatch is False


def testBuildSyntheticPositionSummaryCombinesGreeksAndValidation():
    legs = [
        SyntheticPositionLeg(OptionLegType.CALL, 1.0, strikePrice=100.0),
        SyntheticPositionLeg(OptionLegType.PUT, -1.0, strikePrice=100.0),
    ]
    callGreeks = OptionGreeksResult(delta=0.6, gamma=0.02, vegaPerOnePercentVolatilityChange=0.2, thetaPerCalendarDay=-0.03)
    putGreeks = OptionGreeksResult(delta=-0.4, gamma=0.02, vegaPerOnePercentVolatilityChange=0.2, thetaPerCalendarDay=-0.01)
    legPositions = [
        PortfolioPosition("long-call", 1.0, callGreeks),
        PortfolioPosition("short-put", -1.0, putGreeks),
    ]
    summary = buildSyntheticPositionSummary(legs, legPositions, callerSuppliedMarginEstimate=5000.0)
    assert summary.validation.isValidMatch is True
    assert summary.validation.matchedStructureName == SyntheticStructureName.SYNTHETIC_LONG_STOCK
    # netDelta = 1*0.6 + (-1)*(-0.4) = 0.6 + 0.4 = 1.0 -> synthetic long stock
    # should have delta near 1.0, the hallmark of a synthetic long-stock position.
    assert summary.combinedGreeks.netDelta == pytest.approx(1.0)
    assert summary.callerSuppliedMarginEstimate == 5000.0


def testBuildSyntheticPositionSummaryWithNoMarginEstimateDefaultsToNone():
    legs = [
        SyntheticPositionLeg(OptionLegType.CALL, 1.0, strikePrice=100.0),
        SyntheticPositionLeg(OptionLegType.PUT, -1.0, strikePrice=100.0),
    ]
    zeroGreeks = OptionGreeksResult(0.0, 0.0, 0.0, 0.0)
    legPositions = [PortfolioPosition("a", 1.0, zeroGreeks), PortfolioPosition("b", -1.0, zeroGreeks)]
    summary = buildSyntheticPositionSummary(legs, legPositions)
    assert summary.callerSuppliedMarginEstimate is None


def testMismatchedLegTypeSetIsRejected():
    legs = [SyntheticPositionLeg(OptionLegType.CALL, 1.0, strikePrice=100.0)]
    result = validateProposedCombinationAgainstSyntheticStructure(legs, SyntheticStructureName.SYNTHETIC_LONG_STOCK)
    assert result.isValidMatch is False
    assert "leg types" in result.explanation


def testDuplicateCallLegsAreNotMisidentifiedAsSyntheticLongStock():
    # Two CALL legs (aggregate quantity 2.0) + one PUT leg (quantity
    # -1.0), all at the same strike. Validating leg TYPES as a set alone
    # would see {CALL, PUT} and consider it a type-match against
    # SYNTHETIC_LONG_STOCK's {CALL: 1.0, PUT: -1.0} definition, and
    # checking quantities per-leg independently (rather than aggregated
    # by type) would let EACH CALL leg individually satisfy the 1:1
    # ratio — this must be correctly rejected, since the aggregated CALL
    # exposure (2.0) does not correspond to the same "unit multiplier" as
    # the aggregated PUT exposure (-1.0) implies.
    legs = [
        SyntheticPositionLeg(OptionLegType.CALL, 1.0, strikePrice=100.0),
        SyntheticPositionLeg(OptionLegType.CALL, 1.0, strikePrice=100.0),
        SyntheticPositionLeg(OptionLegType.PUT, -1.0, strikePrice=100.0),
    ]
    result = validateProposedCombinationAgainstSyntheticStructure(legs, SyntheticStructureName.SYNTHETIC_LONG_STOCK)
    assert result.isValidMatch is False
