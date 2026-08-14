"use client";

// Mercurius / web — retail trading dashboard, skeleton.
//
// Talks directly to oms-gateway's real HTTP endpoints — no
// portfolio/watchlist views, no MF/SIP flows yet — see FEATURES.md §11
// for the full retail app scope. This page exists to prove, from a real
// browser client, that everything oms-gateway/matching-engine expose is
// actually reachable and usable end-to-end: order submission across all
// four order types (Limit/Market/SL/SL-M), Cover Orders, After Market
// Orders, cancellation, status queries, positions, and the
// plain-language-rejection-reason contract (FEATURES.md §21).
//
// The AccountSection below talks to the NEW auth service (register/
// login/refresh/logout, real JWTs) — deliberately NOT wired into the
// order ticket's clientAccountIdentifier field yet: auth mints its own
// `acct-<random hex>` identifier space, completely separate from
// oms-gateway's/ledger's seeded demo accounts (acct-001/acct-002). See
// services/auth/README.md's "Known limitations" for why that
// reconciliation is deliberately not done in this build.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useEffect, useState } from "react";
import Link from "next/link";
import NotificationCenterSection from "./notificationCenter/notificationCenterSection";
import LanguageSwitcher from "./localization/languageSwitcher";
import { useLocalization } from "./localization/localizationContext";
import HomeScreenLivePnlWidget from "./homeScreenLivePnlWidget";

const omsGatewayBaseUrl = process.env.NEXT_PUBLIC_OMS_GATEWAY_BASE_URL ?? "http://localhost:8081";
const marketDataBaseUrl = process.env.NEXT_PUBLIC_MARKET_DATA_BASE_URL ?? "http://localhost:9103";
const authBaseUrl = process.env.NEXT_PUBLIC_AUTH_BASE_URL ?? "http://localhost:8086";

type TradeExecutionSummary = {
  buyingClientAccountId: string;
  sellingClientAccountId: string;
  executedPriceInMinorUnits: number;
  executedQuantity: number;
};

type OrderAcknowledgementResponse = {
  wasOrderAccepted: boolean;
  assignedGlobalSequenceNumber?: number;
  humanReadableRejectionReason?: string;
  machineReadableRejectionReason?: string;
  tradeExecutionEvents?: TradeExecutionSummary[];
  matchingEngineHandoffError?: string;
  matchingEngineOrderSequenceNumber?: number;
  isQueuedAsAfterMarketOrder?: boolean;
};

type CoverOrderResponse = {
  entryOrderAcknowledgement: OrderAcknowledgementResponse;
  protectiveStopOrderSequenceNumber?: number;
  protectiveStopOrderError?: string;
};

