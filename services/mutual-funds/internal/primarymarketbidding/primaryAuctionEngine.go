// Package primarymarketbidding is a real primary-market bidding state
// machine for illustrative G-Sec/T-Bill auctions — FEATURES.md §5, "Fixed
// Income", item 1 ("Primary market bidding UI: G-Secs, T-Bills, SGBs (RBI
// auction calendar)").
//
// LOUD CAVEAT, same honesty pattern as internal/amcrouting: THIS IS NOT
// CONNECTED TO ANY REAL RBI AUCTION SYSTEM. There is no E-Kuber (RBI's
// real auction platform) integration anywhere in this repo. The "auction"
// on the other end of every bid is this package's own in-memory state,
// seeded from internal/fixedincome's entirely fictitious auction calendar.
//
// Allotment rule (documented loudly because there is more than one valid
// real-world convention): this package implements a MULTIPLE-PRICE
// ("French") yield-priority auction, not a uniform-price auction. Bids for
// one auction are sorted ascending by requested YieldPercent — the lowest
// yield is the most competitive bid (it asks the government for the least
// return) and is filled first — and allotted greedily against the
// auction's NotifiedAmountInMinorUnits AT EACH BID'S OWN REQUESTED YIELD
// (not a single clearing yield shared by every winner). If the notified
// amount runs out partway through a group of bids that all requested the
// exact same yield, that remaining capacity is split PRO-RATA among just
// those tied bids, by requested quantity, with the last (by BidId, for
// determinism) tied bid absorbing any rounding remainder so the group's
// allotments sum exactly to the remaining capacity. Every bid strictly
// above the cutoff yield is REJECTED — nothing rolls over to a later
// auction.
package primarymarketbidding

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"time"

	"mercurius/mutualFunds/internal/fixedincome"
)

type AuctionStatus string

const (
	AuctionStatusScheduled AuctionStatus = "SCHEDULED"
	AuctionStatusOpen      AuctionStatus = "OPEN"
	AuctionStatusClosed    AuctionStatus = "CLOSED"
)

// Auction is one illustrative primary-market auction window for a single
// bond, seeded from internal/fixedincome.SeedAuctionCalendar.
type Auction struct {
	AuctionId                  string
	BondId                     string
	ScheduledAuctionDate       time.Time
	NotifiedAmountInMinorUnits int64
	Status                     AuctionStatus
	ClosedAt                   time.Time
}

type BidStatus string

const (
	BidStatusSubmitted         BidStatus = "SUBMITTED"
	BidStatusAllotted          BidStatus = "ALLOTTED"
	BidStatusPartiallyAllotted BidStatus = "PARTIALLY_ALLOTTED"
	BidStatusRejected          BidStatus = "REJECTED"
)

// Bid is one competitive bid submitted into an OPEN auction.
// QuantityInMinorUnits is the face-value amount the bidder wants to buy;
// YieldPercent is the annualized yield the bidder is demanding (lower =
// more competitive — see the package doc comment's allotment rule).
type Bid struct {
	BidId                        string
	AuctionId                    string
	BidderAccountIdentifier      string
	QuantityInMinorUnits         int64
	YieldPercent                 float64
	SubmittedAt                  time.Time
	Status                       BidStatus
	AllottedQuantityInMinorUnits int64
}

var ErrUnknownAuction = fmt.Errorf("no such auction exists")
var ErrAuctionNotOpen = fmt.Errorf("auction is not open for bidding")
var ErrAuctionAlreadyClosed = fmt.Errorf("auction has already been closed")
var ErrInvalidQuantity = fmt.Errorf("bid quantity must be strictly positive")
var ErrInvalidYield = fmt.Errorf("bid yield must be strictly positive")

// Engine is safe for concurrent use. See the package doc comment for the
// loud "this is not a real RBI auction" caveat.
type Engine struct {
	catalog *fixedincome.BondCatalog

	mutexGuardingState sync.Mutex
	auctionsById       map[string]*Auction
	bidsByAuctionId    map[string][]*Bid
}

// NewPrimaryAuctionEngine builds an engine against catalog, seeding one
// SCHEDULED auction per internal/fixedincome.SeedAuctionCalendar entry.
func NewPrimaryAuctionEngine(catalog *fixedincome.BondCatalog) *Engine {
	engine := &Engine{
		catalog:         catalog,
		auctionsById:    make(map[string]*Auction),
		bidsByAuctionId: make(map[string][]*Bid),
	}
	for index, entry := range fixedincome.SeedAuctionCalendar() {
		auctionId := fmt.Sprintf("auction-%03d", index+1)
		engine.auctionsById[auctionId] = &Auction{
			AuctionId:                  auctionId,
			BondId:                     entry.BondId,
			ScheduledAuctionDate:       entry.ScheduledAuctionDate,
			NotifiedAmountInMinorUnits: entry.NotifiedAmountInMinorUnits,
			Status:                     AuctionStatusScheduled,
		}
	}
	return engine
}

