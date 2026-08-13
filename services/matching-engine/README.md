# matching-engine

Tier 0 component — see `ARCHITECTURE.md` §3 in the repo root.

## Status: skeleton

What's real:
- Single-instrument, price-time-priority limit order book
  (`src/orderBookCore.rs`), fully tested (`cargo test`)
- Correct partial-fill and multi-level-crossing matching logic
- **A real TCP+JSON server** (`main.rs`, `src/wireProtocol.rs`) reachable
  from `oms-gateway`'s `internal/matchingengineclient` — orders submitted
  through the OMS's HTTP API now genuinely reach this order book and
  produce real fills, verified end-to-end (see
  `docs/BUILD_LOG.md` entry 14)
- **Publishes real book depth to `market-data`** after every processed
  order (`publishBookDepthToMarketData` in `main.rs`) — fire-and-forget,
  deliberately tolerant of market-data being unreachable so a Tier 1
  consumer's availability can never affect order processing. Verified
  end-to-end with all four services running (`docs/BUILD_LOG.md`
  entry 15).
- **Publishes real trade ticks alongside that depth publish**
  (`OutgoingTradeTickWireEvent` in `wireProtocol.rs`) — whatever trades
  the just-processed order produced (possibly none), so market-data can
  build a trade tape and OHLCV candles. See that service's README and
  `docs/BUILD_LOG.md` entry 26.
- **Market orders** (`OrderType::Market`, alongside `Limit`) — crosses
  regardless of resting price, never rests an unfilled remainder
  (IOC-like). See `docs/BUILD_LOG.md` entry 17. Known gap: oms-gateway's
  risk check currently can't estimate a market order's notional (no
  last-price feed yet), so it always passes trivially for market orders —
  flagged in that service's code, not fixed here.
- **Stop-loss orders** (`OrderType::StopLossLimit`/`StopLossMarket`) —
  FEATURES.md §3's SL/SL-M. Armed orders sit in a `pendingStopOrders`
  pool, invisible to depth snapshots, until `lastTradedPriceInMinorUnits`
  crosses their trigger (BUY stops fire on price rising through trigger,
  SELL stops on price falling through it), at which point they convert to
  a live `Market`/`Limit` order and match normally. Triggering cascades:
  a triggered stop's own trade can arm another stop behind it, handled by
  looping until a full scan finds nothing left to trigger. See
  `docs/BUILD_LOG.md` entry 18. Same risk-check notional gap as market
  orders applies to `StopLossMarket`.
- **Order cancellation** (`OrderBookCore::cancelOrder`) — every order is
  assigned a local id at intake (not just ones that end up resting), so a
  resting `Limit` remainder or a still-armed stop order can be pulled off
  the book by id. Reachable over the wire via a
  `cancelOrderSequenceNumber` field on the same JSON line format used for
  submission. See `docs/BUILD_LOG.md` entry 19.
- **Order status queries** (`OrderBookCore::queryOrderStatus`) —
  read-only lookup of whether an order is still resting (with its live
  remaining quantity), still armed as a pending stop, or gone. Same
  flat-JSON extension pattern via `queryOrderStatusSequenceNumber`. See
  `docs/BUILD_LOG.md` entry 22.

What's a placeholder (see inline `TODO(real build)` markers):
- The TCP+JSON bridge is a synchronous one-line-request/one-line-response
  protocol — explicitly NOT the lock-free ring-buffer ingress with a
  zero-copy binary (SBE) encoding described in ARCHITECTURE.md §3.1/§3.5.
  It proves the service boundary; it is not fast.
- The market-data publish sends the FULL book depth every time, not an
  actual diff (`OrderBookCore::currentBookDepthSnapshot`'s own TODO)
- No WAL / event-sourced crash recovery (§3.4)
- No sharding-by-instrument / sequencer router (§3.2) — single hardcoded
  instrument (`DEMO-EQ`)
- No NUMA pinning, busy-spin core isolation, or kernel-bypass networking (§3.3, §3.5)
- The sequential TCP accept loop preserves the single-writer principle
  without a `Mutex`, which is the one design choice in this bridge that
  *does* line up with the real architecture — worth keeping even after
  the bridge itself is replaced.

## Run it

```bash
cargo run      # starts the TCP server on 127.0.0.1:9101
cargo test     # runs the order-book + wire-protocol test suite (29 tests)
```

## Naming convention

This crate intentionally uses long, descriptive **camelCase** identifiers
instead of Rust's default snake_case — see the project-level naming
convention note. `#![allow(non_snake_case)]` at the crate root suppresses
the resulting lint noise; don't remove it and don't let `cargo fmt`/clippy
"fix" names back to snake_case.
