package fractionalshares

import "testing"

func TestApplyFractionalFill_IncrementsBuyerAndDecrementsSeller(t *testing.T) {
	book := NewMilliSharePositionBook()
	book.ApplyFractionalFill("buyer", "seller", "DEMO-EQ", 300) // 0.300 share

	buyerPositions := book.PositionsForAccount("buyer")
	sellerPositions := book.PositionsForAccount("seller")

	if buyerPositions["DEMO-EQ"] != 300 {
		t.Fatalf("expected buyer 300, got %d", buyerPositions["DEMO-EQ"])
	}
	if sellerPositions["DEMO-EQ"] != -300 {
		t.Fatalf("expected seller -300, got %d", sellerPositions["DEMO-EQ"])
	}
}

func TestApplyFractionalFill_AccumulatesAcrossMultipleFills(t *testing.T) {
	book := NewMilliSharePositionBook()
	book.ApplyFractionalFill("acct-1", "acct-2", "DEMO-EQ", 300)
	book.ApplyFractionalFill("acct-1", "acct-2", "DEMO-EQ", 200)

	if got := book.PositionsForAccount("acct-1")["DEMO-EQ"]; got != 500 {
		t.Fatalf("expected accumulated 500 (0.500 share), got %d", got)
	}
}

func TestApplyFractionalFill_NetZeroOmitted(t *testing.T) {
	book := NewMilliSharePositionBook()
	book.ApplyFractionalFill("acct-1", "acct-2", "DEMO-EQ", 500)
	book.ApplyFractionalFill("acct-2", "acct-1", "DEMO-EQ", 500)

	positions := book.PositionsForAccount("acct-1")
	if _, present := positions["DEMO-EQ"]; present {
		t.Fatalf("expected net-zero fractional position to be omitted, got %d", positions["DEMO-EQ"])
	}
}

func TestApplyFractionalFill_IndependentPerInstrument(t *testing.T) {
	book := NewMilliSharePositionBook()
	book.ApplyFractionalFill("acct-1", "acct-2", "DEMO-EQ", 300)
	book.ApplyFractionalFill("acct-1", "acct-2", "OTHER-EQ", 700)

	positions := book.PositionsForAccount("acct-1")
	if positions["DEMO-EQ"] != 300 || positions["OTHER-EQ"] != 700 {
		t.Fatalf("unexpected positions: %+v", positions)
	}
}

func TestPositionsForAccount_UnknownAccountEmpty(t *testing.T) {
	book := NewMilliSharePositionBook()
	positions := book.PositionsForAccount("never-traded")
	if len(positions) != 0 {
		t.Fatalf("expected no positions, got %+v", positions)
	}
}

func TestApplyFractionalFill_SubOneShareFullLifecycle(t *testing.T) {
	// Buy 0.333 share, then sell 0.133 share -- net should be exactly
	// 0.200 share (200 milli-units), proving integer arithmetic never
	// drifts the way repeated float addition/subtraction could.
	book := NewMilliSharePositionBook()
	book.ApplyFractionalFill("acct-1", "market", "DEMO-EQ", 333)
	book.ApplyFractionalFill("market", "acct-1", "DEMO-EQ", 133)

	if got := book.PositionsForAccount("acct-1")["DEMO-EQ"]; got != 200 {
		t.Fatalf("expected exactly 200 milli-units (0.200 share), got %d", got)
	}
}
