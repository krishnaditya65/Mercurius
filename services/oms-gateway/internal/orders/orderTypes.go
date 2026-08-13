// Package orders holds the OMS-facing order domain types.
//
// TODO(real build): per ARCHITECTURE.md §3.5, orders crossing into the
// matching engine must eventually be translated into a fixed-layout
// binary encoding (SBE), not JSON. JSON is fine here at the client-facing
// edge (Tier 1). It is currently ALSO used past the OMS -> matching-engine
// boundary (see internal/matchingengineclient) — a known, explicitly
// documented violation of this rule, acceptable only because that bridge
// is itself a placeholder for the real ring-buffer hand-off, not because
// the rule stopped applying.
package orders

// OrderSubmissionRequest is what a client (web/mobile/terminal/algo) sends
// to place an order. Field names are deliberately long and descriptive
// per project convention — see the mercurius-naming-convention note.
type OrderSubmissionRequest struct {
	ClientAccountIdentifier string `json:"clientAccountIdentifier"`
	InstrumentSymbol        string `json:"instrumentSymbol"`
	OrderSideIsBuyNotSell   bool   `json:"orderSideIsBuyNotSell"`

	// OrderIsMarketOrderNotLimit: FEATURES.md §3's "Order entry: Market,
	// Limit, SL, SL-M" — all four are real as of this build.
	OrderIsMarketOrderNotLimit bool `json:"orderIsMarketOrderNotLimit"`

	// OrderIsStopLossVariant, combined with OrderIsMarketOrderNotLimit,
	// selects one of the matching engine's four OrderTypes: (false,
	// false)=Limit, (false, true)=Market, (true, false)=StopLossLimit,
	// (true, true)=StopLossMarket. A stop order doesn't match
	// immediately — it arms and waits for the instrument's last traded
	// price to cross StopTriggerPriceInMinorUnits.
	OrderIsStopLossVariant bool `json:"orderIsStopLossVariant"`

	// StopTriggerPriceInMinorUnits is required (non-nil, non-zero in
	// practice) when OrderIsStopLossVariant is true; ignored otherwise.
	// Pointer so "not a stop order" (nil) is distinguishable from "stop
	// order triggers at price 0" (which would be nonsensical but is at
	// least representable) — omitempty keeps non-stop client payloads
	// unchanged from before this field existed.
	StopTriggerPriceInMinorUnits *int64 `json:"stopTriggerPriceInMinorUnits,omitempty"`

	// Ignored by the matching engine when OrderIsMarketOrderNotLimit is
	// true — still required in the JSON payload for now (no omitempty)
	// to keep the client-facing contract simple; a market order can just
	// send 0.
	LimitPriceInMinorUnits int64  `json:"limitPriceInMinorUnits"`
	OrderQuantity          uint64 `json:"orderQuantity"`

	// IdempotencyKey: FEATURES.md §2's "idempotent transactions" — a
	// client-generated unique string (e.g. a UUID minted once per user
	// tap/click, reused verbatim on any retry of that same submission). If
	// set and this exact key was already submitted, the OMS returns the
	// SAME OrderAcknowledgementResponse instead of risk-checking and
	// routing a second, distinct order. Optional: a client that omits it
	// gets no idempotency protection at all — every submission is treated
	// as new, exactly as before this field existed.
	IdempotencyKey string `json:"idempotencyKey,omitempty"`

	// OrderIsAfterMarketOrder: FEATURES.md §3's AMO. If true AND the
	// market is currently closed, this order is queued instead of being
	// risk-checked/routed immediately — it's replayed automatically
	// through the exact same pipeline the moment the market opens (see
	// internal/amoqueue). If the market is already open when an AMO
	// arrives, the flag has no effect — it's processed immediately like
	// any other order, same as a real broker treating an AMO placed
	// during market hours as a regular order.
	OrderIsAfterMarketOrder bool `json:"orderIsAfterMarketOrder,omitempty"`
}

// TradeExecutionSummary mirrors matchingengineclient.TradeExecutionWireEvent
// but is deliberately its own type: the `orders` package is the OMS's
// client-facing domain model and shouldn't couple to the shape of one
// specific downstream client package.
type TradeExecutionSummary struct {
	BuyingClientAccountId     string `json:"buyingClientAccountId"`
	SellingClientAccountId    string `json:"sellingClientAccountId"`
	ExecutedPriceInMinorUnits int64  `json:"executedPriceInMinorUnits"`
	ExecutedQuantity          uint64 `json:"executedQuantity"`
}

