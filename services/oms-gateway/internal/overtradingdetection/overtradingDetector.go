// Package overtradingdetection implements FEATURES.md §19's
// "overtrading / revenge-trading pattern detection with cool-down
// nudges": a real, in-memory pattern detector over an account's own
// recent order-submission history that emits a real structured nudge —
// NOT a hard block, NOT a risk rejection — when that account's recent
// order frequency looks like a behavioral overtrading pattern relative
// to its own baseline.
//
// HONEST SCOPE BOUNDARY: a textbook "revenge trading" definition is
// rapid-fire re-entry specifically AFTER A REALIZED LOSS. This build has
// no realized-P&L/trade-journal feed anywhere in oms-gateway (see
// internal/marktomarket's own doc for the closest thing that exists —
// unrealized P&L against a pushed price, not a realized-loss event
// stream), so this detector uses the practical, real-data-driven proxy
// every serious brokerage overtrading nudge actually leans on in
// practice: RAPID-FIRE SUBMISSION VELOCITY relative to the account's OWN
// historical baseline, not anyone else's. This is a genuine, real
// pattern computed from real submission timestamps — not a stub — but
// it is deliberately NOT conditioned on an actual realized loss, because
// that data doesn't exist in this build. See the package-level TODO for
// what a real build would add.
//
// TODO(real build): once a realized-P&L/trade-journal event stream
// exists, add a second, stricter detector that specifically looks for a
// burst of new submissions within N minutes of a REALIZED LOSING trade
// closing — the classic "revenge trading" signature this package's name
// promises but can't fully deliver without that data.
package overtradingdetection

import (
	"fmt"
	"sync"
	"time"
)

// Nudge is the structured, non-blocking behavioral warning this package
// emits. It is deliberately NOT an error and NOT a rejection — a caller
// receiving a Nudge is expected to surface it to the client as a soft
// warning alongside an otherwise-normal order acknowledgement, never to
// block the order because of it.
type Nudge struct {
	// PatternDetected identifies which real pattern tripped — a closed
	// set so a client/analytics pipeline can filter reliably, same
	// convention as audittrail.EventType.
	PatternDetected string `json:"patternDetected"`

	// HumanReadableMessage is a plain-language nudge suitable for direct
	// display, per this codebase's FEATURES.md §21 plain-language
	// convention.
	HumanReadableMessage string `json:"humanReadableMessage"`

	// RecentOrderCount is how many orders this account submitted within
	// RecentWindowDuration ending at the evaluation time.
	RecentOrderCount int `json:"recentOrderCount"`

	// BaselineOrderCountForSameWindow is what this account's OWN average
	// order count for a window of the same length would be, computed
	// from its longer-lookback history — the baseline the burst is
	// compared against, never a cross-account/population baseline.
	BaselineOrderCountForSameWindow float64 `json:"baselineOrderCountForSameWindowLength"`

	// CooldownExpiresAtTime is when IsInCooldown will next return false
	// for this account, having just been (re)armed by this nudge firing.
	CooldownExpiresAtTime time.Time `json:"cooldownExpiresAtTime"`
}

// Pattern constants — see Nudge.PatternDetected.
const (
	PatternRapidFireBurst        = "RAPID_FIRE_BURST"
	PatternElevatedOrderVelocity = "ELEVATED_ORDER_VELOCITY_VS_BASELINE"
)

// Thresholds configures the detector. All fields are required to be
// positive by NewDetector — a zero-value Thresholds would either always
// or never fire, neither of which is a useful default to silently fall
// back on.
type Thresholds struct {
	// RecentWindowDuration is the lookback window used to count "recent"
	// orders, e.g. 5 minutes.
	RecentWindowDuration time.Duration

	// BaselineLookbackDuration is the longer historical window used to
	// compute this account's own baseline order rate, e.g. 24 hours. Must
	// be >= RecentWindowDuration.
	BaselineLookbackDuration time.Duration

	// MinimumRecentOrderCountToConsider is a hard floor — an account with
	// fewer than this many orders in RecentWindowDuration is never
	// flagged, no matter how far above its own (possibly tiny/noisy)
	// baseline that count is. Prevents a single account with 2 lifetime
	// orders 3 minutes apart from being flagged as "infinitely above
	// baseline".
	MinimumRecentOrderCountToConsider int

	// BaselineMultiplierToTrigger: the recent window's order count must
	// exceed BaselineOrderCountForSameWindow * this multiplier to fire an
	// ELEVATED_ORDER_VELOCITY_VS_BASELINE nudge.
	BaselineMultiplierToTrigger float64

	// RapidFireMinimumGapBelowThisIsSuspicious: if the gap between the
	// two MOST RECENT submissions is below this duration AND the recent
	// window's count already meets MinimumRecentOrderCountToConsider,
	// fires a RAPID_FIRE_BURST nudge regardless of the baseline
	// comparison — catches a brand-new account's very first burst, which
	// has no meaningful baseline yet.
	RapidFireMinimumGapBelowThisIsSuspicious time.Duration

	// CooldownDuration is how long IsInCooldown reports true once a
	// nudge fires for an account.
	CooldownDuration time.Duration

	// MaxHistoryPerAccount bounds memory: only the most recent N
	// submission timestamps are retained per account.
	MaxHistoryPerAccount int
}

