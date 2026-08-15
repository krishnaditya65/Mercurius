// Mercurius / backoffice
//
// See FEATURES.md §14 for the full scope: KYC review queue, manual order
// intervention, account freeze/unfreeze, corporate-actions processing,
// support ticket integration. As of this build, account freeze/unfreeze
// is real (internal/accountcontrol) and oms-gateway genuinely gates order
// submission on it — see its internal/backofficeclient.
//
// This build also adds three FEATURES.md §19/§21/§11 admin/curation-
// adjacent features: internal/strategyleaderboard (verified-track-record
// leaderboards, ranked from real oms-gateway data, never self-reported),
// internal/familyaccountaccess (family/joint account view-only linking,
// with a real read-only positions aggregation endpoint), and
// internal/nomineesuccession (a real, auditable nominee-succession
// workflow state machine). Everything else in FEATURES.md §14 is still a
// TODO.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"mercurius/backoffice/internal/accountcontrol"
	"mercurius/backoffice/internal/authmiddleware"
	"mercurius/backoffice/internal/familyaccountaccess"
	"mercurius/backoffice/internal/httplogging"
	"mercurius/backoffice/internal/ledgerclient"
	"mercurius/backoffice/internal/localizationcatalog"
	"mercurius/backoffice/internal/nomineesuccession"
	"mercurius/backoffice/internal/omsgatewayclient"
	"mercurius/backoffice/internal/referralrewards"
	"mercurius/backoffice/internal/strategyleaderboard"
	"mercurius/backoffice/internal/supportticketing"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	omsGatewayBaseUrl := os.Getenv("OMS_GATEWAY_BASE_URL")
	if omsGatewayBaseUrl == "" {
		omsGatewayBaseUrl = "http://127.0.0.1:8081"
	}

	ledgerBaseUrl := os.Getenv("LEDGER_BASE_URL")
	if ledgerBaseUrl == "" {
		ledgerBaseUrl = "http://127.0.0.1:8082"
	}

	jwtSigningSecret := authmiddleware.SigningSecretFromEnv()

	accountFreezeStateMachine := accountcontrol.NewAccountFreezeStateMachine()
	omsGatewayClient := omsgatewayclient.NewOmsGatewayClient(omsGatewayBaseUrl)
	ledgerClient := ledgerclient.NewLedgerClient(ledgerBaseUrl)
	strategyLeaderboardRanker := strategyleaderboard.NewRanker(omsGatewayClient)
	familyAccountAccessRegistry := familyaccountaccess.NewRegistry()
	nomineeSuccessionRegistry := nomineesuccession.NewRegistry()
	supportTicketRegistry := supportticketing.NewRegistry()
	referralRewardsRegistry := referralrewards.NewRegistry()

	httpRequestMultiplexer := http.NewServeMux()
	httpRequestMultiplexer.HandleFunc("/health", func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = responseWriter.Write([]byte(`{"status":"ok","service":"backoffice"}`))
	})
	// Account freeze/unfreeze are operator (compliance/admin) actions —
	// they act ON a customer's account, not the customer acting on their
	// own, so they're role-gated rather than self-service. Freezing/
	// unfreezing is the higher-stakes mutation (admin-only); checking
	// freeze status is a read a support agent routinely needs while
	// assisting a customer, so it's gated to RoleSupport instead.
	httpRequestMultiplexer.HandleFunc("/accounts/freeze", authmiddleware.RequireRole(jwtSigningSecret, authmiddleware.RoleAdmin, buildFreezeHandler(accountFreezeStateMachine)))
	httpRequestMultiplexer.HandleFunc("/accounts/unfreeze", authmiddleware.RequireRole(jwtSigningSecret, authmiddleware.RoleAdmin, buildUnfreezeHandler(accountFreezeStateMachine)))
	httpRequestMultiplexer.HandleFunc("/accounts/freeze-status", authmiddleware.RequireRole(jwtSigningSecret, authmiddleware.RoleSupport, buildFreezeStatusHandler(accountFreezeStateMachine)))

	// The leaderboard carries no account identifier at all (it's a
	// global ranking, not account-scoped data) — any authenticated
	// caller may view it, no ownership check applies.
	httpRequestMultiplexer.HandleFunc("/strategy-leaderboard", authmiddleware.RequireAuth(jwtSigningSecret, buildStrategyLeaderboardHandler(strategyLeaderboardRanker)))

	httpRequestMultiplexer.HandleFunc("/family-access/link", authmiddleware.RequireAuth(jwtSigningSecret, buildRegisterFamilyLinkHandler(familyAccountAccessRegistry)))
	httpRequestMultiplexer.HandleFunc("/family-access/revoke", authmiddleware.RequireAuth(jwtSigningSecret, buildRevokeFamilyLinkHandler(familyAccountAccessRegistry)))
	httpRequestMultiplexer.HandleFunc("/family-access/links", authmiddleware.RequireAuth(jwtSigningSecret, buildLinksForOwnerHandler(familyAccountAccessRegistry)))
	httpRequestMultiplexer.HandleFunc("/family-access/positions", authmiddleware.RequireAuth(jwtSigningSecret, buildFamilyAccessPositionsHandler(familyAccountAccessRegistry, omsGatewayClient)))

	httpRequestMultiplexer.HandleFunc("/nominee-succession/register-nominee", authmiddleware.RequireAuth(jwtSigningSecret, buildRegisterNomineeHandler(nomineeSuccessionRegistry)))
	httpRequestMultiplexer.HandleFunc("/nominee-succession/nominee", authmiddleware.RequireAuth(jwtSigningSecret, buildGetNomineeHandler(nomineeSuccessionRegistry)))
	httpRequestMultiplexer.HandleFunc("/nominee-succession/submit", authmiddleware.RequireAuth(jwtSigningSecret, buildSubmitSuccessionRequestHandler(nomineeSuccessionRegistry)))
	// The succession review/decision pipeline is operator-driven, not
	// self-service: moving a request into review is routine support
	// triage (RoleSupport); approving, marking transferred, and
	// rejecting are the irreversible compliance decisions further down
	// the pipeline, reserved for RoleAdmin. Status/audit-trail lookups
	// are read-only operator tooling (RoleSupport).
	httpRequestMultiplexer.HandleFunc("/nominee-succession/move-to-under-review", authmiddleware.RequireRole(jwtSigningSecret, authmiddleware.RoleSupport, buildMoveToUnderReviewHandler(nomineeSuccessionRegistry)))
	httpRequestMultiplexer.HandleFunc("/nominee-succession/approve", authmiddleware.RequireRole(jwtSigningSecret, authmiddleware.RoleAdmin, buildApproveHandler(nomineeSuccessionRegistry)))
	httpRequestMultiplexer.HandleFunc("/nominee-succession/mark-transferred", authmiddleware.RequireRole(jwtSigningSecret, authmiddleware.RoleAdmin, buildMarkTransferredHandler(nomineeSuccessionRegistry)))
	httpRequestMultiplexer.HandleFunc("/nominee-succession/reject", authmiddleware.RequireRole(jwtSigningSecret, authmiddleware.RoleAdmin, buildRejectHandler(nomineeSuccessionRegistry)))
	httpRequestMultiplexer.HandleFunc("/nominee-succession/status", authmiddleware.RequireRole(jwtSigningSecret, authmiddleware.RoleSupport, buildGetSuccessionRequestHandler(nomineeSuccessionRegistry)))
	httpRequestMultiplexer.HandleFunc("/nominee-succession/audit-trail", authmiddleware.RequireRole(jwtSigningSecret, authmiddleware.RoleSupport, buildSuccessionAuditTrailHandler(nomineeSuccessionRegistry)))

	httpRequestMultiplexer.HandleFunc("/support/tickets/create", authmiddleware.RequireAuth(jwtSigningSecret, buildCreateTicketHandler(supportTicketRegistry)))
	httpRequestMultiplexer.HandleFunc("/support/tickets/customer-message", authmiddleware.RequireAuth(jwtSigningSecret, buildAddCustomerMessageHandler(supportTicketRegistry)))
	// Agent-side ticket operations act on behalf of the support
	// operator, not the customer — RoleSupport, no account-ownership
	// check applies (there is no "own account" for an agent action).
	httpRequestMultiplexer.HandleFunc("/support/tickets/agent-reply", authmiddleware.RequireRole(jwtSigningSecret, authmiddleware.RoleSupport, buildAddAgentReplyHandler(supportTicketRegistry)))
	httpRequestMultiplexer.HandleFunc("/support/tickets/assign", authmiddleware.RequireRole(jwtSigningSecret, authmiddleware.RoleSupport, buildAssignAgentHandler(supportTicketRegistry)))
	httpRequestMultiplexer.HandleFunc("/support/tickets/status", authmiddleware.RequireRole(jwtSigningSecret, authmiddleware.RoleSupport, buildTransitionStatusHandler(supportTicketRegistry)))
	httpRequestMultiplexer.HandleFunc("/support/tickets/get", authmiddleware.RequireAuth(jwtSigningSecret, buildGetTicketHandler(supportTicketRegistry)))
	httpRequestMultiplexer.HandleFunc("/support/tickets/thread", authmiddleware.RequireAuth(jwtSigningSecret, buildGetMessageThreadHandler(supportTicketRegistry)))
	httpRequestMultiplexer.HandleFunc("/support/tickets/by-account", authmiddleware.RequireAuth(jwtSigningSecret, buildTicketsForAccountHandler(supportTicketRegistry)))
	httpRequestMultiplexer.HandleFunc("/support/tickets/by-agent", authmiddleware.RequireRole(jwtSigningSecret, authmiddleware.RoleSupport, buildTicketsForAgentHandler(supportTicketRegistry)))
	httpRequestMultiplexer.HandleFunc("/support/tickets/queue", authmiddleware.RequireRole(jwtSigningSecret, authmiddleware.RoleSupport, buildSupportQueueHandler(supportTicketRegistry)))

	httpRequestMultiplexer.HandleFunc("/referral-rewards/generate-code", authmiddleware.RequireAuth(jwtSigningSecret, buildGenerateReferralCodeHandler(referralRewardsRegistry)))
	httpRequestMultiplexer.HandleFunc("/referral-rewards/record-referral", authmiddleware.RequireAuth(jwtSigningSecret, buildRecordReferralHandler(referralRewardsRegistry)))
	httpRequestMultiplexer.HandleFunc("/referral-rewards/status", authmiddleware.RequireAuth(jwtSigningSecret, buildReferralStatusHandler(referralRewardsRegistry)))
	httpRequestMultiplexer.HandleFunc("/referral-rewards/referrals", authmiddleware.RequireAuth(jwtSigningSecret, buildReferralsForReferrerHandler(referralRewardsRegistry)))
	httpRequestMultiplexer.HandleFunc("/referral-rewards/check-and-qualify", authmiddleware.RequireAuth(jwtSigningSecret, buildCheckAndQualifyReferralHandler(referralRewardsRegistry, omsGatewayClient, ledgerClient)))

	// Genuinely public reference data — no account is ever involved.
	httpRequestMultiplexer.HandleFunc("/localization/languages", buildLocalizationLanguagesHandler())
	httpRequestMultiplexer.HandleFunc("/localization/", buildLocalizationCatalogHandler())

	listenAddress := ":8084"
	log.Printf("backoffice listening on %s (CORS allow-listed via CORS_ALLOWED_ORIGINS)\n", listenAddress)
	instrumentedHandler := withAllowListedCorsForDevelopment(httplogging.WithRequestLogging(httpRequestMultiplexer))
	if serverStartupError := http.ListenAndServe(listenAddress, instrumentedHandler); serverStartupError != nil {
		log.Fatalf("backoffice failed to start: %v", serverStartupError)
	}
}

