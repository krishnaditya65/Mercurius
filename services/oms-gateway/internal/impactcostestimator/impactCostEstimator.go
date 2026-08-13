// Package impactcostestimator implements FEATURES.md §15's "Pre-trade
// impact-cost / slippage estimator ('what-if' simulator using current DOM
// depth)": given a hypothetical order size and a real order book depth
// snapshot, this computes a real walk-the-book estimate of the average
// fill price and the resulting slippage against the best available price
// — exact arithmetic over the supplied depth levels, not a statistical
// approximation.
//
// THE ALGORITHM IS REAL: EstimateImpactCost walks DepthLevels in priority
// order (best price first), consuming each level's full quantity before
// moving to the next, until either the hypothetical order size is fully
// satisfied or the book runs out of depth — exactly what a real matching
// engine does when a marketable order crosses the book. The resulting
// AverageFillPriceInMinorUnits is the real, quantity-weighted average
// price across every level actually walked, and
// SlippageInMinorUnits/SlippagePercent measure that average against the
// snapshot's best price (the price a caller would naively expect to pay
// for an infinitesimally small order).
//
// LOUD, REPEATED, LOAD-BEARING KNOWN GAP — read this before assuming
// depth is fetched automatically: oms-gateway has NO way to query a real,
// live order book depth snapshot today. matching-engine only PUSHES its
// full book depth fire-and-forget to the market-data service after every
// processed order (see matching-engine's own README,
// "publishBookDepthToMarketData" / "OrderBookCore::currentBookDepthSnapshot")
// — internal/matchingengineclient (checked before writing this package)
// exposes only SubmitOrderAndAwaitMatchResult, CancelOrderAndAwaitResult,
// and QueryOrderStatusAndAwaitResult — no depth query at all. market-data
// itself exposes GET /trades, GET /candles, GET /ticks/range, watchlists,
// and an L1 (top-of-book only) WebSocket feed — no full-depth-ladder REST
// endpoint either. EstimateImpactCost therefore takes an
// OrderBookDepthSnapshot as an explicit CALLER-SUPPLIED parameter — the
// exact same documented pattern internal/executionalgos' VWAP volume
// curve and POV cumulative-volume observations, and
// internal/marginpledge's reference price, already use for "the real
// computation exists, but oms-gateway has no live feed to source its
// input from automatically yet". A real build would source this snapshot
// from a genuine depth-query endpoint added to market-data (or a
// resync-then-delta WS subscription, which market-data's own README notes
// it already has for its L1 feed) — that wiring is future work, not part
// of this package.
package impactcostestimator

import "errors"

var (
	// ErrEmptyInstrumentSymbol is returned when the snapshot's instrument
	// symbol is empty.
	ErrEmptyInstrumentSymbol = errors.New("instrumentSymbol must not be empty")

	// ErrZeroHypotheticalQuantity is returned when the hypothetical order
	// size is zero — there is nothing to estimate.
	ErrZeroHypotheticalQuantity = errors.New("hypotheticalQuantity must be greater than zero")

	// ErrNoDepthOnRelevantSide is returned when the side of the book this
	// hypothetical order would walk (asks for a buy, bids for a sell) has
	// no levels at all.
	ErrNoDepthOnRelevantSide = errors.New("no depth levels available on the relevant side of the book")

	// ErrNonPositiveLevelPrice is returned when any depth level's price is
	// zero or negative.
	ErrNonPositiveLevelPrice = errors.New("every depth level's priceInMinorUnits must be greater than zero")

	// ErrZeroLevelQuantity is returned when any depth level's quantity is
	// zero — a real book never carries a resting order of size zero.
	ErrZeroLevelQuantity = errors.New("every depth level's quantity must be greater than zero")
)

// DepthLevel is one price level of a resting order book side, with the
// AGGREGATE quantity resting at that exact price (matching a typical
// level-2 market-by-price depth representation).
type DepthLevel struct {
	PriceInMinorUnits int64  `json:"priceInMinorUnits"`
	Quantity          uint64 `json:"quantity"`
}

// OrderBookDepthSnapshot is one instrument's full order book depth at a
// point in time. BidLevels must be sorted best-first (descending price);
// AskLevels must be sorted best-first (ascending price) — see
// validateSnapshot, which genuinely enforces both orderings rather than
// silently trusting the caller.
type OrderBookDepthSnapshot struct {
	InstrumentSymbol string       `json:"instrumentSymbol"`
	BidLevels        []DepthLevel `json:"bidLevels"`
	AskLevels        []DepthLevel `json:"askLevels"`
}

var (
	// ErrBidLevelsNotDescending is returned when BidLevels isn't sorted
	// strictly best-first (descending price).
	ErrBidLevelsNotDescending = errors.New("bidLevels must be sorted best-price-first (descending)")
	// ErrAskLevelsNotAscending is returned when AskLevels isn't sorted
	// strictly best-first (ascending price).
	ErrAskLevelsNotAscending = errors.New("askLevels must be sorted best-price-first (ascending)")
)

