// Package audittrail is FEATURES.md's "Audit trail: immutable log of
// every order, modification, cancellation" — a compliance-facing record
// of every consequential thing this OMS did, independent of whatever the
// client-facing response said. The client response can be lost (a
// dropped connection); the audit entry, once appended, never is.
package audittrail

import (
	"sync"
	"time"
)

// EventType enumerates every kind of thing worth auditing. Deliberately
// a closed set of string constants (not a free-text field) so a
// compliance query can filter reliably.
type EventType string

const (
	EventOrderSubmitted             EventType = "ORDER_SUBMITTED"
	EventOrderRejected              EventType = "ORDER_REJECTED"
	EventOrderMatchingEngineFailure EventType = "ORDER_MATCHING_ENGINE_FAILURE"
	EventOrderFilled                EventType = "ORDER_FILLED"
	EventOrderCancelled             EventType = "ORDER_CANCELLED"
	EventOrderCancelFailed          EventType = "ORDER_CANCEL_FAILED"
	EventCoverProtectiveLegPlaced   EventType = "COVER_PROTECTIVE_LEG_PLACED"
	EventCoverProtectiveLegFailed   EventType = "COVER_PROTECTIVE_LEG_FAILED"
	EventAfterMarketOrderQueued     EventType = "AFTER_MARKET_ORDER_QUEUED"
	EventMarketSessionOpened        EventType = "MARKET_SESSION_OPENED"
	EventMarketSessionClosed        EventType = "MARKET_SESSION_CLOSED"

	// EventPaperOrderFilled and EventPaperOrderSimulationFailed are
	// FEATURES.md §7's paper trading events — distinct from
	// EventOrderFilled/EventOrderMatchingEngineFailure so the audit trail
	// itself makes it obvious a fill was simulated, not real, without
	// having to cross-reference the order request payload.
	EventPaperOrderFilled           EventType = "PAPER_ORDER_FILLED"
	EventPaperOrderSimulationFailed EventType = "PAPER_ORDER_SIMULATION_FAILED"

	// EventStrategyLimitRejected is FEATURES.md §7's strategy resource
	// limits — recorded when internal/algolimits rejects an order before
	// it ever reaches the KYC/freeze/risk gates.
	EventStrategyLimitRejected EventType = "STRATEGY_LIMIT_REJECTED"
)

// Entry is one immutable audit record. Every field is set at append time
// and never modified afterward — see AuditTrail's own doc comment for
// why there's deliberately no update/delete method.
type Entry struct {
	RecordedAtTime                    time.Time `json:"recordedAtTime"`
	EventType                         EventType `json:"eventType"`
	ClientAccountIdentifier           string    `json:"clientAccountIdentifier,omitempty"`
	InstrumentSymbol                  string    `json:"instrumentSymbol,omitempty"`
	MatchingEngineOrderSequenceNumber uint64    `json:"matchingEngineOrderSequenceNumber,omitempty"`
	DetailMessage                     string    `json:"detailMessage,omitempty"`
}

// AuditTrail is an append-only, in-memory log. "Immutable" here means:
// no method on this type can modify or remove an entry once appended —
// only Append (add) and the two read methods exist. That's a real
// guarantee at the Go API level, not just a naming convention.
//
// TODO(real build): in-memory only — an oms-gateway restart loses the
// entire audit trail, which is disqualifying for anything actually
// regulated. A real build needs this backed by an actual
// append-only/WORM store (e.g. a dedicated audit log table with no
// UPDATE/DELETE grants, or an event log like Kafka with infinite
// retention) that survives a restart and can't be tampered with even by
// this service's own operators.
type AuditTrail struct {
	mutexGuardingEntries sync.Mutex
	entries              []Entry
}

func NewAuditTrail() *AuditTrail {
	return &AuditTrail{}
}

// Append adds a new entry, stamped with the current time. There is no
// corresponding Remove/Update — that's the whole point.
func (trail *AuditTrail) Append(entry Entry) {
	entry.RecordedAtTime = time.Now()

	trail.mutexGuardingEntries.Lock()
	defer trail.mutexGuardingEntries.Unlock()
	trail.entries = append(trail.entries, entry)
}

// AllEntries returns every entry recorded so far, oldest first. Returns
// a copy — callers can't mutate the trail's internal slice through it.
func (trail *AuditTrail) AllEntries() []Entry {
	trail.mutexGuardingEntries.Lock()
	defer trail.mutexGuardingEntries.Unlock()

	entriesCopy := make([]Entry, len(trail.entries))
	copy(entriesCopy, trail.entries)
	return entriesCopy
}

// EntriesForAccount returns every entry for one account, oldest first.
// Entries with no ClientAccountIdentifier (e.g. market-session events)
// are never matched by this filter.
func (trail *AuditTrail) EntriesForAccount(clientAccountIdentifier string) []Entry {
	trail.mutexGuardingEntries.Lock()
	defer trail.mutexGuardingEntries.Unlock()

	var matchingEntries []Entry
	for _, entry := range trail.entries {
		if entry.ClientAccountIdentifier == clientAccountIdentifier {
			matchingEntries = append(matchingEntries, entry)
		}
	}
	return matchingEntries
}
