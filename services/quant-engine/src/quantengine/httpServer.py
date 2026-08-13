"""A real HTTP service wrapper around blackScholesOptionPricer.py.

See ARCHITECTURE.md §6, §8: this is explicitly a RESEARCH-TIER service —
fine for on-demand pricing/Greeks/IV requests at human-perceptible
latency, NOT for the real-time arbitrage scanner running across
thousands of contracts per second (that hot path needs a Rust port, per
the module docstring this wraps). Every request here does one pure,
stateless computation — no shared mutable state, so `ThreadingHTTPServer`
needs no locking anywhere in this file.

Deliberately stdlib-only (`http.server`, `json`), matching this
codebase's convention elsewhere (see matching-engine's and market-data's
own hand-rolled TCP/HTTP bridges in Rust) of not reaching for a framework
dependency until there's a real reason to.

Naming convention: long, descriptive camelCase identifiers throughout —
overrides PEP 8's snake_case, per project convention (see the
mercurius-naming-convention memory).
"""

from __future__ import annotations

import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from quantengine.blackScholesOptionPricer import (
    BlackScholesInputParameters,
    calculateBlackScholesCallOptionPrice,
    calculateBlackScholesPutOptionPrice,
    calculateOptionGreeks,
    solveImpliedVolatilityFromMarketPrice,
)

QUANT_ENGINE_HTTP_LISTEN_ADDRESS = ("127.0.0.1", 8085)


def buildInputParametersFromRequestBody(requestBody: dict) -> BlackScholesInputParameters:
    """Raises `KeyError`/`TypeError` on a malformed body — callers catch
    and turn that into a 400, exactly like a missing/wrong-typed field in
    any other service's JSON request handling in this repo.
    """
    return BlackScholesInputParameters(
        underlyingSpotPrice=float(requestBody["underlyingSpotPrice"]),
        optionStrikePrice=float(requestBody["optionStrikePrice"]),
        annualizedRiskFreeInterestRate=float(requestBody["annualizedRiskFreeInterestRate"]),
        annualizedVolatility=float(requestBody["annualizedVolatility"]),
        timeToExpiryInYears=float(requestBody["timeToExpiryInYears"]),
    )


