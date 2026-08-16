import math

import pytest

from quantengine.arbitrageScanner import (
    PriceDeviationAlert,
    calculateCashAndCarryForwardFairPrice,
    scanForTheoreticalVersusLivePriceDeviation,
    scanManyLivePricesForDeviationAlerts,
)


def test_cashAndCarryForwardFairPriceMatchesHandComputedValue():
    # F = S * e^(r*T) = 100 * e^(0.05*1) = 100 * 1.0512710963760241...
    #   = 105.12710963760241...
    fairPrice = calculateCashAndCarryForwardFairPrice(
        spotPrice=100.0, annualizedRiskFreeInterestRate=0.05, timeToDeliveryInYears=1.0
    )
    assert math.isclose(fairPrice, 105.12710963760241, rel_tol=1e-9)


def test_cashAndCarryForwardFairPriceEqualsSpotWhenRateOrTimeIsZero():
    assert calculateCashAndCarryForwardFairPrice(100.0, 0.0, 1.0) == pytest.approx(100.0)
    assert calculateCashAndCarryForwardFairPrice(100.0, 0.05, 0.0) == pytest.approx(100.0)


def test_deviationScanTriggersAlertWhenLivePriceIsRichBeyondThreshold():
    # theoretical=100, live=102 -> absoluteDeviation=2, percentageDeviation=2%
    # threshold=1% -> |2%| > 1% -> alert triggered, live is overpriced.
    alert = scanForTheoreticalVersusLivePriceDeviation(
        theoreticalFairPrice=100.0, liveMarketPrice=102.0, deviationThresholdPercentage=1.0
    )
    assert isinstance(alert, PriceDeviationAlert)
    assert alert.absoluteDeviation == pytest.approx(2.0)
    assert alert.percentageDeviation == pytest.approx(2.0)
    assert alert.isAlertTriggered is True
    assert alert.isLiveOverpricedRelativeToTheoretical is True


def test_deviationScanTriggersAlertWhenLivePriceIsCheapBeyondThreshold():
    # theoretical=100, live=96 -> absoluteDeviation=-4, percentageDeviation=-4%
    # threshold=1% -> |-4%| > 1% -> alert triggered, live is underpriced.
    alert = scanForTheoreticalVersusLivePriceDeviation(
        theoreticalFairPrice=100.0, liveMarketPrice=96.0, deviationThresholdPercentage=1.0
    )
    assert alert.absoluteDeviation == pytest.approx(-4.0)
    assert alert.percentageDeviation == pytest.approx(-4.0)
    assert alert.isAlertTriggered is True
    assert alert.isLiveOverpricedRelativeToTheoretical is False


def test_deviationScanDoesNotTriggerWhenWithinThreshold():
    # theoretical=100, live=100.5 -> percentageDeviation=0.5%, threshold=1% -> no alert.
    alert = scanForTheoreticalVersusLivePriceDeviation(
        theoreticalFairPrice=100.0, liveMarketPrice=100.5, deviationThresholdPercentage=1.0
    )
    assert alert.isAlertTriggered is False


def test_deviationScanIsExactlyAtThresholdDoesNotTrigger():
    # Deviation exactly equal to the threshold uses strict ">" so it does NOT trigger.
    alert = scanForTheoreticalVersusLivePriceDeviation(
        theoreticalFairPrice=100.0, liveMarketPrice=101.0, deviationThresholdPercentage=1.0
    )
    assert alert.percentageDeviation == pytest.approx(1.0)
    assert alert.isAlertTriggered is False


def test_deviationScanRaisesOnNonPositiveTheoreticalPrice():
    with pytest.raises(ValueError):
        scanForTheoreticalVersusLivePriceDeviation(
            theoreticalFairPrice=0.0, liveMarketPrice=10.0, deviationThresholdPercentage=1.0
        )
    with pytest.raises(ValueError):
        scanForTheoreticalVersusLivePriceDeviation(
            theoreticalFairPrice=-5.0, liveMarketPrice=10.0, deviationThresholdPercentage=1.0
        )


def test_deviationScanRaisesOnNegativeThreshold():
    with pytest.raises(ValueError):
        scanForTheoreticalVersusLivePriceDeviation(
            theoreticalFairPrice=100.0, liveMarketPrice=100.0, deviationThresholdPercentage=-1.0
        )


def test_batchScanOnlyScansSymbolsPresentInBothPriceMaps():
    theoreticalPrices = {"AAPL": 100.0, "MSFT": 200.0, "ONLY_THEORETICAL": 50.0}
    livePrices = {"AAPL": 103.0, "MSFT": 199.0, "ONLY_LIVE": 75.0}

    alerts = scanManyLivePricesForDeviationAlerts(theoreticalPrices, livePrices, deviationThresholdPercentage=1.0)

    assert set(alerts.keys()) == {"AAPL", "MSFT"}
    assert alerts["AAPL"].isAlertTriggered is True  # 3% > 1%
    assert alerts["MSFT"].isAlertTriggered is False  # 0.5% < 1%


def test_scanManyLivePricesForDeviationAlerts_skipsSymbolWithNonPositiveTheoreticalPriceInsteadOfAbortingBatch():
    # One symbol ("BAD") has a non-positive theoreticalFairPrice, which
    # scanForTheoreticalVersusLivePriceDeviation raises ValueError for.
    # Per the module's own documented intent ("a stale/missing live quote
    # for one symbol shouldn't fail the whole scan"), a bad PRICE for one
    # symbol should be skipped too, not lose every already-computed alert
    # for the batch.
    theoreticalFairPricesBySymbol = {"GOOD": 100.0, "BAD": 0.0, "ALSO_GOOD": 50.0}
    liveMarketPricesBySymbol = {"GOOD": 102.0, "BAD": 5.0, "ALSO_GOOD": 51.0}

    alertsBySymbol = scanManyLivePricesForDeviationAlerts(
        theoreticalFairPricesBySymbol, liveMarketPricesBySymbol, deviationThresholdPercentage=1.0
    )

    assert "BAD" not in alertsBySymbol
    assert "GOOD" in alertsBySymbol
    assert "ALSO_GOOD" in alertsBySymbol
    assert alertsBySymbol["GOOD"].isAlertTriggered is True
