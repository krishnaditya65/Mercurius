// Package filltrail turns oms-gateway's real audit-trail entries into
// structured, per-fill records that the rest of reporting (contract
// notes, capital gains, AIS reconciliation) can work with.
//
// PRIMARY DATA SOURCE: oms-gateway's audittrail.Entry now carries
// structured EventOrderFilled fields (BuyingClientAccountIdentifier,
// SellingClientAccountIdentifier, ExecutedPriceInMinorUnits,
// ExecutedQuantity — added for internal/tradesurveillance). This
// package reads those directly; it does NOT need to parse the
// free-text DetailMessage for any entry that has them. A regex fallback
// against DetailMessage's known "filled %d @ %d (buyer=%s seller=%s)"
// format is kept only as a defensive backstop for any entry that
// somehow lacks the structured fields (there should be none from a
// current oms-gateway build).
//
// REAL, LOAD-BEARING GAP DISCOVERED WHILE BUILDING THIS AGAINST A LIVE
// oms-gateway, documented loudly because it changes how callers must use
// this package: GET /audit-trail?accountId=X only returns an
// ORDER_FILLED entry under the account whose order request was the one
// that actually crossed (the taker) — the resting/maker counterparty on
// the SAME trade never gets that entry under ITS OWN
// accountId-filtered query. ParseFillsFromAllAuditTrailEntries below
// therefore requires the CALLER to pass the FULL, unfiltered audit
// trail (see omsgatewayclient.FetchAllAuditTrailEntries) — it filters
// to accountIdentifier's fills itself, by checking whether
// accountIdentifier is the buyer OR the seller on each ORDER_FILLED
// entry, which is the only way to get a complete, correct fill history
// for one account.
package filltrail

import (
	"fmt"
	"regexp"
	"strconv"
	"time"

	"mercurius/reporting/internal/omsgatewayclient"
)

const (
	// EventTypeOrderFilled mirrors oms-gateway's
	// audittrail.EventOrderFilled string constant.
	EventTypeOrderFilled = "ORDER_FILLED"

	// SideBuy and SideSell are this package's own Fill.Side values —
	// determined by comparing the requested account against the
	// buyer/seller accounts on the fill entry, not supplied by the
	// caller.
	SideBuy  = "BUY"
	SideSell = "SELL"
)

// fillDetailMessagePattern matches oms-gateway's ORDER_FILLED
// DetailMessage format "filled %d @ %d (buyer=%s seller=%s)" — used only
// as a defensive fallback, see package doc.
var fillDetailMessagePattern = regexp.MustCompile(`^filled (\d+) @ (\d+) \(buyer=(\S+) seller=(\S+)\)$`)

// Fill is one real, executed trade for one account, reconstructed from
// oms-gateway's real audit trail.
type Fill struct {
	AccountIdentifier                 string
	InstrumentSymbol                  string
	Side                              string // SideBuy or SideSell
	Quantity                          uint64
	PriceInMinorUnits                 int64
	ExecutedAtTime                    time.Time
	MatchingEngineOrderSequenceNumber uint64
	CounterpartyAccountIdentifier     string
}

// extractFillFields returns the buyer, seller, price, and quantity for
// one ORDER_FILLED entry, preferring the structured fields and falling
// back to parsing DetailMessage only if the structured fields are all
// absent.
func extractFillFields(entry omsgatewayclient.AuditTrailEntryWireFormat) (buyer string, seller string, priceInMinorUnits int64, quantity uint64, err error) {
	if entry.BuyingClientAccountIdentifier != "" || entry.SellingClientAccountIdentifier != "" {
		return entry.BuyingClientAccountIdentifier, entry.SellingClientAccountIdentifier, entry.ExecutedPriceInMinorUnits, entry.ExecutedQuantity, nil
	}

	matches := fillDetailMessagePattern.FindStringSubmatch(entry.DetailMessage)
	if matches == nil {
		return "", "", 0, 0, fmt.Errorf(
			"audit-trail entry at %s for %s has no structured fill fields and detailMessage %q does not match the known fallback format",
			entry.RecordedAtTime.Format(time.RFC3339), entry.InstrumentSymbol, entry.DetailMessage,
		)
	}
	parsedQuantity, quantityParseError := strconv.ParseUint(matches[1], 10, 64)
	if quantityParseError != nil {
		return "", "", 0, 0, fmt.Errorf("could not parse fallback quantity %q: %w", matches[1], quantityParseError)
	}
	parsedPrice, priceParseError := strconv.ParseInt(matches[2], 10, 64)
	if priceParseError != nil {
		return "", "", 0, 0, fmt.Errorf("could not parse fallback price %q: %w", matches[2], priceParseError)
	}
	return matches[3], matches[4], parsedPrice, parsedQuantity, nil
}

// ParseFillsFromAllAuditTrailEntries filters the FULL, unfiltered audit
// trail (every account) down to ORDER_FILLED events where
// accountIdentifier is either the buyer or the seller, and parses each
// into a structured Fill, oldest first. See the package doc for why the
// input must be the unfiltered trail, not a per-account one.
func ParseFillsFromAllAuditTrailEntries(accountIdentifier string, allEntries []omsgatewayclient.AuditTrailEntryWireFormat) (fills []Fill, unparseableEntryErrors []error) {
	for _, entry := range allEntries {
		if entry.EventType != EventTypeOrderFilled {
			continue
		}

		buyer, seller, priceInMinorUnits, quantity, extractError := extractFillFields(entry)
		if extractError != nil {
			unparseableEntryErrors = append(unparseableEntryErrors, extractError)
			continue
		}

		var side, counterparty string
		switch accountIdentifier {
		case buyer:
			side = SideBuy
			counterparty = seller
		case seller:
			side = SideSell
			counterparty = buyer
		default:
			continue // this fill doesn't involve accountIdentifier at all
		}

		fills = append(fills, Fill{
			AccountIdentifier:                 accountIdentifier,
			InstrumentSymbol:                  entry.InstrumentSymbol,
			Side:                              side,
			Quantity:                          quantity,
			PriceInMinorUnits:                 priceInMinorUnits,
			ExecutedAtTime:                    entry.RecordedAtTime,
			MatchingEngineOrderSequenceNumber: entry.MatchingEngineOrderSequenceNumber,
			CounterpartyAccountIdentifier:     counterparty,
		})
	}
	return fills, unparseableEntryErrors
}

// FillsOnDate filters fills down to those executed on the given
// calendar date (UTC).
func FillsOnDate(fills []Fill, date time.Time) []Fill {
	year, month, day := date.Date()
	var onDate []Fill
	for _, fill := range fills {
		fillYear, fillMonth, fillDay := fill.ExecutedAtTime.Date()
		if fillYear == year && fillMonth == month && fillDay == day {
			onDate = append(onDate, fill)
		}
	}
	return onDate
}

// FillsInRange filters fills down to those executed within
// [startInclusive, endInclusive].
func FillsInRange(fills []Fill, startInclusive time.Time, endInclusive time.Time) []Fill {
	var inRange []Fill
	for _, fill := range fills {
		if !fill.ExecutedAtTime.Before(startInclusive) && !fill.ExecutedAtTime.After(endInclusive) {
			inRange = append(inRange, fill)
		}
	}
	return inRange
}
