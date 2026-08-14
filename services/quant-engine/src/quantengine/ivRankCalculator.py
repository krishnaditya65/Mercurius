"""IV Rank and IV Percentile: how "expensive" a current implied
volatility reading is relative to its OWN trailing 1-year (or any other
caller-chosen lookback window) range. See FEATURES.md §22 ("Deep Quant &
Algorithmic Trading Internals").

Two real, standard, distinct formulas — options traders commonly conflate
these, so both are implemented explicitly rather than picking one:

1. IV RANK (`calculateImpliedVolatilityRank`) — where the current IV sits
   between the historical MIN and MAX, as a fraction:

       ivRank = (currentImpliedVolatility - min(historicalSeries))
                / (max(historicalSeries) - min(historicalSeries))

   Sensitive to a single outlier extreme in the historical window (one
   volatility spike can permanently compress every future rank reading
   until it rolls out of the window).

2. IV PERCENTILE (`calculateImpliedVolatilityPercentile`) — the fraction
   of historical observations STRICTLY BELOW the current IV:

       ivPercentile = count(historicalObservation < currentImpliedVolatility)
                      / count(historicalObservations)

   Robust to outliers (a single extreme historical spike is just one more
   data point, not a range endpoint), which is why many practitioners
   prefer percentile over rank despite rank being more commonly quoted by
   name.

`historicalImpliedVolatilitySeries` in every test/example here is an
ILLUSTRATIVE/FIXTURE series — this module does no historical IV data
ingestion of its own (no vendor feed, no persistence); it is a pure
function over whatever series a caller supplies. Document your own
data source before treating a real IV Rank/Percentile reading as
tradeable.

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True)
class ImpliedVolatilityRankAndPercentileResult:
    currentImpliedVolatility: float
    historicalMinimumImpliedVolatility: float
    historicalMaximumImpliedVolatility: float
    impliedVolatilityRank: float
    impliedVolatilityPercentile: float


def calculateImpliedVolatilityRank(
    currentImpliedVolatility: float, historicalImpliedVolatilitySeries: list[float]
) -> float:
    """`(current - min) / (max - min)` over `historicalImpliedVolatilitySeries`.

    Raises `ValueError` if the historical series is empty, or if the
    historical range has zero width (max == min — a flat/constant
    series), since the ratio is undefined (division by zero) in that
    case. `currentImpliedVolatility` is NOT required to fall within
    `[min, max]` — a fresh volatility spike beyond the historical range
    legitimately produces a rank below 0.0 or above 1.0, which callers
    should be prepared to see (it's informative, not an error).
    """
    if not historicalImpliedVolatilitySeries:
        raise ValueError("historicalImpliedVolatilitySeries must contain at least one observation")

    historicalMinimum = min(historicalImpliedVolatilitySeries)
    historicalMaximum = max(historicalImpliedVolatilitySeries)
    historicalRangeWidth = historicalMaximum - historicalMinimum
    if historicalRangeWidth == 0.0:
        raise ValueError(
            "cannot compute IV Rank: historicalImpliedVolatilitySeries has zero range "
            "(every historical observation is identical)"
        )

    return (currentImpliedVolatility - historicalMinimum) / historicalRangeWidth


def calculateImpliedVolatilityPercentile(
    currentImpliedVolatility: float, historicalImpliedVolatilitySeries: list[float]
) -> float:
    """Fraction of `historicalImpliedVolatilitySeries` STRICTLY BELOW
    `currentImpliedVolatility`. Always well-defined in `[0.0, 1.0]` for
    any non-empty historical series (no divide-by-zero failure mode like
    IV Rank's flat-range case) — raises `ValueError` only on an empty
    series.
    """
    if not historicalImpliedVolatilitySeries:
        raise ValueError("historicalImpliedVolatilitySeries must contain at least one observation")

    countBelowCurrent = sum(
        1 for oneObservation in historicalImpliedVolatilitySeries if oneObservation < currentImpliedVolatility
    )
    return countBelowCurrent / len(historicalImpliedVolatilitySeries)


def calculateImpliedVolatilityRankAndPercentile(
    currentImpliedVolatility: float, historicalImpliedVolatilitySeries: list[float]
) -> ImpliedVolatilityRankAndPercentileResult:
    """Convenience wrapper computing both metrics (plus the raw
    historical min/max used for IV Rank) in one call — the natural shape
    for an HTTP response or a single UI widget showing both numbers side
    by side.
    """
    ivRank = calculateImpliedVolatilityRank(currentImpliedVolatility, historicalImpliedVolatilitySeries)
    ivPercentile = calculateImpliedVolatilityPercentile(
        currentImpliedVolatility, historicalImpliedVolatilitySeries
    )
    return ImpliedVolatilityRankAndPercentileResult(
        currentImpliedVolatility=currentImpliedVolatility,
        historicalMinimumImpliedVolatility=min(historicalImpliedVolatilitySeries),
        historicalMaximumImpliedVolatility=max(historicalImpliedVolatilitySeries),
        impliedVolatilityRank=ivRank,
        impliedVolatilityPercentile=ivPercentile,
    )
