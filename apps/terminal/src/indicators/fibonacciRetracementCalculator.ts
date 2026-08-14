// Mercurius / terminal — Fibonacci retracement levels. FEATURES.md §10's
// chart indicator overlays.
//
// Standard retracement ratios: 0%, 23.6%, 38.2%, 50% (not a Fibonacci
// ratio itself, but conventionally included by every charting package),
// 61.8%, 78.6%, 100%. For an UPTREND swing (retracing DOWN from a swing
// high back toward the swing low), each level is:
//   level(ratio) = swingHigh - ratio * (swingHigh - swingLow)
// For a DOWNTREND swing (retracing UP from a swing low back toward the
// swing high), each level is:
//   level(ratio) = swingLow + ratio * (swingHigh - swingLow)
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

export const FIBONACCI_RETRACEMENT_RATIOS = [0, 0.236, 0.382, 0.5, 0.618, 0.786, 1] as const;

export type FibonacciRetracementLevel = {
  ratio: number;
  price: number;
};

export function calculateFibonacciRetracementLevels(
  swingHigh: number,
  swingLow: number,
  trendDirection: "uptrend" | "downtrend" = "uptrend"
): FibonacciRetracementLevel[] {
  if (swingHigh <= swingLow) {
    throw new Error("swingHigh must be greater than swingLow");
  }
  const priceRange = swingHigh - swingLow;

  return FIBONACCI_RETRACEMENT_RATIOS.map((ratio) => ({
    ratio,
    price:
      trendDirection === "uptrend" ? swingHigh - ratio * priceRange : swingLow + ratio * priceRange,
  }));
}
