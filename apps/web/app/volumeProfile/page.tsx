"use client";

// Mercurius / web — Volume Profile / Market Profile (TPO) view.
//
// FEATURES.md §20 "[P3] Volume Profile / Market Profile (TPO) charts".
// Calls market-data's real `GET /volumeProfile` endpoint
// (services/market-data/src/volumeProfileAggregator.rs +
// httpQueryServer.rs), which computes a real Volume Profile (volume per
// price bucket, real Point of Control, real Value Area) and a real,
// simplified TPO profile straight off the real trade tape held in
// market-data's columnar tick store — nothing in this component
// fabricates a bar height, a POC, or a Value Area boundary; it only
// renders what market-data actually computed.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useState } from "react";
import Link from "next/link";

const marketDataBaseUrl = process.env.NEXT_PUBLIC_MARKET_DATA_BASE_URL ?? "http://localhost:9103";

type VolumeProfileLevel = {
  priceBucketStart: number;
  totalVolume: number;
};

type VolumeProfileData = {
  levels: VolumeProfileLevel[];
  pointOfControlPriceBucketStart: number | null;
  valueAreaLowPriceBucketStart: number | null;
  valueAreaHighPriceBucketStart: number | null;
  totalVolume: number;
};

type TpoLetter = {
  letter: string;
  intervalStartEpochSeconds: number;
  pricesTouchedThisInterval: number[];
};

type TpoProfileData = {
  letters: TpoLetter[];
  tpoCountsByPriceBucket: [number, number][];
  tpoPointOfControlPriceBucketStart: number | null;
};

type VolumeProfileResponse = {
  instrumentSymbol: string;
  tickCount: number;
  volumeProfile: VolumeProfileData;
  tpoProfile: TpoProfileData;
};

