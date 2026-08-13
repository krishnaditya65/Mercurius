package jwtauth

import (
	"strings"
	"testing"
	"time"
)

var testSigningSecret = []byte("test-signing-secret-do-not-use-in-production")

func TestIssuedTokenHasThreeDotSeparatedSegments(t *testing.T) {
	token, issueError := IssueAccessToken("acct-001", testSigningSecret, time.Hour, time.Unix(1_700_000_000, 0))
	if issueError != nil {
		t.Fatalf("unexpected error issuing token: %v", issueError)
	}
	if len(strings.Split(token, ".")) != 3 {
		t.Fatalf("expected a 3-segment JWT, got %q", token)
	}
}

func TestValidTokenParsesBackToTheOriginalSubject(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	token, _ := IssueAccessToken("acct-001", testSigningSecret, time.Hour, issuedAt)

	claims, parseError := ParseAndVerifyAccessToken(token, testSigningSecret, issuedAt.Add(time.Minute))
	if parseError != nil {
		t.Fatalf("unexpected error parsing a valid token: %v", parseError)
	}
	if claims.Subject != "acct-001" {
		t.Fatalf("expected subject acct-001, got %q", claims.Subject)
	}
}

func TestExpiredTokenIsRejected(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	token, _ := IssueAccessToken("acct-001", testSigningSecret, time.Minute, issuedAt)

	_, parseError := ParseAndVerifyAccessToken(token, testSigningSecret, issuedAt.Add(2*time.Minute))
	if parseError != ErrTokenExpired {
		t.Fatalf("expected ErrTokenExpired, got %v", parseError)
	}
}

func TestTokenAtExactExpiryInstantIsRejected(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	token, _ := IssueAccessToken("acct-001", testSigningSecret, time.Minute, issuedAt)

	_, parseError := ParseAndVerifyAccessToken(token, testSigningSecret, issuedAt.Add(time.Minute))
	if parseError != ErrTokenExpired {
		t.Fatalf("expected a token to be treated as expired at its exact exp instant, got %v", parseError)
	}
}

func TestTokenSignedWithADifferentSecretIsRejected(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	token, _ := IssueAccessToken("acct-001", testSigningSecret, time.Hour, issuedAt)

	_, parseError := ParseAndVerifyAccessToken(token, []byte("a-completely-different-secret"), issuedAt)
	if parseError != ErrTokenSignatureInvalid {
		t.Fatalf("expected ErrTokenSignatureInvalid, got %v", parseError)
	}
}

func TestTamperedClaimsSegmentIsRejected(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	token, _ := IssueAccessToken("acct-001", testSigningSecret, time.Hour, issuedAt)

	segments := strings.Split(token, ".")
	// Swap in a claims segment that decodes to valid JSON for a DIFFERENT
	// account, without recomputing the signature — simulates an attacker
	// trying to escalate to another account's identity.
	forgedClaimsToken, _ := IssueAccessToken("acct-002-attacker", testSigningSecret, time.Hour, issuedAt)
	forgedSegments := strings.Split(forgedClaimsToken, ".")
	tamperedToken := segments[0] + "." + forgedSegments[1] + "." + segments[2]

	_, parseError := ParseAndVerifyAccessToken(tamperedToken, testSigningSecret, issuedAt)
	if parseError != ErrTokenSignatureInvalid {
		t.Fatalf("expected tampering with the claims segment to invalidate the signature, got %v", parseError)
	}
}

func TestMalformedTokenIsRejectedRatherThanPanicking(t *testing.T) {
	for _, malformedToken := range []string{"", "not-a-jwt", "only.two-segments", "a.b.c.d"} {
		_, parseError := ParseAndVerifyAccessToken(malformedToken, testSigningSecret, time.Now())
		if parseError == nil {
			t.Fatalf("expected an error for malformed token %q", malformedToken)
		}
	}
}
