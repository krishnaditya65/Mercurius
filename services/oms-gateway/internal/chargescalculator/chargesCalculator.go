// Package chargescalculator computes a full pre-trade charges breakdown
// for an Indian equity cash-market order — FEATURES.md §21: "Full
// charges breakdown *before* order confirmation: brokerage, STT/CTT,
// stamp duty, GST, exchange transaction charges, DP charges — shown as
// a receipt, not discovered after the fact." The #2 pain-point item
// right next to plain-language rejection reasons in that section.
//
// IMPORTANT — rates here are an ILLUSTRATIVE model based on commonly-
// published discount-broker rate cards (the Zerodha-style "free
// delivery brokerage" structure) and typical recent statutory rates,
// hardcoded as of this build. They are NOT fetched from any live
// regulatory/exchange source, WILL drift out of date (STT/stamp-duty
// rates and slabs change via government notification, not on any
// predictable schedule), and vary by exchange/segment/state in ways
// this simplified model doesn't capture. Treat every number here as
// "illustrative, not authoritative" — a real build needs a maintained,
// versioned rate table (ideally centrally updated, not hardcoded per
// service), not this.
package chargescalculator

// Rate constants — all expressed as a fraction of TURNOVER (price ×
// quantity), except brokerage's flat cap and DP charges, which are
// fixed amounts. See the package doc for the "illustrative, not
// authoritative" caveat.
const (
	// intradayBrokerageRate and intradayBrokerageFlatCapInMinorUnits:
	// intraday brokerage is min(flatCap, rate × turnover) per order —
	// the standard "₹20 or 0.03%, whichever is lower" discount-broker
	// structure. Delivery brokerage is 0 (also a common discount-broker
	// structure, not universal).
	intradayBrokerageRate                = 0.0003
	intradayBrokerageFlatCapInMinorUnits = 2000 // ₹20.00
	deliveryBrokerageInMinorUnits        = 0

	// STT/CTT (Securities/Commodities Transaction Tax): delivery charges
	// BOTH buy and sell sides; intraday charges the SELL side only (this
	// asymmetry is real and load-bearing in the calculation below, not
	// an oversight).
	deliverySttRate = 0.001   // 0.1%, both sides
	intradaySttRate = 0.00025 // 0.025%, sell side only

	// Exchange transaction charges (NSE equity, illustrative) and SEBI
	// turnover fees — both sides, both segments.
	exchangeTransactionChargeRate = 0.0000297
	sebiTurnoverFeeRate           = 0.000001 // ~₹10 per crore of turnover

	// Stamp duty: buy side only, both segments, different rates.
	deliveryStampDutyRate = 0.00015
	intradayStampDutyRate = 0.00003

	// GST applies to (brokerage + exchange transaction charges + SEBI
	// turnover fees) — NOT to STT or stamp duty, which are themselves
	// taxes, not services GST taxes.
	gstRate = 0.18

	// DP (Depository Participant) charges: a flat per-scrip fee charged
	// only when SELLING a delivery (not intraday) holding — this models
	// the common "flat fee regardless of quantity" structure.
	depositoryParticipantChargeInMinorUnits = 1500 // ₹15.00
)

// ChargesBreakdown is every line item FEATURES.md §21 calls for, plus
// the turnover they're all computed from and the final net cash impact
// (what actually settles, after charges) — everything a receipt-style
// pre-order confirmation UI needs, in one response.
type ChargesBreakdown struct {
	TurnoverInMinorUnits                    int64 `json:"turnoverInMinorUnits"`
	BrokerageInMinorUnits                   int64 `json:"brokerageInMinorUnits"`
	SecuritiesTransactionTaxInMinorUnits    int64 `json:"securitiesTransactionTaxInMinorUnits"`
	ExchangeTransactionChargeInMinorUnits   int64 `json:"exchangeTransactionChargeInMinorUnits"`
	SebiTurnoverFeeInMinorUnits             int64 `json:"sebiTurnoverFeeInMinorUnits"`
	StampDutyInMinorUnits                   int64 `json:"stampDutyInMinorUnits"`
	GstInMinorUnits                         int64 `json:"gstInMinorUnits"`
	DepositoryParticipantChargeInMinorUnits int64 `json:"depositoryParticipantChargeInMinorUnits"`
	TotalChargesInMinorUnits                int64 `json:"totalChargesInMinorUnits"`
	// NetAmountInMinorUnits is what actually moves: turnover + charges
	// for a buy (you pay more than the sticker turnover), turnover -
	// charges for a sell (you receive less).
	NetAmountInMinorUnits int64 `json:"netAmountInMinorUnits"`
}

