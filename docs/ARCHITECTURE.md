# Trading App — Architecture (Extreme Low Latency & Scale)

Companion to `FEATURES.md`. That doc says *what* to build; this one says
*how* it has to be built so that the execution path survives contact with
real market volumes and real competitors' latency numbers.

**Read this first:** low latency is not "use Rust" and high scale is not
"add more pods." Both come from a specific discipline — decouple the hot
path from everything else, make every hop on the hot path allocation-free
and lock-free, and push scale onto the paths that can tolerate it (market
data fan-out, retail API surface) while keeping the paths that can't
(order matching) deliberately simple, single-threaded, and mechanically
sympathetic to the hardware.

---

## 1. Design Tenets (non-negotiable, revisit before violating any of them)

1. **One thread owns the order book. Always.** The matching engine core
   never takes a lock. Concurrency is achieved by *not sharing* — every
   other thread talks to it via a ring buffer, never via a mutex.
2. **The hot path never allocates.** No `new`, no GC, no dynamic
   collection growth between "packet received" and "ack sent" on the
   execution path. Pre-allocate every buffer at startup; reuse via object
   pools.
3. **Decouple the path that must be fast from the path that must be
   correct-eventually.** Order acceptance is nanoseconds-sensitive.
   Regulatory reporting, analytics, and the ledger are not — they consume
   the same event stream asynchronously, off the hot path, and are allowed
   to be slow, retryable, and written in a GC'd language.
4. **Measure at every hop, not just end-to-end.** A single "order latency:
   800µs" metric is useless for debugging. Every hop (NIC → parser →
   risk check → matching → ack → NIC out) gets its own histogram with a
   shared, hardware-synced clock (PTP), so a regression is attributable to
   one component, not a mystery.
4. **Design for the 99.9th percentile, not the average.** Averages hide
   GC pauses, page faults, and lock contention. The number that determines
   whether a market maker keeps quoting on your venue is the tail.
5. **Backpressure is a first-class design decision, not an afterthought.**
   Every queue in the system has a bounded size and an explicit policy for
   what happens when it's full (reject, drop-oldest, shed-load) — an
   unbounded queue is a memory leak with extra latency attached.
6. **Scale the paths that tolerate horizontal scale-out; do not try to
   scale the one that doesn't.** The matching engine scales by *sharding
   per instrument*, not by adding replicas of a shared-state engine.

---

## 2. System Topology (Bird's-Eye View)

```
                         ┌─────────────────────────────────────────┐
                         │              CLIENTS                     │
                         │  Retail Web/Mobile │ Pro Terminal │ Algo  │
                         │      (Next.js)     │   (Tauri)    │ (FIX) │
                         └───────┬────────────────┬──────────┬──────┘
                                 │ REST/WS         │ WS/gRPC  │ FIX/binary
                          ┌──────▼────────────────▼──────────▼──────┐
                          │      API GATEWAY / EDGE (Go)             │
                          │  authn, rate limit, session, protocol    │
                          │  translation → internal binary protocol  │
                          └───────────────┬───────────────────────────┘
                                          │  (SBE / FlatBuffers, not JSON)
                          ┌───────────────▼───────────────────────────┐
                          │        OMS / RISK GATEWAY (Go)             │
                          │  pre-trade risk (sub-ms, in-memory limits) │
                          │  order validation, sequencing assignment   │
                          └───────────────┬───────────────────────────┘
                                          │ ring buffer (SPSC/MPSC)
                          ┌───────────────▼───────────────────────────┐
                          │   MATCHING ENGINE CLUSTER (Rust/C++)       │
                          │   sharded by instrument, single-thread     │
                          │   core per shard, lock-free ingress/egress │
                          │   WAL (event sourcing) for crash recovery  │
                          └───────┬───────────────────────┬───────────┘
                                  │ trade events           │ book deltas
                    ┌─────────────▼───────┐   ┌────────────▼─────────────┐
                    │  LEDGER / SETTLEMENT │   │  MARKET DATA PUBLISHER    │
                    │  (Go + Postgres,     │   │  (Rust) → Kafka/Redpanda  │
                    │  off hot path, async)│   │  → WS fan-out fleet       │
                    └───────────────────────┘   └───────────────────────────┘
```

