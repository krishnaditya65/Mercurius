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
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"mercurius/omsgateway/internal/algolimits"
	"mercurius/omsgateway/internal/amoqueue"
	"mercurius/omsgateway/internal/audittrail"
	"mercurius/omsgateway/internal/backofficeclient"
	"mercurius/omsgateway/internal/chargescalculator"
	"mercurius/omsgateway/internal/dmagateway"
	"mercurius/omsgateway/internal/httplogging"
	"mercurius/omsgateway/internal/idempotency"
	"mercurius/omsgateway/internal/kycclient"
	"mercurius/omsgateway/internal/ledgerclient"
	"mercurius/omsgateway/internal/marginengine"
	"mercurius/omsgateway/internal/marginfunding"
	"mercurius/omsgateway/internal/marginpledge"
	"mercurius/omsgateway/internal/marketsession"
	"mercurius/omsgateway/internal/matchingengineclient"
	"mercurius/omsgateway/internal/metrics"
	"mercurius/omsgateway/internal/optionschain"
	"mercurius/omsgateway/internal/orders"
	"mercurius/omsgateway/internal/papertrading"
	"mercurius/omsgateway/internal/positions"
	"mercurius/omsgateway/internal/quantengineclient"
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

	quantEngineBaseUrl := os.Getenv("QUANT_ENGINE_BASE_URL")
	if quantEngineBaseUrl == "" {
		quantEngineBaseUrl = "http://127.0.0.1:8085"
	}
	quantEngineClient := quantengineclient.NewQuantEngineClient(quantEngineBaseUrl)

	positionBook := positions.NewPositionBook()
	// paperPositionBook is FEATURES.md §7's paper trading mode: a
	// COMPLETELY SEPARATE positions.PositionBook instance (same type,
	// distinct state) so simulated paper fills can never contaminate real
	// holdings — see internal/papertrading's package doc and
	// processOrderSubmission's paper-trading branch below.
	paperPositionBook := positions.NewPositionBook()
	pledgeBook := marginpledge.NewPledgeBook()
	fundingBook := marginfunding.NewFundingBook()
	// algoLimitsRegistry: FEATURES.md §7 strategy resource limits &
	// circuit breakers. The default config (used by any strategyId that
	// never gets an explicit override via POST /algo-limits/configure)
	// is deliberately unlimited (0/0) — algolimits only constrains an
	// order that actually opts into a strategyId AND that strategy has
	// been configured with real limits, so this is fully backward
	// compatible with every pre-existing client that never sets
	// strategyIdentifier at all.
	algoLimitsRegistry := algolimits.NewRegistry(algolimits.StrategyLimitConfig{})
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
		pledgeBook:                    pledgeBook,
		paperPositionBook:             paperPositionBook,
		algoLimitsRegistry:            algoLimitsRegistry,
	}

	httpRequestMultiplexer := http.NewServeMux()
	httpRequestMultiplexer.HandleFunc("/health", healthCheckHandler)
	httpRequestMultiplexer.HandleFunc("/orders/submit", buildSubmitOrderHandler(orderSubmissionDeps, idempotencyStore, marketSession, afterMarketOrderQueue))
	httpRequestMultiplexer.HandleFunc("/positions", buildPositionsHandler(positionBook))
	httpRequestMultiplexer.HandleFunc("/paper-positions", buildPositionsHandler(paperPositionBook))
	httpRequestMultiplexer.HandleFunc("/orders/cancel", buildCancelOrderHandler(matchingEngineClient, auditTrail))
	httpRequestMultiplexer.HandleFunc("/orders/cover-submit", buildCoverOrderHandler(orderSubmissionDeps))
	httpRequestMultiplexer.HandleFunc("/orders/status", buildOrderStatusHandler(matchingEngineClient))
	httpRequestMultiplexer.HandleFunc("/market-session/status", buildMarketSessionStatusHandler(marketSession, afterMarketOrderQueue))
	httpRequestMultiplexer.HandleFunc("/market-session/open", buildMarketSessionOpenHandler(marketSession, afterMarketOrderQueue, orderSubmissionDeps))
	httpRequestMultiplexer.HandleFunc("/market-session/close", buildMarketSessionCloseHandler(marketSession, auditTrail))
	httpRequestMultiplexer.HandleFunc("/audit-trail", buildAuditTrailHandler(auditTrail))
	httpRequestMultiplexer.HandleFunc("/orders/estimate-charges", buildEstimateChargesHandler())
	httpRequestMultiplexer.HandleFunc("/metrics", metrics.BuildMetricsHandler(metricsRegistry))
	httpRequestMultiplexer.HandleFunc("/margin-pledge/pledge", buildPledgeHoldingHandler(pledgeBook, positionBook, preTradeRiskEngine))
	httpRequestMultiplexer.HandleFunc("/margin-pledge/unpledge", buildUnpledgeHoldingHandler(pledgeBook, preTradeRiskEngine))
	httpRequestMultiplexer.HandleFunc("/margin-pledge/set-utilized-margin", buildSetUtilizedMarginHandler(pledgeBook))
	httpRequestMultiplexer.HandleFunc("/margin-pledge/holdings", buildPledgesForAccountHandler(pledgeBook))
	httpRequestMultiplexer.HandleFunc("/margin/calculate-span-exposure", buildCalculateSpanExposureMarginHandler())
	httpRequestMultiplexer.HandleFunc("/margin-funding/request", buildMarginFundingRequestHandler(fundingBook, pledgeBook, ledgerClient, preTradeRiskEngine))
	httpRequestMultiplexer.HandleFunc("/margin-funding", buildMarginFundingStatusHandler(fundingBook, pledgeBook))
	httpRequestMultiplexer.HandleFunc("/options/chain", buildOptionsChainHandler(quantEngineClient))
	httpRequestMultiplexer.HandleFunc("/algo-limits/configure", buildConfigureAlgoLimitsHandler(algoLimitsRegistry))
	httpRequestMultiplexer.HandleFunc("/algo-limits", buildAlgoLimitsStatusHandler(algoLimitsRegistry))

	// FEATURES.md §3 DMA/FIX gateway (internal/dmagateway) — LOUD WARNING:
	// NOT FIX-protocol-certified, see that package's doc comment for the
	// full disclaimer. Reuses processOrderSubmission via this closure so
	// an order accepted over the TCP session runs through the exact same
	// risk-check/audit-trail/matching-engine pipeline as HTTP
	// /orders/submit.
	dmaOrderSubmitFunc := func(request orders.OrderSubmissionRequest) orders.OrderAcknowledgementResponse {
		return processOrderSubmission(orderSubmissionDeps, request)
	}
	dmaListenAddress := os.Getenv("DMA_GATEWAY_LISTEN_ADDRESS")
	if dmaListenAddress == "" {
		dmaListenAddress = ":8088"
	}
	dmaServer := dmagateway.NewServer(dmaListenAddress, dmaOrderSubmitFunc)
	go func() {
		if dmaServerError := dmaServer.ListenAndServe(); dmaServerError != nil {
			log.Printf("DMA/FIX-inspired gateway failed to start on %s: %v", dmaListenAddress, dmaServerError)
		}
	}()

	listenAddress := ":8081"
	log.Printf(
		"oms-gateway listening on %s (CORS wide open — see withPermissiveCorsForDevelopment) — matching-engine at %s, ledger at %s, kyc-onboarding at %s, backoffice at %s, quant-engine at %s\n",
		listenAddress,
		matchingEngineTcpAddress,
		ledgerBaseUrl,
		kycOnboardingBaseUrl,
		backofficeBaseUrl,
		quantEngineBaseUrl,
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
	pledgeBook                    *marginpledge.PledgeBook
	// paperPositionBook: FEATURES.md §7 paper trading — see main()'s
	// construction comment.
	paperPositionBook *positions.PositionBook
	// algoLimitsRegistry: FEATURES.md §7 strategy resource limits — see
	// main()'s construction comment.
	algoLimitsRegistry *algolimits.Registry
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

		// FEATURES.md §3 Iceberg/FOK/IOC: validated and accepted here — see
		// orders.ValidateOrderExecutionType's doc comment for the honest
		// boundary on what's NOT enforced (true fill semantics need
		// matching-engine support this build doesn't have).
		if executionTypeError := orders.ValidateOrderExecutionType(incomingOrderSubmissionRequest); executionTypeError != nil {
			http.Error(responseWriter, executionTypeError.Error(), http.StatusBadRequest)
			return
		}
		if incomingOrderSubmissionRequest.OrderExecutionType == orders.OrderExecutionTypeIceberg ||
			incomingOrderSubmissionRequest.OrderExecutionType == orders.OrderExecutionTypeFillOrKill ||
			incomingOrderSubmissionRequest.OrderExecutionType == orders.OrderExecutionTypeImmediateOrCancel {
			log.Printf(
				"order accepted with orderExecutionType=%s for %s on %s -- NOTE: matching-engine does not yet implement true %s fill semantics, this order will match using ordinary continuous-matching rules like any Limit/Market order (see orders.ValidateOrderExecutionType doc comment)",
				incomingOrderSubmissionRequest.OrderExecutionType,
				incomingOrderSubmissionRequest.ClientAccountIdentifier,
				incomingOrderSubmissionRequest.InstrumentSymbol,
				incomingOrderSubmissionRequest.OrderExecutionType,
			)
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
	// FEATURES.md §7 strategy resource limits & circuit breakers —
	// checked FIRST, before even KYC: an order tagged with a
	// strategyIdentifier that trips its configured max-orders/sec or
	// max-notional/day limit is rejected before touching KYC/freeze/
	// pledge/risk/matching-engine at all. An order with no
	// strategyIdentifier at all skips this entirely (see
	// internal/algolimits' package doc — unconfigured/untagged orders
	// are never limited). Uses real wall-clock time.Now() here (the only
	// place in this codebase that's appropriate — internal/algolimits
	// itself takes `now` as an explicit parameter everywhere so its OWN
	// tests never depend on the wall clock).
	if incomingOrderSubmissionRequest.StrategyIdentifier != "" {
		orderNotionalForLimitCheck := incomingOrderSubmissionRequest.LimitPriceInMinorUnits * int64(incomingOrderSubmissionRequest.OrderQuantity)
		if limitError := dependencies.algoLimitsRegistry.CheckAndReserve(
			incomingOrderSubmissionRequest.StrategyIdentifier,
			orderNotionalForLimitCheck,
			time.Now(),
		); limitError != nil {
			machineReadableReason := "STRATEGY_RATE_LIMIT_EXCEEDED"
			if errors.Is(limitError, algolimits.ErrDailyNotionalLimitExceeded) {
				machineReadableReason = "STRATEGY_DAILY_NOTIONAL_LIMIT_EXCEEDED"
			}
			dependencies.auditTrail.Append(audittrail.Entry{
				EventType:               audittrail.EventStrategyLimitRejected,
				ClientAccountIdentifier: incomingOrderSubmissionRequest.ClientAccountIdentifier,
				InstrumentSymbol:        incomingOrderSubmissionRequest.InstrumentSymbol,
				DetailMessage:           fmt.Sprintf("strategyId=%s: %v", incomingOrderSubmissionRequest.StrategyIdentifier, limitError),
			})
			return orders.OrderAcknowledgementResponse{
				WasOrderAccepted:               false,
				HumanReadableRejectionReason:   fmt.Sprintf("This order was blocked by strategy %q's own resource limits: %v", incomingOrderSubmissionRequest.StrategyIdentifier, limitError),
				MachineReadableRejectionReason: machineReadableReason,
			}
		}
	}

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

	// Pledged-holding gate — FEATURES.md §3 margin pledge system: a
	// SELL order can never draw down more of an instrument than the
	// account's UNPLEDGED holding covers. Only checked on the sell
	// side (buying never touches pledged collateral) and only for
	// instruments with a non-zero pledged quantity, so this is a no-op
	// for every order that isn't selling something currently pledged.
	if !incomingOrderSubmissionRequest.OrderSideIsBuyNotSell {
		pledgedQuantity := dependencies.pledgeBook.PledgedQuantity(
			incomingOrderSubmissionRequest.ClientAccountIdentifier,
			incomingOrderSubmissionRequest.InstrumentSymbol,
		)
		if pledgedQuantity > 0 {
			netHoldingQuantity := dependencies.positionBook.PositionsForAccount(incomingOrderSubmissionRequest.ClientAccountIdentifier)[incomingOrderSubmissionRequest.InstrumentSymbol]
			unpledgedQuantity := netHoldingQuantity - int64(pledgedQuantity)
			if unpledgedQuantity < 0 || incomingOrderSubmissionRequest.OrderQuantity > uint64(unpledgedQuantity) {
				dependencies.auditTrail.Append(audittrail.Entry{
					EventType:               audittrail.EventOrderRejected,
					ClientAccountIdentifier: incomingOrderSubmissionRequest.ClientAccountIdentifier,
					InstrumentSymbol:        incomingOrderSubmissionRequest.InstrumentSymbol,
					DetailMessage:           "PLEDGED_QUANTITY_UNAVAILABLE",
				})
				return orders.OrderAcknowledgementResponse{
					WasOrderAccepted: false,
					HumanReadableRejectionReason: fmt.Sprintf(
						"This sell order needs %d shares, but %d of your %d %s shares are currently pledged as collateral and unavailable to sell. Unpledge them first via POST /margin-pledge/unpledge.",
						incomingOrderSubmissionRequest.OrderQuantity, pledgedQuantity, netHoldingQuantity, incomingOrderSubmissionRequest.InstrumentSymbol,
					),
					MachineReadableRejectionReason: "PLEDGED_QUANTITY_UNAVAILABLE",
				}
			}
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
		IsPaperTradingOrder:          incomingOrderSubmissionRequest.IsPaperTradingOrder,
	}

	// FEATURES.md §7 paper trading mode: everything ABOVE this point —
	// KYC, freeze, pledged-holding, and pre-trade risk gates, plus
	// sequence assignment and the ORDER_SUBMITTED audit entry — is
	// IDENTICAL for a paper order and a live one. Only the final
	// hand-off differs: a paper order never reaches the real
	// matching-engine and never posts a real settlement to ledger. Its
	// simulated fill (internal/papertrading) updates paperPositionBook
	// — a completely separate positions.PositionBook instance — instead
	// of the real one, so paper P&L can never contaminate real holdings.
	if incomingOrderSubmissionRequest.IsPaperTradingOrder {
		simulatedFill, simulationError := papertrading.SimulateFill(incomingOrderSubmissionRequest)
		if simulationError != nil {
			acknowledgement.PaperOrderSimulationError = simulationError.Error()
			dependencies.auditTrail.Append(audittrail.Entry{
				EventType:               audittrail.EventPaperOrderSimulationFailed,
				ClientAccountIdentifier: incomingOrderSubmissionRequest.ClientAccountIdentifier,
				InstrumentSymbol:        incomingOrderSubmissionRequest.InstrumentSymbol,
				DetailMessage:           simulationError.Error(),
			})
			return acknowledgement
		}

		buyingAccountId, sellingAccountId := incomingOrderSubmissionRequest.ClientAccountIdentifier, papertrading.SyntheticCounterpartyAccountIdentifier
		if !incomingOrderSubmissionRequest.OrderSideIsBuyNotSell {
			buyingAccountId, sellingAccountId = sellingAccountId, buyingAccountId
		}

		dependencies.paperPositionBook.ApplyFill(
			buyingAccountId,
			sellingAccountId,
			incomingOrderSubmissionRequest.InstrumentSymbol,
			simulatedFill.ExecutedQuantity,
		)
		acknowledgement.TradeExecutionEvents = append(acknowledgement.TradeExecutionEvents, orders.TradeExecutionSummary{
			BuyingClientAccountId:     buyingAccountId,
			SellingClientAccountId:    sellingAccountId,
			ExecutedPriceInMinorUnits: simulatedFill.ExecutedPriceInMinorUnits,
			ExecutedQuantity:          simulatedFill.ExecutedQuantity,
		})
		dependencies.auditTrail.Append(audittrail.Entry{
			EventType:               audittrail.EventPaperOrderFilled,
			ClientAccountIdentifier: incomingOrderSubmissionRequest.ClientAccountIdentifier,
			InstrumentSymbol:        incomingOrderSubmissionRequest.InstrumentSymbol,
			DetailMessage: fmt.Sprintf(
				"SIMULATED fill %d @ %d (NOT a real matching-engine trade, NOT settled to ledger)",
				simulatedFill.ExecutedQuantity, simulatedFill.ExecutedPriceInMinorUnits,
			),
		})
		return acknowledgement
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

// pledgeHoldingWireRequest is the client-facing payload for
// POST /margin-pledge/pledge. currentNetHoldingQuantity is deliberately
// NOT taken from the client — it's looked up server-side from
// positionBook, so a client can't lie about how much it actually holds.
type pledgeHoldingWireRequest struct {
	ClientAccountIdentifier    string `json:"clientAccountIdentifier"`
	InstrumentSymbol           string `json:"instrumentSymbol"`
	Quantity                   uint64 `json:"quantity"`
	ReferencePriceInMinorUnits int64  `json:"referencePriceInMinorUnits"`
}

type pledgeHoldingWireResponse struct {
	WasPledgeAccepted                      bool                       `json:"wasPledgeAccepted"`
	ErrorMessage                           string                     `json:"errorMessage,omitempty"`
	PledgeRecord                           *marginpledge.PledgeRecord `json:"pledgeRecord,omitempty"`
	MarginValueContributedInMinorUnits     int64                      `json:"marginValueContributedInMinorUnits,omitempty"`
	AvailableMarginAfterPledgeInMinorUnits int64                      `json:"availableMarginAfterPledgeInMinorUnits,omitempty"`
}

// buildPledgeHoldingHandler is FEATURES.md §3's Margin Pledge system:
// pledging `quantity` of an existing holding as collateral increases the
// account's available margin (internal/riskengine) by a haircut-adjusted
// value and marks that quantity unavailable to sell (enforced in
// processOrderSubmission's pledged-holding gate) until it's unpledged.
//
// KNOWN GAP, loudly documented in internal/marginpledge's package doc:
// referencePriceInMinorUnits is caller-supplied, not looked up from any
// live price feed — oms-gateway has none yet. Every resulting margin
// figure is illustrative, not authoritative — same caveat as
// internal/chargescalculator and internal/marginengine.
func buildPledgeHoldingHandler(
	pledgeBook *marginpledge.PledgeBook,
	positionBook *positions.PositionBook,
	preTradeRiskEngine *riskengine.PreTradeRiskEngine,
) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest pledgeHoldingWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed pledge request", http.StatusBadRequest)
			return
		}

		currentNetHoldingQuantity := positionBook.PositionsForAccount(wireRequest.ClientAccountIdentifier)[wireRequest.InstrumentSymbol]

		record, marginValueContributed, pledgeError := pledgeBook.PledgeHolding(
			wireRequest.ClientAccountIdentifier,
			wireRequest.InstrumentSymbol,
			wireRequest.Quantity,
			wireRequest.ReferencePriceInMinorUnits,
			currentNetHoldingQuantity,
		)
		if pledgeError != nil {
			respondWithJson(responseWriter, http.StatusOK, pledgeHoldingWireResponse{
				WasPledgeAccepted: false,
				ErrorMessage:      pledgeError.Error(),
			})
			return
		}

		preTradeRiskEngine.AdjustAvailableMarginInMinorUnits(wireRequest.ClientAccountIdentifier, marginValueContributed)
		availableMarginAfterPledge, _ := preTradeRiskEngine.AvailableMarginInMinorUnits(wireRequest.ClientAccountIdentifier)

		respondWithJson(responseWriter, http.StatusOK, pledgeHoldingWireResponse{
			WasPledgeAccepted:                      true,
			PledgeRecord:                           &record,
			MarginValueContributedInMinorUnits:     marginValueContributed,
			AvailableMarginAfterPledgeInMinorUnits: availableMarginAfterPledge,
		})
	}
}

// unpledgeHoldingWireRequest is the client-facing payload for
// POST /margin-pledge/unpledge.
type unpledgeHoldingWireRequest struct {
	ClientAccountIdentifier string `json:"clientAccountIdentifier"`
	InstrumentSymbol        string `json:"instrumentSymbol"`
	Quantity                uint64 `json:"quantity"`
}

type unpledgeHoldingWireResponse struct {
	WasUnpledgeAccepted                      bool   `json:"wasUnpledgeAccepted"`
	ErrorMessage                             string `json:"errorMessage,omitempty"`
	MarginValueReleasedInMinorUnits          int64  `json:"marginValueReleasedInMinorUnits,omitempty"`
	AvailableMarginAfterUnpledgeInMinorUnits int64  `json:"availableMarginAfterUnpledgeInMinorUnits,omitempty"`
}

// buildUnpledgeHoldingHandler releases pledged collateral, refused (real
// state-machine check, see marginpledge.ErrPledgeStillBackingOpenMarginPosition)
// if doing so would drop the account's pledged collateral below its
// currently utilized margin — see POST /margin-pledge/set-utilized-margin
// to set that figure (a real, documented simplification: oms-gateway has
// no structured open-F&O-position book yet to derive it from
// automatically, see marginpledge.PledgeBook's doc comment).
func buildUnpledgeHoldingHandler(
	pledgeBook *marginpledge.PledgeBook,
	preTradeRiskEngine *riskengine.PreTradeRiskEngine,
) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest unpledgeHoldingWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed unpledge request", http.StatusBadRequest)
			return
		}

		marginValueReleased, unpledgeError := pledgeBook.UnpledgeHolding(
			wireRequest.ClientAccountIdentifier,
			wireRequest.InstrumentSymbol,
			wireRequest.Quantity,
		)
		if unpledgeError != nil {
			respondWithJson(responseWriter, http.StatusOK, unpledgeHoldingWireResponse{
				WasUnpledgeAccepted: false,
				ErrorMessage:        unpledgeError.Error(),
			})
			return
		}

		preTradeRiskEngine.AdjustAvailableMarginInMinorUnits(wireRequest.ClientAccountIdentifier, -marginValueReleased)
		availableMarginAfterUnpledge, _ := preTradeRiskEngine.AvailableMarginInMinorUnits(wireRequest.ClientAccountIdentifier)

		respondWithJson(responseWriter, http.StatusOK, unpledgeHoldingWireResponse{
			WasUnpledgeAccepted:                      true,
			MarginValueReleasedInMinorUnits:          marginValueReleased,
			AvailableMarginAfterUnpledgeInMinorUnits: availableMarginAfterUnpledge,
		})
	}
}

