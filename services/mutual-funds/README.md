# mutual-funds

See `FEATURES.md` §4 in the repo root for the full intended scope: this
service builds all six items in that section — the three `[P1]` items
(direct AMC routing, lumpsum + SIP setup/pause/cancel/calendar, step-up
SIPs) plus the `[P2]`/`[P3]` items (index/thematic rebalancing baskets
with one-click rebalance, Robo-Advisory, goal-based investing).

## Status: AMC routing, SIP scheduling, step-up math, basket rebalancing, Robo-Advisory, and goal tracking are all real (simulated) engines — none of it talks to a real AMC

What's real:
- **A static fund catalog** (`internal/fundcatalog`): five illustrative,
  entirely fictitious schemes spanning EQUITY/DEBT/HYBRID, each with a
  `currentNavInMinorUnits`. `UpdateNav` exists purely so tests/demos can
  move a NAV to a known value deterministically.
- **Direct AMC routing** (`internal/amcrouting`, FEATURES.md §4): a real
  `PENDING → CONFIRMED` order state machine for both purchases and
  redemptions. Placing a purchase order does **not** allocate units —
  units are only allocated once `ConfirmDueOrders` processes it, at
  whatever NAV is current in the catalog **at confirmation time**, not at
  placement time — deliberately mirroring how a real AMC/RTA strikes
  end-of-day NAV rather than instant execution. A redemption reserves the
  requested units against the account's holding immediately (so a second
  redemption can't oversell the same units before the first settles —
  the same "hold" pattern `ledger`'s `withdrawalworkflow` uses for cash),
  and only actually removes them + computes the credited amount on
  confirmation.
- **Lumpsum purchases** are just a single call to
  `AmcOrderRouter.PlacePurchaseOrder` — no scheduling involved.
- **SIP registration, pause/resume/cancel, and calendar sweep**
  (`internal/sipscheduler`, FEATURES.md §4): `RegisterSip` validates
  scheme existence, positive amount, `MONTHLY` frequency (the only one
  implemented), and a step-up percent in `[0, 100]`. `PauseSip` freezes
  the schedule exactly where it is — a paused SIP's `nextDueDate` does
  not advance, and resuming does **not** backfill/catch-up the
  installments that would have fallen while paused; the next sweep just
  picks up from the frozen due date. `CancelSip` is terminal. `SweepDueSips`
  executes every `ACTIVE` SIP whose `nextDueDate` has arrived by routing
  a purchase order through `internal/amcrouting`, then advances
  `nextDueDate` by one month — sweeping twice before the next installment
  is due does nothing the second time.
- **Step-Up SIPs** (also `internal/sipscheduler`, FEATURES.md §4): an
  optional `annualStepUpPercent` on a SIP. The installment amount for any
  given due date is computed as `baseAmount * (1 + pct/100) ^
  fullYearsElapsed`, where `fullYearsElapsed` is the number of complete
  12-month anniversaries of `startDate` that the due date has reached —
  computed with calendar-aware `time.Time.AddDate`, not a day-count
  divide. Hand-worked and tested: ₹5000/month with a 10% step-up stays at
  ₹5000 for installments #1–#12 and becomes exactly ₹5500 starting
  installment #13 (the first anniversary of the start date); a second
  anniversary test extends this to installment #25 compounding to
  exactly ₹6050 (500000 × 1.10² = 605000), not a flat re-application of
  +10% on the stepped-up amount.
- **Real HTTP endpoints** over all of the above (`cmd/server/main.go`,
  port `:8087`), all of which accept an optional `?asOf=<RFC3339>` query
  parameter to override "now" — the same testing/demo hook pattern
  `ledger`'s `ProcessDueWithdrawals` uses an explicit `now time.Time`
  parameter for, so time-dependent logic (order confirmation delay, SIP
  due dates, step-up anniversaries) can be exercised deterministically
  without sleeping or waiting real days/months.
