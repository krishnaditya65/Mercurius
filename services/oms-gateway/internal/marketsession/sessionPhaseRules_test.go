package marketsession

import (
	"errors"
	"testing"
)

func TestNewMarketSessionState_DefaultsToClosedPhase(t *testing.T) {
	state := NewMarketSessionState()
	if state.CurrentSessionPhase() != SessionPhaseClosed {
		t.Fatalf("expected default phase CLOSED, got %s", state.CurrentSessionPhase())
	}
}

func TestSetSessionPhase_ValidTransitions(t *testing.T) {
	state := NewMarketSessionState()
	for _, phase := range []SessionPhase{SessionPhasePreMarket, SessionPhaseRegular, SessionPhasePostMarket, SessionPhaseClosed} {
		if err := state.SetSessionPhase(phase); err != nil {
			t.Fatalf("unexpected error setting phase %s: %v", phase, err)
		}
		if state.CurrentSessionPhase() != phase {
			t.Fatalf("expected phase %s, got %s", phase, state.CurrentSessionPhase())
		}
	}
}

func TestSetSessionPhase_UnknownPhaseRejected(t *testing.T) {
	state := NewMarketSessionState()
	err := state.SetSessionPhase("NOT_A_PHASE")
	if !errors.Is(err, ErrUnknownSessionPhase) {
		t.Fatalf("expected ErrUnknownSessionPhase, got %v", err)
	}
	// state must be unaffected by a rejected transition
	if state.CurrentSessionPhase() != SessionPhaseClosed {
		t.Fatalf("expected phase to remain CLOSED after a rejected transition")
	}
}

func TestSetSessionPhase_IndependentOfIsMarketOpen(t *testing.T) {
	state := NewMarketSessionState()
	state.SetMarketOpen(true)
	if state.CurrentSessionPhase() != SessionPhaseClosed {
		t.Fatalf("expected SetMarketOpen to leave sessionPhase untouched, got %s", state.CurrentSessionPhase())
	}
	if err := state.SetSessionPhase(SessionPhaseRegular); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	state.SetMarketOpen(false)
	if state.CurrentSessionPhase() != SessionPhaseRegular {
		t.Fatalf("expected SetMarketOpen(false) to leave sessionPhase untouched, got %s", state.CurrentSessionPhase())
	}
}

func limitOrder() OrderShapeForSessionRules {
	return OrderShapeForSessionRules{}
}

func marketOrder() OrderShapeForSessionRules {
	return OrderShapeForSessionRules{OrderIsMarketOrderNotLimit: true}
}

func stopLossOrder() OrderShapeForSessionRules {
	return OrderShapeForSessionRules{OrderIsStopLossVariant: true}
}

func icebergOrder() OrderShapeForSessionRules {
	return OrderShapeForSessionRules{OrderExecutionType: "ICEBERG"}
}

func TestValidateOrderAgainstSessionPhase_RegularAllowsEverything(t *testing.T) {
	for _, order := range []OrderShapeForSessionRules{limitOrder(), marketOrder(), stopLossOrder(), icebergOrder()} {
		if err := ValidateOrderAgainstSessionPhase(SessionPhaseRegular, order, false); err != nil {
			t.Fatalf("expected REGULAR to allow every order shape, got %v for %+v", err, order)
		}
	}
}

func TestValidateOrderAgainstSessionPhase_PreMarketAllowsOnlyLimit(t *testing.T) {
	if err := ValidateOrderAgainstSessionPhase(SessionPhasePreMarket, limitOrder(), false); err != nil {
		t.Fatalf("expected PRE_MARKET to allow a plain LIMIT order, got %v", err)
	}
	if err := ValidateOrderAgainstSessionPhase(SessionPhasePreMarket, marketOrder(), false); !errors.Is(err, ErrOnlyLimitOrdersInPreMarket) {
		t.Fatalf("expected ErrOnlyLimitOrdersInPreMarket for a MARKET order, got %v", err)
	}
	if err := ValidateOrderAgainstSessionPhase(SessionPhasePreMarket, stopLossOrder(), false); !errors.Is(err, ErrOnlyLimitOrdersInPreMarket) {
		t.Fatalf("expected ErrOnlyLimitOrdersInPreMarket for a stop order, got %v", err)
	}
	if err := ValidateOrderAgainstSessionPhase(SessionPhasePreMarket, icebergOrder(), false); !errors.Is(err, ErrOnlyLimitOrdersInPreMarket) {
		t.Fatalf("expected ErrOnlyLimitOrdersInPreMarket for an ICEBERG order, got %v", err)
	}
}

func TestValidateOrderAgainstSessionPhase_PostMarketAllowsOnlyLimit(t *testing.T) {
	if err := ValidateOrderAgainstSessionPhase(SessionPhasePostMarket, limitOrder(), false); err != nil {
		t.Fatalf("expected POST_MARKET to allow a plain LIMIT order, got %v", err)
	}
	if err := ValidateOrderAgainstSessionPhase(SessionPhasePostMarket, marketOrder(), false); !errors.Is(err, ErrOnlyLimitOrdersInPostMarket) {
		t.Fatalf("expected ErrOnlyLimitOrdersInPostMarket for a MARKET order, got %v", err)
	}
}

// TestValidateOrderAgainstSessionPhase_ClosedIsANoOpForThisGate: CLOSED's
// real rejection behavior belongs to the pre-existing isMarketOpen/AMO
// mechanism, not this new gate — see the package doc's "REGULAR and
// CLOSED are BOTH treated as no additional restriction" note.
func TestValidateOrderAgainstSessionPhase_ClosedIsANoOpForThisGate(t *testing.T) {
	for _, order := range []OrderShapeForSessionRules{limitOrder(), marketOrder(), stopLossOrder(), icebergOrder()} {
		if err := ValidateOrderAgainstSessionPhase(SessionPhaseClosed, order, false); err != nil {
			t.Fatalf("expected CLOSED to impose no restriction from this gate, got %v for %+v", err, order)
		}
	}
}

func TestValidateOrderAgainstSessionPhase_ExplicitLimitExecutionTypeAllowed(t *testing.T) {
	order := OrderShapeForSessionRules{OrderExecutionType: "LIMIT"}
	if err := ValidateOrderAgainstSessionPhase(SessionPhasePreMarket, order, false); err != nil {
		t.Fatalf("expected explicit LIMIT executionType to be treated as a plain limit order, got %v", err)
	}
}

func TestValidateOrderAgainstSessionPhase_FokIocRejectedPrePostMarket(t *testing.T) {
	fok := OrderShapeForSessionRules{OrderExecutionType: "FOK"}
	if err := ValidateOrderAgainstSessionPhase(SessionPhasePreMarket, fok, false); !errors.Is(err, ErrOnlyLimitOrdersInPreMarket) {
		t.Fatalf("expected FOK rejected pre-market, got %v", err)
	}
	ioc := OrderShapeForSessionRules{OrderExecutionType: "IOC"}
	if err := ValidateOrderAgainstSessionPhase(SessionPhasePostMarket, ioc, false); !errors.Is(err, ErrOnlyLimitOrdersInPostMarket) {
		t.Fatalf("expected IOC rejected post-market, got %v", err)
	}
}

func TestValidateOrderAgainstSessionPhase_UnknownPhaseRejected(t *testing.T) {
	if err := ValidateOrderAgainstSessionPhase("BOGUS", limitOrder(), false); !errors.Is(err, ErrUnknownSessionPhase) {
		t.Fatalf("expected ErrUnknownSessionPhase, got %v", err)
	}
}