// ---------------------------------------------------------------------
// CORS — allow-listed origin echo (not the wide-open `*` pattern this
// build's other, still-unauthenticated services use). Now that this
// service gates routes on bearer tokens and a browser client may send
// credentials, `Access-Control-Allow-Origin: *` is actively wrong: the
// CORS spec forbids `*` alongside credentialed requests and browsers
// reject it outright. Instead, this echoes back the exact Origin header
// when (and only when) it's in the allow-list, plus
// Access-Control-Allow-Credentials: true; a non-matching Origin gets no
// CORS headers at all, which the browser then correctly blocks.
func withAllowListedCorsForDevelopment(nextHandler http.Handler) http.Handler {
	allowedOrigins := allowedCorsOriginsFromEnv()
	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		requestOrigin := request.Header.Get("Origin")
		if requestOrigin != "" && allowedOrigins[requestOrigin] {
			responseWriter.Header().Set("Access-Control-Allow-Origin", requestOrigin)
			responseWriter.Header().Set("Access-Control-Allow-Credentials", "true")
			responseWriter.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			responseWriter.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		}

		if request.Method == http.MethodOptions {
			responseWriter.WriteHeader(http.StatusNoContent)
			return
		}

		nextHandler.ServeHTTP(responseWriter, request)
	})
}

