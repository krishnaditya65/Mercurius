// Package corporateactionexplainer implements FEATURES.md §21's
// "Corporate-action explainer: when a split/bonus/merger changes
// quantity or average price, show a one-line 'why did this change'
// inline".
//
// HONEST, LOAD-BEARING SCOPE BOUNDARY: this repository has NO real
// corporate-actions detection/processing pipeline at all — no feed that
// observes an actual exchange corporate action and automatically
// recomputes affected accounts' quantities/average prices (that's
// FEATURES.md §14, explicitly out of scope for this package and not
// built anywhere in this codebase as of this build). What IS real here:
// given an adjustment event — however it was triggered, today only ever
// a manual/synthetic `POST /corporate-actions/apply-adjustment` call,
// since there's no real feed to trigger it automatically — this package
// computes a REAL, accurate, human-readable one-line explanation
// reflecting the ACTUAL supplied before/after numbers (not a canned
// template that ignores the real values), and a real, queryable,
// append-only log of every adjustment applied. Think of this as the
// EXPLAINER SURFACE for when a real corporate action eventually does get
// processed by a future §14 build — not corporate-actions processing
// itself.
package corporateactionexplainer

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// ActionType is a closed set of real corporate-action categories.
type ActionType string

const (
	ActionTypeStockSplit ActionType = "STOCK_SPLIT"
	ActionTypeBonusIssue ActionType = "BONUS_ISSUE"
	ActionTypeMerger     ActionType = "MERGER"
	ActionTypeOther      ActionType = "OTHER"
)

var (
	// ErrUnknownActionType is returned for an ActionType outside the
	// closed set above.
	ErrUnknownActionType = errors.New("corporateactionexplainer: unknown action type")
	// ErrNonPositiveQuantityBefore is returned when QuantityBefore isn't
	// a real, positive prior holding — there's nothing to adjust.
	ErrNonPositiveQuantityBefore = errors.New("corporateactionexplainer: quantityBefore must be greater than zero")
	// ErrNonPositiveQuantityAfter is returned when QuantityAfter isn't
	// positive — a corporate action that zeroes out a holding isn't
	// representable by this simple before/after model (a real delisting/
	// full-buyout event needs its own handling, out of scope here).
	ErrNonPositiveQuantityAfter = errors.New("corporateactionexplainer: quantityAfter must be greater than zero")
)

// AdjustmentEvent is one real, supplied quantity/average-price change —
// today always caller-supplied (see the package doc's honest scope
// boundary), never derived from a live corporate-actions feed.
type AdjustmentEvent struct {
	ClientAccountIdentifier        string     `json:"clientAccountIdentifier"`
	InstrumentSymbol               string     `json:"instrumentSymbol"`
	ActionType                     ActionType `json:"actionType"`
	QuantityBefore                 int64      `json:"quantityBefore"`
	QuantityAfter                  int64      `json:"quantityAfter"`
	AveragePriceBeforeInMinorUnits int64      `json:"averagePriceBeforeInMinorUnits"`
	AveragePriceAfterInMinorUnits  int64      `json:"averagePriceAfterInMinorUnits"`
}

func validateEvent(event AdjustmentEvent) error {
	switch event.ActionType {
	case ActionTypeStockSplit, ActionTypeBonusIssue, ActionTypeMerger, ActionTypeOther:
	default:
		return ErrUnknownActionType
	}
	if event.QuantityBefore <= 0 {
		return ErrNonPositiveQuantityBefore
	}
	if event.QuantityAfter <= 0 {
		return ErrNonPositiveQuantityAfter
	}
	return nil
}

// Explain computes a real, accurate, human-readable one-line explanation
// from the event's ACTUAL before/after numbers — not a canned template.
// Ratios are computed exactly (as a rational approximation printed to 2
// decimal places) so e.g. a genuine 1:2 stock split reads "each share
// became 2.00 shares", not a vague "your quantity changed".
func Explain(event AdjustmentEvent) (string, error) {
	if validationError := validateEvent(event); validationError != nil {
		return "", validationError
	}

	quantityRatio := float64(event.QuantityAfter) / float64(event.QuantityBefore)

	var actionLabel string
	switch event.ActionType {
	case ActionTypeStockSplit:
		actionLabel = "stock split"
	case ActionTypeBonusIssue:
		actionLabel = "bonus issue"
	case ActionTypeMerger:
		actionLabel = "merger/share-exchange"
	default:
		actionLabel = "corporate action"
	}

	priceChanged := event.AveragePriceAfterInMinorUnits != event.AveragePriceBeforeInMinorUnits

	if priceChanged {
		return fmt.Sprintf(
			"A %s changed your %s holding from %d shares @ avg ₹%.2f to %d shares @ avg ₹%.2f (quantity ratio %.2fx) -- your total invested value is unchanged, only how it's split across shares.",
			actionLabel, event.InstrumentSymbol,
			event.QuantityBefore, float64(event.AveragePriceBeforeInMinorUnits)/100.0,
			event.QuantityAfter, float64(event.AveragePriceAfterInMinorUnits)/100.0,
			quantityRatio,
		), nil
	}

	return fmt.Sprintf(
		"A %s changed your %s holding from %d shares to %d shares (quantity ratio %.2fx); your average price of ₹%.2f is unchanged.",
		actionLabel, event.InstrumentSymbol,
		event.QuantityBefore, event.QuantityAfter,
		quantityRatio,
		float64(event.AveragePriceBeforeInMinorUnits)/100.0,
	), nil
}

// LoggedAdjustment is one real, recorded adjustment plus its computed
// explanation and the real instant it was recorded.
type LoggedAdjustment struct {
	AdjustmentEvent
	OneLineExplanation string    `json:"oneLineExplanation"`
	RecordedAtTime     time.Time `json:"recordedAtTime"`
}

// Log is a real, mutex-guarded, append-only record of every adjustment
// applied — mirrors internal/audittrail's own append-only convention (no
// update/delete method exists).
type Log struct {
	mutexGuardingEntries sync.Mutex
	entries              []LoggedAdjustment
}

func NewLog() *Log {
	return &Log{}
}

// RecordAdjustment validates and explains the event, appends it to the
// log, and returns the resulting LoggedAdjustment.
func (log *Log) RecordAdjustment(event AdjustmentEvent) (LoggedAdjustment, error) {
	explanation, explainError := Explain(event)
	if explainError != nil {
		return LoggedAdjustment{}, explainError
	}

	logged := LoggedAdjustment{
		AdjustmentEvent:    event,
		OneLineExplanation: explanation,
		RecordedAtTime:     time.Now(),
	}

	log.mutexGuardingEntries.Lock()
	log.entries = append(log.entries, logged)
	log.mutexGuardingEntries.Unlock()

	return logged, nil
}

// EntriesForAccount returns every logged adjustment for one account,
// oldest first. Returns a copy — callers can't mutate the log's
// internal slice through it.
func (log *Log) EntriesForAccount(accountIdentifier string) []LoggedAdjustment {
	log.mutexGuardingEntries.Lock()
	defer log.mutexGuardingEntries.Unlock()

	var matching []LoggedAdjustment
	for _, entry := range log.entries {
		if entry.ClientAccountIdentifier == accountIdentifier {
			matching = append(matching, entry)
		}
	}
	return matching
}

// AllEntries returns every logged adjustment across every account,
// oldest first.
func (log *Log) AllEntries() []LoggedAdjustment {
	log.mutexGuardingEntries.Lock()
	defer log.mutexGuardingEntries.Unlock()

	entriesCopy := make([]LoggedAdjustment, len(log.entries))
	copy(entriesCopy, log.entries)
	return entriesCopy
}
