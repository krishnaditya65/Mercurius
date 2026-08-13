import math

import pytest

from quantengine.riskStatistics import (
    MaximumDrawdownResult,
    buildCumulativeEquityCurveFromReturns,
    calculateAnnualizedSharpeRatio,
    calculateAnnualizedSortinoRatio,
    calculateDownsideDeviationOfReturnSeries,
    calculateMaximumDrawdownFromEquityCurve,
    calculateMaximumDrawdownFromReturns,
    calculateMeanOfReturnSeries,
    calculatePopulationStandardDeviationOfReturnSeries,
)

# --- Hand-worked reference series -------------------------------------
# periodicReturns = [0.04, -0.02, 0.04, -0.02, 0.04, -0.02]  (6 periods)
#
# mean = (0.04*3 + -0.02*3) / 6 = (0.12 - 0.06) / 6 = 0.06 / 6 = 0.01
#
# deviations from mean: 0.04-0.01=0.03 (x3), -0.02-0.01=-0.03 (x3)
# squared deviations: 0.0009 (x6) -> sum = 0.0054
# population variance = 0.0054 / 6 = 0.0009
# population stddev = sqrt(0.0009) = 0.03  (exact)
#
# With periodicRiskFreeRate = 0.0 and periodsPerYear = 252:
#   meanExcessReturn = 0.01 - 0.0 = 0.01
#   sqrt(252) = 2 * sqrt(63) = 15.874507866387544...
#   Sharpe = (0.01 / 0.03) * 15.874507866387544
#          = 0.3333333333... * 15.874507866387544
#          = 5.291502622129...
#
# Downside deviation (threshold = 0.0, same as risk-free rate):
#   0.04 periods: min(0, 0.04-0) = 0            -> squared = 0
#   -0.02 periods: min(0, -0.02-0) = -0.02       -> squared = 0.0004
#   sum of squared downside deviations = 3 * 0.0004 = 0.0012
#   downside variance = 0.0012 / 6 = 0.0002
#   downside deviation = sqrt(0.0002) = 0.014142135623730951 (= 1/(50*sqrt(2)))
#
#   Sortino = (0.01 / 0.014142135623730951) * 15.874507866387544
#           = 0.7071067811865476 * 15.874507866387544   (0.01/downsideDev = 1/sqrt(2))
#           = 11.224972160321824...
HAND_WORKED_RETURN_SERIES = [0.04, -0.02, 0.04, -0.02, 0.04, -0.02]
HAND_WORKED_EXPECTED_MEAN = 0.01
HAND_WORKED_EXPECTED_POPULATION_STDDEV = 0.03
HAND_WORKED_EXPECTED_DOWNSIDE_DEVIATION = 0.014142135623730951
HAND_WORKED_EXPECTED_SHARPE_RATIO = 5.291502622129181
HAND_WORKED_EXPECTED_SORTINO_RATIO = 11.224972160321824


def test_meanOfHandWorkedReturnSeriesMatchesHandComputedValue():
    assert math.isclose(
        calculateMeanOfReturnSeries(HAND_WORKED_RETURN_SERIES), HAND_WORKED_EXPECTED_MEAN, rel_tol=1e-12
    )


def test_populationStandardDeviationOfHandWorkedReturnSeriesMatchesHandComputedValue():
    assert math.isclose(
        calculatePopulationStandardDeviationOfReturnSeries(HAND_WORKED_RETURN_SERIES),
        HAND_WORKED_EXPECTED_POPULATION_STDDEV,
        rel_tol=1e-9,
    )


def test_downsideDeviationOfHandWorkedReturnSeriesMatchesHandComputedValue():
    downsideDeviation = calculateDownsideDeviationOfReturnSeries(
        HAND_WORKED_RETURN_SERIES, minimumAcceptableReturnPerPeriod=0.0
    )
    assert math.isclose(downsideDeviation, HAND_WORKED_EXPECTED_DOWNSIDE_DEVIATION, rel_tol=1e-9)


def test_annualizedSharpeRatioOfHandWorkedReturnSeriesMatchesHandComputedValue():
    sharpeRatio = calculateAnnualizedSharpeRatio(
        HAND_WORKED_RETURN_SERIES, periodicRiskFreeRate=0.0, periodsPerYear=252
    )
    assert math.isclose(sharpeRatio, HAND_WORKED_EXPECTED_SHARPE_RATIO, rel_tol=1e-9)


def test_annualizedSortinoRatioOfHandWorkedReturnSeriesMatchesHandComputedValue():
    sortinoRatio = calculateAnnualizedSortinoRatio(
        HAND_WORKED_RETURN_SERIES, periodicRiskFreeRate=0.0, periodsPerYear=252
    )
    assert math.isclose(sortinoRatio, HAND_WORKED_EXPECTED_SORTINO_RATIO, rel_tol=1e-9)


