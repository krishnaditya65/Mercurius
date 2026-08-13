// Package basketrebalancing implements index/thematic rebalancing baskets
// with one-click rebalance — FEATURES.md §4, "Index/thematic rebalancing
// baskets with one-click rebalance".
//
// A Basket is a named TARGET allocation across two or more schemes from
// internal/fundcatalog, expressed as percentage weights that must sum to
// exactly 100. An account SUBSCRIBES to a basket with a lumpsum amount,
// which splits that amount proportionally across the basket's constituent
// schemes and routes one purchase order per scheme through
// internal/amcrouting — exactly like a lumpsum purchase, just fanned out
// N ways. Subscribing does not, by itself, keep the account "at target"
// forever: a market move changes each constituent's value at a different
// rate, so the account's actual holding-value mix DRIFTS away from the
// basket's target weights over time. RebalanceAccountBasket is the
// "one-click rebalance": it revalues the account's CURRENT holdings in
// each basket scheme at the CURRENT catalog NAV, compares that to what
// the TARGET weights say those holdings should be worth, and emits the
// exact BUY/SELL orders needed to close the gap — see the package's test
// file for a hand-worked drift-and-rebalance example.
//
// Same loud caveat as internal/amcrouting: every BUY/SELL this package
// emits is just a PlacePurchaseOrder/PlaceRedemptionOrder call against
// amcrouting's own simulated in-memory "AMC" — there is no real AMC/RTA
// on the other end.
package basketrebalancing

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"mercurius/mutualFunds/internal/amcrouting"
	"mercurius/mutualFunds/internal/fundcatalog"
)

// BasketConstituent is one scheme's target weight within a basket.
type BasketConstituent struct {
	SchemeId            string
	TargetWeightPercent float64
}

// Basket is a named target allocation across two or more schemes. Weights
// are validated at creation time to sum to exactly 100 (within a tiny
// floating-point epsilon) — see ErrWeightsMustSumTo100.
type Basket struct {
	BasketId     string
	Name         string
	Constituents []BasketConstituent // sorted by SchemeId for a deterministic response
	CreatedAt    time.Time
}

// ActionType is what one leg of a rebalance does to close the gap between
// an account's current and target holding value in a scheme.
type ActionType string

const (
	ActionBuy  ActionType = "BUY"
	ActionSell ActionType = "SELL"
	ActionHold ActionType = "HOLD" // drift within rebalanceThresholdInMinorUnits — no order placed
)

// RebalanceAction is one scheme's computed adjustment. For ActionBuy,
// AmountInMinorUnits is the purchase amount placed. For ActionSell,
// AmountInMinorUnits is the target sale proceeds and UnitsToSell is the
// exact unit quantity redeemed to realize (approximately) that amount at
// the scheme's current NAV. OrderId is empty (and ErrorMessage set) if
// placing the order failed (e.g. insufficient available units to sell).
type RebalanceAction struct {
	SchemeId                 string
	Action                   ActionType
	CurrentValueInMinorUnits int64
	TargetValueInMinorUnits  int64
	AmountInMinorUnits       int64 // abs(TargetValue - CurrentValue); 0 for ActionHold
	UnitsToSell              float64
	OrderId                  string
	ErrorMessage             string
}

// SubscriptionOrder is one constituent leg of a basket subscription.
type SubscriptionOrder struct {
	SchemeId           string
	AmountInMinorUnits int64
	OrderId            string
}

var ErrEmptyBasketName = fmt.Errorf("basket name is required")
var ErrNoConstituents = fmt.Errorf("a basket must have at least two constituent schemes")
var ErrUnknownScheme = fmt.Errorf("no such scheme exists in the catalog")
var ErrDuplicateScheme = fmt.Errorf("a scheme cannot appear more than once in a basket")
var ErrNonPositiveWeight = fmt.Errorf("every constituent weight must be strictly positive")
var ErrWeightsMustSumTo100 = fmt.Errorf("constituent target weights must sum to exactly 100 percent")
var ErrBasketNotFound = fmt.Errorf("no basket exists with that id")
var ErrInvalidLumpsumAmount = fmt.Errorf("lumpsum amount must be strictly positive")

