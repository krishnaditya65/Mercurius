// Package tradesurveillance is FEATURES.md §1's "Surveillance system:
// spoofing/layering/wash-trade detection for compliance officers, replay
// tooling tied to Tick-to-Trade Analytics".
//
// HONEST SCOPE STATEMENT, read this before trusting any flag this package
// produces:
//
//   - These are HEURISTIC PROXIES for the real regulatory definitions of
//     spoofing, layering, and wash trading (as used e.g. under Dodd-Frank/
//     CFTC and SEC Rule 10b-5 enforcement, and equivalent Indian SEBI
//     PFUTP regulations). They are NOT a certified market-surveillance
//     product, have not been validated against real market abuse cases,
//     and must never be the sole basis for a regulatory filing or an
//     account action without a human compliance officer's independent
//     review of the underlying evidence this package returns.
//   - The wash-trade check (WashTradeDetector, see washTradeDetector.go)
//     is the one exception: it is an EXACT, not heuristic, check — the
//     same account is definitionally on both sides of one recorded trade.
//     There is no "linked accounts" concept anywhere else in this
//     codebase (no beneficial-ownership graph, no KYC household linkage),
//     so this is scoped to same-account wash trades only. Detecting wash
//     trades across two DIFFERENT accounts that are secretly controlled
//     by the same beneficial owner is a real, harder problem this build
//     does not attempt — it would need a linked-accounts data model this
//     repo doesn't have.
//   - Every detector in this package operates ONLY on
//     internal/audittrail's records for one account, scoped to a
//     supplied time window (see EntriesInWindow). oms-gateway's audit
//     trail is in-memory and, as audittrail's own doc comment says, does
//     not survive a restart — neither does anything computed from it.
//   - "Away from the touch" (spoofingDetector.go) is approximated using
//     the account's own most recent recorded fill price in the same
//     instrument as a reference price. oms-gateway has no live top-of-
//     book / last-traded-price feed of its own (see orders package's
//     TODO on the same subject) — a real deployment would want the
//     matching engine's actual best bid/offer at the moment of
//     placement, not one account's own trade history. Documented gap,
//     not silently assumed away.
package tradesurveillance

import (
	"sort"
	"time"

	"mercurius/omsgateway/internal/audittrail"
)

// DetectorConfiguration is every tunable threshold used by this package's
// heuristics. All fields have a documented, non-arbitrary rationale in
// DefaultDetectorConfiguration's comment — tune per-deployment via a real
// config source if these defaults prove too loose/tight for a given
// market's actual order sizes and latencies; this package takes no
// position on what "right" is beyond "documented and testable".
type DetectorConfiguration struct {
	// SpoofingLargeOrderQuantityThreshold: an order must be at least this
	// large (OrderQuantity) to even be considered a spoofing candidate.
	// Spoofing is fundamentally about projecting a false impression of
	// size, so a small order can't spoof regardless of its other
	// characteristics.
	SpoofingLargeOrderQuantityThreshold uint64

	// SpoofingCancelWithinDuration: the order must be cancelled within
	// this long of being routed to matching-engine to count as
	// "cancelled shortly after placement". A resting order that survives
	// longer than this was genuinely exposed to the market for a
	// meaningful time and is not flagged by this heuristic, regardless
	// of its price.
	SpoofingCancelWithinDuration time.Duration

	// SpoofingAwayFromTouchFractionThreshold: how far a limit order's
	// price must sit from the reference price (as a fraction of that
	// price) — on the side that makes it non-marketable — to count as
	// "placed away from the touch". E.g. 0.01 means a buy limit priced
	// more than 1% below the reference price, or a sell limit priced
	// more than 1% above it.
	SpoofingAwayFromTouchFractionThreshold float64

	// SpoofingFastCancelBeforeFillLatencyDuration: an ALTERNATE way to
	// satisfy "never at material risk of execution" when no reference
	// price is available to compute away-from-touch at all (e.g. the
	// account's first order in an instrument). A cancellation this fast
	// is faster than any realistic human decision loop and faster than
	// this deployment's own typical matching-engine round trip — a proxy
	// for "the order was never meant to be at risk", independent of
	// price.
	SpoofingFastCancelBeforeFillLatencyDuration time.Duration

	// SpoofingFollowUpWindowDuration: how long after a spoofing
	// candidate's cancellation to look for corroborating same-side
	// smaller fills or opposite-side fills, recorded as supporting
	// evidence on the flag (see SpoofingIncident.CorroboratingFollowUp).
	// Purely additional evidence — never required to trigger the base
	// flag, per FEATURES.md's own "especially when followed by" phrasing
	// (a strengthening signal, not a precondition).
	SpoofingFollowUpWindowDuration time.Duration

	// LayeringMinimumOrderCount: at least this many resting limit orders
	// at distinct, successive price levels on one side, alive at
	// overlapping times, are needed to call it a "layer" of fake depth.
	// Two orders isn't a ladder; FEATURES.md itself says "multiple
	// orders placed at successive price levels" (plural, implying more
	// than a pair).
	LayeringMinimumOrderCount int

	// LayeringWindowDuration: the layer's orders must all be placed
	// within this long of each other to count as one coordinated
	// layering attempt, not unrelated orders placed hours apart.
	LayeringWindowDuration time.Duration

	// LayeringCancelWithinDuration: how soon after the opposite-side fill
	// the layer's orders must be cancelled to count as "shortly after a
	// fill occurs on the opposite side".
	LayeringCancelWithinDuration time.Duration

	// LayeringMinimumCancelledFraction: the fraction (0..1] of the
	// layer's orders that must be cancelled within
	// LayeringCancelWithinDuration for FEATURES.md's "cancellation of
	// most/all of them" to be satisfied. 1.0 would require every single
	// layer order cancelled; something like 0.75 tolerates one or two
	// left resting (e.g. because they filled first) while still catching
	// the pattern.
	LayeringMinimumCancelledFraction float64
}

