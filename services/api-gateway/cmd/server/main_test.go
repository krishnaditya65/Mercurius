package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mercurius/apiGateway/internal/apikeymanager"
	"mercurius/apiGateway/internal/authmiddleware"
	"mercurius/apiGateway/internal/ratelimiter"
	"mercurius/apiGateway/internal/sloalerting"
	"mercurius/apiGateway/internal/tenantconfig"
	"mercurius/apiGateway/internal/webhookdelivery"
)

// testSigningSecret matches authmiddleware.SigningSecretFromEnv()'s
// dev-only default, since AUTH_JWT_SIGNING_SECRET is unset in this test
// process — exactly the situation main() runs under locally.
var testSigningSecret = authmiddleware.SigningSecretFromEnv()

// issueTestToken hand-builds an HS256 token the same way
// internal/authmiddleware/authMiddleware_test.go's issueTestToken does,
// re-implemented here since computeHmacSignature is unexported in that
// package and this is a different package.
func issueTestToken(t *testing.T, subject string, role string) string {
	t.Helper()
	header := map[string]string{"alg": "HS256", "typ": "JWT"}
	claims := map[string]any{
		"sub":  subject,
		"role": role,
		"iat":  time.Now().Unix(),
		"exp":  time.Now().Add(time.Hour).Unix(),
	}
	headerJson, _ := json.Marshal(header)
	claimsJson, _ := json.Marshal(claims)
	encodedHeader := base64.RawURLEncoding.EncodeToString(headerJson)
	encodedClaims := base64.RawURLEncoding.EncodeToString(claimsJson)
	signingInput := encodedHeader + "." + encodedClaims

	hasher := hmac.New(sha256.New, testSigningSecret)
	hasher.Write([]byte(signingInput))
	signature := base64.RawURLEncoding.EncodeToString(hasher.Sum(nil))

	return signingInput + "." + signature
}

func bearer(request *http.Request, token string) {
	request.Header.Set("Authorization", "Bearer "+token)
}

func TestDeveloperApiKeysRequiresAuthAndOwnAccount(t *testing.T) {
	manager := apikeymanager.NewApiKeyManager()
	handler := authmiddleware.RequireAuth(testSigningSecret, buildApiKeysHandler(manager))

	// No token at all -> 401.
	noAuthRequest := httptest.NewRequest(http.MethodPost, "/developer/api-keys", bytes.NewBufferString(`{"accountIdentifier":"acct-001"}`))
	noAuthRecorder := httptest.NewRecorder()
	handler.ServeHTTP(noAuthRecorder, noAuthRequest)
	if noAuthRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no token, got %d", noAuthRecorder.Code)
	}

	// Authenticated as acct-002 trying to issue a key for acct-001 -> 403.
	wrongAccountToken := issueTestToken(t, "acct-002", authmiddleware.RoleRetail)
	wrongAccountRequest := httptest.NewRequest(http.MethodPost, "/developer/api-keys", bytes.NewBufferString(`{"accountIdentifier":"acct-001"}`))
	bearer(wrongAccountRequest, wrongAccountToken)
	wrongAccountRecorder := httptest.NewRecorder()
	handler.ServeHTTP(wrongAccountRecorder, wrongAccountRequest)
	if wrongAccountRecorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 issuing a key for someone else's account, got %d body=%s", wrongAccountRecorder.Code, wrongAccountRecorder.Body.String())
	}

	// Authenticated as acct-001 issuing a key for acct-001 -> 201.
	ownAccountToken := issueTestToken(t, "acct-001", authmiddleware.RoleRetail)
	ownAccountRequest := httptest.NewRequest(http.MethodPost, "/developer/api-keys", bytes.NewBufferString(`{"accountIdentifier":"acct-001"}`))
	bearer(ownAccountRequest, ownAccountToken)
	ownAccountRecorder := httptest.NewRecorder()
	handler.ServeHTTP(ownAccountRecorder, ownAccountRequest)
	if ownAccountRecorder.Code != http.StatusCreated {
		t.Fatalf("expected 201 issuing a key for one's own account, got %d body=%s", ownAccountRecorder.Code, ownAccountRecorder.Body.String())
	}

	// GET list for someone else's account -> 403.
	listWrongRequest := httptest.NewRequest(http.MethodGet, "/developer/api-keys?accountIdentifier=acct-001", nil)
	bearer(listWrongRequest, wrongAccountToken)
	listWrongRecorder := httptest.NewRecorder()
	handler.ServeHTTP(listWrongRecorder, listWrongRequest)
	if listWrongRecorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 listing someone else's keys, got %d", listWrongRecorder.Code)
	}

	// GET list for own account -> 200.
	listOwnRequest := httptest.NewRequest(http.MethodGet, "/developer/api-keys?accountIdentifier=acct-001", nil)
	bearer(listOwnRequest, ownAccountToken)
	listOwnRecorder := httptest.NewRecorder()
	handler.ServeHTTP(listOwnRecorder, listOwnRequest)
	if listOwnRecorder.Code != http.StatusOK {
		t.Fatalf("expected 200 listing one's own keys, got %d", listOwnRecorder.Code)
	}
}

