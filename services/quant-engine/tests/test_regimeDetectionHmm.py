import math

import pytest

from quantengine.regimeDetectionHmm import (
    BaumWelchFitResult,
    ForwardAlgorithmResult,
    GaussianHiddenMarkovModelParameters,
    MarketRegimeLabel,
    RegimeDetectionResult,
    ViterbiResult,
    buildQuantileInitializedHmmParameters,
    classifyFittedStatesIntoRegimeLabels,
    gaussianEmissionProbabilityDensity,
    runBackwardAlgorithm,
    runBaumWelchExpectationMaximization,
    runForwardAlgorithm,
    runRegimeDetection,
    runViterbiAlgorithm,
)


def buildTinyTwoStateHmm() -> GaussianHiddenMarkovModelParameters:
    # State 0: low/near-zero mean ("quiet"); State 1: high mean ("hot").
    # Both unit variance for a clean hand-computable example.
    return GaussianHiddenMarkovModelParameters(
        numberOfStates=2,
        initialStateProbabilities=[0.6, 0.4],
        transitionMatrix=[[0.7, 0.3], [0.4, 0.6]],
        stateMeans=[0.0, 5.0],
        stateVariances=[1.0, 1.0],
    )


class TestGaussianEmissionProbabilityDensity:
    def testPeakValueAtTheMeanMatchesStandardNormalPeak(self):
        # standard normal peak density = 1/sqrt(2*pi) = 0.3989422804014327
        density = gaussianEmissionProbabilityDensity(0.0, mean=0.0, variance=1.0)
        assert density == pytest.approx(0.3989422804014327, rel=1e-12)

    def testRaisesOnNonPositiveVariance(self):
        with pytest.raises(ValueError):
            gaussianEmissionProbabilityDensity(0.0, mean=0.0, variance=0.0)


class TestGaussianHiddenMarkovModelParametersValidation:
    def testRaisesOnTooFewStates(self):
        with pytest.raises(ValueError):
            GaussianHiddenMarkovModelParameters(
                numberOfStates=1, initialStateProbabilities=[1.0], transitionMatrix=[[1.0]], stateMeans=[0.0], stateVariances=[1.0]
            )

    def testRaisesOnMismatchedTransitionMatrixShape(self):
        with pytest.raises(ValueError):
            GaussianHiddenMarkovModelParameters(
                numberOfStates=2,
                initialStateProbabilities=[0.5, 0.5],
                transitionMatrix=[[0.5, 0.5]],
                stateMeans=[0.0, 1.0],
                stateVariances=[1.0, 1.0],
            )

    def testRaisesOnNonPositiveVariance(self):
        with pytest.raises(ValueError):
            GaussianHiddenMarkovModelParameters(
                numberOfStates=2,
                initialStateProbabilities=[0.5, 0.5],
                transitionMatrix=[[0.5, 0.5], [0.5, 0.5]],
                stateMeans=[0.0, 1.0],
                stateVariances=[1.0, 0.0],
            )


class TestRunForwardAlgorithmHandWorked:
    # Hand-computed (see the module test's git history / task transcript
    # for the derivation script) forward pass over observations [0.0, 5.0]
    # with pi=[0.6,0.4], A=[[0.7,0.3],[0.4,0.6]], state0~N(0,1), state1~N(5,1):
    #
    # unscaled alpha_1 = [0.6*pdf(0;0,1), 0.4*pdf(0;5,1)]
    #                  = [0.6*0.3989422804014327, 0.4*1.4867195147342977e-06]
    #                  = [0.23936536824085962, 5.946878058937191e-07]
    # c1 = 0.2393659629286655
    # alphaHat_1 = [0.9999975155707244, 2.4844292756482867e-06]
    #
    # emis2 = [pdf(5;0,1), pdf(5;5,1)] = [1.4867195147342977e-06, 0.3989422804014327]
    # unscaled alpha_2(0) = (alphaHat1(0)*0.7 + alphaHat1(1)*0.4) * emis2[0]
    # unscaled alpha_2(1) = (alphaHat1(0)*0.3 + alphaHat1(1)*0.6) * emis2[1]
    # unscaled_2 = [1.0407025522191621e-06, 0.11968298146359402]
    # c2 = 0.11968402216614624
    # alphaHat_2 = [8.695417595294811e-06, 0.9999913045824047]
    # logLikelihood = ln(c1) + ln(c2) = -3.5526618301873203

    def testHandWorkedTwoStepForwardProbabilities(self):
        hmm = buildTinyTwoStateHmm()
        result = runForwardAlgorithm([0.0, 5.0], hmm)
        assert isinstance(result, ForwardAlgorithmResult)

        assert result.scalingFactors[0] == pytest.approx(0.2393659629286655, rel=1e-9)
        assert result.scaledAlpha[0][0] == pytest.approx(0.9999975155707244, rel=1e-9)
        assert result.scaledAlpha[0][1] == pytest.approx(2.4844292756482867e-06, rel=1e-6)

        assert result.scalingFactors[1] == pytest.approx(0.11968402216614624, rel=1e-9)
        assert result.scaledAlpha[1][0] == pytest.approx(8.695417595294811e-06, rel=1e-6)
        assert result.scaledAlpha[1][1] == pytest.approx(0.9999913045824047, rel=1e-9)

        assert result.logLikelihood == pytest.approx(-3.5526618301873203, rel=1e-9)

    def testEachTimeStepsScaledAlphaSumsToOne(self):
        # a genuine correctness property of the scaling normalization.
        hmm = buildTinyTwoStateHmm()
        result = runForwardAlgorithm([0.0, 1.0, 5.0, 4.5, 0.2], hmm)
        for row in result.scaledAlpha:
            assert sum(row) == pytest.approx(1.0, rel=1e-9)

    def testRaisesOnEmptyObservations(self):
        with pytest.raises(ValueError):
            runForwardAlgorithm([], buildTinyTwoStateHmm())


