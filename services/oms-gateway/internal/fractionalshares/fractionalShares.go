// Package fractionalshares implements FEATURES.md §17's "Fractional
// share investing" using a documented MILLI-SHARE INTEGER precision
// scheme — 1000 milli-share units = 1.000 whole share — deliberately NOT
// a float, per this repository's project-wide "no float-correctness
// bugs in a financial system" discipline (see e.g.
// internal/chargescalculator and internal/marginfunding, which use
// integer paise and simple-interest integer rounding respectively for
// the same reason).
//
// DESIGN CHOICE (per a previous round's deferred-item note): ADDITIVE,
// mirroring the OrderExecutionType precedent — a NEW, optional
// `MilliShareQuantity *uint64` field on orders.OrderSubmissionRequest,
// layered ALONGSIDE the pre-existing `OrderQuantity uint64` (whole-share)
// field rather than changing what OrderQuantity means. A client that
// never sets MilliShareQuantity is completely unaffected — every
// pre-existing test and consumer of OrderQuantity keeps working exactly
// as before.
//
// HONEST, LOUD, LOAD-BEARING SCOPE BOUNDARY — read this before assuming
// more than what's here: matching-engine's wire protocol
// (internal/matchingengineclient) carries only a whole-share `uint64`
// OrderQuantity — it has NO field for milli-share precision, and
// extending it is out of oms-gateway's boundary (a different service,
// written in Rust). This means a REAL fractional order can only be
// genuinely FILLED at milli-share precision through the ONE execution
// path in this build that never touches matching-engine at all: PAPER
// TRADING (internal/papertrading, FEATURES.md §7) — see
// ValidateMilliShareQuantity's doc comment, which explicitly REJECTS a
// non-paper (live) order that sets MilliShareQuantity, rather than
// silently rounding/truncating it into a whole-share live order (which
// would be a real financial-correctness bug: the client asked for 0.3
// shares and got charged for 1). What IS real and covered end-to-end in
// this build: milli-share quantity validation, milli-share-aware
// notional computation for the pre-trade risk check, and a real,
// dedicated milli-share position book (MilliSharePositionBook, mirroring
// internal/positions.PositionBook's own signed-net-quantity design) fed
// by paper-trading fills. NOT covered (explicitly out of this round's
// time budget, and loudly documented rather than silently gapped):
// internal/chargescalculator and internal/marginengine's SPAN/exposure
// calculators still operate on whole-share OrderQuantity only — a
// fractional paper order's charges/margin estimate would need those
// packages extended too, which this round does not do.
package fractionalshares

import "errors"

// MilliShareUnitsPerWholeShare is the fixed precision scale: 1000
// milli-share units represents exactly 1.000 whole share. Three decimal
// places of share-quantity precision, matching the common real-world
// fractional-share granularity major brokers actually offer.
const MilliShareUnitsPerWholeShare = 1000

var (
	// ErrMilliShareQuantityMustBePositive is returned when
	// MilliShareQuantity is present but zero.
	ErrMilliShareQuantityMustBePositive = errors.New("milliShareQuantity must be greater than zero")

	// ErrFractionalSharesOnlySupportedForPaperTrading is returned when a
	// non-paper (live) order sets MilliShareQuantity — see this
	// package's doc comment for exactly why: matching-engine has no
	// milli-share-precision field in its wire protocol, so a live
	// fractional order cannot be genuinely filled at fractional
	// precision in this build. Rejecting loudly is the financially safe
	// choice; silently rounding to whole shares is not.
	ErrFractionalSharesOnlySupportedForPaperTrading = errors.New(
		"fractional share orders (milliShareQuantity) are only supported for paper trading orders in this build -- matching-engine has no milli-share-precision field yet, see internal/fractionalshares package doc",
	)
)

// ValidateMilliShareQuantity enforces the two real rules this layer can
// enforce: MilliShareQuantity, if set at all, must be positive, AND —
// the load-bearing one — a fractional order MUST be a paper-trading
// order. Nil MilliShareQuantity is always valid (backward-compatible
// no-op, the same convention orders.ValidateOrderExecutionType's empty
// case already establishes).
func ValidateMilliShareQuantity(milliShareQuantity *uint64, isPaperTradingOrder bool) error {
	if milliShareQuantity == nil {
		return nil
	}
	if *milliShareQuantity == 0 {
		return ErrMilliShareQuantityMustBePositive
	}
	if !isPaperTradingOrder {
		return ErrFractionalSharesOnlySupportedForPaperTrading
	}
	return nil
}

// NotionalInMinorUnits computes the real notional value of a milli-share
// quantity at a given per-share price, using exact integer arithmetic
// (no float): notional = priceInMinorUnits * milliShareQuantity /
// MilliShareUnitsPerWholeShare, rounded to the nearest minor unit
// (banker's-rounding-free — round-half-up, matching this codebase's
// other roundToNearestMinorUnit helpers, e.g.
// chargescalculator/marginengine/marginfunding).
//
// Uses integer division with explicit remainder-based rounding rather
// than a float64 intermediate, so this is exact for every input up to
// int64 overflow — no float-precision drift is possible.
func NotionalInMinorUnits(priceInMinorUnits int64, milliShareQuantity uint64) int64 {
	if priceInMinorUnits == 0 || milliShareQuantity == 0 {
		return 0
	}
	numerator := priceInMinorUnits * int64(milliShareQuantity)
	quotient := numerator / MilliShareUnitsPerWholeShare
	remainder := numerator % MilliShareUnitsPerWholeShare
	// Round half up (away from zero for a negative price, consistent
	// with this codebase's other roundToNearestMinorUnit helpers).
	if remainder >= MilliShareUnitsPerWholeShare/2 {
		quotient++
	} else if remainder <= -MilliShareUnitsPerWholeShare/2 {
		quotient--
	}
	return quotient
}

// FormatWholeAndMilliParts splits a milli-share quantity into its whole-
// share and remaining-milli-share parts, e.g. 1500 -> (1, 500) meaning
// "1.500 shares". Pure integer math.
func FormatWholeAndMilliParts(milliShareQuantity int64) (wholeShares int64, remainingMilliUnits int64) {
	wholeShares = milliShareQuantity / MilliShareUnitsPerWholeShare
	remainingMilliUnits = milliShareQuantity % MilliShareUnitsPerWholeShare
	if remainingMilliUnits < 0 {
		remainingMilliUnits = -remainingMilliUnits
	}
	return wholeShares, remainingMilliUnits
}
