# mutual-funds

See `FEATURES.md` §4 in the repo root for the full intended scope: this
service builds all six items in that section — the three `[P1]` items
(direct AMC routing, lumpsum + SIP setup/pause/cancel/calendar, step-up
SIPs) plus the `[P2]`/`[P3]` items (index/thematic rebalancing baskets
with one-click rebalance, Robo-Advisory, goal-based investing).

It also now builds `FEATURES.md` §5 "Fixed Income" in full (primary-market
bidding, secondary-market YTM, bond ladders) and four `[P4]` items from
§17 "Wealth & Product Breadth" (global markets access, retirement account
wrappers, structured products, insurance cross-sell) — see "Fixed Income"
and "Wealth & Product Breadth (§17 `[P4]` items)" below.

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
- 88 tests across the original six §4 packages (6 `fundcatalog`, 11
  `amcrouting`, 19 `sipscheduler`, 18 `basketrebalancing`, 15
  `roboadvisory`, 19 `goalinvesting`) — plus another 136 tests across the
  eight packages added for §5 "Fixed Income" and §17's `[P4]` items (8
  `fixedincome`, 14 `primarymarketbidding`, 17 `secondarymarketbonds`, 18
  `bondladderbuilder`, 26 `globalmarketsaccess`, 17 `retirementaccounts`,
  20 `structuredproducts`, 16 `insurancecrosssell`) — 224 tests total
  across the whole service. The original 88 cover happy paths, pause/cancel/resume
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

## Fixed Income (FEATURES.md §5) — real bidding/allotment, real YTM math, real ladder-building, all over an illustrative catalog with NO real RBI/CCIL connectivity

What's real:
- **A static bond catalog** (`internal/fixedincome`): seven illustrative,
  entirely fictitious G-Sec/T-Bill/SGB instruments (three G-Secs, two
  T-Bills, two SGBs) spanning maturities from ~3 months to 10 years, each
  with an issue name, issue date, maturity date, coupon rate (0 for
  T-Bills — pure discount instruments), payments-per-year (2/semi-annual
  for coupon-bearing instruments), face value, and a STATIC illustrative
  credit rating (AAA/AA/A — every real instrument here is actually a
  sovereign obligation; the varied ratings exist purely to illustrate a
  broader ladder that might one day include corporate bonds). A fixed,
  hand-rolled "auction calendar" (`SeedAuctionCalendar`) ties five of the
  seven bonds (G-Secs and T-Bills; SGBs are sold via subscription in
  reality, not competitive auction, so are deliberately excluded) to
  illustrative scheduled auction dates and notified amounts.
- **Primary market bidding: G-Secs/T-Bills, RBI auction calendar**
  (`internal/primarymarketbidding`, FEATURES.md §5 item 1): a REAL
  `SCHEDULED → OPEN → CLOSED` auction state machine, opened by
  `OpenDueAuctions` sweeping past each auction's scheduled date (same
  "sweep, don't push" pattern as `amcrouting.ConfirmDueOrders`), with
  `SubmitBid` accepting a quantity (face value) + yield only while OPEN.
  `CloseAuction` runs a REAL, documented allotment rule: a MULTIPLE-PRICE
  ("French") yield-priority auction — bids sorted ascending by requested
  yield (lowest yield = most competitive), allotted greedily against the
  notified amount AT EACH BID'S OWN YIELD, with bids tied at the exact
  cutoff yield split PRO-RATA by requested quantity (last tied bid absorbs
  the rounding remainder so the split sums exactly), and everything past
  the cutoff REJECTED outright. Hand-worked and tested: an oversubscribed
  91-day T-Bill auction (notified ₹20,00,000.00) with two ₹15,00,000.00
  bids at different yields allots the lower-yield bid in full and the
  higher-yield bid exactly the remaining ₹5,00,000.00 (PARTIALLY_ALLOTTED);
  a 364-day T-Bill auction (notified ₹15,00,000.00) with two TIED
  ₹10,00,000.00 bids at the same yield splits the notified amount exactly
  50/50 (₹7,50,000.00 each), summing EXACTLY to the notified amount.