class TestRunBackwardAlgorithm:
    def testLastTimeStepBetaIsAllOnes(self):
        hmm = buildTinyTwoStateHmm()
        forwardResult = runForwardAlgorithm([0.0, 5.0, 0.1], hmm)
        beta = runBackwardAlgorithm([0.0, 5.0, 0.1], hmm, forwardResult.scalingFactors)
        assert beta[-1] == [1.0, 1.0]

    def testForwardBackwardProductGivesValidStateOccupationProbabilities(self):
        # gamma_t(i) = alphaHat_t(i) * betaHat_t(i) must sum to 1 across
        # states at every time step — a real correctness property.
        hmm = buildTinyTwoStateHmm()
        observations = [0.0, 4.8, 5.1, 0.3, 4.9]
        forwardResult = runForwardAlgorithm(observations, hmm)
        beta = runBackwardAlgorithm(observations, hmm, forwardResult.scalingFactors)
        for t in range(len(observations)):
            gammaSum = sum(forwardResult.scaledAlpha[t][i] * beta[t][i] for i in range(hmm.numberOfStates))
            assert gammaSum == pytest.approx(1.0, rel=1e-9)


class TestRunViterbiAlgorithmHandWorked:
    def testObviousTwoStateSequenceIsRecoveredExactly(self):
        # observations sit squarely on each state's mean with unit
        # variance and a strong self-transition bias -> Viterbi should
        # trivially recover [state0, state0, state1, state1].
        hmm = GaussianHiddenMarkovModelParameters(
            numberOfStates=2,
            initialStateProbabilities=[0.5, 0.5],
            transitionMatrix=[[0.9, 0.1], [0.1, 0.9]],
            stateMeans=[0.0, 10.0],
            stateVariances=[0.25, 0.25],
        )
        observations = [0.1, -0.1, 10.1, 9.9]
        result = runViterbiAlgorithm(observations, hmm)
        assert isinstance(result, ViterbiResult)
        assert result.mostLikelyStateSequence == [0, 0, 1, 1]
        assert result.logProbability < 0  # log-probability of a proper density product

    def testRaisesOnEmptyObservations(self):
        with pytest.raises(ValueError):
            runViterbiAlgorithm([], buildTinyTwoStateHmm())


class TestBuildQuantileInitializedHmmParameters:
    def testProducesValidStochasticInitialization(self):
        observations = [1.0, 2.0, 3.0, 10.0, 11.0, 12.0, 0.1, -0.1, 0.0]
        hmm = buildQuantileInitializedHmmParameters(observations, numberOfStates=3)
        assert hmm.numberOfStates == 3
        assert sum(hmm.initialStateProbabilities) == pytest.approx(1.0)
        for row in hmm.transitionMatrix:
            assert sum(row) == pytest.approx(1.0)
        assert all(v > 0 for v in hmm.stateVariances)

    def testRaisesWhenFewerObservationsThanStates(self):
        with pytest.raises(ValueError):
            buildQuantileInitializedHmmParameters([1.0, 2.0], numberOfStates=3)


