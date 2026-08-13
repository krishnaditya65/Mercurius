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
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"mercurius/ledger/internal/amlmonitoring"
	"mercurius/ledger/internal/depositrail"
	"mercurius/ledger/internal/doubleentry"
	"mercurius/ledger/internal/fundsegregation"
	"mercurius/ledger/internal/httplogging"
	"mercurius/ledger/internal/multicurrencywallet"
	"mercurius/ledger/internal/paymentmandate"
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
		"client-money-custody-pool",
		"external-cash-suspense",
	})

	// Client fund segregation (FEATURES.md §1): acct-001/acct-002 are the
	// firm's demo CLIENT accounts, firm-clearing-acct is the firm's own
	// money. client-money-custody-pool must always equal the sum of every
	// CLIENT account's balance — see internal/fundsegregation's package
	// doc for exactly what is and isn't covered yet.
	segregationGuard := fundsegregation.NewSegregationGuard(
		ledgerBook,
		"client-money-custody-pool",
		"external-cash-suspense",
		[]string{"acct-001", "acct-002"},
		[]string{"firm-clearing-acct"},
	)

	// AML transaction monitoring (FEATURES.md §1): illustrative
	// thresholds, NOT derived from any real regulatory reporting limit
	// — see internal/amlmonitoring's package doc.
	amlMonitor := amlmonitoring.NewMonitor(amlmonitoring.MonitorConfig{
		LargeTransactionThresholdInMinorUnits:  1_000_000_00, // ₹10,00,000 in paise
		StructuringReportThresholdInMinorUnits: 1_000_000_00,
		StructuringWindow:                      24 * time.Hour,
		VelocityMaxTransactionsInWindow:        5,
		VelocityWindow:                         time.Hour,
	}, []string{"Corrupt Official", "Sanctioned Person"})

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

	// Simulated UPI/NEFT/IMPS/net-banking deposit rail (FEATURES.md §2):
	// see internal/depositrail's package doc — NOT a real bank
	// integration, models the request/confirm state machine only. A
	// CONFIRMED deposit posts real money through the same segregation
	// guard everything else uses.
	depositRailInstance := depositrail.NewSimulatedDepositRail(segregationGuard)

	// Simulated eNACH/standing-instruction SIP mandates (FEATURES.md
	// §2): see internal/paymentmandate's package doc — NOT real eNACH,
	// models the recurring-debit state machine only. Each due sweep
	// posts a real debit through the same segregation guard.
	paymentMandateRegistry := paymentmandate.NewPaymentMandateRegistry(segregationGuard)

	// Multi-currency wallet (FEATURES.md §2, "for platforms offering
	// global/US stocks"): see internal/multicurrencywallet's package doc
	// — a NEW layer on top of internal/doubleentry, not a change to it.
	// The FX rate table below is STATIC and ILLUSTRATIVE, not a live
	// market feed — see the package doc's loud disclaimer.
	multiCurrencyWalletRegistry := multicurrencywallet.NewMultiCurrencyWalletRegistry(
		ledgerBook,
		segregationGuard,
		"client-money-custody-pool",
		"wallet-external-cash-suspense",
		"fx-conversion-clearing-acct",
		"INR",
		multicurrencywallet.NewStaticFxRateTable(map[string]float64{
			"USD/INR": 83.0,
			"INR/USD": 1.0 / 83.0,
		}),
	)

	httpRequestMultiplexer := http.NewServeMux()
	httpRequestMultiplexer.HandleFunc("/health", func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = responseWriter.Write([]byte(`{"status":"ok","service":"ledger"}`))
	})
	httpRequestMultiplexer.HandleFunc("/accounts/balance", buildBalanceLookupHandler(ledgerBook, withdrawalWorkflowInstance))
	httpRequestMultiplexer.HandleFunc("/journal-entries", buildPostJournalEntryHandler(ledgerBook))
	httpRequestMultiplexer.HandleFunc("/withdrawals/request", buildWithdrawalRequestHandler(withdrawalWorkflowInstance, amlMonitor))
	httpRequestMultiplexer.HandleFunc("/withdrawals/cancel", buildWithdrawalCancelHandler(withdrawalWorkflowInstance))
	httpRequestMultiplexer.HandleFunc("/withdrawals", buildWithdrawalListHandler(withdrawalWorkflowInstance))
	httpRequestMultiplexer.HandleFunc("/withdrawals/process-due", buildWithdrawalProcessDueHandler(withdrawalWorkflowInstance))
	httpRequestMultiplexer.HandleFunc("/client-funds/deposit", buildClientFundsDepositHandler(segregationGuard, amlMonitor))
	httpRequestMultiplexer.HandleFunc("/client-funds/transfer", buildClientFundsTransferHandler(segregationGuard))
	httpRequestMultiplexer.HandleFunc("/client-funds/segregation-report", buildSegregationReportHandler(segregationGuard))
	httpRequestMultiplexer.HandleFunc("/client-funds/validate-entry", buildValidateEntryHandler(segregationGuard))
	httpRequestMultiplexer.HandleFunc("/aml/alerts", buildAmlAlertsHandler(amlMonitor))
	httpRequestMultiplexer.HandleFunc("/aml/screen-name", buildAmlScreenNameHandler(amlMonitor))
	httpRequestMultiplexer.HandleFunc("/deposits/initiate", buildDepositInitiateHandler(depositRailInstance))
	httpRequestMultiplexer.HandleFunc("/deposits/confirm", buildDepositConfirmHandler(depositRailInstance, amlMonitor))
	httpRequestMultiplexer.HandleFunc("/deposits", buildDepositListHandler(depositRailInstance))
	httpRequestMultiplexer.HandleFunc("/payment-mandates/register", buildPaymentMandateRegisterHandler(paymentMandateRegistry))
	httpRequestMultiplexer.HandleFunc("/payment-mandates/pause", buildPaymentMandatePauseHandler(paymentMandateRegistry))
	httpRequestMultiplexer.HandleFunc("/payment-mandates/resume", buildPaymentMandateResumeHandler(paymentMandateRegistry))
	httpRequestMultiplexer.HandleFunc("/payment-mandates/cancel", buildPaymentMandateCancelHandler(paymentMandateRegistry))
	httpRequestMultiplexer.HandleFunc("/payment-mandates/sweep-due", buildPaymentMandateSweepDueHandler(paymentMandateRegistry))
	httpRequestMultiplexer.HandleFunc("/payment-mandates", buildPaymentMandateListHandler(paymentMandateRegistry))
	httpRequestMultiplexer.HandleFunc("/wallets/deposit", buildWalletDepositHandler(multiCurrencyWalletRegistry))
	httpRequestMultiplexer.HandleFunc("/wallets/withdraw", buildWalletWithdrawHandler(multiCurrencyWalletRegistry))
	httpRequestMultiplexer.HandleFunc("/wallets/convert", buildWalletConvertHandler(multiCurrencyWalletRegistry))
	httpRequestMultiplexer.HandleFunc("/wallets", buildWalletListHandler(multiCurrencyWalletRegistry))
	httpRequestMultiplexer.HandleFunc("/admin/snapshot", buildAdminSnapshotHandler(ledgerBook))
	httpRequestMultiplexer.HandleFunc("/admin/restore", buildAdminRestoreHandler(ledgerBook))

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

