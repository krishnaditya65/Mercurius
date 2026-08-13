package kycstate

import "testing"

func TestValidPanFormatIsVerified(t *testing.T) {
	stateMachineUnderTest := NewKycVerificationStateMachine()

	record := stateMachineUnderTest.SubmitKycDetails("acct-001", "ABCDE1234F", "Jane Trader")

	if record.VerificationStage != StageVerified {
		t.Fatalf("expected VERIFIED, got %s (reason: %s)", record.VerificationStage, record.RejectionReason)
	}
	if !record.IsEligibleToPlaceOrders() {
		t.Fatal("a VERIFIED record must be eligible to place orders")
	}
}

func TestMalformedPanIsRejectedWithAReason(t *testing.T) {
	stateMachineUnderTest := NewKycVerificationStateMachine()

	record := stateMachineUnderTest.SubmitKycDetails("acct-001", "not-a-pan", "Jane Trader")

	if record.VerificationStage != StageRejected {
		t.Fatalf("expected REJECTED, got %s", record.VerificationStage)
	}
	if record.RejectionReason == "" {
		t.Fatal("expected a non-empty rejection reason")
	}
	if record.IsEligibleToPlaceOrders() {
		t.Fatal("a REJECTED record must not be eligible to place orders")
	}
}

func TestMissingFullNameIsRejected(t *testing.T) {
	stateMachineUnderTest := NewKycVerificationStateMachine()

	record := stateMachineUnderTest.SubmitKycDetails("acct-001", "ABCDE1234F", "")

	if record.VerificationStage != StageRejected {
		t.Fatalf("expected REJECTED, got %s", record.VerificationStage)
	}
}

func TestLookupForAccountThatNeverSubmittedReturnsNotSubmitted(t *testing.T) {
	stateMachineUnderTest := NewKycVerificationStateMachine()

	record := stateMachineUnderTest.LookupKycStatus("acct-never-submitted")

	if record.VerificationStage != StageNotSubmitted {
		t.Fatalf("expected NOT_SUBMITTED, got %s", record.VerificationStage)
	}
	if record.IsEligibleToPlaceOrders() {
		t.Fatal("an account that never submitted KYC must not be eligible to place orders")
	}
}

func TestLookupAfterSubmitReturnsTheStoredRecord(t *testing.T) {
	stateMachineUnderTest := NewKycVerificationStateMachine()

	stateMachineUnderTest.SubmitKycDetails("acct-001", "ABCDE1234F", "Jane Trader")
	record := stateMachineUnderTest.LookupKycStatus("acct-001")

	if record.VerificationStage != StageVerified {
		t.Fatalf("expected VERIFIED on lookup after submit, got %s", record.VerificationStage)
	}
	if record.SubmittedFullName != "Jane Trader" {
		t.Fatalf("expected stored full name to round-trip, got %q", record.SubmittedFullName)
	}
}

func TestListRecordsByStageReturnsOnlyMatchingRecordsSortedByAccountId(t *testing.T) {
	stateMachineUnderTest := NewKycVerificationStateMachine()
	stateMachineUnderTest.SubmitKycDetails("acct-002", "not-a-pan", "Jane Trader")      // rejected
	stateMachineUnderTest.SubmitKycDetails("acct-001", "still-not-a-pan", "Bob Trader") // rejected
	stateMachineUnderTest.SubmitKycDetails("acct-003", "ABCDE1234F", "Sam Trader")      // verified

	rejectedRecords := stateMachineUnderTest.ListRecordsByStage(StageRejected)
	if len(rejectedRecords) != 2 {
		t.Fatalf("expected 2 rejected records, got %d", len(rejectedRecords))
	}
	if rejectedRecords[0].AccountIdentifier != "acct-001" || rejectedRecords[1].AccountIdentifier != "acct-002" {
		t.Fatalf("expected sorted order acct-001, acct-002, got %s, %s", rejectedRecords[0].AccountIdentifier, rejectedRecords[1].AccountIdentifier)
	}

	verifiedRecords := stateMachineUnderTest.ListRecordsByStage(StageVerified)
	if len(verifiedRecords) != 1 || verifiedRecords[0].AccountIdentifier != "acct-003" {
		t.Fatalf("expected exactly acct-003 in the verified list, got %+v", verifiedRecords)
	}
}

func TestListRecordsByStageReturnsEmptyNotNilForNoMatches(t *testing.T) {
	stateMachineUnderTest := NewKycVerificationStateMachine()
	records := stateMachineUnderTest.ListRecordsByStage(StageRejected)
	if records == nil {
		t.Fatal("expected an empty slice, not nil, so JSON serialization yields [] not null")
	}
	if len(records) != 0 {
		t.Fatalf("expected 0 records, got %d", len(records))
	}
}

func TestOverrideStageCanOverturnAnAutomatedRejection(t *testing.T) {
	stateMachineUnderTest := NewKycVerificationStateMachine()
	stateMachineUnderTest.SubmitKycDetails("acct-001", "not-a-pan", "Jane Trader")

	overriddenRecord, err := stateMachineUnderTest.OverrideStage("acct-001", StageVerified, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if overriddenRecord.VerificationStage != StageVerified {
		t.Fatalf("expected VERIFIED after override, got %s", overriddenRecord.VerificationStage)
	}
	if overriddenRecord.RejectionReason != "" {
		t.Fatalf("expected the rejection reason to be cleared on override to VERIFIED, got %q", overriddenRecord.RejectionReason)
	}
	if !stateMachineUnderTest.LookupKycStatus("acct-001").IsEligibleToPlaceOrders() {
		t.Fatal("expected the account to be eligible to place orders after being overridden to VERIFIED")
	}
}

func TestOverrideStageCanRetroactivelyRejectAVerifiedAccount(t *testing.T) {
	stateMachineUnderTest := NewKycVerificationStateMachine()
	stateMachineUnderTest.SubmitKycDetails("acct-001", "ABCDE1234F", "Jane Trader")

	overriddenRecord, err := stateMachineUnderTest.OverrideStage("acct-001", StageRejected, "discovered a mismatch on manual review")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if overriddenRecord.VerificationStage != StageRejected {
		t.Fatalf("expected REJECTED after override, got %s", overriddenRecord.VerificationStage)
	}
	if overriddenRecord.RejectionReason != "discovered a mismatch on manual review" {
		t.Fatalf("expected the override reason to be stored, got %q", overriddenRecord.RejectionReason)
	}
}

func TestOverrideStageFailsForAnAccountThatNeverSubmitted(t *testing.T) {
	stateMachineUnderTest := NewKycVerificationStateMachine()
	_, err := stateMachineUnderTest.OverrideStage("never-submitted", StageVerified, "")
	if err != ErrNoRecordToOverride {
		t.Fatalf("expected ErrNoRecordToOverride, got %v", err)
	}
}

func TestOverrideStageRejectsAnInvalidTargetStage(t *testing.T) {
	stateMachineUnderTest := NewKycVerificationStateMachine()
	stateMachineUnderTest.SubmitKycDetails("acct-001", "ABCDE1234F", "Jane Trader")

	_, err := stateMachineUnderTest.OverrideStage("acct-001", StageNotSubmitted, "")
	if err != ErrInvalidOverrideStage {
		t.Fatalf("expected ErrInvalidOverrideStage, got %v", err)
	}
}
