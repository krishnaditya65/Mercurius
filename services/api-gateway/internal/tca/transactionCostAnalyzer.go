// Package tca is FEATURES.md §18's "Transaction Cost Analysis (TCA)
// dashboards — post-trade best execution reporting". Given real filled
// orders (pulled from oms-gateway's audit trail — see fillsource.go),
// this package computes real TCA metrics: implementation shortfall
// (fill price vs. price at order submission time) and arrival-price
// slippage, and exposes them via api-gateway's `GET /tca/report`
// handler (cmd/server/main.go).
//
// TODO(real build): a real MiFID II-style best-ex report needs venue
// comparison (was this the best available price ACROSS venues at
// execution time, not just vs. this platform's own arrival price),
// benchmark curves (VWAP/TWAP-relative cost, not just arrival price),
// and a durable per-order audit record tying every fill to the specific
// market data snapshot used for the benchmark. This package computes
// the two metrics FEATURES.md names explicitly (implementation
// shortfall, arrival-price slippage) against this platform's own
// arrival price only — a real regulatory submission needs more.
package tca

import "errors"

// ErrOrderQuantityMustBePositive guards against a divide-by-zero /
// meaningless per-share cost computation.
var ErrOrderQuantityMustBePositive = errors.New("tca: order quantity must be positive")

// FilledOrder is the minimal shape this package needs about one
// executed order to compute TCA metrics.
type FilledOrder struct {
	OrderIdentifier              string
	AccountIdentifier            string
	InstrumentSymbol             string
	OrderSideIsBuyNotSell        bool
	OrderQuantity                uint64
	ArrivalPriceInMinorUnits     int64 // market price at the moment the order was SUBMITTED
	AverageFillPriceInMinorUnits int64 // actual volume-weighted fill price achieved
}

// TcaMetrics is one order's computed transaction-cost-analysis result.
type TcaMetrics struct {
	OrderIdentifier                     string  `json:"orderIdentifier"`
	InstrumentSymbol                    string  `json:"instrumentSymbol"`
	ImplementationShortfallInMinorUnits int64   `json:"implementationShortfallInMinorUnits"`
	ImplementationShortfallBasisPoints  float64 `json:"implementationShortfallBasisPoints"`
	ArrivalPriceSlippageInMinorUnits    int64   `json:"arrivalPriceSlippageInMinorUnits"`
	ArrivalPriceSlippageBasisPoints     float64 `json:"arrivalPriceSlippageBasisPoints"`
}

// AccountTcaReport aggregates TCA metrics across every filled order for
// one account.
type AccountTcaReport struct {
	AccountIdentifier                         string       `json:"accountIdentifier"`
	OrderCount                                int          `json:"orderCount"`
	PerOrderMetrics                           []TcaMetrics `json:"perOrderMetrics"`
	AverageImplementationShortfallBasisPoints float64      `json:"averageImplementationShortfallBasisPoints"`
	AverageArrivalPriceSlippageBasisPoints    float64      `json:"averageArrivalPriceSlippageBasisPoints"`
}

// ComputeMetricsForOrder computes implementation shortfall and
// arrival-price slippage for one filled order.
//
// Implementation shortfall (per share, signed so POSITIVE always means
// "cost the client money" regardless of side): for a BUY, shortfall =
// fillPrice - arrivalPrice (paying more than the price at submission
// time is a cost); for a SELL, shortfall = arrivalPrice - fillPrice
// (receiving less than the price at submission time is a cost).
//
// Arrival-price slippage is computed identically here — in this
// simplified single-benchmark model (no separate "decision price" vs.
// "arrival price" distinction), implementation shortfall and
// arrival-price slippage against the SAME benchmark coincide; they are
// still reported as two distinct fields (per FEATURES.md naming both
// explicitly) so a future build that adds a genuinely separate decision
// price doesn't need a field-shape change.
func ComputeMetricsForOrder(order FilledOrder) (TcaMetrics, error) {
	if order.OrderQuantity == 0 {
		return TcaMetrics{}, ErrOrderQuantityMustBePositive
	}

	var perShareCostInMinorUnits int64
	if order.OrderSideIsBuyNotSell {
		perShareCostInMinorUnits = order.AverageFillPriceInMinorUnits - order.ArrivalPriceInMinorUnits
	} else {
		perShareCostInMinorUnits = order.ArrivalPriceInMinorUnits - order.AverageFillPriceInMinorUnits
	}

	totalCostInMinorUnits := perShareCostInMinorUnits * int64(order.OrderQuantity)
	basisPoints := basisPointsOf(perShareCostInMinorUnits, order.ArrivalPriceInMinorUnits)

	return TcaMetrics{
		OrderIdentifier:                     order.OrderIdentifier,
		InstrumentSymbol:                    order.InstrumentSymbol,
		ImplementationShortfallInMinorUnits: totalCostInMinorUnits,
		ImplementationShortfallBasisPoints:  basisPoints,
		ArrivalPriceSlippageInMinorUnits:    totalCostInMinorUnits,
		ArrivalPriceSlippageBasisPoints:     basisPoints,
	}, nil
}

// basisPointsOf returns numerator/denominator expressed in basis points
// (1bp = 0.01%). Returns 0 if denominator is non-positive, to avoid a
// divide-by-zero for the (invalid, but shouldn't crash reporting)
// arrival price of 0.
func basisPointsOf(numerator, denominator int64) float64 {
	if denominator <= 0 {
		return 0
	}
	return (float64(numerator) / float64(denominator)) * 10000.0
}

// BuildAccountReport computes and aggregates TcaMetrics across every
// order in orders. Orders that fail ComputeMetricsForOrder (e.g. zero
// quantity — shouldn't happen for a genuinely filled order, but this is
// defensive) are skipped rather than aborting the whole report.
func BuildAccountReport(accountIdentifier string, orders []FilledOrder) AccountTcaReport {
	report := AccountTcaReport{AccountIdentifier: accountIdentifier}

	var shortfallBpSum, slippageBpSum float64
	for _, order := range orders {
		metrics, err := ComputeMetricsForOrder(order)
		if err != nil {
			continue
		}
		report.PerOrderMetrics = append(report.PerOrderMetrics, metrics)
		shortfallBpSum += metrics.ImplementationShortfallBasisPoints
		slippageBpSum += metrics.ArrivalPriceSlippageBasisPoints
	}

	report.OrderCount = len(report.PerOrderMetrics)
	if report.OrderCount > 0 {
		report.AverageImplementationShortfallBasisPoints = shortfallBpSum / float64(report.OrderCount)
		report.AverageArrivalPriceSlippageBasisPoints = slippageBpSum / float64(report.OrderCount)
	}
	return report
}
