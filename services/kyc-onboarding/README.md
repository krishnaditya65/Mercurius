# kyc-onboarding

See `FEATURES.md` §1 in the repo root for the full intended scope.

## Status: PAN-format-check and bank penny-drop verification are both real; oms-gateway gates on the KYC one

What's real:
- `internal/kycstate`: a real (if simplified) verification state machine
  — `SubmitKycDetails` validates PAN format (`AAAAA9999A`) and full name,
  marks the account `VERIFIED` or `REJECTED` with a reason; `LookupKycStatus`
  answers `NOT_SUBMITTED` for an account that never submitted. 5 tests.
- `POST /kyc/submit`, `GET /kyc/status?accountId=...` — real HTTP endpoints
  over that state machine.
- **`oms-gateway` genuinely rejects order submission** for an account that
  hasn't submitted valid KYC details — verified end-to-end (`docs/
  BUILD_LOG.md` entry 16): an order was rejected before KYC, a malformed
  PAN was rejected, a valid PAN was verified, and the same order then
  succeeded.
- **Structured (JSON) logging** (`internal/httplogging`): every HTTP
  request logs a machine-parseable `http_request` line; `slog.SetDefault`
  upgrades every pre-existing `log.Printf` line to structured JSON too.
  Stdout only.
- **Bank account verification — penny-drop / micro-deposit**
  (`internal/bankverification`, FEATURES.md §1): `POST
  /bank-verification/initiate` generates a real random micro-deposit
  amount (1-99 minor units) that the caller does NOT get back; `POST
  /bank-verification/confirm` checks a claimed amount against it, with a
  hard 3-attempt limit before permanently locking that verification
  (`FAILED_LOCKED` — even the correct amount no longer works, a fresh
  `initiate` call is required). 7 tests, including the lockout path and
  confirming a locked verification rejects the correct answer too.
  **Known gap, loudly documented in the package**: there's no real
  payment rail in this repo to actually deposit anything into an
  external bank account, so the "check your bank statement" step a real
  penny-drop flow relies on is stood in for by a debug-only endpoint
  (`GET /bank-verification/debug-peek`) — a real build deletes that
  endpoint entirely once wired to a real banking API.
- **Risk profiling questionnaire → investor risk category**
  (`internal/riskprofiling`, FEATURES.md §1, feeds a NOT-built
  Robo-Advisory feature): a fixed 6-question, 5-option-per-question
  Likert-style questionnaire (investment horizon, drawdown reaction,
  income stability, investment goal, investing experience, emergency
  fund coverage), scored 6-30 and classified into one of 5 categories
  (Conservative → Aggressive). `GET /risk-profile/questionnaire` (the
  static question set), `POST /risk-profile/submit` (validates every
  question is answered with a real option's point value, no more, no
  less), `GET /risk-profile?accountId=...`. 11 tests, including sweeping
  all 5 category bands and rejecting an unknown question id or an
  invalid point value. Verified live: conservative answers → CONSERVATIVE
  (score 6), aggressive answers → AGGRESSIVE (score 30), an invalid point
  value correctly rejected.

What's a placeholder:
- "Verification" is a PAN format regex check, not a real call to a
  verification provider — see FEATURES.md §1's actual scope (liveness
  check, selfie match, e-signature, Aadhaar).
- No `PENDING_REVIEW` stage / manual review queue between submission and
  a decision — this is exactly the gap backoffice's (still TODO) KYC
  review queue feature would fill.
- In-memory only, no persistence.
- No auth — anyone who can reach `/kyc/submit` or `/bank-verification/*`
  can act on any account.
- Bank verification isn't wired into oms-gateway's order-submission gate
  the way KYC is — nothing currently requires a verified bank account to
  do anything.
- The risk-profiling questionnaire feeds nothing yet — no Robo-Advisory
  feature exists to consume the category, and it doesn't gate anything
  (e.g. F&O eligibility) either. The questionnaire design mirrors real
  investor risk-tolerance questionnaires but hasn't been reviewed by an
  actual compliance/suitability professional — don't treat it as legally
  sufficient.

## Run it

```bash
go run ./cmd/server

curl -X POST localhost:8083/kyc/submit -d '{
  "accountIdentifier": "acct-001",
  "panNumber": "ABCDE1234F",
  "fullName": "Jane Trader"
}'
curl "localhost:8083/kyc/status?accountId=acct-001"

# bank account verification (penny-drop)
curl -X POST localhost:8083/bank-verification/initiate -d '{
  "accountIdentifier": "acct-001",
  "bankAccountNumber": "1234567890",
  "ifscCode": "HDFC0001234"
}'
# -> {"verificationId": "..."}

# in a real build the account holder would check their bank statement;
# this skeleton has no real payment rail, so debug-peek stands in for
# that step (see the loud caveat on it above and in the code)
curl "localhost:8083/bank-verification/debug-peek?verificationId=<id>"

curl -X POST localhost:8083/bank-verification/confirm -d '{
  "verificationId": "<id>",
  "claimedAmountMinorUnits": <the amount from debug-peek>
}'
# -> {"status": "VERIFIED"} — or "PENDING" with attempts remaining,
# or "FAILED_LOCKED" after 3 wrong guesses

curl "localhost:8083/bank-verification/status?verificationId=<id>"

# risk profiling questionnaire
curl localhost:8083/risk-profile/questionnaire
curl -X POST localhost:8083/risk-profile/submit -d '{
  "accountIdentifier": "acct-001",
  "answerPointValuesByQuestionId": {
    "investment-horizon": 3,
    "drawdown-reaction": 3,
    "income-stability": 3,
    "investment-goal": 3,
    "investing-experience": 3,
    "existing-emergency-fund": 3
  }
}'
curl "localhost:8083/risk-profile?accountId=acct-001"

go test ./... -race
```
