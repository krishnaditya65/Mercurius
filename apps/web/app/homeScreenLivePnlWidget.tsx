"use client";

// Mercurius / web — Home-screen live P&L widget (FEATURES.md §21).
// Polls market-data's real `GET /pnl/live` endpoint
// (services/market-data/src/livePnlWidget.rs), which itself combines
// oms-gateway's real cost-basis data (average entry price, over a
// read-only HTTP call to oms-gateway's mark-to-market engine) with
// market-data's OWN real trade-tick price — see that module's doc for the
// full contract and its one documented upstream limitation (oms-gateway
// only returns cost-basis data for accounts it considers "leveraged").
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useCallback, useEffect, useState } from "react";
import { useSequencedFetch } from "./hooks/useSequencedFetch";

const marketDataBaseUrl = process.env.NEXT_PUBLIC_MARKET_DATA_BASE_URL ?? "http://localhost:9103";

const PNL_WIDGET_POLL_INTERVAL_MILLISECONDS = 7_000;

type LivePnlPositionSnapshot = {
  instrumentSymbol: string;
  netQuantity: number;
  averageEntryPriceInMinorUnits: number;
  currentMarketPriceInMinorUnits: number;
  unrealizedPnLInMinorUnits: number;
  currentMarketPriceIsLive: boolean;
};

type LivePnlSnapshot = {
  accountIdentifier: string;
  totalUnrealizedPnLInMinorUnits: number;
  positions: LivePnlPositionSnapshot[];
};

function formatMinorUnitsAsCurrency(minorUnits: number): string {
  // "Minor units" throughout this codebase's Rust/Go services are plain
  // integer price ticks, not necessarily cents of a specific currency —
  // this widget just renders the raw signed integer with a +/- sign and
  // thousands separators rather than fabricating a currency symbol/scale
  // no upstream service defines.
  const sign = minorUnits > 0 ? "+" : minorUnits < 0 ? "−" : "";
  return `${sign}${Math.abs(minorUnits).toLocaleString("en-US")}`;
}

export default function HomeScreenLivePnlWidget() {
  const [accountIdentifier, setAccountIdentifier] = useState("acct-001");
  const [snapshot, setSnapshot] = useState<LivePnlSnapshot | null>(null);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [isAutoRefreshing, setIsAutoRefreshing] = useState(true);
  const startNextPnlRequest = useSequencedFetch();

  const refreshPnl = useCallback(async () => {
    // Financially sensitive display — guard against a stale, slower
    // response (e.g. for the PREVIOUS account identifier) resolving after
    // a newer request already rendered and showing the wrong account's P&L.
    const isStillMostRecentRequest = startNextPnlRequest();
    try {
      const httpResponse = await fetch(
        `${marketDataBaseUrl}/pnl/live?accountIdentifier=${encodeURIComponent(accountIdentifier)}`
      );
      if (!httpResponse.ok) {
        const parsedError = await httpResponse.json().catch(() => null);
        throw new Error(parsedError?.errorMessage ?? `HTTP ${httpResponse.status}`);
      }
      const parsed: LivePnlSnapshot = await httpResponse.json();
      if (!isStillMostRecentRequest()) return;
      setSnapshot(parsed);
      setErrorMessage(null);
    } catch (thrownError) {
      if (!isStillMostRecentRequest()) return;
      setErrorMessage(
        thrownError instanceof Error
          ? `Couldn't compute live P&L: ${thrownError.message}. Is market-data running on ${marketDataBaseUrl} (and oms-gateway reachable from it)?`
          : "Unknown error fetching live P&L."
      );
    }
  }, [accountIdentifier, startNextPnlRequest]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    refreshPnl();
    if (!isAutoRefreshing) return;
    const intervalId = setInterval(refreshPnl, PNL_WIDGET_POLL_INTERVAL_MILLISECONDS);
    return () => clearInterval(intervalId);
  }, [refreshPnl, isAutoRefreshing]);

  const totalIsGain = (snapshot?.totalUnrealizedPnLInMinorUnits ?? 0) >= 0;

  return (
    <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
      <div className="flex items-center justify-between">
        <h2 className="text-lg font-medium">Live P&amp;L</h2>
        <div className="flex items-center gap-2 text-xs">
          <label className="flex items-center gap-1">
            <input
              type="checkbox"
              checked={isAutoRefreshing}
              onChange={(changeEvent) => setIsAutoRefreshing(changeEvent.target.checked)}
            />
            Auto-refresh every {PNL_WIDGET_POLL_INTERVAL_MILLISECONDS / 1000}s
          </label>
          <button className="underline" onClick={refreshPnl} type="button">
            Refresh now
          </button>
        </div>
      </div>

      <label className="flex items-center gap-2 text-sm">
        Account
        <input
          className="rounded border px-2 py-1 text-sm"
          value={accountIdentifier}
          onChange={(changeEvent) => setAccountIdentifier(changeEvent.target.value)}
        />
      </label>

      {errorMessage && <p className="text-sm text-red-600">{errorMessage}</p>}

      {snapshot && (
        <>
          <p className={`text-3xl font-semibold ${totalIsGain ? "text-green-600" : "text-red-600"}`}>
            {formatMinorUnitsAsCurrency(snapshot.totalUnrealizedPnLInMinorUnits)}
          </p>
          {snapshot.positions.length === 0 ? (
            <p className="text-sm text-neutral-500">
              No positions with live cost-basis data for this account. (oms-gateway only reports cost basis for
              accounts it treats as leveraged — margin-funded or pledge-backed — see
              services/market-data/src/livePnlWidget.rs for the full limitation.)
            </p>
          ) : (
            <ul className="flex flex-col gap-1 text-sm">
              {snapshot.positions.map((position) => (
                <li key={position.instrumentSymbol} className="flex items-center justify-between rounded border px-3 py-2">
                  <span>
                    {position.instrumentSymbol} × {position.netQuantity}
                    {!position.currentMarketPriceIsLive && (
                      <span className="ml-1 text-xs text-amber-600">(no live price yet)</span>
                    )}
                  </span>
                  <span className={position.unrealizedPnLInMinorUnits >= 0 ? "text-green-600" : "text-red-600"}>
                    {formatMinorUnitsAsCurrency(position.unrealizedPnLInMinorUnits)}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </section>
  );
}
