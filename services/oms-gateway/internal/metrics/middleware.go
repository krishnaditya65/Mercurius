package metrics

import (
	"net/http"
	"time"
)

// WithRequestTiming wraps an http.Handler so every request's latency is
// recorded into registry, keyed by (method, request.URL.Path). Layer
// this alongside (order doesn't matter relative to)
// httplogging.WithRequestLogging — they're independent concerns that
// happen to both wrap the same requests. Unlike WithRequestLogging, this
// doesn't need to capture the status code — histograms here are keyed by
// route only, not by outcome — so no wrapping ResponseWriter is needed.
func WithRequestTiming(registry *Registry, nextHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		startTime := time.Now()
		nextHandler.ServeHTTP(responseWriter, request)
		durationMs := float64(time.Since(startTime)) / float64(time.Millisecond)
		registry.ObserveRequestDuration(request.Method, request.URL.Path, durationMs)
	})
}

// BuildMetricsHandler serves registry's current state as Prometheus text
// exposition format on GET /metrics.
func BuildMetricsHandler(registry *Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(responseWriter, "only GET is supported", http.StatusMethodNotAllowed)
			return
		}
		responseWriter.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_ = registry.WritePrometheusText(responseWriter)
	}
}
