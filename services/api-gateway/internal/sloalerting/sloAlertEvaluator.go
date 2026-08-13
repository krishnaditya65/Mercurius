// Package sloalerting is FEATURES.md §13's "Alerting on SLO breach (feed
// staleness, order-reject spikes, matching latency)" — a real evaluator
// that polls real metric samples on a real interval, compares them
// against configurable thresholds, and raises a real structured alert
// (logged loudly, and stored queryable — see api-gateway's `GET
// /alerts` handler in cmd/server/main.go) when a threshold is breached.
//
// The evaluation logic (EvaluateSample / the rolling-window math) is
// fully decoupled from HOW samples are collected, so it can be — and IS,
// in sloAlertEvaluator_test.go — tested deterministically by injecting
// synthetic metric samples, with no live services required. A separate
// poller (see MetricsPoller in sloMetricsPoller.go) is what actually
// reaches out to real running services on a real interval; it's a thin,
// mostly-untested-by-design wrapper around real HTTP calls, kept
// separate specifically so the interesting logic stays unit-testable.
package sloalerting

import (
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"
)

// MetricSampleKind names one kind of SLO-relevant signal this evaluator
// understands.
type MetricSampleKind string

const (
	// MetricKindOrderRejectRate is a point-in-time ratio (0.0-1.0) of
	// rejected orders to total orders, e.g. sampled from oms-gateway's
	// audit trail over some recent window.
	MetricKindOrderRejectRate MetricSampleKind = "ORDER_REJECT_RATE"

	// MetricKindFeedStalenessSeconds is how many seconds old the most
	// recent market-data tick/trade is, e.g. sampled from market-data's
	// `GET /trades`.
	MetricKindFeedStalenessSeconds MetricSampleKind = "FEED_STALENESS_SECONDS"

	// MetricKindMatchingLatencyMs is an observed matching-engine
	// round-trip latency in milliseconds.
	MetricKindMatchingLatencyMs MetricSampleKind = "MATCHING_LATENCY_MS"
)

// MetricSample is one observation fed into the evaluator, real or
// synthetic.
type MetricSample struct {
	Kind         MetricSampleKind
	Value        float64
	ObservedTime time.Time
}

// Threshold configures when Kind should raise an alert: Value breaches
// the threshold if it EXCEEDS Threshold for at least MinimumBreachWindow
// (i.e. every sample kept inside the rolling window for that kind must
// exceed Threshold — see EvaluateSample's doc comment for the exact
// semantics).
type Threshold struct {
	Kind                MetricSampleKind
	ThresholdValue      float64
	MinimumBreachWindow time.Duration
}

// DefaultThresholds mirrors FEATURES.md's own example numbers: "e.g.
// order-reject-rate > 10% over 5 minutes, feed staleness > 30s". A
// matching-latency threshold is included too, per the FEATURES.md item's
// third named signal ("matching latency"), using a round number since
// FEATURES.md gives no specific latency figure.
var DefaultThresholds = []Threshold{
	{Kind: MetricKindOrderRejectRate, ThresholdValue: 0.10, MinimumBreachWindow: 5 * time.Minute},
	{Kind: MetricKindFeedStalenessSeconds, ThresholdValue: 30, MinimumBreachWindow: 0},
	{Kind: MetricKindMatchingLatencyMs, ThresholdValue: 250, MinimumBreachWindow: 1 * time.Minute},
}

// Severity of a raised alert.
type Severity string

const (
	SeverityWarning  Severity = "WARNING"
	SeverityCritical Severity = "CRITICAL"
)

// Alert is one raised, structured SLO breach record.
type Alert struct {
	AlertIdentifier   string           `json:"alertIdentifier"`
	Kind              MetricSampleKind `json:"kind"`
	Severity          Severity         `json:"severity"`
	BreachedValue     float64          `json:"breachedValue"`
	ThresholdValue    float64          `json:"thresholdValue"`
	RaisedAtTime      time.Time        `json:"raisedAtTime"`
	HumanReadableText string           `json:"humanReadableText"`
}

// breachTrackingState is the continuous-breach bookkeeping this
// evaluator keeps per MetricSampleKind — deliberately modeled on
// Prometheus alerting's own `for:` duration semantics (a condition must
// hold CONTINUOUSLY for at least the configured duration before it
// fires), rather than "every sample in a trailing window breaches",
// which trivially fires on the very first sample of any positive window
// and so doesn't actually enforce sustained breach.
type breachTrackingState struct {
	isCurrentlyBreaching            bool
	continuousBreachStartedAtTime   time.Time
	alertAlreadyRaisedForThisBreach bool
}

// SloAlertEvaluator holds continuous-breach tracking state per
// MetricSampleKind, evaluates each new sample against its configured
// Threshold, and raises + stores Alerts. Safe for concurrent use.
type SloAlertEvaluator struct {
	mutexGuardingState sync.Mutex
	thresholdsByKind   map[MetricSampleKind]Threshold
	breachStateByKind  map[MetricSampleKind]*breachTrackingState
	raisedAlerts       []Alert
	alertIdSequence    uint64
	nowFunc            func() time.Time
}

