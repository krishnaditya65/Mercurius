package tradesurveillance

import (
	"time"

	"mercurius/omsgateway/internal/audittrail"
)

// LayerOrderEvidence is one order that was part of a flagged layer.
type LayerOrderEvidence struct {
	MatchingEngineOrderSequenceNumber uint64    `json:"matchingEngineOrderSequenceNumber"`
	PriceInMinorUnits                 int64     `json:"priceInMinorUnits"`
	Quantity                          uint64    `json:"quantity"`
	RoutedAtTime                      time.Time `json:"routedAtTime"`
	WasCancelled                      bool      `json:"wasCancelled"`
	CancelledAtTime                   time.Time `json:"cancelledAtTime,omitempty"`
}

// LayeringIncident is one flagged layer of orders, with the specific
// evidence that triggered the flag. See this package's doc comment for
// the heuristic-proxy caveat — the same "lead, not a finding" caution
// from SpoofingIncident applies here.
type LayeringIncident struct {
	InstrumentSymbol      string               `json:"instrumentSymbol"`
	LayerSideIsBuyNotSell bool                 `json:"layerSideIsBuyNotSell"`
	LayerOrders           []LayerOrderEvidence `json:"layerOrders"`
	DistinctPriceLevels   int                  `json:"distinctPriceLevels"`

	// OppositeSideFillQuantity/Price/Time is the real fill on the
	// opposite side of LayerSideIsBuyNotSell that the layer's
	// cancellation pattern followed.
	OppositeSideFillQuantity          uint64    `json:"oppositeSideFillQuantity"`
	OppositeSideFillPriceInMinorUnits int64     `json:"oppositeSideFillPriceInMinorUnits"`
	OppositeSideFillAtTime            time.Time `json:"oppositeSideFillAtTime"`

	CancelledOrderCount int     `json:"cancelledOrderCount"`
	CancelledFraction   float64 `json:"cancelledFraction"`

	// ReplayEvents is the exact order-book-relevant event sequence for
	// this incident (every layer order's routing/cancellation plus the
	// opposite-side fill), reconstructed from the audit trail.
	ReplayEvents []audittrail.Entry `json:"replayEvents"`
}

