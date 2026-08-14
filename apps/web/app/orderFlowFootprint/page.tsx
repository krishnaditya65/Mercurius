"use client";

// Mercurius / web — Order-flow footprint view.
//
// FEATURES.md §20 "[P3] Order-flow footprint charts (bid/ask volume per
// price per candle)". Calls market-data's real `GET /orderFlowFootprint`
// endpoint (services/market-data/src/orderFlowFootprintAggregator.rs +
// httpQueryServer.rs), which computes real buy-volume-vs-sell-volume per
// price level within each candle interval, using the REAL aggressor-side
// flag captured all the way from matching-engine's
// `TradeExecutionEvent::isBuyAggressor` — not a synthetic split. Renders
// one grid per candle: rows are price levels, columns are buy volume /
// sell volume, exactly the "buy x sell" cell a real footprint chart
// shows.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useState } from "react";
import Link from "next/link";

const marketDataBaseUrl = process.env.NEXT_PUBLIC_MARKET_DATA_BASE_URL ?? "http://localhost:9103";

type FootprintPriceLevel = {
  priceBucketStart: number;
  buyVolume: number;
  sellVolume: number;
};

type CandleFootprint = {
  candleStartEpochSeconds: number;
  levels: FootprintPriceLevel[];
  totalBuyVolume: number;
  totalSellVolume: number;
};

export default function OrderFlowFootprintPage() {
  const [instrumentSymbol, setInstrumentSymbol] = useState("DEMO-EQ");
  const [priceBucketSizeInput, setPriceBucketSizeInput] = useState("100");
  const [candleIntervalSecondsInput, setCandleIntervalSecondsInput] = useState("60");
  const [startEpochSecondsInput, setStartEpochSecondsInput] = useState("");
  const [endEpochSecondsInput, setEndEpochSecondsInput] = useState("");
  const [footprints, setFootprints] = useState<CandleFootprint[] | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  async function fetchFootprint() {
    setIsLoading(true);
    setErrorMessage(null);
    setFootprints(null);
    try {
      const queryParams = new URLSearchParams({
        instrumentSymbol,
        priceBucketSizeInMinorUnits: priceBucketSizeInput,
        candleIntervalSeconds: candleIntervalSecondsInput,
      });
      if (startEpochSecondsInput) queryParams.set("startEpochSeconds", startEpochSecondsInput);
      if (endEpochSecondsInput) queryParams.set("endEpochSeconds", endEpochSecondsInput);

      const httpResponse = await fetch(`${marketDataBaseUrl}/orderFlowFootprint?${queryParams.toString()}`);
      if (!httpResponse.ok) {
        const bodyText = await httpResponse.text();
        throw new Error(`market-data responded with HTTP ${httpResponse.status}: ${bodyText}`);
      }
      const parsed: CandleFootprint[] = await httpResponse.json();
      setFootprints(parsed);
    } catch (thrownError) {
      setErrorMessage(
        thrownError instanceof Error
          ? `Couldn't fetch the order-flow footprint: ${thrownError.message}. Is market-data running on ${marketDataBaseUrl}?`
          : "Unknown error fetching order-flow footprint."
      );
    } finally {
      setIsLoading(false);
    }
  }

  return (
    <main className="mx-auto flex max-w-4xl flex-col gap-6 p-8 font-sans">
      <div>
        <Link href="/" className="text-sm text-neutral-500 underline">
          ← Back to dashboard
        </Link>
        <h1 className="text-xl font-semibold">Order-flow footprint</h1>
        <p className="text-sm text-neutral-500">
          Real buy-vs-sell volume per price level per candle, computed by market-data&apos;s{" "}
          <code>GET /orderFlowFootprint</code> from the real aggressor-side flag captured all the way from
          matching-engine&apos;s matching logic. Not a synthetic buy/sell split.
        </p>
      </div>

      <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
        <div className="flex flex-wrap items-end gap-3">
          <label className="flex flex-col gap-1 text-sm">
            Symbol
            <input
              className="rounded border px-3 py-2"
              value={instrumentSymbol}
              onChange={(changeEvent) => setInstrumentSymbol(changeEvent.target.value)}
            />
          </label>
          <label className="flex flex-col gap-1 text-sm">
            Price bucket size (minor units)
            <input
              className="w-40 rounded border px-3 py-2"
              value={priceBucketSizeInput}
              onChange={(changeEvent) => setPriceBucketSizeInput(changeEvent.target.value)}
            />
          </label>
          <label className="flex flex-col gap-1 text-sm">
            Candle interval (seconds)
            <input
              className="w-32 rounded border px-3 py-2"
              value={candleIntervalSecondsInput}
              onChange={(changeEvent) => setCandleIntervalSecondsInput(changeEvent.target.value)}
            />
          </label>
          <label className="flex flex-col gap-1 text-sm">
            Start epoch seconds (optional)
            <input
              className="w-40 rounded border px-3 py-2"
              value={startEpochSecondsInput}
              onChange={(changeEvent) => setStartEpochSecondsInput(changeEvent.target.value)}
            />
          </label>
          <label className="flex flex-col gap-1 text-sm">
            End epoch seconds (optional)
            <input
              className="w-40 rounded border px-3 py-2"
              value={endEpochSecondsInput}
              onChange={(changeEvent) => setEndEpochSecondsInput(changeEvent.target.value)}
            />
          </label>
          <button
            className="rounded bg-black px-4 py-2 text-sm text-white disabled:opacity-50"
            onClick={fetchFootprint}
            disabled={isLoading}
            type="button"
          >
            {isLoading ? "Fetching…" : "Fetch footprint"}
          </button>
        </div>
      </section>

      {errorMessage && <p className="text-sm text-red-600">{errorMessage}</p>}

      {footprints && footprints.length === 0 && (
        <p className="text-sm text-neutral-500">No candles in range — submit some real trades first, or widen the time window.</p>
      )}

      {footprints && footprints.length > 0 && (
        <div className="flex flex-col gap-6">
          {footprints.map((candle) => (
            <CandleFootprintGrid key={candle.candleStartEpochSeconds} candle={candle} />
          ))}
        </div>
      )}
    </main>
  );
}