// ListAuctions returns every auction, sorted by AuctionId for a
// deterministic response.
func (engine *Engine) ListAuctions() []*Auction {
	engine.mutexGuardingState.Lock()
	defer engine.mutexGuardingState.Unlock()

	auctions := make([]*Auction, 0, len(engine.auctionsById))
	for _, auction := range engine.auctionsById {
		auctions = append(auctions, auction)
	}
	sort.Slice(auctions, func(i, j int) bool { return auctions[i].AuctionId < auctions[j].AuctionId })
	return auctions
}

// OpenDueAuctions sweeps every SCHEDULED auction whose ScheduledAuctionDate
// has arrived by now and transitions it to OPEN — same "sweep, don't push"
// pattern as internal/amcrouting.ConfirmDueOrders and
// internal/sipscheduler.SweepDueSips.
func (engine *Engine) OpenDueAuctions(now time.Time) []*Auction {
	engine.mutexGuardingState.Lock()
	defer engine.mutexGuardingState.Unlock()

	opened := make([]*Auction, 0)
	for _, auction := range engine.auctionsById {
		if auction.Status == AuctionStatusScheduled && !now.Before(auction.ScheduledAuctionDate) {
			auction.Status = AuctionStatusOpen
			opened = append(opened, auction)
		}
	}
	sort.Slice(opened, func(i, j int) bool { return opened[i].AuctionId < opened[j].AuctionId })
	return opened
}

// SubmitBid submits a competitive bid into an OPEN auction.
func (engine *Engine) SubmitBid(auctionId string, bidderAccountIdentifier string, quantityInMinorUnits int64, yieldPercent float64, now time.Time) (*Bid, error) {
	if quantityInMinorUnits <= 0 {
		return nil, ErrInvalidQuantity
	}
	if yieldPercent <= 0 {
		return nil, ErrInvalidYield
	}

	engine.mutexGuardingState.Lock()
	defer engine.mutexGuardingState.Unlock()

	auction, wasFound := engine.auctionsById[auctionId]
	if !wasFound {
		return nil, ErrUnknownAuction
	}
	if auction.Status != AuctionStatusOpen {
		return nil, ErrAuctionNotOpen
	}

	bidId, genError := generateBidId()
	if genError != nil {
		return nil, fmt.Errorf("failed to generate bid id: %w", genError)
	}

	bid := &Bid{
		BidId:                   bidId,
		AuctionId:               auctionId,
		BidderAccountIdentifier: bidderAccountIdentifier,
		QuantityInMinorUnits:    quantityInMinorUnits,
		YieldPercent:            yieldPercent,
		SubmittedAt:             now,
		Status:                  BidStatusSubmitted,
	}
	engine.bidsByAuctionId[auctionId] = append(engine.bidsByAuctionId[auctionId], bid)
	return bid, nil
}

