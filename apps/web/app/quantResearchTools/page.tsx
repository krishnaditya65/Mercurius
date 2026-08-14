"use client";

// Mercurius / web — three more of quant-engine's real FEATURES.md §16
// tools, combined onto one page since each is a small, independent
// single-request tool (no shared state needed):
//
//   Alternative data (item 5):
//     POST /alternative-data/sentiment-signal  {snippets: [{source, text}], killSwitchEnabled}
//       -> {aggregatedSentiment, combinedSnippetText, orderHookSuggestion: {direction, confidence, explanation, killSwitchEngaged}}
//     POST /alternative-data/filing-anomaly    {metrics: {<name>: {historicalValues, currentValue}}}
//       -> {<name>: {currentValue, historicalMean, historicalPopulationStandardDeviation, zScore, isAnomalous, zScoreThreshold}}
//
//   P&L attribution (item 6):
//     POST /pnl-attribution/brinson  {sectors: [{sectorName, portfolioWeight, portfolioLocalReturn, benchmarkWeight, benchmarkLocalReturn}]}
//       -> real Brinson-Hood-Beebower allocation/selection/interaction decomposition
//
//   Custom index construction + backtest (item 7):
//     POST /index/construct-and-backtest  {constituentCount, weightingScheme, rebalanceFrequencyInBars}
//       -> {indexLevelSeries, rebalanceEvents, startingIndexLevel, endingIndexLevel,
//           compoundAnnualGrowthRate, annualizedSharpeRatio, maximumDrawdownFraction, barCount, periodsPerYear}
//
// All three genuinely reach quant-engine on :8085 and render its actual
// computed numbers — no client-side math beyond formatting.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useState } from "react";
import Link from "next/link";

const quantEngineBaseUrl = process.env.NEXT_PUBLIC_QUANT_ENGINE_BASE_URL ?? "http://localhost:8085";

export default function QuantResearchToolsPage() {
  return (
    <main className="mx-auto flex max-w-3xl flex-col gap-10 p-8 font-sans">
      <div>
        <Link href="/" className="text-sm text-neutral-500 underline">
          ← Back to dashboard
        </Link>
        <h1 className="text-xl font-semibold">Alternative data · P&amp;L attribution · custom index builder</h1>
        <p className="text-sm text-neutral-500">
          Three more real quant-engine (:8085) tools from FEATURES.md §16, combined here since each is a small
          independent single-request tool.
        </p>
      </div>

      <AlternativeDataSection />
      <PnlAttributionSection />
      <CustomIndexSection />
    </main>
  );
}

// ---------------------------------------------------------------------
// Alternative data: sentiment signal + filing anomaly detection
// ---------------------------------------------------------------------

type SentimentSignalResponse = {
  aggregatedSentiment: {
    snippetCount: number;
    totalPositiveWordCount: number;
    totalNegativeWordCount: number;
    pooledSentimentScore: number;
    meanSentimentScoreBySource: Record<string, number>;
  };
  combinedSnippetText: string;
  orderHookSuggestion: {
    direction: string;
    confidence: number;
    explanation: string;
    killSwitchEngaged: boolean;
  };
};

type FilingAnomalyResponse = Record<
  string,
  {
    currentValue: number;
    historicalMean: number;
    historicalPopulationStandardDeviation: number;
    zScore: number;
    isAnomalous: boolean;
    zScoreThreshold: number;
  }
>;

