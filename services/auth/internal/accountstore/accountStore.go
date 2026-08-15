// Package accountstore is the identity registry this skeleton's
// register/login flow uses: email -> {accountIdentifier, password hash,
// role}. Deliberately separate from every other service's notion of an
// "account" (ledger's balance account, oms-gateway's demo-seeded trading
// account, kyc-onboarding's KYC record) — a real build would need these
// to converge on one account identifier space. This build closes part of
// that gap for the two seeded demo accounts specifically: RegisterAccount
// now accepts an OPTIONAL caller-supplied account identifier so
// cmd/server/main.go can seed acct-001/acct-002 (the same identifiers
// oms-gateway/ledger already use) instead of minting disconnected
// acct-<hex> ids for them — see the package doc TODO below for what's
// still not unified.
package accountstore

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"mercurius/auth/internal/jwtauth"
	"mercurius/auth/internal/passwordhashing"
)

var ErrEmailAlreadyRegistered = errors.New("an account with this email is already registered")
var ErrInvalidCredentials = errors.New("invalid email or password")
var ErrAccountIdentifierAlreadyExists = errors.New("an account with this identifier already exists")

type registeredAccount struct {
	accountIdentifier string
	normalizedEmail   string
	hashedPassword    string
	role              string
}

// AccountStore is safe for concurrent use.
//
// TODO(real build): in-memory only — every registered account is lost on
// restart. Also: for any NEWLY-registered (non-seeded) account, this is
// still a separate identifier space from oms-gateway's
// demoTrackedAccountIdentifiers / ledger's seeded accounts — a real
// build needs one canonical account identifier, created here and then
// propagated (or looked up) by every other service, rather than each
// service independently seeding its own "acct-001"-style demo accounts
// as they do today. Role assignment beyond the default RoleRetail (e.g.
// granting RoleAdmin to a real support/compliance employee account) has
// no admin-facing workflow here — out of scope for this build.
type AccountStore struct {
	mutexGuardingAccounts sync.Mutex
	accountsByEmail       map[string]*registeredAccount
	accountsByIdentifier  map[string]*registeredAccount
}

func NewAccountStore() *AccountStore {
	return &AccountStore{
		accountsByEmail:      make(map[string]*registeredAccount),
		accountsByIdentifier: make(map[string]*registeredAccount),
	}
}

// RegisterAccount hashes plaintextPassword and stores a new account under
// normalizedEmail (case-insensitive, matching how virtually every real
// email-based login treats addresses). Returns ErrEmailAlreadyRegistered
// if the email is already taken.
//
// requestedAccountIdentifier is OPTIONAL: pass "" for the normal
// real-user path, which mints a fresh acct-<8-byte-hex> identifier
// exactly as before. Pass a non-empty value (used by cmd/server/main.go
// to seed acct-001/acct-002) to register the account under that EXACT
// identifier instead — still validated for uniqueness
// (ErrAccountIdentifierAlreadyExists) so this can never silently
// overwrite an existing account.
//
// role is the account's RBAC role (see internal/jwtauth's Role*
// constants). Pass "" to default to jwtauth.RoleRetail.
func (store *AccountStore) RegisterAccount(email string, plaintextPassword string, requestedAccountIdentifier string, role string) (accountIdentifier string, err error) {
	normalizedEmail := normalizeEmail(email)

	hashedPassword, hashError := passwordhashing.HashPassword(plaintextPassword)
	if hashError != nil {
		return "", fmt.Errorf("failed to hash password: %w", hashError)
	}

	resolvedIdentifier := requestedAccountIdentifier
	if resolvedIdentifier == "" {
		generatedIdentifier, genError := generateAccountIdentifier()
		if genError != nil {
			return "", fmt.Errorf("failed to generate account identifier: %w", genError)
		}
		resolvedIdentifier = generatedIdentifier
	}

	resolvedRole := role
	if resolvedRole == "" {
		resolvedRole = jwtauth.RoleRetail
	}

	store.mutexGuardingAccounts.Lock()
	defer store.mutexGuardingAccounts.Unlock()

	if _, alreadyExists := store.accountsByEmail[normalizedEmail]; alreadyExists {
		return "", ErrEmailAlreadyRegistered
	}
	if _, alreadyExists := store.accountsByIdentifier[resolvedIdentifier]; alreadyExists {
		return "", ErrAccountIdentifierAlreadyExists
	}

	account := &registeredAccount{
		accountIdentifier: resolvedIdentifier,
		normalizedEmail:   normalizedEmail,
		hashedPassword:    hashedPassword,
		role:              resolvedRole,
	}
	store.accountsByEmail[normalizedEmail] = account
	store.accountsByIdentifier[resolvedIdentifier] = account
	return resolvedIdentifier, nil
}

// AuthenticateWithPassword verifies email/plaintextPassword and returns
// the account's identifier and role on success. Returns the SAME
// ErrInvalidCredentials whether the email doesn't exist or the password
// is wrong — never reveal which one via a different error message (a
// distinguishable "no such email" response is a classic account-
// enumeration side channel).
func (store *AccountStore) AuthenticateWithPassword(email string, plaintextPassword string) (accountIdentifier string, role string, err error) {
	normalizedEmail := normalizeEmail(email)

	store.mutexGuardingAccounts.Lock()
	account, wasFound := store.accountsByEmail[normalizedEmail]
	store.mutexGuardingAccounts.Unlock()

	if !wasFound {
		// Still run a password verification against a dummy hash so this
		// branch takes roughly the same time as the "email exists but
		// wrong password" branch below — otherwise an attacker could
		// time-oracle which emails are registered. Best-effort in a
		// skeleton (Go's scheduler/GC introduce noise a real timing-
		// attack mitigation would need to account for more carefully),
		// but the intent is documented here rather than skipped
		// silently.
		_, _ = passwordhashing.VerifyPassword(plaintextPassword, dummyHashForTimingParity)
		return "", "", ErrInvalidCredentials
	}

	wasVerified, verifyError := passwordhashing.VerifyPassword(plaintextPassword, account.hashedPassword)
	if verifyError != nil || !wasVerified {
		return "", "", ErrInvalidCredentials
	}

	return account.accountIdentifier, account.role, nil
}

// RoleForAccountIdentifier looks up an already-registered account's role
// by its account identifier (not email) — used on the refresh-token path,
// where only the account identifier (not the original email/password) is
// available, so a rotated access token carries the account's real role
// rather than a hardcoded one. Returns ("", false) for an unknown
// identifier.
func (store *AccountStore) RoleForAccountIdentifier(accountIdentifier string) (role string, wasFound bool) {
	store.mutexGuardingAccounts.Lock()
	defer store.mutexGuardingAccounts.Unlock()

	account, wasFound := store.accountsByIdentifier[accountIdentifier]
	if !wasFound {
		return "", false
	}
	return account.role, true
}

// dummyHashForTimingParity is a syntactically valid (but never actually
// used to protect a real account) password hash, computed once at
// package init so AuthenticateWithPassword's unknown-email branch has
// something real to verify against.
var dummyHashForTimingParity = mustHashDummyPassword()

func mustHashDummyPassword() string {
	hash, err := passwordhashing.HashPassword("dummy-password-for-timing-parity-only")
	if err != nil {
		panic(fmt.Sprintf("accountstore: failed to precompute dummy hash: %v", err))
	}
	return hash
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func generateAccountIdentifier() (string, error) {
	randomBytes := make([]byte, 8)
	if _, readError := rand.Read(randomBytes); readError != nil {
		return "", readError
	}
	return "acct-" + hex.EncodeToString(randomBytes), nil
}
