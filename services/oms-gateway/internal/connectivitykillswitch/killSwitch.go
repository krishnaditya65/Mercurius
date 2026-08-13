// Package connectivitykillswitch implements FEATURES.md §12's "Circuit
// breaker / kill-switch at the exchange-connectivity layer": a real,
// admin-triggerable kill switch that, when engaged, makes oms-gateway
// immediately reject ALL new order submissions with a clear "trading
// halted" error, plus a real health-check-driven auto-trigger that
// engages the SAME switch if matching-engine's connectivity fails N
// times in a row.
//
// DESIGN CHOICE — two independent engagement reasons, one combined
// effect: KillSwitch tracks a MANUAL flag (an admin explicitly engaged
// it via POST /connectivity-kill-switch/engage) and an AUTO flag
// (RecordConnectivityCheckResult saw failureThreshold consecutive
// connectivity failures) completely independently. IsTradingHalted() is
// true if EITHER is set. This matters for the disengage story: an admin
// calling DisengageManually only clears the MANUAL flag — if matching-
// engine is still genuinely failing, the AUTO flag keeps trading halted
// regardless, so an admin can't accidentally re-open trading into a
// still-broken downstream by disengaging the wrong reason. The AUTO flag
// clears itself the moment a connectivity check succeeds again (a real,
// automatic "matching-engine recovered" signal), while the MANUAL flag
// never clears itself — an admin must explicitly disengage it.
//
// DESIGN CHOICE — what counts as a "connectivity check": this package
// does not itself ping anything. cmd/server/main.go calls
// RecordConnectivityCheckResult(succeeded) from TWO real sources: (1)
// every REAL matching-engine hand-off attempt already happening on the
// live order-submission path
// (internal/matchingengineclient.SubmitOrderAndAwaitMatchResult), and
// (2) a genuinely independent background goroutine that polls
// matching-engine every 2 seconds via its real
// QueryOrderStatusAndAwaitResult method, regardless of order flow. (2)
// exists specifically so a real recovery can be detected and the AUTO
// flag can self-clear even while trading is halted — without it, once
// halted, every new order is rejected BEFORE ever reaching
// matchingEngineClient, so (1) alone could never observe a genuine
// recovery again. See cmd/server/main.go's processOrderSubmission and
// its background probe goroutine in main() for both call sites.
//
// DESIGN CHOICE — cancellation is explicitly NOT gated: FEATURES.md §12
// says "existing pending orders can still be cancelled" — this package
// only ever needs to be consulted from the order-SUBMISSION path
// (buildSubmitOrderHandler / the DMA gateway's NewOrderSingle handler via
// processOrderSubmission); buildCancelOrderHandler in cmd/server/main.go
// deliberately never calls IsTradingHalted at all.
package connectivitykillswitch

import (
	"sync"
	"time"
)

// EngagementReason identifies why trading is currently halted.
type EngagementReason string

const (
	EngagementReasonManualAdmin             EngagementReason = "MANUAL_ADMIN"
	EngagementReasonAutoConnectivityFailure EngagementReason = "AUTO_CONNECTIVITY_FAILURE"
)

// Status is a snapshot of the kill switch's current state.
type Status struct {
	IsTradingHalted         bool       `json:"isTradingHalted"`
	IsManuallyEngaged       bool       `json:"isManuallyEngaged"`
	ManualEngagementReason  string     `json:"manualEngagementReason,omitempty"`
	ManualEngagedAtTime     *time.Time `json:"manualEngagedAtTime,omitempty"`
	IsAutoEngaged           bool       `json:"isAutoEngaged"`
	ConsecutiveFailureCount int        `json:"consecutiveFailureCount"`
	FailureThreshold        int        `json:"failureThreshold"`
}

// KillSwitch is the mutex-guarded state machine. Safe for concurrent use
// — every order submission on every goroutine consults IsTradingHalted,
// and matching-engine hand-off results from concurrent requests all call
// RecordConnectivityCheckResult.
type KillSwitch struct {
	mutexGuardingState sync.RWMutex

	isManuallyEngaged      bool
	manualEngagementReason string
	manualEngagedAtTime    *time.Time

	isAutoEngaged           bool
	consecutiveFailureCount int
	failureThreshold        int
}