func TestRevokeApiKeyOnlyAllowsTheOwningAccount(t *testing.T) {
	manager := apikeymanager.NewApiKeyManager()
	issued, err := manager.IssueApiKey(apikeymanager.ApiKeyIssuanceRequest{AccountIdentifier: "acct-001"})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	handler := authmiddleware.RequireAuth(testSigningSecret, buildRevokeApiKeyHandler(manager))

	// A different account tries to revoke acct-001's key -> 403, and the
	// key must remain un-revoked afterward.
	wrongAccountToken := issueTestToken(t, "acct-002", authmiddleware.RoleRetail)
	wrongAccountRequest := httptest.NewRequest(http.MethodPost, "/developer/api-keys/revoke", bytes.NewBufferString(`{"apiKeyValue":"`+issued.ApiKeyValue+`"}`))
	bearer(wrongAccountRequest, wrongAccountToken)
	wrongAccountRecorder := httptest.NewRecorder()
	handler.ServeHTTP(wrongAccountRecorder, wrongAccountRequest)
	if wrongAccountRecorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 revoking someone else's key, got %d body=%s", wrongAccountRecorder.Code, wrongAccountRecorder.Body.String())
	}
	if stillValid, validateErr := manager.ValidateApiKey(issued.ApiKeyValue); validateErr != nil || stillValid.IsRevoked {
		t.Fatalf("key must not have been revoked by the wrong account's request")
	}

	// The owning account revokes its own key -> 200, and it's actually
	// revoked afterward.
	ownAccountToken := issueTestToken(t, "acct-001", authmiddleware.RoleRetail)
	ownAccountRequest := httptest.NewRequest(http.MethodPost, "/developer/api-keys/revoke", bytes.NewBufferString(`{"apiKeyValue":"`+issued.ApiKeyValue+`"}`))
	bearer(ownAccountRequest, ownAccountToken)
	ownAccountRecorder := httptest.NewRecorder()
	handler.ServeHTTP(ownAccountRecorder, ownAccountRequest)
	if ownAccountRecorder.Code != http.StatusOK {
		t.Fatalf("expected 200 revoking one's own key, got %d body=%s", ownAccountRecorder.Code, ownAccountRecorder.Body.String())
	}
	if _, validateErr := manager.ValidateApiKey(issued.ApiKeyValue); validateErr == nil {
		t.Fatalf("expected the key to now be revoked")
	}
}

func TestAlertsRequiresAdminRole(t *testing.T) {
	evaluator := sloalerting.NewSloAlertEvaluator(sloalerting.DefaultThresholds)
	handler := authmiddleware.RequireRole(testSigningSecret, authmiddleware.RoleAdmin, buildListAlertsHandler(evaluator))

	retailToken := issueTestToken(t, "acct-001", authmiddleware.RoleRetail)
	retailRequest := httptest.NewRequest(http.MethodGet, "/alerts", nil)
	bearer(retailRequest, retailToken)
	retailRecorder := httptest.NewRecorder()
	handler.ServeHTTP(retailRecorder, retailRequest)
	if retailRecorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a retail token on /alerts, got %d", retailRecorder.Code)
	}

	adminToken := issueTestToken(t, "acct-ops", authmiddleware.RoleAdmin)
	adminRequest := httptest.NewRequest(http.MethodGet, "/alerts", nil)
	bearer(adminRequest, adminToken)
	adminRecorder := httptest.NewRecorder()
	handler.ServeHTTP(adminRecorder, adminRequest)
	if adminRecorder.Code != http.StatusOK {
		t.Fatalf("expected 200 for an admin token on /alerts, got %d", adminRecorder.Code)
	}
}

