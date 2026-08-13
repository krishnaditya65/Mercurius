// Package basketorders implements FEATURES.md §15's "Basket/program order
// execution (buy/sell N instruments as one logical order)": given a set of
// (symbol, side, quantity-or-weight) constituents plus a net cash
// constraint, this submits every constituent as one logical unit through
// the existing order-submission path and tracks aggregate fill status.
//
// UNLIKE internal/multilegoptions, a basket order is deliberately NOT
// atomic all-or-nothing — a real program/basket trade (e.g. an index
// rebalance) genuinely tolerates partial fills on some constituents while
// others fill fully; FEATURES.md itself only asks for "tracking aggregate
// fill status" here, not atomicity. ExecuteBasket therefore submits every
// constituent regardless of any earlier constituent's outcome and reports
// a real AggregateStatus (ALL_ACCEPTED / PARTIALLY_ACCEPTED / NONE_ACCEPTED)
// plus per-constituent detail — never silently drops a rejection.
//
// WEIGHT-BASED QUANTITY DERIVATION AND THE NET CASH CONSTRAINT: a
// constituent may specify either an explicit Quantity OR a WeightPercent
// (mutually exclusive per constituent, but a basket must be consistently
// ALL-quantity or ALL-weight — mixing the two modes within one basket
// would make the net-cash-constraint math ambiguous). In weight mode, each
// constituent's quantity is derived as
// floor(weightPercent/100 * netCashConstraint / referencePriceInMinorUnits)
// — an ILLUSTRATIVE, simple allocation (see the loud gap note below), not
// a real portfolio-construction optimizer. In BOTH modes, the basket's
// real net cash (buys' notional minus sells' notional) is computed and
// checked against NetCashConstraintInMinorUnits BEFORE any constituent is
// submitted — a basket that would breach its own stated cash constraint is
// rejected wholesale, exactly like internal/exposurelimits rejects an
// order that would breach a configured cap, never partially executed past
// its own declared budget.
//
// LOUD, REPEATED KNOWN GAP: ReferencePriceInMinorUnits (used for both the
// net-cash-constraint check and weight-based quantity derivation) is
// caller-supplied on every constituent, not looked up from any live price
// feed — the exact same "no live last-traded-price feed" gap
// internal/marginpledge, internal/marktomarket, and the pre-trade risk
// check's market-order TODO already document repeatedly throughout this
// codebase. Treat every notional/cash figure here as illustrative, not
// authoritative, until oms-gateway has a real price feed.
package basketorders

import (
	"errors"
	"fmt"
)

var (
	ErrNoConstituents               = errors.New("a basket order requires at least one constituent")
	ErrEmptyBasketIdentifier        = errors.New("basketIdentifier must not be empty")
	ErrEmptyClientAccountIdentifier = errors.New("clientAccountIdentifier must not be empty")
	ErrEmptyInstrumentSymbol        = errors.New("constituent instrumentSymbol must not be empty")
	ErrNonPositiveReferencePrice    = errors.New("constituent referencePriceInMinorUnits must be greater than zero")
	ErrMixedQuantityModes           = errors.New("a basket must use either explicit quantities for every constituent or weights for every constituent, not a mix")
	ErrZeroQuantityOrWeight         = errors.New("constituent must specify a positive quantity or a positive weightPercent")
	ErrWeightsMustBePositive        = errors.New("constituent weightPercent must be greater than zero")

	// ErrNetCashConstraintExceeded is returned when the basket's real net
	// cash (buys' notional minus sells' notional) would exceed the
	// configured NetCashConstraintInMinorUnits — checked BEFORE any
	// constituent is submitted, so a breaching basket is rejected
	// wholesale, not partially executed past its own budget.
	ErrNetCashConstraintExceeded = errors.New("basket's net cash would exceed the configured net cash constraint")
)

