// Package nomineesuccession implements FEATURES.md §21's "Nominee
// succession workflow: documented, auditable transfer process triggered
// by a death certificate submission" — modeled closely on
// internal/accountcontrol's freeze state machine (a real, mutex-guarded,
// in-memory state machine) with a real, immutable, append-only audit
// trail of every state transition, mirroring oms-gateway's
// internal/audittrail's "immutable log" quality bar (no update/delete
// method exists on the trail type — that's a real Go-API-level
// guarantee, not just a naming convention).
//
// Scope boundary, stated loudly: this is explicitly a WORKFLOW/PAPERWORK
// state machine, NOT an identity-verification feature. "Death
// certificate submission" here means accepting a real reference/
// document-id string identifying a submitted document — this package
// makes zero attempt to verify that document is genuine, that the
// account holder is actually deceased, or that the nominee's own
// identity-document reference is real. A real build needs an actual
// identity-verification/document-authentication integration (see
// kyc-onboarding's real KYC flow elsewhere in this repo for the shape
// that would take) sitting IN FRONT of this workflow, not inside it.
//
// State machine: SUBMITTED -> UNDER_REVIEW -> APPROVED -> TRANSFERRED,
// or REJECTED at either review step (UNDER_REVIEW or APPROVED can both
// be rejected; SUBMITTED can also be rejected directly without ever
// entering review). TRANSFERRED and REJECTED are both terminal — no
// further transition is possible from either.
//
// TODO(real build): in-memory only (every nominee registration,
// succession request, and its audit trail is lost on restart); no auth
// on any endpoint (same admin/RBAC gap accountcontrol's freeze endpoints
// already document); no real identity verification of the nominee or
// the submitted death-certificate reference, as stated above; no actual
// asset-transfer mechanism triggered by reaching TRANSFERRED (this
// package only records that the state was reached, it doesn't move any
// ledger balance or position — that would be a separate, much bigger
// integration with ledger/positions); no support for MULTIPLE competing
// nominees or a dispute-resolution process if more than one nominee is
// registered (RegisterNominee for the same account simply overwrites the
// prior nominee registration, same "last write wins" convention as
// accountcontrol's freeze reason).
package nomineesuccession

import (
	"errors"
	"sync"
	"time"
)

// SuccessionState enumerates every state a succession request can be
// in — a closed set of string constants, like audittrail.EventType.
type SuccessionState string

const (
	StateSubmitted   SuccessionState = "SUBMITTED"
	StateUnderReview SuccessionState = "UNDER_REVIEW"
	StateApproved    SuccessionState = "APPROVED"
	StateTransferred SuccessionState = "TRANSFERRED"
	StateRejected    SuccessionState = "REJECTED"
)

// TransitionEventType enumerates every kind of thing worth auditing on a
// succession request — mirrors audittrail.EventType's "closed set of
// string constants" discipline exactly.
type TransitionEventType string

const (
	EventNomineeRegistered   TransitionEventType = "NOMINEE_REGISTERED"
	EventSuccessionSubmitted TransitionEventType = "SUCCESSION_SUBMITTED"
	EventMovedToUnderReview  TransitionEventType = "MOVED_TO_UNDER_REVIEW"
	EventApproved            TransitionEventType = "APPROVED"
	EventTransferred         TransitionEventType = "TRANSFERRED"
	EventRejected            TransitionEventType = "REJECTED"
)

