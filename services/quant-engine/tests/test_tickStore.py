import pytest

from quantengine.backtesting.tickStore import HistoricalPriceTick, InMemoryHistoricalTickStore


def test_addTickAndQueryRangeReturnsTicksWithinInclusiveBounds():
    store = InMemoryHistoricalTickStore()
    store.addTick("AAPL", timestamp=1.0, price=100.0)
    store.addTick("AAPL", timestamp=2.0, price=101.0)
    store.addTick("AAPL", timestamp=3.0, price=102.0)
    store.addTick("AAPL", timestamp=4.0, price=103.0)

    result = store.queryRange("AAPL", startTimestampInclusive=2.0, endTimestampInclusive=3.0)
    assert [tick.price for tick in result] == [101.0, 102.0]
    assert [tick.timestamp for tick in result] == [2.0, 3.0]


def test_queryRangeReturnsEmptyListForUnknownSymbol():
    store = InMemoryHistoricalTickStore()
    assert store.queryRange("UNKNOWN", 0.0, 100.0) == []


def test_ticksAreKeptSortedEvenWhenAddedOutOfOrder():
    store = InMemoryHistoricalTickStore()
    store.addTick("MSFT", timestamp=3.0, price=300.0)
    store.addTick("MSFT", timestamp=1.0, price=100.0)
    store.addTick("MSFT", timestamp=2.0, price=200.0)

    allTicks = store.getAllTicksInOrder("MSFT")
    assert [tick.timestamp for tick in allTicks] == [1.0, 2.0, 3.0]
    assert [tick.price for tick in allTicks] == [100.0, 200.0, 300.0]


def test_addTicksBulkHelperAddsAllTicks():
    store = InMemoryHistoricalTickStore()
    store.addTicks("GOOG", [(1.0, 10.0), (2.0, 11.0), (3.0, 12.0)])
    assert len(store.getAllTicksInOrder("GOOG")) == 3


def test_getKnownSymbolsReflectsAddedSymbols():
    store = InMemoryHistoricalTickStore()
    store.addTick("AAPL", 1.0, 100.0)
    store.addTick("MSFT", 1.0, 200.0)
    assert set(store.getKnownSymbols()) == {"AAPL", "MSFT"}


def test_historicalPriceTickIsOrderedByFieldsForEqualityAndSorting():
    tickA = HistoricalPriceTick(timestamp=1.0, price=100.0)
    tickB = HistoricalPriceTick(timestamp=2.0, price=50.0)
    assert tickA < tickB
    assert HistoricalPriceTick(1.0, 100.0) == tickA
