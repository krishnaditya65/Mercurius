package globalmarketsaccess

import (
	"testing"
	"time"
)

func newTestRouter() (*Catalog, *CurrencyConverter, *Router) {
	catalog := NewCatalog()
	converter := NewCurrencyConverter()
	return catalog, converter, NewRouter(catalog, converter)
}

func TestPlaceOrderStartsPending(t *testing.T) {
	_, _, router := newTestRouter()
	order, err := router.PlaceOrder("acct-1", "ADR-NEXATECH", 830000, "INR", time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if order.Status != OrderStatusPending {
		t.Fatalf("expected PENDING, got %s", order.Status)
	}
	if order.AmountInQuoteCurrencyMinorUnits != 0 {
		t.Fatalf("expected no quote-currency amount before routing, got %d", order.AmountInQuoteCurrencyMinorUnits)
	}
}

func TestPlaceOrderRejectsNonPositiveAmount(t *testing.T) {
	_, _, router := newTestRouter()
	if _, err := router.PlaceOrder("acct-1", "ADR-NEXATECH", 0, "INR", time.Now()); err != ErrInvalidOrderAmount {
		t.Fatalf("expected ErrInvalidOrderAmount, got %v", err)
	}
}

func TestPlaceOrderRejectsUnknownSymbol(t *testing.T) {
	_, _, router := newTestRouter()
	if _, err := router.PlaceOrder("acct-1", "NOT-A-SYMBOL", 830000, "INR", time.Now()); err != ErrUnknownSymbolForOrder {
		t.Fatalf("expected ErrUnknownSymbolForOrder, got %v", err)
	}
}

// TestRouteOrderHandWorkedFxConversion: 830000 paise (8300.00 INR) at the
// seeded 83.00 INR/USD rate converts to exactly 10000 cents (100.00 USD).
func TestRouteOrderHandWorkedFxConversion(t *testing.T) {
	_, _, router := newTestRouter()
	order, _ := router.PlaceOrder("acct-1", "ADR-NEXATECH", 830000, "INR", time.Now())

	routed, err := router.RouteOrder(order.OrderId, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if routed.Status != OrderStatusRouted {
		t.Fatalf("expected ROUTED, got %s", routed.Status)
	}
	if routed.AmountInQuoteCurrencyMinorUnits != 10000 {
		t.Fatalf("expected exactly 10000 (100.00 USD), got %d", routed.AmountInQuoteCurrencyMinorUnits)
	}
}

func TestRouteOrderRejectsNonPendingOrder(t *testing.T) {
	_, _, router := newTestRouter()
	order, _ := router.PlaceOrder("acct-1", "ADR-NEXATECH", 830000, "INR", time.Now())
	router.RouteOrder(order.OrderId, time.Now())

	if _, err := router.RouteOrder(order.OrderId, time.Now()); err != ErrOrderNotPending {
		t.Fatalf("expected ErrOrderNotPending, got %v", err)
	}
}

func TestRouteOrderUnknownOrderReturnsError(t *testing.T) {
	_, _, router := newTestRouter()
	if _, err := router.RouteOrder("no-such-order", time.Now()); err != ErrOrderNotFound {
		t.Fatalf("expected ErrOrderNotFound, got %v", err)
	}
}

// TestConfirmOrderAllocatesUnitsAtConfirmationPrice: symbol price is
// 8420 cents (84.20 USD). Amount in quote currency 10000 cents (100.00
// USD) / 8420 = 1.1876... units.
func TestConfirmOrderAllocatesUnitsAtConfirmationPrice(t *testing.T) {
	catalog, _, router := newTestRouter()
	order, _ := router.PlaceOrder("acct-1", "ADR-NEXATECH", 830000, "INR", time.Now())
	router.RouteOrder(order.OrderId, time.Now())

	confirmed, err := router.ConfirmOrder(order.OrderId, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if confirmed.Status != OrderStatusConfirmed {
		t.Fatalf("expected CONFIRMED, got %s", confirmed.Status)
	}

	symbol, _ := catalog.Lookup("ADR-NEXATECH")
	expectedUnits := roundUnits(float64(confirmed.AmountInQuoteCurrencyMinorUnits) / float64(symbol.CurrentPriceInMinorUnits))
	if confirmed.UnitsAllocated != expectedUnits {
		t.Fatalf("expected units %v, got %v", expectedUnits, confirmed.UnitsAllocated)
	}
	if confirmed.PriceAtConfirmationInMinorUnits != symbol.CurrentPriceInMinorUnits {
		t.Fatalf("expected confirmation price to match catalog price %d, got %d", symbol.CurrentPriceInMinorUnits, confirmed.PriceAtConfirmationInMinorUnits)
	}
}

func TestConfirmOrderStrikesCurrentPriceNotPlacementPrice(t *testing.T) {
	catalog, _, router := newTestRouter()
	order, _ := router.PlaceOrder("acct-1", "ADR-NEXATECH", 830000, "INR", time.Now())
	router.RouteOrder(order.OrderId, time.Now())

	// Price moves AFTER routing but BEFORE confirmation.
	catalog.UpdatePrice("ADR-NEXATECH", 10000)

	confirmed, err := router.ConfirmOrder(order.OrderId, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if confirmed.PriceAtConfirmationInMinorUnits != 10000 {
		t.Fatalf("expected the NEW price 10000 struck at confirmation, got %d", confirmed.PriceAtConfirmationInMinorUnits)
	}
}

func TestConfirmOrderRejectsNonRoutedOrder(t *testing.T) {
	_, _, router := newTestRouter()
	order, _ := router.PlaceOrder("acct-1", "ADR-NEXATECH", 830000, "INR", time.Now())

	if _, err := router.ConfirmOrder(order.OrderId, time.Now()); err != ErrOrderNotRouted {
		t.Fatalf("expected ErrOrderNotRouted, got %v", err)
	}
}

func TestConfirmOrderUnknownOrderReturnsError(t *testing.T) {
	_, _, router := newTestRouter()
	if _, err := router.ConfirmOrder("no-such-order", time.Now()); err != ErrOrderNotFound {
		t.Fatalf("expected ErrOrderNotFound, got %v", err)
	}
}

func TestOrdersForAccountReturnsOnlyThatAccountSortedByPlacedAt(t *testing.T) {
	_, _, router := newTestRouter()
	now := time.Now()
	router.PlaceOrder("acct-1", "ADR-NEXATECH", 100000, "INR", now)
	router.PlaceOrder("acct-2", "ADR-NEXATECH", 100000, "INR", now)
	router.PlaceOrder("acct-1", "ADR-AURORAMOTORS", 100000, "INR", now.Add(time.Hour))

	orders := router.OrdersForAccount("acct-1")
	if len(orders) != 2 {
		t.Fatalf("expected 2 orders for acct-1, got %d", len(orders))
	}
	if orders[0].PlacedAt.After(orders[1].PlacedAt) {
		t.Fatal("expected orders sorted by PlacedAt")
	}
}

func TestLookupOrderFindsPlacedOrder(t *testing.T) {
	_, _, router := newTestRouter()
	order, _ := router.PlaceOrder("acct-1", "ADR-NEXATECH", 100000, "INR", time.Now())
	found, wasFound := router.LookupOrder(order.OrderId)
	if !wasFound || found.OrderId != order.OrderId {
		t.Fatal("expected to find the placed order")
	}
}
