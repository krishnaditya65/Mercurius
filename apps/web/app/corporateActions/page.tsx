"use client";

// Mercurius / web — Corporate actions processing (FEATURES.md §14), wired
// against oms-gateway's real `internal/corporateactionsprocessing`
// endpoints on :8081:
//
//   POST /corporate-actions/holdings/seed   {clientAccountIdentifier, instrumentSymbol, quantity, totalCostBasisInMinorUnits}
//   GET  /corporate-actions/holdings?accountId=...&instrument=...   (BOTH query params required)
//   POST /corporate-actions/process         {actionType: "STOCK_SPLIT"|"BONUS_ISSUE"|"MERGER"|"CASH_DIVIDEND",
//                                             clientAccountIdentifier, instrumentSymbol,
//                                             ratioNumerator, ratioDenominator,      // split / bonus / merger
//                                             mergerTargetInstrumentSymbol,          // merger only
//                                             dividendPerShareInMinorUnits}          // cash dividend only
//   GET  /corporate-actions/processed-actions?accountId=...
//
// This is the REAL implementation (computes correct post-action quantity
// and cost basis per real accounting rules — split/bonus/merger/cash
// dividend), distinct from the older `internal/corporateactionexplainer`
// (which only narrates a caller-supplied outcome and is not wired into
// this page — see oms-gateway's README for the distinction). "Upcoming"
// actions have no real feed anywhere in this repo (loudly documented in
// the backend) — this page only shows the "processed" side (an
// account's holding + its append-only processed-actions log), plus a
// form to actually process one against the real position/cost-basis
// engine, which then immediately reflects into GET /positions too.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useState } from "react";
import Link from "next/link";
import { loadSession } from "../session/authSession";

const omsGatewayBaseUrl = process.env.NEXT_PUBLIC_OMS_GATEWAY_BASE_URL ?? "http://localhost:8081";
const LOG_IN_FIRST_MESSAGE = "Log in first from the dashboard's Account panel to use this.";

type Holding = {
  clientAccountIdentifier: string;
  instrumentSymbol: string;
  quantityHeld: number;
  totalCostBasisInMinorUnits: number;
};

type ProcessedAction = {
  actionType: string;
  clientAccountIdentifier: string;
  instrumentSymbol: string;
  holdingBefore?: Holding;
  holdingAfter?: Holding;
  cashCreditedInMinorUnits?: number;
  processedAtTime: string;
};

type ActionType = "STOCK_SPLIT" | "BONUS_ISSUE" | "MERGER" | "CASH_DIVIDEND";

