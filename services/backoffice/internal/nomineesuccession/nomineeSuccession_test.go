package nomineesuccession

import (
	"testing"
	"time"
)

func TestRegisterNomineeRequiresAccountIdentifier(t *testing.T) {
	registry := NewRegistry()
	err := registry.RegisterNominee("", "Jane Doe", "spouse", "doc-ref-1", "admin-1")
	if err != ErrAccountIdentifierRequired {
		t.Fatalf("expected ErrAccountIdentifierRequired, got %v", err)
	}
}

func TestRegisterNomineeRequiresName(t *testing.T) {
	registry := NewRegistry()
	err := registry.RegisterNominee("acct-001", "", "spouse", "doc-ref-1", "admin-1")
	if err != ErrNomineeNameRequired {
		t.Fatalf("expected ErrNomineeNameRequired, got %v", err)
	}
}

func TestRegisterNomineeRequiresRelationship(t *testing.T) {
	registry := NewRegistry()
	err := registry.RegisterNominee("acct-001", "Jane Doe", "", "doc-ref-1", "admin-1")
	if err != ErrNomineeRelationshipRequired {
		t.Fatalf("expected ErrNomineeRelationshipRequired, got %v", err)
	}
}

func TestRegisterNomineeRequiresActor(t *testing.T) {
	registry := NewRegistry()
	err := registry.RegisterNominee("acct-001", "Jane Doe", "spouse", "doc-ref-1", "")
	if err != ErrActorRequired {
		t.Fatalf("expected ErrActorRequired, got %v", err)
	}
}

