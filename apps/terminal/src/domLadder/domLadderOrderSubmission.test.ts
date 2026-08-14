import { afterEach, describe, expect, it, vi } from "vitest";
import {
  buildLadderLimitOrderRequestBody,
  submitLadderClickToTradeOrder,
} from "./domLadderOrderSubmission";

describe("buildLadderLimitOrderRequestBody", () => {
  it("maps a BUY click to orderSideIsBuyNotSell=true and a plain limit order", () => {
    const body = buildLadderLimitOrderRequestBody({
      clientAccountIdentifier: "acct-001",
      instrumentSymbol: "DEMO-EQ",
      side: "BUY",
      limitPriceInMinorUnits: 10_050,
      orderQuantity: 10,
    });
    expect(body).toMatchObject({
      clientAccountIdentifier: "acct-001",
      instrumentSymbol: "DEMO-EQ",
      orderSideIsBuyNotSell: true,
      orderIsMarketOrderNotLimit: false,
      orderIsStopLossVariant: false,
      limitPriceInMinorUnits: 10_050,
      orderQuantity: 10,
      orderIsAfterMarketOrder: false,
    });
    expect(typeof body.idempotencyKey).toBe("string");
    expect((body.idempotencyKey as string).length).toBeGreaterThan(0);
  });

  it("maps a SELL click to orderSideIsBuyNotSell=false", () => {
    const body = buildLadderLimitOrderRequestBody({
      clientAccountIdentifier: "acct-001",
      instrumentSymbol: "DEMO-EQ",
      side: "SELL",
      limitPriceInMinorUnits: 9_950,
      orderQuantity: 5,
    });
    expect(body.orderSideIsBuyNotSell).toBe(false);
  });

  it("generates a distinct idempotency key per call", () => {
    const requestTemplate = {
      clientAccountIdentifier: "acct-001",
      instrumentSymbol: "DEMO-EQ",
      side: "BUY" as const,
      limitPriceInMinorUnits: 10_000,
      orderQuantity: 1,
    };
    const first = buildLadderLimitOrderRequestBody(requestTemplate);
    const second = buildLadderLimitOrderRequestBody(requestTemplate);
    expect(first.idempotencyKey).not.toBe(second.idempotencyKey);
  });
});

describe("submitLadderClickToTradeOrder", () => {
  const originalFetch = globalThis.fetch;

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("POSTs to {baseUrl}/orders/submit and returns the parsed acknowledgement", async () => {
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      expect(url).toBe("http://127.0.0.1:8081/orders/submit");
      expect(init?.method).toBe("POST");
      expect(init?.headers).toMatchObject({ "Content-Type": "application/json" });
      const parsedBody = JSON.parse(init!.body as string);
      expect(parsedBody.instrumentSymbol).toBe("DEMO-EQ");
      return new Response(
        JSON.stringify({ wasOrderAccepted: true, assignedGlobalSequenceNumber: 42 }),
        { status: 200 }
      );
    });
    globalThis.fetch = fetchMock as unknown as typeof fetch;

    const acknowledgement = await submitLadderClickToTradeOrder("http://127.0.0.1:8081", {
      clientAccountIdentifier: "acct-001",
      instrumentSymbol: "DEMO-EQ",
      side: "BUY",
      limitPriceInMinorUnits: 10_000,
      orderQuantity: 1,
    });

    expect(acknowledgement).toEqual({ wasOrderAccepted: true, assignedGlobalSequenceNumber: 42 });
  });

  it("throws with the HTTP status when oms-gateway responds non-OK", async () => {
    globalThis.fetch = vi.fn(async () => new Response("", { status: 500 })) as unknown as typeof fetch;

    await expect(
      submitLadderClickToTradeOrder("http://127.0.0.1:8081", {
        clientAccountIdentifier: "acct-001",
        instrumentSymbol: "DEMO-EQ",
        side: "BUY",
        limitPriceInMinorUnits: 10_000,
        orderQuantity: 1,
      })
    ).rejects.toThrow(/HTTP 500/);
  });
});
