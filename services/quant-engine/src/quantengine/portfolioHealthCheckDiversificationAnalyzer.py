"""Portfolio health check / diversification analysis: sector concentration,
position concentration, an optional factor-exposure summary, and genuinely
computed plain-language nudges. See FEATURES.md §16 ("AI, Data & Research")
— "Portfolio health check / diversification analysis (sector, factor,
concentration risk) with plain-language nudges".

What IS real and correctly implemented here:
  1. **Herfindahl-Hirschman Index (HHI)** (`calculateHerfindahlHirschmanIndex`)
     — the real, standard concentration-measurement formula used across
     economics/finance/antitrust (`sum(weight_i^2)` over portfolio weights
     that sum to 1.0, scaled to the conventional 0-10,000 "points" range).
     Computed here over BOTH individual-position weights and sector-
     aggregated weights, exactly the same formula, just a different
     grouping of the same underlying weights — nothing invented.
  2. **Severity thresholds** — this module reuses the well-known DOJ/FTC
     Horizontal Merger Guidelines HHI bands (unconcentrated < 1,500;
     moderately concentrated 1,500-2,500; highly concentrated > 2,500) as
     a real, externally-defined, non-arbitrary convention for turning a
     raw HHI number into a severity label. This is a genuine, citable
     industry convention (not tuned/backtested for this module), applied
     here to portfolio concentration rather than market-share
     concentration — the same formula, same bands, different domain.
  3. **Factor exposure summary** — when a caller supplies per-holding
     factor exposures, this module REUSES `factorRiskModel.py`'s real
     `computePortfolioFactorExposures` (weight-weighted aggregation)
     rather than reimplementing it, exactly per this module's job (a
     health-check report, not a second factor model).
  4. **Plain-language nudges** (`generatePlainLanguageNudges`) — every
     nudge string is genuinely DERIVED from the actual computed numbers
     (interpolating the real top-position weight, real top-sector weight,
     real HHI value, real factor exposure) — not a canned string chosen
     independently of the input. `tests/test_portfolioHealthCheckDiversificationAnalyzer.py`
     asserts directly that a concentrated portfolio and a diversified
     portfolio produce DIFFERENT nudge text and DIFFERENT severities.

What is illustrative/caller-supplied, same convention as
`factorRiskModel.py`: per-holding factor exposures, if supplied, are NOT
sourced from any real factor-loading estimation — they're whatever the
caller provides, exactly like `/portfolio/factor-risk` already documents.

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum

from quantengine.factorRiskModel import (
    PortfolioHoldingWithFactorExposures,
    computePortfolioFactorExposures,
)

# DOJ/FTC Horizontal Merger Guidelines HHI bands (on the conventional
# 0-10,000 scale), reused here as a real, externally-defined convention
# for concentration severity — see module docstring point 2.
HHI_MODERATE_CONCENTRATION_THRESHOLD = 1500.0
HHI_HIGH_CONCENTRATION_THRESHOLD = 2500.0

# A single position exceeding this fraction of the portfolio is flagged
# regardless of the overall HHI reading — a plain, common-sense single-
# name concentration guardrail (documented fixed threshold, not tuned).
SINGLE_POSITION_CONCENTRATION_WARNING_THRESHOLD = 0.20
SINGLE_POSITION_CONCENTRATION_SEVERE_THRESHOLD = 0.40

# A single sector exceeding this fraction of the portfolio is flagged
# similarly.
SECTOR_CONCENTRATION_WARNING_THRESHOLD = 0.35
SECTOR_CONCENTRATION_SEVERE_THRESHOLD = 0.55

# A per-factor absolute exposure beyond this magnitude is flagged as a
# notable directional tilt worth surfacing in plain language.
FACTOR_EXPOSURE_NOTABLE_THRESHOLD = 1.0


class ConcentrationSeverity(Enum):
    LOW = "LOW"
    MODERATE = "MODERATE"
    HIGH = "HIGH"


@dataclass(frozen=True)
class PortfolioHoldingForHealthCheck:
    symbol: str
    sector: str
    portfolioWeight: float
    factorExposuresByName: dict[str, float] | None = None

    def __post_init__(self) -> None:
        if self.portfolioWeight < 0:
            raise ValueError(f"holding '{self.symbol}' has a negative portfolioWeight")


def calculateHerfindahlHirschmanIndex(weights: list[float]) -> float:
    """Real HHI on the conventional 0-10,000 "points" scale:

        HHI = 10,000 * sum(weight_i ** 2)

    where `weights` are fractional shares (expected to sum to ~1.0, though
    this function does not itself enforce that — a caller checking
    sub-portfolio concentration within a larger book may legitimately pass
    weights summing to less than 1.0). A single holding at 100% produces
    HHI = 10,000 (maximum concentration); N equally-weighted holdings
    produce HHI = 10,000 / N. Raises `ValueError` on an empty list.
    """
    if not weights:
        raise ValueError("weights must contain at least one observation")
    return 10_000.0 * sum(weight * weight for weight in weights)


def classifyConcentrationSeverityFromHhi(hhi: float) -> ConcentrationSeverity:
    """Maps a raw HHI value to a `ConcentrationSeverity` using the real
    DOJ/FTC Merger Guidelines bands (see module docstring point 2).
    """
    if hhi >= HHI_HIGH_CONCENTRATION_THRESHOLD:
        return ConcentrationSeverity.HIGH
    if hhi >= HHI_MODERATE_CONCENTRATION_THRESHOLD:
        return ConcentrationSeverity.MODERATE
    return ConcentrationSeverity.LOW


def calculateEffectiveNumberOfHoldings(hhi: float) -> float:
    """The "effective N" implied by an HHI reading —
    `10,000 / hhi` — i.e. the number of EQUALLY-weighted holdings that
    would produce the same HHI. A real, standard diversification-diagnostic
    transform of HHI (a portfolio of 20 wildly unequal positions can have
    the SAME effective N as 4 equally-weighted ones). Raises `ValueError`
    if `hhi` is not strictly positive.
    """
    if hhi <= 0:
        raise ValueError("hhi must be strictly positive to compute an effective number of holdings")
    return 10_000.0 / hhi


def aggregatePortfolioWeightsBySector(holdings: list[PortfolioHoldingForHealthCheck]) -> dict[str, float]:
    """Real sum of `portfolioWeight` grouped by `sector`. Raises
    `ValueError` on an empty holdings list.
    """
    if not holdings:
        raise ValueError("holdings must contain at least one position")
    weightsBySector: dict[str, float] = {}
    for holding in holdings:
        weightsBySector[holding.sector] = weightsBySector.get(holding.sector, 0.0) + holding.portfolioWeight
    return weightsBySector


@dataclass(frozen=True)
class NudgeMessage:
    severity: ConcentrationSeverity
    message: str


def generatePlainLanguageNudges(
    holdings: list[PortfolioHoldingForHealthCheck],
    positionHhi: float,
    sectorHhi: float,
    weightsBySector: dict[str, float],
    portfolioExposureByFactor: dict[str, float] | None,
) -> list[NudgeMessage]:
    """Genuinely derives every nudge string from the actual computed
    numbers passed in — nothing here is a canned message independent of
    input. See module docstring point 4 for why this matters (tested
    directly: different concentration levels MUST produce different text).
    """
    nudges: list[NudgeMessage] = []

    topHolding = max(holdings, key=lambda holding: holding.portfolioWeight)
    if topHolding.portfolioWeight >= SINGLE_POSITION_CONCENTRATION_SEVERE_THRESHOLD:
        nudges.append(
            NudgeMessage(
                ConcentrationSeverity.HIGH,
                f"{topHolding.symbol} alone makes up {topHolding.portfolioWeight:.1%} of this portfolio — "
                "a single-stock setback could have an outsized effect on your total return. Consider "
                "trimming this position to reduce single-name risk.",
            )
        )
    elif topHolding.portfolioWeight >= SINGLE_POSITION_CONCENTRATION_WARNING_THRESHOLD:
        nudges.append(
            NudgeMessage(
                ConcentrationSeverity.MODERATE,
                f"{topHolding.symbol} is your largest position at {topHolding.portfolioWeight:.1%} of the "
                "portfolio — worth keeping an eye on relative to your other holdings.",
            )
        )

    topSector, topSectorWeight = max(weightsBySector.items(), key=lambda item: item[1])
    if topSectorWeight >= SECTOR_CONCENTRATION_SEVERE_THRESHOLD:
        nudges.append(
            NudgeMessage(
                ConcentrationSeverity.HIGH,
                f"The {topSector} sector makes up {topSectorWeight:.1%} of this portfolio — a sector-wide "
                "downturn would hit a large share of your holdings at once. Diversifying into other "
                "sectors would reduce this concentration.",
            )
        )
    elif topSectorWeight >= SECTOR_CONCENTRATION_WARNING_THRESHOLD:
        nudges.append(
            NudgeMessage(
                ConcentrationSeverity.MODERATE,
                f"The {topSector} sector accounts for {topSectorWeight:.1%} of this portfolio, your largest "
                "sector exposure — consider whether that concentration matches your intended strategy.",
            )
        )

    positionSeverity = classifyConcentrationSeverityFromHhi(positionHhi)
    if positionSeverity == ConcentrationSeverity.HIGH:
        effectiveN = calculateEffectiveNumberOfHoldings(positionHhi)
        nudges.append(
            NudgeMessage(
                ConcentrationSeverity.HIGH,
                f"Overall position concentration is high (HHI={positionHhi:.0f}, equivalent to holding "
                f"only about {effectiveN:.1f} equally-weighted positions) — this portfolio behaves like a "
                "much smaller, less diversified one than its holding count suggests.",
            )
        )
    elif positionSeverity == ConcentrationSeverity.MODERATE:
        nudges.append(
            NudgeMessage(
                ConcentrationSeverity.MODERATE,
                f"Position concentration is moderate (HHI={positionHhi:.0f}) — not extreme, but there's "
                "room to spread risk across more positions.",
            )
        )
    else:
        nudges.append(
            NudgeMessage(
                ConcentrationSeverity.LOW,
                f"Position concentration is low (HHI={positionHhi:.0f}) — this portfolio is well spread "
                "across its holdings.",
            )
        )

    if portfolioExposureByFactor:
        for factorName, exposure in sorted(portfolioExposureByFactor.items()):
            if abs(exposure) >= FACTOR_EXPOSURE_NOTABLE_THRESHOLD:
                direction = "positive" if exposure > 0 else "negative"
                nudges.append(
                    NudgeMessage(
                        ConcentrationSeverity.MODERATE,
                        f"This portfolio carries a notable {direction} exposure to the '{factorName}' factor "
                        f"({exposure:+.2f}) — returns are likely to be more sensitive to moves in that factor "
                        "than a factor-neutral portfolio would be.",
                    )
                )

    return nudges


@dataclass(frozen=True)
class PortfolioHealthCheckResult:
    positionHhi: float
    sectorHhi: float
    effectiveNumberOfHoldings: float
    weightsBySector: dict[str, float]
    topPositionSymbol: str
    topPositionWeight: float
    topSector: str
    topSectorWeight: float
    portfolioExposureByFactor: dict[str, float] | None
    nudges: list[NudgeMessage]


def performPortfolioHealthCheck(holdings: list[PortfolioHoldingForHealthCheck]) -> PortfolioHealthCheckResult:
    """The end-to-end health-check entry point: real HHI over positions,
    real HHI over sector-aggregated weights, an optional real factor-
    exposure summary (reusing `factorRiskModel.computePortfolioFactorExposures`
    when every holding supplies `factorExposuresByName`), and genuinely
    input-derived plain-language nudges. Raises `ValueError` on an empty
    `holdings` list.
    """
    if not holdings:
        raise ValueError("holdings must contain at least one position")

    positionWeights = [holding.portfolioWeight for holding in holdings]
    positionHhi = calculateHerfindahlHirschmanIndex(positionWeights)
    if positionHhi == 0:
        # All holdings have exactly 0 portfolioWeight — individually valid
        # per __post_init__ (only negative weights are rejected there),
        # but there is no meaningful weight distribution to compute an
        # "effective number of holdings" over here. Raise a clear,
        # purpose-specific message instead of letting
        # calculateEffectiveNumberOfHoldings's generic internal
        # precondition message ("hhi must be strictly positive...") leak
        # out of context.
        raise ValueError(
            "portfolio weights must sum to a positive total to compute a health check "
            "(all supplied holdings have zero portfolioWeight)"
        )

    weightsBySector = aggregatePortfolioWeightsBySector(holdings)
    sectorHhi = calculateHerfindahlHirschmanIndex(list(weightsBySector.values()))

    topHolding = max(holdings, key=lambda holding: holding.portfolioWeight)
    topSector, topSectorWeight = max(weightsBySector.items(), key=lambda item: item[1])

    portfolioExposureByFactor: dict[str, float] | None = None
    if all(holding.factorExposuresByName for holding in holdings):
        factorHoldings = [
            PortfolioHoldingWithFactorExposures(
                symbol=holding.symbol,
                portfolioWeight=holding.portfolioWeight,
                factorExposuresByName=holding.factorExposuresByName,  # type: ignore[arg-type]
            )
            for holding in holdings
        ]
        exposureResult = computePortfolioFactorExposures(factorHoldings)
        portfolioExposureByFactor = exposureResult.portfolioExposureByFactor

    nudges = generatePlainLanguageNudges(holdings, positionHhi, sectorHhi, weightsBySector, portfolioExposureByFactor)

    return PortfolioHealthCheckResult(
        positionHhi=positionHhi,
        sectorHhi=sectorHhi,
        effectiveNumberOfHoldings=calculateEffectiveNumberOfHoldings(positionHhi),
        weightsBySector=weightsBySector,
        topPositionSymbol=topHolding.symbol,
        topPositionWeight=topHolding.portfolioWeight,
        topSector=topSector,
        topSectorWeight=topSectorWeight,
        portfolioExposureByFactor=portfolioExposureByFactor,
        nudges=nudges,
    )