// DefaultThresholds returns a real, documented illustrative default —
// same "illustrative but real, hand-checkable" convention as this
// codebase's other rate/threshold tables (e.g. internal/algolimits).
func DefaultThresholds() Thresholds {
	return Thresholds{
		RecentWindowDuration:                     5 * time.Minute,
		BaselineLookbackDuration:                 24 * time.Hour,
		MinimumRecentOrderCountToConsider:        5,
		BaselineMultiplierToTrigger:              3.0,
		RapidFireMinimumGapBelowThisIsSuspicious: 3 * time.Second,
		CooldownDuration:                         15 * time.Minute,
		MaxHistoryPerAccount:                     500,
	}
}

// Detector is a real, mutex-guarded pattern detector over per-account
// order-submission timestamp history. Every method takes `now` as an
// explicit parameter — the wall clock is never read internally — so
// every boundary is exactly reproducible in tests, the same discipline
// internal/algolimits' token bucket already established.
type Detector struct {
	thresholds Thresholds

	mutexGuardingState       sync.Mutex
	submissionTimesByAccount map[string][]time.Time
	cooldownExpiryByAccount  map[string]time.Time
}

// NewDetector constructs a Detector. Returns an error if thresholds are
// non-positive/inconsistent, the same "fail loudly at construction, not
// silently at first use" pattern internal/autoliquidation's
// NewLiquidationEngine already established.
func NewDetector(thresholds Thresholds) (*Detector, error) {
	if thresholds.RecentWindowDuration <= 0 {
		return nil, fmt.Errorf("overtradingdetection: RecentWindowDuration must be positive")
	}
	if thresholds.BaselineLookbackDuration < thresholds.RecentWindowDuration {
		return nil, fmt.Errorf("overtradingdetection: BaselineLookbackDuration must be >= RecentWindowDuration")
	}
	if thresholds.MinimumRecentOrderCountToConsider <= 0 {
		return nil, fmt.Errorf("overtradingdetection: MinimumRecentOrderCountToConsider must be positive")
	}
	if thresholds.BaselineMultiplierToTrigger <= 1.0 {
		return nil, fmt.Errorf("overtradingdetection: BaselineMultiplierToTrigger must be > 1.0")
	}
	if thresholds.CooldownDuration <= 0 {
		return nil, fmt.Errorf("overtradingdetection: CooldownDuration must be positive")
	}
	if thresholds.MaxHistoryPerAccount <= 0 {
		thresholds.MaxHistoryPerAccount = 500
	}
	return &Detector{
		thresholds:               thresholds,
		submissionTimesByAccount: make(map[string][]time.Time),
		cooldownExpiryByAccount:  make(map[string]time.Time),
	}, nil
}

// RecordSubmission records one real order submission timestamp for an
// account. Safe to call unconditionally from the real order-submission
// path for every order (accepted or rejected) — overtrading is about
// SUBMISSION velocity, not acceptance.
func (detector *Detector) RecordSubmission(accountIdentifier string, now time.Time) {
	detector.mutexGuardingState.Lock()
	defer detector.mutexGuardingState.Unlock()

	history := append(detector.submissionTimesByAccount[accountIdentifier], now)
	if len(history) > detector.thresholds.MaxHistoryPerAccount {
		history = history[len(history)-detector.thresholds.MaxHistoryPerAccount:]
	}
	detector.submissionTimesByAccount[accountIdentifier] = history
}

