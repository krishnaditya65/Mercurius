# backoffice

See `FEATURES.md` §14 in the repo root for the full intended scope.

## Status: account freeze/unfreeze is real (oms-gateway genuinely gates on it); strategy leaderboard, family account access, and nominee succession are also real

What's real:
- `internal/accountcontrol`: `FreezeAccount`/`UnfreezeAccount`/
  `CheckFreezeStatus` — in-memory registry, absence from the map means
  "not frozen". 4 tests.
- `POST /accounts/freeze` (requires a `freezeReason` — freezing without a
  recorded reason is rejected), `POST /accounts/unfreeze`,
  `GET /accounts/freeze-status?accountId=...`.
- **`oms-gateway` genuinely rejects order submission** for a frozen
  account — verified end-to-end (`docs/BUILD_LOG.md` entry 16): an order
  succeeded, the account was frozen, the next order was rejected with
  `ACCOUNT_FROZEN` and the recorded reason, the account was unfrozen, and
  orders succeeded again.
- **Structured (JSON) logging** (`internal/httplogging`): every HTTP
  request logs a machine-parseable `http_request` line; `slog.SetDefault`
  upgrades every pre-existing `log.Printf` line (e.g. "FROZEN: ...") to
  structured JSON too. Stdout only.

- **Verified-track-record strategy leaderboard** (`internal/
  strategyleaderboard`), FEATURES.md §19/§11 — `GET /strategy-
  leaderboard` fetches oms-gateway's real admin-verified strategies +
  live follower counts (`internal/strategyfollowing`, over HTTP via
  `internal/omsgatewayclient`) and ranks them descending by a real,
  live, per-strategy figure from oms-gateway's `internal/algolimits`:
  cumulative notional traded today. **Honesty note (the key bar this
  item was built to)**: NOT self-reported — no endpoint here or
  anywhere in this package accepts a caller-supplied performance
  number; every ranked figure is fetched from oms-gateway, never posted
  in. **Second honesty note (a real, documented gap)**: oms-gateway does
  not expose per-strategy REALIZED P&L/RETURNS anywhere — its audit
  trail records fills with buyer/seller account IDs but, confirmed by
  reading that package and its call sites directly, fill entries are
  NOT tagged with a `strategyIdentifier` at all (only
  `STRATEGY_LIMIT_REJECTED` rejection entries are). So this package uses
  the one real per-strategy figure that IS queryable — trading notional/
  turnover — as an activity proxy, and every response entry carries an
  explicit `isPerformanceDataAvailable: false` so a real client can't
  mistake volume for returns. A strategy trading a large notional could
  still be losing money; this proxy says nothing about that. 7 tests.
  **Verified live**: verified two strategies on a real running
  oms-gateway, followed one 3x and the other 1x, submitted a real order
  tagged with the less-followed strategy's `strategyIdentifier`
  (rejected downstream on KYC, but the algo-limits notional reservation
  happens BEFORE the KYC gate — see oms-gateway's own ordering — so it
  still counted), and confirmed the leaderboard ranked the
  less-followed-but-more-actively-traded strategy first.
- **Family/joint account view-only access** (`internal/
  familyaccountaccess`), FEATURES.md §21 — real account-linking:
  `POST /family-access/link` links a `viewerAccountIdentifier` to an
  `ownerAccountIdentifier` at `VIEW_ONLY` or `VIEW_AND_TRADE` (both
  modeled; only `VIEW_ONLY`'s guarantee — read access, nothing else — is
  actually enforced, since this package exposes no order-submission
  capability at all regardless of the permission level recorded).
  `GET /family-access/positions?ownerAccountId=...&viewerAccountId=...`
  is the real read-only aggregation endpoint: it calls
  `AuthorizeViewOnlyAccess` first (403s with no oms-gateway call at all
  if the viewer has no link to the owner), then fetches the owner's
  real, live positions from oms-gateway's `GET /positions` via
  `internal/omsgatewayclient`. `internal/familyaccountaccess` has a
  dedicated `TestExposedCapabilitySetIsReadOnly` test that reflects over
  the package's method set and fails if anything with a name like
  `Submit`/`Trade`/`Execute`/etc. is ever added — a real, enforced
  assertion of the read-only boundary, not just a doc comment. 17 tests.
  **Verified live**: linked a viewer to an owner account, fetched the
  owner's real positions as that viewer (succeeded), tried the same
  fetch as a never-linked stranger (403, no positions leaked), revoked
  the link, and confirmed the originally-linked viewer is rejected too
  afterward.
- **Nominee succession workflow** (`internal/nomineesuccession`),
  FEATURES.md §21 — modeled closely on `internal/accountcontrol`'s
  freeze state machine, with a real audit trail mirroring oms-gateway's
  `internal/audittrail`'s immutable-log guarantee (no update/delete
  method exists on the trail at all). `POST /nominee-succession/
  register-nominee` records a real nominee (name, relationship,
  illustrative identity-document reference) for an account.
  `POST /nominee-succession/submit` starts a real succession request,
  triggered by a "death certificate submission" event — accepted as a
  real reference/document-id string; **explicitly NOT verified**, see
  the package doc's scope boundary: this is a workflow/paperwork state
  machine, not an identity-verification feature. Real state machine:
  `SUBMITTED -> UNDER_REVIEW -> APPROVED -> TRANSFERRED`, with
  `REJECTED` reachable from `SUBMITTED` or `UNDER_REVIEW` — every
  transition endpoint (`/move-to-under-review`, `/approve`,
  `/mark-transferred`, `/reject`) enforces the real state machine (e.g.
  approving a request still in `SUBMITTED`, which never entered review,
  is rejected with `ErrInvalidStateTransition`) and every transition
  requires a real, non-empty `actor`. `GET /nominee-succession/
  audit-trail?accountId=...` returns the real, append-only who/when/why
  log of every transition (omit `accountId` for every account). 23
  tests. **Verified live**: registered a nominee, submitted a succession
  request, confirmed a premature `/approve` (skipping review) was
  rejected, then walked the full real path
  SUBMITTED→UNDER_REVIEW→APPROVED→TRANSFERRED and confirmed the audit
  trail recorded all 5 transitions (including nominee registration) with
  the correct actor/reason/timestamp on each.

