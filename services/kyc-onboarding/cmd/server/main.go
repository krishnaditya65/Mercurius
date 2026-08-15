// Mercurius / kyc-onboarding
//
// See FEATURES.md §1 for the full scope this service must eventually
// cover: real PAN/Aadhaar verification, liveness + selfie match,
// e-signature on account-opening documents, nominee/joint-holding
// management. As of this build, the PAN-format-check slice of that is
// real (internal/kycstate, oms-gateway genuinely gates order submission
// on it — see its internal/kycclient), bank account penny-drop
// verification is real too (internal/bankverification) — real random
// micro-deposit amount + limited confirmation attempts + lockout, just
// without a real payment rail behind it (see that package's doc
// comment) — and so is a risk-tolerance questionnaire that scores into
// an investor risk category (internal/riskprofiling), feeding a future
// (not built) Robo-Advisory feature.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"mercurius/kycOnboarding/internal/authmiddleware"
	"mercurius/kycOnboarding/internal/bankverification"
	"mercurius/kycOnboarding/internal/httplogging"
	"mercurius/kycOnboarding/internal/jointholding"
	"mercurius/kycOnboarding/internal/kycstate"
	"mercurius/kycOnboarding/internal/nomineedesignation"
	"mercurius/kycOnboarding/internal/riskprofiling"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	kycVerificationStateMachine := kycstate.NewKycVerificationStateMachine()
	bankAccountVerifier := bankverification.NewBankAccountVerifier()
	riskProfiler := riskprofiling.NewRiskProfiler()
	nomineeDesignationRegistry := nomineedesignation.NewNomineeDesignationRegistry()
	jointHoldingRegistry := jointholding.NewHoldingRegistry()

	signingSecret := authmiddleware.SigningSecretFromEnv()

	httpRequestMultiplexer := http.NewServeMux()
	// Public — no account data, nothing to gate.
	httpRequestMultiplexer.HandleFunc("/health", func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = responseWriter.Write([]byte(`{"status":"ok","service":"kyc-onboarding"}`))
	})
	// Public — the static questionnaire template, identical for every
	// account; no account data is read or returned.
	httpRequestMultiplexer.HandleFunc("/risk-profile/questionnaire", buildRiskProfileQuestionnaireHandler())

	// Authenticated, owner-only self-service — each of these acts on a
	// single account's own KYC/bank/nominee/joint-holding data. Where the
	// wire request carries an account identifier, requireOwnAccountFromJsonBody/
	// requireOwnAccountFromQueryParam additionally 403s a mismatch between
	// the authenticated caller and the account the request targets.
	httpRequestMultiplexer.HandleFunc("/kyc/status", requireOwnAccountFromQueryParam("accountId", signingSecret, buildKycStatusHandler(kycVerificationStateMachine)))
	httpRequestMultiplexer.HandleFunc("/kyc/submit", requireOwnAccountFromJsonBody(signingSecret, buildKycSubmitHandler(kycVerificationStateMachine)))
	httpRequestMultiplexer.HandleFunc("/bank-verification/initiate", requireOwnAccountFromJsonBody(signingSecret, buildBankVerificationInitiateHandler(bankAccountVerifier)))
	// /bank-verification/confirm and /status are keyed by verificationId,
	// not accountId — the wire request carries no account identifier
	// field to compare against the authenticated caller, so these are
	// authenticated-only (any logged-in caller who knows/guesses a
	// verificationId can confirm/check it). See report for why this is
	// flagged as worth tightening in a real build.
	httpRequestMultiplexer.HandleFunc("/bank-verification/confirm", authmiddleware.RequireAuth(signingSecret, buildBankVerificationConfirmHandler(bankAccountVerifier)))
	httpRequestMultiplexer.HandleFunc("/bank-verification/status", authmiddleware.RequireAuth(signingSecret, buildBankVerificationStatusHandler(bankAccountVerifier)))
	// Explicitly a debug-only endpoint (see its handler doc comment) that
	// exposes an internal micro-deposit amount that a real bank-verification
	// flow would never return to the account holder themselves (that
	// would defeat the point of the deposit-amount challenge). Gated
	// admin-only rather than owner-only self-service.
	httpRequestMultiplexer.HandleFunc("/bank-verification/debug-peek", authmiddleware.RequireRole(signingSecret, authmiddleware.RoleAdmin, buildBankVerificationDebugPeekHandler(bankAccountVerifier)))
	httpRequestMultiplexer.HandleFunc("/risk-profile/submit", requireOwnAccountFromJsonBody(signingSecret, buildRiskProfileSubmitHandler(riskProfiler)))
	httpRequestMultiplexer.HandleFunc("/risk-profile", requireOwnAccountFromQueryParam("accountId", signingSecret, buildRiskProfileStatusHandler(riskProfiler)))
	httpRequestMultiplexer.HandleFunc("/nominees/submit", requireOwnAccountFromJsonBody(signingSecret, buildNomineeSubmitHandler(nomineeDesignationRegistry)))
	httpRequestMultiplexer.HandleFunc("/nominees/add", requireOwnAccountFromJsonBody(signingSecret, buildNomineeAddHandler(nomineeDesignationRegistry)))
	httpRequestMultiplexer.HandleFunc("/nominees/update", requireOwnAccountFromJsonBody(signingSecret, buildNomineeUpdateHandler(nomineeDesignationRegistry)))
	httpRequestMultiplexer.HandleFunc("/nominees/remove", requireOwnAccountFromJsonBody(signingSecret, buildNomineeRemoveHandler(nomineeDesignationRegistry)))
	httpRequestMultiplexer.HandleFunc("/nominees", requireOwnAccountFromQueryParam("accountId", signingSecret, buildNomineeQueryHandler(nomineeDesignationRegistry)))
	httpRequestMultiplexer.HandleFunc("/joint-holding/register-individual", requireOwnAccountFromJsonBody(signingSecret, buildRegisterIndividualAccountHandler(jointHoldingRegistry)))
	httpRequestMultiplexer.HandleFunc("/joint-holding/register-joint", requireOwnAccountFromJsonBody(signingSecret, buildRegisterJointAccountHandler(jointHoldingRegistry)))
	httpRequestMultiplexer.HandleFunc("/joint-holding/authorize-operation", requireOwnAccountFromJsonBody(signingSecret, buildAuthorizeOperationHandler(jointHoldingRegistry)))
	httpRequestMultiplexer.HandleFunc("/joint-holding", requireOwnAccountFromQueryParam("accountId", signingSecret, buildJointHoldingQueryHandler(jointHoldingRegistry)))

	// Admin-only — the compliance review-queue workflow. RoleCompliance
	// (not RoleAdmin) is used deliberately: this is specifically the KYC
	// compliance review/override workflow FEATURES.md §14 describes, and
	// jwtauth's role set carries a dedicated RoleCompliance for exactly
	// this domain rather than lumping it under general RoleAdmin. See
	// report for the tradeoff this implies (a general RoleAdmin account
	// cannot reach these routes under this build; a real build may want
	// an "any of these roles" helper instead of RequireRole's single-role
	// match).
	httpRequestMultiplexer.HandleFunc("/kyc/review-queue", authmiddleware.RequireRole(signingSecret, authmiddleware.RoleCompliance, buildKycReviewQueueHandler(kycVerificationStateMachine)))
	httpRequestMultiplexer.HandleFunc("/kyc/review-queue/override", authmiddleware.RequireRole(signingSecret, authmiddleware.RoleCompliance, buildKycReviewOverrideHandler(kycVerificationStateMachine)))

	listenAddress := ":8083"
	log.Printf("kyc-onboarding listening on %s\n", listenAddress)
	if serverStartupError := http.ListenAndServe(listenAddress, withAllowListedCors(httplogging.WithRequestLogging(httpRequestMultiplexer))); serverStartupError != nil {
		log.Fatalf("kyc-onboarding failed to start: %v", serverStartupError)
	}
}

