// Mercurius / oms-gateway
//
// Tier 1 component per ARCHITECTURE.md §4. A real HTTP order-submission
// endpoint backed by: a real in-memory pre-trade risk check (seeded from
// the ledger at startup, not hardcoded), real sequence-number assignment,
// a real network hand-off to matching-engine over TCP+JSON, and — as of
// this build — real settlement postings back to the ledger on every fill.
// No FIX/WS session handling, no backpressure/throttling per client
// session yet.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"mercurius/omsgateway/internal/amoqueue"
	"mercurius/omsgateway/internal/audittrail"
	"mercurius/omsgateway/internal/backofficeclient"
	"mercurius/omsgateway/internal/chargescalculator"
	"mercurius/omsgateway/internal/httplogging"
	"mercurius/omsgateway/internal/idempotency"
	"mercurius/omsgateway/internal/kycclient"
	"mercurius/omsgateway/internal/ledgerclient"
	"mercurius/omsgateway/internal/marketsession"
	"mercurius/omsgateway/internal/matchingengineclient"
	"mercurius/omsgateway/internal/metrics"
	"mercurius/omsgateway/internal/orders"
	"mercurius/omsgateway/internal/positions"
	"mercurius/omsgateway/internal/riskengine"
	"mercurius/omsgateway/internal/sequencing"
)

// demoTrackedAccountIdentifiers is the set of accounts this skeleton
// knows to sync from the ledger at startup. TODO(real build): this
// entire list disappears once accounts are created dynamically through a
// real KYC-driven onboarding flow instead of being seeded on both sides.
var demoTrackedAccountIdentifiers = []string{"acct-001", "acct-002"}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	ledgerBaseUrl := os.Getenv("LEDGER_BASE_URL")
	if ledgerBaseUrl == "" {
		ledgerBaseUrl = "http://127.0.0.1:8082"
	}
	ledgerClient := ledgerclient.NewLedgerClient(ledgerBaseUrl)

	kycOnboardingBaseUrl := os.Getenv("KYC_ONBOARDING_BASE_URL")
	if kycOnboardingBaseUrl == "" {
		kycOnboardingBaseUrl = "http://127.0.0.1:8083"
	}
	kycClient := kycclient.NewKycClient(kycOnboardingBaseUrl)

	backofficeBaseUrl := os.Getenv("BACKOFFICE_BASE_URL")
	if backofficeBaseUrl == "" {
		backofficeBaseUrl = "http://127.0.0.1:8084"
	}
	backofficeClient := backofficeclient.NewBackofficeClient(backofficeBaseUrl)

	positionBook := positions.NewPositionBook()
	idempotencyStore := idempotency.NewIdempotencyStore()
	marketSession := marketsession.NewMarketSessionState()
	afterMarketOrderQueue := amoqueue.NewAfterMarketOrderQueue()
	auditTrail := audittrail.NewAuditTrail()
	metricsRegistry := metrics.NewRegistry()

	preTradeRiskEngine := riskengine.NewPreTradeRiskEngineWithSeedBalances(map[string]int64{})
	syncRiskEngineBalancesFromLedger(preTradeRiskEngine, ledgerClient)

	globalSequenceNumberAllocator := sequencing.NewGlobalSequenceNumberAllocatorStartingAtOne()

	matchingEngineTcpAddress := os.Getenv("MATCHING_ENGINE_TCP_ADDRESS")
	if matchingEngineTcpAddress == "" {
		matchingEngineTcpAddress = "127.0.0.1:9101"
	}
	matchingEngineClient := matchingengineclient.NewMatchingEngineClient(matchingEngineTcpAddress)

	orderSubmissionDeps := orderSubmissionDependencies{
		preTradeRiskEngine:            preTradeRiskEngine,
		globalSequenceNumberAllocator: globalSequenceNumberAllocator,
		matchingEngineClient:          matchingEngineClient,
		ledgerClient:                  ledgerClient,
		kycClient:                     kycClient,
		backofficeClient:              backofficeClient,
		positionBook:                  positionBook,
		auditTrail:                    auditTrail,
	}

	httpRequestMultiplexer := http.NewServeMux()
	httpRequestMultiplexer.HandleFunc("/health", healthCheckHandler)
	httpRequestMultiplexer.HandleFunc("/orders/submit", buildSubmitOrderHandler(orderSubmissionDeps, idempotencyStore, marketSession, afterMarketOrderQueue))
	httpRequestMultiplexer.HandleFunc("/positions", buildPositionsHandler(positionBook))
	httpRequestMultiplexer.HandleFunc("/orders/cancel", buildCancelOrderHandler(matchingEngineClient, auditTrail))
	httpRequestMultiplexer.HandleFunc("/orders/cover-submit", buildCoverOrderHandler(orderSubmissionDeps))
	httpRequestMultiplexer.HandleFunc("/orders/status", buildOrderStatusHandler(matchingEngineClient))
	httpRequestMultiplexer.HandleFunc("/market-session/status", buildMarketSessionStatusHandler(marketSession, afterMarketOrderQueue))
	httpRequestMultiplexer.HandleFunc("/market-session/open", buildMarketSessionOpenHandler(marketSession, afterMarketOrderQueue, orderSubmissionDeps))
	httpRequestMultiplexer.HandleFunc("/market-session/close", buildMarketSessionCloseHandler(marketSession, auditTrail))
	httpRequestMultiplexer.HandleFunc("/audit-trail", buildAuditTrailHandler(auditTrail))
	httpRequestMultiplexer.HandleFunc("/orders/estimate-charges", buildEstimateChargesHandler())
	httpRequestMultiplexer.HandleFunc("/metrics", metrics.BuildMetricsHandler(metricsRegistry))

	listenAddress := ":8081"
	log.Printf(
		"oms-gateway listening on %s (CORS wide open — see withPermissiveCorsForDevelopment) — matching-engine at %s, ledger at %s, kyc-onboarding at %s, backoffice at %s\n",
		listenAddress,
		matchingEngineTcpAddress,
		ledgerBaseUrl,
		kycOnboardingBaseUrl,
		backofficeBaseUrl,
	)
	instrumentedHandler := metrics.WithRequestTiming(metricsRegistry, httplogging.WithRequestLogging(httpRequestMultiplexer))
	if serverStartupError := http.ListenAndServe(listenAddress, withPermissiveCorsForDevelopment(instrumentedHandler)); serverStartupError != nil {
		log.Fatalf("oms-gateway failed to start: %v", serverStartupError)
	}
}

