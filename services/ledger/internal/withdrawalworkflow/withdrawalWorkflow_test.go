package withdrawalworkflow

import (
	"testing"
	"time"

	"mercurius/ledger/internal/doubleentry"
)

const testSettlementHoldDuration = 2 * 24 * time.Hour // T+2

var testNow = time.Unix(1_700_000_000, 0)

func newTestWorkflowWithFundedAccount(t *testing.T, fundedAmount int64) (*WithdrawalWorkflow, *doubleentry.InMemoryDoubleEntryLedgerBook) {
	t.Helper()
	ledgerBook := doubleentry.NewInMemoryDoubleEntryLedgerBookWithAccounts([]string{"acct-001", "firm-clearing-acct"})
	if fundedAmount > 0 {
		fundingError := ledgerBook.PostJournalEntry(doubleentry.JournalEntry{
			HumanReadableDescription: "test funding",
			DebitLines:               []doubleentry.LedgerAccountLine{{LedgerAccountIdentifier: "acct-001", AmountInMinorUnits: fundedAmount}},
			CreditLines:              []doubleentry.LedgerAccountLine{{LedgerAccountIdentifier: "firm-clearing-acct", AmountInMinorUnits: fundedAmount}},
		})
		if fundingError != nil {
			t.Fatalf("test setup: failed to fund account: %v", fundingError)
		}
	}
	workflow := NewWithdrawalWorkflow(ledgerBook, "firm-clearing-acct", testSettlementHoldDuration)
	return workflow, ledgerBook
}

func TestAvailableBalanceEqualsLedgerBalanceWithNoPendingWithdrawals(t *testing.T) {
	workflow, _ := newTestWorkflowWithFundedAccount(t, 100_000)

	available, err := workflow.AvailableBalanceInMinorUnits("acct-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if available != 100_000 {
		t.Fatalf("expected 100000, got %d", available)
	}
}

