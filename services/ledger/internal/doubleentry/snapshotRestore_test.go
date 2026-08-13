package doubleentry

import (
	"encoding/json"
	"reflect"
	"sync"
	"testing"
)

func TestCaptureSnapshotOfFreshLedgerHasZeroBalancesForEveryAccountAndNoJournalEntries(t *testing.T) {
	ledgerBookUnderTest := NewInMemoryDoubleEntryLedgerBookWithAccounts([]string{"acct-a", "acct-b"})

	snapshot := ledgerBookUnderTest.CaptureSnapshot()

	if snapshot.SnapshotFormatVersion != currentSnapshotFormatVersion {
		t.Fatalf("expected snapshot format version %d, got %d", currentSnapshotFormatVersion, snapshot.SnapshotFormatVersion)
	}
	if len(snapshot.AccountBalances) != 2 {
		t.Fatalf("expected 2 accounts in snapshot, got %d", len(snapshot.AccountBalances))
	}
	if len(snapshot.PostedJournalEntriesInPostOrder) != 0 {
		t.Fatalf("expected no journal entries, got %d", len(snapshot.PostedJournalEntriesInPostOrder))
	}
	for _, accountBalance := range snapshot.AccountBalances {
		if accountBalance.BalanceInMinorUnits != 0 {
			t.Fatalf("expected zero balance for fresh account %s, got %d", accountBalance.LedgerAccountIdentifier, accountBalance.BalanceInMinorUnits)
		}
	}
}

func TestCaptureSnapshotReflectsPostedJournalEntriesInPostOrder(t *testing.T) {
	ledgerBookUnderTest := NewInMemoryDoubleEntryLedgerBookWithAccounts([]string{"acct-a", "acct-b"})

	firstEntry := JournalEntry{
		HumanReadableDescription: "first",
		DebitLines:               []LedgerAccountLine{{LedgerAccountIdentifier: "acct-a", AmountInMinorUnits: 100}},
		CreditLines:              []LedgerAccountLine{{LedgerAccountIdentifier: "acct-b", AmountInMinorUnits: 100}},
	}
	secondEntry := JournalEntry{
		HumanReadableDescription: "second",
		DebitLines:               []LedgerAccountLine{{LedgerAccountIdentifier: "acct-b", AmountInMinorUnits: 40}},
		CreditLines:              []LedgerAccountLine{{LedgerAccountIdentifier: "acct-a", AmountInMinorUnits: 40}},
	}
	if postError := ledgerBookUnderTest.PostJournalEntry(firstEntry); postError != nil {
		t.Fatalf("unexpected post error: %v", postError)
	}
	if postError := ledgerBookUnderTest.PostJournalEntry(secondEntry); postError != nil {
		t.Fatalf("unexpected post error: %v", postError)
	}

	snapshot := ledgerBookUnderTest.CaptureSnapshot()

	if len(snapshot.PostedJournalEntriesInPostOrder) != 2 {
		t.Fatalf("expected 2 journal entries, got %d", len(snapshot.PostedJournalEntriesInPostOrder))
	}
	if snapshot.PostedJournalEntriesInPostOrder[0].HumanReadableDescription != "first" {
		t.Fatalf("expected first entry first, got %q", snapshot.PostedJournalEntriesInPostOrder[0].HumanReadableDescription)
	}
	if snapshot.PostedJournalEntriesInPostOrder[1].HumanReadableDescription != "second" {
		t.Fatalf("expected second entry second, got %q", snapshot.PostedJournalEntriesInPostOrder[1].HumanReadableDescription)
	}

	balanceA, _ := ledgerBookUnderTest.CurrentBalanceInMinorUnits("acct-a")
	if balanceA != 60 {
		t.Fatalf("expected acct-a balance 60, got %d", balanceA)
	}
}

func TestCaptureSnapshotAccountBalancesAreSortedByIdentifierForDeterministicOutput(t *testing.T) {
	ledgerBookUnderTest := NewInMemoryDoubleEntryLedgerBookWithAccounts([]string{"zebra", "alpha", "mike"})

	snapshot := ledgerBookUnderTest.CaptureSnapshot()

	expectedOrder := []string{"alpha", "mike", "zebra"}
	if len(snapshot.AccountBalances) != len(expectedOrder) {
		t.Fatalf("expected %d accounts, got %d", len(expectedOrder), len(snapshot.AccountBalances))
	}
	for index, expectedIdentifier := range expectedOrder {
		if snapshot.AccountBalances[index].LedgerAccountIdentifier != expectedIdentifier {
			t.Fatalf("expected account %d to be %s, got %s", index, expectedIdentifier, snapshot.AccountBalances[index].LedgerAccountIdentifier)
		}
	}
}

