"""Regime detection via a REAL, hand-rolled Hidden Markov Model (HMM):
TRENDING / MEAN_REVERTING / HIGH_VOLATILITY classification, to gate
which strategies are allowed to run. See FEATURES.md §22 ("Deep Quant &
Algorithmic Trading Internals") — "Regime detection (Hidden Markov
Model: trending/mean-reverting/high-vol classification) to gate which
strategies are allowed to run".

**This is genuine numerical work, not a threshold rule dressed up as an
HMM.** Every piece below is a real, from-scratch implementation, stdlib
`math`-only (no numpy, no `hmmlearn`/scikit-learn — consistent with this
service's convention of hand-rolling numerical algorithms; see
`garchVolatilityForecaster.py` and `cointegrationTester.py` for the same
convention applied to GARCH fitting and OLS/ADF):

1. **Gaussian emission model** (`gaussianEmissionProbabilityDensity`):
   the standard normal-density formula
   `f(x) = 1/sqrt(2*pi*variance) * exp(-(x-mean)^2 / (2*variance))` —
   each hidden state emits observed returns from its own fitted
   Gaussian.
2. **Scaled forward algorithm** (`runForwardAlgorithm`): computes
   `alphaHat_t(i)`, the (rescaled, numerically-stable) probability of
   observing everything up to time `t` and being in state `i`, using the
   standard log-sum-of-scaling-factors trick (`c_t = sum_i alpha_t(i)`,
   `alphaHat_t(i) = alpha_t(i) / c_t`) so `logLikelihood =
   sum(log(c_t))` — the textbook Rabiner (1989) scaled-forward
   formulation, done here to avoid the numerical underflow a naive
   unscaled forward pass suffers over more than a few dozen
   observations.
3. **Scaled backward algorithm** (`runBackwardAlgorithm`): the matching
   backward pass, reusing the SAME per-step scaling factors the forward
   pass computed (required for the forward/backward products below to be
   correctly normalized).
4. **Baum-Welch parameter re-estimation**
   (`runBaumWelchExpectationMaximization`): a real, simplified EM loop —
   E-step computes `gamma_t(i) = alphaHat_t(i) * betaHat_t(i)` (state
   occupation probability) and `xi_t(i,j)` (state-transition
   probability) from the scaled forward/backward passes; M-step
   re-estimates the initial-state distribution, transition matrix, and
   per-state Gaussian mean/variance from those real expectations. Iterates
   until the log-likelihood improvement falls below
   `convergenceTolerance` or `maximumIterationCount` is reached. This is
   the real Baum-Welch algorithm (a special case of EM for HMMs), not a
   from-scratch general-purpose optimizer — "simplified" here means a
   fixed iteration cap and a variance floor (see below) rather than a
   from-scratch continuous optimizer, matching this repo's existing
   "real but not production-grade continuous optimization" convention
   (see `garchVolatilityForecaster.py`'s own honest caveat).
5. **Viterbi decoding** (`runViterbiAlgorithm`): a real dynamic-
   programming most-likely-state-sequence inference in log-space
   (avoids the same underflow the forward pass guards against), the
   textbook Viterbi algorithm — NOT a re-use of the forward pass's
   probabilities, a genuinely separate DP recurrence over `max` instead
   of `sum`.

**Regime labeling** (`classifyFittedStatesIntoRegimeLabels`): a real,
documented rule applied to the FITTED per-state statistics (not to the
raw data) — the state with the largest fitted variance is labeled
`HIGH_VOLATILITY` (variance is literally what "high-vol" means
statistically); among the remaining states, the one with the largest
fitted mean is labeled `TRENDING` (a persistently positive-mean state is
definitionally a trending-up regime); any other remaining states are
labeled `MEAN_REVERTING` (near-zero/mixed-sign mean, lower variance than
the high-vol state — the residual "neither strongly trending nor
volatile" case). With the default `numberOfStates=3` this produces
exactly one label per state; with `numberOfStates=2` (an acceptable
reduced scope per FEATURES.md's own minimum-scope guidance) only
`HIGH_VOLATILITY` and one of `TRENDING`/`MEAN_REVERTING` are produced,
whichever the second state's fitted mean indicates.

**Known limitation, stated honestly**: Baum-Welch is a local-optimum
hill-climbing algorithm — it is NOT guaranteed to find the globally
best-fitting parameters, and its result depends on the initial parameter
guess (`buildQuantileInitializedHmmParameters` below is a real,
documented, data-driven initialization — quantile-bucketing the
observations to seed each state's mean/variance — not a random restart
scheme; running multiple random restarts and keeping the best
log-likelihood is a documented possible future improvement, not
implemented here).

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

import math
from dataclasses import dataclass, field
from enum import Enum

_MINIMUM_VARIANCE_FLOOR = 1e-8


class MarketRegimeLabel(Enum):
    TRENDING = "TRENDING"
    MEAN_REVERTING = "MEAN_REVERTING"
    HIGH_VOLATILITY = "HIGH_VOLATILITY"


def gaussianEmissionProbabilityDensity(observedValue: float, mean: float, variance: float) -> float:
    """The standard univariate normal probability density function.
    Raises `ValueError` if `variance` is not strictly positive (a
    degenerate/zero-variance emission is not a valid Gaussian).
    """
    if variance <= 0:
        raise ValueError("variance must be strictly positive")
    exponent = -((observedValue - mean) ** 2) / (2.0 * variance)
    try:
        return (1.0 / math.sqrt(2.0 * math.pi * variance)) * math.exp(exponent)
    except OverflowError:
        return 0.0


def _logGaussianEmissionProbabilityDensity(observedValue: float, mean: float, variance: float) -> float:
    """Same Gaussian log-density as `gaussianEmissionProbabilityDensity`,
    computed directly in log-space so an observation many standard
    deviations from a very tight-variance state's mean (a real
    possibility once Baum-Welch has converged some state to a very small
    variance) doesn't first underflow to a literal `0.0` density and then
    fail `math.log(0.0)` — used internally by `runViterbiAlgorithm`.
    """
    if variance <= 0:
        raise ValueError("variance must be strictly positive")
    return -0.5 * math.log(2.0 * math.pi * variance) - ((observedValue - mean) ** 2) / (2.0 * variance)


@dataclass
class GaussianHiddenMarkovModelParameters:
    numberOfStates: int
    initialStateProbabilities: list[float]
    transitionMatrix: list[list[float]]
    stateMeans: list[float]
    stateVariances: list[float]

    def __post_init__(self) -> None:
        if self.numberOfStates < 2:
            raise ValueError("numberOfStates must be at least 2")
        if len(self.initialStateProbabilities) != self.numberOfStates:
            raise ValueError("initialStateProbabilities length must equal numberOfStates")
        if len(self.transitionMatrix) != self.numberOfStates or any(
            len(row) != self.numberOfStates for row in self.transitionMatrix
        ):
            raise ValueError("transitionMatrix must be numberOfStates x numberOfStates")
        if len(self.stateMeans) != self.numberOfStates or len(self.stateVariances) != self.numberOfStates:
            raise ValueError("stateMeans/stateVariances length must equal numberOfStates")
        if any(variance <= 0 for variance in self.stateVariances):
            raise ValueError("every stateVariance must be strictly positive")


def buildQuantileInitializedHmmParameters(
    observations: list[float], numberOfStates: int = 3
) -> GaussianHiddenMarkovModelParameters:
    """Real, deterministic, data-driven Baum-Welch initialization: sorts
    `observations`, splits them into `numberOfStates` equal-size
    quantile buckets, and seeds each state's mean/variance from its
    bucket's sample mean/variance (population convention, matching
    `correlationMatrixEngine.py`/`riskStatistics.py`'s convention
    elsewhere in this codebase). The initial-state distribution is
    uniform; the transition matrix starts with a real, documented
    "sticky diagonal" bias (0.85 self-transition, remaining probability
    split evenly across the other states) — regimes are assumed to
    persist for more than one observation on average, a standard,
    reasonable HMM-regime-detection prior, not a claim about any real
    market's actual persistence.
    """
    if numberOfStates < 2:
        raise ValueError("numberOfStates must be at least 2")
    if len(observations) < numberOfStates:
        raise ValueError("observations must contain at least numberOfStates values")

    sortedObservations = sorted(observations)
    bucketSize = len(sortedObservations) / numberOfStates
    stateMeans: list[float] = []
    stateVariances: list[float] = []
    for stateIndex in range(numberOfStates):
        startIndex = int(round(stateIndex * bucketSize))
        endIndex = int(round((stateIndex + 1) * bucketSize)) if stateIndex < numberOfStates - 1 else len(sortedObservations)
        endIndex = max(endIndex, startIndex + 1)
        bucket = sortedObservations[startIndex:endIndex]
        bucketMean = sum(bucket) / len(bucket)
        bucketVariance = sum((v - bucketMean) ** 2 for v in bucket) / len(bucket)
        stateMeans.append(bucketMean)
        stateVariances.append(max(bucketVariance, _MINIMUM_VARIANCE_FLOOR))

    selfTransitionProbability = 0.85
    otherTransitionProbability = (1.0 - selfTransitionProbability) / (numberOfStates - 1)
    transitionMatrix = [
        [selfTransitionProbability if i == j else otherTransitionProbability for j in range(numberOfStates)]
        for i in range(numberOfStates)
    ]

    return GaussianHiddenMarkovModelParameters(
        numberOfStates=numberOfStates,
        initialStateProbabilities=[1.0 / numberOfStates] * numberOfStates,
        transitionMatrix=transitionMatrix,
        stateMeans=stateMeans,
        stateVariances=stateVariances,
    )


@dataclass(frozen=True)
class ForwardAlgorithmResult:
    scaledAlpha: list[list[float]]
    scalingFactors: list[float]
    logLikelihood: float


def runForwardAlgorithm(
    observations: list[float], hmm: GaussianHiddenMarkovModelParameters
) -> ForwardAlgorithmResult:
    """Real scaled forward algorithm (see module docstring for the
    formula). `scaledAlpha[t][i]` is `alphaHat_t(i)`; `scalingFactors[t]`
    is `c_t`; `logLikelihood` is `sum(log(c_t))`, the log-probability of
    the ENTIRE observed sequence under `hmm`. Raises `ValueError` on an
    empty `observations` list.
    """
    if not observations:
        raise ValueError("observations must contain at least one value")

    numberOfStates = hmm.numberOfStates
    timeStepCount = len(observations)
    scaledAlpha: list[list[float]] = [[0.0] * numberOfStates for _ in range(timeStepCount)]
    scalingFactors: list[float] = [0.0] * timeStepCount

    unscaledFirst = [
        hmm.initialStateProbabilities[i]
        * gaussianEmissionProbabilityDensity(observations[0], hmm.stateMeans[i], hmm.stateVariances[i])
        for i in range(numberOfStates)
    ]
    scalingFactors[0] = sum(unscaledFirst)
    scaledAlpha[0] = [value / scalingFactors[0] for value in unscaledFirst]

    for t in range(1, timeStepCount):
        emissionProbabilities = [
            gaussianEmissionProbabilityDensity(observations[t], hmm.stateMeans[j], hmm.stateVariances[j])
            for j in range(numberOfStates)
        ]
        unscaled = [
            sum(scaledAlpha[t - 1][i] * hmm.transitionMatrix[i][j] for i in range(numberOfStates))
            * emissionProbabilities[j]
            for j in range(numberOfStates)
        ]
        scalingFactors[t] = sum(unscaled)
        scaledAlpha[t] = [value / scalingFactors[t] for value in unscaled]

    logLikelihood = sum(math.log(c) for c in scalingFactors)
    return ForwardAlgorithmResult(scaledAlpha=scaledAlpha, scalingFactors=scalingFactors, logLikelihood=logLikelihood)


def runBackwardAlgorithm(
    observations: list[float], hmm: GaussianHiddenMarkovModelParameters, scalingFactors: list[float]
) -> list[list[float]]:
    """Real scaled backward algorithm, reusing the SAME `scalingFactors`
    the forward pass computed (required for `gamma`/`xi` below to be
    correctly normalized — see Rabiner 1989 §V-A). `scaledBeta[t][i]` is
    `betaHat_t(i)`.
    """
    numberOfStates = hmm.numberOfStates
    timeStepCount = len(observations)
    scaledBeta: list[list[float]] = [[0.0] * numberOfStates for _ in range(timeStepCount)]
    scaledBeta[timeStepCount - 1] = [1.0] * numberOfStates

    for t in range(timeStepCount - 2, -1, -1):
        emissionProbabilitiesNext = [
            gaussianEmissionProbabilityDensity(observations[t + 1], hmm.stateMeans[j], hmm.stateVariances[j])
            for j in range(numberOfStates)
        ]
        for i in range(numberOfStates):
            unscaledValue = sum(
                hmm.transitionMatrix[i][j] * emissionProbabilitiesNext[j] * scaledBeta[t + 1][j]
                for j in range(numberOfStates)
            )
            scaledBeta[t][i] = unscaledValue / scalingFactors[t + 1]

    return scaledBeta


@dataclass(frozen=True)
class BaumWelchFitResult:
    fittedParameters: GaussianHiddenMarkovModelParameters
    logLikelihoodHistory: list[float]
    iterationCount: int


def runBaumWelchExpectationMaximization(
    observations: list[float],
    initialParameters: GaussianHiddenMarkovModelParameters,
    maximumIterationCount: int = 50,
    convergenceTolerance: float = 1e-6,
) -> BaumWelchFitResult:
    """Real Baum-Welch EM loop (see module docstring). Returns the fitted
    parameters plus the full log-likelihood history (one value per
    iteration) so a caller/test can verify the log-likelihood is
    monotonically non-decreasing (a genuine correctness property of EM —
    each iteration can only improve or hold the log-likelihood, never
    reduce it) and that it converged before `maximumIterationCount`.
    """
    if len(observations) < 2:
        raise ValueError("observations must contain at least two values to fit an HMM")

    numberOfStates = initialParameters.numberOfStates
    currentParameters = initialParameters
    logLikelihoodHistory: list[float] = []
    timeStepCount = len(observations)

    iterationCount = 0
    for iterationCount in range(1, maximumIterationCount + 1):
        forwardResult = runForwardAlgorithm(observations, currentParameters)
        backwardResult = runBackwardAlgorithm(observations, currentParameters, forwardResult.scalingFactors)
        logLikelihoodHistory.append(forwardResult.logLikelihood)

        # E-step: gamma_t(i) = alphaHat_t(i) * betaHat_t(i).
        gamma = [
            [forwardResult.scaledAlpha[t][i] * backwardResult[t][i] for i in range(numberOfStates)]
            for t in range(timeStepCount)
        ]

        # E-step: xi_t(i, j) for t = 0 .. T-2.
        xi: list[list[list[float]]] = []
        for t in range(timeStepCount - 1):
            emissionProbabilitiesNext = [
                gaussianEmissionProbabilityDensity(
                    observations[t + 1], currentParameters.stateMeans[j], currentParameters.stateVariances[j]
                )
                for j in range(numberOfStates)
            ]
            xiT = [
                [
                    forwardResult.scaledAlpha[t][i]
                    * currentParameters.transitionMatrix[i][j]
                    * emissionProbabilitiesNext[j]
                    * backwardResult[t + 1][j]
                    / forwardResult.scalingFactors[t + 1]
                    for j in range(numberOfStates)
                ]
                for i in range(numberOfStates)
            ]
            xi.append(xiT)

        # M-step.
        newInitialStateProbabilities = list(gamma[0])

        newTransitionMatrix = []
        for i in range(numberOfStates):
            denominator = sum(gamma[t][i] for t in range(timeStepCount - 1))
            if denominator <= 0:
                newTransitionMatrix.append(list(currentParameters.transitionMatrix[i]))
                continue
            newTransitionMatrix.append(
                [sum(xi[t][i][j] for t in range(timeStepCount - 1)) / denominator for j in range(numberOfStates)]
            )

        newStateMeans = []
        newStateVariances = []
        for i in range(numberOfStates):
            stateWeightTotal = sum(gamma[t][i] for t in range(timeStepCount))
            if stateWeightTotal <= 0:
                newStateMeans.append(currentParameters.stateMeans[i])
                newStateVariances.append(currentParameters.stateVariances[i])
                continue
            newMean = sum(gamma[t][i] * observations[t] for t in range(timeStepCount)) / stateWeightTotal
            newVariance = (
                sum(gamma[t][i] * (observations[t] - newMean) ** 2 for t in range(timeStepCount)) / stateWeightTotal
            )
            newStateMeans.append(newMean)
            newStateVariances.append(max(newVariance, _MINIMUM_VARIANCE_FLOOR))

        currentParameters = GaussianHiddenMarkovModelParameters(
            numberOfStates=numberOfStates,
            initialStateProbabilities=newInitialStateProbabilities,
            transitionMatrix=newTransitionMatrix,
            stateMeans=newStateMeans,
            stateVariances=newStateVariances,
        )

        if len(logLikelihoodHistory) >= 2 and (
            logLikelihoodHistory[-1] - logLikelihoodHistory[-2] < convergenceTolerance
        ):
            break

    finalForward = runForwardAlgorithm(observations, currentParameters)
    logLikelihoodHistory.append(finalForward.logLikelihood)

    return BaumWelchFitResult(
        fittedParameters=currentParameters,
        logLikelihoodHistory=logLikelihoodHistory,
        iterationCount=iterationCount,
    )


@dataclass(frozen=True)
class ViterbiResult:
    mostLikelyStateSequence: list[int]
    logProbability: float


def runViterbiAlgorithm(observations: list[float], hmm: GaussianHiddenMarkovModelParameters) -> ViterbiResult:
    """Real Viterbi most-likely-state-sequence decoding in log-space
    (avoids the underflow the forward algorithm's scaling also guards
    against, via a genuinely separate `max`-based DP recurrence rather
    than reusing the forward pass's `sum`-based probabilities). Raises
    `ValueError` on an empty `observations` list.
    """
    if not observations:
        raise ValueError("observations must contain at least one value")

    numberOfStates = hmm.numberOfStates
    timeStepCount = len(observations)

    logInitial = [math.log(p) if p > 0 else -math.inf for p in hmm.initialStateProbabilities]
    logTransition = [
        [math.log(p) if p > 0 else -math.inf for p in row] for row in hmm.transitionMatrix
    ]

    logDelta = [[0.0] * numberOfStates for _ in range(timeStepCount)]
    backpointer = [[0] * numberOfStates for _ in range(timeStepCount)]

    for i in range(numberOfStates):
        logEmission = _logGaussianEmissionProbabilityDensity(observations[0], hmm.stateMeans[i], hmm.stateVariances[i])
        logDelta[0][i] = logInitial[i] + logEmission

    for t in range(1, timeStepCount):
        for j in range(numberOfStates):
            logEmission = _logGaussianEmissionProbabilityDensity(observations[t], hmm.stateMeans[j], hmm.stateVariances[j])
            bestPreviousState = max(
                range(numberOfStates), key=lambda i: logDelta[t - 1][i] + logTransition[i][j]
            )
            logDelta[t][j] = logDelta[t - 1][bestPreviousState] + logTransition[bestPreviousState][j] + logEmission
            backpointer[t][j] = bestPreviousState

    finalState = max(range(numberOfStates), key=lambda i: logDelta[timeStepCount - 1][i])
    logProbability = logDelta[timeStepCount - 1][finalState]

    stateSequence = [0] * timeStepCount
    stateSequence[timeStepCount - 1] = finalState
    for t in range(timeStepCount - 2, -1, -1):
        stateSequence[t] = backpointer[t + 1][stateSequence[t + 1]]

    return ViterbiResult(mostLikelyStateSequence=stateSequence, logProbability=logProbability)


def classifyFittedStatesIntoRegimeLabels(
    hmm: GaussianHiddenMarkovModelParameters,
) -> dict[int, MarketRegimeLabel]:
    """Real, documented labeling rule applied to FITTED per-state
    statistics (see module docstring for the exact rule): highest
    fitted variance -> `HIGH_VOLATILITY`; among the rest, highest fitted
    mean -> `TRENDING`; everything else -> `MEAN_REVERTING`.
    """
    stateIndices = list(range(hmm.numberOfStates))
    highVolatilityState = max(stateIndices, key=lambda i: hmm.stateVariances[i])
    remainingStates = [i for i in stateIndices if i != highVolatilityState]

    labels: dict[int, MarketRegimeLabel] = {highVolatilityState: MarketRegimeLabel.HIGH_VOLATILITY}
    if remainingStates:
        trendingState = max(remainingStates, key=lambda i: hmm.stateMeans[i])
        labels[trendingState] = MarketRegimeLabel.TRENDING
        for i in remainingStates:
            if i != trendingState:
                labels[i] = MarketRegimeLabel.MEAN_REVERTING

    return labels


@dataclass(frozen=True)
class RegimeDetectionResult:
    fittedParameters: GaussianHiddenMarkovModelParameters
    logLikelihoodHistory: list[float]
    mostLikelyStateSequence: list[int]
    regimeLabelByState: dict[int, MarketRegimeLabel]
    regimeLabelSequence: list[MarketRegimeLabel]
    currentRegimeLabel: MarketRegimeLabel


def runRegimeDetection(
    returnSeries: list[float],
    numberOfStates: int = 3,
    maximumIterationCount: int = 50,
    convergenceTolerance: float = 1e-6,
) -> RegimeDetectionResult:
    """End-to-end pipeline: quantile-initialize a Gaussian HMM over
    `returnSeries`, fit it with real Baum-Welch EM, decode the most
    likely state sequence with real Viterbi, and classify each fitted
    state into a `MarketRegimeLabel`. `currentRegimeLabel` is the label
    of the LAST observation's inferred state — the one a strategy gate
    would act on right now.
    """
    initialParameters = buildQuantileInitializedHmmParameters(returnSeries, numberOfStates)
    fitResult = runBaumWelchExpectationMaximization(
        returnSeries, initialParameters, maximumIterationCount, convergenceTolerance
    )
    viterbiResult = runViterbiAlgorithm(returnSeries, fitResult.fittedParameters)
    regimeLabelByState = classifyFittedStatesIntoRegimeLabels(fitResult.fittedParameters)
    regimeLabelSequence = [regimeLabelByState[state] for state in viterbiResult.mostLikelyStateSequence]

    return RegimeDetectionResult(
        fittedParameters=fitResult.fittedParameters,
        logLikelihoodHistory=fitResult.logLikelihoodHistory,
        mostLikelyStateSequence=viterbiResult.mostLikelyStateSequence,
        regimeLabelByState=regimeLabelByState,
        regimeLabelSequence=regimeLabelSequence,
        currentRegimeLabel=regimeLabelSequence[-1],
    )