class TestRunBaumWelchExpectationMaximization:
    def testLogLikelihoodIsMonotonicallyNonDecreasing(self):
        # a genuine correctness property of EM: each iteration can only
        # improve (or hold) the log-likelihood, never reduce it.
        observations = [0.1, -0.2, 0.05, 5.1, 4.9, 5.2, -0.1, 0.15, 5.0, 0.0, -10.0, -9.8, -10.2]
        initialParameters = buildQuantileInitializedHmmParameters(observations, numberOfStates=3)
        fitResult = runBaumWelchExpectationMaximization(observations, initialParameters, maximumIterationCount=25)
        assert isinstance(fitResult, BaumWelchFitResult)
        history = fitResult.logLikelihoodHistory
        for earlier, later in zip(history, history[1:]):
            assert later >= earlier - 1e-9  # tiny float tolerance

    def testFittedParametersRemainValidAfterFitting(self):
        observations = [0.1, -0.2, 0.05, 5.1, 4.9, 5.2, -0.1, 0.15, 5.0]
        initialParameters = buildQuantileInitializedHmmParameters(observations, numberOfStates=2)
        fitResult = runBaumWelchExpectationMaximization(observations, initialParameters, maximumIterationCount=25)
        fitted = fitResult.fittedParameters
        assert sum(fitted.initialStateProbabilities) == pytest.approx(1.0, rel=1e-6)
        for row in fitted.transitionMatrix:
            assert sum(row) == pytest.approx(1.0, rel=1e-6)
        assert all(v > 0 for v in fitted.stateVariances)

    def testRaisesOnFewerThanTwoObservations(self):
        with pytest.raises(ValueError):
            runBaumWelchExpectationMaximization([1.0], buildTinyTwoStateHmm())


class TestClassifyFittedStatesIntoRegimeLabels:
    def testThreeStateLabelingHandWorked(self):
        # state0: mean 0, var 1 (mean-reverting: neither highest mean nor
        #         highest variance)
        # state1: mean 3, var 1 (trending: highest mean among non-high-vol states)
        # state2: mean 0, var 25 (high-vol: highest variance by far)
        hmm = GaussianHiddenMarkovModelParameters(
            numberOfStates=3,
            initialStateProbabilities=[1 / 3, 1 / 3, 1 / 3],
            transitionMatrix=[[1 / 3] * 3] * 3,
            stateMeans=[0.0, 3.0, 0.0],
            stateVariances=[1.0, 1.0, 25.0],
        )
        labels = classifyFittedStatesIntoRegimeLabels(hmm)
        assert labels[2] == MarketRegimeLabel.HIGH_VOLATILITY
        assert labels[1] == MarketRegimeLabel.TRENDING
        assert labels[0] == MarketRegimeLabel.MEAN_REVERTING

    def testTwoStateLabelingProducesHighVolAndOneOther(self):
        hmm = GaussianHiddenMarkovModelParameters(
            numberOfStates=2,
            initialStateProbabilities=[0.5, 0.5],
            transitionMatrix=[[0.5, 0.5], [0.5, 0.5]],
            stateMeans=[2.0, 0.0],
            stateVariances=[1.0, 9.0],
        )
        labels = classifyFittedStatesIntoRegimeLabels(hmm)
        assert labels[1] == MarketRegimeLabel.HIGH_VOLATILITY
        assert labels[0] == MarketRegimeLabel.TRENDING


class TestRunRegimeDetectionEndToEnd:
    def testEndToEndPipelineProducesLabeledRegimeSequence(self):
        # a synthetic series with an obvious quiet block, a trending
        # block, and a volatile block.
        quietBlock = [0.01, -0.02, 0.015, -0.01, 0.02, -0.015, 0.01]
        trendingBlock = [1.0, 1.05, 1.1, 1.02, 1.08, 1.15, 1.2]
        volatileBlock = [5.0, -6.0, 7.0, -8.0, 6.5, -7.5, 8.0]
        observations = quietBlock + trendingBlock + volatileBlock

        result = runRegimeDetection(observations, numberOfStates=3, maximumIterationCount=30)
        assert isinstance(result, RegimeDetectionResult)
        assert len(result.mostLikelyStateSequence) == len(observations)
        assert len(result.regimeLabelSequence) == len(observations)
        assert set(result.regimeLabelByState.values()) <= set(MarketRegimeLabel)
        assert isinstance(result.currentRegimeLabel, MarketRegimeLabel)
        # the volatile block should dominate the HIGH_VOLATILITY state's
        # occupied observations far more than the quiet block does.
        volatileStateLabels = result.regimeLabelSequence[len(quietBlock) + len(trendingBlock):]
        assert MarketRegimeLabel.HIGH_VOLATILITY in volatileStateLabels

    def testTwoStateReducedScopeAlsoWorksEndToEnd(self):
        observations = [0.01, -0.01, 0.02, 5.0, -6.0, 7.0, -5.5, 0.0, 0.01]
        result = runRegimeDetection(observations, numberOfStates=2, maximumIterationCount=20)
        assert len(result.regimeLabelSequence) == len(observations)
        assert MarketRegimeLabel.HIGH_VOLATILITY in result.regimeLabelByState.values()
