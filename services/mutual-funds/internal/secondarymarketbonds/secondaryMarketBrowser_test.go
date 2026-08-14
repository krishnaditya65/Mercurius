package secondarymarketbonds

import (
	"testing"
	"time"

	"mercurius/mutualFunds/internal/fixedincome"
)

func newTestMarket() (*fixedincome.BondCatalog, *SecondaryMarket) {
	catalog := fixedincome.NewBondCatalog()
	return catalog, NewSecondaryMarket(catalog)
}

func TestNewSecondaryMarketSeedsPriceForEveryCatalogBond(t *testing.T) {
	catalog, market := newTestMarket()
	for _, bond := range catalog.ListAll() {
		price, err := market.CurrentPrice(bond.BondId)
		if err != nil {
			t.Fatalf("expected a seeded price for %s: %v", bond.BondId, err)
		}
		if price <= 0 {
			t.Fatalf("expected positive seeded price for %s, got %d", bond.BondId, price)
		}
	}
}

func TestCurrentPriceUnknownBondReturnsError(t *testing.T) {
	_, market := newTestMarket()
	if _, err := market.CurrentPrice("NOT-A-BOND"); err != ErrUnknownBond {
		t.Fatalf("expected ErrUnknownBond, got %v", err)
	}
}

func TestUpdatePriceChangesCurrentPrice(t *testing.T) {
	_, market := newTestMarket()
	if err := market.UpdatePrice("GSEC-07.10-2028", 99000); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	price, _ := market.CurrentPrice("GSEC-07.10-2028")
	if price != 99000 {
		t.Fatalf("expected updated price 99000, got %d", price)
	}
}

func TestUpdatePriceRejectsNonPositivePrice(t *testing.T) {
	_, market := newTestMarket()
	if err := market.UpdatePrice("GSEC-07.10-2028", 0); err != ErrInvalidNewPrice {
		t.Fatalf("expected ErrInvalidNewPrice, got %v", err)
	}
}

func TestUpdatePriceRejectsUnknownBond(t *testing.T) {
	_, market := newTestMarket()
	if err := market.UpdatePrice("NOT-A-BOND", 1000); err != ErrUnknownBond {
		t.Fatalf("expected ErrUnknownBond, got %v", err)
	}
}

func TestListListingsReturnsEveryBondSortedByBondIdWithComputedYtm(t *testing.T) {
	catalog, market := newTestMarket()
	asOf := time.Now()

	listings := market.ListListings(asOf)
	if len(listings) != len(catalog.ListAll()) {
		t.Fatalf("expected a listing per catalog bond, got %d", len(listings))
	}
	for i := 1; i < len(listings); i++ {
		if listings[i-1].Bond.BondId > listings[i].Bond.BondId {
			t.Fatal("expected listings sorted by BondId")
		}
	}
	for _, listing := range listings {
		if listing.YtmError != "" {
			t.Fatalf("unexpected YTM error for %s: %s", listing.Bond.BondId, listing.YtmError)
		}
		if listing.YieldToMaturityPercent <= 0 {
			t.Fatalf("expected a positive YTM for %s, got %v", listing.Bond.BondId, listing.YieldToMaturityPercent)
		}
	}
}

func TestListListingsDiscountedBondShowsHigherYtmThanPremiumBond(t *testing.T) {
	_, market := newTestMarket()
	asOf := time.Now()

	if err := market.UpdatePrice("GSEC-07.26-2031", 90000); err != nil { // deep discount
		t.Fatalf("unexpected error: %v", err)
	}
	if err := market.UpdatePrice("GSEC-06.90-2036", 110000); err != nil { // deep premium
		t.Fatalf("unexpected error: %v", err)
	}

	listings := market.ListListings(asOf)
	byId := map[string]Listing{}
	for _, listing := range listings {
		byId[listing.Bond.BondId] = listing
	}

	if byId["GSEC-07.26-2031"].YieldToMaturityPercent <= byId["GSEC-06.90-2036"].YieldToMaturityPercent {
		t.Fatalf("expected the deeply-discounted bond's YTM to exceed the deeply-premium bond's YTM")
	}
}
