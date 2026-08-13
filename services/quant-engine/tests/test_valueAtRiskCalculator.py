import math

import pytest

from quantengine.valueAtRiskCalculator import (
    PortfolioPosition,
    StressScenario,
    calculateHistoricalValueAtRisk,
    calculateInverseStandardNormalCumulativeDistribution,
    calculateParametricValueAtRisk,
    calculatePortfolioStressTestPnLImpact,
)

# --- Hand-worked historical VaR case -------------------------------------
# periodicReturns = [-0.05, -0.03, -0.02, -0.01, 0.0, 0.01, 0.02, 0.03, 0.04, 0.05]
# (already sorted, n=10)
#
# confidenceLevel=0.90 -> percentileIndex = floor((1-0.90)*10) = floor(1.0) = 1
#   sortedReturns[1] = -0.03 -> VaR = 0.03
# confidenceLevel=0.95 -> percentileIndex = floor((1-0.95)*10) = floor(0.5) = 0
#   sortedReturns[0] = -0.05 -> VaR = 0.05
HAND_WORKED_RETURNS = [-0.05, -0.03, -0.02, -0.01, 0.0, 0.01, 0.02, 0.03, 0.04, 0.05]


def test_historicalVarMatchesHandWorkedValueAt90PercentConfidence():
    var = calculateHistoricalValueAtRisk(HAND_WORKED_RETURNS, confidenceLevel=0.90)
    assert math.isclose(var, 0.03, rel_tol=1e-12)


def test_historicalVarMatchesHandWorkedValueAt95PercentConfidence():
    var = calculateHistoricalValueAtRisk(HAND_WORKED_RETURNS, confidenceLevel=0.95)
    assert math.isclose(var, 0.05, rel_tol=1e-12)


def test_historicalVarUsesUnsortedInputCorrectly():
    shuffled = [0.02, -0.05, 0.05, -0.01, 0.0, -0.03, 0.03, -0.02, 0.04, 0.01]
    var = calculateHistoricalValueAtRisk(shuffled, confidenceLevel=0.90)
    assert math.isclose(var, 0.03, rel_tol=1e-12)


def test_historicalVarRaisesOnEmptySeries():
    with pytest.raises(ValueError):
        calculateHistoricalValueAtRisk([], confidenceLevel=0.95)


def test_historicalVarRaisesOnOutOfRangeConfidence():
    with pytest.raises(ValueError):
        calculateHistoricalValueAtRisk(HAND_WORKED_RETURNS, confidenceLevel=1.0)
    with pytest.raises(ValueError):
        calculateHistoricalValueAtRisk(HAND_WORKED_RETURNS, confidenceLevel=0.0)


def test_historicalVarClampsIndexAtVeryHighConfidenceWithSmallSample():
    # n=10, confidenceLevel=0.999 -> floor(0.001*10)=0, still in range;
    # confidenceLevel very close to 1 with a tiny n should clamp to the
    # worst observation rather than index-error.
    var = calculateHistoricalValueAtRisk(HAND_WORKED_RETURNS, confidenceLevel=0.9999)
    assert math.isclose(var, 0.05, rel_tol=1e-12)


# --- Inverse normal CDF (used by parametric VaR) -------------------------


def test_inverseNormalCdfMatchesKnownStandardQuantiles():
    z95 = calculateInverseStandardNormalCumulativeDistribution(0.95)
    z99 = calculateInverseStandardNormalCumulativeDistribution(0.99)
    assert math.isclose(z95, 1.6448536269514722, abs_tol=1e-6)
    assert math.isclose(z99, 2.3263478740408408, abs_tol=1e-6)


def test_inverseNormalCdfOfOneHalfIsZero():
    z50 = calculateInverseStandardNormalCumulativeDistribution(0.5)
    assert math.isclose(z50, 0.0, abs_tol=1e-6)


def test_inverseNormalCdfRaisesOnOutOfRangeProbability():
    with pytest.raises(ValueError):
        calculateInverseStandardNormalCumulativeDistribution(0.0)
    with pytest.raises(ValueError):
        calculateInverseStandardNormalCumulativeDistribution(1.0)


# --- Hand-worked parametric VaR case -------------------------------------
# Reuses riskStatistics.py's own hand-worked series:
# periodicReturns = [0.04, -0.02, 0.04, -0.02, 0.04, -0.02]
# mean = 0.01, population stddev = 0.03 (both hand-derived there)
#
# z(0.95) ~= 1.6448536269514715  ->  VaR = 1.6448536269514715*0.03 - 0.01
#          = 0.049345608808544145 - 0.01 = 0.039345608808544145
# z(0.99) ~= 2.326347874040838   ->  VaR = 2.326347874040838*0.03 - 0.01
#          = 0.06979043622122514 - 0.01 = 0.05979043622122514
PARAMETRIC_HAND_WORKED_RETURNS = [0.04, -0.02, 0.04, -0.02, 0.04, -0.02]


