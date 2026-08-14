"""Options-aware corporate-action handling: auto-adjust strike/quantity
on splits, flag early-exercise risk around ex-dividend dates for
American-style contracts. See FEATURES.md §22 ("Deep Quant &
Algorithmic Trading Internals") — "Options-aware corporate-action
handling: auto-adjust strike/quantity on splits, flag early-exercise
risk around ex-dividend dates for American-style contracts".

Two real, independent, textbook-standard pieces:

1. **Stock-split strike/quantity adjustment**
   (`applyStockSplitAdjustmentToOptionPosition`): the REAL, standard
   options-industry (OCC-style) adjustment for a forward stock split with
   ratio `splitRatio` (e.g. a 2-for-1 split has `splitRatio = 2.0`):

       newStrikePrice = oldStrikePrice / splitRatio
       newQuantity    = oldQuantity * splitRatio

   This exactly preserves both the contract's aggregate notional exposure
   (`strike * quantity` is invariant across the adjustment) and its
   moneyness relative to the equally-split underlying price. A reverse
   split is just the same formula with `splitRatio < 1.0` (e.g. a 1-for-4
   reverse split is `splitRatio = 0.25`) — no special-cased branch is
   needed because the formula is symmetric.
2. **Early-exercise risk flag for American calls near an ex-dividend
   date** (`evaluateEarlyExerciseRiskAroundExDividendDate`): the REAL
   textbook condition from option-pricing theory (see e.g. Hull,
   *Options, Futures, and Other Derivatives*): early exercise of an
   American-style call option is NEVER optimal except possibly
   immediately before an ex-dividend date, because a call holder gives up
   the remaining time value of the option (at minimum, its intrinsic
   value's protective/insurance value against a price drop) to capture a
   dividend the option itself doesn't receive. The classic sufficient
   condition this module checks: early exercise become POTENTIALLY
   worthwhile when the dividend the holder would capture by exercising
   and holding the stock through the ex-dividend date EXCEEDS the
   remaining time value of the call (`callTimeValue = callMarketPrice -
   intrinsicValue`). This module implements exactly that comparison — it
   does NOT claim exercising is always optimal even when flagged (a
   holder must also weigh transaction costs, tax treatment, and
   opportunity cost of capital, all explicitly out of scope), only that
   the textbook NECESSARY condition for early exercise to be worth
   considering is met. American PUTS and European contracts of either
   side are NEVER flagged (early exercise of a put can be optimal for a
   totally different reason — deep in-the-money interest-rate-driven
   early exercise — which is explicitly OUT OF SCOPE here; only the
   call/ex-dividend case from FEATURES.md's own wording is implemented).

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum


class OptionExerciseStyle(Enum):
    AMERICAN = "AMERICAN"
    EUROPEAN = "EUROPEAN"


class OptionContractSide(Enum):
    CALL = "CALL"
    PUT = "PUT"


@dataclass(frozen=True)
class OptionPosition:
    symbol: str
    strikePrice: float
    quantity: float
    exerciseStyle: OptionExerciseStyle
    contractSide: OptionContractSide

    def __post_init__(self) -> None:
        if self.strikePrice <= 0:
            raise ValueError("strikePrice must be positive")
        if self.quantity == 0:
            raise ValueError("quantity must be non-zero (zero is not a position)")


@dataclass(frozen=True)
class StockSplitAdjustmentResult:
    originalPosition: OptionPosition
    splitRatio: float
    adjustedStrikePrice: float
    adjustedQuantity: float

    def notionalExposureIsPreserved(self, toleranceAbsolute: float = 1e-9) -> bool:
        """Sanity check that `strike * quantity` is invariant across the
        adjustment (up to floating-point tolerance) — a real property of
        the standard split-adjustment formula, exposed here so callers
        (and tests) can verify it directly rather than trusting the
        arithmetic blindly.
        """
        originalNotional = self.originalPosition.strikePrice * self.originalPosition.quantity
        adjustedNotional = self.adjustedStrikePrice * self.adjustedQuantity
        return abs(originalNotional - adjustedNotional) <= toleranceAbsolute


def applyStockSplitAdjustmentToOptionPosition(
    position: OptionPosition, splitRatio: float
) -> StockSplitAdjustmentResult:
    """Real, standard forward/reverse stock-split adjustment:

        adjustedStrikePrice = position.strikePrice / splitRatio
        adjustedQuantity    = position.quantity * splitRatio

    `splitRatio` is expressed as "new shares per old share" — a 2-for-1
    split is `2.0`, a 3-for-2 split is `1.5`, a 1-for-4 reverse split is
    `0.25`. Raises `ValueError` if `splitRatio` is not strictly positive
    (a zero or negative split ratio is not a real corporate action).
    """
    if splitRatio <= 0:
        raise ValueError("splitRatio must be strictly positive")

    return StockSplitAdjustmentResult(
        originalPosition=position,
        splitRatio=splitRatio,
        adjustedStrikePrice=position.strikePrice / splitRatio,
        adjustedQuantity=position.quantity * splitRatio,
    )


@dataclass(frozen=True)
class ExDividendEarlyExerciseRiskResult:
    position: OptionPosition
    intrinsicValue: float
    callTimeValue: float
    dividendAmount: float
    isFlaggedForEarlyExerciseRisk: bool
    reason: str


def evaluateEarlyExerciseRiskAroundExDividendDate(
    position: OptionPosition,
    underlyingSpotPrice: float,
    callMarketPrice: float,
    dividendAmount: float,
) -> ExDividendEarlyExerciseRiskResult:
    """Real textbook early-exercise-risk check (see module docstring for
    the full derivation) for ONE American call position approaching an
    ex-dividend date:

        intrinsicValue = max(underlyingSpotPrice - position.strikePrice, 0)
        callTimeValue  = callMarketPrice - intrinsicValue
        isFlagged      = dividendAmount > callTimeValue

    Only ever flags `OptionExerciseStyle.AMERICAN` `OptionContractSide.CALL`
    positions — European contracts (no early exercise is possible at all)
    and puts (a different, out-of-scope early-exercise driver) are always
    returned with `isFlaggedForEarlyExerciseRisk=False` and a `reason`
    explaining why the check doesn't apply, rather than silently
    evaluating a formula that doesn't mean anything for that
    style/side. Raises `ValueError` if `callMarketPrice` is negative,
    `underlyingSpotPrice` is not positive, or `dividendAmount` is
    negative.
    """
    if underlyingSpotPrice <= 0:
        raise ValueError("underlyingSpotPrice must be positive")
    if callMarketPrice < 0:
        raise ValueError("callMarketPrice must be non-negative")
    if dividendAmount < 0:
        raise ValueError("dividendAmount must be non-negative")

    if position.exerciseStyle != OptionExerciseStyle.AMERICAN:
        return ExDividendEarlyExerciseRiskResult(
            position=position,
            intrinsicValue=max(underlyingSpotPrice - position.strikePrice, 0.0),
            callTimeValue=0.0,
            dividendAmount=dividendAmount,
            isFlaggedForEarlyExerciseRisk=False,
            reason="EUROPEAN-style contracts cannot be exercised early at all — the check does not apply",
        )
    if position.contractSide != OptionContractSide.CALL:
        return ExDividendEarlyExerciseRiskResult(
            position=position,
            intrinsicValue=max(position.strikePrice - underlyingSpotPrice, 0.0),
            callTimeValue=0.0,
            dividendAmount=dividendAmount,
            isFlaggedForEarlyExerciseRisk=False,
            reason=(
                "PUT early-exercise risk is driven by a different mechanism (deep in-the-money "
                "interest-rate considerations), explicitly out of scope for this ex-dividend check"
            ),
        )

    intrinsicValue = max(underlyingSpotPrice - position.strikePrice, 0.0)
    callTimeValue = callMarketPrice - intrinsicValue
    isFlagged = dividendAmount > callTimeValue

    reason = (
        f"dividend ({dividendAmount:.4f}) exceeds remaining call time value ({callTimeValue:.4f}) — "
        "the textbook necessary condition for early exercise to be worth considering is met"
        if isFlagged
        else (
            f"dividend ({dividendAmount:.4f}) does not exceed remaining call time value "
            f"({callTimeValue:.4f}) — holding the option remains preferable to early exercise"
        )
    )

    return ExDividendEarlyExerciseRiskResult(
        position=position,
        intrinsicValue=intrinsicValue,
        callTimeValue=callTimeValue,
        dividendAmount=dividendAmount,
        isFlaggedForEarlyExerciseRisk=isFlagged,
        reason=reason,
    )
