import math

from quantengine.blackScholesOptionPricer import (
    BlackScholesInputParameters,
    calculateBlackScholesCallOptionPrice,
    calculateBlackScholesPutOptionPrice,
    calculateOptionGreeks,
    solveImpliedVolatilityFromMarketPrice,
)

# Known reference values for a standard textbook example:
# S=100, K=100, r=5%, sigma=20%, T=1y -> call ~ 10.4506, put ~ 5.5735
STANDARD_TEXTBOOK_EXAMPLE_PARAMETERS = BlackScholesInputParameters(
    underlyingSpotPrice=100.0,
    optionStrikePrice=100.0,
    annualizedRiskFreeInterestRate=0.05,
    annualizedVolatility=0.20,
    timeToExpiryInYears=1.0,
)


def test_callOptionPriceMatchesKnownReferenceValue():
    callPrice = calculateBlackScholesCallOptionPrice(STANDARD_TEXTBOOK_EXAMPLE_PARAMETERS)
    assert math.isclose(callPrice, 10.4506, rel_tol=1e-3)


def test_putOptionPriceMatchesKnownReferenceValue():
    putPrice = calculateBlackScholesPutOptionPrice(STANDARD_TEXTBOOK_EXAMPLE_PARAMETERS)
    assert math.isclose(putPrice, 5.5735, rel_tol=1e-3)


def test_putCallParityHolds():
    # C - P = S - K*e^(-rT) — must hold exactly (within float tolerance)
    # regardless of the specific reference values above; this is the
    # actual arbitrage-free invariant the FEATURES.md §6 arbitrage scanner
    # implicitly relies on the pricer respecting.
    callPrice = calculateBlackScholesCallOptionPrice(STANDARD_TEXTBOOK_EXAMPLE_PARAMETERS)
    putPrice = calculateBlackScholesPutOptionPrice(STANDARD_TEXTBOOK_EXAMPLE_PARAMETERS)

    spotPrice = STANDARD_TEXTBOOK_EXAMPLE_PARAMETERS.underlyingSpotPrice
    strikePrice = STANDARD_TEXTBOOK_EXAMPLE_PARAMETERS.optionStrikePrice
    riskFreeRate = STANDARD_TEXTBOOK_EXAMPLE_PARAMETERS.annualizedRiskFreeInterestRate
    timeToExpiry = STANDARD_TEXTBOOK_EXAMPLE_PARAMETERS.timeToExpiryInYears

    expectedDifference = spotPrice - strikePrice * math.exp(-riskFreeRate * timeToExpiry)
    assert math.isclose(callPrice - putPrice, expectedDifference, abs_tol=1e-6)


def test_callDeltaIsBetweenZeroAndOneForAtTheMoneyOption():
    greeks = calculateOptionGreeks(STANDARD_TEXTBOOK_EXAMPLE_PARAMETERS, isCallOptionNotPut=True)
    assert 0.0 < greeks.delta < 1.0
    # ATM call delta should be roughly 0.5-0.65 given positive rates
    assert 0.5 < greeks.delta < 0.7


def test_putDeltaIsBetweenNegativeOneAndZero():
    greeks = calculateOptionGreeks(STANDARD_TEXTBOOK_EXAMPLE_PARAMETERS, isCallOptionNotPut=False)
    assert -1.0 < greeks.delta < 0.0


def test_gammaIsIdenticalForCallAndPutAtSameStrike():
    # Gamma has no call/put distinction in Black-Scholes — this is a good
    # regression check that calculateOptionGreeks didn't accidentally
    # branch gamma on isCallOptionNotPut.
    callGreeks = calculateOptionGreeks(STANDARD_TEXTBOOK_EXAMPLE_PARAMETERS, isCallOptionNotPut=True)
    putGreeks = calculateOptionGreeks(STANDARD_TEXTBOOK_EXAMPLE_PARAMETERS, isCallOptionNotPut=False)
    assert math.isclose(callGreeks.gamma, putGreeks.gamma, rel_tol=1e-9)


def test_impliedVolatilitySolverRecoversTheOriginalVolatility():
    # Price a call at a known volatility, then solve backward from that
    # price and confirm we recover (approximately) the same volatility —
    # this is the exact round-trip the arbitrage scanner and IV Rank
    # differentiator (FEATURES.md §6, §22) depend on.
    knownVolatility = 0.25
    parametersAtKnownVolatility = BlackScholesInputParameters(
        underlyingSpotPrice=100.0,
        optionStrikePrice=105.0,
        annualizedRiskFreeInterestRate=0.03,
        annualizedVolatility=knownVolatility,
        timeToExpiryInYears=0.5,
    )
    marketPriceAtKnownVolatility = calculateBlackScholesCallOptionPrice(parametersAtKnownVolatility)

    recoveredVolatility = solveImpliedVolatilityFromMarketPrice(
        observedMarketPrice=marketPriceAtKnownVolatility,
        inputParametersWithoutVolatility=parametersAtKnownVolatility,
        isCallOptionNotPut=True,
    )

    assert math.isclose(recoveredVolatility, knownVolatility, rel_tol=1e-4)
