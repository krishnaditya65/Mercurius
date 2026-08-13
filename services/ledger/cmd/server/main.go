// Mercurius / ledger
//
// Tier 2 component per ARCHITECTURE.md §6. A real in-memory double-entry
// accounting core (internal/doubleentry) exposed over HTTP — now
// including a /journal-entries endpoint so other services (oms-gateway,
// as of this build) can actually post trade settlements, not just read
// balances, and a real withdrawal workflow with T+N settlement holds
// (internal/withdrawalworkflow). NOT yet backed by PostgreSQL.
package main

import (
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"mercurius/ledger/internal/doubleentry"
	"mercurius/ledger/internal/httplogging"
	"mercurius/ledger/internal/withdrawalworkflow"
)

// defaultSettlementHoldDuration is T+2 — a common real settlement cycle
// for equity delivery trades in India (T+1 as of recent SEBI changes,
// but T+2 is still common elsewhere/historically and is a reasonable
// illustrative default here). Overridable via
// WITHDRAWAL_SETTLEMENT_HOLD_DAYS for testing without waiting 2 real
// days.
const defaultSettlementHoldDurationDays = 2

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

	settlementHoldDurationDays := defaultSettlementHoldDurationDays
	if envValue := os.Getenv("WITHDRAWAL_SETTLEMENT_HOLD_DAYS"); envValue != "" {
		if parsedDays, parseError := strconv.Atoi(envValue); parseError == nil {
			settlementHoldDurationDays = parsedDays
		}
	}
	withdrawalWorkflowInstance := withdrawalworkflow.NewWithdrawalWorkflow(
		ledgerBook,
		"firm-clearing-acct",
		time.Duration(settlementHoldDurationDays)*24*time.Hour,
	)

	httpRequestMultiplexer := http.NewServeMux()
	httpRequestMultiplexer.HandleFunc("/health", func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = responseWriter.Write([]byte(`{"status":"ok","service":"ledger"}`))
	})
	httpRequestMultiplexer.HandleFunc("/accounts/balance", buildBalanceLookupHandler(ledgerBook, withdrawalWorkflowInstance))
	httpRequestMultiplexer.HandleFunc("/journal-entries", buildPostJournalEntryHandler(ledgerBook))
	httpRequestMultiplexer.HandleFunc("/withdrawals/request", buildWithdrawalRequestHandler(withdrawalWorkflowInstance))
	httpRequestMultiplexer.HandleFunc("/withdrawals/cancel", buildWithdrawalCancelHandler(withdrawalWorkflowInstance))
	httpRequestMultiplexer.HandleFunc("/withdrawals", buildWithdrawalListHandler(withdrawalWorkflowInstance))
	httpRequestMultiplexer.HandleFunc("/withdrawals/process-due", buildWithdrawalProcessDueHandler(withdrawalWorkflowInstance))

	listenAddress := ":8082"
	log.Printf(
		"ledger listening on %s — in-memory only, not Postgres-backed yet (withdrawal settlement hold: T+%d days)\n",
		listenAddress, settlementHoldDurationDays,
	)
	if serverStartupError := http.ListenAndServe(listenAddress, httplogging.WithRequestLogging(httpRequestMultiplexer)); serverStartupError != nil {
		log.Fatalf("ledger failed to start: %v", serverStartupError)
	}
}

func buildBalanceLookupHandler(ledgerBook *doubleentry.InMemoryDoubleEntryLedgerBook, withdrawalWorkflowInstance *withdrawalworkflow.WithdrawalWorkflow) http.HandlerFunc {
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
		// Also surfaces the AVAILABLE balance (raw balance minus
		// currently-held pending withdrawals) alongside the raw one —
		// added when the withdrawal workflow was built, since "your
		// balance" and "what you can actually withdraw or trade
		// against" are genuinely different numbers once a withdrawal
		// hold exists.
		availableBalance, _ := withdrawalWorkflowInstance.AvailableBalanceInMinorUnits(accountIdentifier)

		responseWriter.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(responseWriter).Encode(map[string]any{
			"accountIdentifier":            accountIdentifier,
			"currentBalanceInMinorUnits":   currentBalance,
			"availableBalanceInMinorUnits": availableBalance,
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

type withdrawalRequestWireRequest struct {
	AccountIdentifier  string `json:"accountIdentifier"`
	AmountInMinorUnits int64  `json:"amountInMinorUnits"`
}

type withdrawalRequestWireResponse struct {
	WithdrawalId        string `json:"withdrawalId,omitempty"`
	AccountIdentifier   string `json:"accountIdentifier,omitempty"`
	AmountInMinorUnits  int64  `json:"amountInMinorUnits,omitempty"`
	Status              string `json:"status,omitempty"`
	EligibleForPayoutAt string `json:"eligibleForPayoutAt,omitempty"`
	ErrorMessage        string `json:"errorMessage,omitempty"`
}

func buildWithdrawalRequestHandler(withdrawalWorkflowInstance *withdrawalworkflow.WithdrawalWorkflow) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest withdrawalRequestWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed withdrawal request payload", http.StatusBadRequest)
			return
		}

		withdrawalRequest, requestError := withdrawalWorkflowInstance.RequestWithdrawal(
			wireRequest.AccountIdentifier,
			wireRequest.AmountInMinorUnits,
			time.Now(),
		)
		if requestError != nil {
			respondWithLedgerJson(responseWriter, http.StatusBadRequest, withdrawalRequestWireResponse{
				ErrorMessage: requestError.Error(),
			})
			return
		}

		respondWithLedgerJson(responseWriter, http.StatusOK, wireResponseFromWithdrawalRequest(withdrawalRequest))
	}
}