var (
	// ErrAccountIdentifierRequired is returned when a caller supplies an
	// empty accountIdentifier.
	ErrAccountIdentifierRequired = errors.New("accountIdentifier is required")

	// ErrNomineeNameRequired is returned when a caller supplies an empty
	// nominee name.
	ErrNomineeNameRequired = errors.New("nominee name is required")

	// ErrNomineeRelationshipRequired is returned when a caller supplies
	// an empty relationship (e.g. "spouse", "child").
	ErrNomineeRelationshipRequired = errors.New("nominee relationship is required")

	// ErrNoNomineeRegistered is returned when a succession request is
	// submitted for an account with no registered nominee.
	ErrNoNomineeRegistered = errors.New("no nominee is registered for this account")

	// ErrDeathCertificateReferenceRequired is returned when a succession
	// request is submitted with an empty document reference.
	ErrDeathCertificateReferenceRequired = errors.New("deathCertificateDocumentReference is required")

	// ErrNoSuccessionRequestFound is returned when a caller tries to
	// transition a succession request that was never submitted.
	ErrNoSuccessionRequestFound = errors.New("no succession request found for this account")

	// ErrSuccessionRequestAlreadyExists is returned when SubmitSuccessionRequest
	// is called for an account that already has an active (non-terminal)
	// succession request — a real build might allow re-submission after
	// a REJECTED outcome, but never while one is still in flight.
	ErrSuccessionRequestAlreadyExists = errors.New("an active succession request already exists for this account")

	// ErrInvalidStateTransition is returned when a transition is
	// attempted from a state that doesn't allow it (e.g. approving a
	// request that's still SUBMITTED and never entered review, or any
	// transition attempted from a terminal state).
	ErrInvalidStateTransition = errors.New("invalid succession state transition")

	// ErrActorRequired is returned when a state-changing call omits who
	// performed it — every transition in this package's audit trail
	// records a real actor, never blank.
	ErrActorRequired = errors.New("actor is required for every state transition")
)

// Nominee is the real, registered beneficiary for one account.
type Nominee struct {
	AccountIdentifier          string `json:"accountIdentifier"`
	NomineeFullName            string `json:"nomineeFullName"`
	NomineeRelationship        string `json:"nomineeRelationship"`
	NomineeIdentityDocumentRef string `json:"nomineeIdentityDocumentReference,omitempty"`
}

// SuccessionRequest is one real succession workflow instance for an
// account — at most one ACTIVE (non-terminal) request per account at a
// time.
type SuccessionRequest struct {
	AccountIdentifier                 string          `json:"accountIdentifier"`
	Nominee                           Nominee         `json:"nominee"`
	DeathCertificateDocumentReference string          `json:"deathCertificateDocumentReference"`
	CurrentState                      SuccessionState `json:"currentState"`
	SubmittedAtTime                   time.Time       `json:"submittedAtTime"`
}

// TransitionRecord is one immutable audit entry for one succession
// request's state transition — the "who/when/why" this feature's
// requirements call for. Mirrors audittrail.Entry's shape and its
// append-only guarantee: AuditTrail below has no update/delete method.
type TransitionRecord struct {
	AccountIdentifier string              `json:"accountIdentifier"`
	EventType         TransitionEventType `json:"eventType"`
	FromState         SuccessionState     `json:"fromState,omitempty"`
	ToState           SuccessionState     `json:"toState"`
	Actor             string              `json:"actor"`
	Reason            string              `json:"reason,omitempty"`
	RecordedAtTime    time.Time           `json:"recordedAtTime"`
}

// Registry is the mutex-guarded, in-memory home for every registered
// nominee, every succession request, and the full immutable audit trail
// of every transition ever recorded.
type Registry struct {
	mutexGuardingState sync.Mutex

	nomineeByAccount           map[string]Nominee
	successionRequestByAccount map[string]*SuccessionRequest
	transitionRecords          []TransitionRecord
}

// NewRegistry builds an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		nomineeByAccount:           make(map[string]Nominee),
		successionRequestByAccount: make(map[string]*SuccessionRequest),
	}
}

// RegisterNominee records (or overwrites) the nominee for an account.
// This is NOT itself a succession request — it's the beneficiary
// designation a real succession request will later reference.
func (registry *Registry) RegisterNominee(accountIdentifier string, nomineeFullName string, nomineeRelationship string, nomineeIdentityDocumentRef string, actor string) error {
	if accountIdentifier == "" {
		return ErrAccountIdentifierRequired
	}
	if nomineeFullName == "" {
		return ErrNomineeNameRequired
	}
	if nomineeRelationship == "" {
		return ErrNomineeRelationshipRequired
	}
	if actor == "" {
		return ErrActorRequired
	}

	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	registry.nomineeByAccount[accountIdentifier] = Nominee{
		AccountIdentifier:          accountIdentifier,
		NomineeFullName:            nomineeFullName,
		NomineeRelationship:        nomineeRelationship,
		NomineeIdentityDocumentRef: nomineeIdentityDocumentRef,
	}
	registry.appendTransitionLocked(TransitionRecord{
		AccountIdentifier: accountIdentifier,
		EventType:         EventNomineeRegistered,
		ToState:           "", // nominee registration has no succession state of its own
		Actor:             actor,
		Reason:            "nominee " + nomineeFullName + " (" + nomineeRelationship + ") registered",
	})
	return nil
}

