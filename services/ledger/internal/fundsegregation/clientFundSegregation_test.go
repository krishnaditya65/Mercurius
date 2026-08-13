package fundsegregation

import (
	"errors"
	"testing"

	"mercurius/ledger/internal/doubleentry"
)

func newTestGuard() (*doubleentry.InMemoryDoubleEntryLedgerBook, *SegregationGuard) {
	ledgerBook := doubleentry.NewInMemoryDoubleEntryLedgerBookWithAccounts([]string{
		"acct-001",
		"acct-002",
		"firm-clearing-acct",
		"client-money-custody-pool",
		"external-cash-suspense",
	})
	guard := NewSegregationGuard(
		ledgerBook,
		"client-money-custody-pool",
		"external-cash-suspense",
		[]string{"acct-001", "acct-002"},
		[]string{"firm-clearing-acct"},
	)
	return ledgerBook, guard
}

func TestPostClientMoneyMovementIncreasesBothClientAndCustodyBalances(t *testing.T) {
	ledgerBook, guard := newTestGuard()

	if postError := guard.PostClientMoneyMovement("acct-001", 500_000, "test deposit"); postError != nil {
		t.Fatalf("expected deposit to succeed, got error: %v", postError)
	}

	clientBalance, _ := ledgerBook.CurrentBalanceInMinorUnits("acct-001")
	custodyBalance, _ := ledgerBook.CurrentBalanceInMinorUnits("client-money-custody-pool")
	if clientBalance != 500_000 {
		t.Errorf("expected client balance 500000, got %d", clientBalance)
	}
	if custodyBalance != 500_000 {
		t.Errorf("expected custody balance 500000, got %d", custodyBalance)
	}
}

func TestPostClientMoneyMovementNegativeAmountDecreasesBoth(t *testing.T) {
	_, guard := newTestGuard()
	_ = guard.PostClientMoneyMovement("acct-001", 500_000, "fund first")

	if postError := guard.PostClientMoneyMovement("acct-001", -200_000, "payout"); postError != nil {
		t.Fatalf("expected payout to succeed, got error: %v", postError)
	}

	report, reportError := guard.CheckSegregationInvariant()
	if reportError != nil {
		t.Fatalf("unexpected error: %v", reportError)
	}
	if report.AggregateClientBalanceInMinorUnits != 300_000 {
		t.Errorf("expected aggregate client balance 300000, got %d", report.AggregateClientBalanceInMinorUnits)
	}
	if !report.IsSegregationIntact {
		t.Errorf("expected segregation to remain intact, got discrepancy=%d", report.DiscrepancyInMinorUnits)
	}
}

func TestPostClientMoneyMovementRejectsZeroAmount(t *testing.T) {
	_, guard := newTestGuard()
	postError := guard.PostClientMoneyMovement("acct-001", 0, "bad")
	if !errors.Is(postError, ErrInvalidMovementAmount) {
		t.Errorf("expected ErrInvalidMovementAmount, got %v", postError)
	}
}

func TestPostClientMoneyMovementRejectsUnclassifiedAccount(t *testing.T) {
	_, guard := newTestGuard()
	postError := guard.PostClientMoneyMovement("firm-clearing-acct", 1000, "bad")
	if !errors.Is(postError, ErrUnclassifiedAccount) {
		t.Errorf("expected ErrUnclassifiedAccount, got %v", postError)
	}
}

func TestCheckSegregationInvariantIsIntactAfterSeveralMovements(t *testing.T) {
	_, guard := newTestGuard()
	_ = guard.PostClientMoneyMovement("acct-001", 700_000, "deposit 1")
	_ = guard.PostClientMoneyMovement("acct-002", 300_000, "deposit 2")
	_ = guard.PostClientMoneyMovement("acct-001", -100_000, "payout")

	report, reportError := guard.CheckSegregationInvariant()
	if reportError != nil {
		t.Fatalf("unexpected error: %v", reportError)
	}
	if !report.IsSegregationIntact {
		t.Errorf("expected segregation intact, got discrepancy=%d", report.DiscrepancyInMinorUnits)
	}
	if report.CustodyPoolBalanceInMinorUnits != 900_000 {
		t.Errorf("expected custody balance 900000, got %d", report.CustodyPoolBalanceInMinorUnits)
	}
	if report.ClientAccountCount != 2 {
		t.Errorf("expected 2 classified client accounts, got %d", report.ClientAccountCount)
	}
}

func TestInterClientTransferPreservesSegregationInvariant(t *testing.T) {
	_, guard := newTestGuard()
	_ = guard.PostClientMoneyMovement("acct-001", 500_000, "fund acct-001")

	if transferError := guard.PostInterClientTransfer("acct-001", "acct-002", 200_000, "internal transfer"); transferError != nil {
		t.Fatalf("expected transfer to succeed, got error: %v", transferError)
	}

	report, _ := guard.CheckSegregationInvariant()
	if !report.IsSegregationIntact {
		t.Errorf("expected segregation intact after client-to-client transfer, got discrepancy=%d", report.DiscrepancyInMinorUnits)
	}
	if report.AggregateClientBalanceInMinorUnits != 500_000 {
		t.Errorf("aggregate client balance should be unchanged by an internal transfer, got %d", report.AggregateClientBalanceInMinorUnits)
	}
}

