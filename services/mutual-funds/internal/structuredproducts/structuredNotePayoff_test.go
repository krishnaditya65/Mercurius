package structuredproducts

import "testing"

func testNote() Note {
	return Note{
		NoteId: "TEST-NOTE", Name: "Test Note", UnderlyingIndexName: "Test Index",
		PrincipalProtectionPercent: 100, ParticipationRatePercent: 150, CapPercent: 20, TenorMonths: 36,
	}
}

// TestCalculatePayoffCapitalProtectedOnNegativeReturn is a hand-worked
// case: the index falls 15%, but PrincipalProtectionPercent is 100%, so
// the FULL principal (100000) is returned regardless — zero downside
// participation, by the note's own definition.
func TestCalculatePayoffCapitalProtectedOnNegativeReturn(t *testing.T) {
	payout, effectiveReturn, wasCapped := CalculatePayoff(testNote(), 100000, -15.0)
	if payout != 100000 {
		t.Fatalf("expected full principal 100000 returned, got %d", payout)
	}
	if effectiveReturn != 0 {
		t.Fatalf("expected 0%% effective return on the downside, got %v", effectiveReturn)
	}
	if wasCapped {
		t.Fatal("expected wasCapped=false on the downside")
	}
}

func TestCalculatePayoffCapitalProtectedOnZeroReturn(t *testing.T) {
	payout, _, _ := CalculatePayoff(testNote(), 100000, 0)
	if payout != 100000 {
		t.Fatalf("expected full principal 100000 at flat index return, got %d", payout)
	}
}

// TestCalculatePayoffUncappedParticipation is a hand-worked case:
// principal 100000, participation rate 150%, index return +10%.
// participatedReturn = 10 * 150/100 = 15%, under the 20% cap, so
// payout = 100000 * 1.15 = 115000 exactly.
func TestCalculatePayoffUncappedParticipation(t *testing.T) {
	payout, effectiveReturn, wasCapped := CalculatePayoff(testNote(), 100000, 10.0)
	if payout != 115000 {
		t.Fatalf("expected payout 115000, got %d", payout)
	}
	if effectiveReturn != 15.0 {
		t.Fatalf("expected effective return 15%%, got %v", effectiveReturn)
	}
	if wasCapped {
		t.Fatal("expected wasCapped=false when under the cap")
	}
}

// TestCalculatePayoffCappedParticipation is a hand-worked case: principal
// 100000, participation rate 150%, index return +20%. participatedReturn
// = 20 * 150/100 = 30%, ABOVE the 20% cap, so effectiveReturn is capped at
// 20% and payout = 100000 * 1.20 = 120000 exactly.
func TestCalculatePayoffCappedParticipation(t *testing.T) {
	payout, effectiveReturn, wasCapped := CalculatePayoff(testNote(), 100000, 20.0)
	if payout != 120000 {
		t.Fatalf("expected capped payout 120000, got %d", payout)
	}
	if effectiveReturn != 20.0 {
		t.Fatalf("expected effective return capped at 20%%, got %v", effectiveReturn)
	}
	if !wasCapped {
		t.Fatal("expected wasCapped=true when the participated return exceeds the cap")
	}
}

// TestCalculatePayoffExactlyAtCapBoundary: participatedReturn EXACTLY
// equal to the cap should NOT be flagged as capped (it's the boundary,
// not an overshoot) — participation rate 100% (not 150%) with index
// return exactly 20% gives participatedReturn = 20%, equal to CapPercent.
func TestCalculatePayoffExactlyAtCapBoundary(t *testing.T) {
	note := testNote()
	note.ParticipationRatePercent = 100
	payout, effectiveReturn, wasCapped := CalculatePayoff(note, 100000, 20.0)
	if payout != 120000 {
		t.Fatalf("expected payout 120000 at the exact cap boundary, got %d", payout)
	}
	if effectiveReturn != 20.0 {
		t.Fatalf("expected effective return exactly 20%%, got %v", effectiveReturn)
	}
	if wasCapped {
		t.Fatal("expected wasCapped=false exactly AT the boundary (not exceeding it)")
	}
}

func TestCalculatePayoffScalesLinearlyWithPrincipal(t *testing.T) {
	payoutSmall, _, _ := CalculatePayoff(testNote(), 50000, 10.0)
	payoutLarge, _, _ := CalculatePayoff(testNote(), 500000, 10.0)
	if payoutLarge != payoutSmall*10 {
		t.Fatalf("expected payout to scale linearly with principal, got %d vs %d*10=%d", payoutLarge, payoutSmall, payoutSmall*10)
	}
}

func TestCalculatePayoffZeroParticipationRateAlwaysReturnsProtectedPrincipal(t *testing.T) {
	note := testNote()
	note.ParticipationRatePercent = 0
	payout, effectiveReturn, wasCapped := CalculatePayoff(note, 100000, 50.0)
	if payout != 100000 {
		t.Fatalf("expected principal-only payout with 0%% participation, got %d", payout)
	}
	if effectiveReturn != 0 {
		t.Fatalf("expected 0%% effective return with 0%% participation, got %v", effectiveReturn)
	}
	if wasCapped {
		t.Fatal("expected wasCapped=false when participated return is 0, below any positive cap")
	}
}

func TestCalculatePayoffPartialProtectionBelowHundredPercent(t *testing.T) {
	note := testNote()
	note.PrincipalProtectionPercent = 90 // hypothetical partially-protected note
	payout, _, _ := CalculatePayoff(note, 100000, -20.0)
	if payout != 90000 {
		t.Fatalf("expected 90%% of principal (90000) returned, got %d", payout)
	}
}