// allowedCorsOriginsFromEnv reads CORS_ALLOWED_ORIGINS as a
// comma-separated origin list, defaulting to the two ports apps/web runs
// on in local development when unset.
func allowedCorsOriginsFromEnv() map[string]bool {
	rawOrigins := os.Getenv("CORS_ALLOWED_ORIGINS")
	if rawOrigins == "" {
		rawOrigins = "http://localhost:3000,http://localhost:3100"
	}
	allowedOrigins := make(map[string]bool)
	for _, origin := range strings.Split(rawOrigins, ",") {
		trimmedOrigin := strings.TrimSpace(origin)
		if trimmedOrigin != "" {
			allowedOrigins[trimmedOrigin] = true
		}
	}
	return allowedOrigins
}

// requireOwnAccount is the standard self-service guard for every
// authenticated-retail route in this service that carries an account
// identifier: the caller may only ever act on the account their own
// access token was issued for. On mismatch it writes the 403 this task
// specifies and returns false; callers must stop handling the request
// (return immediately) when this returns false.
func requireOwnAccount(responseWriter http.ResponseWriter, request *http.Request, accountIdentifier string) bool {
	authenticatedAccountIdentifier, _ := authmiddleware.AuthenticatedAccountIdentifier(request)
	if authenticatedAccountIdentifier != accountIdentifier {
		respondWithJson(responseWriter, http.StatusForbidden, map[string]any{"errorMessage": "you can only act on your own account"})
		return false
	}
	return true
}

// ---------------------------------------------------------------------
// Account freeze/unfreeze (pre-existing)

type freezeStatusWireResponse struct {
	AccountIdentifier string `json:"accountIdentifier"`
	IsFrozen          bool   `json:"isFrozen"`
	FreezeReason      string `json:"freezeReason,omitempty"`
}

type freezeActionWireRequest struct {
	AccountIdentifier string `json:"accountIdentifier"`
	FreezeReason      string `json:"freezeReason,omitempty"`
}

func buildFreezeHandler(accountFreezeStateMachine *accountcontrol.AccountFreezeStateMachine) http.HandlerFunc {
	// RBAC: gated to authmiddleware.RoleAdmin at the mux registration
	// site in main() — only an authenticated admin caller can reach this
	// handler at all.
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest freezeActionWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed freeze request payload", http.StatusBadRequest)
			return
		}
		if wireRequest.FreezeReason == "" {
			http.Error(responseWriter, "freezeReason is required — freezing an account without a recorded reason is not acceptable", http.StatusBadRequest)
			return
		}

		accountFreezeStateMachine.FreezeAccount(wireRequest.AccountIdentifier, wireRequest.FreezeReason)
		log.Printf("FROZEN: %s — %s", wireRequest.AccountIdentifier, wireRequest.FreezeReason)

		respondWithFreezeStatus(responseWriter, accountFreezeStateMachine, wireRequest.AccountIdentifier)
	}
}

func buildUnfreezeHandler(accountFreezeStateMachine *accountcontrol.AccountFreezeStateMachine) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest freezeActionWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed unfreeze request payload", http.StatusBadRequest)
			return
		}

		accountFreezeStateMachine.UnfreezeAccount(wireRequest.AccountIdentifier)
		log.Printf("UNFROZEN: %s", wireRequest.AccountIdentifier)

		respondWithFreezeStatus(responseWriter, accountFreezeStateMachine, wireRequest.AccountIdentifier)
	}
}

func buildFreezeStatusHandler(accountFreezeStateMachine *accountcontrol.AccountFreezeStateMachine) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}
		respondWithFreezeStatus(responseWriter, accountFreezeStateMachine, accountIdentifier)
	}
}

func respondWithFreezeStatus(
	responseWriter http.ResponseWriter,
	accountFreezeStateMachine *accountcontrol.AccountFreezeStateMachine,
	accountIdentifier string,
) {
	status := accountFreezeStateMachine.CheckFreezeStatus(accountIdentifier)
	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(freezeStatusWireResponse{
		AccountIdentifier: status.AccountIdentifier,
		IsFrozen:          status.IsFrozen,
		FreezeReason:      status.FreezeReason,
	})
}

// ---------------------------------------------------------------------
// FEATURES.md §19/§11 verified-track-record strategy leaderboard —
// GET /strategy-leaderboard. See internal/strategyleaderboard's package
// doc for exactly what real oms-gateway data backs this and what's an
// honestly-documented gap (no real per-strategy P&L exists yet).

