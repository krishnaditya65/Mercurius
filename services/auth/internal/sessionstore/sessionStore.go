// Package sessionstore manages refresh tokens with rotation and reuse
// detection (FEATURES.md §1: "session management, JWT + refresh token
// rotation"). Access tokens (jwtauth) are short-lived and stateless —
// this package is what makes the LONGER-lived refresh flow safe:
//
//   - Every refresh token is a random opaque string (NOT a JWT — no
//     reason to make it self-describing, since only this service ever
//     needs to look it up) tied to a "family" id shared by every token
//     ever issued in one continuous login session.
//   - Refreshing consumes (revokes) the presented token and issues a
//     brand new one in the SAME family — this is "rotation."
//   - If an already-consumed token is presented again, that's a signal
//     the token was stolen and used out of order (the legitimate client
//     would never present a token it already exchanged) — the ENTIRE
//     family is revoked, logging out every session descended from that
//     login, not just the one compromised token. This is the standard
//     refresh-token-rotation-with-reuse-detection pattern real auth
//     systems (Auth0, Okta, etc.) use.
package sessionstore

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sync"
	"time"
)

const refreshTokenByteLength = 32

type refreshTokenState int

const (
	refreshTokenStateActive refreshTokenState = iota
	refreshTokenStateConsumed
	refreshTokenStateRevoked
)

type refreshTokenRecord struct {
	accountIdentifier string
	familyIdentifier  string
	state             refreshTokenState
	expiresAt         time.Time
}

// SessionStore is safe for concurrent use.
type SessionStore struct {
	mutexGuardingRecords sync.Mutex
	recordsByToken       map[string]*refreshTokenRecord
	refreshTokenLifetime time.Duration
}

func NewSessionStore(refreshTokenLifetime time.Duration) *SessionStore {
	return &SessionStore{
		recordsByToken:       make(map[string]*refreshTokenRecord),
		refreshTokenLifetime: refreshTokenLifetime,
	}
}

// IssueNewSessionFamily starts a brand new login session (e.g. right
// after a successful password check) and returns the first refresh token
// in a fresh family.
func (store *SessionStore) IssueNewSessionFamily(accountIdentifier string, issuedAt time.Time) (refreshToken string, err error) {
	familyIdentifier, familyGenError := generateRandomToken()
	if familyGenError != nil {
		return "", fmt.Errorf("failed to generate session family id: %w", familyGenError)
	}
	return store.issueTokenInFamily(accountIdentifier, familyIdentifier, issuedAt)
}

// RotationResult is what a caller gets back from RotateRefreshToken.
type RotationResult struct {
	AccountIdentifier string
	NewRefreshToken   string
	// WasReuseDetected is true when presentedRefreshToken had already
	// been consumed by an earlier rotation — the entire family has now
	// been revoked, and the caller MUST treat this as "log the user out
	// of every session," not just reject this one request.
	WasReuseDetected bool
}

var ErrRefreshTokenNotFound = fmt.Errorf("refresh token not found or already revoked")
var ErrRefreshTokenExpired = fmt.Errorf("refresh token has expired")

// RotateRefreshToken consumes presentedRefreshToken and, if it was a
// valid, still-active token, issues a new one in the same family.
//
// If presentedRefreshToken was already consumed by a PRIOR rotation
// (reuse), this revokes every token in that family and returns
// (RotationResult{WasReuseDetected: true}, nil) rather than an error —
// deliberately not an error, since the caller needs the account
// identifier and reuse flag to actually act on it (e.g. force-logout,
// alert), not just a rejection.
func (store *SessionStore) RotateRefreshToken(presentedRefreshToken string, now time.Time) (RotationResult, error) {
	store.mutexGuardingRecords.Lock()
	record, wasFound := store.recordsByToken[presentedRefreshToken]
	if !wasFound {
		store.mutexGuardingRecords.Unlock()
		return RotationResult{}, ErrRefreshTokenNotFound
	}

	if record.state == refreshTokenStateRevoked {
		store.mutexGuardingRecords.Unlock()
		return RotationResult{}, ErrRefreshTokenNotFound
	}

	if record.state == refreshTokenStateConsumed {
		// Reuse detected — revoke the whole family before releasing the
		// lock, so no other goroutine can race a legitimate-looking
		// rotation through in between.
		accountIdentifier := record.accountIdentifier
		familyIdentifier := record.familyIdentifier
		store.revokeFamilyLocked(familyIdentifier)
		store.mutexGuardingRecords.Unlock()
		return RotationResult{AccountIdentifier: accountIdentifier, WasReuseDetected: true}, nil
	}

	if now.After(record.expiresAt) {
		store.mutexGuardingRecords.Unlock()
		return RotationResult{}, ErrRefreshTokenExpired
	}

	// Valid, active, unexpired — consume it and mint the replacement in
	// the same family.
	record.state = refreshTokenStateConsumed
	accountIdentifier := record.accountIdentifier
	familyIdentifier := record.familyIdentifier
	store.mutexGuardingRecords.Unlock()

	newRefreshToken, issueError := store.issueTokenInFamily(accountIdentifier, familyIdentifier, now)
	if issueError != nil {
		return RotationResult{}, issueError
	}

	return RotationResult{AccountIdentifier: accountIdentifier, NewRefreshToken: newRefreshToken}, nil
}

// RevokeRefreshToken is used for an explicit logout — revokes just this
// one token's family (a logout ends the whole session, not just this one
// token, since a client only ever holds the current token in the family
// anyway).
func (store *SessionStore) RevokeRefreshToken(refreshToken string) error {
	store.mutexGuardingRecords.Lock()
	defer store.mutexGuardingRecords.Unlock()

	record, wasFound := store.recordsByToken[refreshToken]
	if !wasFound {
		return ErrRefreshTokenNotFound
	}
	store.revokeFamilyLocked(record.familyIdentifier)
	return nil
}

// revokeFamilyLocked must be called with mutexGuardingRecords already
// held.
func (store *SessionStore) revokeFamilyLocked(familyIdentifier string) {
	for _, record := range store.recordsByToken {
		if record.familyIdentifier == familyIdentifier {
			record.state = refreshTokenStateRevoked
		}
	}
}

func (store *SessionStore) issueTokenInFamily(accountIdentifier string, familyIdentifier string, issuedAt time.Time) (string, error) {
	newToken, genError := generateRandomToken()
	if genError != nil {
		return "", fmt.Errorf("failed to generate refresh token: %w", genError)
	}

	store.mutexGuardingRecords.Lock()
	store.recordsByToken[newToken] = &refreshTokenRecord{
		accountIdentifier: accountIdentifier,
		familyIdentifier:  familyIdentifier,
		state:             refreshTokenStateActive,
		expiresAt:         issuedAt.Add(store.refreshTokenLifetime),
	}
	store.mutexGuardingRecords.Unlock()

	return newToken, nil
}

func generateRandomToken() (string, error) {
	randomBytes := make([]byte, refreshTokenByteLength)
	if _, readError := rand.Read(randomBytes); readError != nil {
		return "", readError
	}
	return base64.RawURLEncoding.EncodeToString(randomBytes), nil
}
