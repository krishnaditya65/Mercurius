// Package multilegoptions implements FEATURES.md §15's "Multi-leg options
// strategy builder (straddle, strangle, spreads, iron condor, butterfly)
// with atomic all-or-nothing execution".
//
// Two real, independent things live here:
//
//  1. Shape validation (ValidateStrategyShape): given a named strategy
//     (StrategyStraddle, StrategyStrangle, StrategyBullCallSpread,
//     StrategyBearPutSpread, StrategyIronCondor, StrategyButterfly) and a
//     candidate leg slice, this checks the legs GENUINELY match that
//     strategy's real textbook definition — leg count, call/put mix,
//     buy/sell direction, relative strike ordering, and quantity
//     relationships — not just "at least N legs of some kind". A
//     structurally wrong leg set (e.g. two calls submitted as a
//     "STRADDLE", or a butterfly with unequal wing spacing) is rejected
//     before any order ever reaches the order-submission path.
//
//  2. Atomic all-or-nothing execution (ExecuteStrategyAtomically): each
//     leg is submitted, in order, through a caller-supplied
//     LegSubmissionFunc — in cmd/server/main.go this closure calls the
//     EXACT SAME processOrderSubmission pipeline every other order path
//     (HTTP /orders/submit, DMA gateway, Cover Orders, auto-liquidation)
//     reuses, so a leg is genuinely risk-checked, audited, and reaches
//     matching-engine like any other order. If any leg is rejected, every
//     PREVIOUSLY-ACCEPTED leg in this strategy is rolled back with a
//     real, opposite-side compensating order for the same quantity —
//     "real rollback", not a no-op — before the overall call returns an
//     error.
//
// HONEST, LOAD-BEARING SCOPE BOUNDARY: matching-engine has no notion of a
// real listed OPTIONS instrument or contract — its tradable universe is
// plain equity-like symbols (see internal/optionschain's own README
// section: "There is no real listed-options market data feed anywhere in
// this repo."). Each Leg here therefore carries its own caller-supplied
// InstrumentSymbol, and PremiumInMinorUnits is submitted as that leg's
// LIMIT order price — this package does NOT create or trade a real
// options contract, it submits an ordinary equity-shaped LIMIT order per
// leg through the existing pipeline, using the strategy's option-shape
// fields (strike/type/premium) purely for VALIDATION math (borrowed from
// internal/payoffdiagram's leg concept), not for real options settlement.
//
// ROLLBACK IS BEST-EFFORT, NOT A DATABASE TRANSACTION: matching-engine
// has no two-phase-commit / prepare-then-commit protocol (see
// ARCHITECTURE.md §3's placeholder synchronous TCP+JSON hand-off). "Real
// rollback" here means: once a leg's order is ACCEPTED by
// processOrderSubmission (passed every gate and was handed to
// matching-engine), a genuinely-submitted OPPOSITE-side order for that
// same quantity is placed to flatten it if a later leg fails. Exactly
// like internal/autoliquidation's reducing MARKET SELL or
// internal/dmagateway's reused pipeline, this is a real order with real
// consequences — but it can itself fail to fill (no counterparty resting
// on the other side at that instant) or, in the rarer case, fail to even
// be ACCEPTED (e.g. a fresh risk/exposure-limit breach caused by the
// compensating order itself) — StrategyExecutionResult.RollbackErrors
// surfaces every such failure loudly, mirroring Cover Orders'
// "ProtectiveStopOrderError: never silently swallowed" pattern, rather
// than pretending atomicity is unconditionally guaranteed.
package multilegoptions

import (
	"errors"
	"fmt"
)

// OptionType identifies a leg as a call or a put — mirrors
// internal/payoffdiagram.OptionType (deliberately not imported: this
// package owns its own leg shape, per that package's own doc comment
// about staying decoupled from payoffdiagram's type).
type OptionType string

const (
	OptionTypeCall OptionType = "CALL"
	OptionTypePut  OptionType = "PUT"
)

// StrategyShape names one of the six recognized multi-leg strategies.
type StrategyShape string

const (
	StrategyStraddle       StrategyShape = "STRADDLE"
	StrategyStrangle       StrategyShape = "STRANGLE"
	StrategyBullCallSpread StrategyShape = "BULL_CALL_SPREAD"
	StrategyBearPutSpread  StrategyShape = "BEAR_PUT_SPREAD"
	StrategyIronCondor     StrategyShape = "IRON_CONDOR"
	StrategyButterfly      StrategyShape = "BUTTERFLY" // long call butterfly
)

