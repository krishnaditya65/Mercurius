"""Live HTTP tests for httpServer.py — spins up a real ThreadingHTTPServer
on an OS-assigned ephemeral port (not the hardcoded 8085) and makes real
HTTP requests against it with urllib, the same "no framework" stdlib-only
convention the server itself follows.
"""

from __future__ import annotations

import json
import threading
import urllib.error
import urllib.request
from http.server import ThreadingHTTPServer

import pytest

from quantengine.httpServer import QuantEngineRequestHandler


@pytest.fixture()
def runningServerBaseUrl():
    httpServer = ThreadingHTTPServer(("127.0.0.1", 0), QuantEngineRequestHandler)
    serverThread = threading.Thread(target=httpServer.serve_forever, daemon=True)
    serverThread.start()

    host, port = httpServer.server_address
    yield f"http://{host}:{port}"

    httpServer.shutdown()
    httpServer.server_close()
    serverThread.join(timeout=5)


def postJson(baseUrl: str, path: str, body: dict):
    request = urllib.request.Request(
        f"{baseUrl}{path}",
        data=json.dumps(body).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request) as response:
            return response.status, json.loads(response.read())
    except urllib.error.HTTPError as httpError:
        return httpError.code, json.loads(httpError.read())


def testHealthEndpointReturnsOk(runningServerBaseUrl):
    with urllib.request.urlopen(f"{runningServerBaseUrl}/health") as response:
        assert response.status == 200
        assert json.loads(response.read()) == {"status": "ok"}


def testPriceEndpointReturnsPriceAndAllFourGreeksForACallOption(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/options/price",
        {
            "underlyingSpotPrice": 100.0,
            "optionStrikePrice": 100.0,
            "annualizedRiskFreeInterestRate": 0.05,
            "annualizedVolatility": 0.2,
            "timeToExpiryInYears": 1.0,
            "isCallOptionNotPut": True,
        },
    )
    assert statusCode == 200
    assert responseBody["theoreticalPriceInMinorUnits"] == pytest.approx(10.4506, abs=1e-3)
    assert 0.0 < responseBody["delta"] < 1.0
    assert responseBody["gamma"] > 0.0


def testPriceEndpointRejectsAMissingFieldWith400(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/options/price",
        {"underlyingSpotPrice": 100.0, "isCallOptionNotPut": True},
    )
    assert statusCode == 400
    assert "errorMessage" in responseBody


def testPriceEndpointRejectsNonPositiveTimeToExpiryWith422(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/options/price",
        {
            "underlyingSpotPrice": 100.0,
            "optionStrikePrice": 100.0,
            "annualizedRiskFreeInterestRate": 0.05,
            "annualizedVolatility": 0.2,
            "timeToExpiryInYears": 0.0,
            "isCallOptionNotPut": True,
        },
    )
    assert statusCode == 422
    assert "errorMessage" in responseBody


def testImpliedVolatilityEndpointRoundTripsAKnownPrice(runningServerBaseUrl):
    # First get a theoretical price at a known volatility...
    _, priceResponse = postJson(
        runningServerBaseUrl,
        "/options/price",
        {
            "underlyingSpotPrice": 100.0,
            "optionStrikePrice": 100.0,
            "annualizedRiskFreeInterestRate": 0.05,
            "annualizedVolatility": 0.25,
            "timeToExpiryInYears": 1.0,
            "isCallOptionNotPut": True,
        },
    )
    knownPrice = priceResponse["theoreticalPriceInMinorUnits"]

    # ...then solve backward for volatility from that price and confirm
    # it recovers (close to) the volatility that produced it.
    statusCode, ivResponse = postJson(
        runningServerBaseUrl,
        "/options/implied-volatility",
        {
            "observedMarketPrice": knownPrice,
            "underlyingSpotPrice": 100.0,
            "optionStrikePrice": 100.0,
            "annualizedRiskFreeInterestRate": 0.05,
            "timeToExpiryInYears": 1.0,
            "isCallOptionNotPut": True,
        },
    )
    assert statusCode == 200
    assert ivResponse["impliedVolatility"] == pytest.approx(0.25, abs=1e-4)


def testUnknownPostRouteReturns404(runningServerBaseUrl):
    statusCode, responseBody = postJson(runningServerBaseUrl, "/nope", {})
    assert statusCode == 404
    assert "errorMessage" in responseBody


