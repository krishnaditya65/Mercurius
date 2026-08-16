package multicurrencywallet

import (
	"sync"
	"errors"
	"testing"

	"mercurius/ledger/internal/doubleentry"
	"mercurius/ledger/internal/fundsegregation"
)

func newTestRegistry() (*doubleentry.InMemoryDoubleEntryLedgerBook, *fundsegregation.SegregationGuard, *MultiCurrencyWalletRegistry) {
	ledgerBook := doubleentry.NewInMemoryDoubleEntryLedgerBookWithAccounts([]string{
		"acct-001",
		"acct-002",
		"firm-clearing-acct",
		"client-money-custody-pool",
		"external-cash-suspense",
	})
	guard := fundsegregation.NewSegregationGuard(
		ledgerBook,
		"client-money-custody-pool",
		"external-cash-suspense",
		[]string{"acct-001", "acct-002"},
		[]string{"firm-clearing-acct"},
	)
	fxRateTable := NewStaticFxRateTable(map[string]float64{
		"USD/INR": 83.0,
		"INR/USD": 1.0 / 83.0,
	})
	registry := NewMultiCurrencyWalletRegistry(
		ledgerBook,
		guard,
		"client-money-custody-pool",
		"wallet-external-cash-suspense",
		"fx-conversion-clearing-acct",
		"INR",
		fxRateTable,
	)
	return ledgerBook, guard, registry
}

// --- Deposits: opening a wallet lazily ---

func TestDepositIntoNewCurrencyWalletOpensItLazily(t *testing.T) {
	_, _, registry := newTestRegistry()

	if balances := registry.GetWalletBalancesForAccount("acct-001"); len(balances) != 0 {
		t.Fatalf("expected no wallets opened yet, got %v", balances)
	}

	if depositError := registry.DepositIntoCurrencyWallet("acct-001", "USD", 10_000); depositError != nil {
		t.Fatalf("expected USD deposit to succeed, got: %v", depositError)
	}

	balances := registry.GetWalletBalancesForAccount("acct-001")
	if len(balances) != 1 {
		t.Fatalf("expected exactly one opened wallet, got %v", balances)
	}
	if balances[0].CurrencyCode != "USD" || balances[0].BalanceInMinorUnits != 10_000 {
		t.Fatalf("expected USD wallet with balance 10000, got %+v", balances[0])
	}
}

func TestDepositIntoNativeCurrencyWalletOfClientAccountRoutesThroughSegregationGuard(t *testing.T) {
	ledgerBook, guard, registry := newTestRegistry()

	if depositError := registry.DepositIntoCurrencyWallet("acct-001", "INR", 500_000); depositError != nil {
		t.Fatalf("expected INR deposit to succeed, got: %v", depositError)
	}

	rawBalance, _ := ledgerBook.CurrentBalanceInMinorUnits("acct-001")
	if rawBalance != 500_000 {
		t.Fatalf("expected raw acct-001 balance 500000 (INR wallet aliases the raw account), got %d", rawBalance)
	}

	report, reportError := guard.CheckSegregationInvariant()
	if reportError != nil {
		t.Fatalf("unexpected error checking segregation invariant: %v", reportError)
	}
	if !report.IsSegregationIntact {
		t.Fatalf("expected segregation invariant to stay intact after an INR wallet deposit, got: %+v", report)
	}
}