// weightSumEpsilon absorbs floating-point summation noise (e.g.
// 33.34+33.33+33.33 landing at 100.00000000000001) without meaningfully
// allowing an actual misconfigured basket through.
const weightSumEpsilon = 1e-6

// rebalanceThresholdInMinorUnits is the minimum drift (in minor currency
// units) worth acting on — a sub-unit drift would round to a zero-amount
// order anyway, and real-world rebalancing tools always apply some
// no-action band to avoid generating economically meaningless trades.
const rebalanceThresholdInMinorUnits = 1

// BasketRebalancer is safe for concurrent use.
type BasketRebalancer struct {
	catalog   *fundcatalog.FundCatalog
	amcRouter *amcrouting.AmcOrderRouter

	mutexGuardingBaskets sync.Mutex
	basketsById          map[string]*Basket
}

func NewBasketRebalancer(catalog *fundcatalog.FundCatalog, amcRouter *amcrouting.AmcOrderRouter) *BasketRebalancer {
	return &BasketRebalancer{
		catalog:     catalog,
		amcRouter:   amcRouter,
		basketsById: make(map[string]*Basket),
	}
}

// CreateBasket validates and stores a new basket. targetWeightPercentBySchemeId
// maps schemeId -> target weight as a percentage (e.g. 40.0 for 40%); every
// scheme must exist in the catalog, every weight must be strictly
// positive, no scheme may repeat, there must be at least two constituents,
// and the weights must sum to exactly 100 (within weightSumEpsilon).
func (rebalancer *BasketRebalancer) CreateBasket(name string, targetWeightPercentBySchemeId map[string]float64) (*Basket, error) {
	if name == "" {
		return nil, ErrEmptyBasketName
	}
	if len(targetWeightPercentBySchemeId) < 2 {
		return nil, ErrNoConstituents
	}

	constituents := make([]BasketConstituent, 0, len(targetWeightPercentBySchemeId))
	weightSum := 0.0
	for schemeId, weightPercent := range targetWeightPercentBySchemeId {
		if weightPercent <= 0 {
			return nil, ErrNonPositiveWeight
		}
		if _, wasFound := rebalancer.catalog.Lookup(schemeId); !wasFound {
			return nil, ErrUnknownScheme
		}
		constituents = append(constituents, BasketConstituent{SchemeId: schemeId, TargetWeightPercent: weightPercent})
		weightSum += weightPercent
	}
	// Duplicate scheme ids can't occur via a Go map key, but a wire-format
	// decoder could still hand us a slice-based request with duplicates —
	// callers building a Basket from a list rather than a map should
	// dedupe before calling in. Guarded here defensively for future
	// callers of this exported function.
	seenSchemeIds := make(map[string]bool, len(constituents))
	for _, constituent := range constituents {
		if seenSchemeIds[constituent.SchemeId] {
			return nil, ErrDuplicateScheme
		}
		seenSchemeIds[constituent.SchemeId] = true
	}

	if math.Abs(weightSum-100.0) > weightSumEpsilon {
		return nil, ErrWeightsMustSumTo100
	}

	sort.Slice(constituents, func(i, j int) bool { return constituents[i].SchemeId < constituents[j].SchemeId })

	basketId, genError := generateBasketId()
	if genError != nil {
		return nil, fmt.Errorf("failed to generate basket id: %w", genError)
	}

	basket := &Basket{
		BasketId:     basketId,
		Name:         name,
		Constituents: constituents,
		CreatedAt:    time.Now(),
	}

	rebalancer.mutexGuardingBaskets.Lock()
	rebalancer.basketsById[basketId] = basket
	rebalancer.mutexGuardingBaskets.Unlock()

	return basket, nil
}

// LookupBasket returns the basket, or false if basketId doesn't exist.
func (rebalancer *BasketRebalancer) LookupBasket(basketId string) (*Basket, bool) {
	rebalancer.mutexGuardingBaskets.Lock()
	defer rebalancer.mutexGuardingBaskets.Unlock()

	basket, wasFound := rebalancer.basketsById[basketId]
	return basket, wasFound
}