def testMalformedJsonBodyReturns400(runningServerBaseUrl):
    request = urllib.request.Request(
        f"{runningServerBaseUrl}/options/price",
        data=b"{not valid json",
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        urllib.request.urlopen(request)
        assert False, "expected an HTTPError"
    except urllib.error.HTTPError as httpError:
        assert httpError.code == 400


def testCorsHeaderIsPresentOnResponses(runningServerBaseUrl):
    with urllib.request.urlopen(f"{runningServerBaseUrl}/health") as response:
        assert response.headers.get("Access-Control-Allow-Origin") == "*"


def testRiskStatisticsEndpointMatchesHandWorkedValues(runningServerBaseUrl):
    # Same hand-worked series as tests/test_riskStatistics.py:
    # [0.04, -0.02, 0.04, -0.02, 0.04, -0.02], periodicRiskFreeRate=0,
    # periodsPerYear=252 -> Sharpe ~5.291502622129181,
    # Sortino ~11.224972160321824, and (from the same series compounded
    # into an equity curve starting at 1.0) a max drawdown fraction.
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/risk/statistics",
        {
            "periodicReturns": [0.04, -0.02, 0.04, -0.02, 0.04, -0.02],
            "periodicRiskFreeRate": 0.0,
            "periodsPerYear": 252,
        },
    )
    assert statusCode == 200
    assert responseBody["annualizedSharpeRatio"] == pytest.approx(5.291502622129181, rel=1e-9)
    assert responseBody["annualizedSortinoRatio"] == pytest.approx(11.224972160321824, rel=1e-9)
    assert responseBody["maximumDrawdownFraction"] >= 0.0


def testRiskStatisticsEndpointReturns422ForZeroVarianceSeries(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/risk/statistics",
        {"periodicReturns": [0.01, 0.01, 0.01], "periodicRiskFreeRate": 0.0, "periodsPerYear": 252},
    )
    assert statusCode == 422
    assert "errorMessage" in responseBody


def testRiskStatisticsEndpointReturns400ForMissingField(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl, "/risk/statistics", {"periodicReturns": [0.01, 0.02]}
    )
    assert statusCode == 400
    assert "errorMessage" in responseBody


def testArbitrageScanEndpointMatchesHandWorkedValues(runningServerBaseUrl):
    # theoretical=100, live=102, threshold=1% -> absoluteDeviation=2,
    # percentageDeviation=2%, triggered=True, overpriced=True.
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/arbitrage/scan",
        {"theoreticalFairPrice": 100.0, "liveMarketPrice": 102.0, "deviationThresholdPercentage": 1.0},
    )
    assert statusCode == 200
    assert responseBody["absoluteDeviation"] == pytest.approx(2.0)
    assert responseBody["percentageDeviation"] == pytest.approx(2.0)
    assert responseBody["isAlertTriggered"] is True
    assert responseBody["isLiveOverpricedRelativeToTheoretical"] is True


def testArbitrageScanEndpointReturns422ForNonPositiveTheoreticalPrice(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/arbitrage/scan",
        {"theoreticalFairPrice": 0.0, "liveMarketPrice": 10.0, "deviationThresholdPercentage": 1.0},
    )
    assert statusCode == 422
    assert "errorMessage" in responseBody


def testGarchForecastEndpointReturnsStationaryParametersAndExpectedRange(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/volatility/garch-forecast",
        {
            "periodicReturns": [
                0.01, -0.015, 0.02, -0.01, 0.005, -0.02, 0.015, -0.005, 0.01, -0.01,
                0.025, -0.02, 0.01, -0.015, 0.02, -0.01, 0.005, -0.025, 0.015, -0.01,
            ],
            "currentPrice": 100.0,
        },
    )
    assert statusCode == 200
    assert responseBody["omega"] > 0
    assert 0 <= responseBody["alphaArchCoefficient"] < 1
    assert 0 <= responseBody["betaGarchCoefficient"] < 1
    assert responseBody["forecastNextPeriodVolatility"] > 0
    assert responseBody["expectedRangeLowerBound"] < 100.0 < responseBody["expectedRangeUpperBound"]


def testGarchForecastEndpointReturns422ForTooFewReturns(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl, "/volatility/garch-forecast", {"periodicReturns": [0.01, 0.02], "currentPrice": 100.0}
    )
    assert statusCode == 422
    assert "errorMessage" in responseBody


def testCorrelationMatrixEndpointMatchesHandWorkedValue(runningServerBaseUrl):
    # Same x/y hand-worked case as test_correlationMatrixEngine.py:
    # x=[1,2,3,4], y=[1,3,2,5] -> r = 0.8315218406202999
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/correlation/matrix",
        {
            "returnSeriesBySymbol": {"X": [1, 2, 3, 4], "Y": [1, 3, 2, 5]},
            "minimumAbsoluteCorrelationThreshold": 0.5,
        },
    )
    assert statusCode == 200
    assert len(responseBody["correlationBySymbolPair"]) == 1
    pairEntry = responseBody["correlationBySymbolPair"][0]
    assert pairEntry["correlationCoefficient"] == pytest.approx(0.8315218406202999, rel=1e-9)
    assert len(responseBody["candidatePairs"]) == 1


def testCorrelationMatrixEndpointReturns422ForSingleSymbol(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl, "/correlation/matrix", {"returnSeriesBySymbol": {"X": [1, 2, 3]}}
    )
    assert statusCode == 422
    assert "errorMessage" in responseBody


def testValueAtRiskEndpointMatchesHandWorkedValues(runningServerBaseUrl):
    # Same hand-worked series as test_valueAtRiskCalculator.py's
    # historical-VaR case: 90% confidence -> 0.03.
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/risk/value-at-risk",
        {
            "periodicReturns": [-0.05, -0.03, -0.02, -0.01, 0.0, 0.01, 0.02, 0.03, 0.04, 0.05],
            "confidenceLevel": 0.90,
        },
    )
    assert statusCode == 200
    assert responseBody["historicalValueAtRisk"] == pytest.approx(0.03, rel=1e-9)
    assert responseBody["parametricValueAtRisk"] > 0