// CalculateCharges computes the full breakdown for one order. Every
// component is computed independently off turnover (not chained off
// each other, except GST which explicitly depends on three of the
// others) so each line item is individually auditable against the rate
// constants above.
func CalculateCharges(orderSideIsBuyNotSell bool, priceInMinorUnits int64, quantity uint64, isIntradayNotDelivery bool) ChargesBreakdown {
	turnover := priceInMinorUnits * int64(quantity)

	brokerage := calculateBrokerage(turnover, isIntradayNotDelivery)
	stt := calculateSecuritiesTransactionTax(turnover, orderSideIsBuyNotSell, isIntradayNotDelivery)
	exchangeTransactionCharge := roundToNearestMinorUnit(float64(turnover) * exchangeTransactionChargeRate)
	sebiTurnoverFee := roundToNearestMinorUnit(float64(turnover) * sebiTurnoverFeeRate)
	stampDuty := calculateStampDuty(turnover, orderSideIsBuyNotSell, isIntradayNotDelivery)
	gst := roundToNearestMinorUnit(float64(brokerage+exchangeTransactionCharge+sebiTurnoverFee) * gstRate)
	depositoryParticipantCharge := calculateDepositoryParticipantCharge(orderSideIsBuyNotSell, isIntradayNotDelivery)

	totalCharges := brokerage + stt + exchangeTransactionCharge + sebiTurnoverFee + stampDuty + gst + depositoryParticipantCharge

	netAmount := turnover + totalCharges
	if !orderSideIsBuyNotSell {
		netAmount = turnover - totalCharges
	}

	return ChargesBreakdown{
		TurnoverInMinorUnits:                    turnover,
		BrokerageInMinorUnits:                   brokerage,
		SecuritiesTransactionTaxInMinorUnits:    stt,
		ExchangeTransactionChargeInMinorUnits:   exchangeTransactionCharge,
		SebiTurnoverFeeInMinorUnits:             sebiTurnoverFee,
		StampDutyInMinorUnits:                   stampDuty,
		GstInMinorUnits:                         gst,
		DepositoryParticipantChargeInMinorUnits: depositoryParticipantCharge,
		TotalChargesInMinorUnits:                totalCharges,
		NetAmountInMinorUnits:                   netAmount,
	}
}

func calculateBrokerage(turnover int64, isIntradayNotDelivery bool) int64 {
	if !isIntradayNotDelivery {
		return deliveryBrokerageInMinorUnits
	}
	percentageBasedBrokerage := roundToNearestMinorUnit(float64(turnover) * intradayBrokerageRate)
	if percentageBasedBrokerage < intradayBrokerageFlatCapInMinorUnits {
		return percentageBasedBrokerage
	}
	return intradayBrokerageFlatCapInMinorUnits
}

func calculateSecuritiesTransactionTax(turnover int64, orderSideIsBuyNotSell bool, isIntradayNotDelivery bool) int64 {
	if isIntradayNotDelivery {
		if orderSideIsBuyNotSell {
			return 0 // intraday STT is sell-side only
		}
		return roundToNearestMinorUnit(float64(turnover) * intradaySttRate)
	}
	// Delivery: both sides.
	return roundToNearestMinorUnit(float64(turnover) * deliverySttRate)
}

func calculateStampDuty(turnover int64, orderSideIsBuyNotSell bool, isIntradayNotDelivery bool) int64 {
	if !orderSideIsBuyNotSell {
		return 0 // stamp duty is buy-side only
	}
	if isIntradayNotDelivery {
		return roundToNearestMinorUnit(float64(turnover) * intradayStampDutyRate)
	}
	return roundToNearestMinorUnit(float64(turnover) * deliveryStampDutyRate)
}

func calculateDepositoryParticipantCharge(orderSideIsBuyNotSell bool, isIntradayNotDelivery bool) int64 {
	if isIntradayNotDelivery || orderSideIsBuyNotSell {
		return 0 // only a delivery SELL triggers a DP charge
	}
	return depositoryParticipantChargeInMinorUnits
}

// roundToNearestMinorUnit rounds a fractional minor-unit amount (e.g.
// paise) to the nearest whole minor unit — real charges are quoted to
// the paisa, not fractions of one.
func roundToNearestMinorUnit(amount float64) int64 {
	if amount < 0 {
		return int64(amount - 0.5)
	}
	return int64(amount + 0.5)
}
