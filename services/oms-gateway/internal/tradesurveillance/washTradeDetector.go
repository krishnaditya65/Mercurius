package tradesurveillance

import (
	"mercurius/omsgateway/internal/audittrail"
)

// WashTradeIncident is one trade where the same account was on both the
// buy and sell side — an EXACT check, not a heuristic. See this
// package's doc comment for why this is scoped to same-account wash
// trades only (no linked-accounts concept exists elsewhere in this
// codebase to extend it to two different accounts under one beneficial
// owner).
type WashTradeIncident struct {
	InstrumentSymbol                  string `json:"instrumentSymbol"`
	MatchingEngineOrderSequenceNumber uint64 `json:"matchingEngineOrderSequenceNumber"`
	ClientAccountIdentifier           string `json:"clientAccountIdentifier"`
	ExecutedPriceInMinorUnits         int64  `json:"executedPriceInMinorUnits"`
	ExecutedQuantity                  uint64 `json:"executedQuantity"`
	RecordedAtTime                    string `json:"recordedAtTime"`

	// ReplayEvents is just this one fill entry (a wash trade is a single
	// atomic event, not a multi-step pattern like spoofing/layering —
	// there is nothing to replay beyond the trade itself), kept as a
	// slice for a consistent shape across all three incident types.
	ReplayEvents []audittrail.Entry `json:"replayEvents"`
}

// DetectWashTrades scans entries for EventOrderFilled records where
// BuyingClientAccountIdentifier equals SellingClientAccountIdentifier and
// both are non-empty — the same account is definitionally on both sides
// of the trade, netting to no genuine change in beneficial ownership.
// This is exact: every match is a real wash trade by definition, no
// threshold or false-positive tuning applies.
func DetectWashTrades(entries []audittrail.Entry) []WashTradeIncident {
	var incidents []WashTradeIncident
	for _, entry := range entries {
		if entry.EventType != audittrail.EventOrderFilled {
			continue
		}
		if entry.BuyingClientAccountIdentifier == "" || entry.SellingClientAccountIdentifier == "" {
			continue
		}
		if entry.BuyingClientAccountIdentifier != entry.SellingClientAccountIdentifier {
			continue
		}

		incidents = append(incidents, WashTradeIncident{
			InstrumentSymbol:                  entry.InstrumentSymbol,
			MatchingEngineOrderSequenceNumber: entry.MatchingEngineOrderSequenceNumber,
			ClientAccountIdentifier:           entry.BuyingClientAccountIdentifier,
			ExecutedPriceInMinorUnits:         entry.ExecutedPriceInMinorUnits,
			ExecutedQuantity:                  entry.ExecutedQuantity,
			RecordedAtTime:                    entry.RecordedAtTime.Format("2006-01-02T15:04:05.000000000Z07:00"),
			ReplayEvents:                      []audittrail.Entry{entry},
		})
	}
	return incidents
}
