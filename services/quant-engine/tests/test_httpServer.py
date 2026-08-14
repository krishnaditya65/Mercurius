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


def testPortfolioGreeksEndpointHandWorkedTwoPositionAggregation(runningServerBaseUrl):
    # Same hand-worked case as test_portfolioGreeksAggregator.py:
    # netDelta = 10*0.5 + (-5)*0.3 = 3.5, netGamma = 10*0.02 + (-5)*0.01 = 0.15
    # netVega = 10*0.15 + (-5)*0.10 = 1.0, netTheta = 10*(-0.05) + (-5)*(-0.02) = -0.4
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/portfolio/greeks",
        {
            "positions": [
                {
                    "identifier": "A",
                    "quantity": 10.0,
                    "delta": 0.5,
                    "gamma": 0.02,
                    "vegaPerOnePercentVolatilityChange": 0.15,
                    "thetaPerCalendarDay": -0.05,
                },
                {
                    "identifier": "B",
                    "quantity": -5.0,
                    "delta": 0.3,
                    "gamma": 0.01,
                    "vegaPerOnePercentVolatilityChange": 0.10,
                    "thetaPerCalendarDay": -0.02,
                },
            ]
        },
    )
    assert statusCode == 200
    assert responseBody["netDelta"] == pytest.approx(3.5)
    assert responseBody["netGamma"] == pytest.approx(0.15)
    assert responseBody["netVegaPerOnePercentVolatilityChange"] == pytest.approx(1.0)
    assert responseBody["netThetaPerCalendarDay"] == pytest.approx(-0.4)
    assert responseBody["positionCount"] == 2


def testPortfolioGreeksEndpointEmptyPositionsReturnsAllZeros(runningServerBaseUrl):
    statusCode, responseBody = postJson(runningServerBaseUrl, "/portfolio/greeks", {"positions": []})
    assert statusCode == 200
    assert responseBody["netDelta"] == 0.0
    assert responseBody["positionCount"] == 0


def testPortfolioGreeksEndpointRejectsMissingPositionsWith400(runningServerBaseUrl):
    statusCode, responseBody = postJson(runningServerBaseUrl, "/portfolio/greeks", {})
    assert statusCode == 400
    assert "errorMessage" in responseBody


def testIvRankEndpointHandWorkedValues(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/volatility/iv-rank",
        {
            "currentImpliedVolatility": 0.19,
            "historicalImpliedVolatilitySeries": [0.10, 0.12, 0.14, 0.16, 0.18, 0.20, 0.50],
        },
    )
    assert statusCode == 200
    assert responseBody["impliedVolatilityRank"] == pytest.approx(0.225)
    assert responseBody["impliedVolatilityPercentile"] == pytest.approx(5.0 / 7.0)
    assert responseBody["historicalMinimumImpliedVolatility"] == 0.10
    assert responseBody["historicalMaximumImpliedVolatility"] == 0.50


def testIvRankEndpointRejectsFlatHistoricalSeriesWith422(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/volatility/iv-rank",
        {"currentImpliedVolatility": 0.20, "historicalImpliedVolatilitySeries": [0.3, 0.3, 0.3]},
    )
    assert statusCode == 422
    assert "errorMessage" in responseBody


def testDeltaHedgeCheckEndpointHandWorkedBreachAndHedgeQuantity(runningServerBaseUrl):
    # netDelta = 10*0.5 + (-5)*0.3 = 3.5 (same hand-worked positions as /portfolio/greeks)
    # threshold = 2.0 -> |3.5| > 2.0 -> breached
    # hedgeQuantityInShares = -3.5 * 100 = -350.0
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/portfolio/delta-hedge-check",
        {
            "positions": [
                {
                    "identifier": "A",
                    "quantity": 10.0,
                    "delta": 0.5,
                    "gamma": 0.02,
                    "vegaPerOnePercentVolatilityChange": 0.15,
                    "thetaPerCalendarDay": -0.05,
                },
                {
                    "identifier": "B",
                    "quantity": -5.0,
                    "delta": 0.3,
                    "gamma": 0.01,
                    "vegaPerOnePercentVolatilityChange": 0.10,
                    "thetaPerCalendarDay": -0.02,
                },
            ],
            "deltaThreshold": 2.0,
        },
    )
    assert statusCode == 200
    assert responseBody["netDelta"] == pytest.approx(3.5)
    assert responseBody["isThresholdBreached"] is True
    assert responseBody["hedgeQuantityInShares"] == pytest.approx(-350.0)