// withPermissiveCorsForDevelopment wraps every request with CORS headers
// permissive enough for apps/web (served from a different origin/port in
// dev, e.g. localhost:3000/3100 vs oms-gateway's :8081) to actually call
// this API from a real browser — without it, every fetch() from the
// dashboard fails silently with a CORS error before it even reaches a
// handler.
//
// TODO(real build): `Access-Control-Allow-Origin: *` is fine for a
// no-auth demo skeleton and actively wrong once any real auth (cookies,
// bearer tokens) exists — a real build must echo back a specific
// allow-listed origin instead of `*`, per the standard CORS-with-
// credentials restriction.
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

// syncRiskEngineBalancesFromLedger does a one-shot fetch-and-seed at
// startup. TODO(real build): this needs to become a continuous
// subscription to the ledger's balance-changed events, not a single
// snapshot taken once when the process starts — a balance change made
// through any other path (a deposit, a different OMS instance) would
// never be reflected here otherwise.
func syncRiskEngineBalancesFromLedger(preTradeRiskEngine *riskengine.PreTradeRiskEngine, ledgerClient *ledgerclient.LedgerClient) {
	for _, accountIdentifier := range demoTrackedAccountIdentifiers {
		balance, fetchError := ledgerClient.FetchAccountBalance(accountIdentifier)
		if fetchError != nil {
			log.Printf("startup balance sync failed for %s (ledger may not be running yet): %v", accountIdentifier, fetchError)
			continue
		}
		preTradeRiskEngine.RefreshAccountBalanceFromLedger(accountIdentifier, balance)
		log.Printf("synced %s balance from ledger: %d minor units", accountIdentifier, balance)
	}
}

func healthCheckHandler(responseWriter http.ResponseWriter, request *http.Request) {
	responseWriter.WriteHeader(http.StatusOK)
	_, _ = responseWriter.Write([]byte(`{"status":"ok","service":"oms-gateway"}`))
}

// orderSubmissionDependencies bundles everything processOrderSubmission
// needs, so both buildSubmitOrderHandler and buildCoverOrderHandler can
// share the exact same gate → risk → matching-engine → settlement
// pipeline instead of two handlers drifting apart over time.
type orderSubmissionDependencies struct {
	preTradeRiskEngine            *riskengine.PreTradeRiskEngine
	globalSequenceNumberAllocator *sequencing.GlobalSequenceNumberAllocator
	matchingEngineClient          *matchingengineclient.MatchingEngineClient
	ledgerClient                  *ledgerclient.LedgerClient
	kycClient                     *kycclient.KycClient
	backofficeClient              *backofficeclient.BackofficeClient
	positionBook                  *positions.PositionBook
	auditTrail                    *audittrail.AuditTrail
}

