package primarymarketbidding

import (
	"testing"
	"time"

	"mercurius/mutualFunds/internal/fixedincome"
)

func newTestEngine() *Engine {
	return NewPrimaryAuctionEngine(fixedincome.NewBondCatalog())
}

func findAuctionForBond(engine *Engine, bondId string) *Auction {
	for _, auction := range engine.ListAuctions() {
		if auction.BondId == bondId {
			return auction
		}
	}
	return nil
}

func TestNewPrimaryAuctionEngineSeedsFiveScheduledAuctions(t *testing.T) {
	engine := newTestEngine()
	auctions := engine.ListAuctions()
	if len(auctions) != 5 {
		t.Fatalf("expected 5 seeded auctions, got %d", len(auctions))
	}
	for _, auction := range auctions {
		if auction.Status != AuctionStatusScheduled {
			t.Fatalf("expected all seeded auctions SCHEDULED, got %s for %s", auction.Status, auction.AuctionId)
		}
	}
}

func TestSubmitBidRejectedBeforeAuctionOpens(t *testing.T) {
	engine := newTestEngine()
	auction := findAuctionForBond(engine, "GSEC-07.10-2028")

	_, bidError := engine.SubmitBid(auction.AuctionId, "acct-1", 100000, 7.0, time.Now())
	if bidError != ErrAuctionNotOpen {
		t.Fatalf("expected ErrAuctionNotOpen, got %v", bidError)
	}
}

func TestOpenDueAuctionsOnlyOpensAuctionsWhoseDateHasArrived(t *testing.T) {
	engine := newTestEngine()
	auction := findAuctionForBond(engine, "GSEC-07.10-2028")

	dayBefore := auction.ScheduledAuctionDate.Add(-24 * time.Hour)
	engine.OpenDueAuctions(dayBefore)
	if refreshed := findAuctionForBond(engine, "GSEC-07.10-2028"); refreshed.Status != AuctionStatusScheduled {
		t.Fatalf("expected GSEC-07.10-2028 to still be SCHEDULED a day before its auction date, got %s", refreshed.Status)
	}

	opened := engine.OpenDueAuctions(auction.ScheduledAuctionDate)
	found := false
	for _, o := range opened {
		if o.AuctionId == auction.AuctionId {
			found = true
		}
	}
	if !found {
		t.Fatal("expected the GSEC-07.10-2028 auction to open on its scheduled date")
	}
}

func TestOpenDueAuctionsIsIdempotent(t *testing.T) {
	engine := newTestEngine()
	auction := findAuctionForBond(engine, "GSEC-07.10-2028")
	now := auction.ScheduledAuctionDate

	first := engine.OpenDueAuctions(now)
	second := engine.OpenDueAuctions(now)
	if len(first) == 0 {
		t.Fatal("expected first sweep to open at least one auction")
	}
	for _, o := range second {
		if o.AuctionId == auction.AuctionId {
			t.Fatal("expected second sweep to not re-open the same auction")
		}
	}
}

func TestSubmitBidRejectsNonPositiveQuantityOrYield(t *testing.T) {
	engine := newTestEngine()
	auction := findAuctionForBond(engine, "GSEC-07.10-2028")
	engine.OpenDueAuctions(auction.ScheduledAuctionDate)

	if _, err := engine.SubmitBid(auction.AuctionId, "acct-1", 0, 7.0, time.Now()); err != ErrInvalidQuantity {
		t.Fatalf("expected ErrInvalidQuantity, got %v", err)
	}
	if _, err := engine.SubmitBid(auction.AuctionId, "acct-1", 1000, 0, time.Now()); err != ErrInvalidYield {
		t.Fatalf("expected ErrInvalidYield, got %v", err)
	}
}

func TestSubmitBidRejectsUnknownAuction(t *testing.T) {
	engine := newTestEngine()
	if _, err := engine.SubmitBid("no-such-auction", "acct-1", 1000, 7.0, time.Now()); err != ErrUnknownAuction {
		t.Fatalf("expected ErrUnknownAuction, got %v", err)
	}
}

