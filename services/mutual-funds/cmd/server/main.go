// Mercurius / mutual-funds
//
// See FEATURES.md §4 for the full intended scope this service must
// eventually cover. As of this build, all six items in that section are
// real (if simulated) skeletons:
//   - Direct AMC routing (internal/amcrouting) — a real PENDING→CONFIRMED
//     order state machine standing in for an actual AMC/RTA integration,
//     which does not exist anywhere in this repo. See that package's doc
//     comment for the loud caveat.
//   - Lumpsum + SIP setup, pause/cancel/calendar (internal/sipscheduler)
//   - Step-Up SIPs (also internal/sipscheduler)
//   - Index/thematic rebalancing baskets with one-click rebalance
//     (internal/basketrebalancing)
//   - Robo-Advisory: risk-profile → illustrative model allocation, wired
//     to quant-engine's real Sharpe/Sortino/max-drawdown endpoint
//     (internal/roboadvisory)
//   - Goal-based investing with progress tracking (internal/goalinvesting)
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"mercurius/mutualFunds/internal/amcrouting"
	"mercurius/mutualFunds/internal/basketrebalancing"
	"mercurius/mutualFunds/internal/fundcatalog"
	"mercurius/mutualFunds/internal/goalinvesting"
	"mercurius/mutualFunds/internal/roboadvisory"
	"mercurius/mutualFunds/internal/sipscheduler"
)

func main() {
	fundCatalog := fundcatalog.NewFundCatalog()

	confirmationDelay := 24 * time.Hour
	if rawDelayHours := os.Getenv("MUTUAL_FUND_ORDER_CONFIRMATION_DELAY_HOURS"); rawDelayHours != "" {
		parsedHours, parseError := strconv.Atoi(rawDelayHours)
		if parseError != nil {
			log.Fatalf("invalid MUTUAL_FUND_ORDER_CONFIRMATION_DELAY_HOURS: %v", parseError)
		}
		confirmationDelay = time.Duration(parsedHours) * time.Hour
	}

	amcOrderRouter := amcrouting.NewAmcOrderRouter(fundCatalog, confirmationDelay)
	sipScheduler := sipscheduler.NewSipScheduler(fundCatalog, amcOrderRouter)
	basketRebalancer := basketrebalancing.NewBasketRebalancer(fundCatalog, amcOrderRouter)
	goalTracker := goalinvesting.NewGoalTracker(fundCatalog, amcOrderRouter)

	quantEngineBaseUrl := os.Getenv("QUANT_ENGINE_BASE_URL")
	if quantEngineBaseUrl == "" {
		quantEngineBaseUrl = "http://127.0.0.1:8085"
	}
	quantEngineClient := roboadvisory.NewQuantEngineClient(quantEngineBaseUrl)

	httpRequestMultiplexer := http.NewServeMux()
	httpRequestMultiplexer.HandleFunc("/health", func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = responseWriter.Write([]byte(`{"status":"ok","service":"mutual-funds"}`))
	})

	httpRequestMultiplexer.HandleFunc("/schemes", buildListSchemesHandler(fundCatalog))
	httpRequestMultiplexer.HandleFunc("/schemes/update-nav", buildUpdateNavHandler(fundCatalog))

	httpRequestMultiplexer.HandleFunc("/orders/lumpsum-purchase", buildLumpsumPurchaseHandler(amcOrderRouter))
	httpRequestMultiplexer.HandleFunc("/orders/redemption", buildRedemptionHandler(amcOrderRouter))
	httpRequestMultiplexer.HandleFunc("/orders/confirm-due", buildConfirmDueOrdersHandler(amcOrderRouter))
	httpRequestMultiplexer.HandleFunc("/orders", buildListOrdersHandler(amcOrderRouter))
	httpRequestMultiplexer.HandleFunc("/holdings", buildListHoldingsHandler(amcOrderRouter))

	httpRequestMultiplexer.HandleFunc("/sips/register", buildRegisterSipHandler(sipScheduler))
	httpRequestMultiplexer.HandleFunc("/sips/pause", buildPauseSipHandler(sipScheduler))
	httpRequestMultiplexer.HandleFunc("/sips/resume", buildResumeSipHandler(sipScheduler))
	httpRequestMultiplexer.HandleFunc("/sips/cancel", buildCancelSipHandler(sipScheduler))
	httpRequestMultiplexer.HandleFunc("/sips/sweep-due", buildSweepDueSipsHandler(sipScheduler))
	httpRequestMultiplexer.HandleFunc("/sips", buildListSipsHandler(sipScheduler))

	httpRequestMultiplexer.HandleFunc("/baskets/create", buildCreateBasketHandler(basketRebalancer))
	httpRequestMultiplexer.HandleFunc("/baskets/subscribe", buildSubscribeToBasketHandler(basketRebalancer))
	httpRequestMultiplexer.HandleFunc("/baskets/rebalance", buildRebalanceAccountBasketHandler(basketRebalancer))
	httpRequestMultiplexer.HandleFunc("/baskets", buildListBasketsHandler(basketRebalancer))

	httpRequestMultiplexer.HandleFunc("/robo-advisory/recommend", buildRoboAdvisoryRecommendHandler(quantEngineClient))

	httpRequestMultiplexer.HandleFunc("/goals/create", buildCreateGoalHandler(goalTracker))
	httpRequestMultiplexer.HandleFunc("/goals/progress", buildGoalProgressHandler(goalTracker))
	httpRequestMultiplexer.HandleFunc("/goals", buildListGoalsHandler(goalTracker))

	listenAddress := ":8087"
	log.Printf("mutual-funds listening on %s (order confirmation delay: %s)\n", listenAddress, confirmationDelay)
	if serverStartupError := http.ListenAndServe(listenAddress, httpRequestMultiplexer); serverStartupError != nil {
		log.Fatalf("mutual-funds failed to start: %v", serverStartupError)
	}
}

