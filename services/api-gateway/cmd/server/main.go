// Mercurius / api-gateway
//
// A real reverse-proxy / API-management layer sitting in front of the
// platform's other services (ledger, oms-gateway, mutual-funds,
// market-data, quant-engine). Home for FEATURES.md §13/§18's
// platform-and-ecosystem tooling that doesn't belong inside any single
// backend service: SLO alerting, secrets-provider abstraction, rate
// limiting + quota tiers, developer API-key management + sandbox,
// webhooks, white-label tenant config, TCA reporting, and an
// illustrative Account Aggregator merge view. See README.md for the
// full per-item breakdown and honest gaps.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"mercurius/apiGateway/internal/accountaggregator"
	"mercurius/apiGateway/internal/apikeymanager"
	"mercurius/apiGateway/internal/authmiddleware"
	"mercurius/apiGateway/internal/ratelimiter"
	"mercurius/apiGateway/internal/reverseproxy"
	"mercurius/apiGateway/internal/sloalerting"
	"mercurius/apiGateway/internal/tca"
	"mercurius/apiGateway/internal/tenantconfig"
	"mercurius/apiGateway/internal/webhookdelivery"
)

// errYouCanOnlyActOnYourOwnAccount is the standard 403 body for every
// account-scoped route below whose request carries an account
// identifier that doesn't match the authenticated caller's own account.
const errYouCanOnlyActOnYourOwnAccount = `{"errorMessage":"you can only act on your own account"}`

// respondForbiddenOwnAccountOnly writes the standard 403 for an
// account-ownership mismatch — checked before a handler does anything
// else with the request.
func respondForbiddenOwnAccountOnly(responseWriter http.ResponseWriter) {
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(http.StatusForbidden)
	_, _ = responseWriter.Write([]byte(errYouCanOnlyActOnYourOwnAccount))
}

func envOrDefault(envVarName, defaultValue string) string {
	if value := os.Getenv(envVarName); value != "" {
		return value
	}
	return defaultValue
}

// withAllowListedCorsForDevelopment is api-gateway's first-ever CORS
// middleware (previously there was none at all — see docs/BUILD_LOG.md
// entry 83, which flagged the gap by grepping the whole tree for
// Access-Control/Cors/CORS and finding zero matches in this service).
// It reads a comma-separated CORS_ALLOWED_ORIGINS env var (defaulting to
// the two local dev frontends below when unset) and only ever echoes
// back an exact allow-listed Origin — never `*` — because this gateway
// now issues Access-Control-Allow-Credentials: true, and the CORS spec
// forbids pairing a wildcard origin with credentialed requests. A
// request whose Origin doesn't match anything on the allow-list gets no
// CORS headers at all, same as this pattern in oms-gateway/backoffice/
// kyc-onboarding.
func withAllowListedCorsForDevelopment(nextHandler http.Handler) http.Handler {
	allowedOrigins := parseCommaSeparatedOrigins(envOrDefault("CORS_ALLOWED_ORIGINS", "http://localhost:3000,http://localhost:3100"))

	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		requestOrigin := request.Header.Get("Origin")
		if requestOrigin != "" && allowedOrigins[requestOrigin] {
			responseWriter.Header().Set("Access-Control-Allow-Origin", requestOrigin)
			responseWriter.Header().Set("Access-Control-Allow-Credentials", "true")
			responseWriter.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			responseWriter.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		}

		if request.Method == http.MethodOptions {
			responseWriter.WriteHeader(http.StatusNoContent)
			return
		}

		nextHandler.ServeHTTP(responseWriter, request)
	})
}

func parseCommaSeparatedOrigins(commaSeparated string) map[string]bool {
	origins := make(map[string]bool)
	for _, origin := range strings.Split(commaSeparated, ",") {
		trimmed := strings.TrimSpace(origin)
		if trimmed != "" {
			origins[trimmed] = true
		}
	}
	return origins
}

