package retirementaccounts

import (
	"testing"
	"time"
)

func mustParseDate(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, parseError := time.Parse("2006-01-02", value)
	if parseError != nil {
		t.Fatalf("bad fixture date %q: %v", value, parseError)
	}
	return parsed
}

func TestOpenAccountRejectsUnknownAccountType(t *testing.T) {
	engine := NewRulesEngine()
	dob := mustParseDate(t, "1990-01-01")
	if _, err := engine.OpenAccount("acct-1", "NOT_A_TYPE", dob, time.Now()); err != ErrUnknownAccountType {
		t.Fatalf("expected ErrUnknownAccountType, got %v", err)
	}
}

func TestOpenAccountRejectsFutureDateOfBirth(t *testing.T) {
	engine := NewRulesEngine()
	future := time.Now().AddDate(1, 0, 0)
	if _, err := engine.OpenAccount("acct-1", AccountTypeNpsEquivalent, future, time.Now()); err != ErrInvalidDateOfBirth {
		t.Fatalf("expected ErrInvalidDateOfBirth, got %v", err)
	}
}

func TestOpenAccountSucceeds(t *testing.T) {
	engine := NewRulesEngine()
	dob := mustParseDate(t, "1990-01-01")
	account, err := engine.OpenAccount("acct-1", AccountTypeNpsEquivalent, dob, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if account.BalanceInMinorUnits != 0 {
		t.Fatalf("expected zero opening balance, got %d", account.BalanceInMinorUnits)
	}
}

func TestContributeIncreasesBalance(t *testing.T) {
	engine := NewRulesEngine()
	dob := mustParseDate(t, "1990-01-01")
	account, _ := engine.OpenAccount("acct-1", AccountTypeNpsEquivalent, dob, time.Now())

	updated, err := engine.Contribute(account.AccountId, 50000, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.BalanceInMinorUnits != 50000 {
		t.Fatalf("expected balance 50000, got %d", updated.BalanceInMinorUnits)
	}
}

func TestContributeRejectsNonPositiveAmount(t *testing.T) {
	engine := NewRulesEngine()
	dob := mustParseDate(t, "1990-01-01")
	account, _ := engine.OpenAccount("acct-1", AccountTypeNpsEquivalent, dob, time.Now())

	if _, err := engine.Contribute(account.AccountId, 0, time.Now()); err != ErrInvalidContributionAmount {
		t.Fatalf("expected ErrInvalidContributionAmount, got %v", err)
	}
}

func TestContributeRejectsUnknownAccount(t *testing.T) {
	engine := NewRulesEngine()
	if _, err := engine.Contribute("no-such-account", 1000, time.Now()); err != ErrUnknownAccount {
		t.Fatalf("expected ErrUnknownAccount, got %v", err)
	}
}

// TestContributeEnforcesAnnualLimitExactBoundary: NPS-equivalent limit is
// exactly 15_00_000_00. Contributing exactly that in one year succeeds; a
// single extra minor unit on top is rejected.
func TestContributeEnforcesAnnualLimitExactBoundary(t *testing.T) {
	engine := NewRulesEngine()
	dob := mustParseDate(t, "1990-01-01")
	account, _ := engine.OpenAccount("acct-1", AccountTypeNpsEquivalent, dob, time.Now())
	now := time.Now()

	if _, err := engine.Contribute(account.AccountId, 15_00_000_00, now); err != nil {
		t.Fatalf("expected the exact limit to be accepted, got %v", err)
	}
	if _, err := engine.Contribute(account.AccountId, 1, now); err != ErrContributionExceedsAnnualLimit {
		t.Fatalf("expected ErrContributionExceedsAnnualLimit for one unit over, got %v", err)
	}
}

func TestContributeEnforcesAnnualLimitAcrossMultipleContributions(t *testing.T) {
	engine := NewRulesEngine()
	dob := mustParseDate(t, "1990-01-01")
	account, _ := engine.OpenAccount("acct-1", AccountTypeIraEquivalent, dob, time.Now())
	now := time.Now()

	// IRA-equivalent limit is 7_00_000_00.
	if _, err := engine.Contribute(account.AccountId, 4_00_000_00, now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := engine.Contribute(account.AccountId, 3_00_000_00, now); err != nil {
		t.Fatalf("unexpected error on second contribution reaching exactly the limit: %v", err)
	}
	if _, err := engine.Contribute(account.AccountId, 1, now); err != ErrContributionExceedsAnnualLimit {
		t.Fatalf("expected the third contribution to breach the limit, got %v", err)
	}
}

func TestContributeAnnualLimitResetsInANewCalendarYear(t *testing.T) {
	engine := NewRulesEngine()
	dob := mustParseDate(t, "1990-01-01")
	account, _ := engine.OpenAccount("acct-1", AccountTypeNpsEquivalent, dob, mustParseDate(t, "2026-01-01"))

	yearOne := mustParseDate(t, "2026-06-01")
	yearTwo := mustParseDate(t, "2027-06-01")

	if _, err := engine.Contribute(account.AccountId, 15_00_000_00, yearOne); err != nil {
		t.Fatalf("unexpected error maxing out year one: %v", err)
	}
	// The SAME limit amount should be contributable again in the next
	// calendar year — the limit is PER YEAR, not lifetime.
	if _, err := engine.Contribute(account.AccountId, 15_00_000_00, yearTwo); err != nil {
		t.Fatalf("expected a fresh limit in the new calendar year, got %v", err)
	}
}

func TestRemainingContributionRoomForYear(t *testing.T) {
	engine := NewRulesEngine()
	dob := mustParseDate(t, "1990-01-01")
	account, _ := engine.OpenAccount("acct-1", AccountTypeIraEquivalent, dob, time.Now())
	now := mustParseDate(t, "2026-06-01")

	engine.Contribute(account.AccountId, 2_00_000_00, now)
	remaining, err := engine.RemainingContributionRoomForYear(account.AccountId, 2026)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if remaining != 5_00_000_00 {
		t.Fatalf("expected remaining room 5_00_000_00, got %d", remaining)
	}
}

// TestWithdrawRejectedBeforeMinimumRetirementAge: NPS-equivalent minimum
// retirement age is 60. Someone born 1990-01-01 turns exactly 60 on
// 2050-01-01 — withdrawing the day before must be rejected, on that exact
// day must succeed.
func TestWithdrawRejectedBeforeMinimumRetirementAge(t *testing.T) {
	engine := NewRulesEngine()
	dob := mustParseDate(t, "1990-01-01")
	account, _ := engine.OpenAccount("acct-1", AccountTypeNpsEquivalent, dob, mustParseDate(t, "2026-01-01"))
	engine.Contribute(account.AccountId, 10000, mustParseDate(t, "2026-01-01"))

	dayBeforeSixtiethBirthday := mustParseDate(t, "2049-12-31")
	if _, err := engine.Withdraw(account.AccountId, 100, dayBeforeSixtiethBirthday); err != ErrWithdrawalBeforeRetirementAge {
		t.Fatalf("expected ErrWithdrawalBeforeRetirementAge, got %v", err)
	}
}

func TestWithdrawSucceedsExactlyOnMinimumRetirementAgeBoundary(t *testing.T) {
	engine := NewRulesEngine()
	dob := mustParseDate(t, "1990-01-01")
	account, _ := engine.OpenAccount("acct-1", AccountTypeNpsEquivalent, dob, mustParseDate(t, "2026-01-01"))
	engine.Contribute(account.AccountId, 10000, mustParseDate(t, "2026-01-01"))

	sixtiethBirthday := mustParseDate(t, "2050-01-01")
	updated, err := engine.Withdraw(account.AccountId, 100, sixtiethBirthday)
	if err != nil {
		t.Fatalf("expected withdrawal to succeed exactly on the retirement-age boundary, got %v", err)
	}
	if updated.BalanceInMinorUnits != 9900 {
		t.Fatalf("expected balance 9900 after withdrawal, got %d", updated.BalanceInMinorUnits)
	}
}

func TestWithdrawRejectsAmountExceedingBalance(t *testing.T) {
	engine := NewRulesEngine()
	dob := mustParseDate(t, "1950-01-01") // already well past 60
	account, _ := engine.OpenAccount("acct-1", AccountTypeNpsEquivalent, dob, mustParseDate(t, "2026-01-01"))
	engine.Contribute(account.AccountId, 10000, mustParseDate(t, "2026-01-01"))

	if _, err := engine.Withdraw(account.AccountId, 20000, mustParseDate(t, "2026-06-01")); err != ErrInsufficientBalance {
		t.Fatalf("expected ErrInsufficientBalance, got %v", err)
	}
}

func TestWithdrawRejectsNonPositiveAmount(t *testing.T) {
	engine := NewRulesEngine()
	dob := mustParseDate(t, "1950-01-01")
	account, _ := engine.OpenAccount("acct-1", AccountTypeNpsEquivalent, dob, mustParseDate(t, "2026-01-01"))

	if _, err := engine.Withdraw(account.AccountId, 0, mustParseDate(t, "2026-06-01")); err != ErrInvalidWithdrawalAmount {
		t.Fatalf("expected ErrInvalidWithdrawalAmount, got %v", err)
	}
}

func TestWithdrawRejectsUnknownAccount(t *testing.T) {
	engine := NewRulesEngine()
	if _, err := engine.Withdraw("no-such-account", 100, time.Now()); err != ErrUnknownAccount {
		t.Fatalf("expected ErrUnknownAccount, got %v", err)
	}
}

func TestAccountsForHolderReturnsOnlyThatHolderSortedByOpenedAt(t *testing.T) {
	engine := NewRulesEngine()
	dob := mustParseDate(t, "1990-01-01")
	now := time.Now()
	engine.OpenAccount("acct-1", AccountTypeNpsEquivalent, dob, now)
	engine.OpenAccount("acct-2", AccountTypeIraEquivalent, dob, now)
	engine.OpenAccount("acct-1", AccountTypeIraEquivalent, dob, now.Add(time.Hour))

	accounts := engine.AccountsForHolder("acct-1")
	if len(accounts) != 2 {
		t.Fatalf("expected 2 accounts for acct-1, got %d", len(accounts))
	}
	if accounts[0].OpenedAt.After(accounts[1].OpenedAt) {
		t.Fatal("expected accounts sorted by OpenedAt")
	}
}

func TestLookupAccountUnknownReturnsFalse(t *testing.T) {
	engine := NewRulesEngine()
	if _, wasFound := engine.LookupAccount("no-such-account"); wasFound {
		t.Fatal("expected not to find an unknown account")
	}
}
