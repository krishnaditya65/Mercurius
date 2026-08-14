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
- **Nominee designation** (`internal/nomineedesignation`, FEATURES.md §1
  "Nominee management"): a real brokerage-style nomination form — one or
  more nominees per account (full name, relationship, date of birth,
  percentage allocation, guardian details if the nominee is a minor), or
  an explicit "opt out of nomination" flag. `POST /nominees/submit` is the
  full-form submission and hard-gates on the real invariant: percentage
  allocations across every nominee must sum to EXACTLY 100 (unless opted
  out), at least one nominee is required unless opted out, and any
  nominee computed as under 18 as of submission requires guardian name/
  relationship/identity-document-reference. `POST /nominees/add`, `POST
  /nominees/update`, `POST /nominees/remove` manage an existing (or
  newly-started) designation incrementally — see the "known gap" note
  below on why they're looser than `/submit`. `GET /nominees?accountId=`
  queries the current designation, including a computed `isComplete`
  flag. 21 tests, including: percentages not summing to 100 rejected on
  submit, zero nominees without opt-out rejected, opt-out with zero
  nominees accepted, opt-out clearing previously-supplied nominees, a
  minor nominee without guardian details rejected then accepted once
  supplied, incremental add building an under-100 draft, add rejected
  once it would exceed 100 (state left untouched), update revalidating
  the resulting total, remove always succeeding (may leave the
  designation incomplete), and unknown-account/unknown-nominee-id lookups.
  This package is explicitly DISTINCT from `services/backoffice`'s
  `internal/nomineesuccession` — that package is the later death-claim
  WORKFLOW (state machine triggered by a death certificate submission);
  this package only manages the DESIGNATION record a later succession
  claim would reference by nominee identity, and (unlike
  nomineesuccession's single-nominee model) genuinely supports multiple
  nominees with percentage splits.
- **Joint holding support** (`internal/jointholding`, FEATURES.md §1
  "joint holding support"): registers an account as INDIVIDUAL (one sole
  holder) or JOINT (2+ named holders) under one of three real, standard
  brokerage/depository holding modes — `JOINTLY` (every holder must
  consent to authorize a gated operation), `EITHER_OR_SURVIVOR` (exactly
  2 holders, any single holder's consent authorizes), `ANYONE_OR_SURVIVOR`
  (3+ holders, any single holder's consent authorizes) — with a real
  primary-holder designation. `POST /joint-holding/register-individual`,
  `POST /joint-holding/register-joint` (validates holder count per mode:
  EITHER_OR_SURVIVOR rejects anything but exactly 2, ANYONE_OR_SURVIVOR
  rejects fewer than 3, every mode rejects fewer than 2 holders total or
  a blank holder name or an out-of-range primary-holder index), `POST
  /joint-holding/authorize-operation` (the real per-mode consent check —
  given a set of consenting holder ids, reports whether that's enough to
  authorize a sensitive operation under the account's registered mode),
  `GET /joint-holding?accountId=` queries the current structure. 17
  tests, including: EITHER_OR_SURVIVOR rejecting 3 holders and accepting
  2, ANYONE_OR_SURVIVOR rejecting 2 holders and accepting 3+, JOINTLY
  accepting 2 or more, every mode rejecting fewer than 2 holders / a
  blank name / an out-of-range primary index, JOINTLY authorization
  failing with partial consent and succeeding once every holder has
  consented, EITHER_OR_SURVIVOR authorizing on a single holder's consent,
  and an unknown holder id or unknown account being rejected.
  Intentionally kept as a separate package from `internal/
  nomineedesignation` rather than one merged `nomineeandjointholding`
  package — see this package's own doc comment for the reasoning: joint
  holding is "who legally co-owns and can operate the account", nominee
  designation is "who receives the assets on death" — related
  account-opening concepts, different questions, different validation
  rules, kept as one-concern-per-package like every other internal/
  package in this service.
  **Known gap, loudly documented in the package**: `/nominees/add`,
  `/nominees/update`, and `/nominees/remove` only enforce that the total
  allocation never EXCEEDS 100 (an incomplete, under-100 draft is a
  valid, queryable state via `isComplete`) — only `/nominees/submit`
  hard-gates on the total being EXACTLY 100. This means rebalancing
  percentages across multiple existing nominees in one atomic step isn't
  possible through the incremental endpoints; a real UI doing that would
  either resubmit the whole form via `/nominees/submit`, or this package
  would need a staged/draft-vs-committed concept it doesn't have today.
  "Consent" on the joint-holding side is just a holder id asserted by the
  caller — there is no e-signature, OTP, or any other proof the named
  holder actually gave it, the same honesty gap `bankverification` and
  `nomineesuccession` already document for their own unverified-identity
  fields.

What's a placeholder:
- "Verification" is a PAN format regex check, not a real call to a
  verification provider — see FEATURES.md §1's actual scope (liveness
  check, selfie match, e-signature, Aadhaar).
- No `PENDING_REVIEW` stage / manual review queue between submission and
  a decision — this is exactly the gap backoffice's (still TODO) KYC
  review queue feature would fill.
- In-memory only, no persistence.
- No auth — anyone who can reach `/kyc/submit` or `/bank-verification/*`
  can act on any account. Same is true of every `/nominees/*` and
  `/joint-holding/*` endpoint below — anyone who can reach them can
  register or alter any account's nominees or holding structure.
- Bank verification isn't wired into oms-gateway's order-submission gate
  the way KYC is — nothing currently requires a verified bank account to
  do anything. Nominee designation and joint holding aren't wired into
  anything either — nothing currently requires `isComplete` nomination
  or a registered holding structure before an account can trade; this
  service only records the designations, the same "not yet gating
  anything downstream" gap the risk-profiling questionnaire has.
- The risk-profiling questionnaire feeds nothing yet — no Robo-Advisory
  feature exists to consume the category, and it doesn't gate anything
  (e.g. F&O eligibility) either. The questionnaire design mirrors real
  investor risk-tolerance questionnaires but hasn't been reviewed by an
  actual compliance/suitability professional — don't treat it as legally
  sufficient.
- Nominee/guardian identity document references and joint-holder
  "consent" are all unverified strings/ids asserted by the caller — no
  real identity verification, no e-signature, no OTP. See the nominee
  designation and joint holding bullets above for the specific honesty
  notes on each.

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

# nominee designation — full-form submit (hard-gates on summing to 100)
curl -X POST localhost:8083/nominees/submit -d '{
  "accountIdentifier": "acct-001",
  "nominees": [
    {"fullName":"Alice Trader","relationship":"spouse","dateOfBirth":"1990-05-01","percentageAllocation":60},
    {"fullName":"Bob Trader","relationship":"child","dateOfBirth":"1995-05-01","percentageAllocation":40}
  ],
  "isOptedOutOfNomination": false
}'
curl "localhost:8083/nominees?accountId=acct-001"

# nominee designation — incremental management (looser: allowed to stay
# under 100 as a draft, only rejects going OVER 100)
curl -X POST localhost:8083/nominees/add -d '{
  "accountIdentifier": "acct-002",
  "nominee": {"fullName":"Eve","relationship":"spouse","dateOfBirth":"1990-05-01","percentageAllocation":40}
}'
curl -X POST localhost:8083/nominees/update -d '{
  "accountIdentifier": "acct-002",
  "nomineeId": "<nomineeId from the add response>",
  "nominee": {"fullName":"Eve Updated","relationship":"spouse","dateOfBirth":"1990-05-01","percentageAllocation":100}
}'
curl -X POST localhost:8083/nominees/remove -d '{
  "accountIdentifier": "acct-002",
  "nomineeId": "<nomineeId>"
}'

# joint holding
curl -X POST localhost:8083/joint-holding/register-individual -d '{
  "accountIdentifier": "acct-003",
  "soleHolderFullName": "Solo Trader"
}'
curl -X POST localhost:8083/joint-holding/register-joint -d '{
  "accountIdentifier": "acct-004",
  "holdingMode": "JOINTLY",
  "holderFullNames": ["Carol", "Dave", "Erin"],
  "primaryHolderIndex": 1
}'
curl "localhost:8083/joint-holding?accountId=acct-004"
curl -X POST localhost:8083/joint-holding/authorize-operation -d '{
  "accountIdentifier": "acct-004",
  "consentingHolderIds": ["<all 3 holderIds for JOINTLY, or just 1 for EITHER/ANYONE_OR_SURVIVOR>"]
}'

go test ./... -race
```