func buildWithdrawalRequestHandler(withdrawalWorkflowInstance *withdrawalworkflow.WithdrawalWorkflow, amlMonitor *amlmonitoring.Monitor) http.HandlerFunc {
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

		now := time.Now()
		withdrawalRequest, requestError := withdrawalWorkflowInstance.RequestWithdrawal(
			wireRequest.AccountIdentifier,
			wireRequest.AmountInMinorUnits,
			now,
		)
		if requestError != nil {
			respondWithLedgerJson(responseWriter, http.StatusBadRequest, withdrawalRequestWireResponse{
				ErrorMessage: requestError.Error(),
			})
			return
		}

		// AML monitoring (FEATURES.md §1): every real withdrawal request
		// is reported for pattern analysis, same as a deposit — see
		// buildClientFundsDepositHandler.
		amlMonitor.RecordTransaction(wireRequest.AccountIdentifier, wireRequest.AmountInMinorUnits, now)

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

type clientFundsDepositWireRequest struct {
	AccountIdentifier  string `json:"accountIdentifier"`
	AmountInMinorUnits int64  `json:"amountInMinorUnits"`
}

type clientFundsWireResponse struct {
	WasApplied   bool   `json:"wasApplied"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

// buildClientFundsDepositHandler is the ring-fenced replacement for
// funding a CLIENT account through the raw /journal-entries endpoint —
// see internal/fundsegregation's package doc. A negative
// amountInMinorUnits is a payout (money leaving the client's custody).
func buildClientFundsDepositHandler(segregationGuard *fundsegregation.SegregationGuard, amlMonitor *amlmonitoring.Monitor) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest clientFundsDepositWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed client funds deposit payload", http.StatusBadRequest)
			return
		}

		postError := segregationGuard.PostClientMoneyMovement(
			wireRequest.AccountIdentifier,
			wireRequest.AmountInMinorUnits,
			fmt.Sprintf("client funds movement, account=%s", wireRequest.AccountIdentifier),
		)
		if postError != nil {
			respondWithLedgerJson(responseWriter, http.StatusBadRequest, clientFundsWireResponse{ErrorMessage: postError.Error()})
			return
		}

		// AML monitoring (FEATURES.md §1): every real, successfully-
		// posted client money movement is reported for pattern analysis
		// — large-transaction, velocity, and structuring rules all
		// evaluate here. Deliberately fires on both deposits AND payouts
		// (a negative amount) since structuring/velocity apply equally
		// to money leaving as to money arriving.
		amlMonitor.RecordTransaction(wireRequest.AccountIdentifier, wireRequest.AmountInMinorUnits, time.Now())

		respondWithLedgerJson(responseWriter, http.StatusOK, clientFundsWireResponse{WasApplied: true})
	}
}

type clientFundsTransferWireRequest struct {
	FromAccountIdentifier string `json:"fromAccountIdentifier"`
	ToAccountIdentifier   string `json:"toAccountIdentifier"`
	AmountInMinorUnits    int64  `json:"amountInMinorUnits"`
}

// buildClientFundsTransferHandler moves money between two CLIENT
// accounts without ever touching the custody pool — rejected outright if
// either side isn't classified CLIENT, so it can't be used to leak money
// to a firm account. See fundsegregation.PostInterClientTransfer.
func buildClientFundsTransferHandler(segregationGuard *fundsegregation.SegregationGuard) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest clientFundsTransferWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed client funds transfer payload", http.StatusBadRequest)
			return
		}

		transferError := segregationGuard.PostInterClientTransfer(
			wireRequest.FromAccountIdentifier,
			wireRequest.ToAccountIdentifier,
			wireRequest.AmountInMinorUnits,
			fmt.Sprintf("client-to-client transfer, from=%s to=%s", wireRequest.FromAccountIdentifier, wireRequest.ToAccountIdentifier),
		)
		if transferError != nil {
			respondWithLedgerJson(responseWriter, http.StatusBadRequest, clientFundsWireResponse{ErrorMessage: transferError.Error()})
			return
		}
		respondWithLedgerJson(responseWriter, http.StatusOK, clientFundsWireResponse{WasApplied: true})
	}
}

type segregationReportWireResponse struct {
	CustodyPoolAccountId               string `json:"custodyPoolAccountId"`
	CustodyPoolBalanceInMinorUnits     int64  `json:"custodyPoolBalanceInMinorUnits"`
	AggregateClientBalanceInMinorUnits int64  `json:"aggregateClientBalanceInMinorUnits"`
	DiscrepancyInMinorUnits            int64  `json:"discrepancyInMinorUnits"`
	IsSegregationIntact                bool   `json:"isSegregationIntact"`
	ClientAccountCount                 int    `json:"clientAccountCount"`
}

// buildSegregationReportHandler is what a compliance officer / regulator
// inquiry actually needs: proof, right now, that segregated client money
// on the books equals what clients are collectively owed.
func buildSegregationReportHandler(segregationGuard *fundsegregation.SegregationGuard) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, _ *http.Request) {
		report, reportError := segregationGuard.CheckSegregationInvariant()
		if reportError != nil {
			http.Error(responseWriter, reportError.Error(), http.StatusInternalServerError)
			return
		}
		respondWithLedgerJson(responseWriter, http.StatusOK, segregationReportWireResponse{
			CustodyPoolAccountId:               report.CustodyPoolAccountId,
			CustodyPoolBalanceInMinorUnits:     report.CustodyPoolBalanceInMinorUnits,
			AggregateClientBalanceInMinorUnits: report.AggregateClientBalanceInMinorUnits,
			DiscrepancyInMinorUnits:            report.DiscrepancyInMinorUnits,
			IsSegregationIntact:                report.IsSegregationIntact,
			ClientAccountCount:                 report.ClientAccountCount,
		})
	}
}

// buildValidateEntryHandler is a dry-run: given an arbitrary journal
// entry payload (the same wire shape /journal-entries accepts), report
// whether posting it WOULD break the segregation invariant — without
// ever posting it. Useful for an ops/compliance tool checking a proposed
// entry before it's submitted through the raw ledger endpoint.
func buildValidateEntryHandler(segregationGuard *fundsegregation.SegregationGuard) http.HandlerFunc {
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

		entry := doubleentry.JournalEntry{
			HumanReadableDescription: wireRequest.HumanReadableDescription,
			DebitLines:               convertWireLinesToLedgerAccountLines(wireRequest.DebitLines),
			CreditLines:              convertWireLinesToLedgerAccountLines(wireRequest.CreditLines),
		}

		validationError := segregationGuard.ValidateEntryPreservesSegregation(entry)
		if validationError != nil {
			respondWithLedgerJson(responseWriter, http.StatusOK, clientFundsWireResponse{
				WasApplied:   false,
				ErrorMessage: validationError.Error(),
			})
			return
		}
		respondWithLedgerJson(responseWriter, http.StatusOK, clientFundsWireResponse{WasApplied: true})
	}
}

type amlAlertWireFormat struct {
	AccountIdentifier string `json:"accountIdentifier"`
	AlertType         string `json:"alertType"`
	Description       string `json:"description"`
	RaisedAt          string `json:"raisedAt"`
}

func wireFormatFromAmlAlert(alert amlmonitoring.Alert) amlAlertWireFormat {
	return amlAlertWireFormat{
		AccountIdentifier: alert.AccountIdentifier,
		AlertType:         string(alert.AlertType),
		Description:       alert.Description,
		RaisedAt:          alert.RaisedAt.Format(time.RFC3339),
	}
}

// buildAmlAlertsHandler is the compliance officer's review queue:
// GET /aml/alerts returns every alert raised so far across every
// account; GET /aml/alerts?accountId=... scopes it to one account.
func buildAmlAlertsHandler(amlMonitor *amlmonitoring.Monitor) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		var alerts []amlmonitoring.Alert
		if accountIdentifier := request.URL.Query().Get("accountId"); accountIdentifier != "" {
			alerts = amlMonitor.AlertsForAccount(accountIdentifier)
		} else {
			alerts = amlMonitor.AllAlerts()
		}

		wireAlerts := make([]amlAlertWireFormat, 0, len(alerts))
		for _, alert := range alerts {
			wireAlerts = append(wireAlerts, wireFormatFromAmlAlert(alert))
		}
		respondWithLedgerJson(responseWriter, http.StatusOK, wireAlerts)
	}
}

type amlScreenNameWireRequest struct {
	AccountIdentifier string `json:"accountIdentifier"`
	FullName          string `json:"fullName"`
}

type amlScreenNameWireResponse struct {
	IsMatch bool                `json:"isMatch"`
	Alert   *amlAlertWireFormat `json:"alert,omitempty"`
}

// buildAmlScreenNameHandler is a real (if illustrative) PEP screen:
// check an account holder's name against the static watch list.
func buildAmlScreenNameHandler(amlMonitor *amlmonitoring.Monitor) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest amlScreenNameWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed screen-name payload", http.StatusBadRequest)
			return
		}

		alert, isMatch := amlMonitor.ScreenName(wireRequest.AccountIdentifier, wireRequest.FullName, time.Now())
		if !isMatch {
			respondWithLedgerJson(responseWriter, http.StatusOK, amlScreenNameWireResponse{IsMatch: false})
			return
		}
		wireAlert := wireFormatFromAmlAlert(alert)
		respondWithLedgerJson(responseWriter, http.StatusOK, amlScreenNameWireResponse{IsMatch: true, Alert: &wireAlert})
	}
}

// ---------------------------------------------------------------------
// Simulated UPI/NEFT/IMPS/net-banking deposit rail (FEATURES.md §2) —
// see internal/depositrail's package doc: NOT a real bank integration,
// no actual UPI/NEFT/IMPS/net-banking network call happens anywhere
// below. /deposits/initiate only records a PENDING claim; /deposits/
// confirm is what stands in for the bank's webhook and is the only
// place real money moves.
// ---------------------------------------------------------------------

type depositWireResponse struct {
	DepositId          string `json:"depositId,omitempty"`
	AccountIdentifier  string `json:"accountIdentifier,omitempty"`
	Method             string `json:"method,omitempty"`
	AmountInMinorUnits int64  `json:"amountInMinorUnits,omitempty"`
	Status             string `json:"status,omitempty"`
	InitiatedAt        string `json:"initiatedAt,omitempty"`
	ConfirmedAt        string `json:"confirmedAt,omitempty"`
	ErrorMessage       string `json:"errorMessage,omitempty"`
}

func wireResponseFromDeposit(deposit *depositrail.SimulatedDeposit) depositWireResponse {
	wireResponse := depositWireResponse{
		DepositId:          deposit.DepositId,
		AccountIdentifier:  deposit.AccountIdentifier,
		Method:             string(deposit.Method),
		AmountInMinorUnits: deposit.AmountInMinorUnits,
		Status:             string(deposit.Status),
		InitiatedAt:        deposit.InitiatedAt.Format(time.RFC3339),
	}
	if deposit.Status == depositrail.DepositStatusConfirmed {
		wireResponse.ConfirmedAt = deposit.ConfirmedAt.Format(time.RFC3339)
	}
	return wireResponse
}

type depositInitiateWireRequest struct {
	AccountIdentifier  string `json:"accountIdentifier"`
	Method             string `json:"method"`
	AmountInMinorUnits int64  `json:"amountInMinorUnits"`
}

// buildDepositInitiateHandler models a client claiming to be sending
// money via UPI/NEFT/IMPS/NETBANKING. This does NOT move any money — see
// internal/depositrail's package doc.
func buildDepositInitiateHandler(depositRailInstance *depositrail.SimulatedDepositRail) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest depositInitiateWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed deposit initiate payload", http.StatusBadRequest)
			return
		}

		deposit, initiateError := depositRailInstance.InitiateDeposit(
			wireRequest.AccountIdentifier,
			depositrail.DepositMethod(wireRequest.Method),
			wireRequest.AmountInMinorUnits,
			time.Now(),
		)
		if initiateError != nil {
			respondWithLedgerJson(responseWriter, http.StatusBadRequest, depositWireResponse{ErrorMessage: initiateError.Error()})
			return
		}

		respondWithLedgerJson(responseWriter, http.StatusOK, wireResponseFromDeposit(deposit))
	}
}

type depositConfirmWireRequest struct {
	DepositId string `json:"depositId"`
}

// buildDepositConfirmHandler stands in for the real bank's
// webhook/callback firing once the money has actually cleared — the
// ONLY place in this endpoint group real money moves, via
// fundsegregation.SegregationGuard.PostClientMoneyMovement underneath
// internal/depositrail. Every confirmed deposit is also reported to the
// AML monitor, matching how /client-funds/deposit already does it.
func buildDepositConfirmHandler(depositRailInstance *depositrail.SimulatedDepositRail, amlMonitor *amlmonitoring.Monitor) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest depositConfirmWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed deposit confirm payload", http.StatusBadRequest)
			return
		}

		now := time.Now()
		deposit, confirmError := depositRailInstance.ConfirmDeposit(wireRequest.DepositId, now)
		if confirmError != nil {
			respondWithLedgerJson(responseWriter, http.StatusBadRequest, depositWireResponse{
				DepositId:    wireRequest.DepositId,
				ErrorMessage: confirmError.Error(),
			})
			return
		}

		// AML monitoring (FEATURES.md §1): every real, successfully-
		// confirmed deposit is reported for pattern analysis, same as
		// /client-funds/deposit.
		amlMonitor.RecordTransaction(deposit.AccountIdentifier, deposit.AmountInMinorUnits, now)

		respondWithLedgerJson(responseWriter, http.StatusOK, wireResponseFromDeposit(deposit))
	}
}

// buildDepositListHandler is deposit history for one account, any
// status, chronological.
func buildDepositListHandler(depositRailInstance *depositrail.SimulatedDepositRail) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}

		deposits := depositRailInstance.DepositsForAccount(accountIdentifier)
		wireResponses := make([]depositWireResponse, 0, len(deposits))
		for _, deposit := range deposits {
			wireResponses = append(wireResponses, wireResponseFromDeposit(deposit))
		}
		respondWithLedgerJson(responseWriter, http.StatusOK, wireResponses)
	}
}

// ---------------------------------------------------------------------
// Simulated eNACH/standing-instruction SIP mandates (FEATURES.md §2) —
// see internal/paymentmandate's package doc: NOT real eNACH, no actual
// bank mandate registration happens anywhere below. Only
// /payment-mandates/sweep-due moves real money (a debit per due
// mandate).
// ---------------------------------------------------------------------

type paymentMandateWireResponse struct {
	MandateId            string `json:"mandateId,omitempty"`
	AccountIdentifier    string `json:"accountIdentifier,omitempty"`
	AmountInMinorUnits   int64  `json:"amountInMinorUnits,omitempty"`
	Frequency            string `json:"frequency,omitempty"`
	NextDebitDate        string `json:"nextDebitDate,omitempty"`
	Status               string `json:"status,omitempty"`
	SuccessfulSweepCount int    `json:"successfulSweepCount,omitempty"`
	ErrorMessage         string `json:"errorMessage,omitempty"`
}

func wireResponseFromPaymentMandate(mandate *paymentmandate.PaymentMandate) paymentMandateWireResponse {
	return paymentMandateWireResponse{
		MandateId:            mandate.MandateId,
		AccountIdentifier:    mandate.AccountIdentifier,
		AmountInMinorUnits:   mandate.AmountInMinorUnits,
		Frequency:            string(mandate.Frequency),
		NextDebitDate:        mandate.NextDebitDate.Format(time.RFC3339),
		Status:               string(mandate.Status),
		SuccessfulSweepCount: mandate.SuccessfulSweepCount,
	}
}

type paymentMandateRegisterWireRequest struct {
	AccountIdentifier  string `json:"accountIdentifier"`
	AmountInMinorUnits int64  `json:"amountInMinorUnits"`
	Frequency          string `json:"frequency"`
	NextDebitDate      string `json:"nextDebitDate"` // RFC3339; empty = now
}

// buildPaymentMandateRegisterHandler registers a new SIP standing
// instruction. This does NOT register anything with a real bank — see
// internal/paymentmandate's package doc.
func buildPaymentMandateRegisterHandler(registry *paymentmandate.PaymentMandateRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest paymentMandateRegisterWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed payment mandate register payload", http.StatusBadRequest)
			return
		}

		now := time.Now()
		nextDebitDate := now
		if wireRequest.NextDebitDate != "" {
			parsedDate, parseError := time.Parse(time.RFC3339, wireRequest.NextDebitDate)
			if parseError != nil {
				respondWithLedgerJson(responseWriter, http.StatusBadRequest, paymentMandateWireResponse{
					ErrorMessage: "nextDebitDate must be RFC3339 formatted",
				})
				return
			}
			nextDebitDate = parsedDate
		}

		mandate, registerError := registry.RegisterMandate(
			wireRequest.AccountIdentifier,
			wireRequest.AmountInMinorUnits,
			paymentmandate.MandateFrequency(wireRequest.Frequency),
			nextDebitDate,
			now,
		)
		if registerError != nil {
			respondWithLedgerJson(responseWriter, http.StatusBadRequest, paymentMandateWireResponse{ErrorMessage: registerError.Error()})
			return
		}

		respondWithLedgerJson(responseWriter, http.StatusOK, wireResponseFromPaymentMandate(mandate))
	}
}

type paymentMandateIdWireRequest struct {
	MandateId string `json:"mandateId"`
}

// buildPaymentMandatePauseHandler suspends an ACTIVE mandate.
func buildPaymentMandatePauseHandler(registry *paymentmandate.PaymentMandateRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest paymentMandateIdWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed payment mandate pause payload", http.StatusBadRequest)
			return
		}

		mandate, pauseError := registry.PauseMandate(wireRequest.MandateId)
		if pauseError != nil {
			respondWithLedgerJson(responseWriter, http.StatusBadRequest, paymentMandateWireResponse{
				MandateId:    wireRequest.MandateId,
				ErrorMessage: pauseError.Error(),
			})
			return
		}

		respondWithLedgerJson(responseWriter, http.StatusOK, wireResponseFromPaymentMandate(mandate))
	}
}

// buildPaymentMandateResumeHandler reactivates a PAUSED mandate.
func buildPaymentMandateResumeHandler(registry *paymentmandate.PaymentMandateRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest paymentMandateIdWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed payment mandate resume payload", http.StatusBadRequest)
			return
		}

		mandate, resumeError := registry.ResumeMandate(wireRequest.MandateId)
		if resumeError != nil {
			respondWithLedgerJson(responseWriter, http.StatusBadRequest, paymentMandateWireResponse{
				MandateId:    wireRequest.MandateId,
				ErrorMessage: resumeError.Error(),
			})
			return
		}

		respondWithLedgerJson(responseWriter, http.StatusOK, wireResponseFromPaymentMandate(mandate))
	}
}

// buildPaymentMandateCancelHandler permanently terminates a mandate.
func buildPaymentMandateCancelHandler(registry *paymentmandate.PaymentMandateRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest paymentMandateIdWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed payment mandate cancel payload", http.StatusBadRequest)
			return
		}

		mandate, cancelError := registry.CancelMandate(wireRequest.MandateId)
		if cancelError != nil {
			respondWithLedgerJson(responseWriter, http.StatusBadRequest, paymentMandateWireResponse{
				MandateId:    wireRequest.MandateId,
				ErrorMessage: cancelError.Error(),
			})
			return
		}

		respondWithLedgerJson(responseWriter, http.StatusOK, wireResponseFromPaymentMandate(mandate))
	}
}

type paymentMandateSweepDueWireResponse struct {
	MandateId string `json:"mandateId"`
	WasPosted bool   `json:"wasPosted"`
	Error     string `json:"error,omitempty"`
}

// buildPaymentMandateSweepDueHandler is the sweep an operator/scheduled
// job calls to actually execute every mandate whose NextDebitDate has
// arrived — see internal/paymentmandate's package doc: this is NOT run
// automatically on a timer in this skeleton, matching
// /withdrawals/process-due's same caveat.
func buildPaymentMandateSweepDueHandler(registry *paymentmandate.PaymentMandateRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		results := registry.SweepDueMandates(time.Now())
		wireResults := make([]paymentMandateSweepDueWireResponse, 0, len(results))
		for _, result := range results {
			wireResults = append(wireResults, paymentMandateSweepDueWireResponse{
				MandateId: result.MandateId,
				WasPosted: result.WasPosted,
				Error:     result.Error,
			})
		}
		respondWithLedgerJson(responseWriter, http.StatusOK, wireResults)
	}
}

// buildPaymentMandateListHandler is mandate history for one account, any
// status, chronological.
func buildPaymentMandateListHandler(registry *paymentmandate.PaymentMandateRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}

		mandates := registry.MandatesForAccount(accountIdentifier)
		wireResponses := make([]paymentMandateWireResponse, 0, len(mandates))
		for _, mandate := range mandates {
			wireResponses = append(wireResponses, wireResponseFromPaymentMandate(mandate))
		}
		respondWithLedgerJson(responseWriter, http.StatusOK, wireResponses)
	}
}

// ---------------------------------------------------------------------
// Multi-currency wallet (FEATURES.md §2) — see
// internal/multicurrencywallet's package doc: a NEW layer on top of
// internal/doubleentry, not a change to it. The FX rate table used to
// build multiCurrencyWalletRegistry above is STATIC and ILLUSTRATIVE,
// NOT a live market feed.
// ---------------------------------------------------------------------

type walletMutationWireRequest struct {
	AccountIdentifier  string `json:"accountId"`
	CurrencyCode       string `json:"currencyCode"`
	AmountInMinorUnits int64  `json:"amountInMinorUnits"`
}

type walletMutationWireResponse struct {
	WasApplied   bool   `json:"wasApplied"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

// buildWalletDepositHandler is a real, currency-scoped deposit — see
// multicurrencywallet.DepositIntoCurrencyWallet.
func buildWalletDepositHandler(registry *multicurrencywallet.MultiCurrencyWalletRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest walletMutationWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed wallet deposit payload", http.StatusBadRequest)
			return
		}

		depositError := registry.DepositIntoCurrencyWallet(
			wireRequest.AccountIdentifier,
			multicurrencywallet.CurrencyCode(wireRequest.CurrencyCode),
			wireRequest.AmountInMinorUnits,
		)
		if depositError != nil {
			respondWithLedgerJson(responseWriter, http.StatusBadRequest, walletMutationWireResponse{ErrorMessage: depositError.Error()})
			return
		}
		respondWithLedgerJson(responseWriter, http.StatusOK, walletMutationWireResponse{WasApplied: true})
	}
}

