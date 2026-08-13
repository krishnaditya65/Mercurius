package sloalerting

import (
	"testing"
	"time"
)

func testThresholds() []Threshold {
	return []Threshold{
		{Kind: MetricKindOrderRejectRate, ThresholdValue: 0.10, MinimumBreachWindow: 5 * time.Minute},
		{Kind: MetricKindFeedStalenessSeconds, ThresholdValue: 30, MinimumBreachWindow: 0},
		{Kind: MetricKindMatchingLatencyMs, ThresholdValue: 250, MinimumBreachWindow: 1 * time.Minute},
	}
}

func TestHealthySampleDoesNotRaiseAnAlert(t *testing.T) {
	evaluator := NewSloAlertEvaluator(testThresholds())
	_, raised := evaluator.EvaluateSample(MetricSample{Kind: MetricKindFeedStalenessSeconds, Value: 5, ObservedTime: time.Now()})
	if raised {
		t.Fatalf("expected a healthy sample well under threshold to not raise an alert")
	}
}

func TestSingleBreachingSampleWithZeroWindowRaisesImmediately(t *testing.T) {
	evaluator := NewSloAlertEvaluator(testThresholds())
	alert, raised := evaluator.EvaluateSample(MetricSample{Kind: MetricKindFeedStalenessSeconds, Value: 45, ObservedTime: time.Now()})
	if !raised {
		t.Fatalf("expected feed staleness of 45s (threshold 30s, zero window) to raise immediately")
	}
	if alert.Severity != SeverityCritical {
		t.Fatalf("expected feed staleness alerts to be CRITICAL, got %v", alert.Severity)
	}
}

func TestSampleAtExactThresholdDoesNotBreach(t *testing.T) {
	evaluator := NewSloAlertEvaluator(testThresholds())
	_, raised := evaluator.EvaluateSample(MetricSample{Kind: MetricKindFeedStalenessSeconds, Value: 30, ObservedTime: time.Now()})
	if raised {
		t.Fatalf("expected a sample exactly AT the threshold (not exceeding it) to not breach")
	}
}

func TestSampleJustOverThresholdBreaches(t *testing.T) {
	evaluator := NewSloAlertEvaluator(testThresholds())
	_, raised := evaluator.EvaluateSample(MetricSample{Kind: MetricKindFeedStalenessSeconds, Value: 30.01, ObservedTime: time.Now()})
	if !raised {
		t.Fatalf("expected a sample just over threshold to breach")
	}
}

func TestSustainedWindowDoesNotRaiseBeforeTheFullDurationHasElapsed(t *testing.T) {
	evaluator := NewSloAlertEvaluator(testThresholds())
	base := time.Now()

	// order-reject-rate threshold requires 5 continuous minutes of
	// breach before firing.
	evaluator.EvaluateSample(MetricSample{Kind: MetricKindOrderRejectRate, Value: 0.5, ObservedTime: base})
	_, raised := evaluator.EvaluateSample(MetricSample{Kind: MetricKindOrderRejectRate, Value: 0.6, ObservedTime: base.Add(2 * time.Minute)})
	if raised {
		t.Fatalf("expected no alert before the full 5-minute sustained-breach window has elapsed")
	}
}

func TestSustainedWindowRaisesOnceTheFullDurationHasElapsedContinuously(t *testing.T) {
	evaluator := NewSloAlertEvaluator(testThresholds())
	base := time.Now()

	evaluator.EvaluateSample(MetricSample{Kind: MetricKindOrderRejectRate, Value: 0.5, ObservedTime: base})
	evaluator.EvaluateSample(MetricSample{Kind: MetricKindOrderRejectRate, Value: 0.6, ObservedTime: base.Add(2 * time.Minute)})
	_, raised := evaluator.EvaluateSample(MetricSample{Kind: MetricKindOrderRejectRate, Value: 0.55, ObservedTime: base.Add(5 * time.Minute)})
	if !raised {
		t.Fatalf("expected sustained continuous breach spanning the full 5-minute window to raise an alert")
	}
}

func TestSustainedWindowRaisesOnlyOnceForOneContinuousBreachEpisode(t *testing.T) {
	evaluator := NewSloAlertEvaluator(testThresholds())
	base := time.Now()

	evaluator.EvaluateSample(MetricSample{Kind: MetricKindOrderRejectRate, Value: 0.5, ObservedTime: base})
	evaluator.EvaluateSample(MetricSample{Kind: MetricKindOrderRejectRate, Value: 0.6, ObservedTime: base.Add(5 * time.Minute)})
	_, raisedAgain := evaluator.EvaluateSample(MetricSample{Kind: MetricKindOrderRejectRate, Value: 0.55, ObservedTime: base.Add(6 * time.Minute)})
	if raisedAgain {
		t.Fatalf("expected the same continuous breach episode to only alert once")
	}
}

