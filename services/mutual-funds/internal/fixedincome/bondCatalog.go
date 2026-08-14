// Package fixedincome is a small STATIC catalog of illustrative government
// fixed-income instruments — G-Secs (Government Securities), T-Bills
// (Treasury Bills), and SGBs (Sovereign Gold Bonds) — see FEATURES.md §5,
// "Fixed Income". It plays the same foundational role for
// internal/primarymarketbidding, internal/secondarymarketbonds, and
// internal/bondladderbuilder that internal/fundcatalog plays for the
// mutual-fund packages.
//
// LOUD CAVEAT: this is NOT connected to any real RBI (Reserve Bank of
// India) auction system, CCIL/NDS-OM (the real secondary market for
// G-Secs), or any real bond depository. Every instrument below is
// hand-invented and entirely fictitious; maturity dates, coupon rates, and
// credit ratings are illustrative only.
package fixedincome

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// BondType classifies the instrument.
type BondType string

const (
	BondTypeGovernmentSecurity BondType = "GSEC"
	BondTypeTreasuryBill       BondType = "TBILL"
	BondTypeSovereignGoldBond  BondType = "SGB"
)

// CreditRating is a STATIC, illustrative rating field — NOT sourced from
// any real credit rating agency (CRISIL, ICRA, Moody's, S&P, ...). In
// reality every instrument modeled here is a sovereign (Government of
// India) obligation and would be treated as the top of the credit
// spectrum; the field carries varied illustrative values purely so this
// catalog can stand in for a broader ladder that might one day include
// non-sovereign (corporate) bonds too.
type CreditRating string

const (
	CreditRatingAAA CreditRating = "AAA"
	CreditRatingAA  CreditRating = "AA"
	CreditRatingA   CreditRating = "A"
)

// Bond is one illustrative fixed-income instrument. CouponRatePercent and
// PaymentsPerYear are both 0 for a BondTypeTreasuryBill and
// BondTypeSovereignGoldBond's cash coupon (SGBs pay a small fixed cash
// interest in reality; this catalog models them as coupon-bearing at
// PaymentsPerYear=2 for illustration) — T-Bills are pure discount
// instruments: no periodic coupon, redeemed at FaceValueInMinorUnits on
// MaturityDate, with the entire return embedded in the discounted issue
// price.
type Bond struct {
	BondId                string
	IssueName             string
	BondType              BondType
	IssueDate             time.Time
	MaturityDate          time.Time
	CouponRatePercent     float64 // annualized coupon rate, 0 for T-Bills
	PaymentsPerYear       int     // 0 for T-Bills, 2 (semi-annual) otherwise
	FaceValueInMinorUnits int64
	CreditRating          CreditRating
}

var ErrUnknownBond = fmt.Errorf("no such bond exists in the fixed-income catalog")

// BondCatalog is a read-mostly, concurrency-safe lookup of the static
// instrument list.
type BondCatalog struct {
	mutexGuardingBonds sync.RWMutex
	bondsById          map[string]Bond
}

