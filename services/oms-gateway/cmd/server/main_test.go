package main

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mercurius/omsgateway/internal/algolimits"
	"mercurius/omsgateway/internal/amoqueue"
	"mercurius/omsgateway/internal/audittrail"
	"mercurius/omsgateway/internal/backofficeclient"
	"mercurius/omsgateway/internal/connectivitykillswitch"
	"mercurius/omsgateway/internal/exposurelimits"
	"mercurius/omsgateway/internal/fractionalshares"
	"mercurius/omsgateway/internal/kycclient"
	"mercurius/omsgateway/internal/ledgerclient"
	"mercurius/omsgateway/internal/marginpledge"
	"mercurius/omsgateway/internal/marketsession"
	"mercurius/omsgateway/internal/marktomarket"
	"mercurius/omsgateway/internal/matchingengineclient"
	"mercurius/omsgateway/internal/orders"
	"mercurius/omsgateway/internal/positions"
	"mercurius/omsgateway/internal/riskengine"
	"mercurius/omsgateway/internal/sequencing"
)

// ---- Test fakes for oms-gateway's downstream HTTP/TCP dependencies ----
// These mirror exactly the wire shapes each client package's own doc
// comment documents (see internal/kycclient, internal/backofficeclient,
// internal/ledgerclient, internal/matchingengineclient) so
// processOrderSubmission can be exercised end-to-end without a real
// kyc-onboarding/backoffice/ledger/matching-engine process running.

func newAlwaysEligibleKycServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(kycclient.KycStatusWireResponse{
			AccountIdentifier:       r.URL.Query().Get("accountId"),
			IsEligibleToPlaceOrders: true,
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func newNeverFrozenBackofficeServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(backofficeclient.FreezeStatusWireResponse{
			AccountIdentifier: r.URL.Query().Get("accountId"),
			IsFrozen:          false,
		})
	}))
	t.Cleanup(server.Close)
	return server
}

// newConfigurableLedgerServer returns a ledger fake whose
// PostTradeSettlementJournalEntry response is controlled by
// shouldSucceed (a pointer so a test can flip it mid-run).
func newConfigurableLedgerServer(t *testing.T, shouldSucceed *bool) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/journal-entries") || r.Method == http.MethodPost {
			if *shouldSucceed {
				json.NewEncoder(w).Encode(ledgerclient.PostJournalEntryWireResponse{WasJournalEntryPosted: true})
			} else {
				json.NewEncoder(w).Encode(ledgerclient.PostJournalEntryWireResponse{WasJournalEntryPosted: false, ErrorMessage: "simulated ledger outage"})
			}
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"accountIdentifier": r.URL.Query().Get("accountId"), "currentBalanceInMinorUnits": 0})
	}))
	t.Cleanup(server.Close)
	return server
}

// newFakeMatchingEngineServer starts a one-line-JSON-in/one-line-JSON-out
// TCP server (mirroring matchingengineclient's real wire protocol — see
// that package's doc comment) whose response is produced by
// responseBuilder for every request it receives.
func newFakeMatchingEngineServer(t *testing.T, responseBuilder func(matchingengineclient.OrderSubmissionWireRequest) matchingengineclient.OrderSubmissionWireResponse) string {
	t.Helper()
	listener, listenError := net.Listen("tcp", "127.0.0.1:0")
	if listenError != nil {
		t.Fatalf("failed to start fake matching-engine listener: %v", listenError)
	}
	t.Cleanup(func() { listener.Close() })

	go func() {
		for {
			connection, acceptError := listener.Accept()
			if acceptError != nil {
				return
			}
			go func() {
				defer connection.Close()
				line, readError := bufio.NewReader(connection).ReadBytes('\n')
				if readError != nil {
					return
				}
				var wireRequest matchingengineclient.OrderSubmissionWireRequest
				if err := json.Unmarshal(line, &wireRequest); err != nil {
					return
				}
				response := responseBuilder(wireRequest)
				responseBytes, _ := json.Marshal(response)
				connection.Write(append(responseBytes, '\n'))
			}()
		}
	}()

	return listener.Addr().String()
}

