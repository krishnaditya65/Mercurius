from __future__ import annotations

import pytest

from quantengine.strategyCorrelationMatrix import (
    buildStrategyCorrelationMatrix,
    identifyHiddenlyCorrelatedStrategyPairs,
)


def testHandWorkedPerfectPositiveCorrelationBetweenTwoStrategies():
    # StrategyA and StrategyB returns move in perfect lockstep (B = 2*A)
    # -> Pearson r = 1.0 exactly, a textbook "secretly correlated bet".
    returns = {
        "MomentumAlpha": [0.01, -0.02, 0.03, -0.01],
        "TrendFollowingBeta": [0.02, -0.04, 0.06, -0.02],
    }
    matrix = buildStrategyCorrelationMatrix(returns)
    assert matrix.getCorrelation("MomentumAlpha", "TrendFollowingBeta") == pytest.approx(1.0)


def testHandWorkedPerfectNegativeCorrelation():
    returns = {
        "LongVolStrategy": [0.01, -0.02, 0.03],
        "ShortVolStrategy": [-0.01, 0.02, -0.03],
    }
    matrix = buildStrategyCorrelationMatrix(returns)
    assert matrix.getCorrelation("LongVolStrategy", "ShortVolStrategy") == pytest.approx(-1.0)


def testUncorrelatedStrategiesAreNotFlaggedAsHiddenlyCorrelated():
    returns = {
        "PairsTradeDesk": [0.01, -0.01, 0.01, -0.01],
        "MacroCarryDesk": [0.01, 0.01, -0.01, -0.01],
    }
    matrix = buildStrategyCorrelationMatrix(returns)
    hiddenPairs = identifyHiddenlyCorrelatedStrategyPairs(matrix, concentrationRiskCorrelationThreshold=0.7)
    assert hiddenPairs == []


def testHighlyCorrelatedStrategiesAreFlaggedAsHiddenlyCorrelated():
    returns = {
        "MomentumAlpha": [0.01, -0.02, 0.03, -0.01],
        "TrendFollowingBeta": [0.02, -0.04, 0.06, -0.02],
        "UnrelatedMeanReversion": [0.01, 0.01, -0.01, -0.01],
    }
    matrix = buildStrategyCorrelationMatrix(returns)
    hiddenPairs = identifyHiddenlyCorrelatedStrategyPairs(matrix, concentrationRiskCorrelationThreshold=0.7)
    assert len(hiddenPairs) == 1
    pair = hiddenPairs[0]
    assert {pair.firstStrategyName, pair.secondStrategyName} == {"MomentumAlpha", "TrendFollowingBeta"}
    assert pair.correlationCoefficient == pytest.approx(1.0)


def testNegativelyCorrelatedStrategiesAreAlsoFlaggedByMagnitude():
    returns = {
        "LongVolStrategy": [0.01, -0.02, 0.03],
        "ShortVolStrategy": [-0.01, 0.02, -0.03],
    }
    matrix = buildStrategyCorrelationMatrix(returns)
    hiddenPairs = identifyHiddenlyCorrelatedStrategyPairs(matrix, concentrationRiskCorrelationThreshold=0.7)
    assert len(hiddenPairs) == 1
    assert hiddenPairs[0].correlationCoefficient == pytest.approx(-1.0)


def testHiddenPairsAreSortedByDescendingAbsoluteCorrelation():
    returns = {
        "A": [0.01, -0.02, 0.03, -0.01, 0.02],
        "B": [0.02, -0.04, 0.06, -0.02, 0.04],  # r(A,B) = 1.0
        "C": [0.015, -0.018, 0.025, -0.005, 0.01],  # weaker but still correlated
    }
    matrix = buildStrategyCorrelationMatrix(returns)
    hiddenPairs = identifyHiddenlyCorrelatedStrategyPairs(matrix, concentrationRiskCorrelationThreshold=0.5)
    absCorrelations = [abs(p.correlationCoefficient) for p in hiddenPairs]
    assert absCorrelations == sorted(absCorrelations, reverse=True)


def testFewerThanTwoStrategiesRaises():
    with pytest.raises(ValueError):
        buildStrategyCorrelationMatrix({"OnlyOne": [0.01, 0.02, 0.03]})


def testDefaultThresholdMatchesCorrelationMatrixEngineConvention():
    returns = {
        "A": [0.01, -0.02, 0.03, -0.01],
        "B": [0.02, -0.04, 0.06, -0.02],
    }
    matrix = buildStrategyCorrelationMatrix(returns)
    hiddenPairs = identifyHiddenlyCorrelatedStrategyPairs(matrix)
    assert len(hiddenPairs) == 1


def testThresholdOfOneOnlyFlagsPerfectCorrelation():
    returns = {
        "A": [0.01, -0.02, 0.03, -0.01],
        "B": [0.02, -0.04, 0.06, -0.02],  # r = 1.0 exactly
        "C": [0.01, 0.03, -0.02, 0.015],  # not perfectly correlated
    }
    matrix = buildStrategyCorrelationMatrix(returns)
    hiddenPairs = identifyHiddenlyCorrelatedStrategyPairs(matrix, concentrationRiskCorrelationThreshold=1.0)
    for pair in hiddenPairs:
        assert abs(pair.correlationCoefficient) == pytest.approx(1.0)