// NewBondCatalog returns a catalog pre-populated with seven illustrative,
// entirely fictitious instruments: three G-Secs, two T-Bills, and two
// SGBs, spanning maturities from a few months to ten years so
// internal/bondladderbuilder has real staggered maturities to spread a
// ladder across.
func NewBondCatalog() *BondCatalog {
	mustParseDate := func(value string) time.Time {
		parsed, parseError := time.Parse("2006-01-02", value)
		if parseError != nil {
			panic(fmt.Sprintf("fixedincome: invalid fixture date %q: %v", value, parseError))
		}
		return parsed
	}

	seedBonds := []Bond{
		{
			BondId: "GSEC-07.10-2028", IssueName: "7.10% GS 2028", BondType: BondTypeGovernmentSecurity,
			IssueDate: mustParseDate("2023-08-01"), MaturityDate: mustParseDate("2028-08-01"),
			CouponRatePercent: 7.10, PaymentsPerYear: 2, FaceValueInMinorUnits: 100000, CreditRating: CreditRatingAAA,
		},
		{
			BondId: "GSEC-07.26-2031", IssueName: "7.26% GS 2031", BondType: BondTypeGovernmentSecurity,
			IssueDate: mustParseDate("2021-02-01"), MaturityDate: mustParseDate("2031-02-01"),
			CouponRatePercent: 7.26, PaymentsPerYear: 2, FaceValueInMinorUnits: 100000, CreditRating: CreditRatingAAA,
		},
		{
			BondId: "GSEC-06.90-2036", IssueName: "6.90% GS 2036", BondType: BondTypeGovernmentSecurity,
			IssueDate: mustParseDate("2026-04-15"), MaturityDate: mustParseDate("2036-04-15"),
			CouponRatePercent: 6.90, PaymentsPerYear: 2, FaceValueInMinorUnits: 100000, CreditRating: CreditRatingAA,
		},
		{
			BondId: "TBILL-91D-NOV26", IssueName: "91-Day T-Bill Nov 2026", BondType: BondTypeTreasuryBill,
			IssueDate: mustParseDate("2026-08-06"), MaturityDate: mustParseDate("2026-11-05"),
			CouponRatePercent: 0, PaymentsPerYear: 0, FaceValueInMinorUnits: 100000, CreditRating: CreditRatingAAA,
		},
		{
			BondId: "TBILL-364D-AUG27", IssueName: "364-Day T-Bill Aug 2027", BondType: BondTypeTreasuryBill,
			IssueDate: mustParseDate("2026-08-06"), MaturityDate: mustParseDate("2027-08-05"),
			CouponRatePercent: 0, PaymentsPerYear: 0, FaceValueInMinorUnits: 100000, CreditRating: CreditRatingAAA,
		},
		{
			BondId: "SGB-2026-SERIES2", IssueName: "SGB 2026-27 Series II", BondType: BondTypeSovereignGoldBond,
			IssueDate: mustParseDate("2026-09-01"), MaturityDate: mustParseDate("2034-09-01"),
			CouponRatePercent: 2.50, PaymentsPerYear: 2, FaceValueInMinorUnits: 620000, CreditRating: CreditRatingAAA,
		},
		{
			BondId: "SGB-2024-SERIES1", IssueName: "SGB 2024-25 Series I", BondType: BondTypeSovereignGoldBond,
			IssueDate: mustParseDate("2024-04-01"), MaturityDate: mustParseDate("2032-04-01"),
			CouponRatePercent: 2.50, PaymentsPerYear: 2, FaceValueInMinorUnits: 590000, CreditRating: CreditRatingAA,
		},
	}

	bondsById := make(map[string]Bond, len(seedBonds))
	for _, bond := range seedBonds {
		bondsById[bond.BondId] = bond
	}

	return &BondCatalog{bondsById: bondsById}
}

// Lookup returns the bond, or false if bondId isn't in the catalog.
func (catalog *BondCatalog) Lookup(bondId string) (Bond, bool) {
	catalog.mutexGuardingBonds.RLock()
	defer catalog.mutexGuardingBonds.RUnlock()

	bond, wasFound := catalog.bondsById[bondId]
	return bond, wasFound
}

// ListAll returns every bond in the catalog, sorted by BondId for a
// deterministic response.
func (catalog *BondCatalog) ListAll() []Bond {
	catalog.mutexGuardingBonds.RLock()
	defer catalog.mutexGuardingBonds.RUnlock()

	bonds := make([]Bond, 0, len(catalog.bondsById))
	for _, bond := range catalog.bondsById {
		bonds = append(bonds, bond)
	}
	sort.Slice(bonds, func(i, j int) bool { return bonds[i].BondId < bonds[j].BondId })
	return bonds
}

// ListAllSortedByMaturity returns every bond sorted by MaturityDate
// ascending — the ordering internal/bondladderbuilder needs to stagger a
// ladder's rungs.
func (catalog *BondCatalog) ListAllSortedByMaturity() []Bond {
	bonds := catalog.ListAll()
	sort.Slice(bonds, func(i, j int) bool { return bonds[i].MaturityDate.Before(bonds[j].MaturityDate) })
	return bonds
}