def test_sortinoRatioDefaultsMinimumAcceptableReturnToRiskFreeRateWhenOmitted():
    # Explicitly passing minimumAcceptableReturnPerPeriod=periodicRiskFreeRate
    # must give the exact same result as omitting it.
    withExplicitThreshold = calculateAnnualizedSortinoRatio(
        HAND_WORKED_RETURN_SERIES,
        periodicRiskFreeRate=0.0,
        periodsPerYear=252,
        minimumAcceptableReturnPerPeriod=0.0,
    )
    withDefaultThreshold = calculateAnnualizedSortinoRatio(
        HAND_WORKED_RETURN_SERIES, periodicRiskFreeRate=0.0, periodsPerYear=252
    )
    assert withExplicitThreshold == withDefaultThreshold


def test_sharpeRatioRaisesOnZeroVarianceReturnSeries():
    with pytest.raises(ValueError):
        calculateAnnualizedSharpeRatio([0.01, 0.01, 0.01], periodicRiskFreeRate=0.0, periodsPerYear=252)


def test_sortinoRatioRaisesWhenNoDownsidePeriodsExist():
    # All returns strictly above the threshold -> downside deviation is 0.
    with pytest.raises(ValueError):
        calculateAnnualizedSortinoRatio(
            [0.01, 0.02, 0.03], periodicRiskFreeRate=0.0, periodsPerYear=252
        )


def test_meanAndStddevRaiseOnEmptyReturnSeries():
    with pytest.raises(ValueError):
        calculateMeanOfReturnSeries([])
    with pytest.raises(ValueError):
        calculatePopulationStandardDeviationOfReturnSeries([])


# --- Hand-worked max-drawdown case -------------------------------------
# equityCurveValues = [100, 120, 90, 150, 60, 130]
#
# index 0: 100 -> runningPeak=100, drawdown=0
# index 1: 120 -> new peak, runningPeak=120, drawdown=0
# index 2: 90  -> drawdown = (120-90)/120 = 30/120 = 0.25
# index 3: 150 -> new peak, runningPeak=150, drawdown=0
# index 4: 60  -> drawdown = (150-60)/150 = 90/150 = 0.6   <- largest
# index 5: 130 -> drawdown = (150-130)/150 = 20/150 = 0.1333...
#
# Maximum drawdown = 0.6 (60%), peak=150 at index 3, trough=60 at index 4.
HAND_WORKED_EQUITY_CURVE = [100, 120, 90, 150, 60, 130]
HAND_WORKED_EXPECTED_MAX_DRAWDOWN_FRACTION = 0.6
HAND_WORKED_EXPECTED_PEAK_VALUE = 150
HAND_WORKED_EXPECTED_TROUGH_VALUE = 60
HAND_WORKED_EXPECTED_PEAK_INDEX = 3
HAND_WORKED_EXPECTED_TROUGH_INDEX = 4


def test_maximumDrawdownOfHandWorkedEquityCurveMatchesHandComputedValue():
    result = calculateMaximumDrawdownFromEquityCurve(HAND_WORKED_EQUITY_CURVE)
    assert isinstance(result, MaximumDrawdownResult)
    assert math.isclose(result.maximumDrawdownFraction, HAND_WORKED_EXPECTED_MAX_DRAWDOWN_FRACTION, rel_tol=1e-12)
    assert result.peakEquityValue == HAND_WORKED_EXPECTED_PEAK_VALUE
    assert result.troughEquityValue == HAND_WORKED_EXPECTED_TROUGH_VALUE
    assert result.peakIndex == HAND_WORKED_EXPECTED_PEAK_INDEX
    assert result.troughIndex == HAND_WORKED_EXPECTED_TROUGH_INDEX


def test_maximumDrawdownIsZeroForAMonotonicallyIncreasingEquityCurve():
    result = calculateMaximumDrawdownFromEquityCurve([100, 110, 120, 130])
    assert result.maximumDrawdownFraction == 0.0


def test_maximumDrawdownRaisesOnEmptyEquityCurve():
    with pytest.raises(ValueError):
        calculateMaximumDrawdownFromEquityCurve([])


def test_buildCumulativeEquityCurveFromReturnsCompoundsCorrectly():
    # startingEquityValue=100, returns [0.10, -0.10] ->
    # 100 -> 110 -> 110*0.9 = 99
    equityCurve = buildCumulativeEquityCurveFromReturns([0.10, -0.10], startingEquityValue=100.0)
    assert equityCurve == pytest.approx([100.0, 110.0, 99.0])


def test_maximumDrawdownFromReturnsMatchesManualEquityCurveConstruction():
    returns = [0.20, -0.25, 0.6666666666666667, -0.6, 1.1666666666666667]
    # Starting from 100 these returns reconstruct exactly the hand-worked
    # equity curve above: 100 -> 120 -> 90 -> 150 -> 60 -> 130.
    resultFromReturns = calculateMaximumDrawdownFromReturns(returns, startingEquityValue=100.0)
    resultFromCurve = calculateMaximumDrawdownFromEquityCurve(HAND_WORKED_EQUITY_CURVE)
    assert math.isclose(
        resultFromReturns.maximumDrawdownFraction, resultFromCurve.maximumDrawdownFraction, rel_tol=1e-9
    )
