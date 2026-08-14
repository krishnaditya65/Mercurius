// Mercurius / reporting
//
// A real regulatory/tax reporting service — FEATURES.md §1's
// "Regulatory reporting: contract notes, ledger statements, tax P&L
// (STCG/LTCG), Annual Information Statement reconciliation" and §21's
// "One-click capital gains statement export", built together since both
// are the same underlying tax/capital-gains reporting domain.
//
// reporting never edits or imports any other service's Go code. Every
// number in every report here is computed from real data pulled, at
// request time, over plain HTTP from oms-gateway's (:8081) and
// ledger's (:8082) genuine, already-shipped APIs — see
// internal/omsgatewayclient and internal/ledgerclient's package docs for
// exactly which real endpoints are used and the honest gaps in what
// those services currently expose (most notably: ledger has no HTTP
// endpoint for historical journal-entry data, and oms-gateway's fill
// history is only reachable as a free-text audit-trail message, not a
// structured fills endpoint — both documented in detail where they
// matter).
//
// The one deliberately NOT-real data source in this service is the
// government Annual Information Statement itself: no such feed exists
// or could exist here without real government API access, so
// internal/aisreconciliation's mock AIS builder is loudly labeled
// illustrative — see that package's doc comment. The reconciliation
// LOGIC that compares it against the platform's real computed summary
// is fully real and fully tested.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"mercurius/reporting/internal/aisreconciliation"
	"mercurius/reporting/internal/capitalgains"
	"mercurius/reporting/internal/capitalgainsexport"
	"mercurius/reporting/internal/contractnotegenerator"
	"mercurius/reporting/internal/filltrail"
	"mercurius/reporting/internal/httplogging"
	"mercurius/reporting/internal/ledgerclient"
	"mercurius/reporting/internal/ledgerstatement"
	"mercurius/reporting/internal/omsgatewayclient"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	omsGatewayBaseUrl := os.Getenv("OMS_GATEWAY_BASE_URL")
	if omsGatewayBaseUrl == "" {
		omsGatewayBaseUrl = "http://127.0.0.1:8081"
	}
	omsGatewayClient := omsgatewayclient.NewOmsGatewayClient(omsGatewayBaseUrl)

	ledgerBaseUrl := os.Getenv("LEDGER_BASE_URL")
	if ledgerBaseUrl == "" {
		ledgerBaseUrl = "http://127.0.0.1:8082"
	}
	ledgerClient := ledgerclient.NewLedgerClient(ledgerBaseUrl)

	httpRequestMultiplexer := http.NewServeMux()
	httpRequestMultiplexer.HandleFunc("/health", func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{"status":"ok","service":"reporting"}`))
	})
	httpRequestMultiplexer.HandleFunc("/contract-notes/generate", buildGenerateContractNoteHandler(omsGatewayClient))
	httpRequestMultiplexer.HandleFunc("/ledger-statements/generate", buildGenerateLedgerStatementHandler(omsGatewayClient, ledgerClient))
	httpRequestMultiplexer.HandleFunc("/capital-gains/compute", buildComputeCapitalGainsHandler(omsGatewayClient))
	httpRequestMultiplexer.HandleFunc("/capital-gains/export", buildExportCapitalGainsCsvHandler(omsGatewayClient))
	httpRequestMultiplexer.HandleFunc("/ais-reconciliation/run", buildRunAisReconciliationHandler(omsGatewayClient))

	listenAddress := os.Getenv("REPORTING_LISTEN_ADDRESS")
	if listenAddress == "" {
		listenAddress = ":8090"
	}
	log.Printf("reporting listening on %s — oms-gateway at %s, ledger at %s\n", listenAddress, omsGatewayBaseUrl, ledgerBaseUrl)
	if serverStartupError := http.ListenAndServe(listenAddress, withPermissiveCorsForDevelopment(httplogging.WithRequestLogging(httpRequestMultiplexer))); serverStartupError != nil {
		log.Fatalf("reporting failed to start: %v", serverStartupError)
	}
}

// withPermissiveCorsForDevelopment mirrors oms-gateway's own
// development-mode CORS wrapper, for the same reason (apps/web calling
// this API directly from a browser during local dev). Same TODO as
// oms-gateway's version: wide-open `*` is fine for a no-auth demo
// skeleton only.
func withPermissiveCorsForDevelopment(nextHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Access-Control-Allow-Origin", "*")
		responseWriter.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		responseWriter.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if request.Method == http.MethodOptions {
			responseWriter.WriteHeader(http.StatusNoContent)
			return
		}
		nextHandler.ServeHTTP(responseWriter, request)
	})
}

func respondWithJson(responseWriter http.ResponseWriter, statusCode int, body any) {
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(statusCode)
	_ = json.NewEncoder(responseWriter).Encode(body)
}

func respondWithError(responseWriter http.ResponseWriter, statusCode int, errorMessage string) {
	respondWithJson(responseWriter, statusCode, map[string]string{"errorMessage": errorMessage})
}

// fetchFillsForAccount fetches oms-gateway's FULL, unfiltered real audit
// trail (every account) and parses out accountIdentifier's fills — see
// filltrail's package doc for why the unfiltered trail is required: a
// per-account accountId-filtered query misses maker-side fills.
func fetchFillsForAccount(omsGatewayClient *omsgatewayclient.OmsGatewayClient, accountIdentifier string) ([]filltrail.Fill, []error, error) {
	entries, fetchError := omsGatewayClient.FetchAllAuditTrailEntries()
	if fetchError != nil {
		return nil, nil, fetchError
	}
	fills, parseErrors := filltrail.ParseFillsFromAllAuditTrailEntries(accountIdentifier, entries)
	return fills, parseErrors, nil
}

// --- 1. Contract notes -----------------------------------------------

func buildGenerateContractNoteHandler(omsGatewayClient *omsgatewayclient.OmsGatewayClient) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		dateParam := request.URL.Query().Get("date")
		if accountIdentifier == "" || dateParam == "" {
			respondWithError(responseWriter, http.StatusBadRequest, "accountId and date (YYYY-MM-DD) query parameters are required")
			return
		}
		tradeDate, parseError := time.Parse("2006-01-02", dateParam)
		if parseError != nil {
			respondWithError(responseWriter, http.StatusBadRequest, fmt.Sprintf("invalid date %q, expected YYYY-MM-DD", dateParam))
			return
		}
		isIntradayNotDelivery := request.URL.Query().Get("isIntradayNotDelivery") == "true"

		fills, parseErrors, fetchError := fetchFillsForAccount(omsGatewayClient, accountIdentifier)
		if fetchError != nil {
			respondWithError(responseWriter, http.StatusBadGateway, fmt.Sprintf("could not fetch fills from oms-gateway: %v", fetchError))
			return
		}

		note := contractnotegenerator.GenerateForAccountAndDate(accountIdentifier, tradeDate, fills, isIntradayNotDelivery, omsGatewayClient, time.Now())
		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"contractNote":                       note,
			"unparseableAuditTrailEntryWarnings": errorsToStrings(parseErrors),
		})
	}
}

// --- 2. Ledger statements ---------------------------------------------

func buildGenerateLedgerStatementHandler(omsGatewayClient *omsgatewayclient.OmsGatewayClient, ledgerClient *ledgerclient.LedgerClient) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		startDateParam := request.URL.Query().Get("startDate")
		endDateParam := request.URL.Query().Get("endDate")
		if accountIdentifier == "" || startDateParam == "" || endDateParam == "" {
			respondWithError(responseWriter, http.StatusBadRequest, "accountId, startDate and endDate (YYYY-MM-DD) query parameters are required")
			return
		}
		startDate, startParseError := time.Parse("2006-01-02", startDateParam)
		endDate, endParseError := time.Parse("2006-01-02", endDateParam)
		if startParseError != nil || endParseError != nil {
			respondWithError(responseWriter, http.StatusBadRequest, "invalid startDate/endDate, expected YYYY-MM-DD")
			return
		}
		endDate = endDate.Add(24*time.Hour - time.Nanosecond) // inclusive of the whole end day

		deposits, depositsError := ledgerClient.FetchDepositsForAccount(accountIdentifier)
		if depositsError != nil {
			respondWithError(responseWriter, http.StatusBadGateway, fmt.Sprintf("could not fetch deposits from ledger: %v", depositsError))
			return
		}
		withdrawals, withdrawalsError := ledgerClient.FetchWithdrawalsForAccount(accountIdentifier)
		if withdrawalsError != nil {
			respondWithError(responseWriter, http.StatusBadGateway, fmt.Sprintf("could not fetch withdrawals from ledger: %v", withdrawalsError))
			return
		}
		currentBalance, _, balanceError := ledgerClient.FetchAccountBalance(accountIdentifier)
		if balanceError != nil {
			respondWithError(responseWriter, http.StatusBadGateway, fmt.Sprintf("could not fetch current balance from ledger: %v", balanceError))
			return
		}
		processedActions, actionsError := omsGatewayClient.FetchProcessedCorporateActionsForAccount(accountIdentifier)
		if actionsError != nil {
			respondWithError(responseWriter, http.StatusBadGateway, fmt.Sprintf("could not fetch processed corporate actions from oms-gateway: %v", actionsError))
			return
		}
		fills, parseErrors, fillsError := fetchFillsForAccount(omsGatewayClient, accountIdentifier)
		if fillsError != nil {
			respondWithError(responseWriter, http.StatusBadGateway, fmt.Sprintf("could not fetch fills from oms-gateway: %v", fillsError))
			return
		}

		depositMovements, depositMovementsError := ledgerstatement.MovementsFromConfirmedDeposits(deposits)
		if depositMovementsError != nil {
			respondWithError(responseWriter, http.StatusInternalServerError, depositMovementsError.Error())
			return
		}
		withdrawalMovements, withdrawalMovementsError := ledgerstatement.MovementsFromCompletedWithdrawals(withdrawals)
		if withdrawalMovementsError != nil {
			respondWithError(responseWriter, http.StatusInternalServerError, withdrawalMovementsError.Error())
			return
		}
		dividendMovements := ledgerstatement.MovementsFromDividendCredits(processedActions)

		signedNetAmounts := make([]int64, len(fills))
		for i, fill := range fills {
			orderSideIsBuyNotSell := fill.Side == filltrail.SideBuy
			charges, _ := omsGatewayClient.EstimateCharges(orderSideIsBuyNotSell, fill.PriceInMinorUnits, fill.Quantity, false)
			if orderSideIsBuyNotSell {
				signedNetAmounts[i] = -charges.NetAmountInMinorUnits
			} else {
				signedNetAmounts[i] = charges.NetAmountInMinorUnits
			}
		}
		tradeMovements := ledgerstatement.MovementsFromTradeSettlements(fills, signedNetAmounts)

		var allMovements []ledgerstatement.Movement
		allMovements = append(allMovements, depositMovements...)
		allMovements = append(allMovements, withdrawalMovements...)
		allMovements = append(allMovements, dividendMovements...)
		allMovements = append(allMovements, tradeMovements...)

		statement := ledgerstatement.BuildStatement(accountIdentifier, startDate, endDate, allMovements, currentBalance)
		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"statement":                          statement,
			"unparseableAuditTrailEntryWarnings": errorsToStrings(parseErrors),
		})
	}
}

// --- 3. Tax P&L (STCG/LTCG) --------------------------------------------

func computeCapitalGainsSummary(omsGatewayClient *omsgatewayclient.OmsGatewayClient, accountIdentifier string, financialYearLabel string) (capitalgains.Summary, []error, error) {
	fyStart, fyEnd, fyParseError := capitalgains.IndianFinancialYearRange(financialYearLabel)
	if fyParseError != nil {
		return capitalgains.Summary{}, nil, fyParseError
	}

	fills, parseErrors, fetchError := fetchFillsForAccount(omsGatewayClient, accountIdentifier)
	if fetchError != nil {
		return capitalgains.Summary{}, nil, fetchError
	}

	realizedGains, matchError := capitalgains.ComputeFifoRealizedGains(fills)
	if matchError != nil {
		// Still return whatever was matched before the error, alongside
		// it, rather than discarding real partial results.
		summary := capitalgains.AggregateForFinancialYear(accountIdentifier, financialYearLabel, realizedGains, fyStart, fyEnd)
		return summary, parseErrors, matchError
	}

	summary := capitalgains.AggregateForFinancialYear(accountIdentifier, financialYearLabel, realizedGains, fyStart, fyEnd)
	return summary, parseErrors, nil
}

func buildComputeCapitalGainsHandler(omsGatewayClient *omsgatewayclient.OmsGatewayClient) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		financialYearLabel := request.URL.Query().Get("financialYear")
		if accountIdentifier == "" || financialYearLabel == "" {
			respondWithError(responseWriter, http.StatusBadRequest, "accountId and financialYear (e.g. 2024-25) query parameters are required")
			return
		}

		summary, parseErrors, computeError := computeCapitalGainsSummary(omsGatewayClient, accountIdentifier, financialYearLabel)
		if computeError != nil {
			respondWithJson(responseWriter, http.StatusOK, map[string]any{
				"summary":                            summary,
				"unparseableAuditTrailEntryWarnings": errorsToStrings(parseErrors),
				"fifoMatchingWarning":                computeError.Error(),
			})
			return
		}

		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"summary":                            summary,
			"unparseableAuditTrailEntryWarnings": errorsToStrings(parseErrors),
		})
	}
}

// --- 5. Capital gains statement CSV export -----------------------------

func buildExportCapitalGainsCsvHandler(omsGatewayClient *omsgatewayclient.OmsGatewayClient) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		financialYearLabel := request.URL.Query().Get("financialYear")
		if accountIdentifier == "" || financialYearLabel == "" {
			respondWithError(responseWriter, http.StatusBadRequest, "accountId and financialYear (e.g. 2024-25) query parameters are required")
			return
		}

		summary, _, computeError := computeCapitalGainsSummary(omsGatewayClient, accountIdentifier, financialYearLabel)
		if computeError != nil {
			respondWithError(responseWriter, http.StatusBadGateway, computeError.Error())
			return
		}

		csvBytes, csvError := capitalgainsexport.WriteCsv(summary)
		if csvError != nil {
			respondWithError(responseWriter, http.StatusInternalServerError, csvError.Error())
			return
		}

		filename := capitalgainsexport.SuggestedFilename(accountIdentifier, financialYearLabel, time.Now())
		responseWriter.Header().Set("Content-Type", "text/csv")
		responseWriter.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = responseWriter.Write(csvBytes)
	}
}

// --- 4. AIS reconciliation ----------------------------------------------

func buildRunAisReconciliationHandler(omsGatewayClient *omsgatewayclient.OmsGatewayClient) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		financialYearLabel := request.URL.Query().Get("financialYear")
		if accountIdentifier == "" || financialYearLabel == "" {
			respondWithError(responseWriter, http.StatusBadRequest, "accountId and financialYear (e.g. 2024-25) query parameters are required")
			return
		}

		fyStart, fyEnd, fyParseError := capitalgains.IndianFinancialYearRange(financialYearLabel)
		if fyParseError != nil {
			respondWithError(responseWriter, http.StatusBadRequest, fyParseError.Error())
			return
		}

		summary, _, computeError := computeCapitalGainsSummary(omsGatewayClient, accountIdentifier, financialYearLabel)
		if computeError != nil {
			respondWithError(responseWriter, http.StatusBadGateway, computeError.Error())
			return
		}

		processedActions, actionsError := omsGatewayClient.FetchProcessedCorporateActionsForAccount(accountIdentifier)
		if actionsError != nil {
			respondWithError(responseWriter, http.StatusBadGateway, fmt.Sprintf("could not fetch processed corporate actions from oms-gateway: %v", actionsError))
			return
		}
		dividendIncomeByInstrument := map[string]int64{}
		for _, action := range processedActions {
			if action.ActionType != "CASH_DIVIDEND" {
				continue
			}
			if action.ProcessedAtTime.Before(fyStart) || action.ProcessedAtTime.After(fyEnd) {
				continue
			}
			dividendIncomeByInstrument[action.InstrumentSymbol] += action.CashCreditedInMinorUnits
		}

		platformRecord := aisreconciliation.BuildPlatformAisRecord(accountIdentifier, financialYearLabel, summary.RealizedGains, dividendIncomeByInstrument)

		// Illustrative, clearly-labeled simulated AIS export — see
		// aisreconciliation's package doc. Perturbs one entry (if any
		// exist) so a real end-to-end run has something genuine to
		// reconcile, rather than trivially matching itself every time.
		var mockDiscrepancies []aisreconciliation.MockDiscrepancy
		if len(platformRecord.Entries) > 0 {
			firstEntry := platformRecord.Entries[0]
			mockDiscrepancies = append(mockDiscrepancies, aisreconciliation.MockDiscrepancy{
				PerturbCategoryAndInstrument: [2]string{firstEntry.Category, firstEntry.InstrumentSymbol},
				PerturbByMinorUnits:          100, // illustrative ₹1.00 reporting-lag discrepancy
			})
		}
		mockAisRecord := aisreconciliation.BuildIllustrativeMockAisRecord(platformRecord, mockDiscrepancies...)

		report := aisreconciliation.Reconcile(platformRecord, mockAisRecord)
		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"platformRecord":       platformRecord,
			"aisRecord":            mockAisRecord,
			"reconciliationReport": report,
		})
	}
}

func errorsToStrings(errs []error) []string {
	strs := make([]string, 0, len(errs))
	for _, e := range errs {
		strs = append(strs, e.Error())
	}
	return strs
}