func TestRequestWithdrawalReducesAvailableBalanceButNotLedgerBalance(t *testing.T) {
	workflow, ledgerBook := newTestWorkflowWithFundedAccount(t, 100_000)

	_, err := workflow.RequestWithdrawal("acct-001", 40_000, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	available, _ := workflow.AvailableBalanceInMinorUnits("acct-001")
	if available != 60_000 {
		t.Fatalf("expected available balance 60000 after a 40000 hold, got %d", available)
	}

	ledgerBalance, _ := ledgerBook.CurrentBalanceInMinorUnits("acct-001")
	if ledgerBalance != 100_000 {
		t.Fatalf("expected the raw ledger balance to be UNCHANGED by a pending hold, got %d", ledgerBalance)
	}
}

func TestRequestWithdrawalExceedingAvailableBalanceIsRejected(t *testing.T) {
	workflow, _ := newTestWorkflowWithFundedAccount(t, 100_000)

	_, err := workflow.RequestWithdrawal("acct-001", 150_000, testNow)
	if err != ErrInsufficientAvailableBalance {
		t.Fatalf("expected ErrInsufficientAvailableBalance, got %v", err)
	}
}

func TestASecondWithdrawalCannotDoubleSpendAlreadyHeldFunds(t *testing.T) {
	workflow, _ := newTestWorkflowWithFundedAccount(t, 100_000)

	_, err := workflow.RequestWithdrawal("acct-001", 70_000, testNow)
	if err != nil {
		t.Fatalf("unexpected error on first request: %v", err)
	}

	// Only 30000 is actually available now — a second request for more
	// than that must fail, proving holds actually stack rather than
	// each being checked independently against the full ledger balance.
	_, err = workflow.RequestWithdrawal("acct-001", 40_000, testNow)
	if err != ErrInsufficientAvailableBalance {
		t.Fatalf("expected the second request to be rejected against the reduced available balance, got %v", err)
	}
}

func TestNonPositiveAmountIsRejected(t *testing.T) {
	workflow, _ := newTestWorkflowWithFundedAccount(t, 100_000)

	_, err := workflow.RequestWithdrawal("acct-001", 0, testNow)
	if err != ErrInvalidWithdrawalAmount {
		t.Fatalf("expected ErrInvalidWithdrawalAmount for zero, got %v", err)
	}
	_, err = workflow.RequestWithdrawal("acct-001", -100, testNow)
	if err != ErrInvalidWithdrawalAmount {
		t.Fatalf("expected ErrInvalidWithdrawalAmount for negative, got %v", err)
	}
}

func TestProcessDueWithdrawalsDoesNothingBeforeTheHoldPeriodElapses(t *testing.T) {
	workflow, ledgerBook := newTestWorkflowWithFundedAccount(t, 100_000)
	workflow.RequestWithdrawal("acct-001", 40_000, testNow)

	completed, failed := workflow.ProcessDueWithdrawals(testNow.Add(1 * time.Hour)) // well before T+2
	if len(completed) != 0 || len(failed) != 0 {
		t.Fatalf("expected nothing processed before the hold period elapses, got completed=%d failed=%d", len(completed), len(failed))
	}

	ledgerBalance, _ := ledgerBook.CurrentBalanceInMinorUnits("acct-001")
	if ledgerBalance != 100_000 {
		t.Fatalf("expected the ledger balance to remain untouched, got %d", ledgerBalance)
	}
}

func TestProcessDueWithdrawalsActuallyMovesTheMoneyOnceTheHoldElapses(t *testing.T) {
	workflow, ledgerBook := newTestWorkflowWithFundedAccount(t, 100_000)
	request, _ := workflow.RequestWithdrawal("acct-001", 40_000, testNow)

	completed, failed := workflow.ProcessDueWithdrawals(testNow.Add(testSettlementHoldDuration))
	if len(failed) != 0 {
		t.Fatalf("expected no failures, got %v", failed)
	}
	if len(completed) != 1 || completed[0].WithdrawalId != request.WithdrawalId {
		t.Fatalf("expected exactly the one due request to complete, got %+v", completed)
	}
	if completed[0].Status != StatusCompleted {
		t.Fatalf("expected StatusCompleted, got %v", completed[0].Status)
	}

	// The money has now GENUINELY left the account via a real journal
	// entry — this is the actual load-bearing assertion of the whole
	// feature.
	accountBalance, _ := ledgerBook.CurrentBalanceInMinorUnits("acct-001")
	if accountBalance != 60_000 {
		t.Fatalf("expected the account's ledger balance to drop to 60000, got %d", accountBalance)
	}
	// The clearing account started at -100000 (it was the funding
	// SOURCE in newTestWorkflowWithFundedAccount's setup journal entry —
	// crediting/decreasing it by 100000 to fund acct-001). The
	// withdrawal's debit line increases it by 40000, landing at -60000
	// — this is the correct accounting outcome, not a bug: the clearing
	// account is -100000 (paid out to fund the user) + 40000 (received
	// back via this withdrawal) = -60000, i.e. net -60000 still owed
	// to/by the firm across both entries.
	clearingBalance, _ := ledgerBook.CurrentBalanceInMinorUnits("firm-clearing-acct")
	if clearingBalance != -60_000 {
		t.Fatalf("expected the clearing account to land at -60000 (-100000 funding + 40000 withdrawal), got %d", clearingBalance)
	}

	// And the available balance calculation should no longer subtract
	// this request — it's COMPLETED, not PENDING_HOLD, and the money is
	// simply gone from the ledger balance now.
	available, _ := workflow.AvailableBalanceInMinorUnits("acct-001")
	if available != 60_000 {
		t.Fatalf("expected available balance to also be 60000 post-completion, got %d", available)
	}
}

func TestCancelWithdrawalReleasesTheHold(t *testing.T) {
	workflow, _ := newTestWorkflowWithFundedAccount(t, 100_000)
	request, _ := workflow.RequestWithdrawal("acct-001", 40_000, testNow)

	cancelledRequest, err := workflow.CancelWithdrawal(request.WithdrawalId)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cancelledRequest.Status != StatusCancelled {
		t.Fatalf("expected StatusCancelled, got %v", cancelledRequest.Status)
	}

	available, _ := workflow.AvailableBalanceInMinorUnits("acct-001")
	if available != 100_000 {
		t.Fatalf("expected the full balance to become available again after cancellation, got %d", available)
	}
}

func TestCancellingAnAlreadyCompletedWithdrawalFails(t *testing.T) {
	workflow, _ := newTestWorkflowWithFundedAccount(t, 100_000)
	request, _ := workflow.RequestWithdrawal("acct-001", 40_000, testNow)
	workflow.ProcessDueWithdrawals(testNow.Add(testSettlementHoldDuration))

	_, err := workflow.CancelWithdrawal(request.WithdrawalId)
	if err != ErrWithdrawalNotCancellable {
		t.Fatalf("expected ErrWithdrawalNotCancellable, got %v", err)
	}
}

func TestCancellingAnUnknownWithdrawalFails(t *testing.T) {
	workflow, _ := newTestWorkflowWithFundedAccount(t, 100_000)
	_, err := workflow.CancelWithdrawal("never-requested")
	if err != ErrWithdrawalRequestNotFound {
		t.Fatalf("expected ErrWithdrawalRequestNotFound, got %v", err)
	}
}

func TestRequestsForAccountReturnsInRequestedOrder(t *testing.T) {
	workflow, _ := newTestWorkflowWithFundedAccount(t, 100_000)
	first, _ := workflow.RequestWithdrawal("acct-001", 10_000, testNow)
	second, _ := workflow.RequestWithdrawal("acct-001", 10_000, testNow.Add(time.Minute))

	requests := workflow.RequestsForAccount("acct-001")
	if len(requests) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(requests))
	}
	if requests[0].WithdrawalId != first.WithdrawalId || requests[1].WithdrawalId != second.WithdrawalId {
		t.Fatal("expected requests ordered by RequestedAt")
	}
}

func TestLookupRequestReturnsNotFoundForAnUnknownId(t *testing.T) {
	workflow, _ := newTestWorkflowWithFundedAccount(t, 100_000)
	_, wasFound := workflow.LookupRequest("never-requested")
	if wasFound {
		t.Fatal("expected not found for an unknown withdrawal id")
	}
}
