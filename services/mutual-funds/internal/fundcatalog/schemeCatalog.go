// Package fundcatalog is a small STATIC catalog of illustrative mutual
// fund schemes — see FEATURES.md §4, "Direct AMC routing (commission-free
// MF investing)". It exists only so internal/amcrouting has somewhere to
// look up a scheme's current NAV; it is NOT a feed from any real AMC/RTA
// (Registrar & Transfer Agent) system.
//
// TODO(real build): a real catalog is either ingested from AMFI's daily
// NAV file or fetched live from each AMC/RTA's API. This one is five
// hardcoded, entirely fictitious schemes with NAVs that never move.
package fundcatalog

import (
	"fmt"
	"sort"
	"sync"
)

// SchemeCategory is a coarse asset-class classification for a scheme.
type SchemeCategory string

const (
	CategoryEquity SchemeCategory = "EQUITY"
	CategoryDebt   SchemeCategory = "DEBT"
	CategoryHybrid SchemeCategory = "HYBRID"
)

// Scheme is one illustrative mutual fund scheme. CurrentNavInMinorUnits is
// the Net Asset Value per unit, expressed in minor currency units (e.g.
// paise for INR) — matching this repo's convention elsewhere
// (fundsegregation, withdrawalworkflow) of never handling money as a
// floating-point major-unit value.
type Scheme struct {
	SchemeId               string
	Name                   string
	Category               SchemeCategory
	CurrentNavInMinorUnits int64
}

// FundCatalog is a read-mostly, concurrency-safe lookup of the static
// scheme list. It supports UpdateNav purely so tests (and a live demo)
// can move a scheme's NAV to a known value deterministically — a real
// catalog's NAV would instead be refreshed by ingesting AMFI's daily NAV
// publication, never by a caller reaching in and setting it directly.
type FundCatalog struct {
	mutexGuardingSchemes sync.RWMutex
	schemesById          map[string]Scheme
}

var ErrUnknownScheme = fmt.Errorf("no such scheme exists in the catalog")
var ErrInvalidNav = fmt.Errorf("NAV must be strictly positive")

// NewFundCatalog returns a catalog pre-populated with five illustrative,
// entirely fictitious schemes spanning EQUITY, DEBT, and HYBRID — enough
// variety to exercise the routing and SIP logic without pretending to be
// a real AMC's product list.
func NewFundCatalog() *FundCatalog {
	seedSchemes := []Scheme{
		{SchemeId: "MF-EQ-BLUECHIP001", Name: "Mercurius Bluechip Equity Fund", Category: CategoryEquity, CurrentNavInMinorUnits: 45237},
		{SchemeId: "MF-EQ-MIDCAP002", Name: "Mercurius Midcap Growth Fund", Category: CategoryEquity, CurrentNavInMinorUnits: 12890},
		{SchemeId: "MF-DT-LIQUID003", Name: "Mercurius Liquid Debt Fund", Category: CategoryDebt, CurrentNavInMinorUnits: 100000},
		{SchemeId: "MF-HY-BALANCED004", Name: "Mercurius Balanced Hybrid Fund", Category: CategoryHybrid, CurrentNavInMinorUnits: 8765},
		{SchemeId: "MF-DT-CORPBOND005", Name: "Mercurius Corporate Bond Fund", Category: CategoryDebt, CurrentNavInMinorUnits: 4520},
	}

	schemesById := make(map[string]Scheme, len(seedSchemes))
	for _, scheme := range seedSchemes {
		schemesById[scheme.SchemeId] = scheme
	}

	return &FundCatalog{schemesById: schemesById}
}

// Lookup returns the scheme, or false if schemeId isn't in the catalog.
func (catalog *FundCatalog) Lookup(schemeId string) (Scheme, bool) {
	catalog.mutexGuardingSchemes.RLock()
	defer catalog.mutexGuardingSchemes.RUnlock()

	scheme, wasFound := catalog.schemesById[schemeId]
	return scheme, wasFound
}

// ListAll returns every scheme in the catalog, sorted by SchemeId for a
// deterministic response.
func (catalog *FundCatalog) ListAll() []Scheme {
	catalog.mutexGuardingSchemes.RLock()
	defer catalog.mutexGuardingSchemes.RUnlock()

	schemes := make([]Scheme, 0, len(catalog.schemesById))
	for _, scheme := range catalog.schemesById {
		schemes = append(schemes, scheme)
	}
	sort.Slice(schemes, func(i, j int) bool { return schemes[i].SchemeId < schemes[j].SchemeId })
	return schemes
}

// UpdateNav overwrites schemeId's current NAV. Exists purely for
// deterministic testing/demoing — see the package doc comment.
func (catalog *FundCatalog) UpdateNav(schemeId string, newNavInMinorUnits int64) error {
	if newNavInMinorUnits <= 0 {
		return ErrInvalidNav
	}

	catalog.mutexGuardingSchemes.Lock()
	defer catalog.mutexGuardingSchemes.Unlock()

	scheme, wasFound := catalog.schemesById[schemeId]
	if !wasFound {
		return ErrUnknownScheme
	}
	scheme.CurrentNavInMinorUnits = newNavInMinorUnits
	catalog.schemesById[schemeId] = scheme
	return nil
}