// accountIdentifierWireEnvelope is the minimal shape shared by every
// mutating request below that carries an account identifier — used only
// to peek at that one field before the real handler runs its own full
// decode.
type accountIdentifierWireEnvelope struct {
	AccountIdentifier string `json:"accountIdentifier"`
}

// ownAccountMismatchErrorWireResponse matches this repo's dominant JSON
// error-response shape (`errorMessage`), same as authmiddleware's own
// error responses.
type ownAccountMismatchErrorWireResponse struct {
	ErrorMessage string `json:"errorMessage"`
}

func respondWithOwnAccountMismatch(responseWriter http.ResponseWriter) {
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(responseWriter).Encode(ownAccountMismatchErrorWireResponse{ErrorMessage: "you can only act on your own account"})
}

// requireOwnAccountFromQueryParam is RequireAuth plus an ownership check
// for GET-style routes that identify the target account via a query
// parameter (?accountId=...).
func requireOwnAccountFromQueryParam(queryParamName string, signingSecret []byte, next http.HandlerFunc) http.HandlerFunc {
	return authmiddleware.RequireAuth(signingSecret, func(responseWriter http.ResponseWriter, request *http.Request) {
		targetAccountIdentifier := request.URL.Query().Get(queryParamName)
		authenticatedAccountIdentifier, _ := authmiddleware.AuthenticatedAccountIdentifier(request)
		if targetAccountIdentifier != "" && targetAccountIdentifier != authenticatedAccountIdentifier {
			respondWithOwnAccountMismatch(responseWriter)
			return
		}
		next(responseWriter, request)
	})
}

// requireOwnAccountFromJsonBody is RequireAuth plus an ownership check
// for POST-style routes that identify the target account via an
// "accountIdentifier" field in the JSON body. It peeks the body just far
// enough to read that one field, then rewinds request.Body so the wrapped
// handler's own decode sees the exact same bytes it always has. A body
// that fails to decode even that far is passed through untouched — the
// wrapped handler's own decode will produce its normal "malformed
// payload" 400, rather than this wrapper masking that with a different
// error.
func requireOwnAccountFromJsonBody(signingSecret []byte, next http.HandlerFunc) http.HandlerFunc {
	return authmiddleware.RequireAuth(signingSecret, func(responseWriter http.ResponseWriter, request *http.Request) {
		bodyBytes, readError := io.ReadAll(request.Body)
		if readError != nil {
			http.Error(responseWriter, "failed to read request body", http.StatusBadRequest)
			return
		}
		_ = request.Body.Close()
		request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

		var envelope accountIdentifierWireEnvelope
		if decodeError := json.Unmarshal(bodyBytes, &envelope); decodeError != nil {
			next(responseWriter, request)
			return
		}

		authenticatedAccountIdentifier, _ := authmiddleware.AuthenticatedAccountIdentifier(request)
		if envelope.AccountIdentifier != "" && envelope.AccountIdentifier != authenticatedAccountIdentifier {
			respondWithOwnAccountMismatch(responseWriter)
			return
		}
		next(responseWriter, request)
	})
}

