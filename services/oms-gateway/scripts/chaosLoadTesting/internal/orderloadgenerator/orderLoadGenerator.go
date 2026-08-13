// Package orderloadgenerator is shared, REAL load-generation machinery for
// FEATURES.md §13's "[P2] Chaos/load testing on the OMS and matching path
// before go-live". Both scripts/chaosLoadTesting/runLoadTest and
// scripts/chaosLoadTesting/runChaosTest reuse this instead of each
// reimplementing "spin up N concurrent workers hammering
// POST /orders/submit and record real latencies" — the load pattern is
// identical between the two; the only difference is that the chaos test
// also kills a dependency process partway through.
//
// Every number this package reports comes from an actual observed HTTP
// round trip against a real, running oms-gateway process — nothing here
// is fabricated or simulated.
package orderloadgenerator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// demoFundedAndKycVerifiedAccountIdentifiers matches oms-gateway's own
// demoTrackedAccountIdentifiers (cmd/server/main.go) — these are the only
// two accounts the running oms-gateway process has synced a real ledger
// balance for at startup, so orders alternate between them to get real
// accept/fill outcomes rather than uniform risk-check rejections.
var demoFundedAndKycVerifiedAccountIdentifiers = []string{"acct-001", "acct-002"}

// tradedInstrumentSymbol matches matching-engine's DEMO-EQ instrument
// (see matching-engine's startup log line in main.rs).
const tradedInstrumentSymbol = "DEMO-EQ"

// OrderSubmissionOutcome is one real, observed result of one real
// POST /orders/submit call.
type OrderSubmissionOutcome struct {
	ElapsedSinceTestStart time.Duration
	RequestLatency        time.Duration
	HttpStatusCode        int
	WasOrderAccepted      bool
	MachineReadableReason string
	TransportError        error
}

// isSuccessfulHttpRoundTrip reports whether this outcome represents a
// real, completed HTTP round trip that got a response back at all
// (regardless of whether the order itself was accepted or rejected by a
// business rule — a clean, fast, well-formed REJECT is a healthy
// response, not an error, for load/chaos testing purposes). Only a
// transport-level failure (connection refused, timeout, dropped
// connection) counts as an error here.
func (outcome OrderSubmissionOutcome) isSuccessfulHttpRoundTrip() bool {
	return outcome.TransportError == nil && outcome.HttpStatusCode == http.StatusOK
}

// orderSubmissionWireRequestPayload mirrors oms-gateway's
// orders.OrderSubmissionRequest JSON shape (internal/orders/orderTypes.go)
// closely enough to exercise the real /orders/submit handler end to end.
// Deliberately a local struct (not an import of oms-gateway's internal
// package) — this is an external load-testing client hitting a real HTTP
// boundary, exactly like a real caller would, not a white-box test.
type orderSubmissionWireRequestPayload struct {
	ClientAccountIdentifier    string `json:"clientAccountIdentifier"`
	InstrumentSymbol           string `json:"instrumentSymbol"`
	OrderSideIsBuyNotSell      bool   `json:"orderSideIsBuyNotSell"`
	OrderIsMarketOrderNotLimit bool   `json:"orderIsMarketOrderNotLimit"`
	LimitPriceInMinorUnits     int64  `json:"limitPriceInMinorUnits"`
	OrderQuantity              uint64 `json:"orderQuantity"`
	IdempotencyKey             string `json:"idempotencyKey"`
}

type orderAcknowledgementWireResponsePayload struct {
	WasOrderAccepted               bool   `json:"wasOrderAccepted"`
	MachineReadableRejectionReason string `json:"machineReadableRejectionReason,omitempty"`
	MatchingEngineHandoffError     string `json:"matchingEngineHandoffError,omitempty"`
}

