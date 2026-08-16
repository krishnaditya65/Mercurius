"use client";

// Mercurius / web — Push notifications: order fills, price alerts,
// margin calls (FEATURES.md §11 "[P2] Push notifications").
//
// Browsers can't receive real server push without a backend push
// service (no Firebase/APNs/web-push infra anywhere in this repo), so
// this is genuinely NOT server push — it's the real Web Notifications
// API (`Notification.requestPermission()` / `new Notification(...)`)
// driven by real CLIENT-SIDE polling against backends that actually
// produce the underlying events today:
//
//   1. ORDER FILLS — polls oms-gateway's real `GET /audit-trail?
//      accountId=...` and fires a browser notification for every NEW
//      ORDER_FILLED / PAPER_ORDER_FILLED entry since the last poll.
//      HONEST GAP: audittrail.Append for a fill is only called inside
//      the request handler of the order that CROSSED (the aggressor) —
//      see cmd/server/main.go's processOrderSubmission. A resting LIMIT
//      order that gets filled later by someone else's incoming crossing
//      order is NOT guaranteed to get its own ORDER_FILLED entry
//      attributed to the resting order's owner. This reliably notifies
//      you for orders that filled immediately (market orders, or a
//      limit order that crossed the book right away) — the common case
//      for a demo — but is not a complete "notify me the instant ANY of
//      my resting orders fills" guarantee. A real build wants a genuine
//      per-account fill event stream (or oms-gateway attributing a
//      PASSIVE_FILL audit entry to the resting side too), not polling.
//
//   2. PRICE ALERTS — uses market-data's real, already-existing
//      `src/pricealerts.rs` feature end to end: this component can
//      create a real alert via `POST /alerts/create`, then polls
//      `GET /alerts?accountIdentifier=...` and fires a browser
//      notification the moment a previously-untriggered alert flips
//      `isTriggered: true` — which only happens when a REAL trade tick
//      crosses the threshold (see that file's own doc comment). Not
//      fabricated in any way; genuinely real market data drives this.
//
//   3. MARGIN CALLS — **NO REAL EVENT-DRIVEN TRIGGER EXISTS FOR THIS
//      TODAY.** oms-gateway's risk/margin state
//      (internal/riskengine/internal/marginpledge) is not event-driven
//      and exposes no "available margin dropped below maintenance"
//      concept or endpoint — margin is only ever surfaced as a side
//      effect of a pledge/unpledge/funding call, or implicitly via an
//      order getting rejected with INSUFFICIENT_MARGIN. Rather than
//      fabricate a margin-call event that doesn't exist, this section
//      wires a clearly-labeled BEST-EFFORT heuristic: it polls the same
//      real `GET /audit-trail` feed and fires a notification for any
//      NEW `ORDER_REJECTED` entry whose real `detailMessage` mentions
//      insufficient margin. This is a genuine signal (a real order was
//      genuinely rejected for real insufficient margin) but it is NOT a
//      real margin call — a real margin call fires on an EXISTING open
//      position's mark-to-market breaching a maintenance threshold,
//      independent of whether you submit any new order at all, and
//      nothing in this repo computes or watches that continuously yet.
//      Labeled as best-effort/approximate everywhere it appears in this
//      UI, never as "margin call".
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useEffect, useRef, useState } from "react";
import { loadSession } from "../session/authSession";

const omsGatewayBaseUrl = process.env.NEXT_PUBLIC_OMS_GATEWAY_BASE_URL ?? "http://localhost:8081";
const marketDataBaseUrl = process.env.NEXT_PUBLIC_MARKET_DATA_BASE_URL ?? "http://localhost:9103";

const NOTIFICATION_POLL_INTERVAL_MILLISECONDS = 4_000;

// A stable, unique id per rendered log entry — the log is prepended to
// (newest first) and truncated to 20 entries on every new arrival, so
// every entry's array INDEX shifts on the next append. Keying <li> by
// index would violate React's key-stability contract; this monotonic
// counter (unique regardless of how fast entries arrive, unlike a bare
// timestamp) gives each entry a real identity instead.
let notificationLogEntryIdCounter = 0;
function nextNotificationLogEntryId(): number {
  notificationLogEntryIdCounter += 1;
  return notificationLogEntryIdCounter;
}

