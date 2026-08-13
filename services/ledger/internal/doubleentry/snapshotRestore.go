// Snapshot/restore support for FEATURES.md §13's "[P1] Automated backups
// + tested restore procedure for ledger DB" — purely additive on top of
// InMemoryDoubleEntryLedgerBook. Every method in this file is new; no
// existing type, method signature, or behavior in
// doubleEntryLedgerCore.go is touched.
//
// TODO(real build): once this ledger is Postgres-backed (see
// doubleEntryLedgerCore.go's package doc), "backup" means a real
// pg_dump/point-in-time-recovery setup, and this in-memory JSON snapshot
// becomes at most a supplementary/dev-environment tool, not the primary
// backup mechanism. Today, with no database at all, this IS the backup
// mechanism: it is the only way to persist ledger state across a process
// restart or to recover from an operator mistake within a running
// process.
package doubleentry

import "sort"

// currentSnapshotFormatVersion lets a future restore path detect and
// reject (or migrate) snapshots taken by an older, incompatible format
// without guessing.
const currentSnapshotFormatVersion = 1

// LedgerAccountBalanceSnapshot is one account's balance at the moment a
// snapshot was captured.
type LedgerAccountBalanceSnapshot struct {
	LedgerAccountIdentifier string `json:"ledgerAccountIdentifier"`
	BalanceInMinorUnits     int64  `json:"balanceInMinorUnits"`
}

// LedgerAccountLineSnapshot mirrors LedgerAccountLine but carries its own
// JSON tags — deliberately decoupled from the domain type the same way
// cmd/server/main.go keeps its own wire types decoupled from
// doubleentry.JournalEntry, so this package's on-disk snapshot format
// never silently changes just because someone edits the domain type.
type LedgerAccountLineSnapshot struct {
	LedgerAccountIdentifier string `json:"ledgerAccountIdentifier"`
	AmountInMinorUnits      int64  `json:"amountInMinorUnits"`
}

// JournalEntrySnapshot mirrors JournalEntry for the same reason
// LedgerAccountLineSnapshot mirrors LedgerAccountLine.
type JournalEntrySnapshot struct {
	HumanReadableDescription string                      `json:"humanReadableDescription"`
	DebitLines               []LedgerAccountLineSnapshot `json:"debitLines"`
	CreditLines              []LedgerAccountLineSnapshot `json:"creditLines"`
}

// LedgerBookSnapshot is the COMPLETE in-memory state of an
// InMemoryDoubleEntryLedgerBook at one instant: every account (even
// zero-balance ones, so RestoreFromSnapshot can recreate the exact
// account set) and every posted journal entry, in the exact order they
// were originally posted. This is the wire/on-disk shape both
// GET /admin/snapshot and POST /admin/restore in cmd/server/main.go use
// directly.
type LedgerBookSnapshot struct {
	SnapshotFormatVersion           int                            `json:"snapshotFormatVersion"`
	AccountBalances                 []LedgerAccountBalanceSnapshot `json:"accountBalances"`
	PostedJournalEntriesInPostOrder []JournalEntrySnapshot         `json:"postedJournalEntriesInPostOrder"`
}

// CaptureSnapshot returns a deep-copied, point-in-time snapshot of every
// account balance and every posted journal entry currently held by
// ledgerBook. Account balances are sorted by identifier so that two
// snapshots of identical underlying state always serialize to byte-for-
// byte identical JSON (map iteration order is not otherwise stable) —
// useful for diffing backups and for the restore-drill test/script that
// compares snapshot JSON directly.
func (ledgerBook *InMemoryDoubleEntryLedgerBook) CaptureSnapshot() LedgerBookSnapshot {
	ledgerBook.mutexGuardingAccountBalances.Lock()
	defer ledgerBook.mutexGuardingAccountBalances.Unlock()

	accountBalances := make([]LedgerAccountBalanceSnapshot, 0, len(ledgerBook.accountBalanceInMinorUnitsByAccountId))
	for accountIdentifier, balance := range ledgerBook.accountBalanceInMinorUnitsByAccountId {
		accountBalances = append(accountBalances, LedgerAccountBalanceSnapshot{
			LedgerAccountIdentifier: accountIdentifier,
			BalanceInMinorUnits:     balance,
		})
	}
	sort.Slice(accountBalances, func(i, j int) bool {
		return accountBalances[i].LedgerAccountIdentifier < accountBalances[j].LedgerAccountIdentifier
	})

	journalEntries := make([]JournalEntrySnapshot, 0, len(ledgerBook.postedJournalEntriesInPostOrder))
	for _, journalEntry := range ledgerBook.postedJournalEntriesInPostOrder {
		journalEntries = append(journalEntries, JournalEntrySnapshot{
			HumanReadableDescription: journalEntry.HumanReadableDescription,
			DebitLines:               convertLedgerAccountLinesToSnapshotLines(journalEntry.DebitLines),
			CreditLines:              convertLedgerAccountLinesToSnapshotLines(journalEntry.CreditLines),
		})
	}

	return LedgerBookSnapshot{
		SnapshotFormatVersion:           currentSnapshotFormatVersion,
		AccountBalances:                 accountBalances,
		PostedJournalEntriesInPostOrder: journalEntries,
	}
}