type withdrawalCancelWireRequest struct {
	WithdrawalId string `json:"withdrawalId"`
}

func buildWithdrawalCancelHandler(withdrawalWorkflowInstance *withdrawalworkflow.WithdrawalWorkflow) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest withdrawalCancelWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed withdrawal cancel payload", http.StatusBadRequest)
			return
		}

		cancelledRequest, cancelError := withdrawalWorkflowInstance.CancelWithdrawal(wireRequest.WithdrawalId)
		if cancelError != nil {
			respondWithLedgerJson(responseWriter, http.StatusBadRequest, withdrawalRequestWireResponse{
				WithdrawalId: wireRequest.WithdrawalId,
				ErrorMessage: cancelError.Error(),
			})
			return
		}

		respondWithLedgerJson(responseWriter, http.StatusOK, wireResponseFromWithdrawalRequest(cancelledRequest))
	}
}

func buildWithdrawalListHandler(withdrawalWorkflowInstance *withdrawalworkflow.WithdrawalWorkflow) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}

		requests := withdrawalWorkflowInstance.RequestsForAccount(accountIdentifier)
		wireResponses := make([]withdrawalRequestWireResponse, 0, len(requests))
		for _, withdrawalRequest := range requests {
			wireResponses = append(wireResponses, wireResponseFromWithdrawalRequest(withdrawalRequest))
		}
		respondWithLedgerJson(responseWriter, http.StatusOK, wireResponses)
	}
}

type withdrawalProcessDueWireResponse struct {
	CompletedWithdrawalIds []string `json:"completedWithdrawalIds"`
	FailedWithdrawalIds    []string `json:"failedWithdrawalIds"`
}

// buildWithdrawalProcessDueHandler is the sweep an operator/scheduled
// job calls to actually pay out every withdrawal whose hold period has
// elapsed — see internal/withdrawalworkflow's package doc: this is NOT
// run automatically on a timer in this skeleton.
func buildWithdrawalProcessDueHandler(withdrawalWorkflowInstance *withdrawalworkflow.WithdrawalWorkflow) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		completed, failed := withdrawalWorkflowInstance.ProcessDueWithdrawals(time.Now())
		completedIds := make([]string, 0, len(completed))
		for _, withdrawalRequest := range completed {
			completedIds = append(completedIds, withdrawalRequest.WithdrawalId)
		}
		if failed == nil {
			failed = []string{}
		}

		respondWithLedgerJson(responseWriter, http.StatusOK, withdrawalProcessDueWireResponse{
			CompletedWithdrawalIds: completedIds,
			FailedWithdrawalIds:    failed,
		})
	}
}

func wireResponseFromWithdrawalRequest(request *withdrawalworkflow.WithdrawalRequest) withdrawalRequestWireResponse {
	return withdrawalRequestWireResponse{
		WithdrawalId:        request.WithdrawalId,
		AccountIdentifier:   request.AccountIdentifier,
		AmountInMinorUnits:  request.AmountInMinorUnits,
		Status:              string(request.Status),
		EligibleForPayoutAt: request.EligibleForPayoutAt.Format(time.RFC3339),
	}
}

func respondWithLedgerJson(responseWriter http.ResponseWriter, statusCode int, body any) {
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(statusCode)
	_ = json.NewEncoder(responseWriter).Encode(body)
}