// DetectLayering implements FEATURES.md §1's layering heuristic. It
// groups one account's real (non-paper) limit orders by
// (instrument, side), buckets them into non-overlapping clusters where
// consecutive orders are all within configuration.LayeringWindowDuration
// of the cluster's first order, and flags a cluster when ALL of:
//
//  1. It has at least configuration.LayeringMinimumOrderCount orders.
//  2. Those orders sit at at least configuration.LayeringMinimumOrderCount
//     DISTINCT price levels ("successive price levels", not the same
//     price repeated — a real ladder building depth across the book, not
//     just several orders stacked at one level).
//  3. A fill occurs on the OPPOSITE side of the cluster's side, for this
//     account, in the same instrument, at or after the cluster's last
//     order and within a bounded search window.
//  4. At least configuration.LayeringMinimumCancelledFraction of the
//     cluster's orders are cancelled within
//     configuration.LayeringCancelWithinDuration after that opposite-side
//     fill.
//
// Note on clustering: this uses simple non-overlapping consecutive
// windows (a new cluster starts whenever an order falls outside
// LayeringWindowDuration of the current cluster's first order), not an
// exhaustive sliding window over every possible sub-sequence — a
// deliberate simplicity tradeoff, documented here rather than silently
// assumed. It will not find every conceivable overlapping cluster
// combination, but will find the straightforward "burst of orders, then
// a fill, then mass-cancellation" pattern this heuristic targets.
func DetectLayering(entries []audittrail.Entry, configuration DetectorConfiguration) []LayeringIncident {
	routedOrders := buildRoutedOrders(entries)

	type groupKey struct {
		instrument string
		isBuy      bool
	}
	groups := make(map[groupKey][]*routedOrder)
	for _, order := range routedOrders {
		if order.IsMarketOrder {
			continue
		}
		key := groupKey{order.InstrumentSymbol, order.OrderSideIsBuy}
		groups[key] = append(groups[key], order)
	}

	var incidents []LayeringIncident
	for key, groupOrders := range groups {
		clusters := clusterByRoutedTime(groupOrders, configuration.LayeringWindowDuration)
		for _, cluster := range clusters {
			if len(cluster) < configuration.LayeringMinimumOrderCount {
				continue
			}
			distinctPrices := make(map[int64]bool)
			for _, order := range cluster {
				distinctPrices[order.PriceInMinorUnits] = true
			}
			if len(distinctPrices) < configuration.LayeringMinimumOrderCount {
				continue
			}

			clusterLastRoutedTime := cluster[len(cluster)-1].RoutedAtTime
			searchEnd := clusterLastRoutedTime.Add(configuration.LayeringWindowDuration + configuration.LayeringCancelWithinDuration)

			oppositeFill, found := findOppositeSideFillAfter(entries, key.instrument, key.isBuy, clusterLastRoutedTime, searchEnd)
			if !found {
				continue
			}

			cancelWindowEnd := oppositeFill.RecordedAtTime.Add(configuration.LayeringCancelWithinDuration)
			cancelledCount := 0
			for _, order := range cluster {
				if order.WasCancelled &&
					!order.CancelledAtTime.Before(oppositeFill.RecordedAtTime) &&
					!order.CancelledAtTime.After(cancelWindowEnd) {
					cancelledCount++
				}
			}
			cancelledFraction := float64(cancelledCount) / float64(len(cluster))
			if cancelledFraction < configuration.LayeringMinimumCancelledFraction {
				continue
			}

			var layerOrderEvidence []LayerOrderEvidence
			var replaySeqNumbers []uint64
			for _, order := range cluster {
				layerOrderEvidence = append(layerOrderEvidence, LayerOrderEvidence{
					MatchingEngineOrderSequenceNumber: order.SequenceNumber,
					PriceInMinorUnits:                 order.PriceInMinorUnits,
					Quantity:                          order.Quantity,
					RoutedAtTime:                      order.RoutedAtTime,
					WasCancelled:                      order.WasCancelled,
					CancelledAtTime:                   order.CancelledAtTime,
				})
				replaySeqNumbers = append(replaySeqNumbers, order.SequenceNumber)
			}

			incidents = append(incidents, LayeringIncident{
				InstrumentSymbol:                  key.instrument,
				LayerSideIsBuyNotSell:             key.isBuy,
				LayerOrders:                       layerOrderEvidence,
				DistinctPriceLevels:               len(distinctPrices),
				OppositeSideFillQuantity:          oppositeFill.ExecutedQuantity,
				OppositeSideFillPriceInMinorUnits: oppositeFill.ExecutedPriceInMinorUnits,
				OppositeSideFillAtTime:            oppositeFill.RecordedAtTime,
				CancelledOrderCount:               cancelledCount,
				CancelledFraction:                 cancelledFraction,
				ReplayEvents:                      ReplayOrderSequenceNumbers(entries, key.instrument, replaySeqNumbers),
			})
		}
	}

	return incidents
}

// clusterByRoutedTime buckets a chronologically sorted slice of orders
// into non-overlapping clusters, starting a new cluster whenever the next
// order's RoutedAtTime is more than windowDuration after the current
// cluster's first order.
func clusterByRoutedTime(orders []*routedOrder, windowDuration time.Duration) [][]*routedOrder {
	var clusters [][]*routedOrder
	var current []*routedOrder

	for _, order := range orders {
		if len(current) == 0 {
			current = []*routedOrder{order}
			continue
		}
		if order.RoutedAtTime.Sub(current[0].RoutedAtTime) <= windowDuration {
			current = append(current, order)
			continue
		}
		clusters = append(clusters, current)
		current = []*routedOrder{order}
	}
	if len(current) > 0 {
		clusters = append(clusters, current)
	}
	return clusters
}

// findOppositeSideFillAfter finds the first EventOrderFilled entry for
// instrumentSymbol, in [afterTime, beforeTime], where the account was on
// the OPPOSITE side of layerSideIsBuy.
func findOppositeSideFillAfter(
	entries []audittrail.Entry,
	instrumentSymbol string,
	layerSideIsBuy bool,
	afterTime, beforeTime time.Time,
) (audittrail.Entry, bool) {
	for _, entry := range entries {
		if entry.EventType != audittrail.EventOrderFilled {
			continue
		}
		if entry.InstrumentSymbol != instrumentSymbol {
			continue
		}
		if entry.RecordedAtTime.Before(afterTime) || entry.RecordedAtTime.After(beforeTime) {
			continue
		}
		accountWasBuyer := entry.BuyingClientAccountIdentifier == entry.ClientAccountIdentifier
		if accountWasBuyer != layerSideIsBuy {
			return entry, true
		}
	}
	return audittrail.Entry{}, false
}