// RestoreFromSnapshot atomically REPLACES ledgerBook's entire current
// in-memory state (every account balance, every posted journal entry)
// with the state captured in snapshot. This is a hard replace, not a
// merge: any account or journal entry that exists in ledgerBook but not
// in snapshot is gone after this call returns — that is the whole point
// of a restore-from-backup operation.
//
// The replacement is built up entirely OUTSIDE the lock (from a deep
// copy of snapshot's data, so later mutation of the caller's snapshot
// value can't corrupt the ledger) and then swapped in under
// mutexGuardingAccountBalances, so a concurrent PostJournalEntry/
// RegisterAccountIfAbsent/CurrentBalanceInMinorUnits call from another
// goroutine either sees the complete pre-restore state or the complete
// post-restore state — never a partially-restored mix.
//
// RestoreFromSnapshot never errors: any well-formed LedgerBookSnapshot
// (including the zero value, which restores an empty ledger) is valid
// input. Malformed JSON is rejected earlier, at JSON-decode time, by the
// caller (see cmd/server/main.go's /admin/restore handler).
func (ledgerBook *InMemoryDoubleEntryLedgerBook) RestoreFromSnapshot(snapshot LedgerBookSnapshot) {
	restoredBalances := make(map[string]int64, len(snapshot.AccountBalances))
	for _, accountBalance := range snapshot.AccountBalances {
		restoredBalances[accountBalance.LedgerAccountIdentifier] = accountBalance.BalanceInMinorUnits
	}

	restoredJournalEntries := make([]JournalEntry, 0, len(snapshot.PostedJournalEntriesInPostOrder))
	for _, journalEntrySnapshot := range snapshot.PostedJournalEntriesInPostOrder {
		restoredJournalEntries = append(restoredJournalEntries, JournalEntry{
			HumanReadableDescription: journalEntrySnapshot.HumanReadableDescription,
			DebitLines:               convertSnapshotLinesToLedgerAccountLines(journalEntrySnapshot.DebitLines),
			CreditLines:              convertSnapshotLinesToLedgerAccountLines(journalEntrySnapshot.CreditLines),
		})
	}

	ledgerBook.mutexGuardingAccountBalances.Lock()
	defer ledgerBook.mutexGuardingAccountBalances.Unlock()
	ledgerBook.accountBalanceInMinorUnitsByAccountId = restoredBalances
	ledgerBook.postedJournalEntriesInPostOrder = restoredJournalEntries
}

func convertLedgerAccountLinesToSnapshotLines(lines []LedgerAccountLine) []LedgerAccountLineSnapshot {
	converted := make([]LedgerAccountLineSnapshot, 0, len(lines))
	for _, line := range lines {
		converted = append(converted, LedgerAccountLineSnapshot{
			LedgerAccountIdentifier: line.LedgerAccountIdentifier,
			AmountInMinorUnits:      line.AmountInMinorUnits,
		})
	}
	return converted
}

func convertSnapshotLinesToLedgerAccountLines(lines []LedgerAccountLineSnapshot) []LedgerAccountLine {
	converted := make([]LedgerAccountLine, 0, len(lines))
	for _, line := range lines {
		converted = append(converted, LedgerAccountLine{
			LedgerAccountIdentifier: line.LedgerAccountIdentifier,
			AmountInMinorUnits:      line.AmountInMinorUnits,
		})
	}
	return converted
}
