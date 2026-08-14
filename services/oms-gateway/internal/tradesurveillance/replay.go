// replay.go is FEATURES.md §1's "replay tooling tied to Tick-to-Trade
// Analytics" half of the surveillance feature.
//
// HONEST SCOPE STATEMENT: this codebase has NO existing tick-to-trade
// latency/analytics concept to wire into. A repo-wide search (oms-gateway,
// matching-engine, quant-engine) for "tickToTrade" and "latency" turns up
// only internal/metrics' generic HTTP request-latency histograms — a
// request-timing tool for oms-gateway's own HTTP handlers, unrelated to
// market-data-tick-to-order-ack latency for a specific instrument/order.
// There is nothing to reuse or reference.
//
// So what's built here is a minimal, REAL replay function: given a
// flagged incident's order sequence number(s), it reconstructs the exact
// chronological sequence of audit-trail events relevant to that order
// (submitted -> routed -> filled/cancelled) from the real audit trail —
// genuinely useful for a compliance officer to see exactly what happened,
// in order, for the flagged order(s).
//
// What this does NOT do, and would need real matching-engine-side
// instrumentation to add: correlate that sequence against actual
// tick-to-trade LATENCY (the wall-clock gap between a market data tick
// arriving and this order's ack/fill in response to it) — oms-gateway's
// audit trail records WHEN the OMS itself observed each event, not when
// matching-engine received/processed the wire message, and there is no
// market-data tick feed recorded anywhere in this audit trail at all.
// Building that correlation is future matching-engine-side work this
// pass does not include, stated here loudly rather than faked.
package tradesurveillance

import (
	"sort"

	"mercurius/omsgateway/internal/audittrail"
)

// ReplayOrderSequenceNumber reconstructs the exact chronological sequence
// of audit-trail entries for one order (identified by instrumentSymbol +
// matchingEngineOrderSequenceNumber) — its EventOrderRoutedToMatchingEngine
// entry plus any EventOrderCancelled/EventOrderCancelFailed/EventOrderFilled
// entries that reference the same sequence number, oldest first.
func ReplayOrderSequenceNumber(entries []audittrail.Entry, instrumentSymbol string, matchingEngineOrderSequenceNumber uint64) []audittrail.Entry {
	return ReplayOrderSequenceNumbers(entries, instrumentSymbol, []uint64{matchingEngineOrderSequenceNumber})
}

// ReplayOrderSequenceNumbers is ReplayOrderSequenceNumber generalized to
// several sequence numbers at once (e.g. every order in a flagged
// layering cluster), merged into one chronological sequence — useful so a
// compliance officer sees the whole incident interleaved in the exact
// order it actually happened, not as separate per-order lists they'd have
// to interleave by hand.
func ReplayOrderSequenceNumbers(entries []audittrail.Entry, instrumentSymbol string, matchingEngineOrderSequenceNumbers []uint64) []audittrail.Entry {
	wanted := make(map[uint64]bool, len(matchingEngineOrderSequenceNumbers))
	for _, seq := range matchingEngineOrderSequenceNumbers {
		wanted[seq] = true
	}

	var replay []audittrail.Entry
	for _, entry := range entries {
		if entry.InstrumentSymbol != instrumentSymbol {
			continue
		}
		switch entry.EventType {
		case audittrail.EventOrderRoutedToMatchingEngine, audittrail.EventOrderCancelled,
			audittrail.EventOrderCancelFailed, audittrail.EventOrderFilled:
			if wanted[entry.MatchingEngineOrderSequenceNumber] {
				replay = append(replay, entry)
			}
		}
	}

	sort.SliceStable(replay, func(i, j int) bool {
		return replay[i].RecordedAtTime.Before(replay[j].RecordedAtTime)
	})
	return replay
}
