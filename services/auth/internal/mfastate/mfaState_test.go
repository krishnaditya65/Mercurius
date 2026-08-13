package mfastate

import (
	"testing"
	"time"

	"mercurius/auth/internal/totp"
)

func TestMfaIsNotEnabledBeforeAnyEnrollment(t *testing.T) {
	state := NewMfaState()
	if state.IsMfaEnabled("acct-001") {
		t.Fatal("expected MFA to be disabled before any enrollment")
	}
}

func TestBeginEnrollmentDoesNotEnableMfaUntilConfirmed(t *testing.T) {
	state := NewMfaState()
	_, err := state.BeginEnrollment("acct-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.IsMfaEnabled("acct-001") {
		t.Fatal("expected MFA to remain disabled until ConfirmEnrollment succeeds")
	}
}

func TestConfirmingWithTheCorrectCodeEnablesMfa(t *testing.T) {
	state := NewMfaState()
	secret, _ := state.BeginEnrollment("acct-001")
	now := time.Now()
	code, _ := totp.GenerateCode(secret, now)

	wasConfirmed, err := state.ConfirmEnrollment("acct-001", code, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !wasConfirmed {
		t.Fatal("expected confirmation with the correct code to succeed")
	}
	if !state.IsMfaEnabled("acct-001") {
		t.Fatal("expected MFA to be enabled after successful confirmation")
	}
}

func TestConfirmingWithAWrongCodeDoesNotEnableMfa(t *testing.T) {
	state := NewMfaState()
	state.BeginEnrollment("acct-001")

	wasConfirmed, err := state.ConfirmEnrollment("acct-001", "000000", time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wasConfirmed {
		t.Fatal("expected confirmation with a wrong code to fail")
	}
	if state.IsMfaEnabled("acct-001") {
		t.Fatal("expected MFA to remain disabled after a failed confirmation")
	}
}

func TestConfirmingWithNoEnrollmentInProgressFails(t *testing.T) {
	state := NewMfaState()
	wasConfirmed, err := state.ConfirmEnrollment("never-enrolled", "123456", time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wasConfirmed {
		t.Fatal("expected confirmation with no prior enrollment to fail")
	}
}

func TestVerifyLoginCodeAcceptsAValidCodeOnceEnabled(t *testing.T) {
	state := NewMfaState()
	secret, _ := state.BeginEnrollment("acct-001")
	now := time.Now()
	enrollmentCode, _ := totp.GenerateCode(secret, now)
	state.ConfirmEnrollment("acct-001", enrollmentCode, now)

	loginCode, _ := totp.GenerateCode(secret, now)
	isValid, err := state.VerifyLoginCode("acct-001", loginCode, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isValid {
		t.Fatal("expected a valid TOTP code to verify at login")
	}
}

func TestVerifyLoginCodeRejectsWhenMfaIsNotEnabled(t *testing.T) {
	state := NewMfaState()
	// Never enrolled at all.
	isValid, err := state.VerifyLoginCode("acct-001", "123456", time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isValid {
		t.Fatal("expected VerifyLoginCode to reject when the account has no enabled MFA")
	}
}

func TestDisableMfaRemovesTheEnrollmentEntirely(t *testing.T) {
	state := NewMfaState()
	secret, _ := state.BeginEnrollment("acct-001")
	now := time.Now()
	code, _ := totp.GenerateCode(secret, now)
	state.ConfirmEnrollment("acct-001", code, now)

	state.DisableMfa("acct-001")

	if state.IsMfaEnabled("acct-001") {
		t.Fatal("expected MFA to be disabled after DisableMfa")
	}
	// The old code must no longer work even if somehow replayed.
	isValid, _ := state.VerifyLoginCode("acct-001", code, now)
	if isValid {
		t.Fatal("expected the old secret to no longer verify after DisableMfa")
	}
}

func TestTwoAccountsHaveIndependentMfaState(t *testing.T) {
	state := NewMfaState()
	secretOne, _ := state.BeginEnrollment("acct-001")
	now := time.Now()
	codeOne, _ := totp.GenerateCode(secretOne, now)
	state.ConfirmEnrollment("acct-001", codeOne, now)

	if state.IsMfaEnabled("acct-002") {
		t.Fatal("expected acct-002 to be unaffected by acct-001's enrollment")
	}
}
