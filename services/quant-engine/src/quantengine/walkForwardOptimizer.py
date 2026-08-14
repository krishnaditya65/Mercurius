"""Walk-forward optimization / out-of-sample validation for the backtest
SDK, with a real (documented) overfitting-warning heuristic. See
FEATURES.md §22 ("Deep Quant & Algorithmic Trading Internals") — "Walk-
forward optimization / out-of-sample validation built into the
backtester, with automatic overfitting warnings".

**What real walk-forward optimization is** (this module implements the
textbook version, not a toy): given a strategy with tunable parameters,
split a historical tick series into consecutive (in-sample, out-of-
sample) window pairs. For each pair:

1. Run the FULL parameter grid over the in-sample ("training"/
   "optimization") window using the EXISTING, unmodified
   `backtesting.backtestRunner.runDeterministicEventDrivenBacktest` — no
   new backtest engine is built here, this module is a real caller of the
   existing one.
2. Pick the single best parameter combination by in-sample total P&L.
3. Re-run ONLY that best combination — with a fresh starting portfolio —
   over the immediately-following out-of-sample ("test") window. This is
   the step naive parameter optimization skips, and it's the whole point
   of walk-forward analysis: it measures how a parameter choice that
   looked good on data it was chosen FROM performs on data it has never
   seen.
4. Roll the window forward by `stepSizeInTicks` (defaults to the
   out-of-sample window size, i.e. non-overlapping out-of-sample windows)
   and repeat until the tick series is exhausted.

**Overfitting warning heuristic — two real, documented rules, not a
single fudge factor:**

1. **Walk-Forward Efficiency (WFE)**, a real, named metric from
   quantitative-trading literature (Robert Pardo, *The Evaluation and
   Optimization of Trading Strategies*, 2nd ed.): `WFE = averageOutOfSample
   PerformanceAcrossWindows / averageInSamplePerformanceAcrossWindows`.
   Pardo treats a WFE below roughly 50% as evidence the optimization is
   overfitting the in-sample window rather than finding a robust edge —
   this module uses that exact documented 0.5 threshold
   (`walkForwardEfficiencyWarningThreshold`, caller-overridable). A WFE
   at or above the threshold means out-of-sample performance held up
   reasonably well relative to in-sample; a WFE well below it (or
   negative — in-sample profits, out-of-sample losses) is the classic
   overfitting signature.
2. **Observations-per-parameter rule of thumb**: a long-standing
   statistical heuristic (closely related to the "10 events per
   variable" rule from regression modeling, e.g. Peduzzi et al. 1996 for
   logistic regression, popularly generalized to "at least ~10
   observations per estimated/tuned parameter" for any data-fitting
   procedure) — this module flags when
   `inSampleWindowSizeInTicks / numberOfTunableParameters` falls below
   `minimumObservationsPerParameterRuleOfThumb` (defaults to 10.0). Few
   observations relative to the number of knobs being tuned is a
   structural overfitting risk independent of any single window's WFE
   number.

Both checks are REAL and independently computed (see
`evaluateOverfittingWarning`, exposed standalone so each can be
hand-verified in isolation from the full rolling-window loop). Neither
threshold is a "regulatory" or empirically-calibrated number — both are
the standard textbook/rule-of-thumb values cited above, used as-is and
documented as such, exactly like this codebase's other illustrative-
but-real thresholds (see `arbitrageScanner.py`'s deviation threshold).

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

import itertools
from dataclasses import dataclass
from typing import Callable

from quantengine.backtesting.backtestRunner import (
    StrategyCallback,
    runDeterministicEventDrivenBacktest,
)
from quantengine.backtesting.tickStore import HistoricalPriceTick

StrategyFactory = Callable[[dict], StrategyCallback]


def generateParameterCombinationsFromGrid(parameterGrid: dict[str, list]) -> list[dict]:
    """Cartesian product of every parameter's candidate-value list, e.g.
    `{"quantity": [1, 2], "threshold": [0.1, 0.2]}` ->
    `[{"quantity": 1, "threshold": 0.1}, {"quantity": 1, "threshold": 0.2},
      {"quantity": 2, "threshold": 0.1}, {"quantity": 2, "threshold": 0.2}]`.
    Raises `ValueError` if `parameterGrid` is empty or any parameter has
    an empty candidate-value list.
    """
    if not parameterGrid:
        raise ValueError("parameterGrid must contain at least one tunable parameter")
    for parameterName, candidateValues in parameterGrid.items():
        if not candidateValues:
            raise ValueError(f"parameter '{parameterName}' has an empty candidate-value list")

    parameterNames = list(parameterGrid.keys())
    combinations = []
    for valuesTuple in itertools.product(*(parameterGrid[name] for name in parameterNames)):
        combinations.append(dict(zip(parameterNames, valuesTuple)))
    return combinations


def _totalProfitAndLossFromBacktest(ticks: list[HistoricalPriceTick], strategyCallback: StrategyCallback, startingCashBalance: float) -> float:
    result = runDeterministicEventDrivenBacktest(ticks, strategyCallback, startingCashBalance=startingCashBalance)
    return result.finalTotalEquity - startingCashBalance


@dataclass(frozen=True)
class WalkForwardWindowResult:
    windowIndex: int
    inSampleTickCount: int
    outOfSampleTickCount: int
    bestParameters: dict
    inSampleTotalProfitAndLoss: float
    outOfSampleTotalProfitAndLoss: float


@dataclass(frozen=True)
class OverfittingWarning:
    isFlagged: bool
    reasons: list[str]
    walkForwardEfficiencyRatio: float | None
    observationsPerParameter: float


def evaluateOverfittingWarning(
    averageInSampleProfitAndLoss: float,
    averageOutOfSampleProfitAndLoss: float,
    numberOfTunableParameters: int,
    inSampleWindowSizeInTicks: int,
    walkForwardEfficiencyWarningThreshold: float = 0.5,
    minimumObservationsPerParameterRuleOfThumb: float = 10.0,
) -> OverfittingWarning:
    """Standalone, independently hand-verifiable overfitting heuristic —
    see the module docstring for the two real, documented rules applied
    here. Exposed separately from the full rolling-window loop so both
    rules can be exercised directly with hand-picked numbers.
    """
    if numberOfTunableParameters <= 0:
        raise ValueError("numberOfTunableParameters must be a positive integer")
    if inSampleWindowSizeInTicks <= 0:
        raise ValueError("inSampleWindowSizeInTicks must be positive")

    reasons: list[str] = []

    observationsPerParameter = inSampleWindowSizeInTicks / numberOfTunableParameters
    if observationsPerParameter < minimumObservationsPerParameterRuleOfThumb:
        reasons.append(
            f"only {observationsPerParameter:.2f} in-sample observations per tunable parameter "
            f"(rule of thumb: at least {minimumObservationsPerParameterRuleOfThumb:.2f}) — "
            "too few data points relative to the number of knobs being tuned"
        )

    walkForwardEfficiencyRatio: float | None
    if averageInSampleProfitAndLoss > 0:
        walkForwardEfficiencyRatio = averageOutOfSampleProfitAndLoss / averageInSampleProfitAndLoss
        if walkForwardEfficiencyRatio < walkForwardEfficiencyWarningThreshold:
            reasons.append(
                f"walk-forward efficiency {walkForwardEfficiencyRatio:.4f} is below the "
                f"{walkForwardEfficiencyWarningThreshold:.4f} threshold (Pardo) — out-of-sample "
                "performance did not hold up relative to in-sample performance"
            )
    else:
        # An in-sample performance of zero or negative makes the ratio
        # itself meaningless (dividing by a non-positive number inverts
        # or nullifies its interpretation) — flag directly instead.
        walkForwardEfficiencyRatio = None
        reasons.append(
            "average in-sample profit-and-loss was not positive — the walk-forward efficiency "
            "ratio is undefined/meaningless here, which is itself a red flag for the optimization"
        )

    return OverfittingWarning(
        isFlagged=bool(reasons),
        reasons=reasons,
        walkForwardEfficiencyRatio=walkForwardEfficiencyRatio,
        observationsPerParameter=observationsPerParameter,
    )


@dataclass(frozen=True)
class WalkForwardOptimizationResult:
    windowResults: list[WalkForwardWindowResult]
    averageInSampleProfitAndLoss: float
    averageOutOfSampleProfitAndLoss: float
    overfittingWarning: OverfittingWarning


def runWalkForwardOptimization(
    orderedTicks: list[HistoricalPriceTick],
    parameterGrid: dict[str, list],
    strategyFactory: StrategyFactory,
    inSampleWindowSizeInTicks: int,
    outOfSampleWindowSizeInTicks: int,
    stepSizeInTicks: int | None = None,
    startingCashBalance: float = 100_000.0,
    walkForwardEfficiencyWarningThreshold: float = 0.5,
    minimumObservationsPerParameterRuleOfThumb: float = 10.0,
) -> WalkForwardOptimizationResult:
    """Real rolling train/test walk-forward loop over `orderedTicks`
    (must already be chronologically ordered, same convention as
    `backtestRunner.runDeterministicEventDrivenBacktest`). `strategyFactory`
    turns one parameter-combination dict into a `StrategyCallback`
    compatible with the existing backtest runner.

    `stepSizeInTicks` defaults to `outOfSampleWindowSizeInTicks` — i.e.
    non-overlapping out-of-sample windows walking straight through the
    series. A smaller step re-uses/overlaps out-of-sample data across
    windows (a valid, more data-efficient walk-forward variant); a step
    larger than `outOfSampleWindowSizeInTicks` skips ticks between
    windows.

    Raises `ValueError` if the windows/step are non-positive or if
    `orderedTicks` isn't long enough to form even one full
    (in-sample, out-of-sample) window pair.
    """
    if inSampleWindowSizeInTicks <= 0 or outOfSampleWindowSizeInTicks <= 0:
        raise ValueError("inSampleWindowSizeInTicks and outOfSampleWindowSizeInTicks must be positive")
    if stepSizeInTicks is None:
        stepSizeInTicks = outOfSampleWindowSizeInTicks
    if stepSizeInTicks <= 0:
        raise ValueError("stepSizeInTicks must be positive")

    windowSpan = inSampleWindowSizeInTicks + outOfSampleWindowSizeInTicks
    if len(orderedTicks) < windowSpan:
        raise ValueError(
            f"orderedTicks has {len(orderedTicks)} ticks, fewer than one full window "
            f"({inSampleWindowSizeInTicks} in-sample + {outOfSampleWindowSizeInTicks} out-of-sample = {windowSpan})"
        )

    parameterCombinations = generateParameterCombinationsFromGrid(parameterGrid)

    windowResults: list[WalkForwardWindowResult] = []
    windowIndex = 0
    startIndex = 0
    while startIndex + windowSpan <= len(orderedTicks):
        inSampleTicks = orderedTicks[startIndex : startIndex + inSampleWindowSizeInTicks]
        outOfSampleTicks = orderedTicks[
            startIndex + inSampleWindowSizeInTicks : startIndex + windowSpan
        ]

        bestParameters = None
        bestInSampleProfitAndLoss = None
        for candidateParameters in parameterCombinations:
            candidatePnl = _totalProfitAndLossFromBacktest(
                inSampleTicks, strategyFactory(candidateParameters), startingCashBalance
            )
            if bestInSampleProfitAndLoss is None or candidatePnl > bestInSampleProfitAndLoss:
                bestInSampleProfitAndLoss = candidatePnl
                bestParameters = candidateParameters

        outOfSampleProfitAndLoss = _totalProfitAndLossFromBacktest(
            outOfSampleTicks, strategyFactory(bestParameters), startingCashBalance
        )

        windowResults.append(
            WalkForwardWindowResult(
                windowIndex=windowIndex,
                inSampleTickCount=len(inSampleTicks),
                outOfSampleTickCount=len(outOfSampleTicks),
                bestParameters=bestParameters,
                inSampleTotalProfitAndLoss=bestInSampleProfitAndLoss,
                outOfSampleTotalProfitAndLoss=outOfSampleProfitAndLoss,
            )
        )

        windowIndex += 1
        startIndex += stepSizeInTicks

    averageInSampleProfitAndLoss = sum(w.inSampleTotalProfitAndLoss for w in windowResults) / len(windowResults)
    averageOutOfSampleProfitAndLoss = sum(w.outOfSampleTotalProfitAndLoss for w in windowResults) / len(windowResults)

    overfittingWarning = evaluateOverfittingWarning(
        averageInSampleProfitAndLoss,
        averageOutOfSampleProfitAndLoss,
        numberOfTunableParameters=len(parameterGrid),
        inSampleWindowSizeInTicks=inSampleWindowSizeInTicks,
        walkForwardEfficiencyWarningThreshold=walkForwardEfficiencyWarningThreshold,
        minimumObservationsPerParameterRuleOfThumb=minimumObservationsPerParameterRuleOfThumb,
    )

    return WalkForwardOptimizationResult(
        windowResults=windowResults,
        averageInSampleProfitAndLoss=averageInSampleProfitAndLoss,
        averageOutOfSampleProfitAndLoss=averageOutOfSampleProfitAndLoss,
        overfittingWarning=overfittingWarning,
    )
