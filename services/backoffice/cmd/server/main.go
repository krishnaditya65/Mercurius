// Mercurius / backoffice
//
// See FEATURES.md §14 for the full scope: KYC review queue, manual order
// intervention, account freeze/unfreeze, corporate-actions processing,
// support ticket integration. As of this build, account freeze/unfreeze
// is real (internal/accountcontrol) and oms-gateway genuinely gates order
// submission on it — see its internal/backofficeclient. Everything else
// in FEATURES.md §14 is still a TODO.
package main

import (
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"os"

	"mercurius/backoffice/internal/accountcontrol"
	"mercurius/backoffice/internal/httplogging"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	accountFreezeStateMachine := accountcontrol.NewAccountFreezeStateMachine()

	httpRequestMultiplexer := http.NewServeMux()
	httpRequestMultiplexer.HandleFunc("/health", func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = responseWriter.Write([]byte(`{"status":"ok","service":"backoffice"}`))
	})
	httpRequestMultiplexer.HandleFunc("/accounts/freeze", buildFreezeHandler(accountFreezeStateMachine))
	httpRequestMultiplexer.HandleFunc("/accounts/unfreeze", buildUnfreezeHandler(accountFreezeStateMachine))
	httpRequestMultiplexer.HandleFunc("/accounts/freeze-status", buildFreezeStatusHandler(accountFreezeStateMachine))

	listenAddress := ":8084"
	log.Printf("backoffice listening on %s\n", listenAddress)
	if serverStartupError := http.ListenAndServe(listenAddress, httplogging.WithRequestLogging(httpRequestMultiplexer)); serverStartupError != nil {
		log.Fatalf("backoffice failed to start: %v", serverStartupError)
	}
}

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
