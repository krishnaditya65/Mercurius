from __future__ import annotations

import pytest

from quantengine.blackScholesOptionPricer import (
    BlackScholesInputParameters,
    calculateOptionGreeks,
)
from quantengine.portfolioGreeksAggregator import (
    PortfolioPosition,
    aggregatePortfolioGreeks,
    buildPortfolioPositionFromBlackScholesInputs,
)
from quantengine.blackScholesOptionPricer import OptionGreeksResult


def testAggregateEmptyPortfolioReturnsAllZeroGreeks():
    result = aggregatePortfolioGreeks([])
    assert result.netDelta == 0.0
    assert result.netGamma == 0.0
    assert result.netVegaPerOnePercentVolatilityChange == 0.0
    assert result.netThetaPerCalendarDay == 0.0
    assert result.positionCount == 0


def testAggregateSinglePositionEqualsQuantityTimesPerContractGreeks():
    greeks = OptionGreeksResult(delta=0.5, gamma=0.02, vegaPerOnePercentVolatilityChange=0.15, thetaPerCalendarDay=-0.05)
    position = PortfolioPosition(identifier="AAPL-C-100", quantity=10.0, perContractGreeks=greeks)
    result = aggregatePortfolioGreeks([position])
    assert result.netDelta == pytest.approx(5.0)
    assert result.netGamma == pytest.approx(0.2)
    assert result.netVegaPerOnePercentVolatilityChange == pytest.approx(1.5)
    assert result.netThetaPerCalendarDay == pytest.approx(-0.5)
    assert result.positionCount == 1


def testHandWorkedTwoPositionAggregation():
    # Position A: long 10 contracts, delta=0.5, gamma=0.02, vega=0.15, theta=-0.05
    # Position B: short 5 contracts, delta=0.3, gamma=0.01, vega=0.10, theta=-0.02
    #
    # netDelta = 10*0.5 + (-5)*0.3 = 5.0 - 1.5 = 3.5
    # netGamma = 10*0.02 + (-5)*0.01 = 0.2 - 0.05 = 0.15
    # netVega  = 10*0.15 + (-5)*0.10 = 1.5 - 0.5 = 1.0
    # netTheta = 10*(-0.05) + (-5)*(-0.02) = -0.5 + 0.1 = -0.4
    positionA = PortfolioPosition(
        identifier="A",
        quantity=10.0,
        perContractGreeks=OptionGreeksResult(delta=0.5, gamma=0.02, vegaPerOnePercentVolatilityChange=0.15, thetaPerCalendarDay=-0.05),
    )
    positionB = PortfolioPosition(
        identifier="B",
        quantity=-5.0,
        perContractGreeks=OptionGreeksResult(delta=0.3, gamma=0.01, vegaPerOnePercentVolatilityChange=0.10, thetaPerCalendarDay=-0.02),
    )
    result = aggregatePortfolioGreeks([positionA, positionB])
    assert result.netDelta == pytest.approx(3.5)
    assert result.netGamma == pytest.approx(0.15)
    assert result.netVegaPerOnePercentVolatilityChange == pytest.approx(1.0)
    assert result.netThetaPerCalendarDay == pytest.approx(-0.4)
    assert result.positionCount == 2


def testLongAndShortIdenticalPositionsNetToExactlyZero():
    greeks = OptionGreeksResult(delta=0.6, gamma=0.03, vegaPerOnePercentVolatilityChange=0.2, thetaPerCalendarDay=-0.07)
    longPosition = PortfolioPosition(identifier="L", quantity=8.0, perContractGreeks=greeks)
    shortPosition = PortfolioPosition(identifier="S", quantity=-8.0, perContractGreeks=greeks)
    result = aggregatePortfolioGreeks([longPosition, shortPosition])
    assert result.netDelta == pytest.approx(0.0)
    assert result.netGamma == pytest.approx(0.0)
    assert result.netVegaPerOnePercentVolatilityChange == pytest.approx(0.0)
    assert result.netThetaPerCalendarDay == pytest.approx(0.0)


