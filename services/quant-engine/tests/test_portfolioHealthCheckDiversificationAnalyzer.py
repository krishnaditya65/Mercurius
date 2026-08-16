import pytest

from quantengine.portfolioHealthCheckDiversificationAnalyzer import (
    ConcentrationSeverity,
    PortfolioHoldingForHealthCheck,
    aggregatePortfolioWeightsBySector,
    calculateEffectiveNumberOfHoldings,
    calculateHerfindahlHirschmanIndex,
    classifyConcentrationSeverityFromHhi,
    performPortfolioHealthCheck,
)


def test_calculateHerfindahlHirschmanIndex_singleFullPositionIsMaximallyConcentrated():
    assert calculateHerfindahlHirschmanIndex([1.0]) == pytest.approx(10_000.0)


def test_calculateHerfindahlHirschmanIndex_fourEqualPositionsHandWorked():
    # HHI = 10000 * 4 * (0.25^2) = 10000 * 4 * 0.0625 = 2500.0
    assert calculateHerfindahlHirschmanIndex([0.25, 0.25, 0.25, 0.25]) == pytest.approx(2500.0)


def test_calculateHerfindahlHirschmanIndex_rejectsEmptyList():
    with pytest.raises(ValueError):
        calculateHerfindahlHirschmanIndex([])


def test_classifyConcentrationSeverityFromHhi_bandsMatchDojFtcThresholds():
    assert classifyConcentrationSeverityFromHhi(1000.0) == ConcentrationSeverity.LOW
    assert classifyConcentrationSeverityFromHhi(1500.0) == ConcentrationSeverity.MODERATE
    assert classifyConcentrationSeverityFromHhi(2000.0) == ConcentrationSeverity.MODERATE
    assert classifyConcentrationSeverityFromHhi(2500.0) == ConcentrationSeverity.HIGH
    assert classifyConcentrationSeverityFromHhi(9000.0) == ConcentrationSeverity.HIGH


def test_calculateEffectiveNumberOfHoldings_handWorkedExample():
    # HHI=2500 (four equal positions) -> effective N = 10000/2500 = 4.0
    assert calculateEffectiveNumberOfHoldings(2500.0) == pytest.approx(4.0)


def test_calculateEffectiveNumberOfHoldings_rejectsNonPositiveHhi():
    with pytest.raises(ValueError):
        calculateEffectiveNumberOfHoldings(0.0)


def test_aggregatePortfolioWeightsBySector_sumsByMatchingSector():
    holdings = [
        PortfolioHoldingForHealthCheck("A", "TECH", 0.3),
        PortfolioHoldingForHealthCheck("B", "TECH", 0.2),
        PortfolioHoldingForHealthCheck("C", "FINANCIALS", 0.5),
    ]
    weightsBySector = aggregatePortfolioWeightsBySector(holdings)
    assert weightsBySector == {"TECH": pytest.approx(0.5), "FINANCIALS": pytest.approx(0.5)}


def test_performPortfolioHealthCheck_rejectsEmptyHoldings():
    with pytest.raises(ValueError):
        performPortfolioHealthCheck([])


def test_performPortfolioHealthCheck_rejectsNegativeWeight():
    with pytest.raises(ValueError):
        PortfolioHoldingForHealthCheck("A", "TECH", -0.1)


def test_performPortfolioHealthCheck_diversifiedPortfolioIsLowSeverityWithNoHighNudges():
    holdings = [
        PortfolioHoldingForHealthCheck(f"SYM-{i}", sector, 0.1)
        for i, sector in enumerate(
            ["TECH", "FINANCIALS", "HEALTHCARE", "ENERGY", "UTILITIES", "CONSUMER", "INDUSTRIALS", "MATERIALS", "REAL_ESTATE", "TELECOM"]
        )
    ]
    result = performPortfolioHealthCheck(holdings)
    assert result.positionHhi == pytest.approx(1000.0)  # 10 equal 10% positions
    assert classifyConcentrationSeverityFromHhi(result.positionHhi) == ConcentrationSeverity.LOW
    assert not any(nudge.severity == ConcentrationSeverity.HIGH for nudge in result.nudges)


def test_performPortfolioHealthCheck_concentratedPortfolioIsHighSeverityWithHighNudges():
    holdings = [
        PortfolioHoldingForHealthCheck("MEGA", "TECH", 0.85),
        PortfolioHoldingForHealthCheck("SMALL1", "FINANCIALS", 0.10),
        PortfolioHoldingForHealthCheck("SMALL2", "FINANCIALS", 0.05),
    ]
    result = performPortfolioHealthCheck(holdings)
    assert result.positionHhi > 7000  # dominated by the 85% position
    assert classifyConcentrationSeverityFromHhi(result.positionHhi) == ConcentrationSeverity.HIGH
    assert any(nudge.severity == ConcentrationSeverity.HIGH for nudge in result.nudges)
    assert any("MEGA" in nudge.message for nudge in result.nudges)


