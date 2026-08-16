// Package autoliquidation implements FEATURES.md §12's "Auto-liquidation
// on margin breach, with graduated warnings first": given an account's
// real margin utilization, emit graduated risk states — WARNING at 80%,
// URGENT at 90%, LIQUIDATION at 100%+ (all configurable via Thresholds) —
// and, ONLY at the final LIQUIDATION state, actually submit real reducing
// (SELL) orders through the caller-supplied order-submission callback to
// bring utilization back under a configurable target.
//
// DESIGN CHOICE — what "margin utilization" means here: this package
// deliberately stays decoupled from internal/marginfunding and
// internal/marginpledge (the same decoupling pattern those two packages
// already use toward each other and toward internal/riskengine) — it
// operates on an AccountLeverageSnapshot the caller assembles from real
// numbers: OutstandingPrincipalInMinorUnits (from
// marginfunding.FundingBook.OutstandingPrincipalInMinorUnits) and
// PledgedMarginValueInMinorUnits (from
// marginpledge.PledgeBook.TotalPledgedMarginValueForAccount). Utilization
// is OutstandingPrincipal / PledgedMarginValue — how much of the
// account's real pledge-backed borrowing capacity is currently drawn
// down. Under normal operation internal/marginfunding.RequestFunding
// never lets this exceed 100% at the moment funding is disbursed, but it
// CAN cross 100% afterwards if the pledged collateral's value falls (a
// live re-pricing this build doesn't automate — see
// internal/marginpledge's own documented gap on caller-supplied reference
// prices) or if utilized-margin-for-open-derivative-positions increases.
// This package is what genuinely reacts once that happens.
//
// DESIGN CHOICE — how a reducing order is sized and submitted: at
// LIQUIDATION state, this package computes exactly how much notional
// needs to be sold to bring OutstandingPrincipal back down to
// TargetUtilizationPercent of PledgedMarginValue, then walks the
// account's LONG positions (largest notional first — an illustrative,
// documented choice; a real book would likely prioritize by liquidity or
// margin-relief-per-share instead) selling whole-share quantities at each
// position's supplied reference price (internal/marktomarket's pushed
// market price is the natural source — see that package) until the
// shortfall is covered or positions run out. Each reducing order is
// submitted through the caller-supplied SubmitReducingOrder callback —
// EXACTLY the same processOrderSubmission pipeline
// cmd/server/main.go's DMA gateway closure already reuses — so a real
// liquidation genuinely risk-checks, audits, and reaches matching-engine
// like any other order. Short positions are NOT liquidated by this build
// (that needs a BUY-to-cover order, an intentionally excluded scope
// boundary documented as a known gap below).
//
// KNOWN GAPS: (1) short-position liquidation is not implemented (only
// long positions are sold down); (2) no automatic scheduler/poller drives
// this — a real build would evaluate every leveraged account
// periodically or on every price tick, this build only evaluates on an
// explicit call (POST /auto-liquidation/evaluate in cmd/server/main.go),
// same pattern internal/marketsession's admin-toggle-not-real-clock
// already uses; (3) liquidation sizing needs a real market price for
// every position being sold — a position with no known market price
// (internal/marktomarket.MarketPrice returns unknown) is skipped, which
// can leave an account under-liquidated if that's its only holding —
// documented, not silently hidden (see LiquidationOutcome.
// SkippedPositionsMissingPrice).
package autoliquidation

import (
	"errors"
	"sync"
)

// ErrSubmitReducingOrderRequired is returned by NewLiquidationEngine when
// constructed without a submission callback.
var ErrSubmitReducingOrderRequired = errors.New("autoliquidation: a SubmitReducingOrder callback is required")

// RiskState is the graduated classification of an account's margin
// utilization.
type RiskState string

const (
	RiskStateNormal      RiskState = "NORMAL"
	RiskStateWarning     RiskState = "WARNING"
	RiskStateUrgent      RiskState = "URGENT"
	RiskStateLiquidation RiskState = "LIQUIDATION"
)

