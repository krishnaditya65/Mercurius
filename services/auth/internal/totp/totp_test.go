package totp

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"
)

// rfc6238TestSecretBase32 is the base32 encoding of RFC 6238 Appendix
// B's standard 20-byte ASCII test seed ("12345678901234567890"),
// computed here (not hand-derived) so the encode/decode round-trip is
// exactly what GenerateCode/VerifyCode actually do internally.
var rfc6238TestSecretBase32 = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))

// TestGeneratedCodeMatchesKnownRfc6238TestVectors cross-checks this
// implementation against RFC 6238 Appendix B's published test vectors
// (originally 8-digit codes; the last 6 digits of an 8-digit HOTP/TOTP
// value are identical to a 6-digit computation of the SAME counter,
// since truncation is just `value mod 10^digitCount` before
// zero-padding — a stronger correctness check than a pure round-trip
// test against this package's own output.
func TestGeneratedCodeMatchesKnownRfc6238TestVectors(t *testing.T) {
	testCases := []struct {
		unixTime     int64
		expectedCode string // last 6 digits of the RFC's published 8-digit vector
	}{
		{unixTime: 59, expectedCode: "287082"},
		{unixTime: 1111111109, expectedCode: "081804"},
		{unixTime: 1111111111, expectedCode: "050471"},
		{unixTime: 1234567890, expectedCode: "005924"},
	}

	for _, testCase := range testCases {
		actualCode, genError := GenerateCode(rfc6238TestSecretBase32, time.Unix(testCase.unixTime, 0).UTC())
		if genError != nil {
			t.Fatalf("unexpected error generating code for unixTime=%d: %v", testCase.unixTime, genError)
		}
		if actualCode != testCase.expectedCode {
			t.Fatalf("for unixTime=%d: expected %q, got %q", testCase.unixTime, testCase.expectedCode, actualCode)
		}
	}
}

func TestGeneratedCodeIsSixDigits(t *testing.T) {
	secret, _ := GenerateRandomSecret()
	code, genError := GenerateCode(secret, time.Now())
	if genError != nil {
		t.Fatalf("unexpected error: %v", genError)
	}
	if len(code) != 6 {
		t.Fatalf("expected a 6-digit code, got %q (length %d)", code, len(code))
	}
}

func TestVerifyCodeAcceptsTheCurrentCode(t *testing.T) {
	secret, _ := GenerateRandomSecret()
	now := time.Now()
	code, _ := GenerateCode(secret, now)

	isValid, verifyError := VerifyCode(secret, code, now, 1)
	if verifyError != nil {
		t.Fatalf("unexpected error: %v", verifyError)
	}
	if !isValid {
		t.Fatal("expected the code generated for `now` to verify against `now`")
	}
}

func TestVerifyCodeRejectsAWrongCode(t *testing.T) {
	secret, _ := GenerateRandomSecret()
	now := time.Now()

	isValid, _ := VerifyCode(secret, "000000", now, 1)
	// Astronomically unlikely for a random secret to actually produce
	// "000000" right now, but guard against test flakiness rather than
	// assert False outright.
	if isValid {
		actualCode, _ := GenerateCode(secret, now)
		if actualCode == "000000" {
			t.Skip("the random secret happened to generate 000000 — vanishingly rare, skip rather than flake")
		}
		t.Fatal("expected a wrong code to be rejected")
	}
}

func TestVerifyCodeToleratesOneStepOfClockSkew(t *testing.T) {
	secret, _ := GenerateRandomSecret()
	now := time.Unix(1_700_000_000, 0)
	codeFromOneStepAgo, _ := GenerateCode(secret, now.Add(-30*time.Second))

	isValid, _ := VerifyCode(secret, codeFromOneStepAgo, now, 1)
	if !isValid {
		t.Fatal("expected a code from exactly one 30s step ago to verify within an allowedSkewSteps of 1")
	}
}

func TestVerifyCodeRejectsCodeOutsideTheAllowedSkewWindow(t *testing.T) {
	secret, _ := GenerateRandomSecret()
	now := time.Unix(1_700_000_000, 0)
	codeFromThreeStepsAgo, _ := GenerateCode(secret, now.Add(-3*30*time.Second))

	isValid, _ := VerifyCode(secret, codeFromThreeStepsAgo, now, 1)
	if isValid {
		t.Fatal("expected a code from 3 steps ago to be rejected with an allowedSkewSteps of 1")
	}
}

func TestTwoRandomSecretsAreDifferent(t *testing.T) {
	firstSecret, _ := GenerateRandomSecret()
	secondSecret, _ := GenerateRandomSecret()
	if firstSecret == secondSecret {
		t.Fatal("expected two independently generated secrets to differ")
	}
}

func TestBuildOtpAuthUriIncludesTheSecretAndStandardParameters(t *testing.T) {
	uri := BuildOtpAuthUri("JBSWY3DPEHPK3PXP", "jane@example.com", "Mercurius")

	for _, expectedFragment := range []string{
		"otpauth://totp/",
		"secret=JBSWY3DPEHPK3PXP",
		"issuer=Mercurius",
		"algorithm=SHA1",
		"digits=6",
		"period=30",
	} {
		if !strings.Contains(uri, expectedFragment) {
			t.Fatalf("expected otpauth URI to contain %q, got: %s", expectedFragment, uri)
		}
	}
}
