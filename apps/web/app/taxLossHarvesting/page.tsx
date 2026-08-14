"use client";

// Mercurius / web — Tax-loss harvesting (FEATURES.md §16, item 4), wired
// against quant-engine's real `taxLossHarvestingAdvisor` endpoint on
// :8085:
//
//   POST /tax/loss-harvesting-plan
//     {lots: [{lotId, symbol, quantity, buyPricePerShare, buyDate, currentPricePerShare}, ...],
//      realizedGainsYtd, proposedSaleDate}
//     -> {proposedSaleDate, realizedGainsYtd, eligibleLotsInHarvestOrder, excludedLotsDueToWashSale,
//         totalHarvestableLoss, amountOffsettingRealizedGains, amountOffsettingOrdinaryIncome, carryForwardLoss}
//
// Real per-lot unrealized-loss identification, the real IRS 61-day
// wash-sale window check, and the real gains/ordinary-income offset
// waterfall. Explicitly NOT tax advice — see quant-engine's module
// docstring for exact scope limits.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useState } from "react";
import Link from "next/link";

const quantEngineBaseUrl = process.env.NEXT_PUBLIC_QUANT_ENGINE_BASE_URL ?? "http://localhost:8085";

type Lot = {
  lotId: string;
  symbol: string;
  quantity: number;
  buyPricePerShare: number;
  buyDate: string;
  currentPricePerShare: number;
};

type EligibleLot = {
  lotId: string;
  symbol: string;
  unrealizedGainOrLoss: number;
};

type ExcludedLot = {
  lotId: string;
  symbol: string;
  unrealizedGainOrLoss: number;
  washSaleViolatingLotIds: string[];
};

type HarvestingPlanResponse = {
  proposedSaleDate: string;
  realizedGainsYtd: number;
  eligibleLotsInHarvestOrder: EligibleLot[];
  excludedLotsDueToWashSale: ExcludedLot[];
  totalHarvestableLoss: number;
  amountOffsettingRealizedGains: number;
  amountOffsettingOrdinaryIncome: number;
  carryForwardLoss: number;
};

