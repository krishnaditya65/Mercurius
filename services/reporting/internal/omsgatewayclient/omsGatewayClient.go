// Package omsgatewayclient is reporting's real, read-only HTTP client
// for the oms-gateway service (services/oms-gateway, default
// :8081). reporting never imports oms-gateway's Go code and never
// writes to it — every call here is a plain GET (or, for the charges
// calculator, a POST that mutates nothing server-side) against
// oms-gateway's genuine already-shipped HTTP API, exactly as an external
// caller would use it.
//
// Endpoints used, and why:
//
//   - GET /audit-trail (NO accountId filter) — the ONLY place oms-gateway
//     exposes per-fill history (instrument, quantity, price, buyer,
//     seller, timestamp), via ORDER_FILLED entries' structured
//     BuyingClientAccountIdentifier/SellingClientAccountIdentifier/
//     ExecutedPriceInMinorUnits/ExecutedQuantity fields (added for
//     internal/tradesurveillance) — plus a DetailMessage free-text
//     fallback kept only for defensiveness, see filltrail's package doc.
//     REAL, LOAD-BEARING GAP discovered while building this against a
//     live oms-gateway: GET /audit-trail?accountId=X only returns
//     ORDER_FILLED entries whose ClientAccountIdentifier is the account
//     whose order request happened to be the one that crossed (the
//     "taker") — the resting/"maker" counterparty's fill on the exact
//     same trade is recorded under a DIFFERENT entry's
//     ClientAccountIdentifier and is therefore invisible to that
//     account's own filtered audit-trail query. Verified live: after a
//     real acct-001 BUY crossed against a real acct-002 SELL,
//     GET /audit-trail?accountId=acct-001 (the buyer/maker here) shows
//     NO ORDER_FILLED entry at all, while
//     GET /audit-trail?accountId=acct-002 (the seller/taker) shows it.
//     FetchAllAuditTrailEntries below works around this correctly by
//     fetching the FULL trail (every account) once and filtering
//     client-side by the structured buyer/seller fields — the only way
//     to get a complete, correct fill history per account from
//     oms-gateway's current API.
//
//   - GET /positions?accountId=...    — current net quantity per
//     instrument, used only as a cross-check display, not for gains
//     math (positions.PositionBook itself documents "no cost basis, no
//     realized/unrealized P&L" — capitalgains recomputes cost basis
//     independently from fills, it does not trust this endpoint for it).
//
//   - GET /corporate-actions/processed-actions?accountId=... — real
//     recorded CASH_DIVIDEND corporate-action events (amount actually
//     credited, and when), used for the dividend-income lines of the
//     ledger statement and AIS reconciliation.
//
//   - POST /orders/estimate-charges — oms-gateway's real, already-
//     documented-as-illustrative brokerage/STT/GST/etc rate model
//     (internal/chargescalculator). reporting calls this live endpoint
//     per fill rather than re-implementing the rate table itself, so
//     contract notes always reflect whatever oms-gateway's calculator
//     currently computes.
package omsgatewayclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// AuditTrailEntryWireFormat mirrors oms-gateway's internal/audittrail.Entry
// JSON shape exactly (field names and JSON tags copied from that
// package, which reporting does not import), including the newer
// structured EventOrderFilled fields.
type AuditTrailEntryWireFormat struct {
	RecordedAtTime                    time.Time `json:"recordedAtTime"`
	EventType                         string    `json:"eventType"`
	ClientAccountIdentifier           string    `json:"clientAccountIdentifier,omitempty"`
	InstrumentSymbol                  string    `json:"instrumentSymbol,omitempty"`
	MatchingEngineOrderSequenceNumber uint64    `json:"matchingEngineOrderSequenceNumber,omitempty"`
	DetailMessage                     string    `json:"detailMessage,omitempty"`
	BuyingClientAccountIdentifier     string    `json:"buyingClientAccountIdentifier,omitempty"`
	SellingClientAccountIdentifier    string    `json:"sellingClientAccountIdentifier,omitempty"`
	ExecutedPriceInMinorUnits         int64     `json:"executedPriceInMinorUnits,omitempty"`
	ExecutedQuantity                  uint64    `json:"executedQuantity,omitempty"`
}

// PositionsWireResponse mirrors oms-gateway's GET /positions response.
type PositionsWireResponse struct {
	AccountIdentifier             string           `json:"accountIdentifier"`
	NetQuantityByInstrumentSymbol map[string]int64 `json:"netQuantityByInstrumentSymbol"`
}

// HoldingWireFormat mirrors corporateactionsprocessing.Holding.
type HoldingWireFormat struct {
	ClientAccountIdentifier    string `json:"clientAccountIdentifier"`
	InstrumentSymbol           string `json:"instrumentSymbol"`
	QuantityHeld               int64  `json:"quantityHeld"`
	TotalCostBasisInMinorUnits int64  `json:"totalCostBasisInMinorUnits"`
}