func respondWithJson(responseWriter http.ResponseWriter, statusCode int, body any) {
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(statusCode)
	_ = json.NewEncoder(responseWriter).Encode(body)
}

// resolveNow lets a caller optionally override "now" via an ?asOf=
// RFC3339 query parameter — a demo/testing hook for exercising
// time-dependent sweep logic (order confirmation, SIP due dates, step-up
// anniversaries) deterministically without waiting real days or months.
// Defaults to the real wall clock when absent.
func resolveNow(request *http.Request) (time.Time, error) {
	rawAsOf := request.URL.Query().Get("asOf")
	if rawAsOf == "" {
		return time.Now(), nil
	}
	return time.Parse(time.RFC3339, rawAsOf)
}

type schemeWireFormat struct {
	SchemeId               string `json:"schemeId"`
	Name                   string `json:"name"`
	Category               string `json:"category"`
	CurrentNavInMinorUnits int64  `json:"currentNavInMinorUnits"`
}

func buildListSchemesHandler(fundCatalog *fundcatalog.FundCatalog) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, _ *http.Request) {
		schemes := fundCatalog.ListAll()
		wireSchemes := make([]schemeWireFormat, 0, len(schemes))
		for _, scheme := range schemes {
			wireSchemes = append(wireSchemes, schemeWireFormat{
				SchemeId:               scheme.SchemeId,
				Name:                   scheme.Name,
				Category:               string(scheme.Category),
				CurrentNavInMinorUnits: scheme.CurrentNavInMinorUnits,
			})
		}
		respondWithJson(responseWriter, http.StatusOK, wireSchemes)
	}
}

type updateNavWireRequest struct {
	SchemeId           string `json:"schemeId"`
	NewNavInMinorUnits int64  `json:"newNavInMinorUnits"`
}

// buildUpdateNavHandler exposes fundcatalog's UpdateNav over HTTP — a
// testing/demo-only hook for simulating a market move (see that
// function's doc comment), same honesty caveat as ?asOf=: a real build
// would never let an external caller dictate a scheme's NAV directly, it
// would come from ingesting AMFI's daily NAV file or an AMC/RTA feed.
func buildUpdateNavHandler(fundCatalog *fundcatalog.FundCatalog) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest updateNavWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed update-nav payload", http.StatusBadRequest)
			return
		}

		if updateError := fundCatalog.UpdateNav(wireRequest.SchemeId, wireRequest.NewNavInMinorUnits); updateError != nil {
			http.Error(responseWriter, updateError.Error(), http.StatusBadRequest)
			return
		}

		scheme, _ := fundCatalog.Lookup(wireRequest.SchemeId)
		respondWithJson(responseWriter, http.StatusOK, schemeWireFormat{
			SchemeId:               scheme.SchemeId,
			Name:                   scheme.Name,
			Category:               string(scheme.Category),
			CurrentNavInMinorUnits: scheme.CurrentNavInMinorUnits,
		})
	}
}

type orderWireFormat struct {
	OrderId                       string  `json:"orderId"`
	AccountIdentifier             string  `json:"accountIdentifier"`
	SchemeId                      string  `json:"schemeId"`
	OrderType                     string  `json:"orderType"`
	AmountInvestedInMinorUnits    int64   `json:"amountInvestedInMinorUnits,omitempty"`
	UnitsRequestedForRedemption   float64 `json:"unitsRequestedForRedemption,omitempty"`
	Status                        string  `json:"status"`
	PlacedAt                      string  `json:"placedAt"`
	EligibleForConfirmationAt     string  `json:"eligibleForConfirmationAt"`
	ConfirmedAt                   string  `json:"confirmedAt,omitempty"`
	NavAtConfirmationInMinorUnits int64   `json:"navAtConfirmationInMinorUnits,omitempty"`
	UnitsAllocated                float64 `json:"unitsAllocated,omitempty"`
	AmountCreditedInMinorUnits    int64   `json:"amountCreditedInMinorUnits,omitempty"`
	ErrorMessage                  string  `json:"errorMessage,omitempty"`
}

