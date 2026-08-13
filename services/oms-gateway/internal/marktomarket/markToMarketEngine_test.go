package marktomarket

import (
	"sync"
	"testing"
)

func TestApplyFill_SingleBuyEstablishesCostBasis(t *testing.T) {
	engine := NewMarkToMarketEngine()
	engine.ApplyFill("buyer", "seller", "DEMO-EQ", 10, 100)

	_ = engine.SetMarketPrice("DEMO-EQ", 100)
	snapshots := engine.PositionsMTMForAccount("buyer")
	if len(snapshots) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snapshots))
	}
	if snapshots[0].NetQuantity != 10 || snapshots[0].AverageEntryPriceInMinorUnits != 100 {
		t.Fatalf("unexpected snapshot: %+v", snapshots[0])
	}
	if snapshots[0].UnrealizedPnLInMinorUnits != 0 {
		t.Fatalf("expected 0 PnL at same price, got %d", snapshots[0].UnrealizedPnLInMinorUnits)
	}
}

func TestApplyFill_WeightedAverageOnAddingSameDirection(t *testing.T) {
	engine := NewMarkToMarketEngine()
	// buy 10 @ 100, then buy 10 @ 200 -> weighted avg = (10*100+10*200)/20 = 150
	engine.ApplyFill("buyer", "seller", "DEMO-EQ", 10, 100)
	engine.ApplyFill("buyer", "seller", "DEMO-EQ", 10, 200)

	_ = engine.SetMarketPrice("DEMO-EQ", 150)
	snapshots := engine.PositionsMTMForAccount("buyer")
	if len(snapshots) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snapshots))
	}
	if snapshots[0].NetQuantity != 20 {
		t.Fatalf("expected net qty 20, got %d", snapshots[0].NetQuantity)
	}
	if snapshots[0].AverageEntryPriceInMinorUnits != 150 {
		t.Fatalf("expected weighted avg 150, got %d", snapshots[0].AverageEntryPriceInMinorUnits)
	}
}

func TestApplyFill_PartialCloseKeepsCostBasis(t *testing.T) {
	engine := NewMarkToMarketEngine()
	// buy 20 @ 100, sell 5 (partial close) -> remaining 15 @ 100 unchanged
	engine.ApplyFill("buyer", "seller1", "DEMO-EQ", 20, 100)
	engine.ApplyFill("someoneElse", "buyer", "DEMO-EQ", 5, 500)

	_ = engine.SetMarketPrice("DEMO-EQ", 120)
	snapshots := engine.PositionsMTMForAccount("buyer")
	if len(snapshots) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snapshots))
	}
	if snapshots[0].NetQuantity != 15 {
		t.Fatalf("expected net qty 15, got %d", snapshots[0].NetQuantity)
	}
	if snapshots[0].AverageEntryPriceInMinorUnits != 100 {
		t.Fatalf("expected avg price unchanged at 100, got %d", snapshots[0].AverageEntryPriceInMinorUnits)
	}
	// pnl = 15 * (120-100) = 300
	if snapshots[0].UnrealizedPnLInMinorUnits != 300 {
		t.Fatalf("expected pnl 300, got %d", snapshots[0].UnrealizedPnLInMinorUnits)
	}
}

func TestApplyFill_FullCloseResetsCostBasis(t *testing.T) {
	engine := NewMarkToMarketEngine()
	engine.ApplyFill("buyer", "seller", "DEMO-EQ", 10, 100)
	engine.ApplyFill("someoneElse", "buyer", "DEMO-EQ", 10, 100)

	_ = engine.SetMarketPrice("DEMO-EQ", 999)
	snapshots := engine.PositionsMTMForAccount("buyer")
	if len(snapshots) != 0 {
		t.Fatalf("expected 0 snapshots for a fully closed position, got %d", len(snapshots))
	}
}

func TestApplyFill_DirectionReversalRestartsCostBasis(t *testing.T) {
	engine := NewMarkToMarketEngine()
	// long 10 @ 100, then sell 15 (5 net short) -> the reversed 5 should
	// cost-basis at the fill price of the reversing trade (200), not
	// blend with the old long avg.
	engine.ApplyFill("trader", "counterparty1", "DEMO-EQ", 10, 100)
	engine.ApplyFill("counterparty2", "trader", "DEMO-EQ", 15, 200)

	_ = engine.SetMarketPrice("DEMO-EQ", 200)
	snapshots := engine.PositionsMTMForAccount("trader")
	if len(snapshots) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snapshots))
	}
	if snapshots[0].NetQuantity != -5 {
		t.Fatalf("expected net qty -5, got %d", snapshots[0].NetQuantity)
	}
	if snapshots[0].AverageEntryPriceInMinorUnits != 200 {
		t.Fatalf("expected reset avg price 200, got %d", snapshots[0].AverageEntryPriceInMinorUnits)
	}
}

