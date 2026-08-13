# Trading App — Feature Manifest & Build Plan

A unified retail + institutional trading platform (Zerodha-grade broking UX,
Bloomberg/QuantConnect-grade terminal and quant engine). This document is the
implementation backlog: every feature is a checkbox, grouped by system and
tagged with a delivery phase so the repo can be built incrementally instead
of attempted all at once.

Companion doc to write next: `ARCHITECTURE.md` (service boundaries, data
flow diagrams, tech stack decisions per service).

**Progress markers:** `🚧` next to an item means a real, tested skeleton
slice exists — not that the item is production-complete (checkboxes stay
unchecked until it genuinely is). See `docs/DOCUMENTATION.md` for exactly
what's real vs. placeholder, and `docs/BUILD_LOG.md` for how it got
built.

---

## 0. Phasing Strategy

Building all of this simultaneously is how these projects die. Ship in
this order:

- **Phase 0 — Foundations:** Auth, ledger, one asset class (equities cash),
  paper trading only. No real money, no real exchange connection.
- **Phase 1 — Retail Broking MVP:** Real equities + MF investing, KYC,
  payments, basic charting, order management, compliance reporting.
- **Phase 2 — Derivatives & Terminal:** F&O, options chain, Greeks, margin,
  the Pro Terminal shell, real-time data pipeline at scale.
- **Phase 3 — Quant Engine:** Backtesting, strategy deployment, algo hooks,
  GARCH/vol surfaces, pairs trading, market making sandbox.
- **Phase 4 — Institutional Grade:** Matching engine internals, DMA/FIX
  access, SOR, kernel-bypass networking, compliance surveillance tooling.

Every feature below is tagged `[P0]`–`[P4]` accordingly, as a rough guide
to typical build order — not a hard gate. A P2+ item can be picked up any
time; if it has real P0/P1 dependencies that don't exist yet, build those
alongside it in the same pass rather than treating the tag as a blocker.

---

## 0.1 V1 Differentiators — Ship These to Feel Better Than Zerodha/Groww, Not Just At-Parity

At-parity features (order types, MF investing, basic charts) get you to the
starting line. The list below is what makes early users say "this is
actually better," not "this is another Zerodha clone." Each one is chosen
because it's a known, sharp pain point on today's incumbents, and each is
buildable within Phase 0–2 — none of these require the matching engine,
FIX access, or other Phase 4 heavy lifting.

| # | Feature | Section | Why it wins | Effort |
|---|---------|---------|-------------|--------|
| 1 | Plain-language order rejection reasons | §21 | The single largest support-ticket category on every incumbent app; costs almost nothing to build, fixes it on day one | Low |
| 2 | Full charges breakdown before order confirmation | §21 | "Hidden charges" is the #1 trust complaint in broking app reviews; a live pre-trade receipt is a 1–2 week build | Low |
| 3 | Idempotent order status + WS-reconnect reconciliation | §21 | "Did my order go through?" during a dropped connection is a recurring five-star-review killer; this is core plumbing, not a UI feature | Low–Med |
| 4 | Portfolio-level Greeks aggregation (net delta/gamma/theta/vega) | §22 | No mainstream Indian retail platform surfaces this; it's exactly what an options seller needs and currently requires exporting to Excel | Med |
| 5 | IV Rank / IV Percentile per instrument | §22 | The metric serious options traders (tastytrade-style) actually trade off — raw IV alone is close to useless for decision-making | Med |
| 6 | Pre-trade impact-cost / slippage estimator | §15 | Turns "I got a bad fill" complaints into an informed decision made *before* the click; differentiates on illiquid F&O strikes especially | Med |
| 7 | Arbitrage scanner (theoretical vs. live options price deviation) | §6 | A direct, visible payoff from the Black-Scholes engine you're building anyway — makes the quant math tangible to users, not just backend plumbing | Med |
| 8 | Portfolio stress test ("Nifty −10% ⇒ you lose ~₹X") | §21 | Existing apps show P&L, not risk; this reframes the dashboard from "what did I make" to "what could I lose," which is what actually retains long-term investors | Med |
| 9 | Public developer API + sandbox (Kite Connect-style) | §18 | Zerodha's Kite Connect single-handedly created an ecosystem of third-party tools around it; Groww has nothing comparable — this is a durable moat, not a feature | Med–High |
| 10 | Mandatory F&O cooling-off + behavioral nudges (overtrading/revenge-trading detection) | §19 | Turns an incoming regulatory requirement into a trust signal shipped early, instead of a compliance patch bolted on later | Low–Med |