// GetNominee returns the currently-registered nominee for an account, if
// any.
func (registry *Registry) GetNominee(accountIdentifier string) (Nominee, bool) {
	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	nominee, exists := registry.nomineeByAccount[accountIdentifier]
	return nominee, exists
}

// SubmitSuccessionRequest starts a real succession workflow for
// accountIdentifier, triggered by a "death certificate submission"
// event — deathCertificateDocumentReference is accepted as a real
// reference/document-id string; see the package doc's honesty note that
// this is NOT verified. Requires a nominee already registered for the
// account, and fails if an active (non-terminal) request already exists.
func (registry *Registry) SubmitSuccessionRequest(accountIdentifier string, deathCertificateDocumentReference string, actor string, now time.Time) (SuccessionRequest, error) {
	if accountIdentifier == "" {
		return SuccessionRequest{}, ErrAccountIdentifierRequired
	}
	if deathCertificateDocumentReference == "" {
		return SuccessionRequest{}, ErrDeathCertificateReferenceRequired
	}
	if actor == "" {
		return SuccessionRequest{}, ErrActorRequired
	}

	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	nominee, hasNominee := registry.nomineeByAccount[accountIdentifier]
	if !hasNominee {
		return SuccessionRequest{}, ErrNoNomineeRegistered
	}

	if existing, exists := registry.successionRequestByAccount[accountIdentifier]; exists && !isTerminalState(existing.CurrentState) {
		return SuccessionRequest{}, ErrSuccessionRequestAlreadyExists
	}

	request := &SuccessionRequest{
		AccountIdentifier:                 accountIdentifier,
		Nominee:                           nominee,
		DeathCertificateDocumentReference: deathCertificateDocumentReference,
		CurrentState:                      StateSubmitted,
		SubmittedAtTime:                   now,
	}
	registry.successionRequestByAccount[accountIdentifier] = request

	registry.appendTransitionLocked(TransitionRecord{
		AccountIdentifier: accountIdentifier,
		EventType:         EventSuccessionSubmitted,
		ToState:           StateSubmitted,
		Actor:             actor,
		Reason:            "death certificate reference " + deathCertificateDocumentReference + " submitted",
		RecordedAtTime:    now,
	})

	return *request, nil
}

// MoveToUnderReview transitions SUBMITTED -> UNDER_REVIEW.
func (registry *Registry) MoveToUnderReview(accountIdentifier string, actor string, reason string, now time.Time) (SuccessionRequest, error) {
	return registry.transition(accountIdentifier, StateSubmitted, StateUnderReview, EventMovedToUnderReview, actor, reason, now)
}

// Approve transitions UNDER_REVIEW -> APPROVED.
func (registry *Registry) Approve(accountIdentifier string, actor string, reason string, now time.Time) (SuccessionRequest, error) {
	return registry.transition(accountIdentifier, StateUnderReview, StateApproved, EventApproved, actor, reason, now)
}

// MarkTransferred transitions APPROVED -> TRANSFERRED — the terminal,
// successful outcome. See the package doc: this only records that the
// state was reached, it does not itself move any real asset/balance.
func (registry *Registry) MarkTransferred(accountIdentifier string, actor string, reason string, now time.Time) (SuccessionRequest, error) {
	return registry.transition(accountIdentifier, StateApproved, StateTransferred, EventTransferred, actor, reason, now)
}

