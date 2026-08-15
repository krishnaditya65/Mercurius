"use client";

// Mercurius / web — Portfolio-level Greeks aggregation (FEATURES.md §22),
// wired against quant-engine's real `portfolioGreeksAggregator.py`
// endpoint on :8085:
//
//   POST /portfolio/greeks
//     body: {positions: [{identifier, quantity, delta, gamma,
//            vegaPerOnePercentVolatilityChange, thetaPerCalendarDay}, ...]}
//     -> {netDelta, netGamma, netVegaPerOnePercentVolatilityChange,
//         netThetaPerCalendarDay, positionCount}
//
// Real quantity-weighted sum across positions — see
// `_handlePortfolioGreeksRequest` in services/quant-engine's httpServer.py.
// An empty positions list is legitimate (a flat book) and returns
// all-zero Greeks, not a 422 — this page allows submitting zero rows.
// Per-position Greeks are entered by hand here (e.g. from repeated
// `/options/price` calls elsewhere in this app) — this page does not
// compute them itself.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useState } from "react";
import Link from "next/link";

const quantEngineBaseUrl = process.env.NEXT_PUBLIC_QUANT_ENGINE_BASE_URL ?? "http://localhost:8085";

type PositionInput = {
  identifier: string;
  quantity: number;
  delta: number;
  gamma: number;
  vegaPerOnePercentVolatilityChange: number;
  thetaPerCalendarDay: number;
};

type PortfolioGreeksResponse = {
  netDelta: number;
  netGamma: number;
  netVegaPerOnePercentVolatilityChange: number;
  netThetaPerCalendarDay: number;
  positionCount: number;
};

function emptyPosition(): PositionInput {
  return { identifier: "", quantity: 1, delta: 0, gamma: 0, vegaPerOnePercentVolatilityChange: 0, thetaPerCalendarDay: 0 };
}

