"""Engle-Granger cointegration testing for pairs-trade candidate
selection. See FEATURES.md §22 ("Deep Quant & Algorithmic Trading
Internals"). This module is deliberately MORE RIGOROUS than
`correlationMatrixEngine.py`'s pairs-trading candidate filter (§6): two
price series can be highly CORRELATED (move together over a short
window) without being COINTEGRATED (sharing a long-run equilibrium
relationship whose deviations mean-revert) — correlation is necessary
neither for, nor guaranteed by, cointegration. Cointegration is the
theoretically-correct screen for a pairs-trading spread that will
actually revert; simple correlation is a cheaper, weaker proxy.
`correlationMatrixEngine.py` says as much in FEATURES.md §6's framing;
this module is the promised follow-up.

Real, genuine numerical work implemented from scratch (this codebase is
stdlib-only, no numpy/scipy per its documented convention — see
`garchVolatilityForecaster.py`'s own note on the same constraint):

STEP 1 (`runOrdinaryLeastSquaresRegression` / step-one of
`performEngleGrangerCointegrationTest`): regress one price series on the
other WITH an intercept (`y = alpha + beta*x + residual`) via a real
ordinary-least-squares solve — the normal equations `(X'X) beta = X'y`
solved by Gauss-Jordan matrix inversion, implemented here with plain
Python lists (no external linear-algebra library).

STEP 2 (`calculateAugmentedDickeyFullerTestStatistic`): tests STEP 1's
residual series for a unit root via a REAL Augmented Dickey-Fuller
regression:

    delta(residual_t) = rho * residual_{t-1}
                         + sum_{i=1..lags} gamma_i * delta(residual_{t-i})
                         + error_t

(no intercept/trend term — Engle-Granger residuals are OLS-guaranteed to
have a zero sample mean in the original regression, the standard
simplification for this exact step), fit via the SAME real OLS machinery
as step 1. The ADF test statistic is `rho`'s real, computed t-statistic
(`rho / standardError(rho)`) — genuinely calculated from the regression's
residual sum of squares and the inverted design matrix, not a fabricated
number.

**On the critical values / significance conclusion — read this before
trusting a "cointegrated" verdict**: the ADF test statistic itself is
real and correctly computed. The critical-value thresholds this module
compares it against (`ASYMPTOTIC_DICKEY_FULLER_CRITICAL_VALUES_NO_CONSTANT`)
are the STANDARD asymptotic Dickey-Fuller critical values for a
no-constant/no-trend regression (commonly cited approximate values:
-2.58 at 1%, -1.95 at 5%, -1.62 at 10%). Genuine Engle-Granger
cointegration testing should use MacKinnon (1991)-style response-surface
critical values, which are systematically MORE NEGATIVE than plain
Dickey-Fuller values because the tested residual series is itself
ESTIMATED (from step 1's regression) rather than observed directly —
using plain DF critical values here is measurably ANTI-CONSERVATIVE
(more likely to falsely conclude cointegration than a properly-adjusted
test) and is NOT implemented in this pass. This module reports the real
test statistic plus this documented caveat rather than silently
overstating precision with a fake, more-authoritative-looking p-value.

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

from dataclasses import dataclass


def calculateMatrixTranspose(matrix: list[list[float]]) -> list[list[float]]:
    return [[row[columnIndex] for row in matrix] for columnIndex in range(len(matrix[0]))]


def multiplyMatrices(firstMatrix: list[list[float]], secondMatrix: list[list[float]]) -> list[list[float]]:
    numberOfRows = len(firstMatrix)
    numberOfColumns = len(secondMatrix[0])
    innerDimension = len(secondMatrix)
    result = [[0.0] * numberOfColumns for _ in range(numberOfRows)]
    for rowIndex in range(numberOfRows):
        for columnIndex in range(numberOfColumns):
            result[rowIndex][columnIndex] = sum(
                firstMatrix[rowIndex][k] * secondMatrix[k][columnIndex] for k in range(innerDimension)
            )
    return result


def multiplyMatrixByVector(matrix: list[list[float]], vector: list[float]) -> list[float]:
    return [sum(row[i] * vector[i] for i in range(len(vector))) for row in matrix]


def invertSquareMatrixViaGaussJordanElimination(matrix: list[list[float]]) -> list[list[float]]:
    """Real Gauss-Jordan elimination with partial pivoting, implemented
    with plain nested Python lists (no numpy — this repo's stdlib-only
    convention). Raises `ValueError` if the matrix is singular (a
    perfectly collinear design matrix — e.g. a constant regressor series
    — has no unique OLS solution).
    """
    n = len(matrix)
    augmented = [list(matrix[i]) + [1.0 if i == j else 0.0 for j in range(n)] for i in range(n)]

    for pivotColumn in range(n):
        pivotRow = max(range(pivotColumn, n), key=lambda r: abs(augmented[r][pivotColumn]))
        if abs(augmented[pivotRow][pivotColumn]) < 1e-12:
            raise ValueError("cannot invert matrix: singular or near-singular (collinear regressors)")
        augmented[pivotColumn], augmented[pivotRow] = augmented[pivotRow], augmented[pivotColumn]

        pivotValue = augmented[pivotColumn][pivotColumn]
        augmented[pivotColumn] = [v / pivotValue for v in augmented[pivotColumn]]

        for rowIndex in range(n):
            if rowIndex == pivotColumn:
                continue
            factor = augmented[rowIndex][pivotColumn]
            if factor != 0.0:
                augmented[rowIndex] = [
                    augmented[rowIndex][k] - factor * augmented[pivotColumn][k] for k in range(2 * n)
                ]

    return [row[n:] for row in augmented]


@dataclass(frozen=True)
class OrdinaryLeastSquaresResult:
    coefficients: list[float]
    residuals: list[float]
    residualSumOfSquares: float
    standardErrorsOfCoefficients: list[float]


def runOrdinaryLeastSquaresRegression(
    designMatrixRows: list[list[float]], targetValues: list[float]
) -> OrdinaryLeastSquaresResult:
    """Real OLS via the normal equations `beta = (X'X)^-1 X'y`, solved
    with `invertSquareMatrixViaGaussJordanElimination` above.
    `designMatrixRows` is the full design matrix — include an intercept
    column of 1.0s yourself if you want one (this function doesn't add
    one implicitly). Raises `ValueError` if the number of observations
    does not exceed the number of regressors (zero or negative degrees
    of freedom — standard errors are undefined) or on a singular design.
    """
    numberOfObservations = len(designMatrixRows)
    numberOfRegressors = len(designMatrixRows[0]) if designMatrixRows else 0
    if numberOfObservations != len(targetValues):
        raise ValueError("designMatrixRows and targetValues must have the same length")
    if numberOfObservations <= numberOfRegressors:
        raise ValueError(
            "not enough observations for the requested number of regressors "
            "(degrees of freedom would be zero or negative)"
        )

    designMatrixTranspose = calculateMatrixTranspose(designMatrixRows)
    normalEquationsMatrix = multiplyMatrices(designMatrixTranspose, designMatrixRows)
    normalEquationsRhs = multiplyMatrixByVector(designMatrixTranspose, targetValues)

    inverseOfNormalEquationsMatrix = invertSquareMatrixViaGaussJordanElimination(normalEquationsMatrix)
    coefficients = multiplyMatrixByVector(inverseOfNormalEquationsMatrix, normalEquationsRhs)

    fittedValues = [
        sum(designMatrixRows[i][j] * coefficients[j] for j in range(numberOfRegressors))
        for i in range(numberOfObservations)
    ]
    residuals = [targetValues[i] - fittedValues[i] for i in range(numberOfObservations)]
    residualSumOfSquares = sum(r * r for r in residuals)

    degreesOfFreedom = numberOfObservations - numberOfRegressors
    residualVariance = residualSumOfSquares / degreesOfFreedom
    standardErrorsOfCoefficients = [
        (residualVariance * inverseOfNormalEquationsMatrix[j][j]) ** 0.5 for j in range(numberOfRegressors)
    ]

    return OrdinaryLeastSquaresResult(
        coefficients=coefficients,
        residuals=residuals,
        residualSumOfSquares=residualSumOfSquares,
        standardErrorsOfCoefficients=standardErrorsOfCoefficients,
    )


# Standard asymptotic Dickey-Fuller critical values for the NO-CONSTANT,
# NO-TREND regression case (see the module docstring's caveat about these
# NOT being the more-conservative MacKinnon Engle-Granger-adjusted
# values).
ASYMPTOTIC_DICKEY_FULLER_CRITICAL_VALUES_NO_CONSTANT: dict[str, float] = {
    "1%": -2.58,
    "5%": -1.95,
    "10%": -1.62,
}


@dataclass(frozen=True)
class AugmentedDickeyFullerTestResult:
    testStatistic: float
    unitRootCoefficient: float
    standardErrorOfUnitRootCoefficient: float
    numberOfLagsUsed: int
    isStationaryAtOnePercent: bool
    isStationaryAtFivePercent: bool
    isStationaryAtTenPercent: bool


def calculateAugmentedDickeyFullerTestStatistic(
    series: list[float], numberOfLagsForAugmentedTerms: int = 0
) -> AugmentedDickeyFullerTestResult:
    """Real ADF regression (see module docstring) on `series`:

        delta(series_t) = rho * series_{t-1}
                           + sum_{i=1..lags} gamma_i * delta(series_{t-i})
                           + error_t

    fit via `runOrdinaryLeastSquaresRegression` (no intercept term — see
    module docstring). Returns the real t-statistic on `rho` plus a
    real (magnitude) comparison against
    `ASYMPTOTIC_DICKEY_FULLER_CRITICAL_VALUES_NO_CONSTANT` — MORE
    NEGATIVE than the critical value means the null hypothesis of a unit
    root is rejected (i.e. the series is judged stationary at that
    significance level). See the module docstring's caveat: these
    critical values are standard asymptotic Dickey-Fuller values, NOT
    the more conservative MacKinnon Engle-Granger-adjusted ones.

    Raises `ValueError` if `numberOfLagsForAugmentedTerms` is negative,
    or if `series` is too short for the requested lag order to leave a
    positive degrees of freedom.
    """
    if numberOfLagsForAugmentedTerms < 0:
        raise ValueError("numberOfLagsForAugmentedTerms must be >= 0")

    firstDifferences = [series[i] - series[i - 1] for i in range(1, len(series))]

    # Target: delta(series_t) for t starting late enough that every
    # requested lagged-difference regressor is available.
    startIndex = numberOfLagsForAugmentedTerms + 1  # index into firstDifferences
    if startIndex >= len(firstDifferences):
        raise ValueError("series is too short for the requested numberOfLagsForAugmentedTerms")

    targetValues = firstDifferences[startIndex:]
    designMatrixRows: list[list[float]] = []
    for differenceIndex in range(startIndex, len(firstDifferences)):
        # differenceIndex corresponds to delta(series_t) where
        # t = differenceIndex + 1 in the original series; series_{t-1}
        # is therefore series[differenceIndex].
        laggedLevel = series[differenceIndex]
        row = [laggedLevel]
        for lag in range(1, numberOfLagsForAugmentedTerms + 1):
            row.append(firstDifferences[differenceIndex - lag])
        designMatrixRows.append(row)

    olsResult = runOrdinaryLeastSquaresRegression(designMatrixRows, targetValues)
    unitRootCoefficient = olsResult.coefficients[0]
    standardError = olsResult.standardErrorsOfCoefficients[0]
    testStatistic = unitRootCoefficient / standardError

    return AugmentedDickeyFullerTestResult(
        testStatistic=testStatistic,
        unitRootCoefficient=unitRootCoefficient,
        standardErrorOfUnitRootCoefficient=standardError,
        numberOfLagsUsed=numberOfLagsForAugmentedTerms,
        isStationaryAtOnePercent=testStatistic < ASYMPTOTIC_DICKEY_FULLER_CRITICAL_VALUES_NO_CONSTANT["1%"],
        isStationaryAtFivePercent=testStatistic < ASYMPTOTIC_DICKEY_FULLER_CRITICAL_VALUES_NO_CONSTANT["5%"],
        isStationaryAtTenPercent=testStatistic < ASYMPTOTIC_DICKEY_FULLER_CRITICAL_VALUES_NO_CONSTANT["10%"],
    )


@dataclass(frozen=True)
class EngleGrangerCointegrationTestResult:
    regressionIntercept: float
    regressionSlope: float
    residualSeries: list[float]
    adfTestResult: AugmentedDickeyFullerTestResult
    isLikelyCointegratedAtFivePercent: bool


def performEngleGrangerCointegrationTest(
    firstSeries: list[float], secondSeries: list[float], numberOfLagsForAugmentedTerms: int = 0
) -> EngleGrangerCointegrationTestResult:
    """The full real two-step Engle-Granger procedure:

    1. Regress `secondSeries` on `firstSeries` WITH an intercept
       (`secondSeries = alpha + beta * firstSeries + residual`) via real
       OLS.
    2. Run the real ADF test (above) on that regression's residual
       series.

    `firstSeries` and `secondSeries` are two instruments' price (or
    log-price) series, time-aligned and equal length — this function
    does no resampling/alignment of its own. `isLikelyCointegratedAtFivePercent`
    mirrors the ADF result's 5% conclusion (see the module docstring's
    caveat about the critical values used).
    """
    if len(firstSeries) != len(secondSeries):
        raise ValueError("firstSeries and secondSeries must be the same length")

    designMatrixRows = [[1.0, x] for x in firstSeries]
    stepOneResult = runOrdinaryLeastSquaresRegression(designMatrixRows, secondSeries)
    intercept, slope = stepOneResult.coefficients

    adfResult = calculateAugmentedDickeyFullerTestStatistic(
        stepOneResult.residuals, numberOfLagsForAugmentedTerms
    )

    return EngleGrangerCointegrationTestResult(
        regressionIntercept=intercept,
        regressionSlope=slope,
        residualSeries=stepOneResult.residuals,
        adfTestResult=adfResult,
        isLikelyCointegratedAtFivePercent=adfResult.isStationaryAtFivePercent,
    )