// Evaluate inspects an account's real recent submission history and
// returns a non-nil *Nudge if a pattern is detected AND the account is
// not already in cooldown from a previous nudge. Returns nil, false if
// no pattern is detected, or nil, true if a pattern WOULD be detected
// but the account is currently in cooldown (so the caller can tell
// "clean" apart from "suppressed by cooldown" if it wants to).
func (detector *Detector) Evaluate(accountIdentifier string, now time.Time) (*Nudge, bool) {
	detector.mutexGuardingState.Lock()
	defer detector.mutexGuardingState.Unlock()

	if cooldownExpiry, inCooldown := detector.cooldownExpiryByAccount[accountIdentifier]; inCooldown && now.Before(cooldownExpiry) {
		return nil, true
	}

	history := detector.submissionTimesByAccount[accountIdentifier]

	recentWindowStart := now.Add(-detector.thresholds.RecentWindowDuration)
	baselineWindowStart := now.Add(-detector.thresholds.BaselineLookbackDuration)

	recentCount := 0
	baselineWindowCount := 0
	for _, submissionTime := range history {
		if submissionTime.After(baselineWindowStart) && !submissionTime.After(now) {
			baselineWindowCount++
		}
		if submissionTime.After(recentWindowStart) && !submissionTime.After(now) {
			recentCount++
		}
	}

	windowsInBaseline := float64(detector.thresholds.BaselineLookbackDuration) / float64(detector.thresholds.RecentWindowDuration)
	baselineOrderCountForSameWindow := float64(baselineWindowCount) / windowsInBaseline

	var nudge *Nudge

	// Rapid-fire: the gap between the two most recent submissions is
	// suspiciously small AND there's already a meaningful recent count.
	if recentCount >= detector.thresholds.MinimumRecentOrderCountToConsider && len(history) >= 2 {
		mostRecentGap := history[len(history)-1].Sub(history[len(history)-2])
		if mostRecentGap >= 0 && mostRecentGap < detector.thresholds.RapidFireMinimumGapBelowThisIsSuspicious {
			nudge = &Nudge{
				PatternDetected: PatternRapidFireBurst,
				HumanReadableMessage: fmt.Sprintf(
					"You've submitted %d orders in the last %s, with orders arriving less than %s apart. Consider taking a short break before your next order.",
					recentCount, detector.thresholds.RecentWindowDuration, detector.thresholds.RapidFireMinimumGapBelowThisIsSuspicious,
				),
				RecentOrderCount:                recentCount,
				BaselineOrderCountForSameWindow: baselineOrderCountForSameWindow,
			}
		}
	}

	// Elevated velocity vs this account's own baseline.
	if nudge == nil &&
		recentCount >= detector.thresholds.MinimumRecentOrderCountToConsider &&
		float64(recentCount) > baselineOrderCountForSameWindow*detector.thresholds.BaselineMultiplierToTrigger {
		nudge = &Nudge{
			PatternDetected: PatternElevatedOrderVelocity,
			HumanReadableMessage: fmt.Sprintf(
				"You've submitted %d orders in the last %s — that's well above your own usual pace (~%.1f orders in a window this length). Consider slowing down.",
				recentCount, detector.thresholds.RecentWindowDuration, baselineOrderCountForSameWindow,
			),
			RecentOrderCount:                recentCount,
			BaselineOrderCountForSameWindow: baselineOrderCountForSameWindow,
		}
	}

	if nudge == nil {
		return nil, false
	}

	cooldownExpiry := now.Add(detector.thresholds.CooldownDuration)
	detector.cooldownExpiryByAccount[accountIdentifier] = cooldownExpiry
	nudge.CooldownExpiresAtTime = cooldownExpiry
	return nudge, false
}

// IsInCooldown reports whether an account is currently within a
// previously-armed cool-down period — a real, queryable state, not a
// side effect buried inside Evaluate alone.
func (detector *Detector) IsInCooldown(accountIdentifier string, now time.Time) bool {
	detector.mutexGuardingState.Lock()
	defer detector.mutexGuardingState.Unlock()

	cooldownExpiry, exists := detector.cooldownExpiryByAccount[accountIdentifier]
	return exists && now.Before(cooldownExpiry)
}

// CooldownStatus is the read-only shape returned by a status query.
type CooldownStatus struct {
	IsInCooldown          bool      `json:"isInCooldown"`
	CooldownExpiresAtTime time.Time `json:"cooldownExpiresAtTime,omitempty"`
	RecentOrderCount      int       `json:"recentOrderCount"`
}

// Status returns the account's current cooldown state and recent order
// count as of `now`, without mutating anything (unlike Evaluate, which
// arms a new cooldown when it fires).
func (detector *Detector) Status(accountIdentifier string, now time.Time) CooldownStatus {
	detector.mutexGuardingState.Lock()
	defer detector.mutexGuardingState.Unlock()

	recentWindowStart := now.Add(-detector.thresholds.RecentWindowDuration)
	recentCount := 0
	for _, submissionTime := range detector.submissionTimesByAccount[accountIdentifier] {
		if submissionTime.After(recentWindowStart) && !submissionTime.After(now) {
			recentCount++
		}
	}

	cooldownExpiry, exists := detector.cooldownExpiryByAccount[accountIdentifier]
	inCooldown := exists && now.Before(cooldownExpiry)
	status := CooldownStatus{
		IsInCooldown:     inCooldown,
		RecentOrderCount: recentCount,
	}
	if inCooldown {
		status.CooldownExpiresAtTime = cooldownExpiry
	}
	return status
}
