# auth

New service — see `ARCHITECTURE.md` (API Gateway/BFF's auth responsibility)
and `FEATURES.md` §1 in the repo root.

## Status: real password auth, real JWT access tokens, real refresh-token rotation with reuse detection — not yet integrated with any other service

What's real:
- **Password hashing** (`internal/passwordhashing`): PBKDF2-HMAC-SHA256
  (`crypto/pbkdf2`, Go stdlib as of Go 1.24 — no external dependency),
  random salt per password, constant-time comparison on verify. 5 tests.
- **Account registry** (`internal/accountstore`): `POST /auth/register` /
  `POST /auth/login` — case-insensitive email matching, duplicate-email
  rejection, and the SAME error for "unknown email" vs "wrong password"
  (avoids leaking which emails are registered), with a timing-parity
  dummy-hash comparison on the unknown-email path. 6 tests.
- **HS256 JWT access tokens** (`internal/jwtauth`): hand-rolled
  (header.payload.signature, stdlib `crypto/hmac`/`crypto/sha256` only),
  15-minute lifetime, constant-time signature comparison, distinguishes
  expired/invalid-signature/malformed. 7 tests.
- **Refresh-token rotation with reuse detection** (`internal/
  sessionstore`): every refresh exchanges the presented token for a new
  one in the same "family"; presenting an already-consumed token (the
  signature of a stolen token) revokes the ENTIRE family, not just that
  one token — the same pattern real auth providers (Auth0, Okta, etc.)
  use. 9 tests, including two independent session families (e.g. two
  devices) not interfering with each other.
- **Real HTTP endpoints**: `POST /auth/register`, `POST /auth/login`,
  `POST /auth/refresh`, `POST /auth/logout`, `GET /auth/verify`
  (Bearer-token introspection), `GET /health`. Structured JSON logging
  (`internal/httplogging`, same package as every other Go service). CORS
  (`withPermissiveCorsForDevelopment`, identical to oms-gateway's) so a
  browser can call these directly.
- **`apps/web` has a real login UI** (`AccountSection` in `app/page.tsx`)
  — register/login/logout against this service for real, with the
  actual JWT displayed on login. Verified live including a real cross-
  origin `OPTIONS` preflight + `POST /auth/register`.
- **Rate limiting on login and register** (`internal/ratelimiter`): a
  sliding-window limiter (not a naive fixed/calendar window — tested
  against exactly the double-burst-across-a-boundary failure mode a
  fixed window has), 5 login attempts/minute per account (keyed by
  normalized email, so distributing an attack across many source IPs
  doesn't help an attacker), 3 registration attempts/minute per source
  address. Verified live: 6 rapid wrong-password attempts against one
  account got 401×5 then 429, a different account was unaffected, and 4
  rapid registrations from one address got 201×2 then 429×2 (rate limit
  keyed on address, not email, for registration).
- **MFA — real RFC 6238 TOTP** (`internal/totp`, `internal/mfastate`):
  hand-rolled TOTP (stdlib `crypto/hmac`/`crypto/sha1` only — SHA-1
  because that's what RFC 6238 and every real authenticator app expect,
  not a security downgrade for HMAC's purposes here, see the package
  doc), cross-checked against RFC 6238 Appendix B's published test
  vectors (not just self-consistency). `POST /auth/mfa/enroll` (bearer-
  token-authenticated, generates a secret + `otpauth://` URI for a QR
  code), `POST /auth/mfa/confirm-enrollment` (proves the code works
  before MFA actually activates), `POST /auth/mfa/disable`. Once
  enabled, `POST /auth/login` requires a `totpCode` — a missing one
  returns `{mfaRequired: true}` with no tokens, a wrong one 401s, only a
  correct one issues real tokens. 17 tests (8 totp + 9 mfastate).
  **Verified live with an INDEPENDENT Python TOTP implementation**
  (written fresh, not copy-pasted from this Go code) computing the exact
  same 6-digit code from the exact same secret — a real second
  implementation agreeing, not just this service agreeing with itself.
- **Verified live** (`docs/BUILD_LOG.md` entries 31, 35): register →
  login → verify (valid token accepted, garbage/missing token rejected
  with 401) → refresh (rotation, new access+refresh tokens issued) →
  REUSE the original now-consumed refresh token (rejected, logged as a
  `SECURITY` event) → confirmed the legitimate rotated token is ALSO now
  revoked (whole family burned, not just the reused one) → fresh login →
  logout → confirmed refreshing after logout fails; separately, the full
  MFA flow: enroll → confirm with a wrong code (rejected) → confirm with
  the real code (enabled) → login without a code (`mfaRequired`) → login
  with a wrong code (401) → login with the correct code (real tokens) →
  disable → login without a code succeeds again → enroll without a valid
  bearer token (401). Every step against a real running process, not
  just `go test`. `go test -race ./...` clean (53 tests across 8
  packages: passwordhashing, jwtauth, sessionstore, accountstore,
  httplogging, ratelimiter, totp, mfastate).

What's a placeholder / not built:
- **`apps/web` can register/login/logout for real now, but nothing GATES
  on it yet** — oms-gateway still accepts every request unauthenticated;
  there's no `RequireValidAccessToken` middleware anywhere, and the order
  ticket's account field is a free-text input, not derived from the
  logged-in session's `accountIdentifier`. Wiring real enforcement into
  oms-gateway is explicitly the next step, blocked in practice on
  reconciling the two separate account-identifier spaces (below) first.
  `apps/web` also has no MFA UI yet — the flow above is `curl`-verified
  only, not clickable in the browser.
- **Two separate account-identifier spaces**: this service mints its own
  `acct-<random hex>` identifiers, completely disconnected from
  oms-gateway's `demoTrackedAccountIdentifiers` / ledger's seeded
  accounts. A real build needs ONE canonical account identifier space —
  see the `internal/accountstore` package doc TODO.
- No SMS fallback, no phone-number auth, no device binding, no biometric
  unlock — see FEATURES.md §1's fuller scope. MFA enrollment is
  in-memory only (`internal/mfastate`'s own TODO) — a restart LOCKS OUT
  every enrolled user (the account still conceptually "requires" MFA per
  real intent, but the secret proving a code is gone); no backup/
  recovery codes either. `DisableMfa` only requires a valid access
  token, not a fresh password/MFA re-confirmation — a stolen live access
  token could turn MFA off entirely, documented as unacceptable for a
  real build.
- No password-strength policy, no email verification (an unverified
  address can register and log in immediately).
- In-memory only for everything (accounts, sessions, rate-limit state) —
  a restart loses every registered account, active session, AND resets
  every rate limit.
- HS256 means every service that verifies a token needs the same shared
  secret — a real build would likely prefer RS256/ES256 (asymmetric) so
  the signing secret never leaves this service. See the package doc.
- `AUTH_JWT_SIGNING_SECRET` falls back to an insecure hardcoded default
  if unset — fine for local dev, loudly logged, but must come from a
  real secrets manager (FEATURES.md §13) before production.
- The registration rate limiter keys on `request.RemoteAddr` directly,
  not real client-IP-aware `X-Forwarded-For` handling — behind any real
  load balancer/reverse proxy this would key on the proxy's address for
  every request, not the actual client. Not meaningful behind anything
  but a direct connection yet.
- Rate-limiter memory is unbounded — an attacker who tries many distinct
  emails (login) or source addresses (register) grows the map forever;
  a real build needs eviction/TTL on stale keys.

## Run it

```bash
go run ./cmd/server
# AUTH_LISTEN_ADDRESS (default :8086), AUTH_JWT_SIGNING_SECRET
# (default: an insecure dev-only value, loudly logged) are overridable

# register
curl -X POST localhost:8086/auth/register -d '{
  "email": "jane@example.com",
  "password": "correct horse battery staple"
}'

# login — returns accessToken (15min) + refreshToken (30 days)
curl -X POST localhost:8086/auth/login -d '{
  "email": "jane@example.com",
  "password": "correct horse battery staple"
}'

# verify a bearer token
curl localhost:8086/auth/verify -H "Authorization: Bearer <accessToken>"

# refresh — rotates: returns a NEW access+refresh token pair; the
# presented refreshToken can never be used again
curl -X POST localhost:8086/auth/refresh -d '{"refreshToken": "<refreshToken>"}'

# logout — revokes the whole session family
curl -X POST localhost:8086/auth/logout -d '{"refreshToken": "<refreshToken>"}'

# MFA: enroll (requires a valid accessToken from login above)
curl -X POST localhost:8086/auth/mfa/enroll -H "Authorization: Bearer <accessToken>"
# -> {"secretBase32": "...", "otpAuthUri": "otpauth://totp/..."}
# scan/paste the secret into a real authenticator app, or compute a
# code yourself with any RFC 6238 TOTP tool, then:
curl -X POST localhost:8086/auth/mfa/confirm-enrollment \
  -H "Authorization: Bearer <accessToken>" -d '{"totpCode": "<code>"}'

# login now requires totpCode
curl -X POST localhost:8086/auth/login -d '{
  "email": "jane@example.com",
  "password": "correct horse battery staple"
}'
# -> {"mfaRequired": true} — no tokens yet
curl -X POST localhost:8086/auth/login -d '{
  "email": "jane@example.com",
  "password": "correct horse battery staple",
  "totpCode": "<code>"
}'
# -> real tokens

curl -X POST localhost:8086/auth/mfa/disable -H "Authorization: Bearer <accessToken>"

go test ./... -race
```