func main() {
	ledgerBaseUrl := envOrDefault("LEDGER_BASE_URL", "http://127.0.0.1:8082")
	omsGatewayBaseUrl := envOrDefault("OMS_GATEWAY_BASE_URL", "http://127.0.0.1:8081")
	mutualFundsBaseUrl := envOrDefault("MUTUAL_FUNDS_BASE_URL", "http://127.0.0.1:8087")
	marketDataBaseUrl := envOrDefault("MARKET_DATA_BASE_URL", "http://127.0.0.1:9103")
	quantEngineBaseUrl := envOrDefault("QUANT_ENGINE_BASE_URL", "http://127.0.0.1:8085")
	matchingEngineTcpAddr := envOrDefault("MATCHING_ENGINE_TCP_ADDRESS", "127.0.0.1:9101")

	apiKeyManager := apikeymanager.NewApiKeyManager()
	globalRateLimiter := ratelimiter.NewTokenBucketRateLimiter(ratelimiter.DefaultTierLimits)
	tenantRegistry := tenantconfig.NewTenantRegistry()
	webhookManager := webhookdelivery.NewWebhookDeliveryManager(webhookdelivery.DefaultRetryPolicy)
	sloEvaluator := sloalerting.NewSloAlertEvaluator(sloalerting.DefaultThresholds)

	// Real background pollers against real running services — see
	// README.md for exactly which endpoints these hit.
	metricsPoller := sloalerting.NewMetricsPoller(sloEvaluator, omsGatewayBaseUrl, marketDataBaseUrl, matchingEngineTcpAddr)
	auditTrailEventSource := webhookdelivery.NewAuditTrailEventSource(omsGatewayBaseUrl, webhookManager)

	pollIntervalSeconds := 10
	if rawInterval := os.Getenv("SLO_POLL_INTERVAL_SECONDS"); rawInterval != "" {
		if parsed, parseErr := strconv.Atoi(rawInterval); parseErr == nil && parsed > 0 {
			pollIntervalSeconds = parsed
		}
	}
	stopChannel := make(chan struct{})
	go metricsPoller.RunForever(time.Duration(pollIntervalSeconds)*time.Second, stopChannel)
	go auditTrailEventSource.RunForever(time.Duration(pollIntervalSeconds)*time.Second, stopChannel)

	// The real reverse proxy — every backend service is reachable
	// through api-gateway under its own path prefix.
	backendProxy := reverseproxy.NewBackendReverseProxy([]reverseproxy.BackendRoute{
		{PathPrefix: "/ledger", BackendBaseUrl: ledgerBaseUrl},
		{PathPrefix: "/oms", BackendBaseUrl: omsGatewayBaseUrl},
		{PathPrefix: "/mutual-funds", BackendBaseUrl: mutualFundsBaseUrl},
		{PathPrefix: "/market-data", BackendBaseUrl: marketDataBaseUrl},
		{PathPrefix: "/quant-engine", BackendBaseUrl: quantEngineBaseUrl},
	})

	signingSecret := authmiddleware.SigningSecretFromEnv()

	httpRequestMultiplexer := http.NewServeMux()

	httpRequestMultiplexer.HandleFunc("/health", func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = responseWriter.Write([]byte(`{"status":"ok","service":"api-gateway"}`))
	})

	// FEATURES.md §13 item 1: SLO alerting — GET /alerts. Operator-facing
	// (no account identifier anywhere in the request/response — it's a
	// platform-wide ops view of SLO breaches), so this is admin-only
	// rather than account-scoped.
	httpRequestMultiplexer.HandleFunc("/alerts", authmiddleware.RequireRole(signingSecret, authmiddleware.RoleAdmin, buildListAlertsHandler(sloEvaluator)))

	// FEATURES.md §18 item 8: developer API-key management. Both the
	// POST-issue and GET-list cases carry an accountIdentifier
	// (body/query respectively) — account-owner only, any authenticated
	// role, ownership verified inside the handler before doing anything
	// else.
	httpRequestMultiplexer.HandleFunc("/developer/api-keys", authmiddleware.RequireAuth(signingSecret, buildApiKeysHandler(apiKeyManager)))
	// The revoke request body only carries apiKeyValue, not an account
	// id directly — but the key record itself (looked up via
	// ValidateApiKey) carries the owning AccountIdentifier, so ownership
	// IS structurally verifiable here; see buildRevokeApiKeyHandler.
	httpRequestMultiplexer.HandleFunc("/developer/api-keys/revoke", authmiddleware.RequireAuth(signingSecret, buildRevokeApiKeyHandler(apiKeyManager)))

	// FEATURES.md §18 item 10: white-label tenant config — registering/
	// listing tenants is a platform/operator-admin action, not scoped to
	// any one account, so admin-only.
	httpRequestMultiplexer.HandleFunc("/tenants", authmiddleware.RequireRole(signingSecret, authmiddleware.RoleAdmin, buildTenantsHandler(tenantRegistry)))

	// FEATURES.md §18 item 9: webhooks. /subscribe carries an
	// accountIdentifier field, so it's account-scoped: account-owner
	// only. /deliveries, by contrast, has no account filter at all —
	// manager.DeliveryHistory() returns EVERY account's delivery history
	// with no way to scope it to the caller — so until that filtering
	// exists, treating it as account-scoped would leak other accounts'
	// webhook history; admin-only instead.
	httpRequestMultiplexer.HandleFunc("/webhooks/subscribe", authmiddleware.RequireAuth(signingSecret, buildWebhookSubscribeHandler(webhookManager)))
	httpRequestMultiplexer.HandleFunc("/webhooks/deliveries", authmiddleware.RequireRole(signingSecret, authmiddleware.RoleAdmin, buildWebhookDeliveriesHandler(webhookManager)))

	// FEATURES.md §18 item 12: TCA reporting — scoped to a single
	// account via the accountId query parameter, so account-owner only.
	httpRequestMultiplexer.HandleFunc("/tca/report", authmiddleware.RequireAuth(signingSecret, buildTcaReportHandler()))

	// FEATURES.md §18 item 13: illustrative Account Aggregator merge —
	// scoped to a single account via the accountId query parameter, so
	// account-owner only.
	httpRequestMultiplexer.HandleFunc("/account-aggregator/net-worth", authmiddleware.RequireAuth(signingSecret, buildAccountAggregatorHandler(omsGatewayBaseUrl, mutualFundsBaseUrl)))

	// Everything else (any path not matched above) is proxied through
	// to the real backend services, gated by rate limiting + API-key
	// validation, AND (see buildProxyAccessGate) either a valid JWT or a
	// valid API key -- this was previously the one route in this file
	// with no authentication check at all, a live bypass around every
	// other route's auth.
	httpRequestMultiplexer.Handle("/", buildProxyAccessGate(signingSecret, apiKeyManager, backendProxy))

	rateLimitedHandler := buildRateLimitingMiddleware(globalRateLimiter, apiKeyManager, httpRequestMultiplexer)
	corsWrappedHandler := withAllowListedCorsForDevelopment(rateLimitedHandler)

	listenAddress := envOrDefault("API_GATEWAY_LISTEN_ADDRESS", ":8089")
	log.Printf("api-gateway listening on %s (proxying ledger=%s oms-gateway=%s mutual-funds=%s market-data=%s quant-engine=%s)",
		listenAddress, ledgerBaseUrl, omsGatewayBaseUrl, mutualFundsBaseUrl, marketDataBaseUrl, quantEngineBaseUrl)
	if serverStartupError := http.ListenAndServe(listenAddress, corsWrappedHandler); serverStartupError != nil {
		log.Fatalf("api-gateway failed to start: %v", serverStartupError)
	}
	close(stopChannel)
}

