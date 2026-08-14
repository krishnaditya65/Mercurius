"use client";

// Mercurius / web — Cross-device watchlist sync (FEATURES.md §21's
// "Cross-device watchlist/alert sync with a home-screen live P&L
// widget"), wired against market-data's real `watchlist.rs` endpoints on
// :9103:
//
//   GET  /watchlist?accountIdentifier=...                        -> {symbols, lastModifiedAtEpochMillis}
//   POST /watchlist/add    {accountIdentifier, instrumentSymbol, deviceIdentifier?}
//   POST /watchlist/remove {accountIdentifier, instrumentSymbol, deviceIdentifier?}
//   GET  /watchlist/changes?accountIdentifier=...&sinceEpochMillis=... -> {changes, lastModifiedAtEpochMillis}
//
// DEVICE IDENTIFIER: a real per-browser-profile identifier, minted once
// with crypto.randomUUID() and persisted in this browser's own
// localStorage (`mercuriusWatchlistDeviceIdentifier`). It's genuinely
// different across two browser profiles / an incognito window (each has
// its own localStorage), which is what makes the "cross-device" story on
// this page real rather than simulated: open this page in a normal
// window and an incognito window side by side, add a symbol in one, and
// watch it appear in the other's next poll — two real, independently
// identified sessions reading/writing the same account-scoped state.
//
// SYNC FRESHNESS: "Sync since last check" below deliberately calls
// /watchlist/changes with this tab's own remembered
// `syncedAsOfEpochMillis` rather than re-fetching the whole watchlist —
// the real delta-sync mechanism the backend exists to support, not just
// "poll the full list every few seconds" (which this page ALSO does, for
// the live display, but the changes panel proves the delta path
// specifically).
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";

const marketDataBaseUrl = process.env.NEXT_PUBLIC_MARKET_DATA_BASE_URL ?? "http://localhost:9103";

const DEVICE_IDENTIFIER_LOCAL_STORAGE_KEY = "mercuriusWatchlistDeviceIdentifier";
const WATCHLIST_POLL_INTERVAL_MILLISECONDS = 5_000;

type WatchlistSnapshotResponse = {
  accountIdentifier: string;
  symbols: string[];
  lastModifiedAtEpochMillis: number;
};

type WatchlistChangeEvent = {
  epochMillis: number;
  instrumentSymbol: string;
  wasAdded: boolean;
  deviceIdentifier?: string | null;
};

type WatchlistChangesResponse = {
  accountIdentifier: string;
  sinceEpochMillis: number;
  changes: WatchlistChangeEvent[];
  lastModifiedAtEpochMillis: number;
};

/// Reads this browser's persisted device identifier, minting and storing
/// a fresh one on first visit. Genuinely per-browser-profile — a separate
/// browser profile or an incognito window has its own localStorage, so it
/// mints (and keeps) its own distinct identifier.
function readOrCreateDeviceIdentifier(): string {
  if (typeof window === "undefined") return "server";
  const existingIdentifier = window.localStorage.getItem(DEVICE_IDENTIFIER_LOCAL_STORAGE_KEY);
  if (existingIdentifier) return existingIdentifier;

  const newIdentifier =
    typeof crypto !== "undefined" && "randomUUID" in crypto
      ? `device-${crypto.randomUUID()}`
      : `device-${Date.now()}-${Math.random().toString(36).slice(2)}`;
  window.localStorage.setItem(DEVICE_IDENTIFIER_LOCAL_STORAGE_KEY, newIdentifier);
  return newIdentifier;
}

