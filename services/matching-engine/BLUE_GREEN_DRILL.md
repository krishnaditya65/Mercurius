# Blue/Green Deploy Drill — matching-engine

Companion to `FEATURES.md` §13's `[P3]` item: "Blue/green or canary
deploys for the matching engine specifically."

## TL;DR

`scripts/blueGreenDeployDrill.py` starts two REAL `matching_engine`
subprocesses ("blue" and "green"), sends blue real orders over the real
TCP wire protocol, copies blue's WAL to green, lets green recover from it
via the existing `WalBackedOrderBook::openRecoveringIfPresent` replay
path, then — with both instances live at the same time — queries every
order's status on both over the wire and diffs the raw JSON responses,
plus an independent structural diff via the existing `--replay` CLI tool
and a WAL-file sha256 comparison. Every check passed on the run this
document was written from (see "Sample run output" below).

**What this proves:** the core primitive a blue/green deploy for a
*stateful* matching engine needs before anything else about the deploy
can be trusted — that a second instance, recovered purely from the
first's write-ahead log, reaches **bit-identical** book state.

**What this does NOT prove, and does not attempt to:** any actual traffic
cutover. See "What's out of scope" below.

## Why this is the right scope for this repo

`ARCHITECTURE.md` §7 is explicit that matching-engine runs on bare-metal
/ dedicated instances, not an autoscaled Kubernetes tier — there is no
load balancer, no service mesh, no health-check-gated router, and no
k8s/cloud infra for this service anywhere in this repo. A blue/green
*deploy pipeline* (traffic shifting, health gates, automated rollback)
genuinely cannot be built honestly here — there's no infrastructure layer
underneath it to drive.

What CAN be built honestly, and is the part that actually matters most
for a stateful single-writer order book (`ARCHITECTURE.md` §1 tenet 1,
§3.1), is proving that a second instance recovered from the first's WAL
is byte-for-byte, order-for-order identical before any cutover
infrastructure would ever be allowed to flip traffic to it. That's what
`blueGreenDeployDrill.py` does, for real, against two real running
processes — not asserted from memory, not simulated in-process.

## What already existed vs. what this drill adds

Nothing new was invented for the "does replay actually reproduce
identical state" question — that mechanism already existed and is
already tested:

- `src/writeAheadLog.rs::replayWalEventRecordsIntoFreshOrderBook` — reads
  WAL event records, replays them into a fresh `OrderBookCore`.
- `src/walBackedOrderBook.rs::WalBackedOrderBook::openRecoveringIfPresent`
  — the real startup path (`main.rs` calls this unconditionally), which
  calls the function above when an existing WAL file is found.
- `src/deterministicReplayHarness.rs::assertDeterministicReplayMatchesLiveBook`
  — the crate's own reusable test harness: runs a scripted sequence
  through a live `WalBackedOrderBook`, replays the WAL it produced into a
  fresh book, and `assert_eq!`s the two `fullBookStateSnapshotForTesting`
  results. Exercised in-process by `cargo test` across 5 hand-written
  scenarios and 12 pseudo-random generated sequences (52 tests total in
  this crate, all passing — see "cargo test" below).
- `main.rs`'s `--replay <walFile>` mode (`runReplayModeAndExit`) — an
  existing offline CLI tool that runs that same replay function and
  prints the reconstructed book depth + pending-stop count.

What this drill adds is driving that SAME mechanism from the outside,
against two real subprocesses talking real TCP, instead of in-process
assertions:

- `scripts/blueGreenDeployDrill.py` (new) — the drill itself.
- `src/main.rs` (small additive change, see below) — the TCP listen
  address was a hardcoded constant with no override, which meant a
  second instance could never run side-by-side with the first on one
  host. Added an env var override
  (`MATCHING_ENGINE_TCP_LISTEN_ADDRESS`), mirroring the WAL file path's
  existing `MATCHING_ENGINE_WAL_FILE_PATH` override pattern exactly. No
  other behavior changed. Full existing test suite re-run after this
  change — see below.

## Running it

```bash
cd services/matching-engine
python3 scripts/blueGreenDeployDrill.py
```

No arguments, no dependencies beyond the Python 3 standard library and a
working Rust toolchain (it runs `cargo build --release` itself). Exits 0
if every parity check passed, 1 otherwise. Always kills both subprocesses
in a `finally` block, success or failure. WAL files and subprocess
stdout/stderr logs from each run are left under
`scripts/.blueGreenDrillRuns/run-<timestamp>-<random>/` for inspection —
safe to delete between runs.

## What it does, step by step

1. **`cargo build --release`.**
2. **Starts "blue"** — a real `matching_engine` subprocess, listening on
   `127.0.0.1:9101`, with its own WAL file in the run's working
   directory.
3. **Sends blue a real sequence of orders over the real wire protocol**
   (`src/wireProtocol.rs`'s JSON-over-TCP, one connection per request,
   exactly like `oms-gateway` talks to it): resting limit orders on both
   sides, a crossing partial fill, a cancel, a stop-loss order that arms
   and later triggers off a real trade. Real WAL-durable state, built
   from real network traffic, not constructed in-process.
4. **Copies blue's WAL file byte-for-byte** to a fresh path for green.
   Verifies the copy's sha256 matches the source's.
