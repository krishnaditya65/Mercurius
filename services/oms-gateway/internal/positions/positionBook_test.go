package positions

import "testing"

func TestFillIncrementsBuyerAndDecrementsSeller(t *testing.T) {
	positionBookUnderTest := NewPositionBook()

	positionBookUnderTest.ApplyFill("buyer", "seller", "DEMO-EQ", 5)

	buyerPositions := positionBookUnderTest.PositionsForAccount("buyer")
	sellerPositions := positionBookUnderTest.PositionsForAccount("seller")

	if buyerPositions["DEMO-EQ"] != 5 {
		t.Fatalf("expected buyer position 5, got %d", buyerPositions["DEMO-EQ"])
	}
	if sellerPositions["DEMO-EQ"] != -5 {
		t.Fatalf("expected seller position -5, got %d", sellerPositions["DEMO-EQ"])
	}
}

func TestMultipleFillsAccumulate(t *testing.T) {
	positionBookUnderTest := NewPositionBook()

	positionBookUnderTest.ApplyFill("acct-001", "acct-002", "DEMO-EQ", 5)
	positionBookUnderTest.ApplyFill("acct-001", "acct-002", "DEMO-EQ", 3)

	if got := positionBookUnderTest.PositionsForAccount("acct-001")["DEMO-EQ"]; got != 8 {
		t.Fatalf("expected accumulated position 8, got %d", got)
	}
}

func TestPositionThatNetsToZeroIsOmittedNotReturnedAsExplicitZero(t *testing.T) {
	positionBookUnderTest := NewPositionBook()

	positionBookUnderTest.ApplyFill("acct-001", "acct-002", "DEMO-EQ", 5)
	positionBookUnderTest.ApplyFill("acct-002", "acct-001", "DEMO-EQ", 5) // acct-001 sells back what it bought

	positions := positionBookUnderTest.PositionsForAccount("acct-001")
	if _, stillPresent := positions["DEMO-EQ"]; stillPresent {
		t.Fatalf("expected a net-zero position to be omitted, got %d", positions["DEMO-EQ"])
	}
}

func TestPositionsAreTrackedIndependentlyPerInstrument(t *testing.T) {
	positionBookUnderTest := NewPositionBook()

	positionBookUnderTest.ApplyFill("acct-001", "acct-002", "DEMO-EQ", 5)
	positionBookUnderTest.ApplyFill("acct-001", "acct-002", "OTHER-EQ", 2)

	positions := positionBookUnderTest.PositionsForAccount("acct-001")
	if positions["DEMO-EQ"] != 5 || positions["OTHER-EQ"] != 2 {
		t.Fatalf("unexpected positions: %+v", positions)
	}
}

func TestUnknownAccountHasNoPositions(t *testing.T) {
	positionBookUnderTest := NewPositionBook()

	positions := positionBookUnderTest.PositionsForAccount("acct-never-traded")
	if len(positions) != 0 {
		t.Fatalf("expected no positions, got %+v", positions)
	}
}

func TestSetPositionDirectly_OverwritesExistingQuantity(t *testing.T) {
	positionBookUnderTest := NewPositionBook()

	positionBookUnderTest.ApplyFill("acct-001", "acct-002", "DEMO-EQ", 10)
	positionBookUnderTest.SetPositionDirectly("acct-001", "DEMO-EQ", 20)

	if got := positionBookUnderTest.PositionsForAccount("acct-001")["DEMO-EQ"]; got != 20 {
		t.Fatalf("expected overwritten position 20, got %d", got)
	}
}

func TestSetPositionDirectly_WorksOnBrandNewAccount(t *testing.T) {
	positionBookUnderTest := NewPositionBook()

	positionBookUnderTest.SetPositionDirectly("acct-new", "DEMO-EQ", 15)

	if got := positionBookUnderTest.PositionsForAccount("acct-new")["DEMO-EQ"]; got != 15 {
		t.Fatalf("expected position 15, got %d", got)
	}
}

func TestSetPositionDirectly_DoesNotAffectOtherInstruments(t *testing.T) {
	positionBookUnderTest := NewPositionBook()

	positionBookUnderTest.ApplyFill("acct-001", "acct-002", "OTHER-EQ", 5)
	positionBookUnderTest.SetPositionDirectly("acct-001", "DEMO-EQ", 20)

	positions := positionBookUnderTest.PositionsForAccount("acct-001")
	if positions["DEMO-EQ"] != 20 || positions["OTHER-EQ"] != 5 {
		t.Fatalf("unexpected positions: %+v", positions)
	}
}
