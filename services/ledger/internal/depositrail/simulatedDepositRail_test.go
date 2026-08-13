package depositrail

import (
	"testing"
	"time"

	"mercurius/ledger/internal/doubleentry"
	"mercurius/ledger/internal/fundsegregation"
)

var testNow = time.Unix(1_700_000_000, 0)

func newTestRail(t *testing.T) (*SimulatedDepositRail, *doubleentry.InMemoryDoubleEntryLedgerBook, *fundsegregation.SegregationGuard) {
	t.Helper()
	ledgerBook := doubleentry.NewInMemoryDoubleEntryLedgerBookWithAccounts([]string{
		"acct-001", "acct-002", "firm-clearing-acct", "client-money-custody-pool", "external-cash-suspense",
	})
	segregationGuard := fundsegregation.NewSegregationGuard(
		ledgerBook,
		"client-money-custody-pool",
		"external-cash-suspense",
		[]string{"acct-001", "acct-002"},
		[]string{"firm-clearing-acct"},
	)
	rail := NewSimulatedDepositRail(segregationGuard)
	return rail, ledgerBook, segregationGuard
}

func TestInitiateDepositStartsPendingAndMovesNoMoney(t *testing.T) {
	rail, ledgerBook, _ := newTestRail(t)

	deposit, err := rail.InitiateDeposit("acct-001", DepositMethodUpi, 50_000, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deposit.Status != DepositStatusPending {
		t.Fatalf("expected PENDING, got %v", deposit.Status)
	}

	balance, _ := ledgerBook.CurrentBalanceInMinorUnits("acct-001")
	if balance != 0 {
		t.Fatalf("expected initiating a deposit to move NO money, got balance %d", balance)
	}
}

func TestInitiateDepositRejectsNonPositiveAmount(t *testing.T) {
	rail, _, _ := newTestRail(t)
	_, err := rail.InitiateDeposit("acct-001", DepositMethodUpi, 0, testNow)
	if err != ErrInvalidDepositAmount {
		t.Fatalf("expected ErrInvalidDepositAmount for zero, got %v", err)
	}
	_, err = rail.InitiateDeposit("acct-001", DepositMethodUpi, -10, testNow)
	if err != ErrInvalidDepositAmount {
		t.Fatalf("expected ErrInvalidDepositAmount for negative, got %v", err)
	}
}

func TestInitiateDepositRejectsMissingAccountIdentifier(t *testing.T) {
	rail, _, _ := newTestRail(t)
	_, err := rail.InitiateDeposit("", DepositMethodUpi, 1000, testNow)
	if err != ErrMissingAccountIdentifier {
		t.Fatalf("expected ErrMissingAccountIdentifier, got %v", err)
	}
}

func TestInitiateDepositRejectsInvalidMethod(t *testing.T) {
	rail, _, _ := newTestRail(t)
	_, err := rail.InitiateDeposit("acct-001", DepositMethod("SWIFT"), 1000, testNow)
	if err != ErrInvalidDepositMethod {
		t.Fatalf("expected ErrInvalidDepositMethod, got %v", err)
	}
}

func TestAllFourValidDepositMethodsAreAccepted(t *testing.T) {
	rail, _, _ := newTestRail(t)
	for _, method := range []DepositMethod{DepositMethodUpi, DepositMethodNeft, DepositMethodImps, DepositMethodNetbanking} {
		if _, err := rail.InitiateDeposit("acct-001", method, 1000, testNow); err != nil {
			t.Fatalf("expected method %v to be accepted, got %v", method, err)
		}
	}
}

func TestConfirmDepositActuallyMovesRealMoneyThroughSegregationGuard(t *testing.T) {
	rail, ledgerBook, segregationGuard := newTestRail(t)
	deposit, _ := rail.InitiateDeposit("acct-001", DepositMethodNeft, 75_000, testNow)

	confirmed, err := rail.ConfirmDeposit(deposit.DepositId, testNow.Add(time.Minute))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if confirmed.Status != DepositStatusConfirmed {
		t.Fatalf("expected CONFIRMED, got %v", confirmed.Status)
	}

	balance, _ := ledgerBook.CurrentBalanceInMinorUnits("acct-001")
	if balance != 75_000 {
		t.Fatalf("expected confirming the deposit to actually move money, got balance %d", balance)
	}

	report, reportErr := segregationGuard.CheckSegregationInvariant()
	if reportErr != nil {
		t.Fatalf("unexpected error: %v", reportErr)
	}
	if !report.IsSegregationIntact {
		t.Fatalf("expected segregation invariant to stay intact, got discrepancy %d", report.DiscrepancyInMinorUnits)
	}
}

func TestConfirmingAnAlreadyConfirmedDepositIsRejected(t *testing.T) {
	rail, _, _ := newTestRail(t)
	deposit, _ := rail.InitiateDeposit("acct-001", DepositMethodUpi, 10_000, testNow)
	if _, err := rail.ConfirmDeposit(deposit.DepositId, testNow); err != nil {
		t.Fatalf("unexpected error on first confirm: %v", err)
	}

	_, err := rail.ConfirmDeposit(deposit.DepositId, testNow)
	if err != ErrDepositAlreadyConfirmed {
		t.Fatalf("expected ErrDepositAlreadyConfirmed, got %v", err)
	}
}

func TestConfirmingAnUnknownDepositFails(t *testing.T) {
	rail, _, _ := newTestRail(t)
	_, err := rail.ConfirmDeposit("never-initiated", testNow)
	if err != ErrDepositNotFound {
		t.Fatalf("expected ErrDepositNotFound, got %v", err)
	}
}

func TestDoubleConfirmDoesNotDoublePostMoney(t *testing.T) {
	rail, ledgerBook, _ := newTestRail(t)
	deposit, _ := rail.InitiateDeposit("acct-001", DepositMethodImps, 20_000, testNow)
	rail.ConfirmDeposit(deposit.DepositId, testNow)
	rail.ConfirmDeposit(deposit.DepositId, testNow) // rejected, should be a no-op on the ledger

	balance, _ := ledgerBook.CurrentBalanceInMinorUnits("acct-001")
	if balance != 20_000 {
		t.Fatalf("expected balance to reflect exactly ONE posting, got %d", balance)
	}
}

func TestDepositsForAccountReturnsOnlyThatAccountsDepositsInInitiatedOrder(t *testing.T) {
	rail, _, _ := newTestRail(t)
	first, _ := rail.InitiateDeposit("acct-001", DepositMethodUpi, 1000, testNow)
	second, _ := rail.InitiateDeposit("acct-001", DepositMethodNeft, 2000, testNow.Add(time.Minute))
	rail.InitiateDeposit("acct-002", DepositMethodImps, 3000, testNow)

	deposits := rail.DepositsForAccount("acct-001")
	if len(deposits) != 2 {
		t.Fatalf("expected 2 deposits for acct-001, got %d", len(deposits))
	}
	if deposits[0].DepositId != first.DepositId || deposits[1].DepositId != second.DepositId {
		t.Fatal("expected deposits ordered by InitiatedAt")
	}
}

func TestLookupDepositReturnsNotFoundForUnknownId(t *testing.T) {
	rail, _, _ := newTestRail(t)
	_, wasFound := rail.LookupDeposit("never-initiated")
	if wasFound {
		t.Fatal("expected not found for an unknown deposit id")
	}
}

func TestConfirmingIntoAnUnclassifiedAccountFails(t *testing.T) {
	rail, _, _ := newTestRail(t)
	// firm-clearing-acct exists in the ledger but is NOT classified
	// CLIENT — the segregation guard must reject a deposit confirmation
	// targeting it, and the deposit should NOT be left CONFIRMED.
	deposit, _ := rail.InitiateDeposit("firm-clearing-acct", DepositMethodUpi, 5000, testNow)

	_, err := rail.ConfirmDeposit(deposit.DepositId, testNow)
	if err == nil {
		t.Fatal("expected an error confirming a deposit into a non-CLIENT account")
	}

	reloaded, _ := rail.LookupDeposit(deposit.DepositId)
	if reloaded.Status != DepositStatusPending {
		t.Fatalf("expected the deposit to remain PENDING after a failed confirm, got %v", reloaded.Status)
	}
}

func TestConfirmDepositReflectsInSegregationReportAcrossMultipleDeposits(t *testing.T) {
	rail, _, segregationGuard := newTestRail(t)
	deposit1, _ := rail.InitiateDeposit("acct-001", DepositMethodUpi, 30_000, testNow)
	deposit2, _ := rail.InitiateDeposit("acct-002", DepositMethodNetbanking, 40_000, testNow)

	rail.ConfirmDeposit(deposit1.DepositId, testNow)
	rail.ConfirmDeposit(deposit2.DepositId, testNow)

	report, _ := segregationGuard.CheckSegregationInvariant()
	if report.AggregateClientBalanceInMinorUnits != 70_000 {
		t.Fatalf("expected aggregate client balance 70000, got %d", report.AggregateClientBalanceInMinorUnits)
	}
	if !report.IsSegregationIntact {
		t.Fatal("expected segregation to remain intact across multiple confirmed deposits")
	}
}