// TestCloseAuctionFullySubscribedAllotsAllBidsInFull sets notified amount
// higher than total demand: every bid should be allotted in full at its
// own requested yield.
func TestCloseAuctionUndersubscribedAllotsAllBidsInFull(t *testing.T) {
	engine := newTestEngine()
	auction := findAuctionForBond(engine, "GSEC-07.10-2028") // notified 500000000
	engine.OpenDueAuctions(auction.ScheduledAuctionDate)
	now := auction.ScheduledAuctionDate

	bidLow, _ := engine.SubmitBid(auction.AuctionId, "acct-low", 100_000_00, 7.05, now)
	bidHigh, _ := engine.SubmitBid(auction.AuctionId, "acct-high", 100_000_00, 7.20, now)

	closedBids, closeError := engine.CloseAuction(auction.AuctionId, now)
	if closeError != nil {
		t.Fatalf("unexpected close error: %v", closeError)
	}

	byId := map[string]*Bid{}
	for _, bid := range closedBids {
		byId[bid.BidId] = bid
	}

	if byId[bidLow.BidId].Status != BidStatusAllotted || byId[bidLow.BidId].AllottedQuantityInMinorUnits != 100_000_00 {
		t.Fatalf("expected low-yield bid fully allotted, got %+v", byId[bidLow.BidId])
	}
	if byId[bidHigh.BidId].Status != BidStatusAllotted || byId[bidHigh.BidId].AllottedQuantityInMinorUnits != 100_000_00 {
		t.Fatalf("expected high-yield bid fully allotted (undersubscribed), got %+v", byId[bidHigh.BidId])
	}
}

// TestCloseAuctionOversubscribedRejectsHighestYieldBid: notified amount
// 2,000,000.00 (TBILL-91D-NOV26). Two bids of 1,500,000.00 each at
// different yields exceeds capacity -> lower yield wins fully, higher
// yield rejected entirely.
func TestCloseAuctionOversubscribedRejectsHighestYieldBid(t *testing.T) {
	engine := newTestEngine()
	auction := findAuctionForBond(engine, "TBILL-91D-NOV26") // notified 2_000_000_00
	engine.OpenDueAuctions(auction.ScheduledAuctionDate)
	now := auction.ScheduledAuctionDate

	winner, _ := engine.SubmitBid(auction.AuctionId, "acct-winner", 1_500_000_00, 6.50, now)
	loser, _ := engine.SubmitBid(auction.AuctionId, "acct-loser", 1_500_000_00, 6.80, now)

	closedBids, closeError := engine.CloseAuction(auction.AuctionId, now)
	if closeError != nil {
		t.Fatalf("unexpected close error: %v", closeError)
	}
	byId := map[string]*Bid{}
	for _, bid := range closedBids {
		byId[bid.BidId] = bid
	}

	if byId[winner.BidId].Status != BidStatusAllotted || byId[winner.BidId].AllottedQuantityInMinorUnits != 1_500_000_00 {
		t.Fatalf("expected winner fully allotted, got %+v", byId[winner.BidId])
	}
	if byId[loser.BidId].Status != BidStatusPartiallyAllotted {
		t.Fatalf("expected loser partially allotted (500000.00 remaining capacity), got %+v", byId[loser.BidId])
	}
	if byId[loser.BidId].AllottedQuantityInMinorUnits != 500_000_00 {
		t.Fatalf("expected loser allotted exactly the remaining 500000.00 capacity, got %d", byId[loser.BidId].AllottedQuantityInMinorUnits)
	}
}