// NewSloAlertEvaluator returns an evaluator configured with thresholds.
func NewSloAlertEvaluator(thresholds []Threshold) *SloAlertEvaluator {
	thresholdsByKind := make(map[MetricSampleKind]Threshold, len(thresholds))
	breachStateByKind := make(map[MetricSampleKind]*breachTrackingState, len(thresholds))
	for _, threshold := range thresholds {
		thresholdsByKind[threshold.Kind] = threshold
		breachStateByKind[threshold.Kind] = &breachTrackingState{}
	}
	return &SloAlertEvaluator{
		thresholdsByKind:  thresholdsByKind,
		breachStateByKind: breachStateByKind,
		nowFunc:           time.Now,
	}
}

// WithClock overrides the evaluator's time source — test-only hook.
func (evaluator *SloAlertEvaluator) WithClock(nowFunc func() time.Time) *SloAlertEvaluator {
	evaluator.mutexGuardingState.Lock()
	defer evaluator.mutexGuardingState.Unlock()
	evaluator.nowFunc = nowFunc
	return evaluator
}

// EvaluateSample records sample and, if its kind has a configured
// threshold, evaluates the rolling window for that kind.
//
// Semantics (Prometheus `for:`-duration style): sample.Value breaches
// the threshold as soon as it exceeds ThresholdValue. The FIRST
// breaching sample after a healthy period starts a continuous-breach
// timer for that MetricSampleKind. An alert fires once the breach has
// held CONTINUOUSLY (every subsequent sample for that kind also
// breaching, with no healthy sample resetting the timer) for at least
// MinimumBreachWindow — matching FEATURES.md's own phrasing "over 5
// minutes" — and fires only ONCE per continuous breach episode, not on
// every sample after the threshold duration has elapsed. A
// MinimumBreachWindow of zero means the very first breaching sample
// fires immediately (e.g. feed staleness — one stale read matters right
// away). Any healthy sample resets the timer and re-arms the alert for
// the next breach episode.
//
// Returns the newly-raised Alert and true if this sample caused a FRESH
// alert — callers (the live poller) use this to know when to actually
// log/notify vs. when the breach is already known and already alerted.
func (evaluator *SloAlertEvaluator) EvaluateSample(sample MetricSample) (Alert, bool) {
	evaluator.mutexGuardingState.Lock()
	defer evaluator.mutexGuardingState.Unlock()

	threshold, hasThreshold := evaluator.thresholdsByKind[sample.Kind]
	if !hasThreshold {
		return Alert{}, false
	}

	state := evaluator.breachStateByKind[sample.Kind]

	if sample.Value <= threshold.ThresholdValue {
		state.isCurrentlyBreaching = false
		state.alertAlreadyRaisedForThisBreach = false
		return Alert{}, false
	}

	if !state.isCurrentlyBreaching {
		state.isCurrentlyBreaching = true
		state.continuousBreachStartedAtTime = sample.ObservedTime
		state.alertAlreadyRaisedForThisBreach = false
	}

	continuousBreachDuration := sample.ObservedTime.Sub(state.continuousBreachStartedAtTime)
	if continuousBreachDuration < threshold.MinimumBreachWindow || state.alertAlreadyRaisedForThisBreach {
		return Alert{}, false
	}
	state.alertAlreadyRaisedForThisBreach = true

	evaluator.alertIdSequence++
	alert := Alert{
		AlertIdentifier:   formatAlertIdentifier(evaluator.alertIdSequence),
		Kind:              sample.Kind,
		Severity:          severityForKind(sample.Kind),
		BreachedValue:     sample.Value,
		ThresholdValue:    threshold.ThresholdValue,
		RaisedAtTime:      evaluator.nowFunc(),
		HumanReadableText: formatAlertText(sample.Kind, sample.Value, threshold.ThresholdValue),
	}
	evaluator.raisedAlerts = append(evaluator.raisedAlerts, alert)

	log.Printf("🚨 SLO BREACH ALERT [%s] %s", alert.Severity, alert.HumanReadableText)

	return alert, true
}

// AllAlerts returns every alert raised so far, oldest first — backs
// api-gateway's `GET /alerts` endpoint.
func (evaluator *SloAlertEvaluator) AllAlerts() []Alert {
	evaluator.mutexGuardingState.Lock()
	defer evaluator.mutexGuardingState.Unlock()

	alertsCopy := make([]Alert, len(evaluator.raisedAlerts))
	copy(alertsCopy, evaluator.raisedAlerts)
	return alertsCopy
}

func severityForKind(kind MetricSampleKind) Severity {
	if kind == MetricKindFeedStalenessSeconds {
		return SeverityCritical
	}
	return SeverityWarning
}

func formatAlertIdentifier(sequence uint64) string {
	return "alert-" + strconv.FormatUint(sequence, 10)
}

func formatAlertText(kind MetricSampleKind, value float64, threshold float64) string {
	switch kind {
	case MetricKindOrderRejectRate:
		return fmt.Sprintf("order-reject rate sustained above threshold: observed %.1f%% vs threshold %.1f%%", value*100, threshold*100)
	case MetricKindFeedStalenessSeconds:
		return fmt.Sprintf("market-data feed is stale: observed %.1fs since last update vs threshold %.1fs", value, threshold)
	case MetricKindMatchingLatencyMs:
		return fmt.Sprintf("matching-engine latency sustained above threshold: observed %.1fms vs threshold %.1fms", value, threshold)
	default:
		return fmt.Sprintf("SLO threshold breached for %s", kind)
	}
}
