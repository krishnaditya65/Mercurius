// Package kycclient is oms-gateway's HTTP client for kyc-onboarding:
// checking whether an account is eligible to trade before an order is
// ever risk-checked or routed.
//
// TODO(real build): called synchronously inline on the order-submission
// path (see cmd/server/main.go) — same caveat as ledgerclient's balance
// fetch: a real build caches KYC eligibility locally and refreshes it
// asynchronously, it doesn't call out to another service on every single
// order.
package kycclient

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type KycStatusWireResponse struct {
	AccountIdentifier       string `json:"accountIdentifier"`
	KycVerificationStage    string `json:"kycVerificationStage"`
	IsEligibleToPlaceOrders bool   `json:"isEligibleToPlaceOrders"`
	RejectionReason         string `json:"rejectionReason,omitempty"`
}

type KycClient struct {
	kycOnboardingBaseUrl string
	httpClient           *http.Client
}

func NewKycClient(kycOnboardingBaseUrl string) *KycClient {
	return &KycClient{
		kycOnboardingBaseUrl: kycOnboardingBaseUrl,
		httpClient:           &http.Client{Timeout: 2 * time.Second},
	}
}

// FetchKycStatus returns the account's current KYC status. A transport
// failure (kyc-onboarding unreachable, timeout, malformed response)
// comes back as a Go error; a successfully-answered "not eligible" comes
// back as a normal response with IsEligibleToPlaceOrders=false — the
// caller decides how to treat each case, and (per cmd/server/main.go)
// treats them very differently: a transport failure fails open with a
// warning logged, an explicit ineligibility fails closed.
func (client *KycClient) FetchKycStatus(accountIdentifier string) (*KycStatusWireResponse, error) {
	requestUrl := fmt.Sprintf("%s/kyc/status?accountId=%s", client.kycOnboardingBaseUrl, url.QueryEscape(accountIdentifier))

	httpResponse, requestError := client.httpClient.Get(requestUrl)
	if requestError != nil {
		return nil, fmt.Errorf("could not reach kyc-onboarding at %s: %w", client.kycOnboardingBaseUrl, requestError)
	}
	defer httpResponse.Body.Close()

	if httpResponse.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kyc-onboarding returned HTTP %d for account %s", httpResponse.StatusCode, accountIdentifier)
	}

	var wireResponse KycStatusWireResponse
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&wireResponse); decodeError != nil {
		return nil, fmt.Errorf("malformed KYC status response: %w", decodeError)
	}

	return &wireResponse, nil
}
