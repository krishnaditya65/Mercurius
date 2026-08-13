package basketrebalancing

import (
	"testing"
	"time"

	"mercurius/mutualFunds/internal/amcrouting"
	"mercurius/mutualFunds/internal/fundcatalog"
)

const schemeA = "MF-EQ-BLUECHIP001"
const schemeB = "MF-EQ-MIDCAP002"
const schemeC = "MF-DT-LIQUID003"

var testNow = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func newTestRebalancer(t *testing.T) (*BasketRebalancer, *fundcatalog.FundCatalog, *amcrouting.AmcOrderRouter) {
	t.Helper()
	catalog := fundcatalog.NewFundCatalog()
	router := amcrouting.NewAmcOrderRouter(catalog, 0)
	return NewBasketRebalancer(catalog, router), catalog, router
}

func TestCreateBasketRejectsEmptyName(t *testing.T) {
	rebalancer, _, _ := newTestRebalancer(t)
	_, err := rebalancer.CreateBasket("", map[string]float64{schemeA: 50, schemeB: 50})
	if err != ErrEmptyBasketName {
		t.Errorf("expected ErrEmptyBasketName, got %v", err)
	}
}

func TestCreateBasketRejectsTooFewConstituents(t *testing.T) {
	rebalancer, _, _ := newTestRebalancer(t)
	_, err := rebalancer.CreateBasket("One Scheme", map[string]float64{schemeA: 100})
	if err != ErrNoConstituents {
		t.Errorf("expected ErrNoConstituents, got %v", err)
	}
}

func TestCreateBasketRejectsUnknownScheme(t *testing.T) {
	rebalancer, _, _ := newTestRebalancer(t)
	_, err := rebalancer.CreateBasket("Bad", map[string]float64{schemeA: 50, "MF-DOES-NOT-EXIST": 50})
	if err != ErrUnknownScheme {
		t.Errorf("expected ErrUnknownScheme, got %v", err)
	}
}

func TestCreateBasketRejectsNonPositiveWeight(t *testing.T) {
	rebalancer, _, _ := newTestRebalancer(t)
	_, err := rebalancer.CreateBasket("Bad", map[string]float64{schemeA: 0, schemeB: 100})
	if err != ErrNonPositiveWeight {
		t.Errorf("expected ErrNonPositiveWeight, got %v", err)
	}
}

func TestCreateBasketRejectsWeightsNotSummingTo100(t *testing.T) {
	rebalancer, _, _ := newTestRebalancer(t)
	_, err := rebalancer.CreateBasket("Bad", map[string]float64{schemeA: 40, schemeB: 40})
	if err != ErrWeightsMustSumTo100 {
		t.Errorf("expected ErrWeightsMustSumTo100, got %v", err)
	}
}

func TestCreateBasketSucceedsAndSortsConstituents(t *testing.T) {
	rebalancer, _, _ := newTestRebalancer(t)
	basket, err := rebalancer.CreateBasket("Growth Basket", map[string]float64{schemeC: 20, schemeA: 50, schemeB: 30})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(basket.Constituents) != 3 {
		t.Fatalf("expected 3 constituents, got %d", len(basket.Constituents))
	}
	for i := 1; i < len(basket.Constituents); i++ {
		if basket.Constituents[i-1].SchemeId >= basket.Constituents[i].SchemeId {
			t.Errorf("constituents not sorted by SchemeId: %v", basket.Constituents)
		}
	}
}

func TestSubscribeToBasketRejectsUnknownBasket(t *testing.T) {
	rebalancer, _, _ := newTestRebalancer(t)
	_, err := rebalancer.SubscribeToBasket("acct-1", "no-such-basket", 10000, testNow)
	if err != ErrBasketNotFound {
		t.Errorf("expected ErrBasketNotFound, got %v", err)
	}
}

func TestSubscribeToBasketRejectsNonPositiveAmount(t *testing.T) {
	rebalancer, _, _ := newTestRebalancer(t)
	basket, _ := rebalancer.CreateBasket("B", map[string]float64{schemeA: 50, schemeB: 50})
	_, err := rebalancer.SubscribeToBasket("acct-1", basket.BasketId, 0, testNow)
	if err != ErrInvalidLumpsumAmount {
		t.Errorf("expected ErrInvalidLumpsumAmount, got %v", err)
	}
}

