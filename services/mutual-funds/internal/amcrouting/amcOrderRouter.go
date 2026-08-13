// Package amcrouting "routes" a mutual fund purchase or redemption order
// to an AMC (Asset Management Company) — FEATURES.md §4, "Direct AMC
// routing (commission-free MF investing)".
//
// LOUD CAVEAT, same honesty pattern as kyc-onboarding's bank-verification
// placeholder and ledger's withdrawalworkflow: THIS NEVER TALKS TO ANY
// REAL AMC OR RTA (Registrar & Transfer Agent). There is no BSE StAR MF /
// NSE NMF II / RTA (CAMS, KFintech) integration anywhere in this repo. A
// real mutual fund purchase does not execute instantly — it takes T+1 (or
// more) business days for the AMC/RTA to actually allocate units at the
// NAV struck at the end of the day the order was accepted, and a
// redemption similarly takes days to settle before the proceeds are
// payable. AmcOrderRouter models exactly that shape as a real order state
// machine — PENDING → CONFIRMED — deliberately NOT faking instant
// execution, but the "AMC" on the other end of it is this package's own
// in-memory map, not a real institution.
package amcrouting

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"mercurius/mutualFunds/internal/fundcatalog"
)

type OrderType string

const (
	OrderTypePurchase   OrderType = "PURCHASE"
	OrderTypeRedemption OrderType = "REDEMPTION"
)

type OrderStatus string

const (
	StatusPendingConfirmation OrderStatus = "PENDING"
	StatusConfirmed           OrderStatus = "CONFIRMED"
)

// Order is one purchase or redemption instruction routed to the
// (simulated) AMC. For a PURCHASE, AmountInvestedInMinorUnits is set at
// placement and UnitsAllocated is computed at confirmation time as
// AmountInvestedInMinorUnits / NavAtConfirmationInMinorUnits — never at
// placement time, because a real AMC strikes the NAV at the end of the
// day the order is accepted, not the moment it's submitted. For a
// REDEMPTION, UnitsRequestedForRedemption is set at placement and
// AmountCreditedInMinorUnits is computed at confirmation.
type Order struct {
	OrderId                       string
	AccountIdentifier             string
	SchemeId                      string
	OrderType                     OrderType
	AmountInvestedInMinorUnits    int64
	UnitsRequestedForRedemption   float64
	PlacedAt                      time.Time
	EligibleForConfirmationAt     time.Time
	ConfirmedAt                   time.Time
	NavAtConfirmationInMinorUnits int64
	UnitsAllocated                float64
	AmountCreditedInMinorUnits    int64
	Status                        OrderStatus
}

// Holding is one account's position in one scheme.
// AvailableUnits = TotalUnits - UnitsReservedForRedemption — a redemption
// already placed but not yet confirmed reserves its units so a second
// redemption can't oversell the same units before the first settles, the
// same "hold" pattern ledger's withdrawalworkflow uses for cash.
type Holding struct {
	SchemeId                   string
	TotalUnits                 float64
	UnitsReservedForRedemption float64
}

func (holding Holding) AvailableUnits() float64 {
	return holding.TotalUnits - holding.UnitsReservedForRedemption
}

var ErrUnknownScheme = fmt.Errorf("no such scheme exists in the catalog")
var ErrInvalidAmount = fmt.Errorf("investment amount must be strictly positive")
var ErrInvalidUnits = fmt.Errorf("unit quantity must be strictly positive")
var ErrInsufficientUnits = fmt.Errorf("requested redemption exceeds the account's available (unreserved) unit balance for this scheme")
var ErrOrderNotFound = fmt.Errorf("no order exists with that id")

// AmcOrderRouter is safe for concurrent use. See the package doc comment
// for the loud "this is not a real AMC" caveat.
type AmcOrderRouter struct {
	catalog           *fundcatalog.FundCatalog
	confirmationDelay time.Duration

	mutexGuardingState sync.Mutex
	ordersById         map[string]*Order
	holdingsByAccount  map[string]map[string]*Holding // accountId -> schemeId -> holding
}

// NewAmcOrderRouter builds a router against catalog, confirming orders
// confirmationDelay after they're placed (simulating the real T+N days a
// live AMC/RTA takes). Pass 0 to confirm instantly for tests/demos.
func NewAmcOrderRouter(catalog *fundcatalog.FundCatalog, confirmationDelay time.Duration) *AmcOrderRouter {
	return &AmcOrderRouter{
		catalog:           catalog,
		confirmationDelay: confirmationDelay,
		ordersById:        make(map[string]*Order),
		holdingsByAccount: make(map[string]map[string]*Holding),
	}
}