func buildSubmitOrderHandler(
	dependencies orderSubmissionDependencies,
	idempotencyStore *idempotency.IdempotencyStore,
	marketSession *marketsession.MarketSessionState,
	afterMarketOrderQueue *amoqueue.AfterMarketOrderQueue,
) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var incomingOrderSubmissionRequest orders.OrderSubmissionRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&incomingOrderSubmissionRequest); decodeError != nil {
			http.Error(responseWriter, "malformed order submission payload", http.StatusBadRequest)
			return
		}

		// FEATURES.md §3 AMO: an after-market order arriving while the
		// market is closed gets queued instead of processed — no KYC/
		// freeze/risk check happens yet either, since all of that runs
		// again (fresh) when the queue drains at market-open, and running
		// it twice would be wasteful and could see stale state (e.g. a
		// balance that's since changed). Checked BEFORE the idempotency
		// claim below: an AMO's real outcome doesn't exist yet at queue
		// time, so claiming (and later completing) the key with "queued"
		// as if it were the final answer would make a legitimate retry
		// after market-open incorrectly replay "queued" forever instead
		// of the real result. A known, accepted gap: idempotency simply
		// doesn't cover AMOs yet — two AMO submissions sharing a key both
		// get queued (not deduplicated).
		if incomingOrderSubmissionRequest.OrderIsAfterMarketOrder && !marketSession.IsMarketOpen() {
			afterMarketOrderQueue.Enqueue(incomingOrderSubmissionRequest)
			dependencies.auditTrail.Append(audittrail.Entry{
				EventType:               audittrail.EventAfterMarketOrderQueued,
				ClientAccountIdentifier: incomingOrderSubmissionRequest.ClientAccountIdentifier,
				InstrumentSymbol:        incomingOrderSubmissionRequest.InstrumentSymbol,
			})
			queuedAcknowledgement := orders.OrderAcknowledgementResponse{
				WasOrderAccepted:           true,
				IsQueuedAsAfterMarketOrder: true,
			}
			respondWithJson(responseWriter, http.StatusOK, queuedAcknowledgement)
			return
		}

		// FEATURES.md §2 "idempotent transactions": if this exact key was
		// already submitted — or is BEING submitted right now by another
		// concurrent request — return the SAME response instead of
		// re-running KYC/freeze/risk/matching a second time for what is,
		// from the client's perspective, a retry of one submission, not a
		// second order. Checked before every other gate so a replay never
		// even touches them. Exactly one caller (isThisCallTheOwner) does
		// the real work below; every other caller with the same key —
		// whether it arrives before or after the owner finishes — blocks
		// here and gets back the owner's completed response (or a
		// synthetic timeout rejection if the owner never completes; see
		// internal/idempotency's package doc).
		existingOrBlockedResponse, isThisCallTheOwner := idempotencyStore.ClaimKeyOrAwaitExistingResponse(
			incomingOrderSubmissionRequest.IdempotencyKey,
		)
		if !isThisCallTheOwner {
			log.Printf("idempotency key %q already claimed by another request — returning its response, not resubmitting", incomingOrderSubmissionRequest.IdempotencyKey)
			respondWithJson(responseWriter, http.StatusOK, existingOrBlockedResponse)
			return
		}

		acknowledgement := processOrderSubmission(dependencies, incomingOrderSubmissionRequest)
		idempotencyStore.CompleteClaimedKey(incomingOrderSubmissionRequest.IdempotencyKey, acknowledgement)
		respondWithJson(responseWriter, http.StatusOK, acknowledgement)
	}
}

