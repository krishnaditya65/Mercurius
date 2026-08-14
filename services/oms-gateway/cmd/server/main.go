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
	"sync"
	"time"

	"mercurius/omsgateway/internal/algolimits"
	"mercurius/omsgateway/internal/amoqueue"
	"mercurius/omsgateway/internal/audittrail"
	"mercurius/omsgateway/internal/autoliquidation"
	"mercurius/omsgateway/internal/backofficeclient"
	"mercurius/omsgateway/internal/basketorders"
	"mercurius/omsgateway/internal/chargescalculator"
	"mercurius/omsgateway/internal/connectivitykillswitch"
	"mercurius/omsgateway/internal/corporateactionexplainer"
	"mercurius/omsgateway/internal/dmagateway"
	"mercurius/omsgateway/internal/drip"
	"mercurius/omsgateway/internal/executionalgos"
	"mercurius/omsgateway/internal/exposurelimits"
	"mercurius/omsgateway/internal/fractionalshares"
	"mercurius/omsgateway/internal/httplogging"
	"mercurius/omsgateway/internal/idempotency"
	"mercurius/omsgateway/internal/impactcostestimator"
	"mercurius/omsgateway/internal/kycclient"
	"mercurius/omsgateway/internal/largeorderfriction"
	"mercurius/omsgateway/internal/ledgerclient"
	"mercurius/omsgateway/internal/liquiditybadge"
	"mercurius/omsgateway/internal/loanagainstsecurities"
	"mercurius/omsgateway/internal/marginengine"
	"mercurius/omsgateway/internal/marginfunding"
	"mercurius/omsgateway/internal/marginpledge"
	"mercurius/omsgateway/internal/marketsession"
	"mercurius/omsgateway/internal/marktomarket"
	"mercurius/omsgateway/internal/matchingengineclient"
	"mercurius/omsgateway/internal/metrics"
	"mercurius/omsgateway/internal/multilegoptions"
	"mercurius/omsgateway/internal/optionschain"
	"mercurius/omsgateway/internal/orders"
	"mercurius/omsgateway/internal/overtradingdetection"
	"mercurius/omsgateway/internal/papertrading"
	"mercurius/omsgateway/internal/payoffdiagram"
	"mercurius/omsgateway/internal/portfoliostresstest"
	"mercurius/omsgateway/internal/positions"
	"mercurius/omsgateway/internal/quantengineclient"
	"mercurius/omsgateway/internal/riskdisclosuregate"
	"mercurius/omsgateway/internal/riskengine"
	"mercurius/omsgateway/internal/securitieslendingborrowing"
	"mercurius/omsgateway/internal/sequencing"
	"mercurius/omsgateway/internal/strategyfollowing"
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
	// milliSharePaperPositionBook: FEATURES.md §17 fractional share
	// investing — see internal/fractionalshares' package doc. Real,
	// milli-share-precision positions, fed exclusively by paper-trading
	// fractional fills (matching-engine has no milli-share field, see
	// that package's honest scope boundary).
	milliSharePaperPositionBook := fractionalshares.NewMilliSharePositionBook()
	pledgeBook := marginpledge.NewPledgeBook()
	fundingBook := marginfunding.NewFundingBook()
	// loanAgainstSecuritiesBook: FEATURES.md §17 LAS — see
	// internal/loanagainstsecurities' package doc.
	loanAgainstSecuritiesBook := loanagainstsecurities.NewLoanBook()
	// securitiesLendingBorrowingDesk: FEATURES.md §15 SLB desk — see
	// internal/securitieslendingborrowing's package doc.
	securitiesLendingBorrowingDesk := securitieslendingborrowing.NewDesk()
	// dripToggleRegistry: FEATURES.md §17 DRIP auto-compounding toggle —
	// see internal/drip's package doc.
	dripToggleRegistry := drip.NewToggleRegistry()
	// algoLimitsRegistry: FEATURES.md §7 strategy resource limits &
	// circuit breakers. The default config (used by any strategyId that
	// never gets an explicit override via POST /algo-limits/configure)
	// is deliberately unlimited (0/0) — algolimits only constrains an
	// order that actually opts into a strategyId AND that strategy has
	// been configured with real limits, so this is fully backward
	// compatible with every pre-existing client that never sets
	// strategyIdentifier at all.
	algoLimitsRegistry := algolimits.NewRegistry(algolimits.StrategyLimitConfig{})
	// executionAlgoOrderRegistry: FEATURES.md §15 execution algos
	// (TWAP/VWAP/POV) — see internal/executionalgos' package doc for the
	// scheduling engine itself; this registry is the HTTP-layer glue that
	// tracks each created parent order's live scheduler by an
	// algoOrderId so PollDueSlices/OnVolumeObservation can be driven over
	// separate requests.
	executionAlgoOrderRegistry := newExecutionAlgoOrderRegistry()
	// strategyFollowingRegistry: FEATURES.md §11 social/copy-trading —
	// opt-in follow/unfollow ONLY, see internal/strategyfollowing's
	// package doc for the explicit "no order mirroring" scope boundary.
	strategyFollowingRegistry := strategyfollowing.NewRegistry()
	// markToMarketEngine: FEATURES.md §12 real-time MTM — see
	// internal/marktomarket's package doc for the push-endpoint and
	// leveraged-positions-only design choices.
	markToMarketEngine := marktomarket.NewMarkToMarketEngine()
	// exposureLimitsRegistry: FEATURES.md §12 per-user, per-segment
	// exposure limits. Default (unconfigured) is unconstrained for every
	// account/segment — see internal/exposurelimits' package doc.
	exposureLimitsRegistry := exposurelimits.NewLimitsRegistry()
	// connectivityKillSwitch: FEATURES.md §12 exchange-connectivity kill
	// switch. Auto-engages after 3 consecutive matching-engine hand-off
	// failures on the real order-submission path — see
	// internal/connectivitykillswitch's package doc for why that's a
	// genuine, already-existing integration point rather than a
	// synthetic heartbeat.
	connectivityKillSwitch := connectivitykillswitch.NewKillSwitch(3)
	// overtradingDetector: FEATURES.md §19 overtrading/revenge-trading
	// pattern detection with cool-down nudges — see
	// internal/overtradingdetection's package doc for the real pattern
	// logic and the honest scope boundary on what it can/can't detect
	// without a realized-P&L feed.
	overtradingDetector, overtradingDetectorError := overtradingdetection.NewDetector(overtradingdetection.DefaultThresholds())
	if overtradingDetectorError != nil {
		log.Fatalf("failed to construct overtrading detector: %v", overtradingDetectorError)
	}
	// riskDisclosureGate: FEATURES.md §19 mandatory F&O risk disclosure +
	// cooling-off flow. 24 hours is an illustrative, configurable default
	// — see internal/riskdisclosuregate's package doc.
	riskDisclosureGate, riskDisclosureGateError := riskdisclosuregate.NewGate(24 * time.Hour)
	if riskDisclosureGateError != nil {
		log.Fatalf("failed to construct risk disclosure gate: %v", riskDisclosureGateError)
	}
	// largeOrderFrictionTracker: FEATURES.md §21 large-order friction —
	// see internal/largeorderfriction's package doc.
	largeOrderFrictionTracker, largeOrderFrictionTrackerError := largeorderfriction.NewTracker(largeorderfriction.DefaultConfig())
	if largeOrderFrictionTrackerError != nil {
		log.Fatalf("failed to construct large-order friction tracker: %v", largeOrderFrictionTrackerError)
	}
	idempotencyStore := idempotency.NewIdempotencyStore()
	marketSession := marketsession.NewMarketSessionState()
	// squareOffCutoffConfig / squareOffReminderTracker: FEATURES.md §21
	// intraday auto square-off countdown timer + reminders — see
	// internal/marketsession/squareOffCountdown.go's package doc.
	squareOffCutoffConfig := marketsession.DefaultSquareOffCutoffConfig()
	squareOffReminderTracker, squareOffReminderTrackerError := marketsession.NewSquareOffReminderTracker(marketsession.DefaultSquareOffReminderThresholds)
	if squareOffReminderTrackerError != nil {
		log.Fatalf("failed to construct square-off reminder tracker: %v", squareOffReminderTrackerError)
	}
	afterMarketOrderQueue := amoqueue.NewAfterMarketOrderQueue()
	auditTrail := audittrail.NewAuditTrail()
	// corporateActionExplainerLog: FEATURES.md §21 corporate-action
	// explainer — see internal/corporateactionexplainer's package doc
	// for the honest "this is the explainer surface only, not real
	// corporate-actions processing (§14)" scope boundary.
	corporateActionExplainerLog := corporateactionexplainer.NewLog()
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
		markToMarketEngine:            markToMarketEngine,
		exposureLimitsRegistry:        exposureLimitsRegistry,
		connectivityKillSwitch:        connectivityKillSwitch,
		marketSession:                 marketSession,
		riskDisclosureGate:            riskDisclosureGate,
		largeOrderFrictionTracker:     largeOrderFrictionTracker,
		milliSharePaperPositionBook:   milliSharePaperPositionBook,
	}

	// FEATURES.md §12 auto-liquidation — the reducing-order submission
	// callback reuses the EXACT SAME processOrderSubmission pipeline
	// every other order path (HTTP /orders/submit, DMA gateway) uses, via
	// a plain Go closure — nothing duplicated. A liquidation order is
	// always a real MARKET SELL, so it fills immediately against
	// whatever's resting on the other side rather than risking never
	// triggering a limit-price liquidation at all.
	submitReducingOrderFunc := func(clientAccountIdentifier string, instrumentSymbol string, quantityToSell int64) (bool, error) {
		acknowledgement := processOrderSubmission(orderSubmissionDeps, orders.OrderSubmissionRequest{
			ClientAccountIdentifier:    clientAccountIdentifier,
			InstrumentSymbol:           instrumentSymbol,
			OrderSideIsBuyNotSell:      false,
			OrderIsMarketOrderNotLimit: true,
			OrderQuantity:              uint64(quantityToSell),
		})
		if !acknowledgement.WasOrderAccepted {
			return false, errors.New(acknowledgement.HumanReadableRejectionReason)
		}
		return true, nil
	}
	liquidationEngine, liquidationEngineError := autoliquidation.NewLiquidationEngine(autoliquidation.DefaultThresholds(), submitReducingOrderFunc)
	if liquidationEngineError != nil {
		log.Fatalf("failed to construct autoliquidation engine: %v", liquidationEngineError)
	}

	httpRequestMultiplexer := http.NewServeMux()
	httpRequestMultiplexer.HandleFunc("/health", healthCheckHandler)
	httpRequestMultiplexer.HandleFunc("/orders/submit", buildSubmitOrderHandler(orderSubmissionDeps, idempotencyStore, marketSession, afterMarketOrderQueue, overtradingDetector))
	httpRequestMultiplexer.HandleFunc("/overtrading-detection/status", buildOvertradingStatusHandler(overtradingDetector))
	httpRequestMultiplexer.HandleFunc("/risk-disclosure/acknowledge", buildAcknowledgeRiskDisclosureHandler(riskDisclosureGate))
	httpRequestMultiplexer.HandleFunc("/risk-disclosure/status", buildRiskDisclosureStatusHandler(riskDisclosureGate))
	httpRequestMultiplexer.HandleFunc("/market-session/square-off/countdown", buildSquareOffCountdownHandler(squareOffCutoffConfig))
	httpRequestMultiplexer.HandleFunc("/market-session/square-off/reminders", buildSquareOffRemindersHandler(squareOffCutoffConfig, squareOffReminderTracker))
	httpRequestMultiplexer.HandleFunc("/positions", buildPositionsHandler(positionBook))
	httpRequestMultiplexer.HandleFunc("/paper-positions", buildPositionsHandler(paperPositionBook))
	httpRequestMultiplexer.HandleFunc("/paper-positions/fractional", buildFractionalPaperPositionsHandler(milliSharePaperPositionBook))
	httpRequestMultiplexer.HandleFunc("/orders/cancel", buildCancelOrderHandler(matchingEngineClient, auditTrail))
	httpRequestMultiplexer.HandleFunc("/orders/cover-submit", buildCoverOrderHandler(orderSubmissionDeps))
	httpRequestMultiplexer.HandleFunc("/orders/status", buildOrderStatusHandler(matchingEngineClient))
	httpRequestMultiplexer.HandleFunc("/market-session/status", buildMarketSessionStatusHandler(marketSession, afterMarketOrderQueue))
	httpRequestMultiplexer.HandleFunc("/market-session/open", buildMarketSessionOpenHandler(marketSession, afterMarketOrderQueue, orderSubmissionDeps))
	httpRequestMultiplexer.HandleFunc("/market-session/close", buildMarketSessionCloseHandler(marketSession, auditTrail))
	httpRequestMultiplexer.HandleFunc("/market-session/set-phase", buildSetSessionPhaseHandler(marketSession))
	httpRequestMultiplexer.HandleFunc("/audit-trail", buildAuditTrailHandler(auditTrail))
	httpRequestMultiplexer.HandleFunc("/orders/reconcile", buildOrderReconcileHandler(idempotencyStore))
	httpRequestMultiplexer.HandleFunc("/positions/corporate-action-adjustments/apply", buildApplyCorporateActionAdjustmentHandler(positionBook, corporateActionExplainerLog))
	httpRequestMultiplexer.HandleFunc("/positions/corporate-action-adjustments", buildCorporateActionAdjustmentsHandler(corporateActionExplainerLog))
	httpRequestMultiplexer.HandleFunc("/orders/estimate-charges", buildEstimateChargesHandler())
	httpRequestMultiplexer.HandleFunc("/metrics", metrics.BuildMetricsHandler(metricsRegistry))
	httpRequestMultiplexer.HandleFunc("/margin-pledge/pledge", buildPledgeHoldingHandler(pledgeBook, positionBook, preTradeRiskEngine))
	httpRequestMultiplexer.HandleFunc("/margin-pledge/unpledge", buildUnpledgeHoldingHandler(pledgeBook, preTradeRiskEngine))
	httpRequestMultiplexer.HandleFunc("/margin-pledge/set-utilized-margin", buildSetUtilizedMarginHandler(pledgeBook))
	httpRequestMultiplexer.HandleFunc("/margin-pledge/holdings", buildPledgesForAccountHandler(pledgeBook))
	httpRequestMultiplexer.HandleFunc("/margin/calculate-span-exposure", buildCalculateSpanExposureMarginHandler())
	httpRequestMultiplexer.HandleFunc("/margin/calculate-portfolio-margin", buildCalculatePortfolioMarginHandler())
	httpRequestMultiplexer.HandleFunc("/margin-funding/request", buildMarginFundingRequestHandler(fundingBook, pledgeBook, ledgerClient, preTradeRiskEngine))
	httpRequestMultiplexer.HandleFunc("/margin-funding", buildMarginFundingStatusHandler(fundingBook, pledgeBook))
	httpRequestMultiplexer.HandleFunc("/margin-funding/interest-cost", buildMarginFundingInterestCostHandler(fundingBook))
	httpRequestMultiplexer.HandleFunc("/loan-against-securities/request", buildLoanAgainstSecuritiesRequestHandler(loanAgainstSecuritiesBook, pledgeBook, ledgerClient, preTradeRiskEngine))
	httpRequestMultiplexer.HandleFunc("/loan-against-securities/repay", buildLoanAgainstSecuritiesRepayHandler(loanAgainstSecuritiesBook, ledgerClient, preTradeRiskEngine))
	httpRequestMultiplexer.HandleFunc("/loan-against-securities", buildLoanAgainstSecuritiesStatusHandler(loanAgainstSecuritiesBook, pledgeBook))
	httpRequestMultiplexer.HandleFunc("/options/chain", buildOptionsChainHandler(quantEngineClient))
	httpRequestMultiplexer.HandleFunc("/algo-limits/configure", buildConfigureAlgoLimitsHandler(algoLimitsRegistry))
	httpRequestMultiplexer.HandleFunc("/algo-limits", buildAlgoLimitsStatusHandler(algoLimitsRegistry))
	httpRequestMultiplexer.HandleFunc("/strategies", buildListVerifiedStrategiesHandler(strategyFollowingRegistry))
	httpRequestMultiplexer.HandleFunc("/strategies/admin/verify", buildVerifyStrategyHandler(strategyFollowingRegistry))
	httpRequestMultiplexer.HandleFunc("/strategies/follow", buildFollowStrategyHandler(strategyFollowingRegistry))
	httpRequestMultiplexer.HandleFunc("/strategies/unfollow", buildUnfollowStrategyHandler(strategyFollowingRegistry))
	httpRequestMultiplexer.HandleFunc("/strategies/followers", buildStrategyFollowersHandler(strategyFollowingRegistry))
	httpRequestMultiplexer.HandleFunc("/strategies/following", buildAccountFollowingHandler(strategyFollowingRegistry))
	httpRequestMultiplexer.HandleFunc("/mark-to-market/price", buildSetMarkToMarketPriceHandler(markToMarketEngine))
	httpRequestMultiplexer.HandleFunc("/mark-to-market", buildMarkToMarketHandler(markToMarketEngine, fundingBook, pledgeBook))
	httpRequestMultiplexer.HandleFunc("/auto-liquidation/status", buildAutoLiquidationStatusHandler(fundingBook, pledgeBook))
	httpRequestMultiplexer.HandleFunc("/auto-liquidation/evaluate", buildAutoLiquidationEvaluateHandler(liquidationEngine, fundingBook, pledgeBook, positionBook, markToMarketEngine))
	httpRequestMultiplexer.HandleFunc("/exposure-limits/configure", buildConfigureExposureLimitsHandler(exposureLimitsRegistry))
	httpRequestMultiplexer.HandleFunc("/exposure-limits", buildExposureLimitsStatusHandler(exposureLimitsRegistry))
	httpRequestMultiplexer.HandleFunc("/connectivity-kill-switch/engage", buildEngageKillSwitchHandler(connectivityKillSwitch))
	httpRequestMultiplexer.HandleFunc("/connectivity-kill-switch/disengage", buildDisengageKillSwitchHandler(connectivityKillSwitch))
	httpRequestMultiplexer.HandleFunc("/connectivity-kill-switch/status", buildKillSwitchStatusHandler(connectivityKillSwitch))
	httpRequestMultiplexer.HandleFunc("/execution-algos/twap/create", buildCreateTwapExecutionAlgoHandler(executionAlgoOrderRegistry))
	httpRequestMultiplexer.HandleFunc("/execution-algos/vwap/create", buildCreateVwapExecutionAlgoHandler(executionAlgoOrderRegistry))
	httpRequestMultiplexer.HandleFunc("/execution-algos/pov/create", buildCreatePovExecutionAlgoHandler(executionAlgoOrderRegistry))
	httpRequestMultiplexer.HandleFunc("/execution-algos/poll", buildPollExecutionAlgoHandler(executionAlgoOrderRegistry))
	httpRequestMultiplexer.HandleFunc("/execution-algos/pov/observe-volume", buildObservePovVolumeHandler(executionAlgoOrderRegistry))
	httpRequestMultiplexer.HandleFunc("/execution-algos/status", buildExecutionAlgoStatusHandler(executionAlgoOrderRegistry))
	httpRequestMultiplexer.HandleFunc("/payoff-diagram/compute", buildComputePayoffDiagramHandler())
	httpRequestMultiplexer.HandleFunc("/multileg-options/execute", buildExecuteMultiLegOptionsHandler(orderSubmissionDeps))
	httpRequestMultiplexer.HandleFunc("/basket-orders/execute", buildExecuteBasketOrderHandler(orderSubmissionDeps))
	httpRequestMultiplexer.HandleFunc("/impact-cost/estimate", buildEstimateImpactCostHandler())
	httpRequestMultiplexer.HandleFunc("/liquidity-badge/compute", buildLiquidityBadgeHandler())
	httpRequestMultiplexer.HandleFunc("/portfolio-stress-test/compute", buildPortfolioStressTestHandler(positionBook))
	httpRequestMultiplexer.HandleFunc("/securities-lending/lend", buildLendSecurityHandler(securitiesLendingBorrowingDesk, positionBook))
	httpRequestMultiplexer.HandleFunc("/securities-lending/recall", buildRecallLendingHandler(securitiesLendingBorrowingDesk))
	httpRequestMultiplexer.HandleFunc("/securities-lending/borrow", buildBorrowSecurityHandler(securitiesLendingBorrowingDesk))
	httpRequestMultiplexer.HandleFunc("/securities-lending/return", buildReturnBorrowingHandler(securitiesLendingBorrowingDesk))
	httpRequestMultiplexer.HandleFunc("/securities-lending", buildSecuritiesLendingStatusHandler(securitiesLendingBorrowingDesk))
	httpRequestMultiplexer.HandleFunc("/drip/toggle", buildSetDripToggleHandler(dripToggleRegistry))
	httpRequestMultiplexer.HandleFunc("/drip/process-dividend", buildProcessDividendEventHandler(orderSubmissionDeps, dripToggleRegistry))

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

	// FEATURES.md §12 connectivity kill-switch — a real, independent
	// background connectivity prober. WITHOUT this, the auto-engaged AUTO
	// flag could never self-clear: once trading is halted, every new
	// order submission is rejected BEFORE it ever reaches
	// matchingEngineClient, so RecordConnectivityCheckResult would never
	// be called again to observe a genuine recovery. This goroutine calls
	// matchingEngineClient's real QueryOrderStatusAndAwaitResult (a cheap,
	// already-existing read-only method — a dial/timeout failure is a
	// genuine Go error from it, a legitimate "order not found" business
	// response is not) every 2 seconds, completely independent of order
	// flow, and feeds every result into the SAME kill switch — the real
	// health-check-driven signal FEATURES.md §12 asks for.
	go func() {
		probeTicker := time.NewTicker(2 * time.Second)
		defer probeTicker.Stop()
		for range probeTicker.C {
			_, probeError := matchingEngineClient.QueryOrderStatusAndAwaitResult("DEMO-EQ", 0)
			if autoEngagedNow := connectivityKillSwitch.RecordConnectivityCheckResult(probeError == nil); autoEngagedNow {
				log.Printf("connectivity kill switch AUTO-ENGAGED by background connectivity probe: matching-engine unreachable")
			}
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
	// markToMarketEngine: FEATURES.md §12 real-time MTM — see
	// main()'s construction comment and internal/marktomarket's package
	// doc.
	markToMarketEngine *marktomarket.MarkToMarketEngine
	// exposureLimitsRegistry: FEATURES.md §12 per-user, per-segment
	// exposure limits — see main()'s construction comment and
	// internal/exposurelimits' package doc.
	exposureLimitsRegistry *exposurelimits.LimitsRegistry
	// connectivityKillSwitch: FEATURES.md §12 exchange-connectivity kill
	// switch — see main()'s construction comment and
	// internal/connectivitykillswitch's package doc.
	connectivityKillSwitch *connectivitykillswitch.KillSwitch
	// marketSession: FEATURES.md §15 pre-market/post-market session
	// support — see internal/marketsession's sessionPhaseRules.go for the
	// real, distinct order-acceptance rules enforced below.
	marketSession *marketsession.MarketSessionState
	// riskDisclosureGate: FEATURES.md §19 mandatory F&O risk disclosure +
	// cooling-off gate — see internal/riskdisclosuregate's package doc.
	riskDisclosureGate *riskdisclosuregate.Gate
	// largeOrderFrictionTracker: FEATURES.md §21 large-order friction —
	// see internal/largeorderfriction's package doc.
	largeOrderFrictionTracker *largeorderfriction.Tracker
	// milliSharePaperPositionBook: FEATURES.md §17 fractional share
	// investing — see internal/fractionalshares' package doc.
	milliSharePaperPositionBook *fractionalshares.MilliSharePositionBook
}

func buildSubmitOrderHandler(
	dependencies orderSubmissionDependencies,
	idempotencyStore *idempotency.IdempotencyStore,
	marketSession *marketsession.MarketSessionState,
	afterMarketOrderQueue *amoqueue.AfterMarketOrderQueue,
	overtradingDetector *overtradingdetection.Detector,
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

		// FEATURES.md §17 fractional share investing: validated here,
		// before any gate runs — see
		// fractionalshares.ValidateMilliShareQuantity's doc comment for
		// the honest "paper trading only" scope boundary this enforces.
		if milliShareError := fractionalshares.ValidateMilliShareQuantity(
			incomingOrderSubmissionRequest.MilliShareQuantity, incomingOrderSubmissionRequest.IsPaperTradingOrder,
		); milliShareError != nil {
			http.Error(responseWriter, milliShareError.Error(), http.StatusBadRequest)
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

		// FEATURES.md §19 overtrading/revenge-trading pattern detection —
		// deliberately evaluated AFTER the order is fully processed and
		// deliberately NEVER changes WasOrderAccepted: this is a
		// non-blocking behavioral nudge, not a risk gate. Recorded/
		// evaluated for every real submission attempt (accepted or
		// rejected), keyed by the account, using the real wall clock —
		// the only appropriate place for that in this handler, mirroring
		// how internal/algolimits' own real wall-clock read is confined
		// to this same file.
		if overtradingDetector != nil && incomingOrderSubmissionRequest.ClientAccountIdentifier != "" {
			now := time.Now()
			overtradingDetector.RecordSubmission(incomingOrderSubmissionRequest.ClientAccountIdentifier, now)
			if nudge, _ := overtradingDetector.Evaluate(incomingOrderSubmissionRequest.ClientAccountIdentifier, now); nudge != nil {
				acknowledgement.OvertradingNudge = &orders.OvertradingNudge{
					PatternDetected:                 nudge.PatternDetected,
					HumanReadableMessage:            nudge.HumanReadableMessage,
					RecentOrderCount:                nudge.RecentOrderCount,
					BaselineOrderCountForSameWindow: nudge.BaselineOrderCountForSameWindow,
					CooldownExpiresAtTime:           nudge.CooldownExpiresAtTime,
				}
			}
		}

		idempotencyStore.CompleteClaimedKey(incomingOrderSubmissionRequest.IdempotencyKey, acknowledgement)
		respondWithJson(responseWriter, http.StatusOK, acknowledgement)
	}
}

// buildOvertradingStatusHandler serves GET /overtrading-detection/status?
// accountId=... — a pure read of an account's current cooldown state and
// recent order count, never mutating anything (see
// overtradingdetection.Detector.Status's own doc comment).
func buildOvertradingStatusHandler(overtradingDetector *overtradingdetection.Detector) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(responseWriter, "only GET is supported", http.StatusMethodNotAllowed)
			return
		}
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "accountId query parameter is required", http.StatusBadRequest)
			return
		}
		status := overtradingDetector.Status(accountIdentifier, time.Now())
		respondWithJson(responseWriter, http.StatusOK, status)
	}
}