func buildStrategyLeaderboardHandler(ranker *strategyleaderboard.Ranker) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		leaderboard, buildError := ranker.BuildLeaderboard()
		if buildError != nil {
			log.Printf("failed to build strategy leaderboard: %v", buildError)
			http.Error(responseWriter, "failed to build strategy leaderboard: "+buildError.Error(), http.StatusBadGateway)
			return
		}
		respondWithJson(responseWriter, http.StatusOK, map[string]any{"leaderboard": leaderboard})
	}
}

// ---------------------------------------------------------------------
// FEATURES.md §21 family/joint account view-only access —
// internal/familyaccountaccess. Every endpoint below requires a valid
// access token (authmiddleware.RequireAuth) and enforces that the
// caller only ever acts as their own account (requireOwnAccount) —
// self-service, not admin-only.

type familyLinkWireRequest struct {
	OwnerAccountIdentifier  string `json:"ownerAccountIdentifier"`
	ViewerAccountIdentifier string `json:"viewerAccountIdentifier"`
	PermissionLevel         string `json:"permissionLevel"`
}

func buildRegisterFamilyLinkHandler(registry *familyaccountaccess.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest familyLinkWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed family-link request payload", http.StatusBadRequest)
			return
		}
		if !requireOwnAccount(responseWriter, request, wireRequest.OwnerAccountIdentifier) {
			return
		}
		linkError := registry.RegisterFamilyLink(
			wireRequest.OwnerAccountIdentifier,
			wireRequest.ViewerAccountIdentifier,
			familyaccountaccess.PermissionLevel(wireRequest.PermissionLevel),
		)
		if linkError != nil {
			respondWithJson(responseWriter, http.StatusBadRequest, map[string]any{"errorMessage": linkError.Error()})
			return
		}
		log.Printf("FAMILY_LINK_REGISTERED: viewer=%s owner=%s permission=%s", wireRequest.ViewerAccountIdentifier, wireRequest.OwnerAccountIdentifier, wireRequest.PermissionLevel)
		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"ownerAccountIdentifier":  wireRequest.OwnerAccountIdentifier,
			"viewerAccountIdentifier": wireRequest.ViewerAccountIdentifier,
			"permissionLevel":         wireRequest.PermissionLevel,
		})
	}
}

func buildRevokeFamilyLinkHandler(registry *familyaccountaccess.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest familyLinkWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed family-link revoke payload", http.StatusBadRequest)
			return
		}
		if !requireOwnAccount(responseWriter, request, wireRequest.OwnerAccountIdentifier) {
			return
		}
		registry.RevokeFamilyLink(wireRequest.OwnerAccountIdentifier, wireRequest.ViewerAccountIdentifier)
		log.Printf("FAMILY_LINK_REVOKED: viewer=%s owner=%s", wireRequest.ViewerAccountIdentifier, wireRequest.OwnerAccountIdentifier)
		respondWithJson(responseWriter, http.StatusOK, map[string]any{"wasRevoked": true})
	}
}

func buildLinksForOwnerHandler(registry *familyaccountaccess.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		ownerAccountIdentifier := request.URL.Query().Get("ownerAccountId")
		if ownerAccountIdentifier == "" {
			http.Error(responseWriter, "missing ownerAccountId query parameter", http.StatusBadRequest)
			return
		}
		if !requireOwnAccount(responseWriter, request, ownerAccountIdentifier) {
			return
		}
		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"ownerAccountIdentifier": ownerAccountIdentifier,
			"links":                  registry.LinksForOwner(ownerAccountIdentifier),
		})
	}
}

// buildFamilyAccessPositionsHandler is the real read-only aggregation
// endpoint: GET /family-access/positions?ownerAccountId=...&viewerAccountId=...
// A caller must present a viewerAccountIdentifier that
// AuthorizeViewOnlyAccess accepts for ownerAccountIdentifier — anything
// else is rejected before oms-gateway is ever contacted. There is no
// order-submission capability reachable from this handler or anywhere
// else in this package; see internal/familyaccountaccess's
// TestExposedCapabilitySetIsReadOnly for a real, enforced assertion of
// that boundary.
func buildFamilyAccessPositionsHandler(registry *familyaccountaccess.Registry, omsGatewayClient *omsgatewayclient.OmsGatewayClient) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		ownerAccountIdentifier := request.URL.Query().Get("ownerAccountId")
		viewerAccountIdentifier := request.URL.Query().Get("viewerAccountId")
		if ownerAccountIdentifier == "" || viewerAccountIdentifier == "" {
			http.Error(responseWriter, "ownerAccountId and viewerAccountId query parameters are both required", http.StatusBadRequest)
			return
		}
		// The caller here is the viewer (a family member looking at the
		// owner's positions under a granted permission), not the owner —
		// self-service means the caller can only ever present themself as
		// the viewerAccountId; whether that viewer may actually see this
		// owner's data is then AuthorizeViewOnlyAccess's job below.
		if !requireOwnAccount(responseWriter, request, viewerAccountIdentifier) {
			return
		}

		if authorizeError := registry.AuthorizeViewOnlyAccess(ownerAccountIdentifier, viewerAccountIdentifier); authorizeError != nil {
			respondWithJson(responseWriter, http.StatusForbidden, map[string]any{"errorMessage": authorizeError.Error()})
			return
		}

		positions, fetchError := omsGatewayClient.FetchPositions(ownerAccountIdentifier)
		if fetchError != nil {
			log.Printf("failed to fetch positions for family-access viewer=%s owner=%s: %v", viewerAccountIdentifier, ownerAccountIdentifier, fetchError)
			http.Error(responseWriter, "failed to fetch positions from oms-gateway: "+fetchError.Error(), http.StatusBadGateway)
			return
		}

		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"ownerAccountIdentifier":        ownerAccountIdentifier,
			"viewerAccountIdentifier":       viewerAccountIdentifier,
			"netQuantityByInstrumentSymbol": positions.NetQuantityByInstrumentSymbol,
		})
	}
}

