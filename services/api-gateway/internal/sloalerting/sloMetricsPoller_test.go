package sloalerting

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSampleOrderRejectRateComputesRatioFromAuditTrail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]auditTrailEntryWire{
			{EventType: "ORDER_SUBMITTED"},
			{EventType: "ORDER_REJECTED"},
			{EventType: "ORDER_SUBMITTED"},
			{EventType: "ORDER_REJECTED"},
		})
	}))
	defer server.Close()

	poller := NewMetricsPoller(NewSloAlertEvaluator(DefaultThresholds), server.URL, "http://unused", "127.0.0.1:1")
	rate, ok := poller.sampleOrderRejectRate()
	if !ok {
		t.Fatalf("expected a successful sample")
	}
	if rate != 0.5 {
		t.Fatalf("expected reject rate 0.5 (2 rejected out of 4 relevant), got %v", rate)
	}
}

func TestSampleOrderRejectRateReturnsFalseOnUnreachableServer(t *testing.T) {
	poller := NewMetricsPoller(NewSloAlertEvaluator(DefaultThresholds), "http://127.0.0.1:1", "http://unused", "127.0.0.1:1")
	_, ok := poller.sampleOrderRejectRate()
	if ok {
		t.Fatalf("expected sampling an unreachable oms-gateway to fail soft (ok=false)")
	}
}

func TestSampleFeedStalenessSecondsComputesAgeOfMostRecentTrade(t *testing.T) {
	recentTimestamp := time.Now().Add(-10 * time.Second).UnixMilli()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]marketDataTradeWire{
			{TimestampUnixMillis: recentTimestamp},
		})
	}))
	defer server.Close()

	poller := NewMetricsPoller(NewSloAlertEvaluator(DefaultThresholds), "http://unused", server.URL, "127.0.0.1:1")
	staleness, ok := poller.sampleFeedStalenessSeconds()
	if !ok {
		t.Fatalf("expected a successful sample")
	}
	if staleness < 9 || staleness > 12 {
		t.Fatalf("expected staleness around 10s, got %v", staleness)
	}
}

func TestSampleFeedStalenessSecondsReturnsFalseWhenNoTrades(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]marketDataTradeWire{})
	}))
	defer server.Close()

	poller := NewMetricsPoller(NewSloAlertEvaluator(DefaultThresholds), "http://unused", server.URL, "127.0.0.1:1")
	_, ok := poller.sampleFeedStalenessSeconds()
	if ok {
		t.Fatalf("expected no-trades response to yield ok=false")
	}
}

func TestSampleMatchingEngineConnectLatencyMsSucceedsAgainstARealListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start test listener: %v", err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			conn.Close()
		}
	}()

	poller := NewMetricsPoller(NewSloAlertEvaluator(DefaultThresholds), "http://unused", "http://unused", listener.Addr().String())
	latency, ok := poller.sampleMatchingEngineConnectLatencyMs()
	if !ok {
		t.Fatalf("expected a successful connect-latency sample against a real listener")
	}
	if latency < 0 {
		t.Fatalf("expected non-negative latency, got %v", latency)
	}
}

func TestSampleMatchingEngineConnectLatencyMsFailsAgainstUnreachableAddress(t *testing.T) {
	poller := NewMetricsPoller(NewSloAlertEvaluator(DefaultThresholds), "http://unused", "http://unused", "127.0.0.1:1")
	_, ok := poller.sampleMatchingEngineConnectLatencyMs()
	if ok {
		t.Fatalf("expected connecting to a closed/unreachable port to fail soft (ok=false)")
	}
}

func TestRunForeverStopsCleanlyWhenStopChannelIsClosed(t *testing.T) {
	poller := NewMetricsPoller(NewSloAlertEvaluator(DefaultThresholds), "http://127.0.0.1:1", "http://127.0.0.1:1", "127.0.0.1:1")
	stopChannel := make(chan struct{})
	done := make(chan struct{})

	go func() {
		poller.RunForever(10*time.Millisecond, stopChannel)
		close(done)
	}()

	time.Sleep(25 * time.Millisecond)
	close(stopChannel)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("expected RunForever to return promptly after stopChannel was closed")
	}
}
