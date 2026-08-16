package marginpledge

import (
	"errors"
	"sync"
	"testing"
)

// TestPledgeHoldingWorkedExample hand-computes the margin value
// contributed by a pledge against the package's own documented haircut
// constants.
//
// Hand computation: DEMO-EQ has an explicit 15% haircut override.
// quantity=10, referencePriceInMinorUnits=10,000 (₹100.00).
// raw value = 10 * 10,000 = 100,000 paise.
// haircut-adjusted = 100,000 * (1 - 0.15) = 85,000 paise.
func TestPledgeHoldingWorkedExample(t *testing.T) {
	pledgeBookUnderTest := NewPledgeBook()

	record, marginValueContributed, err := pledgeBookUnderTest.PledgeHolding("acct-001", "DEMO-EQ", 10, 10_000, 50)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if marginValueContributed != 85_000 {
		t.Fatalf("expected margin value contributed 85000, got %d", marginValueContributed)
	}
	if record.PledgedQuantity != 10 {
		t.Fatalf("expected pledged quantity 10, got %d", record.PledgedQuantity)
	}
	if record.HaircutPercentApplied != 0.15 {
		t.Fatalf("expected haircut 0.15 for DEMO-EQ, got %f", record.HaircutPercentApplied)
	}
}

func TestPledgeHoldingUsesDefaultHaircutForUnlistedSymbol(t *testing.T) {
	pledgeBookUnderTest := NewPledgeBook()

	record, marginValueContributed, err := pledgeBookUnderTest.PledgeHolding("acct-001", "SOME-OTHER-EQ", 10, 10_000, 50)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if record.HaircutPercentApplied != defaultHaircutPercent {
		t.Fatalf("expected default haircut %f, got %f", defaultHaircutPercent, record.HaircutPercentApplied)
	}
	// raw value = 100000, haircut-adjusted at 20% = 80000.
	if marginValueContributed != 80_000 {
		t.Fatalf("expected margin value contributed 80000, got %d", marginValueContributed)
	}
}

func TestPledgeHoldingRejectsZeroQuantity(t *testing.T) {
	pledgeBookUnderTest := NewPledgeBook()

	_, _, err := pledgeBookUnderTest.PledgeHolding("acct-001", "DEMO-EQ", 0, 10_000, 50)
	if err != ErrPledgeQuantityMustBePositive {
		t.Fatalf("expected ErrPledgeQuantityMustBePositive, got %v", err)
	}
}

func TestPledgeHoldingRejectsNonPositiveReferencePrice(t *testing.T) {
	pledgeBookUnderTest := NewPledgeBook()

	_, _, err := pledgeBookUnderTest.PledgeHolding("acct-001", "DEMO-EQ", 5, 0, 50)
	if err != ErrReferencePriceMustBePositive {
		t.Fatalf("expected ErrReferencePriceMustBePositive, got %v", err)
	}
}

func TestPledgeHoldingRejectsQuantityExceedingCurrentHolding(t *testing.T) {
	pledgeBookUnderTest := NewPledgeBook()

	_, _, err := pledgeBookUnderTest.PledgeHolding("acct-001", "DEMO-EQ", 100, 10_000, 50)
	if err != ErrInsufficientUnpledgedHoldingQuantity {
		t.Fatalf("expected ErrInsufficientUnpledgedHoldingQuantity, got %v", err)
	}
}

func TestPledgeHoldingRejectsQuantityExceedingRemainingUnpledgedHolding(t *testing.T) {
	pledgeBookUnderTest := NewPledgeBook()

	// Pledge 40 of a 50-share holding, then try to pledge another 20 —
	// only 10 remains unpledged.
	if _, _, err := pledgeBookUnderTest.PledgeHolding("acct-001", "DEMO-EQ", 40, 10_000, 50); err != nil {
		t.Fatalf("unexpected error on first pledge: %v", err)
	}
	if _, _, err := pledgeBookUnderTest.PledgeHolding("acct-001", "DEMO-EQ", 20, 10_000, 50); err != ErrInsufficientUnpledgedHoldingQuantity {
		t.Fatalf("expected ErrInsufficientUnpledgedHoldingQuantity, got %v", err)
	}
}

func TestMultiplePledgeCallsAccumulateOnTheSameRecord(t *testing.T) {
	pledgeBookUnderTest := NewPledgeBook()

	if _, _, err := pledgeBookUnderTest.PledgeHolding("acct-001", "DEMO-EQ", 10, 10_000, 50); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	record, _, err := pledgeBookUnderTest.PledgeHolding("acct-001", "DEMO-EQ", 15, 10_000, 50)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if record.PledgedQuantity != 25 {
		t.Fatalf("expected accumulated pledged quantity 25, got %d", record.PledgedQuantity)
	}
}