func TestInterClientTransferRejectsNonClientDestination(t *testing.T) {
	_, guard := newTestGuard()
	_ = guard.PostClientMoneyMovement("acct-001", 500_000, "fund")

	transferError := guard.PostInterClientTransfer("acct-001", "firm-clearing-acct", 100_000, "attempted leak")
	if !errors.Is(transferError, ErrUnclassifiedAccount) {
		t.Errorf("expected ErrUnclassifiedAccount, got %v", transferError)
	}

	// And the money must not have moved.
	report, _ := guard.CheckSegregationInvariant()
	if report.AggregateClientBalanceInMinorUnits != 500_000 {
		t.Errorf("rejected transfer should not have moved money, aggregate=%d", report.AggregateClientBalanceInMinorUnits)
	}
}

func TestInterClientTransferRejectsNonPositiveAmount(t *testing.T) {
	_, guard := newTestGuard()
	if transferError := guard.PostInterClientTransfer("acct-001", "acct-002", 0, "bad"); !errors.Is(transferError, ErrInvalidMovementAmount) {
		t.Errorf("expected ErrInvalidMovementAmount for zero amount, got %v", transferError)
	}
	if transferError := guard.PostInterClientTransfer("acct-001", "acct-002", -50, "bad"); !errors.Is(transferError, ErrInvalidMovementAmount) {
		t.Errorf("expected ErrInvalidMovementAmount for negative amount, got %v", transferError)
	}
}

func TestValidateEntryPreservesSegregationAcceptsProperlySegregatedDeposit(t *testing.T) {
	_, guard := newTestGuard()
	entry := doubleentry.JournalEntry{
		HumanReadableDescription: "properly segregated deposit",
		DebitLines: []doubleentry.LedgerAccountLine{
			{LedgerAccountIdentifier: "acct-001", AmountInMinorUnits: 1000},
			{LedgerAccountIdentifier: "client-money-custody-pool", AmountInMinorUnits: 1000},
		},
		CreditLines: []doubleentry.LedgerAccountLine{
			{LedgerAccountIdentifier: "external-cash-suspense", AmountInMinorUnits: 2000},
		},
	}
	if validationError := guard.ValidateEntryPreservesSegregation(entry); validationError != nil {
		t.Errorf("expected properly-segregated entry to validate, got: %v", validationError)
	}
}

func TestValidateEntryPreservesSegregationRejectsDepositThatBypassesCustodyPool(t *testing.T) {
	_, guard := newTestGuard()
	// This is exactly the shape of the OLD (pre-segregation) demo funding
	// pattern used elsewhere in this repo: it moves money into a client
	// account straight from the firm's own operating account, never
	// touching the custody pool at all.
	entry := doubleentry.JournalEntry{
		HumanReadableDescription: "old-style funding that bypasses custody",
		DebitLines: []doubleentry.LedgerAccountLine{
			{LedgerAccountIdentifier: "acct-001", AmountInMinorUnits: 1000},
		},
		CreditLines: []doubleentry.LedgerAccountLine{
			{LedgerAccountIdentifier: "firm-clearing-acct", AmountInMinorUnits: 1000},
		},
	}
	validationError := guard.ValidateEntryPreservesSegregation(entry)
	if !errors.Is(validationError, ErrWouldBreakSegregation) {
		t.Errorf("expected ErrWouldBreakSegregation, got %v", validationError)
	}
}

func TestValidateEntryPreservesSegregationAcceptsClientToClientTransfer(t *testing.T) {
	_, guard := newTestGuard()
	entry := doubleentry.JournalEntry{
		HumanReadableDescription: "trade settlement between two clients",
		DebitLines: []doubleentry.LedgerAccountLine{
			{LedgerAccountIdentifier: "acct-002", AmountInMinorUnits: 250},
		},
		CreditLines: []doubleentry.LedgerAccountLine{
			{LedgerAccountIdentifier: "acct-001", AmountInMinorUnits: 250},
		},
	}
	if validationError := guard.ValidateEntryPreservesSegregation(entry); validationError != nil {
		t.Errorf("expected client-to-client transfer to validate cleanly, got: %v", validationError)
	}
}

func TestValidateEntryPreservesSegregationAcceptsFirmOnlyEntry(t *testing.T) {
	ledgerBook, guard := newTestGuard()
	_ = ledgerBook // firm-only accounts already seeded
	entry := doubleentry.JournalEntry{
		HumanReadableDescription: "firm-internal entry, no clients involved",
		DebitLines: []doubleentry.LedgerAccountLine{
			{LedgerAccountIdentifier: "firm-clearing-acct", AmountInMinorUnits: 500},
		},
		CreditLines: []doubleentry.LedgerAccountLine{
			{LedgerAccountIdentifier: "firm-clearing-acct", AmountInMinorUnits: 500},
		},
	}
	if validationError := guard.ValidateEntryPreservesSegregation(entry); validationError != nil {
		t.Errorf("expected firm-only entry to validate cleanly, got: %v", validationError)
	}
}

func TestAccountKindOfReportsClassification(t *testing.T) {
	_, guard := newTestGuard()

	if kind, isClassified := guard.AccountKindOf("acct-001"); !isClassified || kind != AccountKindClient {
		t.Errorf("expected acct-001 classified as CLIENT, got kind=%v classified=%v", kind, isClassified)
	}
	if kind, isClassified := guard.AccountKindOf("firm-clearing-acct"); !isClassified || kind != AccountKindFirm {
		t.Errorf("expected firm-clearing-acct classified as FIRM, got kind=%v classified=%v", kind, isClassified)
	}
	if _, isClassified := guard.AccountKindOf("nonexistent-acct"); isClassified {
		t.Errorf("expected nonexistent-acct to be unclassified")
	}
}
