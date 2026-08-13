"""In-memory historical tick data store. See ARCHITECTURE.md §7 and
FEATURES.md §7 — "Historical tick data store + backtest runner (Python
strategy SDK)".

Deliberately the simplest possible thing that is genuinely real: a list of
`(timestamp, price)` ticks per symbol, kept sorted by timestamp, with an
`add` and a `queryRange` function. No persistence, no compression, no
multi-symbol joins — this is a research-tier in-memory store for feeding
`backtestRunner.py`, not a production tick database. A real historical
tick store (e.g. backing market-data's own needs) would need on-disk
columnar storage and would very likely live outside quant-engine
entirely; this module's scope is intentionally narrow.

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

import bisect
from dataclasses import dataclass, field


@dataclass(frozen=True, order=True)
class HistoricalPriceTick:
    timestamp: float
    price: float


@dataclass
class InMemoryHistoricalTickStore:
    """Keeps one sorted-by-timestamp list of `HistoricalPriceTick` per
    symbol. Insertion order need not be chronological — `addTick` inserts
    in sorted position via `bisect`, so out-of-order feeds (e.g. two
    merged data sources) still end up queryable in timestamp order.
    """

    ticksBySymbol: dict[str, list[HistoricalPriceTick]] = field(default_factory=dict)

    def addTick(self, symbol: str, timestamp: float, price: float) -> None:
        """Inserts one tick for `symbol`, maintaining sorted-by-timestamp
        order within that symbol's tick list.
        """
        tickList = self.ticksBySymbol.setdefault(symbol, [])
        newTick = HistoricalPriceTick(timestamp=timestamp, price=price)
        insertionIndex = bisect.bisect_right([existingTick.timestamp for existingTick in tickList], timestamp)
        tickList.insert(insertionIndex, newTick)

    def addTicks(self, symbol: str, ticks: list[tuple[float, float]]) -> None:
        """Convenience bulk-add over a list of `(timestamp, price)` pairs."""
        for timestamp, price in ticks:
            self.addTick(symbol, timestamp, price)

    def queryRange(
        self, symbol: str, startTimestampInclusive: float, endTimestampInclusive: float
    ) -> list[HistoricalPriceTick]:
        """Returns all ticks for `symbol` with
        `startTimestampInclusive <= timestamp <= endTimestampInclusive`,
        in ascending timestamp order. Returns an empty list for an unknown
        symbol rather than raising — an empty result is a normal,
        expected outcome of a range query, not an error.
        """
        tickList = self.ticksBySymbol.get(symbol, [])
        if not tickList:
            return []
        timestamps = [oneTick.timestamp for oneTick in tickList]
        startIndex = bisect.bisect_left(timestamps, startTimestampInclusive)
        endIndex = bisect.bisect_right(timestamps, endTimestampInclusive)
        return tickList[startIndex:endIndex]

    def getAllTicksInOrder(self, symbol: str) -> list[HistoricalPriceTick]:
        """Returns the full sorted tick history for `symbol` (empty list
        for an unknown symbol).
        """
        return list(self.ticksBySymbol.get(symbol, []))

    def getKnownSymbols(self) -> list[str]:
        return list(self.ticksBySymbol.keys())
