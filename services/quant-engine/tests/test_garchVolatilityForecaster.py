import math

import pytest

from quantengine.garchVolatilityForecaster import (
    ExpectedIntradayRangeResult,
    GarchOneOneParameters,
    calculateExpectedIntradayRangeFromForecastVariance,
    calculateGarchConditionalVarianceSeries,
    calculateGaussianConditionalLogLikelihood,
    calculateMeanOfSeries,
    calculateSampleVarianceOfSeries,
    fitGarchOneOneParametersByGridSearchQuasiMaximumLikelihood,
)

# --- Hand-worked recursion case ----------------------------------------
# omega=0.00002, alpha=0.10, beta=0.85, seed sigma^2_0 = 0.0004
# demeanedReturns = [0.01, -0.02, 0.03, -0.01, 0.02]
#
# sigma^2_1 = 0.00002 + 0.10*0.01^2   + 0.85*0.0004      = 0.00037
# sigma^2_2 = 0.00002 + 0.10*(-0.02)^2 + 0.85*0.00037     = 0.0003745
# sigma^2_3 = 0.00002 + 0.10*0.03^2   + 0.85*0.0003745    = 0.000428325
# sigma^2_4 = 0.00002 + 0.10*(-0.01)^2 + 0.85*0.000428325 = 0.00039407625
# sigma^2_5 = 0.00002 + 0.10*0.02^2   + 0.85*0.00039407625 = 0.0003949648125
HAND_WORKED_OMEGA = 0.00002
HAND_WORKED_ALPHA = 0.10
HAND_WORKED_BETA = 0.85
HAND_WORKED_SEED_VARIANCE = 0.0004
HAND_WORKED_DEMEANED_RETURNS = [0.01, -0.02, 0.03, -0.01, 0.02]
HAND_WORKED_EXPECTED_SERIES = [
    0.0004,
    0.00037000000000000005,
    0.00037450000000000005,
    0.000428325,
    0.00039407625,
    0.0003949648125,
]


def buildHandWorkedParameters() -> GarchOneOneParameters:
    return GarchOneOneParameters(
        omega=HAND_WORKED_OMEGA,
        alphaArchCoefficient=HAND_WORKED_ALPHA,
        betaGarchCoefficient=HAND_WORKED_BETA,
    )


def test_conditionalVarianceRecursionMatchesHandWorkedSeriesExactly():
    series = calculateGarchConditionalVarianceSeries(
        HAND_WORKED_DEMEANED_RETURNS, buildHandWorkedParameters(), HAND_WORKED_SEED_VARIANCE
    )
    assert len(series) == len(HAND_WORKED_DEMEANED_RETURNS) + 1
    for actual, expected in zip(series, HAND_WORKED_EXPECTED_SERIES):
        assert math.isclose(actual, expected, rel_tol=1e-12)


def test_conditionalVarianceRecursionFirstThreeStepsMatchHandArithmetic():
    series = calculateGarchConditionalVarianceSeries(
        HAND_WORKED_DEMEANED_RETURNS[:3], buildHandWorkedParameters(), HAND_WORKED_SEED_VARIANCE
    )
    assert math.isclose(series[0], 0.0004, rel_tol=1e-12)
    assert math.isclose(series[1], 0.00037, rel_tol=1e-12)
    assert math.isclose(series[2], 0.0003745, rel_tol=1e-12)
    assert math.isclose(series[3], 0.000428325, rel_tol=1e-12)


def test_lastElementOfConditionalVarianceSeriesIsTheNextPeriodForecast():
    series = calculateGarchConditionalVarianceSeries(
        HAND_WORKED_DEMEANED_RETURNS, buildHandWorkedParameters(), HAND_WORKED_SEED_VARIANCE
    )
    assert math.isclose(series[-1], HAND_WORKED_EXPECTED_SERIES[-1], rel_tol=1e-12)


def test_conditionalVarianceRecursionRaisesOnEmptyReturns():
    with pytest.raises(ValueError):
        calculateGarchConditionalVarianceSeries([], buildHandWorkedParameters(), HAND_WORKED_SEED_VARIANCE)


