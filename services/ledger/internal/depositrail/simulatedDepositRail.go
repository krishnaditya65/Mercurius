// Package depositrail implements a SIMULATED UPI / NEFT / IMPS /
// net-banking deposit rail — FEATURES.md §2: "UPI / NEFT / IMPS /
// net-banking deposit integration."
//
// THIS IS NOT A REAL BANK INTEGRATION. No actual UPI, NEFT, IMPS, or
// net-banking network call happens anywhere in this package — there is
// no PSP (payment service provider), no NPCI switch, no bank webhook
// signature verification, nothing that talks to a real bank or payment
// network. What this package DOES model, honestly, is the two-phase
// request/confirm state machine every one of those real rails actually
// needs: a deposit is INITIATED (the client says "I'm sending ₹X via
// UPI/NEFT/IMPS/NETBANKING") and starts PENDING — nothing has moved yet,
// because in the real world the bank hasn't told us the money arrived
// yet either. A separate ConfirmDeposit call — standing in for the
// bank's webhook/callback that would fire once the money actually
// clears — is what transitions a deposit to CONFIRMED, and that
// transition is the one moment real money moves: it posts a REAL journal
// entry via the existing fundsegregation.SegregationGuard's
// PostClientMoneyMovement, so a CONFIRMED deposit is a genuine,
// ring-fenced ledger entry, not a status-field flip pretending to be one.
//
// TODO(real build): integrate an actual PSP/bank API (e.g. a UPI
// collect-request via an NPCI-certified switch, an NEFT/IMPS initiation
// via a banking partner's API) to actually move money and receive a
// cryptographically-verified webhook when it clears, instead of a
// same-process ConfirmDeposit call any caller can invoke. Also needs:
// idempotency keys / webhook replay protection, a reconciliation job
// against the bank's own settlement file, and a REJECTED/FAILED terminal
// status for a deposit the bank actually declines (this skeleton only
// models the successful PENDING -> CONFIRMED path plus rejecting a
// double-confirm).
package depositrail

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"mercurius/ledger/internal/fundsegregation"
)

// DepositMethod is which simulated rail a deposit claims to have arrived
// through. All four are the ones named in FEATURES.md §2 — none of them
// actually touch a real network in this skeleton.
type DepositMethod string

const (
	DepositMethodUpi        DepositMethod = "UPI"
	DepositMethodNeft       DepositMethod = "NEFT"
	DepositMethodImps       DepositMethod = "IMPS"
	DepositMethodNetbanking DepositMethod = "NETBANKING"
)

func (method DepositMethod) isValid() bool {
	switch method {
	case DepositMethodUpi, DepositMethodNeft, DepositMethodImps, DepositMethodNetbanking:
		return true
	default:
		return false
	}
}

// DepositStatus is where a simulated deposit sits in its request/confirm
// state machine.
type DepositStatus string

const (
	DepositStatusPending   DepositStatus = "PENDING"
	DepositStatusConfirmed DepositStatus = "CONFIRMED"
)

var ErrInvalidDepositAmount = fmt.Errorf("deposit amount must be strictly positive")
var ErrInvalidDepositMethod = fmt.Errorf("deposit method must be one of UPI, NEFT, IMPS, NETBANKING")
var ErrMissingAccountIdentifier = fmt.Errorf("accountIdentifier must not be empty")
var ErrDepositNotFound = fmt.Errorf("no deposit exists with that id")
var ErrDepositAlreadyConfirmed = fmt.Errorf("deposit has already been confirmed — cannot confirm twice")

// SimulatedDeposit is one modelled deposit-rail request.
type SimulatedDeposit struct {
	DepositId          string
	AccountIdentifier  string
	Method             DepositMethod
	AmountInMinorUnits int64
	InitiatedAt        time.Time
	ConfirmedAt        time.Time
	Status             DepositStatus
}

// SimulatedDepositRail is safe for concurrent use. A CONFIRMED deposit
// moves REAL money through the shared *fundsegregation.SegregationGuard
// — the exact same ring-fenced path /client-funds/deposit uses — so
// depositrail is not a parallel bookkeeping system, just a different
// front door onto the same client money movement.
type SimulatedDepositRail struct {
	segregationGuard *fundsegregation.SegregationGuard

	mutexGuardingDeposits sync.Mutex
	depositsById          map[string]*SimulatedDeposit
}

func NewSimulatedDepositRail(segregationGuard *fundsegregation.SegregationGuard) *SimulatedDepositRail {
	return &SimulatedDepositRail{
		segregationGuard: segregationGuard,
		depositsById:     make(map[string]*SimulatedDeposit),
	}
}