// buildWalletWithdrawHandler is a real, currency-scoped withdrawal —
// rejected outright if it exceeds THAT currency's own balance, even if
// another currency wallet for the same account has plenty. See
// multicurrencywallet.WithdrawFromCurrencyWallet.
func buildWalletWithdrawHandler(registry *multicurrencywallet.MultiCurrencyWalletRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest walletMutationWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed wallet withdraw payload", http.StatusBadRequest)
			return
		}

		withdrawError := registry.WithdrawFromCurrencyWallet(
			wireRequest.AccountIdentifier,
			multicurrencywallet.CurrencyCode(wireRequest.CurrencyCode),
			wireRequest.AmountInMinorUnits,
		)
		if withdrawError != nil {
			respondWithLedgerJson(responseWriter, http.StatusBadRequest, walletMutationWireResponse{ErrorMessage: withdrawError.Error()})
			return
		}
		respondWithLedgerJson(responseWriter, http.StatusOK, walletMutationWireResponse{WasApplied: true})
	}
}

type walletConvertWireRequest struct {
	AccountIdentifier              string `json:"accountId"`
	FromCurrencyCode               string `json:"fromCurrencyCode"`
	ToCurrencyCode                 string `json:"toCurrencyCode"`
	AmountInFromCurrencyMinorUnits int64  `json:"amountInFromCurrencyMinorUnits"`
}

