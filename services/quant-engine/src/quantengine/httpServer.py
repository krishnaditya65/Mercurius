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
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from quantengine.arbitrageScanner import scanForTheoreticalVersusLivePriceDeviation
from quantengine.blackScholesOptionPricer import (
    BlackScholesInputParameters,
    calculateBlackScholesCallOptionPrice,
    calculateBlackScholesPutOptionPrice,
    calculateOptionGreeks,
    solveImpliedVolatilityFromMarketPrice,
)
from quantengine.correlationMatrixEngine import (
    buildPairwiseCorrelationMatrix,
    findPairsTradingCandidatePairsAboveCorrelationThreshold,
)
from quantengine.esgScoringEngine import (
    EsgScreeningCriteria,
    screenCandidateSymbolsAgainstEsgCriteria,
)
from quantengine.garchVolatilityForecaster import (
    calculateExpectedIntradayRangeFromForecastVariance,
    fitGarchOneOneParametersByGridSearchQuasiMaximumLikelihood,
)
from quantengine.marketMakingSandbox import MarketMakingSandbox, QuoteRejectedError, QuoteSide
from quantengine.riskStatistics import (
    calculateAnnualizedSharpeRatio,
    calculateAnnualizedSortinoRatio,
    calculateMaximumDrawdownFromReturns,
)
from quantengine.valueAtRiskCalculator import (
    calculateHistoricalValueAtRisk,
    calculateParametricValueAtRisk,
)

QUANT_ENGINE_HTTP_LISTEN_ADDRESS = ("127.0.0.1", 8085)

