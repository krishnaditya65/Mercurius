"use client";

// Mercurius / web — Stock/fund screener (FEATURES.md §16, item 1), wired
// against quant-engine's real `stockScreenerFilterBuilder` HTTP endpoints
// on :8085:
//
//   POST /screener/run                       {filterExpression} -> {matchingInstruments: [...]}
//   POST /screener/saved-screens/save         {screenName, filterExpression, description?}
//   POST /screener/saved-screens/get          {screenName}
//   POST /screener/saved-screens/list         {}
//   POST /screener/saved-screens/delete       {screenName}
//
// A real compound AND/OR filter-expression engine over an illustrative
// six-symbol instrument universe (fundamentals hand-fabricated; SMA/RSI
// are real formulas over deterministic synthetic price series — see
// quant-engine's README §16 item 1). This page's filter builder only
// supports a single top-level AND/OR group of leaf conditions (the real
// engine supports arbitrary nesting; a full nested-group builder UI is a
// reasonable follow-up, not required to prove the endpoint is real and
// reachable).
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useState } from "react";
import Link from "next/link";

const quantEngineBaseUrl = process.env.NEXT_PUBLIC_QUANT_ENGINE_BASE_URL ?? "http://localhost:8085";

type FilterCondition = {
  field: string;
  operator: string;
  value: number | string;
};

type MatchingInstrument = {
  symbol: string;
  sector: string;
  priceToEarningsRatio: number;
  marketCapitalizationBillions: number;
  dividendYieldPercent: number;
  currentPrice: number;
  simpleMovingAverage50Day: number;
  relativeStrengthIndex14Day: number;
};

type SavedScreen = {
  screenName: string;
  filterExpression: unknown;
  description?: string;
};

const AVAILABLE_FIELDS = [
  "priceToEarningsRatio",
  "marketCapitalizationBillions",
  "dividendYieldPercent",
  "currentPrice",
  "simpleMovingAverage50Day",
  "relativeStrengthIndex14Day",
  "sector",
];
const AVAILABLE_OPERATORS = ["<", "<=", ">", ">=", "==", "!=", "in", "not_in"];

function buildDefaultCondition(): FilterCondition {
  return { field: "priceToEarningsRatio", operator: "<", value: 20 };
}