type walletConvertWireResponse struct {
	WasApplied                              bool    `json:"wasApplied"`
	FromCurrencyCode                        string  `json:"fromCurrencyCode,omitempty"`
	ToCurrencyCode                          string  `json:"toCurrencyCode,omitempty"`
	AmountDebitedFromSourceInMinorUnits     int64   `json:"amountDebitedFromSourceInMinorUnits,omitempty"`
	AmountCreditedToDestinationInMinorUnits int64   `json:"amountCreditedToDestinationInMinorUnits,omitempty"`
	RateApplied                             float64 `json:"rateApplied,omitempty"`
	ErrorMessage                            string  `json:"errorMessage,omitempty"`
}

// buildWalletConvertHandler converts between two currency wallets of the
// SAME account using the STATIC/ILLUSTRATIVE FX rate table — see
// multicurrencywallet.ConvertBetweenCurrencyWallets.
func buildWalletConvertHandler(registry *multicurrencywallet.MultiCurrencyWalletRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest walletConvertWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed wallet convert payload", http.StatusBadRequest)
			return
		}

		result, convertError := registry.ConvertBetweenCurrencyWallets(
			wireRequest.AccountIdentifier,
			multicurrencywallet.CurrencyCode(wireRequest.FromCurrencyCode),
			multicurrencywallet.CurrencyCode(wireRequest.ToCurrencyCode),
			wireRequest.AmountInFromCurrencyMinorUnits,
		)
		if convertError != nil {
			respondWithLedgerJson(responseWriter, http.StatusBadRequest, walletConvertWireResponse{ErrorMessage: convertError.Error()})
			return
		}

		respondWithLedgerJson(responseWriter, http.StatusOK, walletConvertWireResponse{
			WasApplied:                              true,
			FromCurrencyCode:                        string(result.FromCurrencyCode),
			ToCurrencyCode:                          string(result.ToCurrencyCode),
			AmountDebitedFromSourceInMinorUnits:     result.AmountDebitedFromSourceInMinorUnits,
			AmountCreditedToDestinationInMinorUnits: result.AmountCreditedToDestinationInMinorUnits,
			RateApplied:                             result.RateApplied,
		})
	}
}

