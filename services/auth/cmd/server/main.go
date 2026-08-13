// Mercurius / auth
//
// FEATURES.md §1: "Email/phone auth, session management, JWT + refresh
// token rotation" and "MFA (TOTP + SMS fallback)...". As of this build:
// real password-based register/login (PBKDF2-HMAC-SHA256, internal/
// passwordhashing), real HS256 JWT access tokens (internal/jwtauth),
// real refresh-token rotation with reuse detection (internal/
// sessionstore), rate limiting on login/register (internal/
// ratelimiter), and real RFC 6238 TOTP MFA (internal/totp,
// internal/mfastate) gating login once enrolled. apps/web has a real
// login UI, but nothing else in the platform requires a valid token yet
// — see README's "Known limitations". No SMS fallback, no device
// binding, no biometric (mobile-only, deprioritized per
// [[mercurius-platform-scope]]).
package main

import (
	"encoding/json"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"mercurius/auth/internal/accountstore"
	"mercurius/auth/internal/httplogging"
	"mercurius/auth/internal/jwtauth"
	"mercurius/auth/internal/mfastate"
	"mercurius/auth/internal/ratelimiter"
	"mercurius/auth/internal/sessionstore"
	"mercurius/auth/internal/totp"
)

const accessTokenLifetime = 15 * time.Minute
const refreshTokenLifetime = 30 * 24 * time.Hour

// Rate limit tuning: generous enough that a legitimate user fumbling
// their password a few times, or a legitimate burst of signups from one
// office/campus NAT, never gets caught — tight enough to make an
// automated brute-force/spam script hit a wall fast. Real values should
// come from actual abuse data, not a guess in a skeleton.
const maxLoginAttemptsPerAccountPerWindow = 5
const maxRegistrationAttemptsPerAddressPerWindow = 3
const rateLimitWindowDuration = time.Minute

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	// TODO(real build): a hardcoded fallback signing secret is only
	// acceptable for local development — this MUST come from a real
	// secrets manager (FEATURES.md §13) before this service is anywhere
	// near production, exactly like withPermissiveCorsForDevelopment
	// elsewhere in this repo is flagged as dev-only.
	signingSecret := []byte(os.Getenv("AUTH_JWT_SIGNING_SECRET"))
	if len(signingSecret) == 0 {
		signingSecret = []byte("dev-only-insecure-default-signing-secret-do-not-use-in-production")
		log.Printf("AUTH_JWT_SIGNING_SECRET not set — using an insecure development default. Do not use this in production.")
	}

	accounts := accountstore.NewAccountStore()
	sessions := sessionstore.NewSessionStore(refreshTokenLifetime)
	mfa := mfastate.NewMfaState()
	loginRateLimiter := ratelimiter.NewRateLimiter(maxLoginAttemptsPerAccountPerWindow, rateLimitWindowDuration)
	registerRateLimiter := ratelimiter.NewRateLimiter(maxRegistrationAttemptsPerAddressPerWindow, rateLimitWindowDuration)

	httpRequestMultiplexer := http.NewServeMux()
	httpRequestMultiplexer.HandleFunc("/health", func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = responseWriter.Write([]byte(`{"status":"ok","service":"auth"}`))
	})
	httpRequestMultiplexer.HandleFunc("/auth/register", buildRegisterHandler(accounts, registerRateLimiter))
	httpRequestMultiplexer.HandleFunc("/auth/login", buildLoginHandler(accounts, sessions, mfa, signingSecret, loginRateLimiter))
	httpRequestMultiplexer.HandleFunc("/auth/refresh", buildRefreshHandler(sessions, signingSecret))
	httpRequestMultiplexer.HandleFunc("/auth/logout", buildLogoutHandler(sessions))
	httpRequestMultiplexer.HandleFunc("/auth/mfa/enroll", buildMfaEnrollHandler(mfa, signingSecret))
	httpRequestMultiplexer.HandleFunc("/auth/mfa/confirm-enrollment", buildMfaConfirmEnrollmentHandler(mfa, signingSecret))
	httpRequestMultiplexer.HandleFunc("/auth/mfa/disable", buildMfaDisableHandler(mfa, signingSecret))
	httpRequestMultiplexer.HandleFunc("/auth/verify", buildVerifyHandler(signingSecret))

	listenAddress := ":8086"
	if envListenAddress := os.Getenv("AUTH_LISTEN_ADDRESS"); envListenAddress != "" {
		listenAddress = envListenAddress
	}
	log.Printf("auth listening on %s (CORS wide open — see withPermissiveCorsForDevelopment)\n", listenAddress)
	if serverStartupError := http.ListenAndServe(listenAddress, withPermissiveCorsForDevelopment(httplogging.WithRequestLogging(httpRequestMultiplexer))); serverStartupError != nil {
		log.Fatalf("auth failed to start: %v", serverStartupError)
	}
}

