// Package aisreconciliation implements FEATURES.md §1's "Annual
// Information Statement reconciliation" — real, genuine reconciliation
// logic between two structured tax-summary records for one account +
// financial year: the platform's own REAL computed summary (from
// capitalgains' FIFO STCG/LTCG output and oms-gateway's real dividend
// credits) and a second record shaped like India's real government AIS
// export.
//
// HONEST, LOUD LIMITATION: there is no real government AIS data feed
// available to this build (no such external integration exists or could
// exist without real government API credentials this project doesn't
// have). BuildIllustrativeMockAisRecord constructs a SIMULATED
// AIS-shaped record for demonstration/testing purposes only — it is
// NEVER a real government data source, and every field it returns is
// clearly documented as illustrative. What IS real and fully tested
// here is Reconcile itself: given ANY two AisRecord-shaped inputs
// (a real platform summary and any second source, mock or eventually
// real), it performs genuine field-by-field matching and reports real
// discrepancies — amount mismatches and entries missing on either side.
package aisreconciliation

import (
	"fmt"
	"sort"

	"mercurius/reporting/internal/capitalgains"
)

// AisEntry is one line of an AIS-shaped tax-relevant record: a category
// (e.g. "STCG", "LTCG", "DIVIDEND") for one instrument, and the amount
// reported for it.
type AisEntry struct {
	Category           string `json:"category"`
	InstrumentSymbol   string `json:"instrumentSymbol"`
	AmountInMinorUnits int64  `json:"amountInMinorUnits"`
	Description        string `json:"description"`
}

// AisRecord is a full AIS-shaped record for one account + financial
// year — either the platform's own real computed summary, or a
// (real or, currently, illustrative-mock) counterparty record to
// reconcile it against.
type AisRecord struct {
	AccountIdentifier string     `json:"accountIdentifier"`
	FinancialYear     string     `json:"financialYear"`
	Source            string     `json:"source"` // e.g. "MERCURIUS_PLATFORM_COMPUTED" or "MOCK_SIMULATED_AIS_EXPORT"
	Entries           []AisEntry `json:"entries"`
}

const (
	CategoryShortTermCapitalGains = "STCG"
	CategoryLongTermCapitalGains  = "LTCG"
	CategoryDividendIncome        = "DIVIDEND"
)

// BuildPlatformAisRecord constructs the REAL platform-side AIS-shaped
// summary: one entry per instrument with nonzero net STCG for the FY,
// one per instrument with nonzero net LTCG, and one per instrument with
// dividend income, all computed from real capitalgains.RealizedGain
// data and real dividend-credit totals (in minor units, keyed by
// instrument) — no fabricated figures.
func BuildPlatformAisRecord(
	accountIdentifier string,
	financialYearLabel string,
	realizedGainsInFinancialYear []capitalgains.RealizedGain,
	dividendIncomeInMinorUnitsByInstrument map[string]int64,
) AisRecord {
	stcgByInstrument := map[string]int64{}
	ltcgByInstrument := map[string]int64{}
	for _, gain := range realizedGainsInFinancialYear {
		switch gain.GainType {
		case capitalgains.GainTypeShortTerm:
			stcgByInstrument[gain.InstrumentSymbol] += gain.RealizedGainInMinorUnits
		case capitalgains.GainTypeLongTerm:
			ltcgByInstrument[gain.InstrumentSymbol] += gain.RealizedGainInMinorUnits
		}
	}

	record := AisRecord{
		AccountIdentifier: accountIdentifier,
		FinancialYear:     financialYearLabel,
		Source:            "MERCURIUS_PLATFORM_COMPUTED",
	}
	for _, instrument := range sortedKeys(stcgByInstrument) {
		record.Entries = append(record.Entries, AisEntry{
			Category:           CategoryShortTermCapitalGains,
			InstrumentSymbol:   instrument,
			AmountInMinorUnits: stcgByInstrument[instrument],
			Description:        fmt.Sprintf("Realized short-term capital gain/loss, %s, FY%s (FIFO)", instrument, financialYearLabel),
		})
	}
	for _, instrument := range sortedKeys(ltcgByInstrument) {
		record.Entries = append(record.Entries, AisEntry{
			Category:           CategoryLongTermCapitalGains,
			InstrumentSymbol:   instrument,
			AmountInMinorUnits: ltcgByInstrument[instrument],
			Description:        fmt.Sprintf("Realized long-term capital gain/loss, %s, FY%s (FIFO)", instrument, financialYearLabel),
		})
	}
	for _, instrument := range sortedKeys(dividendIncomeInMinorUnitsByInstrument) {
		amount := dividendIncomeInMinorUnitsByInstrument[instrument]
		if amount == 0 {
			continue
		}
		record.Entries = append(record.Entries, AisEntry{
			Category:           CategoryDividendIncome,
			InstrumentSymbol:   instrument,
			AmountInMinorUnits: amount,
			Description:        fmt.Sprintf("Dividend income, %s, FY%s", instrument, financialYearLabel),
		})
	}
	return record
}