- **Index/thematic rebalancing baskets with one-click rebalance**
  (`internal/basketrebalancing`, FEATURES.md §4): a `Basket` is a named
  target allocation across two or more `fundcatalog` schemes, expressed
  as percentage weights validated to sum to EXACTLY 100 (within a tiny
  float epsilon). `SubscribeToBasket` splits a lumpsum proportionally
  across the constituents and routes one purchase order per scheme
  through `amcrouting` — the per-leg rounding is done so the split sums
  EXACTLY to the lumpsum (every constituent but the last is rounded
  normally, the last absorbs the remainder). `RebalanceAccountBasket` is
  the real "one-click rebalance": it revalues the account's CURRENT
  holding in every constituent scheme at the CURRENT catalog NAV, compares
  that to what the TARGET weights say those holdings should be worth
  against the SAME total, and emits the exact BUY (shortfall) or SELL
  (excess) order needed to close the gap — a drift under a 1-minor-unit
  threshold is reported as a no-op `HOLD`. Hand-worked and tested: a
  50/30/20 basket subscribed with a 10000 lumpsum at NAV 100 across all
  three schemes allocates 50/30/20 units; when one scheme's NAV then
  DOUBLES, the rebalance computes and places an exact SELL of 2500 (12.5
  units) on the scheme that grew, funding exact BUYs of 1500 and 1000 on
  the other two — the sell exactly funds both buys, as it must.
- **Robo-Advisory: risk-profile → illustrative model allocation, wired to
  quant-engine's real Sharpe/Sortino/max-drawdown** (`internal/roboadvisory`,
  FEATURES.md §4/§6): `RecommendAllocation` maps a risk category —
  `CONSERVATIVE` / `MODERATELY_CONSERVATIVE` / `MODERATE` /
  `MODERATELY_AGGRESSIVE` / `AGGRESSIVE`, matching kyc-onboarding's
  `internal/riskprofiling` category names exactly — to a hand-picked
  illustrative EQUITY/DEBT/HYBRID split (CONSERVATIVE 20/70/10 up to
  AGGRESSIVE 80/10/10, a flat 10% HYBRID sleeve throughout), generates a
  deterministic illustrative monthly return series whose volatility scales
  with the allocation's equity weight, and calls quant-engine's REAL
  `POST /risk/statistics` endpoint (port 8085) with that series to surface
  a genuine annualized Sharpe ratio, Sortino ratio, and max drawdown
  alongside the recommendation. If quant-engine is unreachable or rejects
  the request, `RecommendAllocation` still succeeds — it just reports the
  allocation with an explanatory `RiskStatisticsError` and no statistics,
  rather than failing the whole recommendation.
- **Goal-based investing with progress tracking** (`internal/goalinvesting`,
  FEATURES.md §4): a `Goal` names a target (`targetAmountInMinorUnits` by
  `targetDate`, one of `RETIREMENT`/`EDUCATION`/`HOME_PURCHASE`/
  `WEALTH_CREATION`/`OTHER`) and is linked to whichever scheme(s) are
  actually funding it — the scheme a SIP invests into, or every
  constituent scheme of a basket subscription, passed in as
  `linkedSchemeIds` at creation time. `CalculateProgress` computes REAL
  current invested value (units held × current catalog NAV, summed across
  every linked scheme) and a REAL "are we on track" projection: it
  compounds the current value forward to `targetDate` at an ILLUSTRATIVE
  assumed monthly growth rate (0.007/month, ≈8.73%/year compounded — a
  round, undertuned constant, explicitly NOT a forecast, NOT derived from
  any historical return data), adds the future value of an ordinary
  annuity of the goal's assumed monthly contribution over the same
  remaining months, and compares the total to the target. Hand-worked and
  tested: ₹1000 current value + ₹100/month contribution, 12 months
  remaining, at the illustrative rate projects to exactly ₹2334.61 (on a
  ₹2000 target — surplus ₹334.61); a second example with ₹500 current
  value, zero ongoing contribution, and 24 months remaining projects to
  exactly ₹591.12 against a ₹3000 target (NOT on track), with a real
  solved-for required monthly contribution of ₹92.53 to close that gap
  using the same formula run in reverse.
- 88 tests across the six packages (6 `fundcatalog`, 11 `amcrouting`,
  19 `sipscheduler`, 18 `basketrebalancing`, 15 `roboadvisory`,
  19 `goalinvesting`), covering happy paths, pause/cancel/resume
  semantics, the hand-worked step-up boundary math at both the first and
  second anniversary, sweep idempotency (for both order confirmation and
  SIP execution), the hand-worked basket drift/rebalance example, every
  risk category's exact allocation percentages plus a hand-worked
  quant-engine Sharpe/Sortino stub response, the hand-worked goal
  on-track/not-on-track projections, and rejection of invalid inputs.
  `go test ./... -race` is green across the whole service.