// ProcessedActionWireFormat mirrors corporateactionsprocessing.ProcessedAction.
type ProcessedActionWireFormat struct {
	ActionType               string            `json:"actionType"`
	ClientAccountIdentifier  string            `json:"clientAccountIdentifier"`
	InstrumentSymbol         string            `json:"instrumentSymbol"`
	HoldingBefore            HoldingWireFormat `json:"holdingBefore"`
	HoldingAfter             HoldingWireFormat `json:"holdingAfter"`
	CashCreditedInMinorUnits int64             `json:"cashCreditedInMinorUnits,omitempty"`
	ProcessedAtTime          time.Time         `json:"processedAtTime"`
}

type processedActionsWireResponse struct {
	AccountIdentifier string                      `json:"accountIdentifier"`
	ProcessedActions  []ProcessedActionWireFormat `json:"processedActions"`
}

// ChargesBreakdownWireFormat mirrors chargescalculator.ChargesBreakdown.
type ChargesBreakdownWireFormat struct {
	TurnoverInMinorUnits                    int64 `json:"turnoverInMinorUnits"`
	BrokerageInMinorUnits                   int64 `json:"brokerageInMinorUnits"`
	SecuritiesTransactionTaxInMinorUnits    int64 `json:"securitiesTransactionTaxInMinorUnits"`
	ExchangeTransactionChargeInMinorUnits   int64 `json:"exchangeTransactionChargeInMinorUnits"`
	SebiTurnoverFeeInMinorUnits             int64 `json:"sebiTurnoverFeeInMinorUnits"`
	StampDutyInMinorUnits                   int64 `json:"stampDutyInMinorUnits"`
	GstInMinorUnits                         int64 `json:"gstInMinorUnits"`
	DepositoryParticipantChargeInMinorUnits int64 `json:"depositoryParticipantChargeInMinorUnits"`
	TotalChargesInMinorUnits                int64 `json:"totalChargesInMinorUnits"`
	NetAmountInMinorUnits                   int64 `json:"netAmountInMinorUnits"`
}

type estimateChargesWireRequest struct {
	OrderSideIsBuyNotSell  bool   `json:"orderSideIsBuyNotSell"`
	LimitPriceInMinorUnits int64  `json:"limitPriceInMinorUnits"`
	OrderQuantity          uint64 `json:"orderQuantity"`
	IsIntradayNotDelivery  bool   `json:"isIntradayNotDelivery"`
}

// OmsGatewayClient is reporting's real HTTP client for oms-gateway.
type OmsGatewayClient struct {
	omsGatewayBaseUrl string
	httpClient        *http.Client
	// chargesCalculatorReachable is set false by the first failed call
	// to estimate-charges and never reset — once we know the live
	// calculator is unreachable, EstimateCharges short-circuits to the
	// documented illustrative fallback for every subsequent call rather
	// than retrying and failing on every single fill of a report.
	chargesCalculatorReachable bool
	chargesCalculatorProbed    bool
}

func NewOmsGatewayClient(omsGatewayBaseUrl string) *OmsGatewayClient {
	return &OmsGatewayClient{
		omsGatewayBaseUrl: omsGatewayBaseUrl,
		httpClient:        &http.Client{Timeout: 3 * time.Second},
	}
}

// FetchAuditTrailForAccount returns every audit-trail entry recorded for
// one account, oldest first — real data from oms-gateway's genuine
// running audit trail, not a fabricated fill history.
func (client *OmsGatewayClient) FetchAuditTrailForAccount(accountIdentifier string) ([]AuditTrailEntryWireFormat, error) {
	requestUrl := fmt.Sprintf("%s/audit-trail?accountId=%s", client.omsGatewayBaseUrl, accountIdentifier)

	httpResponse, requestError := client.httpClient.Get(requestUrl)
	if requestError != nil {
		return nil, fmt.Errorf("could not reach oms-gateway at %s: %w", client.omsGatewayBaseUrl, requestError)
	}
	defer httpResponse.Body.Close()

	if httpResponse.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oms-gateway returned HTTP %d for audit-trail of account %s", httpResponse.StatusCode, accountIdentifier)
	}

	var entries []AuditTrailEntryWireFormat
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&entries); decodeError != nil {
		return nil, fmt.Errorf("malformed audit-trail response from oms-gateway: %w", decodeError)
	}
	return entries, nil
}