// ---------------------------------------------------------------------
// FEATURES.md §21 nominee succession workflow —
// internal/nomineesuccession. The account-holder-facing endpoints
// (register-nominee, nominee, submit) require self-service auth
// (RequireAuth + requireOwnAccount); the operator review/decision
// pipeline (move-to-under-review, approve, mark-transferred, reject,
// status, audit-trail) is role-gated instead — see the reasoning
// comment at the mux registration sites in main().

type registerNomineeWireRequest struct {
	AccountIdentifier          string `json:"accountIdentifier"`
	NomineeFullName            string `json:"nomineeFullName"`
	NomineeRelationship        string `json:"nomineeRelationship"`
	NomineeIdentityDocumentRef string `json:"nomineeIdentityDocumentReference,omitempty"`
	Actor                      string `json:"actor"`
}

func buildRegisterNomineeHandler(registry *nomineesuccession.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest registerNomineeWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed register-nominee payload", http.StatusBadRequest)
			return
		}
		if !requireOwnAccount(responseWriter, request, wireRequest.AccountIdentifier) {
			return
		}
		registerError := registry.RegisterNominee(
			wireRequest.AccountIdentifier,
			wireRequest.NomineeFullName,
			wireRequest.NomineeRelationship,
			wireRequest.NomineeIdentityDocumentRef,
			wireRequest.Actor,
		)
		if registerError != nil {
			respondWithJson(responseWriter, http.StatusBadRequest, map[string]any{"errorMessage": registerError.Error()})
			return
		}
		nominee, _ := registry.GetNominee(wireRequest.AccountIdentifier)
		respondWithJson(responseWriter, http.StatusOK, nominee)
	}
}

func buildGetNomineeHandler(registry *nomineesuccession.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}
		if !requireOwnAccount(responseWriter, request, accountIdentifier) {
			return
		}
		nominee, exists := registry.GetNominee(accountIdentifier)
		if !exists {
			respondWithJson(responseWriter, http.StatusNotFound, map[string]any{"errorMessage": "no nominee registered for this account"})
			return
		}
		respondWithJson(responseWriter, http.StatusOK, nominee)
	}
}

type submitSuccessionWireRequest struct {
	AccountIdentifier                 string `json:"accountIdentifier"`
	DeathCertificateDocumentReference string `json:"deathCertificateDocumentReference"`
	Actor                             string `json:"actor"`
}

func buildSubmitSuccessionRequestHandler(registry *nomineesuccession.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest submitSuccessionWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed succession-submit payload", http.StatusBadRequest)
			return
		}
		if !requireOwnAccount(responseWriter, request, wireRequest.AccountIdentifier) {
			return
		}
		successionRequest, submitError := registry.SubmitSuccessionRequest(
			wireRequest.AccountIdentifier,
			wireRequest.DeathCertificateDocumentReference,
			wireRequest.Actor,
			time.Now(),
		)
		if submitError != nil {
			respondWithJson(responseWriter, http.StatusBadRequest, map[string]any{"errorMessage": submitError.Error()})
			return
		}
		log.Printf("SUCCESSION_SUBMITTED: account=%s actor=%s docRef=%s", wireRequest.AccountIdentifier, wireRequest.Actor, wireRequest.DeathCertificateDocumentReference)
		respondWithJson(responseWriter, http.StatusOK, successionRequest)
	}
}

type successionTransitionWireRequest struct {
	AccountIdentifier string `json:"accountIdentifier"`
	Actor             string `json:"actor"`
	Reason            string `json:"reason,omitempty"`
}

func buildMoveToUnderReviewHandler(registry *nomineesuccession.Registry) http.HandlerFunc {
	return buildSuccessionTransitionHandler(registry.MoveToUnderReview, "MOVED_TO_UNDER_REVIEW")
}

func buildApproveHandler(registry *nomineesuccession.Registry) http.HandlerFunc {
	return buildSuccessionTransitionHandler(registry.Approve, "APPROVED")
}

func buildMarkTransferredHandler(registry *nomineesuccession.Registry) http.HandlerFunc {
	return buildSuccessionTransitionHandler(registry.MarkTransferred, "TRANSFERRED")
}

func buildRejectHandler(registry *nomineesuccession.Registry) http.HandlerFunc {
	return buildSuccessionTransitionHandler(registry.Reject, "REJECTED")
}

// buildSuccessionTransitionHandler is shared HTTP glue for every state
// transition endpoint above — each just supplies the real state-machine
// method to call, the HTTP layer's job is identical every time (decode,
// call, respond).
func buildSuccessionTransitionHandler(
	transitionFunc func(accountIdentifier string, actor string, reason string, now time.Time) (nomineesuccession.SuccessionRequest, error),
	logLabel string,
) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest successionTransitionWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed succession-transition payload", http.StatusBadRequest)
			return
		}
		successionRequest, transitionError := transitionFunc(wireRequest.AccountIdentifier, wireRequest.Actor, wireRequest.Reason, time.Now())
		if transitionError != nil {
			respondWithJson(responseWriter, http.StatusBadRequest, map[string]any{"errorMessage": transitionError.Error()})
			return
		}
		log.Printf("SUCCESSION_%s: account=%s actor=%s reason=%s", logLabel, wireRequest.AccountIdentifier, wireRequest.Actor, wireRequest.Reason)
		respondWithJson(responseWriter, http.StatusOK, successionRequest)
	}
}