func TestRestoreFromSnapshotReplacesBalancesExactly(t *testing.T) {
	ledgerBookUnderTest := NewInMemoryDoubleEntryLedgerBookWithAccounts([]string{"acct-a", "acct-b"})
	_ = ledgerBookUnderTest.PostJournalEntry(JournalEntry{
		HumanReadableDescription: "pre-restore",
		DebitLines:               []LedgerAccountLine{{LedgerAccountIdentifier: "acct-a", AmountInMinorUnits: 500}},
		CreditLines:              []LedgerAccountLine{{LedgerAccountIdentifier: "acct-b", AmountInMinorUnits: 500}},
	})

	snapshotBeforeMutation := ledgerBookUnderTest.CaptureSnapshot()

	_ = ledgerBookUnderTest.PostJournalEntry(JournalEntry{
		HumanReadableDescription: "further mutation after snapshot",
		DebitLines:               []LedgerAccountLine{{LedgerAccountIdentifier: "acct-a", AmountInMinorUnits: 9_999}},
		CreditLines:              []LedgerAccountLine{{LedgerAccountIdentifier: "acct-b", AmountInMinorUnits: 9_999}},
	})

	balanceAfterMutation, _ := ledgerBookUnderTest.CurrentBalanceInMinorUnits("acct-a")
	if balanceAfterMutation != 500+9_999 {
		t.Fatalf("expected mutated balance %d, got %d", 500+9_999, balanceAfterMutation)
	}

	ledgerBookUnderTest.RestoreFromSnapshot(snapshotBeforeMutation)

	balanceAfterRestore, _ := ledgerBookUnderTest.CurrentBalanceInMinorUnits("acct-a")
	if balanceAfterRestore != 500 {
		t.Fatalf("expected restored balance 500, got %d", balanceAfterRestore)
	}

	snapshotAfterRestore := ledgerBookUnderTest.CaptureSnapshot()
	if !reflect.DeepEqual(snapshotAfterRestore, snapshotBeforeMutation) {
		t.Fatalf("expected post-restore snapshot to exactly equal pre-mutation snapshot.\nwant: %+v\ngot:  %+v", snapshotBeforeMutation, snapshotAfterRestore)
	}
}

func TestRestoreFromSnapshotReplacesJournalEntryHistoryExactly(t *testing.T) {
	ledgerBookUnderTest := NewInMemoryDoubleEntryLedgerBookWithAccounts([]string{"acct-a", "acct-b"})
	_ = ledgerBookUnderTest.PostJournalEntry(JournalEntry{
		HumanReadableDescription: "entry-1",
		DebitLines:               []LedgerAccountLine{{LedgerAccountIdentifier: "acct-a", AmountInMinorUnits: 10}},
		CreditLines:              []LedgerAccountLine{{LedgerAccountIdentifier: "acct-b", AmountInMinorUnits: 10}},
	})
	snapshotWithOneEntry := ledgerBookUnderTest.CaptureSnapshot()

	_ = ledgerBookUnderTest.PostJournalEntry(JournalEntry{
		HumanReadableDescription: "entry-2",
		DebitLines:               []LedgerAccountLine{{LedgerAccountIdentifier: "acct-b", AmountInMinorUnits: 5}},
		CreditLines:              []LedgerAccountLine{{LedgerAccountIdentifier: "acct-a", AmountInMinorUnits: 5}},
	})
	if len(ledgerBookUnderTest.CaptureSnapshot().PostedJournalEntriesInPostOrder) != 2 {
		t.Fatalf("expected 2 entries before restore")
	}

	ledgerBookUnderTest.RestoreFromSnapshot(snapshotWithOneEntry)

	restoredEntries := ledgerBookUnderTest.CaptureSnapshot().PostedJournalEntriesInPostOrder
	if len(restoredEntries) != 1 {
		t.Fatalf("expected 1 journal entry after restore, got %d", len(restoredEntries))
	}
	if restoredEntries[0].HumanReadableDescription != "entry-1" {
		t.Fatalf("expected restored entry to be entry-1, got %q", restoredEntries[0].HumanReadableDescription)
	}
}