// acknowledgeRiskDisclosureRequest is the payload for POST
// /risk-disclosure/acknowledge.
type acknowledgeRiskDisclosureRequest struct {
	ClientAccountIdentifier string `json:"clientAccountIdentifier"`
}

// buildAcknowledgeRiskDisclosureHandler serves POST
// /risk-disclosure/acknowledge — FEATURES.md §19's mandatory F&O risk
// disclosure. Records real acknowledgement state; does not itself place
// or validate any order.
func buildAcknowledgeRiskDisclosureHandler(gate *riskdisclosuregate.Gate) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var incomingRequest acknowledgeRiskDisclosureRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&incomingRequest); decodeError != nil {
			http.Error(responseWriter, "malformed risk disclosure acknowledgement payload", http.StatusBadRequest)
			return
		}
		if incomingRequest.ClientAccountIdentifier == "" {
			http.Error(responseWriter, "clientAccountIdentifier is required", http.StatusBadRequest)
			return
		}
		gate.Acknowledge(incomingRequest.ClientAccountIdentifier, time.Now())
		respondWithJson(responseWriter, http.StatusOK, gate.Status(incomingRequest.ClientAccountIdentifier))
	}
}

// buildRiskDisclosureStatusHandler serves GET /risk-disclosure/status?
// accountId=... — a pure read of an account's current F&O disclosure
// acknowledgement state.
func buildRiskDisclosureStatusHandler(gate *riskdisclosuregate.Gate) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(responseWriter, "only GET is supported", http.StatusMethodNotAllowed)
			return
		}
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "accountId query parameter is required", http.StatusBadRequest)
			return
		}
		respondWithJson(responseWriter, http.StatusOK, gate.Status(accountIdentifier))
	}
}

// parseOptionalNowQueryParam parses an optional `now` RFC3339 query
// parameter, defaulting to the real wall clock if absent/unparseable —
// lets a live client always ask "what's the countdown right now" while
// still letting a test/demo caller pin an exact instant, the same
// pattern internal/executionalgos' HTTP layer already uses for `now`.
func parseOptionalNowQueryParam(request *http.Request) time.Time {
	nowParam := request.URL.Query().Get("now")
	if nowParam == "" {
		return time.Now()
	}
	parsed, parseError := time.Parse(time.RFC3339, nowParam)
	if parseError != nil {
		return time.Now()
	}
	return parsed
}