func buildGetSuccessionRequestHandler(registry *nomineesuccession.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}
		successionRequest, exists := registry.GetSuccessionRequest(accountIdentifier)
		if !exists {
			respondWithJson(responseWriter, http.StatusNotFound, map[string]any{"errorMessage": "no succession request found for this account"})
			return
		}
		respondWithJson(responseWriter, http.StatusOK, successionRequest)
	}
}

func buildSuccessionAuditTrailHandler(registry *nomineesuccession.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			respondWithJson(responseWriter, http.StatusOK, map[string]any{"auditTrail": registry.AllAuditTrailRecords()})
			return
		}
		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"accountIdentifier": accountIdentifier,
			"auditTrail":        registry.AuditTrailForAccount(accountIdentifier),
		})
	}
}

// ---------------------------------------------------------------------
// FEATURES.md §14 in-app support chat / ticketing —
// internal/supportticketing. Customer-facing endpoints (create,
// customer-message, get, thread, by-account) require self-service auth
// (RequireAuth + requireOwnAccount); agent-facing endpoints
// (agent-reply, assign, status, by-agent, queue) are gated to
// authmiddleware.RoleSupport instead.

type createTicketWireRequest struct {
	AccountIdentifier  string `json:"accountIdentifier"`
	Subject            string `json:"subject"`
	InitialMessageBody string `json:"initialMessageBody"`
}

func buildCreateTicketHandler(registry *supportticketing.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest createTicketWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed create-ticket payload", http.StatusBadRequest)
			return
		}
		if !requireOwnAccount(responseWriter, request, wireRequest.AccountIdentifier) {
			return
		}
		ticket, createError := registry.CreateTicket(wireRequest.AccountIdentifier, wireRequest.Subject, wireRequest.InitialMessageBody, time.Now())
		if createError != nil {
			respondWithJson(responseWriter, http.StatusBadRequest, map[string]any{"errorMessage": createError.Error()})
			return
		}
		log.Printf("SUPPORT_TICKET_CREATED: ticket=%s account=%s", ticket.TicketIdentifier, ticket.AccountIdentifier)
		respondWithJson(responseWriter, http.StatusOK, ticket)
	}
}

type ticketMessageWireRequest struct {
	TicketIdentifier  string `json:"ticketIdentifier"`
	AccountIdentifier string `json:"accountIdentifier,omitempty"`
	AgentIdentifier   string `json:"agentIdentifier,omitempty"`
	MessageBody       string `json:"messageBody"`
}

func buildAddCustomerMessageHandler(registry *supportticketing.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest ticketMessageWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed customer-message payload", http.StatusBadRequest)
			return
		}
		if !requireOwnAccount(responseWriter, request, wireRequest.AccountIdentifier) {
			return
		}
		message, messageError := registry.AddCustomerMessage(wireRequest.TicketIdentifier, wireRequest.AccountIdentifier, wireRequest.MessageBody, time.Now())
		if messageError != nil {
			respondWithJson(responseWriter, http.StatusBadRequest, map[string]any{"errorMessage": messageError.Error()})
			return
		}
		respondWithJson(responseWriter, http.StatusOK, message)
	}
}

func buildAddAgentReplyHandler(registry *supportticketing.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest ticketMessageWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed agent-reply payload", http.StatusBadRequest)
			return
		}
		message, replyError := registry.AddAgentReply(wireRequest.TicketIdentifier, wireRequest.AgentIdentifier, wireRequest.MessageBody, time.Now())
		if replyError != nil {
			respondWithJson(responseWriter, http.StatusBadRequest, map[string]any{"errorMessage": replyError.Error()})
			return
		}
		log.Printf("SUPPORT_TICKET_AGENT_REPLY: ticket=%s agent=%s", wireRequest.TicketIdentifier, wireRequest.AgentIdentifier)
		respondWithJson(responseWriter, http.StatusOK, message)
	}
}

type assignAgentWireRequest struct {
	TicketIdentifier string `json:"ticketIdentifier"`
	AgentIdentifier  string `json:"agentIdentifier"`
}

func buildAssignAgentHandler(registry *supportticketing.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest assignAgentWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed assign-agent payload", http.StatusBadRequest)
			return
		}
		ticket, assignError := registry.AssignAgent(wireRequest.TicketIdentifier, wireRequest.AgentIdentifier, time.Now())
		if assignError != nil {
			respondWithJson(responseWriter, http.StatusBadRequest, map[string]any{"errorMessage": assignError.Error()})
			return
		}
		log.Printf("SUPPORT_TICKET_ASSIGNED: ticket=%s agent=%s", ticket.TicketIdentifier, ticket.AssignedAgentIdentifier)
		respondWithJson(responseWriter, http.StatusOK, ticket)
	}
}

type transitionStatusWireRequest struct {
	TicketIdentifier string                        `json:"ticketIdentifier"`
	NewStatus        supportticketing.TicketStatus `json:"newStatus"`
}

func buildTransitionStatusHandler(registry *supportticketing.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest transitionStatusWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed status-transition payload", http.StatusBadRequest)
			return
		}
		ticket, transitionError := registry.TransitionStatus(wireRequest.TicketIdentifier, wireRequest.NewStatus, time.Now())
		if transitionError != nil {
			respondWithJson(responseWriter, http.StatusBadRequest, map[string]any{"errorMessage": transitionError.Error()})
			return
		}
		log.Printf("SUPPORT_TICKET_STATUS: ticket=%s status=%s", ticket.TicketIdentifier, ticket.Status)
		respondWithJson(responseWriter, http.StatusOK, ticket)
	}
}