export default function PortfolioGreeksPage() {
  const [positions, setPositions] = useState<PositionInput[]>([
    { identifier: "DEMO-EQ-CALL-1000", quantity: 10, delta: 0.55, gamma: 0.012, vegaPerOnePercentVolatilityChange: 1.8, thetaPerCalendarDay: -0.42 },
    { identifier: "DEMO-EQ-PUT-950", quantity: -5, delta: -0.31, gamma: 0.01, vegaPerOnePercentVolatilityChange: 1.5, thetaPerCalendarDay: -0.35 },
  ]);
  const [result, setResult] = useState<PortfolioGreeksResponse | null>(null);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  function updatePosition(index: number, patch: Partial<PositionInput>) {
    setPositions((previous) => previous.map((position, positionIndex) => (positionIndex === index ? { ...position, ...patch } : position)));
  }

  async function computeNetGreeks() {
    setIsSubmitting(true);
    setErrorMessage(null);
    setResult(null);
    try {
      const httpResponse = await fetch(`${quantEngineBaseUrl}/portfolio/greeks`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ positions }),
      });
      const bodyText = await httpResponse.text();
      if (!httpResponse.ok) throw new Error(bodyText || `HTTP ${httpResponse.status}`);
      setResult(JSON.parse(bodyText));
    } catch (thrownError) {
      setErrorMessage(
        thrownError instanceof Error
          ? `Couldn't reach quant-engine: ${thrownError.message}. Is it running on ${quantEngineBaseUrl}?`
          : "Unknown error computing portfolio Greeks."
      );
    } finally {
      setIsSubmitting(false);
    }
  }

  return (
    <main className="mx-auto flex max-w-4xl flex-col gap-8 p-8 font-sans">
      <div>
        <Link href="/" className="text-sm text-neutral-500 underline">
          ← Back to dashboard
        </Link>
        <h1 className="text-xl font-semibold">Portfolio Greeks aggregation</h1>
        <p className="text-sm text-neutral-500">
          Backed by quant-engine&apos;s real <code>POST /portfolio/greeks</code> on :8085 — a genuine
          quantity-weighted sum of per-position Greeks you enter below. This page does not compute the per-position
          Greeks itself (e.g. via Black-Scholes) — enter them by hand, or from another tool&apos;s output.
        </p>
      </div>

      {errorMessage && <p className="text-sm text-red-600">{errorMessage}</p>}

      <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
        <h2 className="text-lg font-medium">Positions</h2>
        <div className="overflow-x-auto">
          <table className="w-full min-w-[900px] border-collapse text-sm">
            <thead>
              <tr className="bg-neutral-50 text-left">
                <th className="p-2">Identifier</th>
                <th className="p-2">Quantity</th>
                <th className="p-2">Delta</th>
                <th className="p-2">Gamma</th>
                <th className="p-2">Vega (per 1% vol)</th>
                <th className="p-2">Theta (per day)</th>
                <th className="p-2"></th>
              </tr>
            </thead>
            <tbody>
              {positions.map((position, index) => (
                <tr key={index} className="border-t border-neutral-200">
                  <td className="p-2">
                    <input
                      className="w-40 rounded border px-2 py-1.5"
                      value={position.identifier}
                      onChange={(e) => updatePosition(index, { identifier: e.target.value })}
                    />
                  </td>
                  <td className="p-2">
                    <input
                      type="number"
                      className="w-20 rounded border px-2 py-1.5"
                      value={position.quantity}
                      onChange={(e) => updatePosition(index, { quantity: Number(e.target.value) })}
                    />
                  </td>
                  <td className="p-2">
                    <input
                      type="number"
                      step={0.01}
                      className="w-24 rounded border px-2 py-1.5"
                      value={position.delta}
                      onChange={(e) => updatePosition(index, { delta: Number(e.target.value) })}
                    />
                  </td>
                  <td className="p-2">
                    <input
                      type="number"
                      step={0.001}
                      className="w-24 rounded border px-2 py-1.5"
                      value={position.gamma}
                      onChange={(e) => updatePosition(index, { gamma: Number(e.target.value) })}
                    />
                  </td>
                  <td className="p-2">
                    <input
                      type="number"
                      step={0.01}
                      className="w-24 rounded border px-2 py-1.5"
                      value={position.vegaPerOnePercentVolatilityChange}
                      onChange={(e) => updatePosition(index, { vegaPerOnePercentVolatilityChange: Number(e.target.value) })}
                    />
                  </td>
                  <td className="p-2">
                    <input
                      type="number"
                      step={0.01}
                      className="w-24 rounded border px-2 py-1.5"
                      value={position.thetaPerCalendarDay}
                      onChange={(e) => updatePosition(index, { thetaPerCalendarDay: Number(e.target.value) })}
                    />
                  </td>
                  <td className="p-2">
                    <button
                      type="button"
                      className="rounded border px-2 py-1.5 text-xs text-red-600"
                      onClick={() => setPositions((previous) => previous.filter((_, positionIndex) => positionIndex !== index))}
                    >
                      Remove
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <div className="flex gap-3">
          <button
            type="button"
            className="self-start rounded border px-3 py-1.5 text-sm"
            onClick={() => setPositions((previous) => [...previous, emptyPosition()])}
          >
            + Add position
          </button>
          <button
            type="button"
            disabled={isSubmitting}
            onClick={computeNetGreeks}
            className="self-start rounded bg-black px-4 py-2 text-sm text-white disabled:opacity-50"
          >
            {isSubmitting ? "Computing…" : "Compute net Greeks"}
          </button>
        </div>
      </section>

      {result && (
        <section className="flex flex-col gap-4 rounded border border-neutral-200 p-4">
          <h2 className="text-lg font-medium">Net portfolio Greeks</h2>
          <div className="grid grid-cols-2 gap-3 text-sm sm:grid-cols-5">
            <div className="rounded border border-neutral-100 p-3">
              <p className="text-neutral-500">Net delta</p>
              <p className="text-lg font-semibold">{result.netDelta.toFixed(4)}</p>
            </div>
            <div className="rounded border border-neutral-100 p-3">
              <p className="text-neutral-500">Net gamma</p>
              <p className="text-lg font-semibold">{result.netGamma.toFixed(5)}</p>
            </div>
            <div className="rounded border border-neutral-100 p-3">
              <p className="text-neutral-500">Net vega (per 1% vol)</p>
              <p className="text-lg font-semibold">{result.netVegaPerOnePercentVolatilityChange.toFixed(4)}</p>
            </div>
            <div className="rounded border border-neutral-100 p-3">
              <p className="text-neutral-500">Net theta (per day)</p>
              <p className="text-lg font-semibold">{result.netThetaPerCalendarDay.toFixed(4)}</p>
            </div>
            <div className="rounded border border-neutral-100 p-3">
              <p className="text-neutral-500">Position count</p>
              <p className="text-lg font-semibold">{result.positionCount}</p>
            </div>
          </div>
        </section>
      )}
    </main>
  );
}