// Generates a fresh idempotency key per order-ticket "session" (page
// load), editable by the user — lets a real click-twice retry through
// this UI actually exercise oms-gateway's idempotency guard rather than
// always minting a new key that defeats the point of having one.
function generateIdempotencyKey(): string {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) {
    return crypto.randomUUID();
  }
  // Fallback for environments without crypto.randomUUID — good enough
  // for this demo UI, not cryptographically meaningful.
  return `idem-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

export default function RetailTradingDashboardPage() {
  const { translate } = useLocalization();
  return (
    <main className="mx-auto flex max-w-2xl flex-col gap-10 p-8 font-sans">
      <div>
        <div className="flex items-start justify-between gap-4">
          <h1 className="text-xl font-semibold">{translate("dashboard.title", "Mercurius (skeleton dashboard)")}</h1>
          <LanguageSwitcher />
        </div>
        <p className="text-sm text-neutral-500">
          Talks directly to oms-gateway&apos;s real endpoints — see FEATURES.md §11 for the full retail app scope.
        </p>
        <nav className="mt-2 flex flex-wrap gap-4 text-sm underline">
          <Link href="/watchlist">Watchlist (cross-device sync)</Link>
          <Link href="/optionsChain">Options chain</Link>
          <Link href="/strategies">Follow strategies</Link>
          <Link href="/volumeProfile">Volume Profile / TPO</Link>
          <Link href="/orderFlowFootprint">Order-flow footprint</Link>
          <Link href="/domReplay">Historical DOM replay</Link>
          <Link href="/support">Support tickets</Link>
          <Link href="/corporateActions">Corporate actions</Link>
          <Link href="/referrals">Referrals</Link>
          <Link href="/screener">Screener</Link>
          <Link href="/researchCopilot">Research copilot</Link>
          <Link href="/portfolioHealth">Portfolio health</Link>
          <Link href="/taxLossHarvesting">Tax-loss harvesting</Link>
          <Link href="/quantResearchTools">Alt-data / P&amp;L / index builder</Link>
        </nav>
      </div>

      <AccountSection />
      <HomeScreenLivePnlWidget />
      <OrderTicketSection />
      <PriceChartSection />
      <MarketSessionSection />
      <PositionsSection />
      <OrderLookupSection />
      <NotificationCenterSection />
    </main>
  );
}

// ---------------------------------------------------------------------
// Account — register/login/logout against the new auth service. See the
// file-header comment for why this is deliberately NOT wired into the
// order ticket's account field yet (separate identifier spaces).
// ---------------------------------------------------------------------

type AuthTokenResponse = {
  accountIdentifier?: string;
  accessToken?: string;
  refreshToken?: string;
  expiresInSeconds?: number;
  errorMessage?: string;
};

function AccountSection() {
  const [email, setEmail] = useState("trader@example.com");
  const [password, setPassword] = useState("correct horse battery staple");
  const [session, setSession] = useState<AuthTokenResponse | null>(null);
  const [statusMessage, setStatusMessage] = useState<string | null>(null);
  const [isBusy, setIsBusy] = useState(false);

  async function callAuthEndpoint(path: string, body: unknown): Promise<AuthTokenResponse> {
    const httpResponse = await fetch(`${authBaseUrl}${path}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    return httpResponse.json();
  }

  async function handleRegister() {
    setIsBusy(true);
    setStatusMessage(null);
    try {
      const response = await callAuthEndpoint("/auth/register", { email, password });
      setStatusMessage(
        response.accountIdentifier
          ? `Registered as ${response.accountIdentifier} — now log in.`
          : (response.errorMessage ?? "Registration failed.")
      );
    } catch (thrownError) {
      setStatusMessage(
        thrownError instanceof Error
          ? `Couldn't reach auth: ${thrownError.message}. Is it running on ${authBaseUrl}?`
          : "Unknown error registering."
      );
    } finally {
      setIsBusy(false);
    }
  }

  async function handleLogin() {
    setIsBusy(true);
    setStatusMessage(null);
    try {
      const response = await callAuthEndpoint("/auth/login", { email, password });
      if (response.accessToken) {
        setSession(response);
        setStatusMessage(null);
      } else {
        setStatusMessage(response.errorMessage ?? "Login failed.");
      }
    } catch (thrownError) {
      setStatusMessage(
        thrownError instanceof Error
          ? `Couldn't reach auth: ${thrownError.message}. Is it running on ${authBaseUrl}?`
          : "Unknown error logging in."
      );
    } finally {
      setIsBusy(false);
    }
  }

  async function handleLogout() {
    if (!session?.refreshToken) return;
    setIsBusy(true);
    try {
      await callAuthEndpoint("/auth/logout", { refreshToken: session.refreshToken });
    } finally {
      setSession(null);
      setStatusMessage("Logged out.");
      setIsBusy(false);
    }
  }

  return (
    <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
      <h2 className="text-lg font-medium">Account (services/auth — separate from the order ticket&apos;s account id)</h2>

      {session ? (
        <div className="flex flex-col gap-2 text-sm">
          <p>
            Logged in as <strong>{session.accountIdentifier}</strong>. Access token expires in{" "}
            {session.expiresInSeconds}s.
          </p>
          <p className="break-all rounded bg-neutral-50 p-2 font-mono text-xs text-neutral-600">
            {session.accessToken}
          </p>
          <button
            className="self-start rounded border px-3 py-2 text-sm"
            onClick={handleLogout}
            disabled={isBusy}
            type="button"
          >
            Log out
          </button>
        </div>
      ) : (
        <div className="flex flex-col gap-3">
          <LabeledTextField labelText="Email" fieldValue={email} onFieldValueChange={setEmail} />
          <label className="flex flex-col gap-1 text-sm">
            Password
            <input
              className="rounded border px-3 py-2"
              type="password"
              value={password}
              onChange={(changeEvent) => setPassword(changeEvent.target.value)}
            />
          </label>
          <div className="flex gap-3">
            <button
              className="rounded border px-3 py-2 text-sm"
              onClick={handleRegister}
              disabled={isBusy}
              type="button"
            >
              Register
            </button>
            <button
              className="rounded bg-black px-3 py-2 text-sm text-white disabled:opacity-50"
              onClick={handleLogin}
              disabled={isBusy}
              type="button"
            >
              Log in
            </button>
          </div>
          {statusMessage && <p className="text-sm text-neutral-600">{statusMessage}</p>}
        </div>
      )}
    </section>
  );
}

