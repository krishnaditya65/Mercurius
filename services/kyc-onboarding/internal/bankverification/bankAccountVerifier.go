// Package bankverification implements the "penny-drop / micro-deposit"
// bank account verification flow — FEATURES.md §1. The standard pattern:
// deposit a small, unpredictable amount into the account being
// verified, then require the account holder to state that exact amount
// back (something only someone who can actually see the account's
// transaction history could know) before treating the account as
// verified for future withdrawals/payouts.
//
// TODO(real build): there is no real payment rail in this repo to
// actually deposit anything into an external bank account — see the
// loud caveat on PeekAtMicroDepositAmountForTesting below. A real build
// integrates with a banking API (Razorpay/Cashfree-style penny-drop
// APIs in India, Plaid/Stripe-style micro-deposits in the US) that
// actually moves real money and never exposes the amount to this
// service at all.
package bankverification

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"sync"
)

type VerificationStatus string

const (
	StatusPending      VerificationStatus = "PENDING"
	StatusVerified     VerificationStatus = "VERIFIED"
	StatusFailedLocked VerificationStatus = "FAILED_LOCKED"
	StatusNotFound     VerificationStatus = "NOT_FOUND"
)

// maxConfirmationAttempts bounds brute-forcing the deposited amount. The
// amount is drawn from [minMicroDepositMinorUnits,
// maxMicroDepositMinorUnits) — a 99-value range — so without a limit an
// attacker could just try every value.
const maxConfirmationAttempts = 3
const minMicroDepositMinorUnits = 1
const maxMicroDepositMinorUnits = 99

type verificationRecord struct {
	verificationId            string
	accountIdentifier         string
	bankAccountNumber         string
	ifscCode                  string
	depositedAmountMinorUnits int64
	status                    VerificationStatus
	remainingAttempts         int
}

// BankAccountVerifier is safe for concurrent use.
type BankAccountVerifier struct {
	mutexGuardingRecords sync.Mutex
	recordsById          map[string]*verificationRecord
	// latestVerificationIdByAccount lets InitiateVerification calls be
	// looked up by account without the caller needing to have persisted
	// the verificationId themselves (mirroring how a real bank-linking
	// UI flow would keep the id in its own session state, but this
	// skeleton doesn't have session state to lean on yet).
	latestVerificationIdByAccount map[string]string
}

func NewBankAccountVerifier() *BankAccountVerifier {
	return &BankAccountVerifier{
		recordsById:                   make(map[string]*verificationRecord),
		latestVerificationIdByAccount: make(map[string]string),
	}
}

// InitiateVerification starts a new verification, generating a random
// micro-deposit amount the caller does NOT get back — exactly like a
// real penny-drop flow, where the amount is only discoverable by
// actually looking at the bank account's transaction history.
func (verifier *BankAccountVerifier) InitiateVerification(accountIdentifier string, bankAccountNumber string, ifscCode string) (verificationId string, err error) {
	amountMinorUnits, genError := generateRandomMicroDepositAmount()
	if genError != nil {
		return "", fmt.Errorf("failed to generate micro-deposit amount: %w", genError)
	}

	newVerificationId, idGenError := generateVerificationId()
	if idGenError != nil {
		return "", fmt.Errorf("failed to generate verification id: %w", idGenError)
	}

	verifier.mutexGuardingRecords.Lock()
	defer verifier.mutexGuardingRecords.Unlock()

	verifier.recordsById[newVerificationId] = &verificationRecord{
		verificationId:            newVerificationId,
		accountIdentifier:         accountIdentifier,
		bankAccountNumber:         bankAccountNumber,
		ifscCode:                  ifscCode,
		depositedAmountMinorUnits: amountMinorUnits,
		status:                    StatusPending,
		remainingAttempts:         maxConfirmationAttempts,
	}
	verifier.latestVerificationIdByAccount[accountIdentifier] = newVerificationId

	return newVerificationId, nil
}

