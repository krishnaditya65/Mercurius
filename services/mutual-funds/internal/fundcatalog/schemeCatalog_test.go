package fundcatalog

import "testing"

func TestLookupKnownSchemeReturnsIt(t *testing.T) {
	catalog := NewFundCatalog()

	scheme, wasFound := catalog.Lookup("MF-DT-LIQUID003")
	if !wasFound {
		t.Fatalf("expected MF-DT-LIQUID003 to exist in the seed catalog")
	}
	if scheme.CurrentNavInMinorUnits != 100000 {
		t.Errorf("expected seed NAV 100000, got %d", scheme.CurrentNavInMinorUnits)
	}
	if scheme.Category != CategoryDebt {
		t.Errorf("expected CategoryDebt, got %s", scheme.Category)
	}
}

func TestLookupUnknownSchemeReturnsFalse(t *testing.T) {
	catalog := NewFundCatalog()

	_, wasFound := catalog.Lookup("MF-DOES-NOT-EXIST")
	if wasFound {
		t.Errorf("expected unknown scheme id to not be found")
	}
}

func TestListAllReturnsAllSeedSchemesSorted(t *testing.T) {
	catalog := NewFundCatalog()

	schemes := catalog.ListAll()
	if len(schemes) != 5 {
		t.Fatalf("expected 5 seed schemes, got %d", len(schemes))
	}
	for i := 1; i < len(schemes); i++ {
		if schemes[i-1].SchemeId >= schemes[i].SchemeId {
			t.Errorf("expected sorted ascending SchemeId order, got %s before %s", schemes[i-1].SchemeId, schemes[i].SchemeId)
		}
	}
}

func TestUpdateNavRejectsNonPositiveValue(t *testing.T) {
	catalog := NewFundCatalog()

	if updateError := catalog.UpdateNav("MF-DT-LIQUID003", 0); updateError != ErrInvalidNav {
		t.Errorf("expected ErrInvalidNav for zero NAV, got %v", updateError)
	}
	if updateError := catalog.UpdateNav("MF-DT-LIQUID003", -100); updateError != ErrInvalidNav {
		t.Errorf("expected ErrInvalidNav for negative NAV, got %v", updateError)
	}
}

func TestUpdateNavRejectsUnknownScheme(t *testing.T) {
	catalog := NewFundCatalog()

	if updateError := catalog.UpdateNav("MF-DOES-NOT-EXIST", 100); updateError != ErrUnknownScheme {
		t.Errorf("expected ErrUnknownScheme, got %v", updateError)
	}
}

func TestUpdateNavActuallyChangesTheStoredValue(t *testing.T) {
	catalog := NewFundCatalog()

	if updateError := catalog.UpdateNav("MF-DT-LIQUID003", 123456); updateError != nil {
		t.Fatalf("unexpected error: %v", updateError)
	}

	scheme, _ := catalog.Lookup("MF-DT-LIQUID003")
	if scheme.CurrentNavInMinorUnits != 123456 {
		t.Errorf("expected updated NAV 123456, got %d", scheme.CurrentNavInMinorUnits)
	}
}
