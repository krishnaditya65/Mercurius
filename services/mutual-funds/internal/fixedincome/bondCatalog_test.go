package fixedincome

import "testing"

func TestNewBondCatalogSeedsSevenBonds(t *testing.T) {
	catalog := NewBondCatalog()
	bonds := catalog.ListAll()
	if len(bonds) != 7 {
		t.Fatalf("expected 7 seed bonds, got %d", len(bonds))
	}
}

func TestLookupFindsKnownBond(t *testing.T) {
	catalog := NewBondCatalog()
	bond, wasFound := catalog.Lookup("GSEC-07.10-2028")
	if !wasFound {
		t.Fatal("expected to find GSEC-07.10-2028")
	}
	if bond.CouponRatePercent != 7.10 {
		t.Fatalf("expected coupon 7.10, got %v", bond.CouponRatePercent)
	}
}

func TestLookupMissingBondReturnsFalse(t *testing.T) {
	catalog := NewBondCatalog()
	_, wasFound := catalog.Lookup("NOT-A-BOND")
	if wasFound {
		t.Fatal("expected not to find NOT-A-BOND")
	}
}

func TestListAllIsSortedByBondId(t *testing.T) {
	catalog := NewBondCatalog()
	bonds := catalog.ListAll()
	for i := 1; i < len(bonds); i++ {
		if bonds[i-1].BondId > bonds[i].BondId {
			t.Fatalf("bonds not sorted: %s before %s", bonds[i-1].BondId, bonds[i].BondId)
		}
	}
}

func TestListAllSortedByMaturityIsAscending(t *testing.T) {
	catalog := NewBondCatalog()
	bonds := catalog.ListAllSortedByMaturity()
	for i := 1; i < len(bonds); i++ {
		if bonds[i-1].MaturityDate.After(bonds[i].MaturityDate) {
			t.Fatalf("bonds not sorted by maturity: %s after %s", bonds[i-1].BondId, bonds[i].BondId)
		}
	}
}

func TestTreasuryBillsHaveNoCoupon(t *testing.T) {
	catalog := NewBondCatalog()
	bond, _ := catalog.Lookup("TBILL-91D-NOV26")
	if bond.CouponRatePercent != 0 || bond.PaymentsPerYear != 0 {
		t.Fatalf("expected T-Bill to have zero coupon/payments, got %v/%d", bond.CouponRatePercent, bond.PaymentsPerYear)
	}
}

func TestSeedAuctionCalendarHasEntriesForGsecsAndTbillsOnly(t *testing.T) {
	entries := SeedAuctionCalendar()
	if len(entries) != 5 {
		t.Fatalf("expected 5 auction calendar entries, got %d", len(entries))
	}
	for _, entry := range entries {
		if entry.NotifiedAmountInMinorUnits <= 0 {
			t.Fatalf("expected positive notified amount for %s", entry.BondId)
		}
	}
}

func TestSeedAuctionCalendarBondIdsExistInCatalog(t *testing.T) {
	catalog := NewBondCatalog()
	for _, entry := range SeedAuctionCalendar() {
		if _, wasFound := catalog.Lookup(entry.BondId); !wasFound {
			t.Fatalf("auction calendar references unknown bond %s", entry.BondId)
		}
	}
}