// corsAllowedOriginsFromEnv reads a comma-separated CORS_ALLOWED_ORIGINS
// env var, defaulting to the two local frontend dev ports this repo's
// apps/web has historically run on when the env var is unset.
func corsAllowedOriginsFromEnv() map[string]bool {
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

// withAllowListedCors echoes back Access-Control-Allow-Origin only for a
// request whose Origin header is on the CORS_ALLOWED_ORIGINS allow-list
// (defaulting to the two local frontend dev ports), paired with
// Access-Control-Allow-Credentials: true — never `*`, since this service
// now gates routes on a real bearer token and a wildcard origin has no
// business being paired with real auth. A request from an origin NOT on
// the allow-list gets no CORS headers at all (the browser then blocks the
// response from being read cross-origin, same effective result as a
// same-origin request from a plain non-browser client, which never sends
// an Origin header and is unaffected either way).
func withAllowListedCors(nextHandler http.Handler) http.Handler {
	allowedOrigins := corsAllowedOriginsFromEnv()
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

type kycStatusWireResponse struct {
	AccountIdentifier       string `json:"accountIdentifier"`
	KycVerificationStage    string `json:"kycVerificationStage"`
	IsEligibleToPlaceOrders bool   `json:"isEligibleToPlaceOrders"`
	RejectionReason         string `json:"rejectionReason,omitempty"`
}

func buildKycStatusHandler(kycVerificationStateMachine *kycstate.KycVerificationStateMachine) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}

		record := kycVerificationStateMachine.LookupKycStatus(accountIdentifier)

		responseWriter.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(responseWriter).Encode(kycStatusWireResponse{
			AccountIdentifier:       record.AccountIdentifier,
			KycVerificationStage:    string(record.VerificationStage),
			IsEligibleToPlaceOrders: record.IsEligibleToPlaceOrders(),
			RejectionReason:         record.RejectionReason,
		})
	}
}

type kycSubmitWireRequest struct {
	AccountIdentifier string `json:"accountIdentifier"`
	PanNumber         string `json:"panNumber"`
	FullName          string `json:"fullName"`
}

func buildKycSubmitHandler(kycVerificationStateMachine *kycstate.KycVerificationStateMachine) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest kycSubmitWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed KYC submission payload", http.StatusBadRequest)
			return
		}

		record := kycVerificationStateMachine.SubmitKycDetails(
			wireRequest.AccountIdentifier,
			wireRequest.PanNumber,
			wireRequest.FullName,
		)

		responseWriter.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(responseWriter).Encode(kycStatusWireResponse{
			AccountIdentifier:       record.AccountIdentifier,
			KycVerificationStage:    string(record.VerificationStage),
			IsEligibleToPlaceOrders: record.IsEligibleToPlaceOrders(),
			RejectionReason:         record.RejectionReason,
		})
	}
}

type bankVerificationInitiateWireRequest struct {
	AccountIdentifier string `json:"accountIdentifier"`
	BankAccountNumber string `json:"bankAccountNumber"`
	IfscCode          string `json:"ifscCode"`
}

type bankVerificationInitiateWireResponse struct {
	VerificationId string `json:"verificationId,omitempty"`
	ErrorMessage   string `json:"errorMessage,omitempty"`
}

func buildBankVerificationInitiateHandler(verifier *bankverification.BankAccountVerifier) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest bankVerificationInitiateWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed bank verification initiate payload", http.StatusBadRequest)
			return
		}
		if wireRequest.AccountIdentifier == "" || wireRequest.BankAccountNumber == "" || wireRequest.IfscCode == "" {
			respondWithBankVerificationJson(responseWriter, http.StatusBadRequest, bankVerificationInitiateWireResponse{
				ErrorMessage: "accountIdentifier, bankAccountNumber, and ifscCode are all required",
			})
			return
		}

		verificationId, initiateError := verifier.InitiateVerification(
			wireRequest.AccountIdentifier,
			wireRequest.BankAccountNumber,
			wireRequest.IfscCode,
		)
		if initiateError != nil {
			log.Printf("failed to initiate bank verification for %s: %v", wireRequest.AccountIdentifier, initiateError)
			http.Error(responseWriter, "failed to initiate bank verification", http.StatusInternalServerError)
			return
		}

		// Deliberately does NOT include the deposited amount — see
		// internal/bankverification's package doc. A real integration
		// would trigger the actual bank transfer here.
		respondWithBankVerificationJson(responseWriter, http.StatusOK, bankVerificationInitiateWireResponse{VerificationId: verificationId})
	}
}

type bankVerificationConfirmWireRequest struct {
	VerificationId          string `json:"verificationId"`
	ClaimedAmountMinorUnits int64  `json:"claimedAmountMinorUnits"`
}

