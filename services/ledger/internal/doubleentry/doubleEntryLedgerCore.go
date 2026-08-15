// Package doubleentry is the ledger's actual accounting core: standard
// double-entry bookkeeping where every journal entry's debit lines must
// sum to exactly its credit lines. See ARCHITECTURE.md §6 — this is Tier 2
// (correctness over speed), and it is the system of record for user
// balances, not the risk engine's fast in-memory cache in oms-gateway.
//
// TODO(real build): this is an in-memory skeleton. The real build persists
// every journal entry to PostgreSQL inside a single transaction (never
// eventually-consistent) and must additionally implement client-fund
// segregation per FEATURES.md §1 (client money ring-fenced from firm
// money at the account-structure level, not just a UI label).
package doubleentry

import (
	"errors"
	"fmt"
	"sync"
)

var ErrJournalEntryDoesNotBalance = errors.New("journal entry debit lines do not sum to credit lines")
var ErrUnknownLedgerAccount = errors.New("referenced ledger account does not exist")

// LedgerAccountLine is one side (debit or credit) of a journal entry.
type LedgerAccountLine struct {
	LedgerAccountIdentifier string
	AmountInMinorUnits      int64
}

// JournalEntry is an atomic, balanced set of debits and credits — the
// fundamental unit of every ledger mutation (deposit, withdrawal, trade
// settlement, fee, dividend). It is never partially applied.
type JournalEntry struct {
	HumanReadableDescription string
	DebitLines               []LedgerAccountLine
	CreditLines              []LedgerAccountLine
}

func (journalEntry JournalEntry) sumOfDebitLinesInMinorUnits() int64 {
	total := int64(0)
	for _, line := range journalEntry.DebitLines {
		total += line.AmountInMinorUnits
	}
	return total
}

func (journalEntry JournalEntry) sumOfCreditLinesInMinorUnits() int64 {
	total := int64(0)
	for _, line := range journalEntry.CreditLines {
		total += line.AmountInMinorUnits
	}
	return total
}

// LedgerBook is the public surface every consumer in this service
// (internal/fundsegregation, internal/withdrawalworkflow,
// internal/multicurrencywallet, cmd/server/main.go's admin
// snapshot/restore handlers) actually depends on. It exists so
// cmd/server/main.go can construct EITHER InMemoryDoubleEntryLedgerBook
// (this file) OR internal/pgstore's real Postgres-backed implementation
// and hand either one to every consumer unchanged — the
// interface-preserving persistence swap described in
// docs/BUILD_LOG.md's Postgres-persistence entry. InMemoryDoubleEntryLedgerBook
// satisfies this interface using the exact same method signatures it
// already had before this interface existed; nothing about its own
// behavior changed to conform to it.
type LedgerBook interface {
	PostJournalEntry(journalEntry JournalEntry) error
	RegisterAccountIfAbsent(accountIdentifier string) bool
	CurrentBalanceInMinorUnits(accountIdentifier string) (int64, error)
	CaptureSnapshot() LedgerBookSnapshot
	RestoreFromSnapshot(snapshot LedgerBookSnapshot)
}

// InMemoryDoubleEntryLedgerBook is the skeleton's stand-in for the real
// Postgres-backed ledger. It enforces the one rule that actually matters:
// no journal entry is ever posted unless it balances.
type InMemoryDoubleEntryLedgerBook struct {
	mutexGuardingAccountBalances          sync.Mutex
	accountBalanceInMinorUnitsByAccountId map[string]int64
	postedJournalEntriesInPostOrder       []JournalEntry
}

func NewInMemoryDoubleEntryLedgerBookWithAccounts(initialAccountIdentifiers []string) *InMemoryDoubleEntryLedgerBook {
	balances := make(map[string]int64, len(initialAccountIdentifiers))
	for _, accountIdentifier := range initialAccountIdentifiers {
		balances[accountIdentifier] = 0
	}
	return &InMemoryDoubleEntryLedgerBook{
		accountBalanceInMinorUnitsByAccountId: balances,
	}
}

