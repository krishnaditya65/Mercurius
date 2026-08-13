"""Volatility surface construction (per-expiry smile/skew) for options
desks. See ARCHITECTURE.md §6 ("Quant Math Engine") and FEATURES.md §6 —
"Volatility surface construction (per-expiry smile/skew) for options
desks".

This module does NOT reimplement implied-volatility solving — it reuses
`solveImpliedVolatilityFromMarketPrice` from `blackScholesOptionPricer.py`
for every input quote, and assembles the resulting per-(expiry, strike)
implied volatilities into a queryable surface structure with linear
interpolation across strikes at a fixed expiry.

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

from dataclasses import dataclass

from quantengine.blackScholesOptionPricer import (
    BlackScholesInputParameters,
    solveImpliedVolatilityFromMarketPrice,
)


@dataclass(frozen=True)
class OptionMarketQuote:
    """One observed market quote to be inverted into an implied
    volatility. `expiryInYears` and `optionStrikePrice` together form the
    surface's grid key.
    """

    optionStrikePrice: float
    expiryInYears: float
    observedMarketPrice: float
    underlyingSpotPrice: float
    annualizedRiskFreeInterestRate: float
    isCallOptionNotPut: bool


@dataclass(frozen=True)
class VolatilitySurfacePoint:
    expiryInYears: float
    optionStrikePrice: float
    impliedVolatility: float


class VolatilitySurface:
    """A real (expiry, strike) -> impliedVolatility grid, with a query
    function that linearly interpolates across strikes at a FIXED expiry
    for a strike not exactly on the input grid. Interpolation is only
    across strikes, not across expiries — querying an expiry with no
    quotes on the grid raises rather than silently interpolating in a
    second dimension the surface wasn't built to support.
    """

    def __init__(self, points: list[VolatilitySurfacePoint]) -> None:
        if not points:
            raise ValueError("points must contain at least one VolatilitySurfacePoint")

        self._impliedVolatilityByExpiryAndStrike: dict[tuple[float, float], float] = {}
        self._strikesByExpiry: dict[float, list[float]] = {}

        for point in points:
            key = (point.expiryInYears, point.optionStrikePrice)
            self._impliedVolatilityByExpiryAndStrike[key] = point.impliedVolatility
            self._strikesByExpiry.setdefault(point.expiryInYears, []).append(point.optionStrikePrice)

        for expiry in self._strikesByExpiry:
            self._strikesByExpiry[expiry].sort()

    def getGridPointCount(self) -> int:
        return len(self._impliedVolatilityByExpiryAndStrike)

    def queryImpliedVolatility(self, expiryInYears: float, optionStrikePrice: float) -> float:
        """Returns the implied volatility for `(expiryInYears,
        optionStrikePrice)`:

        - Exact grid match -> returned directly.
        - `optionStrikePrice` strictly between two grid strikes at that
          expiry -> simple LINEAR interpolation between the two
          neighboring strikes' implied volatilities.
        - `optionStrikePrice` outside the grid's strike range at that
          expiry -> clamped to the nearest boundary strike's implied
          volatility (flat extrapolation) — a documented simplification;
          this module does not extrapolate the smile's slope beyond the
          observed data.

        Raises `KeyError` if `expiryInYears` has no quotes on the grid at
        all (interpolating/extrapolating across EXPIRIES is out of scope
        for this module, per its docstring).
        """
        if expiryInYears not in self._strikesByExpiry:
            raise KeyError(f"no quotes on the surface for expiryInYears={expiryInYears}")

        exactKey = (expiryInYears, optionStrikePrice)
        if exactKey in self._impliedVolatilityByExpiryAndStrike:
            return self._impliedVolatilityByExpiryAndStrike[exactKey]

        sortedStrikes = self._strikesByExpiry[expiryInYears]

        if optionStrikePrice <= sortedStrikes[0]:
            return self._impliedVolatilityByExpiryAndStrike[(expiryInYears, sortedStrikes[0])]
        if optionStrikePrice >= sortedStrikes[-1]:
            return self._impliedVolatilityByExpiryAndStrike[(expiryInYears, sortedStrikes[-1])]

        # Find the bracketing pair of grid strikes and interpolate linearly.
        for lowerStrike, upperStrike in zip(sortedStrikes, sortedStrikes[1:]):
            if lowerStrike <= optionStrikePrice <= upperStrike:
                lowerImpliedVolatility = self._impliedVolatilityByExpiryAndStrike[(expiryInYears, lowerStrike)]
                upperImpliedVolatility = self._impliedVolatilityByExpiryAndStrike[(expiryInYears, upperStrike)]
                interpolationFraction = (optionStrikePrice - lowerStrike) / (upperStrike - lowerStrike)
                return lowerImpliedVolatility + interpolationFraction * (
                    upperImpliedVolatility - lowerImpliedVolatility
                )

        raise AssertionError("unreachable: optionStrikePrice was within [min, max] but no bracket found")


def buildVolatilitySurfaceFromMarketQuotes(quotes: list[OptionMarketQuote]) -> VolatilitySurface:
    """Solves implied volatility for EVERY quote in `quotes` via
    `solveImpliedVolatilityFromMarketPrice` (reused, not reimplemented),
    then assembles the results into a `VolatilitySurface`. Raises
    `ValueError` if `quotes` is empty. A single quote's IV solve failing
    to converge propagates as that same `ValueError` from
    `solveImpliedVolatilityFromMarketPrice` — one bad quote fails the
    whole batch rather than silently dropping it, since a caller
    assembling a surface almost certainly wants to know about a bad
    input quote rather than get a surface silently missing a point.
    """
    if not quotes:
        raise ValueError("quotes must contain at least one OptionMarketQuote")

    points: list[VolatilitySurfacePoint] = []
    for quote in quotes:
        inputParametersWithoutVolatility = BlackScholesInputParameters(
            underlyingSpotPrice=quote.underlyingSpotPrice,
            optionStrikePrice=quote.optionStrikePrice,
            annualizedRiskFreeInterestRate=quote.annualizedRiskFreeInterestRate,
            annualizedVolatility=1.0,  # placeholder — the solver builds its own candidates
            timeToExpiryInYears=quote.expiryInYears,
        )
        impliedVolatility = solveImpliedVolatilityFromMarketPrice(
            quote.observedMarketPrice, inputParametersWithoutVolatility, quote.isCallOptionNotPut
        )
        points.append(
            VolatilitySurfacePoint(
                expiryInYears=quote.expiryInYears,
                optionStrikePrice=quote.optionStrikePrice,
                impliedVolatility=impliedVolatility,
            )
        )

    return VolatilitySurface(points)