type bankVerificationStatusWireResponse struct {
	VerificationId string `json:"verificationId"`
	Status         string `json:"status"`
}

func buildBankVerificationConfirmHandler(verifier *bankverification.BankAccountVerifier) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest bankVerificationConfirmWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed bank verification confirm payload", http.StatusBadRequest)
			return
		}

		status := verifier.ConfirmMicroDepositAmount(wireRequest.VerificationId, wireRequest.ClaimedAmountMinorUnits)
		respondWithBankVerificationJson(responseWriter, http.StatusOK, bankVerificationStatusWireResponse{
			VerificationId: wireRequest.VerificationId,
			Status:         string(status),
		})
	}
}

func buildBankVerificationStatusHandler(verifier *bankverification.BankAccountVerifier) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		verificationId := request.URL.Query().Get("verificationId")
		if verificationId == "" {
			http.Error(responseWriter, "missing verificationId query parameter", http.StatusBadRequest)
			return
		}

		status := verifier.QueryVerificationStatus(verificationId)
		respondWithBankVerificationJson(responseWriter, http.StatusOK, bankVerificationStatusWireResponse{
			VerificationId: verificationId,
			Status:         string(status),
		})
	}
}

type bankVerificationDebugPeekWireResponse struct {
	VerificationId            string `json:"verificationId"`
	DepositedAmountMinorUnits int64  `json:"depositedAmountMinorUnits,omitempty"`
	ErrorMessage              string `json:"errorMessage,omitempty"`
}

// buildBankVerificationDebugPeekHandler exists ONLY because this repo
// has no real payment rail to actually move money into an external bank
// account for a live demo/test session to then go check — see
// internal/bankverification's PeekAtMicroDepositAmountForTesting doc
// comment. A real build deletes this endpoint entirely.
func buildBankVerificationDebugPeekHandler(verifier *bankverification.BankAccountVerifier) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		verificationId := request.URL.Query().Get("verificationId")
		if verificationId == "" {
			http.Error(responseWriter, "missing verificationId query parameter", http.StatusBadRequest)
			return
		}

		amount, wasFound := verifier.PeekAtMicroDepositAmountForTesting(verificationId)
		if !wasFound {
			respondWithBankVerificationJson(responseWriter, http.StatusNotFound, bankVerificationDebugPeekWireResponse{
				VerificationId: verificationId,
				ErrorMessage:   "no such verification",
			})
			return
		}

		respondWithBankVerificationJson(responseWriter, http.StatusOK, bankVerificationDebugPeekWireResponse{
			VerificationId:            verificationId,
			DepositedAmountMinorUnits: amount,
		})
	}
}

func respondWithBankVerificationJson(responseWriter http.ResponseWriter, statusCode int, body any) {
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(statusCode)
	_ = json.NewEncoder(responseWriter).Encode(body)
}

type questionOptionWireFormat struct {
	OptionText string `json:"optionText"`
	PointValue int    `json:"pointValue"`
}

type questionWireFormat struct {
	QuestionId   string                     `json:"questionId"`
	QuestionText string                     `json:"questionText"`
	Options      []questionOptionWireFormat `json:"options"`
}

// buildRiskProfileQuestionnaireHandler serves the STATIC standard
// questionnaire — no account context needed, every account answers the
// same fixed set of questions.
func buildRiskProfileQuestionnaireHandler() http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, _ *http.Request) {
		wireQuestions := make([]questionWireFormat, 0, len(riskprofiling.StandardQuestionnaire))
		for _, question := range riskprofiling.StandardQuestionnaire {
			wireOptions := make([]questionOptionWireFormat, 0, len(question.Options))
			for _, option := range question.Options {
				wireOptions = append(wireOptions, questionOptionWireFormat{OptionText: option.OptionText, PointValue: option.PointValue})
			}
			wireQuestions = append(wireQuestions, questionWireFormat{
				QuestionId:   question.QuestionId,
				QuestionText: question.QuestionText,
				Options:      wireOptions,
			})
		}
		respondWithBankVerificationJson(responseWriter, http.StatusOK, wireQuestions)
	}
}

type riskProfileSubmitWireRequest struct {
	AccountIdentifier             string         `json:"accountIdentifier"`
	AnswerPointValuesByQuestionId map[string]int `json:"answerPointValuesByQuestionId"`
}

type riskProfileWireResponse struct {
	AccountIdentifier string `json:"accountIdentifier"`
	TotalScore        int    `json:"totalScore,omitempty"`
	RiskCategory      string `json:"riskCategory,omitempty"`
	ErrorMessage      string `json:"errorMessage,omitempty"`
}

func buildRiskProfileSubmitHandler(riskProfiler *riskprofiling.RiskProfiler) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest riskProfileSubmitWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed risk-profile submission payload", http.StatusBadRequest)
			return
		}

		profile, submitError := riskProfiler.SubmitAnswers(wireRequest.AccountIdentifier, wireRequest.AnswerPointValuesByQuestionId)
		if submitError != nil {
			respondWithBankVerificationJson(responseWriter, http.StatusBadRequest, riskProfileWireResponse{
				AccountIdentifier: wireRequest.AccountIdentifier,
				ErrorMessage:      submitError.Error(),
			})
			return
		}

		respondWithBankVerificationJson(responseWriter, http.StatusOK, riskProfileWireResponse{
			AccountIdentifier: profile.AccountIdentifier,
			TotalScore:        profile.TotalScore,
			RiskCategory:      string(profile.RiskCategory),
		})
	}
}

