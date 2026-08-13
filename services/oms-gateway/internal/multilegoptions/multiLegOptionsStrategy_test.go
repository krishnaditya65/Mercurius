package multilegoptions

import (
	"errors"
	"sync"
	"testing"
)

func straddleLegs() []Leg {
	return []Leg{
		{InstrumentSymbol: "DEMO-EQ", OptionType: OptionTypeCall, StrikePriceInMinorUnits: 100, PremiumInMinorUnits: 5, IsBuyNotSell: true, Quantity: 1},
		{InstrumentSymbol: "DEMO-EQ", OptionType: OptionTypePut, StrikePriceInMinorUnits: 100, PremiumInMinorUnits: 5, IsBuyNotSell: true, Quantity: 1},
	}
}

func TestValidateStraddle_Valid(t *testing.T) {
	if err := ValidateStrategyShape(StrategyStraddle, straddleLegs()); err != nil {
		t.Fatalf("expected valid straddle, got %v", err)
	}
}

func TestValidateStraddle_WrongLegCount(t *testing.T) {
	legs := straddleLegs()[:1]
	if err := ValidateStrategyShape(StrategyStraddle, legs); !errors.Is(err, ErrLegShapeMismatch) {
		t.Fatalf("expected ErrLegShapeMismatch, got %v", err)
	}
}

func TestValidateStraddle_MismatchedStrike(t *testing.T) {
	legs := straddleLegs()
	legs[1].StrikePriceInMinorUnits = 110
	if err := ValidateStrategyShape(StrategyStraddle, legs); !errors.Is(err, ErrLegShapeMismatch) {
		t.Fatalf("expected ErrLegShapeMismatch, got %v", err)
	}
}

func TestValidateStraddle_MismatchedDirection(t *testing.T) {
	legs := straddleLegs()
	legs[1].IsBuyNotSell = false
	if err := ValidateStrategyShape(StrategyStraddle, legs); !errors.Is(err, ErrLegShapeMismatch) {
		t.Fatalf("expected ErrLegShapeMismatch, got %v", err)
	}
}

func TestValidateStraddle_TwoCallsRejected(t *testing.T) {
	legs := straddleLegs()
	legs[1].OptionType = OptionTypeCall
	if err := ValidateStrategyShape(StrategyStraddle, legs); !errors.Is(err, ErrLegShapeMismatch) {
		t.Fatalf("expected ErrLegShapeMismatch, got %v", err)
	}
}

func TestValidateStrangle_Valid(t *testing.T) {
	legs := []Leg{
		{InstrumentSymbol: "DEMO-EQ", OptionType: OptionTypeCall, StrikePriceInMinorUnits: 110, PremiumInMinorUnits: 3, IsBuyNotSell: true, Quantity: 1},
		{InstrumentSymbol: "DEMO-EQ", OptionType: OptionTypePut, StrikePriceInMinorUnits: 90, PremiumInMinorUnits: 3, IsBuyNotSell: true, Quantity: 1},
	}
	if err := ValidateStrategyShape(StrategyStrangle, legs); err != nil {
		t.Fatalf("expected valid strangle, got %v", err)
	}
}

func TestValidateStrangle_CallStrikeMustBeAbovePutStrike(t *testing.T) {
	legs := []Leg{
		{InstrumentSymbol: "DEMO-EQ", OptionType: OptionTypeCall, StrikePriceInMinorUnits: 90, PremiumInMinorUnits: 3, IsBuyNotSell: true, Quantity: 1},
		{InstrumentSymbol: "DEMO-EQ", OptionType: OptionTypePut, StrikePriceInMinorUnits: 110, PremiumInMinorUnits: 3, IsBuyNotSell: true, Quantity: 1},
	}
	if err := ValidateStrategyShape(StrategyStrangle, legs); !errors.Is(err, ErrLegShapeMismatch) {
		t.Fatalf("expected ErrLegShapeMismatch, got %v", err)
	}
}

func bullCallSpreadLegs() []Leg {
	return []Leg{
		{InstrumentSymbol: "DEMO-EQ", OptionType: OptionTypeCall, StrikePriceInMinorUnits: 100, PremiumInMinorUnits: 8, IsBuyNotSell: true, Quantity: 5},
		{InstrumentSymbol: "DEMO-EQ", OptionType: OptionTypeCall, StrikePriceInMinorUnits: 110, PremiumInMinorUnits: 3, IsBuyNotSell: false, Quantity: 5},
	}
}

func TestValidateBullCallSpread_Valid(t *testing.T) {
	if err := ValidateStrategyShape(StrategyBullCallSpread, bullCallSpreadLegs()); err != nil {
		t.Fatalf("expected valid bull call spread, got %v", err)
	}
}

