package globalmarketsaccess

import "testing"

func TestNewCatalogSeedsFiveSymbols(t *testing.T) {
	catalog := NewCatalog()
	if len(catalog.ListAll()) != 5 {
		t.Fatalf("expected 5 seed symbols, got %d", len(catalog.ListAll()))
	}
}

func TestLookupFindsKnownSymbol(t *testing.T) {
	catalog := NewCatalog()
	symbol, wasFound := catalog.Lookup("ADR-NEXATECH")
	if !wasFound {
		t.Fatal("expected to find ADR-NEXATECH")
	}
	if symbol.QuoteCurrency != "USD" {
		t.Fatalf("expected USD quote currency, got %s", symbol.QuoteCurrency)
	}
}

func TestLookupMissingSymbolReturnsFalse(t *testing.T) {
	catalog := NewCatalog()
	if _, wasFound := catalog.Lookup("NOT-A-SYMBOL"); wasFound {
		t.Fatal("expected not to find NOT-A-SYMBOL")
	}
}

func TestListAllIsSortedBySymbolId(t *testing.T) {
	catalog := NewCatalog()
	symbols := catalog.ListAll()
	for i := 1; i < len(symbols); i++ {
		if symbols[i-1].SymbolId > symbols[i].SymbolId {
			t.Fatal("expected symbols sorted by SymbolId")
		}
	}
}

func TestUpdatePriceChangesPrice(t *testing.T) {
	catalog := NewCatalog()
	if err := catalog.UpdatePrice("ADR-NEXATECH", 9000); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	symbol, _ := catalog.Lookup("ADR-NEXATECH")
	if symbol.CurrentPriceInMinorUnits != 9000 {
		t.Fatalf("expected updated price 9000, got %d", symbol.CurrentPriceInMinorUnits)
	}
}

func TestUpdatePriceRejectsNonPositivePrice(t *testing.T) {
	catalog := NewCatalog()
	if err := catalog.UpdatePrice("ADR-NEXATECH", 0); err != ErrInvalidPrice {
		t.Fatalf("expected ErrInvalidPrice, got %v", err)
	}
}

func TestUpdatePriceRejectsUnknownSymbol(t *testing.T) {
	catalog := NewCatalog()
	if err := catalog.UpdatePrice("NOT-A-SYMBOL", 100); err != ErrUnknownSymbol {
		t.Fatalf("expected ErrUnknownSymbol, got %v", err)
	}
}
