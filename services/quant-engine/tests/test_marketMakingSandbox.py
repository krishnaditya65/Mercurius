import pytest

from quantengine.marketMakingSandbox import (
    MarketMakingSandbox,
    QuoteRejectedError,
    QuoteSide,
    TwoSidedQuote,
)


def buildSandboxWithRegisteredSymbol(maximumAbsoluteInventory: float = 100.0) -> MarketMakingSandbox:
    sandbox = MarketMakingSandbox()
    sandbox.registerSymbolWithInventoryLimit("AAPL", maximumAbsoluteInventory)
    return sandbox


def test_twoSidedQuoteRejectsCrossedPrices():
    with pytest.raises(ValueError):
        TwoSidedQuote(bidPrice=101.0, bidQuantity=10, askPrice=100.0, askQuantity=10)


def test_twoSidedQuoteRejectsNonPositivePrices():
    with pytest.raises(ValueError):
        TwoSidedQuote(bidPrice=0.0, bidQuantity=10, askPrice=100.0, askQuantity=10)


def test_submitQuoteRaisesForUnregisteredSymbol():
    sandbox = MarketMakingSandbox()
    with pytest.raises(KeyError):
        sandbox.submitTwoSidedQuote("AAPL", 99.0, 10, 101.0, 10)


def test_registerSymbolRejectsNonPositiveLimit():
    sandbox = MarketMakingSandbox()
    with pytest.raises(ValueError):
        sandbox.registerSymbolWithInventoryLimit("AAPL", 0.0)


def test_submitQuoteAcceptsAQuoteWithinInventoryLimit():
    sandbox = buildSandboxWithRegisteredSymbol(maximumAbsoluteInventory=100.0)
    quote = sandbox.submitTwoSidedQuote("AAPL", bidPrice=99.0, bidQuantity=50, askPrice=101.0, askQuantity=50)
    assert quote.bidPrice == 99.0
    assert sandbox.getInventoryPosition("AAPL") == 0.0


def test_submitQuoteRejectsWhenFullBidFillWouldExceedLongLimit():
    sandbox = buildSandboxWithRegisteredSymbol(maximumAbsoluteInventory=100.0)
    with pytest.raises(QuoteRejectedError):
        sandbox.submitTwoSidedQuote("AAPL", bidPrice=99.0, bidQuantity=150, askPrice=101.0, askQuantity=10)


def test_submitQuoteRejectsWhenFullAskFillWouldExceedShortLimit():
    sandbox = buildSandboxWithRegisteredSymbol(maximumAbsoluteInventory=100.0)
    with pytest.raises(QuoteRejectedError):
        sandbox.submitTwoSidedQuote("AAPL", bidPrice=99.0, bidQuantity=10, askPrice=101.0, askQuantity=150)


def test_submitQuoteAccountsForExistingInventoryWhenCheckingLimit():
    sandbox = buildSandboxWithRegisteredSymbol(maximumAbsoluteInventory=100.0)
    # Build up inventory to +80 first via a fill.
    sandbox.submitTwoSidedQuote("AAPL", bidPrice=99.0, bidQuantity=80, askPrice=101.0, askQuantity=80)
    sandbox.simulateTakerOrderCrossingQuote("AAPL", QuoteSide.BID, 80)
    assert sandbox.getInventoryPosition("AAPL") == 80.0

    # A further bid quote of 30 would push inventory to 110 if fully
    # filled -> should be rejected given the 100 limit.
    with pytest.raises(QuoteRejectedError):
        sandbox.submitTwoSidedQuote("AAPL", bidPrice=99.0, bidQuantity=30, askPrice=101.0, askQuantity=10)

    # A bid quote of 20 (-> 100 exactly) is right at the boundary and
    # should be accepted.
    sandbox.submitTwoSidedQuote("AAPL", bidPrice=99.0, bidQuantity=20, askPrice=101.0, askQuantity=10)


def test_simulateTakerFillOnBidIncreasesInventory():
    sandbox = buildSandboxWithRegisteredSymbol()
    sandbox.submitTwoSidedQuote("AAPL", bidPrice=99.0, bidQuantity=50, askPrice=101.0, askQuantity=50)
    filled = sandbox.simulateTakerOrderCrossingQuote("AAPL", QuoteSide.BID, 20)
    assert filled == 20
    assert sandbox.getInventoryPosition("AAPL") == 20.0


def test_simulateTakerFillOnAskDecreasesInventory():
    sandbox = buildSandboxWithRegisteredSymbol()
    sandbox.submitTwoSidedQuote("AAPL", bidPrice=99.0, bidQuantity=50, askPrice=101.0, askQuantity=50)
    filled = sandbox.simulateTakerOrderCrossingQuote("AAPL", QuoteSide.ASK, 15)
    assert filled == 15
    assert sandbox.getInventoryPosition("AAPL") == -15.0


def test_simulateTakerFillCapsAtQuotedQuantity():
    sandbox = buildSandboxWithRegisteredSymbol()
    sandbox.submitTwoSidedQuote("AAPL", bidPrice=99.0, bidQuantity=10, askPrice=101.0, askQuantity=10)
    filled = sandbox.simulateTakerOrderCrossingQuote("AAPL", QuoteSide.BID, 100)
    assert filled == 10
    assert sandbox.getInventoryPosition("AAPL") == 10.0


def test_simulateTakerFillRaisesWithNoActiveQuote():
    sandbox = buildSandboxWithRegisteredSymbol()
    with pytest.raises(ValueError):
        sandbox.simulateTakerOrderCrossingQuote("AAPL", QuoteSide.BID, 10)


def test_simulateTakerFillRaisesOnNonPositiveQuantity():
    sandbox = buildSandboxWithRegisteredSymbol()
    sandbox.submitTwoSidedQuote("AAPL", bidPrice=99.0, bidQuantity=10, askPrice=101.0, askQuantity=10)
    with pytest.raises(ValueError):
        sandbox.simulateTakerOrderCrossingQuote("AAPL", QuoteSide.BID, 0)


def test_registeringSymbolTwiceUpdatesLimitWithoutResettingInventory():
    sandbox = buildSandboxWithRegisteredSymbol(maximumAbsoluteInventory=100.0)
    sandbox.submitTwoSidedQuote("AAPL", bidPrice=99.0, bidQuantity=30, askPrice=101.0, askQuantity=30)
    sandbox.simulateTakerOrderCrossingQuote("AAPL", QuoteSide.BID, 30)
    assert sandbox.getInventoryPosition("AAPL") == 30.0

    sandbox.registerSymbolWithInventoryLimit("AAPL", 200.0)
    assert sandbox.getInventoryPosition("AAPL") == 30.0
    assert sandbox.getSymbolState("AAPL").maximumAbsoluteInventory == 200.0


def test_getInventoryPositionRaisesForUnregisteredSymbol():
    sandbox = MarketMakingSandbox()
    with pytest.raises(KeyError):
        sandbox.getInventoryPosition("NOPE")
