import { describe, expect, it } from "vitest";
import { parseCommandBarInput } from "./commandBarParser";

describe("parseCommandBarInput", () => {
  it("parses the canonical Bloomberg-style example", () => {
    const result = parseCommandBarInput("AAPL DES <GO>");
    expect(result.wasParseSuccessful).toBe(true);
    if (result.wasParseSuccessful) {
      expect(result.command).toEqual({
        tickerSymbol: "AAPL",
        verb: "DES",
        args: [],
        wasGoTriggered: true,
      });
    }
  });

  it("parses MOD as a distinct verb from DES", () => {
    const result = parseCommandBarInput("AAPL MOD <GO>");
    expect(result.wasParseSuccessful).toBe(true);
    if (result.wasParseSuccessful) {
      expect(result.command.verb).toBe("MOD");
    }
  });

  it("is case-insensitive on ticker, verb, and GO", () => {
    const result = parseCommandBarInput("aapl des go");
    expect(result.wasParseSuccessful).toBe(true);
    if (result.wasParseSuccessful) {
      expect(result.command.tickerSymbol).toBe("AAPL");
      expect(result.command.verb).toBe("DES");
      expect(result.command.wasGoTriggered).toBe(true);
    }
  });

  it("accepts bare GO without angle brackets", () => {
    const result = parseCommandBarInput("MSFT GP GO");
    expect(result.wasParseSuccessful).toBe(true);
    if (result.wasParseSuccessful) {
      expect(result.command.wasGoTriggered).toBe(true);
    }
  });

  it("treats a command without a trailing GO as not-yet-triggered but still parseable", () => {
    const result = parseCommandBarInput("AAPL DES");
    expect(result.wasParseSuccessful).toBe(true);
    if (result.wasParseSuccessful) {
      expect(result.command.wasGoTriggered).toBe(false);
    }
  });

  it("collects trailing tokens as args", () => {
    const result = parseCommandBarInput("AAPL GP 1D <GO>");
    expect(result.wasParseSuccessful).toBe(true);
    if (result.wasParseSuccessful) {
      expect(result.command.args).toEqual(["1D"]);
    }
  });

  it("collapses repeated internal whitespace", () => {
    const result = parseCommandBarInput("AAPL    DES   <GO>");
    expect(result.wasParseSuccessful).toBe(true);
    if (result.wasParseSuccessful) {
      expect(result.command.tickerSymbol).toBe("AAPL");
    }
  });

  it("tolerates leading/trailing whitespace", () => {
    const result = parseCommandBarInput("  AAPL DES <GO>  ");
    expect(result.wasParseSuccessful).toBe(true);
  });

  it("accepts tickers with a dot (share-class style, e.g. BRK.B)", () => {
    const result = parseCommandBarInput("BRK.B DES <GO>");
    expect(result.wasParseSuccessful).toBe(true);
    if (result.wasParseSuccessful) {
      expect(result.command.tickerSymbol).toBe("BRK.B");
    }
  });

  it("accepts tickers with a hyphen (e.g. RELIANCE-BE)", () => {
    const result = parseCommandBarInput("RELIANCE-BE DOM <GO>");
    expect(result.wasParseSuccessful).toBe(true);
  });

  it("rejects an empty input", () => {
    const result = parseCommandBarInput("");
    expect(result.wasParseSuccessful).toBe(false);
  });

  it("rejects whitespace-only input", () => {
    const result = parseCommandBarInput("     ");
    expect(result.wasParseSuccessful).toBe(false);
  });

  it("rejects a single token with no verb", () => {
    const result = parseCommandBarInput("AAPL");
    expect(result.wasParseSuccessful).toBe(false);
    if (!result.wasParseSuccessful) {
      expect(result.errorMessage).toMatch(/missing a command verb/i);
    }
  });

  it("rejects a single token followed only by GO", () => {
    const result = parseCommandBarInput("AAPL <GO>");
    expect(result.wasParseSuccessful).toBe(false);
  });

  it("rejects an unknown verb", () => {
    const result = parseCommandBarInput("AAPL FOO <GO>");
    expect(result.wasParseSuccessful).toBe(false);
    if (!result.wasParseSuccessful) {
      expect(result.errorMessage).toMatch(/unknown verb/i);
    }
  });

  it("rejects a ticker that doesn't start with a letter", () => {
    const result = parseCommandBarInput("123 DES <GO>");
    expect(result.wasParseSuccessful).toBe(false);
    if (!result.wasParseSuccessful) {
      expect(result.errorMessage).toMatch(/doesn't look like a ticker/i);
    }
  });

  it("rejects a ticker longer than 15 characters", () => {
    const result = parseCommandBarInput("ABCDEFGHIJKLMNOPQ DES <GO>");
    expect(result.wasParseSuccessful).toBe(false);
  });

  it("rejects a ticker containing whitespace-adjacent punctuation only", () => {
    const result = parseCommandBarInput("!!! DES <GO>");
    expect(result.wasParseSuccessful).toBe(false);
  });

  it("exposes every verb from the shared verb table", () => {
    for (const verb of ["DES", "MOD", "GP", "DOM", "BLOTTER", "NEWS"] as const) {
      const result = parseCommandBarInput(`AAPL ${verb} <GO>`);
      expect(result.wasParseSuccessful).toBe(true);
      if (result.wasParseSuccessful) {
        expect(result.command.verb).toBe(verb);
      }
    }
  });
});