What's a placeholder:
- No auth/RBAC on the freeze/unfreeze endpoints, the strategy-
  leaderboard endpoint, the family-access endpoints, or the
  nominee-succession endpoints — anyone who can reach them can freeze
  any account, link any two accounts together, or drive any account's
  succession workflow. Fine for a skeleton, not for anything with real
  admin access controls.
- No audit trail of who froze/unfroze what and when (accountcontrol
  specifically — nomineesuccession DOES have a real one, see above).
- In-memory only, no persistence, across every package in this service.
- `internal/strategyleaderboard` has no real per-strategy P&L/returns
  data to rank on (see its honesty note above) — the ranking proxy is
  trading notional, not performance; no caching (queries oms-gateway
  fresh every request); no time-windowing.
- `internal/familyaccountaccess` has no identity verification that a
  linked "viewer" account is actually who they claim to be in relation
  to the owner; `VIEW_AND_TRADE` is modeled but grants no extra
  capability through this package (there's no order-submission code path
  here to grant in the first place).
- `internal/nomineesuccession` makes zero attempt to verify the
  submitted death-certificate reference or the nominee's identity-
  document reference are genuine (see its scope-boundary note); reaching
  `TRANSFERRED` records that the state was reached but doesn't itself
  move any real ledger balance or position; no support for multiple
  competing nominees or a dispute-resolution process.
- **Anomalous-login/account-takeover detection is in `services/auth`**
  (`internal/anomalouslogindetection`), not here — see that service's
  README. It's a distinct capability from account freeze/unfreeze above
  (account-security/login-behavior, not compliance/AML account
  intervention), which is why it lives in auth rather than backoffice.
- Everything else in FEATURES.md §14 (KYC review queue, manual order
  intervention, corporate-actions processing, support ticket integration)
  is still just a TODO.

## Run it

```bash
go run ./cmd/server
# OMS_GATEWAY_BASE_URL (default http://127.0.0.1:8081) is overridable —
# only strategy-leaderboard and family-access/positions call out to it

curl -X POST localhost:8084/accounts/freeze -d '{"accountIdentifier":"acct-001","freezeReason":"suspected AML flag"}'
curl "localhost:8084/accounts/freeze-status?accountId=acct-001"
curl -X POST localhost:8084/accounts/unfreeze -d '{"accountIdentifier":"acct-001"}'

# strategy leaderboard — ranks oms-gateway's real verified strategies by
# real notional traded today (requires oms-gateway running on :8081)
curl localhost:8084/strategy-leaderboard

# family/joint account view-only access
curl -X POST localhost:8084/family-access/link -d '{
  "ownerAccountIdentifier": "acct-owner",
  "viewerAccountIdentifier": "acct-viewer",
  "permissionLevel": "VIEW_ONLY"
}'
curl "localhost:8084/family-access/links?ownerAccountId=acct-owner"
# real, read-only positions aggregation — pulls from oms-gateway's
# GET /positions; 403s if viewer has no link to owner
curl "localhost:8084/family-access/positions?ownerAccountId=acct-owner&viewerAccountId=acct-viewer"
curl -X POST localhost:8084/family-access/revoke -d '{
  "ownerAccountIdentifier": "acct-owner", "viewerAccountIdentifier": "acct-viewer"
}'

# nominee succession workflow
curl -X POST localhost:8084/nominee-succession/register-nominee -d '{
  "accountIdentifier": "acct-owner", "nomineeFullName": "Alex Doe",
  "nomineeRelationship": "child", "nomineeIdentityDocumentReference": "passport-ref-1",
  "actor": "admin-1"
}'
curl -X POST localhost:8084/nominee-succession/submit -d '{
  "accountIdentifier": "acct-owner", "deathCertificateDocumentReference": "death-cert-doc-1",
  "actor": "admin-1"
}'
curl -X POST localhost:8084/nominee-succession/move-to-under-review -d '{
  "accountIdentifier": "acct-owner", "actor": "reviewer-1", "reason": "starting review"
}'
curl -X POST localhost:8084/nominee-succession/approve -d '{
  "accountIdentifier": "acct-owner", "actor": "reviewer-1", "reason": "documents check out"
}'
curl -X POST localhost:8084/nominee-succession/mark-transferred -d '{
  "accountIdentifier": "acct-owner", "actor": "ops-1", "reason": "assets transferred"
}'
curl "localhost:8084/nominee-succession/status?accountId=acct-owner"
curl "localhost:8084/nominee-succession/audit-trail?accountId=acct-owner"

go test ./... -race
```
