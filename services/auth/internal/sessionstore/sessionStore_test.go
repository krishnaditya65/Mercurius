package sessionstore

import (
	"testing"
	"time"
)

var testNow = time.Unix(1_700_000_000, 0)

func TestNewSessionFamilyIssuesAUsableRefreshToken(t *testing.T) {
	store := NewSessionStore(time.Hour)

	refreshToken, issueError := store.IssueNewSessionFamily("acct-001", testNow)
	if issueError != nil {
		t.Fatalf("unexpected error issuing session: %v", issueError)
	}
	if refreshToken == "" {
		t.Fatal("expected a non-empty refresh token")
	}
}

func TestRotatingAValidTokenReturnsANewOneForTheSameAccount(t *testing.T) {
	store := NewSessionStore(time.Hour)
	originalToken, _ := store.IssueNewSessionFamily("acct-001", testNow)

	result, rotateError := store.RotateRefreshToken(originalToken, testNow.Add(time.Minute))
	if rotateError != nil {
		t.Fatalf("unexpected error rotating a valid token: %v", rotateError)
	}
	if result.WasReuseDetected {
		t.Fatal("did not expect reuse detection on a fresh, valid rotation")
	}
	if result.AccountIdentifier != "acct-001" {
		t.Fatalf("expected acct-001, got %q", result.AccountIdentifier)
	}
	if result.NewRefreshToken == "" || result.NewRefreshToken == originalToken {
		t.Fatal("expected a distinct new refresh token")
	}
}

func TestRotatingAnAlreadyConsumedTokenIsDetectedAsReuse(t *testing.T) {
	store := NewSessionStore(time.Hour)
	originalToken, _ := store.IssueNewSessionFamily("acct-001", testNow)

	firstRotation, _ := store.RotateRefreshToken(originalToken, testNow.Add(time.Minute))
	if firstRotation.WasReuseDetected {
		t.Fatal("first rotation of a fresh token must not be flagged as reuse")
	}

	// Presenting the ORIGINAL (already-consumed) token again — this is
	// the theft/reuse scenario the whole package exists to catch.
	secondAttempt, secondError := store.RotateRefreshToken(originalToken, testNow.Add(2*time.Minute))
	if secondError != nil {
		t.Fatalf("expected reuse to be reported via the result, not an error: %v", secondError)
	}
	if !secondAttempt.WasReuseDetected {
		t.Fatal("expected reusing an already-consumed token to be detected")
	}
	if secondAttempt.AccountIdentifier != "acct-001" {
		t.Fatalf("expected the reuse result to still identify the account, got %q", secondAttempt.AccountIdentifier)
	}
}

func TestReuseDetectionRevokesTheEntireFamilyNotJustTheReusedToken(t *testing.T) {
	store := NewSessionStore(time.Hour)
	originalToken, _ := store.IssueNewSessionFamily("acct-001", testNow)
	firstRotation, _ := store.RotateRefreshToken(originalToken, testNow.Add(time.Minute))

	// Trigger reuse detection.
	store.RotateRefreshToken(originalToken, testNow.Add(2*time.Minute))

	// The LEGITIMATE current token (from the first rotation) must now
	// ALSO be revoked — reuse detection burns the whole family, since
	// there's no way to tell which of "attacker" or "legitimate client"
	// currently holds it.
	_, rotateError := store.RotateRefreshToken(firstRotation.NewRefreshToken, testNow.Add(3*time.Minute))
	if rotateError != ErrRefreshTokenNotFound {
		t.Fatalf("expected the legitimate token to also be revoked after reuse detection, got err=%v", rotateError)
	}
}

func TestRotatingAnUnknownTokenReturnsNotFound(t *testing.T) {
	store := NewSessionStore(time.Hour)

	_, rotateError := store.RotateRefreshToken("this-token-was-never-issued", testNow)
	if rotateError != ErrRefreshTokenNotFound {
		t.Fatalf("expected ErrRefreshTokenNotFound, got %v", rotateError)
	}
}

func TestExpiredTokenCannotBeRotated(t *testing.T) {
	store := NewSessionStore(time.Minute)
	token, _ := store.IssueNewSessionFamily("acct-001", testNow)

	_, rotateError := store.RotateRefreshToken(token, testNow.Add(2*time.Minute))
	if rotateError != ErrRefreshTokenExpired {
		t.Fatalf("expected ErrRefreshTokenExpired, got %v", rotateError)
	}
}

func TestExplicitLogoutRevokesTheTokenSoItCanNoLongerBeRotated(t *testing.T) {
	store := NewSessionStore(time.Hour)
	token, _ := store.IssueNewSessionFamily("acct-001", testNow)

	revokeError := store.RevokeRefreshToken(token)
	if revokeError != nil {
		t.Fatalf("unexpected error revoking token: %v", revokeError)
	}

	_, rotateError := store.RotateRefreshToken(token, testNow.Add(time.Minute))
	if rotateError != ErrRefreshTokenNotFound {
		t.Fatalf("expected a revoked token to behave as not-found on rotation, got %v", rotateError)
	}
}

func TestRevokingAnUnknownTokenReturnsNotFound(t *testing.T) {
	store := NewSessionStore(time.Hour)
	revokeError := store.RevokeRefreshToken("never-issued")
	if revokeError != ErrRefreshTokenNotFound {
		t.Fatalf("expected ErrRefreshTokenNotFound, got %v", revokeError)
	}
}

func TestTwoIndependentSessionFamiliesForTheSameAccountDoNotInterfere(t *testing.T) {
	store := NewSessionStore(time.Hour)
	tokenFromDeviceOne, _ := store.IssueNewSessionFamily("acct-001", testNow)
	tokenFromDeviceTwo, _ := store.IssueNewSessionFamily("acct-001", testNow)

	// Simulate theft/reuse on device one's family only.
	store.RotateRefreshToken(tokenFromDeviceOne, testNow.Add(time.Minute))
	store.RotateRefreshToken(tokenFromDeviceOne, testNow.Add(2*time.Minute)) // reuse, revokes family one

	// Device two's independent session must be entirely unaffected.
	result, rotateError := store.RotateRefreshToken(tokenFromDeviceTwo, testNow.Add(time.Minute))
	if rotateError != nil {
		t.Fatalf("expected device two's session to be unaffected by device one's reuse, got err=%v", rotateError)
	}
	if result.WasReuseDetected {
		t.Fatal("device two's rotation must not be affected by device one's family revocation")
	}
}
