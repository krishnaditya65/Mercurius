"""Kelly-criterion-based position sizing calculator per strategy. See
FEATURES.md §22 ("Deep Quant & Algorithmic Trading Internals").

Two real, DIFFERENT Kelly formulas, each correct for a different shape of
input — this module implements both explicitly rather than picking one
and forcing every caller's data into it:

1. THE CLASSIC (discrete win/loss) KELLY FORMULA
   (`calculateKellyFractionFromWinLossStatistics`) — for a strategy
   whose historical performance is naturally summarized as a win rate
   and a win/loss payout ratio (e.g. "wins 55% of trades, and winning
   trades average 1.5x the size of losing trades"):

       f* = (b*p - q) / b

   where `p` = win probability, `q` = 1 - p = loss probability, `b` =
   the ratio of a typical win's size to a typical loss's size (the
   "odds"). This is the textbook Kelly formula as originally derived for
   binary bet sizing (Kelly 1956), reused here for a strategy's
   trade-level win/loss statistics.

2. THE CONTINUOUS-RETURNS (MEAN/VARIANCE) KELLY FORMULA
   (`calculateKellyFractionFromReturnDistributionStatistics`) — for a
   strategy whose performance is naturally summarized as a mean and
   variance of PERIODIC RETURNS (e.g. a backtest's daily return series
   statistics), which does NOT map cleanly onto a discrete win/loss
   bet:

       f* = mean(periodicReturns) / variance(periodicReturns)

   This is the standard continuous/Gaussian-return approximation to the
   Kelly criterion (maximizing expected log growth under a normal-return
   assumption) — a DIFFERENT formula from #1 above, picked because it
   fits continuous return-distribution inputs rather than discrete
   win/loss counts. Uses the POPULATION variance convention, matching
   `riskStatistics.py`'s convention elsewhere in this codebase.

FULL Kelly (`f* = 1.0`, i.e. betting the entire computed fraction) is
famously over-aggressive in practice: it maximizes long-run expected
LOG growth but assumes perfectly known, stationary win/loss statistics
or return distributions — real edge estimates are noisy, and full Kelly
sizing amplifies that estimation error into large, painful drawdowns
even when the underlying edge is real and positive. The standard,
well-documented practitioner mitigation is FRACTIONAL KELLY (most
commonly HALF-KELLY, `fractionalMultiplier=0.5`): betting a fixed
fraction of the full Kelly size trades a proportionally SMALLER share of
theoretical growth rate for a MUCH smaller variance/drawdown profile —
half-Kelly retains about 75% of full Kelly's growth rate while roughly
halving its variance, a well-known asymmetric trade-off in the Kelly
literature. `applyFractionalKelly` implements this real, simple scaling
and is the recommended way to consume this module's output; this module
does not default to full Kelly silently anywhere it returns a
directly-usable position size.

Every function here returns a FRACTION of bankroll/capital to allocate —
it does not know your account size and does not place any order.

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

from dataclasses import dataclass


def calculateKellyFractionFromWinLossStatistics(winProbability: float, winLossPayoutRatio: float) -> float:
    """Classic discrete Kelly formula: `f* = (b*p - q) / b`, where
    `p = winProbability`, `q = 1 - p`, `b = winLossPayoutRatio` (average
    win size / average loss size, e.g. 1.5 means winning trades average
    1.5x the size of losing trades).

    Raises `ValueError` if `winProbability` is not strictly between 0
    and 1, or if `winLossPayoutRatio` is not strictly positive (a
    zero-or-negative payout ratio has no meaningful betting edge to
    size).
    """
    if not (0.0 < winProbability < 1.0):
        raise ValueError("winProbability must be strictly between 0 and 1")
    if winLossPayoutRatio <= 0.0:
        raise ValueError("winLossPayoutRatio must be strictly positive")

    lossProbability = 1.0 - winProbability
    return (winLossPayoutRatio * winProbability - lossProbability) / winLossPayoutRatio


def calculateKellyFractionFromReturnDistributionStatistics(
    meanPeriodicReturn: float, periodicReturnVariance: float
) -> float:
    """Continuous-returns (mean/variance) Kelly approximation:
    `f* = mean / variance`, using the POPULATION variance convention.

    Raises `ValueError` if `periodicReturnVariance` is not strictly
    positive (zero variance makes the fraction undefined/infinite; a
    negative variance is not a valid input at all).
    """
    if periodicReturnVariance <= 0.0:
        raise ValueError("periodicReturnVariance must be strictly positive")
    return meanPeriodicReturn / periodicReturnVariance


def calculateKellyFractionFromPeriodicReturnSeries(periodicReturns: list[float]) -> float:
    """Convenience wrapper: computes the population mean and variance of
    `periodicReturns` itself, then applies
    `calculateKellyFractionFromReturnDistributionStatistics`. Raises
    `ValueError` on an empty series or (via the wrapped call) zero
    variance.
    """
    if not periodicReturns:
        raise ValueError("periodicReturns must contain at least one observation")
    meanReturn = sum(periodicReturns) / len(periodicReturns)
    variance = sum((r - meanReturn) ** 2 for r in periodicReturns) / len(periodicReturns)
    return calculateKellyFractionFromReturnDistributionStatistics(meanReturn, variance)


@dataclass(frozen=True)
class FractionalKellyResult:
    fullKellyFraction: float
    fractionalMultiplier: float
    recommendedAllocationFraction: float


def applyFractionalKelly(fullKellyFraction: float, fractionalMultiplier: float = 0.5) -> FractionalKellyResult:
    """Scales `fullKellyFraction` by `fractionalMultiplier` (default
    0.5, i.e. HALF-KELLY — the standard, well-documented practitioner
    default; see the module docstring for why full Kelly is
    over-aggressive). `fractionalMultiplier` of 1.0 means full Kelly
    (not recommended, but not disallowed — this module trusts the
    caller to have read the docstring). Raises `ValueError` if
    `fractionalMultiplier` is not strictly between 0 and 1 inclusive of
    neither 0 (betting nothing is a degenerate, presumably-unintended
    input) nor anything above 1.0 (over-betting beyond full Kelly is
    never a Kelly-criterion-derived recommendation).
    """
    if not (0.0 < fractionalMultiplier <= 1.0):
        raise ValueError("fractionalMultiplier must be strictly greater than 0 and at most 1.0")
    return FractionalKellyResult(
        fullKellyFraction=fullKellyFraction,
        fractionalMultiplier=fractionalMultiplier,
        recommendedAllocationFraction=fullKellyFraction * fractionalMultiplier,
    )