def test_performPortfolioHealthCheck_differentConcentrationLevelsProduceDifferentNudgeText():
    """Direct proof (per the task requirement) that nudges are genuinely
    driven by the computed numbers, not canned regardless of input."""
    diversifiedSectors = [
        "TECH", "FINANCIALS", "HEALTHCARE", "ENERGY", "UTILITIES",
        "CONSUMER", "INDUSTRIALS", "MATERIALS", "REAL_ESTATE", "TELECOM",
    ]
    diversifiedHoldings = [
        PortfolioHoldingForHealthCheck(f"SYM-{i}", sector, 0.1) for i, sector in enumerate(diversifiedSectors)
    ]
    concentratedHoldings = [
        PortfolioHoldingForHealthCheck("MEGA", "TECH", 0.85),
        PortfolioHoldingForHealthCheck("SMALL1", "FINANCIALS", 0.10),
        PortfolioHoldingForHealthCheck("SMALL2", "FINANCIALS", 0.05),
    ]

    diversifiedResult = performPortfolioHealthCheck(diversifiedHoldings)
    concentratedResult = performPortfolioHealthCheck(concentratedHoldings)

    diversifiedMessages = {nudge.message for nudge in diversifiedResult.nudges}
    concentratedMessages = {nudge.message for nudge in concentratedResult.nudges}
    assert diversifiedMessages != concentratedMessages

    diversifiedSeverities = {nudge.severity for nudge in diversifiedResult.nudges}
    concentratedSeverities = {nudge.severity for nudge in concentratedResult.nudges}
    assert ConcentrationSeverity.HIGH in concentratedSeverities
    assert ConcentrationSeverity.HIGH not in diversifiedSeverities
    assert diversifiedSeverities != concentratedSeverities


def test_performPortfolioHealthCheck_sectorConcentrationNudgeReflectsDominantSector():
    holdings = [
        PortfolioHoldingForHealthCheck("A", "ENERGY", 0.30),
        PortfolioHoldingForHealthCheck("B", "ENERGY", 0.30),
        PortfolioHoldingForHealthCheck("C", "TECH", 0.20),
        PortfolioHoldingForHealthCheck("D", "HEALTHCARE", 0.20),
    ]
    result = performPortfolioHealthCheck(holdings)
    assert result.topSector == "ENERGY"
    assert result.topSectorWeight == pytest.approx(0.60)
    assert any("ENERGY" in nudge.message for nudge in result.nudges)


def test_performPortfolioHealthCheck_computesRealFactorExposureSummaryWhenSupplied():
    holdings = [
        PortfolioHoldingForHealthCheck("A", "TECH", 0.6, factorExposuresByName={"marketBeta": 1.5}),
        PortfolioHoldingForHealthCheck("B", "FINANCIALS", 0.4, factorExposuresByName={"marketBeta": 0.5}),
    ]
    result = performPortfolioHealthCheck(holdings)
    # weight-weighted: 0.6*1.5 + 0.4*0.5 = 0.9 + 0.2 = 1.1
    assert result.portfolioExposureByFactor["marketBeta"] == pytest.approx(1.1)
    assert any("marketBeta" in nudge.message for nudge in result.nudges)


def test_performPortfolioHealthCheck_noFactorSummaryWhenNotEveryHoldingSuppliesExposures():
    holdings = [
        PortfolioHoldingForHealthCheck("A", "TECH", 0.6, factorExposuresByName={"marketBeta": 1.5}),
        PortfolioHoldingForHealthCheck("B", "FINANCIALS", 0.4),
    ]
    result = performPortfolioHealthCheck(holdings)
    assert result.portfolioExposureByFactor is None


def test_performPortfolioHealthCheck_allZeroWeightPortfolioRaisesClearValueErrorNotInternalHhiMessage():
    # Every holding legitimately has 0.0 portfolioWeight (individually
    # valid per __post_init__, which only rejects NEGATIVE weights) — but
    # there's no meaningful weight distribution to compute an "effective
    # number of holdings" over. Should raise a clear, purpose-specific
    # ValueError rather than calculateEffectiveNumberOfHoldings's
    # confusing internal "hhi must be strictly positive..." message.
    holdings = [
        PortfolioHoldingForHealthCheck(symbol="A", sector="Tech", portfolioWeight=0.0),
        PortfolioHoldingForHealthCheck(symbol="B", sector="Tech", portfolioWeight=0.0),
    ]
    with pytest.raises(ValueError, match="portfolio weights must sum to a positive total"):
        performPortfolioHealthCheck(holdings)
