package chargescalculator

import "testing"

// TestDeliveryBuyWorkedExample hand-computes every line item for a
// simple, round-number order (price=₹100.00=10000 paise, qty=10,
// turnover=₹1000.00=100000 paise, delivery buy) against the package's
// own documented rate constants, so this test catches a wrong formula
// even if it's internally "consistent" with the code that produced it.
func TestDeliveryBuyWorkedExample(t *testing.T) {
	breakdown := CalculateCharges(true, 10_000, 10, false)

	if breakdown.TurnoverInMinorUnits != 100_000 {
		t.Fatalf("expected turnover 100000, got %d", breakdown.TurnoverInMinorUnits)
	}
	if breakdown.BrokerageInMinorUnits != 0 {
		t.Fatalf("expected delivery brokerage to be 0, got %d", breakdown.BrokerageInMinorUnits)
	}
	// STT: 0.1% of 100000 = 100.
	if breakdown.SecuritiesTransactionTaxInMinorUnits != 100 {
		t.Fatalf("expected STT 100, got %d", breakdown.SecuritiesTransactionTaxInMinorUnits)
	}
	// Exchange transaction charge: 0.00297% of 100000 = 2.97 -> rounds to 3.
	if breakdown.ExchangeTransactionChargeInMinorUnits != 3 {
		t.Fatalf("expected exchange transaction charge 3, got %d", breakdown.ExchangeTransactionChargeInMinorUnits)
	}
	// SEBI turnover fee: 0.0001% of 100000 = 0.1 -> rounds to 0.
	if breakdown.SebiTurnoverFeeInMinorUnits != 0 {
		t.Fatalf("expected SEBI turnover fee 0, got %d", breakdown.SebiTurnoverFeeInMinorUnits)
	}
	// Stamp duty (buy side, delivery): 0.015% of 100000 = 15.
	if breakdown.StampDutyInMinorUnits != 15 {
		t.Fatalf("expected stamp duty 15, got %d", breakdown.StampDutyInMinorUnits)
	}
	// GST: 18% of (brokerage=0 + exchange=3 + sebi=0) = 18% of 3 = 0.54 -> rounds to 1.
	if breakdown.GstInMinorUnits != 1 {
		t.Fatalf("expected GST 1, got %d", breakdown.GstInMinorUnits)
	}
	// DP charge: not applicable on a BUY.
	if breakdown.DepositoryParticipantChargeInMinorUnits != 0 {
		t.Fatalf("expected DP charge 0 on a buy, got %d", breakdown.DepositoryParticipantChargeInMinorUnits)
	}

	expectedTotal := int64(0 + 100 + 3 + 0 + 15 + 1 + 0)
	if breakdown.TotalChargesInMinorUnits != expectedTotal {
		t.Fatalf("expected total charges %d, got %d", expectedTotal, breakdown.TotalChargesInMinorUnits)
	}
	// Net amount for a buy = turnover + charges.
	if breakdown.NetAmountInMinorUnits != breakdown.TurnoverInMinorUnits+expectedTotal {
		t.Fatalf("expected net amount %d, got %d", breakdown.TurnoverInMinorUnits+expectedTotal, breakdown.NetAmountInMinorUnits)
	}
}

func TestDeliverySellIncursDepositoryParticipantChargeButNotStampDuty(t *testing.T) {
	breakdown := CalculateCharges(false, 10_000, 10, false)

	if breakdown.DepositoryParticipantChargeInMinorUnits != depositoryParticipantChargeInMinorUnits {
		t.Fatalf("expected the flat DP charge on a delivery sell, got %d", breakdown.DepositoryParticipantChargeInMinorUnits)
	}
	if breakdown.StampDutyInMinorUnits != 0 {
		t.Fatalf("expected 0 stamp duty on a sell (buy-side only), got %d", breakdown.StampDutyInMinorUnits)
	}
	// Net amount for a sell = turnover - charges (you receive less than the sticker turnover).
	if breakdown.NetAmountInMinorUnits != breakdown.TurnoverInMinorUnits-breakdown.TotalChargesInMinorUnits {
		t.Fatal("expected net amount to be turnover minus total charges on a sell")
	}
}

