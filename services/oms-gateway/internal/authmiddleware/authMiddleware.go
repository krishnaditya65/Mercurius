// Package authmiddleware gates HTTP handlers on a valid access token
// issued by services/auth. This file is COPIED (byte-identical, not
// imported) into every service that needs auth in this build —
// oms-gateway, backoffice, kyc-onboarding, api-gateway — matching this
// repo's existing convention of duplicating small internal packages
// across service boundaries rather than sharing a cross-service Go
// module (see e.g. internal/httplogging, internal/ledgerclient-style
// packages, each independently re-implemented per service rather than
// imported from a shared location; there is no shared Go module root
// this monorepo's tooling supports importing across service
// directories from).
//
// It deliberately does NOT import services/auth/internal/jwtauth
// (cross-service internal-package imports don't work in Go anyway, and
// wouldn't fit this repo's convention even if they did) — instead it
// re-implements JUST the verify half of that package's HS256 JWT logic
// here, verified against the exact same algorithm (HMAC-SHA256 over
// base64url header.payload, constant-time signature comparison). Token
// ISSUANCE stays exclusively services/auth's job; this package only
// ever reads a token another service already issued.
//
// TODO(real build): this hand-copied duplication is a real maintenance
// burden — a change to the JWT claim shape or verification logic in
// services/auth's internal/jwtauth must be manually re-applied to every
// copy of this file, with nothing enforcing they stay in sync beyond
// developer discipline (see docs/BUILD_LOG.md for the entry documenting
// this decision explicitly). A real build should either vendor this as
// a proper shared internal Go module, or move verification behind a
// small sidecar/library published from one source of truth.
package authmiddleware

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

var ErrTokenExpired = errors.New("token has expired")
var ErrTokenSignatureInvalid = errors.New("token signature is invalid")
var ErrTokenMalformed = errors.New("token is malformed")
var ErrMissingAuthorizationHeader = errors.New("missing or malformed Authorization header")

// Roles: the same closed set services/auth's internal/jwtauth defines —
// duplicated here for the same cross-service-import reason as the rest
// of this file. Keep these string values byte-identical to jwtauth's
// Role* constants; a mismatch here would silently break every RBAC
// check in this service.
const (
	RoleRetail     = "retail"
	RoleAdmin      = "admin"
	RoleSupport    = "support"
	RoleCompliance = "compliance"
)

// AccessTokenClaims mirrors services/auth/internal/jwtauth.AccessTokenClaims
// field-for-field (same JSON key names) — this package only ever reads
// tokens services/auth issued, so the shapes must match exactly.
type AccessTokenClaims struct {
	Subject       string `json:"sub"`
	Role          string `json:"role"`
	IssuedAtUnix  int64  `json:"iat"`
	ExpiresAtUnix int64  `json:"exp"`
}

// contextKey is an unexported type so keys from this package can never
// collide with a context key from another package (the standard Go
// idiom for context keys).
type contextKey int

const (
	accountIdentifierContextKey contextKey = iota
	roleContextKey
)

// devOnlyInsecureDefaultSigningSecret MUST byte-for-byte match
// services/auth's own dev-only default (cmd/server/main.go) — otherwise
// local development without AUTH_JWT_SIGNING_SECRET explicitly set
// would issue tokens this middleware can never verify.
const devOnlyInsecureDefaultSigningSecret = "dev-only-insecure-default-signing-secret-do-not-use-in-production"

// SigningSecretFromEnv reads AUTH_JWT_SIGNING_SECRET, falling back to
// the same hardcoded dev-only default services/auth's cmd/server/main.go
// uses when the env var is unset — identical pattern, loudly logged,
// same reasoning: only acceptable for local development.
func SigningSecretFromEnv() []byte {
	signingSecret := os.Getenv("AUTH_JWT_SIGNING_SECRET")
	if signingSecret == "" {
		log.Printf("AUTH_JWT_SIGNING_SECRET not set — using an insecure development default (must match services/auth's own default). Do not use this in production.")
		return []byte(devOnlyInsecureDefaultSigningSecret)
	}
	return []byte(signingSecret)
}

// ParseAndVerifyAccessToken validates the signature and expiry of
// tokenString exactly like services/auth/internal/jwtauth's function of
// the same name — verify-only, no issuance.
func ParseAndVerifyAccessToken(tokenString string, signingSecret []byte, now time.Time) (AccessTokenClaims, error) {
	var claims AccessTokenClaims

	headerSegment, claimsSegment, signatureSegment, splitError := splitTokenIntoThreeSegments(tokenString)
	if splitError != nil {
		return claims, splitError
	}

	signingInput := headerSegment + "." + claimsSegment
	expectedSignature := computeHmacSignature(signingInput, signingSecret)
	if subtle.ConstantTimeCompare([]byte(expectedSignature), []byte(signatureSegment)) != 1 {
		return claims, ErrTokenSignatureInvalid
	}

	claimsJson, decodeError := base64.RawURLEncoding.DecodeString(claimsSegment)
	if decodeError != nil {
		return claims, fmt.Errorf("%w: %v", ErrTokenMalformed, decodeError)
	}
	if unmarshalError := json.Unmarshal(claimsJson, &claims); unmarshalError != nil {
		return claims, fmt.Errorf("%w: %v", ErrTokenMalformed, unmarshalError)
	}

	if now.Unix() >= claims.ExpiresAtUnix {
		return claims, ErrTokenExpired
	}

	return claims, nil
}