// buildSquareOffCountdownHandler serves GET
// /market-session/square-off/countdown[?now=RFC3339] — FEATURES.md §21's
// real countdown-to-forced-closure timer. See
// internal/marketsession/squareOffCountdown.go's package doc for the
// honest scope boundary (this computes the countdown signal only; it
// never itself forces a closure).
func buildSquareOffCountdownHandler(cutoffConfig marketsession.SquareOffCutoffConfig) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(responseWriter, "only GET is supported", http.StatusMethodNotAllowed)
			return
		}
		now := parseOptionalNowQueryParam(request)
		cutoffTime := cutoffConfig.CutoffForDate(now)
		respondWithJson(responseWriter, http.StatusOK, marketsession.ComputeSquareOffCountdown(cutoffTime, now))
	}
}

// buildSquareOffRemindersHandler serves GET
// /market-session/square-off/reminders?accountId=...[&now=RFC3339] — the
// real, dedup'd reminder-eligibility check a frontend push notification
// would poll and consume. Each configured threshold (30/15/5 minutes by
// default) fires at most once per account per trading day's cutoff — see
// SquareOffReminderTracker.DueReminders' doc comment.
func buildSquareOffRemindersHandler(cutoffConfig marketsession.SquareOffCutoffConfig, tracker *marketsession.SquareOffReminderTracker) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(responseWriter, "only GET is supported", http.StatusMethodNotAllowed)
			return
		}
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "accountId query parameter is required", http.StatusBadRequest)
			return
		}
		now := parseOptionalNowQueryParam(request)
		cutoffTime := cutoffConfig.CutoffForDate(now)
		dueReminders := tracker.DueReminders(accountIdentifier, cutoffTime, now)
		dueReminderSecondsBeforeCutoff := make([]float64, len(dueReminders))
		for i, threshold := range dueReminders {
			dueReminderSecondsBeforeCutoff[i] = threshold.Seconds()
		}
		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"cutoffTime":                     cutoffTime,
			"dueReminderSecondsBeforeCutoff": dueReminderSecondsBeforeCutoff,
			"countdown":                      marketsession.ComputeSquareOffCountdown(cutoffTime, now),
		})
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
	// FEATURES.md §12 exchange-connectivity kill-switch — checked BEFORE
	// EVERYTHING else, including strategy limits: if trading is halted
	// (manually by an admin, or automatically after repeated
	// matching-engine connectivity failures — see
	// internal/connectivitykillswitch's package doc), every new order
	// submission is rejected immediately with a clear "trading halted"
	// error, full stop. Cancellation is deliberately NOT gated here — see
	// buildCancelOrderHandler, which never consults this switch at all,
	// per FEATURES.md §12's "existing pending orders can still be
	// cancelled".
	if dependencies.connectivityKillSwitch.IsTradingHalted() {
		dependencies.auditTrail.Append(audittrail.Entry{
			EventType:               audittrail.EventOrderRejected,
			ClientAccountIdentifier: incomingOrderSubmissionRequest.ClientAccountIdentifier,
			InstrumentSymbol:        incomingOrderSubmissionRequest.InstrumentSymbol,
			DetailMessage:           "TRADING_HALTED",
		})
		return orders.OrderAcknowledgementResponse{
			WasOrderAccepted:               false,
			HumanReadableRejectionReason:   "Trading is currently halted (kill switch engaged) — new order submissions are being rejected. Existing orders can still be cancelled.",
			MachineReadableRejectionReason: "TRADING_HALTED",
		}
	}

	// FEATURES.md §15 pre-market/post-market session rules — checked
	// right after the kill switch, before strategy limits: a real,
	// distinct order-acceptance rule (only plain LIMIT orders during
	// PRE_MARKET/POST_MARKET) enforced for every order path that reuses
	// processOrderSubmission. REGULAR and CLOSED are both no-ops for this
	// gate — see internal/marketsession's sessionPhaseRules.go doc for
	// why CLOSED's real rejection stays owned by the pre-existing
	// isMarketOpen/AMO mechanism, not duplicated here.
	if dependencies.marketSession != nil {
		sessionRuleError := marketsession.ValidateOrderAgainstSessionPhase(
			dependencies.marketSession.CurrentSessionPhase(),
			marketsession.OrderShapeForSessionRules{
				OrderIsMarketOrderNotLimit: incomingOrderSubmissionRequest.OrderIsMarketOrderNotLimit,
				OrderIsStopLossVariant:     incomingOrderSubmissionRequest.OrderIsStopLossVariant,
				OrderExecutionType:         incomingOrderSubmissionRequest.OrderExecutionType,
			},
			incomingOrderSubmissionRequest.OrderIsAfterMarketOrder,
		)
		if sessionRuleError != nil {
			dependencies.auditTrail.Append(audittrail.Entry{
				EventType:               audittrail.EventOrderRejected,
				ClientAccountIdentifier: incomingOrderSubmissionRequest.ClientAccountIdentifier,
				InstrumentSymbol:        incomingOrderSubmissionRequest.InstrumentSymbol,
				DetailMessage:           fmt.Sprintf("SESSION_PHASE_RULE_VIOLATION: %v", sessionRuleError),
			})
			return orders.OrderAcknowledgementResponse{
				WasOrderAccepted:               false,
				HumanReadableRejectionReason:   sessionRuleError.Error(),
				MachineReadableRejectionReason: "SESSION_PHASE_RULE_VIOLATION",
			}
		}
	}

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

	// FEATURES.md §12 per-user, per-segment exposure limits — a real
	// pre-trade check alongside internal/riskengine's own margin check,
	// run right after the strategy-limits gate and before KYC (same
	// rationale: no reason to touch KYC/freeze/margin for an order the
	// risk team has already capped out on exposure grounds). See
	// internal/exposurelimits' package doc for the "cumulative
	// reservation, not live net position" and "illustrative segment"
	// design choices. An account/segment with no configured limit is
	// completely unaffected — fully backward compatible with every
	// pre-existing client and test.
	exposureSegment := exposurelimits.ClassifySegment(incomingOrderSubmissionRequest.InstrumentSymbol)
	exposureNotionalForLimitCheck := incomingOrderSubmissionRequest.LimitPriceInMinorUnits * int64(incomingOrderSubmissionRequest.OrderQuantity)
	if exposureError := dependencies.exposureLimitsRegistry.CheckAndReserveExposure(
		incomingOrderSubmissionRequest.ClientAccountIdentifier,
		exposureSegment,
		exposureNotionalForLimitCheck,
	); exposureError != nil {
		machineReadableReason := "ACCOUNT_EXPOSURE_LIMIT_EXCEEDED"
		if errors.Is(exposureError, exposurelimits.ErrSegmentExposureLimitExceeded) {
			machineReadableReason = "SEGMENT_EXPOSURE_LIMIT_EXCEEDED"
		}
		dependencies.auditTrail.Append(audittrail.Entry{
			EventType:               audittrail.EventOrderRejected,
			ClientAccountIdentifier: incomingOrderSubmissionRequest.ClientAccountIdentifier,
			InstrumentSymbol:        incomingOrderSubmissionRequest.InstrumentSymbol,
			DetailMessage:           fmt.Sprintf("%s: %v", machineReadableReason, exposureError),
		})
		return orders.OrderAcknowledgementResponse{
			WasOrderAccepted:               false,
			HumanReadableRejectionReason:   fmt.Sprintf("This order was blocked by a risk-team-configured exposure limit: %v", exposureError),
			MachineReadableRejectionReason: machineReadableReason,
		}
	}

	// FEATURES.md §21 large-order friction — checked right after
	// exposure limits, before KYC: a brief confirm-with-context step for
	// an order unusually large relative to the account's OWN historical
	// average order size. Soft-rejected (never a permanent block) on the
	// first submission; a resubmission with confirmedLargeOrder:true
	// proceeds. See internal/largeorderfriction's package doc.
	if dependencies.largeOrderFrictionTracker != nil {
		orderNotionalForFrictionCheck := incomingOrderSubmissionRequest.LimitPriceInMinorUnits * int64(incomingOrderSubmissionRequest.OrderQuantity)
		frictionResult := dependencies.largeOrderFrictionTracker.EvaluateOrder(
			incomingOrderSubmissionRequest.ClientAccountIdentifier, orderNotionalForFrictionCheck, 0,
		)
		if frictionResult.RequiresConfirmation && !incomingOrderSubmissionRequest.ConfirmedLargeOrder {
			dependencies.auditTrail.Append(audittrail.Entry{
				EventType:               audittrail.EventOrderRejected,
				ClientAccountIdentifier: incomingOrderSubmissionRequest.ClientAccountIdentifier,
				InstrumentSymbol:        incomingOrderSubmissionRequest.InstrumentSymbol,
				DetailMessage:           fmt.Sprintf("LARGE_ORDER_CONFIRMATION_REQUIRED: %s", frictionResult.Reason),
			})
			return orders.OrderAcknowledgementResponse{
				WasOrderAccepted: false,
				HumanReadableRejectionReason: fmt.Sprintf(
					"%s. If you're sure, resubmit this exact order with confirmedLargeOrder:true.",
					frictionResult.Reason,
				),
				MachineReadableRejectionReason: "LARGE_ORDER_CONFIRMATION_REQUIRED",
			}
		}
		// Record every order that actually proceeds past this gate
		// (confirmed-large or never-flagged) into the real baseline —
		// see Tracker.RecordOrderNotional's doc comment.
		dependencies.largeOrderFrictionTracker.RecordOrderNotional(incomingOrderSubmissionRequest.ClientAccountIdentifier, orderNotionalForFrictionCheck)
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

	// FEATURES.md §19 mandatory F&O risk disclosure + cooling-off gate —
	// checked right after the pledged-holding gate, before the pre-trade
	// risk/margin check: an account that hasn't acknowledged the F&O
	// risk disclosure (or hasn't waited out the cooling-off period since
	// acknowledging) shouldn't have its margin evaluated for an F&O
	// order it's not yet allowed to place at all. A no-op for every
	// equity order and for every F&O order from an account that already
	// placed its first F&O order — see internal/riskdisclosuregate's
	// package doc.
	if dependencies.riskDisclosureGate != nil {
		isFnoInstrument := exposurelimits.ClassifySegment(incomingOrderSubmissionRequest.InstrumentSymbol) == exposurelimits.SegmentFuturesAndOptions
		if disclosureError := dependencies.riskDisclosureGate.CheckFirstFnoOrderGate(
			incomingOrderSubmissionRequest.ClientAccountIdentifier, isFnoInstrument, time.Now(),
		); disclosureError != nil {
			dependencies.auditTrail.Append(audittrail.Entry{
				EventType:               audittrail.EventOrderRejected,
				ClientAccountIdentifier: incomingOrderSubmissionRequest.ClientAccountIdentifier,
				InstrumentSymbol:        incomingOrderSubmissionRequest.InstrumentSymbol,
				DetailMessage:           fmt.Sprintf("FNO_RISK_DISCLOSURE_REQUIRED: %v", disclosureError),
			})
			return orders.OrderAcknowledgementResponse{
				WasOrderAccepted:               false,
				HumanReadableRejectionReason:   fmt.Sprintf("This account cannot place its first F&O order yet: %v", disclosureError),
				MachineReadableRejectionReason: "FNO_RISK_DISCLOSURE_REQUIRED",
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
	// FEATURES.md §17 fractional share investing: when MilliShareQuantity
	// is set, it — not the whole-share OrderQuantity — is the
	// authoritative order size, so the risk check's notional must be
	// computed with milli-share-aware integer math
	// (fractionalshares.NotionalInMinorUnits), not the whole-share
	// formula above. See that function's doc comment for the exact
	// round-half-up integer arithmetic (no float).
	if incomingOrderSubmissionRequest.MilliShareQuantity != nil {
		orderNotionalValueInMinorUnits = fractionalshares.NotionalInMinorUnits(
			incomingOrderSubmissionRequest.LimitPriceInMinorUnits, *incomingOrderSubmissionRequest.MilliShareQuantity,
		)
	}

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

	// The order has now genuinely passed every gate (including the F&O
	// risk-disclosure gate above) and been sequenced — record this as
	// the account's first F&O order if it is one, permanently exempting
	// it from the gate going forward. Deliberately recorded here (once
	// risk-approved), not earlier in CheckFirstFnoOrderGate itself, so a
	// check that later turns out to be for an order that fails
	// downstream (e.g. matching-engine hand-off) still correctly counts
	// — this order WAS accepted by the OMS, which is what "first F&O
	// order" means for this gate's purpose.
	if dependencies.riskDisclosureGate != nil && exposurelimits.ClassifySegment(incomingOrderSubmissionRequest.InstrumentSymbol) == exposurelimits.SegmentFuturesAndOptions {
		dependencies.riskDisclosureGate.RecordFirstFnoOrderPlaced(incomingOrderSubmissionRequest.ClientAccountIdentifier, time.Now())
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
		// FEATURES.md §17 fractional share investing: a fractional paper
		// order (MilliShareQuantity set — already validated as
		// paper-only by fractionalshares.ValidateMilliShareQuantity in
		// buildSubmitOrderHandler) takes a COMPLETELY SEPARATE simulated-
		// fill path and updates dependencies.milliSharePaperPositionBook
		// instead of the whole-share paperPositionBook, so a fractional
		// position can never silently corrupt whole-share position
		// tracking.
		if incomingOrderSubmissionRequest.MilliShareQuantity != nil {
			fractionalFill, fractionalSimulationError := papertrading.SimulateFractionalFill(incomingOrderSubmissionRequest)
			if fractionalSimulationError != nil {
				acknowledgement.PaperOrderSimulationError = fractionalSimulationError.Error()
				dependencies.auditTrail.Append(audittrail.Entry{
					EventType:               audittrail.EventPaperOrderSimulationFailed,
					ClientAccountIdentifier: incomingOrderSubmissionRequest.ClientAccountIdentifier,
					InstrumentSymbol:        incomingOrderSubmissionRequest.InstrumentSymbol,
					DetailMessage:           fractionalSimulationError.Error(),
				})
				return acknowledgement
			}

			buyingAccountId, sellingAccountId := incomingOrderSubmissionRequest.ClientAccountIdentifier, papertrading.SyntheticCounterpartyAccountIdentifier
			if !incomingOrderSubmissionRequest.OrderSideIsBuyNotSell {
				buyingAccountId, sellingAccountId = sellingAccountId, buyingAccountId
			}

			dependencies.milliSharePaperPositionBook.ApplyFractionalFill(
				buyingAccountId, sellingAccountId, incomingOrderSubmissionRequest.InstrumentSymbol, fractionalFill.ExecutedMilliShareQuantity,
			)
			acknowledgement.FractionalTradeExecutionEvents = append(acknowledgement.FractionalTradeExecutionEvents, orders.FractionalTradeExecutionSummary{
				BuyingClientAccountId:      buyingAccountId,
				SellingClientAccountId:     sellingAccountId,
				ExecutedPriceInMinorUnits:  fractionalFill.ExecutedPriceInMinorUnits,
				ExecutedMilliShareQuantity: fractionalFill.ExecutedMilliShareQuantity,
			})
			dependencies.auditTrail.Append(audittrail.Entry{
				EventType:               audittrail.EventPaperOrderFilled,
				ClientAccountIdentifier: incomingOrderSubmissionRequest.ClientAccountIdentifier,
				InstrumentSymbol:        incomingOrderSubmissionRequest.InstrumentSymbol,
				DetailMessage: fmt.Sprintf(
					"SIMULATED FRACTIONAL fill %d milli-share-units @ %d (NOT a real matching-engine trade, NOT settled to ledger)",
					fractionalFill.ExecutedMilliShareQuantity, fractionalFill.ExecutedPriceInMinorUnits,
				),
			})
			return acknowledgement
		}

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

	// FEATURES.md §12 exchange-connectivity kill-switch's real
	// health-check-driven auto-trigger: this is a genuine connectivity
	// signal (a TRANSPORT failure — handoffError, e.g. connection
	// refused/timeout), deliberately distinct from a legitimate business
	// rejection matching-engine itself returns
	// (matchingEngineResponse.ErrorMessage) which is not a connectivity
	// problem at all and must not count against the failure streak. See
	// internal/connectivitykillswitch's package doc.
	if autoEngagedNow := dependencies.connectivityKillSwitch.RecordConnectivityCheckResult(handoffError == nil); autoEngagedNow {
		log.Printf("connectivity kill switch AUTO-ENGAGED: matching-engine hand-off failed too many times in a row — new order submissions are now halted until it recovers or an admin intervenes")
	}

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
			// FEATURES.md §12 real-time MTM: every real fill also folds
			// into internal/marktomarket's cost-basis tracking — the SAME
			// fill event positionBook just consumed, plus the executed
			// price marktomarket additionally needs. See that package's
			// doc for why a live position with no pushed market price yet
			// simply won't appear in GET /mark-to-market until one is.
			dependencies.markToMarketEngine.ApplyFill(
				tradeExecutionWireEvent.BuyingClientAccountId,
				tradeExecutionWireEvent.SellingClientAccountId,
				incomingOrderSubmissionRequest.InstrumentSymbol,
				tradeExecutionWireEvent.ExecutedQuantity,
				tradeExecutionWireEvent.ExecutedPriceInMinorUnits,
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

// buildApplyCorporateActionAdjustmentHandler serves POST
// /positions/corporate-action-adjustments/apply — FEATURES.md §21's
// corporate-action explainer. See
// internal/corporateactionexplainer's package doc for the honest scope
// boundary: this applies a caller-supplied (today always
// manual/synthetic — no real corporate-actions feed exists, that's §14)
// quantity/average-price adjustment to the account's REAL position
// (via positions.PositionBook.SetPositionDirectly) and returns a real,
// accurate one-line explanation of what changed and why.
func buildApplyCorporateActionAdjustmentHandler(
	positionBook *positions.PositionBook,
	corporateActionExplainerLog *corporateactionexplainer.Log,
) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var event corporateactionexplainer.AdjustmentEvent
		if decodeError := json.NewDecoder(request.Body).Decode(&event); decodeError != nil {
			http.Error(responseWriter, "malformed corporate action adjustment payload", http.StatusBadRequest)
			return
		}

		logged, explainError := corporateActionExplainerLog.RecordAdjustment(event)
		if explainError != nil {
			http.Error(responseWriter, explainError.Error(), http.StatusBadRequest)
			return
		}

		// Only mutate the real position once the event is validated and
		// explained -- an invalid event never touches positionBook at all.
		positionBook.SetPositionDirectly(event.ClientAccountIdentifier, event.InstrumentSymbol, event.QuantityAfter)

		respondWithJson(responseWriter, http.StatusOK, logged)
	}
}

// buildCorporateActionAdjustmentsHandler serves GET
// /positions/corporate-action-adjustments[?accountId=...] — a pure read
// of the real, append-only corporate-action explainer log.
func buildCorporateActionAdjustmentsHandler(corporateActionExplainerLog *corporateactionexplainer.Log) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier != "" {
			respondWithJson(responseWriter, http.StatusOK, corporateActionExplainerLog.EntriesForAccount(accountIdentifier))
			return
		}
		respondWithJson(responseWriter, http.StatusOK, corporateActionExplainerLog.AllEntries())
	}
}

// buildFractionalPaperPositionsHandler serves GET
// /paper-positions/fractional?accountId=... — FEATURES.md §17's
// fractional share investing. Mirrors buildPositionsHandler but reads
// the milli-share-precision paper position book — see
// internal/fractionalshares' package doc. Also reports each
// instrument's whole-share/remaining-milli-unit breakdown for a
// friendlier client display (e.g. "3 shares + 250 milli-units" reads as
// "3.250 shares").
func buildFractionalPaperPositionsHandler(milliSharePaperPositionBook *fractionalshares.MilliSharePositionBook) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}

		milliSharePositions := milliSharePaperPositionBook.PositionsForAccount(accountIdentifier)
		formattedPositions := make(map[string]map[string]int64, len(milliSharePositions))
		for instrumentSymbol, netMilliShareQuantity := range milliSharePositions {
			wholeShares, remainingMilliUnits := fractionalshares.FormatWholeAndMilliParts(netMilliShareQuantity)
			formattedPositions[instrumentSymbol] = map[string]int64{
				"netMilliShareQuantity": netMilliShareQuantity,
				"wholeShares":           wholeShares,
				"remainingMilliUnits":   remainingMilliUnits,
			}
		}

		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"accountIdentifier":            accountIdentifier,
			"milliShareUnitsPerWholeShare": fractionalshares.MilliShareUnitsPerWholeShare,
			"positionsByInstrumentSymbol":  formattedPositions,
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
			"sessionPhase":            marketSession.CurrentSessionPhase(),
		})
	}
}

