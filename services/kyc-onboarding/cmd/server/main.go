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
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"os"

	"mercurius/kycOnboarding/internal/bankverification"
	"mercurius/kycOnboarding/internal/httplogging"
	"mercurius/kycOnboarding/internal/kycstate"
	"mercurius/kycOnboarding/internal/riskprofiling"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	kycVerificationStateMachine := kycstate.NewKycVerificationStateMachine()
	bankAccountVerifier := bankverification.NewBankAccountVerifier()
	riskProfiler := riskprofiling.NewRiskProfiler()

	httpRequestMultiplexer := http.NewServeMux()
	httpRequestMultiplexer.HandleFunc("/health", func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = responseWriter.Write([]byte(`{"status":"ok","service":"kyc-onboarding"}`))
	})
	httpRequestMultiplexer.HandleFunc("/kyc/status", buildKycStatusHandler(kycVerificationStateMachine))
	httpRequestMultiplexer.HandleFunc("/kyc/submit", buildKycSubmitHandler(kycVerificationStateMachine))
	httpRequestMultiplexer.HandleFunc("/bank-verification/initiate", buildBankVerificationInitiateHandler(bankAccountVerifier))
	httpRequestMultiplexer.HandleFunc("/bank-verification/confirm", buildBankVerificationConfirmHandler(bankAccountVerifier))
	httpRequestMultiplexer.HandleFunc("/bank-verification/status", buildBankVerificationStatusHandler(bankAccountVerifier))
	httpRequestMultiplexer.HandleFunc("/bank-verification/debug-peek", buildBankVerificationDebugPeekHandler(bankAccountVerifier))
	httpRequestMultiplexer.HandleFunc("/risk-profile/questionnaire", buildRiskProfileQuestionnaireHandler())
	httpRequestMultiplexer.HandleFunc("/risk-profile/submit", buildRiskProfileSubmitHandler(riskProfiler))
	httpRequestMultiplexer.HandleFunc("/risk-profile", buildRiskProfileStatusHandler(riskProfiler))
	httpRequestMultiplexer.HandleFunc("/kyc/review-queue", buildKycReviewQueueHandler(kycVerificationStateMachine))
	httpRequestMultiplexer.HandleFunc("/kyc/review-queue/override", buildKycReviewOverrideHandler(kycVerificationStateMachine))

	listenAddress := ":8083"
	log.Printf("kyc-onboarding listening on %s\n", listenAddress)
	if serverStartupError := http.ListenAndServe(listenAddress, httplogging.WithRequestLogging(httpRequestMultiplexer)); serverStartupError != nil {
		log.Fatalf("kyc-onboarding failed to start: %v", serverStartupError)
	}
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