func TestTenantsRequiresAdminRole(t *testing.T) {
	registry := tenantconfig.NewTenantRegistry()
	handler := authmiddleware.RequireRole(testSigningSecret, authmiddleware.RoleAdmin, buildTenantsHandler(registry))

	request := httptest.NewRequest(http.MethodGet, "/tenants", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no token on /tenants, got %d", recorder.Code)
	}

	adminToken := issueTestToken(t, "acct-ops", authmiddleware.RoleAdmin)
	adminRequest := httptest.NewRequest(http.MethodGet, "/tenants", nil)
	bearer(adminRequest, adminToken)
	adminRecorder := httptest.NewRecorder()
	handler.ServeHTTP(adminRecorder, adminRequest)
	if adminRecorder.Code != http.StatusOK {
		t.Fatalf("expected 200 for an admin token on /tenants, got %d", adminRecorder.Code)
	}
}

func TestWebhookSubscribeRequiresOwnAccount(t *testing.T) {
	manager := webhookdelivery.NewWebhookDeliveryManager(webhookdelivery.DefaultRetryPolicy)
	handler := authmiddleware.RequireAuth(testSigningSecret, buildWebhookSubscribeHandler(manager))

	wrongAccountToken := issueTestToken(t, "acct-002", authmiddleware.RoleRetail)
	wrongRequest := httptest.NewRequest(http.MethodPost, "/webhooks/subscribe", bytes.NewBufferString(`{"accountIdentifier":"acct-001","targetUrl":"https://example.com/hook"}`))
	bearer(wrongRequest, wrongAccountToken)
	wrongRecorder := httptest.NewRecorder()
	handler.ServeHTTP(wrongRecorder, wrongRequest)
	if wrongRecorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 subscribing on someone else's account, got %d body=%s", wrongRecorder.Code, wrongRecorder.Body.String())
	}

	ownAccountToken := issueTestToken(t, "acct-001", authmiddleware.RoleRetail)
	ownRequest := httptest.NewRequest(http.MethodPost, "/webhooks/subscribe", bytes.NewBufferString(`{"accountIdentifier":"acct-001","targetUrl":"https://example.com/hook"}`))
	bearer(ownRequest, ownAccountToken)
	ownRecorder := httptest.NewRecorder()
	handler.ServeHTTP(ownRecorder, ownRequest)
	if ownRecorder.Code != http.StatusCreated {
		t.Fatalf("expected 201 subscribing on one's own account, got %d body=%s", ownRecorder.Code, ownRecorder.Body.String())
	}
}

func TestWebhookDeliveriesRequiresAdminRole(t *testing.T) {
	manager := webhookdelivery.NewWebhookDeliveryManager(webhookdelivery.DefaultRetryPolicy)
	handler := authmiddleware.RequireRole(testSigningSecret, authmiddleware.RoleAdmin, buildWebhookDeliveriesHandler(manager))

	retailToken := issueTestToken(t, "acct-001", authmiddleware.RoleRetail)
	retailRequest := httptest.NewRequest(http.MethodGet, "/webhooks/deliveries", nil)
	bearer(retailRequest, retailToken)
	retailRecorder := httptest.NewRecorder()
	handler.ServeHTTP(retailRecorder, retailRequest)
	if retailRecorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a retail token on /webhooks/deliveries, got %d", retailRecorder.Code)
	}
}

func TestTcaReportRequiresOwnAccount(t *testing.T) {
	handler := authmiddleware.RequireAuth(testSigningSecret, buildTcaReportHandler())

	wrongAccountToken := issueTestToken(t, "acct-002", authmiddleware.RoleRetail)
	wrongRequest := httptest.NewRequest(http.MethodGet, "/tca/report?accountId=acct-001", nil)
	bearer(wrongRequest, wrongAccountToken)
	wrongRecorder := httptest.NewRecorder()
	handler.ServeHTTP(wrongRecorder, wrongRequest)
	if wrongRecorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 requesting someone else's TCA report, got %d body=%s", wrongRecorder.Code, wrongRecorder.Body.String())
	}

	ownAccountToken := issueTestToken(t, "acct-001", authmiddleware.RoleRetail)
	ownRequest := httptest.NewRequest(http.MethodGet, "/tca/report?accountId=acct-001", nil)
	bearer(ownRequest, ownAccountToken)
	ownRecorder := httptest.NewRecorder()
	handler.ServeHTTP(ownRecorder, ownRequest)
	if ownRecorder.Code != http.StatusOK {
		t.Fatalf("expected 200 requesting one's own TCA report, got %d body=%s", ownRecorder.Code, ownRecorder.Body.String())
	}
}