func buildRiskProfileStatusHandler(riskProfiler *riskprofiling.RiskProfiler) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}

		profile, wasFound := riskProfiler.LookupProfile(accountIdentifier)
		if !wasFound {
			respondWithBankVerificationJson(responseWriter, http.StatusNotFound, riskProfileWireResponse{
				AccountIdentifier: accountIdentifier,
				ErrorMessage:      "no risk profile submitted for this account yet",
			})
			return
		}

		respondWithBankVerificationJson(responseWriter, http.StatusOK, riskProfileWireResponse{
			AccountIdentifier: profile.AccountIdentifier,
			TotalScore:        profile.TotalScore,
			RiskCategory:      string(profile.RiskCategory),
		})
	}
}

type kycReviewQueueRecordWireFormat struct {
	AccountIdentifier  string `json:"accountIdentifier"`
	VerificationStage  string `json:"verificationStage"`
	SubmittedPanNumber string `json:"submittedPanNumber"`
	SubmittedFullName  string `json:"submittedFullName"`
	RejectionReason    string `json:"rejectionReason,omitempty"`
}

// buildKycReviewQueueHandler is FEATURES.md §14's "KYC review queue" —
// GET /kyc/review-queue (no filter) defaults to the actually-useful
// queue an admin would page through: every automatically REJECTED
// submission, since those are the ones worth a human second look.
// ?stage=VERIFIED lets an admin instead browse everything currently
// verified (e.g. for a spot-audit), though REJECTED is what "review
// queue" means in the common case.
func buildKycReviewQueueHandler(kycVerificationStateMachine *kycstate.KycVerificationStateMachine) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		stage := kycstate.KycVerificationStage(request.URL.Query().Get("stage"))
		if stage == "" {
			stage = kycstate.StageRejected
		}

		records := kycVerificationStateMachine.ListRecordsByStage(stage)
		wireRecords := make([]kycReviewQueueRecordWireFormat, 0, len(records))
		for _, record := range records {
			wireRecords = append(wireRecords, kycReviewQueueRecordWireFormat{
				AccountIdentifier:  record.AccountIdentifier,
				VerificationStage:  string(record.VerificationStage),
				SubmittedPanNumber: record.SubmittedPanNumber,
				SubmittedFullName:  record.SubmittedFullName,
				RejectionReason:    record.RejectionReason,
			})
		}
		respondWithBankVerificationJson(responseWriter, http.StatusOK, wireRecords)
	}
}

type kycReviewOverrideWireRequest struct {
	AccountIdentifier string `json:"accountIdentifier"`
	NewStage          string `json:"newStage"`
	OverrideReason    string `json:"overrideReason,omitempty"`
}

// buildKycReviewOverrideHandler is the actual admin DECISION a review-
// queue entry resolves to — a human explicitly overturning (or
// retroactively reversing) an automated KYC stage.
//
// TODO(real build): no auth, so anyone who can reach this endpoint can
// override any account's KYC stage — completely unacceptable for a real
// admin action, same category of gap as every other unauthenticated
// endpoint in this repo, but especially severe here since this one
// grants/revokes trading eligibility directly. Also: no audit trail
// entry is recorded for this specific action (unlike oms-gateway's
// audittrail package) — a real build needs one, since "who overrode
// this account's KYC and why" is exactly the kind of thing a compliance
// audit would ask for.
func buildKycReviewOverrideHandler(kycVerificationStateMachine *kycstate.KycVerificationStateMachine) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest kycReviewOverrideWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed review-override payload", http.StatusBadRequest)
			return
		}

		updatedRecord, overrideError := kycVerificationStateMachine.OverrideStage(
			wireRequest.AccountIdentifier,
			kycstate.KycVerificationStage(wireRequest.NewStage),
			wireRequest.OverrideReason,
		)
		if overrideError != nil {
			respondWithBankVerificationJson(responseWriter, http.StatusBadRequest, kycReviewQueueRecordWireFormat{
				AccountIdentifier: wireRequest.AccountIdentifier,
				RejectionReason:   overrideError.Error(),
			})
			return
		}

		respondWithBankVerificationJson(responseWriter, http.StatusOK, kycReviewQueueRecordWireFormat{
			AccountIdentifier:  updatedRecord.AccountIdentifier,
			VerificationStage:  string(updatedRecord.VerificationStage),
			SubmittedPanNumber: updatedRecord.SubmittedPanNumber,
			SubmittedFullName:  updatedRecord.SubmittedFullName,
			RejectionReason:    updatedRecord.RejectionReason,
		})
	}
}

// dateOfBirthWireLayout is the wire format for every date-of-birth field
// below — a plain "YYYY-MM-DD" calendar date, no time-of-day/timezone
// component, matching how a real nomination form asks for a birth date.
const dateOfBirthWireLayout = "2006-01-02"

