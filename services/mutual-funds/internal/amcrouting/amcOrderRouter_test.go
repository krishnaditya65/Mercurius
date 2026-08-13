package amcrouting

import (
	"testing"
	"time"

	"mercurius/mutualFunds/internal/fundcatalog"
)

const testSchemeId = "MF-DT-LIQUID003" // seed NAV: 100000 minor units (₹1000.00)

func newTestRouter(confirmationDelay time.Duration) *AmcOrderRouter {
	return NewAmcOrderRouter(fundcatalog.NewFundCatalog(), confirmationDelay)
}

func TestPlacePurchaseOrderRejectsNonPositiveAmount(t *testing.T) {
	router := newTestRouter(24 * time.Hour)
	now := time.Now()

	if _, placeError := router.PlacePurchaseOrder("acct-1", testSchemeId, 0, now); placeError != ErrInvalidAmount {
		t.Errorf("expected ErrInvalidAmount for zero amount, got %v", placeError)
	}
	if _, placeError := router.PlacePurchaseOrder("acct-1", testSchemeId, -500, now); placeError != ErrInvalidAmount {
		t.Errorf("expected ErrInvalidAmount for negative amount, got %v", placeError)
	}
}

func TestPlacePurchaseOrderRejectsUnknownScheme(t *testing.T) {
	router := newTestRouter(24 * time.Hour)

	if _, placeError := router.PlacePurchaseOrder("acct-1", "MF-DOES-NOT-EXIST", 500000, time.Now()); placeError != ErrUnknownScheme {
		t.Errorf("expected ErrUnknownScheme, got %v", placeError)
	}
}

func TestPlacePurchaseOrderStartsPendingWithNoUnitsAllocatedYet(t *testing.T) {
	router := newTestRouter(24 * time.Hour)
	now := time.Now()

	order, placeError := router.PlacePurchaseOrder("acct-1", testSchemeId, 500000, now)
	if placeError != nil {
		t.Fatalf("unexpected error: %v", placeError)
	}
	if order.Status != StatusPendingConfirmation {
		t.Errorf("expected StatusPendingConfirmation, got %s", order.Status)
	}
	if order.UnitsAllocated != 0 {
		t.Errorf("expected no units allocated before confirmation, got %v", order.UnitsAllocated)
	}

	holdings := router.HoldingsForAccount("acct-1")
	if len(holdings) != 0 {
		t.Errorf("expected no holdings before confirmation, got %+v", holdings)
	}
}

// TestConfirmDueOrdersAllocatesCorrectUnitsForKnownNav is the hand-worked
// example the task calls for: ₹5000 (500000 minor units) invested at a
// known NAV of ₹1000.00 (100000 minor units) allocates exactly 5.0 units.
func TestConfirmDueOrdersAllocatesCorrectUnitsForKnownNav(t *testing.T) {
	router := newTestRouter(0) // confirm instantly for this test
	now := time.Now()

	order, placeError := router.PlacePurchaseOrder("acct-1", testSchemeId, 500000, now)
	if placeError != nil {
		t.Fatalf("unexpected error: %v", placeError)
	}

	confirmed, failed := router.ConfirmDueOrders(now)
	if len(failed) != 0 {
		t.Fatalf("expected no failures, got %v", failed)
	}
	if len(confirmed) != 1 || confirmed[0].OrderId != order.OrderId {
		t.Fatalf("expected exactly the one order confirmed, got %+v", confirmed)
	}
	if confirmed[0].UnitsAllocated != 5.0 {
		t.Errorf("expected exactly 5.0 units allocated (500000/100000), got %v", confirmed[0].UnitsAllocated)
	}

	holdings := router.HoldingsForAccount("acct-1")
	if len(holdings) != 1 || holdings[0].TotalUnits != 5.0 {
		t.Fatalf("expected holding of 5.0 total units, got %+v", holdings)
	}
}

func TestConfirmDueOrdersDoesNothingBeforeEligibleTime(t *testing.T) {
	router := newTestRouter(24 * time.Hour)
	now := time.Now()

	if _, placeError := router.PlacePurchaseOrder("acct-1", testSchemeId, 500000, now); placeError != nil {
		t.Fatalf("unexpected error: %v", placeError)
	}

	confirmed, _ := router.ConfirmDueOrders(now.Add(1 * time.Hour)) // still well before the 24h delay
	if len(confirmed) != 0 {
		t.Errorf("expected no orders confirmed before the confirmation delay elapses, got %+v", confirmed)
	}
	if len(router.HoldingsForAccount("acct-1")) != 0 {
		t.Errorf("expected no holdings yet")
	}
}