Three latency tiers, deliberately separated:

| Tier | Path | Latency budget | Language/runtime constraint |
|---|---|---|---|
| **Tier 0 — Hot** | NIC → risk check → match → ack | low-microseconds to low-tens-of-µs | No GC, no locks, no allocation. Rust/C++. |
| **Tier 1 — Warm** | Match → market data publish → client WS | single-digit ms | GC tolerated if tuned (Go's GC is fine here); no synchronous DB writes. |
| **Tier 2 — Cold** | Match → ledger → compliance/analytics | seconds acceptable | Standard web-stack languages, standard DBs, at-least-once + idempotent consumers. |

The single most important architectural decision in this document is
**never letting Tier 2 work block Tier 0**. Every incident where a
matching engine "pauses" traces back to this rule being violated —
usually a synchronous DB write or log flush inserted into the hot path by
someone who didn't know better.

---

## 3. The Matching Engine (Tier 0 — where microseconds actually live)

### 3.1 Threading model

- **Single-writer principle** (LMAX Disruptor pattern): exactly one thread
  mutates a given instrument's order book. No mutex, no compare-and-swap
  on the book itself — correctness comes from the fact that nothing else
  *can* touch it concurrently.
- Inbound orders arrive via a **lock-free SPSC/MPSC ring buffer**; the
  matching thread busy-spins (not blocks/sleeps) on the buffer when
  idle — sleeping and waking a thread costs microseconds you don't have.
  Busy-spin cores are pinned and isolated (`isolcpus`, `taskset`) so the
  OS scheduler never preempts them.
- Outbound events (fills, book deltas, acks) go out via a second ring
  buffer to separate publisher threads — the matching thread never blocks
  on I/O, ever.

### 3.2 Sharding for scale

A single matching thread tops out around a few million simple order
operations per second — that's not a scale problem for any single
instrument in practice, but you have thousands of instruments. Scale
**horizontally by instrument**, not by trying to parallelize a single
book:

- Each instrument (or small correlated cluster, e.g. an index + its
  derivatives) is pinned to one matching shard/core.
- A stateless **sequencer/router** in front of the shards routes each
  order to the correct shard by instrument ID — this router is the only
  component that needs to know the shard map, and it's trivially
  horizontally scalable itself.
- Cross-instrument operations (basket orders, spread orders) are composed
  at the OMS layer as multiple single-instrument child orders with
  all-or-nothing semantics — the matching core stays dumb and single-
  instrument by design; complexity belongs in Tier 1, not Tier 0.

### 3.3 Memory & data layout

- **Pre-allocated object pools** for order objects, price levels, and
  book nodes — sized at startup for worst-case book depth, never resized
  under load.
- **Intrusive data structures** (price-level buckets as arrays/skip-lists
  indexed by tick, not hash maps) — cache-friendly, predictable access
  pattern, no pointer-chasing through a generic hash implementation.
- **NUMA-aware allocation**: memory for a shard is allocated on the NUMA
  node local to the core it runs on; cross-NUMA memory access alone can
  cost more latency than the entire rest of the matching operation.
- No `std::string`/heap-allocated strings anywhere in the hot path —
  fixed-width binary symbol/account IDs only.

### 3.4 Determinism & recovery

- **Event sourcing, not database queries.** The engine receives a
  *sequenced* stream of binary commands (sequence number assigned once,
  by the OMS/sequencer, before the order reaches any matching shard) and
  emits a stream of events. State is 100% a function of the command
  sequence — no side channels, no wall-clock reads inside matching logic.
- **Write-Ahead Log (WAL)**, append-only, fsync'd on a separate I/O thread
  (never the matching thread). On crash, a shard restarts by replaying
  its WAL from the last verified checkpoint — this must reconstruct
  *bit-identical* book state, and that's the actual test suite for this
  component: replay the same input twice, assert identical output, every
  build.
- **Periodic snapshotting** (book state serialized to disk every N
  seconds/events) so recovery replays only the tail of the WAL, not the
  entire day.
- **Hot standby**: a shadow instance of each shard consumes the same
  sequenced command stream in parallel and stays within one event of the
  primary; on primary failure, the sequencer fails over to the standby,
  which is already caught up — this is what keeps recovery in the
  low-single-digit-seconds range instead of "replay the whole WAL."

### 3.5 Networking

- **Kernel bypass (DPDK or io_uring where DPDK is unavailable)** at the
  edge of the matching engine for institutional/co-located clients —
  packets land in application memory without a syscall or context switch.
- Internal wire format is a **fixed-layout binary encoding** (SBE — Simple
  Binary Encoding, or a hand-rolled equivalent) — zero-copy decode,
  no parsing allocation, no reflection. JSON/Protobuf are fine for Tier 1
  and Tier 2, never for Tier 0.
- External institutional access speaks **FIX** at the gateway boundary;
  the gateway translates FIX ⇄ internal binary format — FIX parsing
  itself never touches the matching thread.

---

## 4. OMS / Risk Gateway (Tier 0/1 boundary)

- Written in **Go** — not for raw speed (it's not Tier 0), but because
  the OMS's job is concurrency at scale: thousands of client sessions,
  FIX/WS connection management, and coordinating with the risk engine.
  Go's goroutine model handles this cleanly; its GC is fine here because
  this tier's budget is low-single-digit milliseconds, not microseconds.
- **Pre-trade risk checks run in-memory**, against a risk-limit cache kept
  hot and updated asynchronously from the ledger — never a synchronous
  DB round-trip in the risk-check path. If the risk cache and the ledger
  ever disagree, the cache loses (conservative: reject and reconcile,
  never allow-and-hope).
- **Sequence number assignment happens here**, once, before the order is
  routed to a matching shard — this is what makes the matching engine's
  event log strictly ordered and replayable.
- Backpressure policy: **bounded queue depth per client session**; a
  client publishing faster than the OMS can risk-check gets explicit
  `THROTTLED` responses, never silent queuing that later bursts.

---

## 5. Market Data Pipeline (Tier 1 — fan-out is the scale problem here, not latency to one client)

Matching-engine latency and market-data-to-a-million-clients latency are
different problems with different solutions:

- **Publisher (Rust)**: consumes the matching engine's outbound event
  ring buffer, builds L1/L2 deltas, and writes to **Kafka/Redpanda**
  partitioned by instrument — this is the durable, replayable backbone
  every downstream consumer (candles, WS fan-out, tick storage,
  compliance replay) reads from independently. Nothing downstream of the
  publisher can ever slow down the matching engine — the ring buffer
  hand-off is the isolation boundary.
- **WS fan-out fleet**: stateless, horizontally scaled workers, each
  owning a shard of connected clients, subscribing to the instruments
  those clients care about. Scale-out is trivial because these workers
  hold no authoritative state — only a local cache of "what my connected
  clients are subscribed to."
- **Snapshot + delta protocol with sequence numbers**: every client
  message carries a monotonic sequence number per instrument. Clients
  detect gaps and request a fresh snapshot rather than silently trading
  on stale/incomplete depth — this single design point is what prevents
  "my DOM was wrong for 400ms and I fat-fingered a bad fill" incidents.
- **Delta compression**: send only changed price levels, not the full
  book, on every update — this is what makes L2 broadcast to millions of
  WS clients bandwidth-feasible at all.
- **UDP multicast** as an optional, additional distribution channel for
  co-located institutional consumers who want kernel-bypass-speed data
  and can handle multicast/PGM-style reliability themselves; retail WS
  fan-out and institutional multicast are separate distribution trees
  fed from the same Kafka backbone, not one trying to serve both needs.

---

## 6. Storage Tiering

| Tier | Store | What lives here | Access pattern |
|---|---|---|---|
| Hot | In-process memory (matching engine, risk cache) | Live order book, live risk limits | Sub-µs, no serialization |
| Warm | ClickHouse / TimescaleDB | Tick data, OHLCV candles, recent order/trade history | High-ingest, fast range-scan queries for charting & backtests |
| Ledger | PostgreSQL | Double-entry accounting, settlement, user balances | ACID, strongly consistent, low-QPS relative to tick data |
| Cold | Object storage (S3-compatible) | Historical tick archives, WAL checkpoints, compliance archives | Batch/replay access, cheap long-term retention |

Tick data volume is the sizing driver here: a liquid index options chain
alone can produce tens of thousands of book updates per second across all
strikes. ClickHouse's columnar compression and Kafka's log-structured
storage are chosen specifically because row-store OLTP databases (i.e.
Postgres) fall over under this ingest rate — that's exactly why the
ledger is a *separate* Postgres instance handling only accounting, at a
much lower QPS, never in the tick-ingest path.

---

## 7. Deployment Topology

- **Matching engine + OMS**: bare-metal or dedicated instances (not
  shared-tenant cloud VMs) in a data center with direct exchange
  connectivity, ideally co-located with the exchange itself if you are
  ever routing DMA order flow. This tier is latency-sensitive enough that
  "which cloud AZ" stops being a good enough answer.
- **Market data fan-out + retail API**: standard multi-AZ Kubernetes,
  horizontally autoscaled — this tier's job is absorbing traffic spikes
  (market open, major news events), and cloud elasticity is exactly the
  right tool here, unlike for the matching engine.
- **Multi-region**: retail web/mobile and the API gateway are
  multi-region for user-facing latency and availability. The matching
  engine is **single active region** with a **hot-standby DR region** —
  running an active-active matching engine across regions means
  reconciling two independently-sequenced order books, which reintroduces
  every consistency problem sharding-by-instrument was designed to avoid.
  Don't do it; a well-drilled failover to a caught-up standby is safer
  than active-active split state.
- **Clock synchronization**: PTP (Precision Time Protocol), not NTP,
  across every Tier 0/1 host — sub-microsecond clock sync is a
  prerequisite for the per-hop latency histograms in §9 to mean anything,
  and for cross-venue timestamp comparison during compliance investigations.

---

## 8. Tech Stack Decisions (with the "why," not just the "what")

| Service | Language | Why |
|---|---|---|
| Matching engine | Rust (preferred) or C++ | No GC pauses; manual memory control; Rust adds memory-safety guarantees at zero runtime cost, reducing the class of bugs that cause a matching-thread crash in production |
| OMS / Risk Gateway | Go | Best-in-class concurrency for thousands of sessions; GC pauses are acceptable at this tier's ms-level budget; fast to hire for and iterate on |
| Market data publisher/consumers | Rust | Sits close enough to the matching engine's output that GC jitter would show up as client-visible latency spikes |
| API Gateway / BFF | Go | Same reasoning as OMS; also owns protocol translation and auth, which is I/O-bound, not compute-bound |
| Quant engine (pricing/GARCH/VaR) | Python (NumPy/SciPy) for research; hot paths (Greeks, IV solve) ported to Rust and exposed via PyO3/gRPC once proven | Python for research velocity and quant-team familiarity; Rust port only for paths actually on a latency budget (e.g. real-time arbitrage scanner) |
| Ledger / Settlement | Go + PostgreSQL | Correctness and ACID guarantees matter more than raw speed here; standard, well-understood operational story |
| Backtester / strategy runtime | Python, sandboxed subprocess per strategy | Matches the language quants already write strategies in; isolation via resource-capped subprocess or microVM (Firecracker), never in-process with the live OMS |
| Terminal frontend | React + Tauri (Rust shell) | Rust shell gives native multi-window + hardware-accelerated rendering without an Electron-sized memory footprint |
| Web/mobile retail app | Next.js / React Native | Standard, fast-iterating, GC-tolerant — this tier's latency budget is human-perception-level (tens/hundreds of ms), not µs |
| Inter-service messaging (Tier 0→1 handoff) | Aeron or raw lock-free ring buffers | Purpose-built for this exact problem — reliable UDP-based messaging with µs-level latency, used by real exchanges (LMAX) |
| Event backbone (Tier 1→2) | Kafka / Redpanda | Durable, replayable, partition-parallel — decouples every downstream consumer from the matching engine's timing |
| Tick storage | ClickHouse | Columnar, extreme ingest rate, sub-second range queries over billions of rows |

---

## 9. Observability Built for Latency Debugging, Not Just Uptime

Standard "is it up" monitoring is necessary but insufficient here. Add:

- **Per-hop latency histograms** (not averages) at every Tier 0/1
  boundary: NIC-in → risk-check-done → matched → ack-sent → published →
  client-received. Each hop tagged with a PTP-synced timestamp so a p99.9
  regression can be attributed to a specific hop, not guessed at.
- **HDR histograms** (not bucketed Prometheus histograms with lossy
  buckets) for anything on the hot path — tail latency is exactly where
  bucket-boundary error matters most.
- **Sequence-gap monitoring** on every ring buffer and Kafka partition —
  a growing gap between producer and consumer sequence numbers is the
  earliest signal of a downstream consumer falling behind, well before
  it becomes a client-visible incident.
- **GC pause tracking** on every Go/Python service on Tier 1 — a GC pause
  distribution shift is often the actual root cause behind a vague
  "latency crept up" report.
- **Chaos/load testing as a standing practice**, not a one-time
  pre-launch exercise: inject packet loss, kill a matching shard mid-day
  in staging, saturate a WS fan-out node — verify the failover/backpressure
  behavior described in §§3–5 actually holds under load, on a schedule.

---

## 10. Explicit Anti-Patterns (things that look reasonable and will quietly destroy your latency budget)

- Logging synchronously (even to stdout) from the matching thread —
  route logs through a separate lock-free queue to a dedicated logging
  thread.
- JSON anywhere on Tier 0 — even "just for this one debug field."
- A retry-with-backoff loop that can block a Tier 0 thread — retries
  belong in Tier 1/2 consumers, never inline in the matching path.
- Autoscaling the matching engine like a stateless web service — it
  doesn't scale that way; scale by sharding instruments across more
  cores/hosts, planned capacity, not reactive autoscale.
- Putting the risk check on the *other side* of the matching engine
  (match-then-check) to "simplify the code path" — pre-trade risk must
  gate order entry before the matching thread ever sees the order, full
  stop; this is a regulatory requirement in most markets, not a style
  preference.
- Treating NTP-synced clocks as good enough for cross-host latency
  attribution — they're not; use PTP on anything you intend to build
  per-hop histograms from.

---

## 11. Open Questions to Resolve Before Phase 4 (matching engine build)

- Build vs. buy for the matching engine core — is a fully custom LOB
  justified, or does the platform route to an existing exchange/venue for
  Phase 0–3 and only build a proprietary matching core if/when operating
  a dark pool or internalizer becomes the actual business model?
- Co-location strategy and vendor (which data center, which exchange
  connectivity provider) — this is a commercial/legal decision as much as
  a technical one, and should be scoped once Phase 2 volumes are known.
- Target p99.9 order-to-ack latency — needs to be set as an explicit,
  numeric SLO (e.g. "<500µs p99.9 for the matching core, excluding
  network transit") before Phase 4 design starts, not discovered after
  the fact via user complaints.