type nomineeInputWireRequest struct {
	FullName                          string `json:"fullName"`
	Relationship                      string `json:"relationship"`
	DateOfBirth                       string `json:"dateOfBirth"`
	PercentageAllocation              int    `json:"percentageAllocation"`
	GuardianFullName                  string `json:"guardianFullName,omitempty"`
	GuardianRelationship              string `json:"guardianRelationship,omitempty"`
	GuardianIdentityDocumentReference string `json:"guardianIdentityDocumentReference,omitempty"`
}

func (wireInput nomineeInputWireRequest) toNomineeInput() (nomineedesignation.NomineeInput, error) {
	var dateOfBirth time.Time
	if wireInput.DateOfBirth != "" {
		parsed, parseErr := time.Parse(dateOfBirthWireLayout, wireInput.DateOfBirth)
		if parseErr != nil {
			return nomineedesignation.NomineeInput{}, fmt.Errorf("dateOfBirth must be in %s format: %w", dateOfBirthWireLayout, parseErr)
		}
		dateOfBirth = parsed
	}
	return nomineedesignation.NomineeInput{
		FullName:                          wireInput.FullName,
		Relationship:                      wireInput.Relationship,
		DateOfBirth:                       dateOfBirth,
		PercentageAllocation:              wireInput.PercentageAllocation,
		GuardianFullName:                  wireInput.GuardianFullName,
		GuardianRelationship:              wireInput.GuardianRelationship,
		GuardianIdentityDocumentReference: wireInput.GuardianIdentityDocumentReference,
	}, nil
}

type nomineeWireResponse struct {
	NomineeId                         string `json:"nomineeId"`
	FullName                          string `json:"fullName"`
	Relationship                      string `json:"relationship"`
	DateOfBirth                       string `json:"dateOfBirth"`
	PercentageAllocation              int    `json:"percentageAllocation"`
	IsMinor                           bool   `json:"isMinor"`
	GuardianFullName                  string `json:"guardianFullName,omitempty"`
	GuardianRelationship              string `json:"guardianRelationship,omitempty"`
	GuardianIdentityDocumentReference string `json:"guardianIdentityDocumentReference,omitempty"`
}

type nomineeDesignationWireResponse struct {
	AccountIdentifier        string                `json:"accountIdentifier"`
	IsOptedOutOfNomination   bool                  `json:"isOptedOutOfNomination"`
	Nominees                 []nomineeWireResponse `json:"nominees"`
	TotalPercentageAllocated int                   `json:"totalPercentageAllocated"`
	IsComplete               bool                  `json:"isComplete"`
	ErrorMessage             string                `json:"errorMessage,omitempty"`
}

func toNomineeDesignationWireResponse(designation nomineedesignation.NomineeDesignation, now time.Time) nomineeDesignationWireResponse {
	wireNominees := make([]nomineeWireResponse, 0, len(designation.Nominees))
	for _, nominee := range designation.Nominees {
		wireNominees = append(wireNominees, nomineeWireResponse{
			NomineeId:                         nominee.NomineeId,
			FullName:                          nominee.FullName,
			Relationship:                      nominee.Relationship,
			DateOfBirth:                       nominee.DateOfBirth.Format(dateOfBirthWireLayout),
			PercentageAllocation:              nominee.PercentageAllocation,
			IsMinor:                           nominee.IsMinorAsOf(now),
			GuardianFullName:                  nominee.GuardianFullName,
			GuardianRelationship:              nominee.GuardianRelationship,
			GuardianIdentityDocumentReference: nominee.GuardianIdentityDocumentReference,
		})
	}
	return nomineeDesignationWireResponse{
		AccountIdentifier:        designation.AccountIdentifier,
		IsOptedOutOfNomination:   designation.IsOptedOutOfNomination,
		Nominees:                 wireNominees,
		TotalPercentageAllocated: designation.TotalPercentageAllocated(),
		IsComplete:               designation.IsComplete(),
	}
}

type nomineeSubmitWireRequest struct {
	AccountIdentifier      string                    `json:"accountIdentifier"`
	Nominees               []nomineeInputWireRequest `json:"nominees"`
	IsOptedOutOfNomination bool                      `json:"isOptedOutOfNomination"`
}

// buildNomineeSubmitHandler is the real "fill in and submit the
// nomination form" action — see internal/nomineedesignation's package doc
// for why this is the one endpoint that hard-gates on percentages summing
// to exactly 100 (or an explicit opt-out).
func buildNomineeSubmitHandler(registry *nomineedesignation.NomineeDesignationRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest nomineeSubmitWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed nominee submission payload", http.StatusBadRequest)
			return
		}

		now := time.Now()
		nomineeInputs := make([]nomineedesignation.NomineeInput, 0, len(wireRequest.Nominees))
		for _, wireNominee := range wireRequest.Nominees {
			input, convertErr := wireNominee.toNomineeInput()
			if convertErr != nil {
				respondWithBankVerificationJson(responseWriter, http.StatusBadRequest, nomineeDesignationWireResponse{
					AccountIdentifier: wireRequest.AccountIdentifier,
					ErrorMessage:      convertErr.Error(),
				})
				return
			}
			nomineeInputs = append(nomineeInputs, input)
		}

		designation, submitErr := registry.SubmitNomination(wireRequest.AccountIdentifier, nomineeInputs, wireRequest.IsOptedOutOfNomination, now)
		if submitErr != nil {
			respondWithBankVerificationJson(responseWriter, http.StatusBadRequest, nomineeDesignationWireResponse{
				AccountIdentifier: wireRequest.AccountIdentifier,
				ErrorMessage:      submitErr.Error(),
			})
			return
		}

		respondWithBankVerificationJson(responseWriter, http.StatusOK, toNomineeDesignationWireResponse(designation, now))
	}
}

