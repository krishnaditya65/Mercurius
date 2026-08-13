package reverseproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMatchRouteFindsAMatchingPrefix(t *testing.T) {
	proxy := NewBackendReverseProxy([]BackendRoute{
		{PathPrefix: "/ledger", BackendBaseUrl: "http://127.0.0.1:8082"},
	})
	route, matched := proxy.MatchRoute("/ledger/accounts/acct-1")
	if !matched {
		t.Fatalf("expected a match")
	}
	if route.BackendBaseUrl != "http://127.0.0.1:8082" {
		t.Fatalf("unexpected backend: %s", route.BackendBaseUrl)
	}
}

func TestMatchRouteReturnsFalseForUnmatchedPath(t *testing.T) {
	proxy := NewBackendReverseProxy([]BackendRoute{{PathPrefix: "/ledger", BackendBaseUrl: "http://127.0.0.1:8082"}})
	_, matched := proxy.MatchRoute("/nonexistent")
	if matched {
		t.Fatalf("expected no match for an unconfigured path")
	}
}

func TestMatchRoutePrefersLongestMatchingPrefix(t *testing.T) {
	proxy := NewBackendReverseProxy([]BackendRoute{
		{PathPrefix: "/oms", BackendBaseUrl: "http://short"},
		{PathPrefix: "/oms/dma", BackendBaseUrl: "http://long"},
	})
	route, _ := proxy.MatchRoute("/oms/dma/sessions")
	if route.BackendBaseUrl != "http://long" {
		t.Fatalf("expected the longer, more specific prefix to win, got %s", route.BackendBaseUrl)
	}
}

func TestServeHttpForwardsToRealBackendWithPrefixStripped(t *testing.T) {
	var receivedPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer backend.Close()

	proxy := NewBackendReverseProxy([]BackendRoute{{PathPrefix: "/ledger", BackendBaseUrl: backend.URL}})
	gateway := httptest.NewServer(proxy)
	defer gateway.Close()

	response, err := http.Get(gateway.URL + "/ledger/accounts/acct-1/balance")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)

	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", response.StatusCode, body)
	}
	if receivedPath != "/accounts/acct-1/balance" {
		t.Fatalf("expected backend to receive prefix-stripped path, got %q", receivedPath)
	}
}

func TestServeHttpReturns404ForUnmatchedPath(t *testing.T) {
	proxy := NewBackendReverseProxy([]BackendRoute{{PathPrefix: "/ledger", BackendBaseUrl: "http://127.0.0.1:1"}})
	gateway := httptest.NewServer(proxy)
	defer gateway.Close()

	response, _ := http.Get(gateway.URL + "/nope")
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", response.StatusCode)
	}
}

func TestServeHttpReturns502WhenBackendUnreachable(t *testing.T) {
	proxy := NewBackendReverseProxy([]BackendRoute{{PathPrefix: "/ledger", BackendBaseUrl: "http://127.0.0.1:1"}})
	gateway := httptest.NewServer(proxy)
	defer gateway.Close()

	response, _ := http.Get(gateway.URL + "/ledger/accounts")
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502 for an unreachable backend, got %d", response.StatusCode)
	}
}

func TestServeHttpStripsPrefixLeavingRootSlash(t *testing.T) {
	var receivedPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
	}))
	defer backend.Close()

	proxy := NewBackendReverseProxy([]BackendRoute{{PathPrefix: "/ledger", BackendBaseUrl: backend.URL}})
	gateway := httptest.NewServer(proxy)
	defer gateway.Close()

	http.Get(gateway.URL + "/ledger")
	if receivedPath != "/" {
		t.Fatalf("expected stripped path to normalize to '/', got %q", receivedPath)
	}
}

func TestNewBackendReverseProxySortsLongestPrefixFirstRegardlessOfInputOrder(t *testing.T) {
	proxy := NewBackendReverseProxy([]BackendRoute{
		{PathPrefix: "/a", BackendBaseUrl: "short"},
		{PathPrefix: "/a/b/c", BackendBaseUrl: "longest"},
		{PathPrefix: "/a/b", BackendBaseUrl: "medium"},
	})
	route, _ := proxy.MatchRoute("/a/b/c/d")
	if route.BackendBaseUrl != "longest" {
		t.Fatalf("expected longest prefix to be matched first, got %s", route.BackendBaseUrl)
	}
}
