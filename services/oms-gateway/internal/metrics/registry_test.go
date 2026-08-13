package metrics

import (
	"strings"
	"testing"
)

func TestObserveRequestDurationCreatesAHistogramLazily(t *testing.T) {
	registry := NewRegistry()
	registry.ObserveRequestDuration("POST", "/orders/submit", 42)

	var output strings.Builder
	if writeError := registry.WritePrometheusText(&output); writeError != nil {
		t.Fatalf("unexpected error writing output: %v", writeError)
	}

	if !strings.Contains(output.String(), `method="POST",path="/orders/submit"`) {
		t.Fatalf("expected output to contain the observed route's labels, got:\n%s", output.String())
	}
}

func TestDifferentRoutesGetIndependentHistograms(t *testing.T) {
	registry := NewRegistry()
	registry.ObserveRequestDuration("GET", "/health", 1)
	registry.ObserveRequestDuration("POST", "/orders/submit", 500)

	var output strings.Builder
	registry.WritePrometheusText(&output)
	text := output.String()

	if !strings.Contains(text, `path="/health"`) {
		t.Fatal("expected /health to appear in the output")
	}
	if !strings.Contains(text, `path="/orders/submit"`) {
		t.Fatal("expected /orders/submit to appear in the output")
	}
	// /health's fast observation should count toward its own count=1,
	// not leak into /orders/submit's.
	if !strings.Contains(text, `http_request_duration_milliseconds_count{method="GET",path="/health"} 1`) {
		t.Fatalf("expected /health's count to be exactly 1, got:\n%s", text)
	}
}

func TestOutputIsValidPrometheusHistogramShape(t *testing.T) {
	registry := NewRegistry()
	registry.ObserveRequestDuration("GET", "/health", 3)

	var output strings.Builder
	registry.WritePrometheusText(&output)
	text := output.String()

	for _, requiredFragment := range []string{
		"# HELP http_request_duration_milliseconds",
		"# TYPE http_request_duration_milliseconds histogram",
		`http_request_duration_milliseconds_bucket{method="GET",path="/health",le="+Inf"} 1`,
		`http_request_duration_milliseconds_sum{method="GET",path="/health"} 3`,
		`http_request_duration_milliseconds_count{method="GET",path="/health"} 1`,
	} {
		if !strings.Contains(text, requiredFragment) {
			t.Fatalf("expected output to contain %q, got:\n%s", requiredFragment, text)
		}
	}
}

func TestSameRouteObservedTwiceAccumulatesOnOneHistogram(t *testing.T) {
	registry := NewRegistry()
	registry.ObserveRequestDuration("GET", "/health", 1)
	registry.ObserveRequestDuration("GET", "/health", 2)

	var output strings.Builder
	registry.WritePrometheusText(&output)

	if !strings.Contains(output.String(), `http_request_duration_milliseconds_count{method="GET",path="/health"} 2`) {
		t.Fatalf("expected two observations on the same route to accumulate on one histogram, got:\n%s", output.String())
	}
}

func TestEmptyRegistryStillWritesValidHeaderWithNoRouteLines(t *testing.T) {
	registry := NewRegistry()

	var output strings.Builder
	if writeError := registry.WritePrometheusText(&output); writeError != nil {
		t.Fatalf("unexpected error: %v", writeError)
	}
	if !strings.Contains(output.String(), "# TYPE http_request_duration_milliseconds histogram") {
		t.Fatal("expected the HELP/TYPE header even with no observations recorded")
	}
}
