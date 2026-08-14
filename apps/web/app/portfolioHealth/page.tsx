"use client";

// Mercurius / web — Portfolio health check / diversification analysis
// (FEATURES.md §16, item 3), wired against quant-engine's real
// `portfolioHealthCheckDiversificationAnalyzer` endpoint on :8085:
//
//   POST /portfolio/health-check  {holdings: [{symbol, sector, portfolioWeight}, ...]}
//     -> {positionHhi, sectorHhi, effectiveNumberOfHoldings, weightsBySector,
//         topPositionSymbol, topPositionWeight, topSector, topSectorWeight,
//         portfolioExposureByFactor, nudges: [{severity, message}]}
//
// Real Herfindahl-Hirschman Index concentration math (DOJ/FTC Merger
// Guidelines HHI severity bands), real plain-language nudges genuinely
// interpolated from the actual computed numbers.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useState } from "react";
import Link from "next/link";

const quantEngineBaseUrl = process.env.NEXT_PUBLIC_QUANT_ENGINE_BASE_URL ?? "http://localhost:8085";

type HoldingInput = {
  symbol: string;
  sector: string;
  portfolioWeight: number;
};

type Nudge = { severity: string; message: string };

type HealthCheckResponse = {
  positionHhi: number;
  sectorHhi: number;
  effectiveNumberOfHoldings: number;
  weightsBySector: Record<string, number>;
  topPositionSymbol: string;
  topPositionWeight: number;
  topSector: string;
  topSectorWeight: number;
  nudges: Nudge[];
};

const SEVERITY_STYLES: Record<string, string> = {
  HIGH: "border-red-300 bg-red-50 text-red-800",
  MODERATE: "border-amber-300 bg-amber-50 text-amber-800",
  LOW: "border-green-300 bg-green-50 text-green-800",
};

