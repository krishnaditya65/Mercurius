// Package matchingengineclient is the OMS's real network hand-off to
// matching-engine, replacing the log-only placeholder from the initial
// scaffold.
//
// TODO(real build): this is a synchronous TCP+JSON round-trip per order —
// a pragmatic bridge to prove the service boundary end-to-end, NOT the
// lock-free ring-buffer / SBE binary hand-off described in
// ARCHITECTURE.md §3.1/§3.5. JSON field names are deliberately identical
// to matching-engine's `wireProtocol.rs` types so the wire contract reads
// the same on both sides.
package matchingengineclient

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

type OrderSubmissionWireRequest struct {
	ClientAccountIdentifier    string `json:"clientAccountIdentifier"`
	InstrumentSymbol           string `json:"instrumentSymbol"`
	OrderSideIsBuyNotSell      bool   `json:"orderSideIsBuyNotSell"`
	OrderIsMarketOrderNotLimit bool   `json:"orderIsMarketOrderNotLimit"`

	// OrderIsStopLossVariant + StopTriggerPriceInMinorUnits mirror
	// matching-engine's wireProtocol.rs IncomingOrderWireRequest
	// field-for-field — see the naming note at the top of this file.
	OrderIsStopLossVariant       bool   `json:"orderIsStopLossVariant,omitempty"`
	StopTriggerPriceInMinorUnits *int64 `json:"stopTriggerPriceInMinorUnits,omitempty"`

	LimitPriceInMinorUnits int64  `json:"limitPriceInMinorUnits"`
	OrderQuantity          uint64 `json:"orderQuantity"`
}

// CancelOrderWireRequest mirrors matching-engine's IncomingOrderWireRequest
// shape closely enough to reuse the same JSON line format: setting
// CancelOrderSequenceNumber (and only that plus InstrumentSymbol) is what
// makes matching-engine treat a line as a cancel instead of a submission —
// see that side's `cancelOrderSequenceNumber` doc comment.
type CancelOrderWireRequest struct {
	InstrumentSymbol          string `json:"instrumentSymbol"`
	CancelOrderSequenceNumber uint64 `json:"cancelOrderSequenceNumber"`

	// clientAccountIdentifier/orderSideIsBuyNotSell/limitPriceInMinorUnits/
	// orderQuantity are required by matching-engine's wire struct
	// (non-Option fields) even though they're ignored for a cancel line —
	// zero values are fine, matching-engine never reads them in this path.
	ClientAccountIdentifier string `json:"clientAccountIdentifier"`
	OrderSideIsBuyNotSell   bool   `json:"orderSideIsBuyNotSell"`
	LimitPriceInMinorUnits  int64  `json:"limitPriceInMinorUnits"`
	OrderQuantity           uint64 `json:"orderQuantity"`
}

// QueryOrderStatusWireRequest mirrors matching-engine's
// IncomingOrderWireRequest shape closely enough to reuse the same JSON
// line format: setting QueryOrderStatusSequenceNumber (and only that plus
// InstrumentSymbol) is what makes matching-engine treat a line as a
// read-only status query instead of a submission or cancel.
type QueryOrderStatusWireRequest struct {
	InstrumentSymbol               string `json:"instrumentSymbol"`
	QueryOrderStatusSequenceNumber uint64 `json:"queryOrderStatusSequenceNumber"`

	ClientAccountIdentifier string `json:"clientAccountIdentifier"`
	OrderSideIsBuyNotSell   bool   `json:"orderSideIsBuyNotSell"`
	LimitPriceInMinorUnits  int64  `json:"limitPriceInMinorUnits"`
	OrderQuantity           uint64 `json:"orderQuantity"`
}

type TradeExecutionWireEvent struct {
	BuyingClientAccountId     string `json:"buyingClientAccountId"`
	SellingClientAccountId    string `json:"sellingClientAccountId"`
	ExecutedPriceInMinorUnits int64  `json:"executedPriceInMinorUnits"`
	ExecutedQuantity          uint64 `json:"executedQuantity"`
}

type OrderSubmissionWireResponse struct {
	TradeExecutionEvents []TradeExecutionWireEvent `json:"tradeExecutionEvents"`
	ErrorMessage         *string                   `json:"errorMessage"`

	// AssignedOrderSequenceNumber is set on a successful order submission
	// response — hold onto it to cancel this order later via
	// CancelOrderAndAwaitResult. Unset on cancel responses.
	AssignedOrderSequenceNumber *uint64 `json:"assignedOrderSequenceNumber,omitempty"`

	// WasOrderCancelled is set only on responses to a cancel request.
	WasOrderCancelled *bool `json:"wasOrderCancelled,omitempty"`

	// OrderStatus* fields are set only on responses to a status query —
	// OrderStatus is one of "RESTING_LIMIT", "PENDING_STOP", or
	// "NOT_FOUND"; the rest are only meaningful for the first two.
	OrderStatus                  *string `json:"orderStatus,omitempty"`
	OrderStatusSideIsBuyNotSell  *bool   `json:"orderStatusSideIsBuyNotSell,omitempty"`
	OrderStatusPriceInMinorUnits *int64  `json:"orderStatusPriceInMinorUnits,omitempty"`
	OrderStatusQuantity          *uint64 `json:"orderStatusQuantity,omitempty"`
}

