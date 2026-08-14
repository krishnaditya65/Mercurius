import { describe, expect, it } from "vitest";
import {
  calculateExponentialMovingAverage,
  calculateSimpleMovingAverage,
} from "./movingAverageCalculations";
import { calculateMovingAverageConvergenceDivergence } from "./macdIndicatorCalculator";
import { calculateRelativeStrengthIndex } from "./rsiIndicatorCalculator";
import { calculateBollingerBands } from "./bollingerBandsCalculator";
import {
  calculateFibonacciRetracementLevels,
  FIBONACCI_RETRACEMENT_RATIOS,
} from "./fibonacciRetracementCalculator";

describe("calculateSimpleMovingAverage", () => {
  it("computes a plain windowed average, aligned to the end of each window", () => {
    expect(calculateSimpleMovingAverage([1, 2, 3, 4, 5, 6], 3)).toEqual([2, 3, 4, 5]);
  });

  it("returns an empty array when there isn't enough data", () => {
    expect(calculateSimpleMovingAverage([1, 2], 3)).toEqual([]);
  });
});

describe("calculateExponentialMovingAverage", () => {
  it("matches a hand-computed EMA(period=3) over [1,2,3,4,5,6]", () => {
    // Seed = SMA(1,2,3) = 2. multiplier = 2/(3+1) = 0.5.
    // ema(4) = (4-2)*0.5+2 = 3
    // ema(5) = (5-3)*0.5+3 = 4
    // ema(6) = (6-4)*0.5+4 = 5
    expect(calculateExponentialMovingAverage([1, 2, 3, 4, 5, 6], 3)).toEqual([2, 3, 4, 5]);
  });
});

describe("calculateRelativeStrengthIndex (Wilder's method)", () => {
  // Hand-derived (see the task notes in this repo's history for the exact
  // fraction-by-fraction derivation) using prices
  // [100, 102, 101, 103, 104, 102, 105] and period=3:
  //   RSI values: 80, 1100/13, 50, (100 - 4400/169)
  it("matches an exact hand-computed sequence for a small dataset", () => {
    const prices = [100, 102, 101, 103, 104, 102, 105];
    const rsiValues = calculateRelativeStrengthIndex(prices, 3);
    expect(rsiValues).toHaveLength(4);
    expect(rsiValues[0]).toBeCloseTo(80, 6);
    expect(rsiValues[1]).toBeCloseTo(1100 / 13, 6);
    expect(rsiValues[2]).toBeCloseTo(50, 6);
    expect(rsiValues[3]).toBeCloseTo(100 - 4400 / 169, 6);
  });

  it("returns 100 for a monotonically increasing series (no losses at all)", () => {
    const prices = [1, 2, 3, 4, 5, 6, 7, 8];
    const rsiValues = calculateRelativeStrengthIndex(prices, 3);
    for (const rsi of rsiValues) {
      expect(rsi).toBe(100);
    }
  });

  it("returns 0 for a monotonically decreasing series (no gains at all)", () => {
    const prices = [8, 7, 6, 5, 4, 3, 2, 1];
    const rsiValues = calculateRelativeStrengthIndex(prices, 3);
    for (const rsi of rsiValues) {
      expect(rsi).toBe(0);
    }
  });

  it("stays within [0, 100] for a longer, noisier series", () => {
    const prices = [
      44.34, 44.09, 44.15, 43.61, 44.33, 44.83, 45.1, 45.42, 45.84, 46.08, 45.89, 46.03, 45.61,
      46.28, 46.28, 46.0, 46.03, 46.41, 46.22, 45.64,
    ];
    const rsiValues = calculateRelativeStrengthIndex(prices, 14);
    expect(rsiValues.length).toBeGreaterThan(0);
    for (const rsi of rsiValues) {
      expect(rsi).toBeGreaterThanOrEqual(0);
      expect(rsi).toBeLessThanOrEqual(100);
    }
  });
});