- **Verified live end-to-end** (see `docs/BUILD_LOG.md` for the full
  transcript this session produced): a lumpsum purchase of ₹5000 into a
  scheme with a known NAV of ₹1000.00 was placed `PENDING`, confirmed
  nothing before the 24h simulated settlement delay, then confirmed
  correctly at exactly 5.0 units once swept past that delay via
  `?asOf=`. A SIP was registered, its first sweep executed and advanced
  `nextDueDate` by exactly one month, a second sweep at the same `asOf`
  did nothing (idempotency), pausing the SIP and sweeping again did
  nothing (and did not advance the schedule), resuming and sweeping
  again executed the frozen installment exactly once (no catch-up
  backfill), and sweeping through 13 monthly installments via `?asOf=`
  produced the exact ₹5000 ×12 → ₹5500 step-up boundary described above.
  This session additionally verified live, with both this service AND a
  real `quant-engine` running: creating a 50/30/20 basket and subscribing
  it with a 10000 lumpsum produced the exact proportional purchase splits
  (5000/3000/2000), which confirmed into 50/30/20 units at NAV 100;
  moving one scheme's NAV to 200 via the new `/schemes/update-nav` demo
  hook (see below) and then calling `/baskets/rebalance` produced the
  EXACT hand-computed SELL of 2500 (12.5 units) and BUYs of 1500 and 1000
  on the other two schemes; a Robo-Advisory request for every one of the
  5 risk categories returned the correct illustrative allocation
  percentages, correctly degraded to a `riskStatisticsError` when
  quant-engine was briefly down, and — once quant-engine was reachable —
  returned REAL Sharpe/Sortino/max-drawdown numbers computed by
  quant-engine's own `riskStatistics.py` over this package's synthetic
  return series; and a goal's progress calculation exactly matched the
  hand-worked ₹23,346 projected-value example from this package's test
  suite.

What's a placeholder:
- **`internal/amcrouting` never talks to any real AMC or RTA.** There is
  no BSE StAR MF / NSE NMF II / RTA (CAMS, KFintech) integration
  anywhere in this repo — the "AMC" on the other end of every order is
  this package's own in-memory map. `POST /orders/confirm-due` is
  manually/externally triggered, not run on a real scheduled job — same
  gap as `ledger`'s `POST /withdrawals/process-due`. Same for
  `POST /sips/sweep-due`: a real build needs an actual cron/scheduler
  calling it, not an operator or script.
- The fund catalog is five hardcoded, entirely fictitious schemes with
  NAVs that never move on their own (only `UpdateNav`, exposed over HTTP
  as the testing/demo-only `POST /schemes/update-nav` hook, changes
  them). A real catalog is either ingested from AMFI's daily NAV file or
  fetched live from each AMC/RTA's API. `/schemes/update-nav` carries the
  exact same "never let an external caller dictate this" caveat as
  `?asOf=` below — it exists purely so this README's and this session's
  rebalance demo can move a NAV deterministically without waiting for a
  real market.
- Only `MONTHLY` SIP frequency is implemented — no weekly/quarterly/
  annual.
- **`internal/roboadvisory`'s allocation table is a hand-picked,
  ILLUSTRATIVE model portfolio, NOT a real mean-variance/Efficient
  Frontier OPTIMIZATION.** A genuine Efficient Frontier allocation solves
  for the weights that maximize expected return for a given risk level
  (or minimize risk for a given return) using the historical mean-return
  vector and covariance matrix of the candidate assets — this repo has
  neither: `fundcatalog`'s five schemes are hardcoded, entirely
  fictitious NAVs with no historical return series behind them at all.
  Building a real optimizer without real historical return data would
  just be dressing up a made-up table in fancier math, so this package is
  honest about it being a made-up table instead. The Sharpe/Sortino/
  max-drawdown numbers it surfaces from quant-engine ARE real math — it's
  the RETURN SERIES fed into that real math which is synthetic/
  illustrative, not historical or forecast data.
- **`internal/goalinvesting`'s "on track" projection uses a single
  ILLUSTRATIVE assumed monthly growth rate (0.007, ≈8.73%/year
  compounded), NOT a forecast and NOT derived from any real historical or
  expected return data**, and it doesn't vary by the goal's linked
  schemes' actual category/risk mix (a RETIREMENT goal funded entirely by
  a DEBT scheme gets the exact same assumed growth rate as one funded by
  an EQUITY scheme). A real projection would need real per-asset-class
  return assumptions and almost certainly a range/confidence interval
  rather than one deterministic point estimate.