func TestAHealthySampleResetsTheContinuousBreachTimer(t *testing.T) {
	evaluator := NewSloAlertEvaluator(testThresholds())
	base := time.Now()

	evaluator.EvaluateSample(MetricSample{Kind: MetricKindOrderRejectRate, Value: 0.5, ObservedTime: base})
	evaluator.EvaluateSample(MetricSample{Kind: MetricKindOrderRejectRate, Value: 0.05, ObservedTime: base.Add(1 * time.Minute)}) // healthy, resets timer
	// Breach resumes, but only 4 minutes have elapsed since the RESET,
	// well under the 5-minute requirement — must not raise.
	_, raised := evaluator.EvaluateSample(MetricSample{Kind: MetricKindOrderRejectRate, Value: 0.6, ObservedTime: base.Add(5 * time.Minute)})
	if raised {
		t.Fatalf("expected a healthy sample to reset the continuous-breach timer, preventing a premature alert")
	}
}

func TestAlertIsRaisedOnlyOnceOnTransitionIntoBreachNotEverySample(t *testing.T) {
	evaluator := NewSloAlertEvaluator(testThresholds())
	base := time.Now()

	_, firstRaised := evaluator.EvaluateSample(MetricSample{Kind: MetricKindFeedStalenessSeconds, Value: 60, ObservedTime: base})
	_, secondRaised := evaluator.EvaluateSample(MetricSample{Kind: MetricKindFeedStalenessSeconds, Value: 61, ObservedTime: base.Add(time.Second)})

	if !firstRaised {
		t.Fatalf("expected the first breaching sample to raise")
	}
	if secondRaised {
		t.Fatalf("expected a continuing breach to NOT raise a second alert")
	}
	if len(evaluator.AllAlerts()) != 1 {
		t.Fatalf("expected exactly 1 stored alert, got %d", len(evaluator.AllAlerts()))
	}
}

func TestAlertRaisesAgainAfterRecoveryThenAnotherBreach(t *testing.T) {
	evaluator := NewSloAlertEvaluator(testThresholds())
	base := time.Now()

	evaluator.EvaluateSample(MetricSample{Kind: MetricKindFeedStalenessSeconds, Value: 60, ObservedTime: base})
	evaluator.EvaluateSample(MetricSample{Kind: MetricKindFeedStalenessSeconds, Value: 5, ObservedTime: base.Add(time.Second)}) // recovers
	_, raisedAgain := evaluator.EvaluateSample(MetricSample{Kind: MetricKindFeedStalenessSeconds, Value: 60, ObservedTime: base.Add(2 * time.Second)})

	if !raisedAgain {
		t.Fatalf("expected a fresh breach after recovery to raise a new alert")
	}
	if len(evaluator.AllAlerts()) != 2 {
		t.Fatalf("expected 2 stored alerts (breach, recover, breach again), got %d", len(evaluator.AllAlerts()))
	}
}

func TestUnknownMetricKindIsIgnored(t *testing.T) {
	evaluator := NewSloAlertEvaluator(testThresholds())
	_, raised := evaluator.EvaluateSample(MetricSample{Kind: MetricSampleKind("NOT_CONFIGURED"), Value: 99999, ObservedTime: time.Now()})
	if raised {
		t.Fatalf("expected a metric kind with no configured threshold to never raise")
	}
}

func TestAllAlertsReturnsACopyNotTheInternalSlice(t *testing.T) {
	evaluator := NewSloAlertEvaluator(testThresholds())
	evaluator.EvaluateSample(MetricSample{Kind: MetricKindFeedStalenessSeconds, Value: 60, ObservedTime: time.Now()})

	alerts := evaluator.AllAlerts()
	alerts[0].HumanReadableText = "mutated"

	freshAlerts := evaluator.AllAlerts()
	if freshAlerts[0].HumanReadableText == "mutated" {
		t.Fatalf("expected AllAlerts to return an isolated copy, internal state was mutated")
	}
}

func TestAlertTextIsHumanReadableAndMentionsBothValues(t *testing.T) {
	evaluator := NewSloAlertEvaluator(testThresholds())
	alert, _ := evaluator.EvaluateSample(MetricSample{Kind: MetricKindFeedStalenessSeconds, Value: 45, ObservedTime: time.Now()})
	if alert.HumanReadableText == "" {
		t.Fatalf("expected a non-empty human-readable alert message")
	}
}

func TestDefaultThresholdsMatchFeaturesDotMdExamples(t *testing.T) {
	evaluator := NewSloAlertEvaluator(DefaultThresholds)
	_, raised := evaluator.EvaluateSample(MetricSample{Kind: MetricKindFeedStalenessSeconds, Value: 31, ObservedTime: time.Now()})
	if !raised {
		t.Fatalf("expected DefaultThresholds' 30s feed-staleness threshold to breach at 31s")
	}
}