describe("calculateMovingAverageConvergenceDivergence", () => {
  it("throws when fastPeriod is not smaller than slowPeriod", () => {
    expect(() => calculateMovingAverageConvergenceDivergence([1, 2, 3], 26, 12)).toThrow();
  });

  it("produces macdLine as the difference of fast/slow EMAs, correctly aligned", () => {
    // Use small periods (fast=2, slow=3) over an easy-to-hand-verify series.
    const closingPrices = [1, 2, 3, 4, 5, 6, 7, 8];
    const result = calculateMovingAverageConvergenceDivergence(closingPrices, 2, 3, 2);

    // fastEma(period=2) seed = SMA(1,2)=1.5, multiplier=2/3
    //   ema(3)=(3-1.5)*2/3+1.5=2.5, ema(4)=(4-2.5)*2/3+2.5=3.5, ema(5)=4.5,
    //   ema(6)=5.5, ema(7)=6.5, ema(8)=7.5
    // slowEma(period=3) seed = SMA(1,2,3)=2, multiplier=0.5
    //   ema(4)=(4-2)*0.5+2=3, ema(5)=4, ema(6)=5, ema(7)=6, ema(8)=7
    // macdLine aligns to slowEma's start (index for price=3 onward, since
    // slowEma(period=3) over 8 values produces 8-3+1=6 outputs):
    //   fast@3=2.5, slow@3=2   -> 0.5
    //   fast@4=3.5, slow@4=3   -> 0.5
    //   fast@5=4.5, slow@5=4   -> 0.5
    //   fast@6=5.5, slow@6=5   -> 0.5
    //   fast@7=6.5, slow@7=6   -> 0.5
    //   fast@8=7.5, slow@8=7   -> 0.5
    expect(result.macdLine.length).toBe(6);
    for (const macdValue of result.macdLine) {
      expect(macdValue).toBeCloseTo(0.5, 9);
    }
  });

  it("produces an empty result when there isn't enough data for the slow EMA", () => {
    const result = calculateMovingAverageConvergenceDivergence([1, 2, 3], 12, 26, 9);
    expect(result).toEqual({ macdLine: [], signalLine: [], histogram: [] });
  });
});

describe("calculateBollingerBands", () => {
  it("matches hand-computed bands for a linear series", () => {
    // prices [1,2,3,4,5,6], period=3, multiplier=2.
    // window [1,2,3]: mean=2, populationVariance=((1)^2+0+(1)^2)/3=2/3, std=sqrt(2/3)
    const expectedStdDev = Math.sqrt(2 / 3);
    const result = calculateBollingerBands([1, 2, 3, 4, 5, 6], 3, 2);
    expect(result.middleBand[0]).toBeCloseTo(2, 9);
    expect(result.upperBand[0]).toBeCloseTo(2 + 2 * expectedStdDev, 9);
    expect(result.lowerBand[0]).toBeCloseTo(2 - 2 * expectedStdDev, 9);
  });

  it("keeps the upper band above the middle band and the lower band below it", () => {
    const prices = [10, 12, 9, 15, 11, 13, 8, 14, 16, 10, 12, 9];
    const result = calculateBollingerBands(prices, 5, 2);
    for (let index = 0; index < result.middleBand.length; index++) {
      expect(result.upperBand[index]).toBeGreaterThanOrEqual(result.middleBand[index]);
      expect(result.lowerBand[index]).toBeLessThanOrEqual(result.middleBand[index]);
    }
  });
});

describe("calculateFibonacciRetracementLevels", () => {
  it("computes the standard retracement levels for an uptrend swing (high=100, low=50)", () => {
    const levels = calculateFibonacciRetracementLevels(100, 50, "uptrend");
    const expectedPrices = [100, 88.2, 80.9, 75, 69.1, 60.7, 50];
    levels.forEach((level, index) => expect(level.price).toBeCloseTo(expectedPrices[index], 9));
  });

  it("computes the downtrend levels as swingLow + ratio * range (not simply the uptrend list reversed, since the ratio set isn't symmetric around 0.5)", () => {
    const levels = calculateFibonacciRetracementLevels(100, 50, "downtrend");
    const expectedPrices = [50, 61.8, 69.1, 75, 80.9, 89.3, 100];
    levels.forEach((level, index) => expect(level.price).toBeCloseTo(expectedPrices[index], 9));
  });

  it("includes every standard ratio exactly once, in ascending order", () => {
    const levels = calculateFibonacciRetracementLevels(200, 100, "uptrend");
    expect(levels.map((level) => level.ratio)).toEqual([...FIBONACCI_RETRACEMENT_RATIOS]);
  });

  it("rejects a swingHigh that isn't above swingLow", () => {
    expect(() => calculateFibonacciRetracementLevels(50, 100, "uptrend")).toThrow();
    expect(() => calculateFibonacciRetracementLevels(50, 50, "uptrend")).toThrow();
  });
});