// setUtilizedMarginWireRequest is the payload for
// POST /margin-pledge/set-utilized-margin — see buildUnpledgeHoldingHandler's
// doc comment for why this is an explicit, documented stand-in rather
// than something derived automatically.
type setUtilizedMarginWireRequest struct {
	ClientAccountIdentifier    string `json:"clientAccountIdentifier"`
	UtilizedMarginInMinorUnits int64  `json:"utilizedMarginInMinorUnits"`
}

func buildSetUtilizedMarginHandler(pledgeBook *marginpledge.PledgeBook) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest setUtilizedMarginWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed set-utilized-margin request", http.StatusBadRequest)
			return
		}

		pledgeBook.SetUtilizedMarginInMinorUnits(wireRequest.ClientAccountIdentifier, wireRequest.UtilizedMarginInMinorUnits)
		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"clientAccountIdentifier":    wireRequest.ClientAccountIdentifier,
			"utilizedMarginInMinorUnits": wireRequest.UtilizedMarginInMinorUnits,
		})
	}
}

// buildPledgesForAccountHandler is a read-only lookup of an account's
// currently pledged holdings — GET /margin-pledge/holdings?accountId=...
func buildPledgesForAccountHandler(pledgeBook *marginpledge.PledgeBook) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}
		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"accountIdentifier":         accountIdentifier,
			"pledgesByInstrumentSymbol": pledgeBook.PledgesForAccount(accountIdentifier),
		})
	}
}

