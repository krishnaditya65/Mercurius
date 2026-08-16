// Package omsgatewayclient is backoffice's real, read-only HTTP client
// for oms-gateway — the reverse direction of oms-gateway's own
// internal/backofficeclient (which calls INTO backoffice for freeze
// status). This package only ever performs GETs; it has no method that
// could submit, modify, or cancel an order, mirroring
// backofficeclient.go's error-handling contract exactly (a transport
// failure is a Go error; a successfully-answered response is not).
//
// Used by internal/familyaccountaccess's read-only aggregation endpoint
// (FEATURES.md §21) to fetch an owner account's real positions, and by
// internal/strategyleaderboard (FEATURES.md §19/§11) to fetch verified
// strategies, follower counts, and per-strategy trading-activity data.
//
// TODO(real build): synchronous, inline on whatever request path calls
// it — same caveat oms-gateway's own kycclient/ledgerclient/
// backofficeclient already document for this pattern across this repo.
package omsgatewayclient

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// PositionsWireResponse mirrors oms-gateway's GET /positions response
// shape exactly (see that service's buildPositionsHandler).
type PositionsWireResponse struct {
	AccountIdentifier             string           `json:"accountIdentifier"`
	NetQuantityByInstrumentSymbol map[string]int64 `json:"netQuantityByInstrumentSymbol"`
}

// VerifiedStrategyWireEntry mirrors one entry of oms-gateway's
// GET /strategies response (internal/strategyfollowing's
// VerifiedStrategyWithFollowerCount, over the wire).
type VerifiedStrategyWireEntry struct {
	StrategyIdentifier string `json:"strategyIdentifier"`
	DisplayName        string `json:"displayName"`
	Description        string `json:"description"`
	FollowerCount      int    `json:"followerCount"`
}

// AlgoLimitsStatusWireResponse mirrors oms-gateway's
// GET /algo-limits?strategyId=... response.
type AlgoLimitsStatusWireResponse struct {
	StrategyIdentifier            string `json:"strategyIdentifier"`
	NotionalUsedTodayInMinorUnits int64  `json:"notionalUsedTodayInMinorUnits"`
}

type OmsGatewayClient struct {
	omsGatewayBaseUrl string
	httpClient        *http.Client
}

func NewOmsGatewayClient(omsGatewayBaseUrl string) *OmsGatewayClient {
	return &OmsGatewayClient{
		omsGatewayBaseUrl: omsGatewayBaseUrl,
		httpClient:        &http.Client{Timeout: 2 * time.Second},
	}
}

// FetchPositions retrieves an account's real, current positions from
// oms-gateway's positionBook — GET /positions?accountId=... This is a
// pure read: nothing on this client can submit an order.
//
// oms-gateway's /positions route requires authmiddleware.RequireAuth, so
// callerAuthorizationHeaderValue must be the exact `Authorization` header
// value (e.g. "Bearer <token>") from the incoming request this call is
// acting on behalf of — this client forwards it verbatim rather than
// minting its own service-level token, so oms-gateway sees the real
// end-user identity making the request (same "acting as the
// authenticated user" semantics as the request that reached backoffice
// in the first place). Pass "" only for callers that genuinely have no
// end-user request to act on behalf of (e.g. tests); oms-gateway will
// then correctly reject the call with 401.
func (client *OmsGatewayClient) FetchPositions(accountIdentifier string, callerAuthorizationHeaderValue string) (*PositionsWireResponse, error) {
	requestUrl := fmt.Sprintf("%s/positions?accountId=%s", client.omsGatewayBaseUrl, url.QueryEscape(accountIdentifier))

	httpRequest, buildRequestError := http.NewRequest(http.MethodGet, requestUrl, nil)
	if buildRequestError != nil {
		return nil, fmt.Errorf("could not build request to oms-gateway at %s: %w", client.omsGatewayBaseUrl, buildRequestError)
	}
	if callerAuthorizationHeaderValue != "" {
		httpRequest.Header.Set("Authorization", callerAuthorizationHeaderValue)
	}

	httpResponse, requestError := client.httpClient.Do(httpRequest)
	if requestError != nil {
		return nil, fmt.Errorf("could not reach oms-gateway at %s: %w", client.omsGatewayBaseUrl, requestError)
	}
	defer httpResponse.Body.Close()

	if httpResponse.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oms-gateway returned HTTP %d for positions of account %s", httpResponse.StatusCode, accountIdentifier)
	}

	var wireResponse PositionsWireResponse
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&wireResponse); decodeError != nil {
		return nil, fmt.Errorf("malformed positions response: %w", decodeError)
	}
	return &wireResponse, nil
}

// FetchVerifiedStrategies retrieves the real, admin-verified strategy
// list with live follower counts — GET /strategies.
func (client *OmsGatewayClient) FetchVerifiedStrategies() ([]VerifiedStrategyWireEntry, error) {
	requestUrl := fmt.Sprintf("%s/strategies", client.omsGatewayBaseUrl)

	httpResponse, requestError := client.httpClient.Get(requestUrl)
	if requestError != nil {
		return nil, fmt.Errorf("could not reach oms-gateway at %s: %w", client.omsGatewayBaseUrl, requestError)
	}
	defer httpResponse.Body.Close()

	if httpResponse.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oms-gateway returned HTTP %d for /strategies", httpResponse.StatusCode)
	}

	var wireResponse []VerifiedStrategyWireEntry
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&wireResponse); decodeError != nil {
		return nil, fmt.Errorf("malformed verified-strategies response: %w", decodeError)
	}
	return wireResponse, nil
}

// FetchAlgoLimitsStatus retrieves the real cumulative notional a
// strategy has traded today — GET /algo-limits?strategyId=... This is
// the ONLY per-strategy trading-activity figure oms-gateway exposes
// today; see internal/strategyleaderboard's package doc for exactly how
// (and how honestly) this is used as a performance-ranking proxy.
func (client *OmsGatewayClient) FetchAlgoLimitsStatus(strategyIdentifier string) (*AlgoLimitsStatusWireResponse, error) {
	requestUrl := fmt.Sprintf("%s/algo-limits?strategyId=%s", client.omsGatewayBaseUrl, url.QueryEscape(strategyIdentifier))

	httpResponse, requestError := client.httpClient.Get(requestUrl)
	if requestError != nil {
		return nil, fmt.Errorf("could not reach oms-gateway at %s: %w", client.omsGatewayBaseUrl, requestError)
	}
	defer httpResponse.Body.Close()

	if httpResponse.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oms-gateway returned HTTP %d for /algo-limits of strategy %s", httpResponse.StatusCode, strategyIdentifier)
	}

	var wireResponse AlgoLimitsStatusWireResponse
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&wireResponse); decodeError != nil {
		return nil, fmt.Errorf("malformed algo-limits status response: %w", decodeError)
	}
	return &wireResponse, nil
}
