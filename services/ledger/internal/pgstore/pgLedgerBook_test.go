// Real tests against a real, locally-running Postgres — no mocks, no
// testcontainers. See docs/BUILD_LOG.md's Postgres-persistence entry:
// these tests were run against the actual `make infra-up` Postgres
// container on this build's dev machine (host port remapped to 5433 —
// see infra/docker/docker-compose.yml's comment on why — via
// PGSTORE_TEST_DSN below).
//
// Table-clobbering note (documented, not fixed): every test in this
// file TRUNCATEs the ledger tables at the start of each test (via
// truncateAllLedgerTables) rather than using a schema/prefix per test —
// simplest option given this package owns the entire ledger schema and
// there is no other legitimate concurrent writer of these specific
// tables during a test run. Do NOT run this test file against a
// database also serving real dev/demo traffic at the same time.
package pgstore

import (
	"context"
	"errors"
	"os"
	"testing"

	"mercurius/ledger/internal/doubleentry"
)

func testPostgresDsn() string {
	if dsn := os.Getenv("PGSTORE_TEST_DSN"); dsn != "" {
		return dsn
	}
	return "postgres://trading:trading@localhost:5432/ledger"
}

func mustOpenTestLedgerBook(t *testing.T, initialAccounts []string) *PostgresLedgerBook {
	t.Helper()
	ctx := context.Background()
	book, connectError := NewPostgresLedgerBook(ctx, testPostgresDsn(), nil)
	if connectError != nil {
		t.Skipf("skipping: real Postgres not reachable at %s: %v", testPostgresDsn(), connectError)
	}
	truncateAllLedgerTables(t, book)
	for _, accountIdentifier := range initialAccounts {
		book.RegisterAccountIfAbsent(accountIdentifier)
	}
	t.Cleanup(book.Close)
	return book
}

func truncateAllLedgerTables(t *testing.T, book *PostgresLedgerBook) {
	t.Helper()
	if _, execError := book.pool.Exec(context.Background(), `TRUNCATE journal_entry_lines, journal_entries, ledger_accounts RESTART IDENTITY`); execError != nil {
		t.Fatalf("truncate ledger tables: %v", execError)
	}
}

func TestPostgresLedgerBook_RegisterAccountIfAbsent(t *testing.T) {
	book := mustOpenTestLedgerBook(t, nil)

	if !book.RegisterAccountIfAbsent("acct-x") {
		t.Fatal("expected true for a brand-new account")
	}
	if book.RegisterAccountIfAbsent("acct-x") {
		t.Fatal("expected false for an already-existing account")
	}

	balance, balanceError := book.CurrentBalanceInMinorUnits("acct-x")
	if balanceError != nil {
		t.Fatalf("unexpected error: %v", balanceError)
	}
	if balance != 0 {
		t.Fatalf("expected 0 starting balance, got %d", balance)
	}
}

func TestPostgresLedgerBook_CurrentBalanceInMinorUnits_UnknownAccount(t *testing.T) {
	book := mustOpenTestLedgerBook(t, nil)

	_, balanceError := book.CurrentBalanceInMinorUnits("does-not-exist")
	if !errors.Is(balanceError, doubleentry.ErrUnknownLedgerAccount) {
		t.Fatalf("expected ErrUnknownLedgerAccount, got %v", balanceError)
	}
}

func TestPostgresLedgerBook_PostJournalEntry_BalancedEntryUpdatesBothAccounts(t *testing.T) {
	book := mustOpenTestLedgerBook(t, []string{"acct-a", "acct-b"})

	postError := book.PostJournalEntry(doubleentry.JournalEntry{
		HumanReadableDescription: "test transfer",
		DebitLines:               []doubleentry.LedgerAccountLine{{LedgerAccountIdentifier: "acct-a", AmountInMinorUnits: 500}},
		CreditLines:              []doubleentry.LedgerAccountLine{{LedgerAccountIdentifier: "acct-b", AmountInMinorUnits: 500}},
	})
	if postError != nil {
		t.Fatalf("unexpected error posting balanced entry: %v", postError)
	}

	balanceA, _ := book.CurrentBalanceInMinorUnits("acct-a")
	balanceB, _ := book.CurrentBalanceInMinorUnits("acct-b")
	if balanceA != 500 {
		t.Fatalf("expected acct-a balance 500, got %d", balanceA)
	}
	if balanceB != -500 {
		t.Fatalf("expected acct-b balance -500, got %d", balanceB)
	}
}

func TestPostgresLedgerBook_PostJournalEntry_RejectsUnbalancedEntry(t *testing.T) {
	book := mustOpenTestLedgerBook(t, []string{"acct-a", "acct-b"})

	postError := book.PostJournalEntry(doubleentry.JournalEntry{
		HumanReadableDescription: "unbalanced",
		DebitLines:               []doubleentry.LedgerAccountLine{{LedgerAccountIdentifier: "acct-a", AmountInMinorUnits: 500}},
		CreditLines:              []doubleentry.LedgerAccountLine{{LedgerAccountIdentifier: "acct-b", AmountInMinorUnits: 400}},
	})
	if !errors.Is(postError, doubleentry.ErrJournalEntryDoesNotBalance) {
		t.Fatalf("expected ErrJournalEntryDoesNotBalance, got %v", postError)
	}

	balanceA, _ := book.CurrentBalanceInMinorUnits("acct-a")
	if balanceA != 0 {
		t.Fatalf("expected no partial application, acct-a balance still 0, got %d", balanceA)
	}
}

