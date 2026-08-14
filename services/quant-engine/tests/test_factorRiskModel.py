import pytest

from quantengine.factorRiskModel import (
    FactorAttributionResult,
    PortfolioFactorExposureResult,
    PortfolioHoldingWithFactorExposures,
    computeFactorAttribution,
    computePortfolioFactorExposures,
)


def buildTwoHoldingPortfolio() -> list[PortfolioHoldingWithFactorExposures]:
    return [
        PortfolioHoldingWithFactorExposures(
            symbol="DEMO-EQ",
            portfolioWeight=0.6,
            factorExposuresByName={"marketBeta": 1.2, "size": 0.5},
        ),
        PortfolioHoldingWithFactorExposures(
            symbol="SIM-AAPL",
            portfolioWeight=0.4,
            factorExposuresByName={"marketBeta": 0.8, "size": -0.3},
        ),
    ]


class TestPortfolioHoldingValidation:
    def testEmptyFactorExposuresRaises(self):
        with pytest.raises(ValueError):
            PortfolioHoldingWithFactorExposures(symbol="X", portfolioWeight=1.0, factorExposuresByName={})


class TestComputePortfolioFactorExposuresHandWorked:
    def testTwoHoldingWeightedSum(self):
        result = computePortfolioFactorExposures(buildTwoHoldingPortfolio())
        assert isinstance(result, PortfolioFactorExposureResult)
        # hand-worked: marketBeta = 0.6*1.2 + 0.4*0.8 = 0.72 + 0.32 = 1.04
        assert result.portfolioExposureByFactor["marketBeta"] == pytest.approx(1.04)
        # hand-worked: size = 0.6*0.5 + 0.4*(-0.3) = 0.30 - 0.12 = 0.18
        assert result.portfolioExposureByFactor["size"] == pytest.approx(0.18)
        assert result.totalPortfolioWeight == pytest.approx(1.0)
        assert result.holdingCount == 2

    def testSingleHoldingExposureEqualsItsOwnExposureScaledByWeight(self):
        holdings = [
            PortfolioHoldingWithFactorExposures(
                symbol="X", portfolioWeight=0.5, factorExposuresByName={"value": 2.0}
            )
        ]
        result = computePortfolioFactorExposures(holdings)
        assert result.portfolioExposureByFactor["value"] == pytest.approx(1.0)

    def testEmptyHoldingsRaises(self):
        with pytest.raises(ValueError):
            computePortfolioFactorExposures([])

    def testMismatchedFactorSetsRaises(self):
        holdings = [
            PortfolioHoldingWithFactorExposures(symbol="A", portfolioWeight=0.5, factorExposuresByName={"marketBeta": 1.0}),
            PortfolioHoldingWithFactorExposures(symbol="B", portfolioWeight=0.5, factorExposuresByName={"size": 1.0}),
        ]
        with pytest.raises(ValueError):
            computePortfolioFactorExposures(holdings)

    def testWeightsNotEnforcedToSumToOneButReportedAsDiagnostic(self):
        holdings = [
            PortfolioHoldingWithFactorExposures(symbol="A", portfolioWeight=0.3, factorExposuresByName={"marketBeta": 1.0}),
            PortfolioHoldingWithFactorExposures(symbol="B", portfolioWeight=0.3, factorExposuresByName={"marketBeta": 1.0}),
        ]
        result = computePortfolioFactorExposures(holdings)
        assert result.totalPortfolioWeight == pytest.approx(0.6)

    def testNegativeWeightRepresentsShortPosition(self):
        holdings = [
            PortfolioHoldingWithFactorExposures(symbol="LONG", portfolioWeight=1.2, factorExposuresByName={"marketBeta": 1.0}),
            PortfolioHoldingWithFactorExposures(symbol="SHORT", portfolioWeight=-0.2, factorExposuresByName={"marketBeta": 1.0}),
        ]
        result = computePortfolioFactorExposures(holdings)
        # hand-worked: 1.2*1.0 + (-0.2)*1.0 = 1.0
        assert result.portfolioExposureByFactor["marketBeta"] == pytest.approx(1.0)