// DefaultDetectorConfiguration returns reasonable, documented starting
// thresholds. These are deliberately conservative-but-usable defaults for
// a demo/dev-scale order flow, NOT thresholds calibrated against any real
// exchange's actual latency/size distribution — see this package's doc
// comment for the broader "heuristic proxy, not certified" caveat.
func DefaultDetectorConfiguration() DetectorConfiguration {
	return DetectorConfiguration{
		SpoofingLargeOrderQuantityThreshold:         500,
		SpoofingCancelWithinDuration:                2 * time.Second,
		SpoofingAwayFromTouchFractionThreshold:      0.01,
		SpoofingFastCancelBeforeFillLatencyDuration: 150 * time.Millisecond,
		SpoofingFollowUpWindowDuration:              2 * time.Second,

		LayeringMinimumOrderCount:        3,
		LayeringWindowDuration:           5 * time.Second,
		LayeringCancelWithinDuration:     2 * time.Second,
		LayeringMinimumCancelledFraction: 0.75,
	}
}

// EntriesInWindow filters entries (already scoped to one account via
// audittrail.AuditTrail.EntriesForAccount) down to ones recorded within
// [windowStart, windowEnd], and returns them sorted oldest-first (the
// input from EntriesForAccount is already append-ordered, i.e. already
// oldest-first, but this function does not assume that — it sorts
// explicitly so it's correct regardless of caller behavior).
func EntriesInWindow(entries []audittrail.Entry, windowStart, windowEnd time.Time) []audittrail.Entry {
	var windowed []audittrail.Entry
	for _, entry := range entries {
		if !entry.RecordedAtTime.Before(windowStart) && !entry.RecordedAtTime.After(windowEnd) {
			windowed = append(windowed, entry)
		}
	}
	sort.SliceStable(windowed, func(i, j int) bool {
		return windowed[i].RecordedAtTime.Before(windowed[j].RecordedAtTime)
	})
	return windowed
}

// SurveillanceEngine runs every detector in this package with one shared
// configuration. Stateless and safe for concurrent use — it holds only
// its configuration, never any audit data itself (that's always passed in
// per call, per this package's "operates on a supplied slice" design).
type SurveillanceEngine struct {
	configuration DetectorConfiguration
}

// NewSurveillanceEngine builds an engine with the given configuration.
func NewSurveillanceEngine(configuration DetectorConfiguration) *SurveillanceEngine {
	return &SurveillanceEngine{configuration: configuration}
}

// SurveillanceReport is what a compliance-officer query returns: every
// incident this engine's detectors found in the supplied account/window,
// each carrying the specific evidence that triggered it.
type SurveillanceReport struct {
	AccountIdentifier  string              `json:"accountIdentifier"`
	WindowStartTime    time.Time           `json:"windowStartTime"`
	WindowEndTime      time.Time           `json:"windowEndTime"`
	SpoofingIncidents  []SpoofingIncident  `json:"spoofingIncidents,omitempty"`
	LayeringIncidents  []LayeringIncident  `json:"layeringIncidents,omitempty"`
	WashTradeIncidents []WashTradeIncident `json:"washTradeIncidents,omitempty"`
}

// RunAllDetectors runs the spoofing, layering, and wash-trade detectors
// for one account, restricted to [windowStart, windowEnd], and returns
// every flagged incident. This is the function the compliance-officer
// query endpoint calls — genuinely computed from whatever real entries
// are passed in, never canned.
//
// allEntries MUST be the FULL, un-account-filtered audit trail (e.g.
// audittrail.AuditTrail.AllEntries(), not EntriesForAccount) — see
// ScopeEntriesToAccount's doc comment for exactly why: a naive
// EntriesForAccount(accountIdentifier) filter silently drops every
// EventOrderCancelled/EventOrderCancelFailed entry (they carry no
// ClientAccountIdentifier at all, by audittrail's own design — see
// EventOrderRoutedToMatchingEngine's doc comment), which would make
// every cancellation this account made invisible to the spoofing/
// layering detectors even though every incident that matters hinges on
// cancellations. ScopeEntriesToAccount does the correct join instead.
func (engine *SurveillanceEngine) RunAllDetectors(
	accountIdentifier string,
	allEntries []audittrail.Entry,
	windowStart, windowEnd time.Time,
) SurveillanceReport {
	windowedEntries := EntriesInWindow(ScopeEntriesToAccount(allEntries, accountIdentifier), windowStart, windowEnd)

	return SurveillanceReport{
		AccountIdentifier:  accountIdentifier,
		WindowStartTime:    windowStart,
		WindowEndTime:      windowEnd,
		SpoofingIncidents:  DetectSpoofing(windowedEntries, engine.configuration),
		LayeringIncidents:  DetectLayering(windowedEntries, engine.configuration),
		WashTradeIncidents: DetectWashTrades(windowedEntries),
	}
}