// buildRateLimitingMiddleware is FEATURES.md §13/§18's "API gateway
// rate limiting, quota tiers (retail vs. institutional)" enforced on
// EVERY request through this gateway. Requests carrying a valid
// `X-Api-Key` header are rate-limited per that key's own issued tier;
// requests with no API key (e.g. the platform's own web/desktop client
// traffic, or unauthenticated calls to endpoints like /health) are
// rate-limited per SOURCE IP ADDRESS (see remoteAddressKey) rather than
// one shared bucket for all anonymous traffic — a single constant key
// meant every anonymous caller on the internet shared one RETAIL-tier
// bucket, so any one anonymous client could exhaust everyone else's
// quota. Still generous enough for normal traffic, but now a real
// per-source limit rather than a global one. An INVALID (present but
// unrecognized or revoked) API key is rejected outright with 401 —
// failing closed, never silently falling back to the anonymous bucket.
func buildRateLimitingMiddleware(limiter *ratelimiter.TokenBucketRateLimiter, keyManager *apikeymanager.ApiKeyManager, next http.Handler) http.Handler {
	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/health" {
			next.ServeHTTP(responseWriter, request)
			return
		}

		rateLimitKey := remoteAddressKey(request)
		tier := ratelimiter.RateLimitTierRetail

		if apiKeyValue := request.Header.Get("X-Api-Key"); apiKeyValue != "" {
			issuedKey, validateErr := keyManager.ValidateApiKey(apiKeyValue)
			if validateErr != nil {
				http.Error(responseWriter, `{"error":"invalid or revoked API key"}`, http.StatusUnauthorized)
				return
			}
			rateLimitKey = issuedKey.ApiKeyValue
			tier = issuedKey.RateLimitTier
			if issuedKey.IsSandboxKey {
				request.Header.Set("X-Sandbox", "true")
			}
		}

		if !limiter.AllowRequest(rateLimitKey, tier) {
			responseWriter.Header().Set("Retry-After", "1")
			http.Error(responseWriter, `{"error":"rate limit exceeded for this API key's tier"}`, http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(responseWriter, request)
	})
}