def test_conditionalVarianceRecursionRaisesOnNonPositiveSeedVariance():
    with pytest.raises(ValueError):
        calculateGarchConditionalVarianceSeries(HAND_WORKED_DEMEANED_RETURNS, buildHandWorkedParameters(), 0.0)


def test_garchOneOneParametersRejectsNonStationaryCoefficients():
    with pytest.raises(ValueError):
        GarchOneOneParameters(omega=0.0001, alphaArchCoefficient=0.6, betaGarchCoefficient=0.5)


def test_garchOneOneParametersRejectsNonPositiveOmega():
    with pytest.raises(ValueError):
        GarchOneOneParameters(omega=0.0, alphaArchCoefficient=0.1, betaGarchCoefficient=0.8)


def test_garchOneOneParametersRejectsNegativeCoefficients():
    with pytest.raises(ValueError):
        GarchOneOneParameters(omega=0.0001, alphaArchCoefficient=-0.1, betaGarchCoefficient=0.8)


def test_unconditionalVarianceMatchesClosedFormFormula():
    parameters = GarchOneOneParameters(omega=0.0002, alphaArchCoefficient=0.1, betaGarchCoefficient=0.8)
    # omega / (1 - alpha - beta) = 0.0002 / 0.1 = 0.002
    assert math.isclose(parameters.calculateUnconditionalVariance(), 0.002, rel_tol=1e-12)


def test_meanAndVarianceOfSeriesHelpers():
    values = [0.01, -0.02, 0.03, -0.01, 0.02]
    assert math.isclose(calculateMeanOfSeries(values), 0.006, rel_tol=1e-12)
    # population variance
    mean = 0.006
    expectedVariance = sum((v - mean) ** 2 for v in values) / len(values)
    assert math.isclose(calculateSampleVarianceOfSeries(values), expectedVariance, rel_tol=1e-12)


def test_meanAndVarianceHelpersRaiseOnEmptySeries():
    with pytest.raises(ValueError):
        calculateMeanOfSeries([])
    with pytest.raises(ValueError):
        calculateSampleVarianceOfSeries([])


def test_gaussianLogLikelihoodRaisesOnLengthMismatch():
    with pytest.raises(ValueError):
        calculateGaussianConditionalLogLikelihood([0.01, 0.02], [0.0004, 0.0005])


def test_gaussianLogLikelihoodIsFiniteRealNumberForHandWorkedSeries():
    logLikelihood = calculateGaussianConditionalLogLikelihood(
        HAND_WORKED_DEMEANED_RETURNS, HAND_WORKED_EXPECTED_SERIES
    )
    assert math.isfinite(logLikelihood)


# --- Fit + forecast integration ----------------------------------------

DETERMINISTIC_FIXTURE_RETURNS = [
    0.01, -0.015, 0.02, -0.01, 0.005, -0.02, 0.015, -0.005, 0.01, -0.01,
    0.025, -0.02, 0.01, -0.015, 0.02, -0.01, 0.005, -0.025, 0.015, -0.01,
]


def test_fitGarchRaisesOnTooFewObservations():
    with pytest.raises(ValueError):
        fitGarchOneOneParametersByGridSearchQuasiMaximumLikelihood([0.01, 0.02])


def test_fitGarchRaisesOnZeroVarianceSeries():
    with pytest.raises(ValueError):
        fitGarchOneOneParametersByGridSearchQuasiMaximumLikelihood([0.01, 0.01, 0.01, 0.01])


def test_fitGarchProducesStationaryParametersWithinSearchGrid():
    fitResult = fitGarchOneOneParametersByGridSearchQuasiMaximumLikelihood(DETERMINISTIC_FIXTURE_RETURNS)
    parameters = fitResult.parameters
    assert parameters.omega > 0
    assert 0 <= parameters.alphaArchCoefficient < 1
    assert 0 <= parameters.betaGarchCoefficient < 1
    assert parameters.alphaArchCoefficient + parameters.betaGarchCoefficient < 1
    assert math.isfinite(fitResult.logLikelihoodAtFittedParameters)
    assert len(fitResult.conditionalVarianceSeries) == len(DETERMINISTIC_FIXTURE_RETURNS) + 1