func TestIntradayBuyHasNoSttButDoesHaveStampDuty(t *testing.T) {
	breakdown := CalculateCharges(true, 10_000, 10, true)

	if breakdown.SecuritiesTransactionTaxInMinorUnits != 0 {
		t.Fatalf("expected 0 STT on an intraday BUY (sell-side only), got %d", breakdown.SecuritiesTransactionTaxInMinorUnits)
	}
	if breakdown.StampDutyInMinorUnits == 0 {
		t.Fatal("expected non-zero stamp duty on an intraday buy")
	}
	if breakdown.DepositoryParticipantChargeInMinorUnits != 0 {
		t.Fatal("expected 0 DP charge on intraday (delivery-sell only)")
	}
}

func TestIntradaySellHasSttButNoStampDuty(t *testing.T) {
	breakdown := CalculateCharges(false, 10_000, 10, true)

	if breakdown.SecuritiesTransactionTaxInMinorUnits == 0 {
		t.Fatal("expected non-zero STT on an intraday sell")
	}
	if breakdown.StampDutyInMinorUnits != 0 {
		t.Fatalf("expected 0 stamp duty on a sell (buy-side only), got %d", breakdown.StampDutyInMinorUnits)
	}
}

func TestIntradayBrokerageIsCappedAtTheFlatFeeForALargeOrder(t *testing.T) {
	// A large enough turnover that 0.03% would exceed the ₹20 flat cap.
	breakdown := CalculateCharges(true, 100_000_00, 100, true) // turnover = 1,000,000,000 paise = ₹1,00,00,000
	if breakdown.BrokerageInMinorUnits != intradayBrokerageFlatCapInMinorUnits {
		t.Fatalf("expected brokerage to be capped at %d, got %d", intradayBrokerageFlatCapInMinorUnits, breakdown.BrokerageInMinorUnits)
	}
}

func TestIntradayBrokerageIsPercentageBasedForASmallOrder(t *testing.T) {
	// A small enough turnover that 0.03% is well under the ₹20 cap.
	breakdown := CalculateCharges(true, 100, 10, true) // turnover = 1000 paise
	expectedBrokerage := roundToNearestMinorUnit(1000 * intradayBrokerageRate)
	if breakdown.BrokerageInMinorUnits != expectedBrokerage {
		t.Fatalf("expected percentage-based brokerage %d, got %d", expectedBrokerage, breakdown.BrokerageInMinorUnits)
	}
	if breakdown.BrokerageInMinorUnits >= intradayBrokerageFlatCapInMinorUnits {
		t.Fatal("expected the percentage-based brokerage to be well under the flat cap for this small order")
	}
}

func TestZeroQuantityProducesAllZeroCharges(t *testing.T) {
	breakdown := CalculateCharges(true, 10_000, 0, false)
	if breakdown.TurnoverInMinorUnits != 0 || breakdown.TotalChargesInMinorUnits != 0 {
		t.Fatalf("expected a zero-quantity order to produce all-zero charges, got %+v", breakdown)
	}
}

func TestNetAmountRoundTripsCorrectlyForBuyVsSell(t *testing.T) {
	buyBreakdown := CalculateCharges(true, 10_000, 10, false)
	sellBreakdown := CalculateCharges(false, 10_000, 10, false)

	if buyBreakdown.NetAmountInMinorUnits <= buyBreakdown.TurnoverInMinorUnits {
		t.Fatal("expected a buy's net amount to exceed the raw turnover (charges add to what you pay)")
	}
	if sellBreakdown.NetAmountInMinorUnits >= sellBreakdown.TurnoverInMinorUnits {
		t.Fatal("expected a sell's net amount to be less than the raw turnover (charges subtract from what you receive)")
	}
}