// ConfirmMicroDepositAmount is the account holder stating what amount
// they saw land in their bank account. Wrong guesses consume one of
// maxConfirmationAttempts; running out locks the verification
// permanently (StatusFailedLocked) — a NEW InitiateVerification call
// (with a fresh amount) is required to try again, not a reset of this
// one, so a locked-out attacker can't just keep guessing indefinitely
// against the same secret.
func (verifier *BankAccountVerifier) ConfirmMicroDepositAmount(verificationId string, claimedAmountMinorUnits int64) VerificationStatus {
	verifier.mutexGuardingRecords.Lock()
	defer verifier.mutexGuardingRecords.Unlock()

	record, wasFound := verifier.recordsById[verificationId]
	if !wasFound {
		return StatusNotFound
	}
	if record.status != StatusPending {
		// Already resolved (verified or locked) — return the final
		// status rather than re-evaluating a guess against a closed
		// verification.
		return record.status
	}

	if claimedAmountMinorUnits == record.depositedAmountMinorUnits {
		record.status = StatusVerified
		return StatusVerified
	}

	record.remainingAttempts--
	if record.remainingAttempts <= 0 {
		record.status = StatusFailedLocked
	}
	return record.status
}

// LatestVerificationIdForAccount returns the most recently initiated
// verification's id for an account — lets a caller that never persisted
// the id itself (e.g. a page reload) look it back up by account instead.
func (verifier *BankAccountVerifier) LatestVerificationIdForAccount(accountIdentifier string) (string, bool) {
	verifier.mutexGuardingRecords.Lock()
	defer verifier.mutexGuardingRecords.Unlock()

	verificationId, wasFound := verifier.latestVerificationIdByAccount[accountIdentifier]
	return verificationId, wasFound
}

// QueryVerificationStatus is a read-only lookup by verificationId — does
// not consume an attempt.
func (verifier *BankAccountVerifier) QueryVerificationStatus(verificationId string) VerificationStatus {
	verifier.mutexGuardingRecords.Lock()
	defer verifier.mutexGuardingRecords.Unlock()

	record, wasFound := verifier.recordsById[verificationId]
	if !wasFound {
		return StatusNotFound
	}
	return record.status
}

// PeekAtMicroDepositAmountForTesting returns the actual deposited
// amount. THIS ENTIRE FUNCTION IS A STAND-IN for a real payment rail:
// in a real build, the amount is genuinely deposited into the user's
// external bank account and this service NEVER has a way to reveal it
// on demand — the account holder can only learn it by checking their
// real bank statement. Since this repo has no real banking integration,
// exposing it here (wired to a clearly-named debug endpoint, never
// documented as anything but a demo/test convenience) is how this
// skeleton lets a live curl session actually complete the flow
// end-to-end. A real build must delete this function entirely.
func (verifier *BankAccountVerifier) PeekAtMicroDepositAmountForTesting(verificationId string) (int64, bool) {
	verifier.mutexGuardingRecords.Lock()
	defer verifier.mutexGuardingRecords.Unlock()

	record, wasFound := verifier.recordsById[verificationId]
	if !wasFound {
		return 0, false
	}
	return record.depositedAmountMinorUnits, true
}

func generateRandomMicroDepositAmount() (int64, error) {
	rangeSize := int64(maxMicroDepositMinorUnits - minMicroDepositMinorUnits + 1)
	randomOffset, genError := rand.Int(rand.Reader, big.NewInt(rangeSize))
	if genError != nil {
		return 0, genError
	}
	return minMicroDepositMinorUnits + randomOffset.Int64(), nil
}

func generateVerificationId() (string, error) {
	randomBytes := make([]byte, 8)
	if _, readError := rand.Read(randomBytes); readError != nil {
		return "", readError
	}
	return "bankverify-" + hex.EncodeToString(randomBytes), nil
}
