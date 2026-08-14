from datetime import date

import pytest

from quantengine.taxLossHarvestingAdvisor import (
    ANNUAL_ORDINARY_INCOME_OFFSET_CAP,
    TaxLot,
    buildTaxLossHarvestingPlan,
    evaluateLotForHarvesting,
    findWashSaleViolatingPurchases,
    isWithinWashSaleWindow,
)


def test_isWithinWashSaleWindow_exactBoundariesAreInclusive():
    saleDate = date(2026, 6, 15)
    assert isWithinWashSaleWindow(date(2026, 5, 16), saleDate) is True  # exactly 30 days before
    assert isWithinWashSaleWindow(date(2026, 7, 15), saleDate) is True  # exactly 30 days after
    assert isWithinWashSaleWindow(saleDate, saleDate) is True  # sale day itself


def test_isWithinWashSaleWindow_justOutsideBoundariesIsFalse():
    saleDate = date(2026, 6, 15)
    assert isWithinWashSaleWindow(date(2026, 5, 15), saleDate) is False  # 31 days before
    assert isWithinWashSaleWindow(date(2026, 7, 16), saleDate) is False  # 31 days after


def test_findWashSaleViolatingPurchases_rejectsMixedSymbols():
    lotToSell = TaxLot("L1", "SIM-AAPL", 10, 150.0, date(2026, 1, 1), 100.0)
    otherLot = TaxLot("L2", "SIM-MSFT", 10, 150.0, date(2026, 6, 1), 100.0)
    with pytest.raises(ValueError):
        findWashSaleViolatingPurchases(lotToSell, date(2026, 6, 15), [lotToSell, otherLot])


def test_evaluateLotForHarvesting_caseThatShouldBeExcluded_repurchaseWithinWindow():
    """A lot with an unrealized loss, where the SAME symbol was
    repurchased 10 days after the proposed sale date -> wash sale ->
    MUST be excluded."""
    lotToSell = TaxLot("L1", "SIM-AAPL", 10, 150.0, date(2025, 1, 1), 100.0)  # unrealized loss
    repurchaseLot = TaxLot("L2", "SIM-AAPL", 10, 105.0, date(2026, 6, 25), 100.0)
    proposedSaleDate = date(2026, 6, 15)

    evaluation = evaluateLotForHarvesting(lotToSell, proposedSaleDate, [lotToSell, repurchaseLot])

    assert lotToSell.unrealizedGainOrLoss < 0
    assert evaluation.isEligible is False
    assert evaluation.washSaleViolatingLotIds == ["L2"]


def test_evaluateLotForHarvesting_caseThatShouldNotBeExcluded_repurchaseOutsideWindow():
    """Same setup, but the repurchase happened 45 days after the sale —
    outside the 30-day window -> NOT a wash sale -> eligible."""
    lotToSell = TaxLot("L1", "SIM-AAPL", 10, 150.0, date(2025, 1, 1), 100.0)
    repurchaseLot = TaxLot("L2", "SIM-AAPL", 10, 105.0, date(2026, 7, 30), 100.0)  # 45 days after
    proposedSaleDate = date(2026, 6, 15)

    evaluation = evaluateLotForHarvesting(lotToSell, proposedSaleDate, [lotToSell, repurchaseLot])

    assert evaluation.isEligible is True
    assert evaluation.washSaleViolatingLotIds == []


def test_evaluateLotForHarvesting_lotWithGainIsNeverEligible():
    gainLot = TaxLot("L1", "SIM-AAPL", 10, 100.0, date(2025, 1, 1), 150.0)  # unrealized gain
    evaluation = evaluateLotForHarvesting(gainLot, date(2026, 6, 15), [gainLot])
    assert evaluation.isEligible is False
    assert evaluation.washSaleViolatingLotIds == []


def test_buildTaxLossHarvestingPlan_rejectsEmptyLots():
    with pytest.raises(ValueError):
        buildTaxLossHarvestingPlan([], 1000.0, date(2026, 6, 15))


def test_buildTaxLossHarvestingPlan_offsetsRealizedGainsFirstThenOrdinaryIncomeThenCarriesForward():
    saleDate = date(2026, 6, 15)
    lots = [
        # unrealized loss of 1000 (10 shares, bought at 200, now 100)
        TaxLot("L1", "SIM-AAPL", 10, 200.0, date(2024, 1, 1), 100.0),
        # unrealized loss of 5000 (50 shares, bought at 150, now 50)
        TaxLot("L2", "SIM-MSFT", 50, 150.0, date(2024, 1, 1), 50.0),
    ]
    plan = buildTaxLossHarvestingPlan(lots, realizedGainsYtd=2000.0, proposedSaleDate=saleDate)

    assert plan.totalHarvestableLoss == pytest.approx(6000.0)
    # Largest loss harvested first
    assert plan.eligibleLotsInHarvestOrder[0].lotId == "L2"
    assert plan.amountOffsettingRealizedGains == pytest.approx(2000.0)
    remainingAfterGains = 6000.0 - 2000.0  # 4000
    assert plan.amountOffsettingOrdinaryIncome == pytest.approx(ANNUAL_ORDINARY_INCOME_OFFSET_CAP)
    assert plan.carryForwardLoss == pytest.approx(remainingAfterGains - ANNUAL_ORDINARY_INCOME_OFFSET_CAP)


def test_buildTaxLossHarvestingPlan_smallLossFullyOffsetsGainsWithNoCarryforward():
    saleDate = date(2026, 6, 15)
    lots = [TaxLot("L1", "SIM-AAPL", 10, 110.0, date(2024, 1, 1), 100.0)]  # loss of 100
    plan = buildTaxLossHarvestingPlan(lots, realizedGainsYtd=5000.0, proposedSaleDate=saleDate)
    assert plan.totalHarvestableLoss == pytest.approx(100.0)
    assert plan.amountOffsettingRealizedGains == pytest.approx(100.0)
    assert plan.amountOffsettingOrdinaryIncome == pytest.approx(0.0)
    assert plan.carryForwardLoss == pytest.approx(0.0)


def test_buildTaxLossHarvestingPlan_excludesWashSaleLotsFromPlanButReportsThem():
    saleDate = date(2026, 6, 15)
    lossLot = TaxLot("L1", "SIM-AAPL", 10, 200.0, date(2024, 1, 1), 100.0)  # loss of 1000
    recentRepurchase = TaxLot("L2", "SIM-AAPL", 5, 105.0, date(2026, 6, 20), 110.0)  # inside window, itself a gain
    cleanLossLot = TaxLot("L3", "SIM-MSFT", 10, 150.0, date(2024, 1, 1), 100.0)  # loss of 500, clean

    plan = buildTaxLossHarvestingPlan(
        [lossLot, recentRepurchase, cleanLossLot], realizedGainsYtd=0.0, proposedSaleDate=saleDate
    )

    eligibleLotIds = {lot.lotId for lot in plan.eligibleLotsInHarvestOrder}
    assert "L1" not in eligibleLotIds  # excluded due to wash sale
    assert "L3" in eligibleLotIds  # clean loss, no wash sale conflict
    excludedLotIds = {evaluation.lot.lotId for evaluation in plan.excludedLotsDueToWashSale}
    assert "L1" in excludedLotIds
    assert plan.totalHarvestableLoss == pytest.approx(500.0)  # only L3's loss counted


def test_taxLot_rejectsNonPositiveQuantity():
    with pytest.raises(ValueError):
        TaxLot("L1", "SIM-AAPL", 0, 100.0, date(2024, 1, 1), 100.0)
