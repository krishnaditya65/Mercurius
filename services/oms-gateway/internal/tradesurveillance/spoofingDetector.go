package tradesurveillance

import (
	"fmt"
	"time"

	"mercurius/omsgateway/internal/audittrail"
)

// SpoofingIncident is one flagged order, with the specific evidence that
// triggered the flag — every field here is computed directly from real
// audittrail.Entry records, never synthesized.
//
// See this package's doc comment for the heuristic-proxy caveat: this
// checks "large order, cancelled shortly after placement, never
// genuinely at risk of executing" — a real regulatory spoofing finding
// additionally requires proving INTENT, which no automated system can do
// from order data alone. This flag is a lead for a human compliance
// officer to investigate, not a finding.
type SpoofingIncident struct {
	InstrumentSymbol                  string    `json:"instrumentSymbol"`
	MatchingEngineOrderSequenceNumber uint64    `json:"matchingEngineOrderSequenceNumber"`
	OrderSideIsBuyNotSell             bool      `json:"orderSideIsBuyNotSell"`
	OrderQuantity                     uint64    `json:"orderQuantity"`
	OrderPriceInMinorUnits            int64     `json:"orderPriceInMinorUnits"`
	RoutedAtTime                      time.Time `json:"routedAtTime"`
	CancelledAtTime                   time.Time `json:"cancelledAtTime"`
	TimeToCancelDuration              string    `json:"timeToCancelDuration"`

	// WasAwayFromTouch and ReferencePriceInMinorUnits explain the
	// away-from-touch leg of the check — ReferencePriceInMinorUnits is
	// nil if no reference price was available at all (see
	// mostRecentFillPriceBefore), in which case WasAwayFromTouch is
	// always false and the flag (if present) rests entirely on
	// WasFasterThanTypicalFillLatency instead.
	WasAwayFromTouch           bool   `json:"wasAwayFromTouch"`
	ReferencePriceInMinorUnits *int64 `json:"referencePriceInMinorUnits,omitempty"`

	// WasFasterThanTypicalFillLatency: the alternate, price-independent
	// leg of the check — see
	// DetectorConfiguration.SpoofingFastCancelBeforeFillLatencyDuration.
	WasFasterThanTypicalFillLatency bool `json:"wasFasterThanTypicalFillLatency"`

	// CorroboratingFollowUpEvidence is a human-readable description of a
	// same-side smaller fill or opposite-side fill found shortly after
	// cancellation, if any — see FEATURES.md's "especially when followed
	// by" language. Empty string if no corroborating follow-up was
	// found; the base flag does not require one.
	CorroboratingFollowUpEvidence string `json:"corroboratingFollowUpEvidence,omitempty"`

	// ReplayEvents is the exact order-book-relevant event sequence for
	// this incident, reconstructed from the audit trail — see
	// replay.go's ReplayOrderSequenceNumber.
	ReplayEvents []audittrail.Entry `json:"replayEvents"`
}