func TestUnpledgeHoldingReleasesMarginValueAndReducesQuantity(t *testing.T) {
	pledgeBookUnderTest := NewPledgeBook()

	if _, _, err := pledgeBookUnderTest.PledgeHolding("acct-001", "DEMO-EQ", 10, 10_000, 50); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	marginValueReleased, err := pledgeBookUnderTest.UnpledgeHolding("acct-001", "DEMO-EQ", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if marginValueReleased != 85_000 {
		t.Fatalf("expected margin value released 85000, got %d", marginValueReleased)
	}
	if pledgeBookUnderTest.PledgedQuantity("acct-001", "DEMO-EQ") != 0 {
		t.Fatal("expected pledged quantity to be 0 after fully unpledging")
	}
}

func TestUnpledgeHoldingPartialReleaseKeepsRemainderPledged(t *testing.T) {
	pledgeBookUnderTest := NewPledgeBook()

	if _, _, err := pledgeBookUnderTest.PledgeHolding("acct-001", "DEMO-EQ", 10, 10_000, 50); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	marginValueReleased, err := pledgeBookUnderTest.UnpledgeHolding("acct-001", "DEMO-EQ", 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 4/10 of the 85000 contributed = 34000.
	if marginValueReleased != 34_000 {
		t.Fatalf("expected margin value released 34000, got %d", marginValueReleased)
	}
	if pledgeBookUnderTest.PledgedQuantity("acct-001", "DEMO-EQ") != 6 {
		t.Fatalf("expected 6 shares still pledged, got %d", pledgeBookUnderTest.PledgedQuantity("acct-001", "DEMO-EQ"))
	}
}

func TestUnpledgeHoldingRejectsZeroQuantity(t *testing.T) {
	pledgeBookUnderTest := NewPledgeBook()
	if _, _, err := pledgeBookUnderTest.PledgeHolding("acct-001", "DEMO-EQ", 10, 10_000, 50); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := pledgeBookUnderTest.UnpledgeHolding("acct-001", "DEMO-EQ", 0); err != ErrPledgeQuantityMustBePositive {
		t.Fatalf("expected ErrPledgeQuantityMustBePositive, got %v", err)
	}
}

func TestUnpledgeHoldingRejectsWhenNoPledgeExists(t *testing.T) {
	pledgeBookUnderTest := NewPledgeBook()

	if _, err := pledgeBookUnderTest.UnpledgeHolding("acct-001", "DEMO-EQ", 1); err != ErrNoPledgeFoundForSymbol {
		t.Fatalf("expected ErrNoPledgeFoundForSymbol, got %v", err)
	}
}

func TestUnpledgeHoldingRejectsQuantityExceedingPledgedQuantity(t *testing.T) {
	pledgeBookUnderTest := NewPledgeBook()
	if _, _, err := pledgeBookUnderTest.PledgeHolding("acct-001", "DEMO-EQ", 10, 10_000, 50); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := pledgeBookUnderTest.UnpledgeHolding("acct-001", "DEMO-EQ", 11); err != ErrUnpledgeQuantityExceedsPledgedQuantity {
		t.Fatalf("expected ErrUnpledgeQuantityExceedsPledgedQuantity, got %v", err)
	}
}

// TestUnpledgeHoldingIsBlockedWhileBackingAnOpenMarginPosition is the
// real state-machine guard: once utilized margin is set (representing an
// open derivative position relying on this collateral), unpledging
// enough to drop below that utilized figure must be refused.
func TestUnpledgeHoldingIsBlockedWhileBackingAnOpenMarginPosition(t *testing.T) {
	pledgeBookUnderTest := NewPledgeBook()
	if _, _, err := pledgeBookUnderTest.PledgeHolding("acct-001", "DEMO-EQ", 10, 10_000, 50); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Pledged margin value = 85000. Utilize all of it against an open
	// position.
	pledgeBookUnderTest.SetUtilizedMarginInMinorUnits("acct-001", 85_000)

	if _, err := pledgeBookUnderTest.UnpledgeHolding("acct-001", "DEMO-EQ", 1); err != ErrPledgeStillBackingOpenMarginPosition {
		t.Fatalf("expected ErrPledgeStillBackingOpenMarginPosition, got %v", err)
	}
}

// TestUnpledgeHoldingSucceedsWhenSurplusCollateralRemainsAboveUtilizedMargin
// proves the guard is a real inequality check, not a blanket "any
// utilized margin blocks any unpledge" rule.
func TestUnpledgeHoldingSucceedsWhenSurplusCollateralRemainsAboveUtilizedMargin(t *testing.T) {
	pledgeBookUnderTest := NewPledgeBook()
	if _, _, err := pledgeBookUnderTest.PledgeHolding("acct-001", "DEMO-EQ", 20, 10_000, 50); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Pledged margin value = 20 * 10000 * 0.85 = 170000. Only 50000 is
	// utilized, leaving 120000 of surplus collateral.
	pledgeBookUnderTest.SetUtilizedMarginInMinorUnits("acct-001", 50_000)

	// Unpledge 5 shares -> releases 5/20 of 170000 = 42500, leaving
	// 127500 pledged, still comfortably above the 50000 utilized.
	marginValueReleased, err := pledgeBookUnderTest.UnpledgeHolding("acct-001", "DEMO-EQ", 5)
	if err != nil {
		t.Fatalf("expected unpledge to succeed with surplus collateral, got error: %v", err)
	}
	if marginValueReleased != 42_500 {
		t.Fatalf("expected margin value released 42500, got %d", marginValueReleased)
	}
}

func TestPledgesForAccountReturnsIndependentCopiesPerSymbol(t *testing.T) {
	pledgeBookUnderTest := NewPledgeBook()
	if _, _, err := pledgeBookUnderTest.PledgeHolding("acct-001", "DEMO-EQ", 10, 10_000, 50); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, _, err := pledgeBookUnderTest.PledgeHolding("acct-001", "OTHER-EQ", 5, 20_000, 20); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pledges := pledgeBookUnderTest.PledgesForAccount("acct-001")
	if len(pledges) != 2 {
		t.Fatalf("expected 2 pledge records, got %d", len(pledges))
	}
	if pledges["DEMO-EQ"].PledgedQuantity != 10 {
		t.Fatalf("expected DEMO-EQ pledged quantity 10, got %d", pledges["DEMO-EQ"].PledgedQuantity)
	}
	if pledges["OTHER-EQ"].PledgedQuantity != 5 {
		t.Fatalf("expected OTHER-EQ pledged quantity 5, got %d", pledges["OTHER-EQ"].PledgedQuantity)
	}
}

func TestPledgedQuantityReturnsZeroForNeverPledgedSymbol(t *testing.T) {
	pledgeBookUnderTest := NewPledgeBook()

	if got := pledgeBookUnderTest.PledgedQuantity("acct-001", "NEVER-PLEDGED"); got != 0 {
		t.Fatalf("expected 0, got %d", got)
	}
}

// TestConcurrentPledgeAndUnpledgeCallsAreRaceFree exercises the mutex
// under concurrent access — run with `go test -race`.
func TestConcurrentPledgeAndUnpledgeCallsAreRaceFree(t *testing.T) {
	pledgeBookUnderTest := NewPledgeBook()
	var waitGroup sync.WaitGroup

	for i := 0; i < 20; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, _, _ = pledgeBookUnderTest.PledgeHolding("acct-concurrent", "DEMO-EQ", 1, 10_000, 1000)
		}()
	}
	waitGroup.Wait()

	if got := pledgeBookUnderTest.PledgedQuantity("acct-concurrent", "DEMO-EQ"); got != 20 {
		t.Fatalf("expected 20 pledged after 20 concurrent pledges of 1 each, got %d", got)
	}

	for i := 0; i < 20; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, _ = pledgeBookUnderTest.UnpledgeHolding("acct-concurrent", "DEMO-EQ", 1)
		}()
	}
	waitGroup.Wait()

	if got := pledgeBookUnderTest.PledgedQuantity("acct-concurrent", "DEMO-EQ"); got != 0 {
		t.Fatalf("expected 0 pledged after unpledging everything, got %d", got)
	}
}

