package riskengine

import (
	"sync"
	"testing"
)

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

func TestAdjustAvailableMarginAppliesSignedDeltaAndCreatesUnknownAccounts(t *testing.T) {
	riskEngineUnderTest := NewPreTradeRiskEngineWithSeedBalances(map[string]int64{
		"acct-001": 100_000,
	})

	riskEngineUnderTest.AdjustAvailableMarginInMinorUnits("acct-001", 25_000)
	if available, _ := riskEngineUnderTest.AvailableMarginInMinorUnits("acct-001"); available != 125_000 {
		t.Fatalf("expected available margin 125000 after positive delta, got %d", available)
	}

	riskEngineUnderTest.AdjustAvailableMarginInMinorUnits("acct-001", -25_000)
	if available, _ := riskEngineUnderTest.AvailableMarginInMinorUnits("acct-001"); available != 100_000 {
		t.Fatalf("expected available margin back to 100000 after negative delta, got %d", available)
	}

	riskEngineUnderTest.AdjustAvailableMarginInMinorUnits("acct-brand-new", 5_000)
	if available, known := riskEngineUnderTest.AvailableMarginInMinorUnits("acct-brand-new"); !known || available != 5_000 {
		t.Fatalf("expected a previously-unknown account to be created with the delta as its balance, got available=%d known=%v", available, known)
	}
}

// TestEvaluateOrderAgainstAvailableMargin_ApprovalReservesMarginImmediately
// is the direct reproduction of the confirmed TOCTOU bug: two orders for
// the SAME account, each individually within the account's balance but
// together exceeding it, must NOT both be approved. The old
// read-only-check-only implementation approved both (never mutating the
// cache); the fix must reserve (debit) on the first approval so the
// second evaluation sees the reduced balance.
func TestEvaluateOrderAgainstAvailableMargin_ApprovalReservesMarginImmediately(t *testing.T) {
	riskEngineUnderTest := NewPreTradeRiskEngineWithSeedBalances(map[string]int64{
		"acct-001": 100_000,
	})

	firstOutcome := riskEngineUnderTest.EvaluateOrderAgainstAvailableMargin("acct-001", 60_000)
	if !firstOutcome.IsOrderApproved {
		t.Fatalf("expected first order (60000 within 100000 balance) to be approved, got rejection: %s", firstOutcome.HumanReadableRejectionReason)
	}

	// A second, concurrent-in-spirit order for the SAME account: 60000 +
	// 60000 = 120000 > the account's 100000 balance. This MUST now be
	// rejected -- if the first approval hadn't reserved anything (the
	// bug), this would wrongly also be approved.
	secondOutcome := riskEngineUnderTest.EvaluateOrderAgainstAvailableMargin("acct-001", 60_000)
	if secondOutcome.IsOrderApproved {
		t.Fatal("expected second order to be REJECTED: the first approval must have reserved (debited) its notional immediately, not left the cached balance untouched")
	}

	availableAfter, _ := riskEngineUnderTest.AvailableMarginInMinorUnits("acct-001")
	if availableAfter != 40_000 {
		t.Fatalf("expected available margin to be reserved down to 40000 (100000-60000) after the first approval, got %d", availableAfter)
	}
}

// TestConcurrentEvaluateOrderAgainstAvailableMargin_NeverOverApproves
// hammers the same account with many concurrent evaluations whose
// combined notional vastly exceeds the account's balance, and asserts
// the total APPROVED notional never exceeds the seeded balance —
// impossible to guarantee under a read-only (RLock-only) check, only
// under an atomic check-and-reserve.
func TestConcurrentEvaluateOrderAgainstAvailableMargin_NeverOverApproves(t *testing.T) {
	const seedBalance = 100_000
	const perOrderNotional = 1_000
	const numberOfConcurrentOrders = 200 // 200 * 1000 = 200000, double the balance

	riskEngineUnderTest := NewPreTradeRiskEngineWithSeedBalances(map[string]int64{
		"acct-001": seedBalance,
	})

	var waitGroup sync.WaitGroup
	var mutexGuardingApprovedCount sync.Mutex
	approvedCount := 0

	for i := 0; i < numberOfConcurrentOrders; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			outcome := riskEngineUnderTest.EvaluateOrderAgainstAvailableMargin("acct-001", perOrderNotional)
			if outcome.IsOrderApproved {
				mutexGuardingApprovedCount.Lock()
				approvedCount++
				mutexGuardingApprovedCount.Unlock()
			}
		}()
	}
	waitGroup.Wait()

	totalApprovedNotional := approvedCount * perOrderNotional
	if totalApprovedNotional > seedBalance {
		t.Fatalf("over-approved: %d orders approved (%d total notional) against a balance of %d", approvedCount, totalApprovedNotional, seedBalance)
	}
	if approvedCount != seedBalance/perOrderNotional {
		t.Fatalf("expected exactly %d orders approved (fully consuming the balance), got %d", seedBalance/perOrderNotional, approvedCount)
	}

	availableAfter, _ := riskEngineUnderTest.AvailableMarginInMinorUnits("acct-001")
	if availableAfter != 0 {
		t.Fatalf("expected available margin fully consumed (0), got %d", availableAfter)
	}
}

func TestReleaseReservedMarginGivesBackCapacity(t *testing.T) {
	riskEngineUnderTest := NewPreTradeRiskEngineWithSeedBalances(map[string]int64{
		"acct-001": 100_000,
	})

	outcome := riskEngineUnderTest.EvaluateOrderAgainstAvailableMargin("acct-001", 100_000)
	if !outcome.IsOrderApproved {
		t.Fatalf("expected approval, got: %s", outcome.HumanReadableRejectionReason)
	}
	if rejected := riskEngineUnderTest.EvaluateOrderAgainstAvailableMargin("acct-001", 1); rejected.IsOrderApproved {
		t.Fatal("expected balance to be fully reserved after the first approval")
	}

	riskEngineUnderTest.ReleaseReservedMargin("acct-001", 100_000)

	if restored := riskEngineUnderTest.EvaluateOrderAgainstAvailableMargin("acct-001", 100_000); !restored.IsOrderApproved {
		t.Fatal("expected ReleaseReservedMargin to give back the full reservation")
	}
}

func TestAvailableMarginInMinorUnitsReportsUnknownAccounts(t *testing.T) {
	riskEngineUnderTest := NewPreTradeRiskEngineWithSeedBalances(map[string]int64{})

	if available, known := riskEngineUnderTest.AvailableMarginInMinorUnits("nobody"); known || available != 0 {
		t.Fatalf("expected unknown account to report known=false available=0, got available=%d known=%v", available, known)
	}
}
