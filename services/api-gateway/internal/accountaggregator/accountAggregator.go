// Package accountaggregator is FEATURES.md §18's "Account Aggregator
// integration — pull holdings from other brokers/banks for a unified
// net-worth view".
//
// ==========================================================================
// LOUD WARNING: THIS DOES NOT CONNECT TO ANY REAL ACCOUNT AGGREGATOR
// NETWORK/FRAMEWORK (e.g. India's AA ecosystem — Sahamati/NBBL, or any
// equivalent regime elsewhere). There is no real external institution
// reachable from this environment, no real consent-manager integration,
// and no real regulatory AA-framework registration. This package
// provides:
//   - a MOCKED "external institution" holdings source — a small fixture
//     set standing in for what a real AA network would return after a
//     real consent flow
//   - a REAL endpoint (see api-gateway's `GET
//     /account-aggregator/net-worth` handler) that merges those FIXTURE
//     external holdings with THIS PLATFORM's own REAL holdings (pulled
//     live from oms-gateway's `GET /positions` and mutual-funds'
//     `GET /holdings`) into one real, computed unified net-worth view.
//
// The MERGING/AGGREGATION MATH is real. The "other institution" data
// feeding into it is not.
// ==========================================================================
package accountaggregator

// ExternalHolding is one holding reported by a MOCKED external
// institution (a stand-in for what a real Account Aggregator consent
// flow would return — see the package doc comment's warning).
type ExternalHolding struct {
	InstitutionName          string `json:"institutionName"`
	InstrumentDescription    string `json:"instrumentDescription"`
	CurrentValueInMinorUnits int64  `json:"currentValueInMinorUnits"`
}

// PlatformHolding is one holding this platform itself already knows
// about (from oms-gateway positions or mutual-funds holdings) —
// real data, not fixture data.
type PlatformHolding struct {
	SourceService            string `json:"sourceService"` // "oms-gateway" or "mutual-funds"
	InstrumentDescription    string `json:"instrumentDescription"`
	CurrentValueInMinorUnits int64  `json:"currentValueInMinorUnits"`
}

// UnifiedNetWorthView is the real, computed merge of this platform's own
// real holdings and the mocked external institutions' holdings.
type UnifiedNetWorthView struct {
	AccountIdentifier               string            `json:"accountIdentifier"`
	PlatformHoldings                []PlatformHolding `json:"platformHoldings"`
	ExternalHoldings                []ExternalHolding `json:"externalHoldings"`
	TotalPlatformValueInMinorUnits  int64             `json:"totalPlatformValueInMinorUnits"`
	TotalExternalValueInMinorUnits  int64             `json:"totalExternalValueInMinorUnits"`
	TotalNetWorthInMinorUnits       int64             `json:"totalNetWorthInMinorUnits"`
	IsExternalDataFromRealAaNetwork bool              `json:"isExternalDataFromRealAaNetwork"` // ALWAYS false — see package warning
}

// MockedExternalInstitutionHoldingsSource returns a fixture set of
// holdings standing in for what a real Account Aggregator network would
// return for accountIdentifier after a real consent flow. Deterministic
// per-account so tests and demos are reproducible — NOT randomized, and
// NOT reflective of any real institution's real data.
func MockedExternalInstitutionHoldingsSource(accountIdentifier string) []ExternalHolding {
	return []ExternalHolding{
		{InstitutionName: "Illustrative Bank Ltd (MOCK — not real)", InstrumentDescription: "Savings Account Balance", CurrentValueInMinorUnits: 15000000},
		{InstitutionName: "Illustrative Bank Ltd (MOCK — not real)", InstrumentDescription: "Fixed Deposit", CurrentValueInMinorUnits: 50000000},
		{InstitutionName: "Illustrative Competitor Broker (MOCK — not real)", InstrumentDescription: "Equity Holdings", CurrentValueInMinorUnits: 32500000},
	}
}

// BuildUnifiedNetWorthView merges platformHoldings (real, caller-supplied
// data pulled live from this platform's own services) with
// MockedExternalInstitutionHoldingsSource's fixture data into one
// real, computed net-worth total.
func BuildUnifiedNetWorthView(accountIdentifier string, platformHoldings []PlatformHolding) UnifiedNetWorthView {
	externalHoldings := MockedExternalInstitutionHoldingsSource(accountIdentifier)

	var totalPlatformValue, totalExternalValue int64
	for _, holding := range platformHoldings {
		totalPlatformValue += holding.CurrentValueInMinorUnits
	}
	for _, holding := range externalHoldings {
		totalExternalValue += holding.CurrentValueInMinorUnits
	}

	return UnifiedNetWorthView{
		AccountIdentifier:               accountIdentifier,
		PlatformHoldings:                platformHoldings,
		ExternalHoldings:                externalHoldings,
		TotalPlatformValueInMinorUnits:  totalPlatformValue,
		TotalExternalValueInMinorUnits:  totalExternalValue,
		TotalNetWorthInMinorUnits:       totalPlatformValue + totalExternalValue,
		IsExternalDataFromRealAaNetwork: false,
	}
}
