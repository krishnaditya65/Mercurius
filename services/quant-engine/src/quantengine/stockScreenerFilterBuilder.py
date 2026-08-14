"""Stock/fund screener with a custom compound filter builder (fundamental
+ technical criteria) and saved-screen persistence. See FEATURES.md §16
("AI, Data & Research") — "Stock/fund screener with custom filter builder
(fundamental + technical criteria, saved screens)".

============================================================================
READ THIS BEFORE TREATING ANY NUMBER FROM THIS MODULE AS REAL MARKET DATA
============================================================================
`ILLUSTRATIVE_INSTRUMENT_UNIVERSE` below is a STATIC, HAND-FABRICATED set of
per-symbol fundamental fields (P/E ratio, market cap, dividend yield,
sector) plus a synthetic daily closing-price series per symbol. None of it
is sourced from a real fundamentals vendor or a real price feed — it is
illustrative fixture data, exactly like `esgScoringEngine.py`'s ESG dataset
and `illustrativeSentimentTradingHook.py`'s toy lexicon elsewhere in this
service. The synthetic price series are deterministic (fixed, hand-written
sequences, not `random`), so tests and demo runs are fully reproducible.

What IS real and correctly implemented here:
  - The TECHNICAL INDICATOR MATH (`calculateSimpleMovingAverage`,
    `calculateRelativeStrengthIndex`) — standard, textbook formulas
    (arithmetic simple moving average; Wilder's original 14-period RSI
    smoothing method), computed genuinely from each symbol's price series,
    not invented/hardcoded numbers.
  - The COMPOUND FILTER EXPRESSION ENGINE (`evaluateFilterExpression`) — a
    real recursive AND/OR/comparison evaluator over an arbitrarily nested
    boolean expression tree, exactly like a real screener's query builder.
  - The SAVED-SCREEN STORE (`SavedScreenStore`) — a real, working
    save/list/get/delete store. It is IN-MEMORY BY DEFAULT (screens do not
    survive a process restart); passing a `persistenceFilePath` makes it
    also durable to a simple JSON file on every mutation. There is no
    database here — this is intentionally the simplest persistence that
    satisfies "saved screens survive across calls within a running
    process, and optionally across restarts too."

What is NOT real: the underlying per-symbol fundamentals and price series
themselves. Wiring this module to a real fundamentals/price vendor feed is
a documented future integration, not attempted here.

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

import json
import os
from dataclasses import dataclass, field
from typing import Any


# --- Technical indicator math (real formulas, applied to illustrative price series) ---


def calculateSimpleMovingAverage(closingPrices: list[float], windowLength: int) -> float:
    """Arithmetic simple moving average of the LAST `windowLength` closing
    prices in `closingPrices` (i.e. the most recent `windowLength` bars).
    Raises `ValueError` if `windowLength` is not a positive integer or
    exceeds the number of available prices.
    """
    if windowLength <= 0:
        raise ValueError("windowLength must be a positive integer")
    if len(closingPrices) < windowLength:
        raise ValueError(
            f"need at least {windowLength} closing prices to compute a {windowLength}-period "
            f"simple moving average, got {len(closingPrices)}"
        )
    windowPrices = closingPrices[-windowLength:]
    return sum(windowPrices) / windowLength


def calculateRelativeStrengthIndex(closingPrices: list[float], period: int = 14) -> float:
    """Standard Wilder's Relative Strength Index over `closingPrices`
    (oldest-first order), using Wilder's original smoothing method:

        firstAverageGain = mean(gains over the first `period` price changes)
        firstAverageLoss = mean(losses over the first `period` price changes)
        for every subsequent price change:
            averageGain = (previousAverageGain * (period - 1) + currentGain) / period
            averageLoss = (previousAverageLoss * (period - 1) + currentLoss) / period
        RS  = averageGain / averageLoss
        RSI = 100 - (100 / (1 + RS))

    where "gain"/"loss" for one price change is `max(0, delta)` /
    `max(0, -delta)` respectively. This is the textbook Wilder smoothing
    formula (the same recursive-EMA-style smoothing convention as
    `garchVolatilityForecaster.py`'s GARCH recursion elsewhere in this
    service), NOT a simple unweighted average of gains/losses over a
    rolling window.

    Requires at least `period + 1` closing prices (period changes).
    Raises `ValueError` if there isn't enough data, or if every price
    change in the whole series is exactly zero (average loss is zero,
    RS is undefined) — in that degenerate case RSI is conventionally 100
    (price never fell), which this function returns explicitly rather
    than dividing by zero.
    """
    if period <= 0:
        raise ValueError("period must be a positive integer")
    if len(closingPrices) < period + 1:
        raise ValueError(
            f"need at least {period + 1} closing prices to compute a {period}-period RSI, "
            f"got {len(closingPrices)}"
        )

    priceChanges = [closingPrices[i] - closingPrices[i - 1] for i in range(1, len(closingPrices))]

    firstWindowChanges = priceChanges[:period]
    averageGain = sum(max(0.0, change) for change in firstWindowChanges) / period
    averageLoss = sum(max(0.0, -change) for change in firstWindowChanges) / period

    for change in priceChanges[period:]:
        currentGain = max(0.0, change)
        currentLoss = max(0.0, -change)
        averageGain = (averageGain * (period - 1) + currentGain) / period
        averageLoss = (averageLoss * (period - 1) + currentLoss) / period

    if averageLoss == 0.0:
        # No downside at all over the smoothed window — RSI saturates at
        # its maximum value of 100 by definition, rather than raising a
        # division-by-zero error.
        return 100.0

    relativeStrength = averageGain / averageLoss
    return 100.0 - (100.0 / (1.0 + relativeStrength))


@dataclass(frozen=True)
class InstrumentRecord:
    """One symbol's illustrative fundamental fields plus its raw price
    series (technical fields are DERIVED from `closingPrices` on demand by
    `buildScreenableInstrumentSnapshot` below, not stored redundantly).
    """

    symbol: str
    sector: str
    priceToEarningsRatio: float
    marketCapitalizationBillions: float
    dividendYieldPercent: float
    closingPrices: list[float]  # oldest-first; illustrative synthetic series


@dataclass(frozen=True)
class ScreenableInstrumentSnapshot:
    """The flat field set a filter expression can query — fundamentals
    copied straight from `InstrumentRecord`, technical fields computed
    fresh from `InstrumentRecord.closingPrices` via the real indicator math
    above.
    """

    symbol: str
    sector: str
    priceToEarningsRatio: float
    marketCapitalizationBillions: float
    dividendYieldPercent: float
    currentPrice: float
    simpleMovingAverage50Day: float
    relativeStrengthIndex14Day: float

    def asFieldDict(self) -> dict[str, Any]:
        return {
            "symbol": self.symbol,
            "sector": self.sector,
            "priceToEarningsRatio": self.priceToEarningsRatio,
            "marketCapitalizationBillions": self.marketCapitalizationBillions,
            "dividendYieldPercent": self.dividendYieldPercent,
            "currentPrice": self.currentPrice,
            "simpleMovingAverage50Day": self.simpleMovingAverage50Day,
            "relativeStrengthIndex14Day": self.relativeStrengthIndex14Day,
        }


def buildScreenableInstrumentSnapshot(
    record: InstrumentRecord, movingAverageWindowLength: int = 50, relativeStrengthIndexPeriod: int = 14
) -> ScreenableInstrumentSnapshot:
    """Computes the technical fields for one `InstrumentRecord` via the
    real SMA/RSI formulas above and returns the flat, filter-queryable
    snapshot. Raises `ValueError` (propagated from the indicator
    functions) if `record.closingPrices` is too short for the requested
    window/period.
    """
    return ScreenableInstrumentSnapshot(
        symbol=record.symbol,
        sector=record.sector,
        priceToEarningsRatio=record.priceToEarningsRatio,
        marketCapitalizationBillions=record.marketCapitalizationBillions,
        dividendYieldPercent=record.dividendYieldPercent,
        currentPrice=record.closingPrices[-1],
        simpleMovingAverage50Day=calculateSimpleMovingAverage(record.closingPrices, movingAverageWindowLength),
        relativeStrengthIndex14Day=calculateRelativeStrengthIndex(record.closingPrices, relativeStrengthIndexPeriod),
    )


# --- Illustrative, fabricated instrument universe -----------------------
#
# Deterministic hand-written synthetic price series (not `random`), long
# enough (60 bars) to satisfy both the default 50-day SMA and the 14-day
# RSI. Series are constructed with a clear directional bias per symbol
# (steadily rising, steadily falling, choppy/flat) precisely so the
# resulting RSI/SMA readings are predictably high/low/mid for tests — see
# `tests/test_stockScreenerFilterBuilder.py`.


def _buildSteadilyRisingPriceSeries(startingPrice: float, barCount: int, perBarIncrement: float) -> list[float]:
    return [startingPrice + perBarIncrement * barIndex for barIndex in range(barCount)]


def _buildSteadilyFallingPriceSeries(startingPrice: float, barCount: int, perBarDecrement: float) -> list[float]:
    return [startingPrice - perBarDecrement * barIndex for barIndex in range(barCount)]


def _buildChoppyFlatPriceSeries(centerPrice: float, barCount: int, oscillationAmplitude: float) -> list[float]:
    return [
        centerPrice + (oscillationAmplitude if barIndex % 2 == 0 else -oscillationAmplitude)
        for barIndex in range(barCount)
    ]


ILLUSTRATIVE_INSTRUMENT_UNIVERSE: dict[str, InstrumentRecord] = {
    record.symbol: record
    for record in (
        InstrumentRecord(
            symbol="SIM-GROWTHCO",
            sector="TECHNOLOGY",
            priceToEarningsRatio=42.0,
            marketCapitalizationBillions=850.0,
            dividendYieldPercent=0.0,
            closingPrices=_buildSteadilyRisingPriceSeries(100.0, 60, 0.75),
        ),
        InstrumentRecord(
            symbol="SIM-VALUECO",
            sector="FINANCIALS",
            priceToEarningsRatio=9.5,
            marketCapitalizationBillions=120.0,
            dividendYieldPercent=4.2,
            closingPrices=_buildChoppyFlatPriceSeries(50.0, 60, 0.5),
        ),
        InstrumentRecord(
            symbol="SIM-DIVCO",
            sector="UTILITIES",
            priceToEarningsRatio=15.0,
            marketCapitalizationBillions=60.0,
            dividendYieldPercent=5.8,
            closingPrices=_buildChoppyFlatPriceSeries(30.0, 60, 0.2),
        ),
        InstrumentRecord(
            symbol="SIM-DECLINECO",
            sector="ENERGY",
            priceToEarningsRatio=6.0,
            marketCapitalizationBillions=25.0,
            dividendYieldPercent=1.5,
            closingPrices=_buildSteadilyFallingPriceSeries(80.0, 60, 0.6),
        ),
        InstrumentRecord(
            symbol="SIM-MEGACAPCO",
            sector="TECHNOLOGY",
            priceToEarningsRatio=28.0,
            marketCapitalizationBillions=2100.0,
            dividendYieldPercent=0.6,
            closingPrices=_buildSteadilyRisingPriceSeries(300.0, 60, 0.3),
        ),
        InstrumentRecord(
            symbol="SIM-SMALLCAPCO",
            sector="HEALTHCARE",
            priceToEarningsRatio=35.0,
            marketCapitalizationBillions=3.5,
            dividendYieldPercent=0.0,
            closingPrices=_buildChoppyFlatPriceSeries(20.0, 60, 1.0),
        ),
    )
}


# --- Compound filter expression engine -----------------------------------

_COMPARISON_OPERATORS: dict[str, Any] = {
    "<": lambda fieldValue, target: fieldValue < target,
    "<=": lambda fieldValue, target: fieldValue <= target,
    ">": lambda fieldValue, target: fieldValue > target,
    ">=": lambda fieldValue, target: fieldValue >= target,
    "==": lambda fieldValue, target: fieldValue == target,
    "!=": lambda fieldValue, target: fieldValue != target,
    "in": lambda fieldValue, target: fieldValue in target,
    "not_in": lambda fieldValue, target: fieldValue not in target,
}


def evaluateFilterExpression(fieldValues: dict[str, Any], expression: dict[str, Any]) -> bool:
    """Recursively evaluates one filter expression node against
    `fieldValues` (typically `ScreenableInstrumentSnapshot.asFieldDict()`).

    Two node shapes:

    - A COMPARISON LEAF: `{"field": <str>, "operator": <one of "<", "<=",
      ">", ">=", "==", "!=", "in", "not_in">, "value": <any>}`. Evaluates
      `fieldValues[field] <operator> value`. Raises `KeyError` if `field`
      isn't present in `fieldValues`, and `ValueError` for an unknown
      operator — both real validation failures, not silently `False`.
    - A BOOLEAN GROUP: `{"logic": "AND"|"OR", "conditions": [<node>, ...]}`.
      Recursively evaluates every child node and combines with Python's
      `all()`/`any()`. An AND group with zero conditions evaluates `True`
      (vacuous truth, the standard convention); an OR group with zero
      conditions evaluates `False`. Groups may nest arbitrarily deep,
      giving a real compound filter builder ("(sector == TECH AND P/E <
      30) OR (dividendYield >= 4)", etc).

    Raises `ValueError` for a node that is neither shape.
    """
    if "logic" in expression:
        logic = str(expression["logic"]).upper()
        childResults = [
            evaluateFilterExpression(fieldValues, childExpression)
            for childExpression in expression.get("conditions", [])
        ]
        if logic == "AND":
            return all(childResults)
        if logic == "OR":
            return any(childResults)
        raise ValueError(f"unknown boolean group logic {expression['logic']!r} — expected 'AND' or 'OR'")

    if "field" in expression and "operator" in expression:
        fieldName = str(expression["field"])
        if fieldName not in fieldValues:
            raise KeyError(f"filter expression references unknown field {fieldName!r}")
        operatorName = str(expression["operator"])
        if operatorName not in _COMPARISON_OPERATORS:
            raise ValueError(
                f"unknown comparison operator {operatorName!r} — expected one of "
                f"{sorted(_COMPARISON_OPERATORS.keys())}"
            )
        return _COMPARISON_OPERATORS[operatorName](fieldValues[fieldName], expression["value"])

    raise ValueError(
        "filter expression node must be either a comparison leaf ({'field', 'operator', 'value'}) "
        "or a boolean group ({'logic', 'conditions'})"
    )


def runScreenAgainstUniverse(
    filterExpression: dict[str, Any],
    universe: dict[str, InstrumentRecord] | None = None,
) -> list[ScreenableInstrumentSnapshot]:
    """Builds a `ScreenableInstrumentSnapshot` for every instrument in
    `universe` (defaults to `ILLUSTRATIVE_INSTRUMENT_UNIVERSE`) and returns
    the ones that pass `filterExpression`, sorted alphabetically by symbol
    for a deterministic result order. An empty/`{"logic": "AND",
    "conditions": []}` expression legitimately matches every instrument
    (vacuous AND truth, per `evaluateFilterExpression`).
    """
    effectiveUniverse = ILLUSTRATIVE_INSTRUMENT_UNIVERSE if universe is None else universe
    snapshots = [buildScreenableInstrumentSnapshot(record) for record in effectiveUniverse.values()]
    matching = [
        snapshot for snapshot in snapshots if evaluateFilterExpression(snapshot.asFieldDict(), filterExpression)
    ]
    return sorted(matching, key=lambda snapshot: snapshot.symbol)


# --- Saved-screen persistence --------------------------------------------


@dataclass(frozen=True)
class SavedScreen:
    screenName: str
    filterExpression: dict[str, Any]
    description: str = ""


class SavedScreenStore:
    """A real, working save/list/get/delete store for named filter
    expressions ("saved screens").

    IN-MEMORY BY DEFAULT: screens live only for the lifetime of this
    Python process — there is no database backing this class. Pass
    `persistenceFilePath` to ALSO durably persist every mutation
    (save/delete) as a JSON file on disk (one JSON object mapping
    `screenName -> {filterExpression, description}`), which is loaded back
    on construction if the file already exists. This is intentionally the
    simplest persistence mechanism that satisfies "saved screens survive
    process restarts" — a real production screener would use a proper
    database table with per-user ownership, not a single shared JSON file;
    that's a documented future extension, not attempted here.
    """

    def __init__(self, persistenceFilePath: str | None = None) -> None:
        self._persistenceFilePath = persistenceFilePath
        self._screensByName: dict[str, SavedScreen] = {}
        if self._persistenceFilePath is not None and os.path.exists(self._persistenceFilePath):
            with open(self._persistenceFilePath, "r", encoding="utf-8") as fileHandle:
                rawData = json.load(fileHandle)
            for screenName, screenData in rawData.items():
                self._screensByName[screenName] = SavedScreen(
                    screenName=screenName,
                    filterExpression=screenData["filterExpression"],
                    description=screenData.get("description", ""),
                )

    def _persistToDiskIfConfigured(self) -> None:
        if self._persistenceFilePath is None:
            return
        rawData = {
            screen.screenName: {"filterExpression": screen.filterExpression, "description": screen.description}
            for screen in self._screensByName.values()
        }
        with open(self._persistenceFilePath, "w", encoding="utf-8") as fileHandle:
            json.dump(rawData, fileHandle, indent=2, sort_keys=True)

    def saveScreen(self, screenName: str, filterExpression: dict[str, Any], description: str = "") -> SavedScreen:
        """Saves (creating or OVERWRITING) a named screen. Raises
        `ValueError` on an empty `screenName`.
        """
        if not screenName or not screenName.strip():
            raise ValueError("screenName must be non-empty")
        screen = SavedScreen(screenName=screenName, filterExpression=filterExpression, description=description)
        self._screensByName[screenName] = screen
        self._persistToDiskIfConfigured()
        return screen

    def getScreen(self, screenName: str) -> SavedScreen:
        """Raises `KeyError` if no screen with that name has been saved."""
        if screenName not in self._screensByName:
            raise KeyError(f"no saved screen named {screenName!r}")
        return self._screensByName[screenName]

    def listScreens(self) -> list[SavedScreen]:
        """Returns every saved screen, sorted alphabetically by name."""
        return sorted(self._screensByName.values(), key=lambda screen: screen.screenName)

    def deleteScreen(self, screenName: str) -> None:
        """Raises `KeyError` if no screen with that name has been saved."""
        if screenName not in self._screensByName:
            raise KeyError(f"no saved screen named {screenName!r}")
        del self._screensByName[screenName]
        self._persistToDiskIfConfigured()
