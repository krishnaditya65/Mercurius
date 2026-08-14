package globalmarketsaccess

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

// OrderStatus is the real state-machine status of an illustrative global
// order — PENDING -> ROUTED -> CONFIRMED, mirroring the honest-placeholder
// shape of internal/amcrouting's PENDING -> CONFIRMED, but with an extra
// intermediate ROUTED state standing in for "handed off to the partner
// brokerage rail, awaiting their fill confirmation" — a real international
// order genuinely has this extra leg (domestic routing -> partner rail ->
// partner fill) that a same-country AMC purchase doesn't.
type OrderStatus string

const (
	OrderStatusPending   OrderStatus = "PENDING"
	OrderStatusRouted    OrderStatus = "ROUTED"
	OrderStatusConfirmed OrderStatus = "CONFIRMED"
)

// Order is one illustrative global-markets buy order.
// AmountInInvestorCurrencyMinorUnits is what the investor funds it with
// (assumed INR, matching this service's convention elsewhere);
// AmountInQuoteCurrencyMinorUnits is that amount converted to the symbol's
// QuoteCurrency at RouteOrder time (NOT at placement time — mirroring
// internal/amcrouting's "NAV struck at confirmation, not placement"
// pattern, except here the FX conversion is struck at ROUTING time, since
// that's the point a real cross-border order would actually convert
// currency before handing off to the foreign rail).
type Order struct {
	OrderId                            string
	AccountIdentifier                  string
	SymbolId                           string
	AmountInInvestorCurrencyMinorUnits int64
	InvestorCurrency                   string
	AmountInQuoteCurrencyMinorUnits    int64
	QuoteCurrency                      string
	FxRateAppliedAtRouting             float64
	UnitsAllocated                     float64
	PriceAtConfirmationInMinorUnits    int64
	PlacedAt                           time.Time
	RoutedAt                           time.Time
	ConfirmedAt                        time.Time
	Status                             OrderStatus
}

var ErrUnknownSymbolForOrder = fmt.Errorf("no such symbol exists in the global markets catalog")
var ErrInvalidOrderAmount = fmt.Errorf("order amount must be strictly positive")
var ErrOrderNotFound = fmt.Errorf("no order exists with that id")
var ErrOrderNotPending = fmt.Errorf("order is not PENDING and cannot be routed")
var ErrOrderNotRouted = fmt.Errorf("order is not ROUTED and cannot be confirmed")

// Router is safe for concurrent use. See the package doc comment for the
// loud "this is not a real partner brokerage" caveat.
type Router struct {
	catalog   *Catalog
	converter *CurrencyConverter

	mutexGuardingState sync.Mutex
	ordersById         map[string]*Order
}

// NewRouter builds a router against catalog and converter.
func NewRouter(catalog *Catalog, converter *CurrencyConverter) *Router {
	return &Router{
		catalog:    catalog,
		converter:  converter,
		ordersById: make(map[string]*Order),
	}
}

// PlaceOrder places a PENDING order funded in investorCurrency (this
// repo's convention: INR). No currency conversion or unit allocation
// happens yet.
func (router *Router) PlaceOrder(accountIdentifier string, symbolId string, amountInInvestorCurrencyMinorUnits int64, investorCurrency string, now time.Time) (*Order, error) {
	if amountInInvestorCurrencyMinorUnits <= 0 {
		return nil, ErrInvalidOrderAmount
	}
	symbol, wasFound := router.catalog.Lookup(symbolId)
	if !wasFound {
		return nil, ErrUnknownSymbolForOrder
	}

	orderId, genError := generateOrderId()
	if genError != nil {
		return nil, fmt.Errorf("failed to generate order id: %w", genError)
	}

	order := &Order{
		OrderId:                            orderId,
		AccountIdentifier:                  accountIdentifier,
		SymbolId:                           symbolId,
		AmountInInvestorCurrencyMinorUnits: amountInInvestorCurrencyMinorUnits,
		InvestorCurrency:                   investorCurrency,
		QuoteCurrency:                      symbol.QuoteCurrency,
		PlacedAt:                           now,
		Status:                             OrderStatusPending,
	}

	router.mutexGuardingState.Lock()
	router.ordersById[orderId] = order
	router.mutexGuardingState.Unlock()

	return order, nil
}