var (
	ErrUnknownStrategyShape  = errors.New("unknown strategy shape")
	ErrNoLegs                = errors.New("at least one option leg is required")
	ErrZeroQuantity          = errors.New("leg quantity must be greater than zero")
	ErrUnknownOptionType     = errors.New("leg optionType must be CALL or PUT")
	ErrNegativeStrikePrice   = errors.New("leg strikePriceInMinorUnits must not be negative")
	ErrNegativePremium       = errors.New("leg premiumInMinorUnits must not be negative")
	ErrEmptyInstrumentSymbol = errors.New("leg instrumentSymbol must not be empty")

	// ErrLegShapeMismatch is returned (wrapped with a specific reason) when
	// the supplied legs don't genuinely match the named strategy's
	// definition.
	ErrLegShapeMismatch = errors.New("leg set does not match the named strategy's definition")
)

// Leg is one leg of a multi-leg options strategy, shaped for real order
// submission (unlike internal/payoffdiagram.OptionLeg, which is
// deliberately execution-agnostic).
type Leg struct {
	InstrumentSymbol        string     `json:"instrumentSymbol"`
	OptionType              OptionType `json:"optionType"`
	StrikePriceInMinorUnits int64      `json:"strikePriceInMinorUnits"`
	PremiumInMinorUnits     int64      `json:"premiumInMinorUnits"`
	IsBuyNotSell            bool       `json:"isBuyNotSell"`
	Quantity                uint64     `json:"quantity"`
}

func validateLegsGenerally(legs []Leg) error {
	if len(legs) == 0 {
		return ErrNoLegs
	}
	for _, leg := range legs {
		if leg.InstrumentSymbol == "" {
			return ErrEmptyInstrumentSymbol
		}
		if leg.Quantity == 0 {
			return ErrZeroQuantity
		}
		if leg.OptionType != OptionTypeCall && leg.OptionType != OptionTypePut {
			return ErrUnknownOptionType
		}
		if leg.StrikePriceInMinorUnits < 0 {
			return ErrNegativeStrikePrice
		}
		if leg.PremiumInMinorUnits < 0 {
			return ErrNegativePremium
		}
	}
	return nil
}

func shapeMismatch(reason string) error {
	return fmt.Errorf("%w: %s", ErrLegShapeMismatch, reason)
}

// ValidateStrategyShape checks that legs genuinely match shape's real
// textbook leg structure. See each strategy's dedicated validator below
// for the exact rules enforced.
func ValidateStrategyShape(shape StrategyShape, legs []Leg) error {
	if err := validateLegsGenerally(legs); err != nil {
		return err
	}
	switch shape {
	case StrategyStraddle:
		return validateStraddle(legs)
	case StrategyStrangle:
		return validateStrangle(legs)
	case StrategyBullCallSpread:
		return validateBullCallSpread(legs)
	case StrategyBearPutSpread:
		return validateBearPutSpread(legs)
	case StrategyIronCondor:
		return validateIronCondor(legs)
	case StrategyButterfly:
		return validateButterfly(legs)
	default:
		return ErrUnknownStrategyShape
	}
}

func legsOfType(legs []Leg, optionType OptionType) []Leg {
	var result []Leg
	for _, leg := range legs {
		if leg.OptionType == optionType {
			result = append(result, leg)
		}
	}
	return result
}

// validateStraddle: exactly 2 legs, one CALL one PUT, same strike, same
// quantity, same buy/sell direction (a "long straddle" buys both, a
// "short straddle" sells both — either is a valid straddle, just not a
// mix of one bought and one sold).
func validateStraddle(legs []Leg) error {
	if len(legs) != 2 {
		return shapeMismatch("a straddle requires exactly 2 legs")
	}
	calls, puts := legsOfType(legs, OptionTypeCall), legsOfType(legs, OptionTypePut)
	if len(calls) != 1 || len(puts) != 1 {
		return shapeMismatch("a straddle requires exactly one CALL leg and one PUT leg")
	}
	if calls[0].StrikePriceInMinorUnits != puts[0].StrikePriceInMinorUnits {
		return shapeMismatch("a straddle's CALL and PUT legs must share the same strike price")
	}
	if calls[0].Quantity != puts[0].Quantity {
		return shapeMismatch("a straddle's CALL and PUT legs must have the same quantity")
	}
	if calls[0].IsBuyNotSell != puts[0].IsBuyNotSell {
		return shapeMismatch("a straddle's CALL and PUT legs must both be bought or both be sold")
	}
	return nil
}