// buildRandomOrderSubmissionPayload constructs a small, cheap, realistic
// order: alternating buy/sell, alternating account, a random quantity in
// [1,20] and a random limit price in [95,105] minor units around a
// nominal 100 so that both sides of the book see real matches without
// ever risking exhausting the 100000000000-minor-unit demo funding this
// script's caller is expected to have seeded via /client-funds/deposit.
func buildRandomOrderSubmissionPayload(workerIndex int, requestIndex int, randomSource *rand.Rand) orderSubmissionWireRequestPayload {
	accountIdentifier := demoFundedAndKycVerifiedAccountIdentifiers[randomSource.Intn(len(demoFundedAndKycVerifiedAccountIdentifiers))]
	return orderSubmissionWireRequestPayload{
		ClientAccountIdentifier:    accountIdentifier,
		InstrumentSymbol:           tradedInstrumentSymbol,
		OrderSideIsBuyNotSell:      randomSource.Intn(2) == 0,
		OrderIsMarketOrderNotLimit: false,
		LimitPriceInMinorUnits:     int64(95 + randomSource.Intn(11)),
		OrderQuantity:              uint64(1 + randomSource.Intn(20)),
		IdempotencyKey:             fmt.Sprintf("chaosLoadTesting-worker%d-req%d-%d", workerIndex, requestIndex, time.Now().UnixNano()),
	}
}

// submitOneRealOrderAndMeasureLatency does one real HTTP POST to a real,
// running oms-gateway's /orders/submit endpoint and returns exactly what
// was observed — no synthetic delay, no fabricated status.
func submitOneRealOrderAndMeasureLatency(
	httpClient *http.Client,
	omsGatewayBaseUrl string,
	testStartTime time.Time,
	workerIndex int,
	requestIndex int,
	randomSource *rand.Rand,
) OrderSubmissionOutcome {
	payload := buildRandomOrderSubmissionPayload(workerIndex, requestIndex, randomSource)
	payloadBytes, marshalError := json.Marshal(payload)
	if marshalError != nil {
		return OrderSubmissionOutcome{
			ElapsedSinceTestStart: time.Since(testStartTime),
			TransportError:        fmt.Errorf("could not marshal request payload: %w", marshalError),
		}
	}

	requestStartedAt := time.Now()
	httpResponse, requestError := httpClient.Post(
		omsGatewayBaseUrl+"/orders/submit",
		"application/json",
		bytes.NewReader(payloadBytes),
	)
	requestLatency := time.Since(requestStartedAt)
	elapsedSinceTestStart := time.Since(testStartTime)

	if requestError != nil {
		return OrderSubmissionOutcome{
			ElapsedSinceTestStart: elapsedSinceTestStart,
			RequestLatency:        requestLatency,
			TransportError:        requestError,
		}
	}
	defer httpResponse.Body.Close()

	var acknowledgement orderAcknowledgementWireResponsePayload
	decodeError := json.NewDecoder(httpResponse.Body).Decode(&acknowledgement)

	outcome := OrderSubmissionOutcome{
		ElapsedSinceTestStart: elapsedSinceTestStart,
		RequestLatency:        requestLatency,
		HttpStatusCode:        httpResponse.StatusCode,
		WasOrderAccepted:      acknowledgement.WasOrderAccepted,
		MachineReadableReason: acknowledgement.MachineReadableRejectionReason,
	}
	if decodeError != nil {
		outcome.TransportError = fmt.Errorf("could not decode acknowledgement body: %w", decodeError)
	}
	return outcome
}

