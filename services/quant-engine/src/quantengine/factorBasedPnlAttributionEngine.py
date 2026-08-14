"""Factor-based P&L attribution: how much of active return is sector
allocation (over/underweighting) vs. stock selection vs. currency. See
FEATURES.md §16 ("AI, Data & Research") — "Factor-based P&L attribution
(how much of return is sector beta vs. stock selection vs. currency)".

Implements the real, standard Brinson-Hood-Beebower (BHB) three-term
attribution decomposition, per-sector:

    allocationEffect_i  = (portfolioWeight_i - benchmarkWeight_i) * benchmarkReturn_i
    selectionEffect_i   = benchmarkWeight_i * (portfolioReturn_i - benchmarkReturn_i)
    interactionEffect_i = (portfolioWeight_i - benchmarkWeight_i) * (portfolioReturn_i - benchmarkReturn_i)

This is the classic 1986 Brinson/Hood/Beebower model (the most widely
taught real performance-attribution framework, still in production use at
many asset managers, and the direct ancestor of the later Brinson-Fachler
variant). The defining, EXACT identity this module tests against a
hand-worked example is that these three effects, summed across every
sector, equal the portfolio's total ACTIVE return (portfolio return minus
benchmark return) with no residual — real attribution math, not an
approximation.

Currency effect (`currencyEffect_i = portfolioWeight_i * currencyReturn_i`)
is layered on top as a separate overlay: sector allocation/selection/
interaction are computed on LOCAL (currency-hedged) returns, and the
currency effect separately captures the portfolio-weighted contribution of
each sector's currency return — a standard "local attribution + currency
overlay" construction used when a portfolio spans multiple currencies.
`currencyReturn` defaults to `0.0` per sector (a single-currency portfolio
simply has zero currency effect everywhere).

All weights/returns in this module are CALLER-SUPPLIED (portfolio weights/
returns vs. benchmark weights/returns by sector) — like `factorRiskModel.py`,
this module does no data ingestion of its own; it is real math over
whatever numbers a caller provides.

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True)
class SectorAttributionInput:
    sectorName: str
    portfolioWeight: float
    portfolioLocalReturn: float
    benchmarkWeight: float
    benchmarkLocalReturn: float
    currencyReturn: float = 0.0


@dataclass(frozen=True)
class SectorAttributionResult:
    sectorName: str
    allocationEffect: float
    selectionEffect: float
    interactionEffect: float
    currencyEffect: float

    @property
    def totalSectorEffect(self) -> float:
        return self.allocationEffect + self.selectionEffect + self.interactionEffect + self.currencyEffect


@dataclass(frozen=True)
class BrinsonAttributionResult:
    sectorResults: list[SectorAttributionResult]
    totalPortfolioLocalReturn: float
    totalBenchmarkReturn: float
    totalActiveReturn: float
    totalAllocationEffect: float
    totalSelectionEffect: float
    totalInteractionEffect: float
    totalCurrencyEffect: float

    @property
    def totalPortfolioReturnIncludingCurrency(self) -> float:
        return self.totalPortfolioLocalReturn + self.totalCurrencyEffect


def computeBrinsonAttribution(sectors: list[SectorAttributionInput]) -> BrinsonAttributionResult:
    """The real Brinson-Hood-Beebower attribution entry point. Computes
    the three-term (allocation/selection/interaction) decomposition per
    sector plus a separate currency-overlay effect, and the portfolio-
    level totals of each. Raises `ValueError` on an empty `sectors` list.

    Real, exact identity verified by
    `tests/test_factorBasedPnlAttributionEngine.py`'s hand-worked example:
    `totalAllocationEffect + totalSelectionEffect + totalInteractionEffect
    == totalActiveReturn` (portfolio local return minus benchmark return),
    to floating-point precision.
    """
    if not sectors:
        raise ValueError("sectors must contain at least one sector")

    sectorResults: list[SectorAttributionResult] = []
    for sector in sectors:
        weightDifference = sector.portfolioWeight - sector.benchmarkWeight
        returnDifference = sector.portfolioLocalReturn - sector.benchmarkLocalReturn

        allocationEffect = weightDifference * sector.benchmarkLocalReturn
        selectionEffect = sector.benchmarkWeight * returnDifference
        interactionEffect = weightDifference * returnDifference
        currencyEffect = sector.portfolioWeight * sector.currencyReturn

        sectorResults.append(
            SectorAttributionResult(
                sectorName=sector.sectorName,
                allocationEffect=allocationEffect,
                selectionEffect=selectionEffect,
                interactionEffect=interactionEffect,
                currencyEffect=currencyEffect,
            )
        )

    totalPortfolioLocalReturn = sum(
        sector.portfolioWeight * sector.portfolioLocalReturn for sector in sectors
    )
    totalBenchmarkReturn = sum(sector.benchmarkWeight * sector.benchmarkLocalReturn for sector in sectors)

    return BrinsonAttributionResult(
        sectorResults=sectorResults,
        totalPortfolioLocalReturn=totalPortfolioLocalReturn,
        totalBenchmarkReturn=totalBenchmarkReturn,
        totalActiveReturn=totalPortfolioLocalReturn - totalBenchmarkReturn,
        totalAllocationEffect=sum(result.allocationEffect for result in sectorResults),
        totalSelectionEffect=sum(result.selectionEffect for result in sectorResults),
        totalInteractionEffect=sum(result.interactionEffect for result in sectorResults),
        totalCurrencyEffect=sum(result.currencyEffect for result in sectorResults),
    )