// newTestOrderSubmissionDependencies builds a full, real
// orderSubmissionDependencies wired to test-double kyc/backoffice/ledger
// HTTP servers and a configurable fake matching-engine TCP server —
// every gate processOrderSubmission checks is REAL, not mocked away,
// except marketSession/riskDisclosureGate/largeOrderFrictionTracker
// (left nil, which processOrderSubmission itself nil-checks and skips —
// exactly the same "unconfigured means no-op" convention those packages
// document).
func newTestOrderSubmissionDependencies(t *testing.T, seedBalances map[string]int64, matchingEngineResponseBuilder func(matchingengineclient.OrderSubmissionWireRequest) matchingengineclient.OrderSubmissionWireResponse) orderSubmissionDependencies {
	t.Helper()

	kycServer := newAlwaysEligibleKycServer(t)
	backofficeServer := newNeverFrozenBackofficeServer(t)
	ledgerAlwaysSucceeds := true
	ledgerServer := newConfigurableLedgerServer(t, &ledgerAlwaysSucceeds)
	matchingEngineAddress := newFakeMatchingEngineServer(t, matchingEngineResponseBuilder)

	return orderSubmissionDependencies{
		preTradeRiskEngine:            riskengine.NewPreTradeRiskEngineWithSeedBalances(seedBalances),
		globalSequenceNumberAllocator: sequencing.NewGlobalSequenceNumberAllocatorStartingAtOne(),
		matchingEngineClient:          matchingengineclient.NewMatchingEngineClient(matchingEngineAddress),
		ledgerClient:                  ledgerclient.NewLedgerClient(ledgerServer.URL),
		kycClient:                     kycclient.NewKycClient(kycServer.URL),
		backofficeClient:              backofficeclient.NewBackofficeClient(backofficeServer.URL),
		positionBook:                  positions.NewPositionBook(),
		auditTrail:                    audittrail.NewAuditTrail(),
		pledgeBook:                    marginpledge.NewPledgeBook(),
		paperPositionBook:             positions.NewPositionBook(),
		algoLimitsRegistry:            algolimits.NewRegistry(algolimits.StrategyLimitConfig{}), // unconfigured default = unlimited
		markToMarketEngine:            marktomarket.NewMarkToMarketEngine(),
		exposureLimitsRegistry:        exposurelimits.NewLimitsRegistry(),
		connectivityKillSwitch:        connectivitykillswitch.NewKillSwitch(1_000_000),
		milliSharePaperPositionBook:   fractionalshares.NewMilliSharePositionBook(),
	}
}

func basicSellOrder(account string, symbol string, quantity uint64, limitPrice int64) orders.OrderSubmissionRequest {
	return orders.OrderSubmissionRequest{
		ClientAccountIdentifier: account,
		InstrumentSymbol:        symbol,
		OrderSideIsBuyNotSell:   false,
		LimitPriceInMinorUnits:  limitPrice,
		OrderQuantity:           quantity,
	}
}

func basicBuyOrder(account string, symbol string, quantity uint64, limitPrice int64) orders.OrderSubmissionRequest {
	return orders.OrderSubmissionRequest{
		ClientAccountIdentifier: account,
		InstrumentSymbol:        symbol,
		OrderSideIsBuyNotSell:   true,
		LimitPriceInMinorUnits:  limitPrice,
		OrderQuantity:           quantity,
	}
}

// alwaysRestingMatchingEngineResponse simulates an order that reaches
// matching-engine and simply rests (no immediate fill) — the common case
// for testing gates upstream of the hand-off without needing real
// crossing liquidity.
func alwaysRestingMatchingEngineResponse(sequenceNumber *uint64) func(matchingengineclient.OrderSubmissionWireRequest) matchingengineclient.OrderSubmissionWireResponse {
	return func(matchingengineclient.OrderSubmissionWireRequest) matchingengineclient.OrderSubmissionWireResponse {
		*sequenceNumber++
		seq := *sequenceNumber
		return matchingengineclient.OrderSubmissionWireResponse{AssignedOrderSequenceNumber: &seq}
	}
}

// alwaysRejectingMatchingEngineResponse simulates matching-engine
// explicitly rejecting every order (e.g. unknown instrument).
func alwaysRejectingMatchingEngineResponse(matchingengineclient.OrderSubmissionWireRequest) matchingengineclient.OrderSubmissionWireResponse {
	reason := "simulated matching-engine rejection"
	return matchingengineclient.OrderSubmissionWireResponse{ErrorMessage: &reason}
}

// -------------------- Finding 3: algoLimits/exposureLimits reservation release --------------------