// Reject transitions either SUBMITTED or UNDER_REVIEW -> REJECTED — the
// terminal, unsuccessful outcome. A request already APPROVED or
// TRANSFERRED cannot be rejected (that's ErrInvalidStateTransition); a
// real build might add a separate "reversal" workflow for that, not
// modeled here.
func (registry *Registry) Reject(accountIdentifier string, actor string, reason string, now time.Time) (SuccessionRequest, error) {
	if accountIdentifier == "" {
		return SuccessionRequest{}, ErrAccountIdentifierRequired
	}
	if actor == "" {
		return SuccessionRequest{}, ErrActorRequired
	}

	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	request, exists := registry.successionRequestByAccount[accountIdentifier]
	if !exists {
		return SuccessionRequest{}, ErrNoSuccessionRequestFound
	}
	if request.CurrentState != StateSubmitted && request.CurrentState != StateUnderReview {
		return SuccessionRequest{}, ErrInvalidStateTransition
	}

	fromState := request.CurrentState
	request.CurrentState = StateRejected

	registry.appendTransitionLocked(TransitionRecord{
		AccountIdentifier: accountIdentifier,
		EventType:         EventRejected,
		FromState:         fromState,
		ToState:           StateRejected,
		Actor:             actor,
		Reason:            reason,
		RecordedAtTime:    now,
	})

	return *request, nil
}

// transition is the shared real state-machine engine every non-Reject
// transition method above calls into: it verifies the request exists,
// verifies it's currently in expectedFromState (returning
// ErrInvalidStateTransition otherwise — this is what makes e.g.
// approving a still-SUBMITTED request or double-transferring impossible),
// mutates the state, and appends a real audit record.
func (registry *Registry) transition(
	accountIdentifier string,
	expectedFromState SuccessionState,
	toState SuccessionState,
	eventType TransitionEventType,
	actor string,
	reason string,
	now time.Time,
) (SuccessionRequest, error) {
	if accountIdentifier == "" {
		return SuccessionRequest{}, ErrAccountIdentifierRequired
	}
	if actor == "" {
		return SuccessionRequest{}, ErrActorRequired
	}

	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	request, exists := registry.successionRequestByAccount[accountIdentifier]
	if !exists {
		return SuccessionRequest{}, ErrNoSuccessionRequestFound
	}
	if request.CurrentState != expectedFromState {
		return SuccessionRequest{}, ErrInvalidStateTransition
	}

	request.CurrentState = toState

	registry.appendTransitionLocked(TransitionRecord{
		AccountIdentifier: accountIdentifier,
		EventType:         eventType,
		FromState:         expectedFromState,
		ToState:           toState,
		Actor:             actor,
		Reason:            reason,
		RecordedAtTime:    now,
	})

	return *request, nil
}

// GetSuccessionRequest returns the current succession request for an
// account, if one exists (active or terminal).
func (registry *Registry) GetSuccessionRequest(accountIdentifier string) (SuccessionRequest, bool) {
	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	request, exists := registry.successionRequestByAccount[accountIdentifier]
	if !exists {
		return SuccessionRequest{}, false
	}
	return *request, true
}

// AuditTrailForAccount returns every transition record for one account,
// oldest first — the real "who/when/why" log this feature's
// requirements call for. Returns a copy; nothing on Registry can mutate
// or remove a record once appended, mirroring audittrail.AuditTrail's
// own guarantee.
func (registry *Registry) AuditTrailForAccount(accountIdentifier string) []TransitionRecord {
	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	var matching []TransitionRecord
	for _, record := range registry.transitionRecords {
		if record.AccountIdentifier == accountIdentifier {
			matching = append(matching, record)
		}
	}
	return matching
}

// AllAuditTrailRecords returns every transition record ever appended,
// across every account, oldest first.
func (registry *Registry) AllAuditTrailRecords() []TransitionRecord {
	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	recordsCopy := make([]TransitionRecord, len(registry.transitionRecords))
	copy(recordsCopy, registry.transitionRecords)
	return recordsCopy
}

// appendTransitionLocked appends one immutable audit record. Caller must
// already hold mutexGuardingState. If RecordedAtTime was left zero by
// the caller (e.g. RegisterNominee, which takes no `now` parameter), it
// is stamped with the real wall clock, exactly like audittrail.Append
// does unconditionally.
func (registry *Registry) appendTransitionLocked(record TransitionRecord) {
	if record.RecordedAtTime.IsZero() {
		record.RecordedAtTime = time.Now()
	}
	registry.transitionRecords = append(registry.transitionRecords, record)
}

func isTerminalState(state SuccessionState) bool {
	return state == StateTransferred || state == StateRejected
}