// ---------------------------------------------------------------------
// Order ticket — submits to either /orders/submit or /orders/cover-submit
// depending on whether "Cover Order" is toggled on.
// ---------------------------------------------------------------------

function OrderTicketSection() {
  const { translate } = useLocalization();
  const [clientAccountIdentifier, setClientAccountIdentifier] = useState("acct-001");
  const [instrumentSymbol, setInstrumentSymbol] = useState("DEMO-EQ");
  const [orderSideIsBuyNotSell, setOrderSideIsBuyNotSell] = useState(true);
  const [orderTypeSelection, setOrderTypeSelection] = useState<"LIMIT" | "MARKET" | "SL" | "SL_M">("LIMIT");
  const [limitPriceInMinorUnits, setLimitPriceInMinorUnits] = useState(10_000);
  const [stopTriggerPriceInMinorUnits, setStopTriggerPriceInMinorUnits] = useState(9_000);
  const [orderQuantity, setOrderQuantity] = useState(5);
  const [orderIsAfterMarketOrder, setOrderIsAfterMarketOrder] = useState(false);
  const [isCoverOrder, setIsCoverOrder] = useState(false);
  const [coverStopLossTriggerPriceInMinorUnits, setCoverStopLossTriggerPriceInMinorUnits] = useState(9_000);
  const [idempotencyKey, setIdempotencyKey] = useState(() => generateIdempotencyKey());

  const [latestAcknowledgement, setLatestAcknowledgement] = useState<OrderAcknowledgementResponse | null>(null);
  const [latestCoverOrderResponse, setLatestCoverOrderResponse] = useState<CoverOrderResponse | null>(null);
  const [isSubmittingOrder, setIsSubmittingOrder] = useState(false);
  const [submissionErrorMessage, setSubmissionErrorMessage] = useState<string | null>(null);

  const requiresTriggerPrice = orderTypeSelection === "SL" || orderTypeSelection === "SL_M";
  const priceFieldIsDisabled = orderTypeSelection === "MARKET" || orderTypeSelection === "SL_M";

  async function handleOrderTicketSubmit(formSubmitEvent: React.FormEvent<HTMLFormElement>) {
    formSubmitEvent.preventDefault();
    setIsSubmittingOrder(true);
    setSubmissionErrorMessage(null);
    setLatestAcknowledgement(null);
    setLatestCoverOrderResponse(null);

    try {
      if (isCoverOrder) {
        const httpResponse = await fetch(`${omsGatewayBaseUrl}/orders/cover-submit`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            clientAccountIdentifier,
            instrumentSymbol,
            orderSideIsBuyNotSell,
            orderIsMarketOrderNotLimit: orderTypeSelection === "MARKET",
            limitPriceInMinorUnits,
            orderQuantity,
            stopLossTriggerPriceInMinorUnits: coverStopLossTriggerPriceInMinorUnits,
          }),
        });
        if (!httpResponse.ok) {
          throw new Error(`oms-gateway responded with HTTP ${httpResponse.status}`);
        }
        const parsedResponse: CoverOrderResponse = await httpResponse.json();
        setLatestCoverOrderResponse(parsedResponse);
      } else {
        const httpResponse = await fetch(`${omsGatewayBaseUrl}/orders/submit`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            clientAccountIdentifier,
            instrumentSymbol,
            orderSideIsBuyNotSell,
            orderIsMarketOrderNotLimit: orderTypeSelection === "MARKET" || orderTypeSelection === "SL_M",
            orderIsStopLossVariant: requiresTriggerPrice,
            stopTriggerPriceInMinorUnits: requiresTriggerPrice ? stopTriggerPriceInMinorUnits : undefined,
            limitPriceInMinorUnits,
            orderQuantity,
            orderIsAfterMarketOrder,
            idempotencyKey,
          }),
        });
        if (!httpResponse.ok) {
          throw new Error(`oms-gateway responded with HTTP ${httpResponse.status}`);
        }
        const parsedAcknowledgement: OrderAcknowledgementResponse = await httpResponse.json();
        setLatestAcknowledgement(parsedAcknowledgement);
      }
    } catch (thrownError) {
      setSubmissionErrorMessage(
        thrownError instanceof Error
          ? `Couldn't reach oms-gateway: ${thrownError.message}. Is it running on ${omsGatewayBaseUrl}?`
          : "Unknown error submitting order."
      );
    } finally {
      setIsSubmittingOrder(false);
    }
  }

  return (
    <section className="flex flex-col gap-4">
      <h2 className="text-lg font-medium">{translate("orderTicket.heading", "Order ticket")}</h2>

      <form onSubmit={handleOrderTicketSubmit} className="flex flex-col gap-4">
        <LabeledTextField
          labelText="Client account"
          fieldValue={clientAccountIdentifier}
          onFieldValueChange={setClientAccountIdentifier}
        />
        <LabeledTextField
          labelText={translate("orderTicket.instrumentSymbol.label", "Instrument symbol")}
          fieldValue={instrumentSymbol}
          onFieldValueChange={setInstrumentSymbol}
        />

        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={orderSideIsBuyNotSell}
            onChange={(changeEvent) => setOrderSideIsBuyNotSell(changeEvent.target.checked)}
          />
          {translate("orderTicket.buySellToggle.label", "Buy (unchecked = sell)")}
        </label>

        <label className="flex flex-col gap-1 text-sm">
          Order type
          <select
            className="rounded border px-3 py-2"
            value={orderTypeSelection}
            onChange={(changeEvent) => setOrderTypeSelection(changeEvent.target.value as typeof orderTypeSelection)}
          >
            <option value="LIMIT">{translate("orderTicket.orderType.limit", "Limit")}</option>
            <option value="MARKET">{translate("orderTicket.orderType.market", "Market")}</option>
            <option value="SL">{translate("orderTicket.orderType.stopLossLimit", "Stop-loss, limit (SL)")}</option>
            <option value="SL_M">{translate("orderTicket.orderType.stopLossMarket", "Stop-loss, market (SL-M)")}</option>
          </select>
        </label>

        <LabeledNumberField
          labelText="Limit price (minor units)"
          fieldValue={limitPriceInMinorUnits}
          onFieldValueChange={setLimitPriceInMinorUnits}
          isDisabled={priceFieldIsDisabled}
        />
        {requiresTriggerPrice && (
          <LabeledNumberField
            labelText="Stop trigger price (minor units)"
            fieldValue={stopTriggerPriceInMinorUnits}
            onFieldValueChange={setStopTriggerPriceInMinorUnits}
          />
        )}
        <LabeledNumberField
          labelText={translate("orderTicket.quantity.label", "Quantity")}
          fieldValue={orderQuantity}
          onFieldValueChange={setOrderQuantity}
        />

        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={orderIsAfterMarketOrder}
            disabled={isCoverOrder}
            onChange={(changeEvent) => setOrderIsAfterMarketOrder(changeEvent.target.checked)}
          />
          After Market Order (queues if the market is closed)
        </label>

        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={isCoverOrder}
            onChange={(changeEvent) => {
              setIsCoverOrder(changeEvent.target.checked);
              if (changeEvent.target.checked) setOrderIsAfterMarketOrder(false);
            }}
          />
          Cover Order (place a protective stop-loss automatically once this fills)
        </label>
        {isCoverOrder && (
          <LabeledNumberField
            labelText="Protective stop-loss trigger price (minor units)"
            fieldValue={coverStopLossTriggerPriceInMinorUnits}
            onFieldValueChange={setCoverStopLossTriggerPriceInMinorUnits}
          />
        )}

        <LabeledTextField
          labelText="Idempotency key (resubmitting with the same key won't double-place)"
          fieldValue={idempotencyKey}
          onFieldValueChange={setIdempotencyKey}
        />
        <button
          type="button"
          className="self-start text-xs text-neutral-500 underline"
          onClick={() => setIdempotencyKey(generateIdempotencyKey())}
        >
          Generate a new key
        </button>

        <button
          type="submit"
          disabled={isSubmittingOrder}
          className="rounded bg-black px-4 py-2 text-white disabled:opacity-50"
        >
          {isSubmittingOrder
            ? translate("orderTicket.submit.inProgress", "Submitting…")
            : isCoverOrder
              ? translate("orderTicket.submit.coverOrder", "Submit cover order")
              : translate("orderTicket.submit.default", "Submit order")}
        </button>
      </form>

      {submissionErrorMessage && <p className="text-sm text-red-600">{submissionErrorMessage}</p>}

      {latestAcknowledgement && <OrderAcknowledgementCard acknowledgement={latestAcknowledgement} />}

      {latestCoverOrderResponse && (
        <div className="flex flex-col gap-2">
          <OrderAcknowledgementCard acknowledgement={latestCoverOrderResponse.entryOrderAcknowledgement} />
          <div className="rounded border border-neutral-300 bg-neutral-50 p-4 text-sm">
            {latestCoverOrderResponse.protectiveStopOrderError ? (
              <p className="text-red-600">
                Protective leg FAILED: {latestCoverOrderResponse.protectiveStopOrderError} — position may be open and
                unprotected.
              </p>
            ) : latestCoverOrderResponse.protectiveStopOrderSequenceNumber ? (
              <p>
                Protective stop-loss placed — order id{" "}
                <strong>{latestCoverOrderResponse.protectiveStopOrderSequenceNumber}</strong>.
              </p>
            ) : (
              <p>Entry hasn&apos;t filled yet, so no protective leg was placed.</p>
            )}
          </div>
        </div>
      )}
    </section>
  );
}

