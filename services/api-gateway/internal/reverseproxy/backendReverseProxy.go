// Package reverseproxy is the real reverse-proxy core of api-gateway:
// it forwards an incoming request to the correct real backend service
// (ledger, oms-gateway, mutual-funds, market-data, quant-engine) based
// on a configured URL-prefix-to-backend map, using Go's real, standard
// httputil.ReverseProxy (not a hand-rolled proxy — this is exactly the
// kind of infrastructure plumbing the standard library already solves
// correctly).
package reverseproxy

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
)

// BackendRoute maps one URL path prefix to the backend base URL that
// prefix should be proxied to. PathPrefix is stripped from the
// forwarded request path before it reaches the backend, so a client
// calling api-gateway's `GET /ledger/accounts/acct-1/balance` reaches
// the real ledger's `GET /accounts/acct-1/balance`.
type BackendRoute struct {
	PathPrefix     string
	BackendBaseUrl string
}

// BackendReverseProxy dispatches to the longest-matching configured
// BackendRoute for each incoming request path.
type BackendReverseProxy struct {
	routes []BackendRoute // sorted longest-prefix-first
}

// NewBackendReverseProxy returns a proxy configured with routes. Routes
// are sorted internally by prefix length (longest first) so a more
// specific prefix always wins over a shorter, more general one.
func NewBackendReverseProxy(routes []BackendRoute) *BackendReverseProxy {
	sortedRoutes := make([]BackendRoute, len(routes))
	copy(sortedRoutes, routes)
	sort.Slice(sortedRoutes, func(i, j int) bool {
		return len(sortedRoutes[i].PathPrefix) > len(sortedRoutes[j].PathPrefix)
	})
	return &BackendReverseProxy{routes: sortedRoutes}
}

// MatchRoute returns the BackendRoute whose PathPrefix matches
// requestPath, if any.
func (proxy *BackendReverseProxy) MatchRoute(requestPath string) (BackendRoute, bool) {
	for _, route := range proxy.routes {
		if strings.HasPrefix(requestPath, route.PathPrefix) {
			return route, true
		}
	}
	return BackendRoute{}, false
}

// ServeHTTP implements http.Handler: it finds the matching route for
// the request path, strips the prefix, and forwards via a real
// httputil.ReverseProxy to the backend. Returns 404 with a clear body
// if no route matches, and 502 with a clear body if the backend itself
// is unreachable (real error surfaced to the caller, not a silent
// hang).
func (proxy *BackendReverseProxy) ServeHTTP(responseWriter http.ResponseWriter, request *http.Request) {
	route, matched := proxy.MatchRoute(request.URL.Path)
	if !matched {
		http.Error(responseWriter, `{"error":"no backend route configured for this path"}`, http.StatusNotFound)
		return
	}

	backendUrl, parseErr := url.Parse(route.BackendBaseUrl)
	if parseErr != nil {
		http.Error(responseWriter, `{"error":"misconfigured backend base URL"}`, http.StatusInternalServerError)
		return
	}

	singleHostProxy := httputil.NewSingleHostReverseProxy(backendUrl)
	originalDirector := singleHostProxy.Director
	singleHostProxy.Director = func(proxiedRequest *http.Request) {
		originalDirector(proxiedRequest)
		proxiedRequest.URL.Path = strings.TrimPrefix(proxiedRequest.URL.Path, route.PathPrefix)
		if !strings.HasPrefix(proxiedRequest.URL.Path, "/") {
			proxiedRequest.URL.Path = "/" + proxiedRequest.URL.Path
		}
	}
	singleHostProxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("reverseproxy: backend %s unreachable for %s: %v", route.BackendBaseUrl, r.URL.Path, err)
		http.Error(w, `{"error":"backend service unreachable"}`, http.StatusBadGateway)
	}

	singleHostProxy.ServeHTTP(responseWriter, request)
}
