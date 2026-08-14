"""Monte Carlo engine for path-dependent option pricing (Asian options,
arithmetic-average payoff) and portfolio-level Monte Carlo VaR. See
FEATURES.md §22 ("Deep Quant & Algorithmic Trading Internals").

Real geometric Brownian motion (GBM) path simulation, SEEDED for
deterministic tests (via `random.Random(randomSeed)` — stdlib `random`,
no numpy, matching this repo's stdlib-only convention elsewhere — see
`garchVolatilityForecaster.py`'s own note on the same constraint):

    S_{t+dt} = S_t * exp((r - 0.5*sigma^2)*dt + sigma*sqrt(dt)*Z),  Z ~ N(0,1)

the standard exact-in-distribution discretization of GBM (not an Euler
approximation — this is the closed-form solution of the GBM SDE applied
step-by-step, so it's exact for any `dt`, not just in the limit).

Two real Monte Carlo pricers:

1. `priceAsianArithmeticAverageOptionViaMonteCarlo` — an ASIAN option
   (payoff based on the ARITHMETIC AVERAGE of the simulated path, not
   just the terminal price) — genuinely path-dependent, so it has NO
   closed-form Black-Scholes analogue; Monte Carlo is the right tool for
   it, which is exactly why this module exists (see FEATURES.md §22's
   framing: "pick Asian ... or barrier, whichever you can get genuinely
   correct and well-tested").
2. `priceEuropeanOptionViaMonteCarlo` — an ordinary European option
   (payoff based on terminal price only), included SPECIFICALLY as a
   correctness/convergence check: as `numberOfPaths` grows, this MUST
   converge toward `blackScholesOptionPricer`'s real closed-form price
   for the same inputs (see `tests/test_monteCarloEngine.py`'s
   convergence test, which asserts exactly this) — a genuine,
   meaningful validation of the whole simulation machinery, not merely
   "it runs".

`simulateMonteCarloPortfolioValueAtRisk` simulates portfolio VALUE paths
under a single aggregate GBM assumption (`portfolioExpectedReturn`,
`portfolioVolatility` as one blended pair — this is a simplification;
a real multi-asset Monte Carlo VaR would simulate each position's own
correlated process, which is out of scope for this pass) and reports the
REAL empirical percentile loss over the simulated terminal-value
distribution, using the SAME nearest-rank convention and POSITIVE-number-
means-loss-magnitude sign convention as `valueAtRiskCalculator.calculateHistoricalValueAtRisk`
(reused convention, not reused code — this module simulates its own
distribution rather than working from an observed historical series).

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

import math
import random
from dataclasses import dataclass


def simulateGeometricBrownianMotionPaths(
    startingPrice: float,
    annualizedDriftRate: float,
    annualizedVolatility: float,
    timeHorizonInYears: float,
    numberOfTimeSteps: int,
    numberOfPaths: int,
    randomSeed: int,
) -> list[list[float]]:
    """Simulates `numberOfPaths` independent GBM price paths, each of
    length `numberOfTimeSteps + 1` (index 0 is `startingPrice`), using
    the EXACT step-wise GBM discretization (see module docstring).
    `randomSeed` is fed to a dedicated `random.Random(randomSeed)`
    instance (not the shared global `random` module state) so this
    function is fully deterministic AND does not disturb any other
    caller's use of the `random` module.

    Raises `ValueError` if `numberOfTimeSteps` or `numberOfPaths` is not
    a strictly positive integer, or if `timeHorizonInYears` or
    `annualizedVolatility` is not strictly positive.
    """
    if numberOfTimeSteps <= 0:
        raise ValueError("numberOfTimeSteps must be a strictly positive integer")
    if numberOfPaths <= 0:
        raise ValueError("numberOfPaths must be a strictly positive integer")
    if timeHorizonInYears <= 0.0:
        raise ValueError("timeHorizonInYears must be strictly positive")
    if annualizedVolatility <= 0.0:
        raise ValueError("annualizedVolatility must be strictly positive")

    randomNumberGenerator = random.Random(randomSeed)
    timeStepSize = timeHorizonInYears / numberOfTimeSteps
    driftPerStep = (annualizedDriftRate - 0.5 * annualizedVolatility * annualizedVolatility) * timeStepSize
    volatilityPerStep = annualizedVolatility * math.sqrt(timeStepSize)

    paths: list[list[float]] = []
    for _ in range(numberOfPaths):
        path = [startingPrice]
        currentPrice = startingPrice
        for _ in range(numberOfTimeSteps):
            standardNormalDraw = randomNumberGenerator.gauss(0.0, 1.0)
            currentPrice = currentPrice * math.exp(driftPerStep + volatilityPerStep * standardNormalDraw)
            path.append(currentPrice)
        paths.append(path)

    return paths


@dataclass(frozen=True)
class MonteCarloOptionPriceResult:
    estimatedPrice: float
    standardErrorOfEstimate: float
    numberOfPaths: int


def priceEuropeanOptionViaMonteCarlo(
    startingPrice: float,
    strikePrice: float,
    annualizedRiskFreeInterestRate: float,
    annualizedVolatility: float,
    timeToExpiryInYears: float,
    isCallOptionNotPut: bool,
    numberOfPaths: int,
    randomSeed: int,
    numberOfTimeSteps: int = 1,
) -> MonteCarloOptionPriceResult:
    """European option price via Monte Carlo: simulates terminal prices
    under risk-neutral GBM (drift = risk-free rate, the standard
    risk-neutral pricing assumption), discounts each path's payoff at
    the risk-free rate, and averages. `numberOfTimeSteps` defaults to 1
    (a European payoff only needs the TERMINAL price — no path
    dependency — so a single big step is both correct and faster than
    stepping through intermediate dates unnecessarily).

    Included as a CONVERGENCE/CORRECTNESS CHECK against
    `blackScholesOptionPricer`'s closed-form price — see
    `tests/test_monteCarloEngine.py`. Also returns
    `standardErrorOfEstimate` (the Monte Carlo standard error of the
    mean discounted payoff, `stddev(payoffs) / sqrt(numberOfPaths)`) so
    callers can judge estimate precision, not just a single point value.
    """
    paths = simulateGeometricBrownianMotionPaths(
        startingPrice,
        annualizedRiskFreeInterestRate,
        annualizedVolatility,
        timeToExpiryInYears,
        numberOfTimeSteps,
        numberOfPaths,
        randomSeed,
    )

    discountFactor = math.exp(-annualizedRiskFreeInterestRate * timeToExpiryInYears)
    payoffs = [
        max(path[-1] - strikePrice, 0.0) if isCallOptionNotPut else max(strikePrice - path[-1], 0.0)
        for path in paths
    ]
    discountedPayoffs = [discountFactor * payoff for payoff in payoffs]

    return _summarizeMonteCarloEstimate(discountedPayoffs)


def priceAsianArithmeticAverageOptionViaMonteCarlo(
    startingPrice: float,
    strikePrice: float,
    annualizedRiskFreeInterestRate: float,
    annualizedVolatility: float,
    timeToExpiryInYears: float,
    isCallOptionNotPut: bool,
    numberOfTimeSteps: int,
    numberOfPaths: int,
    randomSeed: int,
) -> MonteCarloOptionPriceResult:
    """Asian (arithmetic-average) option price via Monte Carlo: the
    payoff is based on the ARITHMETIC MEAN of the simulated path's
    prices at each of `numberOfTimeSteps` observation points (index 0,
    the starting price, is EXCLUDED from the average — the standard
    convention that the average is over OBSERVED prices during the
    option's life, not including the pre-trade starting point), not the
    terminal price alone. This is genuinely path-dependent — there is
    NO closed-form Black-Scholes analogue for an arithmetic-average
    Asian option (the arithmetic average of correlated lognormals is
    not itself lognormal), which is exactly why Monte Carlo is the
    right (and here, the only implemented) pricing tool for it.
    """
    paths = simulateGeometricBrownianMotionPaths(
        startingPrice,
        annualizedRiskFreeInterestRate,
        annualizedVolatility,
        timeToExpiryInYears,
        numberOfTimeSteps,
        numberOfPaths,
        randomSeed,
    )

    discountFactor = math.exp(-annualizedRiskFreeInterestRate * timeToExpiryInYears)
    discountedPayoffs = []
    for path in paths:
        observedPrices = path[1:]  # exclude the starting price, per the module docstring's convention
        arithmeticAveragePrice = sum(observedPrices) / len(observedPrices)
        payoff = (
            max(arithmeticAveragePrice - strikePrice, 0.0)
            if isCallOptionNotPut
            else max(strikePrice - arithmeticAveragePrice, 0.0)
        )
        discountedPayoffs.append(discountFactor * payoff)

    return _summarizeMonteCarloEstimate(discountedPayoffs)


def _summarizeMonteCarloEstimate(discountedPayoffs: list[float]) -> MonteCarloOptionPriceResult:
    numberOfPaths = len(discountedPayoffs)
    meanDiscountedPayoff = sum(discountedPayoffs) / numberOfPaths
    if numberOfPaths > 1:
        sampleVariance = sum((p - meanDiscountedPayoff) ** 2 for p in discountedPayoffs) / (numberOfPaths - 1)
        standardErrorOfEstimate = math.sqrt(sampleVariance / numberOfPaths)
    else:
        standardErrorOfEstimate = 0.0

    return MonteCarloOptionPriceResult(
        estimatedPrice=meanDiscountedPayoff,
        standardErrorOfEstimate=standardErrorOfEstimate,
        numberOfPaths=numberOfPaths,
    )


@dataclass(frozen=True)
class MonteCarloValueAtRiskResult:
    valueAtRisk: float
    confidenceLevel: float
    numberOfPaths: int
    simulatedTerminalPortfolioValues: list[float]


def simulateMonteCarloPortfolioValueAtRisk(
    currentPortfolioValue: float,
    portfolioExpectedAnnualReturn: float,
    portfolioAnnualizedVolatility: float,
    timeHorizonInYears: float,
    confidenceLevel: float,
    numberOfPaths: int,
    randomSeed: int,
) -> MonteCarloValueAtRiskResult:
    """Simulates `numberOfPaths` terminal portfolio VALUES over
    `timeHorizonInYears` under a single aggregate GBM assumption (see
    module docstring's note on this being a simplification vs. a real
    multi-asset correlated simulation), then computes the REAL
    empirical percentile loss — same nearest-rank convention as
    `valueAtRiskCalculator.calculateHistoricalValueAtRisk`, expressed as
    a POSITIVE fraction-of-current-value loss magnitude (so "VaR of 0.05
    at 95% confidence" reads identically to the historical/parametric
    VaR functions elsewhere in this codebase).

    Raises `ValueError` if `confidenceLevel` is outside (0, 1), or (via
    `simulateGeometricBrownianMotionPaths`) on invalid path-simulation
    inputs.
    """
    if not (0.0 < confidenceLevel < 1.0):
        raise ValueError("confidenceLevel must be strictly between 0 and 1")

    paths = simulateGeometricBrownianMotionPaths(
        currentPortfolioValue,
        portfolioExpectedAnnualReturn,
        portfolioAnnualizedVolatility,
        timeHorizonInYears,
        numberOfTimeSteps=1,
        numberOfPaths=numberOfPaths,
        randomSeed=randomSeed,
    )
    terminalValues = [path[-1] for path in paths]
    terminalReturns = sorted((value - currentPortfolioValue) / currentPortfolioValue for value in terminalValues)

    percentileIndex = int((1.0 - confidenceLevel) * len(terminalReturns) + 1e-9)
    percentileIndex = min(percentileIndex, len(terminalReturns) - 1)
    valueAtRisk = -terminalReturns[percentileIndex]

    return MonteCarloValueAtRiskResult(
        valueAtRisk=valueAtRisk,
        confidenceLevel=confidenceLevel,
        numberOfPaths=numberOfPaths,
        simulatedTerminalPortfolioValues=terminalValues,
    )
