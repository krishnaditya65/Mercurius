package tradesurveillance

import (
	"time"

	"mercurius/omsgateway/internal/audittrail"
)

// routedOrder is the reconstructed shape+lifecycle of one real order,
// built by joining its audittrail.EventOrderRoutedToMatchingEngine entry
// (side/quantity/price/instrument, stamped with
// MatchingEngineOrderSequenceNumber once matching-engine assigns it) with
// whatever EventOrderCancelled/EventOrderCancelFailed/EventOrderFilled
// entries later reference that same sequence number. See
// audittrail.EventOrderRoutedToMatchingEngine's doc comment for exactly
// why this join (on MatchingEngineOrderSequenceNumber + InstrumentSymbol)
// is the only reliable way to attribute a cancellation back to the
// account/order-shape that placed it — CancelOrderRequest itself never
// carries ClientAccountIdentifier.
type routedOrder struct {
	SequenceNumber    uint64
	InstrumentSymbol  string
	OrderSideIsBuy    bool
	Quantity          uint64
	PriceInMinorUnits int64
	IsMarketOrder     bool
	RoutedAtTime      time.Time

	WasCancelled    bool
	CancelledAtTime time.Time

	WasFilled    bool
	FilledAtTime time.Time
}

// instrumentSeqKey is the join key: a MatchingEngineOrderSequenceNumber is
// only unique WITHIN one instrument's own sequence space (matching-engine
// assigns sequence numbers per order book), not globally.
type instrumentSeqKey struct {
	instrument string
	seq        uint64
}

// ScopeEntriesToAccount filters allEntries (the FULL audit trail) down to
// the ones relevant to accountIdentifier, correctly including
// EventOrderCancelled/EventOrderCancelFailed entries that belong to one
// of this account's own orders even though those entries carry no
// ClientAccountIdentifier of their own.
//
// Naively filtering on entry.ClientAccountIdentifier == accountIdentifier
// (i.e. audittrail.AuditTrail.EntriesForAccount) silently drops every
// cancellation ever made — a real bug this function exists specifically
// to avoid, found by exercising this package's HTTP endpoint end-to-end
// against a live matching-engine, not just its unit tests (the unit
// tests construct entries by hand and never exercised this filtering
// step at all, which is exactly why this false negative slipped past
// them — see this package's test file for a regression test now covering
// it too).
//
// The join: first find every EventOrderRoutedToMatchingEngine entry
// belonging to accountIdentifier, collecting their
// (InstrumentSymbol, MatchingEngineOrderSequenceNumber) keys — these are
// this account's own orders, definitively. Then return every entry that
// EITHER has entry.ClientAccountIdentifier == accountIdentifier directly
// (covers EventOrderSubmitted, EventOrderRoutedToMatchingEngine,
// EventOrderFilled, EventOrderRejected, etc.) OR is an
// EventOrderCancelled/EventOrderCancelFailed entry whose
// (InstrumentSymbol, MatchingEngineOrderSequenceNumber) matches one of
// those keys.
func ScopeEntriesToAccount(allEntries []audittrail.Entry, accountIdentifier string) []audittrail.Entry {
	ownedKeys := make(map[instrumentSeqKey]bool)
	for _, entry := range allEntries {
		if entry.EventType == audittrail.EventOrderRoutedToMatchingEngine && entry.ClientAccountIdentifier == accountIdentifier {
			ownedKeys[instrumentSeqKey{entry.InstrumentSymbol, entry.MatchingEngineOrderSequenceNumber}] = true
		}
	}

	var scoped []audittrail.Entry
	for _, entry := range allEntries {
		if entry.ClientAccountIdentifier == accountIdentifier {
			scoped = append(scoped, entry)
			continue
		}
		if entry.EventType == audittrail.EventOrderCancelled || entry.EventType == audittrail.EventOrderCancelFailed {
			if ownedKeys[instrumentSeqKey{entry.InstrumentSymbol, entry.MatchingEngineOrderSequenceNumber}] {
				scoped = append(scoped, entry)
			}
		}
	}
	return scoped
}

// buildRoutedOrders reconstructs every real (non-paper) order's
// submit-to-terminal-state lifecycle from a chronologically sorted slice
// of one account's audit entries. Orders with no
// EventOrderRoutedToMatchingEngine entry (rejected before hand-off, or a
// paper-trading order — see that event's own doc comment) are not
// reconstructable this way and are simply absent from the result; that is
// intentional, not a bug — this package only surveils orders that
// genuinely reached the matching engine.
func buildRoutedOrders(entries []audittrail.Entry) []*routedOrder {
	bySeq := make(map[instrumentSeqKey]*routedOrder)
	var ordered []*routedOrder

	for _, entry := range entries {
		switch entry.EventType {
		case audittrail.EventOrderRoutedToMatchingEngine:
			key := instrumentSeqKey{entry.InstrumentSymbol, entry.MatchingEngineOrderSequenceNumber}
			order := &routedOrder{
				SequenceNumber:    entry.MatchingEngineOrderSequenceNumber,
				InstrumentSymbol:  entry.InstrumentSymbol,
				Quantity:          entry.OrderQuantity,
				PriceInMinorUnits: entry.LimitPriceInMinorUnits,
				IsMarketOrder:     entry.OrderIsMarketOrderNotLimit,
				RoutedAtTime:      entry.RecordedAtTime,
			}
			if entry.OrderSideIsBuyNotSell != nil {
				order.OrderSideIsBuy = *entry.OrderSideIsBuyNotSell
			}
			bySeq[key] = order
			ordered = append(ordered, order)

		case audittrail.EventOrderCancelled:
			key := instrumentSeqKey{entry.InstrumentSymbol, entry.MatchingEngineOrderSequenceNumber}
			if order, found := bySeq[key]; found {
				order.WasCancelled = true
				order.CancelledAtTime = entry.RecordedAtTime
			}

		case audittrail.EventOrderFilled:
			key := instrumentSeqKey{entry.InstrumentSymbol, entry.MatchingEngineOrderSequenceNumber}
			if order, found := bySeq[key]; found {
				order.WasFilled = true
				order.FilledAtTime = entry.RecordedAtTime
			}
		}
	}

	return ordered
}

// mostRecentFillPriceBefore returns the ExecutedPriceInMinorUnits of the
// most recent EventOrderFilled entry for instrumentSymbol recorded before
// beforeTime, and whether one was found at all. See this package's doc
// comment for the honest caveat that this is one account's own fill
// history, not a real top-of-book feed.
func mostRecentFillPriceBefore(entries []audittrail.Entry, instrumentSymbol string, beforeTime time.Time) (int64, bool) {
	var found bool
	var mostRecentPrice int64
	var mostRecentTime time.Time

	for _, entry := range entries {
		if entry.EventType != audittrail.EventOrderFilled {
			continue
		}
		if entry.InstrumentSymbol != instrumentSymbol {
			continue
		}
		if !entry.RecordedAtTime.Before(beforeTime) {
			continue
		}
		if !found || entry.RecordedAtTime.After(mostRecentTime) {
			found = true
			mostRecentPrice = entry.ExecutedPriceInMinorUnits
			mostRecentTime = entry.RecordedAtTime
		}
	}
	return mostRecentPrice, found
}