export default function WatchlistPage() {
  const [deviceIdentifier, setDeviceIdentifier] = useState<string | null>(null);
  const [accountIdentifier, setAccountIdentifier] = useState("acct-001");
  const [symbols, setSymbols] = useState<string[]>([]);
  const [lastModifiedAtEpochMillis, setLastModifiedAtEpochMillis] = useState(0);
  const [syncedAsOfEpochMillis, setSyncedAsOfEpochMillis] = useState(0);
  const [recentChanges, setRecentChanges] = useState<WatchlistChangeEvent[]>([]);
  const [newSymbolInput, setNewSymbolInput] = useState("DEMO-EQ");
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [isBusy, setIsBusy] = useState(false);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setDeviceIdentifier(readOrCreateDeviceIdentifier());
  }, []);

  const refreshFullWatchlist = useCallback(async () => {
    try {
      const httpResponse = await fetch(
        `${marketDataBaseUrl}/watchlist?accountIdentifier=${encodeURIComponent(accountIdentifier)}`
      );
      if (!httpResponse.ok) throw new Error(`HTTP ${httpResponse.status}`);
      const parsed: WatchlistSnapshotResponse = await httpResponse.json();
      setSymbols(parsed.symbols);
      setLastModifiedAtEpochMillis(parsed.lastModifiedAtEpochMillis);
      setErrorMessage(null);
    } catch (thrownError) {
      setErrorMessage(
        thrownError instanceof Error
          ? `Couldn't reach market-data: ${thrownError.message}. Is it running on ${marketDataBaseUrl}?`
          : "Unknown error fetching the watchlist."
      );
    }
  }, [accountIdentifier]);

  // Live display: polls the full watchlist on an interval — market-data's
  // HTTP query API is a deliberate polling stopgap (see its README), not
  // a real WebSocket push.
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    refreshFullWatchlist();
    const intervalId = setInterval(refreshFullWatchlist, WATCHLIST_POLL_INTERVAL_MILLISECONDS);
    return () => clearInterval(intervalId);
  }, [refreshFullWatchlist]);

  async function mutateWatchlist(path: "add" | "remove", instrumentSymbol: string) {
    if (!instrumentSymbol.trim()) return;
    setIsBusy(true);
    setErrorMessage(null);
    try {
      const httpResponse = await fetch(`${marketDataBaseUrl}/watchlist/${path}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ accountIdentifier, instrumentSymbol, deviceIdentifier }),
      });
      if (!httpResponse.ok) throw new Error(`HTTP ${httpResponse.status}`);
      await refreshFullWatchlist();
    } catch (thrownError) {
      setErrorMessage(
        thrownError instanceof Error ? thrownError.message : `Failed to ${path} ${instrumentSymbol}.`
      );
    } finally {
      setIsBusy(false);
    }
  }

  // The real delta-sync path: ask for only what changed since this tab's
  // own remembered marker, rather than re-fetching the whole list.
  async function syncSinceLastCheck() {
    setIsBusy(true);
    setErrorMessage(null);
    try {
      const httpResponse = await fetch(
        `${marketDataBaseUrl}/watchlist/changes?accountIdentifier=${encodeURIComponent(
          accountIdentifier
        )}&sinceEpochMillis=${syncedAsOfEpochMillis}`
      );
      if (!httpResponse.ok) throw new Error(`HTTP ${httpResponse.status}`);
      const parsed: WatchlistChangesResponse = await httpResponse.json();
      setRecentChanges(parsed.changes);
      setSyncedAsOfEpochMillis(parsed.lastModifiedAtEpochMillis);
      await refreshFullWatchlist();
    } catch (thrownError) {
      setErrorMessage(thrownError instanceof Error ? thrownError.message : "Failed to fetch watchlist changes.");
    } finally {
      setIsBusy(false);
    }
  }

  return (
    <main className="mx-auto flex max-w-2xl flex-col gap-8 p-8 font-sans">
      <div>
        <div className="flex items-center justify-between">
          <h1 className="text-xl font-semibold">Watchlist (cross-device sync)</h1>
          <Link href="/" className="text-sm underline">
            ← Dashboard
          </Link>
        </div>
        <p className="text-sm text-neutral-500">
          Talks directly to market-data&apos;s real <code>/watchlist</code> endpoints — see FEATURES.md §21.
        </p>
      </div>

      <section className="flex flex-col gap-2 rounded border border-neutral-200 p-4 text-sm">
        <p>
          This browser&apos;s device identifier:{" "}
          <strong className="break-all font-mono text-xs">{deviceIdentifier ?? "…"}</strong>
        </p>
        <p className="text-neutral-500">
          Persisted in this browser&apos;s own localStorage — open this page in a separate browser profile or an
          incognito window and it will mint a genuinely different identifier there. Add/remove a symbol in one
          window and watch it appear in the other on its next poll (every {WATCHLIST_POLL_INTERVAL_MILLISECONDS / 1000}
          s) — proof the sync is real, not simulated.
        </p>
      </section>

      <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
        <label className="flex flex-col gap-1 text-sm">
          Account
          <input
            className="rounded border px-3 py-2"
            value={accountIdentifier}
            onChange={(changeEvent) => setAccountIdentifier(changeEvent.target.value)}
          />
        </label>

        <div className="flex items-end gap-3">
          <label className="flex flex-1 flex-col gap-1 text-sm">
            Instrument symbol
            <input
              className="rounded border px-3 py-2"
              value={newSymbolInput}
              onChange={(changeEvent) => setNewSymbolInput(changeEvent.target.value.toUpperCase())}
            />
          </label>
          <button
            className="rounded bg-black px-4 py-2 text-sm text-white disabled:opacity-50"
            disabled={isBusy}
            onClick={() => mutateWatchlist("add", newSymbolInput)}
            type="button"
          >
            Add
          </button>
        </div>

        {errorMessage && <p className="text-sm text-red-600">{errorMessage}</p>}

        <div className="flex items-center justify-between">
          <h2 className="text-sm font-medium text-neutral-600">
            Live watchlist for <strong>{accountIdentifier}</strong>
          </h2>
          <button className="text-xs underline" onClick={refreshFullWatchlist} type="button">
            Refresh now
          </button>
        </div>

        {symbols.length === 0 ? (
          <p className="text-sm text-neutral-500">No symbols on this watchlist yet.</p>
        ) : (
          <ul className="flex flex-col gap-1 text-sm">
            {symbols.map((instrumentSymbol) => (
              <li key={instrumentSymbol} className="flex items-center justify-between rounded border px-3 py-2">
                {instrumentSymbol}
                <button
                  className="text-xs text-red-600 underline disabled:opacity-50"
                  disabled={isBusy}
                  onClick={() => mutateWatchlist("remove", instrumentSymbol)}
                  type="button"
                >
                  Remove
                </button>
              </li>
            ))}
          </ul>
        )}
        <p className="text-xs text-neutral-400">
          lastModifiedAtEpochMillis: {lastModifiedAtEpochMillis || "never modified"}
        </p>
      </section>

      <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
        <div className="flex items-center justify-between">
          <h2 className="text-lg font-medium">Sync since last check (real delta query)</h2>
          <button
            className="rounded border px-3 py-2 text-sm disabled:opacity-50"
            disabled={isBusy}
            onClick={syncSinceLastCheck}
            type="button"
          >
            Check what changed
          </button>
        </div>
        <p className="text-xs text-neutral-500">
          Calls <code>GET /watchlist/changes?sinceEpochMillis={syncedAsOfEpochMillis}</code> — only what changed
          after this tab&apos;s own last-synced marker, not the whole watchlist.
        </p>
        {recentChanges.length === 0 ? (
          <p className="text-sm text-neutral-500">No changes reported since the last check.</p>
        ) : (
          <ul className="flex flex-col gap-1 text-sm">
            {recentChanges.map((changeEvent, changeIndex) => (
              <li key={changeIndex} className="rounded bg-neutral-50 px-3 py-2">
                {changeEvent.wasAdded ? "+ " : "− "}
                {changeEvent.instrumentSymbol}
                {changeEvent.deviceIdentifier && (
                  <span className="text-xs text-neutral-500"> (from {changeEvent.deviceIdentifier})</span>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>
    </main>
  );
}