// BuildIllustrativeMockAisRecord constructs a SIMULATED, illustrative
// AIS-shaped record for demonstration/testing of Reconcile — NOT a real
// government data feed (see package doc). It starts from a deep copy of
// the real platform record and then applies caller-supplied,
// deliberately-labeled synthetic discrepancies, so the reconciliation
// output has genuine mismatches/missing-entries to report rather than
// trivially matching itself.
func BuildIllustrativeMockAisRecord(platformRecord AisRecord, discrepancies ...MockDiscrepancy) AisRecord {
	mock := AisRecord{
		AccountIdentifier: platformRecord.AccountIdentifier,
		FinancialYear:     platformRecord.FinancialYear,
		Source:            "MOCK_SIMULATED_AIS_EXPORT (illustrative only — not a real government feed)",
		Entries:           append([]AisEntry(nil), platformRecord.Entries...),
	}
	for _, discrepancy := range discrepancies {
		mock = discrepancy.apply(mock)
	}
	return mock
}

// MockDiscrepancy is one deliberate, documented way
// BuildIllustrativeMockAisRecord's output can be made to differ from the
// real platform record, for exercising Reconcile.
type MockDiscrepancy struct {
	// DropCategoryAndInstrument, if non-empty, removes the matching entry
	// entirely — simulating a transaction the (mock) AIS source is
	// missing.
	DropCategoryAndInstrument [2]string
	// PerturbCategoryAndInstrument, if non-empty, is matched the same way
	// and has PerturbByMinorUnits added to its amount — simulating a
	// reported-amount mismatch.
	PerturbCategoryAndInstrument [2]string
	PerturbByMinorUnits          int64
	// AddExtraEntry, if non-nil, is appended verbatim — simulating a
	// transaction the (mock) AIS source has that the platform doesn't.
	AddExtraEntry *AisEntry
}

func (discrepancy MockDiscrepancy) apply(record AisRecord) AisRecord {
	if discrepancy.DropCategoryAndInstrument != [2]string{} {
		var kept []AisEntry
		for _, entry := range record.Entries {
			if entry.Category == discrepancy.DropCategoryAndInstrument[0] && entry.InstrumentSymbol == discrepancy.DropCategoryAndInstrument[1] {
				continue
			}
			kept = append(kept, entry)
		}
		record.Entries = kept
	}
	if discrepancy.PerturbCategoryAndInstrument != [2]string{} {
		for i := range record.Entries {
			if record.Entries[i].Category == discrepancy.PerturbCategoryAndInstrument[0] && record.Entries[i].InstrumentSymbol == discrepancy.PerturbCategoryAndInstrument[1] {
				record.Entries[i].AmountInMinorUnits += discrepancy.PerturbByMinorUnits
			}
		}
	}
	if discrepancy.AddExtraEntry != nil {
		record.Entries = append(record.Entries, *discrepancy.AddExtraEntry)
	}
	return record
}

const (
	DiscrepancyTypeAmountMismatch    = "AMOUNT_MISMATCH"
	DiscrepancyTypeMissingInAis      = "MISSING_IN_AIS"
	DiscrepancyTypeMissingInPlatform = "MISSING_IN_PLATFORM"
)

