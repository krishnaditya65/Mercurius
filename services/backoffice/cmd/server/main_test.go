package main

// Tests for the auth/RBAC wiring and CORS middleware added on top of
// this service's handlers. These exercise the exact same building
// blocks main() wires onto the mux (authmiddleware.RequireAuth/
// RequireRole, requireOwnAccount, withAllowListedCorsForDevelopment) —
// package main has no exported mux-building function to stand up a full
// httptest.NewServer against, so each test wraps the relevant handler
// builder the same way main() does at its registration site.

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

	"mercurius/backoffice/internal/accountcontrol"
	"mercurius/backoffice/internal/authmiddleware"
	"mercurius/backoffice/internal/familyaccountaccess"
)

var testJwtSigningSecret = []byte("test-signing-secret-do-not-use-in-production")

// issueTestToken hand-builds a token the same way
// internal/authmiddleware/authMiddleware_test.go's own issueTestToken
// helper does (same HS256-over-base64url-header.payload construction),
// so these tests exercise the real verification path
// authmiddleware.RequireAuth/RequireRole use, not a mock.
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
	signingInput := base64.RawURLEncoding.EncodeToString(headerJson) + "." + base64.RawURLEncoding.EncodeToString(claimsJson)

	hmacHasher := hmac.New(sha256.New, testJwtSigningSecret)
	hmacHasher.Write([]byte(signingInput))
	signature := base64.RawURLEncoding.EncodeToString(hmacHasher.Sum(nil))

	return signingInput + "." + signature
}

func TestHealthRouteIsPublic(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	recorder := httptest.NewRecorder()

	handler := func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = responseWriter.Write([]byte(`{"status":"ok","service":"backoffice"}`))
	}
	handler(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected /health to be reachable with no token, got %d", recorder.Code)
	}
}

func TestFreezeRouteRejectsAMissingToken(t *testing.T) {
	handler := authmiddleware.RequireRole(testJwtSigningSecret, authmiddleware.RoleAdmin, buildFreezeHandler(accountcontrol.NewAccountFreezeStateMachine()))

	request := httptest.NewRequest(http.MethodPost, "/accounts/freeze", bytes.NewBufferString(`{"accountId":"acct-1","freezeReason":"fraud review"}`))
	recorder := httptest.NewRecorder()
	handler(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no token, got %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestFreezeRouteRejectsARetailToken(t *testing.T) {
	handler := authmiddleware.RequireRole(testJwtSigningSecret, authmiddleware.RoleAdmin, buildFreezeHandler(accountcontrol.NewAccountFreezeStateMachine()))

	request := httptest.NewRequest(http.MethodPost, "/accounts/freeze", bytes.NewBufferString(`{"accountId":"acct-1","freezeReason":"fraud review"}`))
	request.Header.Set("Authorization", "Bearer "+issueTestToken(t, "acct-retail", authmiddleware.RoleRetail))
	recorder := httptest.NewRecorder()
	handler(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a retail-role token on an admin-only route, got %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestFreezeRouteAllowsAnAdminToken(t *testing.T) {
	handler := authmiddleware.RequireRole(testJwtSigningSecret, authmiddleware.RoleAdmin, buildFreezeHandler(accountcontrol.NewAccountFreezeStateMachine()))

	request := httptest.NewRequest(http.MethodPost, "/accounts/freeze", bytes.NewBufferString(`{"accountIdentifier":"acct-1","freezeReason":"fraud review"}`))
	request.Header.Set("Authorization", "Bearer "+issueTestToken(t, "acct-admin", authmiddleware.RoleAdmin))
	recorder := httptest.NewRecorder()
	handler(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200 for an admin-role token, got %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestFamilyAccessLinkRejectsActingOnSomeoneElsesAccount(t *testing.T) {
	handler := authmiddleware.RequireAuth(testJwtSigningSecret, buildRegisterFamilyLinkHandler(familyaccountaccess.NewRegistry()))

	payload := `{"ownerAccountIdentifier":"acct-owner","viewerAccountIdentifier":"acct-viewer","permissionLevel":"VIEW_ONLY"}`
	request := httptest.NewRequest(http.MethodPost, "/family-access/link", bytes.NewBufferString(payload))
	request.Header.Set("Authorization", "Bearer "+issueTestToken(t, "acct-someone-else", authmiddleware.RoleRetail))
	recorder := httptest.NewRecorder()
	handler(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when the caller isn't the owner account, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("expected JSON body, got %s", recorder.Body.String())
	}
	if body["errorMessage"] != "you can only act on your own account" {
		t.Fatalf("unexpected error message: %v", body["errorMessage"])
	}
}

func TestFamilyAccessLinkAllowsActingOnOwnAccount(t *testing.T) {
	handler := authmiddleware.RequireAuth(testJwtSigningSecret, buildRegisterFamilyLinkHandler(familyaccountaccess.NewRegistry()))

	payload := `{"ownerAccountIdentifier":"acct-owner","viewerAccountIdentifier":"acct-viewer","permissionLevel":"VIEW_ONLY"}`
	request := httptest.NewRequest(http.MethodPost, "/family-access/link", bytes.NewBufferString(payload))
	request.Header.Set("Authorization", "Bearer "+issueTestToken(t, "acct-owner", authmiddleware.RoleRetail))
	recorder := httptest.NewRecorder()
	handler(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200 when the caller is the owner account, got %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestAllowListedCorsEchoesAMatchingOrigin(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "http://localhost:3000,http://localhost:3100")
	handler := withAllowListedCorsForDevelopment(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	request.Header.Set("Origin", "http://localhost:3000")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Fatalf("expected the exact allowed origin echoed back, got %q", got)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("expected Access-Control-Allow-Credentials: true, got %q", got)
	}
}

func TestAllowListedCorsOmitsHeadersForANonMatchingOrigin(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "http://localhost:3000")
	handler := withAllowListedCorsForDevelopment(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	request.Header.Set("Origin", "https://evil.example.com")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no CORS header for a non-allow-listed origin, got %q", got)
	}
}

func TestAllowListedCorsShortCircuitsOptionsWith204(t *testing.T) {
	handler := withAllowListedCorsForDevelopment(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("inner handler must not run for an OPTIONS preflight")
	}))

	request := httptest.NewRequest(http.MethodOptions, "/health", nil)
	request.Header.Set("Origin", "http://localhost:3000")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for OPTIONS, got %d", recorder.Code)
	}
}

func TestAllowedCorsOriginsFromEnvDefaultsWhenUnset(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "")
	allowed := allowedCorsOriginsFromEnv()
	if !allowed["http://localhost:3000"] || !allowed["http://localhost:3100"] {
		t.Fatalf("expected the documented default allow-list, got %v", allowed)
	}
}
