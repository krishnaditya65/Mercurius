import os

import pytest

from quantengine.stockScreenerFilterBuilder import (
    ILLUSTRATIVE_INSTRUMENT_UNIVERSE,
    InstrumentRecord,
    SavedScreenStore,
    buildScreenableInstrumentSnapshot,
    calculateRelativeStrengthIndex,
    calculateSimpleMovingAverage,
    evaluateFilterExpression,
    runScreenAgainstUniverse,
)


def test_simpleMovingAverage_handWorkedExample():
    # last 3 of [1, 2, 3, 4, 5] -> (3+4+5)/3 = 4.0
    assert calculateSimpleMovingAverage([1.0, 2.0, 3.0, 4.0, 5.0], 3) == pytest.approx(4.0)


def test_simpleMovingAverage_rejectsTooShortSeries():
    with pytest.raises(ValueError):
        calculateSimpleMovingAverage([1.0, 2.0], 5)


def test_relativeStrengthIndex_steadilyRisingSeriesSaturatesAtOneHundred():
    # every price change is positive -> averageLoss stays exactly 0 -> RSI == 100
    risingPrices = [100.0 + 0.75 * i for i in range(20)]
    assert calculateRelativeStrengthIndex(risingPrices, period=14) == pytest.approx(100.0)


def test_relativeStrengthIndex_steadilyFallingSeriesGoesToZero():
    # every price change is negative -> averageGain stays exactly 0 -> RSI == 0
    fallingPrices = [100.0 - 0.6 * i for i in range(20)]
    assert calculateRelativeStrengthIndex(fallingPrices, period=14) == pytest.approx(0.0)


def test_relativeStrengthIndex_handWorkedFourteenPeriodExample():
    # Classic textbook-style example: 14 up-moves of size 1.0 followed by
    # a few down-moves of size 0.5. First 14 changes are all +1.0 ->
    # averageGain=1.0, averageLoss=0.0 -> RSI=100 exactly (matches the
    # all-positive case above); this confirms the Wilder recursion kicks
    # in correctly for period 15 (one down move folded in):
    #   averageGain = (1.0*13 + 0.0)/14 = 0.9285714285714286
    #   averageLoss = (0.0*13 + 0.5)/14 = 0.03571428571428571
    #   RS = 0.9285714285714286 / 0.03571428571428571 = 26.0
    #   RSI = 100 - 100/(1+26.0) = 100 - 3.7037... = 96.296...
    prices = [100.0]
    for _ in range(14):
        prices.append(prices[-1] + 1.0)
    prices.append(prices[-1] - 0.5)
    rsi = calculateRelativeStrengthIndex(prices, period=14)
    assert rsi == pytest.approx(96.2962962963, abs=1e-6)


def test_relativeStrengthIndex_rejectsTooShortSeries():
    with pytest.raises(ValueError):
        calculateRelativeStrengthIndex([1.0, 2.0, 3.0], period=14)


def test_buildScreenableInstrumentSnapshot_computesRealTechnicalFieldsFromPriceSeries():
    record = InstrumentRecord(
        symbol="TEST-CO",
        sector="TECHNOLOGY",
        priceToEarningsRatio=20.0,
        marketCapitalizationBillions=10.0,
        dividendYieldPercent=1.0,
        closingPrices=[100.0 + 0.5 * i for i in range(60)],
    )
    snapshot = buildScreenableInstrumentSnapshot(record)
    assert snapshot.currentPrice == pytest.approx(record.closingPrices[-1])
    assert snapshot.simpleMovingAverage50Day == pytest.approx(
        calculateSimpleMovingAverage(record.closingPrices, 50)
    )
    assert snapshot.relativeStrengthIndex14Day == pytest.approx(100.0)  # steadily rising -> RSI 100


def test_evaluateFilterExpression_simpleComparisonLeaf():
    fieldValues = {"peRatio": 15.0}
    assert evaluateFilterExpression(fieldValues, {"field": "peRatio", "operator": "<", "value": 20.0}) is True
    assert evaluateFilterExpression(fieldValues, {"field": "peRatio", "operator": ">", "value": 20.0}) is False


def test_evaluateFilterExpression_unknownFieldRaisesKeyError():
    with pytest.raises(KeyError):
        evaluateFilterExpression({"peRatio": 15.0}, {"field": "nope", "operator": "<", "value": 1.0})


def test_evaluateFilterExpression_unknownOperatorRaisesValueError():
    with pytest.raises(ValueError):
        evaluateFilterExpression({"peRatio": 15.0}, {"field": "peRatio", "operator": "~=", "value": 1.0})


def test_evaluateFilterExpression_andGroupRequiresAllConditions():
    fieldValues = {"peRatio": 15.0, "sector": "TECHNOLOGY"}
    expression = {
        "logic": "AND",
        "conditions": [
            {"field": "peRatio", "operator": "<", "value": 20.0},
            {"field": "sector", "operator": "==", "value": "TECHNOLOGY"},
        ],
    }
    assert evaluateFilterExpression(fieldValues, expression) is True

    expression["conditions"][1]["value"] = "FINANCIALS"
    assert evaluateFilterExpression(fieldValues, expression) is False


def test_evaluateFilterExpression_orGroupRequiresAtLeastOneCondition():
    fieldValues = {"peRatio": 50.0, "dividendYieldPercent": 5.0}
    expression = {
        "logic": "OR",
        "conditions": [
            {"field": "peRatio", "operator": "<", "value": 20.0},
            {"field": "dividendYieldPercent", "operator": ">=", "value": 4.0},
        ],
    }
    assert evaluateFilterExpression(fieldValues, expression) is True