export default function ScreenerPage() {
  const [logic, setLogic] = useState<"AND" | "OR">("AND");
  const [conditions, setConditions] = useState<FilterCondition[]>([
    { field: "priceToEarningsRatio", operator: "<", value: 20 },
    { field: "dividendYieldPercent", operator: ">=", value: 4 },
  ]);

  const [matchingInstruments, setMatchingInstruments] = useState<MatchingInstrument[] | null>(null);
  const [isRunning, setIsRunning] = useState(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  const [screenName, setScreenName] = useState("cheap-div");
  const [savedScreens, setSavedScreens] = useState<SavedScreen[]>([]);
  const [saveStatusMessage, setSaveStatusMessage] = useState<string | null>(null);

  function updateCondition(index: number, patch: Partial<FilterCondition>) {
    setConditions((previous) => previous.map((condition, conditionIndex) => (conditionIndex === index ? { ...condition, ...patch } : condition)));
  }

  function buildFilterExpression() {
    if (conditions.length === 1) return conditions[0];
    return { logic, conditions };
  }

  async function runScreener() {
    setIsRunning(true);
    setErrorMessage(null);
    try {
      const httpResponse = await fetch(`${quantEngineBaseUrl}/screener/run`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ filterExpression: buildFilterExpression() }),
      });
      const bodyText = await httpResponse.text();
      if (!httpResponse.ok) throw new Error(bodyText || `HTTP ${httpResponse.status}`);
      const parsed: { matchingInstruments: MatchingInstrument[] } = JSON.parse(bodyText);
      setMatchingInstruments(parsed.matchingInstruments);
    } catch (thrownError) {
      setErrorMessage(
        thrownError instanceof Error
          ? `Couldn't reach quant-engine: ${thrownError.message}. Is it running on ${quantEngineBaseUrl}?`
          : "Unknown error running screener."
      );
    } finally {
      setIsRunning(false);
    }
  }

  async function refreshSavedScreens() {
    try {
      const httpResponse = await fetch(`${quantEngineBaseUrl}/screener/saved-screens/list`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: "{}",
      });
      if (!httpResponse.ok) throw new Error(`HTTP ${httpResponse.status}`);
      const parsed: { screens: SavedScreen[] } = await httpResponse.json();
      setSavedScreens(parsed.screens ?? []);
    } catch (thrownError) {
      setErrorMessage(thrownError instanceof Error ? thrownError.message : "Failed to list saved screens.");
    }
  }

  async function saveCurrentScreen() {
    setSaveStatusMessage(null);
    try {
      const httpResponse = await fetch(`${quantEngineBaseUrl}/screener/saved-screens/save`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ screenName, filterExpression: buildFilterExpression() }),
      });
      const bodyText = await httpResponse.text();
      if (!httpResponse.ok) throw new Error(bodyText || `HTTP ${httpResponse.status}`);
      setSaveStatusMessage(`Saved "${screenName}".`);
      await refreshSavedScreens();
    } catch (thrownError) {
      setSaveStatusMessage(thrownError instanceof Error ? `Failed: ${thrownError.message}` : "Unknown error saving screen.");
    }
  }

  async function loadSavedScreen(name: string) {
    try {
      const httpResponse = await fetch(`${quantEngineBaseUrl}/screener/saved-screens/get`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ screenName: name }),
      });
      if (!httpResponse.ok) throw new Error(`HTTP ${httpResponse.status}`);
      const parsed: SavedScreen = await httpResponse.json();
      const expression = parsed.filterExpression as { logic?: "AND" | "OR"; conditions?: FilterCondition[] } & FilterCondition;
      if (expression.conditions) {
        setLogic(expression.logic ?? "AND");
        setConditions(expression.conditions);
      } else {
        setLogic("AND");
        setConditions([expression as FilterCondition]);
      }
      setScreenName(parsed.screenName);
    } catch (thrownError) {
      setErrorMessage(thrownError instanceof Error ? thrownError.message : "Failed to load saved screen.");
    }
  }

  async function deleteSavedScreen(name: string) {
    try {
      const httpResponse = await fetch(`${quantEngineBaseUrl}/screener/saved-screens/delete`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ screenName: name }),
      });
      if (!httpResponse.ok) throw new Error(`HTTP ${httpResponse.status}`);
      await refreshSavedScreens();
    } catch (thrownError) {
      setErrorMessage(thrownError instanceof Error ? thrownError.message : "Failed to delete saved screen.");
    }
  }

  return (
    <main className="mx-auto flex max-w-3xl flex-col gap-8 p-8 font-sans">
      <div>
        <Link href="/" className="text-sm text-neutral-500 underline">
          ← Back to dashboard
        </Link>
        <h1 className="text-xl font-semibold">Stock / fund screener</h1>
        <p className="text-sm text-neutral-500">
          Backed by quant-engine&apos;s real filter-expression engine on :8085, run against an illustrative
          six-symbol universe (fundamentals fabricated; SMA/RSI real formulas over synthetic prices).
        </p>
      </div>

      {errorMessage && <p className="text-sm text-red-600">{errorMessage}</p>}

      <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
        <h2 className="text-lg font-medium">Filter builder</h2>
        {conditions.length > 1 && (
          <label className="flex flex-col gap-1 text-sm">
            Combine conditions with
            <select className="w-32 rounded border px-3 py-2" value={logic} onChange={(e) => setLogic(e.target.value as "AND" | "OR")}>
              <option value="AND">AND</option>
              <option value="OR">OR</option>
            </select>
          </label>
        )}
        <ul className="flex flex-col gap-2">
          {conditions.map((condition, index) => (
            <li key={index} className="flex flex-wrap items-end gap-2">
              <label className="flex flex-col gap-1 text-sm">
                Field
                <select
                  className="rounded border px-2 py-1.5"
                  value={condition.field}
                  onChange={(e) => updateCondition(index, { field: e.target.value })}
                >
                  {AVAILABLE_FIELDS.map((field) => (
                    <option key={field} value={field}>
                      {field}
                    </option>
                  ))}
                </select>
              </label>
              <label className="flex flex-col gap-1 text-sm">
                Operator
                <select
                  className="rounded border px-2 py-1.5"
                  value={condition.operator}
                  onChange={(e) => updateCondition(index, { operator: e.target.value })}
                >
                  {AVAILABLE_OPERATORS.map((operator) => (
                    <option key={operator} value={operator}>
                      {operator}
                    </option>
                  ))}
                </select>
              </label>
              <label className="flex flex-col gap-1 text-sm">
                Value
                <input
                  className="w-32 rounded border px-2 py-1.5"
                  value={String(condition.value)}
                  onChange={(e) => {
                    const raw = e.target.value;
                    const numeric = Number(raw);
                    updateCondition(index, { value: raw !== "" && !Number.isNaN(numeric) ? numeric : raw });
                  }}
                />
              </label>
              <button
                type="button"
                className="rounded border px-2 py-1.5 text-xs text-red-600"
                onClick={() => setConditions((previous) => previous.filter((_, conditionIndex) => conditionIndex !== index))}
                disabled={conditions.length === 1}
              >
                Remove
              </button>
            </li>
          ))}
        </ul>
        <button
          type="button"
          className="self-start rounded border px-3 py-1.5 text-sm"
          onClick={() => setConditions((previous) => [...previous, buildDefaultCondition()])}
        >
          + Add condition
        </button>

        <div className="flex flex-wrap items-end gap-3 border-t border-neutral-100 pt-3">
          <button
            type="button"
            disabled={isRunning}
            onClick={runScreener}
            className="rounded bg-black px-4 py-2 text-sm text-white disabled:opacity-50"
          >
            {isRunning ? "Running…" : "Run screener"}
          </button>
          <label className="flex flex-col gap-1 text-sm">
            Screen name
            <input
              className="w-40 rounded border px-3 py-2"
              value={screenName}
              onChange={(e) => setScreenName(e.target.value)}
            />
          </label>
          <button type="button" className="rounded border px-4 py-2 text-sm" onClick={saveCurrentScreen}>
            Save screen
          </button>
        </div>
        {saveStatusMessage && <p className="text-sm text-neutral-600">{saveStatusMessage}</p>}
      </section>

      <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
        <div className="flex items-center justify-between">
          <h2 className="text-lg font-medium">Saved screens</h2>
          <button type="button" className="rounded border px-3 py-2 text-sm" onClick={refreshSavedScreens}>
            Load list
          </button>
        </div>
        {savedScreens.length === 0 ? (
          <p className="text-sm text-neutral-500">No saved screens loaded — click &quot;Load list&quot;, or save one above.</p>
        ) : (
          <ul className="flex flex-col gap-2 text-sm">
            {savedScreens.map((screen) => (
              <li key={screen.screenName} className="flex items-center justify-between rounded border border-neutral-100 p-2">
                <span className="font-mono">{screen.screenName}</span>
                <span className="flex gap-2">
                  <button type="button" className="rounded border px-2 py-1 text-xs" onClick={() => loadSavedScreen(screen.screenName)}>
                    Load
                  </button>
                  <button
                    type="button"
                    className="rounded border px-2 py-1 text-xs text-red-600"
                    onClick={() => deleteSavedScreen(screen.screenName)}
                  >
                    Delete
                  </button>
                </span>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
        <h2 className="text-lg font-medium">Results</h2>
        {matchingInstruments === null ? (
          <p className="text-sm text-neutral-500">Run the screener to see results.</p>
        ) : matchingInstruments.length === 0 ? (
          <p className="text-sm text-neutral-500">No instruments matched this filter.</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-sm">
              <thead>
                <tr className="border-b border-neutral-200">
                  <th className="py-1 pr-3">Symbol</th>
                  <th className="py-1 pr-3">Sector</th>
                  <th className="py-1 pr-3">P/E</th>
                  <th className="py-1 pr-3">Mkt cap ($B)</th>
                  <th className="py-1 pr-3">Div yield %</th>
                  <th className="py-1 pr-3">Price</th>
                  <th className="py-1 pr-3">50d SMA</th>
                  <th className="py-1 pr-3">14d RSI</th>
                </tr>
              </thead>
              <tbody>
                {matchingInstruments.map((instrument) => (
                  <tr key={instrument.symbol} className="border-b border-neutral-100">
                    <td className="py-1 pr-3 font-medium">{instrument.symbol}</td>
                    <td className="py-1 pr-3">{instrument.sector}</td>
                    <td className="py-1 pr-3">{instrument.priceToEarningsRatio.toFixed(2)}</td>
                    <td className="py-1 pr-3">{instrument.marketCapitalizationBillions.toFixed(1)}</td>
                    <td className="py-1 pr-3">{instrument.dividendYieldPercent.toFixed(2)}</td>
                    <td className="py-1 pr-3">{instrument.currentPrice.toFixed(2)}</td>
                    <td className="py-1 pr-3">{instrument.simpleMovingAverage50Day.toFixed(2)}</td>
                    <td className="py-1 pr-3">{instrument.relativeStrengthIndex14Day.toFixed(1)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </main>
  );
}
