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
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"mercurius/backoffice/internal/accountcontrol"
	"mercurius/backoffice/internal/familyaccountaccess"
	"mercurius/backoffice/internal/httplogging"
	"mercurius/backoffice/internal/nomineesuccession"
	"mercurius/backoffice/internal/omsgatewayclient"
	"mercurius/backoffice/internal/strategyleaderboard"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	omsGatewayBaseUrl := os.Getenv("OMS_GATEWAY_BASE_URL")
	if omsGatewayBaseUrl == "" {
		omsGatewayBaseUrl = "http://127.0.0.1:8081"
	}

	accountFreezeStateMachine := accountcontrol.NewAccountFreezeStateMachine()
	omsGatewayClient := omsgatewayclient.NewOmsGatewayClient(omsGatewayBaseUrl)
	strategyLeaderboardRanker := strategyleaderboard.NewRanker(omsGatewayClient)
	familyAccountAccessRegistry := familyaccountaccess.NewRegistry()
	nomineeSuccessionRegistry := nomineesuccession.NewRegistry()

	httpRequestMultiplexer := http.NewServeMux()
	httpRequestMultiplexer.HandleFunc("/health", func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = responseWriter.Write([]byte(`{"status":"ok","service":"backoffice"}`))
	})
	httpRequestMultiplexer.HandleFunc("/accounts/freeze", buildFreezeHandler(accountFreezeStateMachine))
	httpRequestMultiplexer.HandleFunc("/accounts/unfreeze", buildUnfreezeHandler(accountFreezeStateMachine))
	httpRequestMultiplexer.HandleFunc("/accounts/freeze-status", buildFreezeStatusHandler(accountFreezeStateMachine))

	httpRequestMultiplexer.HandleFunc("/strategy-leaderboard", buildStrategyLeaderboardHandler(strategyLeaderboardRanker))

	httpRequestMultiplexer.HandleFunc("/family-access/link", buildRegisterFamilyLinkHandler(familyAccountAccessRegistry))
	httpRequestMultiplexer.HandleFunc("/family-access/revoke", buildRevokeFamilyLinkHandler(familyAccountAccessRegistry))
	httpRequestMultiplexer.HandleFunc("/family-access/links", buildLinksForOwnerHandler(familyAccountAccessRegistry))
	httpRequestMultiplexer.HandleFunc("/family-access/positions", buildFamilyAccessPositionsHandler(familyAccountAccessRegistry, omsGatewayClient))

	httpRequestMultiplexer.HandleFunc("/nominee-succession/register-nominee", buildRegisterNomineeHandler(nomineeSuccessionRegistry))
	httpRequestMultiplexer.HandleFunc("/nominee-succession/nominee", buildGetNomineeHandler(nomineeSuccessionRegistry))
	httpRequestMultiplexer.HandleFunc("/nominee-succession/submit", buildSubmitSuccessionRequestHandler(nomineeSuccessionRegistry))
	httpRequestMultiplexer.HandleFunc("/nominee-succession/move-to-under-review", buildMoveToUnderReviewHandler(nomineeSuccessionRegistry))
	httpRequestMultiplexer.HandleFunc("/nominee-succession/approve", buildApproveHandler(nomineeSuccessionRegistry))
	httpRequestMultiplexer.HandleFunc("/nominee-succession/mark-transferred", buildMarkTransferredHandler(nomineeSuccessionRegistry))
	httpRequestMultiplexer.HandleFunc("/nominee-succession/reject", buildRejectHandler(nomineeSuccessionRegistry))
	httpRequestMultiplexer.HandleFunc("/nominee-succession/status", buildGetSuccessionRequestHandler(nomineeSuccessionRegistry))
	httpRequestMultiplexer.HandleFunc("/nominee-succession/audit-trail", buildSuccessionAuditTrailHandler(nomineeSuccessionRegistry))

	listenAddress := ":8084"
	log.Printf("backoffice listening on %s\n", listenAddress)
	if serverStartupError := http.ListenAndServe(listenAddress, httplogging.WithRequestLogging(httpRequestMultiplexer)); serverStartupError != nil {
		log.Fatalf("backoffice failed to start: %v", serverStartupError)
	}
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
	// TODO(real build): this endpoint has no auth/RBAC — anyone who can
	// reach it can freeze any account. Fine for a skeleton exercised by
	// developers; not fine for anything with real admin access controls.
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
// internal/familyaccountaccess. TODO(real build): no auth/RBAC on any
// of these endpoints — same gap the freeze endpoints above already
// document.

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
// internal/nomineesuccession. TODO(real build): no auth/RBAC on any of
// these endpoints, same gap as above.

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

func respondWithJson(responseWriter http.ResponseWriter, statusCode int, body any) {
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(statusCode)
	_ = json.NewEncoder(responseWriter).Encode(body)
}