type nomineeMutateWireRequest struct {
	AccountIdentifier string                  `json:"accountIdentifier"`
	NomineeId         string                  `json:"nomineeId,omitempty"`
	Nominee           nomineeInputWireRequest `json:"nominee"`
}

// buildNomineeAddHandler manages an already-existing (or brand new, empty)
// designation incrementally — see internal/nomineedesignation's
// AddNominee doc comment for how this differs from the hard-gated /submit
// form endpoint above.
func buildNomineeAddHandler(registry *nomineedesignation.NomineeDesignationRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest nomineeMutateWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed nominee add payload", http.StatusBadRequest)
			return
		}

		now := time.Now()
		input, convertErr := wireRequest.Nominee.toNomineeInput()
		if convertErr != nil {
			respondWithBankVerificationJson(responseWriter, http.StatusBadRequest, nomineeDesignationWireResponse{
				AccountIdentifier: wireRequest.AccountIdentifier,
				ErrorMessage:      convertErr.Error(),
			})
			return
		}

		designation, addErr := registry.AddNominee(wireRequest.AccountIdentifier, input, now)
		if addErr != nil {
			respondWithBankVerificationJson(responseWriter, http.StatusBadRequest, nomineeDesignationWireResponse{
				AccountIdentifier: wireRequest.AccountIdentifier,
				ErrorMessage:      addErr.Error(),
			})
			return
		}

		respondWithBankVerificationJson(responseWriter, http.StatusOK, toNomineeDesignationWireResponse(designation, now))
	}
}

func buildNomineeUpdateHandler(registry *nomineedesignation.NomineeDesignationRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest nomineeMutateWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed nominee update payload", http.StatusBadRequest)
			return
		}
		if wireRequest.NomineeId == "" {
			respondWithBankVerificationJson(responseWriter, http.StatusBadRequest, nomineeDesignationWireResponse{
				AccountIdentifier: wireRequest.AccountIdentifier,
				ErrorMessage:      "nomineeId is required",
			})
			return
		}

		now := time.Now()
		input, convertErr := wireRequest.Nominee.toNomineeInput()
		if convertErr != nil {
			respondWithBankVerificationJson(responseWriter, http.StatusBadRequest, nomineeDesignationWireResponse{
				AccountIdentifier: wireRequest.AccountIdentifier,
				ErrorMessage:      convertErr.Error(),
			})
			return
		}

		designation, updateErr := registry.UpdateNominee(wireRequest.AccountIdentifier, wireRequest.NomineeId, input, now)
		if updateErr != nil {
			respondWithBankVerificationJson(responseWriter, http.StatusBadRequest, nomineeDesignationWireResponse{
				AccountIdentifier: wireRequest.AccountIdentifier,
				ErrorMessage:      updateErr.Error(),
			})
			return
		}

		respondWithBankVerificationJson(responseWriter, http.StatusOK, toNomineeDesignationWireResponse(designation, now))
	}
}

type nomineeRemoveWireRequest struct {
	AccountIdentifier string `json:"accountIdentifier"`
	NomineeId         string `json:"nomineeId"`
}

func buildNomineeRemoveHandler(registry *nomineedesignation.NomineeDesignationRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest nomineeRemoveWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed nominee remove payload", http.StatusBadRequest)
			return
		}

		now := time.Now()
		designation, removeErr := registry.RemoveNominee(wireRequest.AccountIdentifier, wireRequest.NomineeId, now)
		if removeErr != nil {
			respondWithBankVerificationJson(responseWriter, http.StatusBadRequest, nomineeDesignationWireResponse{
				AccountIdentifier: wireRequest.AccountIdentifier,
				ErrorMessage:      removeErr.Error(),
			})
			return
		}

		respondWithBankVerificationJson(responseWriter, http.StatusOK, toNomineeDesignationWireResponse(designation, now))
	}
}

func buildNomineeQueryHandler(registry *nomineedesignation.NomineeDesignationRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}

		now := time.Now()
		designation, wasFound := registry.GetDesignation(accountIdentifier)
		if !wasFound {
			respondWithBankVerificationJson(responseWriter, http.StatusNotFound, nomineeDesignationWireResponse{
				AccountIdentifier: accountIdentifier,
				ErrorMessage:      "no nominee designation submitted for this account yet",
			})
			return
		}

		respondWithBankVerificationJson(responseWriter, http.StatusOK, toNomineeDesignationWireResponse(designation, now))
	}
}

type holderWireResponse struct {
	HolderId        string `json:"holderId"`
	FullName        string `json:"fullName"`
	IsPrimaryHolder bool   `json:"isPrimaryHolder"`
}