// remoteAddressKey strips the port off request.RemoteAddr (host:port) so
// the rate limiter keys on just the address — falls back to the raw
// value if it doesn't parse as host:port (e.g. in some test harnesses).
// Matches services/auth's cmd/server/main.go remoteAddressKey exactly,
// including its documented limitation: this reads request.RemoteAddr
// directly, which is the load balancer's/reverse proxy's own address in
// any real deployment, not the actual client — needs real
// X-Forwarded-For handling (with the trusted-proxy-hop caveats that come
// with it) before this is meaningful behind anything but a direct
// connection.
func remoteAddressKey(request *http.Request) string {
	host, _, splitError := net.SplitHostPort(request.RemoteAddr)
	if splitError != nil {
		return request.RemoteAddr
	}
	return host
}

// buildProxyAccessGate closes the one route in this file that used to
// have zero authentication: the reverse-proxy catch-all ("/"). Every
// other route in this file is wrapped in authmiddleware.RequireAuth/
// RequireRole; this one previously had only the OPTIONAL X-Api-Key
// rate-limit check from buildRateLimitingMiddleware, so a fully
// anonymous request (no JWT, no API key at all) could reach every
// backend service proxied through here.
//
// The proxy's access model isn't purely JWT-based, though — README.md
// documents X-Api-Key as this gateway's actual developer/API-management
// mechanism (issued via /developer/api-keys, rate-limited per its own
// tier), and buildRateLimitingMiddleware's own doc comment already
// distinguishes "the platform's own web/desktop client traffic" (which
// authenticates with a JWT, not an API key) from developer/programmatic
// API traffic (which authenticates with an API key, and may have no
// user JWT at all). So this gate accepts EITHER:
//   - a request carrying X-Api-Key: by the time this gate runs,
//     buildRateLimitingMiddleware (which wraps the whole mux, including
//     this route) has already called keyManager.ValidateApiKey and
//     rejected an invalid/revoked key with 401 -- so a present X-Api-Key
//     here is already known-valid, and this gate just lets it through.
//   - a request with no X-Api-Key: this is where the actual bypass was
//     -- it now runs the same authmiddleware.RequireAuth every other
//     route uses, so an anonymous request with neither an API key nor a
//     JWT gets a 401, not a free pass to every backend service.
func buildProxyAccessGate(signingSecret []byte, keyManager *apikeymanager.ApiKeyManager, next http.Handler) http.Handler {
	requireJwtAuth := authmiddleware.RequireAuth(signingSecret, next.ServeHTTP)

	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Api-Key") != "" {
			next.ServeHTTP(responseWriter, request)
			return
		}
		requireJwtAuth(responseWriter, request)
	})
}