type walletBalanceWireFormat struct {
	CurrencyCode        string `json:"currencyCode"`
	BalanceInMinorUnits int64  `json:"balanceInMinorUnits"`
}

// buildWalletListHandler is GET /wallets?accountId=... — every currency
// wallet ever opened for that account, each a real doubleentry balance.
func buildWalletListHandler(registry *multicurrencywallet.MultiCurrencyWalletRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}

		balances := registry.GetWalletBalancesForAccount(accountIdentifier)
		wireBalances := make([]walletBalanceWireFormat, 0, len(balances))
		for _, balance := range balances {
			wireBalances = append(wireBalances, walletBalanceWireFormat{
				CurrencyCode:        string(balance.CurrencyCode),
				BalanceInMinorUnits: balance.BalanceInMinorUnits,
			})
		}
		respondWithLedgerJson(responseWriter, http.StatusOK, wireBalances)
	}
}

// ---------------------------------------------------------------------
// Backup / restore (FEATURES.md §13, "[P1] Automated backups + tested
// restore procedure for ledger DB") — see
// internal/doubleentry/snapshotRestore.go's package doc: with no real
// Postgres in this environment, a full in-memory JSON snapshot of every
// account balance and every posted journal entry IS the backup
// mechanism, not a supplementary one. scripts/backupLedgerSnapshot.sh
// calls GET /admin/snapshot on a schedule/on demand and writes the
// result to services/ledger/backups/ as a timestamped file;
// POST /admin/restore is how an operator (or the restore-drill test)
// loads one of those files back in.
// ---------------------------------------------------------------------