func splitTokenIntoThreeSegments(tokenString string) (header string, claims string, signature string, err error) {
	firstDotIndex := strings.IndexByte(tokenString, '.')
	if firstDotIndex < 0 {
		return "", "", "", ErrTokenMalformed
	}
	remainder := tokenString[firstDotIndex+1:]
	secondDotIndex := strings.IndexByte(remainder, '.')
	if secondDotIndex < 0 {
		return "", "", "", ErrTokenMalformed
	}
	secondDotIndex += firstDotIndex + 1

	return tokenString[:firstDotIndex], tokenString[firstDotIndex+1 : secondDotIndex], tokenString[secondDotIndex+1:], nil
}

func computeHmacSignature(signingInput string, signingSecret []byte) string {
	hmacHasher := hmac.New(sha256.New, signingSecret)
	hmacHasher.Write([]byte(signingInput))
	return base64.RawURLEncoding.EncodeToString(hmacHasher.Sum(nil))
}

func extractBearerToken(request *http.Request) string {
	const bearerPrefix = "Bearer "
	authorizationHeader := request.Header.Get("Authorization")
	if len(authorizationHeader) <= len(bearerPrefix) || authorizationHeader[:len(bearerPrefix)] != bearerPrefix {
		return ""
	}
	return authorizationHeader[len(bearerPrefix):]
}

// errorWireResponse matches this repo's dominant JSON error-response
// shape (`errorMessage` — see e.g. oms-gateway's OrderAcknowledgementResponse,
// services/auth's own wire responses; 48+ existing occurrences of this
// exact field name across the Go services at the time this package was
// written), not a bare `{"error": "..."}`.
type errorWireResponse struct {
	ErrorMessage string `json:"errorMessage"`
}

func respondWithJsonError(responseWriter http.ResponseWriter, statusCode int, message string) {
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(statusCode)
	_ = json.NewEncoder(responseWriter).Encode(errorWireResponse{ErrorMessage: message})
}

// AuthenticatedAccountIdentifier reads back the account identifier
// RequireAuth/RequireRole injected into request's context. Returns
// ("", false) if called on a request that never went through this
// middleware (a bug in the caller, not a normal runtime condition).
func AuthenticatedAccountIdentifier(request *http.Request) (string, bool) {
	accountIdentifier, ok := request.Context().Value(accountIdentifierContextKey).(string)
	return accountIdentifier, ok
}

// AuthenticatedRole reads back the role RequireAuth/RequireRole injected
// into request's context.
func AuthenticatedRole(request *http.Request) (string, bool) {
	role, ok := request.Context().Value(roleContextKey).(string)
	return role, ok
}

// RequireAuth wraps next so it only ever runs for a request carrying a
// valid, unexpired bearer access token — on success, the account
// identifier and role are available to next via AuthenticatedAccountIdentifier/
// AuthenticatedRole. Missing/malformed/expired/signature-invalid tokens
// all get a 401 with a plain-language JSON error, never a panic and
// never a silent pass-through.
func RequireAuth(signingSecret []byte, next http.HandlerFunc) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		bearerToken := extractBearerToken(request)
		if bearerToken == "" {
			respondWithJsonError(responseWriter, http.StatusUnauthorized, ErrMissingAuthorizationHeader.Error())
			return
		}

		claims, parseError := ParseAndVerifyAccessToken(bearerToken, signingSecret, time.Now())
		if parseError != nil {
			respondWithJsonError(responseWriter, http.StatusUnauthorized, parseError.Error())
			return
		}

		ctx := context.WithValue(request.Context(), accountIdentifierContextKey, claims.Subject)
		ctx = context.WithValue(ctx, roleContextKey, claims.Role)
		next.ServeHTTP(responseWriter, request.WithContext(ctx))
	}
}

// RequireRole is RequireAuth plus an additional check that the
// authenticated token's role is exactly requiredRole — for admin-only
// (or support-only, compliance-only, etc.) routes. A valid token for the
// wrong role gets a 403, distinct from RequireAuth's 401 for no/invalid
// token at all.
func RequireRole(signingSecret []byte, requiredRole string, next http.HandlerFunc) http.HandlerFunc {
	return RequireAuth(signingSecret, func(responseWriter http.ResponseWriter, request *http.Request) {
		role, _ := AuthenticatedRole(request)
		if role != requiredRole {
			respondWithJsonError(responseWriter, http.StatusForbidden, fmt.Sprintf("this action requires the %q role", requiredRole))
			return
		}
		next.ServeHTTP(responseWriter, request)
	})
}