func TestAccountAggregatorRequiresOwnAccount(t *testing.T) {
	backendStub := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = responseWriter.Write([]byte(`[]`))
	}))
	defer backendStub.Close()

	handler := authmiddleware.RequireAuth(testSigningSecret, buildAccountAggregatorHandler(backendStub.URL, backendStub.URL))

	wrongAccountToken := issueTestToken(t, "acct-002", authmiddleware.RoleRetail)
	wrongRequest := httptest.NewRequest(http.MethodGet, "/account-aggregator/net-worth?accountId=acct-001", nil)
	bearer(wrongRequest, wrongAccountToken)
	wrongRecorder := httptest.NewRecorder()
	handler.ServeHTTP(wrongRecorder, wrongRequest)
	if wrongRecorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 requesting someone else's net worth view, got %d body=%s", wrongRecorder.Code, wrongRecorder.Body.String())
	}

	ownAccountToken := issueTestToken(t, "acct-001", authmiddleware.RoleRetail)
	ownRequest := httptest.NewRequest(http.MethodGet, "/account-aggregator/net-worth?accountId=acct-001", nil)
	bearer(ownRequest, ownAccountToken)
	ownRecorder := httptest.NewRecorder()
	handler.ServeHTTP(ownRecorder, ownRequest)
	if ownRecorder.Code != http.StatusOK {
		t.Fatalf("expected 200 requesting one's own net worth view, got %d body=%s", ownRecorder.Code, ownRecorder.Body.String())
	}
}