// RunConcurrentOrderSubmissionLoad launches concurrentWorkerCount
// goroutines, each looping real POST /orders/submit calls back-to-back
// (no artificial pacing/sleep between a worker's own requests — this is
// intentionally a saturation load, not a fixed-rate one) against a real,
// already-running oms-gateway process at omsGatewayBaseUrl, for
// testDuration wall-clock time. Every observed outcome is sent to
// outcomesChannel as it completes, in real time, so a caller (e.g. the
// chaos test) can watch outcomes stream in and correlate them against
// when it killed a dependency. The channel is closed once every worker
// has stopped and drained.
func RunConcurrentOrderSubmissionLoad(
	omsGatewayBaseUrl string,
	concurrentWorkerCount int,
	testDuration time.Duration,
	outcomesChannel chan<- OrderSubmissionOutcome,
) {
	testStartTime := time.Now()
	testDeadline := testStartTime.Add(testDuration)

	httpClient := &http.Client{
		Timeout: 5 * time.Second,
		// A real load generator needs enough idle connections to actually
		// achieve concurrentWorkerCount concurrent in-flight requests
		// instead of serializing on Go's default (2 idle conns per host)
		// transport limit.
		Transport: &http.Transport{
			MaxIdleConns:        concurrentWorkerCount * 2,
			MaxIdleConnsPerHost: concurrentWorkerCount * 2,
			MaxConnsPerHost:     0, // unlimited — never artificially throttle the load
		},
	}

	var waitGroupForAllWorkers sync.WaitGroup
	for workerIndex := 0; workerIndex < concurrentWorkerCount; workerIndex++ {
		waitGroupForAllWorkers.Add(1)
		go func(workerIndex int) {
			defer waitGroupForAllWorkers.Done()
			// Each worker gets its own *rand.Rand — math/rand's global
			// source is mutex-guarded and would itself become a point of
			// contention distorting the very latencies being measured.
			workerRandomSource := rand.New(rand.NewSource(time.Now().UnixNano() + int64(workerIndex)))
			requestIndex := 0
			for time.Now().Before(testDeadline) {
				outcome := submitOneRealOrderAndMeasureLatency(
					httpClient, omsGatewayBaseUrl, testStartTime, workerIndex, requestIndex, workerRandomSource,
				)
				outcomesChannel <- outcome
				requestIndex++
			}
		}(workerIndex)
	}

	go func() {
		waitGroupForAllWorkers.Wait()
		close(outcomesChannel)
	}()
}

// LatencyPercentileReport summarizes a batch of real observed outcomes.
type LatencyPercentileReport struct {
	TotalRequestCount        int
	SuccessfulRoundTripCount int
	AcceptedOrderCount       int
	RejectedOrderCount       int
	TransportErrorCount      int
	ErrorRatePercent         float64
	ThroughputRequestsPerSec float64
	P50Latency               time.Duration
	P95Latency               time.Duration
	P99Latency               time.Duration
	MaxLatency               time.Duration
	MinLatency               time.Duration
	RejectionReasonCounts    map[string]int
}

