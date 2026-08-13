package audittrail

import (
	"testing"
	"time"
)

func TestNewAuditTrailStartsEmpty(t *testing.T) {
	trail := NewAuditTrail()
	if len(trail.AllEntries()) != 0 {
		t.Fatalf("expected a new audit trail to start empty, got %d entries", len(trail.AllEntries()))
	}
}

func TestAppendStampsARecordedAtTimeAndPreservesFields(t *testing.T) {
	trail := NewAuditTrail()
	beforeAppend := time.Now()

	trail.Append(Entry{
		EventType:                         EventOrderSubmitted,
		ClientAccountIdentifier:           "acct-001",
		InstrumentSymbol:                  "DEMO-EQ",
		MatchingEngineOrderSequenceNumber: 7,
		DetailMessage:                     "buy 5 @ 100",
	})

	afterAppend := time.Now()
	entries := trail.AllEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	recordedEntry := entries[0]
	if recordedEntry.EventType != EventOrderSubmitted {
		t.Fatalf("expected EventOrderSubmitted, got %v", recordedEntry.EventType)
	}
	if recordedEntry.ClientAccountIdentifier != "acct-001" {
		t.Fatalf("expected acct-001, got %s", recordedEntry.ClientAccountIdentifier)
	}
	if recordedEntry.MatchingEngineOrderSequenceNumber != 7 {
		t.Fatalf("expected order id 7, got %d", recordedEntry.MatchingEngineOrderSequenceNumber)
	}
	if recordedEntry.RecordedAtTime.Before(beforeAppend) || recordedEntry.RecordedAtTime.After(afterAppend) {
		t.Fatalf("expected RecordedAtTime to be stamped between %v and %v, got %v", beforeAppend, afterAppend, recordedEntry.RecordedAtTime)
	}
}

func TestAllEntriesReturnsEntriesInAppendOrder(t *testing.T) {
	trail := NewAuditTrail()
	trail.Append(Entry{EventType: EventOrderSubmitted, DetailMessage: "first"})
	trail.Append(Entry{EventType: EventOrderCancelled, DetailMessage: "second"})
	trail.Append(Entry{EventType: EventOrderFilled, DetailMessage: "third"})

	entries := trail.AllEntries()
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries[0].DetailMessage != "first" || entries[1].DetailMessage != "second" || entries[2].DetailMessage != "third" {
		t.Fatalf("expected append order first,second,third — got %v", entries)
	}
}

func TestAllEntriesReturnsACopyNotTheInternalSlice(t *testing.T) {
	trail := NewAuditTrail()
	trail.Append(Entry{EventType: EventOrderSubmitted})

	entries := trail.AllEntries()
	entries[0].DetailMessage = "mutated by caller"

	freshEntries := trail.AllEntries()
	if freshEntries[0].DetailMessage == "mutated by caller" {
		t.Fatal("AllEntries must return a copy — caller mutation must not affect the trail's internal state")
	}
}

func TestEntriesForAccountFiltersToOnlyThatAccount(t *testing.T) {
	trail := NewAuditTrail()
	trail.Append(Entry{EventType: EventOrderSubmitted, ClientAccountIdentifier: "acct-001"})
	trail.Append(Entry{EventType: EventOrderSubmitted, ClientAccountIdentifier: "acct-002"})
	trail.Append(Entry{EventType: EventOrderCancelled, ClientAccountIdentifier: "acct-001"})
	trail.Append(Entry{EventType: EventMarketSessionOpened}) // no account at all

	acct001Entries := trail.EntriesForAccount("acct-001")
	if len(acct001Entries) != 2 {
		t.Fatalf("expected 2 entries for acct-001, got %d", len(acct001Entries))
	}
	for _, entry := range acct001Entries {
		if entry.ClientAccountIdentifier != "acct-001" {
			t.Fatalf("expected every returned entry to belong to acct-001, got %v", entry)
		}
	}
}

func TestEntriesForAccountReturnsEmptyForAnUnknownAccount(t *testing.T) {
	trail := NewAuditTrail()
	trail.Append(Entry{EventType: EventOrderSubmitted, ClientAccountIdentifier: "acct-001"})

	unknownAccountEntries := trail.EntriesForAccount("acct-999")
	if len(unknownAccountEntries) != 0 {
		t.Fatalf("expected 0 entries for an account with no activity, got %d", len(unknownAccountEntries))
	}
}
