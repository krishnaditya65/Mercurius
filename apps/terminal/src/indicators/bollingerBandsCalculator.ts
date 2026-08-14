// Mercurius / terminal — Bollinger Bands. FEATURES.md §10's chart
// indicator overlays.
//
// Standard definition (John Bollinger):
//   middleBand = SMA(closingPrices, period)
//   upperBand  = middleBand + (standardDeviationMultiplier * populationStdDev(window))
//   lowerBand  = middleBand - (standardDeviationMultiplier * populationStdDev(window))
// Conventional defaults: period=20, standardDeviationMultiplier=2.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import {
  calculateRollingPopulationStandardDeviation,
  calculateSimpleMovingAverage,
} from "./movingAverageCalculations";

export type BollingerBandsResult = {
  middleBand: number[];
  upperBand: number[];
  lowerBand: number[];
};

export function calculateBollingerBands(
  closingPrices: number[],
  period = 20,
  standardDeviationMultiplier = 2
): BollingerBandsResult {
  const middleBand = calculateSimpleMovingAverage(closingPrices, period);
  const rollingStandardDeviation = calculateRollingPopulationStandardDeviation(closingPrices, period);

  const upperBand = middleBand.map(
    (mean, index) => mean + standardDeviationMultiplier * rollingStandardDeviation[index]
  );
  const lowerBand = middleBand.map(
    (mean, index) => mean - standardDeviationMultiplier * rollingStandardDeviation[index]
  );

  return { middleBand, upperBand, lowerBand };
}
