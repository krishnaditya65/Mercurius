import { describe, expect, it } from "vitest";
import {
  buildDetachedTileWindowOptions,
  buildDetachedWindowLabel,
} from "./detachedTileWindowLauncher";

// These test the PURE config-building logic only — see the module doc
// comment in detachedTileWindowLauncher.ts for why actually invoking
// `new WebviewWindow(...)` and observing a real OS window cannot be
// exercised in this headless sandbox (no display server, no Tauri
// runtime). This is the honest boundary of what's testable here.

describe("buildDetachedWindowLabel", () => {
  it("produces a stable, deterministic label for the same tileId", () => {
    expect(buildDetachedWindowLabel("chart-1")).toBe(buildDetachedWindowLabel("chart-1"));
  });

  it("sanitizes characters outside Tauri's allowed label charset", () => {
    const label = buildDetachedWindowLabel("chart:AAPL/2026");
    expect(label).toMatch(/^detached-tile-[a-zA-Z0-9_-]+$/);
  });

  it("produces distinct labels for distinct tileIds", () => {
    expect(buildDetachedWindowLabel("chart-1")).not.toBe(buildDetachedWindowLabel("chart-2"));
  });
});

describe("buildDetachedTileWindowOptions", () => {
  it("includes the instrument symbol in the title when provided", () => {
    const options = buildDetachedTileWindowOptions({
      tileId: "chart-1",
      tileKind: "chart",
      instrumentSymbol: "AAPL",
      detachedTileRoute: "/?detachedTile=chart-1",
    });
    expect(options.title).toContain("AAPL");
    expect(options.title).toContain("Chart");
  });

  it("omits the instrument suffix when none is provided", () => {
    const options = buildDetachedTileWindowOptions({
      tileId: "news-1",
      tileKind: "newsTicker",
      detachedTileRoute: "/?detachedTile=news-1",
    });
    expect(options.title).toBe("Mercurius Terminal: News Ticker");
  });

  it("points the detached window at the requested route", () => {
    const options = buildDetachedTileWindowOptions({
      tileId: "dom-1",
      tileKind: "domLadder",
      detachedTileRoute: "/?detachedTile=dom-1",
    });
    expect(options.url).toBe("/?detachedTile=dom-1");
  });

  it("sets sane, resizable default dimensions", () => {
    const options = buildDetachedTileWindowOptions({
      tileId: "watch-1",
      tileKind: "watchlist",
      detachedTileRoute: "/?detachedTile=watch-1",
    });
    expect(options.resizable).toBe(true);
    expect(options.width).toBeGreaterThan(0);
    expect(options.height).toBeGreaterThan(0);
  });
});