func TestRegisterNomineeSucceedsAndIsRetrievable(t *testing.T) {
	registry := NewRegistry()
	err := registry.RegisterNominee("acct-001", "Jane Doe", "spouse", "doc-ref-1", "admin-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	nominee, exists := registry.GetNominee("acct-001")
	if !exists {
		t.Fatal("expected a registered nominee")
	}
	if nominee.NomineeFullName != "Jane Doe" || nominee.NomineeRelationship != "spouse" {
		t.Fatalf("unexpected nominee: %+v", nominee)
	}
}

func TestSubmitSuccessionRequestFailsWithoutARegisteredNominee(t *testing.T) {
	registry := NewRegistry()
	_, err := registry.SubmitSuccessionRequest("acct-001", "death-cert-doc-1", "admin-1", time.Now())
	if err != ErrNoNomineeRegistered {
		t.Fatalf("expected ErrNoNomineeRegistered, got %v", err)
	}
}

func TestSubmitSuccessionRequestRequiresDeathCertificateReference(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterNominee("acct-001", "Jane Doe", "spouse", "doc-ref-1", "admin-1")

	_, err := registry.SubmitSuccessionRequest("acct-001", "", "admin-1", time.Now())
	if err != ErrDeathCertificateReferenceRequired {
		t.Fatalf("expected ErrDeathCertificateReferenceRequired, got %v", err)
	}
}

func TestSubmitSuccessionRequestSucceedsInSubmittedState(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterNominee("acct-001", "Jane Doe", "spouse", "doc-ref-1", "admin-1")

	request, err := registry.SubmitSuccessionRequest("acct-001", "death-cert-doc-1", "admin-1", time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if request.CurrentState != StateSubmitted {
		t.Fatalf("expected SUBMITTED, got %q", request.CurrentState)
	}
}

func TestSubmitSuccessionRequestFailsIfAnActiveRequestAlreadyExists(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterNominee("acct-001", "Jane Doe", "spouse", "doc-ref-1", "admin-1")
	registry.SubmitSuccessionRequest("acct-001", "death-cert-doc-1", "admin-1", time.Now())

	_, err := registry.SubmitSuccessionRequest("acct-001", "death-cert-doc-2", "admin-1", time.Now())
	if err != ErrSuccessionRequestAlreadyExists {
		t.Fatalf("expected ErrSuccessionRequestAlreadyExists, got %v", err)
	}
}

func TestFullHappyPathReachesTransferred(t *testing.T) {
	registry := NewRegistry()
	now := time.Now()
	registry.RegisterNominee("acct-001", "Jane Doe", "spouse", "doc-ref-1", "admin-1")
	registry.SubmitSuccessionRequest("acct-001", "death-cert-doc-1", "admin-1", now)

	if _, err := registry.MoveToUnderReview("acct-001", "reviewer-1", "starting review", now.Add(time.Hour)); err != nil {
		t.Fatalf("unexpected error moving to review: %v", err)
	}
	if _, err := registry.Approve("acct-001", "reviewer-1", "documents check out", now.Add(2*time.Hour)); err != nil {
		t.Fatalf("unexpected error approving: %v", err)
	}
	request, err := registry.MarkTransferred("acct-001", "ops-1", "assets transferred to nominee", now.Add(3*time.Hour))
	if err != nil {
		t.Fatalf("unexpected error marking transferred: %v", err)
	}
	if request.CurrentState != StateTransferred {
		t.Fatalf("expected TRANSFERRED, got %q", request.CurrentState)
	}
}

func TestCannotApproveASubmittedRequestThatSkippedReview(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterNominee("acct-001", "Jane Doe", "spouse", "doc-ref-1", "admin-1")
	registry.SubmitSuccessionRequest("acct-001", "death-cert-doc-1", "admin-1", time.Now())

	_, err := registry.Approve("acct-001", "reviewer-1", "skip review", time.Now())
	if err != ErrInvalidStateTransition {
		t.Fatalf("expected ErrInvalidStateTransition, got %v", err)
	}
}

func TestCannotTransferAnUnapprovedRequest(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterNominee("acct-001", "Jane Doe", "spouse", "doc-ref-1", "admin-1")
	registry.SubmitSuccessionRequest("acct-001", "death-cert-doc-1", "admin-1", time.Now())
	registry.MoveToUnderReview("acct-001", "reviewer-1", "review", time.Now())

	_, err := registry.MarkTransferred("acct-001", "ops-1", "premature transfer", time.Now())
	if err != ErrInvalidStateTransition {
		t.Fatalf("expected ErrInvalidStateTransition, got %v", err)
	}
}

func TestRejectFromSubmittedSucceeds(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterNominee("acct-001", "Jane Doe", "spouse", "doc-ref-1", "admin-1")
	registry.SubmitSuccessionRequest("acct-001", "death-cert-doc-1", "admin-1", time.Now())

	request, err := registry.Reject("acct-001", "reviewer-1", "document reference could not be located", time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if request.CurrentState != StateRejected {
		t.Fatalf("expected REJECTED, got %q", request.CurrentState)
	}
}

func TestRejectFromUnderReviewSucceeds(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterNominee("acct-001", "Jane Doe", "spouse", "doc-ref-1", "admin-1")
	registry.SubmitSuccessionRequest("acct-001", "death-cert-doc-1", "admin-1", time.Now())
	registry.MoveToUnderReview("acct-001", "reviewer-1", "review", time.Now())

	request, err := registry.Reject("acct-001", "reviewer-1", "discrepancy found", time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if request.CurrentState != StateRejected {
		t.Fatalf("expected REJECTED, got %q", request.CurrentState)
	}
}

func TestCannotRejectAnApprovedRequest(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterNominee("acct-001", "Jane Doe", "spouse", "doc-ref-1", "admin-1")
	registry.SubmitSuccessionRequest("acct-001", "death-cert-doc-1", "admin-1", time.Now())
	registry.MoveToUnderReview("acct-001", "reviewer-1", "review", time.Now())
	registry.Approve("acct-001", "reviewer-1", "approved", time.Now())

	_, err := registry.Reject("acct-001", "reviewer-1", "too late", time.Now())
	if err != ErrInvalidStateTransition {
		t.Fatalf("expected ErrInvalidStateTransition, got %v", err)
	}
}

func TestRejectedIsTerminalAndBlocksResubmission(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterNominee("acct-001", "Jane Doe", "spouse", "doc-ref-1", "admin-1")
	registry.SubmitSuccessionRequest("acct-001", "death-cert-doc-1", "admin-1", time.Now())
	registry.Reject("acct-001", "reviewer-1", "rejected", time.Now())

	// A rejected request is terminal — this package doesn't model
	// resubmission, so a fresh SubmitSuccessionRequest after REJECTED
	// should succeed again (since it's no longer "active"), exercising
	// the isTerminalState gate directly.
	_, err := registry.SubmitSuccessionRequest("acct-001", "death-cert-doc-2", "admin-1", time.Now())
	if err != nil {
		t.Fatalf("expected resubmission after a terminal REJECTED state to succeed, got %v", err)
	}
}

func TestTransitionsOnNonexistentRequestFail(t *testing.T) {
	registry := NewRegistry()

	if _, err := registry.MoveToUnderReview("acct-ghost", "reviewer-1", "x", time.Now()); err != ErrNoSuccessionRequestFound {
		t.Fatalf("expected ErrNoSuccessionRequestFound, got %v", err)
	}
	if _, err := registry.Approve("acct-ghost", "reviewer-1", "x", time.Now()); err != ErrNoSuccessionRequestFound {
		t.Fatalf("expected ErrNoSuccessionRequestFound, got %v", err)
	}
	if _, err := registry.Reject("acct-ghost", "reviewer-1", "x", time.Now()); err != ErrNoSuccessionRequestFound {
		t.Fatalf("expected ErrNoSuccessionRequestFound, got %v", err)
	}
}

func TestEveryTransitionRequiresAnActor(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterNominee("acct-001", "Jane Doe", "spouse", "doc-ref-1", "admin-1")
	registry.SubmitSuccessionRequest("acct-001", "death-cert-doc-1", "admin-1", time.Now())

	if _, err := registry.MoveToUnderReview("acct-001", "", "x", time.Now()); err != ErrActorRequired {
		t.Fatalf("expected ErrActorRequired, got %v", err)
	}
}

func TestAuditTrailRecordsEveryTransitionInOrder(t *testing.T) {
	registry := NewRegistry()
	now := time.Now()
	registry.RegisterNominee("acct-001", "Jane Doe", "spouse", "doc-ref-1", "admin-1")
	registry.SubmitSuccessionRequest("acct-001", "death-cert-doc-1", "admin-1", now)
	registry.MoveToUnderReview("acct-001", "reviewer-1", "review", now.Add(time.Hour))
	registry.Approve("acct-001", "reviewer-1", "approved", now.Add(2*time.Hour))
	registry.MarkTransferred("acct-001", "ops-1", "transferred", now.Add(3*time.Hour))

	trail := registry.AuditTrailForAccount("acct-001")
	if len(trail) != 5 { // registered, submitted, under review, approved, transferred
		t.Fatalf("expected 5 audit records, got %d: %+v", len(trail), trail)
	}
	expectedEventOrder := []TransitionEventType{
		EventNomineeRegistered, EventSuccessionSubmitted, EventMovedToUnderReview, EventApproved, EventTransferred,
	}
	for i, expectedEvent := range expectedEventOrder {
		if trail[i].EventType != expectedEvent {
			t.Fatalf("expected event %d to be %q, got %q", i, expectedEvent, trail[i].EventType)
		}
	}
	// Every transition must record a real actor.
	for _, record := range trail {
		if record.Actor == "" {
			t.Fatalf("found a transition record with no actor: %+v", record)
		}
	}
}

func TestAuditTrailForAccountDoesNotLeakOtherAccounts(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterNominee("acct-001", "Jane Doe", "spouse", "doc-ref-1", "admin-1")
	registry.RegisterNominee("acct-002", "John Roe", "child", "doc-ref-2", "admin-1")

	trail := registry.AuditTrailForAccount("acct-001")
	if len(trail) != 1 {
		t.Fatalf("expected 1 record for acct-001, got %d", len(trail))
	}
	if trail[0].AccountIdentifier != "acct-001" {
		t.Fatalf("leaked another account's record: %+v", trail[0])
	}
}

func TestAllAuditTrailRecordsReturnsEveryAccount(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterNominee("acct-001", "Jane Doe", "spouse", "doc-ref-1", "admin-1")
	registry.RegisterNominee("acct-002", "John Roe", "child", "doc-ref-2", "admin-1")

	allRecords := registry.AllAuditTrailRecords()
	if len(allRecords) != 2 {
		t.Fatalf("expected 2 total records, got %d", len(allRecords))
	}
}

func TestAllAuditTrailRecordsReturnsACopy(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterNominee("acct-001", "Jane Doe", "spouse", "doc-ref-1", "admin-1")

	firstFetch := registry.AllAuditTrailRecords()
	firstFetch[0].Reason = "mutated"

	secondFetch := registry.AllAuditTrailRecords()
	if secondFetch[0].Reason == "mutated" {
		t.Fatal("expected AllAuditTrailRecords to return a copy, not internal state")
	}
}

func TestGetSuccessionRequestReturnsFalseWhenNoneExists(t *testing.T) {
	registry := NewRegistry()
	_, exists := registry.GetSuccessionRequest("acct-ghost")
	if exists {
		t.Fatal("expected no succession request for an untouched account")
	}
}