// processOrderSubmission runs one order through the full gate → risk →
// matching-engine → settlement pipeline and returns the resulting
// acknowledgement. Pulled out of buildSubmitOrderHandler so
// buildCoverOrderHandler (and any future multi-leg order type) can drive
// an entry leg through the exact same real checks instead of
// reimplementing them.
func processOrderSubmission(
	dependencies orderSubmissionDependencies,
	incomingOrderSubmissionRequest orders.OrderSubmissionRequest,
) orders.OrderAcknowledgementResponse {
	// KYC gate comes before the risk check — an unonboarded account
	// shouldn't even have its margin evaluated, per FEATURES.md §1.
	//
	// Deliberate, debatable choice on failure handling: an EXPLICIT
	// "not eligible" answer fails CLOSED (reject the order) — but a
	// TRANSPORT failure (kyc-onboarding unreachable) fails OPEN (logs
	// a warning, proceeds to the risk check) rather than blocking all
	// trading platform-wide on one dependency's uptime. A real
	// production system handling actual client money would very
	// likely want to fail closed here instead; this skeleton
	// prioritizes keeping the demo/dev loop usable when a service is
	// mid-restart. Revisit before this is anywhere near real capital.
	kycStatus, kycFetchError := dependencies.kycClient.FetchKycStatus(incomingOrderSubmissionRequest.ClientAccountIdentifier)
	if kycFetchError != nil {
		log.Printf("KYC check unreachable for %s, failing OPEN (see comment above): %v", incomingOrderSubmissionRequest.ClientAccountIdentifier, kycFetchError)
	} else if !kycStatus.IsEligibleToPlaceOrders {
		dependencies.auditTrail.Append(audittrail.Entry{
			EventType:               audittrail.EventOrderRejected,
			ClientAccountIdentifier: incomingOrderSubmissionRequest.ClientAccountIdentifier,
			InstrumentSymbol:        incomingOrderSubmissionRequest.InstrumentSymbol,
			DetailMessage:           "KYC_NOT_VERIFIED",
		})
		return orders.OrderAcknowledgementResponse{
			WasOrderAccepted: false,
			HumanReadableRejectionReason: "Please complete KYC verification before trading. " +
				"Submit your PAN and name via kyc-onboarding's /kyc/submit endpoint.",
			MachineReadableRejectionReason: "KYC_NOT_VERIFIED",
		}
	}

	// Freeze gate — same fail-open-on-transport-failure /
	// fail-closed-on-explicit-answer pattern as the KYC gate above,
	// for consistency. See that gate's comment for the tradeoff; it
	// applies identically here.
	freezeStatus, freezeFetchError := dependencies.backofficeClient.FetchFreezeStatus(incomingOrderSubmissionRequest.ClientAccountIdentifier)
	if freezeFetchError != nil {
		log.Printf("freeze check unreachable for %s, failing OPEN (see KYC gate comment above): %v", incomingOrderSubmissionRequest.ClientAccountIdentifier, freezeFetchError)
	} else if freezeStatus.IsFrozen {
		dependencies.auditTrail.Append(audittrail.Entry{
			EventType:               audittrail.EventOrderRejected,
			ClientAccountIdentifier: incomingOrderSubmissionRequest.ClientAccountIdentifier,
			InstrumentSymbol:        incomingOrderSubmissionRequest.InstrumentSymbol,
			DetailMessage:           fmt.Sprintf("ACCOUNT_FROZEN: %s", freezeStatus.FreezeReason),
		})
		return orders.OrderAcknowledgementResponse{
			WasOrderAccepted:               false,
			HumanReadableRejectionReason:   fmt.Sprintf("This account is frozen and cannot trade: %s", freezeStatus.FreezeReason),
			MachineReadableRejectionReason: "ACCOUNT_FROZEN",
		}
	}

	// KNOWN GAP, not yet fixed: for a market order OR a
	// StopLossMarket order, LimitPriceInMinorUnits is 0 (ignored by
	// the matching engine), so this notional is always 0 and the
	// risk check below always passes regardless of actual balance.
	// A StopLossLimit order at least has a real LimitPriceInMinorUnits
	// to estimate against, but it's the price the order will fill at
	// IF triggered, not a current one — still an approximation. A
	// real build must estimate notional from the last traded price
	// (or the current best opposite-side quote) for market-priced
	// orders before risk-checking them — this skeleton doesn't have a
	// "last price" feed wired into oms-gateway yet to do that with.
	orderNotionalValueInMinorUnits := incomingOrderSubmissionRequest.LimitPriceInMinorUnits * int64(incomingOrderSubmissionRequest.OrderQuantity)

	riskCheckOutcome := dependencies.preTradeRiskEngine.EvaluateOrderAgainstAvailableMargin(
		incomingOrderSubmissionRequest.ClientAccountIdentifier,
		orderNotionalValueInMinorUnits,
	)

	if !riskCheckOutcome.IsOrderApproved {
		dependencies.auditTrail.Append(audittrail.Entry{
			EventType:               audittrail.EventOrderRejected,
			ClientAccountIdentifier: incomingOrderSubmissionRequest.ClientAccountIdentifier,
			InstrumentSymbol:        incomingOrderSubmissionRequest.InstrumentSymbol,
			DetailMessage:           riskCheckOutcome.MachineReadableRejectionReason,
		})
		return orders.OrderAcknowledgementResponse{
			WasOrderAccepted:               false,
			HumanReadableRejectionReason:   riskCheckOutcome.HumanReadableRejectionReason,
			MachineReadableRejectionReason: riskCheckOutcome.MachineReadableRejectionReason,
		}
	}

	assignedGlobalSequenceNumber := dependencies.globalSequenceNumberAllocator.AllocateNextSequenceNumber()

	dependencies.auditTrail.Append(audittrail.Entry{
		EventType:               audittrail.EventOrderSubmitted,
		ClientAccountIdentifier: incomingOrderSubmissionRequest.ClientAccountIdentifier,
		InstrumentSymbol:        incomingOrderSubmissionRequest.InstrumentSymbol,
		DetailMessage:           fmt.Sprintf("assignedGlobalSequenceNumber=%d", assignedGlobalSequenceNumber),
	})

	acknowledgement := orders.OrderAcknowledgementResponse{
		WasOrderAccepted:             true,
		AssignedGlobalSequenceNumber: assignedGlobalSequenceNumber,
	}

	matchingEngineResponse, handoffError := dependencies.matchingEngineClient.SubmitOrderAndAwaitMatchResult(
		matchingengineclient.OrderSubmissionWireRequest{
			ClientAccountIdentifier:      incomingOrderSubmissionRequest.ClientAccountIdentifier,
			InstrumentSymbol:             incomingOrderSubmissionRequest.InstrumentSymbol,
			OrderSideIsBuyNotSell:        incomingOrderSubmissionRequest.OrderSideIsBuyNotSell,
			OrderIsMarketOrderNotLimit:   incomingOrderSubmissionRequest.OrderIsMarketOrderNotLimit,
			OrderIsStopLossVariant:       incomingOrderSubmissionRequest.OrderIsStopLossVariant,
			StopTriggerPriceInMinorUnits: incomingOrderSubmissionRequest.StopTriggerPriceInMinorUnits,
			LimitPriceInMinorUnits:       incomingOrderSubmissionRequest.LimitPriceInMinorUnits,
			OrderQuantity:                incomingOrderSubmissionRequest.OrderQuantity,
		},
	)

	switch {
	case handoffError != nil:
		// The order already passed risk and was sequenced — it stays
		// accepted. This just means the matching engine couldn't be
		// reached right now, which is a real, expected failure mode
		// (see the TODO in matchingengineclient) and must never be
		// silently swallowed.
		log.Printf("matching-engine hand-off failed for seq=%d: %v", assignedGlobalSequenceNumber, handoffError)
		acknowledgement.MatchingEngineHandoffError = handoffError.Error()
		dependencies.auditTrail.Append(audittrail.Entry{
			EventType:               audittrail.EventOrderMatchingEngineFailure,
			ClientAccountIdentifier: incomingOrderSubmissionRequest.ClientAccountIdentifier,
			InstrumentSymbol:        incomingOrderSubmissionRequest.InstrumentSymbol,
			DetailMessage:           handoffError.Error(),
		})

	case matchingEngineResponse.ErrorMessage != nil:
		log.Printf(
			"matching-engine rejected seq=%d: %s",
			assignedGlobalSequenceNumber,
			*matchingEngineResponse.ErrorMessage,
		)
		acknowledgement.MatchingEngineHandoffError = *matchingEngineResponse.ErrorMessage
		dependencies.auditTrail.Append(audittrail.Entry{
			EventType:               audittrail.EventOrderMatchingEngineFailure,
			ClientAccountIdentifier: incomingOrderSubmissionRequest.ClientAccountIdentifier,
			InstrumentSymbol:        incomingOrderSubmissionRequest.InstrumentSymbol,
			DetailMessage:           *matchingEngineResponse.ErrorMessage,
		})

	default:
		if matchingEngineResponse.AssignedOrderSequenceNumber != nil {
			acknowledgement.MatchingEngineOrderSequenceNumber = *matchingEngineResponse.AssignedOrderSequenceNumber
		}
		for _, tradeExecutionWireEvent := range matchingEngineResponse.TradeExecutionEvents {
			acknowledgement.TradeExecutionEvents = append(acknowledgement.TradeExecutionEvents, orders.TradeExecutionSummary{
				BuyingClientAccountId:     tradeExecutionWireEvent.BuyingClientAccountId,
				SellingClientAccountId:    tradeExecutionWireEvent.SellingClientAccountId,
				ExecutedPriceInMinorUnits: tradeExecutionWireEvent.ExecutedPriceInMinorUnits,
				ExecutedQuantity:          tradeExecutionWireEvent.ExecutedQuantity,
			})
			settleTradeAgainstLedgerAndLocalCache(dependencies.preTradeRiskEngine, dependencies.ledgerClient, tradeExecutionWireEvent, assignedGlobalSequenceNumber)
			dependencies.positionBook.ApplyFill(
				tradeExecutionWireEvent.BuyingClientAccountId,
				tradeExecutionWireEvent.SellingClientAccountId,
				incomingOrderSubmissionRequest.InstrumentSymbol,
				tradeExecutionWireEvent.ExecutedQuantity,
			)
			dependencies.auditTrail.Append(audittrail.Entry{
				EventType:                         audittrail.EventOrderFilled,
				ClientAccountIdentifier:           incomingOrderSubmissionRequest.ClientAccountIdentifier,
				InstrumentSymbol:                  incomingOrderSubmissionRequest.InstrumentSymbol,
				MatchingEngineOrderSequenceNumber: acknowledgement.MatchingEngineOrderSequenceNumber,
				DetailMessage: fmt.Sprintf(
					"filled %d @ %d (buyer=%s seller=%s)",
					tradeExecutionWireEvent.ExecutedQuantity,
					tradeExecutionWireEvent.ExecutedPriceInMinorUnits,
					tradeExecutionWireEvent.BuyingClientAccountId,
					tradeExecutionWireEvent.SellingClientAccountId,
				),
			})
		}
	}

	return acknowledgement
}

