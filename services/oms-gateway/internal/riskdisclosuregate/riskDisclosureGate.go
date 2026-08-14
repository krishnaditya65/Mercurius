// Package riskdisclosuregate implements FEATURES.md §19's "mandatory
// risk disclosure + cooling-off flow before first F&O order": a real,
// mutex-guarded per-account acknowledgement state, plus a real gate that
// REJECTS an account's FIRST-EVER F&O order until it has both (1)
// acknowledged the F&O risk disclosure via Acknowledge, AND (2) waited
// at least a configurable cooling-off duration since that
// acknowledgement.
//
// SCOPE: this gate only applies to an account's F&O orders — see
// internal/exposurelimits.ClassifySegment (reused here, not
// reimplemented) for the same illustrative, symbol-suffix-derived
// FUTURES_AND_OPTIONS classification already used elsewhere in this
// codebase. Equity orders are never gated by this package. "First-ever"
// is tracked per account: once an account's first F&O order clears this
// gate, every SUBSEQUENT F&O order from that account is unaffected by
// it — this is a one-time onboarding friction, not a check on every
// order.
package riskdisclosuregate

import (
	"errors"
	"sync"
	"time"
)

var (
	// ErrNotYetAcknowledged is returned when an account has never called
	// Acknowledge at all.
	ErrNotYetAcknowledged = errors.New("F&O risk disclosure has not been acknowledged for this account yet — call POST /risk-disclosure/acknowledge first")

	// ErrCoolingOffPeriodNotElapsed is returned when an account HAS
	// acknowledged, but not long enough ago — the configured cooling-off
	// duration hasn't elapsed since AcknowledgedAtTime.
	ErrCoolingOffPeriodNotElapsed = errors.New("F&O risk disclosure was acknowledged too recently — the cooling-off period has not yet elapsed")

	// ErrCoolingOffDurationMustBePositive is returned by NewGate when
	// constructed with a non-positive cooling-off duration.
	ErrCoolingOffDurationMustBePositive = errors.New("riskdisclosuregate: cooling-off duration must be positive")
)

// AcknowledgementRecord is one account's real disclosure-acknowledgement
// state.
type AcknowledgementRecord struct {
	HasAcknowledged    bool      `json:"hasAcknowledged"`
	AcknowledgedAtTime time.Time `json:"acknowledgedAtTime,omitempty"`

	// HasPlacedFirstFnoOrder marks that this account's first F&O order
	// already cleared the gate — once true, CheckFirstFnoOrderGate is a
	// permanent no-op for this account (this is a one-time onboarding
	// friction, not a check re-run on every F&O order).
	HasPlacedFirstFnoOrder bool `json:"hasPlacedFirstFnoOrder"`
}

// Gate is a real, mutex-guarded per-account acknowledgement state
// machine. Every method that needs the current time takes it as an
// explicit `now` parameter — never the wall clock internally — so the
// cooling-off boundary is exactly reproducible in tests, the same
// discipline internal/algolimits' token bucket already established.
type Gate struct {
	coolingOffDuration time.Duration

	mutexGuardingState sync.Mutex
	recordsByAccount   map[string]*AcknowledgementRecord
}

// NewGate constructs a Gate with the given cooling-off duration (e.g.
// 24 hours — must elapse between Acknowledge and an account's first F&O
// order actually being allowed through).
func NewGate(coolingOffDuration time.Duration) (*Gate, error) {
	if coolingOffDuration <= 0 {
		return nil, ErrCoolingOffDurationMustBePositive
	}
	return &Gate{
		coolingOffDuration: coolingOffDuration,
		recordsByAccount:   make(map[string]*AcknowledgementRecord),
	}, nil
}

// Acknowledge records that an account has acknowledged the F&O risk
// disclosure at `now`. Calling it again (e.g. a re-acknowledgement)
// resets AcknowledgedAtTime to the latest call — a deliberate choice: a
// real compliance flow would likely want the MOST RECENT acknowledgement
// to govern, not the first one ever, in case disclosure text changes and
// needs re-acknowledgement.
func (gate *Gate) Acknowledge(accountIdentifier string, now time.Time) {
	gate.mutexGuardingState.Lock()
	defer gate.mutexGuardingState.Unlock()

	record, exists := gate.recordsByAccount[accountIdentifier]
	if !exists {
		record = &AcknowledgementRecord{}
		gate.recordsByAccount[accountIdentifier] = record
	}
	record.HasAcknowledged = true
	record.AcknowledgedAtTime = now
}

// Status returns a copy of an account's current acknowledgement record
// (zero-value if the account has never acknowledged at all).
func (gate *Gate) Status(accountIdentifier string) AcknowledgementRecord {
	gate.mutexGuardingState.Lock()
	defer gate.mutexGuardingState.Unlock()

	record, exists := gate.recordsByAccount[accountIdentifier]
	if !exists {
		return AcknowledgementRecord{}
	}
	return *record
}

// CheckFirstFnoOrderGate is the real, load-bearing gate:
//   - if the account already placed its first F&O order, this is always
//     a no-op success (nil) — the gate never re-checks a returning F&O
//     trader.
//   - if the instrument is not F&O at all (per
//     exposurelimits.ClassifySegment), this is always a no-op success —
//     this gate only ever governs F&O orders.
//   - otherwise: the account must have acknowledged (ErrNotYetAcknowledged
//     if not) AND at least coolingOffDuration must have elapsed since
//     that acknowledgement (ErrCoolingOffPeriodNotElapsed if not).
//
// On success for what genuinely is this account's first F&O order,
// RecordFirstFnoOrderPlaced must be called by the caller once the order
// is actually accepted — CheckFirstFnoOrderGate itself is a pure read,
// it never mutates HasPlacedFirstFnoOrder (so a check against an order
// that later gets rejected by a LATER gate, e.g. insufficient margin,
// doesn't wrongly consume the "first order" milestone).
func (gate *Gate) CheckFirstFnoOrderGate(accountIdentifier string, isFnoInstrument bool, now time.Time) error {
	if !isFnoInstrument {
		return nil
	}

	gate.mutexGuardingState.Lock()
	defer gate.mutexGuardingState.Unlock()

	record, exists := gate.recordsByAccount[accountIdentifier]
	if exists && record.HasPlacedFirstFnoOrder {
		return nil
	}

	if !exists || !record.HasAcknowledged {
		return ErrNotYetAcknowledged
	}

	if now.Sub(record.AcknowledgedAtTime) < gate.coolingOffDuration {
		return ErrCoolingOffPeriodNotElapsed
	}

	return nil
}

// RecordFirstFnoOrderPlaced marks that an account's first F&O order has
// now genuinely been accepted, permanently exempting it from
// CheckFirstFnoOrderGate going forward. Idempotent — calling it more
// than once (or on an account that never acknowledged, which shouldn't
// happen if the caller only calls this after a successful
// CheckFirstFnoOrderGate) is harmless.
func (gate *Gate) RecordFirstFnoOrderPlaced(accountIdentifier string, now time.Time) {
	gate.mutexGuardingState.Lock()
	defer gate.mutexGuardingState.Unlock()

	record, exists := gate.recordsByAccount[accountIdentifier]
	if !exists {
		record = &AcknowledgementRecord{}
		gate.recordsByAccount[accountIdentifier] = record
	}
	record.HasPlacedFirstFnoOrder = true
}
