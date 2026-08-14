import pytest

from quantengine.customIndexConstructionBacktester import (
    ILLUSTRATIVE_INDEX_CONSTITUENT_UNIVERSE,
    IndexConstituentHistory,
    IndexConstructionRule,
    IndexWeightingScheme,
    backtestConstructedIndex,
    calculateCompoundAnnualGrowthRate,
    calculateTargetWeightsForConstituents,
    constructCustomIndex,
    selectTopNConstituentsByMarketCap,
)


def _makeUniverse():
    return [
        IndexConstituentHistory("A", [10.0, 11.0, 12.0, 13.0], sharesOutstandingBillions=10.0),  # mktcap 100->130
        IndexConstituentHistory("B", [20.0, 19.0, 18.0, 17.0], sharesOutstandingBillions=5.0),   # mktcap 100->85
        IndexConstituentHistory("C", [5.0, 5.5, 6.0, 6.5], sharesOutstandingBillions=4.0),        # mktcap 20->26
    ]


def test_selectTopNConstituentsByMarketCap_picksHighestMarketCapAtGivenBar():
    universe = _makeUniverse()
    top2 = selectTopNConstituentsByMarketCap(universe, barIndex=0, constituentCount=2)
    assert [c.symbol for c in top2] == ["A", "B"]  # 100, 100 (tie -> alphabetical), C=20 excluded


def test_selectTopNConstituentsByMarketCap_rejectsTooLargeN():
    universe = _makeUniverse()
    with pytest.raises(ValueError):
        selectTopNConstituentsByMarketCap(universe, barIndex=0, constituentCount=10)


def test_calculateTargetWeightsForConstituents_equalWeight():
    universe = _makeUniverse()
    weights = calculateTargetWeightsForConstituents(universe, barIndex=0, weightingScheme=IndexWeightingScheme.EQUAL_WEIGHT)
    assert weights == {"A": pytest.approx(1 / 3), "B": pytest.approx(1 / 3), "C": pytest.approx(1 / 3)}
    assert sum(weights.values()) == pytest.approx(1.0)


def test_calculateTargetWeightsForConstituents_capWeightHandWorked():
    universe = _makeUniverse()
    # market caps at bar 0: A=100, B=100, C=20 -> total=220
    weights = calculateTargetWeightsForConstituents(universe, barIndex=0, weightingScheme=IndexWeightingScheme.CAP_WEIGHT)
    assert weights["A"] == pytest.approx(100 / 220)
    assert weights["B"] == pytest.approx(100 / 220)
    assert weights["C"] == pytest.approx(20 / 220)
    assert sum(weights.values()) == pytest.approx(1.0)


def test_constructCustomIndex_rejectsEmptyUniverse():
    with pytest.raises(ValueError):
        constructCustomIndex([], IndexConstructionRule(2, IndexWeightingScheme.EQUAL_WEIGHT, 1))


def test_constructCustomIndex_startsAtOneHundred():
    universe = _makeUniverse()
    rule = IndexConstructionRule(constituentCount=2, weightingScheme=IndexWeightingScheme.EQUAL_WEIGHT, rebalanceFrequencyInBars=2)
    result = constructCustomIndex(universe, rule)
    assert result.indexLevelSeries[0] == pytest.approx(100.0)


def test_constructCustomIndex_equalWeightTwoConstituentNoRebalanceHandWorked():
    """No rebalancing after bar 0 (rebalanceFrequencyInBars larger than
    series) -> pure buy-and-hold of A and B equal-weighted at 50 each.
    sharesA = 50/10 = 5.0, sharesB = 50/20 = 2.5.
    bar1: 5.0*11 + 2.5*19 = 55 + 47.5 = 102.5
    bar2: 5.0*12 + 2.5*18 = 60 + 45.0 = 105.0
    bar3: 5.0*13 + 2.5*17 = 65 + 42.5 = 107.5
    """
    universe = _makeUniverse()
    rule = IndexConstructionRule(constituentCount=2, weightingScheme=IndexWeightingScheme.EQUAL_WEIGHT, rebalanceFrequencyInBars=100)
    result = constructCustomIndex(universe, rule)
    assert result.indexLevelSeries == [pytest.approx(v) for v in [100.0, 102.5, 105.0, 107.5]]
    assert len(result.rebalanceEvents) == 1  # only the initial bar-0 rebalance
    assert result.rebalanceEvents[0].constituentSymbols == ["A", "B"]