def testDeltaHedgeCheckEndpointNotBreachedWhenWithinThreshold(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/portfolio/delta-hedge-check",
        {
            "positions": [
                {
                    "identifier": "A",
                    "quantity": 1.0,
                    "delta": 0.1,
                    "gamma": 0.0,
                    "vegaPerOnePercentVolatilityChange": 0.0,
                    "thetaPerCalendarDay": 0.0,
                }
            ],
            "deltaThreshold": 5.0,
        },
    )
    assert statusCode == 200
    assert responseBody["isThresholdBreached"] is False


def testDeltaHedgeCheckEndpointRejectsNonPositiveThresholdWith422(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/portfolio/delta-hedge-check",
        {"positions": [], "deltaThreshold": 0.0},
    )
    assert statusCode == 422
    assert "errorMessage" in responseBody


def testKellyCriterionEndpointHandWorkedHalfKelly(runningServerBaseUrl):
    # p=0.55, b=1.5 -> f* = (1.5*0.55 - 0.45)/1.5 = 0.25, half-Kelly = 0.125
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/sizing/kelly-criterion",
        {"winProbability": 0.55, "winLossPayoutRatio": 1.5},
    )
    assert statusCode == 200
    assert responseBody["fullKellyFraction"] == pytest.approx(0.25)
    assert responseBody["fractionalMultiplier"] == 0.5
    assert responseBody["recommendedAllocationFraction"] == pytest.approx(0.125)


def testKellyCriterionEndpointCustomFractionalMultiplier(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/sizing/kelly-criterion",
        {"winProbability": 0.55, "winLossPayoutRatio": 1.5, "fractionalMultiplier": 1.0},
    )
    assert statusCode == 200
    assert responseBody["recommendedAllocationFraction"] == pytest.approx(0.25)


def testKellyCriterionEndpointRejectsInvalidWinProbabilityWith422(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/sizing/kelly-criterion",
        {"winProbability": 1.5, "winLossPayoutRatio": 1.5},
    )
    assert statusCode == 422
    assert "errorMessage" in responseBody


def testFactorRiskEndpointHandWorkedTwoFactorTwoHolding(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/portfolio/factor-risk",
        {
            "holdings": [
                {"symbol": "DEMO-EQ", "portfolioWeight": 0.6, "factorExposuresByName": {"marketBeta": 1.2, "size": 0.5}},
                {"symbol": "SIM-AAPL", "portfolioWeight": 0.4, "factorExposuresByName": {"marketBeta": 0.8, "size": -0.3}},
            ],
            "factorReturnsByName": {"marketBeta": 0.02, "size": 0.01},
            "actualOrExpectedPortfolioReturn": 0.03,
        },
    )
    assert statusCode == 200
    # hand-worked: marketBeta = 0.6*1.2 + 0.4*0.8 = 1.04; size = 0.6*0.5 + 0.4*-0.3 = 0.18
    assert responseBody["portfolioExposureByFactor"]["marketBeta"] == pytest.approx(1.04)
    assert responseBody["portfolioExposureByFactor"]["size"] == pytest.approx(0.18)
    # hand-worked: totalFactorContribution = 1.04*0.02 + 0.18*0.01 = 0.0226
    assert responseBody["totalFactorContribution"] == pytest.approx(0.0226)
    # hand-worked: idiosyncratic = 0.03 - 0.0226 = 0.0074
    assert responseBody["idiosyncraticReturn"] == pytest.approx(0.0074)


def testFactorRiskEndpointRejectsMissingFactorReturnWith422(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/portfolio/factor-risk",
        {
            "holdings": [
                {"symbol": "A", "portfolioWeight": 1.0, "factorExposuresByName": {"marketBeta": 1.0, "size": 0.2}},
            ],
            "factorReturnsByName": {"marketBeta": 0.02},
            "actualOrExpectedPortfolioReturn": 0.02,
        },
    )
    assert statusCode == 422
    assert "errorMessage" in responseBody


def testFactorRiskEndpointRejectsMalformedBodyWith400(runningServerBaseUrl):
    statusCode, responseBody = postJson(runningServerBaseUrl, "/portfolio/factor-risk", {"holdings": []})
    assert statusCode == 400
    assert "errorMessage" in responseBody