// calculateSpanExposureMarginWireRequest is the payload for
// POST /margin/calculate-span-exposure — FEATURES.md §3's SPAN +
// exposure margin calculator for F&O. See internal/marginengine's
// package doc for the loud "illustrative, not exchange-certified"
// warning that applies to every number this endpoint returns.
type calculateSpanExposureMarginWireRequest struct {
	ContractNotionalValueInMinorUnits int64 `json:"contractNotionalValueInMinorUnits"`
}

func buildCalculateSpanExposureMarginHandler() http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest calculateSpanExposureMarginWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed margin-calculation request", http.StatusBadRequest)
			return
		}

		requirement, calculationError := marginengine.CalculateSpanAndExposureMargin(wireRequest.ContractNotionalValueInMinorUnits)
		if calculationError != nil {
			http.Error(responseWriter, calculationError.Error(), http.StatusBadRequest)
			return
		}
		respondWithJson(responseWriter, http.StatusOK, requirement)
	}
}

// marginFundingRequestWireRequest is the payload for
// POST /margin-funding/request.
type marginFundingRequestWireRequest struct {
	ClientAccountIdentifier     string `json:"clientAccountIdentifier"`
	RequestedAmountInMinorUnits int64  `json:"requestedAmountInMinorUnits"`
}

