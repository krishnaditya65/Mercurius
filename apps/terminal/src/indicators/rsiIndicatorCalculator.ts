// Mercurius / terminal — Relative Strength Index (RSI), Wilder's original
// smoothing method. FEATURES.md §10's chart indicator overlays.
//
// Standard definition (J. Welles Wilder, "New Concepts in Technical
// Trading Systems", 1978):
//   1. Compute period-over-period price changes.
//   2. Split into gains (positive changes, 0 otherwise) and losses
//      (absolute value of negative changes, 0 otherwise).
//   3. Seed avgGain/avgLoss as the SIMPLE average of the first `period`
//      gains/losses.
//   4. Smooth every subsequent avgGain/avgLoss with Wilder's recursive
//      formula: avg = (previousAvg * (period - 1) + currentValue) / period.
//   5. RS = avgGain / avgLoss; RSI = 100 - 100 / (1 + RS).
//   Edge case: avgLoss == 0 means every recent change was a gain — by
//   convention RSI is defined as 100 in that case (RS would be infinite).
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

export function calculateRelativeStrengthIndex(closingPrices: number[], period = 14): number[] {
  if (period <= 0) throw new Error("period must be a positive integer");
  if (closingPrices.length <= period) return [];

  const priceChanges: number[] = [];
  for (let index = 1; index < closingPrices.length; index++) {
    priceChanges.push(closingPrices[index] - closingPrices[index - 1]);
  }

  const gains = priceChanges.map((change) => Math.max(change, 0));
  const losses = priceChanges.map((change) => Math.max(-change, 0));

  let averageGain = gains.slice(0, period).reduce((sum, gain) => sum + gain, 0) / period;
  let averageLoss = losses.slice(0, period).reduce((sum, loss) => sum + loss, 0) / period;

  const rsiValues: number[] = [computeRsiFromAverages(averageGain, averageLoss)];

  for (let index = period; index < priceChanges.length; index++) {
    averageGain = (averageGain * (period - 1) + gains[index]) / period;
    averageLoss = (averageLoss * (period - 1) + losses[index]) / period;
    rsiValues.push(computeRsiFromAverages(averageGain, averageLoss));
  }

  return rsiValues;
}

function computeRsiFromAverages(averageGain: number, averageLoss: number): number {
  if (averageLoss === 0) {
    // No losses at all in the window — maximally overbought by convention,
    // and this also sidesteps a division by zero in the RS ratio below.
    return averageGain === 0 ? 50 : 100;
  }
  const relativeStrength = averageGain / averageLoss;
  return 100 - 100 / (1 + relativeStrength);
}