// TestAccountAggregatorForwardsCallersBearerTokenToOmsGateway is a
// REGRESSION test for the bug where the aggregator called oms-gateway's
// /positions completely unauthenticated: it stands up a fake oms-gateway
// that requires the exact Authorization header the incoming
// /account-aggregator/net-worth request carried (mirroring oms-gateway's
// real authmiddleware.RequireAuth on /positions) and asserts the
// resulting view actually contains the oms-gateway holding -- before the
// fix, oms-gateway always 401s here and the holding silently never
// appears.
func TestAccountAggregatorForwardsCallersBearerTokenToOmsGateway(t *testing.T) {
	ownAccountToken := issueTestToken(t, "acct-001", authmiddleware.RoleRetail)
	expectedAuthorizationHeader := "Bearer " + ownAccountToken

	omsGatewayStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != expectedAuthorizationHeader {
			http.Error(w, `{"errorMessage":"missing or malformed Authorization header"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accountIdentifier":"acct-001","netQuantityByInstrumentSymbol":{"AAPL":10}}`))
	}))
	defer omsGatewayStub.Close()

	mutualFundsStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer mutualFundsStub.Close()

	handler := authmiddleware.RequireAuth(testSigningSecret, buildAccountAggregatorHandler(omsGatewayStub.URL, mutualFundsStub.URL))

	request := httptest.NewRequest(http.MethodGet, "/account-aggregator/net-worth?accountId=acct-001", nil)
	bearer(request, ownAccountToken)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}

	var body struct {
		PlatformHoldings []struct {
			SourceService         string `json:"sourceService"`
			InstrumentDescription string `json:"instrumentDescription"`
		} `json:"platformHoldings"`
		DataSourceCaveats []string `json:"dataSourceCaveats"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("expected valid JSON body, got %s: %v", recorder.Body.String(), err)
	}
	found := false
	for _, holding := range body.PlatformHoldings {
		if holding.SourceService == "oms-gateway" && holding.InstrumentDescription == "AAPL" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the oms-gateway AAPL holding to be present once the caller's bearer token is forwarded, got %+v", body.PlatformHoldings)
	}
}

// TestAccountAggregatorSurfacesOmsGatewayFailureInsteadOfSilentZero is a
// REGRESSION test for the swallowed-failure half of the same bug: when
// oms-gateway returns a non-2xx, the old code's
// `if json.Decode(...) == nil { ... }` with no else/log meant the
// failure vanished completely -- the response looked identical to "the
// account genuinely has zero positions", with nothing in the response
// or logs to tell the two apart. The fix must surface it via
// DataSourceCaveats.
func TestAccountAggregatorSurfacesOmsGatewayFailureInsteadOfSilentZero(t *testing.T) {
	ownAccountToken := issueTestToken(t, "acct-001", authmiddleware.RoleRetail)

	omsGatewayStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errorMessage":"missing or malformed Authorization header"}`, http.StatusUnauthorized)
	}))
	defer omsGatewayStub.Close()

	mutualFundsStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer mutualFundsStub.Close()

	handler := authmiddleware.RequireAuth(testSigningSecret, buildAccountAggregatorHandler(omsGatewayStub.URL, mutualFundsStub.URL))

	request := httptest.NewRequest(http.MethodGet, "/account-aggregator/net-worth?accountId=acct-001", nil)
	bearer(request, ownAccountToken)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200 with a caveat rather than an error status, got %d body=%s", recorder.Code, recorder.Body.String())
	}

	var body struct {
		DataSourceCaveats []string `json:"dataSourceCaveats"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("expected valid JSON body, got %s: %v", recorder.Body.String(), err)
	}
	if len(body.DataSourceCaveats) == 0 {
		t.Fatalf("expected a non-empty dataSourceCaveats explaining the oms-gateway failure, got none")
	}
}

// TestAccountAggregatorReportsMutualFundsHoldingsInTheRealWireShape is a
// regression test for the wire-shape mismatch: the real mutual-funds
// GET /holdings response carries schemeId/totalUnits, never
// schemeName/currentValueInMinorUnits. Before the fix, decoding the
// real shape into the old (wrong) struct always failed, so mutual-funds
// holdings never appeared no matter what mutual-funds returned.
func TestAccountAggregatorReportsMutualFundsHoldingsInTheRealWireShape(t *testing.T) {
	ownAccountToken := issueTestToken(t, "acct-001", authmiddleware.RoleRetail)
	expectedAuthorizationHeader := "Bearer " + ownAccountToken

	omsGatewayStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != expectedAuthorizationHeader {
			http.Error(w, `{"errorMessage":"missing or malformed Authorization header"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accountIdentifier":"acct-001","netQuantityByInstrumentSymbol":{}}`))
	}))
	defer omsGatewayStub.Close()

	// The REAL mutual-funds /holdings wire shape (see
	// services/mutual-funds/cmd/server/main.go's holdingWireFormat) --
	// schemeId/totalUnits/unitsReservedForRedemption/availableUnits, no
	// schemeName or currentValueInMinorUnits field.
	mutualFundsStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"schemeId":"scheme-nifty-index","totalUnits":125.5,"unitsReservedForRedemption":0,"availableUnits":125.5}]`))
	}))
	defer mutualFundsStub.Close()

	handler := authmiddleware.RequireAuth(testSigningSecret, buildAccountAggregatorHandler(omsGatewayStub.URL, mutualFundsStub.URL))

	request := httptest.NewRequest(http.MethodGet, "/account-aggregator/net-worth?accountId=acct-001", nil)
	bearer(request, ownAccountToken)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}

	var body struct {
		PlatformHoldings []struct {
			SourceService         string  `json:"sourceService"`
			InstrumentDescription string  `json:"instrumentDescription"`
			UnitsHeld             float64 `json:"unitsHeld"`
			ValuationUnavailable  bool    `json:"valuationUnavailable"`
		} `json:"platformHoldings"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("expected valid JSON body, got %s: %v", recorder.Body.String(), err)
	}
	found := false
	for _, holding := range body.PlatformHoldings {
		if holding.SourceService == "mutual-funds" && holding.InstrumentDescription == "scheme-nifty-index" {
			found = true
			if holding.UnitsHeld != 125.5 {
				t.Fatalf("expected units held 125.5, got %v", holding.UnitsHeld)
			}
			if !holding.ValuationUnavailable {
				t.Fatalf("expected mutual-funds holding to be marked ValuationUnavailable rather than fabricating a 0 value")
			}
		}
	}
	if !found {
		t.Fatalf("expected the real-shaped mutual-funds holding to decode and appear, got %+v", body.PlatformHoldings)
	}
}