func TestUnrealizedPnL_ShortPositionProfitsWhenPriceDrops(t *testing.T) {
	engine := NewMarkToMarketEngine()
	// short 100 @ 100 (sold high), price drops to 90 -> profit = 100*(100-90) = 1000
	engine.ApplyFill("counterparty", "trader", "DEMO-EQ", 100, 100)

	_ = engine.SetMarketPrice("DEMO-EQ", 90)
	snapshots := engine.PositionsMTMForAccount("trader")
	if len(snapshots) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snapshots))
	}
	if snapshots[0].NetQuantity != -100 {
		t.Fatalf("expected net qty -100, got %d", snapshots[0].NetQuantity)
	}
	if snapshots[0].UnrealizedPnLInMinorUnits != 1000 {
		t.Fatalf("expected pnl 1000, got %d", snapshots[0].UnrealizedPnLInMinorUnits)
	}
}

func TestUnrealizedPnL_LongPositionLossWhenPriceDrops(t *testing.T) {
	engine := NewMarkToMarketEngine()
	engine.ApplyFill("trader", "counterparty", "DEMO-EQ", 100, 100)

	_ = engine.SetMarketPrice("DEMO-EQ", 90)
	snapshots := engine.PositionsMTMForAccount("trader")
	// pnl = 100*(90-100) = -1000
	if snapshots[0].UnrealizedPnLInMinorUnits != -1000 {
		t.Fatalf("expected pnl -1000, got %d", snapshots[0].UnrealizedPnLInMinorUnits)
	}
}

func TestPositionsMTMForAccount_OmitsInstrumentWithoutKnownMarketPrice(t *testing.T) {
	engine := NewMarkToMarketEngine()
	engine.ApplyFill("trader", "counterparty", "DEMO-EQ", 10, 100)
	// no SetMarketPrice call at all

	snapshots := engine.PositionsMTMForAccount("trader")
	if len(snapshots) != 0 {
		t.Fatalf("expected 0 snapshots without a known market price, got %d", len(snapshots))
	}
}

func TestSetMarketPrice_RejectsNonPositive(t *testing.T) {
	engine := NewMarkToMarketEngine()
	if err := engine.SetMarketPrice("DEMO-EQ", 0); err != ErrMarketPriceMustBePositive {
		t.Fatalf("expected ErrMarketPriceMustBePositive, got %v", err)
	}
	if err := engine.SetMarketPrice("DEMO-EQ", -5); err != ErrMarketPriceMustBePositive {
		t.Fatalf("expected ErrMarketPriceMustBePositive, got %v", err)
	}
}

func TestAccountLevelUnrealizedPnL_SumsAcrossInstruments(t *testing.T) {
	engine := NewMarkToMarketEngine()
	engine.ApplyFill("trader", "cp", "DEMO-EQ", 10, 100)
	engine.ApplyFill("trader", "cp", "OTHER-EQ", 5, 50)
	_ = engine.SetMarketPrice("DEMO-EQ", 110) // pnl +100
	_ = engine.SetMarketPrice("OTHER-EQ", 40) // pnl -50

	total, snapshots := engine.AccountLevelUnrealizedPnL("trader")
	if total != 50 {
		t.Fatalf("expected total pnl 50, got %d", total)
	}
	if len(snapshots) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(snapshots))
	}
}

func TestMarketPrice_ReturnsKnownFlag(t *testing.T) {
	engine := NewMarkToMarketEngine()
	if _, known := engine.MarketPrice("DEMO-EQ"); known {
		t.Fatalf("expected unknown price before any SetMarketPrice call")
	}
	_ = engine.SetMarketPrice("DEMO-EQ", 42)
	price, known := engine.MarketPrice("DEMO-EQ")
	if !known || price != 42 {
		t.Fatalf("expected known price 42, got known=%v price=%d", known, price)
	}
}

func TestApplyFill_ZeroQuantityPositionsFilteredFromSnapshot(t *testing.T) {
	engine := NewMarkToMarketEngine()
	engine.ApplyFill("trader", "cp", "DEMO-EQ", 10, 100)
	engine.ApplyFill("cp", "trader", "DEMO-EQ", 10, 105) // fully closes trader's position
	engine.ApplyFill("trader", "cp", "OTHER-EQ", 5, 50)

	_ = engine.SetMarketPrice("DEMO-EQ", 100)
	_ = engine.SetMarketPrice("OTHER-EQ", 55)
	snapshots := engine.PositionsMTMForAccount("trader")
	if len(snapshots) != 1 || snapshots[0].InstrumentSymbol != "OTHER-EQ" {
		t.Fatalf("expected only OTHER-EQ to remain, got %+v", snapshots)
	}
}

func TestConcurrentApplyFillAndSetMarketPrice_NoRaceAndConsistentTotals(t *testing.T) {
	engine := NewMarkToMarketEngine()
	var waitGroup sync.WaitGroup
	for i := 0; i < 50; i++ {
		waitGroup.Add(2)
		go func() {
			defer waitGroup.Done()
			engine.ApplyFill("trader", "cp", "DEMO-EQ", 1, 100)
		}()
		go func() {
			defer waitGroup.Done()
			_ = engine.SetMarketPrice("DEMO-EQ", 100)
		}()
	}
	waitGroup.Wait()

	snapshots := engine.PositionsMTMForAccount("trader")
	if len(snapshots) != 1 || snapshots[0].NetQuantity != 50 {
		t.Fatalf("expected net qty 50 after 50 concurrent fills, got %+v", snapshots)
	}
}