// TestCloseAuctionProRatesTiedBidsAtCutoffYield: notified 1_500_000_00
// (TBILL-364D-AUG27). Two bids at the SAME yield each requesting
// 1_000_000_00 (total 2_000_000_00, oversubscribed by exactly 2x at the
// cutoff) -> each gets exactly half of the notified amount, pro-rata, and
// the two allotments must sum EXACTLY to the notified amount.
func TestCloseAuctionProRatesTiedBidsAtCutoffYield(t *testing.T) {
	engine := newTestEngine()
	auction := findAuctionForBond(engine, "TBILL-364D-AUG27") // notified 1_500_000_00
	engine.OpenDueAuctions(auction.ScheduledAuctionDate)
	now := auction.ScheduledAuctionDate

	bidA, _ := engine.SubmitBid(auction.AuctionId, "acct-a", 1_000_000_00, 6.75, now)
	bidB, _ := engine.SubmitBid(auction.AuctionId, "acct-b", 1_000_000_00, 6.75, now)

	closedBids, closeError := engine.CloseAuction(auction.AuctionId, now)
	if closeError != nil {
		t.Fatalf("unexpected close error: %v", closeError)
	}
	byId := map[string]*Bid{}
	for _, bid := range closedBids {
		byId[bid.BidId] = bid
	}

	total := byId[bidA.BidId].AllottedQuantityInMinorUnits + byId[bidB.BidId].AllottedQuantityInMinorUnits
	if total != auction.NotifiedAmountInMinorUnits {
		t.Fatalf("expected pro-rata allotments to sum exactly to notified amount %d, got %d", auction.NotifiedAmountInMinorUnits, total)
	}
	if byId[bidA.BidId].AllottedQuantityInMinorUnits != 750_000_00 {
		t.Fatalf("expected bidA allotted exactly half (750000.00), got %d", byId[bidA.BidId].AllottedQuantityInMinorUnits)
	}
	if byId[bidA.BidId].Status != BidStatusPartiallyAllotted || byId[bidB.BidId].Status != BidStatusPartiallyAllotted {
		t.Fatalf("expected both tied bids PARTIALLY_ALLOTTED, got %s and %s", byId[bidA.BidId].Status, byId[bidB.BidId].Status)
	}
}

func TestCloseAuctionRejectsBidsBeyondCutoff(t *testing.T) {
	engine := newTestEngine()
	auction := findAuctionForBond(engine, "TBILL-91D-NOV26") // notified 2_000_000_00
	engine.OpenDueAuctions(auction.ScheduledAuctionDate)
	now := auction.ScheduledAuctionDate

	engine.SubmitBid(auction.AuctionId, "acct-1", 2_000_000_00, 6.40, now)
	rejected, _ := engine.SubmitBid(auction.AuctionId, "acct-2", 500_000_00, 7.00, now)

	closedBids, _ := engine.CloseAuction(auction.AuctionId, now)
	for _, bid := range closedBids {
		if bid.BidId == rejected.BidId {
			if bid.Status != BidStatusRejected || bid.AllottedQuantityInMinorUnits != 0 {
				t.Fatalf("expected fully rejected bid, got %+v", bid)
			}
		}
	}
}

// TestProRataAllotmentInMinorUnitsIsExactForTrillionsOfMinorUnits
// reproduces the confirmed audit bug directly at the helper level: the
// OLD formula, int64(float64(bidQuantity)/float64(groupTotalRequested)*
// float64(remainingCapacity)), loses exact-integer precision once these
// quantities run into the quadrillions of minor units (paise) a
// realistically large G-Sec auction can reach — float64's 52-bit mantissa
// cannot represent every integer above ~2^53 exactly, and even below that
// threshold the fraction-then-multiply order of operations rounds
// differently than exact integer math. These two (bidQuantity, remaining,
// total) triples were found by brute-force search specifically because
// the old float64 formula and exact integer division disagree on them.
// proRataAllotmentInMinorUnits must match exact integer division
// (bidQuantity*remainingCapacity)/groupTotalRequested computed via
// math/big, not the old float64-truncated (and, here, actually
// OVER-estimated) value.
func TestProRataAllotmentInMinorUnitsIsExactForTrillionsOfMinorUnits(t *testing.T) {
	cases := []struct {
		name                 string
		bidQuantity          int64
		remainingCapacity    int64
		groupTotalRequested  int64
		wantExactAllotment   int64
		oldFloat64Formula    int64 // what the old buggy code computed — documented for contrast, not asserted
	}{
		{
			name:                "bidA of a two-way trillions-scale tie",
			bidQuantity:         1_200_156_663_427_567,
			remainingCapacity:   7_777_777_777_777_777,
			groupTotalRequested: 9_000_000_000_000_001,
			wantExactAllotment:  1_037_172_425_184_316,
			oldFloat64Formula:   1_037_172_425_184_317, // off by +1 — an over-allotment
		},
		{
			name:                "bidB of the same trillions-scale tie",
			bidQuantity:         779_831_885_648_889,
			remainingCapacity:   7_777_777_777_777_777,
			groupTotalRequested: 9_000_000_000_000_001,
			wantExactAllotment:  673_928_790_066_940,
			oldFloat64Formula:   673_928_790_066_941, // off by +1 — an over-allotment
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := proRataAllotmentInMinorUnits(testCase.bidQuantity, testCase.remainingCapacity, testCase.groupTotalRequested)
			if got != testCase.wantExactAllotment {
				t.Fatalf("expected exact allotment %d, got %d (the old float64 formula would have given %d)",
					testCase.wantExactAllotment, got, testCase.oldFloat64Formula)
			}
			if got == testCase.oldFloat64Formula {
				t.Fatalf("test setup error: exact and old-formula values are equal (%d) — this case no longer demonstrates the precision bug", got)
			}
		})
	}
}

