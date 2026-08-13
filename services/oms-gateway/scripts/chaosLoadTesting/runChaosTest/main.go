// Command runChaosTest is FEATURES.md §13's "[P2] Chaos/load testing on
// the OMS and matching path before go-live" — the chaos half.
//
// While a REAL concurrent load runs against a REAL, already-running
// oms-gateway process's POST /orders/submit endpoint, this script kills
// a REAL dependency process (default: kyc-onboarding, listening on
// :8083) partway through the run, and reports the ACTUAL observed
// before/after behavior — request-by-request, not a single manual probe
// — so it can be compared against cmd/server/main.go's documented
// fail-open behavior for the KYC gate:
//
//	"a TRANSPORT failure (kyc-onboarding unreachable) fails OPEN (logs a
//	warning, proceeds to the risk check) rather than blocking all trading
//	platform-wide on one dependency's uptime."
//
// Expected, documented-in-code behavior under this chaos scenario:
//   - orders keep getting HTTP 200 responses (oms-gateway does not hang
//     or crash)
//   - orders keep getting accepted (wasOrderAccepted=true) at
//     essentially the same rate as before the kill, because the KYC gate
//     fails OPEN on a transport error rather than rejecting
//   - oms-gateway's own /health endpoint stays healthy throughout
//
// This script kills the target process directly by PID (discovered via
// `lsof -i :<port> -sTCP:LISTEN -t`) — it does NOT start or restart any
// service; re-launch the killed dependency yourself afterward if you
// want to keep testing.
//
// Usage:
//
//	go run ./scripts/chaosLoadTesting/runChaosTest \
//	  -concurrency 100 -duration 20s -killAfter 8s \
//	  -targetPort 8083 -targetName kyc-onboarding \
//	  -baseUrl http://127.0.0.1:8081
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"mercurius/omsgateway/scripts/chaosLoadTesting/internal/orderloadgenerator"
)

func main() {
	concurrentWorkerCount := flag.Int("concurrency", 100, "number of concurrent workers hammering /orders/submit throughout the chaos run")
	testDuration := flag.Duration("duration", 20*time.Second, "total wall-clock duration of the chaos run")
	killAfter := flag.Duration("killAfter", 8*time.Second, "how far into the run to kill the target dependency process")
	targetPort := flag.Int("targetPort", 8083, "TCP port of the dependency process to kill mid-run (default: kyc-onboarding)")
	targetName := flag.String("targetName", "kyc-onboarding", "human-readable name of the dependency being killed, for the report")
	omsGatewayBaseUrl := flag.String("baseUrl", "http://127.0.0.1:8081", "oms-gateway base URL")
	flag.Parse()

	fmt.Printf(
		"runChaosTest: %d concurrent workers hitting %s/orders/submit for %s; killing %s (port %d) at t+%s\n",
		*concurrentWorkerCount, *omsGatewayBaseUrl, *testDuration, *targetName, *targetPort, *killAfter,
	)

	collector := orderloadgenerator.NewAtomicOutcomeCollector()
	rawOutcomesChannel := make(chan orderloadgenerator.OrderSubmissionOutcome, *concurrentWorkerCount*4)

	testStartedAt := time.Now()
	orderloadgenerator.RunConcurrentOrderSubmissionLoad(*omsGatewayBaseUrl, *concurrentWorkerCount, *testDuration, rawOutcomesChannel)

	channelDrainedSignal := make(chan struct{})
	go func() {
		for outcome := range rawOutcomesChannel {
			collector.Record(outcome)
		}
		close(channelDrainedSignal)
	}()

	// Live heartbeat every second so the kill moment is visible relative
	// to real, in-flight traffic in the terminal output, not just in the
	// final summary.
	heartbeatTicker := time.NewTicker(1 * time.Second)
	defer heartbeatTicker.Stop()

	var actualKillTimestamp time.Time
	var killedProcessId string
	var killError error
	hasKillHappenedYet := false

	testDeadline := testStartedAt.Add(*testDuration)
	for time.Now().Before(testDeadline) {
		select {
		case <-heartbeatTicker.C:
			total, transportErr, accepted, rejected := collector.SnapshotCounts()
			fmt.Printf(
				"  t+%-6s total=%-6d accepted=%-6d rejected=%-6d transportErrors=%-6d\n",
				time.Since(testStartedAt).Round(100*time.Millisecond), total, accepted, rejected, transportErr,
			)
			if !hasKillHappenedYet && time.Since(testStartedAt) >= *killAfter {
				hasKillHappenedYet = true
				killedProcessId, killError = killProcessListeningOnPort(*targetPort)
				actualKillTimestamp = time.Now()
				if killError != nil {
					fmt.Printf("  >>> FAILED to kill %s (port %d): %v\n", *targetName, *targetPort, killError)
				} else {
					fmt.Printf("  >>> KILLED %s (port %d, pid %s) at t+%s\n", *targetName, *targetPort, killedProcessId, actualKillTimestamp.Sub(testStartedAt).Round(time.Millisecond))
				}
			}
		}
	}
	<-channelDrainedSignal
	wallClockDuration := time.Since(testStartedAt)

	allOutcomes := collector.AllOutcomes()

	var beforeKillOutcomes, afterKillOutcomes []orderloadgenerator.OrderSubmissionOutcome
	killElapsedOffset := actualKillTimestamp.Sub(testStartedAt)
	for _, outcome := range allOutcomes {
		if hasKillHappenedYet && outcome.ElapsedSinceTestStart >= killElapsedOffset {
			afterKillOutcomes = append(afterKillOutcomes, outcome)
		} else {
			beforeKillOutcomes = append(beforeKillOutcomes, outcome)
		}
	}

	fmt.Println("\n################################################################")
	fmt.Println("CHAOS TEST RESULTS")
	fmt.Println("################################################################")
	if hasKillHappenedYet && killError == nil {
		fmt.Printf("%s (pid %s, port %d) was killed at t+%s\n", *targetName, killedProcessId, *targetPort, killElapsedOffset.Round(time.Millisecond))
	} else {
		fmt.Printf("WARNING: %s was never successfully killed during this run — before/after comparison is not meaningful.\n", *targetName)
	}

	orderloadgenerator.PrintHumanReadableReport(fmt.Sprintf("BEFORE kill (%s still up)", *targetName), orderloadgenerator.ComputeLatencyPercentileReport(beforeKillOutcomes, *killAfter))
	orderloadgenerator.PrintHumanReadableReport(fmt.Sprintf("AFTER kill (%s down for the rest of the run)", *targetName), orderloadgenerator.ComputeLatencyPercentileReport(afterKillOutcomes, wallClockDuration-*killAfter))
	orderloadgenerator.PrintHumanReadableReport("FULL RUN (combined)", orderloadgenerator.ComputeLatencyPercentileReport(allOutcomes, wallClockDuration))

	fmt.Println("\n--- post-chaos health check on oms-gateway itself ---")
	checkOmsGatewayHealthAfterChaos(*omsGatewayBaseUrl)
}

