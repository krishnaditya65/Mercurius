# reporting

See `FEATURES.md` §1 in the repo root ("Regulatory reporting: contract
notes, ledger statements, tax P&L (STCG/LTCG), Annual Information
Statement reconciliation") and §21 ("One-click capital gains statement
export") — this service builds both together, since they are the same
underlying tax/capital-gains reporting domain.

## Status: real, tested computation over real data pulled live from oms-gateway and ledger — no fabricated transaction data anywhere

`reporting` never imports or edits any other service's Go code. Every
number in every report is computed at request time from real HTTP calls
to oms-gateway's (`:8081`) and ledger's (`:8082`) genuine, already-shipped
APIs — see `internal/omsgatewayclient` and `internal/ledgerclient`'s
package docs for the exact endpoints used. The one deliberately
NOT-real input in this service is the government Annual Information
Statement itself (§4 below) — there is no such feed to integrate with,
and that gap is loudly labeled everywhere it matters.

## Port

**`:8090`** (override with `REPORTING_LISTEN_ADDRESS`) — the next free
port after this repo's existing allocations: `8081` oms-gateway, `8082`
ledger, `8083` kyc-onboarding, `8084` backoffice, `8085` quant-engine,
`8086` auth, `8087` mutual-funds, `8088` oms-gateway's DMA/FIX TCP
gateway, `8089` api-gateway.

Upstream base URLs are configurable via `OMS_GATEWAY_BASE_URL` (default
`http://127.0.0.1:8081`) and `LEDGER_BASE_URL` (default
`http://127.0.0.1:8082`).

## What's real

- **Contract notes** (`internal/contractnotegenerator`, §1): for one
  account + calendar date, pulls that account's real fills (see "Fill
  history" below) and, for each one, calls oms-gateway's real, live
  `POST /orders/estimate-charges` (`internal/chargescalculator`) to get
  a real brokerage/STT/GST/exchange-charge/stamp-duty/DP-charge
  breakdown — the exact same illustrative-but-real rate model
  oms-gateway's own order-ticket UI would use, not a re-implementation.
  Falls back to a clearly-flagged, much simpler illustrative flat 0.05%
  charge only if oms-gateway's calculator is unreachable (see
  `ChargesSource` on every line: `OMS_GATEWAY_LIVE_CALCULATOR` or
  `REPORTING_ILLUSTRATIVE_FALLBACK`).