- **Secondary market bond browsing + YTM calculator**
  (`internal/secondarymarketbonds`, FEATURES.md §5 item 2): a REAL
  Newton-Raphson Yield-to-Maturity solver (`CalculateYieldToMaturity`)
  over the standard bond-pricing equation, using the closed-form ANALYTIC
  derivative of price with respect to yield at each iteration step (not a
  numeric finite-difference approximation), annualizing the solved
  periodic yield. A zero-coupon instrument (a T-Bill) is solved via the
  exact closed form `y = (faceValue/price)^(1/years) - 1` directly — no
  iteration, no rounding drift. Hand-worked and tested with THREE
  independent, non-circular identities: (1) a PAR bond (price == face
  value) has YTM exactly equal to its coupon rate — 7.10% face-value-priced
  GSEC-07.10-2028 over 20 semi-annual periods recovers exactly 7.10%; (2) a
  zero-coupon bond priced at exactly `faceValue/1.21` for a 2-year term
  recovers exactly 10.00%; (3) a zero-coupon bond with a clean 1-year term
  (face 1000, price 900) recovers the exact closed-form 11.1111...%. A
  round-trip test also confirms `CalculateYieldToMaturity` recovers a
  target yield a coupon bond was independently priced at.
  `internal/secondarymarketbonds.SecondaryMarket` seeds an illustrative
  current price per catalog bond (never moves on its own — only
  `UpdatePrice`, same testing/demo-only hook pattern as
  `fundcatalog.UpdateNav`) and `ListListings` returns a browsable listing
  of every bond with its current price and real computed YTM.
