// sloMetricsPoller.go is the "reach real running services" half of
// package sloalerting — see the package doc comment in
// sloAlertEvaluator.go for why the evaluation logic itself lives
// separately and is unit-tested without any of this.
package sloalerting

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"time"
)

// MetricsPoller periodically collects real samples from real running
// services and feeds them into an SloAlertEvaluator.
type MetricsPoller struct {
	evaluator             *SloAlertEvaluator
	httpClient            *http.Client
	omsGatewayBaseUrl     string
	marketDataBaseUrl     string
	matchingEngineTcpAddr string
}

// NewMetricsPoller wires a poller against the real base URLs/addresses
// of the services it samples:
//   - omsGatewayBaseUrl: oms-gateway's HTTP API (default :8081) — polls
//     `GET /audit-trail` and computes a rolling order-reject rate from
//     the most recent ORDER_SUBMITTED/ORDER_REJECTED entries.
//   - marketDataBaseUrl: market-data's HTTP query API (default :9103) —
//     polls `GET /trades` and computes feed staleness as the age of the
//     most recent trade's timestamp.
//   - matchingEngineTcpAddr: matching-engine's TCP wire-protocol listener
//     (default 127.0.0.1:9101, per its own README/main.rs) — since
//     matching-engine has no HTTP metrics endpoint (see
//     services/oms-gateway/internal/metrics's own documented TODO on
//     this exact gap), this poller uses TCP CONNECT latency as a
//     reachability + rough responsiveness proxy, not a true
//     order-matching latency histogram.
func NewMetricsPoller(evaluator *SloAlertEvaluator, omsGatewayBaseUrl, marketDataBaseUrl, matchingEngineTcpAddr string) *MetricsPoller {
	return &MetricsPoller{
		evaluator:             evaluator,
		httpClient:            &http.Client{Timeout: 5 * time.Second},
		omsGatewayBaseUrl:     omsGatewayBaseUrl,
		marketDataBaseUrl:     marketDataBaseUrl,
		matchingEngineTcpAddr: matchingEngineTcpAddr,
	}
}

// RunForever polls every pollInterval until stopChannel is closed. Meant
// to be run in its own goroutine from main.go.
func (poller *MetricsPoller) RunForever(pollInterval time.Duration, stopChannel <-chan struct{}) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stopChannel:
			return
		case <-ticker.C:
			poller.pollOnce()
		}
	}
}

func (poller *MetricsPoller) pollOnce() {
	now := time.Now()

	if rejectRate, ok := poller.sampleOrderRejectRate(); ok {
		poller.evaluator.EvaluateSample(MetricSample{Kind: MetricKindOrderRejectRate, Value: rejectRate, ObservedTime: now})
	}
	if stalenessSeconds, ok := poller.sampleFeedStalenessSeconds(); ok {
		poller.evaluator.EvaluateSample(MetricSample{Kind: MetricKindFeedStalenessSeconds, Value: stalenessSeconds, ObservedTime: now})
	}
	if latencyMs, ok := poller.sampleMatchingEngineConnectLatencyMs(); ok {
		poller.evaluator.EvaluateSample(MetricSample{Kind: MetricKindMatchingLatencyMs, Value: latencyMs, ObservedTime: now})
	}
}

type auditTrailEntryWire struct {
	EventType string `json:"eventType"`
}

func (poller *MetricsPoller) sampleOrderRejectRate() (float64, bool) {
	response, err := poller.httpClient.Get(poller.omsGatewayBaseUrl + "/audit-trail")
	if err != nil {
		log.Printf("sloalerting: failed polling oms-gateway audit trail: %v", err)
		return 0, false
	}
	defer response.Body.Close()

	var entries []auditTrailEntryWire
	if decodeErr := json.NewDecoder(response.Body).Decode(&entries); decodeErr != nil {
		log.Printf("sloalerting: failed decoding oms-gateway audit trail: %v", decodeErr)
		return 0, false
	}

	// Only consider the most recent window of entries so a long-lived
	// process's reject rate reflects RECENT behavior, not all-time
	// history.
	const recentWindowSize = 50
	if len(entries) > recentWindowSize {
		entries = entries[len(entries)-recentWindowSize:]
	}

	var submittedOrRejectedCount, rejectedCount int
	for _, entry := range entries {
		switch entry.EventType {
		case "ORDER_SUBMITTED", "ORDER_FILLED":
			submittedOrRejectedCount++
		case "ORDER_REJECTED":
			submittedOrRejectedCount++
			rejectedCount++
		}
	}
	if submittedOrRejectedCount == 0 {
		return 0, false
	}
	return float64(rejectedCount) / float64(submittedOrRejectedCount), true
}

type marketDataTradeWire struct {
	TimestampUnixMillis int64 `json:"timestampUnixMillis"`
}

func (poller *MetricsPoller) sampleFeedStalenessSeconds() (float64, bool) {
	response, err := poller.httpClient.Get(poller.marketDataBaseUrl + "/trades")
	if err != nil {
		log.Printf("sloalerting: failed polling market-data trades: %v", err)
		return 0, false
	}
	defer response.Body.Close()

	var trades []marketDataTradeWire
	if decodeErr := json.NewDecoder(response.Body).Decode(&trades); decodeErr != nil {
		// market-data's wire shape may not match this poller's guess
		// exactly (this is a best-effort illustrative poller, not a
		// shared client library) — fail soft rather than crash the
		// evaluator loop.
		log.Printf("sloalerting: failed decoding market-data trades response: %v", decodeErr)
		return 0, false
	}
	if len(trades) == 0 {
		return 0, false
	}

	mostRecent := trades[len(trades)-1]
	if mostRecent.TimestampUnixMillis == 0 {
		return 0, false
	}
	ageMs := time.Now().UnixMilli() - mostRecent.TimestampUnixMillis
	if ageMs < 0 {
		ageMs = 0
	}
	return float64(ageMs) / 1000.0, true
}

func (poller *MetricsPoller) sampleMatchingEngineConnectLatencyMs() (float64, bool) {
	startTime := time.Now()
	conn, err := net.DialTimeout("tcp", poller.matchingEngineTcpAddr, 2*time.Second)
	if err != nil {
		log.Printf("sloalerting: matching-engine unreachable at %s: %v", poller.matchingEngineTcpAddr, err)
		return 0, false
	}
	defer conn.Close()
	elapsedMs := float64(time.Since(startTime).Microseconds()) / 1000.0
	return elapsedMs, true
}
