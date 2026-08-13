// Package totp implements Time-based One-Time Passwords per RFC 6238
// (built on HOTP, RFC 4226) — the algorithm behind every standard
// authenticator app (Google Authenticator, Authy, 1Password, etc.).
// FEATURES.md §1: "MFA (TOTP + SMS fallback)...". This package covers
// the TOTP half only — no SMS fallback (see mfastate's package doc for
// the fuller scope this doesn't cover).
//
// Stdlib only (crypto/hmac, crypto/sha1, encoding/base32) — same
// "hand-roll it, no framework dependency" convention as everything else
// in this repo. SHA-1 is used deliberately, not because it's considered
// strong in general, but because it's what RFC 6238 specifies and what
// every real authenticator app expects by default — using SHA-256/512
// here would make this incompatible with real TOTP apps for no security
// benefit (HMAC-SHA1's weaknesses as a hash don't meaningfully weaken
// HMAC's security as a MAC, which is the only thing this algorithm
// depends on SHA-1 for).
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // required by RFC 6238; see package doc
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const timeStepDuration = 30 * time.Second
const codeDigitCount = 6
const secretByteLength = 20 // 160 bits — RFC 4226's recommended HMAC-SHA1 key length

// GenerateRandomSecret returns a fresh, random base32-encoded secret
// (no padding, uppercase — the conventional format authenticator apps
// expect when a user types it in manually).
func GenerateRandomSecret() (string, error) {
	secretBytes := make([]byte, secretByteLength)
	if _, readError := rand.Read(secretBytes); readError != nil {
		return "", fmt.Errorf("failed to generate TOTP secret: %w", readError)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secretBytes), nil
}

// GenerateCode computes the 6-digit TOTP for secretBase32 at atTime.
func GenerateCode(secretBase32 string, atTime time.Time) (string, error) {
	secretBytes, decodeError := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secretBase32))
	if decodeError != nil {
		return "", fmt.Errorf("failed to decode TOTP secret: %w", decodeError)
	}

	counter := uint64(atTime.Unix()) / uint64(timeStepDuration.Seconds())
	return computeHotpCode(secretBytes, counter), nil
}

// VerifyCode checks candidateCode against secretBase32, allowing the
// current time step and up to allowedSkewSteps steps before/after it —
// real clocks (the user's phone, this server) are never perfectly
// synchronized, so a real TOTP verifier always tolerates a small amount
// of drift. `allowedSkewSteps: 1` (the caller's typical choice) means a
// code is valid for up to ~90 seconds around when it was generated (the
// step it was issued in, plus one step either side), not just its exact
// 30-second window.
func VerifyCode(secretBase32 string, candidateCode string, atTime time.Time, allowedSkewSteps int) (bool, error) {
	secretBytes, decodeError := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secretBase32))
	if decodeError != nil {
		return false, fmt.Errorf("failed to decode TOTP secret: %w", decodeError)
	}

	currentCounter := int64(uint64(atTime.Unix()) / uint64(timeStepDuration.Seconds()))
	for skew := -allowedSkewSteps; skew <= allowedSkewSteps; skew++ {
		counterAtSkew := currentCounter + int64(skew)
		if counterAtSkew < 0 {
			continue
		}
		if computeHotpCode(secretBytes, uint64(counterAtSkew)) == candidateCode {
			return true, nil
		}
	}
	return false, nil
}

// BuildOtpAuthUri builds the standard `otpauth://totp/...` URI that
// authenticator apps consume (typically rendered as a QR code by the
// caller — this package doesn't generate the QR image itself, just the
// URI it encodes).
func BuildOtpAuthUri(secretBase32 string, accountLabel string, issuerName string) string {
	label := url.PathEscape(fmt.Sprintf("%s:%s", issuerName, accountLabel))
	queryValues := url.Values{}
	queryValues.Set("secret", secretBase32)
	queryValues.Set("issuer", issuerName)
	queryValues.Set("algorithm", "SHA1")
	queryValues.Set("digits", strconv.Itoa(codeDigitCount))
	queryValues.Set("period", strconv.Itoa(int(timeStepDuration.Seconds())))
	return fmt.Sprintf("otpauth://totp/%s?%s", label, queryValues.Encode())
}

// computeHotpCode is the RFC 4226 HOTP algorithm: HMAC-SHA1 the 8-byte
// big-endian counter, dynamically truncate per the RFC's specified bit
// manipulation, then reduce mod 10^codeDigitCount and zero-pad.
func computeHotpCode(secretBytes []byte, counter uint64) string {
	counterBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(counterBytes, counter)

	hmacHasher := hmac.New(sha1.New, secretBytes)
	hmacHasher.Write(counterBytes)
	hmacSum := hmacHasher.Sum(nil)

	// RFC 4226 §5.3 dynamic truncation.
	offset := hmacSum[len(hmacSum)-1] & 0x0F
	truncatedValue := (uint32(hmacSum[offset])&0x7F)<<24 |
		(uint32(hmacSum[offset+1])&0xFF)<<16 |
		(uint32(hmacSum[offset+2])&0xFF)<<8 |
		(uint32(hmacSum[offset+3]) & 0xFF)

	modulus := uint32(1)
	for i := 0; i < codeDigitCount; i++ {
		modulus *= 10
	}

	return fmt.Sprintf("%0*d", codeDigitCount, truncatedValue%modulus)
}