func toOrderWireFormat(order *amcrouting.Order) orderWireFormat {
	wireFormat := orderWireFormat{
		OrderId:                       order.OrderId,
		AccountIdentifier:             order.AccountIdentifier,
		SchemeId:                      order.SchemeId,
		OrderType:                     string(order.OrderType),
		AmountInvestedInMinorUnits:    order.AmountInvestedInMinorUnits,
		UnitsRequestedForRedemption:   order.UnitsRequestedForRedemption,
		Status:                        string(order.Status),
		PlacedAt:                      order.PlacedAt.Format(time.RFC3339),
		EligibleForConfirmationAt:     order.EligibleForConfirmationAt.Format(time.RFC3339),
		NavAtConfirmationInMinorUnits: order.NavAtConfirmationInMinorUnits,
		UnitsAllocated:                order.UnitsAllocated,
		AmountCreditedInMinorUnits:    order.AmountCreditedInMinorUnits,
	}
	if !order.ConfirmedAt.IsZero() {
		wireFormat.ConfirmedAt = order.ConfirmedAt.Format(time.RFC3339)
	}
	return wireFormat
}

type lumpsumPurchaseWireRequest struct {
	AccountIdentifier  string `json:"accountIdentifier"`
	SchemeId           string `json:"schemeId"`
	AmountInMinorUnits int64  `json:"amountInMinorUnits"`
}

func buildLumpsumPurchaseHandler(amcOrderRouter *amcrouting.AmcOrderRouter) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest lumpsumPurchaseWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed lumpsum purchase payload", http.StatusBadRequest)
			return
		}

		now, nowError := resolveNow(request)
		if nowError != nil {
			http.Error(responseWriter, "malformed asOf query parameter, expected RFC3339", http.StatusBadRequest)
			return
		}

		order, placeError := amcOrderRouter.PlacePurchaseOrder(wireRequest.AccountIdentifier, wireRequest.SchemeId, wireRequest.AmountInMinorUnits, now)
		if placeError != nil {
			respondWithJson(responseWriter, http.StatusBadRequest, orderWireFormat{ErrorMessage: placeError.Error()})
			return
		}

		respondWithJson(responseWriter, http.StatusOK, toOrderWireFormat(order))
	}
}

type redemptionWireRequest struct {
	AccountIdentifier string  `json:"accountIdentifier"`
	SchemeId          string  `json:"schemeId"`
	UnitsToRedeem     float64 `json:"unitsToRedeem"`
}

func buildRedemptionHandler(amcOrderRouter *amcrouting.AmcOrderRouter) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest redemptionWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed redemption payload", http.StatusBadRequest)
			return
		}

		now, nowError := resolveNow(request)
		if nowError != nil {
			http.Error(responseWriter, "malformed asOf query parameter, expected RFC3339", http.StatusBadRequest)
			return
		}

		order, placeError := amcOrderRouter.PlaceRedemptionOrder(wireRequest.AccountIdentifier, wireRequest.SchemeId, wireRequest.UnitsToRedeem, now)
		if placeError != nil {
			respondWithJson(responseWriter, http.StatusBadRequest, orderWireFormat{ErrorMessage: placeError.Error()})
			return
		}

		respondWithJson(responseWriter, http.StatusOK, toOrderWireFormat(order))
	}
}

type confirmDueOrdersWireResponse struct {
	ConfirmedOrders []orderWireFormat `json:"confirmedOrders"`
	FailedOrderIds  []string          `json:"failedOrderIds,omitempty"`
}

func buildConfirmDueOrdersHandler(amcOrderRouter *amcrouting.AmcOrderRouter) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		now, nowError := resolveNow(request)
		if nowError != nil {
			http.Error(responseWriter, "malformed asOf query parameter, expected RFC3339", http.StatusBadRequest)
			return
		}

		confirmedOrders, failedOrderIds := amcOrderRouter.ConfirmDueOrders(now)
		wireOrders := make([]orderWireFormat, 0, len(confirmedOrders))
		for _, order := range confirmedOrders {
			wireOrders = append(wireOrders, toOrderWireFormat(order))
		}

		respondWithJson(responseWriter, http.StatusOK, confirmDueOrdersWireResponse{
			ConfirmedOrders: wireOrders,
			FailedOrderIds:  failedOrderIds,
		})
	}
}

func buildListOrdersHandler(amcOrderRouter *amcrouting.AmcOrderRouter) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}

		orders := amcOrderRouter.OrdersForAccount(accountIdentifier)
		wireOrders := make([]orderWireFormat, 0, len(orders))
		for _, order := range orders {
			wireOrders = append(wireOrders, toOrderWireFormat(order))
		}
		respondWithJson(responseWriter, http.StatusOK, wireOrders)
	}
}