export default function PortfolioHealthCheckPage() {
  const [holdings, setHoldings] = useState<HoldingInput[]>([
    { symbol: "MEGA", sector: "TECH", portfolioWeight: 0.85 },
    { symbol: "SMALL1", sector: "FINANCIALS", portfolioWeight: 0.1 },
    { symbol: "SMALL2", sector: "FINANCIALS", portfolioWeight: 0.05 },
  ]);
  const [result, setResult] = useState<HealthCheckResponse | null>(null);
  const [isChecking, setIsChecking] = useState(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  function updateHolding(index: number, patch: Partial<HoldingInput>) {
    setHoldings((previous) => previous.map((holding, holdingIndex) => (holdingIndex === index ? { ...holding, ...patch } : holding)));
  }

  async function runHealthCheck() {
    setIsChecking(true);
    setErrorMessage(null);
    try {
      const httpResponse = await fetch(`${quantEngineBaseUrl}/portfolio/health-check`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ holdings }),
      });
      const bodyText = await httpResponse.text();
      if (!httpResponse.ok) throw new Error(bodyText || `HTTP ${httpResponse.status}`);
      setResult(JSON.parse(bodyText));
    } catch (thrownError) {
      setErrorMessage(
        thrownError instanceof Error
          ? `Couldn't reach quant-engine: ${thrownError.message}. Is it running on ${quantEngineBaseUrl}?`
          : "Unknown error running health check."
      );
    } finally {
      setIsChecking(false);
    }
  }

  return (
    <main className="mx-auto flex max-w-2xl flex-col gap-8 p-8 font-sans">
      <div>
        <Link href="/" className="text-sm text-neutral-500 underline">
          ← Back to dashboard
        </Link>
        <h1 className="text-xl font-semibold">Portfolio health check</h1>
        <p className="text-sm text-neutral-500">
          Backed by quant-engine&apos;s real Herfindahl-Hirschman Index concentration analyzer on :8085 — real HHI
          math, real DOJ/FTC severity bands, plain-language nudges interpolated from your actual weights.
        </p>
      </div>

      {errorMessage && <p className="text-sm text-red-600">{errorMessage}</p>}

      <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
        <h2 className="text-lg font-medium">Holdings</h2>
        <ul className="flex flex-col gap-2">
          {holdings.map((holding, index) => (
            <li key={index} className="flex flex-wrap items-end gap-2">
              <label className="flex flex-col gap-1 text-sm">
                Symbol
                <input
                  className="w-28 rounded border px-2 py-1.5"
                  value={holding.symbol}
                  onChange={(e) => updateHolding(index, { symbol: e.target.value })}
                />
              </label>
              <label className="flex flex-col gap-1 text-sm">
                Sector
                <input
                  className="w-32 rounded border px-2 py-1.5"
                  value={holding.sector}
                  onChange={(e) => updateHolding(index, { sector: e.target.value })}
                />
              </label>
              <label className="flex flex-col gap-1 text-sm">
                Weight (0–1)
                <input
                  type="number"
                  step={0.01}
                  className="w-24 rounded border px-2 py-1.5"
                  value={holding.portfolioWeight}
                  onChange={(e) => updateHolding(index, { portfolioWeight: Number(e.target.value) })}
                />
              </label>
              <button
                type="button"
                className="rounded border px-2 py-1.5 text-xs text-red-600"
                onClick={() => setHoldings((previous) => previous.filter((_, holdingIndex) => holdingIndex !== index))}
                disabled={holdings.length === 1}
              >
                Remove
              </button>
            </li>
          ))}
        </ul>
        <button
          type="button"
          className="self-start rounded border px-3 py-1.5 text-sm"
          onClick={() => setHoldings((previous) => [...previous, { symbol: "", sector: "", portfolioWeight: 0.1 }])}
        >
          + Add holding
        </button>
        <button
          type="button"
          disabled={isChecking}
          onClick={runHealthCheck}
          className="self-start rounded bg-black px-4 py-2 text-sm text-white disabled:opacity-50"
        >
          {isChecking ? "Checking…" : "Run health check"}
        </button>
      </section>

      {result && (
        <section className="flex flex-col gap-4 rounded border border-neutral-200 p-4">
          <h2 className="text-lg font-medium">Results</h2>
          <div className="grid grid-cols-2 gap-3 text-sm sm:grid-cols-3">
            <div className="rounded border border-neutral-100 p-3">
              <p className="text-neutral-500">Position HHI</p>
              <p className="text-lg font-semibold">{result.positionHhi.toFixed(0)}</p>
            </div>
            <div className="rounded border border-neutral-100 p-3">
              <p className="text-neutral-500">Sector HHI</p>
              <p className="text-lg font-semibold">{result.sectorHhi.toFixed(0)}</p>
            </div>
            <div className="rounded border border-neutral-100 p-3">
              <p className="text-neutral-500">Effective # holdings</p>
              <p className="text-lg font-semibold">{result.effectiveNumberOfHoldings.toFixed(2)}</p>
            </div>
            <div className="rounded border border-neutral-100 p-3">
              <p className="text-neutral-500">Top position</p>
              <p className="text-lg font-semibold">
                {result.topPositionSymbol} ({(result.topPositionWeight * 100).toFixed(1)}%)
              </p>
            </div>
            <div className="rounded border border-neutral-100 p-3">
              <p className="text-neutral-500">Top sector</p>
              <p className="text-lg font-semibold">
                {result.topSector} ({(result.topSectorWeight * 100).toFixed(1)}%)
              </p>
            </div>
          </div>

          <div>
            <p className="text-sm font-medium">Weights by sector</p>
            <ul className="text-sm text-neutral-600">
              {Object.entries(result.weightsBySector).map(([sector, weight]) => (
                <li key={sector}>
                  {sector}: {(weight * 100).toFixed(1)}%
                </li>
              ))}
            </ul>
          </div>

          <div className="flex flex-col gap-2">
            <p className="text-sm font-medium">Nudges</p>
            {result.nudges.length === 0 ? (
              <p className="text-sm text-neutral-500">No nudges — this portfolio looks well diversified.</p>
            ) : (
              result.nudges.map((nudge, nudgeIndex) => (
                <div
                  key={nudgeIndex}
                  className={`rounded border p-3 text-sm ${SEVERITY_STYLES[nudge.severity] ?? "border-neutral-200"}`}
                >
                  <p className="text-xs font-semibold uppercase">{nudge.severity}</p>
                  <p>{nudge.message}</p>
                </div>
              ))
            )}
          </div>
        </section>
      )}
    </main>
  );
}
