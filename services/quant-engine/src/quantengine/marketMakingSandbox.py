"""Market-making sandbox: quote/inventory risk API for approved
institutional clients. See ARCHITECTURE.md §7 ("Algorithmic Trading &
Backtesting") and FEATURES.md §7 — "Market-making sandbox: quote/
inventory risk API for approved institutional clients".

A simulated single-level order book per symbol: a market maker submits a
two-sided quote (bid price/qty, ask price/qty), the sandbox tracks a real
inventory position as simulated TAKER fills cross that quote, and
REJECTS any quote update that would allow inventory to exceed a
configurable per-symbol risk limit if the quote were fully filled.

Deliberately a SIMULATION, not a real order book: one price level per
side (a new quote on a side fully REPLACES the previous quote on that
side, it doesn't queue behind it), and "taker fills" are caller-driven
(`simulateTakerOrderCrossingQuote`) rather than derived from a real
matching-engine feed — this module is quant-engine's research-tier
sandbox for testing market-making risk logic, not a production quoting
engine (that belongs in matching-engine/oms-gateway's real order-book
code path).

This is the one module in this pass that holds SHARED MUTABLE STATE
(inventory positions persist across calls) — unlike every other
quant-engine module, which is a pure, stateless computation. Callers
embedding this in the HTTP service (see httpServer.py) must guard access
with a lock, since `ThreadingHTTPServer` handles requests concurrently.

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum


class QuoteSide(Enum):
    BID = "BID"
    ASK = "ASK"


@dataclass
class TwoSidedQuote:
    bidPrice: float
    bidQuantity: float
    askPrice: float
    askQuantity: float

    def __post_init__(self) -> None:
        if self.bidPrice <= 0 or self.askPrice <= 0:
            raise ValueError("bidPrice and askPrice must both be strictly positive")
        if self.bidQuantity < 0 or self.askQuantity < 0:
            raise ValueError("bidQuantity and askQuantity must both be non-negative")
        if self.bidPrice >= self.askPrice:
            raise ValueError("bidPrice must be strictly less than askPrice (a crossed quote isn't valid)")


@dataclass
class SymbolMarketMakerState:
    symbol: str
    maximumAbsoluteInventory: float
    currentQuote: TwoSidedQuote | None = None
    inventoryPosition: float = 0.0
    fillHistory: list[str] = field(default_factory=list)


class QuoteRejectedError(ValueError):
    """Raised when a submitted quote would allow inventory to exceed the
    configured risk limit if fully filled on either side."""


class MarketMakingSandbox:
    """Holds one `SymbolMarketMakerState` per symbol. All mutation
    happens through this class's methods — callers (including the HTTP
    layer) should treat `_stateBySymbol` as private and never mutate a
    `SymbolMarketMakerState` directly.
    """

    def __init__(self) -> None:
        self._stateBySymbol: dict[str, SymbolMarketMakerState] = {}

    def registerSymbolWithInventoryLimit(self, symbol: str, maximumAbsoluteInventory: float) -> None:
        """Idempotent: re-registering an already-known symbol just
        updates its limit, it doesn't reset inventory or the current
        quote — a risk desk tightening/loosening a limit shouldn't wipe
        out the market maker's live position.
        """
        if maximumAbsoluteInventory <= 0:
            raise ValueError("maximumAbsoluteInventory must be strictly positive")
        if symbol in self._stateBySymbol:
            self._stateBySymbol[symbol].maximumAbsoluteInventory = maximumAbsoluteInventory
            return
        self._stateBySymbol[symbol] = SymbolMarketMakerState(
            symbol=symbol, maximumAbsoluteInventory=maximumAbsoluteInventory
        )

    def _requireRegisteredSymbol(self, symbol: str) -> SymbolMarketMakerState:
        if symbol not in self._stateBySymbol:
            raise KeyError(f"symbol {symbol!r} is not registered with the sandbox")
        return self._stateBySymbol[symbol]

    def submitTwoSidedQuote(
        self, symbol: str, bidPrice: float, bidQuantity: float, askPrice: float, askQuantity: float
    ) -> TwoSidedQuote:
        """Submits (replacing any prior quote on this symbol) a new
        two-sided quote. REJECTS (raises `QuoteRejectedError`) if letting
        either side fill IN FULL would push `inventoryPosition` outside
        `[-maximumAbsoluteInventory, +maximumAbsoluteInventory]`:

        - A full ask fill SELLS `askQuantity` -> inventory decreases by
          `askQuantity`. Rejected if
          `inventoryPosition - askQuantity < -maximumAbsoluteInventory`.
        - A full bid fill BUYS `bidQuantity` -> inventory increases by
          `bidQuantity`. Rejected if
          `inventoryPosition + bidQuantity > maximumAbsoluteInventory`.

        Both sides are checked independently (a real taker could hit
        either side, not necessarily both), so BOTH must be safe for the
        quote to be accepted.
        """
        symbolState = self._requireRegisteredSymbol(symbol)
        candidateQuote = TwoSidedQuote(
            bidPrice=bidPrice, bidQuantity=bidQuantity, askPrice=askPrice, askQuantity=askQuantity
        )

        worstCaseInventoryIfAskFullyFilled = symbolState.inventoryPosition - askQuantity
        worstCaseInventoryIfBidFullyFilled = symbolState.inventoryPosition + bidQuantity

        if worstCaseInventoryIfAskFullyFilled < -symbolState.maximumAbsoluteInventory:
            raise QuoteRejectedError(
                f"quote rejected for {symbol}: a full ask fill of {askQuantity} would take inventory to "
                f"{worstCaseInventoryIfAskFullyFilled}, past the short limit of "
                f"-{symbolState.maximumAbsoluteInventory}"
            )
        if worstCaseInventoryIfBidFullyFilled > symbolState.maximumAbsoluteInventory:
            raise QuoteRejectedError(
                f"quote rejected for {symbol}: a full bid fill of {bidQuantity} would take inventory to "
                f"{worstCaseInventoryIfBidFullyFilled}, past the long limit of "
                f"{symbolState.maximumAbsoluteInventory}"
            )

        symbolState.currentQuote = candidateQuote
        return candidateQuote

    def simulateTakerOrderCrossingQuote(
        self, symbol: str, takerSide: QuoteSide, quantity: float
    ) -> float:
        """Simulates an incoming taker order crossing the sandbox's
        CURRENT quote on `takerSide`. A taker hitting the BID means the
        maker BUYS (inventory increases); a taker hitting the ASK means
        the maker SELLS (inventory decreases). Fill quantity is capped at
        the quoted quantity remaining on that side (no partial re-quote
        beyond decrementing the filled amount — a single-level,
        simplified book, per this module's docstring).

        Returns the ACTUAL filled quantity (may be less than `quantity`
        requested if the quoted size was smaller). Raises `ValueError` if
        there is no current quote, or if `quantity` is non-positive.
        """
        symbolState = self._requireRegisteredSymbol(symbol)
        if symbolState.currentQuote is None:
            raise ValueError(f"no active quote for {symbol} to cross")
        if quantity <= 0:
            raise ValueError("quantity must be strictly positive")

        quote = symbolState.currentQuote
        if takerSide == QuoteSide.BID:
            availableQuantity = quote.bidQuantity
            filledQuantity = min(quantity, availableQuantity)
            quote.bidQuantity -= filledQuantity
            symbolState.inventoryPosition += filledQuantity
        else:
            availableQuantity = quote.askQuantity
            filledQuantity = min(quantity, availableQuantity)
            quote.askQuantity -= filledQuantity
            symbolState.inventoryPosition -= filledQuantity

        if filledQuantity > 0:
            symbolState.fillHistory.append(
                f"taker {takerSide.value} filled {filledQuantity} of {symbol}; "
                f"inventory now {symbolState.inventoryPosition}"
            )
        return filledQuantity

    def getInventoryPosition(self, symbol: str) -> float:
        return self._requireRegisteredSymbol(symbol).inventoryPosition

    def getSymbolState(self, symbol: str) -> SymbolMarketMakerState:
        return self._requireRegisteredSymbol(symbol)