class TestComputeFactorAttributionHandWorked:
    def testTwoFactorAttributionWithIdiosyncraticResidual(self):
        exposures = {"marketBeta": 1.04, "size": 0.18}
        factorReturns = {"marketBeta": 0.02, "size": 0.01}
        result = computeFactorAttribution(exposures, factorReturns, actualOrExpectedPortfolioReturn=0.03)

        # hand-worked: marketBeta contribution = 1.04 * 0.02 = 0.0208
        assert result.contributionByFactor["marketBeta"] == pytest.approx(0.0208)
        # hand-worked: size contribution = 0.18 * 0.01 = 0.0018
        assert result.contributionByFactor["size"] == pytest.approx(0.0018)
        # hand-worked: total = 0.0208 + 0.0018 = 0.0226
        assert result.totalFactorContribution == pytest.approx(0.0226)
        # hand-worked: idiosyncratic = 0.03 - 0.0226 = 0.0074
        assert result.idiosyncraticReturn == pytest.approx(0.0074)

    def testFullyExplainedReturnLeavesZeroIdiosyncraticResidual(self):
        result = computeFactorAttribution({"marketBeta": 1.0}, {"marketBeta": 0.05}, actualOrExpectedPortfolioReturn=0.05)
        assert result.totalFactorContribution == pytest.approx(0.05)
        assert result.idiosyncraticReturn == pytest.approx(0.0)

    def testMissingFactorReturnRaises(self):
        with pytest.raises(ValueError):
            computeFactorAttribution({"marketBeta": 1.0, "size": 0.2}, {"marketBeta": 0.05}, actualOrExpectedPortfolioReturn=0.05)

    def testContributionFractionByFactor(self):
        result = computeFactorAttribution({"marketBeta": 1.0}, {"marketBeta": 0.04}, actualOrExpectedPortfolioReturn=0.08)
        fractions = result.contributionFractionByFactor()
        # hand-worked: contribution 0.04 / actual return 0.08 = 0.5
        assert fractions["marketBeta"] == pytest.approx(0.5)

    def testContributionFractionByFactorEmptyWhenActualReturnIsZero(self):
        result = computeFactorAttribution({"marketBeta": 1.0}, {"marketBeta": 0.0}, actualOrExpectedPortfolioReturn=0.0)
        assert result.contributionFractionByFactor() == {}

    def testNegativeFactorReturnProducesNegativeContribution(self):
        result = computeFactorAttribution({"marketBeta": 2.0}, {"marketBeta": -0.03}, actualOrExpectedPortfolioReturn=-0.10)
        # hand-worked: 2.0 * -0.03 = -0.06
        assert result.contributionByFactor["marketBeta"] == pytest.approx(-0.06)
        # hand-worked: idiosyncratic = -0.10 - (-0.06) = -0.04
        assert result.idiosyncraticReturn == pytest.approx(-0.04)

    def testReturnsFactorAttributionDataclassShape(self):
        result = computeFactorAttribution({"marketBeta": 1.0}, {"marketBeta": 0.01}, actualOrExpectedPortfolioReturn=0.01)
        assert isinstance(result, FactorAttributionResult)


class TestEndToEndExposureThenAttribution:
    def testComputeExposuresFeedsAttributionEndToEnd(self):
        holdings = buildTwoHoldingPortfolio()
        exposureResult = computePortfolioFactorExposures(holdings)
        attributionResult = computeFactorAttribution(
            exposureResult.portfolioExposureByFactor,
            {"marketBeta": 0.02, "size": 0.01},
            actualOrExpectedPortfolioReturn=0.03,
        )
        assert attributionResult.totalFactorContribution == pytest.approx(0.0226)
