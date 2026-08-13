package doubleentry

import (
	"errors"
	"testing"
)

func TestBalancedJournalEntryIsPostedAndUpdatesBothAccounts(t *testing.T) {
	ledgerBookUnderTest := NewInMemoryDoubleEntryLedgerBookWithAccounts([]string{"user-cash-acct-001", "firm-clearing-acct"})

	depositJournalEntry := JournalEntry{
		HumanReadableDescription: "user deposit via UPI",
		DebitLines:               []LedgerAccountLine{{LedgerAccountIdentifier: "user-cash-acct-001", AmountInMinorUnits: 10_000}},
		CreditLines:              []LedgerAccountLine{{LedgerAccountIdentifier: "firm-clearing-acct", AmountInMinorUnits: 10_000}},
	}

	if postError := ledgerBookUnderTest.PostJournalEntry(depositJournalEntry); postError != nil {
		t.Fatalf("expected balanced entry to post cleanly, got: %v", postError)
	}

	userBalance, _ := ledgerBookUnderTest.CurrentBalanceInMinorUnits("user-cash-acct-001")
	if userBalance != 10_000 {
		t.Fatalf("expected user balance 10000, got %d", userBalance)
	}
}

func TestUnbalancedJournalEntryIsRejectedAndNotAppliedToEitherAccount(t *testing.T) {
	ledgerBookUnderTest := NewInMemoryDoubleEntryLedgerBookWithAccounts([]string{"user-cash-acct-001", "firm-clearing-acct"})

	unbalancedJournalEntry := JournalEntry{
		HumanReadableDescription: "deliberately broken entry",
		DebitLines:               []LedgerAccountLine{{LedgerAccountIdentifier: "user-cash-acct-001", AmountInMinorUnits: 10_000}},
		CreditLines:              []LedgerAccountLine{{LedgerAccountIdentifier: "firm-clearing-acct", AmountInMinorUnits: 9_999}},
	}

	postError := ledgerBookUnderTest.PostJournalEntry(unbalancedJournalEntry)
	if !errors.Is(postError, ErrJournalEntryDoesNotBalance) {
		t.Fatalf("expected ErrJournalEntryDoesNotBalance, got: %v", postError)
	}

	userBalance, _ := ledgerBookUnderTest.CurrentBalanceInMinorUnits("user-cash-acct-001")
	if userBalance != 0 {
		t.Fatalf("rejected entry must not partially apply — expected 0, got %d", userBalance)
	}
}

func TestJournalEntryReferencingUnknownAccountIsRejected(t *testing.T) {
	ledgerBookUnderTest := NewInMemoryDoubleEntryLedgerBookWithAccounts([]string{"user-cash-acct-001"})

	journalEntryWithUnknownAccount := JournalEntry{
		HumanReadableDescription: "references an account that was never created",
		DebitLines:               []LedgerAccountLine{{LedgerAccountIdentifier: "user-cash-acct-001", AmountInMinorUnits: 500}},
		CreditLines:              []LedgerAccountLine{{LedgerAccountIdentifier: "does-not-exist", AmountInMinorUnits: 500}},
	}

	postError := ledgerBookUnderTest.PostJournalEntry(journalEntryWithUnknownAccount)
	if !errors.Is(postError, ErrUnknownLedgerAccount) {
		t.Fatalf("expected ErrUnknownLedgerAccount, got: %v", postError)
	}
}
