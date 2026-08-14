import pytest

from quantengine.optionsCorporateActionHandler import (
    ExDividendEarlyExerciseRiskResult,
    OptionContractSide,
    OptionExerciseStyle,
    OptionPosition,
    StockSplitAdjustmentResult,
    applyStockSplitAdjustmentToOptionPosition,
    evaluateEarlyExerciseRiskAroundExDividendDate,
)


def buildPosition(strike=100.0, quantity=10.0, style=OptionExerciseStyle.AMERICAN, side=OptionContractSide.CALL):
    return OptionPosition(symbol="DEMO-EQ", strikePrice=strike, quantity=quantity, exerciseStyle=style, contractSide=side)


class TestOptionPositionValidation:
    def testNonPositiveStrikeRaises(self):
        with pytest.raises(ValueError):
            buildPosition(strike=0.0)

    def testZeroQuantityRaises(self):
        with pytest.raises(ValueError):
            buildPosition(quantity=0.0)


class TestApplyStockSplitAdjustmentHandWorked:
    def testTwoForOneSplitHalvesStrikeDoublesQuantity(self):
        position = buildPosition(strike=100.0, quantity=10.0)
        result = applyStockSplitAdjustmentToOptionPosition(position, splitRatio=2.0)
        assert isinstance(result, StockSplitAdjustmentResult)
        # hand-worked: new strike = 100 / 2 = 50.0; new quantity = 10 * 2 = 20.0
        assert result.adjustedStrikePrice == pytest.approx(50.0)
        assert result.adjustedQuantity == pytest.approx(20.0)
        assert result.notionalExposureIsPreserved()

    def testThreeForTwoSplit(self):
        position = buildPosition(strike=90.0, quantity=4.0)
        result = applyStockSplitAdjustmentToOptionPosition(position, splitRatio=1.5)
        # hand-worked: new strike = 90 / 1.5 = 60.0; new quantity = 4 * 1.5 = 6.0
        assert result.adjustedStrikePrice == pytest.approx(60.0)
        assert result.adjustedQuantity == pytest.approx(6.0)

    def testReverseSplitOneForFour(self):
        position = buildPosition(strike=20.0, quantity=100.0)
        result = applyStockSplitAdjustmentToOptionPosition(position, splitRatio=0.25)
        # hand-worked: new strike = 20 / 0.25 = 80.0; new quantity = 100 * 0.25 = 25.0
        assert result.adjustedStrikePrice == pytest.approx(80.0)
        assert result.adjustedQuantity == pytest.approx(25.0)
        assert result.notionalExposureIsPreserved()

    def testNotionalExposureInvariantAcrossVariousRatios(self):
        position = buildPosition(strike=57.0, quantity=13.0)
        for ratio in (2.0, 3.0, 1.5, 0.5, 0.25, 4.0):
            result = applyStockSplitAdjustmentToOptionPosition(position, splitRatio=ratio)
            assert result.notionalExposureIsPreserved()

    def testRaisesOnNonPositiveSplitRatio(self):
        position = buildPosition()
        with pytest.raises(ValueError):
            applyStockSplitAdjustmentToOptionPosition(position, splitRatio=0.0)
        with pytest.raises(ValueError):
            applyStockSplitAdjustmentToOptionPosition(position, splitRatio=-2.0)

    def testSplitRatioOfOneIsAnNoOp(self):
        position = buildPosition(strike=42.0, quantity=7.0)
        result = applyStockSplitAdjustmentToOptionPosition(position, splitRatio=1.0)
        assert result.adjustedStrikePrice == pytest.approx(42.0)
        assert result.adjustedQuantity == pytest.approx(7.0)