**Sequencing note:** #1–3 and #10 are Phase 0–1 (cheap, foundational,
ship with the MVP). #4–8 land naturally in Phase 2 alongside the options
chain and quant engine — build the differentiator UI *in the same sprint*
as the underlying feature, not as a later polish pass. #9 is the one
long-lead item; start the API contract design in Phase 1 even if the
public release ships in Phase 2, since retrofitting a clean external API
onto an internal one later is expensive.

---

## 1. Identity, Onboarding & Compliance (missing from the original brief — this is the actual gate to launching in any regulated market)

- [ ] 🚧 `[P0]` Email/phone auth, session management, JWT + refresh token rotation
- [ ] 🚧 `[P0]` MFA (TOTP + SMS fallback), device binding, biometric unlock (mobile)
- [ ] 🚧 `[P1]` Digital KYC: PAN/Aadhaar (or local equivalent) verification, liveness
      check + selfie match, e-signature for account opening docs
- [ ] 🚧 `[P1]` Bank account verification (penny-drop / micro-deposit)
- [ ] 🚧 `[P1]` Risk profiling questionnaire → investor risk category (feeds Robo-Advisory)
- [ ] `[P1]` Nominee management, joint holding support
- [ ] `[P1]` Regulatory reporting: contract notes, ledger statements, tax P&L
      (STCG/LTCG), Annual Information Statement reconciliation
- [ ] 🚧 `[P2]` AML transaction monitoring (unusual pattern flags, PEP screening)
- [ ] 🚧 `[P2]` Audit trail: immutable log of every order, modification, cancellation
      with actor, timestamp, IP — required for regulator inquiries
- [ ] 🚧 `[P2]` Segregation of client funds vs. firm funds (regulatory requirement
      in most jurisdictions — client money must be ring-fenced)
- [ ] `[P3]` Surveillance system: spoofing/layering/wash-trade detection for
      compliance officers, replay tooling tied to Tick-to-Trade Analytics

## 2. Payments & Banking Rails

- [ ] 🚧 `[P0]` Wallet/ledger core: double-entry accounting, idempotent transactions
- [ ] 🚧 `[P1]` UPI / NEFT / IMPS / net-banking deposit integration
- [ ] 🚧 `[P1]` Withdrawal workflow with T+N settlement holds
- [ ] 🚧 `[P1]` Auto-payment mandates for SIPs (eNACH/standing instructions)
- [ ] 🚧 `[P2]` Margin funding / instant margin against pledged collateral payout
- [ ] 🚧 `[P2]` Multi-currency wallet (for platforms offering global/US stocks)

## 3. Equities & Derivatives (Wealth + Active Trading)

- [ ] 🚧 `[P1]` Order entry: Market, Limit, SL, SL-M
- [ ] 🚧 `[P1]` Cover Orders (CO), Bracket Orders (BO), GTT, AMO
- [ ] 🚧 `[P1]` Order book / trade book / positions / holdings views
- [ ] 🚧 `[P2]` Real-time Options Chain: OI, Volume, IV, strike ladder, PCR
- [ ] 🚧 `[P2]` Greeks computed live per contract (Delta/Gamma/Theta/Vega/Rho)
- [ ] 🚧 `[P2]` Margin Pledge system (stocks/MF as collateral)
- [ ] 🚧 `[P2]` SPAN + Exposure margin calculator for F&O
- [ ] 🚧 `[P3]` Iceberg, FOK, IOC order types for institutional flow
- [ ] 🚧 `[P4]` Direct Market Access (DMA) / FIX gateway for institutional clients