// Constituent is one instrument leg of a basket order.
type Constituent struct {
	InstrumentSymbol string `json:"instrumentSymbol"`
	IsBuyNotSell     bool   `json:"isBuyNotSell"`

	// Quantity is used directly if non-zero. Mutually exclusive with
	// WeightPercent within one basket (see package doc).
	Quantity uint64 `json:"quantity,omitempty"`

	// WeightPercent (0-100] drives quantity derivation off the basket's
	// NetCashConstraintInMinorUnits when Quantity is zero. See package
	// doc's "WEIGHT-BASED QUANTITY DERIVATION" note.
	WeightPercent float64 `json:"weightPercent,omitempty"`

	// ReferencePriceInMinorUnits is used for the net-cash-constraint
	// check and (in weight mode) quantity derivation — see the package
	// doc's loud gap note: caller-supplied, not fetched from a live feed.
	ReferencePriceInMinorUnits int64 `json:"referencePriceInMinorUnits"`
}

// BasketOrderRequest is the client-facing payload for a basket/program
// order.
type BasketOrderRequest struct {
	BasketIdentifier              string        `json:"basketIdentifier"`
	ClientAccountIdentifier       string        `json:"clientAccountIdentifier"`
	NetCashConstraintInMinorUnits int64         `json:"netCashConstraintInMinorUnits"`
	Constituents                  []Constituent `json:"constituents"`
}

// resolvedConstituent is a Constituent with its final (possibly
// weight-derived) quantity and signed notional pinned down.
type resolvedConstituent struct {
	Constituent
	resolvedQuantity uint64
	signedNotional   int64 // positive for a buy, negative for a sell
}

// ValidateAndResolveBasket validates request and, for weight-mode
// baskets, derives each constituent's real quantity — returning the
// resolved constituents and the basket's real net cash figure, WITHOUT
// submitting anything. ExecuteBasket calls this first and rejects the
// whole basket (no submissions at all) if the net cash constraint would
// be breached.
func ValidateAndResolveBasket(request BasketOrderRequest) ([]resolvedConstituent, int64, error) {
	if request.BasketIdentifier == "" {
		return nil, 0, ErrEmptyBasketIdentifier
	}
	if request.ClientAccountIdentifier == "" {
		return nil, 0, ErrEmptyClientAccountIdentifier
	}
	if len(request.Constituents) == 0 {
		return nil, 0, ErrNoConstituents
	}

	isWeightMode := request.Constituents[0].Quantity == 0
	for _, constituent := range request.Constituents {
		if constituent.InstrumentSymbol == "" {
			return nil, 0, ErrEmptyInstrumentSymbol
		}
		if constituent.ReferencePriceInMinorUnits <= 0 {
			return nil, 0, ErrNonPositiveReferencePrice
		}
		thisIsWeightMode := constituent.Quantity == 0
		if thisIsWeightMode != isWeightMode {
			return nil, 0, ErrMixedQuantityModes
		}
		if thisIsWeightMode && constituent.WeightPercent <= 0 {
			return nil, 0, ErrWeightsMustBePositive
		}
		if !thisIsWeightMode && constituent.Quantity == 0 {
			return nil, 0, ErrZeroQuantityOrWeight
		}
	}

	resolved := make([]resolvedConstituent, 0, len(request.Constituents))
	var netCash int64

	for _, constituent := range request.Constituents {
		quantity := constituent.Quantity
		if isWeightMode {
			budget := float64(request.NetCashConstraintInMinorUnits) * (constituent.WeightPercent / 100.0)
			quantity = uint64(budget / float64(constituent.ReferencePriceInMinorUnits))
			if quantity == 0 {
				return nil, 0, fmt.Errorf("%w: constituent %s's weight of %.4f%% resolves to zero quantity against the net cash constraint", ErrZeroQuantityOrWeight, constituent.InstrumentSymbol, constituent.WeightPercent)
			}
		}

		notional := int64(quantity) * constituent.ReferencePriceInMinorUnits
		signedNotional := notional
		if !constituent.IsBuyNotSell {
			signedNotional = -notional
		}
		netCash += signedNotional

		resolved = append(resolved, resolvedConstituent{
			Constituent:      constituent,
			resolvedQuantity: quantity,
			signedNotional:   signedNotional,
		})
	}

	if netCash > request.NetCashConstraintInMinorUnits {
		return nil, netCash, fmt.Errorf("%w: net cash %d exceeds constraint %d", ErrNetCashConstraintExceeded, netCash, request.NetCashConstraintInMinorUnits)
	}

	return resolved, netCash, nil
}