func buildGetTicketHandler(registry *supportticketing.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		ticketIdentifier := request.URL.Query().Get("ticketId")
		if ticketIdentifier == "" {
			http.Error(responseWriter, "missing ticketId query parameter", http.StatusBadRequest)
			return
		}
		ticket, exists := registry.GetTicket(ticketIdentifier)
		if !exists {
			respondWithJson(responseWriter, http.StatusNotFound, map[string]any{"errorMessage": "no ticket found for this id"})
			return
		}
		// The request only carries ticketId, not an account identifier —
		// self-service ownership has to be checked against the fetched
		// ticket's own AccountIdentifier instead of a request field.
		if !requireOwnAccount(responseWriter, request, ticket.AccountIdentifier) {
			return
		}
		respondWithJson(responseWriter, http.StatusOK, ticket)
	}
}

func buildGetMessageThreadHandler(registry *supportticketing.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		ticketIdentifier := request.URL.Query().Get("ticketId")
		if ticketIdentifier == "" {
			http.Error(responseWriter, "missing ticketId query parameter", http.StatusBadRequest)
			return
		}
		// Same fetch-then-check-ownership shape as buildGetTicketHandler
		// above: a thread's messages are only for the ticket's own
		// account holder to read.
		ticket, exists := registry.GetTicket(ticketIdentifier)
		if !exists {
			respondWithJson(responseWriter, http.StatusNotFound, map[string]any{"errorMessage": supportticketing.ErrTicketNotFound.Error()})
			return
		}
		if !requireOwnAccount(responseWriter, request, ticket.AccountIdentifier) {
			return
		}
		thread, threadError := registry.MessageThread(ticketIdentifier)
		if threadError != nil {
			respondWithJson(responseWriter, http.StatusNotFound, map[string]any{"errorMessage": threadError.Error()})
			return
		}
		respondWithJson(responseWriter, http.StatusOK, map[string]any{"ticketIdentifier": ticketIdentifier, "messages": thread})
	}
}

func buildTicketsForAccountHandler(registry *supportticketing.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}
		if !requireOwnAccount(responseWriter, request, accountIdentifier) {
			return
		}
		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"accountIdentifier": accountIdentifier,
			"tickets":           registry.TicketsForAccount(accountIdentifier),
		})
	}
}

func buildTicketsForAgentHandler(registry *supportticketing.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		agentIdentifier := request.URL.Query().Get("agentId")
		if agentIdentifier == "" {
			http.Error(responseWriter, "missing agentId query parameter", http.StatusBadRequest)
			return
		}
		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"agentIdentifier": agentIdentifier,
			"tickets":         registry.TicketsForAgent(agentIdentifier),
		})
	}
}

// buildSupportQueueHandler serves the staff-facing queue: with no query
// parameter it returns every unassigned, still-open ticket (the real
// triage queue); ?all=true returns every ticket regardless of status or
// assignment.
func buildSupportQueueHandler(registry *supportticketing.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("all") == "true" {
			respondWithJson(responseWriter, http.StatusOK, map[string]any{"tickets": registry.AllTickets()})
			return
		}
		respondWithJson(responseWriter, http.StatusOK, map[string]any{"unassignedOpenTickets": registry.UnassignedOpenTickets()})
	}
}

// ---------------------------------------------------------------------
// FEATURES.md §14 referral & rewards program —
// internal/referralrewards. Every endpoint requires self-service auth
// (RequireAuth + requireOwnAccount), matched against whichever account
// identifier the caller is meant to be presenting themself as (referrer
// for generate-code/status/referrals, referred account for
// record-referral/check-and-qualify — see the reasoning comments on
// each handler).

type generateReferralCodeWireRequest struct {
	AccountIdentifier string `json:"accountIdentifier"`
}

func buildGenerateReferralCodeHandler(registry *referralrewards.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest generateReferralCodeWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed generate-code payload", http.StatusBadRequest)
			return
		}
		if !requireOwnAccount(responseWriter, request, wireRequest.AccountIdentifier) {
			return
		}
		code, generateError := registry.GenerateReferralCode(wireRequest.AccountIdentifier)
		if generateError != nil {
			respondWithJson(responseWriter, http.StatusBadRequest, map[string]any{"errorMessage": generateError.Error()})
			return
		}
		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"accountIdentifier": wireRequest.AccountIdentifier,
			"referralCode":      code,
		})
	}
}

type recordReferralWireRequest struct {
	ReferralCode              string `json:"referralCode"`
	ReferredAccountIdentifier string `json:"referredAccountIdentifier"`
}

func buildRecordReferralHandler(registry *referralrewards.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest recordReferralWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed record-referral payload", http.StatusBadRequest)
			return
		}
		// The caller here is the newly-referred account confirming their
		// own signup against someone else's referral code — self-service
		// means matching the REFERRED account, not the referrer.
		if !requireOwnAccount(responseWriter, request, wireRequest.ReferredAccountIdentifier) {
			return
		}
		link, recordError := registry.RecordReferral(wireRequest.ReferralCode, wireRequest.ReferredAccountIdentifier, time.Now())
		if recordError != nil {
			respondWithJson(responseWriter, http.StatusBadRequest, map[string]any{"errorMessage": recordError.Error()})
			return
		}
		log.Printf("REFERRAL_RECORDED: referrer=%s referred=%s", link.ReferrerAccountIdentifier, link.ReferredAccountIdentifier)
		respondWithJson(responseWriter, http.StatusOK, link)
	}
}

func buildReferralStatusHandler(registry *referralrewards.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}
		if !requireOwnAccount(responseWriter, request, accountIdentifier) {
			return
		}
		link, exists := registry.GetReferralLink(accountIdentifier)
		if !exists {
			respondWithJson(responseWriter, http.StatusNotFound, map[string]any{"errorMessage": "no referral link found for this account"})
			return
		}
		respondWithJson(responseWriter, http.StatusOK, link)
	}
}

func buildReferralsForReferrerHandler(registry *referralrewards.Registry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}
		if !requireOwnAccount(responseWriter, request, accountIdentifier) {
			return
		}
		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"accountIdentifier": accountIdentifier,
			"referrals":         registry.ReferralsForReferrer(accountIdentifier),
		})
	}
}