func TestHealthStaysPublic(t *testing.T) {
	// /health itself is a trivial inline handler with no auth wrapper —
	// this just documents that expectation stays true structurally by
	// hitting it with zero Authorization header through the real
	// rate-limiting middleware, which explicitly special-cases it.
	limiter := buildRateLimitingMiddleware(nil, nil, http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/health" {
			t.Fatalf("only /health should reach here in this test")
		}
		responseWriter.WriteHeader(http.StatusOK)
	}))
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	recorder := httptest.NewRecorder()
	limiter.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected /health to stay public, got %d", recorder.Code)
	}
}

func TestCorsAllowsAnAllowListedOriginAndEchoesItBack(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "http://localhost:3000,http://localhost:3100")
	wrapped := withAllowListedCorsForDevelopment(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
	}))

	// Preflight OPTIONS from an allow-listed origin -> 204 with the
	// exact origin echoed back plus credentials/methods/headers.
	preflightRequest := httptest.NewRequest(http.MethodOptions, "/developer/api-keys", nil)
	preflightRequest.Header.Set("Origin", "http://localhost:3000")
	preflightRecorder := httptest.NewRecorder()
	wrapped.ServeHTTP(preflightRecorder, preflightRequest)

	if preflightRecorder.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for an allow-listed preflight, got %d", preflightRecorder.Code)
	}
	if got := preflightRecorder.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Fatalf("expected the exact origin echoed back, got %q", got)
	}
	if got := preflightRecorder.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("expected Access-Control-Allow-Credentials: true, got %q", got)
	}
	if got := preflightRecorder.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST, OPTIONS" {
		t.Fatalf("unexpected Access-Control-Allow-Methods: %q", got)
	}
	if got := preflightRecorder.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type, Authorization" {
		t.Fatalf("unexpected Access-Control-Allow-Headers: %q", got)
	}
}

func TestCorsOmitsHeadersForANonAllowListedOrigin(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "http://localhost:3000,http://localhost:3100")
	wrapped := withAllowListedCorsForDevelopment(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	request.Header.Set("Origin", "https://evil.example.com")
	recorder := httptest.NewRecorder()
	wrapped.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected the request to still pass through, got %d", recorder.Code)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no Access-Control-Allow-Origin for a non-allow-listed origin, got %q", got)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("expected no Access-Control-Allow-Credentials for a non-allow-listed origin, got %q", got)
	}
}

func TestCorsDefaultsWhenEnvVarUnset(t *testing.T) {
	wrapped := withAllowListedCorsForDevelopment(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	request.Header.Set("Origin", "http://localhost:3100")
	recorder := httptest.NewRecorder()
	wrapped.ServeHTTP(recorder, request)

	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3100" {
		t.Fatalf("expected the default allow-list to include http://localhost:3100, got header %q", got)
	}
}

// TestProxyAccessGateRejectsFullyAnonymousRequests is a REGRESSION test
// for the bug where the reverse-proxy catch-all route ("/") had no
// authentication at all: neither a JWT nor an API key. Before the fix,
// a request with neither reaches the backend proxy untouched; after the
// fix it must be rejected before ever reaching next.
func TestProxyAccessGateRejectsFullyAnonymousRequests(t *testing.T) {
	gate := buildProxyAccessGate(testSigningSecret, apikeymanager.NewApiKeyManager(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("the backend proxy must not be reached for a fully anonymous request")
	}))

	request := httptest.NewRequest(http.MethodGet, "/oms/positions?accountId=acct-1", nil)
	recorder := httptest.NewRecorder()
	gate.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for a fully anonymous proxy request, got %d body=%s", recorder.Code, recorder.Body.String())
	}
}