type holdingStructureWireResponse struct {
	AccountIdentifier string               `json:"accountIdentifier"`
	HoldingType       string               `json:"holdingType"`
	HoldingMode       string               `json:"holdingMode,omitempty"`
	Holders           []holderWireResponse `json:"holders"`
	ErrorMessage      string               `json:"errorMessage,omitempty"`
}

func toHoldingStructureWireResponse(structure jointholding.HoldingStructure) holdingStructureWireResponse {
	wireHolders := make([]holderWireResponse, 0, len(structure.Holders))
	for _, holder := range structure.Holders {
		wireHolders = append(wireHolders, holderWireResponse{
			HolderId:        holder.HolderId,
			FullName:        holder.FullName,
			IsPrimaryHolder: holder.IsPrimaryHolder,
		})
	}
	return holdingStructureWireResponse{
		AccountIdentifier: structure.AccountIdentifier,
		HoldingType:       string(structure.HoldingType),
		HoldingMode:       string(structure.HoldingMode),
		Holders:           wireHolders,
	}
}

type registerIndividualAccountWireRequest struct {
	AccountIdentifier  string `json:"accountIdentifier"`
	SoleHolderFullName string `json:"soleHolderFullName"`
}

func buildRegisterIndividualAccountHandler(registry *jointholding.HoldingRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest registerIndividualAccountWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed register-individual payload", http.StatusBadRequest)
			return
		}

		structure, registerErr := registry.RegisterIndividualAccount(wireRequest.AccountIdentifier, wireRequest.SoleHolderFullName)
		if registerErr != nil {
			respondWithBankVerificationJson(responseWriter, http.StatusBadRequest, holdingStructureWireResponse{
				AccountIdentifier: wireRequest.AccountIdentifier,
				ErrorMessage:      registerErr.Error(),
			})
			return
		}

		respondWithBankVerificationJson(responseWriter, http.StatusOK, toHoldingStructureWireResponse(structure))
	}
}

type registerJointAccountWireRequest struct {
	AccountIdentifier  string   `json:"accountIdentifier"`
	HoldingMode        string   `json:"holdingMode"`
	HolderFullNames    []string `json:"holderFullNames"`
	PrimaryHolderIndex int      `json:"primaryHolderIndex"`
}

func buildRegisterJointAccountHandler(registry *jointholding.HoldingRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest registerJointAccountWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed register-joint payload", http.StatusBadRequest)
			return
		}

		structure, registerErr := registry.RegisterJointAccount(
			wireRequest.AccountIdentifier,
			jointholding.JointHoldingMode(wireRequest.HoldingMode),
			wireRequest.HolderFullNames,
			wireRequest.PrimaryHolderIndex,
		)
		if registerErr != nil {
			respondWithBankVerificationJson(responseWriter, http.StatusBadRequest, holdingStructureWireResponse{
				AccountIdentifier: wireRequest.AccountIdentifier,
				ErrorMessage:      registerErr.Error(),
			})
			return
		}

		respondWithBankVerificationJson(responseWriter, http.StatusOK, toHoldingStructureWireResponse(structure))
	}
}

type authorizeOperationWireRequest struct {
	AccountIdentifier   string   `json:"accountIdentifier"`
	ConsentingHolderIds []string `json:"consentingHolderIds"`
}

type authorizeOperationWireResponse struct {
	AccountIdentifier string `json:"accountIdentifier"`
	IsAuthorized      bool   `json:"isAuthorized"`
	ErrorMessage      string `json:"errorMessage,omitempty"`
}

func buildAuthorizeOperationHandler(registry *jointholding.HoldingRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "only POST is supported", http.StatusMethodNotAllowed)
			return
		}

		var wireRequest authorizeOperationWireRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&wireRequest); decodeError != nil {
			http.Error(responseWriter, "malformed authorize-operation payload", http.StatusBadRequest)
			return
		}

		isAuthorized, authorizeErr := registry.AuthorizeOperation(wireRequest.AccountIdentifier, wireRequest.ConsentingHolderIds)
		if authorizeErr != nil {
			respondWithBankVerificationJson(responseWriter, http.StatusBadRequest, authorizeOperationWireResponse{
				AccountIdentifier: wireRequest.AccountIdentifier,
				ErrorMessage:      authorizeErr.Error(),
			})
			return
		}

		respondWithBankVerificationJson(responseWriter, http.StatusOK, authorizeOperationWireResponse{
			AccountIdentifier: wireRequest.AccountIdentifier,
			IsAuthorized:      isAuthorized,
		})
	}
}

func buildJointHoldingQueryHandler(registry *jointholding.HoldingRegistry) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		accountIdentifier := request.URL.Query().Get("accountId")
		if accountIdentifier == "" {
			http.Error(responseWriter, "missing accountId query parameter", http.StatusBadRequest)
			return
		}

		structure, wasFound := registry.GetHoldingStructure(accountIdentifier)
		if !wasFound {
			respondWithBankVerificationJson(responseWriter, http.StatusNotFound, holdingStructureWireResponse{
				AccountIdentifier: accountIdentifier,
				ErrorMessage:      "no holding structure registered for this account yet",
			})
			return
		}

		respondWithBankVerificationJson(responseWriter, http.StatusOK, toHoldingStructureWireResponse(structure))
	}
}