// withPermissiveCorsForDevelopment mirrors oms-gateway's middleware of
// the same name exactly (see that service's cmd/server/main.go) — lets
// apps/web call this service directly from a browser during development.
// Same caveat: wrong once real auth (cookies/bearer tokens with actual
// trust boundaries) exists — `Access-Control-Allow-Origin: *` has no
// business being paired with a real login endpoint in production.
func withPermissiveCorsForDevelopment(nextHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Access-Control-Allow-Origin", "*")
		responseWriter.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		responseWriter.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if request.Method == http.MethodOptions {
			responseWriter.WriteHeader(http.StatusNoContent)
			return
		}
		nextHandler.ServeHTTP(responseWriter, request)
	})
}

type registerWireRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type registerWireResponse struct {
	AccountIdentifier string `json:"accountIdentifier,omitempty"`
	ErrorMessage      string `json:"errorMessage,omitempty"`
}

func buildRegisterHandler(accounts *accountstore.AccountStore, rateLimiter *ratelimiter.RateLimiter) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		// Keyed by remote address, not email — the point here is
		// stopping registration SPAM from one source, not (yet) a
		// per-email limit the way login has (an attacker choosing a new
		// email every time would trivially bypass an email-keyed limit
		// on register anyway). TODO(real build): remoteAddressKey uses
		// request.RemoteAddr directly, which is the load balancer's/
		// reverse proxy's address in any real deployment, not the
		// actual client — needs real X-Forwarded-For handling (with the
		// trusted-proxy-hop caveats that come with it) before this is
		// meaningful behind anything but a direct connection.
		if !rateLimiter.Allow(remoteAddressKey(request), time.Now()) {
			respondWithJson(responseWriter, http.StatusTooManyRequests, registerWireResponse{
				ErrorMessage: "too many registration attempts from this address — please wait and try again",
			})
			return
		}

		var wireRequest registerWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed registration payload", http.StatusBadRequest)
			return
		}
		if wireRequest.Email == "" || wireRequest.Password == "" {
			respondWithJson(responseWriter, http.StatusBadRequest, registerWireResponse{ErrorMessage: "email and password are both required"})
			return
		}
		// TODO(real build): no password strength policy, no email
		// verification (an unverified address can register and log in
		// immediately) — see FEATURES.md §1's fuller scope.

		accountIdentifier, registerError := accounts.RegisterAccount(wireRequest.Email, wireRequest.Password)
		if registerError != nil {
			respondWithJson(responseWriter, http.StatusConflict, registerWireResponse{ErrorMessage: registerError.Error()})
			return
		}

		respondWithJson(responseWriter, http.StatusCreated, registerWireResponse{AccountIdentifier: accountIdentifier})
	}
}

type loginWireRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	// TotpCode is optional on the first login attempt. If the account
	// has MFA enabled and this is empty or wrong, login responds with
	// MfaRequired: true and NO tokens — the client re-submits the same
	// request with TotpCode filled in.
	TotpCode string `json:"totpCode,omitempty"`
}

