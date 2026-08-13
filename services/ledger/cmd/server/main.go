// Mercurius / ledger
//
// Tier 2 component per ARCHITECTURE.md §6. A real in-memory double-entry
// accounting core (internal/doubleentry) exposed over HTTP — now
// including a /journal-entries endpoint so other services (oms-gateway,
// as of this build) can actually post trade settlements, not just read
// balances. NOT yet backed by PostgreSQL.
package main

import (
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"os"

	"mercurius/ledger/internal/doubleentry"
	"mercurius/ledger/internal/httplogging"
)

func main() {
	// Structured (JSON) logging by default from here on — see
	// internal/httplogging's package doc for exactly what this does and
	// does not cover yet (HTTP access logs, not every business-event log
	// line in this file).
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	// TODO(real build): accounts must be created via a real account-opening
	// flow (tied to KYC completion), not hardcoded, and the ledger must be
	// PostgreSQL-backed, not in-memory. Account IDs here deliberately match
	// oms-gateway's demo seed accounts (acct-001, acct-002) so the two
	// services can be exercised together end-to-end.
	ledgerBook := doubleentry.NewInMemoryDoubleEntryLedgerBookWithAccounts([]string{
		"acct-001",
		"acct-002",
		"firm-clearing-acct",
	})

	httpRequestMultiplexer := http.NewServeMux()
	httpRequestMultiplexer.HandleFunc("/health", func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = responseWriter.Write([]byte(`{"status":"ok","service":"ledger"}`))
	})
	httpRequestMultiplexer.HandleFunc("/accounts/balance", buildBalanceLookupHandler(ledgerBook))
	httpRequestMultiplexer.HandleFunc("/journal-entries", buildPostJournalEntryHandler(ledgerBook))

	listenAddress := ":8082"
	log.Printf("ledger listening on %s — in-memory only, not Postgres-backed yet\n", listenAddress)
	if serverStartupError := http.ListenAndServe(listenAddress, httplogging.WithRequestLogging(httpRequestMultiplexer)); serverStartupError != nil {
		log.Fatalf("ledger failed to start: %v", serverStartupError)
	}
}

func buildBalanceLookupHandler(ledgerBook *doubleentry.InMemoryDoubleEntryLedgerBook) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}

		currentBalance, lookupError := ledgerBook.CurrentBalanceInMinorUnits(accountIdentifier)
		if lookupError != nil {
			http.Error(responseWriter, lookupError.Error(), http.StatusNotFound)
			return
		}

		responseWriter.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(responseWriter).Encode(map[string]any{
			"accountIdentifier":          accountIdentifier,
			"currentBalanceInMinorUnits": currentBalance,
		})
	}
}

// JournalEntryLineWireFormat and PostJournalEntryWireRequest are this
// endpoint's own wire types, deliberately decoupled from
// doubleentry.JournalEntry (which has no JSON tags — it's an internal
// domain type, not a wire contract) — same pattern as oms-gateway's
// `orders` package keeping its own wire types separate from
// matchingengineclient's.
type JournalEntryLineWireFormat struct {
	LedgerAccountIdentifier string `json:"ledgerAccountIdentifier"`
	AmountInMinorUnits      int64  `json:"amountInMinorUnits"`
}

type PostJournalEntryWireRequest struct {
	HumanReadableDescription string                       `json:"humanReadableDescription"`
	DebitLines               []JournalEntryLineWireFormat `json:"debitLines"`
	CreditLines              []JournalEntryLineWireFormat `json:"creditLines"`
}

type PostJournalEntryWireResponse struct {
	WasJournalEntryPosted bool   `json:"wasJournalEntryPosted"`
	ErrorMessage          string `json:"errorMessage,omitempty"`
}

func buildPostJournalEntryHandler(ledgerBook *doubleentry.InMemoryDoubleEntryLedgerBook) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest PostJournalEntryWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed journal entry payload", http.StatusBadRequest)
			return
		}

		journalEntry := doubleentry.JournalEntry{
			HumanReadableDescription: wireRequest.HumanReadableDescription,
			DebitLines:               convertWireLinesToLedgerAccountLines(wireRequest.DebitLines),
			CreditLines:              convertWireLinesToLedgerAccountLines(wireRequest.CreditLines),
		}

		postError := ledgerBook.PostJournalEntry(journalEntry)

		responseWriter.Header().Set("Content-Type", "application/json")
		if postError != nil {
			log.Printf("journal entry rejected: %v", postError)
			responseWriter.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(responseWriter).Encode(PostJournalEntryWireResponse{
				WasJournalEntryPosted: false,
				ErrorMessage:          postError.Error(),
			})
			return
		}

		_ = json.NewEncoder(responseWriter).Encode(PostJournalEntryWireResponse{WasJournalEntryPosted: true})
	}
}

func convertWireLinesToLedgerAccountLines(wireLines []JournalEntryLineWireFormat) []doubleentry.LedgerAccountLine {
	convertedLines := make([]doubleentry.LedgerAccountLine, 0, len(wireLines))
	for _, wireLine := range wireLines {
		convertedLines = append(convertedLines, doubleentry.LedgerAccountLine{
			LedgerAccountIdentifier: wireLine.LedgerAccountIdentifier,
			AmountInMinorUnits:      wireLine.AmountInMinorUnits,
		})
	}
	return convertedLines
}
