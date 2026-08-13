// Package roboadvisory maps a risk-profile category to a model portfolio
// allocation across EQUITY/DEBT/HYBRID scheme categories — FEATURES.md
// §4, "Robo-Advisory: risk-profile → Efficient Frontier allocation".
//
// LOUD CAVEAT: the allocation table in this package is a hand-picked,
// ILLUSTRATIVE model-portfolio table, not a real mean-variance/Efficient
// Frontier OPTIMIZATION. A genuine Efficient Frontier allocation solves
// for the portfolio weights that maximize expected return for a given
// level of risk (or minimize risk for a given expected return) using the
// historical mean-return vector and covariance matrix of the candidate
// assets — this repo has neither: fundcatalog's schemes are five
// hardcoded, entirely fictitious NAVs with no historical return series
// behind them. Building a real optimizer without real historical return
// data would just be dressing up a made-up table in fancier math; this
// package is honest about it being a made-up table instead.
//
// RiskCategory matches kyc-onboarding's internal/riskprofiling category
// names exactly (see that package's RiskCategory type) so a caller can
// feed this package the category kyc-onboarding's questionnaire produced
// without any translation layer. mutual-funds does not import
// kyc-onboarding (separate Go modules, separate services) — the string
// constants are simply kept identical by convention.
//
// The "uses Sharpe Ratio module, see §6" cross-reference in FEATURES.md
// is wired in for real: after producing a model allocation, this package
// calls quant-engine's real POST /risk/statistics endpoint (port 8085)
// with an illustrative synthetic periodic-return series representative of
// that allocation's risk/return profile, and surfaces the resulting
// Sharpe ratio, Sortino ratio, and max drawdown alongside the
// recommendation. The RETURN SERIES itself is synthetic/illustrative (see
// GenerateIllustrativeReturnSeries) — quant-engine's Sharpe/Sortino/
// max-drawdown MATH applied to it is completely real, just fed a made-up
// input, exactly like feeding any other backtest a synthetic price
// series. If quant-engine is unreachable, the recommendation is still
// returned with the risk statistics section empty and an explanatory
// error message — see FetchRiskStatistics.
package roboadvisory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// RiskCategory mirrors kyc-onboarding's internal/riskprofiling.RiskCategory
// values exactly.
type RiskCategory string

const (
	CategoryConservative           RiskCategory = "CONSERVATIVE"
	CategoryModeratelyConservative RiskCategory = "MODERATELY_CONSERVATIVE"
	CategoryModerate               RiskCategory = "MODERATE"
	CategoryModeratelyAggressive   RiskCategory = "MODERATELY_AGGRESSIVE"
	CategoryAggressive             RiskCategory = "AGGRESSIVE"
)

// ModelAllocation is a target percentage split across the three coarse
// scheme categories fundcatalog.SchemeCategory defines. Always sums to
// exactly 100.
type ModelAllocation struct {
	EquityPercent float64
	DebtPercent   float64
	HybridPercent float64
}

// illustrativeModelAllocationsByRiskCategory is the hand-picked,
// illustrative allocation table — see the package doc comment's loud
// caveat. Progressively shifts weight from DEBT to EQUITY as risk
// tolerance rises, holding a flat 10% HYBRID sleeve across every category
// (a reasonable, commonly-seen simplification, not a derived optimum).
var illustrativeModelAllocationsByRiskCategory = map[RiskCategory]ModelAllocation{
	CategoryConservative:           {EquityPercent: 20, DebtPercent: 70, HybridPercent: 10},
	CategoryModeratelyConservative: {EquityPercent: 35, DebtPercent: 55, HybridPercent: 10},
	CategoryModerate:               {EquityPercent: 50, DebtPercent: 40, HybridPercent: 10},
	CategoryModeratelyAggressive:   {EquityPercent: 65, DebtPercent: 25, HybridPercent: 10},
	CategoryAggressive:             {EquityPercent: 80, DebtPercent: 10, HybridPercent: 10},
}

