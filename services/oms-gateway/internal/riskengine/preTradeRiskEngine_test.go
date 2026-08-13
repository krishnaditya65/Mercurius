package riskengine

import "testing"

func TestOrderWithinAvailableMarginIsApproved(t *testing.T) {
	riskEngineUnderTest := NewPreTradeRiskEngineWithSeedBalances(map[string]int64{
		"acct-001": 100_000,
	})

	outcome := riskEngineUnderTest.EvaluateOrderAgainstAvailableMargin("acct-001", 50_000)

	if !outcome.IsOrderApproved {
		t.Fatalf("expected order to be approved, got rejection: %s", outcome.HumanReadableRejectionReason)
	}
}

func TestOrderExceedingAvailableMarginIsRejectedWithPlainLanguageReason(t *testing.T) {
	riskEngineUnderTest := NewPreTradeRiskEngineWithSeedBalances(map[string]int64{
		"acct-001": 10_000,
	})

	outcome := riskEngineUnderTest.EvaluateOrderAgainstAvailableMargin("acct-001", 50_000)

	if outcome.IsOrderApproved {
		t.Fatal("expected order to be rejected for insufficient margin")
	}
	if outcome.MachineReadableRejectionReason != "INSUFFICIENT_MARGIN" {
		t.Fatalf("unexpected machine-readable reason: %s", outcome.MachineReadableRejectionReason)
	}
	if outcome.HumanReadableRejectionReason == "" {
		t.Fatal("human-readable rejection reason must never be empty — see FEATURES.md §21")
	}
}

func TestOrderForUnknownAccountIsRejected(t *testing.T) {
	riskEngineUnderTest := NewPreTradeRiskEngineWithSeedBalances(map[string]int64{})

	outcome := riskEngineUnderTest.EvaluateOrderAgainstAvailableMargin("acct-does-not-exist", 1_000)

	if outcome.IsOrderApproved {
		t.Fatal("expected order for unknown account to be rejected")
	}
	if outcome.MachineReadableRejectionReason != "ACCOUNT_NOT_FOUND" {
		t.Fatalf("unexpected machine-readable reason: %s", outcome.MachineReadableRejectionReason)
	}
}

func TestRefreshAccountBalanceFromLedgerOverwritesAndAddsAccounts(t *testing.T) {
	riskEngineUnderTest := NewPreTradeRiskEngineWithSeedBalances(map[string]int64{
		"acct-001": 1,
	})

	riskEngineUnderTest.RefreshAccountBalanceFromLedger("acct-001", 500_000)
	riskEngineUnderTest.RefreshAccountBalanceFromLedger("acct-new", 250_000)

	if outcome := riskEngineUnderTest.EvaluateOrderAgainstAvailableMargin("acct-001", 500_000); !outcome.IsOrderApproved {
		t.Fatal("expected refreshed balance to be usable immediately")
	}
	if outcome := riskEngineUnderTest.EvaluateOrderAgainstAvailableMargin("acct-new", 250_000); !outcome.IsOrderApproved {
		t.Fatal("expected an account not previously known to be usable after a refresh")
	}
}

func TestApplyTradeSettlementDebitsBuyerAndCreditsSeller(t *testing.T) {
	riskEngineUnderTest := NewPreTradeRiskEngineWithSeedBalances(map[string]int64{
		"buyer":  100_000,
		"seller": 100_000,
	})

	riskEngineUnderTest.ApplyTradeSettlementToLocalCache("buyer", "seller", 40_000)

	if outcome := riskEngineUnderTest.EvaluateOrderAgainstAvailableMargin("buyer", 60_001); outcome.IsOrderApproved {
		t.Fatal("expected buyer's margin to be reduced by the executed notional")
	}
	if outcome := riskEngineUnderTest.EvaluateOrderAgainstAvailableMargin("seller", 140_000); !outcome.IsOrderApproved {
		t.Fatal("expected seller's margin to be increased by the executed notional")
	}
}
