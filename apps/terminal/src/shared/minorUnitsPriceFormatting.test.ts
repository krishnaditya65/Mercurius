import { describe, expect, it } from "vitest";
import { formatMinorUnitsAsDisplayPrice } from "./minorUnitsPriceFormatting";

describe("formatMinorUnitsAsDisplayPrice", () => {
  it("divides minor units by 100 and formats with two decimal places", () => {
    expect(formatMinorUnitsAsDisplayPrice(12345)).toBe("123.45");
  });

  it("pads a whole-number major-unit price with trailing zeros", () => {
    expect(formatMinorUnitsAsDisplayPrice(10000)).toBe("100.00");
  });

  it("handles zero", () => {
    expect(formatMinorUnitsAsDisplayPrice(0)).toBe("0.00");
  });

  it("handles single-digit minor-unit remainders correctly", () => {
    expect(formatMinorUnitsAsDisplayPrice(105)).toBe("1.05");
  });
});