type authTokenWireResponse struct {
	AccountIdentifier string `json:"accountIdentifier,omitempty"`
	AccessToken       string `json:"accessToken,omitempty"`
	RefreshToken      string `json:"refreshToken,omitempty"`
	ExpiresInSeconds  int64  `json:"expiresInSeconds,omitempty"`
	// MfaRequired is set (with no tokens) when the password was correct
	// but a valid totpCode is still needed — distinguishes this from a
	// flat authentication failure so the client knows to prompt for a
	// code rather than just showing "invalid email or password".
	MfaRequired  bool   `json:"mfaRequired,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

func buildLoginHandler(
	accounts *accountstore.AccountStore,
	sessions *sessionstore.SessionStore,
	mfa *mfastate.MfaState,
	signingSecret []byte,
	rateLimiter *ratelimiter.RateLimiter,
) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest loginWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed login payload", http.StatusBadRequest)
			return
		}

		// Keyed by the normalized email being attempted — not remote
		// address — so an attacker distributing a brute-force across
		// many source IPs (or a shared office/campus NAT with many
		// legitimate users behind it) still can't pound on ONE account
		// past the limit. Checked AFTER decoding the body (need the
		// email to key on) but BEFORE ever touching AccountStore, so a
		// rate-limited request never even reaches the password check.
		if !rateLimiter.Allow(normalizeEmailForRateLimitKey(wireRequest.Email), time.Now()) {
			respondWithJson(responseWriter, http.StatusTooManyRequests, authTokenWireResponse{
				ErrorMessage: "too many login attempts for this account — please wait and try again",
			})
			return
		}

		accountIdentifier, authError := accounts.AuthenticateWithPassword(wireRequest.Email, wireRequest.Password)
		if authError != nil {
			respondWithJson(responseWriter, http.StatusUnauthorized, authTokenWireResponse{ErrorMessage: "invalid email or password"})
			return
		}

		now := time.Now()

		// MFA gate: password alone is not enough for an enrolled
		// account. A missing/wrong code gets the SAME "not authenticated
		// yet" outcome either way (no tokens) — the only difference is
		// mfaRequired lets the client tell "need a code" apart from "the
		// code you sent was wrong", both distinct from a password
		// failure above.
		if mfa.IsMfaEnabled(accountIdentifier) {
			if wireRequest.TotpCode == "" {
				respondWithJson(responseWriter, http.StatusOK, authTokenWireResponse{MfaRequired: true})
				return
			}
			isCodeValid, mfaVerifyError := mfa.VerifyLoginCode(accountIdentifier, wireRequest.TotpCode, now)
			if mfaVerifyError != nil {
				log.Printf("MFA code verification error for %s: %v", accountIdentifier, mfaVerifyError)
				http.Error(responseWriter, "failed to verify MFA code", http.StatusInternalServerError)
				return
			}
			if !isCodeValid {
				respondWithJson(responseWriter, http.StatusUnauthorized, authTokenWireResponse{MfaRequired: true, ErrorMessage: "invalid MFA code"})
				return
			}
		}
		accessToken, tokenIssueError := jwtauth.IssueAccessToken(accountIdentifier, signingSecret, accessTokenLifetime, now)
		if tokenIssueError != nil {
			log.Printf("failed to issue access token for %s: %v", accountIdentifier, tokenIssueError)
			http.Error(responseWriter, "failed to issue access token", http.StatusInternalServerError)
			return
		}
		refreshToken, sessionIssueError := sessions.IssueNewSessionFamily(accountIdentifier, now)
		if sessionIssueError != nil {
			log.Printf("failed to issue refresh token for %s: %v", accountIdentifier, sessionIssueError)
			http.Error(responseWriter, "failed to issue refresh token", http.StatusInternalServerError)
			return
		}

		respondWithJson(responseWriter, http.StatusOK, authTokenWireResponse{
			AccountIdentifier: accountIdentifier,
			AccessToken:       accessToken,
			RefreshToken:      refreshToken,
			ExpiresInSeconds:  int64(accessTokenLifetime.Seconds()),
		})
	}
}

type refreshWireRequest struct {
	RefreshToken string `json:"refreshToken"`
}

func buildRefreshHandler(sessions *sessionstore.SessionStore, signingSecret []byte) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest refreshWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed refresh payload", http.StatusBadRequest)
			return
		}

		now := time.Now()
		rotationResult, rotateError := sessions.RotateRefreshToken(wireRequest.RefreshToken, now)
		if rotateError != nil {
			respondWithJson(responseWriter, http.StatusUnauthorized, authTokenWireResponse{ErrorMessage: rotateError.Error()})
			return
		}
		if rotationResult.WasReuseDetected {
			// A previously-consumed refresh token was presented again —
			// the entire session family is already revoked as of
			// RotateRefreshToken returning. Log loudly: this is the
			// signature of a stolen refresh token, not routine client
			// behavior.
			log.Printf(
				"SECURITY: refresh token reuse detected for %s — entire session family revoked, forcing re-login",
				rotationResult.AccountIdentifier,
			)
			respondWithJson(responseWriter, http.StatusUnauthorized, authTokenWireResponse{
				ErrorMessage: "refresh token reuse detected — all sessions for this login have been revoked, please log in again",
			})
			return
		}

		newAccessToken, tokenIssueError := jwtauth.IssueAccessToken(rotationResult.AccountIdentifier, signingSecret, accessTokenLifetime, now)
		if tokenIssueError != nil {
			log.Printf("failed to issue access token on refresh for %s: %v", rotationResult.AccountIdentifier, tokenIssueError)
			http.Error(responseWriter, "failed to issue access token", http.StatusInternalServerError)
			return
		}

		respondWithJson(responseWriter, http.StatusOK, authTokenWireResponse{
			AccountIdentifier: rotationResult.AccountIdentifier,
			AccessToken:       newAccessToken,
			RefreshToken:      rotationResult.NewRefreshToken,
			ExpiresInSeconds:  int64(accessTokenLifetime.Seconds()),
		})
	}
}

type logoutWireRequest struct {
	RefreshToken string `json:"refreshToken"`
}

type logoutWireResponse struct {
	WasLoggedOut bool   `json:"wasLoggedOut"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

func buildLogoutHandler(sessions *sessionstore.SessionStore) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest logoutWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed logout payload", http.StatusBadRequest)
			return
		}

		if revokeError := sessions.RevokeRefreshToken(wireRequest.RefreshToken); revokeError != nil {
			// Logging out of a token that's already gone is not an
			// error from the client's perspective — the end state
			// (this token no longer works) is exactly what they wanted.
			respondWithJson(responseWriter, http.StatusOK, logoutWireResponse{WasLoggedOut: true})
			return
		}

		respondWithJson(responseWriter, http.StatusOK, logoutWireResponse{WasLoggedOut: true})
	}
}