class QuantEngineRequestHandler(BaseHTTPRequestHandler):
    # Quiets BaseHTTPRequestHandler's default per-request stderr log line
    # — the other services in this repo don't log every request either.
    def log_message(self, format: str, *args) -> None:  # noqa: A002 (stdlib signature)
        pass

    def do_GET(self) -> None:  # noqa: N802 (stdlib method name)
        if self.path == "/health":
            self._writeJsonResponse(200, {"status": "ok"})
            return
        self._writeJsonResponse(404, {"errorMessage": f"no GET route for {self.path}"})

    def do_POST(self) -> None:  # noqa: N802 (stdlib method name)
        contentLength = int(self.headers.get("Content-Length", 0))
        rawRequestBody = self.rfile.read(contentLength) if contentLength > 0 else b"{}"

        try:
            requestBody = json.loads(rawRequestBody)
        except json.JSONDecodeError as decodeError:
            self._writeJsonResponse(400, {"errorMessage": f"malformed JSON body: {decodeError}"})
            return

        if self.path == "/options/price":
            self._handlePriceAndGreeksRequest(requestBody)
        elif self.path == "/options/implied-volatility":
            self._handleImpliedVolatilityRequest(requestBody)
        else:
            self._writeJsonResponse(404, {"errorMessage": f"no POST route for {self.path}"})

    def _handlePriceAndGreeksRequest(self, requestBody: dict) -> None:
        """`POST /options/price` — body is a `BlackScholesInputParameters`
        shape plus `isCallOptionNotPut`. Returns the theoretical price AND
        all four Greeks in one response, since a caller pricing a contract
        almost always wants both (FEATURES.md §22's real-time Greeks
        differentiator).
        """
        try:
            inputParameters = buildInputParametersFromRequestBody(requestBody)
            isCallOptionNotPut = bool(requestBody["isCallOptionNotPut"])
        except (KeyError, TypeError, ValueError) as validationError:
            self._writeJsonResponse(400, {"errorMessage": f"invalid request body: {validationError}"})
            return

        try:
            theoreticalPrice = (
                calculateBlackScholesCallOptionPrice(inputParameters)
                if isCallOptionNotPut
                else calculateBlackScholesPutOptionPrice(inputParameters)
            )
            greeks = calculateOptionGreeks(inputParameters, isCallOptionNotPut)
        except ValueError as pricingError:
            # e.g. non-positive timeToExpiryInYears/annualizedVolatility —
            # a business-level rejection, not a malformed request.
            self._writeJsonResponse(422, {"errorMessage": str(pricingError)})
            return

        self._writeJsonResponse(
            200,
            {
                "theoreticalPriceInMinorUnits": theoreticalPrice,
                "delta": greeks.delta,
                "gamma": greeks.gamma,
                "vegaPerOnePercentVolatilityChange": greeks.vegaPerOnePercentVolatilityChange,
                "thetaPerCalendarDay": greeks.thetaPerCalendarDay,
            },
        )

    def _handleImpliedVolatilityRequest(self, requestBody: dict) -> None:
        """`POST /options/implied-volatility` — body carries
        `observedMarketPrice`, `isCallOptionNotPut`, and the same
        parameter shape as `/options/price` MINUS `annualizedVolatility`
        (that's exactly what's being solved for, so it's ignored if
        present rather than required).
        """
        try:
            observedMarketPrice = float(requestBody["observedMarketPrice"])
            isCallOptionNotPut = bool(requestBody["isCallOptionNotPut"])
            inputParametersWithoutVolatility = BlackScholesInputParameters(
                underlyingSpotPrice=float(requestBody["underlyingSpotPrice"]),
                optionStrikePrice=float(requestBody["optionStrikePrice"]),
                annualizedRiskFreeInterestRate=float(requestBody["annualizedRiskFreeInterestRate"]),
                # Placeholder — solveImpliedVolatilityFromMarketPrice never
                # reads this field off inputParametersWithoutVolatility; it
                # builds its own candidate each iteration. Present only
                # because BlackScholesInputParameters requires it.
                annualizedVolatility=1.0,
                timeToExpiryInYears=float(requestBody["timeToExpiryInYears"]),
            )
        except (KeyError, TypeError, ValueError) as validationError:
            self._writeJsonResponse(400, {"errorMessage": f"invalid request body: {validationError}"})
            return

        try:
            impliedVolatility = solveImpliedVolatilityFromMarketPrice(
                observedMarketPrice, inputParametersWithoutVolatility, isCallOptionNotPut
            )
        except ValueError as solveError:
            # Didn't converge, or vega collapsed — a business-level
            # failure of the numerical method, not a malformed request.
            self._writeJsonResponse(422, {"errorMessage": str(solveError)})
            return

        self._writeJsonResponse(200, {"impliedVolatility": impliedVolatility})

    def _writeJsonResponse(self, statusCode: int, responseBody: dict) -> None:
        responseBytes = json.dumps(responseBody).encode("utf-8")
        self.send_response(statusCode)
        self.send_header("Content-Type", "application/json")
        # Permissive CORS, same rationale (and same "wrong once real auth
        # exists" caveat) as oms-gateway's withPermissiveCorsForDevelopment
        # and market-data's httpQueryServer.rs — lets apps/web call this
        # directly from a browser during development.
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Content-Length", str(len(responseBytes)))
        self.end_headers()
        self.wfile.write(responseBytes)


def runQuantEngineHttpServer() -> None:
    httpServer = ThreadingHTTPServer(QUANT_ENGINE_HTTP_LISTEN_ADDRESS, QuantEngineRequestHandler)
    host, port = QUANT_ENGINE_HTTP_LISTEN_ADDRESS
    print(f"quant-engine listening on {host}:{port} (GET /health, POST /options/price, POST /options/implied-volatility)")
    httpServer.serve_forever()


if __name__ == "__main__":
    runQuantEngineHttpServer()