func buildListAlertsHandler(evaluator *sloalerting.SloAlertEvaluator) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, _ *http.Request) {
		writeJson(responseWriter, http.StatusOK, evaluator.AllAlerts())
	}
}

type apiKeyIssuanceWireRequest struct {
	AccountIdentifier string `json:"accountIdentifier"`
	TenantIdentifier  string `json:"tenantIdentifier,omitempty"`
	RateLimitTier     string `json:"rateLimitTier,omitempty"`
	IsSandboxKey      bool   `json:"isSandboxKey,omitempty"`
}

func buildApiKeysHandler(manager *apikeymanager.ApiKeyManager) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodPost:
			var wireRequest apiKeyIssuanceWireRequest
			if decodeErr := json.NewDecoder(request.Body).Decode(&wireRequest); decodeErr != nil {
				http.Error(responseWriter, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
				return
			}
			if authenticatedAccountIdentifier, _ := authmiddleware.AuthenticatedAccountIdentifier(request); authenticatedAccountIdentifier != wireRequest.AccountIdentifier {
				respondForbiddenOwnAccountOnly(responseWriter)
				return
			}
			issued, issueErr := manager.IssueApiKey(apikeymanager.ApiKeyIssuanceRequest{
				AccountIdentifier: wireRequest.AccountIdentifier,
				TenantIdentifier:  wireRequest.TenantIdentifier,
				RateLimitTier:     ratelimiter.RateLimitTier(wireRequest.RateLimitTier),
				IsSandboxKey:      wireRequest.IsSandboxKey,
			})
			if issueErr != nil {
				http.Error(responseWriter, `{"error":"`+issueErr.Error()+`"}`, http.StatusBadRequest)
				return
			}
			writeJson(responseWriter, http.StatusCreated, issued)
		case http.MethodGet:
			accountIdentifier := request.URL.Query().Get("accountIdentifier")
			if authenticatedAccountIdentifier, _ := authmiddleware.AuthenticatedAccountIdentifier(request); authenticatedAccountIdentifier != accountIdentifier {
				respondForbiddenOwnAccountOnly(responseWriter)
				return
			}
			writeJson(responseWriter, http.StatusOK, manager.ListApiKeysForAccount(accountIdentifier))
		default:
			http.Error(responseWriter, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		}
	}
}

func buildRevokeApiKeyHandler(manager *apikeymanager.ApiKeyManager) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		var wireRequest struct {
			ApiKeyValue string `json:"apiKeyValue"`
		}
		if decodeErr := json.NewDecoder(request.Body).Decode(&wireRequest); decodeErr != nil {
			http.Error(responseWriter, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
			return
		}
		// The wire request only carries apiKeyValue, not an account id —
		// but the key record itself knows who owns it, so look that up
		// FIRST and verify ownership before revoking anything.
		// ValidateApiKey still returns the record (with ErrApiKeyRevoked)
		// for an already-revoked key, which is fine for an ownership
		// check; only ErrApiKeyNotFound means there's no record to check
		// ownership against at all.
		existingRecord, lookupErr := manager.ValidateApiKey(wireRequest.ApiKeyValue)
		if lookupErr != nil && !errors.Is(lookupErr, apikeymanager.ErrApiKeyRevoked) {
			http.Error(responseWriter, `{"error":"`+lookupErr.Error()+`"}`, http.StatusNotFound)
			return
		}
		if authenticatedAccountIdentifier, _ := authmiddleware.AuthenticatedAccountIdentifier(request); authenticatedAccountIdentifier != existingRecord.AccountIdentifier {
			respondForbiddenOwnAccountOnly(responseWriter)
			return
		}
		if revokeErr := manager.RevokeApiKey(wireRequest.ApiKeyValue); revokeErr != nil {
			http.Error(responseWriter, `{"error":"`+revokeErr.Error()+`"}`, http.StatusNotFound)
			return
		}
		writeJson(responseWriter, http.StatusOK, map[string]bool{"wasRevoked": true})
	}
}