5. **Starts "green"** — a second real `matching_engine` subprocess,
   listening on `127.0.0.1:9201`, pointed at the copied WAL. On startup
   it goes through the exact same `openRecoveringIfPresent` →
   `replayWalEventRecordsIntoFreshOrderBook` path described above. Its
   own stderr recovery log line is captured and printed.
6. **With both instances live at once**, queries every order's status
   (`queryOrderStatusSequenceNumber`) on BOTH blue and green over the
   real wire protocol and diffs the raw JSON responses byte-for-byte.
   This is the state-parity proof — obtained the same way any real
   client would observe book state, not by reading internal Rust
   structs.
7. **A second, independent structural check** that reuses the existing
   `--replay` CLI tool (rather than inventing a new comparator): runs
   `matching_engine --replay <walFile>` against both WAL files and diffs
   the printed book-depth/pending-stop-count output.
8. **Proves green is genuinely live**, not a static replay dump: after
   every comparison above, sends ONE new order to green only and shows
   it's accepted and matched correctly against the recovered book.
9. **Prints an explicit "traffic cutover" section** — documented only,
   see below.
10. **Kills both subprocesses.**

## Sample run output (real, from this environment)

```
STEP 3 — copy BLUE's WAL byte-for-byte to a fresh path for GREEN
  blue  WAL sha256: 34ecaa98252901472c948d218a6e7b223494c0e0f91a3ae27836eeb23e050638
  green WAL sha256: 34ecaa98252901472c948d218a6e7b223494c0e0f91a3ae27836eeb23e050638
  MATCH — green's starting WAL is byte-identical to blue's.

STEP 4 — start GREEN, recovering from the copied WAL via openRecoveringIfPresent
  green's own recovery log line: walBackedOrderBook: recovered 10 event(s) from ...

STEP 5 — BOTH instances live: query every order's status on BLUE and GREEN, diff
  [OK  ] order 1: blue={..., 'orderStatus': 'RESTING_LIMIT', ..., 'orderStatusQuantity': 6}
                        green={..., 'orderStatus': 'RESTING_LIMIT', ..., 'orderStatusQuantity': 6}
  ... (7 orders total)
  MATCH — all 7 order-status query responses are byte-identical between blue and green.

STEP 6 — independent structural check via the existing --replay tool
  --- order book depth for DEMO-EQ ---     (blue)      --- order book depth for DEMO-EQ ---   (green)
  BIDS (best first):                                   BIDS (best first):
  ASKS (best first):                                   ASKS (best first):
         100 x 6                                              100 x 6
         105 x 5                                               105 x 5
  MATCH — standalone replay of blue's WAL and green's WAL produce identical output.

STEP 7 — GREEN accepts its own next order post-recovery:
  {'tradeExecutionEvents': [{'buyingClientAccountId': 'postCutoverTrader',
    'sellingClientAccountId': 'sellerA', 'executedPriceInMinorUnits': 100,
    'executedQuantity': 1}], 'errorMessage': None, 'assignedOrderSequenceNumber': 8}

DRILL RESULT
ALL PARITY CHECKS PASSED (0 failures).
```

Both spawned processes were confirmed killed after the run (`lsof -i
:9101 -i :9201` and `ps aux | grep matching_engine` both empty).

## What a real cutover would do next (documented, NOT implemented here)

With state parity confirmed, a real blue/green cutover for one
matching-engine shard would:

1. Stop routing NEW inbound orders to blue at the sequencer/router
   (`ARCHITECTURE.md` §3.2) — e.g. a load-balancer/service-mesh weight
   flip, or a health-check-gated DNS/VIP move.
2. Drain blue: let any order already in flight through its ring
   buffer/socket finish and its response reach the client before blue
   stops accepting new connections.
3. Confirm green's sequence numbering picks up exactly where blue's left
   off — no gap, no duplicate. (Step 7 of the drill, above, is the
   single-instrument version of this check: green's first post-recovery
   order lands cleanly with the next sequence number.)
4. Point the router's shard map — or the "Hot standby" failover mechanism
   `ARCHITECTURE.md` §3.4 describes — at green as the new "blue" for this
   instrument/shard.
5. Keep the old blue warm for a rollback window, then retire it.

## What's out of scope (explicitly, on purpose)

There is no load balancer, no service mesh, no health-check-gated router,
and no DNS/VIP anywhere in this repo/environment to reconfigure — none of
step 1, 2, 4, or 5 above is implemented or simulated by this drill. This
drill proves the state-parity primitive those steps depend on; it does
not, and given this repo's current infrastructure, cannot, prove anything
about live traffic-shifting safety. That remains real, unbuilt work for
whenever this platform has actual deploy infrastructure (a load balancer
or router in front of the matching-engine's TCP listener) to drive.

## Files

- `services/matching-engine/scripts/blueGreenDeployDrill.py` — the drill.
- `services/matching-engine/src/main.rs` — additive-only change: env var
  override for the TCP listen address (`MATCHING_ENGINE_TCP_LISTEN_ADDRESS`),
  same pattern as the existing `MATCHING_ENGINE_WAL_FILE_PATH` override.
  No other behavior changed. `cargo build --release` and `cargo test`
  (52/52 tests) both clean after this change.
