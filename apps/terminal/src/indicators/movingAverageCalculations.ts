// Mercurius / terminal — shared moving-average building blocks used by the
// MACD/RSI/Bollinger Band indicator calculators. FEATURES.md §10
// "[P2] WebGL/Canvas candlestick charts with indicator overlays (MACD,
// RSI, BB, Fib)".
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

/** Simple moving average — a plain arithmetic mean over a sliding window
 * of `period` values. Returns one output per input index once the window
 * is full, i.e. `values.length - period + 1` outputs, aligned to the END
 * of each window (output[0] corresponds to values[period-1]). */
export function calculateSimpleMovingAverage(values: number[], period: number): number[] {
  if (period <= 0) throw new Error("period must be a positive integer");
  if (values.length < period) return [];

  const movingAverages: number[] = [];
  let windowSum = 0;
  for (let index = 0; index < values.length; index++) {
    windowSum += values[index];
    if (index >= period) {
      windowSum -= values[index - period];
    }
    if (index >= period - 1) {
      movingAverages.push(windowSum / period);
    }
  }
  return movingAverages;
}

/** Population standard deviation over each sliding window of `period`
 * values — the same window alignment as `calculateSimpleMovingAverage`
 * (used together to build Bollinger Bands, whose bands are conventionally
 * defined against population, not sample, standard deviation). */
export function calculateRollingPopulationStandardDeviation(values: number[], period: number): number[] {
  if (period <= 0) throw new Error("period must be a positive integer");
  if (values.length < period) return [];

  const deviations: number[] = [];
  for (let windowEnd = period; windowEnd <= values.length; windowEnd++) {
    const window = values.slice(windowEnd - period, windowEnd);
    const mean = window.reduce((sum, value) => sum + value, 0) / period;
    const variance = window.reduce((sum, value) => sum + (value - mean) ** 2, 0) / period;
    deviations.push(Math.sqrt(variance));
  }
  return deviations;
}

/** Exponential moving average, seeded with a simple moving average of the
 * first `period` values (the standard convention — see Wilder/most
 * charting-package implementations) and then recursively smoothed with
 * multiplier `2 / (period + 1)`. Output is aligned the same way as
 * `calculateSimpleMovingAverage`: output[0] corresponds to values[period-1]. */
export function calculateExponentialMovingAverage(values: number[], period: number): number[] {
  if (period <= 0) throw new Error("period must be a positive integer");
  if (values.length < period) return [];

  const smoothingMultiplier = 2 / (period + 1);
  const seedSimpleMovingAverage =
    values.slice(0, period).reduce((sum, value) => sum + value, 0) / period;

  const exponentialMovingAverages: number[] = [seedSimpleMovingAverage];
  for (let index = period; index < values.length; index++) {
    const previousEma = exponentialMovingAverages[exponentialMovingAverages.length - 1];
    const nextEma = (values[index] - previousEma) * smoothingMultiplier + previousEma;
    exponentialMovingAverages.push(nextEma);
  }
  return exponentialMovingAverages;
}
