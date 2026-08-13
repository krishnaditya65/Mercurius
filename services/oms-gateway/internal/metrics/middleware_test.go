package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWithRequestTimingRecordsAnObservationForTheRoute(t *testing.T) {
	registry := NewRegistry()
	innerHandler := http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
	})

	wrappedHandler := WithRequestTiming(registry, innerHandler)
	wrappedHandler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/orders/submit", nil))

	var output strings.Builder
	registry.WritePrometheusText(&output)
	if !strings.Contains(output.String(), `http_request_duration_milliseconds_count{method="POST",path="/orders/submit"} 1`) {
		t.Fatalf("expected exactly one recorded observation for POST /orders/submit, got:\n%s", output.String())
	}
}

func TestWithRequestTimingPreservesTheHandlersResponse(t *testing.T) {
	registry := NewRegistry()
	innerHandler := http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusTeapot)
		_, _ = responseWriter.Write([]byte("hello"))
	})

	wrappedHandler := WithRequestTiming(registry, innerHandler)
	recordedResponse := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(recordedResponse, httptest.NewRequest(http.MethodGet, "/whatever", nil))

	if recordedResponse.Code != http.StatusTeapot {
		t.Fatalf("expected status %d to pass through unchanged, got %d", http.StatusTeapot, recordedResponse.Code)
	}
	if recordedResponse.Body.String() != "hello" {
		t.Fatalf("expected body to pass through unchanged, got %q", recordedResponse.Body.String())
	}
}

func TestBuildMetricsHandlerServesPrometheusTextOnGet(t *testing.T) {
	registry := NewRegistry()
	registry.ObserveRequestDuration("GET", "/health", 1)

	metricsHandler := BuildMetricsHandler(registry)
	recordedResponse := httptest.NewRecorder()
	metricsHandler.ServeHTTP(recordedResponse, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if recordedResponse.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recordedResponse.Code)
	}
	if !strings.Contains(recordedResponse.Body.String(), "http_request_duration_milliseconds") {
		t.Fatalf("expected Prometheus-format output, got: %s", recordedResponse.Body.String())
	}
}

func TestBuildMetricsHandlerRejectsNonGet(t *testing.T) {
	registry := NewRegistry()
	metricsHandler := BuildMetricsHandler(registry)
	recordedResponse := httptest.NewRecorder()
	metricsHandler.ServeHTTP(recordedResponse, httptest.NewRequest(http.MethodPost, "/metrics", nil))

	if recordedResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", recordedResponse.Code)
	}
}