// ConstituentSubmissionFunc submits ONE constituent as a real order and
// reports its acceptance and (if any) filled quantity. In
// cmd/server/main.go this closure calls processOrderSubmission — the
// exact same pipeline every other order path reuses — and sums
// TradeExecutionEvents for filledQuantity.
type ConstituentSubmissionFunc func(instrumentSymbol string, isBuyNotSell bool, quantity uint64) (wasAccepted bool, filledQuantity uint64, rejectionReason string, submissionErr error)

// ConstituentOutcome is one constituent's real submission result.
type ConstituentOutcome struct {
	InstrumentSymbol  string `json:"instrumentSymbol"`
	IsBuyNotSell      bool   `json:"isBuyNotSell"`
	SubmittedQuantity uint64 `json:"submittedQuantity"`
	WasAccepted       bool   `json:"wasAccepted"`
	FilledQuantity    uint64 `json:"filledQuantity"`
	RejectionReason   string `json:"rejectionReason,omitempty"`
}

// AggregateStatus summarizes a basket's overall outcome.
type AggregateStatus string

const (
	AggregateStatusAllAccepted     AggregateStatus = "ALL_ACCEPTED"
	AggregateStatusPartialAccepted AggregateStatus = "PARTIALLY_ACCEPTED"
	AggregateStatusNoneAccepted    AggregateStatus = "NONE_ACCEPTED"
)

// BasketExecutionResult is ExecuteBasket's full outcome.
type BasketExecutionResult struct {
	BasketIdentifier    string               `json:"basketIdentifier"`
	NetCashInMinorUnits int64                `json:"netCashInMinorUnits"`
	AggregateStatus     AggregateStatus      `json:"aggregateStatus"`
	ConstituentOutcomes []ConstituentOutcome `json:"constituentOutcomes"`
}

// ExecuteBasket resolves and validates request (including the net cash
// constraint check — a breaching basket is rejected wholesale, with zero
// submissions), then submits every constituent via submitConstituent,
// tracking real aggregate fill status. See the package doc for why this
// is deliberately NOT atomic all-or-nothing (contrast
// internal/multilegoptions).
func ExecuteBasket(request BasketOrderRequest, submitConstituent ConstituentSubmissionFunc) (BasketExecutionResult, error) {
	resolvedConstituents, netCash, err := ValidateAndResolveBasket(request)
	if err != nil {
		return BasketExecutionResult{}, err
	}
	if submitConstituent == nil {
		return BasketExecutionResult{}, errors.New("submitConstituent function must not be nil")
	}

	result := BasketExecutionResult{
		BasketIdentifier:    request.BasketIdentifier,
		NetCashInMinorUnits: netCash,
	}

	var acceptedCount int
	for _, constituent := range resolvedConstituents {
		wasAccepted, filledQuantity, rejectionReason, submissionErr := submitConstituent(
			constituent.InstrumentSymbol, constituent.IsBuyNotSell, constituent.resolvedQuantity,
		)
		outcome := ConstituentOutcome{
			InstrumentSymbol:  constituent.InstrumentSymbol,
			IsBuyNotSell:      constituent.IsBuyNotSell,
			SubmittedQuantity: constituent.resolvedQuantity,
			WasAccepted:       wasAccepted,
			FilledQuantity:    filledQuantity,
			RejectionReason:   rejectionReason,
		}
		if submissionErr != nil && rejectionReason == "" {
			outcome.RejectionReason = submissionErr.Error()
		}
		if wasAccepted {
			acceptedCount++
		}
		result.ConstituentOutcomes = append(result.ConstituentOutcomes, outcome)
	}

	switch {
	case acceptedCount == len(resolvedConstituents):
		result.AggregateStatus = AggregateStatusAllAccepted
	case acceptedCount == 0:
		result.AggregateStatus = AggregateStatusNoneAccepted
	default:
		result.AggregateStatus = AggregateStatusPartialAccepted
	}

	return result, nil
}