func TestPostgresLedgerBook_PostJournalEntry_RejectsUnknownAccount(t *testing.T) {
	book := mustOpenTestLedgerBook(t, []string{"acct-a"})

	postError := book.PostJournalEntry(doubleentry.JournalEntry{
		HumanReadableDescription: "references unknown account",
		DebitLines:               []doubleentry.LedgerAccountLine{{LedgerAccountIdentifier: "acct-a", AmountInMinorUnits: 500}},
		CreditLines:              []doubleentry.LedgerAccountLine{{LedgerAccountIdentifier: "acct-nonexistent", AmountInMinorUnits: 500}},
	})
	if !errors.Is(postError, doubleentry.ErrUnknownLedgerAccount) {
		t.Fatalf("expected ErrUnknownLedgerAccount, got %v", postError)
	}
}

func TestPostgresLedgerBook_PersistsAcrossFreshConnection(t *testing.T) {
	// The actual point of this whole exercise, at the pgstore-unit level:
	// data written through one *PostgresLedgerBook connection pool is
	// visible from a brand-new one — i.e. it lives in Postgres, not in
	// process memory. The real restart-survival proof (a killed and
	// restarted OS process) is done at the live-service level — see
	// docs/BUILD_LOG.md.
	firstBook := mustOpenTestLedgerBook(t, []string{"acct-a", "acct-b"})
	if postError := firstBook.PostJournalEntry(doubleentry.JournalEntry{
		HumanReadableDescription: "persisted across connections",
		DebitLines:               []doubleentry.LedgerAccountLine{{LedgerAccountIdentifier: "acct-a", AmountInMinorUnits: 777}},
		CreditLines:              []doubleentry.LedgerAccountLine{{LedgerAccountIdentifier: "acct-b", AmountInMinorUnits: 777}},
	}); postError != nil {
		t.Fatalf("unexpected error: %v", postError)
	}

	secondBook, connectError := NewPostgresLedgerBook(context.Background(), testPostgresDsn(), nil)
	if connectError != nil {
		t.Fatalf("unexpected error opening second connection: %v", connectError)
	}
	defer secondBook.Close()

	balance, balanceError := secondBook.CurrentBalanceInMinorUnits("acct-a")
	if balanceError != nil {
		t.Fatalf("unexpected error: %v", balanceError)
	}
	if balance != 777 {
		t.Fatalf("expected balance 777 visible from a fresh connection, got %d", balance)
	}
}

func TestPostgresLedgerBook_CaptureAndRestoreSnapshot(t *testing.T) {
	book := mustOpenTestLedgerBook(t, []string{"acct-a", "acct-b"})
	if postError := book.PostJournalEntry(doubleentry.JournalEntry{
		HumanReadableDescription: "before snapshot",
		DebitLines:               []doubleentry.LedgerAccountLine{{LedgerAccountIdentifier: "acct-a", AmountInMinorUnits: 100}},
		CreditLines:              []doubleentry.LedgerAccountLine{{LedgerAccountIdentifier: "acct-b", AmountInMinorUnits: 100}},
	}); postError != nil {
		t.Fatalf("unexpected error: %v", postError)
	}

	snapshot := book.CaptureSnapshot()
	if len(snapshot.AccountBalances) != 2 {
		t.Fatalf("expected 2 accounts in snapshot, got %d", len(snapshot.AccountBalances))
	}
	if len(snapshot.PostedJournalEntriesInPostOrder) != 1 {
		t.Fatalf("expected 1 journal entry in snapshot, got %d", len(snapshot.PostedJournalEntriesInPostOrder))
	}

	// Mutate further, then restore back to the captured snapshot and
	// confirm the mutation is gone — the same "hard replace" contract
	// the in-memory RestoreFromSnapshot documents.
	if postError := book.PostJournalEntry(doubleentry.JournalEntry{
		HumanReadableDescription: "after snapshot, should be wiped by restore",
		DebitLines:               []doubleentry.LedgerAccountLine{{LedgerAccountIdentifier: "acct-b", AmountInMinorUnits: 900}},
		CreditLines:              []doubleentry.LedgerAccountLine{{LedgerAccountIdentifier: "acct-a", AmountInMinorUnits: 900}},
	}); postError != nil {
		t.Fatalf("unexpected error: %v", postError)
	}

	book.RestoreFromSnapshot(snapshot)

	restoredBalanceA, _ := book.CurrentBalanceInMinorUnits("acct-a")
	if restoredBalanceA != 100 {
		t.Fatalf("expected restored acct-a balance 100, got %d", restoredBalanceA)
	}

	restoredSnapshot := book.CaptureSnapshot()
	if len(restoredSnapshot.PostedJournalEntriesInPostOrder) != 1 {
		t.Fatalf("expected exactly 1 journal entry after restore, got %d", len(restoredSnapshot.PostedJournalEntriesInPostOrder))
	}
}
