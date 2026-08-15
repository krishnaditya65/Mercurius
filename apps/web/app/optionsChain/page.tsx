"use client";

// Mercurius / web — Options chain (simplified retail view).
//
// FEATURES.md §11 "[P2] Options chain (simplified retail view)". Calls
// oms-gateway's REAL `GET /options/chain?underlyingSpotPrice=&expiryDate=
// &symbol=` endpoint (internal/optionschain), which in turn calls
// quant-engine's real Black-Scholes HTTP pricer for every contract in the
// ladder — nothing in THIS component fabricates a price or a Greek, it
// only renders what oms-gateway actually returned.
//
// Spot price is prefilled from market-data's real `GET /trades` (most
// recent trade tick) when available, editable by hand otherwise — the
// options-chain endpoint itself has no notion of "current price", it
// takes underlyingSpotPrice as an explicit query parameter.
//
// LOUD, HONEST GAP carried straight through from oms-gateway's own
// README/package doc for internal/optionschain — repeated here so this
// UI never misrepresents what's real:
//   - Open Interest and Volume per contract are SYNTHETIC/illustrative,
//     not observed market data.
//   - "Implied Volatility" shown here is really the ASSUMED flat
//     volatility fed INTO the pricer (assumedAnnualizedVolatility), not
//     a genuine IV solved from an observed price.
//   - The "call/put price" column is the contract's real theoretical
//     price from quant-engine — there is no real bid/ask spread anywhere
//     in this repo, so it stands in as a single bid/ask-equivalent
//     figure, not a genuine two-sided quote.
// Only the strike ladder, the Greeks/theoretical price themselves, and
// the Put-Call Ratio arithmetic are real.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useState } from "react";
import Link from "next/link";
import { loadSession } from "../session/authSession";

const omsGatewayBaseUrl = process.env.NEXT_PUBLIC_OMS_GATEWAY_BASE_URL ?? "http://localhost:8081";
const marketDataBaseUrl = process.env.NEXT_PUBLIC_MARKET_DATA_BASE_URL ?? "http://localhost:9103";

type OptionContractQuote = {
  optionType: "CALL" | "PUT";
  theoreticalPriceInMinorUnits: number;
  delta: number;
  gamma: number;
  vegaPerOnePercentVolatilityChange: number;
  thetaPerCalendarDay: number;
  assumedAnnualizedVolatility: number;
  syntheticOpenInterest: number;
  syntheticVolume: number;
};

type StrikeRow = {
  strikePrice: number;
  call: OptionContractQuote;
  put: OptionContractQuote;
};

type OptionsChainResponse = {
  underlyingSymbol: string;
  underlyingSpotPrice: number;
  expiryDate: string;
  timeToExpiryInYears: number;
  strikes: StrikeRow[];
  totalCallOpenInterest: number;
  totalPutOpenInterest: number;
  putCallRatio: number;
};

function defaultExpiryDateSixWeeksOut(): string {
  const sixWeeksFromNow = new Date(Date.now() + 42 * 24 * 60 * 60 * 1000);
  return sixWeeksFromNow.toISOString().slice(0, 10);
}

