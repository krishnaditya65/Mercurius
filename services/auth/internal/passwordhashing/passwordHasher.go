// Package passwordhashing hashes and verifies account passwords using
// PBKDF2-HMAC-SHA256 (crypto/pbkdf2 + crypto/sha256, both Go stdlib as of
// Go 1.24 — no external dependency needed, matching this repo's
// "stdlib-only until there's a real reason" convention elsewhere). A
// real build would likely prefer Argon2id (memory-hard, better resists
// GPU/ASIC cracking) — that's still not in the Go stdlib as of this
// build (it lives in golang.org/x/crypto), so PBKDF2 is the pragmatic
// stdlib-only choice here, documented as a deliberate tradeoff, not an
// oversight.
package passwordhashing

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// pbkdf2IterationCount is on the low end of current OWASP guidance
// (600,000+ for PBKDF2-HMAC-SHA256) — kept low here so this skeleton's
// tests and live demo requests stay fast, not because it's the
// recommended production value. A real build should raise this and
// tune it against real hardware, not hardcode a demo-friendly number.
const pbkdf2IterationCount = 100_000
const saltByteLength = 16
const derivedKeyByteLength = 32

// HashPassword returns an encoded string safe to store — the algorithm
// tag, iteration count, and a fresh random salt are embedded in the
// output itself (`pbkdf2-sha256$<iterations>$<saltBase64>$<hashBase64>`),
// so VerifyPassword never needs the caller to separately track how a
// given password was hashed. This is the same self-describing-format
// convention most real password-hashing libraries use (e.g. PHC string
// format), simplified for this skeleton.
func HashPassword(plaintextPassword string) (string, error) {
	saltBytes := make([]byte, saltByteLength)
	if _, readError := rand.Read(saltBytes); readError != nil {
		return "", fmt.Errorf("failed to generate a random salt: %w", readError)
	}

	derivedKey, derivationError := pbkdf2.Key(sha256.New, plaintextPassword, saltBytes, pbkdf2IterationCount, derivedKeyByteLength)
	if derivationError != nil {
		return "", fmt.Errorf("failed to derive password hash: %w", derivationError)
	}

	return fmt.Sprintf(
		"pbkdf2-sha256$%d$%s$%s",
		pbkdf2IterationCount,
		base64.RawURLEncoding.EncodeToString(saltBytes),
		base64.RawURLEncoding.EncodeToString(derivedKey),
	), nil
}

// VerifyPassword re-derives the hash from the candidate plaintext using
// the salt/iteration count embedded in encodedHash and compares it in
// constant time (crypto/subtle.ConstantTimeCompare) — a plain `==` byte
// comparison would leak timing information about how many leading bytes
// matched, which is exactly what an attacker doing an offline guessing
// attack against a leaked hash (not this function, but the PRINCIPLE
// still matters here for defense in depth) could exploit.
func VerifyPassword(candidatePlaintextPassword string, encodedHash string) (bool, error) {
	hashParts := strings.Split(encodedHash, "$")
	if len(hashParts) != 4 || hashParts[0] != "pbkdf2-sha256" {
		return false, fmt.Errorf("unrecognized password hash format")
	}

	iterationCount, parseError := strconv.Atoi(hashParts[1])
	if parseError != nil {
		return false, fmt.Errorf("malformed iteration count in stored hash: %w", parseError)
	}

	saltBytes, saltDecodeError := base64.RawURLEncoding.DecodeString(hashParts[2])
	if saltDecodeError != nil {
		return false, fmt.Errorf("malformed salt in stored hash: %w", saltDecodeError)
	}

	storedDerivedKey, keyDecodeError := base64.RawURLEncoding.DecodeString(hashParts[3])
	if keyDecodeError != nil {
		return false, fmt.Errorf("malformed derived key in stored hash: %w", keyDecodeError)
	}

	candidateDerivedKey, derivationError := pbkdf2.Key(sha256.New, candidatePlaintextPassword, saltBytes, iterationCount, len(storedDerivedKey))
	if derivationError != nil {
		return false, fmt.Errorf("failed to derive candidate password hash: %w", derivationError)
	}

	return subtle.ConstantTimeCompare(storedDerivedKey, candidateDerivedKey) == 1, nil
}