var ErrUnknownRiskCategory = fmt.Errorf("unknown risk category")

// RiskStatistics mirrors quant-engine's POST /risk/statistics response
// body exactly (field-for-field, same names translated to Go casing).
type RiskStatistics struct {
	AnnualizedSharpeRatio            float64
	AnnualizedSortinoRatio           float64
	MaximumDrawdownFraction          float64
	MaximumDrawdownPeakEquityValue   float64
	MaximumDrawdownTroughEquityValue float64
}

// Recommendation is a full Robo-Advisory response: the model allocation
// for riskCategory, the illustrative return series used to probe
// quant-engine, and (if quant-engine was reachable) the resulting risk
// statistics. RiskStatistics is nil and RiskStatisticsError is set if
// quant-engine could not be reached or rejected the request — a
// Robo-Advisory recommendation is still useful without the risk
// statistics attached, so this is not a hard failure of Recommend.
type Recommendation struct {
	RiskCategory             RiskCategory
	Allocation               ModelAllocation
	IllustrativeReturnSeries []float64
	RiskStatistics           *RiskStatistics
	RiskStatisticsError      string
}

// GenerateIllustrativeReturnSeries produces a deterministic, illustrative
// monthly periodic-return series representative of allocation's
// risk/return profile — NOT a forecast, NOT historical data, purely a
// synthetic input so quant-engine's real Sharpe/Sortino/max-drawdown math
// has something concrete to compute over. Built from a fixed 12-period
// oscillating base pattern (mean slightly positive, alternating up/down
// swings) scaled by a volatility multiplier that grows with the
// allocation's equity weight: a more equity-heavy allocation gets larger
// swings (both up and down), matching the real-world intuition that
// equity-heavy portfolios carry more volatility than debt-heavy ones,
// without claiming to be an actual historical or projected return series
// for any real Mercurius scheme.
func GenerateIllustrativeReturnSeries(allocation ModelAllocation) []float64 {
	basePatternPercent := []float64{
		0.020, -0.010, 0.015, -0.005, 0.025, -0.015,
		0.010, -0.020, 0.030, -0.010, 0.005, -0.005,
	}
	equityFraction := allocation.EquityPercent / 100.0
	volatilityMultiplier := 0.25 + equityFraction // 0.25 (all-debt floor) .. 1.25 (all-equity)

	returnSeries := make([]float64, len(basePatternPercent))
	for i, basePercent := range basePatternPercent {
		returnSeries[i] = basePercent * volatilityMultiplier
	}
	return returnSeries
}

// QuantEngineClient calls quant-engine's real HTTP API. baseURL is
// typically "http://127.0.0.1:8085" (see quant-engine's README).
type QuantEngineClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewQuantEngineClient builds a client with a bounded per-request
// timeout — quant-engine being unreachable should fail fast, not hang
// the caller's HTTP handler indefinitely.
func NewQuantEngineClient(baseURL string) *QuantEngineClient {
	return &QuantEngineClient{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 3 * time.Second},
	}
}

type riskStatisticsWireRequest struct {
	PeriodicReturns      []float64 `json:"periodicReturns"`
	PeriodicRiskFreeRate float64   `json:"periodicRiskFreeRate"`
	PeriodsPerYear       float64   `json:"periodsPerYear"`
}

type riskStatisticsWireResponse struct {
	AnnualizedSharpeRatio            float64 `json:"annualizedSharpeRatio"`
	AnnualizedSortinoRatio           float64 `json:"annualizedSortinoRatio"`
	MaximumDrawdownFraction          float64 `json:"maximumDrawdownFraction"`
	MaximumDrawdownPeakEquityValue   float64 `json:"maximumDrawdownPeakEquityValue"`
	MaximumDrawdownTroughEquityValue float64 `json:"maximumDrawdownTroughEquityValue"`
	ErrorMessage                     string  `json:"errorMessage,omitempty"`
}

