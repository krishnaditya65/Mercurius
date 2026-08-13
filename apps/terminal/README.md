# terminal (Pro Desktop Terminal)

**Status: not yet scaffolded.** See `ARCHITECTURE.md` §10 and
`FEATURES.md` §10 for the intended design (Tauri + React shell,
GoldenLayout tiling workspace, WebGL charts, DOM ladder, command bar,
multi-monitor window detachment, local Python hook sandbox).

## Why this one is deliberately left undone in this pass

Scaffolding a real Tauri app requires the Rust target toolchain plus
platform webview dependencies, and — much more importantly — it wraps
the *same* React code as `apps/web`. Standing it up before `apps/web` has
any real shared components/state (watchlists, auth, order ticket as a
reusable component rather than a page) means either throwing that work
away or scaffolding it twice. Sequencing per `FEATURES.md` §0: this is a
Phase 2 item (arrives alongside the options chain and the rest of the Pro
Terminal feature set), not a Phase 0 scaffold item.

## To scaffold it when the time comes

```bash
npm create tauri-app@latest terminal -- --template react-ts
```

Then wire it to consume `apps/web`'s components as a shared package
(e.g. via a `libs/ui` workspace package) rather than duplicating the
order-ticket/watchlist/chart components independently.