func TestValidateBullCallSpread_WrongDirection(t *testing.T) {
	legs := bullCallSpreadLegs()
	legs[0].IsBuyNotSell = false
	legs[1].IsBuyNotSell = true
	if err := ValidateStrategyShape(StrategyBullCallSpread, legs); !errors.Is(err, ErrLegShapeMismatch) {
		t.Fatalf("expected ErrLegShapeMismatch, got %v", err)
	}
}

func TestValidateBullCallSpread_SameStrikeRejected(t *testing.T) {
	legs := bullCallSpreadLegs()
	legs[1].StrikePriceInMinorUnits = 100
	if err := ValidateStrategyShape(StrategyBullCallSpread, legs); !errors.Is(err, ErrLegShapeMismatch) {
		t.Fatalf("expected ErrLegShapeMismatch, got %v", err)
	}
}

func bearPutSpreadLegs() []Leg {
	return []Leg{
		{InstrumentSymbol: "DEMO-EQ", OptionType: OptionTypePut, StrikePriceInMinorUnits: 110, PremiumInMinorUnits: 8, IsBuyNotSell: true, Quantity: 5},
		{InstrumentSymbol: "DEMO-EQ", OptionType: OptionTypePut, StrikePriceInMinorUnits: 100, PremiumInMinorUnits: 3, IsBuyNotSell: false, Quantity: 5},
	}
}

func TestValidateBearPutSpread_Valid(t *testing.T) {
	if err := ValidateStrategyShape(StrategyBearPutSpread, bearPutSpreadLegs()); err != nil {
		t.Fatalf("expected valid bear put spread, got %v", err)
	}
}

func TestValidateBearPutSpread_WrongDirection(t *testing.T) {
	legs := bearPutSpreadLegs()
	legs[0].IsBuyNotSell = false
	legs[1].IsBuyNotSell = true
	if err := ValidateStrategyShape(StrategyBearPutSpread, legs); !errors.Is(err, ErrLegShapeMismatch) {
		t.Fatalf("expected ErrLegShapeMismatch, got %v", err)
	}
}

func ironCondorLegs() []Leg {
	return []Leg{
		{InstrumentSymbol: "DEMO-EQ", OptionType: OptionTypePut, StrikePriceInMinorUnits: 90, PremiumInMinorUnits: 2, IsBuyNotSell: true, Quantity: 2},
		{InstrumentSymbol: "DEMO-EQ", OptionType: OptionTypePut, StrikePriceInMinorUnits: 95, PremiumInMinorUnits: 4, IsBuyNotSell: false, Quantity: 2},
		{InstrumentSymbol: "DEMO-EQ", OptionType: OptionTypeCall, StrikePriceInMinorUnits: 105, PremiumInMinorUnits: 4, IsBuyNotSell: false, Quantity: 2},
		{InstrumentSymbol: "DEMO-EQ", OptionType: OptionTypeCall, StrikePriceInMinorUnits: 110, PremiumInMinorUnits: 2, IsBuyNotSell: true, Quantity: 2},
	}
}

func TestValidateIronCondor_Valid(t *testing.T) {
	if err := ValidateStrategyShape(StrategyIronCondor, ironCondorLegs()); err != nil {
		t.Fatalf("expected valid iron condor, got %v", err)
	}
}

func TestValidateIronCondor_WingsCrossRejected(t *testing.T) {
	legs := ironCondorLegs()
	// short put strike (95) above short call strike (would need call at
	// e.g. 92) -- construct a crossing case directly.
	legs[1].StrikePriceInMinorUnits = 108 // short put now above short call (105)
	if err := ValidateStrategyShape(StrategyIronCondor, legs); !errors.Is(err, ErrLegShapeMismatch) {
		t.Fatalf("expected ErrLegShapeMismatch, got %v", err)
	}
}

func TestValidateIronCondor_WrongLegCount(t *testing.T) {
	legs := ironCondorLegs()[:3]
	if err := ValidateStrategyShape(StrategyIronCondor, legs); !errors.Is(err, ErrLegShapeMismatch) {
		t.Fatalf("expected ErrLegShapeMismatch, got %v", err)
	}
}

func butterflyLegs() []Leg {
	return []Leg{
		{InstrumentSymbol: "DEMO-EQ", OptionType: OptionTypeCall, StrikePriceInMinorUnits: 90, PremiumInMinorUnits: 12, IsBuyNotSell: true, Quantity: 1},
		{InstrumentSymbol: "DEMO-EQ", OptionType: OptionTypeCall, StrikePriceInMinorUnits: 100, PremiumInMinorUnits: 5, IsBuyNotSell: false, Quantity: 2},
		{InstrumentSymbol: "DEMO-EQ", OptionType: OptionTypeCall, StrikePriceInMinorUnits: 110, PremiumInMinorUnits: 1, IsBuyNotSell: true, Quantity: 1},
	}
}