// buildSetSessionPhaseHandler is POST /market-session/set-phase —
// FEATURES.md §15's pre-market/post-market/extended-hours support. See
// internal/marketsession's sessionPhaseRules.go for the real,
// distinct order-acceptance rules this phase now enforces (checked in
// processOrderSubmission) and the honest note on why this is
// deliberately independent of the pre-existing isMarketOpen/AMO gate.
func buildSetSessionPhaseHandler(marketSession *marketsession.MarketSessionState) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest struct {
			Phase string `json:"phase"`
		}
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed set-phase request", http.StatusBadRequest)
			return
		}
		if setError := marketSession.SetSessionPhase(marketsession.SessionPhase(wireRequest.Phase)); setError != nil {
			http.Error(responseWriter, setError.Error(), http.StatusBadRequest)
			return
		}
		respondWithJson(responseWriter, http.StatusOK, map[string]any{"sessionPhase": marketSession.CurrentSessionPhase()})
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

// buildOrderReconcileHandler serves GET /orders/reconcile?idempotencyKey=...
// — FEATURES.md §21's "idempotent order status with WebSocket-reconnect
// reconciliation": a client that dropped its connection mid-submission
// (or just wants to double-check after a reconnect) can ask "what
// happened to the order I submitted with this idempotency key" WITHOUT
// resubmitting the order body — a pure, non-blocking read over
// internal/idempotency's real claim/complete state. See
// idempotency.IdempotencyStore.Reconcile's doc comment: repeated calls
// always return the identical answer once COMPLETED, the same
// idempotency guarantee a full resubmission would have provided.
func buildOrderReconcileHandler(idempotencyStore *idempotency.IdempotencyStore) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(responseWriter, "only GET is supported", http.StatusMethodNotAllowed)
			return
		}
		idempotencyKey := request.URL.Query().Get("idempotencyKey")
		if idempotencyKey == "" {
			http.Error(responseWriter, "idempotencyKey query parameter is required", http.StatusBadRequest)
			return
		}
		respondWithJson(responseWriter, http.StatusOK, idempotencyStore.Reconcile(idempotencyKey))
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

// buildCalculatePortfolioMarginHandler is POST
// /margin/calculate-portfolio-margin — FEATURES.md §15's portfolio
// margining / cross-margining. See
// internal/marginengine's portfolioCrossMargining.go for the real
// netting-benefit formula and its loud illustrative-correlation-table
// warning.
func buildCalculatePortfolioMarginHandler() http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest struct {
			Positions []marginengine.Position `json:"positions"`
		}
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed portfolio margin request", http.StatusBadRequest)
			return
		}
		result, calculationError := marginengine.CalculatePortfolioMargin(wireRequest.Positions)
		if calculationError != nil {
			http.Error(responseWriter, calculationError.Error(), http.StatusBadRequest)
			return
		}
		respondWithJson(responseWriter, http.StatusOK, result)
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

		// FEATURES.md §21 live interest cost calculator — records the
		// real wall-clock instant this account's interest clock starts
		// (a no-op if it already has one, e.g. a second draw on top of
		// an existing outstanding loan — see FundingBook's doc comment
		// on that honest simplification).
		fundingBook.RecordDisbursementStartTime(wireRequest.ClientAccountIdentifier, time.Now())

		respondWithJson(responseWriter, http.StatusOK, marginFundingRequestWireResponse{
			WasFundingApproved:                           true,
			DisbursedAmountInMinorUnits:                  wireRequest.RequestedAmountInMinorUnits,
			OutstandingPrincipalInMinorUnits:             newOutstanding,
			AvailableMarginAfterDisbursementInMinorUnits: availableMarginAfter,
		})
	}
}

// buildMarginFundingInterestCostHandler serves GET
// /margin-funding/interest-cost?accountId=...[&now=RFC3339]
// [&projectDays=N] — FEATURES.md §21's live interest cost calculator:
// real "cost so far" AND "projected cost if held N more days" figures,
// using the exact same illustrative simple-interest formula
// CalculateIllustrativeAccruedInterest already had. An account with no
// outstanding principal (or no recorded disbursement start time) gets an
// explicit all-zero snapshot rather than an error — there's simply
// nothing accruing.
func buildMarginFundingInterestCostHandler(fundingBook *marginfunding.FundingBook) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(responseWriter, "only GET is supported", http.StatusMethodNotAllowed)
			return
		}
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "accountId query parameter is required", http.StatusBadRequest)
			return
		}
		now := parseOptionalNowQueryParam(request)

		projectDays := int64(30)
		if projectDaysParam := request.URL.Query().Get("projectDays"); projectDaysParam != "" {
			if parsed, parseError := strconv.ParseInt(projectDaysParam, 10, 64); parseError == nil {
				projectDays = parsed
			}
		}

		principal := fundingBook.OutstandingPrincipalInMinorUnits(accountIdentifier)
		disbursedAtTime, hasDisbursement := fundingBook.DisbursementStartTime(accountIdentifier)
		if !hasDisbursement || principal <= 0 {
			respondWithJson(responseWriter, http.StatusOK, marginfunding.LiveInterestCostSnapshot{
				OutstandingPrincipalInMinorUnits: principal,
				AsOfTime:                         now,
				AdditionalDaysProjected:          projectDays,
			})
			return
		}

		snapshot := marginfunding.BuildLiveInterestCostSnapshot(principal, disbursedAtTime, now, projectDays)
		respondWithJson(responseWriter, http.StatusOK, snapshot)
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

// ---------------------------------------------------------------------
// Loan Against Securities (LAS) — FEATURES.md §17. See
// internal/loanagainstsecurities' package doc for the full contract: a
// genuinely distinct, longer-tenure loan product from margin funding
// (stricter loan-to-value cap), same two-phase reserve-then-disburse-
// then-rollback-on-failure flow, real disbursement via
// internal/ledgerclient.
// ---------------------------------------------------------------------

type loanAgainstSecuritiesRequestWireRequest struct {
	ClientAccountIdentifier     string `json:"clientAccountIdentifier"`
	RequestedAmountInMinorUnits int64  `json:"requestedAmountInMinorUnits"`
	TenureInMonths              uint32 `json:"tenureInMonths"`
}

type loanAgainstSecuritiesRequestWireResponse struct {
	WasLoanApproved                              bool   `json:"wasLoanApproved"`
	ErrorMessage                                 string `json:"errorMessage,omitempty"`
	DisbursedAmountInMinorUnits                  int64  `json:"disbursedAmountInMinorUnits,omitempty"`
	OutstandingPrincipalInMinorUnits             int64  `json:"outstandingPrincipalInMinorUnits,omitempty"`
	AvailableMarginAfterDisbursementInMinorUnits int64  `json:"availableMarginAfterDisbursementInMinorUnits,omitempty"`
}

// buildLoanAgainstSecuritiesRequestHandler is POST
// /loan-against-securities/request — mirrors
// buildMarginFundingRequestHandler's two-phase reserve-then-disburse-
// then-rollback-on-failure flow exactly, against
// internal/loanagainstsecurities.LoanBook's stricter loan-to-value cap
// instead of margin funding's full-pledged-value cap.
func buildLoanAgainstSecuritiesRequestHandler(
	loanBook *loanagainstsecurities.LoanBook,
	pledgeBook *marginpledge.PledgeBook,
	ledgerClient *ledgerclient.LedgerClient,
	preTradeRiskEngine *riskengine.PreTradeRiskEngine,
) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest loanAgainstSecuritiesRequestWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed loan-against-securities request", http.StatusBadRequest)
			return
		}

		pledgedMarginValue := pledgeBook.TotalPledgedMarginValueForAccount(wireRequest.ClientAccountIdentifier)

		newOutstanding, requestError := loanBook.RequestLoan(
			wireRequest.ClientAccountIdentifier,
			wireRequest.RequestedAmountInMinorUnits,
			pledgedMarginValue,
			wireRequest.TenureInMonths,
		)
		if requestError != nil {
			respondWithJson(responseWriter, http.StatusOK, loanAgainstSecuritiesRequestWireResponse{
				WasLoanApproved: false,
				ErrorMessage:    requestError.Error(),
			})
			return
		}

		disbursementError := ledgerClient.PostLoanAgainstSecuritiesDisbursementJournalEntry(
			wireRequest.ClientAccountIdentifier,
			wireRequest.RequestedAmountInMinorUnits,
			fmt.Sprintf("LAS disbursement for %s (tenure %d months)", wireRequest.ClientAccountIdentifier, wireRequest.TenureInMonths),
		)
		if disbursementError != nil {
			log.Printf("LAS DISBURSEMENT FAILED for %s — rolling back reservation: %v", wireRequest.ClientAccountIdentifier, disbursementError)
			loanBook.RollbackReservation(wireRequest.ClientAccountIdentifier, wireRequest.RequestedAmountInMinorUnits)
			respondWithJson(responseWriter, http.StatusOK, loanAgainstSecuritiesRequestWireResponse{
				WasLoanApproved: false,
				ErrorMessage:    fmt.Sprintf("loan was approved but the cash disbursement to the ledger failed: %v", disbursementError),
			})
			return
		}

		preTradeRiskEngine.AdjustAvailableMarginInMinorUnits(wireRequest.ClientAccountIdentifier, wireRequest.RequestedAmountInMinorUnits)
		availableMarginAfter, _ := preTradeRiskEngine.AvailableMarginInMinorUnits(wireRequest.ClientAccountIdentifier)

		respondWithJson(responseWriter, http.StatusOK, loanAgainstSecuritiesRequestWireResponse{
			WasLoanApproved:                              true,
			DisbursedAmountInMinorUnits:                  wireRequest.RequestedAmountInMinorUnits,
			OutstandingPrincipalInMinorUnits:             newOutstanding,
			AvailableMarginAfterDisbursementInMinorUnits: availableMarginAfter,
		})
	}
}

