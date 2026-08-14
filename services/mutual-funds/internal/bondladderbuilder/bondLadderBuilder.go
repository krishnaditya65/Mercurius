// Package bondladderbuilder builds a real bond ladder — a spread of
// purchases across bonds with staggered maturities — over
// internal/fixedincome's illustrative catalog, and computes a real coupon-
// payment calendar/reminder list for a holder's ladder positions.
// FEATURES.md §5, "Fixed Income", item 3 ("Bond ladder builder, credit
// rating display, coupon calendar/reminders").
//
// LOUD CAVEAT, same honesty pattern as the rest of this service: building
// a ladder here does NOT place any real order anywhere — there is no
// integration with internal/primarymarketbidding or
// internal/secondarymarketbonds; a ladder purchase is recorded directly
// into this package's own in-memory holdings, at face value, as if
// executed instantly at par. A real build would route each rung's
// purchase through either a primary-market bid or a secondary-market
// trade and only record the holding once that settles. Credit ratings
// displayed here are internal/fixedincome's STATIC illustrative field, not
// a real ratings-agency feed.
package bondladderbuilder

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"mercurius/mutualFunds/internal/fixedincome"
)

var ErrInvalidInvestmentAmount = fmt.Errorf("total investment amount must be strictly positive")
var ErrInvalidNumberOfRungs = fmt.Errorf("number of rungs must be at least 1")
var ErrNotEnoughBondsForLadder = fmt.Errorf("catalog does not have enough distinct bonds to build a ladder with this many rungs")

// LadderRung is one bond purchased as part of a ladder, staggered by
// maturity against the other rungs.
type LadderRung struct {
	BondId                      string
	IssueName                   string
	MaturityDate                time.Time
	CreditRating                fixedincome.CreditRating
	AllocatedAmountInMinorUnits int64
}

// Ladder is one account's built bond ladder.
type Ladder struct {
	LadderId          string
	AccountIdentifier string
	Rungs             []LadderRung
	BuiltAt           time.Time
}

// Builder is safe for concurrent use.
type Builder struct {
	catalog *fixedincome.BondCatalog

	mutexGuardingState sync.Mutex
	laddersByAccount   map[string][]*Ladder
	holdingsByAccount  map[string]map[string]int64 // accountId -> bondId -> faceValueHeldInMinorUnits
	nextLadderSequence int
}

// NewBuilder builds a ladder builder against catalog.
func NewBuilder(catalog *fixedincome.BondCatalog) *Builder {
	return &Builder{
		catalog:           catalog,
		laddersByAccount:  make(map[string][]*Ladder),
		holdingsByAccount: make(map[string]map[string]int64),
	}
}

// BuildLadder spreads totalInvestmentInMinorUnits across numberOfRungs
// bonds picked from the catalog with STAGGERED maturities: the catalog is
// sorted ascending by MaturityDate, then numberOfRungs bonds are chosen at
// evenly-spaced indices across that sorted list (index
// round(i * (n-1)/(rungs-1)) for i in [0, rungs-1], so the first rung is
// always the nearest-maturity bond and the last rung is always the
// farthest-maturity bond, with the remaining rungs spread as evenly as
// possible in between) — a real staggered-maturity ladder, not just the
// first N bonds in the catalog.
//
// The investment amount is split evenly across rungs, with the LAST rung
// absorbing any rounding remainder so the allocations sum EXACTLY to
// totalInvestmentInMinorUnits — the same rounding convention
// internal/basketrebalancing uses for its per-leg splits.
//
// Building a ladder immediately records each rung's allocated amount as a
// held face-value position for accountIdentifier (see the package doc
// comment's caveat: this does NOT route through any real order engine).
func (builder *Builder) BuildLadder(accountIdentifier string, totalInvestmentInMinorUnits int64, numberOfRungs int, now time.Time) (*Ladder, error) {
	if totalInvestmentInMinorUnits <= 0 {
		return nil, ErrInvalidInvestmentAmount
	}
	if numberOfRungs < 1 {
		return nil, ErrInvalidNumberOfRungs
	}

	sortedBonds := builder.catalog.ListAllSortedByMaturity()
	if numberOfRungs > len(sortedBonds) {
		return nil, ErrNotEnoughBondsForLadder
	}

	selectedBonds := selectStaggeredBonds(sortedBonds, numberOfRungs)

	perRungAmount := totalInvestmentInMinorUnits / int64(numberOfRungs)
	rungs := make([]LadderRung, numberOfRungs)
	allocatedSoFar := int64(0)
	for i, bond := range selectedBonds {
		amount := perRungAmount
		if i == numberOfRungs-1 {
			amount = totalInvestmentInMinorUnits - allocatedSoFar
		}
		allocatedSoFar += amount
		rungs[i] = LadderRung{
			BondId:                      bond.BondId,
			IssueName:                   bond.IssueName,
			MaturityDate:                bond.MaturityDate,
			CreditRating:                bond.CreditRating,
			AllocatedAmountInMinorUnits: amount,
		}
	}

	builder.mutexGuardingState.Lock()
	defer builder.mutexGuardingState.Unlock()

	builder.nextLadderSequence++
	ladder := &Ladder{
		LadderId:          fmt.Sprintf("ladder-%04d", builder.nextLadderSequence),
		AccountIdentifier: accountIdentifier,
		Rungs:             rungs,
		BuiltAt:           now,
	}
	builder.laddersByAccount[accountIdentifier] = append(builder.laddersByAccount[accountIdentifier], ladder)

	holdings, wasFound := builder.holdingsByAccount[accountIdentifier]
	if !wasFound {
		holdings = make(map[string]int64)
		builder.holdingsByAccount[accountIdentifier] = holdings
	}
	for _, rung := range rungs {
		holdings[rung.BondId] += rung.AllocatedAmountInMinorUnits
	}

	return ladder, nil
}

// selectStaggeredBonds picks numberOfRungs bonds at evenly-spaced indices
// across sortedBonds (already sorted by maturity ascending).
func selectStaggeredBonds(sortedBonds []fixedincome.Bond, numberOfRungs int) []fixedincome.Bond {
	selected := make([]fixedincome.Bond, numberOfRungs)
	if numberOfRungs == 1 {
		selected[0] = sortedBonds[0]
		return selected
	}
	lastIndex := len(sortedBonds) - 1
	for i := 0; i < numberOfRungs; i++ {
		index := int(math.Round(float64(i) * float64(lastIndex) / float64(numberOfRungs-1)))
		selected[i] = sortedBonds[index]
	}
	return selected
}

// LaddersForAccount returns every ladder accountIdentifier has built,
// sorted by BuiltAt.
func (builder *Builder) LaddersForAccount(accountIdentifier string) []*Ladder {
	builder.mutexGuardingState.Lock()
	defer builder.mutexGuardingState.Unlock()

	ladders := builder.laddersByAccount[accountIdentifier]
	result := make([]*Ladder, len(ladders))
	copy(result, ladders)
	sort.Slice(result, func(i, j int) bool { return result[i].BuiltAt.Before(result[j].BuiltAt) })
	return result
}

// HoldingsForAccount returns accountIdentifier's total held face value per
// bond, across every ladder it has built.
func (builder *Builder) HoldingsForAccount(accountIdentifier string) map[string]int64 {
	builder.mutexGuardingState.Lock()
	defer builder.mutexGuardingState.Unlock()

	holdings := builder.holdingsByAccount[accountIdentifier]
	result := make(map[string]int64, len(holdings))
	for bondId, amount := range holdings {
		result[bondId] = amount
	}
	return result
}
