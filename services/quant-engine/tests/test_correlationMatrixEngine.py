import math

import pytest

from quantengine.correlationMatrixEngine import (
    CorrelationMatrixResult,
    PairsTradingCandidatePair,
    buildPairwiseCorrelationMatrix,
    calculatePearsonCorrelationCoefficient,
    findPairsTradingCandidatePairsAboveCorrelationThreshold,
)

# --- Hand-worked Pearson correlation case -------------------------------
# x = [1, 2, 3, 4], y = [1, 3, 2, 5]
# mean(x) = 2.5, mean(y) = 2.75
# deviations x: -1.5, -0.5, 0.5, 1.5
# deviations y: -1.75, 0.25, -0.75, 2.25
# products: 2.625, -0.125, -0.375, 3.375 -> sum = 5.5
# sum sq x = 2.25+0.25+0.25+2.25 = 5.0
# sum sq y = 3.0625+0.0625+0.5625+5.0625 = 8.75
# r = 5.5 / sqrt(5.0 * 8.75) = 5.5 / sqrt(43.75) = 0.8315218406202999
HAND_WORKED_X = [1, 2, 3, 4]
HAND_WORKED_Y = [1, 3, 2, 5]
HAND_WORKED_EXPECTED_CORRELATION = 0.8315218406202999


def test_pearsonCorrelationMatchesHandWorkedValue():
    r = calculatePearsonCorrelationCoefficient(HAND_WORKED_X, HAND_WORKED_Y)
    assert math.isclose(r, HAND_WORKED_EXPECTED_CORRELATION, rel_tol=1e-12)


def test_pearsonCorrelationIsSymmetric():
    r1 = calculatePearsonCorrelationCoefficient(HAND_WORKED_X, HAND_WORKED_Y)
    r2 = calculatePearsonCorrelationCoefficient(HAND_WORKED_Y, HAND_WORKED_X)
    assert math.isclose(r1, r2, rel_tol=1e-12)


def test_pearsonCorrelationOfPerfectlyLinearSeriesIsOne():
    x = [1, 2, 3, 4, 5]
    y = [2, 4, 6, 8, 10]
    assert math.isclose(calculatePearsonCorrelationCoefficient(x, y), 1.0, rel_tol=1e-12)


def test_pearsonCorrelationOfInverselyLinearSeriesIsNegativeOne():
    x = [1, 2, 3, 4, 5]
    y = [10, 8, 6, 4, 2]
    assert math.isclose(calculatePearsonCorrelationCoefficient(x, y), -1.0, rel_tol=1e-12)


def test_pearsonCorrelationRaisesOnMismatchedLengths():
    with pytest.raises(ValueError):
        calculatePearsonCorrelationCoefficient([1, 2, 3], [1, 2])


def test_pearsonCorrelationRaisesOnEmptySeries():
    with pytest.raises(ValueError):
        calculatePearsonCorrelationCoefficient([], [])


def test_pearsonCorrelationRaisesOnZeroVarianceSeries():
    with pytest.raises(ValueError):
        calculatePearsonCorrelationCoefficient([1, 1, 1, 1], [1, 2, 3, 4])


def test_buildPairwiseCorrelationMatrixComputesAllDistinctPairs():
    returnSeriesBySymbol = {
        "AAA": [1, 2, 3, 4, 5],
        "BBB": [2, 4, 6, 8, 10],  # perfectly correlated with AAA
        "CCC": [10, 8, 6, 4, 2],  # perfectly anti-correlated with AAA
    }
    matrix = buildPairwiseCorrelationMatrix(returnSeriesBySymbol)
    assert isinstance(matrix, CorrelationMatrixResult)
    assert matrix.symbolsInOrder == ["AAA", "BBB", "CCC"]
    assert len(matrix.correlationBySymbolPair) == 3  # 3 choose 2
    assert math.isclose(matrix.getCorrelation("AAA", "BBB"), 1.0, rel_tol=1e-12)
    assert math.isclose(matrix.getCorrelation("BBB", "AAA"), 1.0, rel_tol=1e-12)  # order-independent
    assert math.isclose(matrix.getCorrelation("AAA", "CCC"), -1.0, rel_tol=1e-12)