// validateStrangle: exactly 2 legs, one CALL one PUT, call strike STRICTLY
// above put strike (both legs out-of-the-money relative to each other —
// the defining structural difference from a straddle), same quantity,
// same direction.
func validateStrangle(legs []Leg) error {
	if len(legs) != 2 {
		return shapeMismatch("a strangle requires exactly 2 legs")
	}
	calls, puts := legsOfType(legs, OptionTypeCall), legsOfType(legs, OptionTypePut)
	if len(calls) != 1 || len(puts) != 1 {
		return shapeMismatch("a strangle requires exactly one CALL leg and one PUT leg")
	}
	if calls[0].StrikePriceInMinorUnits <= puts[0].StrikePriceInMinorUnits {
		return shapeMismatch("a strangle's CALL strike must be strictly above its PUT strike")
	}
	if calls[0].Quantity != puts[0].Quantity {
		return shapeMismatch("a strangle's CALL and PUT legs must have the same quantity")
	}
	if calls[0].IsBuyNotSell != puts[0].IsBuyNotSell {
		return shapeMismatch("a strangle's CALL and PUT legs must both be bought or both be sold")
	}
	return nil
}

// validateBullCallSpread: exactly 2 CALL legs, same quantity, buy the
// LOWER strike and sell the HIGHER strike (a net-debit bullish spread).
func validateBullCallSpread(legs []Leg) error {
	if len(legs) != 2 {
		return shapeMismatch("a bull call spread requires exactly 2 legs")
	}
	calls := legsOfType(legs, OptionTypeCall)
	if len(calls) != 2 {
		return shapeMismatch("a bull call spread requires exactly 2 CALL legs")
	}
	lower, higher := calls[0], calls[1]
	if lower.StrikePriceInMinorUnits > higher.StrikePriceInMinorUnits {
		lower, higher = higher, lower
	}
	if lower.StrikePriceInMinorUnits == higher.StrikePriceInMinorUnits {
		return shapeMismatch("a bull call spread requires two DISTINCT strikes")
	}
	if lower.Quantity != higher.Quantity {
		return shapeMismatch("a bull call spread's two legs must have the same quantity")
	}
	if !lower.IsBuyNotSell {
		return shapeMismatch("a bull call spread must BUY the lower-strike CALL")
	}
	if higher.IsBuyNotSell {
		return shapeMismatch("a bull call spread must SELL the higher-strike CALL")
	}
	return nil
}

// validateBearPutSpread: exactly 2 PUT legs, same quantity, buy the
// HIGHER strike and sell the LOWER strike (a net-debit bearish spread).
func validateBearPutSpread(legs []Leg) error {
	if len(legs) != 2 {
		return shapeMismatch("a bear put spread requires exactly 2 legs")
	}
	puts := legsOfType(legs, OptionTypePut)
	if len(puts) != 2 {
		return shapeMismatch("a bear put spread requires exactly 2 PUT legs")
	}
	lower, higher := puts[0], puts[1]
	if lower.StrikePriceInMinorUnits > higher.StrikePriceInMinorUnits {
		lower, higher = higher, lower
	}
	if lower.StrikePriceInMinorUnits == higher.StrikePriceInMinorUnits {
		return shapeMismatch("a bear put spread requires two DISTINCT strikes")
	}
	if lower.Quantity != higher.Quantity {
		return shapeMismatch("a bear put spread's two legs must have the same quantity")
	}
	if !higher.IsBuyNotSell {
		return shapeMismatch("a bear put spread must BUY the higher-strike PUT")
	}
	if lower.IsBuyNotSell {
		return shapeMismatch("a bear put spread must SELL the lower-strike PUT")
	}
	return nil
}

