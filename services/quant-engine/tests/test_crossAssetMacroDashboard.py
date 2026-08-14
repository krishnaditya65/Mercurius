import math

import pytest

from quantengine.crossAssetMacroDashboard import (
    MacroDashboardSnapshot,
    buildMacroDashboardSnapshot,
    findStrongestMacroCorrelationPairs,
)

# Same hand-worked Pearson fixture as test_correlationMatrixEngine.py,
# reused here to prove this module's aggregation reuses the SAME real
# correlation math (verbatim), not a reimplementation.
# x = [1, 2, 3, 4], y = [1, 3, 2, 5] -> r = 5.5 / sqrt(5.0 * 8.75) = 0.8315218406202999
HAND_WORKED_X = [1, 2, 3, 4]
HAND_WORKED_Y = [1, 3, 2, 5]
HAND_WORKED_EXPECTED_CORRELATION = 0.8315218406202999


class TestBuildMacroDashboardSnapshotHandWorked:
    def testTwoSeriesReproducesReusedCorrelationEngineValue(self):
        snapshot = buildMacroDashboardSnapshot({"US10Y_YIELD": HAND_WORKED_X, "DXY": HAND_WORKED_Y})
        assert isinstance(snapshot, MacroDashboardSnapshot)
        assert snapshot.observationCount == 4
        assert snapshot.seriesLabelsInOrder == ["US10Y_YIELD", "DXY"]
        r = snapshot.correlationMatrix.getCorrelation("US10Y_YIELD", "DXY")
        assert math.isclose(r, HAND_WORKED_EXPECTED_CORRELATION, rel_tol=1e-12)

    def testFiveIllustrativeMacroSeriesAggregation(self):
        namedSeries = {
            "US10Y_YIELD": [4.0, 4.1, 4.2, 4.3],
            "DXY": [103.0, 103.5, 104.0, 104.5],
            "WTI_CRUDE": [75.0, 74.0, 76.0, 73.0],
            "VIX": [15.0, 18.0, 14.0, 20.0],
            "SPX": [5000.0, 4950.0, 5050.0, 4900.0],
        }
        snapshot = buildMacroDashboardSnapshot(namedSeries)
        assert snapshot.observationCount == 4
        assert set(snapshot.seriesLabelsInOrder) == set(namedSeries.keys())
        grid = snapshot.toDenseCorrelationGrid()
        assert len(grid) == 5
        # diagonal is always 1.0 exactly
        for i in range(5):
            assert grid[i][i] == pytest.approx(1.0)

    def testPerfectlyCoMovingYieldsAndDxyGiveCorrelationOfOne(self):
        # a fabricated illustrative scenario where DXY moves in perfect
        # lockstep with the 10-year yield -> correlation must be exactly 1.0.
        snapshot = buildMacroDashboardSnapshot(
            {"US10Y_YIELD": [1.0, 2.0, 3.0, 4.0], "DXY": [100.0, 102.0, 104.0, 106.0]}
        )
        r = snapshot.correlationMatrix.getCorrelation("US10Y_YIELD", "DXY")
        assert r == pytest.approx(1.0)

    def testInverselyCoMovingVixAndSpxGiveCorrelationOfNegativeOne(self):
        # illustrative "risk-off" fixture: VIX up exactly as SPX goes down.
        snapshot = buildMacroDashboardSnapshot({"VIX": [10.0, 20.0, 30.0, 40.0], "SPX": [5000.0, 4900.0, 4800.0, 4700.0]})
        r = snapshot.correlationMatrix.getCorrelation("VIX", "SPX")
        assert r == pytest.approx(-1.0)

    def testMisalignedSeriesLengthsRaiseNamingTheOffendingLabel(self):
        with pytest.raises(ValueError, match="DXY"):
            buildMacroDashboardSnapshot({"US10Y_YIELD": [1.0, 2.0, 3.0], "DXY": [1.0, 2.0]})

    def testEmptyDictRaises(self):
        with pytest.raises(ValueError):
            buildMacroDashboardSnapshot({})

    def testSingleSeriesRaisesViaReusedCorrelationEngine(self):
        with pytest.raises(ValueError):
            buildMacroDashboardSnapshot({"VIX": [1.0, 2.0, 3.0]})


class TestFindStrongestMacroCorrelationPairs:
    def testFiltersAndSortsByDescendingAbsoluteCorrelation(self):
        snapshot = buildMacroDashboardSnapshot(
            {
                "US10Y_YIELD": [1.0, 2.0, 3.0, 4.0],
                "DXY": [100.0, 102.0, 104.0, 106.0],  # perfectly +1 correlated with yields
                "VIX": [10.0, 8.0, 12.0, 5.0],  # weakly correlated with either
            }
        )
        pairs = findStrongestMacroCorrelationPairs(snapshot, minimumAbsoluteCorrelationThreshold=0.9)
        assert len(pairs) == 1
        assert {pairs[0].firstSymbol, pairs[0].secondSymbol} == {"US10Y_YIELD", "DXY"}
        assert pairs[0].correlationCoefficient == pytest.approx(1.0)

    def testEmptyResultWhenNoPairMeetsThreshold(self):
        snapshot = buildMacroDashboardSnapshot({"A": [1.0, 2.0, 3.0], "B": [3.0, 1.0, 2.0]})
        pairs = findStrongestMacroCorrelationPairs(snapshot, minimumAbsoluteCorrelationThreshold=0.999)
        assert pairs == []

    def testNegativeCorrelationCountsByAbsoluteMagnitude(self):
        snapshot = buildMacroDashboardSnapshot({"VIX": [10.0, 20.0, 30.0], "SPX": [100.0, 90.0, 80.0]})
        pairs = findStrongestMacroCorrelationPairs(snapshot, minimumAbsoluteCorrelationThreshold=0.5)
        assert len(pairs) == 1
        assert pairs[0].correlationCoefficient == pytest.approx(-1.0)