export default function TaxLossHarvestingPage() {
  const [lots, setLots] = useState<Lot[]>([
    { lotId: "L1", symbol: "SIM-AAPL", quantity: 10, buyPricePerShare: 200.0, buyDate: "2024-01-01", currentPricePerShare: 100.0 },
    { lotId: "L2", symbol: "SIM-AAPL", quantity: 5, buyPricePerShare: 105.0, buyDate: "2026-06-20", currentPricePerShare: 110.0 },
  ]);
  const [realizedGainsYtd, setRealizedGainsYtd] = useState(2000);
  const [proposedSaleDate, setProposedSaleDate] = useState("2026-06-15");

  const [result, setResult] = useState<HarvestingPlanResponse | null>(null);
  const [isRunning, setIsRunning] = useState(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  function updateLot(index: number, patch: Partial<Lot>) {
    setLots((previous) => previous.map((lot, lotIndex) => (lotIndex === index ? { ...lot, ...patch } : lot)));
  }

  async function runPlan() {
    setIsRunning(true);
    setErrorMessage(null);
    try {
      const httpResponse = await fetch(`${quantEngineBaseUrl}/tax/loss-harvesting-plan`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ lots, realizedGainsYtd, proposedSaleDate }),
      });
      const bodyText = await httpResponse.text();
      if (!httpResponse.ok) throw new Error(bodyText || `HTTP ${httpResponse.status}`);
      setResult(JSON.parse(bodyText));
    } catch (thrownError) {
      setErrorMessage(
        thrownError instanceof Error
          ? `Couldn't reach quant-engine: ${thrownError.message}. Is it running on ${quantEngineBaseUrl}?`
          : "Unknown error computing harvesting plan."
      );
    } finally {
      setIsRunning(false);
    }
  }

  return (
    <main className="mx-auto flex max-w-2xl flex-col gap-8 p-8 font-sans">
      <div>
        <Link href="/" className="text-sm text-neutral-500 underline">
          ← Back to dashboard
        </Link>
        <h1 className="text-xl font-semibold">Tax-loss harvesting</h1>
        <p className="text-sm text-neutral-500">
          Backed by quant-engine&apos;s real per-lot loss identification, IRS 61-day wash-sale window check, and
          gains/ordinary-income offset waterfall on :8085. <strong>Not tax advice</strong> — see quant-engine&apos;s
          README for exact scope limits (no short/long-term rate handling, no state tax rules).
        </p>
      </div>

      {errorMessage && <p className="text-sm text-red-600">{errorMessage}</p>}

      <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
        <h2 className="text-lg font-medium">Lots</h2>
        <ul className="flex flex-col gap-3">
          {lots.map((lot, index) => (
            <li key={index} className="flex flex-wrap items-end gap-2 rounded border border-neutral-100 p-2">
              <label className="flex flex-col gap-1 text-xs">
                Lot ID
                <input className="w-20 rounded border px-2 py-1" value={lot.lotId} onChange={(e) => updateLot(index, { lotId: e.target.value })} />
              </label>
              <label className="flex flex-col gap-1 text-xs">
                Symbol
                <input className="w-28 rounded border px-2 py-1" value={lot.symbol} onChange={(e) => updateLot(index, { symbol: e.target.value })} />
              </label>
              <label className="flex flex-col gap-1 text-xs">
                Quantity
                <input
                  type="number"
                  className="w-20 rounded border px-2 py-1"
                  value={lot.quantity}
                  onChange={(e) => updateLot(index, { quantity: Number(e.target.value) })}
                />
              </label>
              <label className="flex flex-col gap-1 text-xs">
                Buy price/share
                <input
                  type="number"
                  className="w-24 rounded border px-2 py-1"
                  value={lot.buyPricePerShare}
                  onChange={(e) => updateLot(index, { buyPricePerShare: Number(e.target.value) })}
                />
              </label>
              <label className="flex flex-col gap-1 text-xs">
                Buy date
                <input
                  type="date"
                  className="w-36 rounded border px-2 py-1"
                  value={lot.buyDate}
                  onChange={(e) => updateLot(index, { buyDate: e.target.value })}
                />
              </label>
              <label className="flex flex-col gap-1 text-xs">
                Current price/share
                <input
                  type="number"
                  className="w-24 rounded border px-2 py-1"
                  value={lot.currentPricePerShare}
                  onChange={(e) => updateLot(index, { currentPricePerShare: Number(e.target.value) })}
                />
              </label>
              <button
                type="button"
                className="rounded border px-2 py-1 text-xs text-red-600"
                onClick={() => setLots((previous) => previous.filter((_, lotIndex) => lotIndex !== index))}
                disabled={lots.length === 1}
              >
                Remove
              </button>
            </li>
          ))}
        </ul>
        <button
          type="button"
          className="self-start rounded border px-3 py-1.5 text-sm"
          onClick={() =>
            setLots((previous) => [
              ...previous,
              { lotId: `L${previous.length + 1}`, symbol: "", quantity: 1, buyPricePerShare: 100, buyDate: "2025-01-01", currentPricePerShare: 90 },
            ])
          }
        >
          + Add lot
        </button>

        <div className="flex flex-wrap items-end gap-3 border-t border-neutral-100 pt-3">
          <label className="flex flex-col gap-1 text-sm">
            Realized gains YTD
            <input
              type="number"
              className="w-32 rounded border px-3 py-2"
              value={realizedGainsYtd}
              onChange={(e) => setRealizedGainsYtd(Number(e.target.value))}
            />
          </label>
          <label className="flex flex-col gap-1 text-sm">
            Proposed sale date
            <input
              type="date"
              className="w-40 rounded border px-3 py-2"
              value={proposedSaleDate}
              onChange={(e) => setProposedSaleDate(e.target.value)}
            />
          </label>
          <button
            type="button"
            disabled={isRunning}
            onClick={runPlan}
            className="rounded bg-black px-4 py-2 text-sm text-white disabled:opacity-50"
          >
            {isRunning ? "Computing…" : "Compute harvesting plan"}
          </button>
        </div>
      </section>

      {result && (
        <section className="flex flex-col gap-4 rounded border border-neutral-200 p-4">
          <h2 className="text-lg font-medium">Plan</h2>
          <div className="grid grid-cols-2 gap-3 text-sm sm:grid-cols-4">
            <div className="rounded border border-neutral-100 p-3">
              <p className="text-neutral-500">Total harvestable loss</p>
              <p className="text-lg font-semibold">${result.totalHarvestableLoss.toFixed(2)}</p>
            </div>
            <div className="rounded border border-neutral-100 p-3">
              <p className="text-neutral-500">Offsets realized gains</p>
              <p className="text-lg font-semibold">${result.amountOffsettingRealizedGains.toFixed(2)}</p>
            </div>
            <div className="rounded border border-neutral-100 p-3">
              <p className="text-neutral-500">Offsets ordinary income</p>
              <p className="text-lg font-semibold">${result.amountOffsettingOrdinaryIncome.toFixed(2)}</p>
            </div>
            <div className="rounded border border-neutral-100 p-3">
              <p className="text-neutral-500">Carried forward</p>
              <p className="text-lg font-semibold">${result.carryForwardLoss.toFixed(2)}</p>
            </div>
          </div>

          <div>
            <p className="text-sm font-medium">Eligible lots (harvest order)</p>
            {result.eligibleLotsInHarvestOrder.length === 0 ? (
              <p className="text-sm text-neutral-500">None eligible.</p>
            ) : (
              <ul className="text-sm">
                {result.eligibleLotsInHarvestOrder.map((lot) => (
                  <li key={lot.lotId}>
                    {lot.lotId} ({lot.symbol}): ${lot.unrealizedGainOrLoss.toFixed(2)}
                  </li>
                ))}
              </ul>
            )}
          </div>

          <div>
            <p className="text-sm font-medium">Excluded lots (wash-sale rule)</p>
            {result.excludedLotsDueToWashSale.length === 0 ? (
              <p className="text-sm text-neutral-500">None excluded.</p>
            ) : (
              <ul className="text-sm">
                {result.excludedLotsDueToWashSale.map((lot) => (
                  <li key={lot.lotId}>
                    {lot.lotId} ({lot.symbol}): ${lot.unrealizedGainOrLoss.toFixed(2)} — conflicts with lot(s){" "}
                    {lot.washSaleViolatingLotIds.join(", ")}
                  </li>
                ))}
              </ul>
            )}
          </div>
        </section>
      )}
    </main>
  );
}
