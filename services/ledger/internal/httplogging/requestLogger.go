// Package httplogging provides structured (JSON, via log/slog) HTTP
// access logging — one log line per request with the method, path,
// status code, and duration, machine-parseable instead of the ad hoc
// log.Printf strings this repo used everywhere before. See FEATURES.md
// §13 ("Structured logging + centralized log aggregation").
//
// This package provides the HTTP ACCESS log line specifically
// ("http_request": method/path/statusCode/durationMs). Every other
// log.Printf/log.Fatalf business-event line scattered through each
// service's cmd/server/main.go (settlement failures, fail-open warnings,
// freeze/unfreeze events, etc.) is ALSO now structured JSON, for free —
// slog.SetDefault(slog.New(slog.NewJSONHandler(...))) in each main()
// redirects the whole stdlib `log` package through the same slog
// handler (a documented log/slog behavior), so none of those call sites
// needed to be rewritten individually. Verified live — see
// docs/BUILD_LOG.md.
//
// TODO(real build): "centralized" is the other half of FEATURES.md §13's
// item — these JSON lines currently just go to stdout, not to a real log
// aggregation backend (e.g. Loki/ELK). That's an infra concern outside
// this skeleton's reach without Docker.
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
		// Mirrors http.ResponseWriter's own documented default — a Write
		// without a prior WriteHeader implicitly sends 200.
		wrappedWriter.capturedStatusCode = http.StatusOK
		wrappedWriter.headerWasWritten = true
	}
	return wrappedWriter.ResponseWriter.Write(bodyBytes)
}

// WithRequestLogging wraps an http.Handler so every request logs one
// structured line — "method", "path", "statusCode", "durationMs" — via
// slog.Default() after the handler completes. Wrap this as close to the
// underlying mux as convenient; middleware layered outside it (e.g. CORS)
// still gets its own behavior for requests it short-circuits (like an
// OPTIONS preflight), which then never reach this wrapper — intentional,
// since only requests that actually reached application logic are worth
// an access-log line.
func WithRequestLogging(nextHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		requestStartTime := time.Now()
		wrappedWriter := &statusCapturingResponseWriter{ResponseWriter: responseWriter, capturedStatusCode: http.StatusOK}

		nextHandler.ServeHTTP(wrappedWriter, request)

		slog.Info("http_request",
			"method", request.Method,
			"path", request.URL.Path,
			"statusCode", wrappedWriter.capturedStatusCode,
			"durationMs", time.Since(requestStartTime).Milliseconds(),
		)
	})
}