type tenantRegistrationWireRequest struct {
	TenantIdentifier string                        `json:"tenantIdentifier"`
	Branding         tenantconfig.BrandingMetadata `json:"branding"`
}

func buildTenantsHandler(registry *tenantconfig.TenantRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodPost:
			var wireRequest tenantRegistrationWireRequest
			if decodeErr := json.NewDecoder(request.Body).Decode(&wireRequest); decodeErr != nil {
				http.Error(responseWriter, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
				return
			}
			tenant, registerErr := registry.RegisterTenant(wireRequest.TenantIdentifier, wireRequest.Branding, nil)
			if registerErr != nil {
				http.Error(responseWriter, `{"error":"`+registerErr.Error()+`"}`, http.StatusBadRequest)
				return
			}
			writeJson(responseWriter, http.StatusCreated, tenant)
		case http.MethodGet:
			writeJson(responseWriter, http.StatusOK, registry.ListTenants())
		default:
			http.Error(responseWriter, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		}
	}
}

type webhookSubscribeWireRequest struct {
	AccountIdentifier string `json:"accountIdentifier"`
	EventType         string `json:"eventType,omitempty"`
	TargetUrl         string `json:"targetUrl"`
}

func buildWebhookSubscribeHandler(manager *webhookdelivery.WebhookDeliveryManager) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		var wireRequest webhookSubscribeWireRequest
		if decodeErr := json.NewDecoder(request.Body).Decode(&wireRequest); decodeErr != nil {
			http.Error(responseWriter, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
			return
		}
		if authenticatedAccountIdentifier, _ := authmiddleware.AuthenticatedAccountIdentifier(request); authenticatedAccountIdentifier != wireRequest.AccountIdentifier {
			respondForbiddenOwnAccountOnly(responseWriter)
			return
		}
		subscription, subscribeErr := manager.RegisterSubscription(
			wireRequest.AccountIdentifier,
			webhookdelivery.EventType(wireRequest.EventType),
			wireRequest.TargetUrl,
		)
		if subscribeErr != nil {
			http.Error(responseWriter, `{"error":"`+subscribeErr.Error()+`"}`, http.StatusBadRequest)
			return
		}
		writeJson(responseWriter, http.StatusCreated, subscription)
	}
}

func buildWebhookDeliveriesHandler(manager *webhookdelivery.WebhookDeliveryManager) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, _ *http.Request) {
		writeJson(responseWriter, http.StatusOK, manager.DeliveryHistory())
	}
}

func buildTcaReportHandler() http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, `{"error":"accountId query parameter is required"}`, http.StatusBadRequest)
			return
		}
		if authenticatedAccountIdentifier, _ := authmiddleware.AuthenticatedAccountIdentifier(request); authenticatedAccountIdentifier != accountIdentifier {
			respondForbiddenOwnAccountOnly(responseWriter)
			return
		}
		// See internal/tca/fillDataSource.go for exactly why this uses
		// fixture data today and what a real build needs to add.
		orders := tca.FixtureFilledOrdersForAccount(accountIdentifier)
		report := tca.BuildAccountReport(accountIdentifier, orders)
		writeJson(responseWriter, http.StatusOK, map[string]any{
			"report":           report,
			"dataSourceIsLive": false,
			"dataSourceCaveat": tca.ErrLiveFillHistoryNotYetAvailable.Error(),
		})
	}
}