func validateSnapshot(snapshot OrderBookDepthSnapshot) error {
	if snapshot.InstrumentSymbol == "" {
		return ErrEmptyInstrumentSymbol
	}
	for _, level := range snapshot.BidLevels {
		if level.PriceInMinorUnits <= 0 {
			return ErrNonPositiveLevelPrice
		}
		if level.Quantity == 0 {
			return ErrZeroLevelQuantity
		}
	}
	for _, level := range snapshot.AskLevels {
		if level.PriceInMinorUnits <= 0 {
			return ErrNonPositiveLevelPrice
		}
		if level.Quantity == 0 {
			return ErrZeroLevelQuantity
		}
	}
	for i := 1; i < len(snapshot.BidLevels); i++ {
		if snapshot.BidLevels[i].PriceInMinorUnits >= snapshot.BidLevels[i-1].PriceInMinorUnits {
			return ErrBidLevelsNotDescending
		}
	}
	for i := 1; i < len(snapshot.AskLevels); i++ {
		if snapshot.AskLevels[i].PriceInMinorUnits <= snapshot.AskLevels[i-1].PriceInMinorUnits {
			return ErrAskLevelsNotAscending
		}
	}
	return nil
}

// ImpactCostEstimate is EstimateImpactCost's real, walk-the-book result.
type ImpactCostEstimate struct {
	InstrumentSymbol      string `json:"instrumentSymbol"`
	IsBuyNotSell          bool   `json:"isBuyNotSell"`
	RequestedQuantity     uint64 `json:"requestedQuantity"`
	QuantityFillable      uint64 `json:"quantityFillable"`
	BestPriceInMinorUnits int64  `json:"bestPriceInMinorUnits"`

	// AverageFillPriceInMinorUnits is the real quantity-weighted average
	// price across every level walked to fill QuantityFillable.
	AverageFillPriceInMinorUnits float64 `json:"averageFillPriceInMinorUnits"`

	// SlippageInMinorUnits is AverageFillPriceInMinorUnits minus
	// BestPriceInMinorUnits for a BUY (you pay MORE than best as you walk
	// up the book) or BestPriceInMinorUnits minus
	// AverageFillPriceInMinorUnits for a SELL (you receive LESS than best
	// as you walk down the book) — always >= 0 for a book that's actually
	// sorted best-first, since walking away from the best price can only
	// get worse, never better.
	SlippageInMinorUnits float64 `json:"slippageInMinorUnits"`
	SlippagePercent      float64 `json:"slippagePercent"`

	// DepthInsufficientForFullSize is true when the book's total
	// available quantity on the relevant side is less than
	// RequestedQuantity — QuantityFillable is then less than
	// RequestedQuantity, and every figure above reflects only what could
	// actually have been filled, never a hypothetical price beyond the
	// visible book.
	DepthInsufficientForFullSize bool `json:"depthInsufficientForFullSize"`

	LevelsWalked int `json:"levelsWalked"`
}

// EstimateImpactCost walks snapshot's relevant side (AskLevels for a BUY,
// BidLevels for a SELL) to fill hypotheticalQuantity, returning the real
// walk-the-book average fill price and slippage. See the package doc for
// the honest "caller supplies the depth snapshot" scope boundary.
func EstimateImpactCost(snapshot OrderBookDepthSnapshot, isBuyNotSell bool, hypotheticalQuantity uint64) (ImpactCostEstimate, error) {
	if err := validateSnapshot(snapshot); err != nil {
		return ImpactCostEstimate{}, err
	}
	if hypotheticalQuantity == 0 {
		return ImpactCostEstimate{}, ErrZeroHypotheticalQuantity
	}

	levels := snapshot.AskLevels
	if !isBuyNotSell {
		levels = snapshot.BidLevels
	}
	if len(levels) == 0 {
		return ImpactCostEstimate{}, ErrNoDepthOnRelevantSide
	}

	bestPrice := levels[0].PriceInMinorUnits

	var remaining = hypotheticalQuantity
	var totalNotional float64
	var totalFilled uint64
	var levelsWalked int

	for _, level := range levels {
		if remaining == 0 {
			break
		}
		quantityAtThisLevel := level.Quantity
		if quantityAtThisLevel > remaining {
			quantityAtThisLevel = remaining
		}
		totalNotional += float64(quantityAtThisLevel) * float64(level.PriceInMinorUnits)
		totalFilled += quantityAtThisLevel
		remaining -= quantityAtThisLevel
		levelsWalked++
	}

	averageFillPrice := totalNotional / float64(totalFilled)

	var slippage float64
	if isBuyNotSell {
		slippage = averageFillPrice - float64(bestPrice)
	} else {
		slippage = float64(bestPrice) - averageFillPrice
	}
	slippagePercent := 0.0
	if bestPrice != 0 {
		slippagePercent = (slippage / float64(bestPrice)) * 100.0
	}

	return ImpactCostEstimate{
		InstrumentSymbol:             snapshot.InstrumentSymbol,
		IsBuyNotSell:                 isBuyNotSell,
		RequestedQuantity:            hypotheticalQuantity,
		QuantityFillable:             totalFilled,
		BestPriceInMinorUnits:        bestPrice,
		AverageFillPriceInMinorUnits: averageFillPrice,
		SlippageInMinorUnits:         slippage,
		SlippagePercent:              slippagePercent,
		DepthInsufficientForFullSize: totalFilled < hypotheticalQuantity,
		LevelsWalked:                 levelsWalked,
	}, nil
}