def test_constructCustomIndex_rebalancingReselectsConstituentsOverTime():
    # C's market cap grows relative to B over time; with frequent
    # rebalancing and constituentCount=2, membership can change.
    universe = [
        IndexConstituentHistory("A", [100.0] * 6, sharesOutstandingBillions=10.0),  # flat, mktcap 1000
        IndexConstituentHistory("B", [50.0, 40.0, 30.0, 20.0, 10.0, 5.0], sharesOutstandingBillions=10.0),  # shrinking
        IndexConstituentHistory("C", [5.0, 10.0, 20.0, 40.0, 80.0, 160.0], sharesOutstandingBillions=10.0),  # growing
    ]
    rule = IndexConstructionRule(constituentCount=2, weightingScheme=IndexWeightingScheme.CAP_WEIGHT, rebalanceFrequencyInBars=1)
    result = constructCustomIndex(universe, rule)
    firstRebalanceSymbols = result.rebalanceEvents[0].constituentSymbols
    lastRebalanceSymbols = result.rebalanceEvents[-1].constituentSymbols
    assert firstRebalanceSymbols == ["A", "B"]  # bar 0: mktcap A=1000,B=500,C=50
    assert lastRebalanceSymbols == ["A", "C"]  # bar 5: mktcap A=1000,B=50,C=1600


def test_constructCustomIndex_rejectsBarCountExceedingHistory():
    universe = _makeUniverse()
    rule = IndexConstructionRule(2, IndexWeightingScheme.EQUAL_WEIGHT, 1)
    with pytest.raises(ValueError):
        constructCustomIndex(universe, rule, barCount=100)


def test_calculateCompoundAnnualGrowthRate_handWorkedExample():
    # doubling over 252 bars (1 year at periodsPerYear=252) -> CAGR = 100%
    cagr = calculateCompoundAnnualGrowthRate(100.0, 200.0, barCount=252, periodsPerYear=252.0)
    assert cagr == pytest.approx(1.0)


def test_calculateCompoundAnnualGrowthRate_rejectsNonPositiveInputs():
    with pytest.raises(ValueError):
        calculateCompoundAnnualGrowthRate(0.0, 100.0, 10, 252.0)
    with pytest.raises(ValueError):
        calculateCompoundAnnualGrowthRate(100.0, 110.0, 0, 252.0)


def test_backtestConstructedIndex_rejectsTooShortSeries():
    from quantengine.customIndexConstructionBacktester import ConstructedIndexResult

    with pytest.raises(ValueError):
        backtestConstructedIndex(ConstructedIndexResult(indexLevelSeries=[100.0], rebalanceEvents=[]))


def test_backtestConstructedIndex_computesRealStatsFromActualPricePath():
    universe = _makeUniverse()
    rule = IndexConstructionRule(constituentCount=2, weightingScheme=IndexWeightingScheme.EQUAL_WEIGHT, rebalanceFrequencyInBars=100)
    constructed = constructCustomIndex(universe, rule)
    performance = backtestConstructedIndex(constructed, periodsPerYear=252.0)

    assert performance.startingIndexLevel == pytest.approx(100.0)
    assert performance.endingIndexLevel == pytest.approx(constructed.indexLevelSeries[-1])
    # index rose monotonically (100 -> 102.5 -> 105.0 -> 107.5) so CAGR > 0
    assert performance.compoundAnnualGrowthRate > 0
    # monotonically rising series -> zero drawdown
    assert performance.maximumDrawdownFraction == pytest.approx(0.0)


def test_backtestConstructedIndex_illustrativeUniverseProducesRealStats():
    rule = IndexConstructionRule(constituentCount=3, weightingScheme=IndexWeightingScheme.CAP_WEIGHT, rebalanceFrequencyInBars=20)
    constructed = constructCustomIndex(ILLUSTRATIVE_INDEX_CONSTITUENT_UNIVERSE, rule)
    performance = backtestConstructedIndex(constructed)
    assert performance.barCount == len(constructed.indexLevelSeries)
    assert performance.startingIndexLevel == pytest.approx(100.0)
    assert isinstance(performance.compoundAnnualGrowthRate, float)
    assert 0.0 <= performance.maximumDrawdownFraction <= 1.0
    assert len(constructed.rebalanceEvents) == 6  # bars 0,20,40,60,80,100 out of 120