- **Ledger statements** (`internal/ledgerstatement`, §1): a real
  running-balance statement for one account + date range, built from:
  ledger's real `GET /deposits` (CONFIRMED only) and `GET /withdrawals`
  (COMPLETED only), oms-gateway's real `GET
  /corporate-actions/processed-actions` CASH_DIVIDEND credits, and a
  trade-settlement row **derived** from each real fill's real/illustrative
  charges breakdown (ledger has no endpoint for its own trade-settlement
  journal entries — see "Honest limitations" below). Every row's replayed
  balance is real arithmetic; the statement also reports whether the
  final replayed balance matches ledger's real
  `GET /accounts/balance` exactly, as an honest diagnostic
  (`ReconciliationNote`) — it generally will NOT match exactly, and the
  note explains why.

- **Tax P&L, STCG/LTCG** (`internal/capitalgains`, §1): genuine FIFO
  lot-matching over an account's full real fill history. A sold lot is
  classified **long-term** if sold on or after exactly 12 months
  (`time.AddDate(1,0,0)`, calendar-accurate) from its acquisition date —
  the real Indian STCG/LTCG threshold for listed equity (Income Tax Act
  §2(42A)) — and **short-term** otherwise. Aggregates into STCG/LTCG
  totals for an Indian financial year (April 1 – March 31). Tax
  *rates* are deliberately out of scope (see chargescalculator's own
  "rates drift, don't hardcode" posture) — this computes the realized
  gain/loss and its classification only.

- **AIS reconciliation** (`internal/aisreconciliation`, §1): real,
  generic field-by-field reconciliation logic (`Reconcile`) between any
  two AIS-shaped records. `BuildPlatformAisRecord` builds the real side
  from real FIFO STCG/LTCG output plus real dividend credits.
  `BuildIllustrativeMockAisRecord` builds a **simulated, clearly-labeled
  mock** AIS-shaped record for the other side — see "Honest limitations".
  `Reconcile` itself has no idea which input is real or mock; it's fully
  tested independent of that.

- **One-click capital gains CSV export** (`internal/capitalgainsexport`,
  §21): a genuine `encoding/csv` writer (not JSON dressed up as CSV) over
  the real FIFO output — one row per matched lot, a documented fixed
  header row, plus `TOTAL`/`TOTAL_STCG`/`TOTAL_LTCG` summary rows. One
  request (`GET /capital-gains/export`) returns the whole thing with a
  `Content-Disposition: attachment` filename — the "one-click" framing.

- **Fill history** (`internal/filltrail`): the shared foundation every
  report above is built on. See "Honest limitations" for the real
  audit-trail gap this package works around, and how.

## Endpoint contracts

All endpoints are `GET` except where noted; all respond `application/json`
except the CSV export. CORS is wide open (same development-mode posture
as every other service in this repo).

### `GET /health`
`{"status":"ok","service":"reporting"}`

### `GET /contract-notes/generate?accountId=...&date=YYYY-MM-DD[&isIntradayNotDelivery=true]`
```json
{
  "contractNote": {
    "accountIdentifier": "acct-001",
    "tradeDate": "2026-08-14T00:00:00Z",
    "tradeLines": [{
      "instrumentSymbol": "DEMO-EQ", "side": "BUY", "quantity": 30,
      "priceInMinorUnits": 9800, "executedAtTime": "...",
      "counterpartyAccountIdentifier": "acct-002",
      "charges": { "...": "full ChargesBreakdown, see chargescalculator" },
      "chargesSource": "OMS_GATEWAY_LIVE_CALCULATOR"
    }],
    "totalTurnoverInMinorUnits": 294000,
    "totalChargesInMinorUnits": 349,
    "totalNetAmountInMinorUnits": 294349,
    "generatedAtTime": "..."
  },
  "unparseableAuditTrailEntryWarnings": []
}
```

### `GET /ledger-statements/generate?accountId=...&startDate=YYYY-MM-DD&endDate=YYYY-MM-DD`
```json
{
  "statement": {
    "accountIdentifier": "acct-001", "startDate": "...", "endDate": "...",
    "openingBalanceInMinorUnits": 0, "closingBalanceInMinorUnits": 99705651,
    "rows": [{
      "movementType": "DEPOSIT", "description": "Deposit via UPI (...)",
      "occurredAtTime": "...", "amountInMinorUnits": 100000000,
      "sourceService": "ledger", "runningBalanceInMinorUnits": 100000000
    }],
    "ledgerReportedCurrentBalanceInMinorUnits": 98081000,
    "reconciliationNote": "..."
  },
  "unparseableAuditTrailEntryWarnings": []
}
```
`movementType` is one of `DEPOSIT`, `WITHDRAWAL`, `DIVIDEND_CREDIT`,
`TRADE_SETTLEMENT`.

### `GET /capital-gains/compute?accountId=...&financialYear=YYYY-YY`
```json
{
  "summary": {
    "accountIdentifier": "acct-001", "financialYear": "2026-27",
    "financialYearStartDate": "2026-04-01T00:00:00Z",
    "financialYearEndDate": "2027-03-31T23:59:59.999999999Z",
    "realizedGains": [{
      "instrumentSymbol": "DEMO-EQ", "quantity": 15,
      "acquiredAtTime": "...", "soldAtTime": "...",
      "buyPriceInMinorUnits": 9800, "sellPriceInMinorUnits": 11000,
      "holdingPeriodDays": 0, "gainType": "STCG",
      "realizedGainInMinorUnits": 18000
    }],
    "shortTermTotalInMinorUnits": 18000, "longTermTotalInMinorUnits": 0
  },
  "unparseableAuditTrailEntryWarnings": [],
  "fifoMatchingWarning": "present only if a sell exceeded open lot quantity"
}
```
`financialYear` is `"YYYY-YY"`, e.g. `"2024-25"` for April 2024–March
2025.

### `GET /capital-gains/export?accountId=...&financialYear=YYYY-YY`
Returns `text/csv`, `Content-Disposition: attachment;
filename="capital-gains-<accountId>-FY<financialYear>-<generatedDate>.csv"`.
Header row (exact, stable — see `capitalgainsexport.ColumnHeaders`):
```
instrumentSymbol,quantity,acquiredDate,soldDate,holdingPeriodDays,gainType,buyPriceInMinorUnits,sellPriceInMinorUnits,realizedGainInMinorUnits
```
followed by one row per FIFO-matched lot and three summary rows
(`TOTAL`, `TOTAL_STCG`, `TOTAL_LTCG`).

### `GET /ais-reconciliation/run?accountId=...&financialYear=YYYY-YY`
```json
{
  "platformRecord": { "source": "MERCURIUS_PLATFORM_COMPUTED", "entries": [...] },
  "aisRecord": { "source": "MOCK_SIMULATED_AIS_EXPORT (illustrative only — not a real government feed)", "entries": [...] },
  "reconciliationReport": {
    "isFullyReconciled": false,
    "discrepancies": [{
      "type": "AMOUNT_MISMATCH", "category": "STCG", "instrumentSymbol": "DEMO-EQ",
      "platformAmountInMinorUnits": 18000, "aisAmountInMinorUnits": 18100,
      "deltaInMinorUnits": -100
    }]
  }
}
```
`type` is one of `AMOUNT_MISMATCH`, `MISSING_IN_AIS`,
`MISSING_IN_PLATFORM`.

## Honest limitations

- **No real government AIS feed.** There is no real Annual Information
  Statement data source this build could reach. `aisreconciliation`'s
  mock AIS record is explicitly labeled
  `"MOCK_SIMULATED_AIS_EXPORT (illustrative only — not a real government
  feed)"` in its own `source` field — never presented as real. The
  reconciliation *logic* comparing it against the platform's real
  computed summary is fully real and fully tested (`Reconcile` is
  generic — it doesn't know or care which side is "real").

- **oms-gateway's fill history is only reachable through the audit
  trail, and its per-account filter has a real, load-bearing gap.**
  There is no dedicated "fills"/"trades" endpoint on oms-gateway.
  `GET /audit-trail?accountId=X` only returns an `ORDER_FILLED` entry
  under the account whose order request happened to be the one that
  crossed (the taker) — the resting/maker counterparty's identical fill
  is recorded under a *different* entry's `ClientAccountIdentifier` and
  is invisible to that account's own filtered query. **Verified live**:
  after a real `acct-001` BUY crossed against a real `acct-002` SELL,
  `GET /audit-trail?accountId=acct-001` (the buyer) returned zero
  `ORDER_FILLED` entries for that trade, while
  `GET /audit-trail?accountId=acct-002` (the seller, the taker here)
  showed it. `omsgatewayclient.FetchAllAuditTrailEntries` works around
  this by fetching the full, unfiltered trail once and filtering
  client-side on the entry's own structured
  `buyingClientAccountIdentifier`/`sellingClientAccountIdentifier`
  fields — the only currently-correct way to get one account's complete
  fill history from oms-gateway. `filltrail.ParseFillsFromAllAuditTrailEntries`
  prefers those structured fields (added to `audittrail.Entry` for
  `internal/tradesurveillance`) and falls back to regex-parsing the
  older free-text `DetailMessage` ("filled %d @ %d (buyer=%s
  seller=%s)") only if the structured fields are absent.

- **ledger has no HTTP endpoint for historical journal entries.**
  `internal/doubleentry.InMemoryDoubleEntryLedgerBook` keeps every
  posted `JournalEntry` in an unexported slice with no HTTP-reachable
  getter — only a current-balance snapshot
  (`GET /accounts/balance`) is exposed, plus the two higher-level
  sub-ledgers that DO have real history endpoints
  (`GET /deposits`, `GET /withdrawals`). `ledgerstatement` therefore
  **derives** trade-settlement cash-impact rows from oms-gateway's real
  fills + real/illustrative charges rather than reading them from
  ledger's own journal, and the statement's `reconciliationNote` says so
  explicitly whenever the replayed balance doesn't match ledger's real
  reported balance (margin funding interest, LAS, securities lending
  fees, wallet conversions, and any other ledger activity this service
  has no read API for are all invisible to it).

- **Illustrative charges fallback.** If oms-gateway's live
  `POST /orders/estimate-charges` is unreachable, contract notes fall
  back to a simple flat 0.05%-of-turnover charge, clearly flagged via
  `chargesSource: "REPORTING_ILLUSTRATIVE_FALLBACK"` on the affected
  line — never silently presented as oms-gateway's real rate model.

- **No PDF generation.** Contract notes and ledger statements are JSON
  only; the capital gains export is real CSV. No PDF rendering exists
  anywhere in this service.

- **Withdrawal timestamps are a proxy.** Ledger's
  `withdrawalRequestWireResponse` has no "completed at" timestamp field
  — only `eligibleForPayoutAt`. `ledgerstatement` uses that as the
  withdrawal movement's `occurredAtTime`, which is an honest
  approximation, not necessarily the exact real completion instant.

- **`isIntradayNotDelivery` is caller-supplied, not derived.** Neither
  oms-gateway's audit trail nor its fill data records whether an order
  was intraday or delivery, so `contractnotegenerator` applies whichever
  value the caller passes (default: delivery) uniformly to every line of
  one contract note. A genuinely mixed intraday/delivery trading day
  needs two calls (one per settlement type) merged by the caller.

## Verification performed

- `gofmt -l .` — clean.
- `go vet ./...` — clean.
- `go build ./...` — clean.
- `go test ./... -race` — 38 tests across 8 packages, all passing,
  including a hand-worked FIFO STCG/LTCG example
  (`capitalgains.TestFifoHandWorkedExampleTwoLotsPartialSellStcgLtcgSplit`):
  two buy lots (100 @ ₹100.00 on 2024-01-10; 100 @ ₹120.00 on
  2024-06-15) and one partial sell (150 @ ₹150.00 on 2025-02-01)
  produce exactly two FIFO matches — 100 units from lot 1 (LTCG, gain
  ₹5000.00) and 50 units from lot 2 (STCG, gain ₹1500.00), leaving 50
  units of lot 2 still open — verified against exact expected minor-unit
  amounts.
- **Real, live end-to-end run** against real `matching-engine`,
  `ledger`, and `oms-gateway` processes (no Docker): deposited real
  money into two ledger accounts, submitted real crossing BUY/SELL
  orders through oms-gateway (including a real partial-lot sell to
  realize a gain), then called every one of `reporting`'s five
  endpoints against the live services and confirmed each reflected the
  real submitted activity — a real contract note with the real live
  charges breakdown, a real ledger statement with a real deposit +
  derived trade-settlement row and an honest reconciliation note, a
  real FIFO STCG computation (15 units, buy ₹98.00, sell ₹110.00, gain
  ₹180.00, correctly STCG), a real parseable CSV export with matching
  totals, and a real AIS reconciliation correctly flagging the
  deliberately-perturbed mock entry. All four processes were then
  stopped and `lsof -i :8081/:8082/:8090/:9101 -sTCP:LISTEN` confirmed
  nothing was left listening.
