// Package globalmarketsaccess is a small, self-contained, illustrative
// stand-in for "Global markets access (US/international stocks via GDR/ADR
// or partner brokerage rails)" — FEATURES.md §17, "Wealth & Product
// Breadth", a `[P4]` item.
//
// LOUD CAVEAT: THIS DOES NOT CONNECT TO ANY REAL GDR/ADR ISSUANCE PROCESS
// OR PARTNER BROKERAGE. There is no integration anywhere in this repo with
// a real US/international broker (e.g. DriveWealth, Interactive Brokers,
// or a real GDR/ADR depository bank like BNY Mellon or Citibank). Every
// symbol in this catalog is fictitious. The order-routing state machine
// below stands in for "sent to a partner brokerage rail" the same honest-
// placeholder way internal/amcrouting stands in for "sent to a real AMC" —
// the "partner" on the other end is this package's own in-memory map.
//
// A real build would integrate currency conversion with ledger's actual
// internal/multicurrencywallet (this package deliberately does NOT call
// that real service — it is self-contained and reuses only the CONCEPT of
// a multi-currency wallet: a per-currency balance and an FX rate table).
package globalmarketsaccess

import (
	"fmt"
	"sort"
	"sync"
)

// GlobalSymbol is one illustrative, entirely fictitious international
// equity available for "global markets access" — modeled as an ADR-style
// listing (a US-quoted receipt representing shares of a foreign company).
type GlobalSymbol struct {
	SymbolId                 string
	CompanyName              string
	HomeExchangeCountry      string
	QuoteCurrency            string // ISO 4217, e.g. "USD"
	CurrentPriceInMinorUnits int64  // in QuoteCurrency's minor units (e.g. cents)
}

var ErrUnknownSymbol = fmt.Errorf("no such symbol exists in the global markets catalog")
var ErrInvalidPrice = fmt.Errorf("price must be strictly positive")

// Catalog is a read-mostly, concurrency-safe lookup of the static symbol
// list — same role fundcatalog plays for mutual funds.
type Catalog struct {
	mutexGuardingSymbols sync.RWMutex
	symbolsById          map[string]GlobalSymbol
}

// NewCatalog returns a catalog pre-populated with five illustrative,
// entirely fictitious ADR-style international symbols.
func NewCatalog() *Catalog {
	seedSymbols := []GlobalSymbol{
		{SymbolId: "ADR-NEXATECH", CompanyName: "NexaTech Global Holdings ADR", HomeExchangeCountry: "JP", QuoteCurrency: "USD", CurrentPriceInMinorUnits: 8420},
		{SymbolId: "ADR-AURORAMOTORS", CompanyName: "Aurora Motors International ADR", HomeExchangeCountry: "DE", QuoteCurrency: "USD", CurrentPriceInMinorUnits: 15630},
		{SymbolId: "ADR-BLUEHARBOR", CompanyName: "Blue Harbor Shipping ADR", HomeExchangeCountry: "SG", QuoteCurrency: "USD", CurrentPriceInMinorUnits: 4275},
		{SymbolId: "ADR-VERDANTFOODS", CompanyName: "Verdant Foods Group ADR", HomeExchangeCountry: "BR", QuoteCurrency: "USD", CurrentPriceInMinorUnits: 3190},
		{SymbolId: "ADR-SILICONBAY", CompanyName: "Silicon Bay Semiconductors ADR", HomeExchangeCountry: "TW", QuoteCurrency: "USD", CurrentPriceInMinorUnits: 22750},
	}

	symbolsById := make(map[string]GlobalSymbol, len(seedSymbols))
	for _, symbol := range seedSymbols {
		symbolsById[symbol.SymbolId] = symbol
	}

	return &Catalog{symbolsById: symbolsById}
}

// Lookup returns the symbol, or false if symbolId isn't in the catalog.
func (catalog *Catalog) Lookup(symbolId string) (GlobalSymbol, bool) {
	catalog.mutexGuardingSymbols.RLock()
	defer catalog.mutexGuardingSymbols.RUnlock()

	symbol, wasFound := catalog.symbolsById[symbolId]
	return symbol, wasFound
}

// ListAll returns every symbol, sorted by SymbolId for a deterministic
// response.
func (catalog *Catalog) ListAll() []GlobalSymbol {
	catalog.mutexGuardingSymbols.RLock()
	defer catalog.mutexGuardingSymbols.RUnlock()

	symbols := make([]GlobalSymbol, 0, len(catalog.symbolsById))
	for _, symbol := range catalog.symbolsById {
		symbols = append(symbols, symbol)
	}
	sort.Slice(symbols, func(i, j int) bool { return symbols[i].SymbolId < symbols[j].SymbolId })
	return symbols
}

// UpdatePrice overwrites symbolId's current price. Testing/demo-only hook,
// same caveat as internal/fundcatalog.UpdateNav.
func (catalog *Catalog) UpdatePrice(symbolId string, newPriceInMinorUnits int64) error {
	if newPriceInMinorUnits <= 0 {
		return ErrInvalidPrice
	}

	catalog.mutexGuardingSymbols.Lock()
	defer catalog.mutexGuardingSymbols.Unlock()

	symbol, wasFound := catalog.symbolsById[symbolId]
	if !wasFound {
		return ErrUnknownSymbol
	}
	symbol.CurrentPriceInMinorUnits = newPriceInMinorUnits
	catalog.symbolsById[symbolId] = symbol
	return nil
}