// Thresholds configures the utilization percentages (0-100 scale, e.g.
// 80.0 for 80%) at which each graduated state begins. LIQUIDATION begins
// at 100.0 implicitly (utilization >= 100% means the account has drawn
// more than its real pledge-backed capacity) and is not itself
// configurable, since it is definitionally "at or past the limit", not an
// arbitrary early-warning line.
type Thresholds struct {
	WarningUtilizationPercent float64
	UrgentUtilizationPercent  float64
	// TargetUtilizationPercent is how far DOWN a liquidation should bring
	// the account — deliberately below 100% (not exactly 100%) so a
	// single liquidation pass doesn't leave the account sitting exactly
	// on the breach line.
	TargetUtilizationPercent float64
}

// DefaultThresholds returns the illustrative graduated levels FEATURES.md
// §12 names explicitly: WARNING at 80%, URGENT at 90%, and a liquidation
// target of 75% (comfortably under the implicit 100% LIQUIDATION line).
func DefaultThresholds() Thresholds {
	return Thresholds{
		WarningUtilizationPercent: 80.0,
		UrgentUtilizationPercent:  90.0,
		TargetUtilizationPercent:  75.0,
	}
}

// PositionForLiquidation is one long position a liquidation pass may
// partially or fully sell down.
type PositionForLiquidation struct {
	InstrumentSymbol               string
	NetQuantity                    int64
	CurrentMarketPriceInMinorUnits int64
	// MarketPriceIsKnown must be true for this position to be eligible
	// for liquidation — see the package doc's third known gap.
	MarketPriceIsKnown bool
}

// AccountLeverageSnapshot is everything one evaluation pass needs about
// one account, assembled by the caller from real package state (see the
// package doc's decoupling design choice).
type AccountLeverageSnapshot struct {
	ClientAccountIdentifier          string
	OutstandingPrincipalInMinorUnits int64
	PledgedMarginValueInMinorUnits   int64
	Positions                        []PositionForLiquidation
}

// UtilizationPercent computes OutstandingPrincipal / PledgedMarginValue
// as a 0-100+ percentage. An account with zero pledged margin value and
// zero outstanding principal is 0% utilized (not leveraged at all); zero
// pledged margin value with positive outstanding principal is treated as
// maximally breached (a real, if degenerate, case: e.g. every pledge was
// released while a loan was still outstanding).
func (snapshot AccountLeverageSnapshot) UtilizationPercent() float64 {
	if snapshot.PledgedMarginValueInMinorUnits <= 0 {
		if snapshot.OutstandingPrincipalInMinorUnits > 0 {
			return 100000.0 // arbitrarily large: unambiguously past every threshold
		}
		return 0.0
	}
	return (float64(snapshot.OutstandingPrincipalInMinorUnits) / float64(snapshot.PledgedMarginValueInMinorUnits)) * 100.0
}

// ClassifyUtilization is the pure, side-effect-free graduated-state
// classification — used both by the real evaluate-and-act path below and
// by a read-only status endpoint that must NEVER trigger a liquidation
// just by being queried.
func ClassifyUtilization(utilizationPercent float64, thresholds Thresholds) RiskState {
	switch {
	case utilizationPercent >= 100.0:
		return RiskStateLiquidation
	case utilizationPercent >= thresholds.UrgentUtilizationPercent:
		return RiskStateUrgent
	case utilizationPercent >= thresholds.WarningUtilizationPercent:
		return RiskStateWarning
	default:
		return RiskStateNormal
	}
}

// SubmittedReducingOrder records one real reducing order a liquidation
// pass actually submitted.
type SubmittedReducingOrder struct {
	InstrumentSymbol           string
	QuantitySold               int64 // the quantity the engine ATTEMPTED to sell -- not necessarily what actually filled
	ReferencePriceInMinorUnits int64
	// NotionalReducedInMinorUnits is the REAL executed notional this
	// order actually filled, as reported by SubmitReducingOrderFunc —
	// NOT the assumed (QuantitySold * ReferencePriceInMinorUnits)
	// estimate. This is what remainingShortfall is actually decremented
	// by (see the fix on this bug in SubmitReducingOrderFunc's doc
	// comment).
	NotionalReducedInMinorUnits int64
	SubmissionError             string // empty if the submission itself succeeded (the order may still have been risk/KYC/freeze-rejected downstream — see SubmissionError vs WasAccepted below)
	WasAccepted                 bool   // true if the ORDER-SUBMISSION pipeline accepted the order -- NOT proof it filled; see NotionalReducedInMinorUnits for what actually happened
}

