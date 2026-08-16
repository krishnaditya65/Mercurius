// Mercurius / terminal — shared minor-units-to-display-price formatting.
//
// Every price value crossing the wire (matching-engine, oms-gateway,
// market-data) is expressed in minor units (e.g. paise, cents) — see the
// `*InMinorUnits` field naming convention throughout this codebase. Every
// UI that DISPLAYS a price must divide by 100 before rendering it; the
// raw minor-units integer must still be what's sent back over the wire
// (order submission, etc). apps/web's pages (e.g. corporateActions,
// optionsChain) inline this same `(x / 100).toFixed(2)` convention; this
// helper centralizes it for apps/terminal so every price display here
// does the conversion consistently.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

/** Converts a minor-units integer price (e.g. paise) into a display-ready
 * major-units string with two decimal places (e.g. 12345 -> "123.45").
 * Does NOT prefix a currency symbol — callers that want one (₹, $, ...)
 * prepend it themselves, matching apps/web's convention. */
export function formatMinorUnitsAsDisplayPrice(priceInMinorUnits: number): string {
  return (priceInMinorUnits / 100).toFixed(2);
}