// omsPositionsWireResponse mirrors oms-gateway's real GET /positions
// response shape exactly (see that service's buildPositionsHandler /
// backoffice's omsgatewayclient.PositionsWireResponse, which mirrors the
// same thing): a net QUANTITY per instrument symbol. oms-gateway's
// /positions response has no market-value field at all.
type omsPositionsWireResponse struct {
	AccountIdentifier             string           `json:"accountIdentifier"`
	NetQuantityByInstrumentSymbol map[string]int64 `json:"netQuantityByInstrumentSymbol"`
}

// mutualFundsHoldingWireResponse mirrors mutual-funds' real GET
// /holdings response shape exactly (see that service's
// holdingWireFormat) — unit counts per scheme, keyed by schemeId. There
// is no schemeName or currentValueInMinorUnits field: mutual-funds
// never returns a priced value for a holding, only units.
type mutualFundsHoldingWireResponse struct {
	SchemeId                   string  `json:"schemeId"`
	TotalUnits                 float64 `json:"totalUnits"`
	UnitsReservedForRedemption float64 `json:"unitsReservedForRedemption"`
	AvailableUnits             float64 `json:"availableUnits"`
}

func buildAccountAggregatorHandler(omsGatewayBaseUrl, mutualFundsBaseUrl string) http.HandlerFunc {
	httpClient := &http.Client{Timeout: 5 * time.Second}

	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, `{"error":"accountId query parameter is required"}`, http.StatusBadRequest)
			return
		}
		if authenticatedAccountIdentifier, _ := authmiddleware.AuthenticatedAccountIdentifier(request); authenticatedAccountIdentifier != accountIdentifier {
			respondForbiddenOwnAccountOnly(responseWriter)
			return
		}

		// oms-gateway's /positions route requires authmiddleware.RequireAuth
		// — forward the caller's own bearer token verbatim (same
		// "acting as the authenticated user" fix as backoffice's
		// omsgatewayclient.FetchPositions) rather than calling anonymously.
		callerAuthorizationHeader := request.Header.Get("Authorization")

		var platformHoldings []accountaggregator.PlatformHolding
		var dataSourceCaveats []string

		if positions, err := fetchOmsGatewayPositions(httpClient, omsGatewayBaseUrl, accountIdentifier, callerAuthorizationHeader); err != nil {
			log.Printf("accountaggregator: failed to fetch oms-gateway positions for account %s: %v", accountIdentifier, err)
			dataSourceCaveats = append(dataSourceCaveats, "oms-gateway positions unavailable: "+err.Error())
		} else if len(positions.NetQuantityByInstrumentSymbol) > 0 {
			for instrumentSymbol, netQuantity := range positions.NetQuantityByInstrumentSymbol {
				platformHoldings = append(platformHoldings, accountaggregator.PlatformHolding{
					SourceService:         "oms-gateway",
					InstrumentDescription: instrumentSymbol,
					UnitsHeld:             float64(netQuantity),
					ValuationUnavailable:  true,
				})
			}
			dataSourceCaveats = append(dataSourceCaveats, "oms-gateway positions report unit quantities only; market value is not yet available from that endpoint")
		}

		if holdings, err := fetchMutualFundsHoldings(httpClient, mutualFundsBaseUrl, accountIdentifier, callerAuthorizationHeader); err != nil {
			log.Printf("accountaggregator: failed to fetch mutual-funds holdings for account %s: %v", accountIdentifier, err)
			dataSourceCaveats = append(dataSourceCaveats, "mutual-funds holdings unavailable: "+err.Error())
		} else if len(holdings) > 0 {
			for _, holding := range holdings {
				platformHoldings = append(platformHoldings, accountaggregator.PlatformHolding{
					SourceService:         "mutual-funds",
					InstrumentDescription: holding.SchemeId,
					UnitsHeld:             holding.TotalUnits,
					ValuationUnavailable:  true,
				})
			}
			dataSourceCaveats = append(dataSourceCaveats, "mutual-funds holdings report unit counts only; market value is not yet available from that endpoint")
		}

		view := accountaggregator.BuildUnifiedNetWorthView(accountIdentifier, platformHoldings)
		view.DataSourceCaveats = dataSourceCaveats
		writeJson(responseWriter, http.StatusOK, view)
	}
}