func TestProcessOrderSubmission_RejectedOrderReleasesAlgoLimitsReservation(t *testing.T) {
	var seq uint64
	deps := newTestOrderSubmissionDependencies(t, map[string]int64{"acct-frozen": 0}, alwaysRestingMatchingEngineResponse(&seq))
	// A strategy tightly capped at exactly one order's notional -- the
	// SECOND order must be rejected by algolimits UNLESS the first
	// order's KYC/freeze/risk rejection correctly released its
	// reservation.
	deps.algoLimitsRegistry.SetStrategyLimits("strat-1", algolimits.StrategyLimitConfig{MaxOrdersPerSecond: 0, MaxNotionalPerDayInMinorUnits: 1000})

	request := orders.OrderSubmissionRequest{
		ClientAccountIdentifier: "acct-unknown", // not seeded in riskengine -> ACCOUNT_NOT_FOUND rejection, AFTER algolimits reservation
		InstrumentSymbol:        "DEMO-EQ",
		OrderSideIsBuyNotSell:   true,
		LimitPriceInMinorUnits:  1000,
		OrderQuantity:           1,
		StrategyIdentifier:      "strat-1",
	}

	firstAck := processOrderSubmission(deps, request, "")
	if firstAck.WasOrderAccepted {
		t.Fatalf("expected first order to be rejected (unknown account), got accepted: %+v", firstAck)
	}
	if firstAck.MachineReadableRejectionReason != "ACCOUNT_NOT_FOUND" {
		t.Fatalf("expected ACCOUNT_NOT_FOUND, got %s", firstAck.MachineReadableRejectionReason)
	}

	// If the reservation from the first (rejected) order wasn't
	// released, this second, otherwise-identical-cost order would be
	// wrongly rejected by algolimits itself.
	secondAck := processOrderSubmission(deps, request, "")
	if secondAck.MachineReadableRejectionReason == "STRATEGY_DAILY_NOTIONAL_LIMIT_EXCEEDED" || secondAck.MachineReadableRejectionReason == "STRATEGY_RATE_LIMIT_EXCEEDED" {
		t.Fatalf("BUG REPRODUCED: algolimits reservation from the first rejected order was never released: %+v", secondAck)
	}
}

func TestProcessOrderSubmission_RejectedOrderReleasesExposureReservation(t *testing.T) {
	var seq uint64
	deps := newTestOrderSubmissionDependencies(t, map[string]int64{}, alwaysRestingMatchingEngineResponse(&seq))
	deps.exposureLimitsRegistry.SetAccountLimit("acct-unknown", 1000)

	request := orders.OrderSubmissionRequest{
		ClientAccountIdentifier: "acct-unknown",
		InstrumentSymbol:        "DEMO-EQ",
		OrderSideIsBuyNotSell:   true,
		LimitPriceInMinorUnits:  1000,
		OrderQuantity:           1,
	}

	firstAck := processOrderSubmission(deps, request, "")
	if firstAck.WasOrderAccepted {
		t.Fatalf("expected rejection (unknown account), got accepted")
	}

	secondAck := processOrderSubmission(deps, request, "")
	if secondAck.MachineReadableRejectionReason == "ACCOUNT_EXPOSURE_LIMIT_EXCEEDED" {
		t.Fatalf("BUG REPRODUCED: exposure reservation from the first rejected order was never released: %+v", secondAck)
	}
}

// -------------------- Finding 2: pre-trade risk engine reserve-on-approve wired through processOrderSubmission --------------------

func TestProcessOrderSubmission_ConcurrentOrdersSameAccountCannotBothOverApproveMargin(t *testing.T) {
	var seq uint64
	// Seed exactly enough margin for ONE of two 60,000-notional orders.
	deps := newTestOrderSubmissionDependencies(t, map[string]int64{"acct-1": 100_000}, alwaysRestingMatchingEngineResponse(&seq))

	firstAck := processOrderSubmission(deps, basicBuyOrder("acct-1", "DEMO-EQ", 60, 1000), "")
	secondAck := processOrderSubmission(deps, basicBuyOrder("acct-1", "DEMO-EQ", 60, 1000), "")

	acceptedCount := 0
	if firstAck.WasOrderAccepted {
		acceptedCount++
	}
	if secondAck.WasOrderAccepted {
		acceptedCount++
	}
	if acceptedCount != 1 {
		t.Fatalf("BUG REPRODUCED: expected exactly 1 of 2 orders (60000 notional each, 100000 available) to be approved, got %d accepted (first=%+v second=%+v)", acceptedCount, firstAck, secondAck)
	}
}

