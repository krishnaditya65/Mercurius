// Mercurius / terminal — MACD (Moving Average Convergence/Divergence).
//
// FEATURES.md §10's chart indicator overlays. Standard definition:
//   macdLine   = EMA(closingPrices, fastPeriod) - EMA(closingPrices, slowPeriod)
//   signalLine = EMA(macdLine, signalPeriod)
//   histogram  = macdLine - signalLine
// Default periods (12/26/9) are the conventional Gerald Appel defaults.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { calculateExponentialMovingAverage } from "./movingAverageCalculations";

export type MacdIndicatorResult = {
  macdLine: number[];
  signalLine: number[];
  histogram: number[];
};

export function calculateMovingAverageConvergenceDivergence(
  closingPrices: number[],
  fastPeriod = 12,
  slowPeriod = 26,
  signalPeriod = 9
): MacdIndicatorResult {
  if (fastPeriod >= slowPeriod) {
    throw new Error("fastPeriod must be smaller than slowPeriod");
  }

  const fastExponentialMovingAverage = calculateExponentialMovingAverage(closingPrices, fastPeriod);
  const slowExponentialMovingAverage = calculateExponentialMovingAverage(closingPrices, slowPeriod);

  if (slowExponentialMovingAverage.length === 0) {
    // Not enough data yet for even the slow EMA to produce a single value.
    return { macdLine: [], signalLine: [], histogram: [] };
  }

  // fastExponentialMovingAverage starts (fastPeriod-1) into `closingPrices`;
  // slowExponentialMovingAverage starts (slowPeriod-1) in. Align both to the
  // slow EMA's start index before subtracting.
  const alignmentOffset = slowPeriod - fastPeriod;
  const macdLine = slowExponentialMovingAverage.map((slowValue, index) => {
    const fastValue = fastExponentialMovingAverage[index + alignmentOffset];
    return fastValue - slowValue;
  });

  const signalLine = calculateExponentialMovingAverage(macdLine, signalPeriod);
  // signalLine starts (signalPeriod-1) into macdLine — align histogram to
  // signalLine's (shorter) length.
  const signalAlignmentOffset = signalPeriod - 1;
  const histogram = signalLine.map((signalValue, index) => {
    const macdValue = macdLine[index + signalAlignmentOffset];
    return macdValue - signalValue;
  });

  return { macdLine, signalLine, histogram };
}
