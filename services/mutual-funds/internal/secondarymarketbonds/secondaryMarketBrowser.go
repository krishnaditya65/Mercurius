package secondarymarketbonds

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"mercurius/mutualFunds/internal/fixedincome"
)

var ErrUnknownBond = fmt.Errorf("no such bond exists in the fixed-income catalog")
var ErrInvalidNewPrice = fmt.Errorf("price must be strictly positive")

// Listing is one bond's secondary-market browsing entry: the catalog's
// static instrument details, an illustrative CurrentPriceInMinorUnits, and
// (where computable) that price's real YieldToMaturityPercent as of asOf.
// YtmError is set instead when the YTM can't be computed for the current
// asOf/price combination (e.g. the bond has already matured as of asOf).
type Listing struct {
	Bond                     fixedincome.Bond
	CurrentPriceInMinorUnits int64
	YieldToMaturityPercent   float64
	PeriodsRemaining         int
	YtmError                 string
}

// SecondaryMarket is a small, self-contained illustrative secondary market:
// a catalog reference plus a per-bond current price that never moves on
// its own (only UpdatePrice, exposed the same demo-only way
// internal/fundcatalog.UpdateNav is, changes it).
type SecondaryMarket struct {
	catalog *fixedincome.BondCatalog

	mutexGuardingPrices sync.RWMutex
	pricesByBondId      map[string]int64
}

// NewSecondaryMarket seeds an illustrative current price for every bond in
// catalog: G-Secs and the longer SGB trade at a small discount to face
// value, the shorter SGB and both T-Bills trade at a small discount too —
// hand-picked round numbers, not derived from any real market data.
func NewSecondaryMarket(catalog *fixedincome.BondCatalog) *SecondaryMarket {
	seedPrices := map[string]int64{
		"GSEC-07.10-2028":  98500,
		"GSEC-07.26-2031":  101200,
		"GSEC-06.90-2036":  95800,
		"TBILL-91D-NOV26":  98600,
		"TBILL-364D-AUG27": 93900,
		"SGB-2026-SERIES2": 615000,
		"SGB-2024-SERIES1": 601500,
	}

	pricesByBondId := make(map[string]int64, len(seedPrices))
	for _, bond := range catalog.ListAll() {
		if price, wasSeeded := seedPrices[bond.BondId]; wasSeeded {
			pricesByBondId[bond.BondId] = price
		} else {
			pricesByBondId[bond.BondId] = bond.FaceValueInMinorUnits
		}
	}

	return &SecondaryMarket{catalog: catalog, pricesByBondId: pricesByBondId}
}

// UpdatePrice overwrites bondId's illustrative current secondary-market
// price. Testing/demo-only hook, same caveat as
// internal/fundcatalog.UpdateNav.
func (market *SecondaryMarket) UpdatePrice(bondId string, newPriceInMinorUnits int64) error {
	if newPriceInMinorUnits <= 0 {
		return ErrInvalidNewPrice
	}

	market.mutexGuardingPrices.Lock()
	defer market.mutexGuardingPrices.Unlock()

	if _, wasFound := market.pricesByBondId[bondId]; !wasFound {
		return ErrUnknownBond
	}
	market.pricesByBondId[bondId] = newPriceInMinorUnits
	return nil
}

// CurrentPrice returns bondId's current illustrative secondary-market
// price.
func (market *SecondaryMarket) CurrentPrice(bondId string) (int64, error) {
	market.mutexGuardingPrices.RLock()
	defer market.mutexGuardingPrices.RUnlock()

	price, wasFound := market.pricesByBondId[bondId]
	if !wasFound {
		return 0, ErrUnknownBond
	}
	return price, nil
}

// ListListings returns a browsable listing for every bond in the catalog,
// each with its current price and — where the bond hasn't matured as of
// asOf — a real computed YieldToMaturityPercent. Sorted by BondId for a
// deterministic response.
func (market *SecondaryMarket) ListListings(asOf time.Time) []Listing {
	bonds := market.catalog.ListAll()

	market.mutexGuardingPrices.RLock()
	defer market.mutexGuardingPrices.RUnlock()

	listings := make([]Listing, 0, len(bonds))
	for _, bond := range bonds {
		price := market.pricesByBondId[bond.BondId]
		listing := Listing{Bond: bond, CurrentPriceInMinorUnits: price}

		ytm, periods, ytmError := CalculateYieldToMaturity(bond.FaceValueInMinorUnits, bond.CouponRatePercent, bond.PaymentsPerYear, price, bond.MaturityDate, asOf)
		if ytmError != nil {
			listing.YtmError = ytmError.Error()
		} else {
			listing.YieldToMaturityPercent = ytm
			listing.PeriodsRemaining = periods
		}
		listings = append(listings, listing)
	}

	sort.Slice(listings, func(i, j int) bool { return listings[i].Bond.BondId < listings[j].Bond.BondId })
	return listings
}