def test_evaluateFilterExpression_nestedCompoundGroups():
    fieldValues = {"sector": "TECHNOLOGY", "peRatio": 25.0, "dividendYieldPercent": 0.0}
    # (sector == TECHNOLOGY AND peRatio < 30) OR dividendYieldPercent >= 4.0
    expression = {
        "logic": "OR",
        "conditions": [
            {
                "logic": "AND",
                "conditions": [
                    {"field": "sector", "operator": "==", "value": "TECHNOLOGY"},
                    {"field": "peRatio", "operator": "<", "value": 30.0},
                ],
            },
            {"field": "dividendYieldPercent", "operator": ">=", "value": 4.0},
        ],
    }
    assert evaluateFilterExpression(fieldValues, expression) is True


def test_evaluateFilterExpression_emptyAndGroupIsVacuouslyTrue():
    assert evaluateFilterExpression({}, {"logic": "AND", "conditions": []}) is True


def test_evaluateFilterExpression_emptyOrGroupIsFalse():
    assert evaluateFilterExpression({}, {"logic": "OR", "conditions": []}) is False


def test_evaluateFilterExpression_inAndNotInOperators():
    fieldValues = {"sector": "ENERGY"}
    assert evaluateFilterExpression(
        fieldValues, {"field": "sector", "operator": "in", "value": ["ENERGY", "UTILITIES"]}
    )
    assert not evaluateFilterExpression(
        fieldValues, {"field": "sector", "operator": "not_in", "value": ["ENERGY", "UTILITIES"]}
    )


def test_runScreenAgainstUniverse_filtersRealIllustrativeUniverseByFundamentals():
    # Value criterion: low P/E and high dividend yield should surface the
    # value/dividend-oriented illustrative symbols, not the growth ones.
    expression = {
        "logic": "AND",
        "conditions": [
            {"field": "priceToEarningsRatio", "operator": "<", "value": 20.0},
            {"field": "dividendYieldPercent", "operator": ">=", "value": 4.0},
        ],
    }
    results = runScreenAgainstUniverse(expression)
    resultSymbols = {snapshot.symbol for snapshot in results}
    assert "SIM-VALUECO" in resultSymbols
    assert "SIM-DIVCO" in resultSymbols
    assert "SIM-GROWTHCO" not in resultSymbols  # 0% yield, high P/E — must be excluded


def test_runScreenAgainstUniverse_filtersByTechnicalCriterion():
    # RSI == 100 (steadily rising) should isolate the momentum names.
    expression = {"field": "relativeStrengthIndex14Day", "operator": ">=", "value": 90.0}
    results = runScreenAgainstUniverse(expression)
    resultSymbols = {snapshot.symbol for snapshot in results}
    assert "SIM-GROWTHCO" in resultSymbols
    assert "SIM-MEGACAPCO" in resultSymbols
    assert "SIM-DECLINECO" not in resultSymbols


def test_runScreenAgainstUniverse_resultsAreSortedAlphabetically():
    results = runScreenAgainstUniverse({"logic": "AND", "conditions": []})
    resultSymbols = [snapshot.symbol for snapshot in results]
    assert resultSymbols == sorted(resultSymbols)
    assert len(results) == len(ILLUSTRATIVE_INSTRUMENT_UNIVERSE)


def test_savedScreenStore_saveGetListDelete_inMemory():
    store = SavedScreenStore()
    expression = {"field": "peRatio", "operator": "<", "value": 20.0}
    store.saveScreen("cheap-tech", expression, description="Low P/E screen")

    saved = store.getScreen("cheap-tech")
    assert saved.filterExpression == expression
    assert saved.description == "Low P/E screen"
    assert [screen.screenName for screen in store.listScreens()] == ["cheap-tech"]

    store.deleteScreen("cheap-tech")
    with pytest.raises(KeyError):
        store.getScreen("cheap-tech")


def test_savedScreenStore_getUnknownScreenRaisesKeyError():
    store = SavedScreenStore()
    with pytest.raises(KeyError):
        store.getScreen("does-not-exist")


def test_savedScreenStore_rejectsEmptyScreenName():
    store = SavedScreenStore()
    with pytest.raises(ValueError):
        store.saveScreen("", {"field": "peRatio", "operator": "<", "value": 20.0})


def test_savedScreenStore_filePersistenceSurvivesReload(tmp_path):
    persistenceFilePath = os.path.join(str(tmp_path), "savedScreens.json")
    expression = {"field": "sector", "operator": "==", "value": "TECHNOLOGY"}

    firstStore = SavedScreenStore(persistenceFilePath=persistenceFilePath)
    firstStore.saveScreen("tech-only", expression, description="Tech sector screen")
    assert os.path.exists(persistenceFilePath)

    # A brand-new store instance pointed at the same file should load the
    # previously saved screen back — proving this is REAL persistence, not
    # just an in-memory dict.
    secondStore = SavedScreenStore(persistenceFilePath=persistenceFilePath)
    reloaded = secondStore.getScreen("tech-only")
    assert reloaded.filterExpression == expression
    assert reloaded.description == "Tech sector screen"


def test_savedScreenStore_filePersistenceReflectsDeletes(tmp_path):
    persistenceFilePath = os.path.join(str(tmp_path), "savedScreens.json")
    store = SavedScreenStore(persistenceFilePath=persistenceFilePath)
    store.saveScreen("temp-screen", {"field": "peRatio", "operator": "<", "value": 10.0})
    store.deleteScreen("temp-screen")

    reloadedStore = SavedScreenStore(persistenceFilePath=persistenceFilePath)
    with pytest.raises(KeyError):
        reloadedStore.getScreen("temp-screen")