def testValueAtRiskEndpointReturns422OnZeroVarianceSeries(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl, "/risk/value-at-risk", {"periodicReturns": [0.01, 0.01, 0.01], "confidenceLevel": 0.95}
    )
    assert statusCode == 422
    assert "errorMessage" in responseBody


def testMarketMakingQuoteThenSimulateFillThenInventoryRoundTrips(runningServerBaseUrl):
    quoteStatus, quoteResponse = postJson(
        runningServerBaseUrl,
        "/market-making/quote",
        {
            "symbol": "AAPL",
            "maximumAbsoluteInventory": 100.0,
            "bidPrice": 99.0,
            "bidQuantity": 50.0,
            "askPrice": 101.0,
            "askQuantity": 50.0,
        },
    )
    assert quoteStatus == 200
    assert quoteResponse["inventoryPosition"] == 0.0

    fillStatus, fillResponse = postJson(
        runningServerBaseUrl,
        "/market-making/simulate-fill",
        {"symbol": "AAPL", "takerSide": "BID", "quantity": 20.0},
    )
    assert fillStatus == 200
    assert fillResponse["filledQuantity"] == 20.0
    assert fillResponse["inventoryPosition"] == 20.0

    inventoryStatus, inventoryResponse = postJson(
        runningServerBaseUrl, "/market-making/inventory", {"symbol": "AAPL"}
    )
    assert inventoryStatus == 200
    assert inventoryResponse["inventoryPosition"] == 20.0


def testMarketMakingQuoteRejectsWhenFillWouldExceedInventoryLimit(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/market-making/quote",
        {
            "symbol": "TSLA",
            "maximumAbsoluteInventory": 50.0,
            "bidPrice": 99.0,
            "bidQuantity": 100.0,
            "askPrice": 101.0,
            "askQuantity": 10.0,
        },
    )
    assert statusCode == 422
    assert "errorMessage" in responseBody


def testMarketMakingInventoryReturns404ForUnknownSymbol(runningServerBaseUrl):
    statusCode, responseBody = postJson(runningServerBaseUrl, "/market-making/inventory", {"symbol": "NOPE"})
    assert statusCode == 404
    assert "errorMessage" in responseBody


def testEsgScreenEndpointRanksByCompositeScoreWithNoCriteria(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/esg/screen",
        {"candidateSymbols": ["DEMO-EQ", "SIM-AAPL", "SIM-MSFT", "SIM-JPM"]},
    )
    assert statusCode == 200
    rankedSymbolsInOrder = [result["symbol"] for result in responseBody["rankedResults"]]
    # composites: SIM-MSFT=82.2, SIM-AAPL=81.7, DEMO-EQ=70.0, SIM-JPM=61.5
    assert rankedSymbolsInOrder == ["SIM-MSFT", "SIM-AAPL", "DEMO-EQ", "SIM-JPM"]
    demoEqResult = next(r for r in responseBody["rankedResults"] if r["symbol"] == "DEMO-EQ")
    assert demoEqResult["compositeEsgScore"] == pytest.approx(70.0, abs=1e-9)
    assert responseBody["excludedSymbols"] == []
    assert responseBody["unknownSymbols"] == []


def testEsgScreenEndpointAppliesMinimumCompositeScoreAndSectorExclusionCriteria(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/esg/screen",
        {
            "candidateSymbols": [
                "DEMO-EQ",
                "SIM-AAPL",
                "SIM-MSFT",
                "SIM-JPM",
                "SIM-THERMAL-COAL-CO",
                "SIM-TOBACCO-CO",
            ],
            "minimumCompositeEsgScore": 60.0,
            "excludedControversialSectorFlags": ["TOBACCO", "THERMAL_COAL"],
        },
    )
    assert statusCode == 200
    rankedSymbols = {result["symbol"] for result in responseBody["rankedResults"]}
    assert rankedSymbols == {"SIM-MSFT", "SIM-AAPL", "DEMO-EQ", "SIM-JPM"}
    assert set(responseBody["excludedSymbols"]) == {"SIM-THERMAL-COAL-CO", "SIM-TOBACCO-CO"}


def testEsgScreenEndpointReportsUnknownSymbolsSeparately(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl, "/esg/screen", {"candidateSymbols": ["DEMO-EQ", "NOT-A-REAL-SYMBOL"]}
    )
    assert statusCode == 200
    assert [r["symbol"] for r in responseBody["rankedResults"]] == ["DEMO-EQ"]
    assert responseBody["unknownSymbols"] == ["NOT-A-REAL-SYMBOL"]


def testEsgScreenEndpointRejectsMissingCandidateSymbolsWith400(runningServerBaseUrl):
    statusCode, responseBody = postJson(runningServerBaseUrl, "/esg/screen", {})
    assert statusCode == 400
    assert "errorMessage" in responseBody