# The market-making sandbox is the ONE piece of shared mutable state this
# HTTP service holds (see marketMakingSandbox.py's own docstring) — every
# other endpoint in this file is a pure, stateless computation per
# request, which is exactly why ThreadingHTTPServer needed zero locking
# before this. This lock guards every sandbox access below.
_marketMakingSandbox = MarketMakingSandbox()
_marketMakingSandboxLock = threading.Lock()


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
        elif self.path == "/risk/statistics":
            self._handleRiskStatisticsRequest(requestBody)
        elif self.path == "/arbitrage/scan":
            self._handleArbitrageScanRequest(requestBody)
        elif self.path == "/volatility/garch-forecast":
            self._handleGarchForecastRequest(requestBody)
        elif self.path == "/correlation/matrix":
            self._handleCorrelationMatrixRequest(requestBody)
        elif self.path == "/risk/value-at-risk":
            self._handleValueAtRiskRequest(requestBody)
        elif self.path == "/market-making/quote":
            self._handleMarketMakingQuoteRequest(requestBody)
        elif self.path == "/market-making/simulate-fill":
            self._handleMarketMakingSimulateFillRequest(requestBody)
        elif self.path == "/market-making/inventory":
            self._handleMarketMakingInventoryRequest(requestBody)
        elif self.path == "/esg/screen":
            self._handleEsgScreenRequest(requestBody)
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

    def _handleRiskStatisticsRequest(self, requestBody: dict) -> None:
        """`POST /risk/statistics` — body carries `periodicReturns` (a
        list of floats), `periodicRiskFreeRate`, and `periodsPerYear`.
        Returns the annualized Sharpe ratio, annualized Sortino ratio, and
        max drawdown (computed over the compounded equity curve implied
        by `periodicReturns`) in one response — see
        `quantengine/riskStatistics.py` (FEATURES.md §6).
        """
        try:
            periodicReturns = [float(oneReturn) for oneReturn in requestBody["periodicReturns"]]
            periodicRiskFreeRate = float(requestBody["periodicRiskFreeRate"])
            periodsPerYear = float(requestBody["periodsPerYear"])
        except (KeyError, TypeError, ValueError) as validationError:
            self._writeJsonResponse(400, {"errorMessage": f"invalid request body: {validationError}"})
            return

        try:
            sharpeRatio = calculateAnnualizedSharpeRatio(periodicReturns, periodicRiskFreeRate, periodsPerYear)
            sortinoRatio = calculateAnnualizedSortinoRatio(periodicReturns, periodicRiskFreeRate, periodsPerYear)
            maxDrawdown = calculateMaximumDrawdownFromReturns(periodicReturns)
        except ValueError as statisticsError:
            # e.g. zero-variance return series, empty series, or no
            # downside periods for Sortino — a business-level rejection.
            self._writeJsonResponse(422, {"errorMessage": str(statisticsError)})
            return

        self._writeJsonResponse(
            200,
            {
                "annualizedSharpeRatio": sharpeRatio,
                "annualizedSortinoRatio": sortinoRatio,
                "maximumDrawdownFraction": maxDrawdown.maximumDrawdownFraction,
                "maximumDrawdownPeakEquityValue": maxDrawdown.peakEquityValue,
                "maximumDrawdownTroughEquityValue": maxDrawdown.troughEquityValue,
            },
        )

    def _handleArbitrageScanRequest(self, requestBody: dict) -> None:
        """`POST /arbitrage/scan` — body carries `theoreticalFairPrice`,
        `liveMarketPrice`, and `deviationThresholdPercentage`. Returns the
        full `PriceDeviationAlert` — see `quantengine/arbitrageScanner.py`
        (FEATURES.md §6). Doesn't compute the theoretical price itself;
        callers get that from `/options/price` (or their own cash-and-
        carry calculation) first.
        """
        try:
            theoreticalFairPrice = float(requestBody["theoreticalFairPrice"])
            liveMarketPrice = float(requestBody["liveMarketPrice"])
            deviationThresholdPercentage = float(requestBody["deviationThresholdPercentage"])
        except (KeyError, TypeError, ValueError) as validationError:
            self._writeJsonResponse(400, {"errorMessage": f"invalid request body: {validationError}"})
            return

        try:
            alert = scanForTheoreticalVersusLivePriceDeviation(
                theoreticalFairPrice, liveMarketPrice, deviationThresholdPercentage
            )
        except ValueError as scanError:
            self._writeJsonResponse(422, {"errorMessage": str(scanError)})
            return

        self._writeJsonResponse(
            200,
            {
                "theoreticalFairPrice": alert.theoreticalFairPrice,
                "liveMarketPrice": alert.liveMarketPrice,
                "absoluteDeviation": alert.absoluteDeviation,
                "percentageDeviation": alert.percentageDeviation,
                "deviationThresholdPercentage": alert.deviationThresholdPercentage,
                "isAlertTriggered": alert.isAlertTriggered,
                "isLiveOverpricedRelativeToTheoretical": alert.isLiveOverpricedRelativeToTheoretical,
            },
        )

    def _handleGarchForecastRequest(self, requestBody: dict) -> None:
        """`POST /volatility/garch-forecast` — body carries
        `periodicReturns` (a daily/periodic return series),
        `currentPrice`, and an optional `zScoreMultiple`. Fits GARCH(1,1)
        via `garchVolatilityForecaster`'s grid-search quasi-MLE, and
        returns the fitted parameters, the one-step-ahead forecast
        variance/volatility, and the resulting "Expected Intraday Range"
        (FEATURES.md §6).
        """
        try:
            periodicReturns = [float(oneReturn) for oneReturn in requestBody["periodicReturns"]]
            currentPrice = float(requestBody["currentPrice"])
            zScoreMultiple = float(requestBody.get("zScoreMultiple", 1.645))
        except (KeyError, TypeError, ValueError) as validationError:
            self._writeJsonResponse(400, {"errorMessage": f"invalid request body: {validationError}"})
            return

        try:
            fitResult = fitGarchOneOneParametersByGridSearchQuasiMaximumLikelihood(periodicReturns)
            forecastVariance = fitResult.conditionalVarianceSeries[-1]
            rangeResult = calculateExpectedIntradayRangeFromForecastVariance(
                forecastVariance, currentPrice, zScoreMultiple
            )
        except ValueError as garchError:
            self._writeJsonResponse(422, {"errorMessage": str(garchError)})
            return

        self._writeJsonResponse(
            200,
            {
                "omega": fitResult.parameters.omega,
                "alphaArchCoefficient": fitResult.parameters.alphaArchCoefficient,
                "betaGarchCoefficient": fitResult.parameters.betaGarchCoefficient,
                "logLikelihoodAtFittedParameters": fitResult.logLikelihoodAtFittedParameters,
                "forecastNextPeriodVariance": forecastVariance,
                "forecastNextPeriodVolatility": rangeResult.forecastNextPeriodVolatility,
                "expectedRangeLowerBound": rangeResult.expectedRangeLowerBound,
                "expectedRangeUpperBound": rangeResult.expectedRangeUpperBound,
            },
        )

    def _handleCorrelationMatrixRequest(self, requestBody: dict) -> None:
        """`POST /correlation/matrix` — body carries
        `returnSeriesBySymbol` (a dict of symbol -> aligned return
        series) and `minimumAbsoluteCorrelationThreshold`. Returns the
        full pairwise Pearson correlation matrix AND the candidate-pairs
        filter above the threshold (FEATURES.md §6).
        """
        try:
            returnSeriesBySymbol = {
                symbol: [float(v) for v in series]
                for symbol, series in requestBody["returnSeriesBySymbol"].items()
            }
            minimumAbsoluteCorrelationThreshold = float(
                requestBody.get("minimumAbsoluteCorrelationThreshold", 0.7)
            )
        except (KeyError, TypeError, ValueError) as validationError:
            self._writeJsonResponse(400, {"errorMessage": f"invalid request body: {validationError}"})
            return

        try:
            matrix = buildPairwiseCorrelationMatrix(returnSeriesBySymbol)
            candidatePairs = findPairsTradingCandidatePairsAboveCorrelationThreshold(
                matrix, minimumAbsoluteCorrelationThreshold
            )
        except ValueError as correlationError:
            self._writeJsonResponse(422, {"errorMessage": str(correlationError)})
            return

        self._writeJsonResponse(
            200,
            {
                "symbolsInOrder": matrix.symbolsInOrder,
                "correlationBySymbolPair": [
                    {"firstSymbol": pair[0], "secondSymbol": pair[1], "correlationCoefficient": correlation}
                    for pair, correlation in matrix.correlationBySymbolPair.items()
                ],
                "candidatePairs": [
                    {
                        "firstSymbol": candidate.firstSymbol,
                        "secondSymbol": candidate.secondSymbol,
                        "correlationCoefficient": candidate.correlationCoefficient,
                    }
                    for candidate in candidatePairs
                ],
            },
        )

    def _handleValueAtRiskRequest(self, requestBody: dict) -> None:
        """`POST /risk/value-at-risk` — body carries `periodicReturns`
        and `confidenceLevel`. Returns BOTH historical VaR and parametric
        (variance-covariance) VaR for the same series (FEATURES.md §6).
        """
        try:
            periodicReturns = [float(oneReturn) for oneReturn in requestBody["periodicReturns"]]
            confidenceLevel = float(requestBody["confidenceLevel"])
        except (KeyError, TypeError, ValueError) as validationError:
            self._writeJsonResponse(400, {"errorMessage": f"invalid request body: {validationError}"})
            return

        try:
            historicalVar = calculateHistoricalValueAtRisk(periodicReturns, confidenceLevel)
            parametricVar = calculateParametricValueAtRisk(periodicReturns, confidenceLevel)
        except ValueError as varError:
            self._writeJsonResponse(422, {"errorMessage": str(varError)})
            return

        self._writeJsonResponse(
            200,
            {
                "confidenceLevel": confidenceLevel,
                "historicalValueAtRisk": historicalVar,
                "parametricValueAtRisk": parametricVar,
            },
        )

    def _handleMarketMakingQuoteRequest(self, requestBody: dict) -> None:
        """`POST /market-making/quote` — body carries `symbol`,
        `maximumAbsoluteInventory` (registers/updates the symbol's risk
        limit), `bidPrice`, `bidQuantity`, `askPrice`, `askQuantity`.
        Registers the symbol if new, then submits the two-sided quote —
        REJECTS with 422 if a full fill on either side would breach the
        inventory limit (FEATURES.md §7).
        """
        try:
            symbol = str(requestBody["symbol"])
            maximumAbsoluteInventory = float(requestBody["maximumAbsoluteInventory"])
            bidPrice = float(requestBody["bidPrice"])
            bidQuantity = float(requestBody["bidQuantity"])
            askPrice = float(requestBody["askPrice"])
            askQuantity = float(requestBody["askQuantity"])
        except (KeyError, TypeError, ValueError) as validationError:
            self._writeJsonResponse(400, {"errorMessage": f"invalid request body: {validationError}"})
            return

        try:
            with _marketMakingSandboxLock:
                _marketMakingSandbox.registerSymbolWithInventoryLimit(symbol, maximumAbsoluteInventory)
                quote = _marketMakingSandbox.submitTwoSidedQuote(
                    symbol, bidPrice, bidQuantity, askPrice, askQuantity
                )
                inventoryPosition = _marketMakingSandbox.getInventoryPosition(symbol)
        except (QuoteRejectedError, ValueError) as quoteError:
            self._writeJsonResponse(422, {"errorMessage": str(quoteError)})
            return

        self._writeJsonResponse(
            200,
            {
                "symbol": symbol,
                "bidPrice": quote.bidPrice,
                "bidQuantity": quote.bidQuantity,
                "askPrice": quote.askPrice,
                "askQuantity": quote.askQuantity,
                "inventoryPosition": inventoryPosition,
            },
        )

    def _handleMarketMakingSimulateFillRequest(self, requestBody: dict) -> None:
        """`POST /market-making/simulate-fill` — body carries `symbol`,
        `takerSide` ("BID" or "ASK"), `quantity`. Simulates a taker order
        crossing the sandbox's current quote and returns the actual
        filled quantity plus the resulting inventory position
        (FEATURES.md §7).
        """
        try:
            symbol = str(requestBody["symbol"])
            takerSide = QuoteSide(str(requestBody["takerSide"]).upper())
            quantity = float(requestBody["quantity"])
        except (KeyError, TypeError, ValueError) as validationError:
            self._writeJsonResponse(400, {"errorMessage": f"invalid request body: {validationError}"})
            return

        try:
            with _marketMakingSandboxLock:
                filledQuantity = _marketMakingSandbox.simulateTakerOrderCrossingQuote(symbol, takerSide, quantity)
                inventoryPosition = _marketMakingSandbox.getInventoryPosition(symbol)
        except KeyError as unknownSymbolError:
            self._writeJsonResponse(404, {"errorMessage": str(unknownSymbolError)})
            return
        except ValueError as fillError:
            self._writeJsonResponse(422, {"errorMessage": str(fillError)})
            return

        self._writeJsonResponse(
            200, {"symbol": symbol, "filledQuantity": filledQuantity, "inventoryPosition": inventoryPosition}
        )

    def _handleMarketMakingInventoryRequest(self, requestBody: dict) -> None:
        """`POST /market-making/inventory` — body carries `symbol`.
        Returns the current inventory position and quote for that symbol.
        POST (not GET) purely to keep a JSON-body-in / JSON-body-out
        convention consistent with every other route in this file.
        """
        try:
            symbol = str(requestBody["symbol"])
        except (KeyError, TypeError) as validationError:
            self._writeJsonResponse(400, {"errorMessage": f"invalid request body: {validationError}"})
            return

        try:
            with _marketMakingSandboxLock:
                symbolState = _marketMakingSandbox.getSymbolState(symbol)
                currentQuote = symbolState.currentQuote
                responseBody = {
                    "symbol": symbol,
                    "inventoryPosition": symbolState.inventoryPosition,
                    "maximumAbsoluteInventory": symbolState.maximumAbsoluteInventory,
                    "currentQuote": (
                        {
                            "bidPrice": currentQuote.bidPrice,
                            "bidQuantity": currentQuote.bidQuantity,
                            "askPrice": currentQuote.askPrice,
                            "askQuantity": currentQuote.askQuantity,
                        }
                        if currentQuote is not None
                        else None
                    ),
                }
        except KeyError as unknownSymbolError:
            self._writeJsonResponse(404, {"errorMessage": str(unknownSymbolError)})
            return

        self._writeJsonResponse(200, responseBody)

    def _handleEsgScreenRequest(self, requestBody: dict) -> None:
        """`POST /esg/screen` — body carries `candidateSymbols` (a list of
        symbols) and an optional criteria object: `minimumCompositeEsgScore`,
        `minimumEnvironmentalScore`, `minimumSocialScore`,
        `minimumGovernanceScore`, `excludedControversialSectorFlags` (a
        list of sector flag strings, e.g. ["TOBACCO", "THERMAL_COAL"]).
        Returns candidates ranked descending by composite ESG score
        (`rankedResults`), symbols that failed at least one criterion
        (`excludedSymbols`), and candidate symbols with no illustrative
        ESG profile at all (`unknownSymbols`) — see
        `quantengine/esgScoringEngine.py` (FEATURES.md §17).

        **The underlying per-symbol ESG dataset is illustrative/fabricated
        fixture data — see that module's docstring — the scoring math and
        screening logic below are real.**
        """
        try:
            candidateSymbols = [str(symbol) for symbol in requestBody["candidateSymbols"]]
            criteria = EsgScreeningCriteria(
                minimumCompositeEsgScore=(
                    float(requestBody["minimumCompositeEsgScore"])
                    if requestBody.get("minimumCompositeEsgScore") is not None
                    else None
                ),
                minimumEnvironmentalScore=(
                    float(requestBody["minimumEnvironmentalScore"])
                    if requestBody.get("minimumEnvironmentalScore") is not None
                    else None
                ),
                minimumSocialScore=(
                    float(requestBody["minimumSocialScore"])
                    if requestBody.get("minimumSocialScore") is not None
                    else None
                ),
                minimumGovernanceScore=(
                    float(requestBody["minimumGovernanceScore"])
                    if requestBody.get("minimumGovernanceScore") is not None
                    else None
                ),
                excludedControversialSectorFlags=frozenset(
                    str(flag) for flag in requestBody.get("excludedControversialSectorFlags", [])
                ),
            )
        except (KeyError, TypeError, ValueError) as validationError:
            self._writeJsonResponse(400, {"errorMessage": f"invalid request body: {validationError}"})
            return

        result = screenCandidateSymbolsAgainstEsgCriteria(candidateSymbols, criteria)

        self._writeJsonResponse(
            200,
            {
                "rankedResults": [
                    {
                        "symbol": profile.symbol,
                        "environmentalScore": profile.environmentalScore,
                        "socialScore": profile.socialScore,
                        "governanceScore": profile.governanceScore,
                        "compositeEsgScore": profile.compositeEsgScore,
                        "controversialSectorFlags": sorted(profile.controversialSectorFlags),
                    }
                    for profile in result.rankedProfiles
                ],
                "excludedSymbols": result.excludedSymbols,
                "unknownSymbols": result.unknownSymbols,
            },
        )

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
    print(
        f"quant-engine listening on {host}:{port} (GET /health, POST /options/price, "
        "POST /options/implied-volatility, POST /risk/statistics, POST /arbitrage/scan, "
        "POST /volatility/garch-forecast, POST /correlation/matrix, POST /risk/value-at-risk, "
        "POST /market-making/quote, POST /market-making/simulate-fill, POST /market-making/inventory, "
        "POST /esg/screen)"
    )
    httpServer.serve_forever()


if __name__ == "__main__":
    runQuantEngineHttpServer()