// buildAdminSnapshotHandler is GET /admin/snapshot: returns the COMPLETE
// current in-memory ledger state (every account balance, every posted
// journal entry, in post order) as JSON — exactly
// doubleentry.LedgerBookSnapshot's shape, so the response body can be
// saved to disk and later POSTed back to /admin/restore unmodified.
func buildAdminSnapshotHandler(ledgerBook *doubleentry.InMemoryDoubleEntryLedgerBook) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(responseWriter, "only GET is supported", http.StatusMethodNotAllowed)
			return
		}

		snapshot := ledgerBook.CaptureSnapshot()
		respondWithLedgerJson(responseWriter, http.StatusOK, snapshot)
	}
}

type adminRestoreWireResponse struct {
	WasRestored          bool   `json:"wasRestored"`
	RestoredAccountCount int    `json:"restoredAccountCount,omitempty"`
	RestoredEntryCount   int    `json:"restoredEntryCount,omitempty"`
	ErrorMessage         string `json:"errorMessage,omitempty"`
}

// buildAdminRestoreHandler is POST /admin/restore: accepts a
// doubleentry.LedgerBookSnapshot JSON body (the exact shape
// GET /admin/snapshot returns) and atomically replaces THIS running
// ledger process's entire in-memory state with it — not a new process,
// not a merge. Safe under concurrent requests: the actual swap happens
// inside doubleentry.RestoreFromSnapshot, under the same mutex every
// other ledger mutation goes through.
func buildAdminRestoreHandler(ledgerBook *doubleentry.InMemoryDoubleEntryLedgerBook) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var snapshot doubleentry.LedgerBookSnapshot
		if decodeError := json.NewDecoder(request.Body).Decode(&snapshot); decodeError != nil {
			respondWithLedgerJson(responseWriter, http.StatusBadRequest, adminRestoreWireResponse{
				ErrorMessage: fmt.Sprintf("malformed snapshot payload: %v", decodeError),
			})
			return
		}

		ledgerBook.RestoreFromSnapshot(snapshot)
		log.Printf(
			"ledger state restored from snapshot: %d accounts, %d journal entries",
			len(snapshot.AccountBalances), len(snapshot.PostedJournalEntriesInPostOrder),
		)

		respondWithLedgerJson(responseWriter, http.StatusOK, adminRestoreWireResponse{
			WasRestored:          true,
			RestoredAccountCount: len(snapshot.AccountBalances),
			RestoredEntryCount:   len(snapshot.PostedJournalEntriesInPostOrder),
		})
	}
}