## 4. Mutual Funds, ETFs & Baskets

- [ ] 🚧 `[P1]` Direct AMC routing (commission-free MF investing)
- [ ] 🚧 `[P1]` Lumpsum + SIP setup, SIP pause/cancel, SIP calendar
- [ ] 🚧 `[P1]` Step-Up SIPs (auto-increase % annually)
- [ ] 🚧 `[P2]` Index/thematic rebalancing baskets with one-click rebalance
- [ ] 🚧 `[P2]` Robo-Advisory: risk-profile → Efficient Frontier allocation
      (uses Sharpe Ratio module, see §6)
- [ ] 🚧 `[P3]` Goal-based investing (retirement, education) with progress tracking

## 5. Fixed Income

- [ ] `[P2]` Primary market bidding UI: G-Secs, T-Bills, SGBs (RBI auction calendar)
- [ ] `[P2]` Secondary market bond browsing + YTM calculator
- [ ] `[P3]` Bond ladder builder, credit rating display, coupon calendar/reminders

## 6. Quant Math Engine (as a standalone internal service, not scattered logic)

- [ ] 🚧 `[P2]` Black-Scholes pricer + Greeks, exposed via internal gRPC + public API
- [ ] 🚧 `[P2]` Implied Volatility solver (Newton-Raphson/Brent on market price)
- [ ] 🚧 `[P2]` Arbitrage scanner: theoretical vs. live price deviation alerts
- [ ] 🚧 `[P2]` Sharpe Ratio / Sortino Ratio / max drawdown per portfolio & per strategy
- [ ] 🚧 `[P3]` GARCH(1,1) overnight batch job → "Expected Intraday Range" widget
- [ ] 🚧 `[P3]` Correlation matrix engine for pairs-trading candidate discovery
- [ ] 🚧 `[P3]` Value-at-Risk (VaR) and stress-testing for margin/risk engine
- [ ] 🚧 `[P4]` Volatility surface construction (per-expiry smile/skew) for options desks

## 7. Algorithmic Trading & Backtesting

- [ ] 🚧 `[P3]` Historical tick data store + backtest runner (Python strategy SDK)
- [ ] 🚧 `[P3]` Paper trading mode sharing the exact same OMS code path as live
- [ ] 🚧 `[P3]` Strategy deployment pipeline: backtest → paper → live promotion gates
- [ ] 🚧 `[P3]` Pairs trading template (z-score mean reversion) as reference strategy
- [ ] 🚧 `[P4]` Market-making sandbox: quote/inventory risk API for approved institutional clients
- [ ] 🚧 `[P4]` Event-driven NLP trading: filings/earnings ingestion → sentiment → order hook
      (build with strict kill-switches; this category is the easiest to blow up an account)
- [ ] 🚧 `[P4]` Strategy resource limits & circuit breakers (max orders/sec, max notional/day per algo)

## 8. Market Data Pipeline

- [ ] 🚧 `[P1]` Exchange feed ingestion (simulated/sandbox feed for Phase 0–1)
- [ ] 🚧 `[P1]` OHLCV candle aggregation (1m/5m/15m/1D) + historical bar storage
- [ ] 🚧 `[P1]` WebSocket broadcast for L1 quotes to web/mobile clients
- [ ] 🚧 `[P2]` L2 market depth (DOM) broadcast with delta compression
- [ ] 🚧 `[P2]` Client-side reconnect/resync protocol (sequence numbers, snapshot+delta)
- [ ] 🚧 `[P3]` Tick-level storage in a columnar time-series store for replay/backtest
- [ ] 🚧 `[P4]` UDP multicast fan-out for co-located institutional consumers

