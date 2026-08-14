"""Synthetic position builder: recognizes and validates multi-leg option
combinations that replicate a named synthetic-equivalent structure (e.g.
"synthetic long stock" = long call + short put at the same strike/
expiry), and aggregates their combined Greeks as one unit. See
FEATURES.md §22 ("Deep Quant & Algorithmic Trading Internals").

Real, textbook synthetic-equivalent definitions (put-call parity based),
each expressed as the SIGNED per-leg quantities (relative to one another,
not absolute contract counts) a combination must match at the SAME
strike and SAME expiry to legitimately be called that structure:

- SYNTHETIC LONG STOCK   = long 1 call + short 1 put (same strike/expiry)
- SYNTHETIC SHORT STOCK  = short 1 call + long 1 put (same strike/expiry)
- SYNTHETIC LONG CALL    = long 1 underlying share (delta-equivalent
                            proxy: 100 shares per contract) + long 1 put
- SYNTHETIC LONG PUT     = short 1 underlying share (100 shares per
                            contract) + long 1 call
- SYNTHETIC COVERED CALL = long 1 underlying share (100 shares) + short
                            1 call  (a.k.a. "synthetic short put")

This module does NOT invent new synthetic definitions or assign a
"fair value" to any of them — it validates a caller-supplied combination
of legs against these fixed, real, well-known put-call-parity structures,
and reuses `portfolioGreeksAggregator.aggregatePortfolioGreeks` (not
reimplemented) to combine their Greeks into one net figure per structure.

Margin for these structures is NOT computed here — real margin rules
(Reg T, portfolio margin, broker-specific haircuts) are exchange/broker-
specific and out of scope for this research-tier module; see the
`combinedGreeks` aggregation as this module's real, tested contribution,
with margin left as an explicit caller-supplied number for reporting
purposes only (not derived).

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

import enum
from dataclasses import dataclass

from quantengine.portfolioGreeksAggregator import (
    PortfolioGreeksAggregationResult,
    PortfolioPosition,
    aggregatePortfolioGreeks,
)


class OptionLegType(enum.Enum):
    CALL = "CALL"
    PUT = "PUT"
    UNDERLYING_SHARE = "UNDERLYING_SHARE"


@dataclass(frozen=True)
class SyntheticPositionLeg:
    """One leg of a proposed synthetic combination. `quantity` is signed
    (positive = long, negative = short). For `OptionLegType.CALL`/`PUT`
    legs, `strikePrice` must be supplied (all option legs in a valid
    synthetic structure share the same strike); for
    `OptionLegType.UNDERLYING_SHARE` legs, `strikePrice` is ignored (pass
    `None`).
    """

    legType: OptionLegType
    quantity: float
    strikePrice: float | None = None


class SyntheticStructureName(enum.Enum):
    SYNTHETIC_LONG_STOCK = "SYNTHETIC_LONG_STOCK"
    SYNTHETIC_SHORT_STOCK = "SYNTHETIC_SHORT_STOCK"
    SYNTHETIC_LONG_CALL = "SYNTHETIC_LONG_CALL"
    SYNTHETIC_LONG_PUT = "SYNTHETIC_LONG_PUT"
    SYNTHETIC_COVERED_CALL = "SYNTHETIC_COVERED_CALL"


# One standard equity option contract represents 100 underlying shares —
# used to compare an UNDERLYING_SHARE leg's quantity against an option
# leg's quantity on a like-for-like ("per contract") basis.
SHARES_PER_STANDARD_OPTION_CONTRACT = 100.0

# Each real definition below is expressed as the exact SIGNED per-leg
# quantity (in "per one option contract" units — an UNDERLYING_SHARE leg
# quantity of 1.0 here means SHARES_PER_STANDARD_OPTION_CONTRACT shares)
# that a combination's legs must match, per unit of structure.
_SYNTHETIC_STRUCTURE_DEFINITIONS: dict[SyntheticStructureName, dict[OptionLegType, float]] = {
    SyntheticStructureName.SYNTHETIC_LONG_STOCK: {OptionLegType.CALL: 1.0, OptionLegType.PUT: -1.0},
    SyntheticStructureName.SYNTHETIC_SHORT_STOCK: {OptionLegType.CALL: -1.0, OptionLegType.PUT: 1.0},
    SyntheticStructureName.SYNTHETIC_LONG_CALL: {
        OptionLegType.UNDERLYING_SHARE: 1.0,
        OptionLegType.PUT: 1.0,
    },
    SyntheticStructureName.SYNTHETIC_LONG_PUT: {
        OptionLegType.UNDERLYING_SHARE: -1.0,
        OptionLegType.CALL: 1.0,
    },
    SyntheticStructureName.SYNTHETIC_COVERED_CALL: {
        OptionLegType.UNDERLYING_SHARE: 1.0,
        OptionLegType.CALL: -1.0,
    },
}


@dataclass(frozen=True)
class SyntheticStructureValidationResult:
    matchedStructureName: SyntheticStructureName | None
    isValidMatch: bool
    explanation: str


def validateProposedCombinationAgainstSyntheticStructure(
    legs: list[SyntheticPositionLeg], candidateStructureName: SyntheticStructureName
) -> SyntheticStructureValidationResult:
    """Checks whether `legs` exactly matches `candidateStructureName`'s
    real definition: the same set of leg types, all option legs sharing
    ONE common strike price, and each leg's quantity a POSITIVE common
    multiple of the structure's defined per-unit ratio (e.g. 2 long
    calls + 2 short puts at the same strike is 2 units of
    SYNTHETIC_LONG_STOCK — still valid).

    Returns a result object rather than raising on a mismatch — "this
    combination is NOT that synthetic structure" is an expected, valid
    outcome for a caller probing several candidate structures, not an
    error condition.
    """
    structureDefinition = _SYNTHETIC_STRUCTURE_DEFINITIONS[candidateStructureName]

    if not legs:
        return SyntheticStructureValidationResult(None, False, "no legs supplied")

    legTypesPresent = {leg.legType for leg in legs}
    if legTypesPresent != set(structureDefinition.keys()):
        return SyntheticStructureValidationResult(
            None,
            False,
            f"leg types {sorted(t.value for t in legTypesPresent)} do not match "
            f"{candidateStructureName.value}'s required leg types "
            f"{sorted(t.value for t in structureDefinition.keys())}",
        )

    optionLegs = [leg for leg in legs if leg.legType in (OptionLegType.CALL, OptionLegType.PUT)]
    if optionLegs:
        strikePrices = {leg.strikePrice for leg in optionLegs}
        if len(strikePrices) != 1 or None in strikePrices:
            return SyntheticStructureValidationResult(
                None, False, "all option legs in a synthetic structure must share exactly one strike price"
            )

    # Determine the implied "unit multiplier" from the first leg, then
    # require every other leg's quantity to be exactly that multiplier
    # times the structure's defined ratio for its leg type.
    firstLeg = legs[0]
    definedRatioForFirstLeg = structureDefinition[firstLeg.legType]
    if definedRatioForFirstLeg == 0.0:
        return SyntheticStructureValidationResult(None, False, "malformed structure definition")
    unitMultiplier = firstLeg.quantity / definedRatioForFirstLeg
    if unitMultiplier <= 0.0:
        return SyntheticStructureValidationResult(
            None, False, "implied unit multiplier is not strictly positive — quantities/signs don't match"
        )

    for leg in legs:
        definedRatio = structureDefinition[leg.legType]
        expectedQuantity = definedRatio * unitMultiplier
        if abs(leg.quantity - expectedQuantity) > 1e-9:
            return SyntheticStructureValidationResult(
                None,
                False,
                f"leg {leg.legType.value} quantity {leg.quantity} does not match expected "
                f"{expectedQuantity} for {unitMultiplier}x {candidateStructureName.value}",
            )

    return SyntheticStructureValidationResult(
        candidateStructureName,
        True,
        f"matches {unitMultiplier}x {candidateStructureName.value}",
    )


def identifySyntheticStructure(legs: list[SyntheticPositionLeg]) -> SyntheticStructureValidationResult:
    """Tries every known synthetic structure definition against `legs`
    and returns the first match (structures are mutually exclusive by
    leg-type set, so at most one can match). Returns an `isValidMatch =
    False` result if `legs` doesn't match any known structure.
    """
    for candidateStructureName in SyntheticStructureName:
        result = validateProposedCombinationAgainstSyntheticStructure(legs, candidateStructureName)
        if result.isValidMatch:
            return result
    return SyntheticStructureValidationResult(None, False, "does not match any known synthetic structure")


@dataclass(frozen=True)
class SyntheticPositionSummary:
    validation: SyntheticStructureValidationResult
    combinedGreeks: PortfolioGreeksAggregationResult
    callerSuppliedMarginEstimate: float | None


def buildSyntheticPositionSummary(
    legs: list[SyntheticPositionLeg],
    legPositionsWithGreeks: list[PortfolioPosition],
    callerSuppliedMarginEstimate: float | None = None,
) -> SyntheticPositionSummary:
    """Identifies which (if any) synthetic structure `legs` matches, and
    combines the SAME combination's Greeks into one net figure via the
    EXISTING `portfolioGreeksAggregator.aggregatePortfolioGreeks`
    (reused, not reimplemented) — showing the whole synthetic structure
    "as one unit", per FEATURES.md §22's framing.

    `legPositionsWithGreeks` is a SEPARATE, caller-supplied list of
    `PortfolioPosition` objects (one per leg, carrying each leg's real
    per-contract Greeks) — this module does not compute Greeks itself
    (that's `blackScholesOptionPricer`/`portfolioGreeksAggregator`'s
    job); it only combines them. Underlying-share legs typically carry
    delta=1.0 (or -1.0 if short), and zero gamma/vega/theta, as their
    Greeks — that's a caller-supplied convention, not enforced here.

    `callerSuppliedMarginEstimate` is passed through unchanged for
    reporting only — see the module docstring for why real margin
    calculation is out of scope here.
    """
    validation = identifySyntheticStructure(legs)
    combinedGreeks = aggregatePortfolioGreeks(legPositionsWithGreeks)
    return SyntheticPositionSummary(
        validation=validation,
        combinedGreeks=combinedGreeks,
        callerSuppliedMarginEstimate=callerSuppliedMarginEstimate,
    )
