import { describe, expect, it } from "vitest";
import { shouldRenderFibonacciOverlay } from "./CandlestickChartCanvas";
import { calculateFibonacciRetracementLevels } from "../indicators/fibonacciRetracementCalculator";

describe("shouldRenderFibonacciOverlay", () => {
  it("returns false when the overlay isn't toggled on, regardless of range", () => {
    expect(shouldRenderFibonacciOverlay(false, 100, 50)).toBe(false);
  });

  it("returns true when toggled on and swingHigh is strictly above swingLow", () => {
    expect(shouldRenderFibonacciOverlay(true, 100, 50)).toBe(true);
  });

  it("returns false for a flat price range (swingHigh === swingLow) — the case that used to crash the chart", () => {
    expect(shouldRenderFibonacciOverlay(true, 100, 100)).toBe(false);
  });

  it("returns false for an inverted range (swingHigh < swingLow), also guarded defensively", () => {
    expect(shouldRenderFibonacciOverlay(true, 50, 100)).toBe(false);
  });

  it("never calls into a range for which calculateFibonacciRetracementLevels would throw", () => {
    // Sanity check tying the guard directly to the throwing function: any
    // (swingHigh, swingLow) pair the guard says "false" for must indeed be
    // a pair the calculator rejects, and vice versa for "true" pairs.
    const flatOrInverted: [number, number][] = [
      [100, 100],
      [50, 100],
      [0, 0],
    ];
    for (const [swingHigh, swingLow] of flatOrInverted) {
      expect(shouldRenderFibonacciOverlay(true, swingHigh, swingLow)).toBe(false);
      expect(() => calculateFibonacciRetracementLevels(swingHigh, swingLow)).toThrow();
    }

    const validRanges: [number, number][] = [
      [100, 50],
      [1, 0],
    ];
    for (const [swingHigh, swingLow] of validRanges) {
      expect(shouldRenderFibonacciOverlay(true, swingHigh, swingLow)).toBe(true);
      expect(() => calculateFibonacciRetracementLevels(swingHigh, swingLow)).not.toThrow();
    }
  });
});