// FetchAllAuditTrailEntries returns EVERY audit-trail entry oms-gateway
// has recorded, for every account, oldest first — the only way to get a
// complete per-account fill history given the accountId-filter gap
// documented in this package's doc comment above.
func (client *OmsGatewayClient) FetchAllAuditTrailEntries() ([]AuditTrailEntryWireFormat, error) {
	requestUrl := fmt.Sprintf("%s/audit-trail", client.omsGatewayBaseUrl)

	httpResponse, requestError := client.httpClient.Get(requestUrl)
	if requestError != nil {
		return nil, fmt.Errorf("could not reach oms-gateway at %s: %w", client.omsGatewayBaseUrl, requestError)
	}
	defer httpResponse.Body.Close()

	if httpResponse.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oms-gateway returned HTTP %d for the full audit-trail", httpResponse.StatusCode)
	}

	var entries []AuditTrailEntryWireFormat
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&entries); decodeError != nil {
		return nil, fmt.Errorf("malformed audit-trail response from oms-gateway: %w", decodeError)
	}
	return entries, nil
}

// FetchPositionsForAccount returns the account's current real net
// quantity per instrument.
func (client *OmsGatewayClient) FetchPositionsForAccount(accountIdentifier string) (PositionsWireResponse, error) {
	requestUrl := fmt.Sprintf("%s/positions?accountId=%s", client.omsGatewayBaseUrl, accountIdentifier)

	httpResponse, requestError := client.httpClient.Get(requestUrl)
	if requestError != nil {
		return PositionsWireResponse{}, fmt.Errorf("could not reach oms-gateway at %s: %w", client.omsGatewayBaseUrl, requestError)
	}
	defer httpResponse.Body.Close()

	if httpResponse.StatusCode != http.StatusOK {
		return PositionsWireResponse{}, fmt.Errorf("oms-gateway returned HTTP %d for positions of account %s", httpResponse.StatusCode, accountIdentifier)
	}

	var wireResponse PositionsWireResponse
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&wireResponse); decodeError != nil {
		return PositionsWireResponse{}, fmt.Errorf("malformed positions response from oms-gateway: %w", decodeError)
	}
	return wireResponse, nil
}

// FetchProcessedCorporateActionsForAccount returns every real recorded
// corporate action (including CASH_DIVIDEND credits) for one account.
func (client *OmsGatewayClient) FetchProcessedCorporateActionsForAccount(accountIdentifier string) ([]ProcessedActionWireFormat, error) {
	requestUrl := fmt.Sprintf("%s/corporate-actions/processed-actions?accountId=%s", client.omsGatewayBaseUrl, accountIdentifier)

	httpResponse, requestError := client.httpClient.Get(requestUrl)
	if requestError != nil {
		return nil, fmt.Errorf("could not reach oms-gateway at %s: %w", client.omsGatewayBaseUrl, requestError)
	}
	defer httpResponse.Body.Close()

	if httpResponse.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oms-gateway returned HTTP %d for processed-actions of account %s", httpResponse.StatusCode, accountIdentifier)
	}

	var wireResponse processedActionsWireResponse
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&wireResponse); decodeError != nil {
		return nil, fmt.Errorf("malformed processed-actions response from oms-gateway: %w", decodeError)
	}
	return wireResponse.ProcessedActions, nil
}

// EstimateCharges calls oms-gateway's real, live POST
// /orders/estimate-charges for one fill and returns its real charges
// breakdown. isChargesCalculatorLive reports whether the live
// oms-gateway calculator actually answered (true) or whether the caller
// should fall back to a documented illustrative local computation
// (false) because oms-gateway's charges endpoint was unreachable.
func (client *OmsGatewayClient) EstimateCharges(
	orderSideIsBuyNotSell bool,
	priceInMinorUnits int64,
	quantity uint64,
	isIntradayNotDelivery bool,
) (breakdown ChargesBreakdownWireFormat, isChargesCalculatorLive bool) {
	requestBody, marshalError := json.Marshal(estimateChargesWireRequest{
		OrderSideIsBuyNotSell:  orderSideIsBuyNotSell,
		LimitPriceInMinorUnits: priceInMinorUnits,
		OrderQuantity:          quantity,
		IsIntradayNotDelivery:  isIntradayNotDelivery,
	})
	if marshalError != nil {
		return ChargesBreakdownWireFormat{}, false
	}

	requestUrl := fmt.Sprintf("%s/orders/estimate-charges", client.omsGatewayBaseUrl)
	httpResponse, requestError := client.httpClient.Post(requestUrl, "application/json", bytes.NewReader(requestBody))
	if requestError != nil {
		client.chargesCalculatorProbed = true
		client.chargesCalculatorReachable = false
		return ChargesBreakdownWireFormat{}, false
	}
	defer httpResponse.Body.Close()

	if httpResponse.StatusCode != http.StatusOK {
		client.chargesCalculatorProbed = true
		client.chargesCalculatorReachable = false
		return ChargesBreakdownWireFormat{}, false
	}

	var wireResponse ChargesBreakdownWireFormat
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&wireResponse); decodeError != nil {
		client.chargesCalculatorProbed = true
		client.chargesCalculatorReachable = false
		return ChargesBreakdownWireFormat{}, false
	}

	client.chargesCalculatorProbed = true
	client.chargesCalculatorReachable = true
	return wireResponse, true
}