// fetchOmsGatewayPositions is a real GET against oms-gateway's real
// /positions?accountId=... endpoint. Unlike the old inline call this
// replaces, it (a) forwards callerAuthorizationHeader so it isn't
// rejected by oms-gateway's authmiddleware.RequireAuth, and (b) checks
// the response status BEFORE attempting to decode, so a non-2xx (e.g.
// the 401 this call used to draw when unauthenticated) is a real,
// logged Go error rather than a silently-swallowed decode failure.
func fetchOmsGatewayPositions(httpClient *http.Client, omsGatewayBaseUrl, accountIdentifier, callerAuthorizationHeader string) (omsPositionsWireResponse, error) {
	httpRequest, buildErr := http.NewRequest(http.MethodGet, omsGatewayBaseUrl+"/positions?accountId="+accountIdentifier, nil)
	if buildErr != nil {
		return omsPositionsWireResponse{}, fmt.Errorf("could not build request: %w", buildErr)
	}
	if callerAuthorizationHeader != "" {
		httpRequest.Header.Set("Authorization", callerAuthorizationHeader)
	}

	response, requestErr := httpClient.Do(httpRequest)
	if requestErr != nil {
		return omsPositionsWireResponse{}, fmt.Errorf("could not reach oms-gateway: %w", requestErr)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return omsPositionsWireResponse{}, fmt.Errorf("oms-gateway returned HTTP %d for positions of account %s", response.StatusCode, accountIdentifier)
	}

	var positions omsPositionsWireResponse
	if decodeErr := json.NewDecoder(response.Body).Decode(&positions); decodeErr != nil {
		return omsPositionsWireResponse{}, fmt.Errorf("malformed positions response: %w", decodeErr)
	}
	return positions, nil
}

// fetchMutualFundsHoldings is a real GET against mutual-funds' real
// /holdings?accountId=... endpoint. mutual-funds has no auth middleware
// today, but callerAuthorizationHeader is still forwarded (harmless if
// ignored, and correct forward-compatible behavior if that ever
// changes) — see fetchOmsGatewayPositions for the same pattern where it
// matters today. Same explicit-status-check-before-decode discipline as
// fetchOmsGatewayPositions: a non-2xx or malformed body is a real,
// logged error, never silently swallowed into an empty holdings list.
func fetchMutualFundsHoldings(httpClient *http.Client, mutualFundsBaseUrl, accountIdentifier, callerAuthorizationHeader string) ([]mutualFundsHoldingWireResponse, error) {
	httpRequest, buildErr := http.NewRequest(http.MethodGet, mutualFundsBaseUrl+"/holdings?accountId="+accountIdentifier, nil)
	if buildErr != nil {
		return nil, fmt.Errorf("could not build request: %w", buildErr)
	}
	if callerAuthorizationHeader != "" {
		httpRequest.Header.Set("Authorization", callerAuthorizationHeader)
	}

	response, requestErr := httpClient.Do(httpRequest)
	if requestErr != nil {
		return nil, fmt.Errorf("could not reach mutual-funds: %w", requestErr)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mutual-funds returned HTTP %d for holdings of account %s", response.StatusCode, accountIdentifier)
	}

	var holdings []mutualFundsHoldingWireResponse
	if decodeErr := json.NewDecoder(response.Body).Decode(&holdings); decodeErr != nil {
		return nil, fmt.Errorf("malformed holdings response: %w", decodeErr)
	}
	return holdings, nil
}

func writeJson(responseWriter http.ResponseWriter, statusCode int, payload any) {
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(statusCode)
	_ = json.NewEncoder(responseWriter).Encode(payload)
}