type holdingWireFormat struct {
	SchemeId                   string  `json:"schemeId"`
	TotalUnits                 float64 `json:"totalUnits"`
	UnitsReservedForRedemption float64 `json:"unitsReservedForRedemption"`
	AvailableUnits             float64 `json:"availableUnits"`
}

func buildListHoldingsHandler(amcOrderRouter *amcrouting.AmcOrderRouter) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}

		holdings := amcOrderRouter.HoldingsForAccount(accountIdentifier)
		wireHoldings := make([]holdingWireFormat, 0, len(holdings))
		for _, holding := range holdings {
			wireHoldings = append(wireHoldings, holdingWireFormat{
				SchemeId:                   holding.SchemeId,
				TotalUnits:                 holding.TotalUnits,
				UnitsReservedForRedemption: holding.UnitsReservedForRedemption,
				AvailableUnits:             holding.AvailableUnits(),
			})
		}
		respondWithJson(responseWriter, http.StatusOK, wireHoldings)
	}
}

type sipWireFormat struct {
	SipId                                string  `json:"sipId"`
	AccountIdentifier                    string  `json:"accountIdentifier"`
	SchemeId                             string  `json:"schemeId"`
	BaseInstallmentAmountInMinorUnits    int64   `json:"baseInstallmentAmountInMinorUnits"`
	CurrentInstallmentAmountInMinorUnits int64   `json:"currentInstallmentAmountInMinorUnits"`
	Frequency                            string  `json:"frequency"`
	StartDate                            string  `json:"startDate"`
	NextDueDate                          string  `json:"nextDueDate"`
	AnnualStepUpPercent                  float64 `json:"annualStepUpPercent"`
	InstallmentsExecuted                 int     `json:"installmentsExecuted"`
	Status                               string  `json:"status"`
	ErrorMessage                         string  `json:"errorMessage,omitempty"`
}

func toSipWireFormat(sip *sipscheduler.Sip) sipWireFormat {
	return sipWireFormat{
		SipId:                                sip.SipId,
		AccountIdentifier:                    sip.AccountIdentifier,
		SchemeId:                             sip.SchemeId,
		BaseInstallmentAmountInMinorUnits:    sip.BaseInstallmentAmountInMinorUnits,
		CurrentInstallmentAmountInMinorUnits: sip.CurrentInstallmentAmountInMinorUnits,
		Frequency:                            string(sip.Frequency),
		StartDate:                            sip.StartDate.Format(time.RFC3339),
		NextDueDate:                          sip.NextDueDate.Format(time.RFC3339),
		AnnualStepUpPercent:                  sip.AnnualStepUpPercent,
		InstallmentsExecuted:                 sip.InstallmentsExecuted,
		Status:                               string(sip.Status),
	}
}

type registerSipWireRequest struct {
	AccountIdentifier             string  `json:"accountIdentifier"`
	SchemeId                      string  `json:"schemeId"`
	InstallmentAmountInMinorUnits int64   `json:"installmentAmountInMinorUnits"`
	Frequency                     string  `json:"frequency"`
	StartDate                     string  `json:"startDate"`
	AnnualStepUpPercent           float64 `json:"annualStepUpPercent,omitempty"`
}

func buildRegisterSipHandler(sipScheduler *sipscheduler.SipScheduler) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest registerSipWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed SIP registration payload", http.StatusBadRequest)
			return
		}

		startDate, parseError := time.Parse(time.RFC3339, wireRequest.StartDate)
		if parseError != nil {
			respondWithJson(responseWriter, http.StatusBadRequest, sipWireFormat{ErrorMessage: "startDate must be RFC3339"})
			return
		}

		sip, registerError := sipScheduler.RegisterSip(
			wireRequest.AccountIdentifier,
			wireRequest.SchemeId,
			wireRequest.InstallmentAmountInMinorUnits,
			sipscheduler.SipFrequency(wireRequest.Frequency),
			startDate,
			wireRequest.AnnualStepUpPercent,
		)
		if registerError != nil {
			respondWithJson(responseWriter, http.StatusBadRequest, sipWireFormat{ErrorMessage: registerError.Error()})
			return
		}

		respondWithJson(responseWriter, http.StatusOK, toSipWireFormat(sip))
	}
}

type sipIdWireRequest struct {
	SipId string `json:"sipId"`
}

func buildPauseSipHandler(sipScheduler *sipscheduler.SipScheduler) http.HandlerFunc {
	return buildSipMutationHandler(sipScheduler.PauseSip)
}

func buildResumeSipHandler(sipScheduler *sipscheduler.SipScheduler) http.HandlerFunc {
	return buildSipMutationHandler(sipScheduler.ResumeSip)
}

