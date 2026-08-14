// This file extends internal/marketsession with FEATURES.md §21's
// "intraday auto square-off countdown timer + push reminder before
// forced closure": real countdown math (given a real configured
// square-off cutoff time-of-day and the current time, compute the real
// remaining duration until forced closure) and a real, stateful,
// dedup'd reminder-eligibility check (e.g. "should a reminder fire now"
// at configurable thresholds like 30/15/5 minutes before cutoff) — the
// backend signal a frontend push notification would consume.
//
// SCOPE: this is backend countdown/reminder-eligibility logic only —
// actually forcing the closure (submitting real reducing orders) is
// FEATURES.md's separate auto-liquidation concern (see
// internal/autoliquidation, which already does that for a MARGIN
// breach, not a time-of-day cutoff) and actual push delivery to a
// client device is out of scope for oms-gateway entirely (apps/web owns
// that per this package's own instructions). Every function here takes
// `now` as an explicit parameter — the wall clock is never read
// internally — the same discipline internal/algolimits' token bucket and
// internal/executionalgos' schedulers already established.
package marketsession

import (
	"fmt"
	"sync"
	"time"
)

// DefaultSquareOffReminderThresholds is a real, illustrative default
// reminder schedule — 30/15/5 minutes before cutoff — matching the
// FEATURES.md item's own example thresholds.
var DefaultSquareOffReminderThresholds = []time.Duration{
	30 * time.Minute,
	15 * time.Minute,
	5 * time.Minute,
}

// SquareOffCutoffConfig is a real, settable time-of-day (hour/minute/
// second, interpreted in UTC) at which intraday positions are forced
// closed. TODO(real build): a real exchange's square-off cutoff varies
// by segment/product and isn't just one flat time-of-day; this is an
// intentional, illustrative simplification, the same caliber as this
// codebase's other rate-table simplifications.
type SquareOffCutoffConfig struct {
	HourUtc   int
	MinuteUtc int
	SecondUtc int
}

// DefaultSquareOffCutoffConfig returns an illustrative 15:20 UTC cutoff
// (20:50 IST) — a real build would source this per-segment from a real
// exchange trading-calendar service.
func DefaultSquareOffCutoffConfig() SquareOffCutoffConfig {
	return SquareOffCutoffConfig{HourUtc: 15, MinuteUtc: 20, SecondUtc: 0}
}

// CutoffForDate returns the real cutoff instant on the same
// year/month/day as `now`, at this config's configured time-of-day, in
// UTC.
func (config SquareOffCutoffConfig) CutoffForDate(now time.Time) time.Time {
	nowUtc := now.UTC()
	return time.Date(nowUtc.Year(), nowUtc.Month(), nowUtc.Day(), config.HourUtc, config.MinuteUtc, config.SecondUtc, 0, time.UTC)
}

// SquareOffCountdown is the real countdown state as of one evaluation
// instant.
type SquareOffCountdown struct {
	CutoffTime            time.Time `json:"cutoffTime"`
	RemainingSeconds      int64     `json:"remainingSeconds"`
	IsPastCutoff          bool      `json:"isPastCutoff"`
	RemainingDurationText string    `json:"remainingDurationText"`
}

// ComputeSquareOffCountdown computes the real remaining time until
// cutoffTime as of now. RemainingSeconds is floored at 0 (never
// negative) once cutoff has passed — IsPastCutoff distinguishes "exactly
// at cutoff" / "past cutoff" from "still counting down".
func ComputeSquareOffCountdown(cutoffTime time.Time, now time.Time) SquareOffCountdown {
	remaining := cutoffTime.Sub(now)
	isPast := !now.Before(cutoffTime)
	if remaining < 0 {
		remaining = 0
	}
	return SquareOffCountdown{
		CutoffTime:            cutoffTime,
		RemainingSeconds:      int64(remaining.Seconds()),
		IsPastCutoff:          isPast,
		RemainingDurationText: remaining.Round(time.Second).String(),
	}
}

