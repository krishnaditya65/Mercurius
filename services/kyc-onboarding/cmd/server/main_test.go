package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mercurius/kycOnboarding/internal/authmiddleware"
	"mercurius/kycOnboarding/internal/bankverification"
	"mercurius/kycOnboarding/internal/kycstate"
)

// issueTestToken hand-builds an HS256 token the exact same way
// internal/authmiddleware/authMiddleware_test.go's issueTestToken does,
// signed with the same signing secret authmiddleware.SigningSecretFromEnv
// falls back to when AUTH_JWT_SIGNING_SECRET is unset (the normal case in
// this test binary), so it verifies through the real RequireAuth/RequireRole
// wiring these tests exercise.
func issueTestToken(t *testing.T, subject string, role string) string {
	t.Helper()
	signingSecret := authmiddleware.SigningSecretFromEnv()

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

	hmacHasher := hmac.New(sha256.New, signingSecret)
	hmacHasher.Write([]byte(signingInput))
	signature := base64.RawURLEncoding.EncodeToString(hmacHasher.Sum(nil))

	return signingInput + "." + signature
}

func newAuthorizedRequest(t *testing.T, method string, target string, body []byte, subject string, role string) *http.Request {
	t.Helper()
	var request *http.Request
	if body != nil {
		request = httptest.NewRequest(method, target, bytes.NewReader(body))
	} else {
		request = httptest.NewRequest(method, target, nil)
	}
	request.Header.Set("Authorization", "Bearer "+issueTestToken(t, subject, role))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func decodeJsonErrorMessage(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		ErrorMessage string `json:"errorMessage"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("expected a JSON error body, got %q: %v", recorder.Body.String(), err)
	}
	return body.ErrorMessage
}

func TestRequireOwnAccountFromJsonBodyAllowsTheOwningAccountThrough(t *testing.T) {
	stateMachine := kycstate.NewKycVerificationStateMachine()
	handler := requireOwnAccountFromJsonBody(authmiddleware.SigningSecretFromEnv(), buildKycSubmitHandler(stateMachine))

	body, _ := json.Marshal(kycSubmitWireRequest{AccountIdentifier: "acct-001", PanNumber: "ABCDE1234F", FullName: "Test User"})
	request := newAuthorizedRequest(t, http.MethodPost, "/kyc/submit", body, "acct-001", authmiddleware.RoleRetail)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200 for the account acting on its own data, got %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestRequireOwnAccountFromJsonBodyRejectsAMismatchedAccountWith403(t *testing.T) {
	stateMachine := kycstate.NewKycVerificationStateMachine()
	handler := requireOwnAccountFromJsonBody(authmiddleware.SigningSecretFromEnv(), buildKycSubmitHandler(stateMachine))

	body, _ := json.Marshal(kycSubmitWireRequest{AccountIdentifier: "acct-002", PanNumber: "ABCDE1234F", FullName: "Test User"})
	request := newAuthorizedRequest(t, http.MethodPost, "/kyc/submit", body, "acct-001", authmiddleware.RoleRetail)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a caller acting on someone else's account, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if message := decodeJsonErrorMessage(t, recorder); message != "you can only act on your own account" {
		t.Fatalf("unexpected error message %q", message)
	}
}

func TestRequireOwnAccountFromJsonBodyRejectsAMissingTokenWith401(t *testing.T) {
	stateMachine := kycstate.NewKycVerificationStateMachine()
	handler := requireOwnAccountFromJsonBody(authmiddleware.SigningSecretFromEnv(), buildKycSubmitHandler(stateMachine))

	body, _ := json.Marshal(kycSubmitWireRequest{AccountIdentifier: "acct-001", PanNumber: "ABCDE1234F", FullName: "Test User"})
	request := httptest.NewRequest(http.MethodPost, "/kyc/submit", bytes.NewReader(body))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for a missing token, got %d", recorder.Code)
	}
}

func TestRequireOwnAccountFromJsonBodyPassesThroughMalformedBodyForTheHandlersOwnErrorMessage(t *testing.T) {
	stateMachine := kycstate.NewKycVerificationStateMachine()
	handler := requireOwnAccountFromJsonBody(authmiddleware.SigningSecretFromEnv(), buildKycSubmitHandler(stateMachine))

	request := newAuthorizedRequest(t, http.MethodPost, "/kyc/submit", []byte("not json"), "acct-001", authmiddleware.RoleRetail)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected the wrapped handler's own 400 for malformed JSON, got %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestRequireOwnAccountFromQueryParamAllowsTheOwningAccountThrough(t *testing.T) {
	stateMachine := kycstate.NewKycVerificationStateMachine()
	stateMachine.SubmitKycDetails("acct-001", "ABCDE1234F", "Test User")
	handler := requireOwnAccountFromQueryParam("accountId", authmiddleware.SigningSecretFromEnv(), buildKycStatusHandler(stateMachine))

	request := newAuthorizedRequest(t, http.MethodGet, "/kyc/status?accountId=acct-001", nil, "acct-001", authmiddleware.RoleRetail)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200 for the account querying its own status, got %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestRequireOwnAccountFromQueryParamRejectsAMismatchedAccountWith403(t *testing.T) {
	stateMachine := kycstate.NewKycVerificationStateMachine()
	handler := requireOwnAccountFromQueryParam("accountId", authmiddleware.SigningSecretFromEnv(), buildKycStatusHandler(stateMachine))

	request := newAuthorizedRequest(t, http.MethodGet, "/kyc/status?accountId=acct-002", nil, "acct-001", authmiddleware.RoleRetail)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for querying someone else's account, got %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestBankVerificationDebugPeekRequiresTheAdminRole(t *testing.T) {
	verifier := bankverification.NewBankAccountVerifier()
	handler := authmiddleware.RequireRole(authmiddleware.SigningSecretFromEnv(), authmiddleware.RoleAdmin, buildBankVerificationDebugPeekHandler(verifier))

	retailRequest := newAuthorizedRequest(t, http.MethodGet, "/bank-verification/debug-peek?verificationId=whatever", nil, "acct-001", authmiddleware.RoleRetail)
	retailRecorder := httptest.NewRecorder()
	handler.ServeHTTP(retailRecorder, retailRequest)
	if retailRecorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a retail caller on the debug-peek endpoint, got %d body=%s", retailRecorder.Code, retailRecorder.Body.String())
	}

	adminRequest := newAuthorizedRequest(t, http.MethodGet, "/bank-verification/debug-peek?verificationId=whatever", nil, "acct-admin", authmiddleware.RoleAdmin)
	adminRecorder := httptest.NewRecorder()
	handler.ServeHTTP(adminRecorder, adminRequest)
	// 404 (no such verification) is fine here — the point is the admin
	// caller clears the role gate and reaches the handler at all.
	if adminRecorder.Code != http.StatusNotFound && adminRecorder.Code != http.StatusOK {
		t.Fatalf("expected an admin caller to reach the handler (200 or 404), got %d body=%s", adminRecorder.Code, adminRecorder.Body.String())
	}
}

func TestKycReviewQueueRequiresTheComplianceRole(t *testing.T) {
	stateMachine := kycstate.NewKycVerificationStateMachine()
	handler := authmiddleware.RequireRole(authmiddleware.SigningSecretFromEnv(), authmiddleware.RoleCompliance, buildKycReviewQueueHandler(stateMachine))

	retailRequest := newAuthorizedRequest(t, http.MethodGet, "/kyc/review-queue", nil, "acct-001", authmiddleware.RoleRetail)
	retailRecorder := httptest.NewRecorder()
	handler.ServeHTTP(retailRecorder, retailRequest)
	if retailRecorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a retail caller on the review-queue endpoint, got %d", retailRecorder.Code)
	}

	adminRequest := newAuthorizedRequest(t, http.MethodGet, "/kyc/review-queue", nil, "acct-admin", authmiddleware.RoleAdmin)
	adminRecorder := httptest.NewRecorder()
	handler.ServeHTTP(adminRecorder, adminRequest)
	if adminRecorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a plain admin caller (this route requires RoleCompliance specifically), got %d", adminRecorder.Code)
	}

	complianceRequest := newAuthorizedRequest(t, http.MethodGet, "/kyc/review-queue", nil, "acct-compliance", authmiddleware.RoleCompliance)
	complianceRecorder := httptest.NewRecorder()
	handler.ServeHTTP(complianceRecorder, complianceRequest)
	if complianceRecorder.Code != http.StatusOK {
		t.Fatalf("expected 200 for a compliance caller, got %d body=%s", complianceRecorder.Code, complianceRecorder.Body.String())
	}
}

func TestWithAllowListedCorsEchoesAnAllowedOriginWithCredentials(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://app.example.com")
	handler := withAllowListedCors(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	request.Header.Set("Origin", "https://app.example.com")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("expected the exact origin echoed back, got %q", got)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("expected Access-Control-Allow-Credentials: true, got %q", got)
	}
}

func TestWithAllowListedCorsOmitsHeadersForADisallowedOrigin(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://app.example.com")
	handler := withAllowListedCors(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	request.Header.Set("Origin", "https://evil.example.com")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no CORS header for a disallowed origin, got %q", got)
	}
}

func TestWithAllowListedCorsShortCircuitsOptionsWith204(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://app.example.com")
	innerHandlerCalled := false
	handler := withAllowListedCors(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		innerHandlerCalled = true
	}))

	request := httptest.NewRequest(http.MethodOptions, "/kyc/submit", nil)
	request.Header.Set("Origin", "https://app.example.com")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for an OPTIONS preflight, got %d", recorder.Code)
	}
	if innerHandlerCalled {
		t.Fatalf("expected the inner handler NOT to run for an OPTIONS preflight")
	}
}

func TestCorsAllowedOriginsFromEnvDefaultsToLocalDevPortsWhenUnset(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "")
	allowedOrigins := corsAllowedOriginsFromEnv()

	for _, expected := range []string{"http://localhost:3000", "http://localhost:3100"} {
		if !allowedOrigins[expected] {
			t.Fatalf("expected default allow-list to include %q, got %v", expected, allowedOrigins)
		}
	}
}

func TestCorsAllowedOriginsFromEnvParsesACommaSeparatedList(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://a.example.com, https://b.example.com")
	allowedOrigins := corsAllowedOriginsFromEnv()

	for _, expected := range []string{"https://a.example.com", "https://b.example.com"} {
		if !allowedOrigins[expected] {
			t.Fatalf("expected allow-list to include %q, got %v", expected, allowedOrigins)
		}
	}
}

func TestPublicRoutesRequireNoAuthentication(t *testing.T) {
	recorder := httptest.NewRecorder()
	buildRiskProfileQuestionnaireHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/risk-profile/questionnaire", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected the public questionnaire endpoint to serve without a token, got %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "questionId") {
		t.Fatalf("expected questionnaire JSON in the response body, got %s", recorder.Body.String())
	}
}
