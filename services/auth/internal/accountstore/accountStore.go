// Package accountstore is the identity registry this skeleton's
// register/login flow uses: email -> {accountIdentifier, password hash}.
// Deliberately separate from every other service's notion of an
// "account" (ledger's balance account, oms-gateway's demo-seeded trading
// account, kyc-onboarding's KYC record) — a real build would need these
// to converge on one account identifier space; this skeleton doesn't
// attempt that yet (see the package doc TODO below).
package accountstore

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"mercurius/auth/internal/passwordhashing"
)

var ErrEmailAlreadyRegistered = errors.New("an account with this email is already registered")
var ErrInvalidCredentials = errors.New("invalid email or password")

type registeredAccount struct {
	accountIdentifier string
	normalizedEmail   string
	hashedPassword    string
}

// AccountStore is safe for concurrent use.
//
// TODO(real build): in-memory only — every registered account is lost on
// restart. Also: this is a SEPARATE identifier space from oms-gateway's
// demoTrackedAccountIdentifiers / ledger's seeded accounts — a real build
// needs one canonical account identifier, created here and then
// propagated (or looked up) by every other service, rather than each
// service independently seeding its own "acct-001"-style demo accounts
// as they do today.
type AccountStore struct {
	mutexGuardingAccounts sync.Mutex
	accountsByEmail       map[string]*registeredAccount
}

func NewAccountStore() *AccountStore {
	return &AccountStore{accountsByEmail: make(map[string]*registeredAccount)}
}

// RegisterAccount hashes plaintextPassword and stores a new account under
// normalizedEmail (case-insensitive, matching how virtually every real
// email-based login treats addresses). Returns ErrEmailAlreadyRegistered
// if the email is already taken.
func (store *AccountStore) RegisterAccount(email string, plaintextPassword string) (accountIdentifier string, err error) {
	normalizedEmail := normalizeEmail(email)

	hashedPassword, hashError := passwordhashing.HashPassword(plaintextPassword)
	if hashError != nil {
		return "", fmt.Errorf("failed to hash password: %w", hashError)
	}

	newAccountIdentifier, genError := generateAccountIdentifier()
	if genError != nil {
		return "", fmt.Errorf("failed to generate account identifier: %w", genError)
	}

	store.mutexGuardingAccounts.Lock()
	defer store.mutexGuardingAccounts.Unlock()

	if _, alreadyExists := store.accountsByEmail[normalizedEmail]; alreadyExists {
		return "", ErrEmailAlreadyRegistered
	}

	store.accountsByEmail[normalizedEmail] = &registeredAccount{
		accountIdentifier: newAccountIdentifier,
		normalizedEmail:   normalizedEmail,
		hashedPassword:    hashedPassword,
	}
	return newAccountIdentifier, nil
}

// AuthenticateWithPassword verifies email/plaintextPassword and returns
// the account's identifier on success. Returns the SAME
// ErrInvalidCredentials whether the email doesn't exist or the password
// is wrong — never reveal which one via a different error message (a
// distinguishable "no such email" response is a classic account-
// enumeration side channel).
func (store *AccountStore) AuthenticateWithPassword(email string, plaintextPassword string) (accountIdentifier string, err error) {
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
		return "", ErrInvalidCredentials
	}

	wasVerified, verifyError := passwordhashing.VerifyPassword(plaintextPassword, account.hashedPassword)
	if verifyError != nil || !wasVerified {
		return "", ErrInvalidCredentials
	}

	return account.accountIdentifier, nil
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
