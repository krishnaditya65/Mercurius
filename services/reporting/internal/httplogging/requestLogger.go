// Package httplogging provides structured (JSON, via log/slog) HTTP
// access logging for reporting — one log line per request with the
// method, path, status code, and duration. Deliberately the same small,
// self-contained pattern used by services/oms-gateway and
// services/ledger's own internal/httplogging packages (this repo's
// house style favors each service owning its own copy of small
// cross-cutting utilities rather than a shared library dependency
// between services).
package httplogging

import (
	"log/slog"
	"net/http"
	"time"
)

// statusCapturingResponseWriter wraps http.ResponseWriter to remember
// what status code the handler actually wrote, since the standard
// interface has no getter for it. Defaults to 200 — if a handler never
// calls WriteHeader explicitly (writing a body directly instead),
// net/http itself defaults to 200, so this mirrors that.
type statusCapturingResponseWriter struct {
	http.ResponseWriter
	capturedStatusCode int
	headerWasWritten   bool
}

func (wrappedWriter *statusCapturingResponseWriter) WriteHeader(statusCode int) {
	wrappedWriter.capturedStatusCode = statusCode
	wrappedWriter.headerWasWritten = true
	wrappedWriter.ResponseWriter.WriteHeader(statusCode)
}

func (wrappedWriter *statusCapturingResponseWriter) Write(bodyBytes []byte) (int, error) {
	if !wrappedWriter.headerWasWritten {
		wrappedWriter.capturedStatusCode = http.StatusOK
		wrappedWriter.headerWasWritten = true
	}
	return wrappedWriter.ResponseWriter.Write(bodyBytes)
}

// WithRequestLogging wraps an http.Handler so every request logs one
// structured line ("method", "path", "statusCode", "durationMs") via
// slog.Default() after the handler completes.
func WithRequestLogging(nextHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		startTime := time.Now()
		wrappedWriter := &statusCapturingResponseWriter{ResponseWriter: responseWriter}
		nextHandler.ServeHTTP(wrappedWriter, request)
		slog.Info("http_request",
			"method", request.Method,
			"path", request.URL.Path,
			"statusCode", wrappedWriter.capturedStatusCode,
			"durationMs", time.Since(startTime).Milliseconds(),
		)
	})
}