// killProcessListeningOnPort discovers the real OS process bound to the
// given TCP port via `lsof` and sends it a real SIGKILL via `kill -9` —
// this is an actual process termination, not a simulated failure
// injection.
func killProcessListeningOnPort(port int) (pid string, err error) {
	lsofOutput, lsofError := exec.Command("lsof", "-i", fmt.Sprintf(":%d", port), "-sTCP:LISTEN", "-t").Output()
	if lsofError != nil {
		return "", fmt.Errorf("lsof could not find a process listening on port %d: %w", port, lsofError)
	}
	pidString := strings.TrimSpace(string(lsofOutput))
	if pidString == "" {
		return "", fmt.Errorf("no process found listening on port %d", port)
	}
	// lsof can return multiple PIDs (e.g. `go run`'s wrapper plus the
	// exec'd binary) — kill all of them so the dependency actually goes
	// down, not just one half of a parent/child pair.
	pids := strings.Fields(pidString)
	for _, onePid := range pids {
		if _, parseErr := strconv.Atoi(onePid); parseErr != nil {
			continue
		}
		killCmd := exec.Command("kill", "-9", onePid)
		if killErr := killCmd.Run(); killErr != nil {
			err = killErr
		}
	}
	return pidString, err
}

// checkOmsGatewayHealthAfterChaos confirms oms-gateway itself is still
// alive and answering after the dependency kill and the full load run —
// the chaos test's "didn't crash the OMS" assertion, checked for real
// via one more real HTTP call.
func checkOmsGatewayHealthAfterChaos(omsGatewayBaseUrl string) {
	httpClient := &http.Client{Timeout: 3 * time.Second}
	response, err := httpClient.Get(omsGatewayBaseUrl + "/health")
	if err != nil {
		fmt.Printf("oms-gateway /health FAILED after chaos run: %v -- THIS WOULD BE A REAL BUG (oms-gateway should never go down because a downstream dependency did)\n", err)
		return
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusOK {
		fmt.Printf("oms-gateway /health = HTTP %d — still healthy after the chaos run.\n", response.StatusCode)
	} else {
		fmt.Printf("oms-gateway /health = HTTP %d — NOT healthy after the chaos run.\n", response.StatusCode)
	}
}