export default function CorporateActionsPage() {
  const [accountIdentifier, setAccountIdentifier] = useState("acct-001");
  const [instrumentSymbol, setInstrumentSymbol] = useState("DEMO-EQ");
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  const [seedQuantity, setSeedQuantity] = useState(10);
  const [seedCostBasisInMinorUnits, setSeedCostBasisInMinorUnits] = useState(1_000_000);
  const [isSeeding, setIsSeeding] = useState(false);
  const [seedStatusMessage, setSeedStatusMessage] = useState<string | null>(null);

  const [currentHolding, setCurrentHolding] = useState<{ holding: Holding; averageCostPerShareInMinorUnits: number } | null>(
    null
  );
  const [processedActions, setProcessedActions] = useState<ProcessedAction[]>([]);

  const [actionType, setActionType] = useState<ActionType>("STOCK_SPLIT");
  const [ratioNumerator, setRatioNumerator] = useState(2);
  const [ratioDenominator, setRatioDenominator] = useState(1);
  const [mergerTargetInstrumentSymbol, setMergerTargetInstrumentSymbol] = useState("DEMO-ACQUIRER");
  const [dividendPerShareInMinorUnits, setDividendPerShareInMinorUnits] = useState(500);
  const [isProcessing, setIsProcessing] = useState(false);
  const [lastProcessedResult, setLastProcessedResult] = useState<ProcessedAction | null>(null);

  async function refreshHolding() {
    setErrorMessage(null);
    const storedSession = loadSession();
    if (!storedSession?.accessToken) {
      setErrorMessage(LOG_IN_FIRST_MESSAGE);
      return;
    }
    try {
      const httpResponse = await fetch(
        `${omsGatewayBaseUrl}/corporate-actions/holdings?accountId=${encodeURIComponent(accountIdentifier)}&instrument=${encodeURIComponent(instrumentSymbol)}`,
        { headers: { Authorization: `Bearer ${storedSession.accessToken}` } }
      );
      if (!httpResponse.ok) {
        const bodyText = await httpResponse.text();
        throw new Error(bodyText || `HTTP ${httpResponse.status}`);
      }
      setCurrentHolding(await httpResponse.json());
    } catch (thrownError) {
      setCurrentHolding(null);
      setErrorMessage(thrownError instanceof Error ? thrownError.message : "Failed to fetch holding.");
    }
  }

  async function refreshProcessedActions() {
    const storedSession = loadSession();
    if (!storedSession?.accessToken) {
      setErrorMessage(LOG_IN_FIRST_MESSAGE);
      return;
    }
    try {
      const httpResponse = await fetch(
        `${omsGatewayBaseUrl}/corporate-actions/processed-actions?accountId=${encodeURIComponent(accountIdentifier)}`,
        { headers: { Authorization: `Bearer ${storedSession.accessToken}` } }
      );
      if (!httpResponse.ok) throw new Error(`HTTP ${httpResponse.status}`);
      const parsed: { accountIdentifier: string; processedActions: ProcessedAction[] | null } = await httpResponse.json();
      setProcessedActions(parsed.processedActions ?? []);
    } catch (thrownError) {
      setErrorMessage(thrownError instanceof Error ? thrownError.message : "Failed to fetch processed actions.");
    }
  }

  async function refreshAll() {
    await Promise.all([refreshHolding(), refreshProcessedActions()]);
  }

  async function seedHolding() {
    setSeedStatusMessage(null);
    const storedSession = loadSession();
    if (!storedSession?.accessToken) {
      setSeedStatusMessage(LOG_IN_FIRST_MESSAGE);
      return;
    }
    setIsSeeding(true);
    try {
      const httpResponse = await fetch(`${omsGatewayBaseUrl}/corporate-actions/holdings/seed`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${storedSession.accessToken}`,
        },
        body: JSON.stringify({
          clientAccountIdentifier: accountIdentifier,
          instrumentSymbol,
          quantity: seedQuantity,
          totalCostBasisInMinorUnits: seedCostBasisInMinorUnits,
        }),
      });
      const bodyText = await httpResponse.text();
      if (!httpResponse.ok) throw new Error(bodyText || `HTTP ${httpResponse.status}`);
      setSeedStatusMessage("Holding seeded.");
      await refreshAll();
    } catch (thrownError) {
      setSeedStatusMessage(thrownError instanceof Error ? `Failed: ${thrownError.message}` : "Unknown error seeding holding.");
    } finally {
      setIsSeeding(false);
    }
  }

  async function processAction() {
    setErrorMessage(null);
    setLastProcessedResult(null);
    const storedSession = loadSession();
    if (!storedSession?.accessToken) {
      setErrorMessage(LOG_IN_FIRST_MESSAGE);
      return;
    }
    setIsProcessing(true);
    try {
      const requestBody: Record<string, unknown> = {
        actionType,
        clientAccountIdentifier: accountIdentifier,
        instrumentSymbol,
      };
      if (actionType === "STOCK_SPLIT" || actionType === "BONUS_ISSUE" || actionType === "MERGER") {
        requestBody.ratioNumerator = ratioNumerator;
        requestBody.ratioDenominator = ratioDenominator;
      }
      if (actionType === "MERGER") {
        requestBody.mergerTargetInstrumentSymbol = mergerTargetInstrumentSymbol;
      }
      if (actionType === "CASH_DIVIDEND") {
        requestBody.dividendPerShareInMinorUnits = dividendPerShareInMinorUnits;
      }

      const httpResponse = await fetch(`${omsGatewayBaseUrl}/corporate-actions/process`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${storedSession.accessToken}`,
        },
        body: JSON.stringify(requestBody),
      });
      const bodyText = await httpResponse.text();
      if (!httpResponse.ok) throw new Error(bodyText || `HTTP ${httpResponse.status}`);
      setLastProcessedResult(JSON.parse(bodyText));
      await refreshAll();
    } catch (thrownError) {
      setErrorMessage(thrownError instanceof Error ? thrownError.message : "Failed to process corporate action.");
    } finally {
      setIsProcessing(false);
    }
  }

  return (
    <main className="mx-auto flex max-w-3xl flex-col gap-8 p-8 font-sans">
      <div>
        <Link href="/" className="text-sm text-neutral-500 underline">
          ← Back to dashboard
        </Link>
        <h1 className="text-xl font-semibold">Corporate actions</h1>
        <p className="text-sm text-neutral-500">
          Backed by oms-gateway&apos;s real <code>internal/corporateactionsprocessing</code> package on :8081 — real
          split/bonus/merger/cash-dividend accounting, immediately reflected into a real cost-basis-aware holding AND
          into <code>GET /positions</code>. <strong>Honest gap</strong>: there is no real upcoming-corporate-actions
          feed anywhere in this repo, so there is no &quot;upcoming actions&quot; list here — only holdings you seed
          and actions you actually process below.
        </p>
      </div>

      {errorMessage && <p className="text-sm text-red-600">{errorMessage}</p>}

      <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
        <h2 className="text-lg font-medium">Account / instrument</h2>
        <div className="flex flex-wrap gap-3">
          <label className="flex flex-col gap-1 text-sm">
            Account
            <input
              className="w-40 rounded border px-3 py-2"
              value={accountIdentifier}
              onChange={(changeEvent) => setAccountIdentifier(changeEvent.target.value)}
            />
          </label>
          <label className="flex flex-col gap-1 text-sm">
            Instrument
            <input
              className="w-40 rounded border px-3 py-2"
              value={instrumentSymbol}
              onChange={(changeEvent) => setInstrumentSymbol(changeEvent.target.value)}
            />
          </label>
          <button type="button" className="self-end rounded border px-3 py-2 text-sm" onClick={refreshAll}>
            Load holding + processed actions
          </button>
        </div>
      </section>

      <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
        <h2 className="text-lg font-medium">Seed a demo holding</h2>
        <p className="text-xs text-neutral-500">
          Corporate-actions holdings are a separate cost-basis-aware store from ordinary trading positions — seed one
          here to have something to process an action against.
        </p>
        <div className="flex flex-wrap items-end gap-3">
          <label className="flex flex-col gap-1 text-sm">
            Quantity
            <input
              type="number"
              className="w-32 rounded border px-3 py-2"
              value={seedQuantity}
              onChange={(changeEvent) => setSeedQuantity(Number(changeEvent.target.value))}
            />
          </label>
          <label className="flex flex-col gap-1 text-sm">
            Total cost basis (minor units)
            <input
              type="number"
              className="w-48 rounded border px-3 py-2"
              value={seedCostBasisInMinorUnits}
              onChange={(changeEvent) => setSeedCostBasisInMinorUnits(Number(changeEvent.target.value))}
            />
          </label>
          <button
            type="button"
            disabled={isSeeding}
            onClick={seedHolding}
            className="rounded bg-black px-4 py-2 text-sm text-white disabled:opacity-50"
          >
            {isSeeding ? "Seeding…" : "Seed holding"}
          </button>
        </div>
        {seedStatusMessage && <p className="text-sm text-neutral-600">{seedStatusMessage}</p>}
      </section>

      <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
        <h2 className="text-lg font-medium">Current holding</h2>
        {currentHolding ? (
          <p className="text-sm">
            {currentHolding.holding.quantityHeld} shares of {currentHolding.holding.instrumentSymbol}, total cost
            basis ₹{(currentHolding.holding.totalCostBasisInMinorUnits / 100).toFixed(2)}, average ₹
            {(currentHolding.averageCostPerShareInMinorUnits / 100).toFixed(2)}/share.
          </p>
        ) : (
          <p className="text-sm text-neutral-500">No holding loaded — seed one above, then load.</p>
        )}
      </section>

      <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
        <h2 className="text-lg font-medium">Process a corporate action</h2>
        <label className="flex flex-col gap-1 text-sm">
          Action type
          <select
            className="w-64 rounded border px-3 py-2"
            value={actionType}
            onChange={(changeEvent) => setActionType(changeEvent.target.value as ActionType)}
          >
            <option value="STOCK_SPLIT">Stock split</option>
            <option value="BONUS_ISSUE">Bonus issue</option>
            <option value="MERGER">Merger / share exchange</option>
            <option value="CASH_DIVIDEND">Cash dividend</option>
          </select>
        </label>

        {(actionType === "STOCK_SPLIT" || actionType === "BONUS_ISSUE" || actionType === "MERGER") && (
          <div className="flex gap-3">
            <label className="flex flex-col gap-1 text-sm">
              Ratio numerator
              <input
                type="number"
                className="w-28 rounded border px-3 py-2"
                value={ratioNumerator}
                onChange={(changeEvent) => setRatioNumerator(Number(changeEvent.target.value))}
              />
            </label>
            <label className="flex flex-col gap-1 text-sm">
              Ratio denominator
              <input
                type="number"
                className="w-28 rounded border px-3 py-2"
                value={ratioDenominator}
                onChange={(changeEvent) => setRatioDenominator(Number(changeEvent.target.value))}
              />
            </label>
          </div>
        )}

        {actionType === "MERGER" && (
          <label className="flex flex-col gap-1 text-sm">
            Merger target instrument symbol
            <input
              className="w-64 rounded border px-3 py-2"
              value={mergerTargetInstrumentSymbol}
              onChange={(changeEvent) => setMergerTargetInstrumentSymbol(changeEvent.target.value)}
            />
          </label>
        )}

        {actionType === "CASH_DIVIDEND" && (
          <label className="flex flex-col gap-1 text-sm">
            Dividend per share (minor units)
            <input
              type="number"
              className="w-40 rounded border px-3 py-2"
              value={dividendPerShareInMinorUnits}
              onChange={(changeEvent) => setDividendPerShareInMinorUnits(Number(changeEvent.target.value))}
            />
          </label>
        )}

        <button
          type="button"
          disabled={isProcessing}
          onClick={processAction}
          className="self-start rounded bg-black px-4 py-2 text-sm text-white disabled:opacity-50"
        >
          {isProcessing ? "Processing…" : "Process action"}
        </button>

        {lastProcessedResult && (
          <div className="rounded border border-green-200 bg-green-50 p-3 text-sm">
            <p className="font-medium">{lastProcessedResult.actionType} processed.</p>
            {lastProcessedResult.holdingBefore && lastProcessedResult.holdingAfter && (
              <p>
                {lastProcessedResult.holdingBefore.quantityHeld} shares → {lastProcessedResult.holdingAfter.quantityHeld}{" "}
                shares (cost basis ₹{(lastProcessedResult.holdingBefore.totalCostBasisInMinorUnits / 100).toFixed(2)} →
                ₹{(lastProcessedResult.holdingAfter.totalCostBasisInMinorUnits / 100).toFixed(2)})
              </p>
            )}
            {typeof lastProcessedResult.cashCreditedInMinorUnits === "number" && (
              <p>Cash credited to ledger: ₹{(lastProcessedResult.cashCreditedInMinorUnits / 100).toFixed(2)}</p>
            )}
          </div>
        )}
      </section>

      <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
        <h2 className="text-lg font-medium">Processed actions (append-only log)</h2>
        {processedActions.length === 0 ? (
          <p className="text-sm text-neutral-500">No processed actions for this account yet.</p>
        ) : (
          <ul className="flex flex-col gap-2 text-sm">
            {processedActions.map((processedAction, actionIndex) => (
              <li key={actionIndex} className="rounded border border-neutral-100 p-2">
                <p className="font-medium">
                  {processedAction.actionType} — {processedAction.instrumentSymbol}
                </p>
                <p className="text-xs text-neutral-500">{new Date(processedAction.processedAtTime).toLocaleString()}</p>
                {processedAction.holdingBefore && processedAction.holdingAfter && (
                  <p>
                    {processedAction.holdingBefore.quantityHeld} → {processedAction.holdingAfter.quantityHeld} shares
                  </p>
                )}
                {typeof processedAction.cashCreditedInMinorUnits === "number" && (
                  <p>Cash credited: ₹{(processedAction.cashCreditedInMinorUnits / 100).toFixed(2)}</p>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>
    </main>
  );
}
