// Package audittrail is FEATURES.md's "Audit trail: immutable log of
// every order, modification, cancellation" — a compliance-facing record
// of every consequential thing this OMS did, independent of whatever the
// client-facing response said. The client response can be lost (a
// dropped connection); the audit entry, once appended, never is.
package audittrail

import (
	"log"
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

	// EventOrderRoutedToMatchingEngine is appended ONLY once a live
	// (non-paper) order successfully hands off to matching-engine and is
	// assigned a MatchingEngineOrderSequenceNumber — i.e. strictly after
	// the EventOrderSubmitted entry for the same order, which is
	// appended earlier (right after OMS-side sequencing, before the
	// matching-engine round trip) and therefore can never itself carry
	// that sequence number.
	//
	// This entry exists SOLELY so internal/tradesurveillance (and any
	// other future consumer) has a reliable join key: it repeats the
	// order's shape (side/quantity/price/order-type) already recorded on
	// EventOrderSubmitted, but this time paired with the
	// MatchingEngineOrderSequenceNumber that a later EventOrderCancelled/
	// EventOrderCancelFailed/EventOrderFilled entry for the same order
	// will also carry — see cmd/server/main.go's buildCancelOrderHandler,
	// which never learns ClientAccountIdentifier from
	// orders.CancelOrderRequest and so cannot stamp it directly; this
	// entry's MatchingEngineOrderSequenceNumber is the only thing that
	// links a cancellation back to the account/order-shape that placed
	// it. A paper-trading order never reaches this point (see
	// EventPaperOrderFilled/EventPaperOrderSimulationFailed) and so never
	// gets one of these — surveillance is scoped to real, matching-
	// engine-routed orders only.
	EventOrderRoutedToMatchingEngine EventType = "ORDER_ROUTED_TO_MATCHING_ENGINE"

	// EventSettlementFailedPositionNotApplied is appended when a REAL
	// trade genuinely happened at matching-engine (a fill event came
	// back) but posting its settlement journal entry to the ledger
	// failed — see cmd/server/main.go's settleTradeAgainstLedgerAndLocalCache.
	// Distinct from EventOrderMatchingEngineFailure (which means the
	// order never reached/matched at matching-engine at all): this means
	// it DID match, but the OMS deliberately did NOT apply the fill to
	// positionBook/markToMarketEngine, to avoid the position book
	// silently drifting out of sync with an unsettled ledger. This is
	// the loud, non-silent record of that discrepancy — a real build
	// would also drive a reconciliation/retry job off entries like this
	// one, which this build does not implement (see that function's own
	// doc comment).
	EventSettlementFailedPositionNotApplied EventType = "SETTLEMENT_FAILED_POSITION_NOT_APPLIED"
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

	// AuthenticatedActorAccountIdentifier is the REAL authenticated caller
	// (authmiddleware.AuthenticatedAccountIdentifier(request)) — new this
	// build, alongside real Postgres persistence (see
	// docs/BUILD_LOG.md's Postgres-persistence entry). Distinct from
	// ClientAccountIdentifier (the account the order/action is FOR — which
	// authenticatedAccountMatches already guarantees equals the
	// authenticated caller for a directly-submitted order): this field
	// exists so an entry appended by an internal, non-per-request-
	// authenticated caller (the DMA/FIX-style gateway, the auto-
	// liquidation engine) is honestly distinguishable from one a real
	// logged-in human triggered — empty ("") for the former, never
	// guessed. Additive/omitempty — every entry appended before this
	// field existed is completely unaffected.
	AuthenticatedActorAccountIdentifier string `json:"authenticatedActorAccountIdentifier,omitempty"`

	// The fields below are ADDITIVE (all omitempty; every pre-existing
	// caller/entry that never sets them is completely unaffected) and
	// exist so internal/tradesurveillance can compute real spoofing/
	// layering/wash-trade heuristics directly off structured audit data
	// instead of parsing DetailMessage's free-text sentences. Only
	// EventOrderSubmitted, EventOrderRoutedToMatchingEngine, and
	// EventOrderFilled entries populate them as of this build — see each
	// field's own comment for exactly which event types set it.

	// OrderSideIsBuyNotSell mirrors orders.OrderSubmissionRequest's field
	// of the same name. Set on EventOrderSubmitted and
	// EventOrderRoutedToMatchingEngine. Pointer so "side unknown/not
	// applicable" (nil — every event type other than those two, plus
	// every entry appended before this field existed) is distinguishable
	// from a genuine sell (false).
	OrderSideIsBuyNotSell *bool `json:"orderSideIsBuyNotSell,omitempty"`

	// OrderQuantity and LimitPriceInMinorUnits mirror
	// orders.OrderSubmissionRequest's fields of the same name. Set on
	// EventOrderSubmitted and EventOrderRoutedToMatchingEngine.
	// LimitPriceInMinorUnits is 0 (meaningless, same convention as the
	// request type itself) for a market order — see
	// OrderIsMarketOrderNotLimit below to tell the two apart.
	OrderQuantity          uint64 `json:"orderQuantity,omitempty"`
	LimitPriceInMinorUnits int64  `json:"limitPriceInMinorUnits,omitempty"`

	// OrderIsMarketOrderNotLimit mirrors
	// orders.OrderSubmissionRequest's field of the same name. Set on
	// EventOrderSubmitted and EventOrderRoutedToMatchingEngine.
	OrderIsMarketOrderNotLimit bool `json:"orderIsMarketOrderNotLimit,omitempty"`

	// BuyingClientAccountIdentifier, SellingClientAccountIdentifier,
	// ExecutedPriceInMinorUnits, and ExecutedQuantity are set only on
	// EventOrderFilled — the structured equivalent of that event's
	// existing free-text DetailMessage (kept as-is for backward
	// compatibility/human readability). A wash-trade check needs these
	// two account fields on the SAME entry to detect one account on both
	// sides of one trade without any text parsing.
	BuyingClientAccountIdentifier  string `json:"buyingClientAccountIdentifier,omitempty"`
	SellingClientAccountIdentifier string `json:"sellingClientAccountIdentifier,omitempty"`
	ExecutedPriceInMinorUnits      int64  `json:"executedPriceInMinorUnits,omitempty"`
	ExecutedQuantity               uint64 `json:"executedQuantity,omitempty"`
}