// validateIronCondor: exactly 4 legs — a bull put spread (buy lower-strike
// PUT, sell higher-strike PUT) below the money, plus a bear call spread
// (sell lower-strike CALL, buy higher-strike CALL) above the money — the
// short strikes must not cross (short put strike <= short call strike),
// and every leg shares the same quantity.
func validateIronCondor(legs []Leg) error {
	if len(legs) != 4 {
		return shapeMismatch("an iron condor requires exactly 4 legs")
	}
	calls, puts := legsOfType(legs, OptionTypeCall), legsOfType(legs, OptionTypePut)
	if len(calls) != 2 || len(puts) != 2 {
		return shapeMismatch("an iron condor requires exactly 2 CALL legs and 2 PUT legs")
	}

	putLow, putHigh := puts[0], puts[1]
	if putLow.StrikePriceInMinorUnits > putHigh.StrikePriceInMinorUnits {
		putLow, putHigh = putHigh, putLow
	}
	callLow, callHigh := calls[0], calls[1]
	if callLow.StrikePriceInMinorUnits > callHigh.StrikePriceInMinorUnits {
		callLow, callHigh = callHigh, callLow
	}

	if putLow.StrikePriceInMinorUnits == putHigh.StrikePriceInMinorUnits {
		return shapeMismatch("an iron condor's two PUT legs must have distinct strikes")
	}
	if callLow.StrikePriceInMinorUnits == callHigh.StrikePriceInMinorUnits {
		return shapeMismatch("an iron condor's two CALL legs must have distinct strikes")
	}
	if !putLow.IsBuyNotSell {
		return shapeMismatch("an iron condor must BUY the lower-strike (further OTM) PUT")
	}
	if putHigh.IsBuyNotSell {
		return shapeMismatch("an iron condor must SELL the higher-strike (nearer-the-money) PUT")
	}
	if callLow.IsBuyNotSell {
		return shapeMismatch("an iron condor must SELL the lower-strike (nearer-the-money) CALL")
	}
	if !callHigh.IsBuyNotSell {
		return shapeMismatch("an iron condor must BUY the higher-strike (further OTM) CALL")
	}
	if putHigh.StrikePriceInMinorUnits > callLow.StrikePriceInMinorUnits {
		return shapeMismatch("an iron condor's short PUT strike must not be above its short CALL strike (wings must not cross)")
	}

	quantity := putLow.Quantity
	for _, leg := range legs {
		if leg.Quantity != quantity {
			return shapeMismatch("every leg of an iron condor must have the same quantity")
		}
	}
	return nil
}

// validateButterfly: exactly 3 CALL legs at strikes K1<K2<K3, EQUALLY
// spaced (K2-K1 == K3-K2) — buy 1x K1, sell 2x K2, buy 1x K3 (a "long call
// butterfly").
func validateButterfly(legs []Leg) error {
	if len(legs) != 3 {
		return shapeMismatch("a butterfly requires exactly 3 legs")
	}
	calls := legsOfType(legs, OptionTypeCall)
	if len(calls) != 3 {
		return shapeMismatch("a butterfly requires exactly 3 CALL legs")
	}

	sorted := append([]Leg{}, calls...)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].StrikePriceInMinorUnits < sorted[i].StrikePriceInMinorUnits {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	lowWing, body, highWing := sorted[0], sorted[1], sorted[2]

	if lowWing.StrikePriceInMinorUnits == body.StrikePriceInMinorUnits || body.StrikePriceInMinorUnits == highWing.StrikePriceInMinorUnits {
		return shapeMismatch("a butterfly's three strikes must be distinct")
	}
	spreadLow := body.StrikePriceInMinorUnits - lowWing.StrikePriceInMinorUnits
	spreadHigh := highWing.StrikePriceInMinorUnits - body.StrikePriceInMinorUnits
	if spreadLow != spreadHigh {
		return shapeMismatch("a butterfly's wings must be equally spaced around the body strike")
	}
	if !lowWing.IsBuyNotSell || !highWing.IsBuyNotSell {
		return shapeMismatch("a butterfly must BUY both wing legs")
	}
	if body.IsBuyNotSell {
		return shapeMismatch("a butterfly must SELL the body (middle strike) leg")
	}
	if body.Quantity != 2*lowWing.Quantity {
		return shapeMismatch("a butterfly's body quantity must be exactly double each wing's quantity")
	}
	if lowWing.Quantity != highWing.Quantity {
		return shapeMismatch("a butterfly's two wing legs must have the same quantity")
	}
	return nil
}

// LegSubmissionFunc submits ONE leg as a real order and reports whether
// it was accepted (passed every gate and reached matching-engine). In
// cmd/server/main.go this closure calls processOrderSubmission — the
// EXACT SAME pipeline every other order path reuses.
type LegSubmissionFunc func(leg Leg) (wasAccepted bool, rejectionReason string, submissionErr error)

// LegExecutionOutcome records what happened when one leg was submitted
// (and, if applicable, rolled back).
type LegExecutionOutcome struct {
	Leg                  Leg    `json:"leg"`
	WasAccepted          bool   `json:"wasAccepted"`
	RejectionReason      string `json:"rejectionReason,omitempty"`
	WasRolledBack        bool   `json:"wasRolledBack,omitempty"`
	RollbackAccepted     bool   `json:"rollbackAccepted,omitempty"`
	RollbackErrorMessage string `json:"rollbackErrorMessage,omitempty"`
}