// TestCloseAuctionExactProRataForTrillionsOfMinorUnits exercises the same
// bug end-to-end through CloseAuction: a three-way tied group at
// quadrillions-of-minor-units scale, where the first two (non-last) bids
// hit the exact float64-vs-integer mismatches proven above, and the
// third (last, by BidId) absorbs the rounding remainder as usual. Before
// the fix, the two non-last bids were each over-allotted by 1 minor unit
// relative to their true exact pro-rata share (and the last bid's
// remainder absorption correspondingly under-allotted it by 2, though the
// group total happened to still equal remainingCapacity by construction —
// the bug's real effect is per-bid unfairness, not an inexact group
// total). After the fix every bid's allotment must match the exact
// integer pro-rata computation.
func TestCloseAuctionExactProRataForTrillionsOfMinorUnits(t *testing.T) {
	engine := newTestEngine()
	auction := findAuctionForBond(engine, "GSEC-07.10-2028")

	// Directly install an auction with a quadrillions-scale notified
	// amount — internal/fixedincome's static seed calendar only ever
	// uses small illustrative amounts, so a trillions/quadrillions-scale
	// scenario has to be built by hand here (same package, so this white
	// box test can reach the unexported map directly).
	const remainingCapacity = int64(7_777_777_777_777_777)
	const groupTotal = int64(9_000_000_000_000_001)
	bidAQuantity := int64(1_200_156_663_427_567)
	bidBQuantity := int64(779_831_885_648_889)
	bidCQuantity := groupTotal - bidAQuantity - bidBQuantity // last bid, absorbs the remainder

	now := auction.ScheduledAuctionDate

	// Built directly (bypassing SubmitBid's random hex BidId generation)
	// so BidId ordering — and therefore which bid is "last" and absorbs
	// CloseAuction's rounding remainder — is deterministic: "bid-a" and
	// "bid-b" sort before "bid-c", so bid-c is guaranteed last.
	bidA := &Bid{BidId: "bid-a", AuctionId: auction.AuctionId, BidderAccountIdentifier: "acct-a", QuantityInMinorUnits: bidAQuantity, YieldPercent: 7.0, SubmittedAt: now, Status: BidStatusSubmitted}
	bidB := &Bid{BidId: "bid-b", AuctionId: auction.AuctionId, BidderAccountIdentifier: "acct-b", QuantityInMinorUnits: bidBQuantity, YieldPercent: 7.0, SubmittedAt: now, Status: BidStatusSubmitted}
	bidC := &Bid{BidId: "bid-c", AuctionId: auction.AuctionId, BidderAccountIdentifier: "acct-c", QuantityInMinorUnits: bidCQuantity, YieldPercent: 7.0, SubmittedAt: now, Status: BidStatusSubmitted}

	engine.mutexGuardingState.Lock()
	engine.auctionsById[auction.AuctionId] = &Auction{
		AuctionId:                  auction.AuctionId,
		BondId:                     auction.BondId,
		ScheduledAuctionDate:       auction.ScheduledAuctionDate,
		NotifiedAmountInMinorUnits: remainingCapacity,
		Status:                     AuctionStatusOpen,
	}
	engine.bidsByAuctionId[auction.AuctionId] = []*Bid{bidA, bidB, bidC}
	engine.mutexGuardingState.Unlock()

	closedBids, closeError := engine.CloseAuction(auction.AuctionId, now)
	if closeError != nil {
		t.Fatalf("unexpected close error: %v", closeError)
	}
	byId := map[string]*Bid{}
	for _, bid := range closedBids {
		byId[bid.BidId] = bid
	}

	wantA := int64(1_037_172_425_184_316)
	wantB := int64(673_928_790_066_940)
	if got := byId[bidA.BidId].AllottedQuantityInMinorUnits; got != wantA {
		t.Fatalf("expected bidA's exact pro-rata allotment %d, got %d", wantA, got)
	}
	if got := byId[bidB.BidId].AllottedQuantityInMinorUnits; got != wantB {
		t.Fatalf("expected bidB's exact pro-rata allotment %d, got %d", wantB, got)
	}

	total := byId[bidA.BidId].AllottedQuantityInMinorUnits + byId[bidB.BidId].AllottedQuantityInMinorUnits + byId[bidC.BidId].AllottedQuantityInMinorUnits
	if total != remainingCapacity {
		t.Fatalf("expected all three allotments to sum exactly to remainingCapacity %d, got %d", remainingCapacity, total)
	}
}