export default function OptionsChainPage() {
  const [underlyingSymbol, setUnderlyingSymbol] = useState("DEMO-EQ");
  const [underlyingSpotPriceInput, setUnderlyingSpotPriceInput] = useState("1000");
  const [expiryDateInput, setExpiryDateInput] = useState(defaultExpiryDateSixWeeksOut());
  const [optionsChain, setOptionsChain] = useState<OptionsChainResponse | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [lastTradePriceStatus, setLastTradePriceStatus] = useState<string | null>(null);

  // Best-effort prefill of the spot-price field from market-data's real
  // last-trade tick. This is a convenience only — the field stays fully
  // editable, and a failure here (market-data not running) never blocks
  // fetching the options chain itself.
  async function prefillSpotPriceFromLastTrade() {
    setLastTradePriceStatus(null);
    try {
      const httpResponse = await fetch(
        `${marketDataBaseUrl}/trades?instrumentSymbol=${encodeURIComponent(underlyingSymbol)}&limit=1`
      );
      if (!httpResponse.ok) throw new Error(`HTTP ${httpResponse.status}`);
      const trades: { priceInMinorUnits: number }[] = await httpResponse.json();
      if (trades.length > 0) {
        const lastPrice = trades[trades.length - 1].priceInMinorUnits;
        setUnderlyingSpotPriceInput(String(lastPrice));
        setLastTradePriceStatus(`Prefilled from market-data's last real trade: ${lastPrice}`);
      } else {
        setLastTradePriceStatus("market-data has no trades yet for this symbol — enter a spot price by hand.");
      }
    } catch (thrownError) {
      setLastTradePriceStatus(
        thrownError instanceof Error
          ? `Couldn't reach market-data to prefill spot price: ${thrownError.message}.`
          : "Unknown error prefilling spot price."
      );
    }
  }

  async function fetchOptionsChain() {
    setErrorMessage(null);
    setOptionsChain(null);
    const storedSession = loadSession();
    if (!storedSession?.accessToken) {
      setErrorMessage("Log in first (see the Account panel above).");
      return;
    }
    setIsLoading(true);
    try {
      const httpResponse = await fetch(
        `${omsGatewayBaseUrl}/options/chain?underlyingSpotPrice=${encodeURIComponent(underlyingSpotPriceInput)}&expiryDate=${encodeURIComponent(expiryDateInput)}&symbol=${encodeURIComponent(underlyingSymbol)}`,
        { headers: { Authorization: `Bearer ${storedSession.accessToken}` } }
      );
      if (!httpResponse.ok) {
        const bodyText = await httpResponse.text();
        throw new Error(`oms-gateway responded with HTTP ${httpResponse.status}: ${bodyText}`);
      }
      const parsedChain: OptionsChainResponse = await httpResponse.json();
      setOptionsChain(parsedChain);
    } catch (thrownError) {
      setErrorMessage(
        thrownError instanceof Error
          ? `Couldn't fetch the options chain: ${thrownError.message}. Is oms-gateway running on ${omsGatewayBaseUrl} with quant-engine reachable behind it?`
          : "Unknown error fetching options chain."
      );
    } finally {
      setIsLoading(false);
    }
  }

  return (
    <main className="mx-auto flex max-w-5xl flex-col gap-6 p-8 font-sans">
      <div>
        <Link href="/" className="text-sm text-neutral-500 underline">
          ← Back to dashboard
        </Link>
        <h1 className="text-xl font-semibold">Options chain (simplified retail view)</h1>
        <p className="text-sm text-neutral-500">
          Real strike ladder + real, live-computed Greeks from oms-gateway&apos;s <code>GET /options/chain</code>{" "}
          (which itself calls quant-engine&apos;s real Black-Scholes pricer). Open Interest/Volume are synthetic —
          see the file header comment for the full honest breakdown of what&apos;s real vs. illustrative here.
        </p>
      </div>

      <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
        <div className="flex flex-wrap items-end gap-3">
          <label className="flex flex-col gap-1 text-sm">
            Symbol
            <input
              className="rounded border px-3 py-2"
              value={underlyingSymbol}
              onChange={(changeEvent) => setUnderlyingSymbol(changeEvent.target.value)}
            />
          </label>
          <label className="flex flex-col gap-1 text-sm">
            Underlying spot price (minor units)
            <input
              className="rounded border px-3 py-2"
              value={underlyingSpotPriceInput}
              onChange={(changeEvent) => setUnderlyingSpotPriceInput(changeEvent.target.value)}
            />
          </label>
          <button className="rounded border px-3 py-2 text-sm" onClick={prefillSpotPriceFromLastTrade} type="button">
            Prefill from last real trade
          </button>
          <label className="flex flex-col gap-1 text-sm">
            Expiry date
            <input
              className="rounded border px-3 py-2"
              type="date"
              value={expiryDateInput}
              onChange={(changeEvent) => setExpiryDateInput(changeEvent.target.value)}
            />
          </label>
          <button
            className="rounded bg-black px-4 py-2 text-sm text-white disabled:opacity-50"
            onClick={fetchOptionsChain}
            disabled={isLoading}
            type="button"
          >
            {isLoading ? "Fetching…" : "Fetch chain"}
          </button>
        </div>
        {lastTradePriceStatus && <p className="text-xs text-neutral-500">{lastTradePriceStatus}</p>}
      </section>

      {errorMessage && <p className="text-sm text-red-600">{errorMessage}</p>}

      {optionsChain && <OptionsChainTable optionsChain={optionsChain} />}
    </main>
  );
}

