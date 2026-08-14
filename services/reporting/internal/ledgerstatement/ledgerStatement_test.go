package ledgerstatement

import (
	"testing"
	"time"

	"mercurius/reporting/internal/ledgerclient"
	"mercurius/reporting/internal/omsgatewayclient"
)

func date(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestMovementsFromConfirmedDepositsExcludesPending(t *testing.T) {
	deposits := []ledgerclient.DepositWireFormat{
		{DepositId: "d1", AmountInMinorUnits: 100000, Status: "CONFIRMED", ConfirmedAt: "2025-01-05T10:00:00Z", InitiatedAt: "2025-01-05T09:00:00Z"},
		{DepositId: "d2", AmountInMinorUnits: 50000, Status: "PENDING", InitiatedAt: "2025-01-06T09:00:00Z"},
	}
	movements, err := MovementsFromConfirmedDeposits(deposits)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(movements) != 1 || movements[0].AmountInMinorUnits != 100000 {
		t.Fatalf("unexpected movements: %+v", movements)
	}
}

func TestMovementsFromCompletedWithdrawalsAreNegative(t *testing.T) {
	withdrawals := []ledgerclient.WithdrawalWireFormat{
		{WithdrawalId: "w1", AmountInMinorUnits: 20000, Status: "COMPLETED", EligibleForPayoutAt: "2025-01-10T00:00:00Z"},
		{WithdrawalId: "w2", AmountInMinorUnits: 10000, Status: "PENDING_HOLD", EligibleForPayoutAt: "2025-01-12T00:00:00Z"},
	}
	movements, err := MovementsFromCompletedWithdrawals(withdrawals)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(movements) != 1 || movements[0].AmountInMinorUnits != -20000 {
		t.Fatalf("unexpected movements: %+v", movements)
	}
}

func TestMovementsFromDividendCreditsOnlyIncludesCashDividends(t *testing.T) {
	actions := []omsgatewayclient.ProcessedActionWireFormat{
		{ActionType: "CASH_DIVIDEND", InstrumentSymbol: "ITC", CashCreditedInMinorUnits: 5000, ProcessedAtTime: date("2025-01-15T00:00:00Z")},
		{ActionType: "STOCK_SPLIT", InstrumentSymbol: "ITC"},
	}
	movements := MovementsFromDividendCredits(actions)
	if len(movements) != 1 || movements[0].AmountInMinorUnits != 5000 {
		t.Fatalf("unexpected movements: %+v", movements)
	}
}

func TestBuildStatementRunningBalanceAndRangeFiltering(t *testing.T) {
	movements := []Movement{
		{MovementType: MovementTypeDeposit, OccurredAtTime: date("2025-01-01T00:00:00Z"), AmountInMinorUnits: 100000},
		{MovementType: MovementTypeTradeSettlement, OccurredAtTime: date("2025-01-10T00:00:00Z"), AmountInMinorUnits: -20000},
		{MovementType: MovementTypeDividendCredit, OccurredAtTime: date("2025-01-20T00:00:00Z"), AmountInMinorUnits: 500},
		{MovementType: MovementTypeWithdrawal, OccurredAtTime: date("2025-02-05T00:00:00Z"), AmountInMinorUnits: -30000},
	}

	statement := BuildStatement("acct-001", date("2025-01-05T00:00:00Z"), date("2025-01-25T00:00:00Z"), movements, 50500)

	if statement.OpeningBalanceInMinorUnits != 100000 {
		t.Errorf("expected opening balance 100000, got %d", statement.OpeningBalanceInMinorUnits)
	}
	if len(statement.Rows) != 2 {
		t.Fatalf("expected 2 rows within range, got %d: %+v", len(statement.Rows), statement.Rows)
	}
	if statement.Rows[0].RunningBalanceInMinorUnits != 80000 {
		t.Errorf("expected running balance 80000 after trade settlement, got %d", statement.Rows[0].RunningBalanceInMinorUnits)
	}
	if statement.Rows[1].RunningBalanceInMinorUnits != 80500 {
		t.Errorf("expected running balance 80500 after dividend credit, got %d", statement.Rows[1].RunningBalanceInMinorUnits)
	}
	if statement.ClosingBalanceInMinorUnits != 80500 {
		t.Errorf("expected closing balance 80500, got %d", statement.ClosingBalanceInMinorUnits)
	}
	// Final replayed balance (including the Feb withdrawal after
	// endDate) is 80500 - 30000 = 50500, matching
	// ledgerReportedCurrentBalanceInMinorUnits exactly.
	if statement.ReconciliationNote == "" {
		t.Error("expected a reconciliation note")
	}
	wantNote := "Replayed balance from all known real movements matches ledger's real current balance exactly."
	if statement.ReconciliationNote != wantNote {
		t.Errorf("expected exact-match reconciliation note, got %q", statement.ReconciliationNote)
	}
}

func TestBuildStatementReportsMismatchHonestly(t *testing.T) {
	movements := []Movement{
		{MovementType: MovementTypeDeposit, OccurredAtTime: date("2025-01-01T00:00:00Z"), AmountInMinorUnits: 100000},
	}
	statement := BuildStatement("acct-001", date("2025-01-01T00:00:00Z"), date("2025-01-31T00:00:00Z"), movements, 999999)
	if statement.ReconciliationNote == "" {
		t.Fatal("expected a non-empty mismatch reconciliation note")
	}
	if statement.ClosingBalanceInMinorUnits == statement.LedgerReportedCurrentBalanceInMinorUnits {
		t.Fatal("test setup should produce a genuine mismatch")
	}
}