type marginFundingRequestWireResponse struct {
	WasFundingApproved                           bool   `json:"wasFundingApproved"`
	ErrorMessage                                 string `json:"errorMessage,omitempty"`
	DisbursedAmountInMinorUnits                  int64  `json:"disbursedAmountInMinorUnits,omitempty"`
	OutstandingPrincipalInMinorUnits             int64  `json:"outstandingPrincipalInMinorUnits,omitempty"`
	AvailableMarginAfterDisbursementInMinorUnits int64  `json:"availableMarginAfterDisbursementInMinorUnits,omitempty"`
}

// buildMarginFundingRequestHandler is FEATURES.md §2's "Margin funding /
// instant margin against pledged collateral": a real CASH ADVANCE, capped
// at whatever of the account's pledged collateral value
// (internal/marginpledge) isn't already drawn against
// (internal/marginfunding). Approval is a two-phase operation: the
// funding book reserves the principal FIRST (so a concurrent request
// can't double-spend the same capacity), then the actual cash is
// disbursed via a real balanced journal entry to `ledger`
// (internal/ledgerclient) — if that disbursement fails, the reservation
// is rolled back so the account's funding capacity isn't permanently
// (and incorrectly) consumed by a cash advance that never happened. See
// internal/marginfunding's package doc for the full "REAL money
// movement, illustrative interest" contract.
func buildMarginFundingRequestHandler(
	fundingBook *marginfunding.FundingBook,
	pledgeBook *marginpledge.PledgeBook,
	ledgerClient *ledgerclient.LedgerClient,
	preTradeRiskEngine *riskengine.PreTradeRiskEngine,
) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest marginFundingRequestWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed margin-funding request", http.StatusBadRequest)
			return
		}

		pledgedMarginValue := pledgeBook.TotalPledgedMarginValueForAccount(wireRequest.ClientAccountIdentifier)

		newOutstanding, requestError := fundingBook.RequestFunding(
			wireRequest.ClientAccountIdentifier,
			wireRequest.RequestedAmountInMinorUnits,
			pledgedMarginValue,
		)
		if requestError != nil {
			respondWithJson(responseWriter, http.StatusOK, marginFundingRequestWireResponse{
				WasFundingApproved: false,
				ErrorMessage:       requestError.Error(),
			})
			return
		}

		disbursementError := ledgerClient.PostMarginFundingDisbursementJournalEntry(
			wireRequest.ClientAccountIdentifier,
			wireRequest.RequestedAmountInMinorUnits,
			fmt.Sprintf("margin funding disbursement for %s", wireRequest.ClientAccountIdentifier),
		)
		if disbursementError != nil {
			// The reservation must not outlive a disbursement that never
			// actually happened — see the handler's doc comment above.
			log.Printf("MARGIN FUNDING DISBURSEMENT FAILED for %s — rolling back reservation: %v", wireRequest.ClientAccountIdentifier, disbursementError)
			fundingBook.RollbackReservation(wireRequest.ClientAccountIdentifier, wireRequest.RequestedAmountInMinorUnits)
			respondWithJson(responseWriter, http.StatusOK, marginFundingRequestWireResponse{
				WasFundingApproved: false,
				ErrorMessage:       fmt.Sprintf("margin funding was approved but the cash disbursement to the ledger failed: %v", disbursementError),
			})
			return
		}

		// Real cash landed in the account — reflect it in the local risk
		// cache immediately, the same pattern settleTradeAgainstLedgerAndLocalCache
		// uses for a trade fill.
		preTradeRiskEngine.AdjustAvailableMarginInMinorUnits(wireRequest.ClientAccountIdentifier, wireRequest.RequestedAmountInMinorUnits)
		availableMarginAfter, _ := preTradeRiskEngine.AvailableMarginInMinorUnits(wireRequest.ClientAccountIdentifier)

		respondWithJson(responseWriter, http.StatusOK, marginFundingRequestWireResponse{
			WasFundingApproved:                           true,
			DisbursedAmountInMinorUnits:                  wireRequest.RequestedAmountInMinorUnits,
			OutstandingPrincipalInMinorUnits:             newOutstanding,
			AvailableMarginAfterDisbursementInMinorUnits: availableMarginAfter,
		})
	}
}