// settleTradeAgainstLedgerAndLocalCache posts the trade's settlement
// journal entry to the ledger and immediately reflects it in the local
// risk cache. TODO(real build): per ARCHITECTURE.md §4, this belongs off
// the hot request path — an async consumer of matching-engine's trade
// events should post settlement, not the same handler that just
// risk-checked and routed the order. Kept synchronous and inline here
// only because it's the simplest way to prove the three-service loop
// (risk check -> match -> settle) actually closes.
func settleTradeAgainstLedgerAndLocalCache(
	preTradeRiskEngine *riskengine.PreTradeRiskEngine,
	ledgerClient *ledgerclient.LedgerClient,
	tradeExecutionWireEvent matchingengineclient.TradeExecutionWireEvent,
	assignedGlobalSequenceNumber uint64,
) {
	executedNotionalValueInMinorUnits := tradeExecutionWireEvent.ExecutedPriceInMinorUnits * int64(tradeExecutionWireEvent.ExecutedQuantity)

	settlementError := ledgerClient.PostTradeSettlementJournalEntry(
		tradeExecutionWireEvent.BuyingClientAccountId,
		tradeExecutionWireEvent.SellingClientAccountId,
		executedNotionalValueInMinorUnits,
		assignedGlobalSequenceNumber,
	)
	if settlementError != nil {
		// A failed settlement posting is a serious, non-silent problem in
		// any real build (the ledger is now out of sync with a trade that
		// genuinely happened) — logged loudly here since there's no
		// reconciliation/retry job yet to catch it.
		log.Printf("SETTLEMENT FAILED for seq=%d: %v — ledger is now out of sync with this trade", assignedGlobalSequenceNumber, settlementError)
		return
	}

	preTradeRiskEngine.ApplyTradeSettlementToLocalCache(
		tradeExecutionWireEvent.BuyingClientAccountId,
		tradeExecutionWireEvent.SellingClientAccountId,
		executedNotionalValueInMinorUnits,
	)
}

func buildPositionsHandler(positionBook *positions.PositionBook) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}

		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"accountIdentifier":             accountIdentifier,
			"netQuantityByInstrumentSymbol": positionBook.PositionsForAccount(accountIdentifier),
		})
	}
}

