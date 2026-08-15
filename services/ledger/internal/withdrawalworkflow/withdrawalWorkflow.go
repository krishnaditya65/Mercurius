// Package withdrawalworkflow implements a real withdrawal request flow
// with T+N settlement holds — FEATURES.md §2: "Withdrawal workflow with
// T+N settlement holds." Requesting a withdrawal doesn't move any money
// immediately: it reserves ("holds") the amount — making it unavailable
// for a further withdrawal request, though still visible in the
// account's raw ledger balance — until the settlement hold period
// elapses, at which point the actual ledger entry moving the funds out
// is posted.
//
// TODO(real build): there is no real payment rail here — "payout" means
// posting a journal entry moving the balance to a clearing account, not
// an actual bank transfer (same category of gap as kyc-onboarding's
// bank-verification penny-drop, which has the same caveat). Also:
// ProcessDueWithdrawals is exposed as an endpoint an operator (or a
// script) calls, not run automatically on a schedule — a real build
// needs a real scheduled job, not a manually-triggered sweep.
package withdrawalworkflow

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"mercurius/ledger/internal/doubleentry"
)

type WithdrawalStatus string

const (
	StatusPendingHold WithdrawalStatus = "PENDING_HOLD"
	StatusCompleted   WithdrawalStatus = "COMPLETED"
	StatusCancelled   WithdrawalStatus = "CANCELLED"
)

var ErrInvalidWithdrawalAmount = fmt.Errorf("withdrawal amount must be strictly positive")
var ErrInsufficientAvailableBalance = fmt.Errorf("requested amount exceeds available (non-held) balance")
var ErrWithdrawalRequestNotFound = fmt.Errorf("no withdrawal request exists with that id")
var ErrWithdrawalNotCancellable = fmt.Errorf("only a PENDING_HOLD withdrawal can be cancelled")

type WithdrawalRequest struct {
	WithdrawalId        string
	AccountIdentifier   string
	AmountInMinorUnits  int64
	RequestedAt         time.Time
	EligibleForPayoutAt time.Time
	Status              WithdrawalStatus
}

// WithdrawalWorkflow is safe for concurrent use. It shares the SAME
// ledger book the rest of this service uses — a completed withdrawal is
// a real, balanced journal entry through the exact same
// `doubleentry.InMemoryDoubleEntryLedgerBook`, not a separate parallel
// bookkeeping system.
type WithdrawalWorkflow struct {
	ledgerBook                              doubleentry.LedgerBook
	firmWithdrawalClearingAccountIdentifier string
	settlementHoldDuration                  time.Duration

	mutexGuardingRequests sync.Mutex
	requestsById          map[string]*WithdrawalRequest
}

func NewWithdrawalWorkflow(
	ledgerBook doubleentry.LedgerBook,
	firmWithdrawalClearingAccountIdentifier string,
	settlementHoldDuration time.Duration,
) *WithdrawalWorkflow {
	return &WithdrawalWorkflow{
		ledgerBook:                              ledgerBook,
		firmWithdrawalClearingAccountIdentifier: firmWithdrawalClearingAccountIdentifier,
		settlementHoldDuration:                  settlementHoldDuration,
		requestsById:                            make(map[string]*WithdrawalRequest),
	}
}

// AvailableBalanceInMinorUnits is the ledger's raw balance MINUS every
// currently-held (PENDING_HOLD, not yet completed or cancelled)
// withdrawal amount for this account — this is what a client should
// actually be allowed to withdraw or trade against, not the raw ledger
// balance, which still includes money already earmarked to leave.
func (workflow *WithdrawalWorkflow) AvailableBalanceInMinorUnits(accountIdentifier string) (int64, error) {
	ledgerBalance, balanceError := workflow.ledgerBook.CurrentBalanceInMinorUnits(accountIdentifier)
	if balanceError != nil {
		return 0, balanceError
	}

	workflow.mutexGuardingRequests.Lock()
	defer workflow.mutexGuardingRequests.Unlock()

	heldAmount := int64(0)
	for _, request := range workflow.requestsById {
		if request.AccountIdentifier == accountIdentifier && request.Status == StatusPendingHold {
			heldAmount += request.AmountInMinorUnits
		}
	}

	return ledgerBalance - heldAmount, nil
}

// RequestWithdrawal places a hold for amountInMinorUnits, payable no
// earlier than settlementHoldDuration from now. Fails if the amount
// exceeds the account's currently AVAILABLE (not raw) balance — you
// cannot hold money that's already held by an earlier pending
// withdrawal.
func (workflow *WithdrawalWorkflow) RequestWithdrawal(
	accountIdentifier string,
	amountInMinorUnits int64,
	now time.Time,
) (*WithdrawalRequest, error) {
	if amountInMinorUnits <= 0 {
		return nil, ErrInvalidWithdrawalAmount
	}

	availableBalance, balanceError := workflow.AvailableBalanceInMinorUnits(accountIdentifier)
	if balanceError != nil {
		return nil, balanceError
	}
	if amountInMinorUnits > availableBalance {
		return nil, ErrInsufficientAvailableBalance
	}

	withdrawalId, genError := generateWithdrawalId()
	if genError != nil {
		return nil, fmt.Errorf("failed to generate withdrawal id: %w", genError)
	}

	request := &WithdrawalRequest{
		WithdrawalId:        withdrawalId,
		AccountIdentifier:   accountIdentifier,
		AmountInMinorUnits:  amountInMinorUnits,
		RequestedAt:         now,
		EligibleForPayoutAt: now.Add(workflow.settlementHoldDuration),
		Status:              StatusPendingHold,
	}

	workflow.mutexGuardingRequests.Lock()
	workflow.requestsById[withdrawalId] = request
	workflow.mutexGuardingRequests.Unlock()

	return request, nil
}