func TestRestoreFromSnapshotRemovesAccountsNotPresentInTheSnapshot(t *testing.T) {
	ledgerBookUnderTest := NewInMemoryDoubleEntryLedgerBookWithAccounts([]string{"acct-a"})
	snapshotWithOnlyAcctA := ledgerBookUnderTest.CaptureSnapshot()

	ledgerBookUnderTest.RegisterAccountIfAbsent("acct-b-added-later")
	if _, lookupError := ledgerBookUnderTest.CurrentBalanceInMinorUnits("acct-b-added-later"); lookupError != nil {
		t.Fatalf("expected acct-b-added-later to exist before restore")
	}

	ledgerBookUnderTest.RestoreFromSnapshot(snapshotWithOnlyAcctA)

	if _, lookupError := ledgerBookUnderTest.CurrentBalanceInMinorUnits("acct-b-added-later"); lookupError == nil {
		t.Fatalf("expected acct-b-added-later to be gone after restoring a snapshot that never had it")
	}
	if _, lookupError := ledgerBookUnderTest.CurrentBalanceInMinorUnits("acct-a"); lookupError != nil {
		t.Fatalf("expected acct-a to still exist after restore")
	}
}

func TestRestoreFromSnapshotOfEmptyValueProducesEmptyLedger(t *testing.T) {
	ledgerBookUnderTest := NewInMemoryDoubleEntryLedgerBookWithAccounts([]string{"acct-a", "acct-b"})
	_ = ledgerBookUnderTest.PostJournalEntry(JournalEntry{
		HumanReadableDescription: "will be wiped",
		DebitLines:               []LedgerAccountLine{{LedgerAccountIdentifier: "acct-a", AmountInMinorUnits: 1}},
		CreditLines:              []LedgerAccountLine{{LedgerAccountIdentifier: "acct-b", AmountInMinorUnits: 1}},
	})

	ledgerBookUnderTest.RestoreFromSnapshot(LedgerBookSnapshot{})

	emptySnapshot := ledgerBookUnderTest.CaptureSnapshot()
	if len(emptySnapshot.AccountBalances) != 0 {
		t.Fatalf("expected zero accounts after restoring empty snapshot, got %d", len(emptySnapshot.AccountBalances))
	}
	if len(emptySnapshot.PostedJournalEntriesInPostOrder) != 0 {
		t.Fatalf("expected zero journal entries after restoring empty snapshot, got %d", len(emptySnapshot.PostedJournalEntriesInPostOrder))
	}
	if _, lookupError := ledgerBookUnderTest.CurrentBalanceInMinorUnits("acct-a"); lookupError == nil {
		t.Fatalf("expected acct-a to be gone after restoring an empty snapshot")
	}
}

func TestSnapshotRoundTripsThroughJSONExactly(t *testing.T) {
	ledgerBookUnderTest := NewInMemoryDoubleEntryLedgerBookWithAccounts([]string{"acct-a", "acct-b"})
	_ = ledgerBookUnderTest.PostJournalEntry(JournalEntry{
		HumanReadableDescription: "json round trip entry",
		DebitLines:               []LedgerAccountLine{{LedgerAccountIdentifier: "acct-a", AmountInMinorUnits: 777}},
		CreditLines:              []LedgerAccountLine{{LedgerAccountIdentifier: "acct-b", AmountInMinorUnits: 777}},
	})
	originalSnapshot := ledgerBookUnderTest.CaptureSnapshot()

	marshalled, marshalError := json.Marshal(originalSnapshot)
	if marshalError != nil {
		t.Fatalf("unexpected marshal error: %v", marshalError)
	}

	var roundTrippedSnapshot LedgerBookSnapshot
	if unmarshalError := json.Unmarshal(marshalled, &roundTrippedSnapshot); unmarshalError != nil {
		t.Fatalf("unexpected unmarshal error: %v", unmarshalError)
	}

	if !reflect.DeepEqual(originalSnapshot, roundTrippedSnapshot) {
		t.Fatalf("expected JSON round trip to be lossless.\nwant: %+v\ngot:  %+v", originalSnapshot, roundTrippedSnapshot)
	}
}