// buildCancelOrderHandler is the HTTP pass-through for
// matching-engine's cancelOrder — FEATURES.md-worthy gap-closer: without
// this, an order that rests (or a stop order that arms) sits on the book
// forever with no way for a client to pull it back. Deliberately NOT
// risk-checked or KYC/freeze-gated: cancelling an order can only reduce
// exposure, never increase it, so none of those gates apply here.
//
// TODO(real build): this doesn't verify the cancelling caller actually
// owns the order being cancelled — matching-engine's cancelOrder doesn't
// carry a client account id to check against. Fine for a skeleton with
// no auth anywhere yet; a real build must not ship this gap.
func buildCancelOrderHandler(matchingEngineClient *matchingengineclient.MatchingEngineClient, auditTrail *audittrail.AuditTrail) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		var cancelRequest orders.CancelOrderRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&cancelRequest); decodeError != nil {
			http.Error(responseWriter, fmt.Sprintf("malformed cancel request: %v", decodeError), http.StatusBadRequest)
			return
		}

		matchingEngineResponse, cancelError := matchingEngineClient.CancelOrderAndAwaitResult(
			cancelRequest.InstrumentSymbol,
			cancelRequest.MatchingEngineOrderSequenceNumber,
		)
		if cancelError != nil {
			// FEATURES.md §21 "plain-language rejection reasons": the raw
			// wrapped Go error (host:port, dial errors, etc.) is exactly
			// what gets logged and audited — genuinely useful for an
			// engineer debugging this — but it's not something a client
			// UI should show a user. `ErrorMessage` on the response gets
			// a separate, plain-language sentence instead; the raw error
			// stays available in the logs/audit trail for diagnostics.
			log.Printf("matching-engine cancel hand-off failed for order %d: %v", cancelRequest.MatchingEngineOrderSequenceNumber, cancelError)
			auditTrail.Append(audittrail.Entry{
				EventType:                         audittrail.EventOrderCancelFailed,
				InstrumentSymbol:                  cancelRequest.InstrumentSymbol,
				MatchingEngineOrderSequenceNumber: cancelRequest.MatchingEngineOrderSequenceNumber,
				DetailMessage:                     cancelError.Error(),
			})
			respondWithJson(responseWriter, http.StatusOK, orders.CancelOrderResponse{
				WasOrderCancelled: false,
				ErrorMessage:      "We couldn't reach the matching engine to cancel this order. Please try again in a moment — if it keeps failing, check the order's status before assuming it's still resting.",
			})
			return
		}

		wasOrderCancelled := matchingEngineResponse.WasOrderCancelled != nil && *matchingEngineResponse.WasOrderCancelled
		const orderNotFoundReason = "No matching order was found to cancel — it may have already fully filled, already been cancelled, or never existed."
		if wasOrderCancelled {
			auditTrail.Append(audittrail.Entry{
				EventType:                         audittrail.EventOrderCancelled,
				InstrumentSymbol:                  cancelRequest.InstrumentSymbol,
				MatchingEngineOrderSequenceNumber: cancelRequest.MatchingEngineOrderSequenceNumber,
			})
			respondWithJson(responseWriter, http.StatusOK, orders.CancelOrderResponse{WasOrderCancelled: true})
			return
		}

		auditTrail.Append(audittrail.Entry{
			EventType:                         audittrail.EventOrderCancelFailed,
			InstrumentSymbol:                  cancelRequest.InstrumentSymbol,
			MatchingEngineOrderSequenceNumber: cancelRequest.MatchingEngineOrderSequenceNumber,
			DetailMessage:                     orderNotFoundReason,
		})
		// Previously this branch left ErrorMessage empty on the actual
		// client-facing response — the plain-language reason only ever
		// reached the audit trail, not the caller who asked for the
		// cancellation. Fixed: the same reason now goes on the response
		// too.
		respondWithJson(responseWriter, http.StatusOK, orders.CancelOrderResponse{
			WasOrderCancelled: false,
			ErrorMessage:      orderNotFoundReason,
		})
	}
}

