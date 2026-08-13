# backoffice

See `FEATURES.md` §14 in the repo root for the full intended scope.

## Status: account freeze/unfreeze is real, and oms-gateway genuinely gates on it

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

What's a placeholder:
- No auth/RBAC on the freeze/unfreeze endpoints — anyone who can reach
  them can freeze any account. Fine for a skeleton, not for anything with
  real admin access controls.
- No audit trail of who froze/unfroze what and when.
- In-memory only, no persistence.
- Everything else in FEATURES.md §14 (KYC review queue, manual order
  intervention, corporate-actions processing, support ticket integration)
  is still just a TODO.

## Run it

```bash
go run ./cmd/server

curl -X POST localhost:8084/accounts/freeze -d '{"accountIdentifier":"acct-001","freezeReason":"suspected AML flag"}'
curl "localhost:8084/accounts/freeze-status?accountId=acct-001"
curl -X POST localhost:8084/accounts/unfreeze -d '{"accountIdentifier":"acct-001"}'

go test ./...
```