// PostJournalEntry validates that the entry balances and that every
// referenced account exists, then atomically applies every line. If
// validation fails, no line is applied — the ledger is never left
// half-updated.
func (ledgerBook *InMemoryDoubleEntryLedgerBook) PostJournalEntry(journalEntry JournalEntry) error {
	if journalEntry.sumOfDebitLinesInMinorUnits() != journalEntry.sumOfCreditLinesInMinorUnits() {
		return fmt.Errorf(
			"%w: debits=%d credits=%d entry=%q",
			ErrJournalEntryDoesNotBalance,
			journalEntry.sumOfDebitLinesInMinorUnits(),
			journalEntry.sumOfCreditLinesInMinorUnits(),
			journalEntry.HumanReadableDescription,
		)
	}

	ledgerBook.mutexGuardingAccountBalances.Lock()
	defer ledgerBook.mutexGuardingAccountBalances.Unlock()

	for _, debitLine := range journalEntry.DebitLines {
		if _, accountExists := ledgerBook.accountBalanceInMinorUnitsByAccountId[debitLine.LedgerAccountIdentifier]; !accountExists {
			return fmt.Errorf("%w: %s", ErrUnknownLedgerAccount, debitLine.LedgerAccountIdentifier)
		}
	}
	for _, creditLine := range journalEntry.CreditLines {
		if _, accountExists := ledgerBook.accountBalanceInMinorUnitsByAccountId[creditLine.LedgerAccountIdentifier]; !accountExists {
			return fmt.Errorf("%w: %s", ErrUnknownLedgerAccount, creditLine.LedgerAccountIdentifier)
		}
	}

	// Convention: debit increases the named account, credit decreases it.
	// (Real chart-of-accounts semantics — asset vs. liability normal
	// balances — is a TODO for the real build; this skeleton uses one
	// uniform convention across all accounts for simplicity.)
	for _, debitLine := range journalEntry.DebitLines {
		ledgerBook.accountBalanceInMinorUnitsByAccountId[debitLine.LedgerAccountIdentifier] += debitLine.AmountInMinorUnits
	}
	for _, creditLine := range journalEntry.CreditLines {
		ledgerBook.accountBalanceInMinorUnitsByAccountId[creditLine.LedgerAccountIdentifier] -= creditLine.AmountInMinorUnits
	}

	ledgerBook.postedJournalEntriesInPostOrder = append(ledgerBook.postedJournalEntriesInPostOrder, journalEntry)
	return nil
}

// RegisterAccountIfAbsent adds a brand-new ledger account with a zero
// starting balance if — and only if — accountIdentifier does not already
// exist. It is purely additive: existing accounts and every other method
// on this type are completely unaffected, so callers that only ever used
// NewInMemoryDoubleEntryLedgerBookWithAccounts's fixed initial account
// list (e.g. oms-gateway posting trade settlements) see no behavior
// change whatsoever. This exists for callers that need to open new
// ledger accounts dynamically at runtime — e.g.
// internal/multicurrencywallet registering a new "acct-001:USD"-style
// sub-account the first time a client deposits into a currency it has
// never held before, rather than requiring every possible account to be
// hardcoded at startup.
//
// Returns true if the account was newly created, false if it already
// existed (a harmless no-op in that case — this method is idempotent).
func (ledgerBook *InMemoryDoubleEntryLedgerBook) RegisterAccountIfAbsent(accountIdentifier string) bool {
	ledgerBook.mutexGuardingAccountBalances.Lock()
	defer ledgerBook.mutexGuardingAccountBalances.Unlock()

	if _, accountAlreadyExists := ledgerBook.accountBalanceInMinorUnitsByAccountId[accountIdentifier]; accountAlreadyExists {
		return false
	}
	ledgerBook.accountBalanceInMinorUnitsByAccountId[accountIdentifier] = 0
	return true
}

func (ledgerBook *InMemoryDoubleEntryLedgerBook) CurrentBalanceInMinorUnits(accountIdentifier string) (int64, error) {
	ledgerBook.mutexGuardingAccountBalances.Lock()
	defer ledgerBook.mutexGuardingAccountBalances.Unlock()

	balance, accountExists := ledgerBook.accountBalanceInMinorUnitsByAccountId[accountIdentifier]
	if !accountExists {
		return 0, fmt.Errorf("%w: %s", ErrUnknownLedgerAccount, accountIdentifier)
	}
	return balance, nil
}