def test_correlationMatrixSelfCorrelationIsOne():
    returnSeriesBySymbol = {"AAA": [1, 2, 3], "BBB": [3, 1, 2]}
    matrix = buildPairwiseCorrelationMatrix(returnSeriesBySymbol)
    assert matrix.getCorrelation("AAA", "AAA") == 1.0


def test_correlationMatrixToDenseGridIsSymmetricWithUnitDiagonal():
    returnSeriesBySymbol = {
        "AAA": [1, 2, 3, 4, 5],
        "BBB": [2, 4, 6, 8, 10],
        "CCC": [5, 3, 6, 1, 4],
    }
    matrix = buildPairwiseCorrelationMatrix(returnSeriesBySymbol)
    grid = matrix.toDenseGrid()
    assert len(grid) == 3
    for rowIndex in range(3):
        assert grid[rowIndex][rowIndex] == 1.0
        for columnIndex in range(3):
            assert math.isclose(grid[rowIndex][columnIndex], grid[columnIndex][rowIndex], rel_tol=1e-12)


def test_buildPairwiseCorrelationMatrixRaisesOnFewerThanTwoSymbols():
    with pytest.raises(ValueError):
        buildPairwiseCorrelationMatrix({"AAA": [1, 2, 3]})


def test_findCandidatePairsFiltersByAbsoluteThresholdAndSortsDescending():
    returnSeriesBySymbol = {
        "AAA": [1, 2, 3, 4, 5],
        "BBB": [2, 4, 6, 8, 10],  # r = 1.0 with AAA
        "CCC": [10, 8, 6, 4, 2],  # r = -1.0 with AAA, -1.0 with BBB
        "DDD": [5, 3, 6, 1, 4],  # weakly correlated with everything
    }
    matrix = buildPairwiseCorrelationMatrix(returnSeriesBySymbol)
    candidates = findPairsTradingCandidatePairsAboveCorrelationThreshold(
        matrix, minimumAbsoluteCorrelationThreshold=0.9
    )
    assert all(isinstance(c, PairsTradingCandidatePair) for c in candidates)
    pairSet = {frozenset((c.firstSymbol, c.secondSymbol)) for c in candidates}
    assert frozenset(("AAA", "BBB")) in pairSet
    assert frozenset(("AAA", "CCC")) in pairSet
    assert frozenset(("BBB", "CCC")) in pairSet
    assert frozenset(("DDD", "AAA")) not in pairSet
    # sorted descending by absolute correlation
    absCorrelations = [abs(c.correlationCoefficient) for c in candidates]
    assert absCorrelations == sorted(absCorrelations, reverse=True)


def test_findCandidatePairsReturnsEmptyListWhenNoneMeetThreshold():
    returnSeriesBySymbol = {"AAA": [1, 2, 3, 4, 5], "BBB": [5, 1, 4, 2, 3]}
    matrix = buildPairwiseCorrelationMatrix(returnSeriesBySymbol)
    candidates = findPairsTradingCandidatePairsAboveCorrelationThreshold(
        matrix, minimumAbsoluteCorrelationThreshold=0.999
    )
    assert candidates == []


def test_findCandidatePairsRaisesOnOutOfRangeThreshold():
    returnSeriesBySymbol = {"AAA": [1, 2, 3], "BBB": [3, 1, 2]}
    matrix = buildPairwiseCorrelationMatrix(returnSeriesBySymbol)
    with pytest.raises(ValueError):
        findPairsTradingCandidatePairsAboveCorrelationThreshold(matrix, minimumAbsoluteCorrelationThreshold=1.5)
    with pytest.raises(ValueError):
        findPairsTradingCandidatePairsAboveCorrelationThreshold(matrix, minimumAbsoluteCorrelationThreshold=-0.1)
