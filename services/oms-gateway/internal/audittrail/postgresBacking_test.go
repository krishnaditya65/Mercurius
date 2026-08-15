// Real tests against a real, locally-running Postgres — no mocks. See
// docs/BUILD_LOG.md's Postgres-persistence entry: run against the
// actual `make infra-up` container (host port remapped to 5433 on this
// build's dev machine — see infra/docker/docker-compose.yml's comment).
package audittrail

import (
	"context"
	"os"
	"testing"
)

func testOmsPostgresDsn() string {
	if dsn := os.Getenv("OMS_PGSTORE_TEST_DSN"); dsn != "" {
		return dsn
	}
	return "postgres://trading:trading@localhost:5432/omsgateway"
}

func mustOpenTestAuditTrail(t *testing.T) *AuditTrail {
	t.Helper()
	trail, connectError := NewPostgresBackedAuditTrail(context.Background(), testOmsPostgresDsn())
	if connectError != nil {
		t.Skipf("skipping: real Postgres not reachable at %s: %v", testOmsPostgresDsn(), connectError)
	}
	if _, execError := trail.postgres.pool.Exec(context.Background(), `TRUNCATE audit_trail_entries RESTART IDENTITY`); execError != nil {
		t.Fatalf("truncate audit_trail_entries: %v", execError)
	}
	t.Cleanup(trail.postgres.pool.Close)
	return trail
}

func TestPostgresBackedAuditTrail_AppendAndAllEntries(t *testing.T) {
	trail := mustOpenTestAuditTrail(t)

	trail.Append(Entry{
		EventType:                           EventOrderSubmitted,
		ClientAccountIdentifier:             "acct-001",
		InstrumentSymbol:                    "DEMO-EQ",
		AuthenticatedActorAccountIdentifier: "acct-001",
	})
	trail.Append(Entry{
		EventType:               EventOrderRejected,
		ClientAccountIdentifier: "acct-002",
		InstrumentSymbol:        "DEMO-EQ",
		DetailMessage:           "INSUFFICIENT_MARGIN",
	})

	all := trail.AllEntries()
	if len(all) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(all))
	}
	if all[0].EventType != EventOrderSubmitted || all[0].ClientAccountIdentifier != "acct-001" {
		t.Fatalf("unexpected first entry: %+v", all[0])
	}
	if all[0].AuthenticatedActorAccountIdentifier != "acct-001" {
		t.Fatalf("expected authenticated actor recorded, got %+v", all[0])
	}
	if all[1].DetailMessage != "INSUFFICIENT_MARGIN" {
		t.Fatalf("unexpected second entry: %+v", all[1])
	}
	if all[0].RecordedAtTime.IsZero() {
		t.Fatal("expected RecordedAtTime to be stamped")
	}
}

func TestPostgresBackedAuditTrail_EntriesForAccount(t *testing.T) {
	trail := mustOpenTestAuditTrail(t)

	trail.Append(Entry{EventType: EventOrderSubmitted, ClientAccountIdentifier: "acct-001"})
	trail.Append(Entry{EventType: EventOrderSubmitted, ClientAccountIdentifier: "acct-002"})
	trail.Append(Entry{EventType: EventOrderFilled, ClientAccountIdentifier: "acct-001"})

	acct1Entries := trail.EntriesForAccount("acct-001")
	if len(acct1Entries) != 2 {
		t.Fatalf("expected 2 entries for acct-001, got %d", len(acct1Entries))
	}

	unknownEntries := trail.EntriesForAccount("acct-nonexistent")
	if len(unknownEntries) != 0 {
		t.Fatalf("expected 0 entries for unknown account, got %d", len(unknownEntries))
	}
}

func TestPostgresBackedAuditTrail_PersistsAcrossFreshConnection(t *testing.T) {
	firstTrail := mustOpenTestAuditTrail(t)
	firstTrail.Append(Entry{EventType: EventOrderFilled, ClientAccountIdentifier: "acct-restart-test", DetailMessage: "persisted"})

	secondTrail, connectError := NewPostgresBackedAuditTrail(context.Background(), testOmsPostgresDsn())
	if connectError != nil {
		t.Fatalf("unexpected error opening second connection: %v", connectError)
	}
	defer secondTrail.postgres.pool.Close()

	entries := secondTrail.EntriesForAccount("acct-restart-test")
	if len(entries) != 1 || entries[0].DetailMessage != "persisted" {
		t.Fatalf("expected entry visible from a fresh connection, got %+v", entries)
	}
}

func TestPostgresBackedAuditTrail_NilOrderSideRoundTrips(t *testing.T) {
	trail := mustOpenTestAuditTrail(t)
	isBuy := true
	trail.Append(Entry{EventType: EventOrderSubmitted, ClientAccountIdentifier: "acct-side-test", OrderSideIsBuyNotSell: &isBuy})
	trail.Append(Entry{EventType: EventOrderCancelled, ClientAccountIdentifier: "acct-side-test-2"}) // OrderSideIsBuyNotSell nil

	all := trail.AllEntries()
	if len(all) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(all))
	}
	if all[0].OrderSideIsBuyNotSell == nil || !*all[0].OrderSideIsBuyNotSell {
		t.Fatalf("expected OrderSideIsBuyNotSell=true, got %+v", all[0])
	}
	if all[1].OrderSideIsBuyNotSell != nil {
		t.Fatalf("expected nil OrderSideIsBuyNotSell, got %+v", all[1])
	}
}