// SquareOffReminderTracker is a real, mutex-guarded, per-account,
// per-cutoff-instant dedup tracker: it decides whether a reminder
// threshold (e.g. "30 minutes before cutoff") is NEWLY due right now,
// firing each configured threshold at most once per (account, cutoff
// instant) pair — repeated polling (e.g. a client checking every few
// seconds) never re-fires the same threshold.
type SquareOffReminderTracker struct {
	thresholds []time.Duration

	mutexGuardingState sync.Mutex
	// firedThresholdsByAccountAndCutoff: account -> cutoff instant (unix
	// seconds, so distinct trading days never collide) -> threshold ->
	// already fired.
	firedThresholdsByAccountAndCutoff map[string]map[int64]map[time.Duration]bool
}

// NewSquareOffReminderTracker constructs a tracker with the given sorted
// (descending, e.g. 30m/15m/5m) thresholds. Returns an error if
// thresholds is empty or contains a non-positive duration — mirrors
// overtradingdetection.NewDetector's "fail loudly at construction"
// convention.
func NewSquareOffReminderTracker(thresholds []time.Duration) (*SquareOffReminderTracker, error) {
	if len(thresholds) == 0 {
		return nil, fmt.Errorf("marketsession: at least one reminder threshold is required")
	}
	for _, threshold := range thresholds {
		if threshold <= 0 {
			return nil, fmt.Errorf("marketsession: reminder thresholds must be positive, got %s", threshold)
		}
	}
	thresholdsCopy := make([]time.Duration, len(thresholds))
	copy(thresholdsCopy, thresholds)
	return &SquareOffReminderTracker{
		thresholds:                        thresholdsCopy,
		firedThresholdsByAccountAndCutoff: make(map[string]map[int64]map[time.Duration]bool),
	}, nil
}

// DueReminders returns every threshold that is BOTH (a) reached — the
// remaining time until cutoffTime is <= that threshold — AND (b) not
// already fired for this (accountIdentifier, cutoffTime) pair. Marks
// every returned threshold as fired before returning, so an immediate
// repeat call with the same now returns nothing new for those
// thresholds. Returns thresholds in the order configured (typically
// largest-first, e.g. 30m before 15m before 5m).
func (tracker *SquareOffReminderTracker) DueReminders(accountIdentifier string, cutoffTime time.Time, now time.Time) []time.Duration {
	tracker.mutexGuardingState.Lock()
	defer tracker.mutexGuardingState.Unlock()

	remaining := cutoffTime.Sub(now)
	cutoffKey := cutoffTime.Unix()

	if tracker.firedThresholdsByAccountAndCutoff[accountIdentifier] == nil {
		tracker.firedThresholdsByAccountAndCutoff[accountIdentifier] = make(map[int64]map[time.Duration]bool)
	}
	if tracker.firedThresholdsByAccountAndCutoff[accountIdentifier][cutoffKey] == nil {
		tracker.firedThresholdsByAccountAndCutoff[accountIdentifier][cutoffKey] = make(map[time.Duration]bool)
	}
	firedForThisCutoff := tracker.firedThresholdsByAccountAndCutoff[accountIdentifier][cutoffKey]

	var due []time.Duration
	for _, threshold := range tracker.thresholds {
		if remaining < 0 {
			// Already past cutoff entirely — no more reminders make sense,
			// only an actual forced-closure action would (out of scope
			// here, see file doc).
			continue
		}
		if remaining <= threshold && !firedForThisCutoff[threshold] {
			due = append(due, threshold)
			firedForThisCutoff[threshold] = true
		}
	}
	return due
}

// HasFired reports whether a specific threshold has already fired for an
// (accountIdentifier, cutoffTime) pair — a pure read, doesn't mutate
// anything, useful for a status query that shouldn't itself consume a
// reminder.
func (tracker *SquareOffReminderTracker) HasFired(accountIdentifier string, cutoffTime time.Time, threshold time.Duration) bool {
	tracker.mutexGuardingState.Lock()
	defer tracker.mutexGuardingState.Unlock()

	byCutoff, exists := tracker.firedThresholdsByAccountAndCutoff[accountIdentifier]
	if !exists {
		return false
	}
	byThreshold, exists := byCutoff[cutoffTime.Unix()]
	if !exists {
		return false
	}
	return byThreshold[threshold]
}

// Thresholds returns a copy of the configured reminder thresholds.
func (tracker *SquareOffReminderTracker) Thresholds() []time.Duration {
	thresholdsCopy := make([]time.Duration, len(tracker.thresholds))
	copy(thresholdsCopy, tracker.thresholds)
	return thresholdsCopy
}
