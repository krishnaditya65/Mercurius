# ledger

Tier 2 component — see `ARCHITECTURE.md` §6 in the repo root.

## Status: the accounting core is real, and it's now actually reachable end-to-end

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

## Run it

```bash
go run ./cmd/server

# fund an account (debit the account, credit the clearing account)
curl -X POST localhost:8082/journal-entries -d '{
  "humanReadableDescription": "demo funding",
  "debitLines": [{"ledgerAccountIdentifier":"acct-001","amountInMinorUnits":1000000}],
  "creditLines": [{"ledgerAccountIdentifier":"firm-clearing-acct","amountInMinorUnits":1000000}]
}'

curl "localhost:8082/accounts/balance?accountId=acct-001"

go test ./...
```