type verifyWireResponse struct {
	IsValid           bool   `json:"isValid"`
	AccountIdentifier string `json:"accountIdentifier,omitempty"`
	ErrorMessage      string `json:"errorMessage,omitempty"`
}

// buildVerifyHandler is an HTTP introspection endpoint another service
// COULD call to check a bearer token without holding the JWT signing
// secret itself — a convenience for now. TODO(real build): for anything
// on a hot path, a service should verify the JWT LOCALLY with
// jwtauth.ParseAndVerifyAccessToken (given the shared secret) instead of
// making a network round-trip per request to this endpoint; this exists
// mainly to prove/demo the verification path independently.
func buildVerifyHandler(signingSecret []byte) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		bearerToken := extractBearerToken(request)
		if bearerToken == "" {
			respondWithJson(responseWriter, http.StatusUnauthorized, verifyWireResponse{ErrorMessage: "missing or malformed Authorization header"})
			return
		}

		claims, parseError := jwtauth.ParseAndVerifyAccessToken(bearerToken, signingSecret, time.Now())
		if parseError != nil {
			respondWithJson(responseWriter, http.StatusUnauthorized, verifyWireResponse{ErrorMessage: parseError.Error()})
			return
		}

		respondWithJson(responseWriter, http.StatusOK, verifyWireResponse{IsValid: true, AccountIdentifier: claims.Subject})
	}
}

// requireValidAccessToken extracts and verifies the bearer token on
// request, writing a 401 and returning ("", false) if it's missing or
// invalid. Every MFA management endpoint below uses this — enrolling,
// confirming, or disabling MFA for an account requires proving you ARE
// that account via a real access token, not just naming an
// accountIdentifier in the request body the way some of this skeleton's
// earlier endpoints still do (a gap those endpoints' own docs already
// flag).
func requireValidAccessToken(responseWriter http.ResponseWriter, request *http.Request, signingSecret []byte) (accountIdentifier string, isValid bool) {
	bearerToken := extractBearerToken(request)
	if bearerToken == "" {
		respondWithJson(responseWriter, http.StatusUnauthorized, verifyWireResponse{ErrorMessage: "missing or malformed Authorization header"})
		return "", false
	}
	claims, parseError := jwtauth.ParseAndVerifyAccessToken(bearerToken, signingSecret, time.Now())
	if parseError != nil {
		respondWithJson(responseWriter, http.StatusUnauthorized, verifyWireResponse{ErrorMessage: parseError.Error()})
		return "", false
	}
	return claims.Subject, true
}

type mfaEnrollWireResponse struct {
	SecretBase32 string `json:"secretBase32"`
	OtpAuthUri   string `json:"otpAuthUri"`
}

