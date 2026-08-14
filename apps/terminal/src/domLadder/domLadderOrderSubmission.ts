// Mercurius / terminal — DOM ladder click-to-trade order submission.
//
// FEATURES.md §10 "[P2] DOM ladder widget with click-to-trade". Talks to
// oms-gateway's REAL `POST /orders/submit` endpoint — same request shape
// apps/web's order ticket uses (see apps/web/app/page.tsx's
// `OrderTicketSection`, read-only reference; deliberately duplicated here
// rather than sharing a package across apps/web and apps/terminal, per
// this task's instructions). A ladder click always submits a LIMIT order
// at the clicked price level — that's the entire point of a DOM ladder
// (one-click order-at-this-price), unlike the general order ticket which
// exposes every order type.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

export type LadderOrderSide = "BUY" | "SELL";

export type LadderClickToTradeRequest = {
  clientAccountIdentifier: string;
  instrumentSymbol: string;
  side: LadderOrderSide;
  limitPriceInMinorUnits: number;
  orderQuantity: number;
};

export type OrderAcknowledgementResponse = {
  wasOrderAccepted: boolean;
  assignedGlobalSequenceNumber?: number;
  humanReadableRejectionReason?: string;
  machineReadableRejectionReason?: string;
  matchingEngineHandoffError?: string;
  matchingEngineOrderSequenceNumber?: number;
  isQueuedAsAfterMarketOrder?: boolean;
};

/** Generates a fresh idempotency key for a single ladder click — same
 * rationale/fallback as apps/web's `generateIdempotencyKey`: lets a
 * genuine double-click through this UI actually exercise oms-gateway's
 * idempotency guard, rather than the UI always inventing a new key that
 * would defeat the point of having one. */
export function generateLadderClickIdempotencyKey(): string {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) {
    return crypto.randomUUID();
  }
  return `ladder-idem-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

/** Builds the exact JSON body oms-gateway's `POST /orders/submit` expects
 * for a plain LIMIT order, matching apps/web's `OrderTicketSection` field
 * names exactly (`orderSideIsBuyNotSell`, `orderIsMarketOrderNotLimit`,
 * `orderIsStopLossVariant`, etc). Exported separately from the fetch call
 * below so the request-shape logic is unit-testable without a network
 * mock. */
export function buildLadderLimitOrderRequestBody(request: LadderClickToTradeRequest): Record<string, unknown> {
  return {
    clientAccountIdentifier: request.clientAccountIdentifier,
    instrumentSymbol: request.instrumentSymbol,
    orderSideIsBuyNotSell: request.side === "BUY",
    orderIsMarketOrderNotLimit: false,
    orderIsStopLossVariant: false,
    limitPriceInMinorUnits: request.limitPriceInMinorUnits,
    orderQuantity: request.orderQuantity,
    orderIsAfterMarketOrder: false,
    idempotencyKey: generateLadderClickIdempotencyKey(),
  };
}

export async function submitLadderClickToTradeOrder(
  omsGatewayBaseUrl: string,
  request: LadderClickToTradeRequest
): Promise<OrderAcknowledgementResponse> {
  const httpResponse = await fetch(`${omsGatewayBaseUrl}/orders/submit`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(buildLadderLimitOrderRequestBody(request)),
  });
  if (!httpResponse.ok) {
    throw new Error(`oms-gateway responded with HTTP ${httpResponse.status}`);
  }
  return httpResponse.json();
}
