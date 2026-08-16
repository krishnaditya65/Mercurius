"""Arbitrage scanner: theoretical-vs-live price deviation alerting. See
ARCHITECTURE.md §6 ("Quant Math Engine") and FEATURES.md §6 — "Arbitrage
scanner: theoretical vs. live price deviation alerts".

This module does NOT do the theoretical pricing itself — it takes a
theoretical fair price as an input (computed by the caller, e.g. via
`blackScholesOptionPricer.calculateBlackScholesCallOptionPrice` for an
option, or via `calculateCashAndCarryForwardFairPrice` below for a simple
futures cash-and-carry) and a live observed market price, and answers one
narrow question: does the deviation between them exceed a configurable
threshold, and if so, what does the alert look like?

Per blackScholesOptionPricer.py's own docstring and ARCHITECTURE.md §8,
this whole module remains RESEARCH-TIER: fine for scanning a modest
number of contracts at human-perceptible latency (e.g. from a periodic
poll or a backtest), NOT for the real-time scanner running across
thousands of contracts per second — that hot path still needs the Rust
port noted in blackScholesOptionPricer.py once this logic is trusted.

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

import math
from dataclasses import dataclass


def calculateCashAndCarryForwardFairPrice(
    spotPrice: float, annualizedRiskFreeInterestRate: float, timeToDeliveryInYears: float
) -> float:
    """Theoretical fair price of a simple cash-and-carry future/forward
    with no dividends/carry costs other than the risk-free rate:

        F = S * e^(r * T)

    This is the simplest possible "theoretical price" input this module's
    scanner can be fed — a genuine formula, not a stub, but intentionally
    the plainest cash-and-carry case (no continuous dividend yield, no
    storage cost, no convenience yield). A dividend-adjusted version would
    subtract a continuous yield term from `annualizedRiskFreeInterestRate`
    in the exponent; that's left as a future extension rather than faked
    here.
    """
    return spotPrice * math.exp(annualizedRiskFreeInterestRate * timeToDeliveryInYears)


@dataclass(frozen=True)
class PriceDeviationAlert:
    theoreticalFairPrice: float
    liveMarketPrice: float
    absoluteDeviation: float
    percentageDeviation: float
    deviationThresholdPercentage: float
    isAlertTriggered: bool
    isLiveOverpricedRelativeToTheoretical: bool


def scanForTheoreticalVersusLivePriceDeviation(
    theoreticalFairPrice: float,
    liveMarketPrice: float,
    deviationThresholdPercentage: float,
) -> PriceDeviationAlert:
    """Compares a theoretical fair price against a live observed market
    price and flags an alert if the deviation exceeds
    `deviationThresholdPercentage` (expressed as a percentage, e.g. 1.0
    means "alert if live price deviates more than 1% from theoretical").

        absoluteDeviation   = liveMarketPrice - theoreticalFairPrice
        percentageDeviation = absoluteDeviation / theoreticalFairPrice * 100

    Deviation sign convention: `absoluteDeviation`/`percentageDeviation`
    are signed (positive means the live price trades ABOVE theoretical —
    "rich"/overpriced; negative means it trades below — "cheap"/
    underpriced), which is what a trading desk needs to know to decide
    direction (sell the rich leg, buy the cheap leg in a classic
    cash-and-carry / options arbitrage). `isAlertTriggered` is based on
    the deviation's magnitude (`abs(percentageDeviation)`) against the
    threshold, regardless of sign.

    Raises `ValueError` if `theoreticalFairPrice` is non-positive (percent
    deviation is undefined/meaningless against a zero or negative
    reference price) or if `deviationThresholdPercentage` is negative.
    """
    if theoreticalFairPrice <= 0:
        raise ValueError(
            "theoreticalFairPrice must be strictly positive — percentage deviation against a "
            "zero or negative reference price is undefined"
        )
    if deviationThresholdPercentage < 0:
        raise ValueError("deviationThresholdPercentage must be non-negative")

    absoluteDeviation = liveMarketPrice - theoreticalFairPrice
    percentageDeviation = (absoluteDeviation / theoreticalFairPrice) * 100.0
    isAlertTriggered = abs(percentageDeviation) > deviationThresholdPercentage

    return PriceDeviationAlert(
        theoreticalFairPrice=theoreticalFairPrice,
        liveMarketPrice=liveMarketPrice,
        absoluteDeviation=absoluteDeviation,
        percentageDeviation=percentageDeviation,
        deviationThresholdPercentage=deviationThresholdPercentage,
        isAlertTriggered=isAlertTriggered,
        isLiveOverpricedRelativeToTheoretical=absoluteDeviation > 0,
    )


def scanManyLivePricesForDeviationAlerts(
    theoreticalFairPricesBySymbol: dict[str, float],
    liveMarketPricesBySymbol: dict[str, float],
    deviationThresholdPercentage: float,
) -> dict[str, PriceDeviationAlert]:
    """Batch convenience wrapper over `scanForTheoreticalVersusLivePriceDeviation`
    for scanning a whole symbol universe in one call — e.g. an options
    chain or a basket of futures contracts. Only symbols present in BOTH
    dictionaries are scanned; a symbol with a theoretical price but no
    live quote (or vice versa) is silently skipped rather than raising,
    since a stale/missing live quote for one symbol shouldn't fail the
    whole scan.
    """
    alertsBySymbol: dict[str, PriceDeviationAlert] = {}
    for symbol, theoreticalFairPrice in theoreticalFairPricesBySymbol.items():
        if symbol not in liveMarketPricesBySymbol:
            continue
        try:
            alertsBySymbol[symbol] = scanForTheoreticalVersusLivePriceDeviation(
                theoreticalFairPrice, liveMarketPricesBySymbol[symbol], deviationThresholdPercentage
            )
        except ValueError:
            # A non-positive theoreticalFairPrice for one symbol (e.g. a
            # bad upstream pricing input) shouldn't fail the whole batch
            # scan — same "skip this symbol, keep going" contract already
            # documented above for a missing live quote.
            continue
    return alertsBySymbol