class TestEvaluateEarlyExerciseRiskHandWorked:
    def testFlaggedWhenDividendExceedsTimeValue(self):
        # strike=100, spot=110 -> intrinsic = 10. callMarketPrice=10.5 ->
        # timeValue = 10.5 - 10 = 0.5. dividend=1.0 > 0.5 -> flagged.
        position = buildPosition(strike=100.0, quantity=1.0)
        result = evaluateEarlyExerciseRiskAroundExDividendDate(
            position, underlyingSpotPrice=110.0, callMarketPrice=10.5, dividendAmount=1.0
        )
        assert isinstance(result, ExDividendEarlyExerciseRiskResult)
        assert result.intrinsicValue == pytest.approx(10.0)
        assert result.callTimeValue == pytest.approx(0.5)
        assert result.isFlaggedForEarlyExerciseRisk is True

    def testNotFlaggedWhenDividendBelowTimeValue(self):
        # intrinsic = 10; callMarketPrice=13 -> timeValue = 3.0.
        # dividend=1.0 < 3.0 -> not flagged.
        position = buildPosition(strike=100.0, quantity=1.0)
        result = evaluateEarlyExerciseRiskAroundExDividendDate(
            position, underlyingSpotPrice=110.0, callMarketPrice=13.0, dividendAmount=1.0
        )
        assert result.callTimeValue == pytest.approx(3.0)
        assert result.isFlaggedForEarlyExerciseRisk is False

    def testExactBoundaryDividendEqualsTimeValueIsNotFlagged(self):
        # strict-greater-than convention: dividend == timeValue -> not flagged.
        position = buildPosition(strike=100.0, quantity=1.0)
        result = evaluateEarlyExerciseRiskAroundExDividendDate(
            position, underlyingSpotPrice=110.0, callMarketPrice=12.0, dividendAmount=2.0
        )
        assert result.callTimeValue == pytest.approx(2.0)
        assert result.isFlaggedForEarlyExerciseRisk is False

    def testEuropeanCallNeverFlagged(self):
        position = buildPosition(strike=100.0, quantity=1.0, style=OptionExerciseStyle.EUROPEAN)
        result = evaluateEarlyExerciseRiskAroundExDividendDate(
            position, underlyingSpotPrice=200.0, callMarketPrice=1.0, dividendAmount=1000.0
        )
        assert result.isFlaggedForEarlyExerciseRisk is False
        assert "EUROPEAN" in result.reason

    def testAmericanPutNeverFlaggedByThisCheck(self):
        position = buildPosition(strike=100.0, quantity=1.0, side=OptionContractSide.PUT)
        result = evaluateEarlyExerciseRiskAroundExDividendDate(
            position, underlyingSpotPrice=50.0, callMarketPrice=1.0, dividendAmount=1000.0
        )
        assert result.isFlaggedForEarlyExerciseRisk is False
        assert "PUT" in result.reason

    def testOutOfTheMoneyCallHasZeroIntrinsicValue(self):
        position = buildPosition(strike=100.0, quantity=1.0)
        result = evaluateEarlyExerciseRiskAroundExDividendDate(
            position, underlyingSpotPrice=90.0, callMarketPrice=2.0, dividendAmount=0.5
        )
        assert result.intrinsicValue == pytest.approx(0.0)
        # timeValue = 2.0 - 0 = 2.0; dividend 0.5 < 2.0 -> not flagged
        assert result.callTimeValue == pytest.approx(2.0)
        assert result.isFlaggedForEarlyExerciseRisk is False

    def testRaisesOnNonPositiveSpotPrice(self):
        position = buildPosition()
        with pytest.raises(ValueError):
            evaluateEarlyExerciseRiskAroundExDividendDate(position, underlyingSpotPrice=0.0, callMarketPrice=1.0, dividendAmount=0.1)

    def testRaisesOnNegativeCallMarketPrice(self):
        position = buildPosition()
        with pytest.raises(ValueError):
            evaluateEarlyExerciseRiskAroundExDividendDate(position, underlyingSpotPrice=100.0, callMarketPrice=-1.0, dividendAmount=0.1)

    def testRaisesOnNegativeDividendAmount(self):
        position = buildPosition()
        with pytest.raises(ValueError):
            evaluateEarlyExerciseRiskAroundExDividendDate(position, underlyingSpotPrice=100.0, callMarketPrice=1.0, dividendAmount=-0.1)

    def testZeroDividendNeverFlags(self):
        position = buildPosition(strike=100.0, quantity=1.0)
        result = evaluateEarlyExerciseRiskAroundExDividendDate(
            position, underlyingSpotPrice=110.0, callMarketPrice=10.0, dividendAmount=0.0
        )
        # timeValue = 10 - 10 = 0.0; dividend 0.0 > 0.0 is False.
        assert result.isFlaggedForEarlyExerciseRisk is False