// LiquidationOutcome is the full result of one evaluate-and-act pass.
type LiquidationOutcome struct {
	ClientAccountIdentifier        string
	UtilizationPercentBeforeAction float64
	RiskState                      RiskState
	SubmittedReducingOrders        []SubmittedReducingOrder
	RemainingShortfallInMinorUnits int64 // > 0 if liquidation couldn't fully close the gap (ran out of eligible long positions)
	SkippedPositionsMissingPrice   []string
}

// SubmitReducingOrderFunc is the real order-submission callback — the
// caller wires this to the exact same processOrderSubmission pipeline
// every other order path in this service uses (see the package doc).
// Implementations should submit a real SELL order for quantityToSell of
// instrumentSymbol and report:
//   - wasAccepted: whether the order-submission pipeline itself accepted
//     the order (passed KYC/freeze/risk/etc and was handed to the
//     matching engine) — NOT whether it actually filled. An accepted
//     order can rest, partially fill, or fill nothing at all.
//   - actualExecutedNotionalInMinorUnits: the REAL notional that
//     actually filled (e.g. summed from
//     OrderAcknowledgementResponse.TradeExecutionEvents), NOT the
//     assumedNotionalInMinorUnits the engine sized the order from. This
//     is the figure EvaluateAndLiquidateIfBreached uses to decrement
//     remainingShortfall — an accepted-but-unfilled (or
//     partially-filled) order must NOT be treated as having closed the
//     full assumed shortfall.
//   - submissionError: non-nil if the submission itself failed.
//
// assumedNotionalInMinorUnits is quantityToSell *
// position.CurrentMarketPriceInMinorUnits — the estimate the engine used
// to size this order — passed through so an implementation MAY report it
// back verbatim for the common "filled exactly as assumed" case.
type SubmitReducingOrderFunc func(
	clientAccountIdentifier string,
	instrumentSymbol string,
	quantityToSell int64,
	assumedNotionalInMinorUnits int64,
) (wasAccepted bool, actualExecutedNotionalInMinorUnits int64, submissionError error)

// LiquidationEngine is the mutex-guarded evaluator. Mutex-guarded even
// though it holds no mutable state of its own beyond the callback,
// mirroring this codebase's convention of every stateful-looking engine
// type carrying an explicit mutex — here it exists to serialize
// concurrent evaluate-and-act calls for the SAME account so two
// simultaneous breaches of one account can't both compute a shortfall
// against stale data and double-liquidate.
type LiquidationEngine struct {
	mutexGuardingEvaluation sync.Mutex
	thresholds              Thresholds
	submitReducingOrder     SubmitReducingOrderFunc
}

// NewLiquidationEngine constructs a LiquidationEngine. submitReducingOrder
// must not be nil.
func NewLiquidationEngine(thresholds Thresholds, submitReducingOrder SubmitReducingOrderFunc) (*LiquidationEngine, error) {
	if submitReducingOrder == nil {
		return nil, ErrSubmitReducingOrderRequired
	}
	return &LiquidationEngine{
		thresholds:          thresholds,
		submitReducingOrder: submitReducingOrder,
	}, nil
}

