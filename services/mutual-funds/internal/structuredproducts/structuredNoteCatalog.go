// Package structuredproducts is a real payoff-structure definition and
// subscription/maturity-payout state machine for illustrative capital-
// protected, market-linked structured notes — FEATURES.md §17, "Wealth &
// Product Breadth", a `[P4]` item ("Structured products desk (capital-
// protected notes, market-linked debentures)").
//
// LOUD CAVEAT: every note in this catalog is entirely fictitious, and this
// package is NOT connected to any real structured-products issuance desk,
// investment bank, or exchange. There is no real underlying index feed —
// "the index's return over the note's tenor" is a plain float64 the caller
// supplies at maturity time (a real build would need a genuine index-level
// data feed struck at issue and again at maturity). The PAYOFF MATH,
// however, IS real and testable — see structuredNotePayoff.go — the same
// "illustrative inputs, real math" spirit as internal/roboadvisory's
// Sharpe/Sortino call or oms-gateway's options payoff diagram.
package structuredproducts

import (
	"fmt"
	"sort"
	"sync"
)

// Note is one illustrative structured-product payoff structure:
// "PrincipalProtectionPercent% capital protection + ParticipationRatePercent%
// participation in UnderlyingIndexName's upside, capped at CapPercent%
// total return."
type Note struct {
	NoteId                     string
	Name                       string
	UnderlyingIndexName        string
	PrincipalProtectionPercent float64
	ParticipationRatePercent   float64
	CapPercent                 float64
	TenorMonths                int
}

var ErrUnknownNote = fmt.Errorf("no such structured note exists in the catalog")

// Catalog is a read-mostly, concurrency-safe lookup of the static note
// list.
type Catalog struct {
	mutexGuardingNotes sync.RWMutex
	notesById          map[string]Note
}

// NewCatalog returns a catalog pre-populated with three illustrative,
// entirely fictitious structured notes spanning a range of participation
// rates and caps.
func NewCatalog() *Catalog {
	seedNotes := []Note{
		{
			NoteId: "SP-CPN-NIFTY-150-20", Name: "Capital Protected Nifty Growth Note",
			UnderlyingIndexName: "Illustrative Nifty 50 Index", PrincipalProtectionPercent: 100,
			ParticipationRatePercent: 150, CapPercent: 20, TenorMonths: 36,
		},
		{
			NoteId: "SP-CPN-SENSEX-100-15", Name: "Capital Protected Sensex Income Note",
			UnderlyingIndexName: "Illustrative Sensex Index", PrincipalProtectionPercent: 100,
			ParticipationRatePercent: 100, CapPercent: 15, TenorMonths: 24,
		},
		{
			NoteId: "SP-CPN-GLOBAL-200-30", Name: "Capital Protected Global Equity Note",
			UnderlyingIndexName: "Illustrative Global Equity Index", PrincipalProtectionPercent: 100,
			ParticipationRatePercent: 200, CapPercent: 30, TenorMonths: 60,
		},
	}

	notesById := make(map[string]Note, len(seedNotes))
	for _, note := range seedNotes {
		notesById[note.NoteId] = note
	}

	return &Catalog{notesById: notesById}
}

// Lookup returns the note, or false if noteId isn't in the catalog.
func (catalog *Catalog) Lookup(noteId string) (Note, bool) {
	catalog.mutexGuardingNotes.RLock()
	defer catalog.mutexGuardingNotes.RUnlock()

	note, wasFound := catalog.notesById[noteId]
	return note, wasFound
}

// ListAll returns every note, sorted by NoteId for a deterministic
// response.
func (catalog *Catalog) ListAll() []Note {
	catalog.mutexGuardingNotes.RLock()
	defer catalog.mutexGuardingNotes.RUnlock()

	notes := make([]Note, 0, len(catalog.notesById))
	for _, note := range catalog.notesById {
		notes = append(notes, note)
	}
	sort.Slice(notes, func(i, j int) bool { return notes[i].NoteId < notes[j].NoteId })
	return notes
}