func TestValidateButterfly_Valid(t *testing.T) {
	if err := ValidateStrategyShape(StrategyButterfly, butterflyLegs()); err != nil {
		t.Fatalf("expected valid butterfly, got %v", err)
	}
}

func TestValidateButterfly_UnequalWingSpacingRejected(t *testing.T) {
	legs := butterflyLegs()
	legs[2].StrikePriceInMinorUnits = 115 // spacing now 10 vs 15
	if err := ValidateStrategyShape(StrategyButterfly, legs); !errors.Is(err, ErrLegShapeMismatch) {
		t.Fatalf("expected ErrLegShapeMismatch, got %v", err)
	}
}

func TestValidateButterfly_BodyQuantityMustBeDouble(t *testing.T) {
	legs := butterflyLegs()
	legs[1].Quantity = 3
	if err := ValidateStrategyShape(StrategyButterfly, legs); !errors.Is(err, ErrLegShapeMismatch) {
		t.Fatalf("expected ErrLegShapeMismatch, got %v", err)
	}
}

func TestValidateStrategyShape_UnknownShape(t *testing.T) {
	if err := ValidateStrategyShape("NOT_A_SHAPE", straddleLegs()); !errors.Is(err, ErrUnknownStrategyShape) {
		t.Fatalf("expected ErrUnknownStrategyShape, got %v", err)
	}
}

func TestValidateStrategyShape_NoLegs(t *testing.T) {
	if err := ValidateStrategyShape(StrategyStraddle, nil); !errors.Is(err, ErrNoLegs) {
		t.Fatalf("expected ErrNoLegs, got %v", err)
	}
}

func TestValidateStrategyShape_ZeroQuantity(t *testing.T) {
	legs := straddleLegs()
	legs[0].Quantity = 0
	if err := ValidateStrategyShape(StrategyStraddle, legs); !errors.Is(err, ErrZeroQuantity) {
		t.Fatalf("expected ErrZeroQuantity, got %v", err)
	}
}

// --- ExecuteStrategyAtomically tests ---

func alwaysAcceptSubmitter() (LegSubmissionFunc, *[]Leg) {
	var submitted []Leg
	return func(leg Leg) (bool, string, error) {
		submitted = append(submitted, leg)
		return true, "", nil
	}, &submitted
}