// TestReserveUnpledgedQuantityForSell_ConcurrentSellsCannotBothOversell is
// the direct reproduction of the confirmed TOCTOU bug: two concurrent
// sell checks against the SAME unpledged holding, each individually
// within the available (unpledged) quantity but together exceeding it,
// must NOT both be allowed to reserve. A separate PledgedQuantity() read
// followed by an independent PositionsForAccount() read (the old
// pattern) has no way to prevent this; an atomic check-and-reserve does.
func TestReserveUnpledgedQuantityForSell_ConcurrentSellsCannotBothOversell(t *testing.T) {
	pledgeBookUnderTest := NewPledgeBook()
	const netHolding = int64(100) // nothing pledged -- all 100 unpledged

	var waitGroup sync.WaitGroup
	var mutexGuardingSuccessCount sync.Mutex
	successCount := 0

	// 3 concurrent sell checks of 60 shares each against a 100-share
	// unpledged holding: only ONE can succeed (60 <= 100), a second
	// would need 120 > 100.
	for i := 0; i < 3; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			if err := pledgeBookUnderTest.ReserveUnpledgedQuantityForSell("acct-1", "DEMO-EQ", 60, netHolding); err == nil {
				mutexGuardingSuccessCount.Lock()
				successCount++
				mutexGuardingSuccessCount.Unlock()
			}
		}()
	}
	waitGroup.Wait()

	if successCount != 1 {
		t.Fatalf("expected exactly 1 of 3 concurrent 60-share sell reservations to succeed against a 100-share holding, got %d", successCount)
	}
}