// buildCoverOrderHandler implements FEATURES.md §3's Cover Orders (CO):
// submit an entry order, and if (and only if) it actually fills — fully
// or partially — immediately place a protective StopLossMarket order on
// the opposite side for the filled quantity. Reuses processOrderSubmission
// for the entry leg so it gets the exact same KYC/freeze/risk/settlement
// treatment as a normal /orders/submit call.
//
// Deliberately simpler than a full Bracket Order (which also has a
// target/take-profit leg): with only one protective order in play, there
// is no one-cancels-other race to manage — the stop leg either triggers
// and exits the position, or it doesn't and the position stays open. A
// real Bracket Order implementation would need matching-engine to expose
// order-status queries or push fill notifications so the OMS can cancel
// the sibling leg the instant one fires; that capability doesn't exist
// yet, which is exactly why CO (one leg) ships before BO (two, OCO-linked
// legs) in this build.
//
// The protective leg deliberately bypasses KYC/freeze/risk checks — same
// rationale as order cancellation: a stop-loss that EXITS a position can
// only reduce exposure, never increase it, so gating it the same way a
// fresh entry is gated would be actively wrong, not just unnecessary.
//
// KNOWN GAP: if the entry partially fills across multiple resting price
// levels and then the matching-engine hand-off for the protective leg
// itself fails, the client is left with a real, unprotected open
// position and only a ProtectiveStopOrderError string to notice it by —
// no retry loop, no alerting. Logged loudly but not otherwise handled.
func buildCoverOrderHandler(dependencies orderSubmissionDependencies) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var coverOrderRequest orders.CoverOrderRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&coverOrderRequest); decodeError != nil {
			http.Error(responseWriter, "malformed cover order payload", http.StatusBadRequest)
			return
		}

		entryAcknowledgement := processOrderSubmission(dependencies, orders.OrderSubmissionRequest{
			ClientAccountIdentifier:    coverOrderRequest.ClientAccountIdentifier,
			InstrumentSymbol:           coverOrderRequest.InstrumentSymbol,
			OrderSideIsBuyNotSell:      coverOrderRequest.OrderSideIsBuyNotSell,
			OrderIsMarketOrderNotLimit: coverOrderRequest.OrderIsMarketOrderNotLimit,
			LimitPriceInMinorUnits:     coverOrderRequest.LimitPriceInMinorUnits,
			OrderQuantity:              coverOrderRequest.OrderQuantity,
		})

		coverOrderResponse := orders.CoverOrderResponse{EntryOrderAcknowledgement: entryAcknowledgement}

		var totalFilledQuantity uint64
		for _, tradeExecutionSummary := range entryAcknowledgement.TradeExecutionEvents {
			totalFilledQuantity += tradeExecutionSummary.ExecutedQuantity
		}

		if totalFilledQuantity == 0 {
			// Nothing filled — nothing to protect. Not an error: a
			// resting Limit entry with zero fills so far simply doesn't
			// have a protective leg yet in this skeleton (a real CO
			// implementation would likely still place the stop leg in
			// advance, deactivated until the entry fills — out of scope
			// here).
			respondWithJson(responseWriter, http.StatusOK, coverOrderResponse)
			return
		}

		protectiveStopTriggerPrice := coverOrderRequest.StopLossTriggerPriceInMinorUnits
		matchingEngineResponse, protectiveLegError := dependencies.matchingEngineClient.SubmitOrderAndAwaitMatchResult(
			matchingengineclient.OrderSubmissionWireRequest{
				ClientAccountIdentifier: coverOrderRequest.ClientAccountIdentifier,
				InstrumentSymbol:        coverOrderRequest.InstrumentSymbol,
				// Opposite side of the entry: a protective leg for a BUY
				// entry is a SELL stop, and vice versa.
				OrderSideIsBuyNotSell:        !coverOrderRequest.OrderSideIsBuyNotSell,
				OrderIsMarketOrderNotLimit:   true,
				OrderIsStopLossVariant:       true,
				StopTriggerPriceInMinorUnits: &protectiveStopTriggerPrice,
				OrderQuantity:                totalFilledQuantity,
			},
		)

		switch {
		case protectiveLegError != nil:
			log.Printf("COVER ORDER PROTECTIVE LEG FAILED for %s on %s — position is OPEN and UNPROTECTED: %v", coverOrderRequest.ClientAccountIdentifier, coverOrderRequest.InstrumentSymbol, protectiveLegError)
			coverOrderResponse.ProtectiveStopOrderError = protectiveLegError.Error()
			dependencies.auditTrail.Append(audittrail.Entry{
				EventType:               audittrail.EventCoverProtectiveLegFailed,
				ClientAccountIdentifier: coverOrderRequest.ClientAccountIdentifier,
				InstrumentSymbol:        coverOrderRequest.InstrumentSymbol,
				DetailMessage:           protectiveLegError.Error(),
			})
		case matchingEngineResponse.ErrorMessage != nil:
			log.Printf("COVER ORDER PROTECTIVE LEG REJECTED for %s on %s — position is OPEN and UNPROTECTED: %s", coverOrderRequest.ClientAccountIdentifier, coverOrderRequest.InstrumentSymbol, *matchingEngineResponse.ErrorMessage)
			coverOrderResponse.ProtectiveStopOrderError = *matchingEngineResponse.ErrorMessage
			dependencies.auditTrail.Append(audittrail.Entry{
				EventType:               audittrail.EventCoverProtectiveLegFailed,
				ClientAccountIdentifier: coverOrderRequest.ClientAccountIdentifier,
				InstrumentSymbol:        coverOrderRequest.InstrumentSymbol,
				DetailMessage:           *matchingEngineResponse.ErrorMessage,
			})
		default:
			if matchingEngineResponse.AssignedOrderSequenceNumber != nil {
				coverOrderResponse.ProtectiveStopOrderSequenceNumber = *matchingEngineResponse.AssignedOrderSequenceNumber
			}
			dependencies.auditTrail.Append(audittrail.Entry{
				EventType:                         audittrail.EventCoverProtectiveLegPlaced,
				ClientAccountIdentifier:           coverOrderRequest.ClientAccountIdentifier,
				InstrumentSymbol:                  coverOrderRequest.InstrumentSymbol,
				MatchingEngineOrderSequenceNumber: coverOrderResponse.ProtectiveStopOrderSequenceNumber,
			})
		}

		respondWithJson(responseWriter, http.StatusOK, coverOrderResponse)
	}
}

// buildOrderStatusHandler is the HTTP pass-through for matching-engine's
// queryOrderStatus — read-only, no gating needed (same rationale as
// cancellation and positions: looking something up can't change
// exposure). Query params: instrumentSymbol, matchingEngineOrderSequenceNumber.
func buildOrderStatusHandler(matchingEngineClient *matchingengineclient.MatchingEngineClient) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		instrumentSymbol := request.URL.Query().Get("instrumentSymbol")
		orderSequenceNumberParam := request.URL.Query().Get("matchingEngineOrderSequenceNumber")
		if instrumentSymbol == "" || orderSequenceNumberParam == "" {
			http.Error(responseWriter, "missing instrumentSymbol or matchingEngineOrderSequenceNumber query parameter", http.StatusBadRequest)
			return
		}

		orderSequenceNumberToQuery, parseError := strconv.ParseUint(orderSequenceNumberParam, 10, 64)
		if parseError != nil {
			http.Error(responseWriter, "matchingEngineOrderSequenceNumber must be a non-negative integer", http.StatusBadRequest)
			return
		}

		matchingEngineResponse, queryError := matchingEngineClient.QueryOrderStatusAndAwaitResult(instrumentSymbol, orderSequenceNumberToQuery)
		if queryError != nil {
			log.Printf("matching-engine status query failed for order %d: %v", orderSequenceNumberToQuery, queryError)
			respondWithJson(responseWriter, http.StatusOK, orders.OrderStatusResponse{
				OrderStatus:  "UNKNOWN",
				ErrorMessage: queryError.Error(),
			})
			return
		}

		statusResponse := orders.OrderStatusResponse{
			OrderSideIsBuyNotSell: matchingEngineResponse.OrderStatusSideIsBuyNotSell,
			PriceInMinorUnits:     matchingEngineResponse.OrderStatusPriceInMinorUnits,
			Quantity:              matchingEngineResponse.OrderStatusQuantity,
		}
		if matchingEngineResponse.OrderStatus != nil {
			statusResponse.OrderStatus = *matchingEngineResponse.OrderStatus
		}
		respondWithJson(responseWriter, http.StatusOK, statusResponse)
	}
}