## 9. Matching Engine (only once Phase 2+ demands owning the book, e.g. running your own exchange/dark pool — most retail brokers route to an exchange instead of building this)

- [ ] 🚧 `[P4]` Price-time priority Limit Order Book, single-threaded core
- [ ] 🚧 `[P4]` Event sourcing + WAL replay for crash recovery
- [ ] 🚧 `[P4]` Lock-free ring buffer ingress/egress
- [ ] 🚧 `[P4]` Deterministic replay test harness (same input sequence → same book state, always)

## 10. The Terminal (Pro Desktop)

- [ ] `[P2]` GoldenLayout-based tiling workspace, saved layouts per user
- [ ] `[P2]` Command bar / hotkey system (`AAPL DES <GO>` style)
- [ ] `[P2]` WebGL/Canvas candlestick charts with indicator overlays (MACD, RSI, BB, Fib)
- [ ] `[P2]` DOM ladder widget with click-to-trade
- [ ] `[P3]` Multi-monitor window detachment (Tauri native windows)
- [ ] `[P3]` Local Python hook sandbox for algo traders (isolated subprocess, resource-capped)
- [ ] `[P3]` News/sentiment ticker widget

## 11. Retail Web/Mobile App

- [ ] 🚧 `[P0]` Auth, dashboard, portfolio summary
- [ ] 🚧 `[P1]` Order ticket (equities), MF investing flow, SIP management
- [ ] 🚧 `[P1]` Watchlists, alerts (price/technical triggers)
- [ ] `[P2]` Options chain (simplified retail view)
- [ ] `[P2]` Push notifications: order fills, price alerts, margin calls
- [ ] `[P3]` Social/copy-trading: follow verified strategies or traders (opt-in, disclosed)

## 12. Risk, Margin & Surveillance

- [ ] 🚧 `[P1]` Pre-trade risk checks: balance/margin sufficiency, position limits
- [ ] `[P2]` Real-time Mark-to-Market engine across leveraged positions
- [ ] `[P2]` Auto-liquidation on margin breach, with graduated warnings first
- [ ] `[P2]` Per-user, per-segment exposure limits (configurable by risk team)
- [ ] `[P4]` Circuit breaker / kill-switch at the exchange-connectivity layer

## 13. Platform, DevOps & Observability (absent from the original brief — non-negotiable for anything handling money)

- [ ] 🚧 `[P0]` CI/CD per service, environment promotion (dev → staging → prod)
- [ ] 🚧 `[P0]` Structured logging + centralized log aggregation
- [ ] 🚧 `[P0]` Metrics/tracing (latency histograms on the execution path especially)
- [ ] `[P1]` Alerting on SLO breach (feed staleness, order-reject spikes, matching latency)
- [ ] `[P1]` Secrets management, least-privilege IAM per service
- [ ] `[P1]` Automated backups + tested restore procedure for ledger DB
- [ ] `[P2]` Disaster recovery: documented RTO/RPO, DR region failover drill
- [ ] `[P2]` Chaos/load testing on the OMS and matching path before go-live
- [ ] `[P2]` API gateway rate limiting, quota tiers (retail vs. institutional)
- [ ] `[P3]` Blue/green or canary deploys for the matching engine specifically

## 14. Customer Support & Ops Tooling

- [ ] `[P1]` In-app support chat / ticketing integration
- [ ] 🚧 `[P1]` Admin/backoffice panel: KYC review queue, manual order intervention,
      account freeze/unfreeze
- [ ] `[P2]` Corporate actions processing: dividends, splits, bonuses, mergers
      reflected automatically in holdings and cost basis
- [ ] `[P2]` Referral & rewards program
- [ ] `[P3]` Multi-language/localization support for the retail app

## 15. Advanced Execution & Trading Sophistication

- [ ] `[P3]` Execution algos for institutional/large orders: VWAP, TWAP, POV
      (Percentage of Volume), Implementation Shortfall