func TestExecuteStrategyAtomically_AllLegsAccepted(t *testing.T) {
	submitFunc, submitted := alwaysAcceptSubmitter()
	result, err := ExecuteStrategyAtomically(StrategyStraddle, straddleLegs(), submitFunc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.WasFullyExecuted {
		t.Fatalf("expected WasFullyExecuted=true")
	}
	if len(*submitted) != 2 {
		t.Fatalf("expected 2 legs submitted, got %d", len(*submitted))
	}
	for _, outcome := range result.LegOutcomes {
		if !outcome.WasAccepted || outcome.WasRolledBack {
			t.Fatalf("expected every leg accepted and not rolled back, got %+v", outcome)
		}
	}
}

func TestExecuteStrategyAtomically_SecondLegRejectedRollsBackFirst(t *testing.T) {
	callCount := 0
	var rollbackLegSeen Leg
	submitFunc := func(leg Leg) (bool, string, error) {
		callCount++
		switch callCount {
		case 1:
			return true, "", nil // first leg accepted
		case 2:
			return false, "INSUFFICIENT_MARGIN", nil // second leg rejected
		case 3:
			rollbackLegSeen = leg
			return true, "", nil // rollback of first leg accepted
		default:
			t.Fatalf("unexpected extra submitLeg call #%d", callCount)
			return false, "", nil
		}
	}

	result, err := ExecuteStrategyAtomically(StrategyStraddle, straddleLegs(), submitFunc)
	if err == nil {
		t.Fatalf("expected an error for a rejected leg")
	}
	if !errors.Is(err, ErrLegRejectedDuringExecution) {
		t.Fatalf("expected ErrLegRejectedDuringExecution, got %v", err)
	}
	if result.WasFullyExecuted {
		t.Fatalf("expected WasFullyExecuted=false")
	}
	if callCount != 3 {
		t.Fatalf("expected exactly 3 submitLeg calls (leg1, leg2, rollback of leg1), got %d", callCount)
	}
	// the rollback leg must be the opposite side of the original first leg
	originalFirstLeg := straddleLegs()[0]
	if rollbackLegSeen.IsBuyNotSell == originalFirstLeg.IsBuyNotSell {
		t.Fatalf("expected rollback leg to be opposite side of original leg")
	}
	if rollbackLegSeen.Quantity != originalFirstLeg.Quantity {
		t.Fatalf("expected rollback leg quantity to match original")
	}

	firstOutcome := result.LegOutcomes[0]
	if !firstOutcome.WasAccepted || !firstOutcome.WasRolledBack || !firstOutcome.RollbackAccepted {
		t.Fatalf("expected first leg outcome accepted+rolled-back+rollback-accepted, got %+v", firstOutcome)
	}
	secondOutcome := result.LegOutcomes[1]
	if secondOutcome.WasAccepted {
		t.Fatalf("expected second leg outcome not accepted")
	}
	if secondOutcome.RejectionReason != "INSUFFICIENT_MARGIN" {
		t.Fatalf("expected rejection reason propagated, got %q", secondOutcome.RejectionReason)
	}
}

func TestExecuteStrategyAtomically_RollbackFailureSurfaced(t *testing.T) {
	callCount := 0
	submitFunc := func(leg Leg) (bool, string, error) {
		callCount++
		switch callCount {
		case 1:
			return true, "", nil
		case 2:
			return false, "INSUFFICIENT_MARGIN", nil
		case 3:
			return false, "TRADING_HALTED", nil // rollback itself fails
		default:
			t.Fatalf("unexpected call #%d", callCount)
			return false, "", nil
		}
	}
	result, err := ExecuteStrategyAtomically(StrategyStraddle, straddleLegs(), submitFunc)
	if err == nil {
		t.Fatalf("expected error")
	}
	firstOutcome := result.LegOutcomes[0]
	if !firstOutcome.WasRolledBack || firstOutcome.RollbackAccepted {
		t.Fatalf("expected rollback attempted but not accepted, got %+v", firstOutcome)
	}
	if firstOutcome.RollbackErrorMessage != "TRADING_HALTED" {
		t.Fatalf("expected rollback error message surfaced, got %q", firstOutcome.RollbackErrorMessage)
	}
}

func TestExecuteStrategyAtomically_FirstLegRejectedNoRollbackNeeded(t *testing.T) {
	callCount := 0
	submitFunc := func(leg Leg) (bool, string, error) {
		callCount++
		return false, "KYC_NOT_VERIFIED", nil
	}
	result, err := ExecuteStrategyAtomically(StrategyStraddle, straddleLegs(), submitFunc)
	if err == nil {
		t.Fatalf("expected error")
	}
	if callCount != 1 {
		t.Fatalf("expected exactly 1 submitLeg call (no rollback needed for zero accepted legs), got %d", callCount)
	}
	if result.LegOutcomes[0].WasRolledBack {
		t.Fatalf("first (only) leg was never accepted, should not be marked rolled back")
	}
}

func TestExecuteStrategyAtomically_InvalidShapeRejectedBeforeAnySubmission(t *testing.T) {
	callCount := 0
	submitFunc := func(leg Leg) (bool, string, error) {
		callCount++
		return true, "", nil
	}
	_, err := ExecuteStrategyAtomically(StrategyStraddle, straddleLegs()[:1], submitFunc)
	if !errors.Is(err, ErrLegShapeMismatch) {
		t.Fatalf("expected ErrLegShapeMismatch, got %v", err)
	}
	if callCount != 0 {
		t.Fatalf("expected zero submissions for a shape-invalid request, got %d", callCount)
	}
}

func TestExecuteStrategyAtomically_NilSubmitFunc(t *testing.T) {
	_, err := ExecuteStrategyAtomically(StrategyStraddle, straddleLegs(), nil)
	if err == nil {
		t.Fatalf("expected error for nil submitLeg func")
	}
}

// TestExecuteStrategyAtomically_Concurrency proves the pure function has
// no shared mutable state of its own (no package-level state at all) by
// running many concurrent atomic executions against independent
// per-goroutine submitters and checking each one's own bookkeeping stays
// internally consistent.
func TestExecuteStrategyAtomically_Concurrency(t *testing.T) {
	var waitGroup sync.WaitGroup
	for i := 0; i < 50; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			submitFunc, submitted := alwaysAcceptSubmitter()
			result, err := ExecuteStrategyAtomically(StrategyIronCondor, ironCondorLegs(), submitFunc)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if !result.WasFullyExecuted {
				t.Errorf("expected fully executed")
			}
			if len(*submitted) != 4 {
				t.Errorf("expected 4 legs submitted, got %d", len(*submitted))
			}
		}()
	}
	waitGroup.Wait()
}
