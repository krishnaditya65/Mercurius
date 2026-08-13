// Command runLoadTest is FEATURES.md §13's "[P2] Chaos/load testing on
// the OMS and matching path before go-live" — the pure-load half.
//
// It fires REAL concurrent HTTP requests at a REAL, already-running
// oms-gateway process's POST /orders/submit endpoint and reports latency
// percentiles (p50/p95/p99), throughput, and error rate computed from
// the actual observed response times — nothing here is simulated.
//
// Prerequisites (this script does NOT start any service itself):
//   - matching-engine running on 127.0.0.1:9101
//   - ledger running on :8082, with acct-001/acct-002 funded via
//     POST /client-funds/deposit
//   - kyc-onboarding running on :8083, with acct-001/acct-002 submitted
//     via POST /kyc/submit
//   - oms-gateway running on :8081 (started AFTER the above, so its
//     startup balance sync sees real funded balances)
//
// Usage:
//
//	go run ./scripts/chaosLoadTesting/runLoadTest \
//	  -concurrency 100 -duration 15s -baseUrl http://127.0.0.1:8081
package main

import (
	"flag"
	"fmt"
	"time"

	"mercurius/omsgateway/scripts/chaosLoadTesting/internal/orderloadgenerator"
)

func main() {
	concurrentWorkerCount := flag.Int("concurrency", 100, "number of concurrent workers hammering /orders/submit")
	testDuration := flag.Duration("duration", 15*time.Second, "how long to sustain the concurrent load")
	omsGatewayBaseUrl := flag.String("baseUrl", "http://127.0.0.1:8081", "oms-gateway base URL")
	flag.Parse()

	fmt.Printf(
		"runLoadTest: %d concurrent workers hitting %s/orders/submit for %s (real HTTP, real oms-gateway, real matching-engine hand-off)\n",
		*concurrentWorkerCount, *omsGatewayBaseUrl, *testDuration,
	)

	outcomesChannel := make(chan orderloadgenerator.OrderSubmissionOutcome, *concurrentWorkerCount*4)
	testStartedAt := time.Now()
	orderloadgenerator.RunConcurrentOrderSubmissionLoad(*omsGatewayBaseUrl, *concurrentWorkerCount, *testDuration, outcomesChannel)

	var allOutcomes []orderloadgenerator.OrderSubmissionOutcome
	for outcome := range outcomesChannel {
		allOutcomes = append(allOutcomes, outcome)
	}
	wallClockDuration := time.Since(testStartedAt)

	report := orderloadgenerator.ComputeLatencyPercentileReport(allOutcomes, wallClockDuration)
	orderloadgenerator.PrintHumanReadableReport(
		fmt.Sprintf("LOAD TEST — %d workers, %s wall clock", *concurrentWorkerCount, wallClockDuration.Round(time.Millisecond)),
		report,
	)
}