func TestReserveUnpledgedQuantityForSell_RejectsWhenPledgedQuantityLeavesInsufficientUnpledged(t *testing.T) {
	pledgeBookUnderTest := NewPledgeBook()
	_, _, err := pledgeBookUnderTest.PledgeHolding("acct-1", "DEMO-EQ", 80, 10_000, 100) // 100 held, 80 pledged -> 20 unpledged
	if err != nil {
		t.Fatalf("unexpected pledge error: %v", err)
	}

	if err := pledgeBookUnderTest.ReserveUnpledgedQuantityForSell("acct-1", "DEMO-EQ", 21, 100); !errors.Is(err, ErrInsufficientUnpledgedHoldingQuantity) {
		t.Fatalf("expected ErrInsufficientUnpledgedHoldingQuantity selling 21 of only 20 unpledged, got %v", err)
	}
	if err := pledgeBookUnderTest.ReserveUnpledgedQuantityForSell("acct-1", "DEMO-EQ", 20, 100); err != nil {
		t.Fatalf("expected exactly the unpledged quantity to be reservable, got %v", err)
	}
}

func TestReserveUnpledgedQuantityForSell_SecondReservationStacksAgainstFirst(t *testing.T) {
	pledgeBookUnderTest := NewPledgeBook()
	const netHolding = int64(100)

	if err := pledgeBookUnderTest.ReserveUnpledgedQuantityForSell("acct-1", "DEMO-EQ", 70, netHolding); err != nil {
		t.Fatalf("unexpected error on first reservation: %v", err)
	}
	// 70 already reserved, 100 held -> only 30 left; a second 40-share
	// reservation must be rejected even though NO pledge exists at all.
	if err := pledgeBookUnderTest.ReserveUnpledgedQuantityForSell("acct-1", "DEMO-EQ", 40, netHolding); !errors.Is(err, ErrInsufficientUnpledgedHoldingQuantity) {
		t.Fatalf("expected the second reservation to be rejected (70+40 > 100 net holding), got %v", err)
	}
}

func TestReleaseSellReservation_GivesBackCapacityFlooredAtZero(t *testing.T) {
	pledgeBookUnderTest := NewPledgeBook()
	const netHolding = int64(100)

	if err := pledgeBookUnderTest.ReserveUnpledgedQuantityForSell("acct-1", "DEMO-EQ", 100, netHolding); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := pledgeBookUnderTest.ReserveUnpledgedQuantityForSell("acct-1", "DEMO-EQ", 1, netHolding); err == nil {
		t.Fatal("expected no capacity left after fully reserving the holding")
	}

	pledgeBookUnderTest.ReleaseSellReservation("acct-1", "DEMO-EQ", 100)

	if err := pledgeBookUnderTest.ReserveUnpledgedQuantityForSell("acct-1", "DEMO-EQ", 100, netHolding); err != nil {
		t.Fatalf("expected full capacity restored after release, got %v", err)
	}

	// Releasing more than was ever reserved must floor at zero, not
	// panic or underflow into a negative reservation that would
	// incorrectly grant MORE than the real holding.
	pledgeBookUnderTest.ReleaseSellReservation("acct-1", "DEMO-EQ", 99999)
	if err := pledgeBookUnderTest.ReserveUnpledgedQuantityForSell("acct-1", "DEMO-EQ", 100, netHolding); err != nil {
		t.Fatalf("expected full capacity still available (floored, not over-released), got %v", err)
	}
}