func buildCancelSipHandler(sipScheduler *sipscheduler.SipScheduler) http.HandlerFunc {
	return buildSipMutationHandler(sipScheduler.CancelSip)
}

func buildSipMutationHandler(mutate func(sipId string) (*sipscheduler.Sip, error)) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest sipIdWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed payload, expected {\"sipId\": \"...\"}", http.StatusBadRequest)
			return
		}

		sip, mutateError := mutate(wireRequest.SipId)
		if mutateError != nil {
			respondWithJson(responseWriter, http.StatusBadRequest, sipWireFormat{ErrorMessage: mutateError.Error()})
			return
		}

		respondWithJson(responseWriter, http.StatusOK, toSipWireFormat(sip))
	}
}

type sweepDueSipsWireResponse struct {
	Executed     []sipExecutionResultWireFormat `json:"executed"`
	FailedSipIds []string                       `json:"failedSipIds,omitempty"`
}

type sipExecutionResultWireFormat struct {
	SipId                         string `json:"sipId"`
	OrderId                       string `json:"orderId"`
	InstallmentAmountInMinorUnits int64  `json:"installmentAmountInMinorUnits"`
	ExecutedAt                    string `json:"executedAt"`
	NewNextDueDate                string `json:"newNextDueDate"`
}

func buildSweepDueSipsHandler(sipScheduler *sipscheduler.SipScheduler) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		now, nowError := resolveNow(request)
		if nowError != nil {
			http.Error(responseWriter, "malformed asOf query parameter, expected RFC3339", http.StatusBadRequest)
			return
		}

		executed, failedSipIds := sipScheduler.SweepDueSips(now)
		wireExecuted := make([]sipExecutionResultWireFormat, 0, len(executed))
		for _, result := range executed {
			wireExecuted = append(wireExecuted, sipExecutionResultWireFormat{
				SipId:                         result.SipId,
				OrderId:                       result.OrderId,
				InstallmentAmountInMinorUnits: result.InstallmentAmountInMinorUnits,
				ExecutedAt:                    result.ExecutedAt.Format(time.RFC3339),
				NewNextDueDate:                result.NewNextDueDate.Format(time.RFC3339),
			})
		}

		respondWithJson(responseWriter, http.StatusOK, sweepDueSipsWireResponse{
			Executed:     wireExecuted,
			FailedSipIds: failedSipIds,
		})
	}
}

func buildListSipsHandler(sipScheduler *sipscheduler.SipScheduler) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}

		sips := sipScheduler.ListSipsForAccount(accountIdentifier)
		wireSips := make([]sipWireFormat, 0, len(sips))
		for _, sip := range sips {
			wireSips = append(wireSips, toSipWireFormat(sip))
		}
		respondWithJson(responseWriter, http.StatusOK, wireSips)
	}
}

// --- internal/basketrebalancing wire handlers ---

type basketConstituentWireFormat struct {
	SchemeId            string  `json:"schemeId"`
	TargetWeightPercent float64 `json:"targetWeightPercent"`
}

type basketWireFormat struct {
	BasketId     string                        `json:"basketId,omitempty"`
	Name         string                        `json:"name,omitempty"`
	Constituents []basketConstituentWireFormat `json:"constituents,omitempty"`
	CreatedAt    string                        `json:"createdAt,omitempty"`
	ErrorMessage string                        `json:"errorMessage,omitempty"`
}

func toBasketWireFormat(basket *basketrebalancing.Basket) basketWireFormat {
	wireConstituents := make([]basketConstituentWireFormat, 0, len(basket.Constituents))
	for _, constituent := range basket.Constituents {
		wireConstituents = append(wireConstituents, basketConstituentWireFormat{
			SchemeId:            constituent.SchemeId,
			TargetWeightPercent: constituent.TargetWeightPercent,
		})
	}
	return basketWireFormat{
		BasketId:     basket.BasketId,
		Name:         basket.Name,
		Constituents: wireConstituents,
		CreatedAt:    basket.CreatedAt.Format(time.RFC3339),
	}
}

type createBasketWireRequest struct {
	Name                          string             `json:"name"`
	TargetWeightPercentBySchemeId map[string]float64 `json:"targetWeightPercentBySchemeId"`
}

func buildCreateBasketHandler(basketRebalancer *basketrebalancing.BasketRebalancer) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest createBasketWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed basket creation payload", http.StatusBadRequest)
			return
		}

		basket, createError := basketRebalancer.CreateBasket(wireRequest.Name, wireRequest.TargetWeightPercentBySchemeId)
		if createError != nil {
			respondWithJson(responseWriter, http.StatusBadRequest, basketWireFormat{ErrorMessage: createError.Error()})
			return
		}

		respondWithJson(responseWriter, http.StatusOK, toBasketWireFormat(basket))
	}
}