type loanAgainstSecuritiesRepayWireRequest struct {
	ClientAccountIdentifier string `json:"clientAccountIdentifier"`
	AmountInMinorUnits      int64  `json:"amountInMinorUnits"`
}

// buildLoanAgainstSecuritiesRepayHandler is POST
// /loan-against-securities/repay — posts a real repayment journal entry
// (debiting the account's real ledger balance) and pays down real
// outstanding LAS principal. Unlike internal/marginfunding (whose own
// repayment endpoint is a documented gap), this ships a real HTTP route.
func buildLoanAgainstSecuritiesRepayHandler(
	loanBook *loanagainstsecurities.LoanBook,
	ledgerClient *ledgerclient.LedgerClient,
	preTradeRiskEngine *riskengine.PreTradeRiskEngine,
) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest loanAgainstSecuritiesRepayWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed loan-against-securities repayment request", http.StatusBadRequest)
			return
		}

		newOutstanding, repayError := loanBook.RepayLoan(wireRequest.ClientAccountIdentifier, wireRequest.AmountInMinorUnits)
		if repayError != nil {
			http.Error(responseWriter, repayError.Error(), http.StatusBadRequest)
			return
		}

		ledgerError := ledgerClient.PostLoanAgainstSecuritiesRepaymentJournalEntry(
			wireRequest.ClientAccountIdentifier,
			wireRequest.AmountInMinorUnits,
			fmt.Sprintf("LAS repayment for %s", wireRequest.ClientAccountIdentifier),
		)
		if ledgerError != nil {
			// The repayment was already applied to the loan book above —
			// undo it (re-add the principal) since the real cash never
			// actually left the client's ledger balance.
			loanBook.RestorePrincipalAfterFailedRepaymentLedgerPosting(wireRequest.ClientAccountIdentifier, wireRequest.AmountInMinorUnits)
			http.Error(responseWriter, fmt.Sprintf("repayment ledger posting failed, rolled back: %v", ledgerError), http.StatusBadGateway)
			return
		}

		preTradeRiskEngine.AdjustAvailableMarginInMinorUnits(wireRequest.ClientAccountIdentifier, -wireRequest.AmountInMinorUnits)

		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"outstandingPrincipalInMinorUnits": newOutstanding,
		})
	}
}

// buildLoanAgainstSecuritiesStatusHandler is GET
// /loan-against-securities?accountId=...
func buildLoanAgainstSecuritiesStatusHandler(
	loanBook *loanagainstsecurities.LoanBook,
	pledgeBook *marginpledge.PledgeBook,
) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}
		loanRecord := loanBook.LoanRecordForAccount(accountIdentifier)
		pledgedMarginValue := pledgeBook.TotalPledgedMarginValueForAccount(accountIdentifier)
		loanToValueCap := loanagainstsecurities.CalculateLoanToValueCap(pledgedMarginValue)
		remainingCapacity := loanToValueCap - loanRecord.OutstandingPrincipalInMinorUnits
		if remainingCapacity < 0 {
			remainingCapacity = 0
		}
		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"clientAccountIdentifier":             accountIdentifier,
			"outstandingPrincipalInMinorUnits":    loanRecord.OutstandingPrincipalInMinorUnits,
			"tenureInMonths":                      loanRecord.TenureInMonths,
			"totalPledgedMarginValueInMinorUnits": pledgedMarginValue,
			"loanToValueCapInMinorUnits":          loanToValueCap,
			"remainingLoanCapacityInMinorUnits":   remainingCapacity,
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

// ---------------------------------------------------------------------
// Execution algos (TWAP/VWAP/POV) — FEATURES.md §15. See
// internal/executionalgos' package doc for the real slicing/scheduling
// engine; this section is the HTTP-layer glue: a created parent order
// gets an algoOrderId, and separate requests drive its scheduler forward
// (poll for TWAP/VWAP due slices; feed real-time volume for POV) — the
// same "accept `now` explicitly, no server-side sleeping" discipline the
// underlying package uses, just moved to the wire boundary. Known gap,
// stated loudly: nothing here automatically submits the returned
// ChildOrderSlice values as real orders.OrderSubmissionRequest calls —
// see internal/executionalgos' package doc for why that's out of scope
// for this build.
// ---------------------------------------------------------------------

// executionAlgoOrderRegistry is the mutex-guarded, in-memory map from a
// generated algoOrderId to its live scheduler (exactly one of the two
// maps below has an entry for any given id — TWAP/VWAP orders use a
// executionalgos.Scheduler, POV orders use a executionalgos.PovScheduler,
// since the two need different real-time inputs to advance).
type executionAlgoOrderRegistry struct {
	mutexGuardingState sync.Mutex

	nextAlgoOrderSequence  uint64
	twapOrVwapSchedulers   map[string]*executionalgos.Scheduler
	povSchedulers          map[string]*executionalgos.PovScheduler
	parentOrderByAlgoOrder map[string]executionalgos.ParentOrder
}

func newExecutionAlgoOrderRegistry() *executionAlgoOrderRegistry {
	return &executionAlgoOrderRegistry{
		twapOrVwapSchedulers:   make(map[string]*executionalgos.Scheduler),
		povSchedulers:          make(map[string]*executionalgos.PovScheduler),
		parentOrderByAlgoOrder: make(map[string]executionalgos.ParentOrder),
	}
}

func (registry *executionAlgoOrderRegistry) newAlgoOrderId(prefix string) string {
	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()
	registry.nextAlgoOrderSequence++
	return fmt.Sprintf("%s-%d", prefix, registry.nextAlgoOrderSequence)
}

func (registry *executionAlgoOrderRegistry) storeTwapOrVwap(algoOrderId string, parent executionalgos.ParentOrder, scheduler *executionalgos.Scheduler) {
	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()
	registry.twapOrVwapSchedulers[algoOrderId] = scheduler
	registry.parentOrderByAlgoOrder[algoOrderId] = parent
}

func (registry *executionAlgoOrderRegistry) storePov(algoOrderId string, parent executionalgos.ParentOrder, scheduler *executionalgos.PovScheduler) {
	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()
	registry.povSchedulers[algoOrderId] = scheduler
	registry.parentOrderByAlgoOrder[algoOrderId] = parent
}

func (registry *executionAlgoOrderRegistry) lookupTwapOrVwap(algoOrderId string) (*executionalgos.Scheduler, bool) {
	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()
	scheduler, exists := registry.twapOrVwapSchedulers[algoOrderId]
	return scheduler, exists
}

func (registry *executionAlgoOrderRegistry) lookupPov(algoOrderId string) (*executionalgos.PovScheduler, bool) {
	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()
	scheduler, exists := registry.povSchedulers[algoOrderId]
	return scheduler, exists
}

type createTwapExecutionAlgoWireRequest struct {
	InstrumentSymbol      string    `json:"instrumentSymbol"`
	OrderSideIsBuyNotSell bool      `json:"orderSideIsBuyNotSell"`
	TotalQuantity         uint64    `json:"totalQuantity"`
	StartTime             time.Time `json:"startTime"`
	EndTime               time.Time `json:"endTime"`
	NumberOfSlices        int       `json:"numberOfSlices"`
}

// buildCreateTwapExecutionAlgoHandler is POST /execution-algos/twap/create
// — builds a full TWAP slice schedule up front and returns it along with
// the algoOrderId a caller then drives via POST /execution-algos/poll.
func buildCreateTwapExecutionAlgoHandler(registry *executionAlgoOrderRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest createTwapExecutionAlgoWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed twap create request", http.StatusBadRequest)
			return
		}

		parent := executionalgos.ParentOrder{
			InstrumentSymbol:      wireRequest.InstrumentSymbol,
			OrderSideIsBuyNotSell: wireRequest.OrderSideIsBuyNotSell,
			TotalQuantity:         wireRequest.TotalQuantity,
			StartTime:             wireRequest.StartTime,
			EndTime:               wireRequest.EndTime,
		}
		slices, buildError := executionalgos.BuildTwapSchedule(parent, wireRequest.NumberOfSlices)
		if buildError != nil {
			http.Error(responseWriter, buildError.Error(), http.StatusBadRequest)
			return
		}

		algoOrderId := registry.newAlgoOrderId("TWAP")
		scheduler := executionalgos.NewScheduler(parent, slices)
		registry.storeTwapOrVwap(algoOrderId, parent, scheduler)

		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"algoOrderId": algoOrderId,
			"schedule":    slices,
		})
	}
}

type createVwapExecutionAlgoWireRequest struct {
	InstrumentSymbol      string                            `json:"instrumentSymbol"`
	OrderSideIsBuyNotSell bool                              `json:"orderSideIsBuyNotSell"`
	TotalQuantity         uint64                            `json:"totalQuantity"`
	VolumeCurve           []executionalgos.VolumeCurvePoint `json:"volumeCurve"`
}

// buildCreateVwapExecutionAlgoHandler is POST /execution-algos/vwap/create.
func buildCreateVwapExecutionAlgoHandler(registry *executionAlgoOrderRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest createVwapExecutionAlgoWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed vwap create request", http.StatusBadRequest)
			return
		}

		parent := executionalgos.ParentOrder{
			InstrumentSymbol:      wireRequest.InstrumentSymbol,
			OrderSideIsBuyNotSell: wireRequest.OrderSideIsBuyNotSell,
			TotalQuantity:         wireRequest.TotalQuantity,
		}
		slices, buildError := executionalgos.BuildVwapSchedule(parent, wireRequest.VolumeCurve)
		if buildError != nil {
			http.Error(responseWriter, buildError.Error(), http.StatusBadRequest)
			return
		}

		algoOrderId := registry.newAlgoOrderId("VWAP")
		scheduler := executionalgos.NewScheduler(parent, slices)
		registry.storeTwapOrVwap(algoOrderId, parent, scheduler)

		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"algoOrderId": algoOrderId,
			"schedule":    slices,
		})
	}
}

type createPovExecutionAlgoWireRequest struct {
	InstrumentSymbol      string  `json:"instrumentSymbol"`
	OrderSideIsBuyNotSell bool    `json:"orderSideIsBuyNotSell"`
	TotalQuantity         uint64  `json:"totalQuantity"`
	ParticipationRate     float64 `json:"participationRate"`
	MaxClipSizeQuantity   uint64  `json:"maxClipSizeQuantity"`
}

// buildCreatePovExecutionAlgoHandler is POST /execution-algos/pov/create.
func buildCreatePovExecutionAlgoHandler(registry *executionAlgoOrderRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest createPovExecutionAlgoWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed pov create request", http.StatusBadRequest)
			return
		}

		parent := executionalgos.ParentOrder{
			InstrumentSymbol:      wireRequest.InstrumentSymbol,
			OrderSideIsBuyNotSell: wireRequest.OrderSideIsBuyNotSell,
			TotalQuantity:         wireRequest.TotalQuantity,
		}
		scheduler, buildError := executionalgos.NewPovScheduler(parent, executionalgos.PovConfig{
			ParticipationRate:   wireRequest.ParticipationRate,
			MaxClipSizeQuantity: wireRequest.MaxClipSizeQuantity,
		})
		if buildError != nil {
			http.Error(responseWriter, buildError.Error(), http.StatusBadRequest)
			return
		}

		algoOrderId := registry.newAlgoOrderId("POV")
		registry.storePov(algoOrderId, parent, scheduler)

		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"algoOrderId": algoOrderId,
		})
	}
}

type pollExecutionAlgoWireRequest struct {
	AlgoOrderId string    `json:"algoOrderId"`
	Now         time.Time `json:"now"`
}

// buildPollExecutionAlgoHandler is POST /execution-algos/poll — for a
// TWAP or VWAP algoOrderId, returns every slice newly due as of the
// caller-supplied `now`. Calling this repeatedly (e.g. from a poller
// every few seconds, passing time.Now() each time in a real deployment)
// never returns the same slice twice — see
// executionalgos.Scheduler.PollDueSlices's doc comment.
func buildPollExecutionAlgoHandler(registry *executionAlgoOrderRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest pollExecutionAlgoWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed poll request", http.StatusBadRequest)
			return
		}

		scheduler, exists := registry.lookupTwapOrVwap(wireRequest.AlgoOrderId)
		if !exists {
			http.Error(responseWriter, "unknown algoOrderId (or it is a POV order — use /execution-algos/pov/observe-volume instead)", http.StatusNotFound)
			return
		}

		now := wireRequest.Now
		if now.IsZero() {
			now = time.Now()
		}
		dueSlices := scheduler.PollDueSlices(now)

		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"algoOrderId":       wireRequest.AlgoOrderId,
			"dueSlices":         dueSlices,
			"remainingQuantity": scheduler.RemainingQuantity(),
			"isComplete":        scheduler.IsComplete(),
		})
	}
}

type observePovVolumeWireRequest struct {
	AlgoOrderId            string    `json:"algoOrderId"`
	Now                    time.Time `json:"now"`
	CumulativeMarketVolume uint64    `json:"cumulativeMarketVolume"`
}