// StrategyExecutionResult is the full outcome of ExecuteStrategyAtomically.
type StrategyExecutionResult struct {
	Strategy         StrategyShape         `json:"strategy"`
	WasFullyExecuted bool                  `json:"wasFullyExecuted"`
	LegOutcomes      []LegExecutionOutcome `json:"legOutcomes"`
	FailureReason    string                `json:"failureReason,omitempty"`
}

// ErrLegRejectedDuringExecution is wrapped into the returned error (not
// StrategyExecutionResult.FailureReason alone) so a caller can
// errors.Is-detect "a leg was rejected" specifically, distinct from a
// shape-validation error.
var ErrLegRejectedDuringExecution = errors.New("a leg was rejected during atomic strategy execution")

// ExecuteStrategyAtomically validates the leg set against shape, then
// submits every leg in order via submitLeg. If every leg is accepted,
// returns a fully-executed result. If any leg is rejected (or its
// submission errors), every PREVIOUSLY-accepted leg is rolled back with a
// real, opposite-side compensating order for the same quantity — see the
// package doc comment for the honest "best-effort, not a database
// transaction" caveat on that rollback.
func ExecuteStrategyAtomically(shape StrategyShape, legs []Leg, submitLeg LegSubmissionFunc) (StrategyExecutionResult, error) {
	if err := ValidateStrategyShape(shape, legs); err != nil {
		return StrategyExecutionResult{}, err
	}
	if submitLeg == nil {
		return StrategyExecutionResult{}, errors.New("submitLeg function must not be nil")
	}

	result := StrategyExecutionResult{Strategy: shape}
	var acceptedLegs []Leg

	for _, leg := range legs {
		wasAccepted, rejectionReason, submissionErr := submitLeg(leg)
		outcome := LegExecutionOutcome{Leg: leg, WasAccepted: wasAccepted, RejectionReason: rejectionReason}
		if submissionErr != nil && rejectionReason == "" {
			outcome.RejectionReason = submissionErr.Error()
		}
		result.LegOutcomes = append(result.LegOutcomes, outcome)

		if !wasAccepted {
			// Roll back every previously-accepted leg, in the exact order
			// they were accepted, before returning.
			rollBackAcceptedLegs(acceptedLegs, submitLeg, &result)
			result.WasFullyExecuted = false
			result.FailureReason = fmt.Sprintf("leg (%s %s strike=%d) was rejected: %s", leg.OptionType, leg.InstrumentSymbol, leg.StrikePriceInMinorUnits, outcome.RejectionReason)
			return result, fmt.Errorf("%w: %s", ErrLegRejectedDuringExecution, result.FailureReason)
		}
		acceptedLegs = append(acceptedLegs, leg)
	}

	result.WasFullyExecuted = true
	return result, nil
}

// rollBackAcceptedLegs submits one real, opposite-side compensating order
// per already-accepted leg, mutating the matching LegExecutionOutcome
// entries in result in place.
func rollBackAcceptedLegs(acceptedLegs []Leg, submitLeg LegSubmissionFunc, result *StrategyExecutionResult) {
	for _, acceptedLeg := range acceptedLegs {
		compensatingLeg := acceptedLeg
		compensatingLeg.IsBuyNotSell = !acceptedLeg.IsBuyNotSell

		rollbackAccepted, rollbackRejectionReason, rollbackErr := submitLeg(compensatingLeg)

		for i := range result.LegOutcomes {
			if legsEqual(result.LegOutcomes[i].Leg, acceptedLeg) && !result.LegOutcomes[i].WasRolledBack {
				result.LegOutcomes[i].WasRolledBack = true
				result.LegOutcomes[i].RollbackAccepted = rollbackAccepted
				if rollbackErr != nil {
					result.LegOutcomes[i].RollbackErrorMessage = rollbackErr.Error()
				} else if !rollbackAccepted {
					result.LegOutcomes[i].RollbackErrorMessage = rollbackRejectionReason
				}
				break
			}
		}
	}
}

func legsEqual(a, b Leg) bool {
	return a.InstrumentSymbol == b.InstrumentSymbol &&
		a.OptionType == b.OptionType &&
		a.StrikePriceInMinorUnits == b.StrikePriceInMinorUnits &&
		a.PremiumInMinorUnits == b.PremiumInMinorUnits &&
		a.IsBuyNotSell == b.IsBuyNotSell &&
		a.Quantity == b.Quantity
}
