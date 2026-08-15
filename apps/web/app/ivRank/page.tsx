"use client";

// Mercurius / web — Implied Volatility Rank / Percentile (FEATURES.md
// §22), wired against quant-engine's real `ivRankCalculator.py` endpoint
// on :8085:
//
//   POST /volatility/iv-rank
//     body: {currentImpliedVolatility: number, historicalImpliedVolatilitySeries: number[]}
//     -> {currentImpliedVolatility, historicalMinimumImpliedVolatility,
//         historicalMaximumImpliedVolatility, impliedVolatilityRank,
//         impliedVolatilityPercentile}
//
// The historical series is caller-supplied — quant-engine does no
// historical IV ingestion of its own (see `_handleIvRankRequest`'s doc
// comment), so what you paste in below is an ILLUSTRATIVE/FIXTURE
// series, not a live 1-year lookback. The rank/percentile MATH itself is
// real. A degenerate series (e.g. every value identical, so there's no
// real min/max range to rank against) makes the underlying calculator
// raise a `ValueError`, which the HTTP layer turns into a real 422 —
// this page surfaces that distinctly from a network/connection failure.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useState } from "react";
import Link from "next/link";

const quantEngineBaseUrl = process.env.NEXT_PUBLIC_QUANT_ENGINE_BASE_URL ?? "http://localhost:8085";

type IvRankResponse = {
  currentImpliedVolatility: number;
  historicalMinimumImpliedVolatility: number;
  historicalMaximumImpliedVolatility: number;
  impliedVolatilityRank: number;
  impliedVolatilityPercentile: number;
};

function parseHistoricalSeries(rawText: string): number[] {
  return rawText
    .split(/[\n,]+/)
    .map((token) => token.trim())
    .filter((token) => token.length > 0)
    .map((token) => Number(token));
}

export default function IvRankPage() {
  const [currentImpliedVolatilityInput, setCurrentImpliedVolatilityInput] = useState("0.28");
  const [historicalSeriesText, setHistoricalSeriesText] = useState(
    "0.18, 0.19, 0.21, 0.22, 0.20, 0.24, 0.26, 0.30, 0.32, 0.29, 0.25, 0.23"
  );
  const [result, setResult] = useState<IvRankResponse | null>(null);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [isValueErrorResponse, setIsValueErrorResponse] = useState(false);

  const parsedSeries = parseHistoricalSeries(historicalSeriesText);
  const seriesHasInvalidEntries = parsedSeries.some((value) => Number.isNaN(value));

  async function runIvRankCalculation() {
    setIsSubmitting(true);
    setErrorMessage(null);
    setIsValueErrorResponse(false);
    setResult(null);
    try {
      const currentImpliedVolatility = Number(currentImpliedVolatilityInput);
      const httpResponse = await fetch(`${quantEngineBaseUrl}/volatility/iv-rank`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          currentImpliedVolatility,
          historicalImpliedVolatilitySeries: parsedSeries,
        }),
      });
      const bodyText = await httpResponse.text();
      if (!httpResponse.ok) {
        if (httpResponse.status === 422) {
          setIsValueErrorResponse(true);
        }
        const parsedError = safeJsonParse(bodyText);
        throw new Error(parsedError?.errorMessage ?? bodyText ?? `HTTP ${httpResponse.status}`);
      }
      setResult(JSON.parse(bodyText));
    } catch (thrownError) {
      setErrorMessage(
        thrownError instanceof Error
          ? thrownError.message
          : "Unknown error computing IV Rank."
      );
    } finally {
      setIsSubmitting(false);
    }
  }

  function safeJsonParse(text: string): { errorMessage?: string } | null {
    try {
      return JSON.parse(text);
    } catch {
      return null;
    }
  }

  return (
    <main className="mx-auto flex max-w-2xl flex-col gap-8 p-8 font-sans">
      <div>
        <Link href="/" className="text-sm text-neutral-500 underline">
          ← Back to dashboard
        </Link>
        <h1 className="text-xl font-semibold">Implied Volatility Rank / Percentile</h1>
        <p className="text-sm text-neutral-500">
          Backed by quant-engine&apos;s real <code>POST /volatility/iv-rank</code> on :8085 — real IV Rank/
          Percentile math over whatever historical series you supply below. The series itself is an
          illustrative/fixture input, not a live lookback quant-engine ingested on its own.
        </p>
      </div>

      <section className="flex flex-col gap-4 rounded border border-neutral-200 p-4">
        <label className="flex flex-col gap-1 text-sm">
          Current implied volatility (e.g. 0.28 for 28%)
          <input
            className="rounded border px-3 py-2"
            value={currentImpliedVolatilityInput}
            onChange={(e) => setCurrentImpliedVolatilityInput(e.target.value)}
          />
        </label>
        <label className="flex flex-col gap-1 text-sm">
          Historical IV series (comma-separated or one per line)
          <textarea
            className="h-32 rounded border px-3 py-2 font-mono text-xs"
            value={historicalSeriesText}
            onChange={(e) => setHistoricalSeriesText(e.target.value)}
          />
        </label>
        <p className="text-xs text-neutral-500">
          Parsed {parsedSeries.length} value(s){seriesHasInvalidEntries && " — one or more entries aren't valid numbers"}.
        </p>
        <button
          type="button"
          disabled={isSubmitting || parsedSeries.length === 0 || seriesHasInvalidEntries}
          onClick={runIvRankCalculation}
          className="self-start rounded bg-black px-4 py-2 text-sm text-white disabled:opacity-50"
        >
          {isSubmitting ? "Computing…" : "Compute IV Rank"}
        </button>
      </section>

      {errorMessage && (
        <p className="text-sm text-red-600">
          {isValueErrorResponse
            ? `Rejected by quant-engine (422 — degenerate series): ${errorMessage}`
            : `Couldn't reach quant-engine: ${errorMessage}. Is it running on ${quantEngineBaseUrl}?`}
        </p>
      )}

      {result && (
        <section className="flex flex-col gap-4 rounded border border-neutral-200 p-4">
          <h2 className="text-lg font-medium">Results</h2>
          <div className="grid grid-cols-2 gap-3 text-sm sm:grid-cols-3">
            <div className="rounded border border-neutral-100 p-3">
              <p className="text-neutral-500">Current IV</p>
              <p className="text-lg font-semibold">{(result.currentImpliedVolatility * 100).toFixed(2)}%</p>
            </div>
            <div className="rounded border border-neutral-100 p-3">
              <p className="text-neutral-500">Historical min</p>
              <p className="text-lg font-semibold">{(result.historicalMinimumImpliedVolatility * 100).toFixed(2)}%</p>
            </div>
            <div className="rounded border border-neutral-100 p-3">
              <p className="text-neutral-500">Historical max</p>
              <p className="text-lg font-semibold">{(result.historicalMaximumImpliedVolatility * 100).toFixed(2)}%</p>
            </div>
            <div className="rounded border border-neutral-100 p-3">
              <p className="text-neutral-500">IV Rank</p>
              <p className="text-lg font-semibold">{(result.impliedVolatilityRank * 100).toFixed(1)}%</p>
            </div>
            <div className="rounded border border-neutral-100 p-3">
              <p className="text-neutral-500">IV Percentile</p>
              <p className="text-lg font-semibold">{(result.impliedVolatilityPercentile * 100).toFixed(1)}%</p>
            </div>
          </div>
        </section>
      )}
    </main>
  );
}
