"""A Fama-French-style / Barra-lite factor risk model for portfolio
construction and exposure reporting. See FEATURES.md §22 ("Deep Quant &
Algorithmic Trading Internals") — "Factor risk model ... for portfolio
construction and exposure reporting".

**Read this before treating any factor EXPOSURE number here as real
research.** A real factor model (Fama-French three/five-factor, or a
commercial Barra multi-factor model) estimates each holding's factor
LOADINGS via time-series or cross-sectional regression against decades
of real market, size, value, momentum, quality, etc. factor return
series. Building or sourcing those factor return series and running that
regression is explicitly OUT OF SCOPE here — this module does not fetch,
estimate, or fabricate any real per-symbol factor loading. Every
per-holding factor exposure (`marketBeta`, `size`, `value`, or any other
factor name a caller supplies) is an ILLUSTRATIVE, CALLER-SUPPLIED number
— treat it exactly like `esgScoringEngine.py`'s fabricated dataset: the
INPUT is illustrative, but everything this module DOES with that input is
real, documented math:

1. **Portfolio-level factor exposure aggregation**
   (`computePortfolioFactorExposures`): a real weight-weighted sum of
   each holding's per-factor exposure — `portfolioExposure_f =
   sum(holding.weight * holding.factorExposures[f] for holding in
   holdings)`. Standard portfolio-construction math, not illustrative.
2. **Factor-attribution decomposition of return**
   (`computeFactorAttribution`): given the portfolio's aggregated factor
   exposures and a (real or illustrative, caller-supplied) return for
   each factor over some period, this computes each factor's REAL
   contribution to portfolio return as `exposure_f * factorReturn_f`,
   sums them into `totalFactorContribution`, and attributes whatever's
   left of the portfolio's actual/expected return to
   `idiosyncraticReturn = actualOrExpectedPortfolioReturn -
   totalFactorContribution` — the standard linear factor-model
   decomposition `r_p = sum(beta_f * f) + epsilon`, computed exactly, not
   approximated.

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True)
class PortfolioHoldingWithFactorExposures:
    symbol: str
    portfolioWeight: float
    factorExposuresByName: dict[str, float]

    def __post_init__(self) -> None:
        if not self.factorExposuresByName:
            raise ValueError(f"holding '{self.symbol}' must carry at least one factor exposure")


@dataclass(frozen=True)
class PortfolioFactorExposureResult:
    portfolioExposureByFactor: dict[str, float]
    totalPortfolioWeight: float
    holdingCount: int


def computePortfolioFactorExposures(
    holdings: list[PortfolioHoldingWithFactorExposures],
) -> PortfolioFactorExposureResult:
    """Real weight-weighted sum of each holding's factor exposures into
    one portfolio-level exposure per factor. Every holding must carry
    exposures for the SAME set of factor names (a holding missing a
    factor the others have is almost certainly a data-entry mistake, not
    a legitimate "zero exposure" — raises `ValueError` rather than
    silently defaulting to zero). Raises `ValueError` on an empty
    `holdings` list.

    `totalPortfolioWeight` is returned as a diagnostic (not enforced to
    equal 1.0) — a caller may legitimately pass sub-1.0 weights (partial
    cash allocation) or above-1.0 weights (leveraged/gross exposure); it
    is on the caller to interpret that number for their own portfolio
    construction convention.
    """
    if not holdings:
        raise ValueError("holdings must contain at least one position")

    factorNames = set(holdings[0].factorExposuresByName.keys())
    for holding in holdings[1:]:
        if set(holding.factorExposuresByName.keys()) != factorNames:
            raise ValueError(
                f"holding '{holding.symbol}' carries a different set of factor names than "
                f"'{holdings[0].symbol}' — every holding must expose the same factor set"
            )

    portfolioExposureByFactor = {
        factorName: sum(holding.portfolioWeight * holding.factorExposuresByName[factorName] for holding in holdings)
        for factorName in factorNames
    }

    return PortfolioFactorExposureResult(
        portfolioExposureByFactor=portfolioExposureByFactor,
        totalPortfolioWeight=sum(holding.portfolioWeight for holding in holdings),
        holdingCount=len(holdings),
    )


@dataclass(frozen=True)
class FactorAttributionResult:
    contributionByFactor: dict[str, float]
    totalFactorContribution: float
    idiosyncraticReturn: float
    actualOrExpectedPortfolioReturn: float

    def contributionFractionByFactor(self) -> dict[str, float]:
        """Each factor's contribution as a FRACTION of the total
        (signed) portfolio return — useful for a "how much of my return
        came from beta vs. size vs. stock-picking" report. Returns an
        empty dict rather than dividing by zero when
        `actualOrExpectedPortfolioReturn` is exactly zero.
        """
        if self.actualOrExpectedPortfolioReturn == 0.0:
            return {}
        return {
            factorName: contribution / self.actualOrExpectedPortfolioReturn
            for factorName, contribution in self.contributionByFactor.items()
        }


def computeFactorAttribution(
    portfolioExposureByFactor: dict[str, float],
    factorReturnsByName: dict[str, float],
    actualOrExpectedPortfolioReturn: float,
) -> FactorAttributionResult:
    """Real linear factor-model return decomposition:
    `contribution_f = portfolioExposure_f * factorReturn_f`,
    `totalFactorContribution = sum(contribution_f for every factor f)`,
    `idiosyncraticReturn = actualOrExpectedPortfolioReturn -
    totalFactorContribution`.

    `factorReturnsByName` must supply a return for every factor named in
    `portfolioExposureByFactor` (raises `KeyError`-wrapped `ValueError`
    on a missing one — silently treating a missing factor return as zero
    would hide a real data gap).
    """
    missingFactors = set(portfolioExposureByFactor.keys()) - set(factorReturnsByName.keys())
    if missingFactors:
        raise ValueError(f"factorReturnsByName is missing a return for factor(s): {sorted(missingFactors)}")

    contributionByFactor = {
        factorName: exposure * factorReturnsByName[factorName]
        for factorName, exposure in portfolioExposureByFactor.items()
    }
    totalFactorContribution = sum(contributionByFactor.values())
    idiosyncraticReturn = actualOrExpectedPortfolioReturn - totalFactorContribution

    return FactorAttributionResult(
        contributionByFactor=contributionByFactor,
        totalFactorContribution=totalFactorContribution,
        idiosyncraticReturn=idiosyncraticReturn,
        actualOrExpectedPortfolioReturn=actualOrExpectedPortfolioReturn,
    )
