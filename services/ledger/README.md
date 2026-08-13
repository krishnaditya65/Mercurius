# ledger

Tier 2 component — see `ARCHITECTURE.md` §6 in the repo root.

## Status: the accounting core is real, reachable end-to-end, and now has a real withdrawal workflow, a simulated deposit rail, and simulated SIP payment mandates

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
- **Client fund segregation** (`internal/fundsegregation`, FEATURES.md
  §1): accounts are classified CLIENT or FIRM, and a dedicated
  `client-money-custody-pool` account's balance is a genuine, enforced
  invariant — it must always equal the sum of every CLIENT account's
  balance. `POST /client-funds/deposit` is the ring-fenced replacement
  for funding/paying out a client account (moves both the client account
  and the custody pool together, through a real three-leg balanced
  journal entry — not just a convention). `POST /client-funds/transfer`
  moves money between two CLIENT accounts without touching custody at
  all (rejected outright if either side isn't classified CLIENT — can't
  be used to leak client money to a firm account). `GET
  /client-funds/segregation-report` is the compliance/regulator-facing
  check: custody pool balance vs. aggregate client balance, right now.
  `POST /client-funds/validate-entry` is a dry-run — checks whether an
  arbitrary journal entry payload would break the invariant without ever
  posting it. 13 tests. Verified live: deposited into two client
  accounts (report stayed intact), transferred between them (report
  stayed intact — no custody movement needed), a same-endpoint attempt to
  transfer client money to `firm-clearing-acct` was rejected outright,
  a dry-run against the *old* funding pattern (debit client / credit
  `firm-clearing-acct`) was correctly flagged as segregation-breaking
  without posting it, and — to prove the report isn't just always
  green — posting a real entry through the pre-existing, unmigrated
  `/journal-entries` endpoint using that old pattern produced a genuine,
  correctly-computed discrepancy in the segregation report. That
  discrepancy is expected and documented, not a bug: it's the honest
  state of a partially-migrated ledger, see "What's a placeholder" below.
- **AML transaction monitoring** (`internal/amlmonitoring`, FEATURES.md
  §1): rule-based, real (not simulated) pattern detection over every
  transaction reported to it. Three rules: a single transaction at or
  above a large-value threshold; velocity (too many transactions for one
  account within a time window); structuring (several individually
  sub-threshold transactions whose sum within a time window crosses the
  reporting threshold — the classic technique of splitting a large
  transaction to dodge reporting). Plus a static-list PEP name screen.
  `POST /client-funds/deposit` and `POST /withdrawals/request` both
  report every real, successfully-posted transaction to the monitor.
  `GET /aml/alerts` (optionally `?accountId=...`) is the compliance
  review queue. `POST /aml/screen-name` runs the PEP check. 15 tests.
  Verified live end-to-end: a large deposit triggered a
  `LARGE_TRANSACTION` alert immediately; three sub-threshold deposits
  summing over the reporting threshold triggered `STRUCTURING` on the
  third one, not before; a PEP-listed name matched case-insensitively,
  an ordinary name didn't; six withdrawal requests within an hour
  (limit 5) triggered real `VELOCITY` alerts; the aggregate `/aml/alerts`
  view correctly showed every alert across every account, chronologically
  sorted.