- No KYC/eligibility gating — unlike `oms-gateway`'s genuine
  `kyc-onboarding` gate on order submission, nothing here checks whether
  an account is KYC-verified before placing a purchase or redemption
  order, subscribing to a basket, or creating a goal.
- No cash-side integration — a purchase order here doesn't debit any
  ledger account, and a redemption's credited amount isn't actually paid
  into one either. `ledger`'s double-entry book is not wired into this
  service at all; a real build would settle both legs (units + cash)
  atomically against it, the way `oms-gateway` does for trade
  settlement.
- In-memory only — no persistence. Restarting the process loses every
  scheme's NAV override, every order, every holding, every SIP, every
  basket (and subscription), every Robo-Advisory call's inputs (nothing
  is stored — each recommendation is computed fresh), and every goal.
- No auth on any endpoint — anyone who can reach `/sips/*`, `/orders/*`,
  `/baskets/*`, `/robo-advisory/*`, or `/goals/*` can register, pause,
  cancel, purchase, redeem, subscribe, rebalance, or create/track a goal
  for any account.
- Unit allocation is rounded to 4 decimal places (matching the real-world
  convention of 3-4 decimal precision unit statements), but there's no
  concept of a minimum lumpsum/SIP amount, exit load, lock-in (e.g.
  ELSS), or STT/stamp duty — a real mutual fund purchase has all of
  these. Basket subscriptions and rebalance orders route through the
  exact same `amcrouting` primitives, so they inherit this gap too.
- `internal/basketrebalancing`'s `RebalanceAccountBasket` values a
  holding's CURRENT position using `TotalUnits` (not `AvailableUnits`),
  so a scheme with units already reserved against a pending redemption is
  still counted at full value when computing drift — but the SELL order
  it then tries to place is still checked against `AvailableUnits` by
  `amcrouting` and will fail (recorded as that action's `ErrorMessage`,
  not a hard error for the whole rebalance) if there aren't enough
  unreserved units to cover it. See the package's
  `TestRebalanceAccountBasketRecordsErrorWhenSellExceedsAvailableUnits`.
- `internal/goalinvesting`'s `RequiredMonthlyContributionInMinorUnits` and
  `internal/basketrebalancing`'s BUY/SELL split are both real solved
  formulas, but neither package enforces any relationship between a
  goal's `linkedSchemeIds`/a basket's constituents and an ACTUAL SIP or
  basket subscription elsewhere in the service — a caller must pass the
  correct scheme id(s) in by hand; nothing cross-references
  `internal/sipscheduler` or `internal/basketrebalancing`'s own
  subscription records to derive them automatically.
- `?asOf=` overriding "now" on every endpoint is a testing/demo
  convenience with no access control of its own — a real build would
  never let an external caller dictate the server's notion of time.

## Run it