function AlternativeDataSection() {
  const [snippetText, setSnippetText] = useState("strong revenue growth and record profit");
  const [snippetSource, setSnippetSource] = useState("NEWSWIRE-A");
  const [killSwitchEnabled, setKillSwitchEnabled] = useState(true);
  const [sentimentResult, setSentimentResult] = useState<SentimentSignalResponse | null>(null);
  const [isRunningSentiment, setIsRunningSentiment] = useState(false);

  const [metricName, setMetricName] = useState("debtToEquityRatio");
  const [historicalValuesCsv, setHistoricalValuesCsv] = useState("0.4, 0.42, 0.39, 0.41, 0.40, 0.43");
  const [currentValue, setCurrentValue] = useState(0.95);
  const [anomalyResult, setAnomalyResult] = useState<FilingAnomalyResponse | null>(null);
  const [isRunningAnomaly, setIsRunningAnomaly] = useState(false);

  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  async function runSentimentSignal() {
    setIsRunningSentiment(true);
    setErrorMessage(null);
    try {
      const httpResponse = await fetch(`${quantEngineBaseUrl}/alternative-data/sentiment-signal`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ snippets: [{ source: snippetSource, text: snippetText }], killSwitchEnabled }),
      });
      const bodyText = await httpResponse.text();
      if (!httpResponse.ok) throw new Error(bodyText || `HTTP ${httpResponse.status}`);
      setSentimentResult(JSON.parse(bodyText));
    } catch (thrownError) {
      setErrorMessage(
        thrownError instanceof Error
          ? `Couldn't reach quant-engine: ${thrownError.message}. Is it running on ${quantEngineBaseUrl}?`
          : "Unknown error computing sentiment signal."
      );
    } finally {
      setIsRunningSentiment(false);
    }
  }

  async function runFilingAnomaly() {
    setIsRunningAnomaly(true);
    setErrorMessage(null);
    try {
      const historicalValues = historicalValuesCsv
        .split(",")
        .map((token) => Number(token.trim()))
        .filter((value) => !Number.isNaN(value));
      const httpResponse = await fetch(`${quantEngineBaseUrl}/alternative-data/filing-anomaly`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ metrics: { [metricName]: { historicalValues, currentValue } } }),
      });
      const bodyText = await httpResponse.text();
      if (!httpResponse.ok) throw new Error(bodyText || `HTTP ${httpResponse.status}`);
      setAnomalyResult(JSON.parse(bodyText));
    } catch (thrownError) {
      setErrorMessage(
        thrownError instanceof Error
          ? `Couldn't reach quant-engine: ${thrownError.message}. Is it running on ${quantEngineBaseUrl}?`
          : "Unknown error computing filing anomaly."
      );
    } finally {
      setIsRunningAnomaly(false);
    }
  }

  return (
    <section className="flex flex-col gap-4 rounded border border-neutral-200 p-4">
      <h2 className="text-lg font-medium">Alternative data</h2>
      {errorMessage && <p className="text-sm text-red-600">{errorMessage}</p>}

      <div className="flex flex-col gap-2">
        <p className="text-sm font-medium">News/social sentiment signal</p>
        <label className="flex flex-col gap-1 text-sm">
          Source
          <input className="w-48 rounded border px-3 py-2" value={snippetSource} onChange={(e) => setSnippetSource(e.target.value)} />
        </label>
        <label className="flex flex-col gap-1 text-sm">
          Snippet text
          <textarea className="rounded border px-3 py-2" rows={2} value={snippetText} onChange={(e) => setSnippetText(e.target.value)} />
        </label>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={killSwitchEnabled} onChange={(e) => setKillSwitchEnabled(e.target.checked)} />
          Kill switch enabled (blocks any resulting order suggestion from acting)
        </label>
        <button
          type="button"
          disabled={isRunningSentiment}
          onClick={runSentimentSignal}
          className="self-start rounded bg-black px-4 py-2 text-sm text-white disabled:opacity-50"
        >
          {isRunningSentiment ? "Computing…" : "Compute sentiment signal"}
        </button>
        {sentimentResult && (
          <div className="rounded border border-neutral-100 p-3 text-sm">
            <p>
              Pooled sentiment score: <strong>{sentimentResult.aggregatedSentiment.pooledSentimentScore.toFixed(3)}</strong> (
              {sentimentResult.aggregatedSentiment.totalPositiveWordCount} positive /{" "}
              {sentimentResult.aggregatedSentiment.totalNegativeWordCount} negative word matches)
            </p>
            <p>
              Order hook suggestion: <strong>{sentimentResult.orderHookSuggestion.direction}</strong> (confidence{" "}
              {sentimentResult.orderHookSuggestion.confidence.toFixed(2)}, kill switch engaged:{" "}
              {String(sentimentResult.orderHookSuggestion.killSwitchEngaged)})
            </p>
            <p className="text-xs text-neutral-500">{sentimentResult.orderHookSuggestion.explanation}</p>
          </div>
        )}
      </div>

      <div className="flex flex-col gap-2 border-t border-neutral-100 pt-3">
        <p className="text-sm font-medium">Filing metric anomaly (z-score) detection</p>
        <div className="flex flex-wrap gap-3">
          <label className="flex flex-col gap-1 text-sm">
            Metric name
            <input className="w-48 rounded border px-3 py-2" value={metricName} onChange={(e) => setMetricName(e.target.value)} />
          </label>
          <label className="flex flex-col gap-1 text-sm">
            Historical values (comma-separated)
            <input
              className="w-64 rounded border px-3 py-2"
              value={historicalValuesCsv}
              onChange={(e) => setHistoricalValuesCsv(e.target.value)}
            />
          </label>
          <label className="flex flex-col gap-1 text-sm">
            Current value
            <input
              type="number"
              step={0.01}
              className="w-28 rounded border px-3 py-2"
              value={currentValue}
              onChange={(e) => setCurrentValue(Number(e.target.value))}
            />
          </label>
        </div>
        <button
          type="button"
          disabled={isRunningAnomaly}
          onClick={runFilingAnomaly}
          className="self-start rounded bg-black px-4 py-2 text-sm text-white disabled:opacity-50"
        >
          {isRunningAnomaly ? "Computing…" : "Check for anomaly"}
        </button>
        {anomalyResult &&
          Object.entries(anomalyResult).map(([name, metric]) => (
            <div key={name} className="rounded border border-neutral-100 p-3 text-sm">
              <p>
                {name}: current {metric.currentValue}, historical mean {metric.historicalMean.toFixed(4)}, z-score{" "}
                <strong>{metric.zScore.toFixed(2)}</strong> (threshold {metric.zScoreThreshold}) —{" "}
                <span className={metric.isAnomalous ? "font-semibold text-red-600" : "text-green-700"}>
                  {metric.isAnomalous ? "ANOMALOUS" : "normal"}
                </span>
              </p>
            </div>
          ))}
      </div>
    </section>
  );
}