// buildObservePovVolumeHandler is
// POST /execution-algos/pov/observe-volume — feeds one real-time
// cumulative-volume reading into a POV order's scheduler; see
// executionalgos.PovScheduler.OnVolumeObservation's doc comment for
// exactly how the returned slice (if any) is sized.
func buildObservePovVolumeHandler(registry *executionAlgoOrderRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest observePovVolumeWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed observe-volume request", http.StatusBadRequest)
			return
		}

		scheduler, exists := registry.lookupPov(wireRequest.AlgoOrderId)
		if !exists {
			http.Error(responseWriter, "unknown POV algoOrderId", http.StatusNotFound)
			return
		}

		now := wireRequest.Now
		if now.IsZero() {
			now = time.Now()
		}
		slice, observeError := scheduler.OnVolumeObservation(now, wireRequest.CumulativeMarketVolume)
		if observeError != nil {
			http.Error(responseWriter, observeError.Error(), http.StatusBadRequest)
			return
		}

		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"algoOrderId":       wireRequest.AlgoOrderId,
			"newSlice":          slice,
			"remainingQuantity": scheduler.RemainingQuantity(),
			"isComplete":        scheduler.IsComplete(),
		})
	}
}

// buildExecutionAlgoStatusHandler is
// GET /execution-algos/status?algoOrderId=... — a read-only status check
// that works for either a TWAP/VWAP or a POV algoOrderId.
func buildExecutionAlgoStatusHandler(registry *executionAlgoOrderRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		algoOrderId := request.URL.Query().Get("algoOrderId")
		if algoOrderId == "" {
			http.Error(responseWriter, "missing algoOrderId query parameter", http.StatusBadRequest)
			return
		}

		if scheduler, exists := registry.lookupTwapOrVwap(algoOrderId); exists {
			respondWithJson(responseWriter, http.StatusOK, map[string]any{
				"algoOrderId":       algoOrderId,
				"remainingQuantity": scheduler.RemainingQuantity(),
				"isComplete":        scheduler.IsComplete(),
			})
			return
		}
		if scheduler, exists := registry.lookupPov(algoOrderId); exists {
			respondWithJson(responseWriter, http.StatusOK, map[string]any{
				"algoOrderId":       algoOrderId,
				"remainingQuantity": scheduler.RemainingQuantity(),
				"isComplete":        scheduler.IsComplete(),
			})
			return
		}
		http.Error(responseWriter, "unknown algoOrderId", http.StatusNotFound)
	}
}

// ---------------------------------------------------------------------
// Options strategy payoff diagram — FEATURES.md §15. See
// internal/payoffdiagram's package doc for the real piecewise-linear
// analysis behind max profit/loss and breakevens. This handler is
// deliberately stateless — POST the full current leg set every time
// ("computed live as legs are added" per FEATURES.md's own framing means
// the CALLER re-POSTs with one more leg each time, not that this
// endpoint remembers a leg set between calls).
// ---------------------------------------------------------------------

type computePayoffDiagramWireRequest struct {
	Legs []payoffdiagram.OptionLeg `json:"legs"`
}

// buildComputePayoffDiagramHandler is POST /payoff-diagram/compute.
func buildComputePayoffDiagramHandler() http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest computePayoffDiagramWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed payoff-diagram compute request", http.StatusBadRequest)
			return
		}

		result, computeError := payoffdiagram.ComputePayoffDiagram(wireRequest.Legs)
		if computeError != nil {
			http.Error(responseWriter, computeError.Error(), http.StatusBadRequest)
			return
		}

		respondWithJson(responseWriter, http.StatusOK, result)
	}
}

// ---------------------------------------------------------------------
// Multi-leg options strategy builder — FEATURES.md §15. See
// internal/multilegoptions' package doc for the full contract: named
// strategy shape validation plus atomic all-or-nothing execution (with
// real, best-effort compensating rollback) through the exact same
// processOrderSubmission pipeline every other order path reuses.
// ---------------------------------------------------------------------

type executeMultiLegOptionsWireRequest struct {
	ClientAccountIdentifier string                        `json:"clientAccountIdentifier"`
	Strategy                multilegoptions.StrategyShape `json:"strategy"`
	Legs                    []multilegoptions.Leg         `json:"legs"`
}

// buildExecuteMultiLegOptionsHandler is POST /multileg-options/execute.
func buildExecuteMultiLegOptionsHandler(dependencies orderSubmissionDependencies) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest executeMultiLegOptionsWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed multi-leg options execution request", http.StatusBadRequest)
			return
		}
		if wireRequest.ClientAccountIdentifier == "" {
			http.Error(responseWriter, "clientAccountIdentifier is required", http.StatusBadRequest)
			return
		}

		// submitLeg: each leg reuses the EXACT SAME processOrderSubmission
		// pipeline (KYC/freeze/risk/matching-engine/audit-trail) every
		// other order path in this file reuses, via a plain Go closure —
		// nothing duplicated. See internal/multilegoptions' package doc
		// for the "no real listed options instrument" scope boundary: a
		// leg submits as an ordinary LIMIT order at its premium.
		submitLeg := func(leg multilegoptions.Leg) (bool, string, error) {
			acknowledgement := processOrderSubmission(dependencies, orders.OrderSubmissionRequest{
				ClientAccountIdentifier: wireRequest.ClientAccountIdentifier,
				InstrumentSymbol:        leg.InstrumentSymbol,
				OrderSideIsBuyNotSell:   leg.IsBuyNotSell,
				LimitPriceInMinorUnits:  leg.PremiumInMinorUnits,
				OrderQuantity:           leg.Quantity,
			})
			return acknowledgement.WasOrderAccepted, acknowledgement.HumanReadableRejectionReason, nil
		}

		result, executionError := multilegoptions.ExecuteStrategyAtomically(wireRequest.Strategy, wireRequest.Legs, submitLeg)
		if executionError != nil && errors.Is(executionError, multilegoptions.ErrLegShapeMismatch) {
			http.Error(responseWriter, executionError.Error(), http.StatusBadRequest)
			return
		}
		if executionError != nil && !errors.Is(executionError, multilegoptions.ErrLegRejectedDuringExecution) {
			http.Error(responseWriter, executionError.Error(), http.StatusBadRequest)
			return
		}

		dependencies.auditTrail.Append(audittrail.Entry{
			EventType:               audittrail.EventOrderSubmitted,
			ClientAccountIdentifier: wireRequest.ClientAccountIdentifier,
			DetailMessage:           fmt.Sprintf("MULTI_LEG_STRATEGY_%s wasFullyExecuted=%v", wireRequest.Strategy, result.WasFullyExecuted),
		})

		respondWithJson(responseWriter, http.StatusOK, result)
	}
}

// ---------------------------------------------------------------------
// Basket/program order execution — FEATURES.md §15. See
// internal/basketorders' package doc for the full contract: a net-cash-
// constrained set of constituents, each submitted through the exact same
// order-submission path, with real aggregate fill-status tracking
// (deliberately NOT atomic, unlike multi-leg options above).
// ---------------------------------------------------------------------

// buildExecuteBasketOrderHandler is POST /basket-orders/execute.
func buildExecuteBasketOrderHandler(dependencies orderSubmissionDependencies) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var basketRequest basketorders.BasketOrderRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&basketRequest); decodeError != nil {
			http.Error(responseWriter, "malformed basket order execution request", http.StatusBadRequest)
			return
		}

		submitConstituent := func(instrumentSymbol string, isBuyNotSell bool, quantity uint64) (bool, uint64, string, error) {
			acknowledgement := processOrderSubmission(dependencies, orders.OrderSubmissionRequest{
				ClientAccountIdentifier:    basketRequest.ClientAccountIdentifier,
				InstrumentSymbol:           instrumentSymbol,
				OrderSideIsBuyNotSell:      isBuyNotSell,
				OrderIsMarketOrderNotLimit: true,
				OrderQuantity:              quantity,
			})
			var filledQuantity uint64
			for _, tradeExecutionSummary := range acknowledgement.TradeExecutionEvents {
				filledQuantity += tradeExecutionSummary.ExecutedQuantity
			}
			return acknowledgement.WasOrderAccepted, filledQuantity, acknowledgement.HumanReadableRejectionReason, nil
		}

		result, executionError := basketorders.ExecuteBasket(basketRequest, submitConstituent)
		if executionError != nil {
			http.Error(responseWriter, executionError.Error(), http.StatusBadRequest)
			return
		}

		dependencies.auditTrail.Append(audittrail.Entry{
			EventType:               audittrail.EventOrderSubmitted,
			ClientAccountIdentifier: basketRequest.ClientAccountIdentifier,
			DetailMessage:           fmt.Sprintf("BASKET_ORDER_%s aggregateStatus=%s", basketRequest.BasketIdentifier, result.AggregateStatus),
		})

		respondWithJson(responseWriter, http.StatusOK, result)
	}
}

// ---------------------------------------------------------------------
// Pre-trade impact-cost / slippage estimator — FEATURES.md §15. See
// internal/impactcostestimator's package doc for the real
// walk-the-book algorithm AND its honest "oms-gateway has no queryable
// live depth source yet, caller supplies the snapshot" scope boundary.
// ---------------------------------------------------------------------

type estimateImpactCostWireRequest struct {
	Snapshot             impactcostestimator.OrderBookDepthSnapshot `json:"snapshot"`
	IsBuyNotSell         bool                                       `json:"isBuyNotSell"`
	HypotheticalQuantity uint64                                     `json:"hypotheticalQuantity"`
}

// buildEstimateImpactCostHandler is POST /impact-cost/estimate.
func buildEstimateImpactCostHandler() http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest estimateImpactCostWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed impact-cost estimate request", http.StatusBadRequest)
			return
		}

		estimate, estimateError := impactcostestimator.EstimateImpactCost(wireRequest.Snapshot, wireRequest.IsBuyNotSell, wireRequest.HypotheticalQuantity)
		if estimateError != nil {
			http.Error(responseWriter, estimateError.Error(), http.StatusBadRequest)
			return
		}

		respondWithJson(responseWriter, http.StatusOK, estimate)
	}
}

// portfolioStressTestWireRequest is the payload for POST
// /portfolio-stress-test/compute. Equity positions are looked up
// SERVER-SIDE from internal/positions for accountId (never
// client-asserted quantities) — the caller only supplies each held
// equity instrument's CURRENT PRICE (oms-gateway has no live price feed,
// same documented gap as every other package that needs one) via
// EquityCurrentPricesInMinorUnits. Option positions have no server-side
// book at all in this build, so they're supplied directly by the caller
// — the same "the real computation exists, the live/held-state feed to
// source its input doesn't" pattern internal/impactcostestimator and
// internal/executionalgos' VWAP/POV already established.
type portfolioStressTestWireRequest struct {
	ClientAccountIdentifier         string                                        `json:"clientAccountIdentifier"`
	ShockPercent                    float64                                       `json:"shockPercent"`
	EquityCurrentPricesInMinorUnits map[string]int64                              `json:"equityCurrentPricesInMinorUnits"`
	OptionPositions                 []portfoliostresstest.StressTestPositionInput `json:"optionPositions"`
}

// buildPortfolioStressTestHandler serves POST
// /portfolio-stress-test/compute — FEATURES.md §21's "if Nifty drops 10%
// tomorrow, your portfolio loses ~₹X". See
// internal/portfoliostresstest's package doc for the honest
// exact-for-equity / first-order-delta-approximation-for-options scope
// boundary.
func buildPortfolioStressTestHandler(positionBook *positions.PositionBook) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest portfolioStressTestWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed portfolio stress test request", http.StatusBadRequest)
			return
		}
		if wireRequest.ClientAccountIdentifier == "" {
			http.Error(responseWriter, "clientAccountIdentifier is required", http.StatusBadRequest)
			return
		}

		var allPositions []portfoliostresstest.StressTestPositionInput
		var skippedEquityPositionsMissingPrice []string
		for instrumentSymbol, netQuantity := range positionBook.PositionsForAccount(wireRequest.ClientAccountIdentifier) {
			currentPrice, hasPrice := wireRequest.EquityCurrentPricesInMinorUnits[instrumentSymbol]
			if !hasPrice {
				skippedEquityPositionsMissingPrice = append(skippedEquityPositionsMissingPrice, instrumentSymbol)
				continue
			}
			allPositions = append(allPositions, portfoliostresstest.StressTestPositionInput{
				InstrumentSymbol:         instrumentSymbol,
				PositionType:             portfoliostresstest.PositionTypeEquity,
				NetQuantity:              netQuantity,
				CurrentPriceInMinorUnits: currentPrice,
			})
		}
		allPositions = append(allPositions, wireRequest.OptionPositions...)

		if len(allPositions) == 0 {
			respondWithJson(responseWriter, http.StatusOK, map[string]any{
				"shockPercent":                        wireRequest.ShockPercent,
				"totalEstimatedPnLImpactInMinorUnits": 0,
				"perPositionImpacts":                  []portfoliostresstest.PositionImpact{},
				"skippedEquityPositionsMissingPrice":  skippedEquityPositionsMissingPrice,
			})
			return
		}

		result, stressTestError := portfoliostresstest.ComputeStressTest(allPositions, wireRequest.ShockPercent)
		if stressTestError != nil {
			http.Error(responseWriter, stressTestError.Error(), http.StatusBadRequest)
			return
		}

		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"shockPercent":                        result.ShockPercent,
			"totalEstimatedPnLImpactInMinorUnits": result.TotalEstimatedPnLImpactInMinorUnits,
			"perPositionImpacts":                  result.PerPositionImpacts,
			"skippedEquityPositionsMissingPrice":  skippedEquityPositionsMissingPrice,
		})
	}
}

// ---------------------------------------------------------------------
// Liquidity / fill-probability badge — FEATURES.md §21. See
// internal/liquiditybadge's package doc: reuses
// impactcostestimator.OrderBookDepthSnapshot verbatim, and is honest
// that the expected-time-to-fill figure is illustrative, not ML-fitted.
// ---------------------------------------------------------------------

type liquidityBadgeWireRequest struct {
	Snapshot             impactcostestimator.OrderBookDepthSnapshot `json:"snapshot"`
	IsBuyNotSell         bool                                       `json:"isBuyNotSell"`
	HypotheticalQuantity uint64                                     `json:"hypotheticalQuantity"`
}

// buildLiquidityBadgeHandler is POST /liquidity-badge/compute.
func buildLiquidityBadgeHandler() http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest liquidityBadgeWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed liquidity badge request", http.StatusBadRequest)
			return
		}

		badge, badgeError := liquiditybadge.ComputeLiquidityBadge(
			wireRequest.Snapshot, wireRequest.IsBuyNotSell, wireRequest.HypotheticalQuantity, liquiditybadge.DefaultThresholds(),
		)
		if badgeError != nil {
			http.Error(responseWriter, badgeError.Error(), http.StatusBadRequest)
			return
		}

		respondWithJson(responseWriter, http.StatusOK, badge)
	}
}