```bash
go run ./cmd/server
# MUTUAL_FUND_ORDER_CONFIRMATION_DELAY_HOURS (default 24) is overridable —
# how long a PENDING order takes to become eligible for confirmation,
# simulating a real AMC/RTA's T+N settlement.

curl localhost:8087/schemes

# lumpsum purchase — PENDING, no units allocated yet
curl -X POST localhost:8087/orders/lumpsum-purchase -d '{
  "accountIdentifier": "acct-001",
  "schemeId": "MF-DT-LIQUID003",
  "amountInMinorUnits": 500000
}'

# confirming right now does nothing — the order isn't eligible yet
curl -X POST localhost:8087/orders/confirm-due

# ?asOf= lets you fast-forward past the confirmation delay for
# testing/demo purposes instead of waiting real hours
curl -X POST "localhost:8087/orders/confirm-due?asOf=2026-01-02T00:00:00Z"
curl "localhost:8087/holdings?accountId=acct-001"
# -> 5.0 units, since 500000 / 100000 (the seed NAV) = 5.0

# redemption — reserves units immediately, credits on confirmation
curl -X POST localhost:8087/orders/redemption -d '{
  "accountIdentifier": "acct-001",
  "schemeId": "MF-DT-LIQUID003",
  "unitsToRedeem": 2.0
}'
curl "localhost:8087/orders?accountId=acct-001"

# register a SIP with a 10% annual step-up
curl -X POST localhost:8087/sips/register -d '{
  "accountIdentifier": "acct-002",
  "schemeId": "MF-EQ-BLUECHIP001",
  "installmentAmountInMinorUnits": 500000,
  "frequency": "MONTHLY",
  "startDate": "2024-01-15T00:00:00Z",
  "annualStepUpPercent": 10
}'

# sweep at the start date — executes installment #1, advances nextDueDate
curl -X POST "localhost:8087/sips/sweep-due?asOf=2024-01-15T00:00:00Z"
# sweeping again at the same asOf does nothing
curl -X POST "localhost:8087/sips/sweep-due?asOf=2024-01-15T00:00:00Z"

curl "localhost:8087/sips?accountId=acct-002"

curl -X POST localhost:8087/sips/pause -d '{"sipId": "<sipId>"}'
curl -X POST localhost:8087/sips/resume -d '{"sipId": "<sipId>"}'
curl -X POST localhost:8087/sips/cancel -d '{"sipId": "<sipId>"}'

# --- basket rebalancing ---

# create a 50/30/20 basket
curl -X POST localhost:8087/baskets/create -d '{
  "name": "Balanced Growth Basket",
  "targetWeightPercentBySchemeId": {
    "MF-EQ-BLUECHIP001": 50,
    "MF-EQ-MIDCAP002": 30,
    "MF-DT-LIQUID003": 20
  }
}'
# -> {"basketId": "...", ...}

# testing/demo-only: move NAVs to round numbers for a clean example
curl -X POST localhost:8087/schemes/update-nav -d '{"schemeId":"MF-EQ-BLUECHIP001","newNavInMinorUnits":100}'
curl -X POST localhost:8087/schemes/update-nav -d '{"schemeId":"MF-EQ-MIDCAP002","newNavInMinorUnits":100}'
curl -X POST localhost:8087/schemes/update-nav -d '{"schemeId":"MF-DT-LIQUID003","newNavInMinorUnits":100}'

# subscribe with a 10000 lumpsum -> splits 5000/3000/2000, then confirm
curl -X POST localhost:8087/baskets/subscribe -d '{
  "accountIdentifier": "acct-basket-1",
  "basketId": "<basketId>",
  "lumpsumAmountInMinorUnits": 10000
}'
curl -X POST "localhost:8087/orders/confirm-due?asOf=2026-01-03T00:00:00Z"
curl "localhost:8087/holdings?accountId=acct-basket-1"
# -> 50/30/20 units, at NAV 100 each

# simulate a market move: one scheme's NAV doubles
curl -X POST localhost:8087/schemes/update-nav -d '{"schemeId":"MF-EQ-BLUECHIP001","newNavInMinorUnits":200}'

# one-click rebalance -> exact SELL 2500 (12.5 units) on the scheme that
# grew, funding exact BUYs of 1500 and 1000 on the other two
curl -X POST localhost:8087/baskets/rebalance -d '{
  "accountIdentifier": "acct-basket-1",
  "basketId": "<basketId>"
}'

# --- Robo-Advisory (calls quant-engine's real Sharpe/Sortino endpoint) ---
# start quant-engine too: services/quant-engine/.venv/bin/quant-engine-server
# QUANT_ENGINE_BASE_URL overrides the default http://127.0.0.1:8085

curl -X POST localhost:8087/robo-advisory/recommend -d '{"riskCategory": "AGGRESSIVE"}'
# -> {"equityPercent":80,"debtPercent":10,"hybridPercent":10,
#     "riskStatistics": {"annualizedSharpeRatio": ..., "annualizedSortinoRatio": ..., ...}}
# (if quant-engine isn't running, the same call still returns the
# allocation, with "riskStatisticsError" explaining why no stats attached)

# --- goal-based investing ---

curl -X POST localhost:8087/goals/create -d '{
  "accountIdentifier": "acct-basket-1",
  "name": "Retirement Fund",
  "goalType": "RETIREMENT",
  "targetAmountInMinorUnits": 20000,
  "targetDate": "2027-01-03T00:00:00Z",
  "linkedSchemeIds": ["MF-EQ-BLUECHIP001"],
  "assumedMonthlyContributionInMinorUnits": 1000
}'
# -> {"goalId": "...", ...}

curl "localhost:8087/goals/progress?goalId=<goalId>&asOf=2026-01-03T00:00:00Z"
# -> currentValueInMinorUnits, progressPercent, projectedValueAtTargetDateInMinorUnits,
#    isOnTrack, and (if not on track) requiredMonthlyContributionInMinorUnits

curl "localhost:8087/goals?accountId=acct-basket-1"

go test ./... -race
```