// FetchRiskStatistics calls quant-engine's POST /risk/statistics with
// periodicReturns (monthly, so periodsPerYear=12) and a 0 periodic
// risk-free rate (kept simple/illustrative — a real build would use an
// actual short-term rate). Returns a wrapped error (never panics, never
// hangs past the client's timeout) if quant-engine is unreachable or
// rejects the request, so callers can degrade gracefully.
func (client *QuantEngineClient) FetchRiskStatistics(ctx context.Context, periodicReturns []float64) (*RiskStatistics, error) {
	requestBody, marshalError := json.Marshal(riskStatisticsWireRequest{
		PeriodicReturns:      periodicReturns,
		PeriodicRiskFreeRate: 0.0,
		PeriodsPerYear:       12,
	})
	if marshalError != nil {
		return nil, fmt.Errorf("failed to marshal risk statistics request: %w", marshalError)
	}

	httpRequest, requestBuildError := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+"/risk/statistics", bytes.NewReader(requestBody))
	if requestBuildError != nil {
		return nil, fmt.Errorf("failed to build risk statistics request: %w", requestBuildError)
	}
	httpRequest.Header.Set("Content-Type", "application/json")

	httpResponse, doError := client.httpClient.Do(httpRequest)
	if doError != nil {
		return nil, fmt.Errorf("quant-engine unreachable: %w", doError)
	}
	defer httpResponse.Body.Close()

	responseBytes, readError := io.ReadAll(httpResponse.Body)
	if readError != nil {
		return nil, fmt.Errorf("failed to read quant-engine response: %w", readError)
	}

	var wireResponse riskStatisticsWireResponse
	if decodeError := json.Unmarshal(responseBytes, &wireResponse); decodeError != nil {
		return nil, fmt.Errorf("failed to decode quant-engine response: %w", decodeError)
	}

	if httpResponse.StatusCode != http.StatusOK {
		errorMessage := wireResponse.ErrorMessage
		if errorMessage == "" {
			errorMessage = string(responseBytes)
		}
		return nil, fmt.Errorf("quant-engine rejected risk statistics request (status %d): %s", httpResponse.StatusCode, errorMessage)
	}

	return &RiskStatistics{
		AnnualizedSharpeRatio:            wireResponse.AnnualizedSharpeRatio,
		AnnualizedSortinoRatio:           wireResponse.AnnualizedSortinoRatio,
		MaximumDrawdownFraction:          wireResponse.MaximumDrawdownFraction,
		MaximumDrawdownPeakEquityValue:   wireResponse.MaximumDrawdownPeakEquityValue,
		MaximumDrawdownTroughEquityValue: wireResponse.MaximumDrawdownTroughEquityValue,
	}, nil
}

// RecommendAllocation maps riskCategory to its illustrative model
// allocation, generates an illustrative return series for that
// allocation, and — best-effort — calls quantEngineClient for real
// Sharpe/Sortino/max-drawdown statistics over that series. quantEngineClient
// may be nil (skips the risk-statistics call entirely, RiskStatisticsError
// explains why) so callers/tests that don't care about the quant-engine
// integration don't need to stand one up.
func RecommendAllocation(ctx context.Context, riskCategory RiskCategory, quantEngineClient *QuantEngineClient) (*Recommendation, error) {
	allocation, wasFound := illustrativeModelAllocationsByRiskCategory[riskCategory]
	if !wasFound {
		return nil, ErrUnknownRiskCategory
	}

	returnSeries := GenerateIllustrativeReturnSeries(allocation)

	recommendation := &Recommendation{
		RiskCategory:             riskCategory,
		Allocation:               allocation,
		IllustrativeReturnSeries: returnSeries,
	}

	if quantEngineClient == nil {
		recommendation.RiskStatisticsError = "no quant-engine client configured"
		return recommendation, nil
	}

	riskStatistics, fetchError := quantEngineClient.FetchRiskStatistics(ctx, returnSeries)
	if fetchError != nil {
		recommendation.RiskStatisticsError = fetchError.Error()
		return recommendation, nil
	}

	recommendation.RiskStatistics = riskStatistics
	return recommendation, nil
}