function OptionsChainTable(props: { optionsChain: OptionsChainResponse }) {
  const { optionsChain } = props;

  return (
    <section className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-6 rounded border border-neutral-300 bg-neutral-50 p-4 text-sm">
        <p>
          <strong>{optionsChain.underlyingSymbol}</strong> @ spot {optionsChain.underlyingSpotPrice} — expiry{" "}
          {optionsChain.expiryDate} ({(optionsChain.timeToExpiryInYears * 365).toFixed(0)} days out)
        </p>
        <p>
          Total call OI: <strong>{optionsChain.totalCallOpenInterest.toLocaleString()}</strong>
        </p>
        <p>
          Total put OI: <strong>{optionsChain.totalPutOpenInterest.toLocaleString()}</strong>
        </p>
        <p>
          Put-Call Ratio: <strong>{optionsChain.putCallRatio.toFixed(3)}</strong>
        </p>
      </div>

      <div className="overflow-x-auto rounded border border-neutral-200">
        <table className="w-full min-w-[1100px] border-collapse text-xs">
          <thead>
            <tr className="bg-neutral-100 text-left">
              <th className="p-2" colSpan={6}>
                CALLS
              </th>
              <th className="border-x border-neutral-300 p-2 text-center">Strike</th>
              <th className="p-2" colSpan={6}>
                PUTS
              </th>
            </tr>
            <tr className="bg-neutral-50 text-left">
              <th className="p-2">Price</th>
              <th className="p-2">Delta</th>
              <th className="p-2">Gamma</th>
              <th className="p-2">Vega</th>
              <th className="p-2">Theta</th>
              <th className="p-2">OI / Vol</th>
              <th className="border-x border-neutral-300 p-2 text-center"></th>
              <th className="p-2">Price</th>
              <th className="p-2">Delta</th>
              <th className="p-2">Gamma</th>
              <th className="p-2">Vega</th>
              <th className="p-2">Theta</th>
              <th className="p-2">OI / Vol</th>
            </tr>
          </thead>
          <tbody>
            {optionsChain.strikes.map((row) => (
              <tr key={row.strikePrice} className="border-t border-neutral-200">
                <OptionContractCells quote={row.call} />
                <td className="border-x border-neutral-300 p-2 text-center font-semibold">{row.strikePrice}</td>
                <OptionContractCells quote={row.put} />
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function OptionContractCells(props: { quote: OptionContractQuote }) {
  const { quote } = props;
  return (
    <>
      <td className="p-2">{quote.theoreticalPriceInMinorUnits.toFixed(2)}</td>
      <td className="p-2">{quote.delta.toFixed(4)}</td>
      <td className="p-2">{quote.gamma.toFixed(5)}</td>
      <td className="p-2">{quote.vegaPerOnePercentVolatilityChange.toFixed(4)}</td>
      <td className="p-2">{quote.thetaPerCalendarDay.toFixed(4)}</td>
      <td className="p-2">
        {quote.syntheticOpenInterest.toLocaleString()} / {quote.syntheticVolume.toLocaleString()}
      </td>
    </>
  );
}