// ComputeLatencyPercentileReport computes REAL percentiles from a slice
// of REAL observed outcomes — sorted, then indexed, no interpolation
// tricks that would let a small sample masquerade as more precise than
// it is.
func ComputeLatencyPercentileReport(outcomes []OrderSubmissionOutcome, wallClockDuration time.Duration) LatencyPercentileReport {
	report := LatencyPercentileReport{
		TotalRequestCount:     len(outcomes),
		RejectionReasonCounts: map[string]int{},
	}
	if len(outcomes) == 0 {
		return report
	}

	latencies := make([]time.Duration, 0, len(outcomes))
	for _, outcome := range outcomes {
		latencies = append(latencies, outcome.RequestLatency)
		if outcome.isSuccessfulHttpRoundTrip() {
			report.SuccessfulRoundTripCount++
			if outcome.WasOrderAccepted {
				report.AcceptedOrderCount++
			} else {
				report.RejectedOrderCount++
				report.RejectionReasonCounts[outcome.MachineReadableReason]++
			}
		} else {
			report.TransportErrorCount++
		}
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	percentileIndex := func(percentile float64) time.Duration {
		index := int(percentile * float64(len(latencies)))
		if index >= len(latencies) {
			index = len(latencies) - 1
		}
		return latencies[index]
	}

	report.P50Latency = percentileIndex(0.50)
	report.P95Latency = percentileIndex(0.95)
	report.P99Latency = percentileIndex(0.99)
	report.MinLatency = latencies[0]
	report.MaxLatency = latencies[len(latencies)-1]
	report.ErrorRatePercent = 100 * float64(report.TransportErrorCount) / float64(report.TotalRequestCount)
	if wallClockDuration > 0 {
		report.ThroughputRequestsPerSec = float64(report.TotalRequestCount) / wallClockDuration.Seconds()
	}
	return report
}

// PrintHumanReadableReport writes a real-numbers report to the given
// writer-like function (kept as a plain fmt.Println-style function
// pointer so both scripts can format identically without importing an
// io.Writer ceremony for a one-off CLI report).
func PrintHumanReadableReport(label string, report LatencyPercentileReport) {
	fmt.Printf("\n=== %s ===\n", label)
	fmt.Printf("total requests:          %d\n", report.TotalRequestCount)
	fmt.Printf("successful round trips:  %d\n", report.SuccessfulRoundTripCount)
	fmt.Printf("  accepted orders:       %d\n", report.AcceptedOrderCount)
	fmt.Printf("  rejected orders:       %d\n", report.RejectedOrderCount)
	for reason, count := range report.RejectionReasonCounts {
		if reason == "" {
			reason = "(no machine-readable reason set)"
		}
		fmt.Printf("    - %-40s %d\n", reason, count)
	}
	fmt.Printf("transport errors:        %d\n", report.TransportErrorCount)
	fmt.Printf("error rate:              %.3f%%\n", report.ErrorRatePercent)
	fmt.Printf("throughput:              %.1f req/sec\n", report.ThroughputRequestsPerSec)
	fmt.Printf("latency p50:             %s\n", report.P50Latency)
	fmt.Printf("latency p95:             %s\n", report.P95Latency)
	fmt.Printf("latency p99:             %s\n", report.P99Latency)
	fmt.Printf("latency min/max:         %s / %s\n", report.MinLatency, report.MaxLatency)
}

// AtomicOutcomeCollector is a concurrency-safe sink used by the chaos
// test to accumulate outcomes from RunConcurrentOrderSubmissionLoad's
// channel while ALSO letting a separate goroutine watch the running
// count/error-rate live (so the chaos test can print a real-time
// heartbeat while the dependency-kill happens mid-run).
type AtomicOutcomeCollector struct {
	totalCount            int64
	transportErr          int64
	acceptedCount         int64
	rejectedCount         int64
	mutexGuardingOutcomes sync.Mutex
	collectedOutcomes     []OrderSubmissionOutcome
}

func NewAtomicOutcomeCollector() *AtomicOutcomeCollector {
	return &AtomicOutcomeCollector{}
}

func (collector *AtomicOutcomeCollector) Record(outcome OrderSubmissionOutcome) {
	atomic.AddInt64(&collector.totalCount, 1)
	if !outcome.isSuccessfulHttpRoundTrip() {
		atomic.AddInt64(&collector.transportErr, 1)
	} else if outcome.WasOrderAccepted {
		atomic.AddInt64(&collector.acceptedCount, 1)
	} else {
		atomic.AddInt64(&collector.rejectedCount, 1)
	}
	collector.mutexGuardingOutcomes.Lock()
	collector.collectedOutcomes = append(collector.collectedOutcomes, outcome)
	collector.mutexGuardingOutcomes.Unlock()
}

func (collector *AtomicOutcomeCollector) SnapshotCounts() (total, transportErr, accepted, rejected int64) {
	return atomic.LoadInt64(&collector.totalCount),
		atomic.LoadInt64(&collector.transportErr),
		atomic.LoadInt64(&collector.acceptedCount),
		atomic.LoadInt64(&collector.rejectedCount)
}

func (collector *AtomicOutcomeCollector) AllOutcomes() []OrderSubmissionOutcome {
	collector.mutexGuardingOutcomes.Lock()
	defer collector.mutexGuardingOutcomes.Unlock()
	outcomesCopy := make([]OrderSubmissionOutcome, len(collector.collectedOutcomes))
	copy(outcomesCopy, collector.collectedOutcomes)
	return outcomesCopy
}