// DetectSpoofing implements FEATURES.md §1's spoofing heuristic. It flags
// a routed order when ALL of:
//
//  1. It is a LIMIT order (a market order cannot spoof — it is
//     marketable by definition, never resting away from the touch).
//  2. OrderQuantity >= configuration.SpoofingLargeOrderQuantityThreshold.
//  3. It was cancelled, and the cancellation happened within
//     configuration.SpoofingCancelWithinDuration of being routed.
//  4. It was "never at material risk of execution", satisfied by EITHER:
//     a) it was priced away from the touch (see mostRecentFillPriceBefore
//     and configuration.SpoofingAwayFromTouchFractionThreshold), OR
//     b) it was cancelled faster than
//     configuration.SpoofingFastCancelBeforeFillLatencyDuration —
//     faster than any realistic human/algo decision loop, used ONLY
//     when (a) cannot be evaluated (no reference price available) or
//     as an additional independent signal.
//
// A same-side smaller follow-up fill or an opposite-side fill within
// configuration.SpoofingFollowUpWindowDuration after cancellation is
// recorded as CorroboratingFollowUpEvidence but is NEVER required to
// trigger the flag — see FEATURES.md's own "especially when" phrasing.
func DetectSpoofing(entries []audittrail.Entry, configuration DetectorConfiguration) []SpoofingIncident {
	routedOrders := buildRoutedOrders(entries)

	var incidents []SpoofingIncident
	for _, order := range routedOrders {
		if order.IsMarketOrder {
			continue
		}
		if order.Quantity < configuration.SpoofingLargeOrderQuantityThreshold {
			continue
		}
		if !order.WasCancelled {
			continue
		}
		timeToCancel := order.CancelledAtTime.Sub(order.RoutedAtTime)
		if timeToCancel < 0 || timeToCancel > configuration.SpoofingCancelWithinDuration {
			continue
		}

		referencePrice, referencePriceFound := mostRecentFillPriceBefore(entries, order.InstrumentSymbol, order.RoutedAtTime)
		wasAwayFromTouch := false
		if referencePriceFound && referencePrice > 0 {
			threshold := configuration.SpoofingAwayFromTouchFractionThreshold
			referencePriceFloat := float64(referencePrice)
			orderPriceFloat := float64(order.PriceInMinorUnits)
			if order.OrderSideIsBuy {
				wasAwayFromTouch = orderPriceFloat < referencePriceFloat*(1-threshold)
			} else {
				wasAwayFromTouch = orderPriceFloat > referencePriceFloat*(1+threshold)
			}
		}

		wasFasterThanTypicalFillLatency := timeToCancel <= configuration.SpoofingFastCancelBeforeFillLatencyDuration

		if !wasAwayFromTouch && !wasFasterThanTypicalFillLatency {
			continue
		}

		incident := SpoofingIncident{
			InstrumentSymbol:                  order.InstrumentSymbol,
			MatchingEngineOrderSequenceNumber: order.SequenceNumber,
			OrderSideIsBuyNotSell:             order.OrderSideIsBuy,
			OrderQuantity:                     order.Quantity,
			OrderPriceInMinorUnits:            order.PriceInMinorUnits,
			RoutedAtTime:                      order.RoutedAtTime,
			CancelledAtTime:                   order.CancelledAtTime,
			TimeToCancelDuration:              timeToCancel.String(),
			WasAwayFromTouch:                  wasAwayFromTouch,
			WasFasterThanTypicalFillLatency:   wasFasterThanTypicalFillLatency,
			ReplayEvents:                      ReplayOrderSequenceNumber(entries, order.InstrumentSymbol, order.SequenceNumber),
		}
		if referencePriceFound {
			referencePriceCopy := referencePrice
			incident.ReferencePriceInMinorUnits = &referencePriceCopy
		}
		incident.CorroboratingFollowUpEvidence = findSpoofingCorroboration(entries, order, configuration.SpoofingFollowUpWindowDuration)

		incidents = append(incidents, incident)
	}

	return incidents
}

// findSpoofingCorroboration looks, within followUpWindow after
// cancelledOrder's cancellation, for either a same-side smaller filled
// order or an opposite-side fill by the same account — FEATURES.md's
// "especially when followed by" corroborating pattern. Returns a
// human-readable description of whichever it finds first
// (chronologically), or "" if neither is found.
func findSpoofingCorroboration(entries []audittrail.Entry, cancelledOrder *routedOrder, followUpWindow time.Duration) string {
	windowEnd := cancelledOrder.CancelledAtTime.Add(followUpWindow)

	for _, entry := range entries {
		if entry.EventType != audittrail.EventOrderFilled {
			continue
		}
		if entry.InstrumentSymbol != cancelledOrder.InstrumentSymbol {
			continue
		}
		if entry.RecordedAtTime.Before(cancelledOrder.CancelledAtTime) || entry.RecordedAtTime.After(windowEnd) {
			continue
		}

		accountWasBuyer := entry.BuyingClientAccountIdentifier == entry.ClientAccountIdentifier
		if accountWasBuyer == cancelledOrder.OrderSideIsBuy && entry.ExecutedQuantity < cancelledOrder.Quantity {
			return fmt.Sprintf(
				"same-side smaller fill of %d @ %d recorded at %s, %s after the flagged order's cancellation",
				entry.ExecutedQuantity, entry.ExecutedPriceInMinorUnits, entry.RecordedAtTime.Format(time.RFC3339Nano),
				entry.RecordedAtTime.Sub(cancelledOrder.CancelledAtTime),
			)
		}
		if accountWasBuyer != cancelledOrder.OrderSideIsBuy {
			return fmt.Sprintf(
				"opposite-side fill of %d @ %d recorded at %s, %s after the flagged order's cancellation",
				entry.ExecutedQuantity, entry.ExecutedPriceInMinorUnits, entry.RecordedAtTime.Format(time.RFC3339Nano),
				entry.RecordedAtTime.Sub(cancelledOrder.CancelledAtTime),
			)
		}
	}

	return ""
}
