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
	"log"
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
	// validation.
	httpRequestMultiplexer.Handle("/", backendProxy)

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
// rate-limited under a shared RETAIL-tier bucket keyed by a constant —
// generous enough for normal traffic, but still a real limit rather
// than unlimited passthrough. An INVALID (present but unrecognized or
// revoked) API key is rejected outright with 401 — failing closed,
// never silently falling back to the anonymous bucket.
func buildRateLimitingMiddleware(limiter *ratelimiter.TokenBucketRateLimiter, keyManager *apikeymanager.ApiKeyManager, next http.Handler) http.Handler {
	const anonymousRateLimitKey = "anonymous"

	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/health" {
			next.ServeHTTP(responseWriter, request)
			return
		}

		rateLimitKey := anonymousRateLimitKey
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

type omsPositionsWireResponse []struct {
	InstrumentSymbol        string `json:"instrumentSymbol"`
	MarketValueInMinorUnits int64  `json:"marketValueInMinorUnits"`
}

type mutualFundsHoldingsWireResponse []struct {
	SchemeName               string `json:"schemeName"`
	CurrentValueInMinorUnits int64  `json:"currentValueInMinorUnits"`
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

		var platformHoldings []accountaggregator.PlatformHolding

		if response, err := httpClient.Get(omsGatewayBaseUrl + "/positions?accountId=" + accountIdentifier); err == nil {
			defer response.Body.Close()
			var positions omsPositionsWireResponse
			if json.NewDecoder(response.Body).Decode(&positions) == nil {
				for _, position := range positions {
					platformHoldings = append(platformHoldings, accountaggregator.PlatformHolding{
						SourceService:            "oms-gateway",
						InstrumentDescription:    position.InstrumentSymbol,
						CurrentValueInMinorUnits: position.MarketValueInMinorUnits,
					})
				}
			}
		} else {
			log.Printf("accountaggregator: failed reaching oms-gateway positions: %v", err)
		}

		if response, err := httpClient.Get(mutualFundsBaseUrl + "/holdings?accountId=" + accountIdentifier); err == nil {
			defer response.Body.Close()
			var holdings mutualFundsHoldingsWireResponse
			if json.NewDecoder(response.Body).Decode(&holdings) == nil {
				for _, holding := range holdings {
					platformHoldings = append(platformHoldings, accountaggregator.PlatformHolding{
						SourceService:            "mutual-funds",
						InstrumentDescription:    holding.SchemeName,
						CurrentValueInMinorUnits: holding.CurrentValueInMinorUnits,
					})
				}
			}
		} else {
			log.Printf("accountaggregator: failed reaching mutual-funds holdings: %v", err)
		}

		view := accountaggregator.BuildUnifiedNetWorthView(accountIdentifier, platformHoldings)
		writeJson(responseWriter, http.StatusOK, view)
	}
}

func writeJson(responseWriter http.ResponseWriter, statusCode int, payload any) {
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(statusCode)
	_ = json.NewEncoder(responseWriter).Encode(payload)
}