// ---------------------------------------------------------------------
// Securities Lending & Borrowing (SLB) desk — FEATURES.md §15. See
// internal/securitieslendingborrowing's package doc for the full
// contract: two independent, mutex-guarded ledgers (lending, borrowing),
// server-looked-up holdings for LendSecurity (never client-asserted),
// and the illustrative-fee-rate warning.
// ---------------------------------------------------------------------

type lendSecurityWireRequest struct {
	ClientAccountIdentifier    string `json:"clientAccountIdentifier"`
	InstrumentSymbol           string `json:"instrumentSymbol"`
	Quantity                   uint64 `json:"quantity"`
	ReferencePriceInMinorUnits int64  `json:"referencePriceInMinorUnits"`
}

// buildLendSecurityHandler is POST /securities-lending/lend.
func buildLendSecurityHandler(desk *securitieslendingborrowing.Desk, positionBook *positions.PositionBook) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest lendSecurityWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed lend security request", http.StatusBadRequest)
			return
		}
		currentNetHoldingQuantity := positionBook.PositionsForAccount(wireRequest.ClientAccountIdentifier)[wireRequest.InstrumentSymbol]
		record, lendError := desk.LendSecurity(
			wireRequest.ClientAccountIdentifier,
			wireRequest.InstrumentSymbol,
			wireRequest.Quantity,
			wireRequest.ReferencePriceInMinorUnits,
			currentNetHoldingQuantity,
		)
		if lendError != nil {
			http.Error(responseWriter, lendError.Error(), http.StatusBadRequest)
			return
		}
		respondWithJson(responseWriter, http.StatusOK, record)
	}
}

type recallLendingWireRequest struct {
	ClientAccountIdentifier string `json:"clientAccountIdentifier"`
	InstrumentSymbol        string `json:"instrumentSymbol"`
	Quantity                uint64 `json:"quantity"`
}

// buildRecallLendingHandler is POST /securities-lending/recall.
func buildRecallLendingHandler(desk *securitieslendingborrowing.Desk) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest recallLendingWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed recall lending request", http.StatusBadRequest)
			return
		}
		record, recallError := desk.RecallLending(wireRequest.ClientAccountIdentifier, wireRequest.InstrumentSymbol, wireRequest.Quantity)
		if recallError != nil {
			http.Error(responseWriter, recallError.Error(), http.StatusBadRequest)
			return
		}
		respondWithJson(responseWriter, http.StatusOK, record)
	}
}

type borrowSecurityWireRequest struct {
	ClientAccountIdentifier    string `json:"clientAccountIdentifier"`
	InstrumentSymbol           string `json:"instrumentSymbol"`
	Quantity                   uint64 `json:"quantity"`
	ReferencePriceInMinorUnits int64  `json:"referencePriceInMinorUnits"`
}

// buildBorrowSecurityHandler is POST /securities-lending/borrow.
func buildBorrowSecurityHandler(desk *securitieslendingborrowing.Desk) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest borrowSecurityWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed borrow security request", http.StatusBadRequest)
			return
		}
		record, borrowError := desk.BorrowSecurity(
			wireRequest.ClientAccountIdentifier,
			wireRequest.InstrumentSymbol,
			wireRequest.Quantity,
			wireRequest.ReferencePriceInMinorUnits,
		)
		if borrowError != nil {
			http.Error(responseWriter, borrowError.Error(), http.StatusBadRequest)
			return
		}
		respondWithJson(responseWriter, http.StatusOK, record)
	}
}

type returnBorrowingWireRequest struct {
	ClientAccountIdentifier string `json:"clientAccountIdentifier"`
	InstrumentSymbol        string `json:"instrumentSymbol"`
	Quantity                uint64 `json:"quantity"`
}

// buildReturnBorrowingHandler is POST /securities-lending/return.
func buildReturnBorrowingHandler(desk *securitieslendingborrowing.Desk) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest returnBorrowingWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed return borrowing request", http.StatusBadRequest)
			return
		}
		record, returnError := desk.ReturnBorrowing(wireRequest.ClientAccountIdentifier, wireRequest.InstrumentSymbol, wireRequest.Quantity)
		if returnError != nil {
			http.Error(responseWriter, returnError.Error(), http.StatusBadRequest)
			return
		}
		respondWithJson(responseWriter, http.StatusOK, record)
	}
}

// buildSecuritiesLendingStatusHandler is GET /securities-lending?accountId=...
func buildSecuritiesLendingStatusHandler(desk *securitieslendingborrowing.Desk) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		respondWithJson(responseWriter, http.StatusOK, map[string]interface{}{
			"accountIdentifier": accountIdentifier,
			"lendingRecords":    desk.LendingRecordsForAccount(accountIdentifier),
			"borrowingRecords":  desk.BorrowingRecordsForAccount(accountIdentifier),
		})
	}
}

// ---------------------------------------------------------------------
// Dividend Reinvestment Plan (DRIP) — FEATURES.md §17. See
// internal/drip's package doc: a real dividend cash credit proportional
// to held quantity, posted through the real ledger, plus a real
// auto-reinvestment toggle that — when ON — re-invests the credited cash
// via the EXACT SAME processOrderSubmission pipeline every other order
// path reuses.
// ---------------------------------------------------------------------

type setDripToggleWireRequest struct {
	ClientAccountIdentifier string `json:"clientAccountIdentifier"`
	Enabled                 bool   `json:"enabled"`
}

// buildSetDripToggleHandler is POST /drip/toggle.
func buildSetDripToggleHandler(dripToggleRegistry *drip.ToggleRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest setDripToggleWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed DRIP toggle request", http.StatusBadRequest)
			return
		}
		dripToggleRegistry.SetAutoReinvest(wireRequest.ClientAccountIdentifier, wireRequest.Enabled)
		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"clientAccountIdentifier": wireRequest.ClientAccountIdentifier,
			"autoReinvestEnabled":     dripToggleRegistry.IsAutoReinvestEnabled(wireRequest.ClientAccountIdentifier),
		})
	}
}

type processDividendEventWireRequest struct {
	ClientAccountIdentifier      string `json:"clientAccountIdentifier"`
	InstrumentSymbol             string `json:"instrumentSymbol"`
	DividendPerShareInMinorUnits int64  `json:"dividendPerShareInMinorUnits"`

	// ReinvestmentReferencePriceInMinorUnits is required only if the
	// account's auto-reinvest toggle is ON — see internal/drip's package
	// doc for the honest "no live price feed" gap this mirrors.
	ReinvestmentReferencePriceInMinorUnits int64 `json:"reinvestmentReferencePriceInMinorUnits,omitempty"`
}

type processDividendEventWireResponse struct {
	CashCreditedInMinorUnits int64                                `json:"cashCreditedInMinorUnits"`
	WasLedgerCreditPosted    bool                                 `json:"wasLedgerCreditPosted"`
	LedgerError              string                               `json:"ledgerError,omitempty"`
	WasAutoReinvested        bool                                 `json:"wasAutoReinvested"`
	ReinvestmentPlan         *drip.ReinvestmentPlan               `json:"reinvestmentPlan,omitempty"`
	ReinvestmentOrder        *orders.OrderAcknowledgementResponse `json:"reinvestmentOrder,omitempty"`
}

// buildProcessDividendEventHandler is POST /drip/process-dividend — the
// real end-to-end flow: look up the account's real held quantity
// (internal/positions, never client-asserted), compute the real dividend
// cash credit, post it through the real ledger, update the local risk
// cache, then — if auto-reinvest is ON for this account — re-invest that
// cash via the EXACT SAME processOrderSubmission pipeline every other
// order path reuses.
func buildProcessDividendEventHandler(
	dependencies orderSubmissionDependencies,
	dripToggleRegistry *drip.ToggleRegistry,
) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest processDividendEventWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed dividend event request", http.StatusBadRequest)
			return
		}

		quantityHeld := dependencies.positionBook.PositionsForAccount(wireRequest.ClientAccountIdentifier)[wireRequest.InstrumentSymbol]
		if quantityHeld <= 0 {
			http.Error(responseWriter, "account holds no quantity of this instrument — nothing to credit a dividend against", http.StatusBadRequest)
			return
		}

		cashCredited, creditError := drip.CalculateDividendCredit(uint64(quantityHeld), wireRequest.DividendPerShareInMinorUnits)
		if creditError != nil {
			http.Error(responseWriter, creditError.Error(), http.StatusBadRequest)
			return
		}

		wireResponse := processDividendEventWireResponse{CashCreditedInMinorUnits: cashCredited}

		ledgerError := dependencies.ledgerClient.PostDividendCreditJournalEntry(
			wireRequest.ClientAccountIdentifier,
			cashCredited,
			fmt.Sprintf("dividend credit for %s: %d shares @ %d/share", wireRequest.InstrumentSymbol, quantityHeld, wireRequest.DividendPerShareInMinorUnits),
		)
		if ledgerError != nil {
			log.Printf("DRIP dividend ledger credit FAILED for %s: %v", wireRequest.ClientAccountIdentifier, ledgerError)
			wireResponse.LedgerError = ledgerError.Error()
			respondWithJson(responseWriter, http.StatusOK, wireResponse)
			return
		}
		wireResponse.WasLedgerCreditPosted = true
		dependencies.preTradeRiskEngine.AdjustAvailableMarginInMinorUnits(wireRequest.ClientAccountIdentifier, cashCredited)

		dependencies.auditTrail.Append(audittrail.Entry{
			EventType:               audittrail.EventOrderSubmitted,
			ClientAccountIdentifier: wireRequest.ClientAccountIdentifier,
			InstrumentSymbol:        wireRequest.InstrumentSymbol,
			DetailMessage:           fmt.Sprintf("DIVIDEND_CREDITED cashCreditedInMinorUnits=%d", cashCredited),
		})

		if !dripToggleRegistry.IsAutoReinvestEnabled(wireRequest.ClientAccountIdentifier) {
			respondWithJson(responseWriter, http.StatusOK, wireResponse)
			return
		}
		if wireRequest.ReinvestmentReferencePriceInMinorUnits <= 0 {
			// Auto-reinvest is ON but no reference price was supplied —
			// same honest "no live price feed" gap this package's doc
			// warns about; leave the cash credited but not reinvested
			// rather than guessing a price.
			respondWithJson(responseWriter, http.StatusOK, wireResponse)
			return
		}

		reinvestmentPlan, planError := drip.CalculateReinvestmentQuantity(cashCredited, wireRequest.ReinvestmentReferencePriceInMinorUnits)
		if planError != nil || reinvestmentPlan.ReinvestmentQuantity == 0 {
			respondWithJson(responseWriter, http.StatusOK, wireResponse)
			return
		}
		wireResponse.ReinvestmentPlan = &reinvestmentPlan

		reinvestmentAcknowledgement := processOrderSubmission(dependencies, orders.OrderSubmissionRequest{
			ClientAccountIdentifier: wireRequest.ClientAccountIdentifier,
			InstrumentSymbol:        wireRequest.InstrumentSymbol,
			OrderSideIsBuyNotSell:   true,
			LimitPriceInMinorUnits:  wireRequest.ReinvestmentReferencePriceInMinorUnits,
			OrderQuantity:           reinvestmentPlan.ReinvestmentQuantity,
		})
		wireResponse.WasAutoReinvested = reinvestmentAcknowledgement.WasOrderAccepted
		wireResponse.ReinvestmentOrder = &reinvestmentAcknowledgement

		respondWithJson(responseWriter, http.StatusOK, wireResponse)
	}
}

// ---------------------------------------------------------------------
// Social/copy-trading follow graph — FEATURES.md §11. Opt-in
// follow/unfollow of admin-verified strategies ONLY; no order mirroring.
// See internal/strategyfollowing's package doc for the full scope
// boundary.
// ---------------------------------------------------------------------

// buildListVerifiedStrategiesHandler is GET /strategies — the public,
// disclosed list of strategies an admin has verified, each with its live
// follower count. This is the ONLY set of strategyIdentifiers
// POST /strategies/follow will accept.
func buildListVerifiedStrategiesHandler(strategyFollowingRegistry *strategyfollowing.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		respondWithJson(responseWriter, http.StatusOK, strategyFollowingRegistry.ListVerifiedStrategies())
	}
}

type verifyStrategyWireRequest struct {
	StrategyIdentifier string `json:"strategyIdentifier"`
	DisplayName        string `json:"displayName"`
	Description        string `json:"description"`
}

// buildVerifyStrategyHandler is POST /strategies/admin/verify — the
// admin-approval step (reusing the spirit of backoffice's admin-approval
// pattern) that adds a strategy to the public verified list. TODO(real
// build): unauthenticated, like every other endpoint in this service —
// a real build gates this behind whatever admin auth backoffice's own
// approval actions use.
func buildVerifyStrategyHandler(strategyFollowingRegistry *strategyfollowing.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest verifyStrategyWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed strategy-verify request", http.StatusBadRequest)
			return
		}

		if verifyError := strategyFollowingRegistry.MarkStrategyVerified(wireRequest.StrategyIdentifier, wireRequest.DisplayName, wireRequest.Description); verifyError != nil {
			http.Error(responseWriter, verifyError.Error(), http.StatusBadRequest)
			return
		}

		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"strategyIdentifier": wireRequest.StrategyIdentifier,
			"displayName":        wireRequest.DisplayName,
			"description":        wireRequest.Description,
			"isVerified":         true,
		})
	}
}

type followStrategyWireRequest struct {
	AccountIdentifier  string `json:"accountIdentifier"`
	StrategyIdentifier string `json:"strategyIdentifier"`
}

// buildFollowStrategyHandler is POST /strategies/follow — the one
// opt-in action this feature supports. Rejects (400) any
// strategyIdentifier that isn't on the verified list; this is a
// disclosed relationship, never a silent/implicit one, and never
// triggers any order replication.
func buildFollowStrategyHandler(strategyFollowingRegistry *strategyfollowing.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest followStrategyWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed follow request", http.StatusBadRequest)
			return
		}

		if followError := strategyFollowingRegistry.Follow(wireRequest.AccountIdentifier, wireRequest.StrategyIdentifier); followError != nil {
			http.Error(responseWriter, followError.Error(), http.StatusBadRequest)
			return
		}

		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"accountIdentifier":  wireRequest.AccountIdentifier,
			"strategyIdentifier": wireRequest.StrategyIdentifier,
			"isFollowing":        true,
		})
	}
}