func buildListBasketsHandler(basketRebalancer *basketrebalancing.BasketRebalancer) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, _ *http.Request) {
		baskets := basketRebalancer.ListBaskets()
		wireBaskets := make([]basketWireFormat, 0, len(baskets))
		for _, basket := range baskets {
			wireBaskets = append(wireBaskets, toBasketWireFormat(basket))
		}
		respondWithJson(responseWriter, http.StatusOK, wireBaskets)
	}
}

type subscribeToBasketWireRequest struct {
	AccountIdentifier         string `json:"accountIdentifier"`
	BasketId                  string `json:"basketId"`
	LumpsumAmountInMinorUnits int64  `json:"lumpsumAmountInMinorUnits"`
}

type subscriptionOrderWireFormat struct {
	SchemeId           string `json:"schemeId"`
	AmountInMinorUnits int64  `json:"amountInMinorUnits"`
	OrderId            string `json:"orderId"`
}

type subscribeToBasketWireResponse struct {
	Orders       []subscriptionOrderWireFormat `json:"orders,omitempty"`
	ErrorMessage string                        `json:"errorMessage,omitempty"`
}

func buildSubscribeToBasketHandler(basketRebalancer *basketrebalancing.BasketRebalancer) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest subscribeToBasketWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed basket subscription payload", http.StatusBadRequest)
			return
		}

		now, nowError := resolveNow(request)
		if nowError != nil {
			http.Error(responseWriter, "malformed asOf query parameter, expected RFC3339", http.StatusBadRequest)
			return
		}

		orders, subscribeError := basketRebalancer.SubscribeToBasket(wireRequest.AccountIdentifier, wireRequest.BasketId, wireRequest.LumpsumAmountInMinorUnits, now)
		if subscribeError != nil {
			respondWithJson(responseWriter, http.StatusBadRequest, subscribeToBasketWireResponse{ErrorMessage: subscribeError.Error()})
			return
		}

		wireOrders := make([]subscriptionOrderWireFormat, 0, len(orders))
		for _, order := range orders {
			wireOrders = append(wireOrders, subscriptionOrderWireFormat{
				SchemeId:           order.SchemeId,
				AmountInMinorUnits: order.AmountInMinorUnits,
				OrderId:            order.OrderId,
			})
		}
		respondWithJson(responseWriter, http.StatusOK, subscribeToBasketWireResponse{Orders: wireOrders})
	}
}

type rebalanceAccountBasketWireRequest struct {
	AccountIdentifier string `json:"accountIdentifier"`
	BasketId          string `json:"basketId"`
}

type rebalanceActionWireFormat struct {
	SchemeId                 string  `json:"schemeId"`
	Action                   string  `json:"action"`
	CurrentValueInMinorUnits int64   `json:"currentValueInMinorUnits"`
	TargetValueInMinorUnits  int64   `json:"targetValueInMinorUnits"`
	AmountInMinorUnits       int64   `json:"amountInMinorUnits"`
	UnitsToSell              float64 `json:"unitsToSell,omitempty"`
	OrderId                  string  `json:"orderId,omitempty"`
	ErrorMessage             string  `json:"errorMessage,omitempty"`
}

type rebalanceAccountBasketWireResponse struct {
	Actions      []rebalanceActionWireFormat `json:"actions,omitempty"`
	ErrorMessage string                      `json:"errorMessage,omitempty"`
}

func buildRebalanceAccountBasketHandler(basketRebalancer *basketrebalancing.BasketRebalancer) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest rebalanceAccountBasketWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed rebalance payload", http.StatusBadRequest)
			return
		}

		now, nowError := resolveNow(request)
		if nowError != nil {
			http.Error(responseWriter, "malformed asOf query parameter, expected RFC3339", http.StatusBadRequest)
			return
		}

		actions, rebalanceError := basketRebalancer.RebalanceAccountBasket(wireRequest.AccountIdentifier, wireRequest.BasketId, now)
		if rebalanceError != nil {
			respondWithJson(responseWriter, http.StatusBadRequest, rebalanceAccountBasketWireResponse{ErrorMessage: rebalanceError.Error()})
			return
		}

		wireActions := make([]rebalanceActionWireFormat, 0, len(actions))
		for _, action := range actions {
			wireActions = append(wireActions, rebalanceActionWireFormat{
				SchemeId:                 action.SchemeId,
				Action:                   string(action.Action),
				CurrentValueInMinorUnits: action.CurrentValueInMinorUnits,
				TargetValueInMinorUnits:  action.TargetValueInMinorUnits,
				AmountInMinorUnits:       action.AmountInMinorUnits,
				UnitsToSell:              action.UnitsToSell,
				OrderId:                  action.OrderId,
				ErrorMessage:             action.ErrorMessage,
			})
		}
		respondWithJson(responseWriter, http.StatusOK, rebalanceAccountBasketWireResponse{Actions: wireActions})
	}
}

// --- internal/roboadvisory wire handlers ---

type roboAdvisoryRecommendWireRequest struct {
	RiskCategory string `json:"riskCategory"`
}