// -------------------- Finding 4: ledger settlement failure must not silently apply the fill --------------------

func TestProcessOrderSubmission_LedgerSettlementFailureDoesNotApplyPositionBook(t *testing.T) {
	var seq uint64
	deps := newTestOrderSubmissionDependencies(t, map[string]int64{"buyer": 1_000_000, "seller": 1_000_000},
		func(req matchingengineclient.OrderSubmissionWireRequest) matchingengineclient.OrderSubmissionWireResponse {
			seq++
			s := seq
			return matchingengineclient.OrderSubmissionWireResponse{
				AssignedOrderSequenceNumber: &s,
				TradeExecutionEvents: []matchingengineclient.TradeExecutionWireEvent{
					{BuyingClientAccountId: "buyer", SellingClientAccountId: "seller", ExecutedPriceInMinorUnits: 100, ExecutedQuantity: 10},
				},
			}
		},
	)
	// Force the ledger to fail settlement for this call.
	ledgerFails := false
	ledgerServerFails := newConfigurableLedgerServer(t, &ledgerFails)
	deps.ledgerClient = ledgerclient.NewLedgerClient(ledgerServerFails.URL)

	ack := processOrderSubmission(deps, basicBuyOrder("buyer", "DEMO-EQ", 10, 100), "")

	if !ack.WasOrderAccepted {
		t.Fatalf("expected order to remain accepted despite settlement failure, got: %+v", ack)
	}
	if len(ack.TradeExecutionEvents) != 1 {
		t.Fatalf("expected the real trade to still be reported in TradeExecutionEvents, got %d", len(ack.TradeExecutionEvents))
	}
	if len(ack.SettlementFailures) != 1 {
		t.Fatalf("expected exactly 1 surfaced settlement failure, got %d: %+v", len(ack.SettlementFailures), ack.SettlementFailures)
	}

	buyerPosition := deps.positionBook.PositionsForAccount("buyer")["DEMO-EQ"]
	sellerPosition := deps.positionBook.PositionsForAccount("seller")["DEMO-EQ"]
	if buyerPosition != 0 || sellerPosition != 0 {
		t.Fatalf("BUG REPRODUCED: position book was updated despite a failed ledger settlement -- buyer=%d seller=%d (expected both 0)", buyerPosition, sellerPosition)
	}

	foundAuditEntry := false
	for _, entry := range deps.auditTrail.AllEntries() {
		if entry.EventType == audittrail.EventSettlementFailedPositionNotApplied {
			foundAuditEntry = true
		}
	}
	if !foundAuditEntry {
		t.Fatalf("expected a SETTLEMENT_FAILED_POSITION_NOT_APPLIED audit entry")
	}
}

func TestProcessOrderSubmission_SuccessfulSettlementStillAppliesPositionBook(t *testing.T) {
	var seq uint64
	deps := newTestOrderSubmissionDependencies(t, map[string]int64{"buyer": 1_000_000, "seller": 1_000_000},
		func(req matchingengineclient.OrderSubmissionWireRequest) matchingengineclient.OrderSubmissionWireResponse {
			seq++
			s := seq
			return matchingengineclient.OrderSubmissionWireResponse{
				AssignedOrderSequenceNumber: &s,
				TradeExecutionEvents: []matchingengineclient.TradeExecutionWireEvent{
					{BuyingClientAccountId: "buyer", SellingClientAccountId: "seller", ExecutedPriceInMinorUnits: 100, ExecutedQuantity: 10},
				},
			}
		},
	)

	ack := processOrderSubmission(deps, basicBuyOrder("buyer", "DEMO-EQ", 10, 100), "")

	if len(ack.SettlementFailures) != 0 {
		t.Fatalf("expected no settlement failures, got %+v", ack.SettlementFailures)
	}
	if got := deps.positionBook.PositionsForAccount("buyer")["DEMO-EQ"]; got != 10 {
		t.Fatalf("expected buyer position 10, got %d", got)
	}
}

// -------------------- Finding 5: pledged-holding sell reservation --------------------

