package tca

import (
	"errors"
	"testing"
)

func TestComputeMetricsForBuyOrderThatFilledWorseThanArrivalIsACost(t *testing.T) {
	metrics, err := ComputeMetricsForOrder(FilledOrder{
		OrderIdentifier:              "ord-1",
		OrderSideIsBuyNotSell:        true,
		OrderQuantity:                100,
		ArrivalPriceInMinorUnits:     10000, // ₹100.00
		AverageFillPriceInMinorUnits: 10050, // ₹100.50 — paid MORE than arrival
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if metrics.ImplementationShortfallInMinorUnits != 5000 { // 50 minor units/share * 100
		t.Fatalf("expected shortfall of 5000 minor units, got %d", metrics.ImplementationShortfallInMinorUnits)
	}
	if metrics.ImplementationShortfallBasisPoints <= 0 {
		t.Fatalf("expected a positive (cost) basis point figure for a worse-than-arrival buy fill, got %v", metrics.ImplementationShortfallBasisPoints)
	}
}

func TestComputeMetricsForBuyOrderThatFilledBetterThanArrivalIsAGain(t *testing.T) {
	metrics, err := ComputeMetricsForOrder(FilledOrder{
		OrderIdentifier:              "ord-2",
		OrderSideIsBuyNotSell:        true,
		OrderQuantity:                100,
		ArrivalPriceInMinorUnits:     10000,
		AverageFillPriceInMinorUnits: 9950, // paid LESS than arrival — a favorable fill
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if metrics.ImplementationShortfallInMinorUnits >= 0 {
		t.Fatalf("expected a negative (favorable) shortfall for a better-than-arrival buy fill, got %d", metrics.ImplementationShortfallInMinorUnits)
	}
}

func TestComputeMetricsForSellOrderThatFilledWorseThanArrivalIsACost(t *testing.T) {
	metrics, err := ComputeMetricsForOrder(FilledOrder{
		OrderIdentifier:              "ord-3",
		OrderSideIsBuyNotSell:        false,
		OrderQuantity:                50,
		ArrivalPriceInMinorUnits:     10000,
		AverageFillPriceInMinorUnits: 9900, // received LESS than arrival on a sell — a cost
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if metrics.ImplementationShortfallInMinorUnits <= 0 {
		t.Fatalf("expected a positive (cost) shortfall for a worse-than-arrival sell fill, got %d", metrics.ImplementationShortfallInMinorUnits)
	}
}

func TestComputeMetricsForSellOrderThatFilledBetterThanArrivalIsAGain(t *testing.T) {
	metrics, err := ComputeMetricsForOrder(FilledOrder{
		OrderIdentifier:              "ord-4",
		OrderSideIsBuyNotSell:        false,
		OrderQuantity:                50,
		ArrivalPriceInMinorUnits:     10000,
		AverageFillPriceInMinorUnits: 10100, // received MORE than arrival on a sell — favorable
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if metrics.ImplementationShortfallInMinorUnits >= 0 {
		t.Fatalf("expected a negative (favorable) shortfall, got %d", metrics.ImplementationShortfallInMinorUnits)
	}
}

func TestComputeMetricsForOrderExactlyAtArrivalPriceHasZeroCost(t *testing.T) {
	metrics, err := ComputeMetricsForOrder(FilledOrder{
		OrderSideIsBuyNotSell:        true,
		OrderQuantity:                10,
		ArrivalPriceInMinorUnits:     10000,
		AverageFillPriceInMinorUnits: 10000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if metrics.ImplementationShortfallInMinorUnits != 0 || metrics.ImplementationShortfallBasisPoints != 0 {
		t.Fatalf("expected zero cost for a fill exactly at arrival price, got %+v", metrics)
	}
}

func TestComputeMetricsForOrderRejectsZeroQuantity(t *testing.T) {
	_, err := ComputeMetricsForOrder(FilledOrder{OrderQuantity: 0, ArrivalPriceInMinorUnits: 10000, AverageFillPriceInMinorUnits: 10000})
	if !errors.Is(err, ErrOrderQuantityMustBePositive) {
		t.Fatalf("expected ErrOrderQuantityMustBePositive, got %v", err)
	}
}

func TestArrivalPriceSlippageMirrorsImplementationShortfallInThisModel(t *testing.T) {
	metrics, _ := ComputeMetricsForOrder(FilledOrder{
		OrderSideIsBuyNotSell:        true,
		OrderQuantity:                10,
		ArrivalPriceInMinorUnits:     10000,
		AverageFillPriceInMinorUnits: 10100,
	})
	if metrics.ArrivalPriceSlippageInMinorUnits != metrics.ImplementationShortfallInMinorUnits {
		t.Fatalf("expected slippage and shortfall to coincide in this single-benchmark model")
	}
}

func TestBuildAccountReportAggregatesAcrossMultipleOrders(t *testing.T) {
	orders := []FilledOrder{
		{OrderIdentifier: "ord-1", OrderSideIsBuyNotSell: true, OrderQuantity: 10, ArrivalPriceInMinorUnits: 10000, AverageFillPriceInMinorUnits: 10050},
		{OrderIdentifier: "ord-2", OrderSideIsBuyNotSell: true, OrderQuantity: 10, ArrivalPriceInMinorUnits: 10000, AverageFillPriceInMinorUnits: 9950},
	}
	report := BuildAccountReport("acct-1", orders)

	if report.OrderCount != 2 {
		t.Fatalf("expected 2 orders in the report, got %d", report.OrderCount)
	}
	if len(report.PerOrderMetrics) != 2 {
		t.Fatalf("expected 2 per-order metrics entries, got %d", len(report.PerOrderMetrics))
	}
	// One cost, one gain of equal magnitude should roughly cancel out.
	if report.AverageImplementationShortfallBasisPoints < -1 || report.AverageImplementationShortfallBasisPoints > 1 {
		t.Fatalf("expected the average shortfall to be near zero for offsetting orders, got %v", report.AverageImplementationShortfallBasisPoints)
	}
}

func TestBuildAccountReportWithNoOrdersHasZeroAverages(t *testing.T) {
	report := BuildAccountReport("acct-1", nil)
	if report.OrderCount != 0 {
		t.Fatalf("expected 0 orders, got %d", report.OrderCount)
	}
	if report.AverageImplementationShortfallBasisPoints != 0 {
		t.Fatalf("expected 0 average shortfall with no orders, got %v", report.AverageImplementationShortfallBasisPoints)
	}
}

func TestBuildAccountReportSkipsInvalidOrdersWithoutAborting(t *testing.T) {
	orders := []FilledOrder{
		{OrderIdentifier: "ord-bad", OrderQuantity: 0, ArrivalPriceInMinorUnits: 10000, AverageFillPriceInMinorUnits: 10000},
		{OrderIdentifier: "ord-good", OrderSideIsBuyNotSell: true, OrderQuantity: 10, ArrivalPriceInMinorUnits: 10000, AverageFillPriceInMinorUnits: 10000},
	}
	report := BuildAccountReport("acct-1", orders)
	if report.OrderCount != 1 {
		t.Fatalf("expected the invalid zero-quantity order to be skipped, leaving 1 valid order, got %d", report.OrderCount)
	}
}

func TestBasisPointsOfWithZeroDenominatorReturnsZeroNotPanic(t *testing.T) {
	metrics, err := ComputeMetricsForOrder(FilledOrder{
		OrderSideIsBuyNotSell:        true,
		OrderQuantity:                10,
		ArrivalPriceInMinorUnits:     0,
		AverageFillPriceInMinorUnits: 100,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if metrics.ImplementationShortfallBasisPoints != 0 {
		t.Fatalf("expected basis points to safely return 0 for a zero arrival price, got %v", metrics.ImplementationShortfallBasisPoints)
	}
}