// Discrepancy is one real finding from Reconcile.
type Discrepancy struct {
	Type                       string `json:"type"`
	Category                   string `json:"category"`
	InstrumentSymbol           string `json:"instrumentSymbol"`
	PlatformAmountInMinorUnits int64  `json:"platformAmountInMinorUnits,omitempty"`
	AisAmountInMinorUnits      int64  `json:"aisAmountInMinorUnits,omitempty"`
	DeltaInMinorUnits          int64  `json:"deltaInMinorUnits,omitempty"`
}

// ReconciliationReport is Reconcile's full output.
type ReconciliationReport struct {
	AccountIdentifier string        `json:"accountIdentifier"`
	FinancialYear     string        `json:"financialYear"`
	PlatformSource    string        `json:"platformSource"`
	AisSource         string        `json:"aisSource"`
	IsFullyReconciled bool          `json:"isFullyReconciled"`
	Discrepancies     []Discrepancy `json:"discrepancies"`
}

type entryKey struct {
	category         string
	instrumentSymbol string
}

// Reconcile is the REAL reconciliation logic: given any two AisRecord
// inputs (regardless of where each came from), it matches entries by
// (category, instrument), flags amount mismatches, and flags entries
// present on only one side. This function has no knowledge of which
// input is "real" or "mock" — it is generic, deterministic comparison
// logic.
func Reconcile(platformRecord AisRecord, aisRecord AisRecord) ReconciliationReport {
	platformByKey := map[entryKey]AisEntry{}
	for _, entry := range platformRecord.Entries {
		platformByKey[entryKey{entry.Category, entry.InstrumentSymbol}] = entry
	}
	aisByKey := map[entryKey]AisEntry{}
	for _, entry := range aisRecord.Entries {
		aisByKey[entryKey{entry.Category, entry.InstrumentSymbol}] = entry
	}

	report := ReconciliationReport{
		AccountIdentifier: platformRecord.AccountIdentifier,
		FinancialYear:     platformRecord.FinancialYear,
		PlatformSource:    platformRecord.Source,
		AisSource:         aisRecord.Source,
	}

	allKeys := map[entryKey]bool{}
	for key := range platformByKey {
		allKeys[key] = true
	}
	for key := range aisByKey {
		allKeys[key] = true
	}

	var sortedKeysList []entryKey
	for key := range allKeys {
		sortedKeysList = append(sortedKeysList, key)
	}
	sort.Slice(sortedKeysList, func(i, j int) bool {
		if sortedKeysList[i].category != sortedKeysList[j].category {
			return sortedKeysList[i].category < sortedKeysList[j].category
		}
		return sortedKeysList[i].instrumentSymbol < sortedKeysList[j].instrumentSymbol
	})

	for _, key := range sortedKeysList {
		platformEntry, hasPlatform := platformByKey[key]
		aisEntry, hasAis := aisByKey[key]

		switch {
		case hasPlatform && !hasAis:
			report.Discrepancies = append(report.Discrepancies, Discrepancy{
				Type:                       DiscrepancyTypeMissingInAis,
				Category:                   key.category,
				InstrumentSymbol:           key.instrumentSymbol,
				PlatformAmountInMinorUnits: platformEntry.AmountInMinorUnits,
			})
		case !hasPlatform && hasAis:
			report.Discrepancies = append(report.Discrepancies, Discrepancy{
				Type:                  DiscrepancyTypeMissingInPlatform,
				Category:              key.category,
				InstrumentSymbol:      key.instrumentSymbol,
				AisAmountInMinorUnits: aisEntry.AmountInMinorUnits,
			})
		case platformEntry.AmountInMinorUnits != aisEntry.AmountInMinorUnits:
			report.Discrepancies = append(report.Discrepancies, Discrepancy{
				Type:                       DiscrepancyTypeAmountMismatch,
				Category:                   key.category,
				InstrumentSymbol:           key.instrumentSymbol,
				PlatformAmountInMinorUnits: platformEntry.AmountInMinorUnits,
				AisAmountInMinorUnits:      aisEntry.AmountInMinorUnits,
				DeltaInMinorUnits:          platformEntry.AmountInMinorUnits - aisEntry.AmountInMinorUnits,
			})
		}
	}

	report.IsFullyReconciled = len(report.Discrepancies) == 0
	return report
}

func sortedKeys(m map[string]int64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