export default function VolumeProfilePage() {
  const [instrumentSymbol, setInstrumentSymbol] = useState("DEMO-EQ");
  const [priceBucketSizeInput, setPriceBucketSizeInput] = useState("100");
  const [valueAreaFractionInput, setValueAreaFractionInput] = useState("0.70");
  const [startEpochSecondsInput, setStartEpochSecondsInput] = useState("");
  const [endEpochSecondsInput, setEndEpochSecondsInput] = useState("");
  const [response, setResponse] = useState<VolumeProfileResponse | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  async function fetchVolumeProfile() {
    setIsLoading(true);
    setErrorMessage(null);
    setResponse(null);
    try {
      const queryParams = new URLSearchParams({
        instrumentSymbol,
        priceBucketSizeInMinorUnits: priceBucketSizeInput,
        valueAreaVolumeFraction: valueAreaFractionInput,
      });
      if (startEpochSecondsInput) queryParams.set("startEpochSeconds", startEpochSecondsInput);
      if (endEpochSecondsInput) queryParams.set("endEpochSeconds", endEpochSecondsInput);

      const httpResponse = await fetch(`${marketDataBaseUrl}/volumeProfile?${queryParams.toString()}`);
      if (!httpResponse.ok) {
        const bodyText = await httpResponse.text();
        throw new Error(`market-data responded with HTTP ${httpResponse.status}: ${bodyText}`);
      }
      const parsed: VolumeProfileResponse = await httpResponse.json();
      setResponse(parsed);
    } catch (thrownError) {
      setErrorMessage(
        thrownError instanceof Error
          ? `Couldn't fetch the volume profile: ${thrownError.message}. Is market-data running on ${marketDataBaseUrl}?`
          : "Unknown error fetching volume profile."
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
        <h1 className="text-xl font-semibold">Volume Profile / Market Profile (TPO)</h1>
        <p className="text-sm text-neutral-500">
          Real volume-by-price bars, real Point of Control, real Value Area, and a real TPO letter profile — all
          computed by market-data&apos;s <code>GET /volumeProfile</code> straight off the real trade tape. Nothing
          here is illustrative.
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
            Value Area fraction (0-1)
            <input
              className="w-32 rounded border px-3 py-2"
              value={valueAreaFractionInput}
              onChange={(changeEvent) => setValueAreaFractionInput(changeEvent.target.value)}
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
            onClick={fetchVolumeProfile}
            disabled={isLoading}
            type="button"
          >
            {isLoading ? "Fetching…" : "Fetch profile"}
          </button>
        </div>
      </section>

      {errorMessage && <p className="text-sm text-red-600">{errorMessage}</p>}

      {response && <VolumeProfileView response={response} />}
    </main>
  );
}

function VolumeProfileView(props: { response: VolumeProfileResponse }) {
  const { response } = props;
  const { volumeProfile, tpoProfile } = response;

  if (volumeProfile.levels.length === 0) {
    return (
      <p className="text-sm text-neutral-500">
        market-data has {response.tickCount} tick(s) in range for {response.instrumentSymbol} — no volume profile
        levels to show. Submit some real trades first (see the README) or widen the time window.
      </p>
    );
  }

  const maxVolume = Math.max(...volumeProfile.levels.map((level) => level.totalVolume));
  // Descending by price so the tallest/highest price renders at the top,
  // matching how a real Volume Profile chart is read top-to-bottom.
  const levelsDescending = [...volumeProfile.levels].sort((a, b) => b.priceBucketStart - a.priceBucketStart);

  return (
    <section className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center gap-6 rounded border border-neutral-300 bg-neutral-50 p-4 text-sm">
        <p>
          <strong>{response.instrumentSymbol}</strong> — {response.tickCount} tick(s) in range
        </p>
        <p>
          Total volume: <strong>{volumeProfile.totalVolume.toLocaleString()}</strong>
        </p>
        <p>
          Point of Control (POC): <strong className="text-amber-700">{volumeProfile.pointOfControlPriceBucketStart}</strong>
        </p>
        <p>
          Value Area:{" "}
          <strong className="text-blue-700">
            {volumeProfile.valueAreaLowPriceBucketStart} – {volumeProfile.valueAreaHighPriceBucketStart}
          </strong>
        </p>
      </div>

      <div className="rounded border border-neutral-200 p-4">
        <h2 className="mb-3 text-sm font-semibold">Volume by price level (real horizontal bars)</h2>
        <div className="flex flex-col gap-1">
          {levelsDescending.map((level) => {
            const isPoc = level.priceBucketStart === volumeProfile.pointOfControlPriceBucketStart;
            const isInValueArea =
              volumeProfile.valueAreaLowPriceBucketStart !== null &&
              volumeProfile.valueAreaHighPriceBucketStart !== null &&
              level.priceBucketStart >= volumeProfile.valueAreaLowPriceBucketStart &&
              level.priceBucketStart <= volumeProfile.valueAreaHighPriceBucketStart;
            const widthPercent = maxVolume > 0 ? (level.totalVolume / maxVolume) * 100 : 0;

            return (
              <div key={level.priceBucketStart} className="flex items-center gap-2 text-xs">
                <span className="w-20 shrink-0 text-right font-mono">{level.priceBucketStart}</span>
                <div className="h-5 flex-1 bg-neutral-100">
                  <div
                    className={`h-5 ${isPoc ? "bg-amber-500" : isInValueArea ? "bg-blue-400" : "bg-neutral-400"}`}
                    style={{ width: `${widthPercent}%` }}
                  />
                </div>
                <span className="w-14 shrink-0 font-mono">{level.totalVolume}</span>
                {isPoc && <span className="text-amber-700">POC</span>}
              </div>
            );
          })}
        </div>
        <p className="mt-3 text-xs text-neutral-500">
          Amber = Point of Control. Blue = inside the Value Area (the contiguous price range containing the
          requested fraction of total volume, grown outward from the POC). Gray = outside the Value Area.
        </p>
      </div>

      <div className="rounded border border-neutral-200 p-4">
        <h2 className="mb-3 text-sm font-semibold">
          TPO (Time Price Opportunity) profile — {tpoProfile.letters.length} letter(s)
        </h2>
        {tpoProfile.letters.length === 0 ? (
          <p className="text-xs text-neutral-500">No TPO letters for this window.</p>
        ) : (
          <div className="flex flex-col gap-2">
            <p className="text-xs text-neutral-500">
              TPO Point of Control (most letters touching one price):{" "}
              <strong>{tpoProfile.tpoPointOfControlPriceBucketStart}</strong>
            </p>
            <div className="overflow-x-auto">
              <table className="w-full min-w-[500px] border-collapse text-xs">
                <thead>
                  <tr className="bg-neutral-100 text-left">
                    <th className="p-2">Letter</th>
                    <th className="p-2">Interval start (epoch s)</th>
                    <th className="p-2">Prices touched</th>
                  </tr>
                </thead>
                <tbody>
                  {tpoProfile.letters.map((letter) => (
                    <tr key={letter.letter} className="border-t border-neutral-200">
                      <td className="p-2 font-mono font-semibold">{letter.letter}</td>
                      <td className="p-2 font-mono">{letter.intervalStartEpochSeconds}</td>
                      <td className="p-2 font-mono">{letter.pricesTouchedThisInterval.join(", ")}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        )}
      </div>
    </section>
  );
}
