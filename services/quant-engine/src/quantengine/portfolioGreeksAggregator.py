"""Portfolio-level Greeks aggregation: net delta/gamma/theta/vega across
ALL positions in a book, not per-contract. See FEATURES.md §22 ("Deep
Quant & Algorithmic Trading Internals") — the first, most tractable item
in that list.

`blackScholesOptionPricer.calculateOptionGreeks` already computes real
per-CONTRACT Greeks (see that module's `calculateOptionGreeks` docstring,
which explicitly defers this aggregation to this module). This module
does the real, simple math of summing those per-contract Greeks across a
whole portfolio, quantity-weighted:

    netDelta = sum(position.quantity * position.perContractGreeks.delta)
    netGamma = sum(position.quantity * position.perContractGreeks.gamma)
    ... etc for vega and theta

`quantity` is SIGNED (positive = long, negative = short) and represents
the number of OPTION CONTRACTS (not underlying shares) held at that
position's Greeks. A caller who already has per-contract Greeks from
repeated `calculateOptionGreeks` calls (or from any other pricer) can
build `PortfolioPosition` objects directly; `buildPortfolioPositionFromBlackScholesInputs`
is a convenience that additionally calls `calculateOptionGreeks` itself
for callers starting from raw contract terms instead.

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

from dataclasses import dataclass

from quantengine.blackScholesOptionPricer import (
    BlackScholesInputParameters,
    OptionGreeksResult,
    calculateOptionGreeks,
)


@dataclass(frozen=True)
class PortfolioPosition:
    """One position in a Greeks-aggregation portfolio: a SIGNED quantity
    of option contracts (positive = long, negative = short) at their own
    per-contract Greeks. `identifier` is a free-form caller label (e.g.
    an OCC option symbol) used only for reporting, not for any
    computation here.
    """

    identifier: str
    quantity: float
    perContractGreeks: OptionGreeksResult


@dataclass(frozen=True)
class PortfolioGreeksAggregationResult:
    netDelta: float
    netGamma: float
    netVegaPerOnePercentVolatilityChange: float
    netThetaPerCalendarDay: float
    positionCount: int


def buildPortfolioPositionFromBlackScholesInputs(
    identifier: str,
    quantity: float,
    inputParameters: BlackScholesInputParameters,
    isCallOptionNotPut: bool,
) -> PortfolioPosition:
    """Convenience constructor for callers who have raw Black-Scholes
    contract terms rather than already-computed Greeks: calls the real
    `calculateOptionGreeks` once and wraps the result as a
    `PortfolioPosition`.
    """
    perContractGreeks = calculateOptionGreeks(inputParameters, isCallOptionNotPut)
    return PortfolioPosition(identifier=identifier, quantity=quantity, perContractGreeks=perContractGreeks)


def aggregatePortfolioGreeks(positions: list[PortfolioPosition]) -> PortfolioGreeksAggregationResult:
    """Sums quantity-weighted per-contract Greeks across every position
    in `positions` into real net portfolio Greeks. An empty portfolio is
    a well-defined, legitimate "flat" book — this returns all-zero
    Greeks rather than raising, unlike most other modules in this
    codebase that raise on an empty input (there's nothing invalid about
    holding zero positions).
    """
    netDelta = 0.0
    netGamma = 0.0
    netVega = 0.0
    netTheta = 0.0

    for position in positions:
        greeks = position.perContractGreeks
        netDelta += position.quantity * greeks.delta
        netGamma += position.quantity * greeks.gamma
        netVega += position.quantity * greeks.vegaPerOnePercentVolatilityChange
        netTheta += position.quantity * greeks.thetaPerCalendarDay

    return PortfolioGreeksAggregationResult(
        netDelta=netDelta,
        netGamma=netGamma,
        netVegaPerOnePercentVolatilityChange=netVega,
        netThetaPerCalendarDay=netTheta,
        positionCount=len(positions),
    )