- [ ] `[P3]` Multi-leg options strategy builder (straddle, strangle, spreads,
      iron condor, butterfly) with atomic all-or-nothing execution
- [ ] `[P3]` Options strategy payoff diagram (max profit/loss, breakevens
      computed live as legs are added)
- [ ] `[P3]` Basket/program order execution (buy/sell N instruments as one
      logical order with net cash constraint)
- [ ] `[P3]` Pre-trade impact-cost / slippage estimator ("what-if" simulator
      before order submission, using current DOM depth)
- [ ] `[P4]` Portfolio margining / cross-margining across correlated asset
      classes (equities + derivatives netted, not siloed)
- [ ] `[P4]` Securities Lending & Borrowing (SLB) desk for short-sellers and
      idle-holding yield generation
- [ ] `[P4]` Pre-market / post-market / extended-hours session support with
      distinct matching rules

## 16. AI, Data & Research

- [ ] `[P2]` Stock/fund screener with custom filter builder (fundamental +
      technical criteria, saved screens)
- [ ] `[P3]` AI research copilot: RAG over filings/earnings calls/annual
      reports, cites sources, clearly labeled as non-advisory
- [ ] `[P3]` Portfolio health check / diversification analysis (sector,
      factor, concentration risk) with plain-language nudges
- [ ] `[P3]` Tax-loss harvesting suggestions (identify unrealized losses to
      offset realized gains, respecting wash-sale-equivalent rules)
- [ ] `[P4]` Alternative data feeds (sentiment aggregation, filing-anomaly
      detection) feeding into the NLP trading module from §7
- [ ] `[P4]` Factor-based P&L attribution (how much of return is sector beta
      vs. stock selection vs. currency)
- [ ] `[P4]` Custom index construction + backtested historical performance,
      licensable to other institutions

## 17. Wealth & Product Breadth

- [ ] `[P3]` Fractional share investing
- [ ] `[P3]` Dividend Reinvestment Plans (DRIP), auto-compounding toggle
- [ ] `[P3]` ESG/sustainability scoring and screening filters
- [ ] `[P3]` Loan Against Securities (LAS) — instant credit line against holdings
- [ ] `[P4]` Global markets access (US/international stocks via GDR/ADR or
      partner brokerage rails)
- [ ] `[P4]` Retirement account wrappers (NPS/IRA-equivalent, tax-advantaged
      structures per jurisdiction)
- [ ] `[P4]` Structured products desk (capital-protected notes, market-linked
      debentures)
- [ ] `[P4]` Insurance cross-sell (term/health) — separate regulated entity,
      integrated via API only

## 18. Platform, Ecosystem & Institutional Tooling

- [ ] `[P3]` Public developer API with sandbox, API-key management, tiered
      rate limits (this is how Zerodha's Kite Connect and Alpaca monetize
      the platform beyond retail commissions)
- [ ] `[P3]` Webhook system for order/portfolio events (for third-party
      integrations, accounting software, tax tools)
- [ ] `[P4]` White-label / Broker-as-a-Service offering for fintechs
- [ ] `[P4]` FIX protocol certification suite for institutional onboarding
- [ ] `[P4]` Transaction Cost Analysis (TCA) dashboards — post-trade best
      execution reporting for institutional clients (regulatory requirement
      in many markets: MiFID II-style best-ex proof)
- [ ] `[P4]` Account Aggregator integration — pull holdings from other
      brokers/banks for a unified net-worth view (where a regulatory AA
      framework exists)

## 19. Trust, Safety & Behavioral Design

- [ ] `[P2]` Overtrading / revenge-trading pattern detection with cool-down
      nudges (behavioral finance, not just risk-limit enforcement)
- [ ] `[P2]` Mandatory risk disclosure + cooling-off flow before first F&O
      order (increasingly a regulatory requirement, not optional UX)