// buildMarginFundingStatusHandler is the read-only lookup for
// GET /margin-funding?accountId=... — shows an account's currently
// outstanding margin-funding principal and remaining unutilized
// capacity against its currently pledged collateral.
func buildMarginFundingStatusHandler(
	fundingBook *marginfunding.FundingBook,
	pledgeBook *marginpledge.PledgeBook,
) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}

		fundingRecord := fundingBook.FundingRecordForAccount(accountIdentifier)
		pledgedMarginValue := pledgeBook.TotalPledgedMarginValueForAccount(accountIdentifier)
		remainingCapacity := pledgedMarginValue - fundingRecord.OutstandingPrincipalInMinorUnits
		if remainingCapacity < 0 {
			remainingCapacity = 0
		}

		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"clientAccountIdentifier":              accountIdentifier,
			"outstandingPrincipalInMinorUnits":     fundingRecord.OutstandingPrincipalInMinorUnits,
			"totalPledgedMarginValueInMinorUnits":  pledgedMarginValue,
			"remainingFundingCapacityInMinorUnits": remainingCapacity,
		})
	}
}

// buildOptionsChainHandler is FEATURES.md §3's "Real-time Options Chain"
// + "Greeks computed live per contract": GET /options/chain?
// underlyingSpotPrice=&expiryDate=&symbol=. See internal/optionschain's
// package doc for the full, loud "what's real (Greeks, PCR math) vs.
// synthetic (OI, Volume, assumed volatility)" contract. If quant-engine
// is unreachable, this returns a clear 502 error rather than crashing or
// hanging oms-gateway.
func buildOptionsChainHandler(quantEngineClient *quantengineclient.QuantEngineClient) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		underlyingSpotPriceParam := request.URL.Query().Get("underlyingSpotPrice")
		expiryDateParam := request.URL.Query().Get("expiryDate")
		symbol := request.URL.Query().Get("symbol")
		if underlyingSpotPriceParam == "" || expiryDateParam == "" || symbol == "" {
			http.Error(responseWriter, "missing underlyingSpotPrice, expiryDate, or symbol query parameter", http.StatusBadRequest)
			return
		}

		underlyingSpotPrice, parseSpotError := strconv.ParseFloat(underlyingSpotPriceParam, 64)
		if parseSpotError != nil {
			http.Error(responseWriter, "underlyingSpotPrice must be a valid number", http.StatusBadRequest)
			return
		}

		expiryDate, parseExpiryError := time.Parse("2006-01-02", expiryDateParam)
		if parseExpiryError != nil {
			http.Error(responseWriter, "expiryDate must be in YYYY-MM-DD format", http.StatusBadRequest)
			return
		}

		chain, chainError := optionschain.GenerateSyntheticOptionsChain(quantEngineClient, symbol, underlyingSpotPrice, expiryDate, time.Now())
		if chainError != nil {
			log.Printf("options chain generation failed for %s: %v", symbol, chainError)
			http.Error(responseWriter, fmt.Sprintf("could not generate options chain (is quant-engine running?): %v", chainError), http.StatusBadGateway)
			return
		}

		respondWithJson(responseWriter, http.StatusOK, chain)
	}
}