// CancelWithdrawal releases a still-pending hold — only valid while the
// request is StatusPendingHold; a COMPLETED withdrawal has already
// actually moved the money and cannot be undone this way, and a
// CANCELLED one is already resolved.
func (workflow *WithdrawalWorkflow) CancelWithdrawal(withdrawalId string) (*WithdrawalRequest, error) {
	workflow.mutexGuardingRequests.Lock()
	defer workflow.mutexGuardingRequests.Unlock()

	request, wasFound := workflow.requestsById[withdrawalId]
	if !wasFound {
		return nil, ErrWithdrawalRequestNotFound
	}
	if request.Status != StatusPendingHold {
		return nil, ErrWithdrawalNotCancellable
	}

	request.Status = StatusCancelled
	return request, nil
}

// ProcessDueWithdrawals sweeps every PENDING_HOLD request whose
// EligibleForPayoutAt has passed and actually posts the settlement
// journal entry — money genuinely leaves the account's ledger balance
// here (crediting the account, debiting the firm withdrawal clearing
// account, per this service's existing debit-increases/credit-decreases
// convention), not just a status-field flip. A request that fails to
// post (e.g. the account was somehow removed) is skipped, left
// PENDING_HOLD, and reported in failedWithdrawalIds rather than silently
// dropped.
func (workflow *WithdrawalWorkflow) ProcessDueWithdrawals(now time.Time) (completed []*WithdrawalRequest, failedWithdrawalIds []string) {
	workflow.mutexGuardingRequests.Lock()
	dueRequests := make([]*WithdrawalRequest, 0)
	for _, request := range workflow.requestsById {
		if request.Status == StatusPendingHold && !now.Before(request.EligibleForPayoutAt) {
			dueRequests = append(dueRequests, request)
		}
	}
	workflow.mutexGuardingRequests.Unlock()

	sort.Slice(dueRequests, func(i, j int) bool { return dueRequests[i].WithdrawalId < dueRequests[j].WithdrawalId })

	for _, request := range dueRequests {
		postError := workflow.ledgerBook.PostJournalEntry(doubleentry.JournalEntry{
			HumanReadableDescription: fmt.Sprintf("withdrawal payout, withdrawalId=%s", request.WithdrawalId),
			DebitLines: []doubleentry.LedgerAccountLine{
				{LedgerAccountIdentifier: workflow.firmWithdrawalClearingAccountIdentifier, AmountInMinorUnits: request.AmountInMinorUnits},
			},
			CreditLines: []doubleentry.LedgerAccountLine{
				{LedgerAccountIdentifier: request.AccountIdentifier, AmountInMinorUnits: request.AmountInMinorUnits},
			},
		})
		if postError != nil {
			failedWithdrawalIds = append(failedWithdrawalIds, request.WithdrawalId)
			continue
		}

		workflow.mutexGuardingRequests.Lock()
		request.Status = StatusCompleted
		workflow.mutexGuardingRequests.Unlock()
		completed = append(completed, request)
	}

	return completed, failedWithdrawalIds
}

// RequestsForAccount returns every withdrawal request (any status) for
// accountIdentifier, sorted by RequestedAt for a deterministic response.
func (workflow *WithdrawalWorkflow) RequestsForAccount(accountIdentifier string) []*WithdrawalRequest {
	workflow.mutexGuardingRequests.Lock()
	defer workflow.mutexGuardingRequests.Unlock()

	matchingRequests := make([]*WithdrawalRequest, 0)
	for _, request := range workflow.requestsById {
		if request.AccountIdentifier == accountIdentifier {
			matchingRequests = append(matchingRequests, request)
		}
	}
	sort.Slice(matchingRequests, func(i, j int) bool { return matchingRequests[i].RequestedAt.Before(matchingRequests[j].RequestedAt) })
	return matchingRequests
}

func (workflow *WithdrawalWorkflow) LookupRequest(withdrawalId string) (*WithdrawalRequest, bool) {
	workflow.mutexGuardingRequests.Lock()
	defer workflow.mutexGuardingRequests.Unlock()

	request, wasFound := workflow.requestsById[withdrawalId]
	return request, wasFound
}

func generateWithdrawalId() (string, error) {
	randomBytes := make([]byte, 8)
	if _, readError := rand.Read(randomBytes); readError != nil {
		return "", readError
	}
	return "withdrawal-" + hex.EncodeToString(randomBytes), nil
}