// OrderAcknowledgementResponse is what the OMS returns synchronously after
// a pre-trade risk decision. Per FEATURES.md §21 ("plain-language order
// rejection reasons"), a rejected order must always carry a human-readable
// explanation, never just a bare error code.
//
// An order can be WasOrderAccepted=true (passed risk + was sequenced) yet
// still have a non-empty MatchingEngineHandoffError: risk acceptance and
// successful hand-off to the matching engine are two different things.
// This is deliberate, not a bug — it reflects that in a real async system
// an order can be validly accepted before it's confirmed matched.
type OrderAcknowledgementResponse struct {
	WasOrderAccepted               bool                    `json:"wasOrderAccepted"`
	AssignedGlobalSequenceNumber   uint64                  `json:"assignedGlobalSequenceNumber,omitempty"`
	HumanReadableRejectionReason   string                  `json:"humanReadableRejectionReason,omitempty"`
	MachineReadableRejectionReason string                  `json:"machineReadableRejectionReason,omitempty"`
	TradeExecutionEvents           []TradeExecutionSummary `json:"tradeExecutionEvents,omitempty"`
	MatchingEngineHandoffError     string                  `json:"matchingEngineHandoffError,omitempty"`

	// MatchingEngineOrderSequenceNumber is matching-engine's own local
	// order id for this order (a DIFFERENT sequence space from
	// AssignedGlobalSequenceNumber, which is the OMS's own acceptance
	// ordering) — pass this to POST /orders/cancel to cancel a resting
	// Limit remainder or a still-armed stop order. Zero/omitted if the
	// hand-off to matching-engine never succeeded.
	MatchingEngineOrderSequenceNumber uint64 `json:"matchingEngineOrderSequenceNumber,omitempty"`

	// IsQueuedAsAfterMarketOrder is true when this response is for an AMO
	// that got QUEUED, not processed — the market was closed, so nothing
	// else in this response (sequence numbers, trade events) is
	// meaningful yet. The real acknowledgement happens later, when the
	// market opens and the queue drains; this skeleton has no push
	// channel to deliver that later result, so a client has to poll
	// GET /orders/status once it has an id — except an AMO has no id
	// until it's actually submitted. Documented gap, not fixed: a real
	// build needs either a webhook/WS push or an "AMO ticket id" a client
	// can poll before submission even happens.
	IsQueuedAsAfterMarketOrder bool `json:"isQueuedAsAfterMarketOrder,omitempty"`
}

// CancelOrderRequest is the client-facing payload for POST /orders/cancel.
type CancelOrderRequest struct {
	InstrumentSymbol                  string `json:"instrumentSymbol"`
	MatchingEngineOrderSequenceNumber uint64 `json:"matchingEngineOrderSequenceNumber"`
}

// CancelOrderResponse is what POST /orders/cancel returns.
type CancelOrderResponse struct {
	WasOrderCancelled bool   `json:"wasOrderCancelled"`
	ErrorMessage      string `json:"errorMessage,omitempty"`
}

// CoverOrderRequest is the client-facing payload for POST
// /orders/cover-submit — FEATURES.md §3's "Cover Orders (CO)": an entry
// order plus one mandatory protective stop-loss leg, submitted together.
// Unlike a Bracket Order, a Cover Order has no target/take-profit leg and
// so needs no one-cancels-other logic between two live orders — there's
// only ever one protective order, and it simply exits the position if
// triggered.
type CoverOrderRequest struct {
	ClientAccountIdentifier    string `json:"clientAccountIdentifier"`
	InstrumentSymbol           string `json:"instrumentSymbol"`
	OrderSideIsBuyNotSell      bool   `json:"orderSideIsBuyNotSell"`
	OrderIsMarketOrderNotLimit bool   `json:"orderIsMarketOrderNotLimit"`
	LimitPriceInMinorUnits     int64  `json:"limitPriceInMinorUnits"`
	OrderQuantity              uint64 `json:"orderQuantity"`

	// StopLossTriggerPriceInMinorUnits arms the protective leg — placed
	// as a StopLossMarket order on the OPPOSITE side of the entry, for
	// whatever quantity of the entry actually filled. Required.
	StopLossTriggerPriceInMinorUnits int64 `json:"stopLossTriggerPriceInMinorUnits"`
}

// CoverOrderResponse is what POST /orders/cover-submit returns: the
// entry leg's own acknowledgement, plus whatever happened with the
// protective leg.
type CoverOrderResponse struct {
	EntryOrderAcknowledgement OrderAcknowledgementResponse `json:"entryOrderAcknowledgement"`

	// ProtectiveStopOrderSequenceNumber is matching-engine's local id for
	// the stop-loss leg, if one was placed. Zero if the entry didn't fill
	// at all (nothing to protect) or if placing the protective leg
	// itself failed — check ProtectiveStopOrderError in that case.
	ProtectiveStopOrderSequenceNumber uint64 `json:"protectiveStopOrderSequenceNumber,omitempty"`

	// ProtectiveStopOrderError is set if the entry filled (so a
	// protective leg SHOULD exist) but placing it failed — this is a
	// real, actionable problem: the client now has an open, unprotected
	// position. Never silently swallowed.
	ProtectiveStopOrderError string `json:"protectiveStopOrderError,omitempty"`
}

// OrderStatusResponse is what GET /orders/status returns — a read-only
// snapshot of what matching-engine currently knows about one order,
// looked up by the MatchingEngineOrderSequenceNumber an earlier
// submission or cover-order response returned.
type OrderStatusResponse struct {
	// OrderStatus is one of "RESTING_LIMIT", "PENDING_STOP", or
	// "NOT_FOUND" — see matching-engine's OrderStatusQueryResult doc
	// comment for exactly what "NOT_FOUND" can mean (never existed,
	// fully filled, cancelled, or triggered-and-then-fully-filled; this
	// skeleton keeps no history to tell those apart).
	OrderStatus string `json:"orderStatus"`

	OrderSideIsBuyNotSell *bool `json:"orderSideIsBuyNotSell,omitempty"`
	// PriceInMinorUnits is the resting limit price for RESTING_LIMIT, or
	// the trigger price for PENDING_STOP.
	PriceInMinorUnits *int64 `json:"priceInMinorUnits,omitempty"`
	// Quantity is the remaining quantity for RESTING_LIMIT, or the
	// original armed quantity for PENDING_STOP.
	Quantity *uint64 `json:"quantity,omitempty"`

	ErrorMessage string `json:"errorMessage,omitempty"`
}