// CloseAuction closes auctionId and runs the multiple-price, yield-priority
// allotment described in the package doc comment. Returns every bid that
// was submitted into the auction, each mutated in place with its final
// Status and AllottedQuantityInMinorUnits.
func (engine *Engine) CloseAuction(auctionId string, now time.Time) ([]*Bid, error) {
	engine.mutexGuardingState.Lock()
	defer engine.mutexGuardingState.Unlock()

	auction, wasFound := engine.auctionsById[auctionId]
	if !wasFound {
		return nil, ErrUnknownAuction
	}
	if auction.Status == AuctionStatusClosed {
		return nil, ErrAuctionAlreadyClosed
	}

	bids := engine.bidsByAuctionId[auctionId]
	sortedBids := make([]*Bid, len(bids))
	copy(sortedBids, bids)
	sort.Slice(sortedBids, func(i, j int) bool {
		if sortedBids[i].YieldPercent != sortedBids[j].YieldPercent {
			return sortedBids[i].YieldPercent < sortedBids[j].YieldPercent
		}
		return sortedBids[i].BidId < sortedBids[j].BidId // deterministic tiebreak
	})

	remainingCapacity := auction.NotifiedAmountInMinorUnits

	index := 0
	for index < len(sortedBids) && remainingCapacity > 0 {
		currentYield := sortedBids[index].YieldPercent

		// Gather the run of bids tied at currentYield.
		groupStart := index
		for index < len(sortedBids) && sortedBids[index].YieldPercent == currentYield {
			index++
		}
		group := sortedBids[groupStart:index]

		groupTotalRequested := int64(0)
		for _, bid := range group {
			groupTotalRequested += bid.QuantityInMinorUnits
		}

		if groupTotalRequested <= remainingCapacity {
			// The whole group fits — allot every bid in it in full.
			for _, bid := range group {
				bid.AllottedQuantityInMinorUnits = bid.QuantityInMinorUnits
				bid.Status = BidStatusAllotted
			}
			remainingCapacity -= groupTotalRequested
			continue
		}

		// The group is oversubscribed at the cutoff: split remainingCapacity
		// pro-rata by requested quantity, last bid (by BidId, already
		// deterministically ordered within the group) absorbs the rounding
		// remainder so allotments sum exactly to remainingCapacity.
		allottedSoFar := int64(0)
		for i, bid := range group {
			var allotted int64
			if i == len(group)-1 {
				allotted = remainingCapacity - allottedSoFar
			} else {
				allotted = proRataAllotmentInMinorUnits(bid.QuantityInMinorUnits, remainingCapacity, groupTotalRequested)
			}
			bid.AllottedQuantityInMinorUnits = allotted
			allottedSoFar += allotted
			if allotted == 0 {
				bid.Status = BidStatusRejected
			} else if allotted < bid.QuantityInMinorUnits {
				bid.Status = BidStatusPartiallyAllotted
			} else {
				bid.Status = BidStatusAllotted
			}
		}
		remainingCapacity = 0
	}

	// Everything past the cutoff (not reached by the loop above) is
	// rejected outright.
	for ; index < len(sortedBids); index++ {
		sortedBids[index].AllottedQuantityInMinorUnits = 0
		sortedBids[index].Status = BidStatusRejected
	}

	auction.Status = AuctionStatusClosed
	auction.ClosedAt = now

	result := make([]*Bid, len(sortedBids))
	copy(result, sortedBids)
	sort.Slice(result, func(i, j int) bool { return result[i].BidId < result[j].BidId })
	return result, nil
}

// BidsForBidder returns every bid bidderAccountIdentifier has submitted
// across every auction, sorted by SubmittedAt.
func (engine *Engine) BidsForBidder(bidderAccountIdentifier string) []*Bid {
	engine.mutexGuardingState.Lock()
	defer engine.mutexGuardingState.Unlock()

	matching := make([]*Bid, 0)
	for _, bids := range engine.bidsByAuctionId {
		for _, bid := range bids {
			if bid.BidderAccountIdentifier == bidderAccountIdentifier {
				matching = append(matching, bid)
			}
		}
	}
	sort.Slice(matching, func(i, j int) bool { return matching[i].SubmittedAt.Before(matching[j].SubmittedAt) })
	return matching
}

// BidsForAuction returns every bid submitted into auctionId, sorted by
// BidId.
func (engine *Engine) BidsForAuction(auctionId string) []*Bid {
	engine.mutexGuardingState.Lock()
	defer engine.mutexGuardingState.Unlock()

	bids := engine.bidsByAuctionId[auctionId]
	result := make([]*Bid, len(bids))
	copy(result, bids)
	sort.Slice(result, func(i, j int) bool { return result[i].BidId < result[j].BidId })
	return result
}

// proRataAllotmentInMinorUnits computes
// bidQuantity * remainingCapacity / groupTotalRequested exactly, for a
// non-last tied bid in CloseAuction's oversubscribed-group pro-rata split
// (the group's last bid, by BidId, instead absorbs the rounding remainder
// directly in CloseAuction so the group sums exactly to remainingCapacity —
// see that function).
//
// This uses math/big rather than float64 deliberately: float64 has only a
// 52-bit mantissa, so float64(bidQuantity)/float64(groupTotalRequested)*
// float64(remainingCapacity) loses exact-integer precision once these
// values run into the trillions of paise a large G-Sec auction can
// realistically reach (values above ~2^53 ≈ 9.007e15 are no longer
// exactly representable), causing allotments to not sum exactly to
// remainingCapacity. Multiplying first with big.Int (instead of
// bidQuantity*remainingCapacity as plain int64, which can itself overflow
// int64 before the divide) keeps the intermediate product exact
// regardless of magnitude, and integer division (Quo, i.e. truncation
// toward zero) matches this function's original float64-then-int64-cast
// truncating behavior for every non-last bid in the group.
func proRataAllotmentInMinorUnits(bidQuantity, remainingCapacity, groupTotalRequested int64) int64 {
	numerator := new(big.Int).Mul(big.NewInt(bidQuantity), big.NewInt(remainingCapacity))
	denominator := big.NewInt(groupTotalRequested)
	quotient := new(big.Int).Quo(numerator, denominator)
	return quotient.Int64()
}

func generateBidId() (string, error) {
	randomBytes := make([]byte, 8)
	if _, readError := rand.Read(randomBytes); readError != nil {
		return "", readError
	}
	return "bid-" + hex.EncodeToString(randomBytes), nil
}
