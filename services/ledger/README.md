# ledger

Tier 2 component — see `ARCHITECTURE.md` §6 in the repo root.

## Status: the accounting core is real, reachable end-to-end, and now has a real withdrawal workflow

What's real:
- Genuine double-entry bookkeeping (`internal/doubleentry`): every journal
  entry must balance (debits == credits) or it's rejected outright, and no
  entry is ever partially applied — tested for both the happy path and the
  two failure modes (unbalanced entry, unknown account)
- **A real `POST /journal-entries` HTTP endpoint** — `oms-gateway` posts
  trade settlement entries here on every fill. Verified end-to-end
  (`docs/BUILD_LOG.md` entry 15): a real trade between two live accounts
  moved real balances, checked via `GET /accounts/balance` before and
  after.
- Seed accounts (`acct-001`, `acct-002`, `firm-clearing-acct`)
  deliberately match `oms-gateway`'s demo accounts so the two services can
  be exercised together without extra setup.
- **Structured (JSON) logging** (`internal/httplogging`): every HTTP
  request logs a machine-parseable `http_request` line, and
  `slog.SetDefault` upgrades every pre-existing `log.Printf` line (e.g.
  "journal entry rejected") to structured JSON too, for free. Stdout only
  — not yet shipped to a real log aggregation backend.
- **Withdrawal workflow with T+N settlement holds**
  (`internal/withdrawalworkflow`, FEATURES.md §2): `POST
  /withdrawals/request` places a HOLD (not an immediate transfer) —
  reduces the account's *available* balance without touching the raw
  ledger balance, rejects if it would exceed what's actually available
  (holds stack — a second request can't double-spend an amount an
  earlier one already reserved). `POST /withdrawals/process-due` sweeps
  every hold whose settlement period has elapsed and posts a REAL,
  balanced journal entry through the exact same `doubleentry` core
  everything else uses — money genuinely leaves the account at that
  point, not just a status-field flip. `POST /withdrawals/cancel`
  releases a still-pending hold. `GET /withdrawals?accountId=...` for
  history, `GET /accounts/balance` now returns both
  `currentBalanceInMinorUnits` (raw) and `availableBalanceInMinorUnits`
  (raw minus pending holds) since they're genuinely different numbers
  once any hold exists. 13 tests. Verified live end-to-end: funded an
  account, requested a withdrawal (available balance dropped, raw
  balance didn't), confirmed a second request exceeding what was left
  was rejected, processed due withdrawals (raw balance actually dropped
  this time, matching available), then separately requested and
  cancelled another withdrawal and confirmed the balance was fully
  restored. `WITHDRAWAL_SETTLEMENT_HOLD_DAYS` env var (default 2)
  overridable for testing without waiting real days — set to 0 in the
  live-verification run above.

What's a placeholder:
- In-memory only — no PostgreSQL persistence yet
- No client-fund segregation (FEATURES.md §1) at the account-structure level
- Settlement is posted synchronously, inline in oms-gateway's order-
  submission handler — not consumed asynchronously from an event backbone
  the way ARCHITECTURE.md §6 describes
- No chart-of-accounts semantics (asset vs. liability normal balances) —
  uses one uniform debit/credit convention for the skeleton
- No reconciliation/retry job if a settlement post fails — currently just
  logged loudly by oms-gateway (`SETTLEMENT FAILED`)
- Withdrawal payout is still a ledger-internal journal entry, not a real
  bank transfer — there's no real payment rail anywhere in this repo
  (same category of gap as kyc-onboarding's bank-verification penny-drop)
- `POST /withdrawals/process-due` is manually/externally triggered, not
  run on a real scheduled job
- No auth on any endpoint — anyone who can reach `/withdrawals/*` can
  request or cancel a withdrawal for any account

## Run it

```bash
go run ./cmd/server
# WITHDRAWAL_SETTLEMENT_HOLD_DAYS (default 2) is overridable

# fund an account (debit the account, credit the clearing account)
curl -X POST localhost:8082/journal-entries -d '{
  "humanReadableDescription": "demo funding",
  "debitLines": [{"ledgerAccountIdentifier":"acct-001","amountInMinorUnits":1000000}],
  "creditLines": [{"ledgerAccountIdentifier":"firm-clearing-acct","amountInMinorUnits":1000000}]
}'

curl "localhost:8082/accounts/balance?accountId=acct-001"
# -> {"currentBalanceInMinorUnits": 1000000, "availableBalanceInMinorUnits": 1000000}

# request a withdrawal — places a hold, doesn't move money yet
curl -X POST localhost:8082/withdrawals/request -d '{
  "accountIdentifier": "acct-001",
  "amountInMinorUnits": 400000
}'
curl "localhost:8082/accounts/balance?accountId=acct-001"
# -> availableBalanceInMinorUnits drops by 400000, currentBalanceInMinorUnits unchanged

# once the settlement hold period elapses (or immediately, if
# WITHDRAWAL_SETTLEMENT_HOLD_DAYS=0), sweep and actually pay out
curl -X POST localhost:8082/withdrawals/process-due
curl "localhost:8082/accounts/balance?accountId=acct-001"
# -> currentBalanceInMinorUnits has now genuinely dropped too

curl "localhost:8082/withdrawals?accountId=acct-001"

go test ./... -race
```