type NotificationLogEntry = {
  logEntryId: number;
  message: string;
};

type AuditTrailEntry = {
  recordedAtTime: string;
  eventType: string;
  clientAccountIdentifier?: string;
  instrumentSymbol?: string;
  matchingEngineOrderSequenceNumber?: number;
  detailMessage?: string;
};

type PriceAlert = {
  alertId: number;
  accountIdentifier: string;
  instrumentSymbol: string;
  isAboveNotBelow: boolean;
  thresholdPriceInMinorUnits: number;
  isTriggered: boolean;
  triggeredAtEpochSeconds?: number;
};

function showBrowserNotification(title: string, body: string) {
  if (typeof window === "undefined" || !("Notification" in window)) return;
  if (Notification.permission !== "granted") return;
  new Notification(title, { body });
}

// A stable, per-entry identity for de-duplicating audit-trail entries
// across polls — the API doesn't hand out an entry id, so this combines
// the fields that make an entry unique in practice.
function auditEntryIdentity(entry: AuditTrailEntry): string {
  return `${entry.recordedAtTime}|${entry.eventType}|${entry.instrumentSymbol ?? ""}|${entry.matchingEngineOrderSequenceNumber ?? ""}`;
}

export default function NotificationCenterSection() {
  const [notificationPermission, setNotificationPermission] = useState<NotificationPermission | "unsupported">(
    "default"
  );
  const [accountIdentifier, setAccountIdentifier] = useState("acct-001");
  const [isOrderFillWatchEnabled, setIsOrderFillWatchEnabled] = useState(false);
  const [isMarginCallWatchEnabled, setIsMarginCallWatchEnabled] = useState(false);
  const [recentNotificationLog, setRecentNotificationLog] = useState<NotificationLogEntry[]>([]);

  const [priceAlertSymbol, setPriceAlertSymbol] = useState("DEMO-EQ");
  const [priceAlertIsAbove, setPriceAlertIsAbove] = useState(true);
  const [priceAlertThreshold, setPriceAlertThreshold] = useState(100);
  const [isPriceAlertWatchEnabled, setIsPriceAlertWatchEnabled] = useState(false);
  const [priceAlertStatusMessage, setPriceAlertStatusMessage] = useState<string | null>(null);

  // Sets of entry/alert identities already notified-on, so a poll never
  // re-fires a notification for something already surfaced. Refs (not
  // state) because they're pure bookkeeping the render never needs to
  // read.
  const seenAuditEntryIdentities = useRef<Set<string>>(new Set());
  const seenTriggeredAlertIds = useRef<Set<number>>(new Set());
  const hasWarnedAboutMissingSessionForAuditTrail = useRef(false);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setNotificationPermission(
      typeof window !== "undefined" && "Notification" in window ? Notification.permission : "unsupported"
    );
  }, []);

  function appendToNotificationLog(message: string) {
    setRecentNotificationLog((previousLog) =>
      [{ logEntryId: nextNotificationLogEntryId(), message }, ...previousLog].slice(0, 20)
    );
  }

  async function requestBrowserNotificationPermission() {
    if (typeof window === "undefined" || !("Notification" in window)) return;
    const permission = await Notification.requestPermission();
    setNotificationPermission(permission);
  }

  // Real Web Notifications API integration, polling oms-gateway's real
  // audit trail for order fills and (best-effort) margin-related
  // rejections. One shared poll loop covers both watches so a running
  // demo only opens one interval, not two.
  useEffect(() => {
    if (!isOrderFillWatchEnabled && !isMarginCallWatchEnabled) return;

    async function pollAuditTrailOnce() {
      const storedSession = loadSession();
      if (!storedSession?.accessToken) {
        if (!hasWarnedAboutMissingSessionForAuditTrail.current) {
          hasWarnedAboutMissingSessionForAuditTrail.current = true;
          appendToNotificationLog("Log in first from the dashboard's Account panel to watch order fills / margin rejections.");
        }
        return;
      }
      hasWarnedAboutMissingSessionForAuditTrail.current = false;
      try {
        const httpResponse = await fetch(
          `${omsGatewayBaseUrl}/audit-trail?accountId=${encodeURIComponent(accountIdentifier)}`,
          { headers: { Authorization: `Bearer ${storedSession.accessToken}` } }
        );
        if (!httpResponse.ok) return;
        const entries: AuditTrailEntry[] = await httpResponse.json();

        for (const entry of entries) {
          const identity = auditEntryIdentity(entry);
          if (seenAuditEntryIdentities.current.has(identity)) continue;
          seenAuditEntryIdentities.current.add(identity);

          const isFillEvent = entry.eventType === "ORDER_FILLED" || entry.eventType === "PAPER_ORDER_FILLED";
          const isMarginRejection =
            entry.eventType === "ORDER_REJECTED" &&
            (entry.detailMessage ?? "").toUpperCase().includes("MARGIN");

          if (isOrderFillWatchEnabled && isFillEvent) {
            const title = "Order filled";
            const body = `${entry.instrumentSymbol ?? "an order"} — ${entry.detailMessage ?? "filled"}`;
            showBrowserNotification(title, body);
            appendToNotificationLog(`[order fill] ${entry.instrumentSymbol ?? ""}: ${entry.detailMessage ?? ""}`);
          }

          if (isMarginCallWatchEnabled && isMarginRejection) {
            const title = "Best-effort margin alert (NOT a real margin call)";
            const body = `Order rejected for insufficient margin: ${entry.detailMessage ?? ""}`;
            showBrowserNotification(title, body);
            appendToNotificationLog(`[best-effort margin] ${entry.detailMessage ?? ""}`);
          }
        }
      } catch {
        // Transient poll failure (backend not running) — silently retry
        // on the next tick, same tolerance as the rest of this app's
        // polling loops.
      }
    }

    pollAuditTrailOnce();
    const intervalId = setInterval(pollAuditTrailOnce, NOTIFICATION_POLL_INTERVAL_MILLISECONDS);
    return () => clearInterval(intervalId);
  }, [isOrderFillWatchEnabled, isMarginCallWatchEnabled, accountIdentifier]);

  // Real price-alert wiring: polls market-data's real GET /alerts and
  // fires a browser notification the instant a previously-untriggered
  // alert flips to triggered (which only happens off a real trade tick,
  // per pricealerts.rs).
  useEffect(() => {
    if (!isPriceAlertWatchEnabled) return;

    async function pollPriceAlertsOnce() {
      try {
        const httpResponse = await fetch(
          `${marketDataBaseUrl}/alerts?accountIdentifier=${encodeURIComponent(accountIdentifier)}`
        );
        if (!httpResponse.ok) return;
        const alerts: PriceAlert[] = await httpResponse.json();

        for (const alert of alerts) {
          if (!alert.isTriggered) continue;
          if (seenTriggeredAlertIds.current.has(alert.alertId)) continue;
          seenTriggeredAlertIds.current.add(alert.alertId);

          const direction = alert.isAboveNotBelow ? "at/above" : "at/below";
          const title = `Price alert: ${alert.instrumentSymbol}`;
          const body = `Crossed ${direction} ${alert.thresholdPriceInMinorUnits}`;
          showBrowserNotification(title, body);
          appendToNotificationLog(`[price alert] ${alert.instrumentSymbol} ${direction} ${alert.thresholdPriceInMinorUnits}`);
        }
      } catch {
        // Same tolerance as the audit-trail poll above.
      }
    }

    pollPriceAlertsOnce();
    const intervalId = setInterval(pollPriceAlertsOnce, NOTIFICATION_POLL_INTERVAL_MILLISECONDS);
    return () => clearInterval(intervalId);
  }, [isPriceAlertWatchEnabled, accountIdentifier]);

  async function createPriceAlertThenWatch() {
    setPriceAlertStatusMessage(null);
    try {
      const httpResponse = await fetch(`${marketDataBaseUrl}/alerts/create`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          accountIdentifier,
          instrumentSymbol: priceAlertSymbol,
          isAboveNotBelow: priceAlertIsAbove,
          thresholdPriceInMinorUnits: priceAlertThreshold,
        }),
      });
      if (!httpResponse.ok) throw new Error(`HTTP ${httpResponse.status}`);
      const parsed: { alertId: number } = await httpResponse.json();
      setPriceAlertStatusMessage(
        `Created alert #${parsed.alertId} — watching for it to trigger off a real trade print.`
      );
      setIsPriceAlertWatchEnabled(true);
    } catch (thrownError) {
      setPriceAlertStatusMessage(
        thrownError instanceof Error
          ? `Couldn't reach market-data: ${thrownError.message}. Is it running on ${marketDataBaseUrl}?`
          : "Unknown error creating price alert."
      );
    }
  }

  return (
    <section className="flex flex-col gap-4 rounded border border-neutral-200 p-4">
      <h2 className="text-lg font-medium">Notifications (real Web Notifications API)</h2>

      <div className="flex flex-wrap items-center gap-3 text-sm">
        <span>
          Browser permission: <strong>{notificationPermission}</strong>
        </span>
        {notificationPermission !== "granted" && notificationPermission !== "unsupported" && (
          <button className="rounded border px-3 py-2" onClick={requestBrowserNotificationPermission} type="button">
            Enable browser notifications
          </button>
        )}
        {notificationPermission === "unsupported" && (
          <span className="text-neutral-500">This browser/context doesn&apos;t support the Notifications API.</span>
        )}
      </div>

      <label className="flex flex-col gap-1 text-sm">
        Account to watch
        <input
          className="w-64 rounded border px-3 py-2"
          value={accountIdentifier}
          onChange={(changeEvent) => setAccountIdentifier(changeEvent.target.value)}
        />
      </label>

      <div className="flex flex-col gap-3 rounded border border-neutral-100 bg-neutral-50 p-3">
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={isOrderFillWatchEnabled}
            onChange={(changeEvent) => setIsOrderFillWatchEnabled(changeEvent.target.checked)}
          />
          Notify on order fills (polls real <code>GET /audit-trail</code> every{" "}
          {NOTIFICATION_POLL_INTERVAL_MILLISECONDS / 1000}s)
        </label>

        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={isMarginCallWatchEnabled}
            onChange={(changeEvent) => setIsMarginCallWatchEnabled(changeEvent.target.checked)}
          />
          Best-effort margin alert — <strong>NOT a real margin call</strong> (fires on a real
          INSUFFICIENT_MARGIN order rejection only; see file header comment for the honest gap)
        </label>
      </div>

      <div className="flex flex-col gap-3 rounded border border-neutral-100 bg-neutral-50 p-3">
        <p className="text-sm font-medium">Real price alert (market-data)</p>
        <div className="flex flex-wrap items-end gap-3 text-sm">
          <label className="flex flex-col gap-1">
            Symbol
            <input
              className="rounded border px-3 py-2"
              value={priceAlertSymbol}
              onChange={(changeEvent) => setPriceAlertSymbol(changeEvent.target.value)}
            />
          </label>
          <label className="flex flex-col gap-1">
            Direction
            <select
              className="rounded border px-3 py-2"
              value={priceAlertIsAbove ? "above" : "below"}
              onChange={(changeEvent) => setPriceAlertIsAbove(changeEvent.target.value === "above")}
            >
              <option value="above">At/above</option>
              <option value="below">At/below</option>
            </select>
          </label>
          <label className="flex flex-col gap-1">
            Threshold (minor units)
            <input
              className="rounded border px-3 py-2"
              type="number"
              value={priceAlertThreshold}
              onChange={(changeEvent) => setPriceAlertThreshold(Number(changeEvent.target.value))}
            />
          </label>
          <button className="rounded bg-black px-3 py-2 text-white" onClick={createPriceAlertThenWatch} type="button">
            Create + watch
          </button>
          <label className="flex items-center gap-2">
            <input
              type="checkbox"
              checked={isPriceAlertWatchEnabled}
              onChange={(changeEvent) => setIsPriceAlertWatchEnabled(changeEvent.target.checked)}
            />
            Watch existing alerts for this account
          </label>
        </div>
        {priceAlertStatusMessage && <p className="text-xs text-neutral-500">{priceAlertStatusMessage}</p>}
      </div>

      {recentNotificationLog.length > 0 && (
        <div className="flex flex-col gap-1">
          <p className="text-sm font-medium">Recent notifications fired</p>
          <ul className="max-h-40 overflow-y-auto rounded bg-neutral-50 p-2 text-xs">
            {recentNotificationLog.map((logEntry) => (
              <li key={logEntry.logEntryId}>{logEntry.message}</li>
            ))}
          </ul>
        </div>
      )}
    </section>
  );
}