// buildUnfollowStrategyHandler is POST /strategies/unfollow — always
// idempotent, mirrors buildFollowStrategyHandler's wire shape.
func buildUnfollowStrategyHandler(strategyFollowingRegistry *strategyfollowing.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest followStrategyWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed unfollow request", http.StatusBadRequest)
			return
		}

		if unfollowError := strategyFollowingRegistry.Unfollow(wireRequest.AccountIdentifier, wireRequest.StrategyIdentifier); unfollowError != nil {
			http.Error(responseWriter, unfollowError.Error(), http.StatusBadRequest)
			return
		}

		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"accountIdentifier":  wireRequest.AccountIdentifier,
			"strategyIdentifier": wireRequest.StrategyIdentifier,
			"isFollowing":        false,
		})
	}
}

// buildStrategyFollowersHandler is GET /strategies/followers?strategyId=...
func buildStrategyFollowersHandler(strategyFollowingRegistry *strategyfollowing.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		strategyIdentifier := request.URL.Query().Get("strategyId")
		if strategyIdentifier == "" {
			http.Error(responseWriter, "missing strategyId query parameter", http.StatusBadRequest)
			return
		}

		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"strategyIdentifier":         strategyIdentifier,
			"followerAccountIdentifiers": strategyFollowingRegistry.FollowersOfStrategy(strategyIdentifier),
		})
	}
}

// buildAccountFollowingHandler is GET /strategies/following?accountId=...
func buildAccountFollowingHandler(strategyFollowingRegistry *strategyfollowing.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}

		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"accountIdentifier":           accountIdentifier,
			"followedStrategyIdentifiers": strategyFollowingRegistry.FollowingOfAccount(accountIdentifier),
		})
	}
}

// buildSetMarkToMarketPriceHandler is POST /mark-to-market/price — the
// real HTTP push endpoint internal/marktomarket's package doc describes:
// the smallest genuinely real way to feed this engine a current market
// price until a real market-data service with a subscribable feed exists
// in this repo. Body: {"instrumentSymbol":"DEMO-EQ","priceInMinorUnits":10500}
func buildSetMarkToMarketPriceHandler(markToMarketEngine *marktomarket.MarkToMarketEngine) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest struct {
			InstrumentSymbol  string `json:"instrumentSymbol"`
			PriceInMinorUnits int64  `json:"priceInMinorUnits"`
		}
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed mark-to-market price payload", http.StatusBadRequest)
			return
		}
		if wireRequest.InstrumentSymbol == "" {
			http.Error(responseWriter, "instrumentSymbol is required", http.StatusBadRequest)
			return
		}

		if setError := markToMarketEngine.SetMarketPrice(wireRequest.InstrumentSymbol, wireRequest.PriceInMinorUnits); setError != nil {
			http.Error(responseWriter, setError.Error(), http.StatusBadRequest)
			return
		}

		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"instrumentSymbol":  wireRequest.InstrumentSymbol,
			"priceInMinorUnits": wireRequest.PriceInMinorUnits,
		})
	}
}

// buildMarkToMarketHandler is GET /mark-to-market?accountId=... — real
// unrealized P&L for LEVERAGED positions only, per FEATURES.md §12. "Is
// this account leveraged" is decided HERE, not inside
// internal/marktomarket (see that package's doc for the decoupling
// rationale): an account counts as leveraged if it has any outstanding
// margin-funding principal (internal/marginfunding) OR any pledged
// quantity at all (internal/marginpledge). An account that is neither
// gets a clear 200 response with isLeveragedAccount:false and an empty
// positions list, rather than a confusing 404 or a silently-wrong P&L for
// unleveraged exposure this endpoint isn't scoped to report.
func buildMarkToMarketHandler(
	markToMarketEngine *marktomarket.MarkToMarketEngine,
	fundingBook *marginfunding.FundingBook,
	pledgeBook *marginpledge.PledgeBook,
) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}

		hasOutstandingMarginFunding := fundingBook.OutstandingPrincipalInMinorUnits(accountIdentifier) > 0
		hasAnyPledgedHolding := len(pledgeBook.PledgesForAccount(accountIdentifier)) > 0
		isLeveragedAccount := hasOutstandingMarginFunding || hasAnyPledgedHolding

		if !isLeveragedAccount {
			respondWithJson(responseWriter, http.StatusOK, map[string]any{
				"accountIdentifier":              accountIdentifier,
				"isLeveragedAccount":             false,
				"positions":                      []marktomarket.PositionMTM{},
				"totalUnrealizedPnLInMinorUnits": 0,
			})
			return
		}

		totalUnrealizedPnL, positionSnapshots := markToMarketEngine.AccountLevelUnrealizedPnL(accountIdentifier)
		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"accountIdentifier":              accountIdentifier,
			"isLeveragedAccount":             true,
			"hasOutstandingMarginFunding":    hasOutstandingMarginFunding,
			"hasAnyPledgedHolding":           hasAnyPledgedHolding,
			"positions":                      positionSnapshots,
			"totalUnrealizedPnLInMinorUnits": totalUnrealizedPnL,
		})
	}
}

// buildAssembleLeverageSnapshot builds a real
// autoliquidation.AccountLeverageSnapshot for one account from real state
// in internal/marginfunding, internal/marginpledge, internal/positions,
// and internal/marktomarket — the decoupling boundary
// internal/autoliquidation's package doc describes.
func buildAssembleLeverageSnapshot(
	accountIdentifier string,
	fundingBook *marginfunding.FundingBook,
	pledgeBook *marginpledge.PledgeBook,
	positionBook *positions.PositionBook,
	markToMarketEngine *marktomarket.MarkToMarketEngine,
) autoliquidation.AccountLeverageSnapshot {
	var positionsForLiquidation []autoliquidation.PositionForLiquidation
	for instrumentSymbol, netQuantity := range positionBook.PositionsForAccount(accountIdentifier) {
		marketPrice, priceKnown := markToMarketEngine.MarketPrice(instrumentSymbol)
		positionsForLiquidation = append(positionsForLiquidation, autoliquidation.PositionForLiquidation{
			InstrumentSymbol:               instrumentSymbol,
			NetQuantity:                    netQuantity,
			CurrentMarketPriceInMinorUnits: marketPrice,
			MarketPriceIsKnown:             priceKnown,
		})
	}

	return autoliquidation.AccountLeverageSnapshot{
		ClientAccountIdentifier:          accountIdentifier,
		OutstandingPrincipalInMinorUnits: fundingBook.OutstandingPrincipalInMinorUnits(accountIdentifier),
		PledgedMarginValueInMinorUnits:   pledgeBook.TotalPledgedMarginValueForAccount(accountIdentifier),
		Positions:                        positionsForLiquidation,
	}
}

// buildAutoLiquidationStatusHandler is GET /auto-liquidation/status?
// accountId=... — a PURE, side-effect-free read of the account's current
// graduated risk state. Querying this endpoint NEVER triggers a
// liquidation, even at the LIQUIDATION state — only POST
// /auto-liquidation/evaluate can actually act, and even that only acts
// when genuinely breached. See internal/autoliquidation.ClassifyUtilization.
func buildAutoLiquidationStatusHandler(
	fundingBook *marginfunding.FundingBook,
	pledgeBook *marginpledge.PledgeBook,
) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}

		outstanding := fundingBook.OutstandingPrincipalInMinorUnits(accountIdentifier)
		pledgedValue := pledgeBook.TotalPledgedMarginValueForAccount(accountIdentifier)
		snapshot := autoliquidation.AccountLeverageSnapshot{
			ClientAccountIdentifier:          accountIdentifier,
			OutstandingPrincipalInMinorUnits: outstanding,
			PledgedMarginValueInMinorUnits:   pledgedValue,
		}
		utilizationPercent := snapshot.UtilizationPercent()
		thresholds := autoliquidation.DefaultThresholds()

		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"accountIdentifier":                accountIdentifier,
			"outstandingPrincipalInMinorUnits": outstanding,
			"pledgedMarginValueInMinorUnits":   pledgedValue,
			"utilizationPercent":               utilizationPercent,
			"riskState":                        autoliquidation.ClassifyUtilization(utilizationPercent, thresholds),
			"warningThresholdPercent":          thresholds.WarningUtilizationPercent,
			"urgentThresholdPercent":           thresholds.UrgentUtilizationPercent,
		})
	}
}

// buildAutoLiquidationEvaluateHandler is POST /auto-liquidation/evaluate
// — an admin/scheduler-triggerable action endpoint (see
// internal/autoliquidation's package doc gap (2): no automatic poller
// ships in this build, this is the real hook a real scheduler would
// call). Assembles a real AccountLeverageSnapshot from
// marginfunding+marginpledge+positions+marktomarket and hands it to the
// LiquidationEngine, which ONLY submits real reducing orders if the
// account is genuinely at the LIQUIDATION state.
func buildAutoLiquidationEvaluateHandler(
	liquidationEngine *autoliquidation.LiquidationEngine,
	fundingBook *marginfunding.FundingBook,
	pledgeBook *marginpledge.PledgeBook,
	positionBook *positions.PositionBook,
	markToMarketEngine *marktomarket.MarkToMarketEngine,
) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest struct {
			AccountIdentifier string `json:"accountIdentifier"`
		}
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed auto-liquidation evaluate payload", http.StatusBadRequest)
			return
		}
		if wireRequest.AccountIdentifier == "" {
			http.Error(responseWriter, "accountIdentifier is required", http.StatusBadRequest)
			return
		}

		snapshot := buildAssembleLeverageSnapshot(wireRequest.AccountIdentifier, fundingBook, pledgeBook, positionBook, markToMarketEngine)
		outcome := liquidationEngine.EvaluateAndLiquidateIfBreached(snapshot)
		respondWithJson(responseWriter, http.StatusOK, outcome)
	}
}

// buildConfigureExposureLimitsHandler is POST /exposure-limits/configure
// — the risk team's real configuration entry point for
// internal/exposurelimits. Body:
// {"accountIdentifier":"acct-001","segment":"EQUITY","accountLimitInMinorUnits":1000000,"segmentLimitInMinorUnits":500000}
// Either limit field may be omitted (zero value, meaning "don't set that
// one this call") — segment is only required if segmentLimitInMinorUnits
// is being set.
func buildConfigureExposureLimitsHandler(exposureLimitsRegistry *exposurelimits.LimitsRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest struct {
			AccountIdentifier        string `json:"accountIdentifier"`
			Segment                  string `json:"segment,omitempty"`
			AccountLimitInMinorUnits *int64 `json:"accountLimitInMinorUnits,omitempty"`
			SegmentLimitInMinorUnits *int64 `json:"segmentLimitInMinorUnits,omitempty"`
		}
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed exposure-limits configure payload", http.StatusBadRequest)
			return
		}
		if wireRequest.AccountIdentifier == "" {
			http.Error(responseWriter, "accountIdentifier is required", http.StatusBadRequest)
			return
		}
		if wireRequest.SegmentLimitInMinorUnits != nil && wireRequest.Segment == "" {
			http.Error(responseWriter, "segment is required when setting segmentLimitInMinorUnits", http.StatusBadRequest)
			return
		}

		if wireRequest.AccountLimitInMinorUnits != nil {
			exposureLimitsRegistry.SetAccountLimit(wireRequest.AccountIdentifier, *wireRequest.AccountLimitInMinorUnits)
		}
		if wireRequest.SegmentLimitInMinorUnits != nil {
			exposureLimitsRegistry.SetSegmentLimit(wireRequest.AccountIdentifier, wireRequest.Segment, *wireRequest.SegmentLimitInMinorUnits)
		}

		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"accountIdentifier": wireRequest.AccountIdentifier,
			"configured":        true,
		})
	}
}

// buildExposureLimitsStatusHandler is GET /exposure-limits?accountId=...
// [&segment=...] — real current configured limits and current cumulative
// usage. If segment is omitted, only account-level figures are returned.
func buildExposureLimitsStatusHandler(exposureLimitsRegistry *exposurelimits.LimitsRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}

		accountLimit, accountLimitConfigured := exposureLimitsRegistry.AccountLimit(accountIdentifier)
		responseBody := map[string]any{
			"accountIdentifier":                  accountIdentifier,
			"accountLimitInMinorUnits":           accountLimit,
			"accountLimitConfigured":             accountLimitConfigured,
			"currentAccountExposureInMinorUnits": exposureLimitsRegistry.CurrentAccountExposure(accountIdentifier),
		}

		if segment := request.URL.Query().Get("segment"); segment != "" {
			segmentLimit, segmentLimitConfigured := exposureLimitsRegistry.SegmentLimit(accountIdentifier, segment)
			responseBody["segment"] = segment
			responseBody["segmentLimitInMinorUnits"] = segmentLimit
			responseBody["segmentLimitConfigured"] = segmentLimitConfigured
			responseBody["currentSegmentExposureInMinorUnits"] = exposureLimitsRegistry.CurrentSegmentExposure(accountIdentifier, segment)
		}

		respondWithJson(responseWriter, http.StatusOK, responseBody)
	}
}

// buildEngageKillSwitchHandler is POST /connectivity-kill-switch/engage —
// the real admin manual trigger. Body: {"reason":"..."}
func buildEngageKillSwitchHandler(killSwitch *connectivitykillswitch.KillSwitch) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest struct {
			Reason string `json:"reason"`
		}
		_ = json.NewDecoder(request.Body).Decode(&wireRequest) // reason is optional
		if wireRequest.Reason == "" {
			wireRequest.Reason = "manually engaged via POST /connectivity-kill-switch/engage (no reason given)"
		}

		killSwitch.EngageManually(wireRequest.Reason)
		log.Printf("connectivity kill switch MANUALLY ENGAGED: %s", wireRequest.Reason)
		respondWithJson(responseWriter, http.StatusOK, killSwitch.CurrentStatus())
	}
}

// buildDisengageKillSwitchHandler is POST
// /connectivity-kill-switch/disengage — clears ONLY the manual
// engagement flag. See internal/connectivitykillswitch's package doc:
// trading may still be halted afterward if the AUTO flag is set.
func buildDisengageKillSwitchHandler(killSwitch *connectivitykillswitch.KillSwitch) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		killSwitch.DisengageManually()
		log.Printf("connectivity kill switch manually disengaged (auto-engagement, if any, is unaffected)")
		respondWithJson(responseWriter, http.StatusOK, killSwitch.CurrentStatus())
	}
}

// buildKillSwitchStatusHandler is GET /connectivity-kill-switch/status.
func buildKillSwitchStatusHandler(killSwitch *connectivitykillswitch.KillSwitch) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		respondWithJson(responseWriter, http.StatusOK, killSwitch.CurrentStatus())
	}
}