def testBuildPortfolioPositionFromBlackScholesInputsReusesRealPricer():
    inputParameters = BlackScholesInputParameters(
        underlyingSpotPrice=100.0,
        optionStrikePrice=100.0,
        annualizedRiskFreeInterestRate=0.05,
        annualizedVolatility=0.2,
        timeToExpiryInYears=1.0,
    )
    expectedGreeks = calculateOptionGreeks(inputParameters, isCallOptionNotPut=True)
    position = buildPortfolioPositionFromBlackScholesInputs(
        "ATM-CALL", quantity=3.0, inputParameters=inputParameters, isCallOptionNotPut=True
    )
    assert position.perContractGreeks == expectedGreeks
    assert position.quantity == 3.0


def testAggregationOverMultipleRealBlackScholesPositionsIsConsistentWithManualSum():
    callParams = BlackScholesInputParameters(100.0, 100.0, 0.05, 0.2, 1.0)
    putParams = BlackScholesInputParameters(100.0, 95.0, 0.05, 0.25, 0.5)

    callPosition = buildPortfolioPositionFromBlackScholesInputs("C1", 5.0, callParams, True)
    putPosition = buildPortfolioPositionFromBlackScholesInputs("P1", -2.0, putParams, False)

    result = aggregatePortfolioGreeks([callPosition, putPosition])

    callGreeks = calculateOptionGreeks(callParams, True)
    putGreeks = calculateOptionGreeks(putParams, False)
    expectedDelta = 5.0 * callGreeks.delta + (-2.0) * putGreeks.delta
    expectedGamma = 5.0 * callGreeks.gamma + (-2.0) * putGreeks.gamma
    expectedVega = 5.0 * callGreeks.vegaPerOnePercentVolatilityChange + (-2.0) * putGreeks.vegaPerOnePercentVolatilityChange
    expectedTheta = 5.0 * callGreeks.thetaPerCalendarDay + (-2.0) * putGreeks.thetaPerCalendarDay

    assert result.netDelta == pytest.approx(expectedDelta)
    assert result.netGamma == pytest.approx(expectedGamma)
    assert result.netVegaPerOnePercentVolatilityChange == pytest.approx(expectedVega)
    assert result.netThetaPerCalendarDay == pytest.approx(expectedTheta)


def testPositionCountReflectsNumberOfPositionsNotQuantity():
    greeks = OptionGreeksResult(delta=0.1, gamma=0.01, vegaPerOnePercentVolatilityChange=0.05, thetaPerCalendarDay=-0.01)
    positions = [PortfolioPosition(identifier=f"P{i}", quantity=100.0, perContractGreeks=greeks) for i in range(4)]
    result = aggregatePortfolioGreeks(positions)
    assert result.positionCount == 4


def testZeroQuantityPositionContributesNothing():
    greeks = OptionGreeksResult(delta=0.9, gamma=0.09, vegaPerOnePercentVolatilityChange=0.5, thetaPerCalendarDay=-0.2)
    zeroPosition = PortfolioPosition(identifier="Z", quantity=0.0, perContractGreeks=greeks)
    result = aggregatePortfolioGreeks([zeroPosition])
    assert result.netDelta == 0.0
    assert result.netGamma == 0.0
    assert result.netVegaPerOnePercentVolatilityChange == 0.0
    assert result.netThetaPerCalendarDay == 0.0


def testFractionalQuantitiesAreSupported():
    greeks = OptionGreeksResult(delta=0.4, gamma=0.02, vegaPerOnePercentVolatilityChange=0.1, thetaPerCalendarDay=-0.03)
    position = PortfolioPosition(identifier="F", quantity=2.5, perContractGreeks=greeks)
    result = aggregatePortfolioGreeks([position])
    assert result.netDelta == pytest.approx(1.0)
    assert result.netGamma == pytest.approx(0.05)


def testManyPositionsSameSignAllAddConstructively():
    greeks = OptionGreeksResult(delta=0.5, gamma=0.01, vegaPerOnePercentVolatilityChange=0.05, thetaPerCalendarDay=-0.01)
    positions = [PortfolioPosition(identifier=f"P{i}", quantity=1.0, perContractGreeks=greeks) for i in range(20)]
    result = aggregatePortfolioGreeks(positions)
    assert result.netDelta == pytest.approx(10.0)
    assert result.positionCount == 20