def test_fitGarchIsDeterministicAcrossRepeatedCalls():
    firstFit = fitGarchOneOneParametersByGridSearchQuasiMaximumLikelihood(DETERMINISTIC_FIXTURE_RETURNS)
    secondFit = fitGarchOneOneParametersByGridSearchQuasiMaximumLikelihood(DETERMINISTIC_FIXTURE_RETURNS)
    assert firstFit.parameters == secondFit.parameters
    assert firstFit.logLikelihoodAtFittedParameters == secondFit.logLikelihoodAtFittedParameters


def test_fitGarchFoundParametersBeatEveryOtherCandidateInTheSearchGrid():
    fitResult = fitGarchOneOneParametersByGridSearchQuasiMaximumLikelihood(DETERMINISTIC_FIXTURE_RETURNS)
    sampleMean = calculateMeanOfSeries(DETERMINISTIC_FIXTURE_RETURNS)
    demeaned = [r - sampleMean for r in DETERMINISTIC_FIXTURE_RETURNS]
    sampleVariance = calculateSampleVarianceOfSeries(DETERMINISTIC_FIXTURE_RETURNS)

    alphaSearchGrid = [0.02 * stepIndex for stepIndex in range(1, 15)]
    betaSearchGrid = [0.50 + 0.05 * stepIndex for stepIndex in range(0, 10)]

    for alphaCandidate in alphaSearchGrid:
        for betaCandidate in betaSearchGrid:
            if alphaCandidate + betaCandidate >= 0.999:
                continue
            candidateParameters = GarchOneOneParameters(
                omega=sampleVariance * (1 - alphaCandidate - betaCandidate),
                alphaArchCoefficient=alphaCandidate,
                betaGarchCoefficient=betaCandidate,
            )
            candidateSeries = calculateGarchConditionalVarianceSeries(demeaned, candidateParameters, sampleVariance)
            candidateLogLikelihood = calculateGaussianConditionalLogLikelihood(demeaned, candidateSeries)
            assert fitResult.logLikelihoodAtFittedParameters >= candidateLogLikelihood - 1e-9


# --- Expected Intraday Range widget -------------------------------------


def test_expectedIntradayRangeHandWorkedExample():
    # forecastVariance = 0.0004 -> forecastVolatility = 0.02
    # currentPrice = 100, zScoreMultiple = 1.645
    # halfRange = 1.645 * 0.02 * 100 = 3.29
    result = calculateExpectedIntradayRangeFromForecastVariance(
        forecastNextPeriodVariance=0.0004, currentPrice=100.0, zScoreMultiple=1.645
    )
    assert isinstance(result, ExpectedIntradayRangeResult)
    assert math.isclose(result.forecastNextPeriodVolatility, 0.02, rel_tol=1e-12)
    assert math.isclose(result.expectedRangeLowerBound, 96.71, rel_tol=1e-9)
    assert math.isclose(result.expectedRangeUpperBound, 103.29, rel_tol=1e-9)


def test_expectedIntradayRangeUsesDefaultZScoreWhenOmitted():
    result = calculateExpectedIntradayRangeFromForecastVariance(0.0001, 50.0)
    assert result.zScoreMultiple == 1.645


def test_expectedIntradayRangeRaisesOnNonPositiveInputs():
    with pytest.raises(ValueError):
        calculateExpectedIntradayRangeFromForecastVariance(0.0, 100.0)
    with pytest.raises(ValueError):
        calculateExpectedIntradayRangeFromForecastVariance(0.0001, 0.0)
    with pytest.raises(ValueError):
        calculateExpectedIntradayRangeFromForecastVariance(0.0001, 100.0, zScoreMultiple=0.0)


def test_fitThenForecastEndToEndProducesAPlausibleExpectedRange():
    fitResult = fitGarchOneOneParametersByGridSearchQuasiMaximumLikelihood(DETERMINISTIC_FIXTURE_RETURNS)
    forecastVariance = fitResult.conditionalVarianceSeries[-1]
    rangeResult = calculateExpectedIntradayRangeFromForecastVariance(forecastVariance, currentPrice=100.0)
    assert rangeResult.expectedRangeLowerBound < 100.0 < rangeResult.expectedRangeUpperBound