- **Simulated UPI / NEFT / IMPS / net-banking deposit rail**
  (`internal/depositrail`, FEATURES.md §2): **this is NOT a real bank
  integration** — no actual UPI, NEFT, IMPS, or net-banking network call
  happens anywhere in this codebase; see the package doc for the full
  disclaimer. What IS real: the two-phase request/confirm state machine
  a real integration would need. `POST /deposits/initiate` records a
  client's claim to be sending money via one of the four methods and
  starts the deposit `PENDING` — no money moves yet. `POST
  /deposits/confirm` stands in for the bank's webhook/callback firing
  once money actually clears, and is the ONLY place real money moves: it
  posts a real, ring-fenced journal entry through the exact same
  `fundsegregation.SegregationGuard.PostClientMoneyMovement`
  `/client-funds/deposit` uses — not a status-field flip. Confirming an
  already-confirmed or unknown deposit is rejected outright (no
  double-posting). Every confirmed deposit is reported to
  `amlmonitoring.Monitor`, same as `/client-funds/deposit`. `GET
  /deposits?accountId=...` for history. 13 tests. Verified live: an
  account's balance was genuinely unchanged right after `/deposits/
  initiate`, jumped by the deposited amount only after `/deposits/
  confirm`, the segregation report stayed intact throughout, and a
  second confirm attempt on the same deposit was rejected.
- **Auto-payment mandates for SIPs (eNACH/standing instructions)**
  (`internal/paymentmandate`, FEATURES.md §2): **this is NOT real
  eNACH** — no actual bank mandate registration happens anywhere in this
  codebase; see the package doc for the full disclaimer. What IS real:
  the recurring-debit state machine a real eNACH integration would need.
  `POST /payment-mandates/register` creates an `ACTIVE` standing
  instruction (account, amount, frequency — `DAILY`/`WEEKLY`/`MONTHLY`,
  and the next debit date). `POST /payment-mandates/sweep-due` — the
  `ProcessDueWithdrawals`-style sweep — executes every `ACTIVE` mandate
  whose next debit date has arrived: posts a REAL debit (a negative
  client money movement, since a SIP mandate pulls money OUT of the
  client account to invest elsewhere) via the same
  `fundsegregation.SegregationGuard`, then advances the next debit date
  by one frequency period, genuinely moving money each cycle rather than
  flipping a status field. `POST /payment-mandates/pause` and `POST
  /payment-mandates/resume` suspend/reactivate an `ACTIVE`/`PAUSED`
  mandate; `POST /payment-mandates/cancel` permanently terminates one —
  a cancelled mandate can never resume or sweep again. `GET
  /payment-mandates?accountId=...` for history. 17 tests. Verified live:
  a mandate registered with an immediately-due `nextDebitDate` was swept
  and the account balance genuinely dropped by the mandate amount, its
  `nextDebitDate` advanced by one month; a second mandate paused before
  ever sweeping was skipped by `sweep-due` (balance untouched), remained
  skipped after being cancelled, and a resume attempt on the cancelled
  mandate was correctly rejected.

- **Multi-currency wallet** (`internal/multicurrencywallet`, FEATURES.md
  §2, "for platforms offering global/US stocks"): a NEW layer on top of
  `internal/doubleentry` — the accounting core's "minor units, no
  currency tag" contract is completely unchanged, so oms-gateway's trade
  settlement posts are unaffected. Each (account, currency) pair is a
  real, separate doubleentry-backed ledger account: an account's INR
  wallet is an alias for its pre-existing raw ledger account (the exact
  same balance `/accounts/balance` and everything else already reads),
  and every other currency (USD, etc.) gets its own genuinely new
  `"<accountId>:<CURRENCY>"` sub-account, registered lazily on first use.
  `POST /wallets/deposit` and `POST /wallets/withdraw` are real,
  currency-scoped movements: depositing INR into a CLIENT-classified
  account routes through the exact same `fundsegregation.SegregationGuard`
  `/client-funds/deposit` uses (so the acct-001/acct-002 custody-pool
  invariant stays intact, unmodified); every other case posts a real
  balanced journal entry against a dedicated
  `wallet-external-cash-suspense` account instead. `POST /wallets/convert`
  moves money between two currency wallets of the SAME account via one
  real, balanced journal entry — debit the destination wallet the
  converted amount, credit the source wallet the source amount, plus a
  small `fx-conversion-clearing-acct` leg that exists purely to satisfy
  `doubleentry`'s currency-agnostic "debits must equal credits" check
  when the two legs are numerically different magnitudes (e.g. 10,000 USD
  cents converting to 830,000 INR paise) — see the package doc for
  exactly why that leg is needed and what it does and doesn't mean. If
  either leg of a conversion is a CLIENT account's INR wallet, the SAME
  journal entry also moves the custody pool by that leg's exact delta, so
  `isSegregationIntact` stays true throughout. `GET /wallets?accountId=`
  returns every currency balance opened for an account. **The FX rate
  table (illustrative example: USD/INR = 83.0) is STATIC AND HARDCODED —
  NOT a live market feed.** 18 tests, including a fully hand-worked
  conversion (100.00 USD at the static 83.0 rate converts to EXACTLY
  8,300.00 INR — 10,000 minor units in, 830,000 minor units out, no
  rounding) and dedicated currency-isolation tests (a withdrawal
  exceeding one currency's balance is rejected even when another
  currency for the same account has plenty). Verified live: depositing
  into acct-001's USD wallet left its raw INR balance and
  `/client-funds/segregation-report` completely unchanged; converting
  100 USD to INR moved the exact hand-computed 830,000 minor units and
  kept the segregation invariant intact (custody pool balance tracked
  the account's new INR total exactly); withdrawing from an empty USD
  wallet was rejected even with a large INR balance present, and a
  partial-balance USD withdrawal (5,000 available, 6,000 requested) was
  also rejected without touching the 5,000 that was there; an
  unconfigured currency pair (GBP/JPY) was rejected outright.

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
- Client fund segregation is enforced ONLY on the new
  `/client-funds/*` endpoints. The pre-existing `/journal-entries`
  endpoint (used by oms-gateway for trade settlement, and by this
  README's own demo funding example) is NOT migrated to route through
  the segregation guard, so it can still post entries that move client
  money without touching the custody pool — exactly what
  `/client-funds/segregation-report` will correctly flag as a
  discrepancy if you exercise it, as demonstrated in the live
  verification above. The real build needs every client-money-touching
  path (deposits, withdrawals, trade settlement) migrated onto
  `internal/fundsegregation`, not just the new deposit/transfer
  endpoints.
- The custody pool has no real bank account behind it — it's just
  another in-memory ledger account, same as everything else here
- AML thresholds are illustrative constants, not derived from any real
  regulatory reporting limit or tuned against real transaction data; PEP
  screening is a static hardcoded name list, not a real sanctions/PEP
  database with fuzzy matching; there's no case-management lifecycle for
  an alert once raised (no assign/investigate/close/escalate-to-STR
  workflow); `/journal-entries`, `/withdrawals/process-due`, and
  `/payment-mandates/sweep-due` (all real money-leaving/entering moments)
  are NOT reported to the AML monitor — only `/client-funds/deposit`,
  `/withdrawals/request`, and `/deposits/confirm` are, so trade
  settlement and SIP mandate debits aren't monitored yet
- `internal/depositrail` is a SIMULATED UPI/NEFT/IMPS/net-banking rail —
  there is no PSP/bank integration, no NPCI switch, no cryptographically
  verified webhook signature; `/deposits/confirm` is a same-process call
  any caller can invoke to pretend to be the bank, and there's no
  REJECTED/FAILED terminal status for a deposit a real bank would
  actually decline (only the successful PENDING -> CONFIRMED path plus
  rejecting a double-confirm is modelled)
- `internal/paymentmandate` is SIMULATED eNACH — there is no NPCI eNACH
  API call and no bank-verified standing instruction backing a mandate;
  `/payment-mandates/sweep-due` is manually/externally triggered, not
  run on a real scheduled job, matching `/withdrawals/process-due`'s
  same caveat; a failed sweep is reported but not retried, and there's
  no real investment destination for swept money — it only leaves the
  client account via the segregation guard's external suspense leg
- No auth on any endpoint — anyone who can reach `/deposits/*` or
  `/payment-mandates/*` can initiate/confirm a deposit or
  register/sweep a mandate for any account
- `internal/multicurrencywallet`'s FX rate table is a STATIC, HARDCODED
  illustrative map (e.g. `USD/INR: 83.0`) — there is no live market data
  feed anywhere in this codebase, no bid/ask spread (one flat mid-rate is
  used for both directions), and rates never change while the process
  runs; a real build needs a genuine live FX feed. There is no real
  foreign-currency custody/settlement rail either — a real global-stocks
  broker needs an actual USD-denominated account held somewhere real, not
  just an internal ledger sub-account with a suffix in its name. There is
  no per-currency regulatory reporting — a real Indian platform offering
  US stocks needs LRS (Liberalised Remittance Scheme) limit tracking and
  reporting per client per financial year, none of which is modelled
  here. The `fx-conversion-clearing-acct` account absorbs the numeric
  difference between a conversion's two currencies' minor-unit
  magnitudes purely so `internal/doubleentry`'s currency-agnostic balance
  check passes — its balance is ledger plumbing, not real money, and
  should not be read as anything currency-meaningful. Non-native-currency
  wallets (USD, etc.) are deliberately NOT classified in
  `fundsegregation`'s CLIENT/FIRM map at all — mixing a foreign
  currency's raw minor units into the SAME custody-pool sum as
  acct-001/acct-002's INR minor units would make that sum numerically
  meaningless, so the segregation invariant as it exists today only ever
  covers INR; a real build needs either a genuinely per-currency custody
  pool and per-currency invariant, or every balance normalized to one
  base currency via a live rate before summing. No auth on any endpoint —
  anyone who can reach `/wallets/*` can deposit/withdraw/convert for any
  account.

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

# the RING-FENCED way to fund a client account (use this, not the raw
# /journal-entries demo above, if you care about segregation staying intact)
curl -X POST localhost:8082/client-funds/deposit -d '{
  "accountIdentifier": "acct-002",
  "amountInMinorUnits": 500000
}'
curl localhost:8082/client-funds/segregation-report
# -> isSegregationIntact: true, custody pool balance == aggregate client balance

# client-to-client transfer — doesn't touch custody, invariant stays intact
curl -X POST localhost:8082/client-funds/transfer -d '{
  "fromAccountIdentifier": "acct-002",
  "toAccountIdentifier": "acct-001",
  "amountInMinorUnits": 100000
}'

# dry-run check whether a proposed journal entry would break segregation,
# without posting it
curl -X POST localhost:8082/client-funds/validate-entry -d '{
  "humanReadableDescription": "would this leak client money?",
  "debitLines": [{"ledgerAccountIdentifier":"acct-001","amountInMinorUnits":1000}],
  "creditLines": [{"ledgerAccountIdentifier":"firm-clearing-acct","amountInMinorUnits":1000}]
}'
# -> wasApplied: false, flags it — and nothing was posted

# AML: PEP name screen
curl -X POST localhost:8082/aml/screen-name -d '{
  "accountIdentifier": "acct-001",
  "fullName": "Corrupt Official"
}'
# -> isMatch: true, with the raised alert

# AML: compliance review queue (all accounts, or ?accountId=... for one)
curl localhost:8082/aml/alerts

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

# SIMULATED deposit rail — initiate does NOT move money (see
# internal/depositrail's package doc for the loud "not a real bank
# integration" disclaimer)
DEPOSIT_ID=$(curl -s -X POST localhost:8082/deposits/initiate -d '{
  "accountIdentifier": "acct-001",
  "method": "UPI",
  "amountInMinorUnits": 200000
}' | grep -o '"depositId":"[^"]*"' | cut -d'"' -f4)

# confirm stands in for the bank's webhook — this is where money
# genuinely moves, through the same ring-fenced segregation guard
curl -X POST localhost:8082/deposits/confirm -d "{\"depositId\":\"$DEPOSIT_ID\"}"
curl "localhost:8082/accounts/balance?accountId=acct-001"
# -> currentBalanceInMinorUnits is up by 200000

curl "localhost:8082/deposits?accountId=acct-001"

# SIMULATED SIP mandate (eNACH/standing instruction) — see
# internal/paymentmandate's package doc for the "not real eNACH"
# disclaimer
MANDATE_ID=$(curl -s -X POST localhost:8082/payment-mandates/register -d '{
  "accountIdentifier": "acct-001",
  "amountInMinorUnits": 50000,
  "frequency": "MONTHLY",
  "nextDebitDate": "2026-01-01T00:00:00Z"
}' | grep -o '"mandateId":"[^"]*"' | cut -d'"' -f4)

# sweep every mandate whose nextDebitDate has arrived — a due mandate's
# debit is a REAL journal entry, and nextDebitDate advances by one
# frequency period
curl -X POST localhost:8082/payment-mandates/sweep-due
curl "localhost:8082/accounts/balance?accountId=acct-001"
# -> currentBalanceInMinorUnits drops by 50000 once the mandate is due

# pause/resume/cancel
curl -X POST localhost:8082/payment-mandates/pause -d "{\"mandateId\":\"$MANDATE_ID\"}"
curl -X POST localhost:8082/payment-mandates/resume -d "{\"mandateId\":\"$MANDATE_ID\"}"
curl -X POST localhost:8082/payment-mandates/cancel -d "{\"mandateId\":\"$MANDATE_ID\"}"

curl "localhost:8082/payment-mandates?accountId=acct-001"

# Multi-currency wallet (FEATURES.md §2) — see
# internal/multicurrencywallet's package doc. The FX rate table is
# STATIC/ILLUSTRATIVE, NOT a live feed.
curl -X POST localhost:8082/wallets/deposit -d '{
  "accountId": "acct-001",
  "currencyCode": "USD",
  "amountInMinorUnits": 10000
}'
curl "localhost:8082/wallets?accountId=acct-001"
# -> USD wallet shows 10000; acct-001's raw/INR balance is UNCHANGED

# convert 100.00 USD -> INR at the static 83.0 rate: EXACTLY 830000
# minor units (8300.00 INR), no rounding
curl -X POST localhost:8082/wallets/convert -d '{
  "accountId": "acct-001",
  "fromCurrencyCode": "USD",
  "toCurrencyCode": "INR",
  "amountInFromCurrencyMinorUnits": 10000
}'
curl "localhost:8082/wallets?accountId=acct-001"
curl "localhost:8082/client-funds/segregation-report"
# -> isSegregationIntact stays true; custody pool moved by exactly 830000

# a withdrawal exceeding ONE currency's balance is rejected even if
# another currency wallet for the same account has plenty
curl -X POST localhost:8082/wallets/withdraw -d '{
  "accountId": "acct-001",
  "currencyCode": "USD",
  "amountInMinorUnits": 1000
}'
# -> rejected: USD wallet balance is 0 after the conversion above,
#    even though the INR wallet has a large balance

go test ./... -race
```