// RouteOrder transitions a PENDING order to ROUTED: converts
// AmountInInvestorCurrencyMinorUnits to the symbol's QuoteCurrency at the
// CURRENT FX rate (struck now, not at placement) — standing in for "handed
// off to the partner brokerage rail".
func (router *Router) RouteOrder(orderId string, now time.Time) (*Order, error) {
	router.mutexGuardingState.Lock()
	defer router.mutexGuardingState.Unlock()

	order, wasFound := router.ordersById[orderId]
	if !wasFound {
		return nil, ErrOrderNotFound
	}
	if order.Status != OrderStatusPending {
		return nil, ErrOrderNotPending
	}

	rate, rateError := router.converter.Rate(order.InvestorCurrency, order.QuoteCurrency)
	if rateError != nil {
		return nil, rateError
	}

	order.FxRateAppliedAtRouting = rate
	order.AmountInQuoteCurrencyMinorUnits = int64(math.Round(float64(order.AmountInInvestorCurrencyMinorUnits) * rate))
	order.RoutedAt = now
	order.Status = OrderStatusRouted

	return order, nil
}

// ConfirmOrder transitions a ROUTED order to CONFIRMED: allocates units at
// the symbol's CURRENT catalog price, struck at confirmation time — the
// same "strike at confirmation, not earlier" pattern
// internal/amcrouting.ConfirmDueOrders uses.
func (router *Router) ConfirmOrder(orderId string, now time.Time) (*Order, error) {
	router.mutexGuardingState.Lock()
	defer router.mutexGuardingState.Unlock()

	order, wasFound := router.ordersById[orderId]
	if !wasFound {
		return nil, ErrOrderNotFound
	}
	if order.Status != OrderStatusRouted {
		return nil, ErrOrderNotRouted
	}

	symbol, wasFound := router.catalog.Lookup(order.SymbolId)
	if !wasFound {
		return nil, ErrUnknownSymbolForOrder
	}

	units := roundUnits(float64(order.AmountInQuoteCurrencyMinorUnits) / float64(symbol.CurrentPriceInMinorUnits))
	order.UnitsAllocated = units
	order.PriceAtConfirmationInMinorUnits = symbol.CurrentPriceInMinorUnits
	order.ConfirmedAt = now
	order.Status = OrderStatusConfirmed

	return order, nil
}

// OrdersForAccount returns every order accountIdentifier has placed,
// sorted by PlacedAt.
func (router *Router) OrdersForAccount(accountIdentifier string) []*Order {
	router.mutexGuardingState.Lock()
	defer router.mutexGuardingState.Unlock()

	matching := make([]*Order, 0)
	for _, order := range router.ordersById {
		if order.AccountIdentifier == accountIdentifier {
			matching = append(matching, order)
		}
	}
	sort.Slice(matching, func(i, j int) bool { return matching[i].PlacedAt.Before(matching[j].PlacedAt) })
	return matching
}

func (router *Router) LookupOrder(orderId string) (*Order, bool) {
	router.mutexGuardingState.Lock()
	defer router.mutexGuardingState.Unlock()

	order, wasFound := router.ordersById[orderId]
	return order, wasFound
}

func roundUnits(units float64) float64 {
	return math.Round(units*10000) / 10000
}

func generateOrderId() (string, error) {
	randomBytes := make([]byte, 8)
	if _, readError := rand.Read(randomBytes); readError != nil {
		return "", readError
	}
	return "gm-order-" + hex.EncodeToString(randomBytes), nil
}