func TestDepositIntoForeignCurrencyWalletDoesNotTouchNativeWalletOrRawBalance(t *testing.T) {
	ledgerBook, guard, registry := newTestRegistry()

	if depositError := registry.DepositIntoCurrencyWallet("acct-001", "INR", 500_000); depositError != nil {
		t.Fatalf("unexpected INR deposit error: %v", depositError)
	}
	if depositError := registry.DepositIntoCurrencyWallet("acct-001", "USD", 20_000); depositError != nil {
		t.Fatalf("unexpected USD deposit error: %v", depositError)
	}

	rawBalance, _ := ledgerBook.CurrentBalanceInMinorUnits("acct-001")
	if rawBalance != 500_000 {
		t.Fatalf("USD deposit must not touch the raw/native INR balance — expected 500000, got %d", rawBalance)
	}

	usdBalance, usdLookupError := ledgerBook.CurrentBalanceInMinorUnits("acct-001:USD")
	if usdLookupError != nil {
		t.Fatalf("expected acct-001:USD to have been registered as its own ledger account: %v", usdLookupError)
	}
	if usdBalance != 20_000 {
		t.Fatalf("expected acct-001:USD balance 20000, got %d", usdBalance)
	}

	report, _ := guard.CheckSegregationInvariant()
	if !report.IsSegregationIntact {
		t.Fatalf("expected segregation invariant to stay intact after a USD wallet deposit, got: %+v", report)
	}
}

func TestDepositRejectsNonPositiveAmount(t *testing.T) {
	_, _, registry := newTestRegistry()

	if depositError := registry.DepositIntoCurrencyWallet("acct-001", "USD", 0); !errors.Is(depositError, ErrInvalidWalletAmount) {
		t.Fatalf("expected ErrInvalidWalletAmount for zero amount, got: %v", depositError)
	}
	if depositError := registry.DepositIntoCurrencyWallet("acct-001", "USD", -100); !errors.Is(depositError, ErrInvalidWalletAmount) {
		t.Fatalf("expected ErrInvalidWalletAmount for negative amount, got: %v", depositError)
	}
}

func TestDepositRejectsMissingAccountIdentifierAndCurrencyCode(t *testing.T) {
	_, _, registry := newTestRegistry()

	if depositError := registry.DepositIntoCurrencyWallet("", "USD", 1_000); !errors.Is(depositError, ErrMissingAccountIdentifier) {
		t.Fatalf("expected ErrMissingAccountIdentifier, got: %v", depositError)
	}
	if depositError := registry.DepositIntoCurrencyWallet("acct-001", "", 1_000); !errors.Is(depositError, ErrMissingCurrencyCode) {
		t.Fatalf("expected ErrMissingCurrencyCode, got: %v", depositError)
	}
}

// --- Withdrawals: currency isolation ---

func TestWithdrawFromCurrencyWalletExceedingItsOwnBalanceIsRejectedEvenWithPlentyInAnotherCurrency(t *testing.T) {
	_, _, registry := newTestRegistry()

	// acct-001 has a healthy INR balance...
	if depositError := registry.DepositIntoCurrencyWallet("acct-001", "INR", 10_000_000); depositError != nil {
		t.Fatalf("unexpected INR deposit error: %v", depositError)
	}
	// ...but its USD wallet has never been funded at all.
	withdrawError := registry.WithdrawFromCurrencyWallet("acct-001", "USD", 1_000)
	if !errors.Is(withdrawError, ErrInsufficientCurrencyWalletBalance) {
		t.Fatalf("expected ErrInsufficientCurrencyWalletBalance, got: %v", withdrawError)
	}

	// A large INR balance must not have been touched by the rejected USD withdrawal.
	balances := registry.GetWalletBalancesForAccount("acct-001")
	if len(balances) != 1 || balances[0].CurrencyCode != "INR" || balances[0].BalanceInMinorUnits != 10_000_000 {
		t.Fatalf("expected INR balance untouched at 10000000, got %+v", balances)
	}
}