// AuditTrail is an append-only log. "Immutable" here means: no method
// on this type can modify or remove an entry once appended — only
// Append (add) and the two read methods exist. That's a real guarantee
// at the Go API level, not just a naming convention.
//
// Real Postgres persistence (docs/BUILD_LOG.md's Postgres-persistence
// entry): when constructed via NewPostgresBackedAuditTrail
// (postgresBacking.go, same package), `postgres` is set and every
// method below reads/writes real Postgres instead of the in-memory
// `entries` slice — Postgres becomes the sole source of truth in that
// mode, not a mirror. NewAuditTrail() (unchanged) leaves `postgres` nil
// and behaves exactly as it always did — in-memory only, restart loses
// everything, which is exactly the historical behavior this comment
// used to describe as "disqualifying for anything actually regulated."
// A real production deployment should always use the Postgres-backed
// constructor; the in-memory one remains for tests and for a
// Postgres-unreachable-at-startup fallback (see cmd/server/main.go).
type AuditTrail struct {
	mutexGuardingEntries sync.Mutex
	entries              []Entry
	postgres             *postgresBacking
}

func NewAuditTrail() *AuditTrail {
	return &AuditTrail{}
}

// Append adds a new entry, stamped with the current time. There is no
// corresponding Remove/Update — that's the whole point. When Postgres-
// backed, a write failure is logged-and-swallowed (matching this
// method's pre-existing signature, which has no error return) rather
// than panicking — see docs/BUILD_LOG.md's known-limitations list.
func (trail *AuditTrail) Append(entry Entry) {
	entry.RecordedAtTime = time.Now()

	if trail.postgres != nil {
		if insertError := trail.appendToPostgres(entry); insertError != nil {
			log.Printf("audittrail: FAILED to persist entry to Postgres (event=%s account=%s): %v", entry.EventType, entry.ClientAccountIdentifier, insertError)
		}
		return
	}

	trail.mutexGuardingEntries.Lock()
	defer trail.mutexGuardingEntries.Unlock()
	trail.entries = append(trail.entries, entry)
}

// AllEntries returns every entry recorded so far, oldest first. Returns
// a copy — callers can't mutate the trail's internal slice through it.
func (trail *AuditTrail) AllEntries() []Entry {
	if trail.postgres != nil {
		return trail.allEntriesFromPostgres()
	}

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
	if trail.postgres != nil {
		return trail.entriesForAccountFromPostgres(clientAccountIdentifier)
	}

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