function CandleFootprintGrid(props: { candle: CandleFootprint }) {
  const { candle } = props;
  // Descending by price so the grid reads top-to-bottom like a real
  // footprint chart's price ladder.
  const levelsDescending = [...candle.levels].sort((a, b) => b.priceBucketStart - a.priceBucketStart);
  const maxVolumeAtALevel = Math.max(1, ...candle.levels.map((level) => Math.max(level.buyVolume, level.sellVolume)));

  return (
    <div className="rounded border border-neutral-200 p-4">
      <div className="mb-3 flex flex-wrap items-center gap-4 text-xs text-neutral-600">
        <span className="font-semibold text-neutral-900">
          Candle @ {new Date(candle.candleStartEpochSeconds * 1000).toLocaleString()} (epoch {candle.candleStartEpochSeconds})
        </span>
        <span>
          Total buy: <strong className="text-emerald-700">{candle.totalBuyVolume}</strong>
        </span>
        <span>
          Total sell: <strong className="text-rose-700">{candle.totalSellVolume}</strong>
        </span>
      </div>
      <table className="w-full min-w-[400px] border-collapse text-xs">
        <thead>
          <tr className="bg-neutral-100 text-left">
            <th className="p-2">Price</th>
            <th className="p-2">Sell volume</th>
            <th className="p-2">Buy volume</th>
          </tr>
        </thead>
        <tbody>
          {levelsDescending.map((level) => (
            <tr key={level.priceBucketStart} className="border-t border-neutral-200">
              <td className="p-2 font-mono font-semibold">{level.priceBucketStart}</td>
              <td className="p-2">
                <div className="flex items-center gap-2">
                  <div
                    className="h-4 bg-rose-400"
                    style={{ width: `${(level.sellVolume / maxVolumeAtALevel) * 60}px` }}
                  />
                  <span className="font-mono text-rose-700">{level.sellVolume}</span>
                </div>
              </td>
              <td className="p-2">
                <div className="flex items-center gap-2">
                  <div
                    className="h-4 bg-emerald-400"
                    style={{ width: `${(level.buyVolume / maxVolumeAtALevel) * 60}px` }}
                  />
                  <span className="font-mono text-emerald-700">{level.buyVolume}</span>
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