- [ ] `[P3]` ML-based account-takeover / anomalous-login detection, distinct
      from the AML monitoring in §1
- [ ] `[P3]` Explicit "paper trading only" mode gate for strategies below a
      track-record threshold before they can request live capital
- [ ] `[P4]` Verified-track-record social/copy-trading leaderboards with
      disclosed, audited performance (not self-reported returns)

## 20. Advanced Charting & Market Microstructure Views

- [ ] `[P3]` Volume Profile / Market Profile (TPO) charts
- [ ] `[P3]` Order-flow footprint charts (bid/ask volume per price per candle)
- [ ] `[P4]` Historical DOM replay for a chosen instrument/time window (ties
      into the Tick-to-Trade Analytics replay tool in §4 of the original brief)

## 21. Customer Pain-Point Features (the stuff users actually complain about)

- [ ] 🚧 `[P1]` Plain-language order rejection reasons ("Insufficient margin: need
      ₹X more" not `ERR_4021`) — the #1 support-ticket generator on every
      retail broking app
- [ ] 🚧 `[P1]` Full charges breakdown *before* order confirmation: brokerage,
      STT/CTT, stamp duty, GST, exchange transaction charges, DP charges —
      shown as a receipt, not discovered after the fact
- [ ] `[P1]` Intraday auto square-off countdown timer + push reminder before
      forced closure, with the exact cutoff time per exchange/segment
- [ ] `[P2]` Corporate-action explainer: when a split/bonus/merger changes
      quantity or average price, show a one-line "why did this change"
      inline on the holding, not buried in a statement
- [ ] `[P2]` Idempotent order status with WebSocket-reconnect reconciliation —
      never let a user wonder "did my order actually go through?" after a
      dropped connection
- [ ] `[P2]` One-click capital gains statement export (broker-generated,
      pre-formatted for common tax-filing tools)
- [ ] `[P2]` Liquidity/fill-probability badge on the order ticket for
      illiquid instruments, with expected-time-to-fill estimate
- [ ] `[P2]` Margin/leverage interest cost calculator shown live — real cost
      of carrying a leveraged position, not just the margin required
- [ ] `[P3]` Portfolio stress test: "if Nifty drops 10% tomorrow, your
      portfolio loses ~₹X" using current Greeks/beta exposure
- [ ] `[P3]` Large-order friction: a brief confirm-with-context step for
      orders that are large relative to the user's history or the
      instrument's average volume (anti-fat-finger, anti-impulse)
- [ ] `[P3]` Family/joint account views with granular permissions (view-only
      access for a spouse/dependent, not full trading rights)
- [ ] `[P3]` Nominee succession workflow: documented, auditable transfer
      process triggered by a death certificate submission — currently a
      multi-week ordeal at most brokers
- [ ] `[P3]` Cross-device watchlist/alert sync with a home-screen live P&L
      widget (mobile)
- [ ] `[P4]` Conversational order placement (chat/voice) with an explicit
      confirm-before-execute step, aimed at less tech-savvy investors

## 22. Deep Quant & Algorithmic Trading Internals

- [ ] `[P3]` Portfolio-level Greeks aggregation: net delta/gamma/theta/vega
      across *all* positions, not per-contract — this is what actually
      matters to an options seller managing risk, and almost no retail
      platform surfaces it
- [ ] `[P3]` IV Rank / IV Percentile (current IV vs. its own 1-year range),
      not just raw IV — the metric serious options traders actually use to
      decide whether to buy or sell premium (tastytrade-style)
- [ ] `[P3]` Implied vs. realized volatility comparison chart, per instrument
- [ ] `[P3]` Synthetic position builder (e.g., synthetic long = long call +
      short put) with combined Greeks and margin shown as one unit
- [ ] `[P3]` Delta-hedging automation: auto-hedge or alert when portfolio net
      delta crosses a user-defined threshold
- [ ] `[P4]` Cointegration testing (Engle-Granger/Johansen) for pairs-trade
      candidate selection — correlation alone produces false pairs; this is
      the actual statistical test quant desks use
- [ ] `[P4]` Regime detection (Hidden Markov Model: trending/mean-reverting/
      high-vol classification) to gate which strategies are allowed to run
- [ ] `[P4]` Factor risk model (Fama-French-style or custom Barra-lite) for
      portfolio construction and exposure reporting
- [ ] `[P4]` Monte Carlo engine for path-dependent option pricing (barrier,
      Asian options) and portfolio-level VaR simulation
- [ ] `[P4]` Realistic backtest cost modeling: slippage, partial fills, and
      market-impact curves (Almgren-Chriss-style) instead of idealized
      fill-at-close backtests — the single biggest reason backtested
      strategies fail live
- [ ] `[P4]` Walk-forward optimization / out-of-sample validation built into
      the backtester, with automatic overfitting warnings (e.g. flag
      strategies with too many tunable parameters relative to sample size)
- [ ] `[P4]` Kelly-criterion-based position sizing calculator per strategy
- [ ] `[P4]` Strategy correlation matrix across a user's/desk's live
      strategies — surfaces when "5 algos" are secretly one correlated bet
- [ ] `[P4]` Latency benchmarking dashboard for algo clients: order
      round-trip histograms, per-venue comparison
- [ ] `[P4]` Cross-asset macro dashboard (yields, DXY, crude, VIX vs. equity
      indices) for macro/derivatives desks
- [ ] `[P4]` Options-aware corporate-action handling: auto-adjust strike/
      quantity on splits, flag early-exercise risk around ex-dividend dates
      for American-style contracts

---

## Cross-Cutting "What Else to Add" Summary

The original brief covered architecture, wealth features, quant math, and
matching-engine internals well, but skipped several things every real
brokerage/exchange platform treats as first-class:

1. **Regulatory & compliance** (§1) — KYC, AML, audit trails, fund
   segregation. This is usually the actual bottleneck to launch, not the
   tech.
2. **Payments/banking rails** (§2) — deposits, withdrawals, mandates.
3. **Corporate actions** (§14) — dividends/splits/bonuses silently corrupt
   holdings data if not handled explicitly.
4. **Risk/margin/surveillance as a distinct system** (§12) — not just an
   Options Greeks calculator, but the auto-liquidation and exposure-limit
   engine that keeps the firm solvent.
5. **DevOps/observability/DR** (§13) — a matching engine with no chaos
   testing or DR drill is a liability, not a feature.
6. **Support/backoffice tooling** (§14) — someone has to manually fix a
   stuck order or review a flagged KYC document; build that panel early.
7. **Kill-switches on anything autonomous** (§7) — NLP-driven auto-execution
   needs hard per-strategy notional/rate limits before it ever touches
   live capital.

---

## Suggested Repo Layout

```
Mercurius/
├── FEATURES.md              # this file
├── ARCHITECTURE.md          # service map, data flow, tech stack (write next)
├── services/
│   ├── oms-gateway/         # Go — order mgmt, risk checks, routing
│   ├── matching-engine/     # Rust/C++ — LOB core (Phase 4)
│   ├── market-data/         # Rust — feed ingestion, normalization, WS broadcast
│   ├── quant-engine/        # Python/Rust — Black-Scholes, GARCH, Sharpe, VaR
│   ├── ledger/              # Go/Postgres — double-entry accounting, settlement
│   ├── kyc-onboarding/      # Go — identity verification workflows
│   ├── mutual-funds/        # Go — AMC routing, SIP/lumpsum, step-up SIPs
│   └── backoffice/          # admin panel API
├── apps/
│   ├── web/                 # Next.js retail app
│   ├── terminal/            # Tauri + React pro terminal
│   └── mobile/              # React Native (Phase 2+)
└── infra/
    ├── ci/
    └── k8s/ or terraform/
```