func TestWithdrawFromCurrencyWalletExceedingItsPartialBalanceIsRejected(t *testing.T) {
	_, _, registry := newTestRegistry()

	if depositError := registry.DepositIntoCurrencyWallet("acct-001", "USD", 5_000); depositError != nil {
		t.Fatalf("unexpected deposit error: %v", depositError)
	}
	if depositError := registry.DepositIntoCurrencyWallet("acct-001", "INR", 50_000_000); depositError != nil {
		t.Fatalf("unexpected deposit error: %v", depositError)
	}

	// USD balance is only 5000 — a 6000 USD withdrawal must fail even
	// though INR has a much larger balance.
	withdrawError := registry.WithdrawFromCurrencyWallet("acct-001", "USD", 6_000)
	if !errors.Is(withdrawError, ErrInsufficientCurrencyWalletBalance) {
		t.Fatalf("expected ErrInsufficientCurrencyWalletBalance, got: %v", withdrawError)
	}
}

func TestWithdrawFromCurrencyWalletWithinBalanceSucceedsAndDecrementsOnlyThatCurrency(t *testing.T) {
	ledgerBook, _, registry := newTestRegistry()

	if depositError := registry.DepositIntoCurrencyWallet("acct-001", "USD", 10_000); depositError != nil {
		t.Fatalf("unexpected deposit error: %v", depositError)
	}
	if depositError := registry.DepositIntoCurrencyWallet("acct-001", "INR", 1_000_000); depositError != nil {
		t.Fatalf("unexpected deposit error: %v", depositError)
	}

	if withdrawError := registry.WithdrawFromCurrencyWallet("acct-001", "USD", 4_000); withdrawError != nil {
		t.Fatalf("expected withdrawal to succeed, got: %v", withdrawError)
	}

	usdBalance, _ := ledgerBook.CurrentBalanceInMinorUnits("acct-001:USD")
	if usdBalance != 6_000 {
		t.Fatalf("expected USD balance 6000 after withdrawal, got %d", usdBalance)
	}
	inrBalance, _ := ledgerBook.CurrentBalanceInMinorUnits("acct-001")
	if inrBalance != 1_000_000 {
		t.Fatalf("INR balance must be untouched by a USD withdrawal, expected 1000000, got %d", inrBalance)
	}
}

func TestWithdrawRejectsNonPositiveAmount(t *testing.T) {
	_, _, registry := newTestRegistry()
	if withdrawError := registry.WithdrawFromCurrencyWallet("acct-001", "USD", 0); !errors.Is(withdrawError, ErrInvalidWalletAmount) {
		t.Fatalf("expected ErrInvalidWalletAmount, got: %v", withdrawError)
	}
}

// --- Currency conversion ---

func TestConvertBetweenCurrencyWalletsHandWorkedUsdToInrExample(t *testing.T) {
	ledgerBook, _, registry := newTestRegistry()

	// Fund acct-001's USD wallet with exactly 100.00 USD (2dp minor units).
	if depositError := registry.DepositIntoCurrencyWallet("acct-001", "USD", 10_000); depositError != nil {
		t.Fatalf("unexpected deposit error: %v", depositError)
	}

	result, convertError := registry.ConvertBetweenCurrencyWallets("acct-001", "USD", "INR", 10_000)
	if convertError != nil {
		t.Fatalf("expected conversion to succeed, got: %v", convertError)
	}

	// 100.00 USD * 83.0 = 8300.00 INR = 830000 minor units, EXACTLY —
	// no rounding surprises for this hand-worked example.
	if result.AmountCreditedToDestinationInMinorUnits != 830_000 {
		t.Fatalf("expected converted amount 830000, got %d", result.AmountCreditedToDestinationInMinorUnits)
	}
	if result.AmountDebitedFromSourceInMinorUnits != 10_000 {
		t.Fatalf("expected debited source amount 10000, got %d", result.AmountDebitedFromSourceInMinorUnits)
	}
	if result.RateApplied != 83.0 {
		t.Fatalf("expected rate 83.0, got %v", result.RateApplied)
	}

	usdBalance, _ := ledgerBook.CurrentBalanceInMinorUnits("acct-001:USD")
	if usdBalance != 0 {
		t.Fatalf("expected USD wallet fully drained to 0, got %d", usdBalance)
	}
	inrBalance, _ := ledgerBook.CurrentBalanceInMinorUnits("acct-001")
	if inrBalance != 830_000 {
		t.Fatalf("expected INR (raw acct-001) balance exactly 830000, got %d", inrBalance)
	}
}