// PlacePurchaseOrder places a lumpsum (or one SIP installment's) purchase
// order. It does NOT allocate units — that only happens once
// ConfirmDueOrders processes it, at whatever NAV is current at that
// point, exactly like a real AMC striking end-of-day NAV rather than the
// NAV at the moment the order was submitted.
func (router *AmcOrderRouter) PlacePurchaseOrder(
	accountIdentifier string,
	schemeId string,
	amountInMinorUnits int64,
	now time.Time,
) (*Order, error) {
	if amountInMinorUnits <= 0 {
		return nil, ErrInvalidAmount
	}
	if _, wasFound := router.catalog.Lookup(schemeId); !wasFound {
		return nil, ErrUnknownScheme
	}

	orderId, genError := generateOrderId()
	if genError != nil {
		return nil, fmt.Errorf("failed to generate order id: %w", genError)
	}

	order := &Order{
		OrderId:                    orderId,
		AccountIdentifier:          accountIdentifier,
		SchemeId:                   schemeId,
		OrderType:                  OrderTypePurchase,
		AmountInvestedInMinorUnits: amountInMinorUnits,
		PlacedAt:                   now,
		EligibleForConfirmationAt:  now.Add(router.confirmationDelay),
		Status:                     StatusPendingConfirmation,
	}

	router.mutexGuardingState.Lock()
	router.ordersById[orderId] = order
	router.mutexGuardingState.Unlock()

	return order, nil
}

// PlaceRedemptionOrder places a redemption order and immediately reserves
// unitsToRedeem against the account's holding in this scheme (rejected if
// the account doesn't hold that many AVAILABLE units) — the units aren't
// actually removed from the holding until confirmation, but they're no
// longer redeemable a second time in the meantime.
func (router *AmcOrderRouter) PlaceRedemptionOrder(
	accountIdentifier string,
	schemeId string,
	unitsToRedeem float64,
	now time.Time,
) (*Order, error) {
	if unitsToRedeem <= 0 {
		return nil, ErrInvalidUnits
	}
	if _, wasFound := router.catalog.Lookup(schemeId); !wasFound {
		return nil, ErrUnknownScheme
	}

	router.mutexGuardingState.Lock()
	defer router.mutexGuardingState.Unlock()

	holding := router.lookupOrCreateHoldingLocked(accountIdentifier, schemeId)
	if unitsToRedeem > holding.AvailableUnits()+unitEpsilon {
		return nil, ErrInsufficientUnits
	}

	orderId, genError := generateOrderId()
	if genError != nil {
		return nil, fmt.Errorf("failed to generate order id: %w", genError)
	}

	order := &Order{
		OrderId:                     orderId,
		AccountIdentifier:           accountIdentifier,
		SchemeId:                    schemeId,
		OrderType:                   OrderTypeRedemption,
		UnitsRequestedForRedemption: unitsToRedeem,
		PlacedAt:                    now,
		EligibleForConfirmationAt:   now.Add(router.confirmationDelay),
		Status:                      StatusPendingConfirmation,
	}
	router.ordersById[orderId] = order
	holding.UnitsReservedForRedemption += unitsToRedeem

	return order, nil
}

// ConfirmDueOrders sweeps every PENDING order whose EligibleForConfirmationAt
// has passed and confirms it at the scheme's CURRENT catalog NAV: a
// purchase allocates units = amountInvested / navAtConfirmation to the
// holding; a redemption removes the reserved units from the holding and
// computes amountCredited = units * navAtConfirmation. An order that
// fails to confirm (e.g. the scheme was somehow removed from the
// catalog) is skipped, left PENDING, and reported in failedOrderIds
// rather than silently dropped — same pattern as ledger's
// ProcessDueWithdrawals.
func (router *AmcOrderRouter) ConfirmDueOrders(now time.Time) (confirmed []*Order, failedOrderIds []string) {
	router.mutexGuardingState.Lock()
	dueOrders := make([]*Order, 0)
	for _, order := range router.ordersById {
		if order.Status == StatusPendingConfirmation && !now.Before(order.EligibleForConfirmationAt) {
			dueOrders = append(dueOrders, order)
		}
	}
	router.mutexGuardingState.Unlock()

	sort.Slice(dueOrders, func(i, j int) bool { return dueOrders[i].OrderId < dueOrders[j].OrderId })

	for _, order := range dueOrders {
		scheme, wasFound := router.catalog.Lookup(order.SchemeId)
		if !wasFound {
			failedOrderIds = append(failedOrderIds, order.OrderId)
			continue
		}

		router.mutexGuardingState.Lock()
		holding := router.lookupOrCreateHoldingLocked(order.AccountIdentifier, order.SchemeId)

		order.NavAtConfirmationInMinorUnits = scheme.CurrentNavInMinorUnits
		order.ConfirmedAt = now

		switch order.OrderType {
		case OrderTypePurchase:
			units := roundUnits(float64(order.AmountInvestedInMinorUnits) / float64(scheme.CurrentNavInMinorUnits))
			order.UnitsAllocated = units
			holding.TotalUnits = roundUnits(holding.TotalUnits + units)
		case OrderTypeRedemption:
			amountCredited := int64(math.Round(order.UnitsRequestedForRedemption * float64(scheme.CurrentNavInMinorUnits)))
			order.AmountCreditedInMinorUnits = amountCredited
			holding.TotalUnits = roundUnits(holding.TotalUnits - order.UnitsRequestedForRedemption)
			holding.UnitsReservedForRedemption = roundUnits(holding.UnitsReservedForRedemption - order.UnitsRequestedForRedemption)
		}
		order.Status = StatusConfirmed
		router.mutexGuardingState.Unlock()

		confirmed = append(confirmed, order)
	}

	return confirmed, failedOrderIds
}