def testLatencyBenchmarkEndpointHandWorkedPercentiles(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/latency/benchmark",
        {
            "roundTripTimeSamplesInMillisecondsByVenue": {
                "VENUE-A": [1, 2, 3, 4, 5, 6, 7, 8, 9, 10],
            },
            "bucketCount": 5,
        },
    )
    assert statusCode == 200
    # hand-worked nearest-rank: n=10, p50 index=floor(0.5*10)=5 -> value 6.0
    assert responseBody["VENUE-A"]["p50Milliseconds"] == pytest.approx(6.0)
    assert responseBody["VENUE-A"]["maximumMilliseconds"] == pytest.approx(10.0)
    assert responseBody["VENUE-A"]["sampleCount"] == 10
    assert len(responseBody["VENUE-A"]["histogramBuckets"]) == 5


def testLatencyBenchmarkEndpointComparesMultipleVenues(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/latency/benchmark",
        {
            "roundTripTimeSamplesInMillisecondsByVenue": {
                "VENUE-A": [1.0, 2.0, 3.0],
                "VENUE-B": [10.0, 20.0, 30.0],
            }
        },
    )
    assert statusCode == 200
    assert set(responseBody.keys()) == {"VENUE-A", "VENUE-B"}
    assert responseBody["VENUE-B"]["maximumMilliseconds"] > responseBody["VENUE-A"]["maximumMilliseconds"]


def testLatencyBenchmarkEndpointRejectsEmptyVenueDictWith422(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl, "/latency/benchmark", {"roundTripTimeSamplesInMillisecondsByVenue": {}}
    )
    assert statusCode == 422
    assert "errorMessage" in responseBody


def testLatencyBenchmarkEndpointRejectsMalformedBodyWith400(runningServerBaseUrl):
    statusCode, responseBody = postJson(runningServerBaseUrl, "/latency/benchmark", {})
    assert statusCode == 400
    assert "errorMessage" in responseBody


def testStockSplitAdjustmentEndpointHandWorkedTwoForOne(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/options/corporate-action/split-adjustment",
        {
            "symbol": "DEMO-EQ",
            "strikePrice": 100.0,
            "quantity": 10.0,
            "exerciseStyle": "AMERICAN",
            "contractSide": "CALL",
            "splitRatio": 2.0,
        },
    )
    assert statusCode == 200
    # hand-worked: newStrike = 100/2 = 50.0; newQuantity = 10*2 = 20.0
    assert responseBody["adjustedStrikePrice"] == pytest.approx(50.0)
    assert responseBody["adjustedQuantity"] == pytest.approx(20.0)
    assert responseBody["notionalExposureIsPreserved"] is True


def testStockSplitAdjustmentEndpointRejectsNonPositiveSplitRatioWith422(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/options/corporate-action/split-adjustment",
        {
            "symbol": "DEMO-EQ",
            "strikePrice": 100.0,
            "quantity": 10.0,
            "exerciseStyle": "AMERICAN",
            "contractSide": "CALL",
            "splitRatio": 0.0,
        },
    )
    assert statusCode == 422
    assert "errorMessage" in responseBody


def testEarlyExerciseRiskEndpointHandWorkedFlagged(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/options/corporate-action/early-exercise-risk",
        {
            "symbol": "DEMO-EQ",
            "strikePrice": 100.0,
            "quantity": 1.0,
            "exerciseStyle": "AMERICAN",
            "contractSide": "CALL",
            "underlyingSpotPrice": 110.0,
            "callMarketPrice": 10.5,
            "dividendAmount": 1.0,
        },
    )
    assert statusCode == 200
    # hand-worked: intrinsic=10.0, timeValue=0.5, dividend 1.0 > 0.5 -> flagged
    assert responseBody["intrinsicValue"] == pytest.approx(10.0)
    assert responseBody["callTimeValue"] == pytest.approx(0.5)
    assert responseBody["isFlaggedForEarlyExerciseRisk"] is True


def testEarlyExerciseRiskEndpointEuropeanNeverFlagged(runningServerBaseUrl):
    statusCode, responseBody = postJson(
        runningServerBaseUrl,
        "/options/corporate-action/early-exercise-risk",
        {
            "symbol": "DEMO-EQ",
            "strikePrice": 100.0,
            "quantity": 1.0,
            "exerciseStyle": "EUROPEAN",
            "contractSide": "CALL",
            "underlyingSpotPrice": 200.0,
            "callMarketPrice": 1.0,
            "dividendAmount": 1000.0,
        },
    )
    assert statusCode == 200
    assert responseBody["isFlaggedForEarlyExerciseRisk"] is False
    assert "EUROPEAN" in responseBody["reason"]


def testEarlyExerciseRiskEndpointRejectsMalformedBodyWith400(runningServerBaseUrl):
    statusCode, responseBody = postJson(runningServerBaseUrl, "/options/corporate-action/early-exercise-risk", {})
    assert statusCode == 400
    assert "errorMessage" in responseBody
