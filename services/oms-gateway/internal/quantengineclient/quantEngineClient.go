// Package quantengineclient is oms-gateway's HTTP client for the
// `quant-engine` research-tier service — specifically its
// `POST /options/price` endpoint, which returns a REAL Black-Scholes
// theoretical price and all four Greeks for one option contract. Used by
// internal/optionschain to compute genuine per-contract Greeks for a
// synthetic strike ladder — see that package's doc comment for the full
// "what's real vs. synthetic" contract.
//
// Same synchronous-HTTP-client shape as internal/ledgerclient,
// internal/kycclient, and internal/backofficeclient: a short timeout, a
// transport-error/business-error split, nothing fancier.
package quantengineclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// OptionPricingRequest mirrors quant-engine's `POST /options/price`
// request body field-for-field (see that service's README).
type OptionPricingRequest struct {
	UnderlyingSpotPrice            float64 `json:"underlyingSpotPrice"`
	OptionStrikePrice              float64 `json:"optionStrikePrice"`
	AnnualizedRiskFreeInterestRate float64 `json:"annualizedRiskFreeInterestRate"`
	AnnualizedVolatility           float64 `json:"annualizedVolatility"`
	TimeToExpiryInYears            float64 `json:"timeToExpiryInYears"`
	IsCallOptionNotPut             bool    `json:"isCallOptionNotPut"`
}

// OptionPricingResponse mirrors quant-engine's `POST /options/price`
// response body field-for-field.
type OptionPricingResponse struct {
	TheoreticalPriceInMinorUnits      float64 `json:"theoreticalPriceInMinorUnits"`
	Delta                             float64 `json:"delta"`
	Gamma                             float64 `json:"gamma"`
	VegaPerOnePercentVolatilityChange float64 `json:"vegaPerOnePercentVolatilityChange"`
	ThetaPerCalendarDay               float64 `json:"thetaPerCalendarDay"`
	ErrorMessage                      string  `json:"errorMessage,omitempty"`
}

// QuantEngineClient is oms-gateway's HTTP client for quant-engine.
type QuantEngineClient struct {
	quantEngineBaseUrl string
	httpClient         *http.Client
}

func NewQuantEngineClient(quantEngineBaseUrl string) *QuantEngineClient {
	return &QuantEngineClient{
		quantEngineBaseUrl: quantEngineBaseUrl,
		httpClient:         &http.Client{Timeout: 2 * time.Second},
	}
}

// PriceOptionContract calls quant-engine's real Black-Scholes pricer +
// Greeks calculator for one contract. Returns a Go error only for a
// transport failure (unreachable, timeout, malformed response) or a
// business-level rejection quant-engine itself reports (e.g. non-positive
// time to expiry) — both are treated the same way here (as an error)
// since internal/optionschain has nothing useful to do with a single
// failed contract inside an otherwise-successful chain.
func (client *QuantEngineClient) PriceOptionContract(request OptionPricingRequest) (OptionPricingResponse, error) {
	requestBodyBytes, marshalError := json.Marshal(request)
	if marshalError != nil {
		return OptionPricingResponse{}, fmt.Errorf("failed to marshal option pricing request: %w", marshalError)
	}

	httpResponse, requestError := client.httpClient.Post(
		client.quantEngineBaseUrl+"/options/price",
		"application/json",
		bytes.NewReader(requestBodyBytes),
	)
	if requestError != nil {
		return OptionPricingResponse{}, fmt.Errorf("could not reach quant-engine at %s: %w", client.quantEngineBaseUrl, requestError)
	}
	defer httpResponse.Body.Close()

	var wireResponse OptionPricingResponse
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&wireResponse); decodeError != nil {
		return OptionPricingResponse{}, fmt.Errorf("malformed response from quant-engine: %w", decodeError)
	}

	if httpResponse.StatusCode != http.StatusOK {
		errorMessage := wireResponse.ErrorMessage
		if errorMessage == "" {
			errorMessage = fmt.Sprintf("quant-engine returned HTTP %d", httpResponse.StatusCode)
		}
		return OptionPricingResponse{}, fmt.Errorf("quant-engine rejected the pricing request: %s", errorMessage)
	}

	return wireResponse, nil
}