type checkAndQualifyReferralWireRequest struct {
	ReferredAccountIdentifier string `json:"referredAccountIdentifier"`
}

// buildCheckAndQualifyReferralHandler is the real qualifying-event
// check: given a referred account, it calls the REAL oms-gateway
// /positions endpoint (via omsGatewayClient.FetchPositions, the exact
// same client method buildFamilyAccessPositionsHandler already uses for
// a different feature) to see whether that account has completed at
// least one real trade (a non-empty position book). If — and only if —
// that's true, it credits the referrer's reward via a REAL ledger HTTP
// call (ledgerclient.PostCashRewardCreditJournalEntry) and marks the
// referral rewarded. If the account has not traded yet, or the referral
// has already been rewarded, this is a harmless no-op that reports the
// real current status rather than erroring.
func buildCheckAndQualifyReferralHandler(
	registry *referralrewards.Registry,
	omsGatewayClient *omsgatewayclient.OmsGatewayClient,
	ledgerClient *ledgerclient.LedgerClient,
) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}
		var wireRequest checkAndQualifyReferralWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed check-and-qualify payload", http.StatusBadRequest)
			return
		}
		if !requireOwnAccount(responseWriter, request, wireRequest.ReferredAccountIdentifier) {
			return
		}

		link, exists := registry.GetReferralLink(wireRequest.ReferredAccountIdentifier)
		if !exists {
			respondWithJson(responseWriter, http.StatusNotFound, map[string]any{"errorMessage": "no referral link found for this account"})
			return
		}
		if link.Status == referralrewards.ReferralStatusRewarded {
			respondWithJson(responseWriter, http.StatusOK, map[string]any{"alreadyRewarded": true, "referralLink": link})
			return
		}

		positions, fetchError := omsGatewayClient.FetchPositions(wireRequest.ReferredAccountIdentifier)
		if fetchError != nil {
			log.Printf("referral qualification check failed to reach oms-gateway for %s: %v", wireRequest.ReferredAccountIdentifier, fetchError)
			http.Error(responseWriter, "failed to check qualifying event against oms-gateway: "+fetchError.Error(), http.StatusBadGateway)
			return
		}
		if len(positions.NetQuantityByInstrumentSymbol) == 0 {
			respondWithJson(responseWriter, http.StatusOK, map[string]any{"qualified": false, "reason": "referred account has not completed a first trade yet", "referralLink": link})
			return
		}

		creditError := ledgerClient.PostCashRewardCreditJournalEntry(
			link.ReferrerAccountIdentifier,
			referralrewards.StandardReferralRewardInMinorUnits,
			fmt.Sprintf("referral reward for referring %s (first trade completed)", link.ReferredAccountIdentifier),
		)
		if creditError != nil {
			log.Printf("referral reward credit failed: referrer=%s referred=%s error=%v", link.ReferrerAccountIdentifier, link.ReferredAccountIdentifier, creditError)
			http.Error(responseWriter, "qualifying event confirmed but ledger credit failed: "+creditError.Error(), http.StatusBadGateway)
			return
		}

		rewarded, markError := registry.MarkRewarded(wireRequest.ReferredAccountIdentifier, referralrewards.StandardReferralRewardInMinorUnits, time.Now())
		if markError != nil {
			// The ledger credit already succeeded but the in-memory
			// status update raced with a concurrent call -- honestly
			// surface this rather than pretending it didn't happen.
			log.Printf("referral reward credited but status update failed: referred=%s error=%v", wireRequest.ReferredAccountIdentifier, markError)
			respondWithJson(responseWriter, http.StatusOK, map[string]any{"qualified": true, "rewardCredited": true, "statusUpdateWarning": markError.Error()})
			return
		}

		log.Printf("REFERRAL_REWARDED: referrer=%s referred=%s amount=%d", rewarded.ReferrerAccountIdentifier, rewarded.ReferredAccountIdentifier, rewarded.RewardAmountInMinorUnits)
		respondWithJson(responseWriter, http.StatusOK, map[string]any{"qualified": true, "rewardCredited": true, "referralLink": rewarded})
	}
}

// ---------------------------------------------------------------------
// FEATURES.md §14 multi-language/localization support —
// internal/localizationcatalog. See that package's README for the exact
// GET /localization/{lang} contract a future frontend-wiring pass
// should consume.

func buildLocalizationLanguagesHandler() http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"supportedLanguages": localizationcatalog.SupportedLanguages,
		})
	}
}

// buildLocalizationCatalogHandler serves GET /localization/{lang} — the
// full real translation catalog for one language, as a flat
// stringKey -> translatedText JSON object. Registered on the
// "/localization/" subtree pattern so {lang} is read from the URL path
// itself (e.g. GET /localization/hi), not a query parameter — see the
// package README for exactly why this shape was chosen for frontend
// consumption (a simple fetch(`/localization/${lang}`) with no query
// string).
func buildLocalizationCatalogHandler() http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		languageCode := localizationcatalog.LanguageCode(strings.TrimPrefix(request.URL.Path, "/localization/"))
		if languageCode == "" {
			http.Error(responseWriter, "missing language code in path, e.g. GET /localization/hi", http.StatusBadRequest)
			return
		}
		translations, ok := localizationcatalog.TranslationsForLanguage(languageCode)
		if !ok {
			respondWithJson(responseWriter, http.StatusNotFound, map[string]any{
				"errorMessage":       "unsupported language code",
				"supportedLanguages": localizationcatalog.SupportedLanguages,
			})
			return
		}
		respondWithJson(responseWriter, http.StatusOK, map[string]any{
			"languageCode": languageCode,
			"translations": translations,
		})
	}
}

// ---------------------------------------------------------------------

func respondWithJson(responseWriter http.ResponseWriter, statusCode int, body any) {
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(statusCode)
	_ = json.NewEncoder(responseWriter).Encode(body)
}