// ListBaskets returns every basket, sorted by BasketId.
func (rebalancer *BasketRebalancer) ListBaskets() []*Basket {
	rebalancer.mutexGuardingBaskets.Lock()
	defer rebalancer.mutexGuardingBaskets.Unlock()

	baskets := make([]*Basket, 0, len(rebalancer.basketsById))
	for _, basket := range rebalancer.basketsById {
		baskets = append(baskets, basket)
	}
	sort.Slice(baskets, func(i, j int) bool { return baskets[i].BasketId < baskets[j].BasketId })
	return baskets
}

// SubscribeToBasket splits lumpsumAmountInMinorUnits proportionally across
// basketId's constituents (by TargetWeightPercent) and routes one purchase
// order per constituent through amcrouting, exactly like a lumpsum
// purchase fanned out N ways. Splits are computed so they sum EXACTLY to
// lumpsumAmountInMinorUnits: every constituent but the last is
// round(amount * weight/100); the last constituent (in sorted SchemeId
// order) absorbs whatever rounding remainder is left, rather than letting
// per-leg rounding silently lose or manufacture money.
func (rebalancer *BasketRebalancer) SubscribeToBasket(
	accountIdentifier string,
	basketId string,
	lumpsumAmountInMinorUnits int64,
	now time.Time,
) ([]SubscriptionOrder, error) {
	if lumpsumAmountInMinorUnits <= 0 {
		return nil, ErrInvalidLumpsumAmount
	}

	basket, wasFound := rebalancer.LookupBasket(basketId)
	if !wasFound {
		return nil, ErrBasketNotFound
	}

	splits := splitProportionally(lumpsumAmountInMinorUnits, basket.Constituents)

	subscriptionOrders := make([]SubscriptionOrder, 0, len(basket.Constituents))
	for _, constituent := range basket.Constituents {
		amount := splits[constituent.SchemeId]
		if amount <= 0 {
			// A basket with many constituents and a very small lumpsum can
			// round a leg down to zero; amcrouting rejects a zero-amount
			// purchase, so skip it rather than erroring the whole
			// subscription — a real build would enforce a minimum lumpsum
			// large enough that this never happens (see fundcatalog's/
			// amcrouting's "no minimum lumpsum" placeholder note).
			continue
		}

		order, placeError := rebalancer.amcRouter.PlacePurchaseOrder(accountIdentifier, constituent.SchemeId, amount, now)
		if placeError != nil {
			return nil, fmt.Errorf("failed to place purchase order for %s: %w", constituent.SchemeId, placeError)
		}
		subscriptionOrders = append(subscriptionOrders, SubscriptionOrder{
			SchemeId:           constituent.SchemeId,
			AmountInMinorUnits: amount,
			OrderId:            order.OrderId,
		})
	}

	return subscriptionOrders, nil
}