def test_parametricVarMatchesHandWorkedValueAt95PercentConfidence():
    var = calculateParametricValueAtRisk(PARAMETRIC_HAND_WORKED_RETURNS, confidenceLevel=0.95)
    assert math.isclose(var, 0.039345608808544145, abs_tol=1e-6)


def test_parametricVarMatchesHandWorkedValueAt99PercentConfidence():
    var = calculateParametricValueAtRisk(PARAMETRIC_HAND_WORKED_RETURNS, confidenceLevel=0.99)
    assert math.isclose(var, 0.05979043622122514, abs_tol=1e-6)


def test_parametricVarIncreasesWithConfidenceLevel():
    var90 = calculateParametricValueAtRisk(PARAMETRIC_HAND_WORKED_RETURNS, confidenceLevel=0.90)
    var95 = calculateParametricValueAtRisk(PARAMETRIC_HAND_WORKED_RETURNS, confidenceLevel=0.95)
    var99 = calculateParametricValueAtRisk(PARAMETRIC_HAND_WORKED_RETURNS, confidenceLevel=0.99)
    assert var90 < var95 < var99


def test_parametricVarRaisesOnZeroVarianceSeries():
    with pytest.raises(ValueError):
        calculateParametricValueAtRisk([0.01, 0.01, 0.01], confidenceLevel=0.95)


def test_parametricVarRaisesOnEmptySeries():
    with pytest.raises(ValueError):
        calculateParametricValueAtRisk([], confidenceLevel=0.95)


# --- Stress testing --------------------------------------------------


def test_stressTestAppliesEquityShockScenarioAndComputesPnlImpact():
    positions = [
        PortfolioPosition(symbol="AAPL", quantity=100, currentPrice=200.0),
        PortfolioPosition(symbol="BONDX", quantity=1000, currentPrice=100.0),
    ]
    scenarios = [
        StressScenario(scenarioName="-20% equity shock (illustrative)", shockPercentageBySymbol={"AAPL": -0.20}),
        StressScenario(
            scenarioName="+2% rate shock (illustrative)", shockPercentageBySymbol={"BONDX": -0.02}
        ),
    ]
    result = calculatePortfolioStressTestPnLImpact(positions, scenarios)
    # AAPL market value = 100*200 = 20,000; -20% shock -> -4,000; BONDX untouched -> 0
    assert math.isclose(result["-20% equity shock (illustrative)"], -4000.0, rel_tol=1e-12)
    # BONDX market value = 1000*100 = 100,000; -2% shock -> -2,000; AAPL untouched -> 0
    assert math.isclose(result["+2% rate shock (illustrative)"], -2000.0, rel_tol=1e-12)


def test_stressTestUnshockedSymbolContributesZero():
    positions = [PortfolioPosition(symbol="XYZ", quantity=10, currentPrice=50.0)]
    scenarios = [StressScenario(scenarioName="unrelated shock", shockPercentageBySymbol={"OTHER": -0.5})]
    result = calculatePortfolioStressTestPnLImpact(positions, scenarios)
    assert result["unrelated shock"] == 0.0


def test_stressTestCombinesMultiplePositionsInSameScenario():
    positions = [
        PortfolioPosition(symbol="AAA", quantity=10, currentPrice=100.0),  # value 1000
        PortfolioPosition(symbol="BBB", quantity=20, currentPrice=50.0),  # value 1000
    ]
    scenarios = [
        StressScenario(scenarioName="broad shock", shockPercentageBySymbol={"AAA": -0.1, "BBB": -0.1})
    ]
    result = calculatePortfolioStressTestPnLImpact(positions, scenarios)
    assert math.isclose(result["broad shock"], -200.0, rel_tol=1e-12)


def test_stressTestRaisesOnEmptyPositionsOrScenarios():
    with pytest.raises(ValueError):
        calculatePortfolioStressTestPnLImpact([], [StressScenario("x", {})])
    with pytest.raises(ValueError):
        calculatePortfolioStressTestPnLImpact([PortfolioPosition("A", 1, 1.0)], [])


def test_stressTestRaisesOnDuplicateScenarioNames():
    positions = [PortfolioPosition(symbol="A", quantity=1, currentPrice=1.0)]
    scenarios = [StressScenario("dup", {}), StressScenario("dup", {})]
    with pytest.raises(ValueError):
        calculatePortfolioStressTestPnLImpact(positions, scenarios)