// configureAlgoLimitsWireRequest is the payload for
// POST /algo-limits/configure — FEATURES.md §7's per-strategy resource
// limits. maxOrdersPerSecond<=0 disables rate limiting for this
// strategy; maxNotionalPerDayInMinorUnits<=0 disables the daily notional
// cap — see internal/algolimits.StrategyLimitConfig's doc comment.
type configureAlgoLimitsWireRequest struct {
	StrategyIdentifier            string  `json:"strategyIdentifier"`
	MaxOrdersPerSecond            float64 `json:"maxOrdersPerSecond"`
	MaxNotionalPerDayInMinorUnits int64   `json:"maxNotionalPerDayInMinorUnits"`
}

func buildConfigureAlgoLimitsHandler(algoLimitsRegistry *algolimits.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest configureAlgoLimitsWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed algo-limits configure request", http.StatusBadRequest)
			return
		}
		if wireRequest.StrategyIdentifier == "" {
			http.Error(responseWriter, "missing strategyIdentifier", http.StatusBadRequest)
			return
		}

		algoLimitsRegistry.SetStrategyLimits(wireRequest.StrategyIdentifier, algolimits.StrategyLimitConfig{
			MaxOrdersPerSecond:            wireRequest.MaxOrdersPerSecond,
			MaxNotionalPerDayInMinorUnits: wireRequest.MaxNotionalPerDayInMinorUnits,
		})

		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"strategyIdentifier":            wireRequest.StrategyIdentifier,
			"maxOrdersPerSecond":            wireRequest.MaxOrdersPerSecond,
			"maxNotionalPerDayInMinorUnits": wireRequest.MaxNotionalPerDayInMinorUnits,
		})
	}
}

// buildAlgoLimitsStatusHandler is a read-only lookup of how much of a
// strategy's daily notional cap it has used so far today —
// GET /algo-limits?strategyId=...
func buildAlgoLimitsStatusHandler(algoLimitsRegistry *algolimits.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		strategyIdentifier := request.URL.Query().Get("strategyId")
		if strategyIdentifier == "" {
			http.Error(responseWriter, "missing strategyId query parameter", http.StatusBadRequest)
			return
		}

		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"strategyIdentifier":            strategyIdentifier,
			"notionalUsedTodayInMinorUnits": algoLimitsRegistry.NotionalUsedTodayInMinorUnits(strategyIdentifier, time.Now()),
		})
	}
}