function OrderAcknowledgementCard(props: { acknowledgement: OrderAcknowledgementResponse }) {
  const acknowledgement = props.acknowledgement;

  if (acknowledgement.isQueuedAsAfterMarketOrder) {
    return (
      <div className="rounded border border-amber-400 bg-amber-50 p-4 text-sm">
        <p>Market is closed — order QUEUED as an After Market Order. It will be submitted for real at market open.</p>
      </div>
    );
  }

  return (
    <div
      className={`rounded border p-4 text-sm ${
        acknowledgement.wasOrderAccepted ? "border-green-400 bg-green-50" : "border-red-400 bg-red-50"
      }`}
    >
      {acknowledgement.wasOrderAccepted ? (
        <div className="flex flex-col gap-1">
          <p>
            Order accepted — assigned sequence number <strong>{acknowledgement.assignedGlobalSequenceNumber}</strong>
            {acknowledgement.matchingEngineOrderSequenceNumber !== undefined && (
              <>
                {" "}
                (matching-engine order id <strong>{acknowledgement.matchingEngineOrderSequenceNumber}</strong> — use
                this to cancel or check status below).
              </>
            )}
          </p>
          {acknowledgement.matchingEngineHandoffError && (
            <p className="text-amber-700">Matching-engine hand-off issue: {acknowledgement.matchingEngineHandoffError}</p>
          )}
          {acknowledgement.tradeExecutionEvents && acknowledgement.tradeExecutionEvents.length > 0 && (
            <ul className="list-inside list-disc">
              {acknowledgement.tradeExecutionEvents.map((tradeEvent, tradeIndex) => (
                <li key={tradeIndex}>
                  {tradeEvent.executedQuantity} @ {tradeEvent.executedPriceInMinorUnits} ({tradeEvent.buyingClientAccountId} ←{" "}
                  {tradeEvent.sellingClientAccountId})
                </li>
              ))}
            </ul>
          )}
        </div>
      ) : (
        <p>{acknowledgement.humanReadableRejectionReason}</p>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------
// Price chart — polls market-data's HTTP query API (not oms-gateway),
// see FEATURES.md §8's OHLCV item and §10's charting item. Hand-rolled
// SVG candlesticks, no charting library dependency — consistent with
// this codebase's "don't reach for a framework until there's a real
// reason to" convention (matching-engine/market-data's own hand-rolled
// TCP/HTTP bridges, quant-engine's stdlib-only HTTP server). Good enough
// to prove the pipeline end-to-end; a real build would want a proper
// WebGL/Canvas charting library per FEATURES.md §10.
// ---------------------------------------------------------------------

type CandleBar = {
  instrumentSymbol: string;
  bucketStartEpochSeconds: number;
  openPriceInMinorUnits: number;
  highPriceInMinorUnits: number;
  lowPriceInMinorUnits: number;
  closePriceInMinorUnits: number;
  totalVolume: number;
};

type TradeTick = {
  instrumentSymbol: string;
  executedAtEpochSeconds: number;
  priceInMinorUnits: number;
  quantity: number;
};

const CHART_POLL_INTERVAL_MILLISECONDS = 5_000;

function PriceChartSection() {
  const { translate } = useLocalization();
  const [instrumentSymbol, setInstrumentSymbol] = useState("DEMO-EQ");
  const [candles, setCandles] = useState<CandleBar[]>([]);
  const [mostRecentTrade, setMostRecentTrade] = useState<TradeTick | null>(null);
  const [isAutoRefreshing, setIsAutoRefreshing] = useState(true);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  async function refreshChartData() {
    try {
      const [candlesResponse, tradesResponse] = await Promise.all([
        fetch(`${marketDataBaseUrl}/candles?instrumentSymbol=${encodeURIComponent(instrumentSymbol)}&limit=60`),
        fetch(`${marketDataBaseUrl}/trades?instrumentSymbol=${encodeURIComponent(instrumentSymbol)}&limit=1`),
      ]);
      if (!candlesResponse.ok) throw new Error(`HTTP ${candlesResponse.status}`);
      const parsedCandles: CandleBar[] = await candlesResponse.json();
      setCandles(parsedCandles);

      if (tradesResponse.ok) {
        const parsedTrades: TradeTick[] = await tradesResponse.json();
        setMostRecentTrade(parsedTrades.length > 0 ? parsedTrades[parsedTrades.length - 1] : null);
      }
      setErrorMessage(null);
    } catch (thrownError) {
      setErrorMessage(
        thrownError instanceof Error
          ? `Couldn't reach market-data: ${thrownError.message}. Is it running on ${marketDataBaseUrl}?`
          : "Unknown error fetching candles."
      );
    }
  }

  // Polls on an interval rather than opening a WebSocket — market-data's
  // HTTP query API is a deliberate polling stopgap (see its README),
  // not the real streaming path FEATURES.md/ARCHITECTURE.md §5 call for.
  useEffect(() => {
    // Deliberate: this is the standard "fetch on mount / dep change"
    // effect pattern (https://react.dev/learn/you-might-not-need-an-effect
    // itself documents this as a valid effect use, not a smell) —
    // refreshChartData's setState calls all happen after an `await`, not
    // synchronously in this call frame, so there's no cascading-render
    // hazard here despite the rule's conservative static analysis.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    refreshChartData();
    if (!isAutoRefreshing) return;
    const intervalId = setInterval(refreshChartData, CHART_POLL_INTERVAL_MILLISECONDS);
    return () => clearInterval(intervalId);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [instrumentSymbol, isAutoRefreshing]);

  return (
    <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
      <h2 className="text-lg font-medium">{translate("dashboard.priceChart.heading", "Price chart (1m candles)")}</h2>
      <div className="flex items-end gap-3">
        <LabeledTextField labelText="Instrument" fieldValue={instrumentSymbol} onFieldValueChange={setInstrumentSymbol} />
        <button className="rounded border px-3 py-2 text-sm" onClick={refreshChartData} type="button">
          Refresh now
        </button>
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={isAutoRefreshing}
            onChange={(changeEvent) => setIsAutoRefreshing(changeEvent.target.checked)}
          />
          Auto-refresh every 5s
        </label>
      </div>

      {errorMessage && <p className="text-sm text-red-600">{errorMessage}</p>}

      {mostRecentTrade && (
        <p className="text-sm text-neutral-600">
          Last trade: <strong>{mostRecentTrade.priceInMinorUnits}</strong> × {mostRecentTrade.quantity}
        </p>
      )}

      {candles.length === 0 ? (
        <p className="text-sm text-neutral-500">No candles yet — submit a crossing order to print a trade.</p>
      ) : (
        <CandlestickChart candles={candles} />
      )}
    </section>
  );
}

function CandlestickChart(props: { candles: CandleBar[] }) {
  const { candles } = props;
  const { translate } = useLocalization();

  const chartWidth = 640;
  const chartHeight = 200;
  const candleSlotWidth = chartWidth / candles.length;
  const candleBodyWidth = Math.max(2, candleSlotWidth * 0.6);

  const lowestPrice = Math.min(...candles.map((candle) => candle.lowPriceInMinorUnits));
  const highestPrice = Math.max(...candles.map((candle) => candle.highPriceInMinorUnits));
  // Avoid a zero-height price range (e.g. a single flat candle) collapsing
  // every y-coordinate onto the same pixel.
  const priceRange = Math.max(1, highestPrice - lowestPrice);

  function priceToY(price: number): number {
    return chartHeight - ((price - lowestPrice) / priceRange) * chartHeight;
  }

  return (
    <svg
      viewBox={`0 0 ${chartWidth} ${chartHeight}`}
      className="w-full rounded border border-neutral-100 bg-white"
      role="img"
      aria-label={translate("dashboard.candlestickChart.ariaLabel", "OHLC candlestick chart")}
    >
      {candles.map((candle, candleIndex) => {
        const isBullish = candle.closePriceInMinorUnits >= candle.openPriceInMinorUnits;
        const candleColor = isBullish ? "#16a34a" : "#dc2626";
        const slotCenterX = candleIndex * candleSlotWidth + candleSlotWidth / 2;

        const bodyTopY = priceToY(Math.max(candle.openPriceInMinorUnits, candle.closePriceInMinorUnits));
        const bodyBottomY = priceToY(Math.min(candle.openPriceInMinorUnits, candle.closePriceInMinorUnits));
        const bodyHeight = Math.max(1, bodyBottomY - bodyTopY);

        return (
          <g key={candle.bucketStartEpochSeconds}>
            <line
              x1={slotCenterX}
              x2={slotCenterX}
              y1={priceToY(candle.highPriceInMinorUnits)}
              y2={priceToY(candle.lowPriceInMinorUnits)}
              stroke={candleColor}
              strokeWidth={1}
            />
            <rect
              x={slotCenterX - candleBodyWidth / 2}
              y={bodyTopY}
              width={candleBodyWidth}
              height={bodyHeight}
              fill={candleColor}
            />
          </g>
        );
      })}
    </svg>
  );
}

// ---------------------------------------------------------------------
// Market session admin panel
// ---------------------------------------------------------------------

function MarketSessionSection() {
  const { translate } = useLocalization();
  const [isMarketOpen, setIsMarketOpen] = useState<boolean | null>(null);
  const [queuedAfterMarketOrders, setQueuedAfterMarketOrders] = useState<number | null>(null);
  const [statusErrorMessage, setStatusErrorMessage] = useState<string | null>(null);

  async function refreshStatus() {
    setStatusErrorMessage(null);
    try {
      const httpResponse = await fetch(`${omsGatewayBaseUrl}/market-session/status`);
      const parsed = await httpResponse.json();
      setIsMarketOpen(parsed.isMarketOpen);
      setQueuedAfterMarketOrders(parsed.queuedAfterMarketOrders);
    } catch (thrownError) {
      setStatusErrorMessage(thrownError instanceof Error ? thrownError.message : "Failed to fetch market session status.");
    }
  }

  async function toggleMarketSession(shouldOpen: boolean) {
    setStatusErrorMessage(null);
    try {
      await fetch(`${omsGatewayBaseUrl}/market-session/${shouldOpen ? "open" : "close"}`, { method: "POST" });
      await refreshStatus();
    } catch (thrownError) {
      setStatusErrorMessage(thrownError instanceof Error ? thrownError.message : "Failed to change market session.");
    }
  }

  return (
    <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
      <h2 className="text-lg font-medium">{translate("dashboard.marketSessionAdmin.heading", "Market session (admin)")}</h2>
      <div className="flex items-center gap-3 text-sm">
        <button className="rounded border px-3 py-1" onClick={refreshStatus} type="button">
          Refresh
        </button>
        <button className="rounded border px-3 py-1" onClick={() => toggleMarketSession(true)} type="button">
          Open market (drains AMOs)
        </button>
        <button className="rounded border px-3 py-1" onClick={() => toggleMarketSession(false)} type="button">
          Close market
        </button>
      </div>
      {isMarketOpen !== null && (
        <p className="text-sm">
          Market is <strong>{isMarketOpen ? "OPEN" : "CLOSED"}</strong>
          {queuedAfterMarketOrders !== null && queuedAfterMarketOrders > 0 && (
            <> — {queuedAfterMarketOrders} After Market Order(s) queued.</>
          )}
        </p>
      )}
      {statusErrorMessage && <p className="text-sm text-red-600">{statusErrorMessage}</p>}
    </section>
  );
}

// ---------------------------------------------------------------------
// Positions panel
// ---------------------------------------------------------------------

function PositionsSection() {
  const { translate } = useLocalization();
  const [accountIdentifier, setAccountIdentifier] = useState("acct-001");
  const [netQuantityByInstrumentSymbol, setNetQuantityByInstrumentSymbol] = useState<Record<string, number> | null>(
    null
  );
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  async function fetchPositions() {
    setErrorMessage(null);
    setNetQuantityByInstrumentSymbol(null);
    try {
      const httpResponse = await fetch(`${omsGatewayBaseUrl}/positions?accountId=${encodeURIComponent(accountIdentifier)}`);
      if (!httpResponse.ok) throw new Error(`HTTP ${httpResponse.status}`);
      const parsed = await httpResponse.json();
      setNetQuantityByInstrumentSymbol(parsed.netQuantityByInstrumentSymbol ?? {});
    } catch (thrownError) {
      setErrorMessage(thrownError instanceof Error ? thrownError.message : "Failed to fetch positions.");
    }
  }

  return (
    <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
      <h2 className="text-lg font-medium">{translate("dashboard.positions.heading", "Positions")}</h2>
      <div className="flex items-end gap-3">
        <LabeledTextField labelText="Account" fieldValue={accountIdentifier} onFieldValueChange={setAccountIdentifier} />
        <button className="rounded border px-3 py-2 text-sm" onClick={fetchPositions} type="button">
          Fetch
        </button>
      </div>
      {errorMessage && <p className="text-sm text-red-600">{errorMessage}</p>}
      {netQuantityByInstrumentSymbol && (
        <ul className="text-sm">
          {Object.keys(netQuantityByInstrumentSymbol).length === 0 ? (
            <li className="text-neutral-500">{translate("dashboard.positions.empty", "No open positions.")}</li>
          ) : (
            Object.entries(netQuantityByInstrumentSymbol).map(([instrumentSymbol, netQuantity]) => (
              <li key={instrumentSymbol}>
                {instrumentSymbol}: <strong>{netQuantity}</strong>
              </li>
            ))
          )}
        </ul>
      )}
    </section>
  );
}

// ---------------------------------------------------------------------
// Order status / cancel panel
// ---------------------------------------------------------------------

function OrderLookupSection() {
  const { translate } = useLocalization();
  const [instrumentSymbol, setInstrumentSymbol] = useState("DEMO-EQ");
  const [matchingEngineOrderSequenceNumber, setMatchingEngineOrderSequenceNumber] = useState(1);
  const [statusResult, setStatusResult] = useState<Record<string, unknown> | null>(null);
  const [cancelResult, setCancelResult] = useState<{ wasOrderCancelled: boolean; errorMessage?: string } | null>(null);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  async function checkStatus() {
    setErrorMessage(null);
    setCancelResult(null);
    try {
      const httpResponse = await fetch(
        `${omsGatewayBaseUrl}/orders/status?instrumentSymbol=${encodeURIComponent(instrumentSymbol)}&matchingEngineOrderSequenceNumber=${matchingEngineOrderSequenceNumber}`
      );
      if (!httpResponse.ok) throw new Error(`HTTP ${httpResponse.status}`);
      setStatusResult(await httpResponse.json());
    } catch (thrownError) {
      setErrorMessage(thrownError instanceof Error ? thrownError.message : "Failed to fetch order status.");
    }
  }

  async function cancelOrder() {
    setErrorMessage(null);
    setStatusResult(null);
    try {
      const httpResponse = await fetch(`${omsGatewayBaseUrl}/orders/cancel`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ instrumentSymbol, matchingEngineOrderSequenceNumber }),
      });
      if (!httpResponse.ok) throw new Error(`HTTP ${httpResponse.status}`);
      setCancelResult(await httpResponse.json());
    } catch (thrownError) {
      setErrorMessage(thrownError instanceof Error ? thrownError.message : "Failed to cancel order.");
    }
  }

  return (
    <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
      <h2 className="text-lg font-medium">{translate("orderStatus.heading", "Order status / cancel")}</h2>
      <div className="flex items-end gap-3">
        <LabeledTextField labelText="Instrument" fieldValue={instrumentSymbol} onFieldValueChange={setInstrumentSymbol} />
        <LabeledNumberField
          labelText="matchingEngineOrderSequenceNumber"
          fieldValue={matchingEngineOrderSequenceNumber}
          onFieldValueChange={setMatchingEngineOrderSequenceNumber}
        />
      </div>
      <div className="flex gap-3">
        <button className="rounded border px-3 py-2 text-sm" onClick={checkStatus} type="button">
          Check status
        </button>
        <button className="rounded border px-3 py-2 text-sm" onClick={cancelOrder} type="button">
          Cancel
        </button>
      </div>
      {errorMessage && <p className="text-sm text-red-600">{errorMessage}</p>}
      {statusResult && <pre className="overflow-x-auto rounded bg-neutral-50 p-3 text-xs">{JSON.stringify(statusResult, null, 2)}</pre>}
      {cancelResult && (
        <p className="text-sm">
          {cancelResult.wasOrderCancelled
            ? "Order cancelled."
            : cancelResult.errorMessage
              ? `Cancel failed: ${cancelResult.errorMessage}`
              : "No matching order found to cancel (already filled, cancelled, or never existed)."}
        </p>
      )}
    </section>
  );
}

// ---------------------------------------------------------------------
// Shared field components
// ---------------------------------------------------------------------

function LabeledTextField(props: {
  labelText: string;
  fieldValue: string;
  onFieldValueChange: (nextValue: string) => void;
}) {
  return (
    <label className="flex flex-col gap-1 text-sm">
      {props.labelText}
      <input
        className="rounded border px-3 py-2"
        type="text"
        value={props.fieldValue}
        onChange={(changeEvent) => props.onFieldValueChange(changeEvent.target.value)}
      />
    </label>
  );
}

function LabeledNumberField(props: {
  labelText: string;
  fieldValue: number;
  onFieldValueChange: (nextValue: number) => void;
  isDisabled?: boolean;
}) {
  return (
    <label className="flex flex-col gap-1 text-sm">
      {props.labelText}
      <input
        className="rounded border px-3 py-2 disabled:opacity-50"
        type="number"
        disabled={props.isDisabled}
        value={props.fieldValue}
        onChange={(changeEvent) => props.onFieldValueChange(Number(changeEvent.target.value))}
      />
    </label>
  );
}