func TestConvertBetweenCurrencyWalletsKeepsSegregationInvariantIntactForClientAccount(t *testing.T) {
	_, guard, registry := newTestRegistry()

	if depositError := registry.DepositIntoCurrencyWallet("acct-001", "USD", 10_000); depositError != nil {
		t.Fatalf("unexpected deposit error: %v", depositError)
	}
	if _, convertError := registry.ConvertBetweenCurrencyWallets("acct-001", "USD", "INR", 10_000); convertError != nil {
		t.Fatalf("unexpected conversion error: %v", convertError)
	}

	report, reportError := guard.CheckSegregationInvariant()
	if reportError != nil {
		t.Fatalf("unexpected error checking segregation invariant: %v", reportError)
	}
	if !report.IsSegregationIntact {
		t.Fatalf("expected segregation invariant to stay intact after a USD->INR conversion into a CLIENT account, got: %+v", report)
	}
	// The custody pool must have moved by exactly the converted (INR) amount,
	// since the destination leg (INR) is the account's CLIENT-classified
	// native-currency wallet.
	if report.CustodyPoolBalanceInMinorUnits != 830_000 {
		t.Fatalf("expected custody pool to have moved by the converted INR amount (830000), got %d", report.CustodyPoolBalanceInMinorUnits)
	}
}

func TestConvertRejectsUnknownCurrencyPair(t *testing.T) {
	_, _, registry := newTestRegistry()

	if depositError := registry.DepositIntoCurrencyWallet("acct-001", "GBP", 10_000); depositError != nil {
		t.Fatalf("unexpected deposit error: %v", depositError)
	}

	_, convertError := registry.ConvertBetweenCurrencyWallets("acct-001", "GBP", "JPY", 5_000)
	if !errors.Is(convertError, ErrUnknownCurrencyPair) {
		t.Fatalf("expected ErrUnknownCurrencyPair, got: %v", convertError)
	}
}

func TestConvertRejectsAmountExceedingSourceCurrencyBalance(t *testing.T) {
	_, _, registry := newTestRegistry()

	if depositError := registry.DepositIntoCurrencyWallet("acct-001", "USD", 1_000); depositError != nil {
		t.Fatalf("unexpected deposit error: %v", depositError)
	}

	_, convertError := registry.ConvertBetweenCurrencyWallets("acct-001", "USD", "INR", 2_000)
	if !errors.Is(convertError, ErrInsufficientCurrencyWalletBalance) {
		t.Fatalf("expected ErrInsufficientCurrencyWalletBalance, got: %v", convertError)
	}
}

func TestConvertRejectsSameCurrencyConversion(t *testing.T) {
	_, _, registry := newTestRegistry()
	_, convertError := registry.ConvertBetweenCurrencyWallets("acct-001", "USD", "USD", 1_000)
	if !errors.Is(convertError, ErrCannotConvertSameCurrency) {
		t.Fatalf("expected ErrCannotConvertSameCurrency, got: %v", convertError)
	}
}

func TestConvertRejectsNonPositiveAmount(t *testing.T) {
	_, _, registry := newTestRegistry()
	_, convertError := registry.ConvertBetweenCurrencyWallets("acct-001", "USD", "INR", 0)
	if !errors.Is(convertError, ErrInvalidWalletAmount) {
		t.Fatalf("expected ErrInvalidWalletAmount, got: %v", convertError)
	}
}

