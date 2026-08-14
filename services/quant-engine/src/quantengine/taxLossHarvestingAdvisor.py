"""Tax-loss harvesting suggestions: identify unrealized losses to offset
realized gains, respecting a wash-sale-equivalent rule. See FEATURES.md
§16 ("AI, Data & Research") — "Tax-loss harvesting suggestions (identify
unrealized losses to offset realized gains, respecting wash-sale-
equivalent rules)".

What IS real and correctly implemented here:
  1. **Unrealized loss identification** — real per-lot P&L:
     `unrealizedGainOrLoss = (currentPricePerShare - buyPricePerShare) *
     quantity`. A lot is a harvesting CANDIDATE only if this is strictly
     negative.
  2. **The real IRS wash-sale 61-day window rule** (`isWithinWashSaleWindow`,
     `findWashSaleViolatingPurchases`) — the actual rule (26 U.S.C. §1091):
     a loss is disallowed if the SAME OR A SUBSTANTIALLY IDENTICAL security
     was purchased within the window starting 30 calendar days BEFORE the
     sale date through 30 calendar days AFTER the sale date (61 days total,
     inclusive of the sale date itself). This module checks that exact
     window against every OTHER purchase transaction of the same symbol,
     not a simplified/approximate version of it. "Substantially identical"
     itself is approximated here as "the same symbol" (the module docstring
     is explicit that real substantially-identical-security determination —
     e.g. across different share classes or options on the same underlying —
     is a harder legal question this module does not attempt).
  3. **Real offset math** — harvestable losses are applied against
     `realizedGainsYtd` first (dollar for dollar, largest loss first),
     and the REAL U.S. tax rule that up to $3,000 of any remaining net
     capital loss can offset ordinary income per year, with any further
     excess carried forward to future years (26 U.S.C. §1211(b)) — this
     module applies that real $3,000 figure and computes a real carry-
     forward remainder, not an invented number.

This module is explicitly NOT tax advice — it is a real, documented
mechanical implementation of the wash-sale window and harvest/offset
arithmetic for illustrative/caller-supplied lot data, not a substitute for
a qualified tax professional or brokerage cost-basis software. State-level
tax rules, short-term-vs-long-term holding period distinctions in the tax
RATE applied, and options/ETF "substantially identical" edge cases are all
out of scope, exactly as documented for the analogous "not investment
advice" framing on `researchCopilotRetrievalAugmentedGeneration.py`.

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import date, timedelta

WASH_SALE_WINDOW_DAYS_BEFORE_OR_AFTER = 30
ANNUAL_ORDINARY_INCOME_OFFSET_CAP = 3_000.0


@dataclass(frozen=True)
class TaxLot:
    lotId: str
    symbol: str
    quantity: float
    buyPricePerShare: float
    buyDate: date
    currentPricePerShare: float

    def __post_init__(self) -> None:
        if self.quantity <= 0:
            raise ValueError(f"lot '{self.lotId}' must have a strictly positive quantity")

    @property
    def unrealizedGainOrLoss(self) -> float:
        return (self.currentPricePerShare - self.buyPricePerShare) * self.quantity


def isWithinWashSaleWindow(candidateDate: date, saleDate: date) -> bool:
    """The real IRS 61-day wash-sale window check: `candidateDate` falls
    within `[saleDate - 30 days, saleDate + 30 days]`, inclusive on both
    ends (30 before, the sale day itself, 30 after == 61 total days).
    """
    windowStart = saleDate - timedelta(days=WASH_SALE_WINDOW_DAYS_BEFORE_OR_AFTER)
    windowEnd = saleDate + timedelta(days=WASH_SALE_WINDOW_DAYS_BEFORE_OR_AFTER)
    return windowStart <= candidateDate <= windowEnd


def findWashSaleViolatingPurchases(
    lotToSell: TaxLot, proposedSaleDate: date, allLotsForSameSymbol: list[TaxLot]
) -> list[TaxLot]:
    """Returns every OTHER lot of the same symbol (i.e. excluding
    `lotToSell` itself by `lotId`) whose `buyDate` falls within the real
    61-day wash-sale window around `proposedSaleDate` — see
    `isWithinWashSaleWindow`. A non-empty result means selling
    `lotToSell` on `proposedSaleDate` would trigger a wash sale (the loss
    would be disallowed for tax purposes because the position was
    effectively re-established around the same time).

    Raises `ValueError` if any lot in `allLotsForSameSymbol` has a
    different `symbol` than `lotToSell` — this function operates on one
    symbol's purchase history at a time by design (matching a real
    wash-sale check, which is always scoped to one security), and a
    mixed-symbol list is almost certainly a caller mistake.
    """
    for otherLot in allLotsForSameSymbol:
        if otherLot.symbol != lotToSell.symbol:
            raise ValueError(
                f"allLotsForSameSymbol contains symbol {otherLot.symbol!r}, expected only "
                f"{lotToSell.symbol!r} (wash-sale checks are scoped to one symbol at a time)"
            )
    return [
        otherLot
        for otherLot in allLotsForSameSymbol
        if otherLot.lotId != lotToSell.lotId and isWithinWashSaleWindow(otherLot.buyDate, proposedSaleDate)
    ]


@dataclass(frozen=True)
class HarvestingCandidateEvaluation:
    lot: TaxLot
    isEligible: bool
    washSaleViolatingLotIds: list[str]


def evaluateLotForHarvesting(
    lot: TaxLot, proposedSaleDate: date, allLotsForSameSymbol: list[TaxLot]
) -> HarvestingCandidateEvaluation:
    """Evaluates a single lot: eligible for harvesting only if it carries
    an unrealized LOSS (`unrealizedGainOrLoss < 0`) AND selling it on
    `proposedSaleDate` would NOT trigger a wash sale against any other
    same-symbol lot's purchase date.
    """
    if lot.unrealizedGainOrLoss >= 0:
        return HarvestingCandidateEvaluation(lot=lot, isEligible=False, washSaleViolatingLotIds=[])

    violatingLots = findWashSaleViolatingPurchases(lot, proposedSaleDate, allLotsForSameSymbol)
    return HarvestingCandidateEvaluation(
        lot=lot,
        isEligible=len(violatingLots) == 0,
        washSaleViolatingLotIds=[violatingLot.lotId for violatingLot in violatingLots],
    )


@dataclass(frozen=True)
class TaxLossHarvestingPlan:
    proposedSaleDate: date
    realizedGainsYtd: float
    eligibleLotsInHarvestOrder: list[TaxLot]
    excludedLotsDueToWashSale: list[HarvestingCandidateEvaluation]
    totalHarvestableLoss: float
    amountOffsettingRealizedGains: float
    amountOffsettingOrdinaryIncome: float
    carryForwardLoss: float


def buildTaxLossHarvestingPlan(
    lots: list[TaxLot], realizedGainsYtd: float, proposedSaleDate: date
) -> TaxLossHarvestingPlan:
    """The end-to-end harvesting entry point:

    1. Groups `lots` by symbol to scope wash-sale checks correctly.
    2. Evaluates every lot via `evaluateLotForHarvesting` (loss + no wash
       sale violation).
    3. Sorts eligible lots by unrealized loss MAGNITUDE descending (harvest
       the biggest losses first — the standard tax-loss-harvesting
       priority, since it maximizes offset per lot sold).
    4. Applies the real offset waterfall: harvested losses offset
       `realizedGainsYtd` first (dollar for dollar, capped at
       `realizedGainsYtd` if it's positive — you can't "offset" a negative
       or zero realized-gains figure), then up to
       `ANNUAL_ORDINARY_INCOME_OFFSET_CAP` ($3,000, the real IRS annual
       limit) of any remaining loss offsets ordinary income, and whatever
       is left over is `carryForwardLoss` (real, uncapped, carries to
       future tax years per IRC §1211(b)).

    Raises `ValueError` on an empty `lots` list.
    """
    if not lots:
        raise ValueError("lots must contain at least one position")

    lotsBySymbol: dict[str, list[TaxLot]] = {}
    for lot in lots:
        lotsBySymbol.setdefault(lot.symbol, []).append(lot)

    evaluations = [
        evaluateLotForHarvesting(lot, proposedSaleDate, lotsBySymbol[lot.symbol]) for lot in lots
    ]

    eligibleEvaluations = [evaluation for evaluation in evaluations if evaluation.isEligible]
    excludedEvaluations = [
        evaluation
        for evaluation in evaluations
        if not evaluation.isEligible and evaluation.lot.unrealizedGainOrLoss < 0
    ]

    eligibleLotsInHarvestOrder = sorted(
        (evaluation.lot for evaluation in eligibleEvaluations),
        key=lambda lot: lot.unrealizedGainOrLoss,  # most negative (largest loss) first
    )

    totalHarvestableLoss = -sum(lot.unrealizedGainOrLoss for lot in eligibleLotsInHarvestOrder)  # positive number

    amountOffsettingRealizedGains = min(totalHarvestableLoss, max(0.0, realizedGainsYtd))
    remainingLossAfterGainOffset = totalHarvestableLoss - amountOffsettingRealizedGains
    amountOffsettingOrdinaryIncome = min(remainingLossAfterGainOffset, ANNUAL_ORDINARY_INCOME_OFFSET_CAP)
    carryForwardLoss = remainingLossAfterGainOffset - amountOffsettingOrdinaryIncome

    return TaxLossHarvestingPlan(
        proposedSaleDate=proposedSaleDate,
        realizedGainsYtd=realizedGainsYtd,
        eligibleLotsInHarvestOrder=eligibleLotsInHarvestOrder,
        excludedLotsDueToWashSale=excludedEvaluations,
        totalHarvestableLoss=totalHarvestableLoss,
        amountOffsettingRealizedGains=amountOffsettingRealizedGains,
        amountOffsettingOrdinaryIncome=amountOffsettingOrdinaryIncome,
        carryForwardLoss=carryForwardLoss,
    )