// NewKillSwitch constructs a KillSwitch that auto-engages once
// failureThreshold consecutive connectivity failures are recorded. A
// non-positive threshold disables the auto-trigger entirely (manual
// engagement still works) — useful for tests and for an operator who
// wants purely manual control.
func NewKillSwitch(failureThreshold int) *KillSwitch {
	return &KillSwitch{
		failureThreshold: failureThreshold,
	}
}

// EngageManually halts all new order submissions immediately, recording
// reason and the current time. Safe to call when already engaged
// (idempotent — overwrites the reason/time with the latest call).
func (killSwitch *KillSwitch) EngageManually(reason string) {
	killSwitch.mutexGuardingState.Lock()
	defer killSwitch.mutexGuardingState.Unlock()

	now := time.Now()
	killSwitch.isManuallyEngaged = true
	killSwitch.manualEngagementReason = reason
	killSwitch.manualEngagedAtTime = &now
}

// DisengageManually clears ONLY the manual engagement flag — see the
// package doc's design-choice note on why this alone does not
// necessarily resume trading if the AUTO flag is still set.
func (killSwitch *KillSwitch) DisengageManually() {
	killSwitch.mutexGuardingState.Lock()
	defer killSwitch.mutexGuardingState.Unlock()

	killSwitch.isManuallyEngaged = false
	killSwitch.manualEngagementReason = ""
	killSwitch.manualEngagedAtTime = nil
}

// RecordConnectivityCheckResult folds one real matching-engine hand-off
// attempt's outcome into the consecutive-failure counter. A success
// resets the counter to zero and clears the AUTO engagement flag (a real
// recovery signal); a failure increments the counter and, once it
// reaches failureThreshold, sets the AUTO engagement flag. Returns
// whether this call caused the AUTO flag to newly become engaged (useful
// for logging the exact moment the auto-trigger fired).
func (killSwitch *KillSwitch) RecordConnectivityCheckResult(succeeded bool) (autoEngagedByThisCall bool) {
	killSwitch.mutexGuardingState.Lock()
	defer killSwitch.mutexGuardingState.Unlock()

	if succeeded {
		killSwitch.consecutiveFailureCount = 0
		killSwitch.isAutoEngaged = false
		return false
	}

	killSwitch.consecutiveFailureCount++
	if killSwitch.failureThreshold > 0 && killSwitch.consecutiveFailureCount >= killSwitch.failureThreshold {
		wasAlreadyAutoEngaged := killSwitch.isAutoEngaged
		killSwitch.isAutoEngaged = true
		return !wasAlreadyAutoEngaged
	}
	return false
}

// IsTradingHalted is the real pre-trade gate: true if EITHER the manual
// or auto engagement flag is set.
func (killSwitch *KillSwitch) IsTradingHalted() bool {
	killSwitch.mutexGuardingState.RLock()
	defer killSwitch.mutexGuardingState.RUnlock()

	return killSwitch.isManuallyEngaged || killSwitch.isAutoEngaged
}

// CurrentStatus returns a full snapshot for a status endpoint / admin UI.
func (killSwitch *KillSwitch) CurrentStatus() Status {
	killSwitch.mutexGuardingState.RLock()
	defer killSwitch.mutexGuardingState.RUnlock()

	return Status{
		IsTradingHalted:         killSwitch.isManuallyEngaged || killSwitch.isAutoEngaged,
		IsManuallyEngaged:       killSwitch.isManuallyEngaged,
		ManualEngagementReason:  killSwitch.manualEngagementReason,
		ManualEngagedAtTime:     killSwitch.manualEngagedAtTime,
		IsAutoEngaged:           killSwitch.isAutoEngaged,
		ConsecutiveFailureCount: killSwitch.consecutiveFailureCount,
		FailureThreshold:        killSwitch.failureThreshold,
	}
}
