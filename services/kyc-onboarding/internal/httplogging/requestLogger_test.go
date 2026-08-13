package httplogging

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWithRequestLoggingPreservesTheHandlersResponse(testInstance *testing.T) {
	innerHandler := http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusTeapot)
		_, _ = responseWriter.Write([]byte("hello"))
	})

	wrappedHandler := WithRequestLogging(innerHandler)
	recordedResponse := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(recordedResponse, httptest.NewRequest(http.MethodGet, "/whatever", nil))

	if recordedResponse.Code != http.StatusTeapot {
		testInstance.Fatalf("expected status %d to pass through unchanged, got %d", http.StatusTeapot, recordedResponse.Code)
	}
	if recordedResponse.Body.String() != "hello" {
		testInstance.Fatalf("expected body to pass through unchanged, got %q", recordedResponse.Body.String())
	}
}

func TestWithRequestLoggingLogsMethodPathAndCapturedStatusCode(testInstance *testing.T) {
	var logOutputBuffer bytes.Buffer
	previousDefaultLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logOutputBuffer, nil)))
	defer slog.SetDefault(previousDefaultLogger)

	innerHandler := http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusCreated)
	})

	wrappedHandler := WithRequestLogging(innerHandler)
	wrappedHandler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/journal-entries", nil))

	loggedLine := logOutputBuffer.String()
	for _, expectedFragment := range []string{`"method":"POST"`, `"path":"/journal-entries"`, `"statusCode":201`} {
		if !strings.Contains(loggedLine, expectedFragment) {
			testInstance.Fatalf("expected log line to contain %q, got: %s", expectedFragment, loggedLine)
		}
	}
}

func TestWithRequestLoggingDefaultsToStatus200WhenHandlerNeverCallsWriteHeader(testInstance *testing.T) {
	innerHandler := http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		_, _ = responseWriter.Write([]byte("no explicit WriteHeader call"))
	})

	wrappedHandler := WithRequestLogging(innerHandler)
	recordedResponse := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(recordedResponse, httptest.NewRequest(http.MethodGet, "/health", nil))

	if recordedResponse.Code != http.StatusOK {
		testInstance.Fatalf("expected implicit 200, got %d", recordedResponse.Code)
	}
}