// RebalanceAccountBasket is the "one-click rebalance": for each of
// basketId's constituents, it revalues accountIdentifier's CURRENT
// holding (TotalUnits, regardless of any pending redemption reservation —
// see the package doc comment) at the scheme's CURRENT catalog NAV,
// derives the account's total current value across ONLY this basket's
// constituent schemes, computes what each constituent SHOULD be worth
// under the basket's target weights applied to that same total, and emits
// a BUY order for the shortfall or a SELL order for the excess. A drift of
// rebalanceThresholdInMinorUnits or less is reported as ActionHold with no
// order placed. See the test file for a fully hand-worked example.
//
// An order placement failure for one scheme (e.g. a SELL that exceeds the
// account's available — unreserved — units) is recorded in that action's
// ErrorMessage rather than aborting the whole rebalance; every other
// scheme's order still executes.
func (rebalancer *BasketRebalancer) RebalanceAccountBasket(
	accountIdentifier string,
	basketId string,
	now time.Time,
) ([]RebalanceAction, error) {
	basket, wasFound := rebalancer.LookupBasket(basketId)
	if !wasFound {
		return nil, ErrBasketNotFound
	}

	holdings := rebalancer.amcRouter.HoldingsForAccount(accountIdentifier)
	unitsBySchemeId := make(map[string]float64, len(holdings))
	for _, holding := range holdings {
		unitsBySchemeId[holding.SchemeId] = holding.TotalUnits
	}

	type valuedConstituent struct {
		constituent  BasketConstituent
		navInMinor   int64
		unitsHeld    float64
		currentValue int64
	}

	valuedConstituents := make([]valuedConstituent, 0, len(basket.Constituents))
	totalCurrentValue := int64(0)
	for _, constituent := range basket.Constituents {
		scheme, schemeFound := rebalancer.catalog.Lookup(constituent.SchemeId)
		if !schemeFound {
			// Scheme vanished from the catalog since the basket was
			// created — nothing sensible to rebalance it against.
			return nil, fmt.Errorf("%w: %s", ErrUnknownScheme, constituent.SchemeId)
		}
		unitsHeld := unitsBySchemeId[constituent.SchemeId]
		currentValue := int64(math.Round(unitsHeld * float64(scheme.CurrentNavInMinorUnits)))
		valuedConstituents = append(valuedConstituents, valuedConstituent{
			constituent:  constituent,
			navInMinor:   scheme.CurrentNavInMinorUnits,
			unitsHeld:    unitsHeld,
			currentValue: currentValue,
		})
		totalCurrentValue += currentValue
	}

	actions := make([]RebalanceAction, 0, len(valuedConstituents))
	for _, valued := range valuedConstituents {
		targetValue := int64(math.Round(float64(totalCurrentValue) * valued.constituent.TargetWeightPercent / 100.0))
		diff := targetValue - valued.currentValue

		action := RebalanceAction{
			SchemeId:                 valued.constituent.SchemeId,
			CurrentValueInMinorUnits: valued.currentValue,
			TargetValueInMinorUnits:  targetValue,
		}

		switch {
		case diff > rebalanceThresholdInMinorUnits:
			action.Action = ActionBuy
			action.AmountInMinorUnits = diff
			order, placeError := rebalancer.amcRouter.PlacePurchaseOrder(accountIdentifier, valued.constituent.SchemeId, diff, now)
			if placeError != nil {
				action.ErrorMessage = placeError.Error()
			} else {
				action.OrderId = order.OrderId
			}
		case diff < -rebalanceThresholdInMinorUnits:
			action.Action = ActionSell
			amountToSell := -diff
			action.AmountInMinorUnits = amountToSell
			unitsToSell := roundUnits(float64(amountToSell) / float64(valued.navInMinor))
			action.UnitsToSell = unitsToSell
			order, placeError := rebalancer.amcRouter.PlaceRedemptionOrder(accountIdentifier, valued.constituent.SchemeId, unitsToSell, now)
			if placeError != nil {
				action.ErrorMessage = placeError.Error()
			} else {
				action.OrderId = order.OrderId
			}
		default:
			action.Action = ActionHold
		}

		actions = append(actions, action)
	}

	return actions, nil
}

// splitProportionally divides totalAmount across constituents by
// TargetWeightPercent, rounding every constituent but the last (in
// constituents' existing — sorted — order) and giving the last whatever
// remainder makes the split sum EXACTLY to totalAmount.
func splitProportionally(totalAmount int64, constituents []BasketConstituent) map[string]int64 {
	splits := make(map[string]int64, len(constituents))
	runningTotal := int64(0)
	for i, constituent := range constituents {
		if i == len(constituents)-1 {
			splits[constituent.SchemeId] = totalAmount - runningTotal
			break
		}
		share := int64(math.Round(float64(totalAmount) * constituent.TargetWeightPercent / 100.0))
		splits[constituent.SchemeId] = share
		runningTotal += share
	}
	return splits
}

// roundUnits rounds to 4 decimal places, matching amcrouting's convention
// that unit quantities are struck to 3-4 decimal precision.
func roundUnits(units float64) float64 {
	return math.Round(units*10000) / 10000
}

func generateBasketId() (string, error) {
	randomBytes := make([]byte, 8)
	if _, readError := rand.Read(randomBytes); readError != nil {
		return "", readError
	}
	return "basket-" + hex.EncodeToString(randomBytes), nil
}