func TestConvertOpensDestinationWalletLazilyOnFirstUse(t *testing.T) {
	_, _, registry := newTestRegistry()

	if depositError := registry.DepositIntoCurrencyWallet("acct-001", "USD", 10_000); depositError != nil {
		t.Fatalf("unexpected deposit error: %v", depositError)
	}
	if _, convertError := registry.ConvertBetweenCurrencyWallets("acct-001", "USD", "INR", 10_000); convertError != nil {
		t.Fatalf("unexpected conversion error: %v", convertError)
	}

	balances := registry.GetWalletBalancesForAccount("acct-001")
	currencySeen := map[CurrencyCode]bool{}
	for _, balance := range balances {
		currencySeen[balance.CurrencyCode] = true
	}
	if !currencySeen["INR"] || !currencySeen["USD"] {
		t.Fatalf("expected both USD and INR wallets to be listed as opened, got %+v", balances)
	}
}

// --- Wallet listing ---

func TestGetWalletBalancesForAccountReturnsEmptySliceForNeverUsedAccount(t *testing.T) {
	_, _, registry := newTestRegistry()
	balances := registry.GetWalletBalancesForAccount("acct-002")
	if balances == nil {
		t.Fatalf("expected a non-nil empty slice, got nil")
	}
	if len(balances) != 0 {
		t.Fatalf("expected no wallets for an account that never used the wallet API, got %+v", balances)
	}
}

func TestGetWalletBalancesForAccountIsScopedPerAccount(t *testing.T) {
	_, _, registry := newTestRegistry()

	if depositError := registry.DepositIntoCurrencyWallet("acct-001", "USD", 5_000); depositError != nil {
		t.Fatalf("unexpected deposit error: %v", depositError)
	}
	if depositError := registry.DepositIntoCurrencyWallet("acct-002", "USD", 9_000); depositError != nil {
		t.Fatalf("unexpected deposit error: %v", depositError)
	}

	acct001Balances := registry.GetWalletBalancesForAccount("acct-001")
	acct002Balances := registry.GetWalletBalancesForAccount("acct-002")

	if len(acct001Balances) != 1 || acct001Balances[0].BalanceInMinorUnits != 5_000 {
		t.Fatalf("expected acct-001 USD balance 5000, got %+v", acct001Balances)
	}
	if len(acct002Balances) != 1 || acct002Balances[0].BalanceInMinorUnits != 9_000 {
		t.Fatalf("expected acct-002 USD balance 9000, got %+v", acct002Balances)
	}
}

func TestFxRateTableReturnsFalseForUnconfiguredPair(t *testing.T) {
	fxRateTable := NewStaticFxRateTable(map[string]float64{"USD/INR": 83.0})
	if _, found := fxRateTable.Rate("EUR", "JPY"); found {
		t.Fatalf("expected EUR/JPY to be unconfigured")
	}
	rate, found := fxRateTable.Rate("USD", "INR")
	if !found || rate != 83.0 {
		t.Fatalf("expected configured USD/INR rate 83.0, got %v (found=%v)", rate, found)
	}
}

// --- Concurrency ---

// TestConcurrentWithdrawalsFromSameCurrencyWalletCannotOverdraw reproduces
// the unlocked check-then-post race in WithdrawFromCurrencyWallet: two
// concurrent withdrawals for more than half the wallet balance each must
// not BOTH succeed.
func TestConcurrentWithdrawalsFromSameCurrencyWalletCannotOverdraw(t *testing.T) {
	const trials = 100
	for trial := 0; trial < trials; trial++ {
		ledgerBook, _, registry := newTestRegistry()
		if depositError := registry.DepositIntoCurrencyWallet("acct-001", "USD", 10_000); depositError != nil {
			t.Fatalf("unexpected deposit error: %v", depositError)
		}

		var wg sync.WaitGroup
		results := make([]error, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				results[idx] = registry.WithdrawFromCurrencyWallet("acct-001", "USD", 6_000)
			}(i)
		}
		wg.Wait()

		successCount := 0
		for _, err := range results {
			if err == nil {
				successCount++
			}
		}
		if successCount > 1 {
			t.Fatalf("trial %d: expected at most 1 of 2 concurrent 6000 withdrawals against a 10000 balance to succeed, got %d", trial, successCount)
		}

		usdBalance, _ := ledgerBook.CurrentBalanceInMinorUnits("acct-001:USD")
		if usdBalance < 0 {
			t.Fatalf("trial %d: USD wallet balance went negative: %d", trial, usdBalance)
		}
	}
}