type riskStatisticsWireFormat struct {
	AnnualizedSharpeRatio            float64 `json:"annualizedSharpeRatio"`
	AnnualizedSortinoRatio           float64 `json:"annualizedSortinoRatio"`
	MaximumDrawdownFraction          float64 `json:"maximumDrawdownFraction"`
	MaximumDrawdownPeakEquityValue   float64 `json:"maximumDrawdownPeakEquityValue"`
	MaximumDrawdownTroughEquityValue float64 `json:"maximumDrawdownTroughEquityValue"`
}

type roboAdvisoryRecommendWireResponse struct {
	RiskCategory             string                    `json:"riskCategory,omitempty"`
	EquityPercent            float64                   `json:"equityPercent,omitempty"`
	DebtPercent              float64                   `json:"debtPercent,omitempty"`
	HybridPercent            float64                   `json:"hybridPercent,omitempty"`
	IllustrativeReturnSeries []float64                 `json:"illustrativeReturnSeries,omitempty"`
	RiskStatistics           *riskStatisticsWireFormat `json:"riskStatistics,omitempty"`
	RiskStatisticsError      string                    `json:"riskStatisticsError,omitempty"`
	ErrorMessage             string                    `json:"errorMessage,omitempty"`
}

func buildRoboAdvisoryRecommendHandler(quantEngineClient *roboadvisory.QuantEngineClient) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest roboAdvisoryRecommendWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed robo-advisory request payload", http.StatusBadRequest)
			return
		}

		recommendation, recommendError := roboadvisory.RecommendAllocation(request.Context(), roboadvisory.RiskCategory(wireRequest.RiskCategory), quantEngineClient)
		if recommendError != nil {
			respondWithJson(responseWriter, http.StatusBadRequest, roboAdvisoryRecommendWireResponse{ErrorMessage: recommendError.Error()})
			return
		}

		wireResponse := roboAdvisoryRecommendWireResponse{
			RiskCategory:             string(recommendation.RiskCategory),
			EquityPercent:            recommendation.Allocation.EquityPercent,
			DebtPercent:              recommendation.Allocation.DebtPercent,
			HybridPercent:            recommendation.Allocation.HybridPercent,
			IllustrativeReturnSeries: recommendation.IllustrativeReturnSeries,
			RiskStatisticsError:      recommendation.RiskStatisticsError,
		}
		if recommendation.RiskStatistics != nil {
			wireResponse.RiskStatistics = &riskStatisticsWireFormat{
				AnnualizedSharpeRatio:            recommendation.RiskStatistics.AnnualizedSharpeRatio,
				AnnualizedSortinoRatio:           recommendation.RiskStatistics.AnnualizedSortinoRatio,
				MaximumDrawdownFraction:          recommendation.RiskStatistics.MaximumDrawdownFraction,
				MaximumDrawdownPeakEquityValue:   recommendation.RiskStatistics.MaximumDrawdownPeakEquityValue,
				MaximumDrawdownTroughEquityValue: recommendation.RiskStatistics.MaximumDrawdownTroughEquityValue,
			}
		}
		respondWithJson(responseWriter, http.StatusOK, wireResponse)
	}
}

// --- internal/goalinvesting wire handlers ---

type goalWireFormat struct {
	GoalId                                 string   `json:"goalId,omitempty"`
	AccountIdentifier                      string   `json:"accountIdentifier,omitempty"`
	Name                                   string   `json:"name,omitempty"`
	GoalType                               string   `json:"goalType,omitempty"`
	TargetAmountInMinorUnits               int64    `json:"targetAmountInMinorUnits,omitempty"`
	TargetDate                             string   `json:"targetDate,omitempty"`
	LinkedSchemeIds                        []string `json:"linkedSchemeIds,omitempty"`
	AssumedMonthlyContributionInMinorUnits int64    `json:"assumedMonthlyContributionInMinorUnits,omitempty"`
	CreatedAt                              string   `json:"createdAt,omitempty"`
	ErrorMessage                           string   `json:"errorMessage,omitempty"`
}

func toGoalWireFormat(goal *goalinvesting.Goal) goalWireFormat {
	return goalWireFormat{
		GoalId:                                 goal.GoalId,
		AccountIdentifier:                      goal.AccountIdentifier,
		Name:                                   goal.Name,
		GoalType:                               string(goal.GoalType),
		TargetAmountInMinorUnits:               goal.TargetAmountInMinorUnits,
		TargetDate:                             goal.TargetDate.Format(time.RFC3339),
		LinkedSchemeIds:                        goal.LinkedSchemeIds,
		AssumedMonthlyContributionInMinorUnits: goal.AssumedMonthlyContributionInMinorUnits,
		CreatedAt:                              goal.CreatedAt.Format(time.RFC3339),
	}
}