func TestMutatingCapturedSnapshotSliceDoesNotAffectLedgerBookState(t *testing.T) {
	ledgerBookUnderTest := NewInMemoryDoubleEntryLedgerBookWithAccounts([]string{"acct-a", "acct-b"})
	_ = ledgerBookUnderTest.PostJournalEntry(JournalEntry{
		HumanReadableDescription: "original",
		DebitLines:               []LedgerAccountLine{{LedgerAccountIdentifier: "acct-a", AmountInMinorUnits: 300}},
		CreditLines:              []LedgerAccountLine{{LedgerAccountIdentifier: "acct-b", AmountInMinorUnits: 300}},
	})

	snapshot := ledgerBookUnderTest.CaptureSnapshot()
	// Mutate the returned snapshot's slices directly — this must be a
	// deep-enough copy that it cannot reach back into the ledger's live
	// state.
	snapshot.AccountBalances[0].BalanceInMinorUnits = 999_999
	snapshot.PostedJournalEntriesInPostOrder[0].HumanReadableDescription = "tampered"

	freshSnapshot := ledgerBookUnderTest.CaptureSnapshot()
	if freshSnapshot.PostedJournalEntriesInPostOrder[0].HumanReadableDescription != "original" {
		t.Fatalf("mutating a captured snapshot must not affect the live ledger state")
	}
}

func TestRestoreFromSnapshotIsSafeUnderConcurrentReadersAndWriters(t *testing.T) {
	ledgerBookUnderTest := NewInMemoryDoubleEntryLedgerBookWithAccounts([]string{"acct-a", "acct-b"})
	_ = ledgerBookUnderTest.PostJournalEntry(JournalEntry{
		HumanReadableDescription: "baseline",
		DebitLines:               []LedgerAccountLine{{LedgerAccountIdentifier: "acct-a", AmountInMinorUnits: 10}},
		CreditLines:              []LedgerAccountLine{{LedgerAccountIdentifier: "acct-b", AmountInMinorUnits: 10}},
	})
	baselineSnapshot := ledgerBookUnderTest.CaptureSnapshot()

	var waitGroup sync.WaitGroup
	for i := 0; i < 20; i++ {
		waitGroup.Add(3)
		go func() {
			defer waitGroup.Done()
			ledgerBookUnderTest.RestoreFromSnapshot(baselineSnapshot)
		}()
		go func() {
			defer waitGroup.Done()
			_ = ledgerBookUnderTest.CaptureSnapshot()
		}()
		go func() {
			defer waitGroup.Done()
			_, _ = ledgerBookUnderTest.CurrentBalanceInMinorUnits("acct-a")
		}()
	}
	waitGroup.Wait()

	finalBalance, lookupError := ledgerBookUnderTest.CurrentBalanceInMinorUnits("acct-a")
	if lookupError != nil {
		t.Fatalf("unexpected lookup error after concurrent restores: %v", lookupError)
	}
	if finalBalance != 10 {
		t.Fatalf("expected balance 10 after all concurrent restores settle, got %d", finalBalance)
	}
}

func TestCaptureSnapshotDoesNotMutateExistingLedgerBehaviorForPostJournalEntry(t *testing.T) {
	// Regression guard: CaptureSnapshot/RestoreFromSnapshot must be
	// purely additive — PostJournalEntry's existing balance/rejection
	// behavior must be completely unaffected by their presence.
	ledgerBookUnderTest := NewInMemoryDoubleEntryLedgerBookWithAccounts([]string{"acct-a", "acct-b"})
	_ = ledgerBookUnderTest.CaptureSnapshot()

	unbalancedEntry := JournalEntry{
		HumanReadableDescription: "still rejected",
		DebitLines:               []LedgerAccountLine{{LedgerAccountIdentifier: "acct-a", AmountInMinorUnits: 100}},
		CreditLines:              []LedgerAccountLine{{LedgerAccountIdentifier: "acct-b", AmountInMinorUnits: 99}},
	}
	if postError := ledgerBookUnderTest.PostJournalEntry(unbalancedEntry); postError == nil {
		t.Fatalf("expected unbalanced entry to still be rejected after CaptureSnapshot was called")
	}
}
