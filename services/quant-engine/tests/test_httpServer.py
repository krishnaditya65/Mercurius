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