- **Bond ladder builder, credit rating display, coupon calendar/reminders**
  (`internal/bondladderbuilder`, FEATURES.md §5 item 3): `BuildLadder`
  spreads a target investment across a target number of rungs picked at
  EVENLY-SPACED indices across the catalog sorted by maturity (so the
  first rung is always the nearest-maturity bond, the last is always the
  farthest, staggered in between — not just "the first N bonds"), split
  evenly across rungs with the LAST rung absorbing the rounding remainder
  (same convention as `basketrebalancing`'s per-leg split), each rung
  carrying `fixedincome`'s static credit rating. `UpcomingCoupons` computes
  a REAL coupon-payment calendar for a holder's ladder positions: coupon
  dates are stepped forward genuinely from each bond's own `IssueDate` in
  `12/PaymentsPerYear`-month increments (calendar-aware, via
  `time.Time.AddDate` — same pattern `sipscheduler` uses for step-up
  anniversaries) up to `MaturityDate`, filtered to dates after `asOf`; a
  zero-coupon T-Bill correctly produces NO reminders. Hand-worked and
  tested: holding GSEC-07.10-2028 (issued 2023-08-01, 7.10% semi-annual,
  face 100000) at full face value, `asOf` 2026-08-14 (just past the
  2026-08-01 coupon), the next reminder is computed as exactly 2027-02-01
  for exactly ₹35.50 (3550 minor units) — the same amount the catalog's
  own full-face-value coupon would pay.
- **Verified live end-to-end**: opened the GSEC-07.10-2028 auction via
  `?asOf=`, submitted two competing bids, closed the auction, and got back
  the exact hand-computed ALLOTTED/PARTIALLY_ALLOTTED split; browsed the
  secondary market and confirmed a bond re-priced to exactly its face
  value reports YTM ≈ 7.0999999998% (its own 7.10% coupon rate, to
  float-precision); built a 7-rung ladder spanning the FULL catalog
  (₹7,00,000.00 across all 7 bonds) and confirmed the rungs come back
  sorted nearest-to-farthest maturity (T-Bills first, GSEC-06.90-2036
  last); and pulled the resulting coupon calendar and confirmed
  GSEC-07.10-2028's entry lands on 2027-02-01 for exactly ₹35.50, matching
  the hand-worked test.

What's a placeholder in Fixed Income:
- **NOT connected to any real RBI auction system.** There is no E-Kuber
  (RBI's real primary-auction platform) integration anywhere in this repo
  — the "auction" on the other end of every bid in
  `internal/primarymarketbidding` is that package's own in-memory state.
- **NOT connected to any real secondary bond market.** There is no
  CCIL/NDS-OM (the real institutional G-Sec secondary market) or exchange
  (NSE/BSE retail bond trading) integration — `internal/secondarymarketbonds`'s
  "current price" is its own in-memory, entirely illustrative number.
- The bond catalog is seven hardcoded, entirely fictitious instruments —
  no AMFI/RBI feed, no real issue calendar beyond the five hand-picked
  fixture dates in `SeedAuctionCalendar`.
- Credit ratings are a static illustrative field, not a real ratings-agency
  feed — see `internal/fixedincome`'s doc comment.
- `internal/bondladderbuilder.BuildLadder` does NOT route through
  `internal/primarymarketbidding` or `internal/secondarymarketbonds` at
  all — a ladder purchase is recorded directly into the builder's own
  holdings, instantly, at face value, as if executed at par; a real build
  would route each rung through a real bid or trade and only record the
  holding once that settles.
- No cash-side integration anywhere in this section — same gap as §4's
  mutual fund orders.
- In-memory only, no auth, no persistence — same gaps as the rest of this
  service.

## Wealth & Product Breadth (§17 `[P4]` items) — four smaller, more explicitly illustrative packages

FEATURES.md §17 lists several `[P4]` "wealth & product breadth" items.
This service builds four of them as small, self-contained, honestly-
caveated packages — genuinely regulated products (global brokerage,
retirement tax wrappers, insurance) can't actually be built end-to-end in
this repo, so each package draws its own honest line around what's real
(the mechanics/rules engine) versus what's illustrative (the regulated
substance).

- **Global markets access (US/international stocks via GDR/ADR or partner
  brokerage rails)** (`internal/globalmarketsaccess`, FEATURES.md §17): a
  small catalog of five illustrative, entirely fictitious ADR-style
  symbols; a REAL `PENDING → ROUTED → CONFIRMED` order state machine
  (`Router`) — `RouteOrder` converts the investor's INR funding amount to
  the symbol's quote currency at the CURRENT FX rate (struck at routing
  time, standing in for "handed off to the partner brokerage rail"), and
  `ConfirmOrder` allocates units at the symbol's CURRENT catalog price
  (struck at confirmation, mirroring `amcrouting`'s "NAV struck at
  confirmation, not placement" pattern). Currency conversion
  (`CurrencyConverter`) reuses the CONCEPT of a multi-currency wallet — a
  per-pair FX rate table — WITHOUT calling ledger's real
  `internal/multicurrencywallet`; a real build would integrate with that
  service directly. Hand-worked and tested: ₹8,300.00 (830000 minor units)
  at the seeded 83.00 INR/USD rate converts to exactly $100.00 (10000
  cents). LOUD CAVEAT: NO real GDR/ADR issuance or partner-brokerage
  connectivity exists anywhere in this repo.
- **Retirement account wrappers (NPS/IRA-equivalent, tax-advantaged
  structures per jurisdiction)** (`internal/retirementaccounts`,
  FEATURES.md §17): a real account-type classification
  (`NPS_EQUIVALENT`/`IRA_EQUIVALENT`), each with its own illustrative,
  hand-picked ANNUAL CONTRIBUTION LIMIT that is REALLY ENFORCED —
  `Contribute` sums the calendar year's contributions so far and REJECTS
  anything that would push the total past the limit (hand-worked and
  tested at the EXACT boundary: the limit itself succeeds, one minor unit
  more is rejected; the limit resets in a new calendar year) — and a real,
  ENFORCED lock-in rule: `Withdraw` computes the holder's eligible
  withdrawal date as `dateOfBirth.AddDate(minimumRetirementAge, 0, 0)`
  (calendar-aware, same pattern `sipscheduler` uses for step-up
  anniversaries) and REJECTS any withdrawal before that date, tested at
  the exact day-before/day-of boundary. LOUD CAVEAT: the underlying TAX
  ADVANTAGE is entirely illustrative — no real PFRDA/IRS integration, no
  real tax benefit computed or claimed anywhere; what's real is the RULES
  ENGINE (limits + lock-in), genuinely enforced over illustrative inputs.
- **Structured products desk (capital-protected notes, market-linked
  debentures)** (`internal/structuredproducts`, FEATURES.md §17): a real,
  testable payoff-structure definition — "100% capital protection +
  PARTICIPATION_RATE% participation in an underlying index's upside, capped
  at CAP%" — computed by `CalculatePayoff`: a non-positive index return
  returns the FULL protected principal (zero downside participation, by
  construction); a positive return participates at the note's rate, capped
  at the note's ceiling. A real `SUBSCRIBED → MATURED` state machine
  (`Desk`) tracks subscriptions and computes the real payout at maturity.
  Hand-worked and tested: ₹1,000.00 principal, 150% participation, 20%
  cap, +10% index return → participated return 15% (under the cap) →
  payout exactly ₹1,150.00; the SAME note at +20% index return →
  participated return 30% (OVER the cap) → capped at 20% → payout exactly
  ₹1,200.00, `wasCapped=true`; a -25% index return still returns the FULL
  principal. LOUD CAVEAT: no real underlying index feed exists — the
  index's return is a plain number the caller supplies at maturity time,
  not fetched from any real market data source, and this is NOT connected
  to any real structured-products issuance desk or investment bank.
- **Insurance cross-sell (term/health)** (`internal/insurancecrosssell`,
  FEATURES.md §17): a DELIBERATELY THIN integration stub — `PartnerClient`
  is the boundary this platform calls OUT across to reach a separate,
  independently regulated insurer; `MockInsurancePartnerClient` stands in
  for that real partner's quote API with a simple, illustrative flat-rate
  premium formula (`annualRatePercent = baseRate + ageLoading * age`),
  hand-worked and tested: TERM_LIFE, age 30, ₹10,00,000.00 coverage →
  0.10% + 0.01%×30 = 0.40% → premium exactly ₹4,000.00; HEALTH, age 40,
  ₹5,00,000.00 coverage → 1.00% + 0.05%×40 = 3.00% → premium exactly
  ₹15,000.00. `Service.RegisterInterest` records a lead against a
  previously-quoted `quoteId` — the ENTIRE scope of what this platform
  does; everything past that (application, underwriting, policy issuance)
  is explicitly out of scope, real, and belongs to the separately
  regulated partner insurer. LOUD CAVEAT: this is NOT an insurance
  underwriting engine — there is no real actuarial rate table, no medical
  underwriting, and no real insurer integration anywhere in this repo.
- **Verified live end-to-end** for all four: placed and routed a global
  order (₹8,300.00 → $100.00 exactly), confirmed it into 1.1876 units at
  the catalog's $84.20 price; opened an NPS-equivalent retirement account,
  contributed exactly its ₹15,00,000.00 illustrative annual limit
  (succeeded), then one more minor unit (rejected), then confirmed
  withdrawal is rejected before age 60 and succeeds at/after it; subscribed
  to a capital-protected structured note and matured it at +20% index
  return for the exact hand-computed capped payout of ₹1,200.00 on a
  ₹1,000.00 principal; and requested a TERM_LIFE quote (exact ₹4,000.00
  premium) and registered interest against it.

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