// TestProxyAccessGateAllowsAValidJwt covers the "platform's own
// web/desktop client traffic" access path this route must keep
// supporting: a caller with a valid JWT and no API key at all.
func TestProxyAccessGateAllowsAValidJwt(t *testing.T) {
	reached := false
	gate := buildProxyAccessGate(testSigningSecret, apikeymanager.NewApiKeyManager(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodGet, "/oms/positions?accountId=acct-1", nil)
	bearer(request, issueTestToken(t, "acct-1", authmiddleware.RoleRetail))
	recorder := httptest.NewRecorder()
	gate.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || !reached {
		t.Fatalf("expected a valid JWT to reach the proxy, got %d reached=%v", recorder.Code, reached)
	}
}

// TestProxyAccessGateAllowsAValidApiKeyWithNoJwt covers the developer
// API-key access path: an API-key holder has no user JWT at all, and
// must still be allowed through.
func TestProxyAccessGateAllowsAValidApiKeyWithNoJwt(t *testing.T) {
	keyManager := apikeymanager.NewApiKeyManager()
	issued, err := keyManager.IssueApiKey(apikeymanager.ApiKeyIssuanceRequest{AccountIdentifier: "acct-1"})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	reached := false
	gate := buildProxyAccessGate(testSigningSecret, keyManager, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodGet, "/oms/positions?accountId=acct-1", nil)
	request.Header.Set("X-Api-Key", issued.ApiKeyValue)
	recorder := httptest.NewRecorder()
	gate.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || !reached {
		t.Fatalf("expected a valid API key with no JWT to reach the proxy, got %d reached=%v", recorder.Code, reached)
	}
}

// TestProxyAccessGateAppliedThroughTheFullMiddlewareChainRejectsAnonymous
// exercises the gate the way main() actually wires it: through
// buildRateLimitingMiddleware first (which validates X-Api-Key, or
// falls back to the per-IP anonymous bucket), then buildProxyAccessGate.
// A fully anonymous request must still be rejected end-to-end, not just
// in isolation.
func TestProxyAccessGateAppliedThroughTheFullMiddlewareChainRejectsAnonymous(t *testing.T) {
	keyManager := apikeymanager.NewApiKeyManager()
	limiter := ratelimiter.NewTokenBucketRateLimiter(ratelimiter.DefaultTierLimits)
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.Handle("/", buildProxyAccessGate(testSigningSecret, keyManager, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("the backend proxy must not be reached for a fully anonymous request")
	})))
	fullChain := buildRateLimitingMiddleware(limiter, keyManager, mux)

	request := httptest.NewRequest(http.MethodGet, "/oms/positions?accountId=acct-1", nil)
	recorder := httptest.NewRecorder()
	fullChain.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 end-to-end for a fully anonymous proxy request, got %d body=%s", recorder.Code, recorder.Body.String())
	}
}

// TestRateLimitingMiddlewareKeysAnonymousTrafficByClientIp is a
// REGRESSION test for the bug where every anonymous (no API key)
// request shared one hardcoded rate-limit bucket ("anonymous"): two
// different source IPs must each get their own bucket, so one client
// exhausting its burst must not affect the other's.
func TestRateLimitingMiddlewareKeysAnonymousTrafficByClientIp(t *testing.T) {
	// RETAIL tier's real burst size (see ratelimiter.DefaultTierLimits) —
	// exhaust it from one IP and confirm a second IP is unaffected.
	limiter := ratelimiter.NewTokenBucketRateLimiter(ratelimiter.DefaultTierLimits)
	handler := buildRateLimitingMiddleware(limiter, apikeymanager.NewApiKeyManager(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	burst := int(ratelimiter.DefaultTierLimits[ratelimiter.RateLimitTierRetail].BurstCapacity)

	firstIpExhausted := false
	for i := 0; i < burst+1; i++ {
		request := httptest.NewRequest(http.MethodGet, "/some-route", nil)
		request.RemoteAddr = "203.0.113.10:5555"
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code == http.StatusTooManyRequests {
			firstIpExhausted = true
		}
	}
	if !firstIpExhausted {
		t.Fatalf("expected the first IP's burst to eventually be exhausted (429)")
	}

	// A second, different source IP must still get through on its own
	// fresh bucket -- this is exactly what broke when every anonymous
	// request shared one constant key.
	secondIpRequest := httptest.NewRequest(http.MethodGet, "/some-route", nil)
	secondIpRequest.RemoteAddr = "198.51.100.20:6666"
	secondIpRecorder := httptest.NewRecorder()
	handler.ServeHTTP(secondIpRecorder, secondIpRequest)
	if secondIpRecorder.Code != http.StatusOK {
		t.Fatalf("expected a different source IP to be unaffected by the first IP's exhausted bucket, got %d", secondIpRecorder.Code)
	}
}

func TestRemoteAddressKeyStripsThePort(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "203.0.113.10:54321"
	if got := remoteAddressKey(request); got != "203.0.113.10" {
		t.Fatalf("expected the port stripped, got %q", got)
	}
}

func TestRemoteAddressKeyFallsBackToRawValueWhenNotHostPort(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "not-a-host-port"
	if got := remoteAddressKey(request); got != "not-a-host-port" {
		t.Fatalf("expected the raw RemoteAddr as a fallback, got %q", got)
	}
}
