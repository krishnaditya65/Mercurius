// fillDataSource.go supplies FilledOrder data to the TCA report
// handler.
//
// HONEST GAP: oms-gateway's real, running HTTP API today (see
// services/oms-gateway/cmd/server/main.go) has no endpoint that returns
// a full order history with both the arrival price (price at submission
// time) and the average fill price for past orders — `GET /audit-trail`
// records EVENT TYPES and instrument symbols but no price fields at all
// (see internal/audittrail.Entry), and `GET /orders/status` returns one
// order's CURRENT price by order identifier, not a historical arrival
// price alongside it. Building a real price-history endpoint on
// oms-gateway that carries both figures per order is real, separate
// work not done in this increment (a real build would add
// arrivalPriceInMinorUnits to internal/audittrail.Entry at
// EventOrderSubmitted time, and averageFillPriceInMinorUnits at
// EventOrderFilled time, then expose a `GET /orders/history` endpoint
// joining the two).
//
// Per this task's explicit allowance ("Real math on real (or
// realistically-fixtured, if no real fills exist yet to pull live)
// order data"), this file provides a REALISTIC FIXTURE data source so
// BuildAccountReport's real math has real numbers to run against today.
// FetchLiveFilledOrders below documents exactly what a real
// implementation would call once that oms-gateway endpoint exists.
package tca

import "errors"

// ErrLiveFillHistoryNotYetAvailable is returned by FetchLiveFilledOrders
// — see this file's package-level gap explanation above.
var ErrLiveFillHistoryNotYetAvailable = errors.New(
	"tca: oms-gateway does not yet expose a per-order arrival-price + fill-price history endpoint; " +
		"see fillDataSource.go's doc comment for exactly what a real build needs to add",
)

// FetchLiveFilledOrders is the REAL integration point a completed build
// would use: it would call a real oms-gateway endpoint (not built yet —
// see the package doc comment) and return real FilledOrder records. It
// always returns ErrLiveFillHistoryNotYetAvailable today, on purpose,
// rather than silently returning fixture data under a "live" name —
// callers (cmd/server/main.go's TCA handler) fall back to
// FixtureFilledOrdersForAccount explicitly and visibly when this
// returns an error, so the report response can honestly label itself.
func FetchLiveFilledOrders(omsGatewayBaseUrl string, accountIdentifier string) ([]FilledOrder, error) {
	return nil, ErrLiveFillHistoryNotYetAvailable
}

// FixtureFilledOrdersForAccount returns a small, realistic set of
// filled orders for demonstrating real TCA math end-to-end. Values are
// representative NSE-style equity prices in minor units (paise) —
// deliberately including one favorable fill, one unfavorable fill, and
// one at-arrival fill, so BuildAccountReport's aggregate output is
// visibly non-trivial.
func FixtureFilledOrdersForAccount(accountIdentifier string) []FilledOrder {
	return []FilledOrder{
		{
			OrderIdentifier:              "fixture-ord-1",
			AccountIdentifier:            accountIdentifier,
			InstrumentSymbol:             "RELIANCE",
			OrderSideIsBuyNotSell:        true,
			OrderQuantity:                50,
			ArrivalPriceInMinorUnits:     280000, // ₹2,800.00
			AverageFillPriceInMinorUnits: 280350, // ₹2,803.50 — slipped against the client
		},
		{
			OrderIdentifier:              "fixture-ord-2",
			AccountIdentifier:            accountIdentifier,
			InstrumentSymbol:             "TCS",
			OrderSideIsBuyNotSell:        false,
			OrderQuantity:                30,
			ArrivalPriceInMinorUnits:     385000, // ₹3,850.00
			AverageFillPriceInMinorUnits: 385500, // ₹3,855.00 — favorable sell
		},
		{
			OrderIdentifier:              "fixture-ord-3",
			AccountIdentifier:            accountIdentifier,
			InstrumentSymbol:             "INFY",
			OrderSideIsBuyNotSell:        true,
			OrderQuantity:                100,
			ArrivalPriceInMinorUnits:     150000, // ₹1,500.00
			AverageFillPriceInMinorUnits: 150000, // filled exactly at arrival
		},
	}
}