func TestProcessOrderSubmission_ConcurrentSellsAgainstSameUnpledgedHoldingCannotBothSucceed(t *testing.T) {
	var seq uint64
	deps := newTestOrderSubmissionDependencies(t, map[string]int64{"acct-1": 10_000_000}, alwaysRestingMatchingEngineResponse(&seq))
	deps.positionBook.SetPositionDirectly("acct-1", "DEMO-EQ", 100) // 100 shares held, none pledged

	// Two sell orders of 60 shares each against a 100-share holding: only
	// one can be accepted; the other must be rejected as
	// PLEDGED_QUANTITY_UNAVAILABLE (oversell prevention).
	firstAck := processOrderSubmission(deps, basicSellOrder("acct-1", "DEMO-EQ", 60, 1000), "")
	secondAck := processOrderSubmission(deps, basicSellOrder("acct-1", "DEMO-EQ", 60, 1000), "")

	acceptedCount := 0
	if firstAck.WasOrderAccepted {
		acceptedCount++
	}
	if secondAck.WasOrderAccepted {
		acceptedCount++
	}
	if acceptedCount != 1 {
		t.Fatalf("BUG REPRODUCED: expected exactly 1 of 2 sixty-share sells against a 100-share holding to be accepted, got %d (first=%+v second=%+v)", acceptedCount, firstAck, secondAck)
	}
}

// -------------------- Finding 6: market-session open/close must reject non-POST --------------------

func TestBuildMarketSessionCloseHandler_RejectsNonPost(t *testing.T) {
	handler := buildMarketSessionCloseHandler(marketsession.NewMarketSessionState(), audittrail.NewAuditTrail())

	request := httptest.NewRequest(http.MethodGet, "/market-session/close", nil)
	recorder := httptest.NewRecorder()
	handler(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("BUG REPRODUCED: expected 405 for GET /market-session/close, got %d", recorder.Code)
	}
}

func TestBuildMarketSessionOpenHandler_RejectsNonPost(t *testing.T) {
	deps := newTestOrderSubmissionDependencies(t, map[string]int64{}, alwaysRestingMatchingEngineResponse(new(uint64)))
	handler := buildMarketSessionOpenHandler(marketsession.NewMarketSessionState(), amoqueue.NewAfterMarketOrderQueue(), deps)

	request := httptest.NewRequest(http.MethodGet, "/market-session/open", nil)
	recorder := httptest.NewRecorder()
	handler(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("BUG REPRODUCED: expected 405 for GET /market-session/open, got %d", recorder.Code)
	}
}

// -------------------- Finding 7: AMO drain partial-failure visibility --------------------

func TestBuildMarketSessionOpenHandler_SurfacesRejectedAmoCounts(t *testing.T) {
	var seq uint64
	deps := newTestOrderSubmissionDependencies(t, map[string]int64{"acct-good": 1_000_000}, alwaysRestingMatchingEngineResponse(&seq))
	marketSessionState := marketsession.NewMarketSessionState()
	queue := amoqueue.NewAfterMarketOrderQueue()

	// One order that will be accepted (known, funded account) and one
	// that will be rejected (unknown account -> ACCOUNT_NOT_FOUND).
	queue.Enqueue(basicBuyOrder("acct-good", "DEMO-EQ", 1, 1000))
	queue.Enqueue(basicBuyOrder("acct-unknown", "DEMO-EQ", 1, 1000))

	handler := buildMarketSessionOpenHandler(marketSessionState, queue, deps)
	request := httptest.NewRequest(http.MethodPost, "/market-session/open", nil)
	recorder := httptest.NewRecorder()
	handler(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		TotalDrainedAfterMarketOrders int `json:"totalDrainedAfterMarketOrders"`
		AcceptedAfterMarketOrders     int `json:"acceptedAfterMarketOrders"`
		RejectedAfterMarketOrders     int `json:"rejectedAfterMarketOrders"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode response: %v (body=%s)", err, recorder.Body.String())
	}

	if response.TotalDrainedAfterMarketOrders != 2 {
		t.Fatalf("BUG REPRODUCED (no visibility): expected totalDrainedAfterMarketOrders=2, got %d", response.TotalDrainedAfterMarketOrders)
	}
	if response.AcceptedAfterMarketOrders != 1 || response.RejectedAfterMarketOrders != 1 {
		t.Fatalf("BUG REPRODUCED (no visibility): expected 1 accepted and 1 rejected AMO reported, got accepted=%d rejected=%d",
			response.AcceptedAfterMarketOrders, response.RejectedAfterMarketOrders)
	}
}
