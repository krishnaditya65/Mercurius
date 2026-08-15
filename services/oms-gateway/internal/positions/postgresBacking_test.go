// Real tests against a real, locally-running Postgres — no mocks. See
// docs/BUILD_LOG.md's Postgres-persistence entry.
package positions

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

func mustOpenTestPositionBook(t *testing.T) *PositionBook {
	t.Helper()
	book, connectError := NewPostgresBackedPositionBook(context.Background(), testOmsPostgresDsn())
	if connectError != nil {
		t.Skipf("skipping: real Postgres not reachable at %s: %v", testOmsPostgresDsn(), connectError)
	}
	if _, execError := book.postgres.pool.Exec(context.Background(), `TRUNCATE positions`); execError != nil {
		t.Fatalf("truncate positions: %v", execError)
	}
	t.Cleanup(book.postgres.pool.Close)
	return book
}

func TestPostgresBackedPositionBook_ApplyFill(t *testing.T) {
	book := mustOpenTestPositionBook(t)

	book.ApplyFill("buyer-1", "seller-1", "DEMO-EQ", 100)

	buyerPositions := book.PositionsForAccount("buyer-1")
	if buyerPositions["DEMO-EQ"] != 100 {
		t.Fatalf("expected buyer long 100, got %+v", buyerPositions)
	}
	sellerPositions := book.PositionsForAccount("seller-1")
	if sellerPositions["DEMO-EQ"] != -100 {
		t.Fatalf("expected seller short 100, got %+v", sellerPositions)
	}
}

func TestPostgresBackedPositionBook_MultipleFillsAccumulate(t *testing.T) {
	book := mustOpenTestPositionBook(t)

	book.ApplyFill("acct-a", "acct-b", "DEMO-EQ", 50)
	book.ApplyFill("acct-a", "acct-b", "DEMO-EQ", 30)

	positions := book.PositionsForAccount("acct-a")
	if positions["DEMO-EQ"] != 80 {
		t.Fatalf("expected accumulated position 80, got %+v", positions)
	}
}

func TestPostgresBackedPositionBook_NetZeroOmitted(t *testing.T) {
	book := mustOpenTestPositionBook(t)

	book.ApplyFill("acct-a", "acct-b", "DEMO-EQ", 50)
	book.ApplyFill("acct-b", "acct-a", "DEMO-EQ", 50) // reverses it exactly

	positions := book.PositionsForAccount("acct-a")
	if _, present := positions["DEMO-EQ"]; present {
		t.Fatalf("expected net-zero position omitted, got %+v", positions)
	}
}

func TestPostgresBackedPositionBook_SetPositionDirectly(t *testing.T) {
	book := mustOpenTestPositionBook(t)

	book.SetPositionDirectly("acct-corp-action", "DEMO-EQ", 250)
	positions := book.PositionsForAccount("acct-corp-action")
	if positions["DEMO-EQ"] != 250 {
		t.Fatalf("expected 250, got %+v", positions)
	}

	book.SetPositionDirectly("acct-corp-action", "DEMO-EQ", 500)
	positions = book.PositionsForAccount("acct-corp-action")
	if positions["DEMO-EQ"] != 500 {
		t.Fatalf("expected overwritten to 500, got %+v", positions)
	}
}

func TestPostgresBackedPositionBook_PersistsAcrossFreshConnection(t *testing.T) {
	firstBook := mustOpenTestPositionBook(t)
	firstBook.ApplyFill("restart-buyer", "restart-seller", "DEMO-EQ", 42)

	secondBook, connectError := NewPostgresBackedPositionBook(context.Background(), testOmsPostgresDsn())
	if connectError != nil {
		t.Fatalf("unexpected error opening second connection: %v", connectError)
	}
	defer secondBook.postgres.pool.Close()

	positions := secondBook.PositionsForAccount("restart-buyer")
	if positions["DEMO-EQ"] != 42 {
		t.Fatalf("expected position visible from a fresh connection, got %+v", positions)
	}
}

func TestPostgresBackedPositionBook_UnknownAccountReturnsEmpty(t *testing.T) {
	book := mustOpenTestPositionBook(t)
	positions := book.PositionsForAccount("does-not-exist")
	if len(positions) != 0 {
		t.Fatalf("expected empty map, got %+v", positions)
	}
}