// EvaluateAndLiquidateIfBreached classifies the account's current
// utilization and — ONLY if that classification is RiskStateLiquidation
// — actually submits real reducing orders to bring utilization back down
// to thresholds.TargetUtilizationPercent. At WARNING or URGENT (or
// NORMAL), this is a pure read: no order is ever submitted, proven by
// dedicated tests.
func (engine *LiquidationEngine) EvaluateAndLiquidateIfBreached(snapshot AccountLeverageSnapshot) LiquidationOutcome {
	engine.mutexGuardingEvaluation.Lock()
	defer engine.mutexGuardingEvaluation.Unlock()

	utilizationPercent := snapshot.UtilizationPercent()
	riskState := ClassifyUtilization(utilizationPercent, engine.thresholds)

	outcome := LiquidationOutcome{
		ClientAccountIdentifier:        snapshot.ClientAccountIdentifier,
		UtilizationPercentBeforeAction: utilizationPercent,
		RiskState:                      riskState,
	}

	if riskState != RiskStateLiquidation {
		return outcome
	}

	targetOutstandingInMinorUnits := int64(engine.thresholds.TargetUtilizationPercent / 100.0 * float64(snapshot.PledgedMarginValueInMinorUnits))
	remainingShortfall := snapshot.OutstandingPrincipalInMinorUnits - targetOutstandingInMinorUnits
	if remainingShortfall <= 0 {
		// Degenerate (e.g. zero pledged margin value): nothing sensible
		// to compute a target against. Report the state; no orders.
		outcome.RemainingShortfallInMinorUnits = snapshot.OutstandingPrincipalInMinorUnits
		return outcome
	}

	orderedPositions := sortPositionsByNotionalDescending(snapshot.Positions)
	for _, position := range orderedPositions {
		if remainingShortfall <= 0 {
			break
		}
		if position.NetQuantity <= 0 {
			continue // short positions are not liquidated here — see package doc gap (1)
		}
		if !position.MarketPriceIsKnown || position.CurrentMarketPriceInMinorUnits <= 0 {
			outcome.SkippedPositionsMissingPrice = append(outcome.SkippedPositionsMissingPrice, position.InstrumentSymbol)
			continue
		}

		quantityNeededToCoverShortfall := ceilDiv(remainingShortfall, position.CurrentMarketPriceInMinorUnits)
		quantityToSell := quantityNeededToCoverShortfall
		if quantityToSell > position.NetQuantity {
			quantityToSell = position.NetQuantity
		}
		if quantityToSell <= 0 {
			continue
		}

		assumedNotionalInMinorUnits := quantityToSell * position.CurrentMarketPriceInMinorUnits
		wasAccepted, actualExecutedNotionalInMinorUnits, submissionError := engine.submitReducingOrder(
			snapshot.ClientAccountIdentifier, position.InstrumentSymbol, quantityToSell, assumedNotionalInMinorUnits,
		)
		submittedOrder := SubmittedReducingOrder{
			InstrumentSymbol:            position.InstrumentSymbol,
			QuantitySold:                quantityToSell,
			ReferencePriceInMinorUnits:  position.CurrentMarketPriceInMinorUnits,
			NotionalReducedInMinorUnits: actualExecutedNotionalInMinorUnits,
			WasAccepted:                 wasAccepted,
		}
		if submissionError != nil {
			submittedOrder.SubmissionError = submissionError.Error()
		}
		outcome.SubmittedReducingOrders = append(outcome.SubmittedReducingOrders, submittedOrder)

		// Only the REAL executed notional closes the shortfall — mere
		// acceptance (wasAccepted) does not: an accepted order can rest,
		// partially fill, or fill nothing at all (see this bug's fix in
		// SubmitReducingOrderFunc's doc comment above).
		if actualExecutedNotionalInMinorUnits > 0 {
			remainingShortfall -= actualExecutedNotionalInMinorUnits
		}
	}

	if remainingShortfall > 0 {
		outcome.RemainingShortfallInMinorUnits = remainingShortfall
	}
	return outcome
}

func sortPositionsByNotionalDescending(positions []PositionForLiquidation) []PositionForLiquidation {
	sorted := make([]PositionForLiquidation, len(positions))
	copy(sorted, positions)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0; j-- {
			currentNotional := sorted[j].NetQuantity * sorted[j].CurrentMarketPriceInMinorUnits
			previousNotional := sorted[j-1].NetQuantity * sorted[j-1].CurrentMarketPriceInMinorUnits
			if currentNotional > previousNotional {
				sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
			} else {
				break
			}
		}
	}
	return sorted
}

func ceilDiv(numerator int64, denominator int64) int64 {
	if denominator <= 0 {
		return 0
	}
	if numerator <= 0 {
		return 0
	}
	quotient := numerator / denominator
	if numerator%denominator != 0 {
		quotient++
	}
	return quotient
}