func buildMarketSessionStatusHandler(
	marketSession *marketsession.MarketSessionState,
	afterMarketOrderQueue *amoqueue.AfterMarketOrderQueue,
) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"isMarketOpen":            marketSession.IsMarketOpen(),
			"queuedAfterMarketOrders": afterMarketOrderQueue.QueuedCount(),
		})
	}
}

func buildMarketSessionCloseHandler(marketSession *marketsession.MarketSessionState, auditTrail *audittrail.AuditTrail) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		marketSession.SetMarketOpen(false)
		log.Printf("market session CLOSED")
		auditTrail.Append(audittrail.Entry{EventType: audittrail.EventMarketSessionClosed})
		respondWithJson(responseWriter, http.StatusOK, map[string]any{"isMarketOpen": false})
	}
}

// buildMarketSessionOpenHandler flips the market open AND, in the same
// call, drains every queued AMO through processOrderSubmission — this is
// the one place AMOs actually reach KYC/freeze/risk/matching-engine.
// Synchronous and inline (not a background goroutine) so the drain is
// deterministic and easy to verify: the HTTP response only returns once
// every queued order has been processed.
//
// TODO(real build): draining synchronously inside the HTTP handler means
// a market open with a large queued backlog blocks the caller for as
// long as the whole drain takes — fine for a skeleton demo, not fine at
// real scale. A real build would ack the "market open" request
// immediately and drain asynchronously, publishing each AMO's real
// outcome somewhere a client can actually observe it (see the gap noted
// on OrderAcknowledgementResponse.IsQueuedAsAfterMarketOrder).
func buildMarketSessionOpenHandler(
	marketSession *marketsession.MarketSessionState,
	afterMarketOrderQueue *amoqueue.AfterMarketOrderQueue,
	dependencies orderSubmissionDependencies,
) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		marketSession.SetMarketOpen(true)
		dependencies.auditTrail.Append(audittrail.Entry{EventType: audittrail.EventMarketSessionOpened})

		drainedRequests := afterMarketOrderQueue.DrainAll()
		log.Printf("market session OPENED — draining %d queued after-market order(s)", len(drainedRequests))

		processedAcknowledgements := make([]orders.OrderAcknowledgementResponse, 0, len(drainedRequests))
		for _, queuedRequest := range drainedRequests {
			acknowledgement := processOrderSubmission(dependencies, queuedRequest)
			processedAcknowledgements = append(processedAcknowledgements, acknowledgement)
		}

		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"isMarketOpen":                     true,
			"processedAfterMarketOrderResults": processedAcknowledgements,
		})
	}
}

// buildAuditTrailHandler exposes the audit trail for compliance/support
// queries — FEATURES.md's "Audit trail: immutable log of every order,
// modification, cancellation". `GET /audit-trail` returns everything;
// `GET /audit-trail?accountId=...` filters to one account.
func buildAuditTrailHandler(auditTrail *audittrail.AuditTrail) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier != "" {
			respondWithJson(responseWriter, http.StatusOK, auditTrail.EntriesForAccount(accountIdentifier))
			return
		}
		respondWithJson(responseWriter, http.StatusOK, auditTrail.AllEntries())
	}
}

func respondWithJson(responseWriter http.ResponseWriter, httpStatusCode int, responseBody any) {
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(httpStatusCode)
	_ = json.NewEncoder(responseWriter).Encode(responseBody)
}

type estimateChargesWireRequest struct {
	OrderSideIsBuyNotSell  bool   `json:"orderSideIsBuyNotSell"`
	LimitPriceInMinorUnits int64  `json:"limitPriceInMinorUnits"`
	OrderQuantity          uint64 `json:"orderQuantity"`
	IsIntradayNotDelivery  bool   `json:"isIntradayNotDelivery"`
}

// buildEstimateChargesHandler is FEATURES.md §21's "Full charges
// breakdown *before* order confirmation" — a read-only, non-gated query
// (no KYC/freeze/risk check, no idempotency, no matching-engine
// involvement at all) a client calls BEFORE submitting an order, purely
// to show the receipt-style breakdown chargescalculator produces. Takes
// the same price/quantity/side shape as /orders/submit so a client can
// reuse its order-ticket form state directly.
func buildEstimateChargesHandler() http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest estimateChargesWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed charges-estimate payload", http.StatusBadRequest)
			return
		}

		breakdown := chargescalculator.CalculateCharges(
			wireRequest.OrderSideIsBuyNotSell,
			wireRequest.LimitPriceInMinorUnits,
			wireRequest.OrderQuantity,
			wireRequest.IsIntradayNotDelivery,
		)
		respondWithJson(responseWriter, http.StatusOK, breakdown)
	}
}