// buildMfaEnrollHandler starts MFA enrollment for the calling account
// (identified by its bearer token, not a request-body field). Returns
// the raw secret AND the otpauth:// URI a real client would render as a
// QR code — MFA is not yet ENABLED until buildMfaConfirmEnrollmentHandler
// proves the user can produce a valid code from it.
func buildMfaEnrollHandler(mfa *mfastate.MfaState, signingSecret []byte) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		accountIdentifier, isValid := requireValidAccessToken(responseWriter, request, signingSecret)
		if !isValid {
			return
		}

		secret, genError := mfa.BeginEnrollment(accountIdentifier)
		if genError != nil {
			log.Printf("failed to begin MFA enrollment for %s: %v", accountIdentifier, genError)
			http.Error(responseWriter, "failed to begin MFA enrollment", http.StatusInternalServerError)
			return
		}

		respondWithJson(responseWriter, http.StatusOK, mfaEnrollWireResponse{
			SecretBase32: secret,
			OtpAuthUri:   totp.BuildOtpAuthUri(secret, accountIdentifier, "Mercurius"),
		})
	}
}

type mfaConfirmEnrollmentWireRequest struct {
	TotpCode string `json:"totpCode"`
}

type mfaStatusWireResponse struct {
	MfaEnabled   bool   `json:"mfaEnabled"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

func buildMfaConfirmEnrollmentHandler(mfa *mfastate.MfaState, signingSecret []byte) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		accountIdentifier, isValid := requireValidAccessToken(responseWriter, request, signingSecret)
		if !isValid {
			return
		}

		var wireRequest mfaConfirmEnrollmentWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed MFA confirmation payload", http.StatusBadRequest)
			return
		}

		wasConfirmed, confirmError := mfa.ConfirmEnrollment(accountIdentifier, wireRequest.TotpCode, time.Now())
		if confirmError != nil {
			log.Printf("MFA enrollment confirmation error for %s: %v", accountIdentifier, confirmError)
			http.Error(responseWriter, "failed to confirm MFA enrollment", http.StatusInternalServerError)
			return
		}
		if !wasConfirmed {
			respondWithJson(responseWriter, http.StatusBadRequest, mfaStatusWireResponse{
				MfaEnabled:   false,
				ErrorMessage: "invalid MFA code, or no enrollment in progress",
			})
			return
		}

		respondWithJson(responseWriter, http.StatusOK, mfaStatusWireResponse{MfaEnabled: true})
	}
}

// buildMfaDisableHandler turns MFA off for the calling account.
// TODO(real build): this only requires a valid ACCESS token — the same
// thing every other authenticated action here requires — not a fresh
// password or MFA re-confirmation. Disabling a security feature this
// cheaply (an attacker who steals a live access token could turn off
// MFA entirely) is not acceptable in a real build; documented here
// rather than silently shipped as if it were fine.
func buildMfaDisableHandler(mfa *mfastate.MfaState, signingSecret []byte) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		accountIdentifier, isValid := requireValidAccessToken(responseWriter, request, signingSecret)
		if !isValid {
			return
		}

		mfa.DisableMfa(accountIdentifier)
		respondWithJson(responseWriter, http.StatusOK, mfaStatusWireResponse{MfaEnabled: false})
	}
}

func extractBearerToken(request *http.Request) string {
	const bearerPrefix = "Bearer "
	authorizationHeader := request.Header.Get("Authorization")
	if len(authorizationHeader) <= len(bearerPrefix) || authorizationHeader[:len(bearerPrefix)] != bearerPrefix {
		return ""
	}
	return authorizationHeader[len(bearerPrefix):]
}

func respondWithJson(responseWriter http.ResponseWriter, statusCode int, body any) {
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(statusCode)
	_ = json.NewEncoder(responseWriter).Encode(body)
}

// normalizeEmailForRateLimitKey mirrors internal/accountstore's own
// email normalization (lowercase, trimmed) — deliberately duplicated
// rather than imported, since accountstore doesn't export it and pulling
// in normalization logic for a rate-limit key isn't worth widening that
// package's public API for.
func normalizeEmailForRateLimitKey(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// remoteAddressKey strips the port off request.RemoteAddr (host:port)
// so the rate limiter keys on just the address — falls back to the raw
// value if it doesn't parse as host:port (e.g. in some test harnesses).
func remoteAddressKey(request *http.Request) string {
	host, _, splitError := net.SplitHostPort(request.RemoteAddr)
	if splitError != nil {
		return request.RemoteAddr
	}
	return host
}
