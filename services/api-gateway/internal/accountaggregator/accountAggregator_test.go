package accountaggregator

import "testing"

func TestMockedExternalInstitutionHoldingsSourceReturnsNonEmptyFixtures(t *testing.T) {
	holdings := MockedExternalInstitutionHoldingsSource("acct-1")
	if len(holdings) == 0 {
		t.Fatalf("expected non-empty fixture external holdings")
	}
	for _, holding := range holdings {
		if holding.InstitutionName == "" {
			t.Fatalf("expected every fixture holding to name an institution")
		}
	}
}

func TestMockedExternalInstitutionHoldingsSourceIsDeterministic(t *testing.T) {
	first := MockedExternalInstitutionHoldingsSource("acct-1")
	second := MockedExternalInstitutionHoldingsSource("acct-1")
	if len(first) != len(second) {
		t.Fatalf("expected deterministic fixture output across calls")
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("expected identical fixture holdings across calls, differed at index %d", i)
		}
	}
}

func TestBuildUnifiedNetWorthViewSumsPlatformHoldings(t *testing.T) {
	platformHoldings := []PlatformHolding{
		{SourceService: "oms-gateway", InstrumentDescription: "RELIANCE", CurrentValueInMinorUnits: 1000000},
		{SourceService: "mutual-funds", InstrumentDescription: "Index Fund", CurrentValueInMinorUnits: 2000000},
	}
	view := BuildUnifiedNetWorthView("acct-1", platformHoldings)

	if view.TotalPlatformValueInMinorUnits != 3000000 {
		t.Fatalf("expected platform total 3000000, got %d", view.TotalPlatformValueInMinorUnits)
	}
}

func TestBuildUnifiedNetWorthViewTotalIsPlatformPlusExternal(t *testing.T) {
	platformHoldings := []PlatformHolding{{CurrentValueInMinorUnits: 1000000}}
	view := BuildUnifiedNetWorthView("acct-1", platformHoldings)

	expectedTotal := view.TotalPlatformValueInMinorUnits + view.TotalExternalValueInMinorUnits
	if view.TotalNetWorthInMinorUnits != expectedTotal {
		t.Fatalf("expected total net worth to equal platform+external, got %d vs expected %d", view.TotalNetWorthInMinorUnits, expectedTotal)
	}
}

func TestBuildUnifiedNetWorthViewWithNoPlatformHoldingsStillIncludesExternal(t *testing.T) {
	view := BuildUnifiedNetWorthView("acct-1", nil)
	if view.TotalPlatformValueInMinorUnits != 0 {
		t.Fatalf("expected zero platform value with no platform holdings, got %d", view.TotalPlatformValueInMinorUnits)
	}
	if view.TotalExternalValueInMinorUnits == 0 {
		t.Fatalf("expected non-zero external (fixture) value even with no platform holdings")
	}
}

func TestBuildUnifiedNetWorthViewAlwaysMarksExternalDataAsNotFromARealAaNetwork(t *testing.T) {
	view := BuildUnifiedNetWorthView("acct-1", nil)
	if view.IsExternalDataFromRealAaNetwork {
		t.Fatalf("expected IsExternalDataFromRealAaNetwork to always be false — this package never connects to a real AA network")
	}
}

func TestBuildUnifiedNetWorthViewPreservesAccountIdentifier(t *testing.T) {
	view := BuildUnifiedNetWorthView("acct-42", nil)
	if view.AccountIdentifier != "acct-42" {
		t.Fatalf("expected account identifier to round-trip, got %q", view.AccountIdentifier)
	}
}

func TestBuildUnifiedNetWorthViewPreservesPlatformHoldingsSlice(t *testing.T) {
	platformHoldings := []PlatformHolding{{SourceService: "oms-gateway", CurrentValueInMinorUnits: 500}}
	view := BuildUnifiedNetWorthView("acct-1", platformHoldings)
	if len(view.PlatformHoldings) != 1 || view.PlatformHoldings[0].SourceService != "oms-gateway" {
		t.Fatalf("expected platform holdings to round-trip in the view")
	}
}