// MatchingEngineClient dials a fresh TCP connection per order — no
// connection pooling yet (another TODO for the real build, once this
// isn't a one-line-request/one-line-response bridge).
type MatchingEngineClient struct {
	matchingEngineTcpAddress string
	dialTimeout              time.Duration
	readTimeout              time.Duration
}

func NewMatchingEngineClient(matchingEngineTcpAddress string) *MatchingEngineClient {
	return &MatchingEngineClient{
		matchingEngineTcpAddress: matchingEngineTcpAddress,
		dialTimeout:              2 * time.Second,
		readTimeout:              2 * time.Second,
	}
}

// SubmitOrderAndAwaitMatchResult opens a connection, writes one JSON line,
// reads one JSON line back, and closes the connection. Returns an error
// if the matching engine is unreachable, times out, or returns malformed
// JSON — it does NOT return an error for a business-level rejection
// (unknown instrument etc.); that comes back as a populated ErrorMessage
// on a successfully-parsed response.
func (client *MatchingEngineClient) SubmitOrderAndAwaitMatchResult(
	wireRequest OrderSubmissionWireRequest,
) (*OrderSubmissionWireResponse, error) {
	return client.sendOneLineAndAwaitResponse(wireRequest)
}

// CancelOrderAndAwaitResult sends a cancel instruction for a previously
// submitted order (identified by the id matching-engine returned in that
// order's AssignedOrderSequenceNumber). Same transport-vs-business-error
// split as SubmitOrderAndAwaitMatchResult: a Go error only for
// unreachable/timeout/malformed-response; "no such order" comes back as
// WasOrderCancelled=false on a successfully-parsed response.
func (client *MatchingEngineClient) CancelOrderAndAwaitResult(
	instrumentSymbol string,
	orderSequenceNumberToCancel uint64,
) (*OrderSubmissionWireResponse, error) {
	return client.sendOneLineAndAwaitResponse(CancelOrderWireRequest{
		InstrumentSymbol:          instrumentSymbol,
		CancelOrderSequenceNumber: orderSequenceNumberToCancel,
	})
}

// QueryOrderStatusAndAwaitResult asks matching-engine what's currently
// happening with a previously submitted order — still resting, still
// armed as a pending stop, or gone (filled/cancelled/triggered-and-
// filled — this skeleton can't tell those apart, see the wire response's
// own doc comment). Read-only, no side effects; same
// transport-error-only Go-error split as the other two calls.
func (client *MatchingEngineClient) QueryOrderStatusAndAwaitResult(
	instrumentSymbol string,
	orderSequenceNumberToQuery uint64,
) (*OrderSubmissionWireResponse, error) {
	return client.sendOneLineAndAwaitResponse(QueryOrderStatusWireRequest{
		InstrumentSymbol:               instrumentSymbol,
		QueryOrderStatusSequenceNumber: orderSequenceNumberToQuery,
	})
}

// sendOneLineAndAwaitResponse is the shared TCP+JSON round-trip
// SubmitOrderAndAwaitMatchResult, CancelOrderAndAwaitResult, and
// QueryOrderStatusAndAwaitResult all use: dial, write one JSON line, read
// one JSON line back, close. `wireRequest` can be any of
// OrderSubmissionWireRequest / CancelOrderWireRequest /
// QueryOrderStatusWireRequest — all three marshal to a shape
// matching-engine's single wire format understands.
func (client *MatchingEngineClient) sendOneLineAndAwaitResponse(
	wireRequest any,
) (*OrderSubmissionWireResponse, error) {
	connection, dialError := net.DialTimeout("tcp", client.matchingEngineTcpAddress, client.dialTimeout)
	if dialError != nil {
		return nil, fmt.Errorf("could not reach matching-engine at %s: %w", client.matchingEngineTcpAddress, dialError)
	}
	defer connection.Close()

	_ = connection.SetDeadline(time.Now().Add(client.readTimeout))

	requestBytes, marshalError := json.Marshal(wireRequest)
	if marshalError != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", marshalError)
	}
	if _, writeError := connection.Write(append(requestBytes, '\n')); writeError != nil {
		return nil, fmt.Errorf("failed to write request to matching-engine: %w", writeError)
	}

	responseLine, readError := bufio.NewReader(connection).ReadBytes('\n')
	if readError != nil {
		return nil, fmt.Errorf("no response from matching-engine: %w", readError)
	}

	var wireResponse OrderSubmissionWireResponse
	if unmarshalError := json.Unmarshal(responseLine, &wireResponse); unmarshalError != nil {
		return nil, fmt.Errorf("malformed response from matching-engine: %w", unmarshalError)
	}
	return &wireResponse, nil
}
