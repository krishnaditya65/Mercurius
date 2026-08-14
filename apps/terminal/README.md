# terminal (Pro Desktop Terminal)

**Status: built.** A real Tauri v2 + React 19 + TypeScript desktop app
implementing FEATURES.md §10 "The Terminal (Pro Desktop)" end to end. This
was previously left as a deliberate stub pending apps/web having reusable
components (see git history / the old version of this file) — that
sequencing concern was resolved by duplicating the small pieces of logic
this app needs (order-submission request shape, candlestick rendering
approach) from apps/web rather than a cross-package refactor, per this
build's task brief. apps/web was read but never modified.

## What's real vs. illustrative — read this first

Everything below is genuinely implemented and tested unless explicitly
flagged otherwise. Two things are honestly NOT real, and are labeled as
such in the UI/code, not silently passed off as real:

- **News/sentiment ticker data** (`src/newsTicker/illustrativeNewsFeed.ts`)
  is hand-written fixture data, not a live feed — no service in this
  monorepo exposes a real news/headlines HTTP endpoint. The sentiment
  classification reuses the same toy lexicon-scoring approach as
  `services/quant-engine/src/quantengine/illustrativeSentimentTradingHook.py`
  (deliberately, for consistency), which is itself explicitly documented
  as a placeholder, not real NLP. The widget shows an "ILLUSTRATIVE DATA"
  badge at runtime.
- **Multi-monitor window detachment** cannot be visually verified in this
  sandboxed dev environment — there's no attached display server here.
  What IS real and tested: `buildDetachedTileWindowOptions`/
  `buildDetachedWindowLabel` (pure config-building logic, unit-tested) and
  `launchDetachedTileWindow`, which makes the actual, real
  `new WebviewWindow(...)` call from `@tauri-apps/api/webviewWindow` — the
  exact call a running desktop build makes. See
  `src/windowDetachment/detachedTileWindowLauncher.ts`'s header comment.

Also worth knowing: the Python hook sandbox's memory cap (`RLIMIT_AS`) is
a real syscall but is not reliably enforced by macOS's kernel against
malloc-heavy Python workloads (a documented Darwin quirk, verified while
building this — see `src-tauri/src/pythonHookSandbox.rs`'s header
comment). The CPU-time cap (`RLIMIT_CPU`) IS reliably enforced and is
verified by a real integration test that kills a genuine busy-loop
subprocess via the kernel's SIGXCPU. Network isolation is attempted via
macOS's `sandbox-exec` (real, but Apple-deprecated, macOS-only).

## The 7 FEATURES.md §10 items, and where they live

