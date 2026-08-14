// Package contractnotegenerator produces a real, per-trade-day contract
// note: a trade-by-trade breakdown of every real fill an account had on
// one calendar date, each line's real brokerage/statutory charges (from
// oms-gateway's live chargescalculator via HTTP when reachable), and a
// summed total — FEATURES.md §1's "Regulatory reporting: contract
// notes".
package contractnotegenerator

import (
	"time"

	"mercurius/reporting/internal/filltrail"
	"mercurius/reporting/internal/omsgatewayclient"
)

// TradeLine is one real fill's row on the contract note, with its real
// (or, if oms-gateway's live calculator was unreachable, honestly
// flagged illustrative-fallback) charges breakdown.
type TradeLine struct {
	InstrumentSymbol              string                                      `json:"instrumentSymbol"`
	Side                          string                                      `json:"side"`
	Quantity                      uint64                                      `json:"quantity"`
	PriceInMinorUnits             int64                                       `json:"priceInMinorUnits"`
	ExecutedAtTime                time.Time                                   `json:"executedAtTime"`
	CounterpartyAccountIdentifier string                                      `json:"counterpartyAccountIdentifier"`
	Charges                       omsgatewayclient.ChargesBreakdownWireFormat `json:"charges"`
	ChargesSource                 string                                      `json:"chargesSource"` // "OMS_GATEWAY_LIVE_CALCULATOR" or "REPORTING_ILLUSTRATIVE_FALLBACK"
}

// ContractNote is the full document for one account + one calendar
// date.
type ContractNote struct {
	AccountIdentifier          string      `json:"accountIdentifier"`
	TradeDate                  time.Time   `json:"tradeDate"`
	TradeLines                 []TradeLine `json:"tradeLines"`
	TotalTurnoverInMinorUnits  int64       `json:"totalTurnoverInMinorUnits"`
	TotalChargesInMinorUnits   int64       `json:"totalChargesInMinorUnits"`
	TotalNetAmountInMinorUnits int64       `json:"totalNetAmountInMinorUnits"`
	GeneratedAtTime            time.Time   `json:"generatedAtTime"`
}

// illustrativeFallbackCharges is used ONLY when oms-gateway's live
// /orders/estimate-charges endpoint could not be reached — a
// deliberately simple, loudly-labeled placeholder (flat 0.05% of
// turnover, no line-item breakdown) so a contract note can still be
// produced rather than failing outright, never silently presented as
// oms-gateway's real rate model.
func illustrativeFallbackCharges(priceInMinorUnits int64, quantity uint64, orderSideIsBuyNotSell bool) omsgatewayclient.ChargesBreakdownWireFormat {
	turnover := priceInMinorUnits * int64(quantity)
	flatCharge := turnover * 5 / 10000 // 0.05% flat, illustrative only
	netAmount := turnover + flatCharge
	if !orderSideIsBuyNotSell {
		netAmount = turnover - flatCharge
	}
	return omsgatewayclient.ChargesBreakdownWireFormat{
		TurnoverInMinorUnits:     turnover,
		TotalChargesInMinorUnits: flatCharge,
		NetAmountInMinorUnits:    netAmount,
	}
}

// GenerateForAccountAndDate builds a real contract note for
// accountIdentifier's fills on tradeDate, given that account's full fill
// history (already fetched and parsed by the caller via filltrail) and
// a live omsgatewayclient to call the real charges calculator per line.
// isIntradayNotDelivery is applied uniformly to every line — callers
// that need a mixed intraday/delivery contract note should call this
// once per settlement type and merge, since fills alone don't record
// which settlement type an order was (oms-gateway's audit trail doesn't
// carry that field either — see filltrail's package doc for the same
// class of honest gap).
func GenerateForAccountAndDate(
	accountIdentifier string,
	tradeDate time.Time,
	allFills []filltrail.Fill,
	isIntradayNotDelivery bool,
	omsGatewayClient *omsgatewayclient.OmsGatewayClient,
	now time.Time,
) ContractNote {
	dayFills := filltrail.FillsOnDate(allFills, tradeDate)

	note := ContractNote{
		AccountIdentifier: accountIdentifier,
		TradeDate:         tradeDate,
		GeneratedAtTime:   now,
	}

	for _, fill := range dayFills {
		orderSideIsBuyNotSell := fill.Side == filltrail.SideBuy

		charges, isLive := omsGatewayClient.EstimateCharges(orderSideIsBuyNotSell, fill.PriceInMinorUnits, fill.Quantity, isIntradayNotDelivery)
		chargesSource := "OMS_GATEWAY_LIVE_CALCULATOR"
		if !isLive {
			charges = illustrativeFallbackCharges(fill.PriceInMinorUnits, fill.Quantity, orderSideIsBuyNotSell)
			chargesSource = "REPORTING_ILLUSTRATIVE_FALLBACK"
		}

		note.TradeLines = append(note.TradeLines, TradeLine{
			InstrumentSymbol:              fill.InstrumentSymbol,
			Side:                          fill.Side,
			Quantity:                      fill.Quantity,
			PriceInMinorUnits:             fill.PriceInMinorUnits,
			ExecutedAtTime:                fill.ExecutedAtTime,
			CounterpartyAccountIdentifier: fill.CounterpartyAccountIdentifier,
			Charges:                       charges,
			ChargesSource:                 chargesSource,
		})

		note.TotalTurnoverInMinorUnits += charges.TurnoverInMinorUnits
		note.TotalChargesInMinorUnits += charges.TotalChargesInMinorUnits
		note.TotalNetAmountInMinorUnits += charges.NetAmountInMinorUnits
	}

	return note
}
