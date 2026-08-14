// Mercurius / terminal — DOM ladder widget with click-to-trade.
//
// FEATURES.md §10 "[P2] DOM ladder widget with click-to-trade". Polls
// matching-engine's real `GET /domReplay` endpoint (same one apps/web's
// domReplay page uses, read-only reference — see that page's header
// comment for the WAL-replay mechanics) for the CURRENT book depth
// (calling it with no start/end window returns the full/most-recent
// snapshot set; this widget just takes the last snapshot), and renders it
// as a genuine price ladder: one row per price level, clicking a row's
// BUY/SELL cell submits a real LIMIT order at that exact price via
// `domLadderOrderSubmission.ts` -> oms-gateway's `POST /orders/submit`.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useEffect, useState } from "react";
import {
  submitLadderClickToTradeOrder,
  type LadderOrderSide,
  type OrderAcknowledgementResponse,
} from "./domLadderOrderSubmission";

type DomReplaySnapshot = {
  epochMillis: number;
  walEventIndex: number;
  bidLevelsBestFirst: [number, number][];
  askLevelsBestFirst: [number, number][];
};

type LadderRow = {
  priceInMinorUnits: number;
  bidQuantity: number | null;
  askQuantity: number | null;
};

const LADDER_POLL_INTERVAL_MILLISECONDS = 3_000;

export function DomLadderWidget(props: {
  matchingEngineDomReplayBaseUrl: string;
  omsGatewayBaseUrl: string;
  instrumentSymbol: string;
  clientAccountIdentifier: string;
  defaultOrderQuantity?: number;
}) {
  const {
    matchingEngineDomReplayBaseUrl,
    omsGatewayBaseUrl,
    instrumentSymbol,
    clientAccountIdentifier,
    defaultOrderQuantity = 5,
  } = props;

  const [ladderRows, setLadderRows] = useState<LadderRow[]>([]);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [lastOrderStatusMessage, setLastOrderStatusMessage] = useState<string | null>(null);
  const [isSubmittingPriceLevel, setIsSubmittingPriceLevel] = useState<number | null>(null);

  useEffect(() => {
    let cancelled = false;

    async function refreshLadder() {
      try {
        const httpResponse = await fetch(
          `${matchingEngineDomReplayBaseUrl}/domReplay?instrumentSymbol=${encodeURIComponent(instrumentSymbol)}`
        );
        if (!httpResponse.ok) throw new Error(`HTTP ${httpResponse.status}`);
        const snapshots: DomReplaySnapshot[] = await httpResponse.json();
        if (cancelled) return;
        const latestSnapshot = snapshots.length > 0 ? snapshots[snapshots.length - 1] : null;
        setLadderRows(latestSnapshot ? buildLadderRowsFromSnapshot(latestSnapshot) : []);
        setErrorMessage(null);
      } catch (thrownError) {
        if (cancelled) return;
        setErrorMessage(
          thrownError instanceof Error
            ? `Couldn't reach matching-engine: ${thrownError.message}. Is it running on ${matchingEngineDomReplayBaseUrl}?`
            : "Unknown error fetching DOM depth."
        );
      }
    }

    // eslint-disable-next-line react-hooks/set-state-in-effect -- see apps/web's PriceChartSection: same pattern, same rationale (state updates happen after an await).
    refreshLadder();
    const intervalId = setInterval(refreshLadder, LADDER_POLL_INTERVAL_MILLISECONDS);
    return () => {
      cancelled = true;
      clearInterval(intervalId);
    };
  }, [matchingEngineDomReplayBaseUrl, instrumentSymbol]);

  async function handleLadderClick(priceInMinorUnits: number, side: LadderOrderSide) {
    setIsSubmittingPriceLevel(priceInMinorUnits);
    setLastOrderStatusMessage(null);
    try {
      const acknowledgement: OrderAcknowledgementResponse = await submitLadderClickToTradeOrder(
        omsGatewayBaseUrl,
        {
          clientAccountIdentifier,
          instrumentSymbol,
          side,
          limitPriceInMinorUnits: priceInMinorUnits,
          orderQuantity: defaultOrderQuantity,
        }
      );
      setLastOrderStatusMessage(
        acknowledgement.wasOrderAccepted
          ? `${side} ${defaultOrderQuantity} @ ${priceInMinorUnits} accepted (seq ${acknowledgement.assignedGlobalSequenceNumber ?? "?"}).`
          : `Rejected: ${acknowledgement.humanReadableRejectionReason ?? "unknown reason"}`
      );
    } catch (thrownError) {
      setLastOrderStatusMessage(
        thrownError instanceof Error ? `Order failed: ${thrownError.message}` : "Order failed: unknown error."
      );
    } finally {
      setIsSubmittingPriceLevel(null);
    }
  }

  return (
    <div className="domLadderWidget">
      <div className="domLadderWidget__header">
        <strong>{instrumentSymbol}</strong> DOM ladder — click a price to trade
      </div>
      {errorMessage && <div className="domLadderWidget__error">{errorMessage}</div>}
      {ladderRows.length === 0 && !errorMessage && (
        <div className="domLadderWidget__empty">No depth yet.</div>
      )}
      {ladderRows.length > 0 && (
        <table className="domLadderWidget__table">
          <thead>
            <tr>
              <th>Bid qty</th>
              <th>Price</th>
              <th>Ask qty</th>
            </tr>
          </thead>
          <tbody>
            {ladderRows.map((row) => (
              <tr key={row.priceInMinorUnits}>
                <td
                  className="domLadderWidget__bidCell"
                  onClick={() => row.bidQuantity !== null && handleLadderClick(row.priceInMinorUnits, "SELL")}
                  title="Click to SELL at this price"
                >
                  {isSubmittingPriceLevel === row.priceInMinorUnits ? "…" : row.bidQuantity ?? ""}
                </td>
                <td className="domLadderWidget__priceCell">{row.priceInMinorUnits}</td>
                <td
                  className="domLadderWidget__askCell"
                  onClick={() => row.askQuantity !== null && handleLadderClick(row.priceInMinorUnits, "BUY")}
                  title="Click to BUY at this price"
                >
                  {isSubmittingPriceLevel === row.priceInMinorUnits ? "…" : row.askQuantity ?? ""}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {lastOrderStatusMessage && <div className="domLadderWidget__status">{lastOrderStatusMessage}</div>}
    </div>
  );
}

/** Merges bid/ask levels into one price-sorted ladder (highest price at
 * top, matching how every real DOM ladder is conventionally drawn) — pure
 * function, exported for unit testing. */
export function buildLadderRowsFromSnapshot(snapshot: DomReplaySnapshot): LadderRow[] {
  const rowsByPrice = new Map<number, LadderRow>();
  for (const [price, quantity] of snapshot.bidLevelsBestFirst) {
    rowsByPrice.set(price, { priceInMinorUnits: price, bidQuantity: quantity, askQuantity: null });
  }
  for (const [price, quantity] of snapshot.askLevelsBestFirst) {
    const existingRow = rowsByPrice.get(price);
    if (existingRow) {
      existingRow.askQuantity = quantity;
    } else {
      rowsByPrice.set(price, { priceInMinorUnits: price, bidQuantity: null, askQuantity: quantity });
    }
  }
  return Array.from(rowsByPrice.values()).sort((a, b) => b.priceInMinorUnits - a.priceInMinorUnits);
}