1. **GoldenLayout-based tiling workspace, saved layouts per user**
   `src/workspace/TilingWorkspace.tsx` — a real `golden-layout` (npm)
   instance; each tile's content is a genuine React root mounted into
   GoldenLayout's component-factory DOM element. Layouts persist to
   `localStorage`, keyed per `userIdentifier`
   (`src/workspace/workspaceLayoutPersistence.ts` — storage choice and
   rationale documented in that file's header comment).
2. **Command bar / hotkey system (`AAPL DES <GO>` style)**
   `src/commandBar/commandBarParser.ts` (pure grammar parser, no side
   effects) + `src/commandBar/CommandBar.tsx` (the input box, global
   `` ` `` focus hotkey, dispatch into the workspace). `GP`/`DOM`/`NEWS`
   verbs actually open a new tile in the real tiling workspace; `DES`/
   `MOD`/`BLOTTER` are recognized by the parser but not yet wired to a
   distinct tile type in this build (an honest `alert`, not a fake
   success) — see `App.tsx`'s `VERB_TO_WIDGET_TYPE` map.
3. **WebGL/Canvas candlestick charts with indicator overlays (MACD, RSI,
   BB, Fib)** `src/chart/CandlestickChartCanvas.tsx` — hand-rolled Canvas
   2D renderer (rendering-choice rationale in its header comment) that
   plots real MACD/RSI/Bollinger Bands/Fibonacci retracement values
   computed by `src/indicators/*.ts` (real formulas — Wilder's RSI,
   EMA-based MACD, population-stddev Bollinger Bands, standard Fibonacci
   ratios — each unit-tested against hand-derived exact values, not just
   "doesn't crash" smoke tests).
4. **DOM ladder widget with click-to-trade** `src/domLadder/DomLadderWidget.tsx`
   polls matching-engine's real `GET /domReplay` for current book depth
   and submits a real LIMIT order to oms-gateway's `POST /orders/submit`
   on click, using the exact same request field names as apps/web's order
   ticket (`src/domLadder/domLadderOrderSubmission.ts`).
5. **Multi-monitor window detachment (Tauri native windows)**
   `src/windowDetachment/detachedTileWindowLauncher.ts` — real Tauri v2
   `WebviewWindow` API usage; see the "honest limitations" section above.
6. **Local Python hook sandbox for algo traders (isolated subprocess,
   resource-capped)** `src-tauri/src/pythonHookSandbox.rs` (Rust/Tauri
   backend) + `src/pythonHook/PythonHookPanel.tsx` (frontend editor +
   `invoke` call). Real `setrlimit`-based CPU/memory caps, a Rust-side
   wall-clock watchdog, and (macOS) `sandbox-exec`-based network denial —
   see that Rust module's extensive header comment for exactly what's
   guaranteed vs. best-effort, and
   `src-tauri/tests/pythonHookSandboxIntegrationTest.rs` for the real
   subprocess-killing integration tests that back those claims.
7. **News/sentiment ticker widget** `src/newsTicker/NewsTickerWidget.tsx` —
   real CSS-animated scrolling marquee; data source is illustrative (see
   above).

## Running it

```bash
cd apps/terminal
npm install
npm run dev          # vite dev server only (browser tab, no Tauri backend —
                      # the Python hook panel and window-detachment tile
                      # button won't work without the Tauri runtime)
npm run tauri dev    # full Tauri app (real desktop window) — NOT run in
                      # this sandbox (no display server here; see the repo's
                      # standing rule against starting anything requiring a
                      # GUI in this environment)
```

Default backend URLs (override via Vite env vars, see
`src/workspace/widgetRegistry.tsx`):

| Service | Env var | Default |
|---|---|---|
| oms-gateway | `VITE_OMS_GATEWAY_BASE_URL` | `http://127.0.0.1:8081` |
| market-data | `VITE_MARKET_DATA_BASE_URL` | `http://127.0.0.1:9103` |
| matching-engine (DOM replay) | `VITE_MATCHING_ENGINE_DOM_REPLAY_BASE_URL` | `http://127.0.0.1:9106` |

The Vite dev server itself binds port `1420` (fixed, required by
`src-tauri/tauri.conf.json`'s `devUrl` — standard Tauri convention).

## Testing

```bash
npm run test              # Vitest — 64 tests across 7 files (command bar
                           # parser, all 4 indicator calculators, workspace
                           # layout persistence, DOM ladder order-submission
                           # request shape + fetch wiring, DOM ladder
                           # snapshot-merging, window-detachment config
                           # builders, illustrative news feed)
cd src-tauri && cargo test   # 6 Rust tests — 2 unit tests + 4 integration
                              # tests in pythonHookSandboxIntegrationTest.rs
                              # that spawn REAL python3 subprocesses and
                              # assert on real kernel-enforced resource
                              # limits (SIGXCPU from RLIMIT_CPU, the
                              # wall-clock watchdog)
```

Also verified clean before every commit to this app:

```bash
cd src-tauri
cargo fmt --check
cargo clippy --all-targets -- -D warnings
cargo build
```

## Naming convention

Long, descriptive camelCase identifiers throughout — including in the Rust
`src-tauri/` code, per project convention (see the
mercurius-naming-convention memory). This overrides Rust's normal
snake_case idiom; `#![allow(non_snake_case)]` is set crate-wide in
`src-tauri/src/lib.rs`, matching `services/matching-engine`'s precedent for
the same convention.

## Known gaps / not built in this pass

- `DES`/`MOD`/`BLOTTER` command bar verbs parse correctly but aren't wired
  to a distinct workspace tile yet (see item 2 above).
- No authentication/multi-user account switching in this app — the
  workspace layout persistence key and DOM ladder's `clientAccountIdentifier`
  are both hard-coded to `acct-001` (matching apps/web's own demo-account
  convention).
- No `libs/ui` shared package between apps/web and apps/terminal — small
  pieces of logic (order-submission request shape, candlestick math) were
  deliberately duplicated per this task's instructions rather than
  refactoring apps/web.
- App icons are Tauri's default scaffold icons — no real Mercurius brand
  icon set was designed for this pass.