func TestSubscribeToBasketSplitsProportionallyAndSumsExactly(t *testing.T) {
	rebalancer, _, router := newTestRebalancer(t)
	basket, _ := rebalancer.CreateBasket("Balanced", map[string]float64{schemeA: 50, schemeB: 30, schemeC: 20})

	orders, err := rebalancer.SubscribeToBasket("acct-1", basket.BasketId, 10000, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(orders) != 3 {
		t.Fatalf("expected 3 orders, got %d", len(orders))
	}

	amountsBySchemeId := make(map[string]int64, 3)
	sum := int64(0)
	for _, order := range orders {
		amountsBySchemeId[order.SchemeId] = order.AmountInMinorUnits
		sum += order.AmountInMinorUnits
		if order.OrderId == "" {
			t.Errorf("expected non-empty OrderId for %s", order.SchemeId)
		}
	}
	if sum != 10000 {
		t.Errorf("expected split amounts to sum to 10000, got %d", sum)
	}
	if amountsBySchemeId[schemeA] != 5000 {
		t.Errorf("expected schemeA split 5000, got %d", amountsBySchemeId[schemeA])
	}
	if amountsBySchemeId[schemeB] != 3000 {
		t.Errorf("expected schemeB split 3000, got %d", amountsBySchemeId[schemeB])
	}
	if amountsBySchemeId[schemeC] != 2000 {
		t.Errorf("expected schemeC split 2000, got %d", amountsBySchemeId[schemeC])
	}

	// The orders are real PENDING amcrouting orders, confirmable like any
	// other lumpsum purchase.
	confirmed, failed := router.ConfirmDueOrders(testNow)
	if len(failed) != 0 {
		t.Errorf("expected no failed confirmations, got %v", failed)
	}
	if len(confirmed) != 3 {
		t.Errorf("expected 3 confirmed orders, got %d", len(confirmed))
	}
}

func TestSubscribeToBasketRemainderGoesToLastScheme(t *testing.T) {
	rebalancer, _, _ := newTestRebalancer(t)
	// Three equal-ish weights that don't divide evenly: 33.34/33.33/33.33
	// summing to exactly 100. schemeA sorts first, schemeB second, schemeC
	// last (schemeA < schemeB < schemeC alphabetically as
	// MF-EQ-BLUECHIP001 < MF-EQ-MIDCAP002 < MF-DT-LIQUID003? verify by
	// sorting: MF-DT-LIQUID003 < MF-EQ-BLUECHIP001 < MF-EQ-MIDCAP002).
	basket, err := rebalancer.CreateBasket("Thirds", map[string]float64{schemeA: 33.34, schemeB: 33.33, schemeC: 33.33})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	orders, err := rebalancer.SubscribeToBasket("acct-1", basket.BasketId, 100, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sum := int64(0)
	for _, order := range orders {
		sum += order.AmountInMinorUnits
	}
	if sum != 100 {
		t.Errorf("expected split to sum exactly to 100, got %d", sum)
	}

	lastConstituentSchemeId := basket.Constituents[len(basket.Constituents)-1].SchemeId
	for _, order := range orders {
		if order.SchemeId == lastConstituentSchemeId {
			// schemeC (MF-DT-LIQUID003) is alphabetically first, so the
			// last constituent in sorted order is schemeB
			// (MF-EQ-MIDCAP002): round(100*33.33/100)=33 for schemeC,
			// round(100*33.34/100)=33 for schemeA, remainder for schemeB
			// = 100-33-33=34.
			if order.AmountInMinorUnits != 34 {
				t.Errorf("expected last constituent (remainder) split of 34, got %d for %s", order.AmountInMinorUnits, order.SchemeId)
			}
		}
	}
}

func TestRebalanceAccountBasketRejectsUnknownBasket(t *testing.T) {
	rebalancer, _, _ := newTestRebalancer(t)
	_, err := rebalancer.RebalanceAccountBasket("acct-1", "no-such-basket", testNow)
	if err != ErrBasketNotFound {
		t.Errorf("expected ErrBasketNotFound, got %v", err)
	}
}

func TestRebalanceAccountBasketWithNoHoldingsIsAllHold(t *testing.T) {
	rebalancer, _, _ := newTestRebalancer(t)
	basket, _ := rebalancer.CreateBasket("Empty Holdings", map[string]float64{schemeA: 50, schemeB: 50})

	actions, err := rebalancer.RebalanceAccountBasket("acct-never-invested", basket.BasketId, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, action := range actions {
		if action.Action != ActionHold {
			t.Errorf("expected ActionHold with zero holdings (0%% of 0 is still 0), got %v for %s", action.Action, action.SchemeId)
		}
	}
}

// TestRebalanceAccountBasketHandWorkedDriftProducesExactBuySellAmounts is
// the hand-worked example required by FEATURES.md §4:
//
// Basket: schemeA 50%, schemeB 30%, schemeC 20%.
// Set all three NAVs to 100 (minor units) and subscribe acct-1 with a
// 10000 lumpsum: splits are A=5000, B=3000, C=2000 (exactly, see the
// split test above). Confirming at NAV=100 allocates:
//
//	A: 5000 / 100 = 50.0 units
//	B: 3000 / 100 = 30.0 units
//	C: 2000 / 100 = 20.0 units
//
// Now schemeA's NAV DOUBLES to 200 (a market move) while B and C stay at
// 100. Current holding values:
//
//	A: 50.0 units * 200 = 10000
//	B: 30.0 units * 100 = 3000
//	C: 20.0 units * 100 = 2000
//	total = 15000
//
// Target values at 50/30/20 of that SAME total:
//
//	A target: 15000 * 0.50 = 7500  -> diff = 7500  - 10000 = -2500 => SELL 2500
//	B target: 15000 * 0.30 = 4500  -> diff = 4500  - 3000  = +1500 => BUY 1500
//	C target: 15000 * 0.20 = 3000  -> diff = 3000  - 2000  = +1000 => BUY 1000
//
// Note the SELL exactly offsets the two BUYs (2500 == 1500+1000) — a
// rebalance neither creates nor destroys money, it just reallocates it.
// schemeA's SELL of 2500 at its NAV of 200 is exactly 12.5 units.
func TestRebalanceAccountBasketHandWorkedDriftProducesExactBuySellAmounts(t *testing.T) {
	rebalancer, catalog, router := newTestRebalancer(t)

	if err := catalog.UpdateNav(schemeA, 100); err != nil {
		t.Fatalf("failed to seed schemeA NAV: %v", err)
	}
	if err := catalog.UpdateNav(schemeB, 100); err != nil {
		t.Fatalf("failed to seed schemeB NAV: %v", err)
	}
	if err := catalog.UpdateNav(schemeC, 100); err != nil {
		t.Fatalf("failed to seed schemeC NAV: %v", err)
	}

	basket, err := rebalancer.CreateBasket("Drift Test", map[string]float64{schemeA: 50, schemeB: 30, schemeC: 20})
	if err != nil {
		t.Fatalf("unexpected error creating basket: %v", err)
	}

	if _, err := rebalancer.SubscribeToBasket("acct-1", basket.BasketId, 10000, testNow); err != nil {
		t.Fatalf("unexpected error subscribing: %v", err)
	}
	confirmed, failed := router.ConfirmDueOrders(testNow)
	if len(failed) != 0 || len(confirmed) != 3 {
		t.Fatalf("expected all 3 subscription orders to confirm cleanly, confirmed=%d failed=%v", len(confirmed), failed)
	}

	holdings := router.HoldingsForAccount("acct-1")
	unitsBySchemeId := make(map[string]float64, len(holdings))
	for _, holding := range holdings {
		unitsBySchemeId[holding.SchemeId] = holding.TotalUnits
	}
	if unitsBySchemeId[schemeA] != 50.0 {
		t.Fatalf("expected schemeA units 50.0, got %v", unitsBySchemeId[schemeA])
	}
	if unitsBySchemeId[schemeB] != 30.0 {
		t.Fatalf("expected schemeB units 30.0, got %v", unitsBySchemeId[schemeB])
	}
	if unitsBySchemeId[schemeC] != 20.0 {
		t.Fatalf("expected schemeC units 20.0, got %v", unitsBySchemeId[schemeC])
	}

	// The market move: schemeA's NAV doubles.
	if err := catalog.UpdateNav(schemeA, 200); err != nil {
		t.Fatalf("failed to move schemeA NAV: %v", err)
	}

	actions, err := rebalancer.RebalanceAccountBasket("acct-1", basket.BasketId, testNow)
	if err != nil {
		t.Fatalf("unexpected error rebalancing: %v", err)
	}

	actionsBySchemeId := make(map[string]RebalanceAction, len(actions))
	for _, action := range actions {
		actionsBySchemeId[action.SchemeId] = action
	}

	sellA := actionsBySchemeId[schemeA]
	if sellA.Action != ActionSell {
		t.Errorf("expected schemeA action SELL, got %v", sellA.Action)
	}
	if sellA.CurrentValueInMinorUnits != 10000 {
		t.Errorf("expected schemeA current value 10000, got %d", sellA.CurrentValueInMinorUnits)
	}
	if sellA.TargetValueInMinorUnits != 7500 {
		t.Errorf("expected schemeA target value 7500, got %d", sellA.TargetValueInMinorUnits)
	}
	if sellA.AmountInMinorUnits != 2500 {
		t.Errorf("expected schemeA sell amount 2500, got %d", sellA.AmountInMinorUnits)
	}
	if sellA.UnitsToSell != 12.5 {
		t.Errorf("expected schemeA units to sell 12.5, got %v", sellA.UnitsToSell)
	}
	if sellA.OrderId == "" || sellA.ErrorMessage != "" {
		t.Errorf("expected schemeA SELL order to place cleanly, got orderId=%q errorMessage=%q", sellA.OrderId, sellA.ErrorMessage)
	}

	buyB := actionsBySchemeId[schemeB]
	if buyB.Action != ActionBuy {
		t.Errorf("expected schemeB action BUY, got %v", buyB.Action)
	}
	if buyB.AmountInMinorUnits != 1500 {
		t.Errorf("expected schemeB buy amount 1500, got %d", buyB.AmountInMinorUnits)
	}
	if buyB.OrderId == "" {
		t.Errorf("expected schemeB BUY order to place cleanly")
	}

	buyC := actionsBySchemeId[schemeC]
	if buyC.Action != ActionBuy {
		t.Errorf("expected schemeC action BUY, got %v", buyC.Action)
	}
	if buyC.AmountInMinorUnits != 1000 {
		t.Errorf("expected schemeC buy amount 1000, got %d", buyC.AmountInMinorUnits)
	}

	// The sell exactly funds the two buys.
	if sellA.AmountInMinorUnits != buyB.AmountInMinorUnits+buyC.AmountInMinorUnits {
		t.Errorf("expected sell amount to exactly fund both buys: sell=%d buys=%d", sellA.AmountInMinorUnits, buyB.AmountInMinorUnits+buyC.AmountInMinorUnits)
	}
}

func TestRebalanceAccountBasketSkipsWithinThreshold(t *testing.T) {
	rebalancer, catalog, router := newTestRebalancer(t)
	if err := catalog.UpdateNav(schemeA, 100); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := catalog.UpdateNav(schemeB, 100); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	basket, _ := rebalancer.CreateBasket("Already Balanced", map[string]float64{schemeA: 50, schemeB: 50})
	if _, err := rebalancer.SubscribeToBasket("acct-2", basket.BasketId, 10000, testNow); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	router.ConfirmDueOrders(testNow)

	actions, err := rebalancer.RebalanceAccountBasket("acct-2", basket.BasketId, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, action := range actions {
		if action.Action != ActionHold {
			t.Errorf("expected ActionHold with no drift, got %v for %s (current=%d target=%d)", action.Action, action.SchemeId, action.CurrentValueInMinorUnits, action.TargetValueInMinorUnits)
		}
		if action.OrderId != "" {
			t.Errorf("expected no order placed for a HOLD action, got orderId=%q", action.OrderId)
		}
	}
}

func TestRebalanceAccountBasketRecordsErrorWhenSellExceedsAvailableUnits(t *testing.T) {
	rebalancer, catalog, router := newTestRebalancer(t)
	if err := catalog.UpdateNav(schemeA, 100); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := catalog.UpdateNav(schemeB, 100); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	basket, _ := rebalancer.CreateBasket("Reserved Units", map[string]float64{schemeA: 50, schemeB: 50})
	if _, err := rebalancer.SubscribeToBasket("acct-3", basket.BasketId, 10000, testNow); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	router.ConfirmDueOrders(testNow)

	// Reserve essentially all of schemeA's units against a pending
	// redemption placed OUTSIDE the rebalancer, so the rebalance's own
	// attempted sell of schemeA can't find enough AVAILABLE units.
	if _, err := router.PlaceRedemptionOrder("acct-3", schemeA, 49.9, testNow); err != nil {
		t.Fatalf("unexpected error reserving units: %v", err)
	}

	// Drift schemeA UP in value relative to schemeB (NAV triples) so
	// schemeA becomes overweight and the rebalancer wants to SELL it.
	if err := catalog.UpdateNav(schemeA, 300); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	actions, err := rebalancer.RebalanceAccountBasket("acct-3", basket.BasketId, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	actionsBySchemeId := make(map[string]RebalanceAction, len(actions))
	for _, action := range actions {
		actionsBySchemeId[action.SchemeId] = action
	}
	sellA := actionsBySchemeId[schemeA]
	if sellA.Action != ActionSell {
		t.Fatalf("expected schemeA action SELL, got %v", sellA.Action)
	}
	if sellA.ErrorMessage == "" {
		t.Errorf("expected an ErrorMessage since available units are mostly reserved")
	}
	if sellA.OrderId != "" {
		t.Errorf("expected no OrderId when the sell order failed to place, got %q", sellA.OrderId)
	}
}

func TestListBasketsSortedById(t *testing.T) {
	rebalancer, _, _ := newTestRebalancer(t)
	basketOne, _ := rebalancer.CreateBasket("One", map[string]float64{schemeA: 50, schemeB: 50})
	basketTwo, _ := rebalancer.CreateBasket("Two", map[string]float64{schemeA: 50, schemeC: 50})

	baskets := rebalancer.ListBaskets()
	if len(baskets) != 2 {
		t.Fatalf("expected 2 baskets, got %d", len(baskets))
	}
	foundIds := map[string]bool{baskets[0].BasketId: true, baskets[1].BasketId: true}
	if !foundIds[basketOne.BasketId] || !foundIds[basketTwo.BasketId] {
		t.Errorf("expected both created baskets to be listed")
	}
	if baskets[0].BasketId >= baskets[1].BasketId {
		t.Errorf("expected baskets sorted by BasketId, got %v", baskets)
	}
}

func TestLookupBasketUnknownReturnsFalse(t *testing.T) {
	rebalancer, _, _ := newTestRebalancer(t)
	_, wasFound := rebalancer.LookupBasket("no-such-basket")
	if wasFound {
		t.Errorf("expected LookupBasket to return false for an unknown basket")
	}
}

func TestSplitProportionallySumsExactlyForThreeWayWeights(t *testing.T) {
	constituents := []BasketConstituent{
		{SchemeId: schemeA, TargetWeightPercent: 33.34},
		{SchemeId: schemeB, TargetWeightPercent: 33.33},
		{SchemeId: schemeC, TargetWeightPercent: 33.33},
	}
	splits := splitProportionally(999, constituents)
	sum := int64(0)
	for _, amount := range splits {
		sum += amount
	}
	if sum != 999 {
		t.Errorf("expected splits to sum exactly to 999, got %d", sum)
	}
}