// HoldingsForAccount returns every scheme holding for accountIdentifier
// with a nonzero total or reserved unit count, sorted by SchemeId.
func (router *AmcOrderRouter) HoldingsForAccount(accountIdentifier string) []Holding {
	router.mutexGuardingState.Lock()
	defer router.mutexGuardingState.Unlock()

	holdingsBySchemeId, wasFound := router.holdingsByAccount[accountIdentifier]
	if !wasFound {
		return []Holding{}
	}

	holdings := make([]Holding, 0, len(holdingsBySchemeId))
	for _, holding := range holdingsBySchemeId {
		if holding.TotalUnits != 0 || holding.UnitsReservedForRedemption != 0 {
			holdings = append(holdings, *holding)
		}
	}
	sort.Slice(holdings, func(i, j int) bool { return holdings[i].SchemeId < holdings[j].SchemeId })
	return holdings
}

// OrdersForAccount returns every order (any status) placed by
// accountIdentifier, sorted by PlacedAt for a deterministic response.
func (router *AmcOrderRouter) OrdersForAccount(accountIdentifier string) []*Order {
	router.mutexGuardingState.Lock()
	defer router.mutexGuardingState.Unlock()

	matchingOrders := make([]*Order, 0)
	for _, order := range router.ordersById {
		if order.AccountIdentifier == accountIdentifier {
			matchingOrders = append(matchingOrders, order)
		}
	}
	sort.Slice(matchingOrders, func(i, j int) bool { return matchingOrders[i].PlacedAt.Before(matchingOrders[j].PlacedAt) })
	return matchingOrders
}

func (router *AmcOrderRouter) LookupOrder(orderId string) (*Order, bool) {
	router.mutexGuardingState.Lock()
	defer router.mutexGuardingState.Unlock()

	order, wasFound := router.ordersById[orderId]
	return order, wasFound
}

// lookupOrCreateHoldingLocked must be called with mutexGuardingState held.
func (router *AmcOrderRouter) lookupOrCreateHoldingLocked(accountIdentifier string, schemeId string) *Holding {
	holdingsBySchemeId, wasFound := router.holdingsByAccount[accountIdentifier]
	if !wasFound {
		holdingsBySchemeId = make(map[string]*Holding)
		router.holdingsByAccount[accountIdentifier] = holdingsBySchemeId
	}

	holding, wasFound := holdingsBySchemeId[schemeId]
	if !wasFound {
		holding = &Holding{SchemeId: schemeId}
		holdingsBySchemeId[schemeId] = holding
	}
	return holding
}

// unitEpsilon absorbs floating-point rounding noise (e.g. a redemption of
// exactly a holding's full unit balance shouldn't be rejected because of
// a 1e-13 rounding sliver) without meaningfully allowing over-redemption.
const unitEpsilon = 1e-9

// roundUnits rounds to 4 decimal places, matching the real-world
// convention that mutual fund unit allocations are struck to 3-4 decimal
// precision, not raw float64 precision.
func roundUnits(units float64) float64 {
	return math.Round(units*10000) / 10000
}

func generateOrderId() (string, error) {
	randomBytes := make([]byte, 8)
	if _, readError := rand.Read(randomBytes); readError != nil {
		return "", readError
	}
	return "mf-order-" + hex.EncodeToString(randomBytes), nil
}