// InitiateDeposit records that a client claims to be sending
// amountInMinorUnits via method — this does NOT move any money. It only
// starts the PENDING state that a real bank webhook would later resolve.
func (rail *SimulatedDepositRail) InitiateDeposit(
	accountIdentifier string,
	method DepositMethod,
	amountInMinorUnits int64,
	now time.Time,
) (*SimulatedDeposit, error) {
	if accountIdentifier == "" {
		return nil, ErrMissingAccountIdentifier
	}
	if !method.isValid() {
		return nil, ErrInvalidDepositMethod
	}
	if amountInMinorUnits <= 0 {
		return nil, ErrInvalidDepositAmount
	}

	depositId, genError := generateDepositId()
	if genError != nil {
		return nil, fmt.Errorf("failed to generate deposit id: %w", genError)
	}

	deposit := &SimulatedDeposit{
		DepositId:          depositId,
		AccountIdentifier:  accountIdentifier,
		Method:             method,
		AmountInMinorUnits: amountInMinorUnits,
		InitiatedAt:        now,
		Status:             DepositStatusPending,
	}

	rail.mutexGuardingDeposits.Lock()
	rail.depositsById[depositId] = deposit
	rail.mutexGuardingDeposits.Unlock()

	return deposit, nil
}

// ConfirmDeposit stands in for the real bank's webhook/callback firing
// once the money has actually cleared. This is the ONLY place real money
// moves: it posts a real, ring-fenced journal entry via
// fundsegregation.SegregationGuard.PostClientMoneyMovement. Confirming an
// already-confirmed or unknown deposit is rejected outright — money must
// never be posted twice for the same deposit.
func (rail *SimulatedDepositRail) ConfirmDeposit(depositId string, now time.Time) (*SimulatedDeposit, error) {
	rail.mutexGuardingDeposits.Lock()
	deposit, wasFound := rail.depositsById[depositId]
	if !wasFound {
		rail.mutexGuardingDeposits.Unlock()
		return nil, ErrDepositNotFound
	}
	if deposit.Status == DepositStatusConfirmed {
		rail.mutexGuardingDeposits.Unlock()
		return nil, ErrDepositAlreadyConfirmed
	}
	// Claim this deposit for confirmation while still holding the lock
	// — this is what prevents two concurrent ConfirmDeposit calls for
	// the same id from both posting the money. Anything that fails
	// after this point (the actual ledger post) rolls the status back.
	deposit.Status = DepositStatusConfirmed
	rail.mutexGuardingDeposits.Unlock()

	postError := rail.segregationGuard.PostClientMoneyMovement(
		deposit.AccountIdentifier,
		deposit.AmountInMinorUnits,
		fmt.Sprintf("simulated %s deposit confirmed, depositId=%s", deposit.Method, deposit.DepositId),
	)
	if postError != nil {
		rail.mutexGuardingDeposits.Lock()
		deposit.Status = DepositStatusPending
		rail.mutexGuardingDeposits.Unlock()
		return nil, postError
	}

	rail.mutexGuardingDeposits.Lock()
	deposit.ConfirmedAt = now
	rail.mutexGuardingDeposits.Unlock()
	return deposit, nil
}

// DepositsForAccount returns every deposit (any status) for
// accountIdentifier, sorted by InitiatedAt for a deterministic response.
func (rail *SimulatedDepositRail) DepositsForAccount(accountIdentifier string) []*SimulatedDeposit {
	rail.mutexGuardingDeposits.Lock()
	defer rail.mutexGuardingDeposits.Unlock()

	matching := make([]*SimulatedDeposit, 0)
	for _, deposit := range rail.depositsById {
		if deposit.AccountIdentifier == accountIdentifier {
			matching = append(matching, deposit)
		}
	}
	sort.Slice(matching, func(i, j int) bool { return matching[i].InitiatedAt.Before(matching[j].InitiatedAt) })
	return matching
}

// LookupDeposit returns a single deposit by id, if it exists.
func (rail *SimulatedDepositRail) LookupDeposit(depositId string) (*SimulatedDeposit, bool) {
	rail.mutexGuardingDeposits.Lock()
	defer rail.mutexGuardingDeposits.Unlock()

	deposit, wasFound := rail.depositsById[depositId]
	return deposit, wasFound
}

func generateDepositId() (string, error) {
	randomBytes := make([]byte, 8)
	if _, readError := rand.Read(randomBytes); readError != nil {
		return "", readError
	}
	return "deposit-" + hex.EncodeToString(randomBytes), nil
}
