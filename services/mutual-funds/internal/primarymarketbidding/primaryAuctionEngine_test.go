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