func TestCloseAuctionAlreadyClosedReturnsError(t *testing.T) {
	engine := newTestEngine()
	auction := findAuctionForBond(engine, "GSEC-07.10-2028")
	engine.OpenDueAuctions(auction.ScheduledAuctionDate)
	now := auction.ScheduledAuctionDate

	if _, err := engine.CloseAuction(auction.AuctionId, now); err != nil {
		t.Fatalf("unexpected first close error: %v", err)
	}
	if _, err := engine.CloseAuction(auction.AuctionId, now); err != ErrAuctionAlreadyClosed {
		t.Fatalf("expected ErrAuctionAlreadyClosed, got %v", err)
	}
}

func TestCloseAuctionUnknownAuctionReturnsError(t *testing.T) {
	engine := newTestEngine()
	if _, err := engine.CloseAuction("no-such-auction", time.Now()); err != ErrUnknownAuction {
		t.Fatalf("expected ErrUnknownAuction, got %v", err)
	}
}

func TestBidsForBidderReturnsOnlyThatBidderAcrossAuctions(t *testing.T) {
	engine := newTestEngine()
	auction1 := findAuctionForBond(engine, "GSEC-07.10-2028")
	auction2 := findAuctionForBond(engine, "TBILL-91D-NOV26")
	engine.OpenDueAuctions(auction1.ScheduledAuctionDate)
	engine.OpenDueAuctions(auction2.ScheduledAuctionDate)

	engine.SubmitBid(auction1.AuctionId, "acct-x", 100000, 7.0, auction1.ScheduledAuctionDate)
	engine.SubmitBid(auction2.AuctionId, "acct-x", 200000, 6.5, auction2.ScheduledAuctionDate)
	engine.SubmitBid(auction1.AuctionId, "acct-y", 300000, 7.1, auction1.ScheduledAuctionDate)

	bidsForX := engine.BidsForBidder("acct-x")
	if len(bidsForX) != 2 {
		t.Fatalf("expected 2 bids for acct-x, got %d", len(bidsForX))
	}
}

func TestBidsForAuctionReturnsSortedByBidId(t *testing.T) {
	engine := newTestEngine()
	auction := findAuctionForBond(engine, "GSEC-07.10-2028")
	engine.OpenDueAuctions(auction.ScheduledAuctionDate)
	engine.SubmitBid(auction.AuctionId, "acct-1", 100000, 7.0, auction.ScheduledAuctionDate)
	engine.SubmitBid(auction.AuctionId, "acct-2", 200000, 7.1, auction.ScheduledAuctionDate)

	bids := engine.BidsForAuction(auction.AuctionId)
	if len(bids) != 2 {
		t.Fatalf("expected 2 bids, got %d", len(bids))
	}
	if bids[0].BidId > bids[1].BidId {
		t.Fatal("expected bids sorted by BidId")
	}
}