// TestConfirmDueOrdersIsIdempotent proves sweeping twice in a row before
// anything new becomes due does nothing the second time.
func TestConfirmDueOrdersIsIdempotent(t *testing.T) {
	router := newTestRouter(0)
	now := time.Now()

	if _, placeError := router.PlacePurchaseOrder("acct-1", testSchemeId, 500000, now); placeError != nil {
		t.Fatalf("unexpected error: %v", placeError)
	}

	firstSweepConfirmed, _ := router.ConfirmDueOrders(now)
	if len(firstSweepConfirmed) != 1 {
		t.Fatalf("expected first sweep to confirm exactly 1 order, got %d", len(firstSweepConfirmed))
	}

	secondSweepConfirmed, secondSweepFailed := router.ConfirmDueOrders(now)
	if len(secondSweepConfirmed) != 0 || len(secondSweepFailed) != 0 {
		t.Errorf("expected second sweep to do nothing, got confirmed=%v failed=%v", secondSweepConfirmed, secondSweepFailed)
	}

	holdings := router.HoldingsForAccount("acct-1")
	if len(holdings) != 1 || holdings[0].TotalUnits != 5.0 {
		t.Errorf("expected holding to still be exactly 5.0 units after the second, no-op sweep, got %+v", holdings)
	}
}

func TestPlaceRedemptionOrderRejectsInsufficientUnits(t *testing.T) {
	router := newTestRouter(0)
	now := time.Now()

	// No purchase confirmed yet, so the account has 0 units.
	if _, placeError := router.PlaceRedemptionOrder("acct-1", testSchemeId, 1.0, now); placeError != ErrInsufficientUnits {
		t.Errorf("expected ErrInsufficientUnits, got %v", placeError)
	}
}

func TestPlaceRedemptionOrderRejectsNonPositiveUnits(t *testing.T) {
	router := newTestRouter(0)

	if _, placeError := router.PlaceRedemptionOrder("acct-1", testSchemeId, 0, time.Now()); placeError != ErrInvalidUnits {
		t.Errorf("expected ErrInvalidUnits, got %v", placeError)
	}
}

func TestRedemptionReservesUnitsAndConfirmationRemovesThemAndCreditsAmount(t *testing.T) {
	router := newTestRouter(0)
	now := time.Now()

	// Fund the account: 500000 / 100000 = 5.0 units.
	if _, placeError := router.PlacePurchaseOrder("acct-1", testSchemeId, 500000, now); placeError != nil {
		t.Fatalf("unexpected error: %v", placeError)
	}
	router.ConfirmDueOrders(now)

	redemptionOrder, placeError := router.PlaceRedemptionOrder("acct-1", testSchemeId, 2.0, now)
	if placeError != nil {
		t.Fatalf("unexpected error: %v", placeError)
	}

	holdingsAfterPlace := router.HoldingsForAccount("acct-1")
	if holdingsAfterPlace[0].AvailableUnits() != 3.0 {
		t.Errorf("expected 3.0 available units immediately after reserving 2.0 of 5.0, got %v", holdingsAfterPlace[0].AvailableUnits())
	}

	// A second redemption for more than what remains available (3.0) must
	// be rejected even though the raw TotalUnits (5.0) would cover it.
	if _, placeError := router.PlaceRedemptionOrder("acct-1", testSchemeId, 4.0, now); placeError != ErrInsufficientUnits {
		t.Errorf("expected ErrInsufficientUnits for over-redemption against reserved units, got %v", placeError)
	}

	confirmed, _ := router.ConfirmDueOrders(now)
	var confirmedRedemption *Order
	for _, order := range confirmed {
		if order.OrderId == redemptionOrder.OrderId {
			confirmedRedemption = order
		}
	}
	if confirmedRedemption == nil {
		t.Fatalf("expected the redemption order to be confirmed")
	}
	if confirmedRedemption.AmountCreditedInMinorUnits != 200000 { // 2.0 units * 100000 NAV
		t.Errorf("expected amount credited 200000, got %d", confirmedRedemption.AmountCreditedInMinorUnits)
	}

	finalHoldings := router.HoldingsForAccount("acct-1")
	if finalHoldings[0].TotalUnits != 3.0 {
		t.Errorf("expected 3.0 total units remaining after redeeming 2.0 of 5.0, got %v", finalHoldings[0].TotalUnits)
	}
	if finalHoldings[0].UnitsReservedForRedemption != 0 {
		t.Errorf("expected reservation cleared after confirmation, got %v", finalHoldings[0].UnitsReservedForRedemption)
	}
}

func TestOrdersForAccountOnlyReturnsThatAccountsOrders(t *testing.T) {
	router := newTestRouter(0)
	now := time.Now()

	router.PlacePurchaseOrder("acct-1", testSchemeId, 500000, now)
	router.PlacePurchaseOrder("acct-2", testSchemeId, 700000, now)

	acct1Orders := router.OrdersForAccount("acct-1")
	if len(acct1Orders) != 1 || acct1Orders[0].AccountIdentifier != "acct-1" {
		t.Errorf("expected exactly 1 order for acct-1, got %+v", acct1Orders)
	}
}

func TestLookupOrderReturnsFalseForUnknownId(t *testing.T) {
	router := newTestRouter(0)

	_, wasFound := router.LookupOrder("mf-order-does-not-exist")
	if wasFound {
		t.Errorf("expected unknown order id to not be found")
	}
}