type createGoalWireRequest struct {
	AccountIdentifier                      string   `json:"accountIdentifier"`
	Name                                   string   `json:"name"`
	GoalType                               string   `json:"goalType"`
	TargetAmountInMinorUnits               int64    `json:"targetAmountInMinorUnits"`
	TargetDate                             string   `json:"targetDate"`
	LinkedSchemeIds                        []string `json:"linkedSchemeIds"`
	AssumedMonthlyContributionInMinorUnits int64    `json:"assumedMonthlyContributionInMinorUnits"`
}

func buildCreateGoalHandler(goalTracker *goalinvesting.GoalTracker) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest createGoalWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed goal creation payload", http.StatusBadRequest)
			return
		}

		targetDate, parseError := time.Parse(time.RFC3339, wireRequest.TargetDate)
		if parseError != nil {
			respondWithJson(responseWriter, http.StatusBadRequest, goalWireFormat{ErrorMessage: "targetDate must be RFC3339"})
			return
		}

		now, nowError := resolveNow(request)
		if nowError != nil {
			http.Error(responseWriter, "malformed asOf query parameter, expected RFC3339", http.StatusBadRequest)
			return
		}

		goal, createError := goalTracker.CreateGoal(
			wireRequest.AccountIdentifier,
			wireRequest.Name,
			goalinvesting.GoalType(wireRequest.GoalType),
			wireRequest.TargetAmountInMinorUnits,
			targetDate,
			wireRequest.LinkedSchemeIds,
			wireRequest.AssumedMonthlyContributionInMinorUnits,
			now,
		)
		if createError != nil {
			respondWithJson(responseWriter, http.StatusBadRequest, goalWireFormat{ErrorMessage: createError.Error()})
			return
		}

		respondWithJson(responseWriter, http.StatusOK, toGoalWireFormat(goal))
	}
}

func buildListGoalsHandler(goalTracker *goalinvesting.GoalTracker) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}

		goals := goalTracker.ListGoalsForAccount(accountIdentifier)
		wireGoals := make([]goalWireFormat, 0, len(goals))
		for _, goal := range goals {
			wireGoals = append(wireGoals, toGoalWireFormat(goal))
		}
		respondWithJson(responseWriter, http.StatusOK, wireGoals)
	}
}

type goalProgressWireResponse struct {
	GoalId                                  string  `json:"goalId,omitempty"`
	CurrentValueInMinorUnits                int64   `json:"currentValueInMinorUnits"`
	TargetAmountInMinorUnits                int64   `json:"targetAmountInMinorUnits"`
	ProgressPercent                         float64 `json:"progressPercent"`
	MonthsElapsed                           int     `json:"monthsElapsed"`
	MonthsRemaining                         int     `json:"monthsRemaining"`
	ProjectedValueAtTargetDateInMinorUnits  int64   `json:"projectedValueAtTargetDateInMinorUnits"`
	IsOnTrack                               bool    `json:"isOnTrack"`
	ProjectedSurplusOrShortfallInMinorUnits int64   `json:"projectedSurplusOrShortfallInMinorUnits"`
	RequiredMonthlyContributionInMinorUnits int64   `json:"requiredMonthlyContributionInMinorUnits,omitempty"`
	ErrorMessage                            string  `json:"errorMessage,omitempty"`
}

func buildGoalProgressHandler(goalTracker *goalinvesting.GoalTracker) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		goalId := request.URL.Query().Get("goalId")
		if goalId == "" {
			http.Error(responseWriter, "missing goalId query parameter", http.StatusBadRequest)
			return
		}

		now, nowError := resolveNow(request)
		if nowError != nil {
			http.Error(responseWriter, "malformed asOf query parameter, expected RFC3339", http.StatusBadRequest)
			return
		}

		progress, progressError := goalTracker.CalculateProgress(goalId, now)
		if progressError != nil {
			respondWithJson(responseWriter, http.StatusBadRequest, goalProgressWireResponse{ErrorMessage: progressError.Error()})
			return
		}

		respondWithJson(responseWriter, http.StatusOK, goalProgressWireResponse{
			GoalId:                                  progress.GoalId,
			CurrentValueInMinorUnits:                progress.CurrentValueInMinorUnits,
			TargetAmountInMinorUnits:                progress.TargetAmountInMinorUnits,
			ProgressPercent:                         progress.ProgressPercent,
			MonthsElapsed:                           progress.MonthsElapsed,
			MonthsRemaining:                         progress.MonthsRemaining,
			ProjectedValueAtTargetDateInMinorUnits:  progress.ProjectedValueAtTargetDateInMinorUnits,
			IsOnTrack:                               progress.IsOnTrack,
			ProjectedSurplusOrShortfallInMinorUnits: progress.ProjectedSurplusOrShortfallInMinorUnits,
			RequiredMonthlyContributionInMinorUnits: progress.RequiredMonthlyContributionInMinorUnits,
		})
	}
}
