import pytest

from quantengine.factorBasedPnlAttributionEngine import (
    SectorAttributionInput,
    computeBrinsonAttribution,
)


def test_computeBrinsonAttribution_rejectsEmptySectors():
    with pytest.raises(ValueError):
        computeBrinsonAttribution([])


def test_computeBrinsonAttribution_handWorkedTwoSectorExample():
    """Hand-computed expected values (see module docstring's worked
    example, reproduced here):

    Tech: wp=0.6 Rp=0.10 wb=0.5 Rb=0.08
      allocation = (0.6-0.5)*0.08 = 0.008
      selection  = 0.5*(0.10-0.08) = 0.01
      interaction= (0.1)*(0.02) = 0.002
    Financials: wp=0.4 Rp=0.03 wb=0.5 Rb=0.05
      allocation = (0.4-0.5)*0.05 = -0.005
      selection  = 0.5*(0.03-0.05) = -0.01
      interaction= (-0.1)*(-0.02) = 0.002

    totals: allocation=0.003, selection=0.0, interaction=0.004
    totalPortfolioReturn = 0.6*0.10+0.4*0.03 = 0.072
    totalBenchmarkReturn = 0.5*0.08+0.5*0.05 = 0.065
    activeReturn = 0.007
    0.003 + 0.0 + 0.004 == 0.007  (exact identity)
    """
    sectors = [
        SectorAttributionInput(
            sectorName="TECH", portfolioWeight=0.6, portfolioLocalReturn=0.10,
            benchmarkWeight=0.5, benchmarkLocalReturn=0.08,
        ),
        SectorAttributionInput(
            sectorName="FINANCIALS", portfolioWeight=0.4, portfolioLocalReturn=0.03,
            benchmarkWeight=0.5, benchmarkLocalReturn=0.05,
        ),
    ]
    result = computeBrinsonAttribution(sectors)

    techResult = next(r for r in result.sectorResults if r.sectorName == "TECH")
    finResult = next(r for r in result.sectorResults if r.sectorName == "FINANCIALS")

    assert techResult.allocationEffect == pytest.approx(0.008)
    assert techResult.selectionEffect == pytest.approx(0.01)
    assert techResult.interactionEffect == pytest.approx(0.002)

    assert finResult.allocationEffect == pytest.approx(-0.005)
    assert finResult.selectionEffect == pytest.approx(-0.01)
    assert finResult.interactionEffect == pytest.approx(0.002)

    assert result.totalPortfolioLocalReturn == pytest.approx(0.072)
    assert result.totalBenchmarkReturn == pytest.approx(0.065)
    assert result.totalActiveReturn == pytest.approx(0.007)

    assert result.totalAllocationEffect == pytest.approx(0.003)
    assert result.totalSelectionEffect == pytest.approx(0.0, abs=1e-12)
    assert result.totalInteractionEffect == pytest.approx(0.004)

    # The defining exact identity of the BHB decomposition:
    reconstructedActiveReturn = (
        result.totalAllocationEffect + result.totalSelectionEffect + result.totalInteractionEffect
    )
    assert reconstructedActiveReturn == pytest.approx(result.totalActiveReturn, abs=1e-12)


def test_computeBrinsonAttribution_identityHoldsForArbitraryMultiSectorInput():
    """Property-style check: the allocation+selection+interaction identity
    must hold exactly regardless of the specific numbers, for any number
    of sectors (as long as weights are internally consistent per side)."""
    sectors = [
        SectorAttributionInput("TECH", 0.30, 0.12, 0.25, 0.09),
        SectorAttributionInput("ENERGY", 0.15, -0.04, 0.20, -0.02),
        SectorAttributionInput("HEALTHCARE", 0.25, 0.06, 0.20, 0.07),
        SectorAttributionInput("FINANCIALS", 0.30, 0.02, 0.35, 0.03),
    ]
    result = computeBrinsonAttribution(sectors)
    reconstructedActiveReturn = (
        result.totalAllocationEffect + result.totalSelectionEffect + result.totalInteractionEffect
    )
    assert reconstructedActiveReturn == pytest.approx(result.totalActiveReturn, abs=1e-12)


def test_computeBrinsonAttribution_zeroCurrencyReturnGivesZeroCurrencyEffect():
    sectors = [SectorAttributionInput("TECH", 0.5, 0.1, 0.5, 0.08)]
    result = computeBrinsonAttribution(sectors)
    assert result.totalCurrencyEffect == pytest.approx(0.0)
    assert result.totalPortfolioReturnIncludingCurrency == pytest.approx(result.totalPortfolioLocalReturn)


def test_computeBrinsonAttribution_currencyEffectIsPortfolioWeightedContribution():
    sectors = [
        SectorAttributionInput(
            "TECH", portfolioWeight=0.6, portfolioLocalReturn=0.10,
            benchmarkWeight=0.5, benchmarkLocalReturn=0.08, currencyReturn=0.02,
        ),
        SectorAttributionInput(
            "FINANCIALS", portfolioWeight=0.4, portfolioLocalReturn=0.03,
            benchmarkWeight=0.5, benchmarkLocalReturn=0.05, currencyReturn=-0.01,
        ),
    ]
    result = computeBrinsonAttribution(sectors)
    # currency effect_i = portfolioWeight_i * currencyReturn_i
    expectedTotalCurrency = 0.6 * 0.02 + 0.4 * (-0.01)
    assert result.totalCurrencyEffect == pytest.approx(expectedTotalCurrency)
    assert result.totalPortfolioReturnIncludingCurrency == pytest.approx(
        result.totalPortfolioLocalReturn + expectedTotalCurrency
    )


def test_sectorAttributionResult_totalSectorEffectSumsAllFourComponents():
    sectors = [
        SectorAttributionInput(
            "TECH", portfolioWeight=0.6, portfolioLocalReturn=0.10,
            benchmarkWeight=0.5, benchmarkLocalReturn=0.08, currencyReturn=0.02,
        )
    ]
    result = computeBrinsonAttribution(sectors)
    sectorResult = result.sectorResults[0]
    expected = (
        sectorResult.allocationEffect
        + sectorResult.selectionEffect
        + sectorResult.interactionEffect
        + sectorResult.currencyEffect
    )
    assert sectorResult.totalSectorEffect == pytest.approx(expected)
