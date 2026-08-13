package drip

import (
	"errors"
	"sync"
	"testing"
)

func TestCalculateDividendCredit_HandWorked(t *testing.T) {
	credit, err := CalculateDividendCredit(50, 200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if credit != 10000 {
		t.Fatalf("expected credit 10000 (50*200), got %d", credit)
	}
}

func TestCalculateDividendCredit_ZeroQuantityRejected(t *testing.T) {
	_, err := CalculateDividendCredit(0, 200)
	if !errors.Is(err, ErrQuantityHeldMustBePositive) {
		t.Fatalf("expected ErrQuantityHeldMustBePositive, got %v", err)
	}
}

func TestCalculateDividendCredit_NonPositiveDividendRejected(t *testing.T) {
	_, err := CalculateDividendCredit(50, 0)
	if !errors.Is(err, ErrDividendPerShareMustBePositive) {
		t.Fatalf("expected ErrDividendPerShareMustBePositive, got %v", err)
	}
	_, err = CalculateDividendCredit(50, -10)
	if !errors.Is(err, ErrDividendPerShareMustBePositive) {
		t.Fatalf("expected ErrDividendPerShareMustBePositive for negative, got %v", err)
	}
}

func TestCalculateReinvestmentQuantity_ExactDivision(t *testing.T) {
	plan, err := CalculateReinvestmentQuantity(10000, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.ReinvestmentQuantity != 100 {
		t.Fatalf("expected quantity 100, got %d", plan.ReinvestmentQuantity)
	}
	if plan.SpentCashInMinorUnits != 10000 {
		t.Fatalf("expected spent 10000, got %d", plan.SpentCashInMinorUnits)
	}
	if plan.LeftoverCashInMinorUnits != 0 {
		t.Fatalf("expected zero leftover, got %d", plan.LeftoverCashInMinorUnits)
	}
}

func TestCalculateReinvestmentQuantity_WithLeftover(t *testing.T) {
	// 10099 / 100 = 100 shares, spent 10000, leftover 99
	plan, err := CalculateReinvestmentQuantity(10099, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.ReinvestmentQuantity != 100 {
		t.Fatalf("expected quantity 100, got %d", plan.ReinvestmentQuantity)
	}
	if plan.LeftoverCashInMinorUnits != 99 {
		t.Fatalf("expected leftover 99, got %d", plan.LeftoverCashInMinorUnits)
	}
}

func TestCalculateReinvestmentQuantity_CashLessThanOneSharePrice(t *testing.T) {
	plan, err := CalculateReinvestmentQuantity(50, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.ReinvestmentQuantity != 0 {
		t.Fatalf("expected zero reinvestment quantity, got %d", plan.ReinvestmentQuantity)
	}
	if plan.LeftoverCashInMinorUnits != 50 {
		t.Fatalf("expected all 50 leftover, got %d", plan.LeftoverCashInMinorUnits)
	}
}

func TestCalculateReinvestmentQuantity_NonPositiveCashRejected(t *testing.T) {
	_, err := CalculateReinvestmentQuantity(0, 100)
	if !errors.Is(err, ErrCashAmountMustBePositive) {
		t.Fatalf("expected ErrCashAmountMustBePositive, got %v", err)
	}
}

func TestCalculateReinvestmentQuantity_NonPositiveReferencePriceRejected(t *testing.T) {
	_, err := CalculateReinvestmentQuantity(1000, 0)
	if !errors.Is(err, ErrReferencePriceMustBePositive) {
		t.Fatalf("expected ErrReferencePriceMustBePositive, got %v", err)
	}
}

func TestToggleRegistry_DefaultsToOff(t *testing.T) {
	registry := NewToggleRegistry()
	if registry.IsAutoReinvestEnabled("acct-001") {
		t.Fatalf("expected default auto-reinvest OFF for an untoggled account")
	}
}

func TestToggleRegistry_SetAndRead(t *testing.T) {
	registry := NewToggleRegistry()
	registry.SetAutoReinvest("acct-001", true)
	if !registry.IsAutoReinvestEnabled("acct-001") {
		t.Fatalf("expected auto-reinvest ON after SetAutoReinvest(true)")
	}
	registry.SetAutoReinvest("acct-001", false)
	if registry.IsAutoReinvestEnabled("acct-001") {
		t.Fatalf("expected auto-reinvest OFF after SetAutoReinvest(false)")
	}
}

func TestToggleRegistry_PerAccountIndependence(t *testing.T) {
	registry := NewToggleRegistry()
	registry.SetAutoReinvest("acct-001", true)
	if registry.IsAutoReinvestEnabled("acct-002") {
		t.Fatalf("expected acct-002 unaffected by acct-001's toggle")
	}
}

func TestToggleRegistry_Concurrency(t *testing.T) {
	registry := NewToggleRegistry()
	var waitGroup sync.WaitGroup
	for i := 0; i < 100; i++ {
		waitGroup.Add(1)
		go func(enabled bool) {
			defer waitGroup.Done()
			registry.SetAutoReinvest("acct-shared", enabled)
			_ = registry.IsAutoReinvestEnabled("acct-shared")
		}(i%2 == 0)
	}
	waitGroup.Wait()
	// No assertion on final value (racy by design) -- this test exists to
	// prove -race finds no data race, not to pin a specific outcome.
}