// TestConcurrentConversionsFromSameCurrencyWalletCannotOverdraw reproduces
// the identical unlocked check-then-post race in
// ConvertBetweenCurrencyWallets.
func TestConcurrentConversionsFromSameCurrencyWalletCannotOverdraw(t *testing.T) {
	const trials = 100
	for trial := 0; trial < trials; trial++ {
		ledgerBook, _, registry := newTestRegistry()
		if depositError := registry.DepositIntoCurrencyWallet("acct-001", "USD", 10_000); depositError != nil {
			t.Fatalf("unexpected deposit error: %v", depositError)
		}

		var wg sync.WaitGroup
		results := make([]error, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				_, err := registry.ConvertBetweenCurrencyWallets("acct-001", "USD", "INR", 6_000)
				results[idx] = err
			}(i)
		}
		wg.Wait()

		successCount := 0
		for _, err := range results {
			if err == nil {
				successCount++
			}
		}
		if successCount > 1 {
			t.Fatalf("trial %d: expected at most 1 of 2 concurrent 6000 USD->INR conversions against a 10000 balance to succeed, got %d", trial, successCount)
		}

		usdBalance, _ := ledgerBook.CurrentBalanceInMinorUnits("acct-001:USD")
		if usdBalance < 0 {
			t.Fatalf("trial %d: USD wallet balance went negative: %d", trial, usdBalance)
		}
	}
}

// TestConvertBetweenCurrencyWalletsUsesExactRoundHalfUpNotFloat64Drift
// reproduces the float64-arithmetic rounding bug: 50 minor units at a
// 0.29 rate is EXACTLY 14.5, which should round HALF UP to 15 under a
// documented, deterministic rounding rule. Naive float64 multiplication
// (50 * 0.29) actually produces 14.499999999999996 in IEEE754 double
// precision (0.29 has no exact binary representation), which rounds DOWN
// to 14 — a silent, non-obvious one-minor-unit rounding error in real
// money math.
func TestConvertBetweenCurrencyWalletsUsesExactRoundHalfUpNotFloat64Drift(t *testing.T) {
	ledgerBook := doubleentry.NewInMemoryDoubleEntryLedgerBookWithAccounts([]string{
		"acct-001", "firm-clearing-acct", "client-money-custody-pool", "external-cash-suspense",
	})
	guard := fundsegregation.NewSegregationGuard(
		ledgerBook,
		"client-money-custody-pool",
		"external-cash-suspense",
		[]string{"acct-001"},
		[]string{"firm-clearing-acct"},
	)
	fxRateTable := NewStaticFxRateTable(map[string]float64{"USD/INR": 0.29})
	registry := NewMultiCurrencyWalletRegistry(
		ledgerBook, guard,
		"client-money-custody-pool", "wallet-external-cash-suspense", "fx-conversion-clearing-acct",
		"INR", fxRateTable,
	)

	if depositError := registry.DepositIntoCurrencyWallet("acct-001", "USD", 50); depositError != nil {
		t.Fatalf("unexpected deposit error: %v", depositError)
	}

	result, convertError := registry.ConvertBetweenCurrencyWallets("acct-001", "USD", "INR", 50)
	if convertError != nil {
		t.Fatalf("unexpected conversion error: %v", convertError)
	}

	// 50 * 0.29 = 14.5 EXACTLY — round-half-up must yield 15, not 14.
	if result.AmountCreditedToDestinationInMinorUnits != 15 {
		t.Fatalf("expected exact round-half-up result 15 for 50 minor units at rate 0.29, got %d (float64 drift bug)", result.AmountCreditedToDestinationInMinorUnits)
	}
}
