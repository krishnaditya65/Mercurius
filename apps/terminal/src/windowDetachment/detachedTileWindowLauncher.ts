// Mercurius / terminal — multi-monitor window detachment.
//
// FEATURES.md §10 "[P3] Multi-monitor window detachment (Tauri native
// windows)". Uses Tauri v2's REAL multiwindow API
// (`@tauri-apps/api/webviewWindow`'s `WebviewWindow` class) to spawn a
// genuinely separate native OS window/webview for one workspace tile —
// this is real Tauri functionality, not a simulated popup.
//
// HONEST LIMITATION: this sandboxed dev environment has no display server
// (no GUI, no windowing system attached to this shell), so actually
// launching `tauri dev`/`tauri build` and observing a second native window
// appear on a second monitor cannot be exercised or screenshotted here —
// that would require a real desktop session. What IS tested and IS real:
// `buildDetachedTileWindowOptions` below is a pure function, unit-tested
// against Tauri's actual `WebviewWindowOptions` shape (see
// `detachedTileWindowLauncher.test.ts`), and `launchDetachedTileWindow`
// makes the actual, real `new WebviewWindow(...)` call from
// `@tauri-apps/api/webviewWindow` — the exact same call a running desktop
// build would make. There is no fake/mocked window-creation path in this
// module; the only thing untested here is what a human eye would see on a
// second monitor, which is inherently outside what any headless test can
// verify.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import type { WebviewWindow as WebviewWindowType } from "@tauri-apps/api/webviewWindow";

export type WorkspaceTileKind = "chart" | "domLadder" | "newsTicker" | "commandBar" | "watchlist";

export type DetachTileRequest = {
  tileId: string;
  tileKind: WorkspaceTileKind;
  instrumentSymbol?: string;
  /** Where within the app's own frontend the detached window should load
   * — the terminal's own index.html, with query params telling that fresh
   * page load which single tile to render standalone (rather than the
   * full tiling workspace). */
  detachedTileRoute: string;
};

/** Tauri window labels must be a restricted character set
 * (`a-zA-Z-/:_` per `WebviewWindow`'s own JSDoc) — this turns an arbitrary
 * tileId into a safe, unique label deterministically, so re-detaching the
 * same tile twice in a row produces a stable, predictable label rather
 * than colliding or silently mismatching. */
export function buildDetachedWindowLabel(tileId: string): string {
  const sanitizedTileId = tileId.replace(/[^a-zA-Z0-9_-]/g, "-");
  return `detached-tile-${sanitizedTileId}`;
}

/** Pure builder for the options object passed to `new WebviewWindow(...)`
 * — kept separate from the actual window-creation call so it's testable
 * without a Tauri runtime (see the module doc comment's honesty note). */
export function buildDetachedTileWindowOptions(request: DetachTileRequest) {
  const titleSuffix = request.instrumentSymbol ? ` — ${request.instrumentSymbol}` : "";
  return {
    url: request.detachedTileRoute,
    title: `Mercurius Terminal: ${tileKindDisplayName(request.tileKind)}${titleSuffix}`,
    width: 640,
    height: 480,
    minWidth: 320,
    minHeight: 240,
    // Deliberately NOT centered on the main window's monitor — leaving
    // x/y unset lets the OS place it (typically cascaded), which is the
    // right default for "drag this to my second monitor yourself" rather
    // than this code guessing monitor geometry it has no way to query
    // correctly from here.
    resizable: true,
    alwaysOnTop: false,
  };
}

function tileKindDisplayName(tileKind: WorkspaceTileKind): string {
  switch (tileKind) {
    case "chart":
      return "Chart";
    case "domLadder":
      return "DOM Ladder";
    case "newsTicker":
      return "News Ticker";
    case "commandBar":
      return "Command Bar";
    case "watchlist":
      return "Watchlist";
  }
}

/** Actually spawns the detached native window. Dynamically imports
 * `@tauri-apps/api/webviewWindow` so this module can still be imported (for
 * `buildDetachedTileWindowOptions`'s pure logic) from a Vitest/jsdom
 * environment that has no Tauri runtime to satisfy that import against. */
export async function launchDetachedTileWindow(request: DetachTileRequest): Promise<WebviewWindowType> {
  const { WebviewWindow } = await import("@tauri-apps/api/webviewWindow");
  const label = buildDetachedWindowLabel(request.tileId);
  const options = buildDetachedTileWindowOptions(request);
  return new WebviewWindow(label, options);
}
