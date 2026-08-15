package authmiddleware

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

var testSigningSecret = []byte("test-signing-secret-do-not-use-in-production")

// issueTestToken hand-builds a token using this package's OWN signing
// logic (computeHmacSignature) so these tests don't depend on
// services/auth's jwtauth package (can't import it cross-service
// anyway) while still exercising the exact verification path RequireAuth
// uses.
func issueTestToken(t *testing.T, subject string, role string, lifetime time.Duration, issuedAt time.Time) string {
	t.Helper()
	header := map[string]string{"alg": "HS256", "typ": "JWT"}
	claims := AccessTokenClaims{
		Subject:       subject,
		Role:          role,
		IssuedAtUnix:  issuedAt.Unix(),
		ExpiresAtUnix: issuedAt.Add(lifetime).Unix(),
	}
	headerJson, _ := json.Marshal(header)
	claimsJson, _ := json.Marshal(claims)
	encodedHeader := base64.RawURLEncoding.EncodeToString(headerJson)
	encodedClaims := base64.RawURLEncoding.EncodeToString(claimsJson)
	signingInput := encodedHeader + "." + encodedClaims
	signature := computeHmacSignature(signingInput, testSigningSecret)
	return signingInput + "." + signature
}

func newPassThroughHandler(t *testing.T) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier, hasAccount := AuthenticatedAccountIdentifier(request)
		role, hasRole := AuthenticatedRole(request)
		if !hasAccount || !hasRole {
			t.Fatalf("expected authenticated context values to be present, got hasAccount=%v hasRole=%v", hasAccount, hasRole)
		}
		responseWriter.Header().Set("X-Test-Account", accountIdentifier)
		responseWriter.Header().Set("X-Test-Role", role)
		responseWriter.WriteHeader(http.StatusOK)
	}
}

func TestRequireAuthAllowsAValidTokenThroughWithCorrectContextValues(t *testing.T) {
	handler := RequireAuth(testSigningSecret, newPassThroughHandler(t))

	token := issueTestToken(t, "acct-001", RoleRetail, time.Hour, time.Now())
	request := httptest.NewRequest(http.MethodGet, "/whatever", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("X-Test-Account") != "acct-001" {
		t.Fatalf("expected account context acct-001, got %q", recorder.Header().Get("X-Test-Account"))
	}
	if recorder.Header().Get("X-Test-Role") != RoleRetail {
		t.Fatalf("expected role context %q, got %q", RoleRetail, recorder.Header().Get("X-Test-Role"))
	}
}

func TestRequireAuthRejectsAMissingAuthorizationHeader(t *testing.T) {
	handler := RequireAuth(testSigningSecret, func(http.ResponseWriter, *http.Request) {
		t.Fatal("inner handler must not run for a missing Authorization header")
	})

	request := httptest.NewRequest(http.MethodGet, "/whatever", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", recorder.Code)
	}
	assertJsonErrorBody(t, recorder)
}

func TestRequireAuthRejectsAMalformedToken(t *testing.T) {
	handler := RequireAuth(testSigningSecret, func(http.ResponseWriter, *http.Request) {
		t.Fatal("inner handler must not run for a malformed token")
	})

	request := httptest.NewRequest(http.MethodGet, "/whatever", nil)
	request.Header.Set("Authorization", "Bearer not-a-real-jwt")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", recorder.Code)
	}
	assertJsonErrorBody(t, recorder)
}

func TestRequireAuthRejectsAnExpiredToken(t *testing.T) {
	handler := RequireAuth(testSigningSecret, func(http.ResponseWriter, *http.Request) {
		t.Fatal("inner handler must not run for an expired token")
	})

	token := issueTestToken(t, "acct-001", RoleRetail, time.Minute, time.Now().Add(-time.Hour))
	request := httptest.NewRequest(http.MethodGet, "/whatever", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", recorder.Code)
	}
	assertJsonErrorBody(t, recorder)
}

func TestRequireAuthRejectsATokenSignedWithAWrongSecret(t *testing.T) {
	handler := RequireAuth(testSigningSecret, func(http.ResponseWriter, *http.Request) {
		t.Fatal("inner handler must not run for a wrong-secret token")
	})

	header := map[string]string{"alg": "HS256", "typ": "JWT"}
	claims := AccessTokenClaims{Subject: "acct-001", Role: RoleRetail, IssuedAtUnix: time.Now().Unix(), ExpiresAtUnix: time.Now().Add(time.Hour).Unix()}
	headerJson, _ := json.Marshal(header)
	claimsJson, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(headerJson) + "." + base64.RawURLEncoding.EncodeToString(claimsJson)
	wrongSignature := computeHmacSignature(signingInput, []byte("a-completely-different-secret"))
	token := signingInput + "." + wrongSignature

	request := httptest.NewRequest(http.MethodGet, "/whatever", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", recorder.Code)
	}
}

func TestRequireRoleAllowsTheCorrectRoleThrough(t *testing.T) {
	handler := RequireRole(testSigningSecret, RoleAdmin, newPassThroughHandler(t))

	token := issueTestToken(t, "acct-admin", RoleAdmin, time.Hour, time.Now())
	request := httptest.NewRequest(http.MethodGet, "/whatever", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestRequireRoleRejectsTheWrongRoleWith403(t *testing.T) {
	handler := RequireRole(testSigningSecret, RoleAdmin, func(http.ResponseWriter, *http.Request) {
		t.Fatal("inner handler must not run for the wrong role")
	})

	token := issueTestToken(t, "acct-001", RoleRetail, time.Hour, time.Now())
	request := httptest.NewRequest(http.MethodGet, "/whatever", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", recorder.Code)
	}
	assertJsonErrorBody(t, recorder)
}

func TestRequireRoleRejectsAMissingTokenWith401NotDoubleWrapped403(t *testing.T) {
	handler := RequireRole(testSigningSecret, RoleAdmin, func(http.ResponseWriter, *http.Request) {
		t.Fatal("inner handler must not run without a token")
	})

	request := httptest.NewRequest(http.MethodGet, "/whatever", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for a missing token even on a role-gated route, got %d", recorder.Code)
	}
}

func assertJsonErrorBody(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	var body errorWireResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("expected a JSON error body, got %q: %v", recorder.Body.String(), err)
	}
	if body.ErrorMessage == "" {
		t.Fatalf("expected a non-empty errorMessage")
	}
}
