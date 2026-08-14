package bondladderbuilder

import (
	"testing"
	"time"

	"mercurius/mutualFunds/internal/fixedincome"
)

func newTestBuilder() (*fixedincome.BondCatalog, *Builder) {
	catalog := fixedincome.NewBondCatalog()
	return catalog, NewBuilder(catalog)
}

func TestBuildLadderRejectsNonPositiveInvestmentAmount(t *testing.T) {
	_, builder := newTestBuilder()
	if _, err := builder.BuildLadder("acct-1", 0, 3, time.Now()); err != ErrInvalidInvestmentAmount {
		t.Fatalf("expected ErrInvalidInvestmentAmount, got %v", err)
	}
}

func TestBuildLadderRejectsNonPositiveRungs(t *testing.T) {
	_, builder := newTestBuilder()
	if _, err := builder.BuildLadder("acct-1", 100000, 0, time.Now()); err != ErrInvalidNumberOfRungs {
		t.Fatalf("expected ErrInvalidNumberOfRungs, got %v", err)
	}
}

func TestBuildLadderRejectsTooManyRungsForCatalog(t *testing.T) {
	_, builder := newTestBuilder()
	if _, err := builder.BuildLadder("acct-1", 100000, 100, time.Now()); err != ErrNotEnoughBondsForLadder {
		t.Fatalf("expected ErrNotEnoughBondsForLadder, got %v", err)
	}
}

func TestBuildLadderSplitsEvenlyAndSumsExactly(t *testing.T) {
	_, builder := newTestBuilder()
	ladder, err := builder.BuildLadder("acct-1", 100000, 3, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ladder.Rungs) != 3 {
		t.Fatalf("expected 3 rungs, got %d", len(ladder.Rungs))
	}

	total := int64(0)
	for _, rung := range ladder.Rungs {
		total += rung.AllocatedAmountInMinorUnits
	}
	if total != 100000 {
		t.Fatalf("expected rungs to sum exactly to 100000, got %d", total)
	}
	// 100000 / 3 = 33333.33 -> 33333, 33333, and the last rung absorbs the
	// remainder: 100000 - 33333 - 33333 = 33334.
	if ladder.Rungs[0].AllocatedAmountInMinorUnits != 33333 || ladder.Rungs[1].AllocatedAmountInMinorUnits != 33333 {
		t.Fatalf("expected first two rungs at 33333 each, got %d and %d", ladder.Rungs[0].AllocatedAmountInMinorUnits, ladder.Rungs[1].AllocatedAmountInMinorUnits)
	}
	if ladder.Rungs[2].AllocatedAmountInMinorUnits != 33334 {
		t.Fatalf("expected last rung to absorb the remainder (33334), got %d", ladder.Rungs[2].AllocatedAmountInMinorUnits)
	}
}

func TestBuildLadderStaggersMaturitiesFirstAndLastAreNearestAndFarthest(t *testing.T) {
	catalog, builder := newTestBuilder()
	ladder, err := builder.BuildLadder("acct-1", 300000, 3, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sortedBonds := catalog.ListAllSortedByMaturity()
	nearest := sortedBonds[0]
	farthest := sortedBonds[len(sortedBonds)-1]

	if ladder.Rungs[0].BondId != nearest.BondId {
		t.Fatalf("expected first rung to be the nearest-maturity bond %s, got %s", nearest.BondId, ladder.Rungs[0].BondId)
	}
	if ladder.Rungs[2].BondId != farthest.BondId {
		t.Fatalf("expected last rung to be the farthest-maturity bond %s, got %s", farthest.BondId, ladder.Rungs[2].BondId)
	}
	// Maturities must be strictly ascending across rungs (genuinely
	// staggered, not duplicated).
	for i := 1; i < len(ladder.Rungs); i++ {
		if !ladder.Rungs[i].MaturityDate.After(ladder.Rungs[i-1].MaturityDate) {
			t.Fatalf("expected strictly ascending maturities across rungs, rung %d (%s) not after rung %d (%s)", i, ladder.Rungs[i].MaturityDate, i-1, ladder.Rungs[i-1].MaturityDate)
		}
	}
}

func TestBuildLadderRungsCarryCreditRating(t *testing.T) {
	_, builder := newTestBuilder()
	ladder, _ := builder.BuildLadder("acct-1", 100000, 2, time.Now())
	for _, rung := range ladder.Rungs {
		if rung.CreditRating == "" {
			t.Fatalf("expected every rung to carry a credit rating, got empty for %s", rung.BondId)
		}
	}
}

func TestBuildLadderSingleRungPicksNearestMaturityBond(t *testing.T) {
	catalog, builder := newTestBuilder()
	ladder, err := builder.BuildLadder("acct-1", 50000, 1, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	nearest := catalog.ListAllSortedByMaturity()[0]
	if ladder.Rungs[0].BondId != nearest.BondId {
		t.Fatalf("expected single rung to be nearest-maturity bond %s, got %s", nearest.BondId, ladder.Rungs[0].BondId)
	}
	if ladder.Rungs[0].AllocatedAmountInMinorUnits != 50000 {
		t.Fatalf("expected the entire amount allocated to the single rung, got %d", ladder.Rungs[0].AllocatedAmountInMinorUnits)
	}
}

func TestBuildLadderRecordsHoldings(t *testing.T) {
	_, builder := newTestBuilder()
	ladder, _ := builder.BuildLadder("acct-1", 90000, 3, time.Now())

	holdings := builder.HoldingsForAccount("acct-1")
	total := int64(0)
	for _, amount := range holdings {
		total += amount
	}
	if total != 90000 {
		t.Fatalf("expected total holdings to equal invested amount 90000, got %d", total)
	}
	for _, rung := range ladder.Rungs {
		if holdings[rung.BondId] != rung.AllocatedAmountInMinorUnits {
			t.Fatalf("expected holding for %s to equal allocated amount %d, got %d", rung.BondId, rung.AllocatedAmountInMinorUnits, holdings[rung.BondId])
		}
	}
}

func TestBuildLadderTwiceAccumulatesHoldings(t *testing.T) {
	_, builder := newTestBuilder()
	builder.BuildLadder("acct-1", 30000, 1, time.Now())
	builder.BuildLadder("acct-1", 30000, 1, time.Now())

	holdings := builder.HoldingsForAccount("acct-1")
	total := int64(0)
	for _, amount := range holdings {
		total += amount
	}
	if total != 60000 {
		t.Fatalf("expected accumulated holdings of 60000 across two ladders, got %d", total)
	}
}

func TestLaddersForAccountReturnsOnlyThatAccountSortedByBuiltAt(t *testing.T) {
	_, builder := newTestBuilder()
	now := time.Now()
	builder.BuildLadder("acct-1", 10000, 1, now)
	builder.BuildLadder("acct-2", 10000, 1, now)
	builder.BuildLadder("acct-1", 10000, 1, now.Add(time.Hour))

	ladders := builder.LaddersForAccount("acct-1")
	if len(ladders) != 2 {
		t.Fatalf("expected 2 ladders for acct-1, got %d", len(ladders))
	}
	if ladders[0].BuiltAt.After(ladders[1].BuiltAt) {
		t.Fatal("expected ladders sorted by BuiltAt")
	}
}

func TestLaddersForAccountUnknownAccountReturnsEmpty(t *testing.T) {
	_, builder := newTestBuilder()
	ladders := builder.LaddersForAccount("no-such-account")
	if len(ladders) != 0 {
		t.Fatalf("expected empty slice for unknown account, got %d", len(ladders))
	}
}