// ---------------------------------------------------------------------
// P&L attribution — Brinson-Hood-Beebower decomposition
// ---------------------------------------------------------------------

type BrinsonSectorInput = {
  sectorName: string;
  portfolioWeight: number;
  portfolioLocalReturn: number;
  benchmarkWeight: number;
  benchmarkLocalReturn: number;
};

type BrinsonSectorResult = {
  sectorName: string;
  allocationEffect: number;
  selectionEffect: number;
  interactionEffect: number;
  currencyEffect: number;
  totalSectorEffect: number;
};

type BrinsonResponse = {
  sectorResults: BrinsonSectorResult[];
  totalPortfolioLocalReturn: number;
  totalBenchmarkReturn: number;
  totalActiveReturn: number;
  totalAllocationEffect: number;
  totalSelectionEffect: number;
  totalInteractionEffect: number;
  totalCurrencyEffect: number;
};

function PnlAttributionSection() {
  const [sectors, setSectors] = useState<BrinsonSectorInput[]>([
    { sectorName: "TECH", portfolioWeight: 0.6, portfolioLocalReturn: 0.1, benchmarkWeight: 0.5, benchmarkLocalReturn: 0.08 },
    { sectorName: "FINANCIALS", portfolioWeight: 0.4, portfolioLocalReturn: 0.03, benchmarkWeight: 0.5, benchmarkLocalReturn: 0.05 },
  ]);
  const [result, setResult] = useState<BrinsonResponse | null>(null);
  const [isRunning, setIsRunning] = useState(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  function updateSector(index: number, patch: Partial<BrinsonSectorInput>) {
    setSectors((previous) => previous.map((sector, sectorIndex) => (sectorIndex === index ? { ...sector, ...patch } : sector)));
  }

  async function runAttribution() {
    setIsRunning(true);
    setErrorMessage(null);
    try {
      const httpResponse = await fetch(`${quantEngineBaseUrl}/pnl-attribution/brinson`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ sectors }),
      });
      const bodyText = await httpResponse.text();
      if (!httpResponse.ok) throw new Error(bodyText || `HTTP ${httpResponse.status}`);
      setResult(JSON.parse(bodyText));
    } catch (thrownError) {
      setErrorMessage(
        thrownError instanceof Error
          ? `Couldn't reach quant-engine: ${thrownError.message}. Is it running on ${quantEngineBaseUrl}?`
          : "Unknown error computing attribution."
      );
    } finally {
      setIsRunning(false);
    }
  }

  return (
    <section className="flex flex-col gap-4 rounded border border-neutral-200 p-4">
      <h2 className="text-lg font-medium">P&amp;L attribution (Brinson-Hood-Beebower)</h2>
      {errorMessage && <p className="text-sm text-red-600">{errorMessage}</p>}

      <ul className="flex flex-col gap-2">
        {sectors.map((sector, index) => (
          <li key={index} className="flex flex-wrap items-end gap-2">
            <label className="flex flex-col gap-1 text-xs">
              Sector
              <input className="w-28 rounded border px-2 py-1" value={sector.sectorName} onChange={(e) => updateSector(index, { sectorName: e.target.value })} />
            </label>
            <label className="flex flex-col gap-1 text-xs">
              Portfolio weight
              <input
                type="number"
                step={0.01}
                className="w-24 rounded border px-2 py-1"
                value={sector.portfolioWeight}
                onChange={(e) => updateSector(index, { portfolioWeight: Number(e.target.value) })}
              />
            </label>
            <label className="flex flex-col gap-1 text-xs">
              Portfolio return
              <input
                type="number"
                step={0.001}
                className="w-24 rounded border px-2 py-1"
                value={sector.portfolioLocalReturn}
                onChange={(e) => updateSector(index, { portfolioLocalReturn: Number(e.target.value) })}
              />
            </label>
            <label className="flex flex-col gap-1 text-xs">
              Benchmark weight
              <input
                type="number"
                step={0.01}
                className="w-24 rounded border px-2 py-1"
                value={sector.benchmarkWeight}
                onChange={(e) => updateSector(index, { benchmarkWeight: Number(e.target.value) })}
              />
            </label>
            <label className="flex flex-col gap-1 text-xs">
              Benchmark return
              <input
                type="number"
                step={0.001}
                className="w-24 rounded border px-2 py-1"
                value={sector.benchmarkLocalReturn}
                onChange={(e) => updateSector(index, { benchmarkLocalReturn: Number(e.target.value) })}
              />
            </label>
            <button
              type="button"
              className="rounded border px-2 py-1 text-xs text-red-600"
              onClick={() => setSectors((previous) => previous.filter((_, sectorIndex) => sectorIndex !== index))}
              disabled={sectors.length === 1}
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
          setSectors((previous) => [
            ...previous,
            { sectorName: "", portfolioWeight: 0, portfolioLocalReturn: 0, benchmarkWeight: 0, benchmarkLocalReturn: 0 },
          ])
        }
      >
        + Add sector
      </button>
      <button
        type="button"
        disabled={isRunning}
        onClick={runAttribution}
        className="self-start rounded bg-black px-4 py-2 text-sm text-white disabled:opacity-50"
      >
        {isRunning ? "Computing…" : "Compute attribution"}
      </button>

      {result && (
        <div className="flex flex-col gap-3 text-sm">
          <div className="overflow-x-auto">
            <table className="w-full text-left">
              <thead>
                <tr className="border-b border-neutral-200">
                  <th className="py-1 pr-3">Sector</th>
                  <th className="py-1 pr-3">Allocation</th>
                  <th className="py-1 pr-3">Selection</th>
                  <th className="py-1 pr-3">Interaction</th>
                  <th className="py-1 pr-3">Total</th>
                </tr>
              </thead>
              <tbody>
                {result.sectorResults.map((sectorResult) => (
                  <tr key={sectorResult.sectorName} className="border-b border-neutral-100">
                    <td className="py-1 pr-3 font-medium">{sectorResult.sectorName}</td>
                    <td className="py-1 pr-3">{(sectorResult.allocationEffect * 100).toFixed(2)}%</td>
                    <td className="py-1 pr-3">{(sectorResult.selectionEffect * 100).toFixed(2)}%</td>
                    <td className="py-1 pr-3">{(sectorResult.interactionEffect * 100).toFixed(2)}%</td>
                    <td className="py-1 pr-3">{(sectorResult.totalSectorEffect * 100).toFixed(2)}%</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <p>
            Total active return: <strong>{(result.totalActiveReturn * 100).toFixed(2)}%</strong> (portfolio{" "}
            {(result.totalPortfolioLocalReturn * 100).toFixed(2)}% vs benchmark {(result.totalBenchmarkReturn * 100).toFixed(2)}%) = allocation{" "}
            {(result.totalAllocationEffect * 100).toFixed(2)}% + selection {(result.totalSelectionEffect * 100).toFixed(2)}% + interaction{" "}
            {(result.totalInteractionEffect * 100).toFixed(2)}%
          </p>
        </div>
      )}
    </section>
  );
}

// ---------------------------------------------------------------------
// Custom index construction + backtest
// ---------------------------------------------------------------------

type IndexBacktestResponse = {
  indexLevelSeries: number[];
  startingIndexLevel: number;
  endingIndexLevel: number;
  compoundAnnualGrowthRate: number;
  annualizedSharpeRatio: number;
  maximumDrawdownFraction: number;
  barCount: number;
  rebalanceEvents: { barIndex: number; constituentSymbols: string[]; targetWeights: Record<string, number> }[];
};

function CustomIndexSection() {
  const [constituentCount, setConstituentCount] = useState(3);
  const [weightingScheme, setWeightingScheme] = useState<"EQUAL_WEIGHT" | "CAP_WEIGHT">("CAP_WEIGHT");
  const [rebalanceFrequencyInBars, setRebalanceFrequencyInBars] = useState(20);
  const [result, setResult] = useState<IndexBacktestResponse | null>(null);
  const [isRunning, setIsRunning] = useState(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  async function runBacktest() {
    setIsRunning(true);
    setErrorMessage(null);
    try {
      const httpResponse = await fetch(`${quantEngineBaseUrl}/index/construct-and-backtest`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ constituentCount, weightingScheme, rebalanceFrequencyInBars }),
      });
      const bodyText = await httpResponse.text();
      if (!httpResponse.ok) throw new Error(bodyText || `HTTP ${httpResponse.status}`);
      setResult(JSON.parse(bodyText));
    } catch (thrownError) {
      setErrorMessage(
        thrownError instanceof Error
          ? `Couldn't reach quant-engine: ${thrownError.message}. Is it running on ${quantEngineBaseUrl}?`
          : "Unknown error running index backtest."
      );
    } finally {
      setIsRunning(false);
    }
  }

  return (
    <section className="flex flex-col gap-4 rounded border border-neutral-200 p-4">
      <h2 className="text-lg font-medium">Custom index construction + backtest</h2>
      {errorMessage && <p className="text-sm text-red-600">{errorMessage}</p>}

      <div className="flex flex-wrap items-end gap-3">
        <label className="flex flex-col gap-1 text-sm">
          Constituent count
          <input
            type="number"
            min={1}
            max={6}
            className="w-24 rounded border px-3 py-2"
            value={constituentCount}
            onChange={(e) => setConstituentCount(Number(e.target.value))}
          />
        </label>
        <label className="flex flex-col gap-1 text-sm">
          Weighting scheme
          <select
            className="rounded border px-3 py-2"
            value={weightingScheme}
            onChange={(e) => setWeightingScheme(e.target.value as "EQUAL_WEIGHT" | "CAP_WEIGHT")}
          >
            <option value="EQUAL_WEIGHT">Equal weight</option>
            <option value="CAP_WEIGHT">Market-cap weight</option>
          </select>
        </label>
        <label className="flex flex-col gap-1 text-sm">
          Rebalance frequency (bars)
          <input
            type="number"
            min={1}
            className="w-32 rounded border px-3 py-2"
            value={rebalanceFrequencyInBars}
            onChange={(e) => setRebalanceFrequencyInBars(Number(e.target.value))}
          />
        </label>
        <button
          type="button"
          disabled={isRunning}
          onClick={runBacktest}
          className="rounded bg-black px-4 py-2 text-sm text-white disabled:opacity-50"
        >
          {isRunning ? "Backtesting…" : "Construct + backtest"}
        </button>
      </div>

      {result && (
        <div className="flex flex-col gap-3 text-sm">
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <div className="rounded border border-neutral-100 p-3">
              <p className="text-neutral-500">CAGR</p>
              <p className="text-lg font-semibold">{(result.compoundAnnualGrowthRate * 100).toFixed(2)}%</p>
            </div>
            <div className="rounded border border-neutral-100 p-3">
              <p className="text-neutral-500">Sharpe (annualized)</p>
              <p className="text-lg font-semibold">{result.annualizedSharpeRatio.toFixed(2)}</p>
            </div>
            <div className="rounded border border-neutral-100 p-3">
              <p className="text-neutral-500">Max drawdown</p>
              <p className="text-lg font-semibold">{(result.maximumDrawdownFraction * 100).toFixed(2)}%</p>
            </div>
            <div className="rounded border border-neutral-100 p-3">
              <p className="text-neutral-500">Index level</p>
              <p className="text-lg font-semibold">
                {result.startingIndexLevel.toFixed(1)} → {result.endingIndexLevel.toFixed(1)}
              </p>
            </div>
          </div>

          <IndexLevelSparkline levels={result.indexLevelSeries} />

          <div>
            <p className="font-medium">Rebalance events ({result.rebalanceEvents.length})</p>
            <ul className="max-h-48 overflow-y-auto">
              {result.rebalanceEvents.map((event) => (
                <li key={event.barIndex} className="text-xs text-neutral-600">
                  bar {event.barIndex}: {event.constituentSymbols.join(", ")} —{" "}
                  {Object.entries(event.targetWeights)
                    .map(([symbol, weight]) => `${symbol} ${(weight * 100).toFixed(1)}%`)
                    .join(", ")}
                </li>
              ))}
            </ul>
          </div>
        </div>
      )}
    </section>
  );
}

// A minimal inline SVG sparkline of the real computed index level path —
// no charting library dependency, mirrors app/page.tsx's own hand-rolled
// CandlestickChart pattern.
function IndexLevelSparkline(props: { levels: number[] }) {
  const { levels } = props;
  if (levels.length < 2) return null;

  const width = 640;
  const height = 120;
  const minLevel = Math.min(...levels);
  const maxLevel = Math.max(...levels);
  const range = Math.max(1e-9, maxLevel - minLevel);

  const points = levels
    .map((level, index) => {
      const x = (index / (levels.length - 1)) * width;
      const y = height - ((level - minLevel) / range) * height;
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(" ");

  return (
    <svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} className="w-full max-w-full rounded border border-neutral-100" aria-label="Index level path">
      <polyline points={points} fill="none" stroke="black" strokeWidth={1.5} />
    </svg>
  );
}
