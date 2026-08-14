"""Delta-hedging automation: auto-hedge-alert logic for when a
portfolio's net delta crosses a user-defined threshold. See FEATURES.md
§22 ("Deep Quant & Algorithmic Trading Internals").

Reuses `portfolioGreeksAggregator.aggregatePortfolioGreeks` (NOT
reimplemented) to get the real net portfolio delta, then applies real,
simple hedge-sizing arithmetic:

    hedgeQuantityInShares = -netDelta * SHARES_PER_STANDARD_OPTION_CONTRACT

i.e. the number of underlying SHARES a trader would need to buy (positive
result) or sell (negative result) to bring the portfolio's delta-
equivalent share exposure back to flat. `netDelta` here is expressed in
OPTION-CONTRACT delta units (each contract's delta already scaled to
[-1, 1] by `blackScholesOptionPricer.calculateOptionGreeks`), and one
option contract's delta corresponds to `SHARES_PER_STANDARD_OPTION_CONTRACT`
(100, the industry-standard equity option contract multiplier) shares of
delta-equivalent underlying exposure — this module documents that
multiplier explicitly rather than silently assuming delta is already in
share terms.

This module does NOT place any real hedge order — like
`illustrativeSentimentTradingHook.py`'s explicit scope note, it computes
a real hedge quantity and a real alert-worthy boolean, and stops there.
No order-submission path is wired here.

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

from dataclasses import dataclass

from quantengine.portfolioGreeksAggregator import PortfolioGreeksAggregationResult

# Standard equity option contract multiplier: one contract's delta of
# 1.0 corresponds to this many shares of delta-equivalent underlying
# exposure. Matches syntheticPositionBuilder.py's own documented
# convention for the same constant.
SHARES_PER_STANDARD_OPTION_CONTRACT = 100.0


@dataclass(frozen=True)
class DeltaHedgingAlertResult:
    netDelta: float
    deltaThreshold: float
    isThresholdBreached: bool
    hedgeQuantityInShares: float
    sharesPerContractMultiplierUsed: float


def evaluateDeltaHedgingThreshold(
    portfolioGreeks: PortfolioGreeksAggregationResult,
    deltaThreshold: float,
    sharesPerContractMultiplier: float = SHARES_PER_STANDARD_OPTION_CONTRACT,
) -> DeltaHedgingAlertResult:
    """Given a portfolio's already-aggregated net Greeks (from
    `portfolioGreeksAggregator.aggregatePortfolioGreeks`) and a
    caller-configured `deltaThreshold` (a POSITIVE number — the maximum
    ABSOLUTE net delta the desk is willing to carry unhedged), computes:

    - `isThresholdBreached`: `abs(netDelta) > deltaThreshold` — the real
      alert-worthy boolean.
    - `hedgeQuantityInShares`: the EXACT share quantity (signed: positive
      = buy underlying, negative = sell/short underlying) needed to bring
      net delta-equivalent exposure back to EXACTLY ZERO — not merely
      back within the threshold. Hedging to flat (rather than to the
      threshold boundary) is the simpler, more common desk convention
      documented here explicitly, since "hedge back to the threshold
      edge" would immediately re-breach on the next small delta move.

    Raises `ValueError` if `deltaThreshold` is not strictly positive (a
    zero or negative threshold has no meaningful "breach" semantics) or
    if `sharesPerContractMultiplier` is not strictly positive.
    """
    if deltaThreshold <= 0.0:
        raise ValueError("deltaThreshold must be strictly positive")
    if sharesPerContractMultiplier <= 0.0:
        raise ValueError("sharesPerContractMultiplier must be strictly positive")

    netDelta = portfolioGreeks.netDelta
    isThresholdBreached = abs(netDelta) > deltaThreshold
    hedgeQuantityInShares = -netDelta * sharesPerContractMultiplier

    return DeltaHedgingAlertResult(
        netDelta=netDelta,
        deltaThreshold=deltaThreshold,
        isThresholdBreached=isThresholdBreached,
        hedgeQuantityInShares=hedgeQuantityInShares,
        sharesPerContractMultiplierUsed=sharesPerContractMultiplier,
    )
