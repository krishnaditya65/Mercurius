// Package backofficeclient is oms-gateway's HTTP client for backoffice's
// account freeze/unfreeze feature (FEATURES.md §14) — checking whether an
// account has been frozen by compliance/support before an order is
// routed.
//
// TODO(real build): same caveat as kycclient/ledgerclient — synchronous,
// inline on the order-submission path. A real build caches freeze status
// locally and invalidates it on a freeze/unfreeze event, it doesn't call
// out to another service on every order.
package backofficeclient

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type FreezeStatusWireResponse struct {
	AccountIdentifier string `json:"accountIdentifier"`
	IsFrozen          bool   `json:"isFrozen"`
	FreezeReason      string `json:"freezeReason,omitempty"`
}

type BackofficeClient struct {
	backofficeBaseUrl string
	httpClient        *http.Client
}

func NewBackofficeClient(backofficeBaseUrl string) *BackofficeClient {
	return &BackofficeClient{
		backofficeBaseUrl: backofficeBaseUrl,
		httpClient:        &http.Client{Timeout: 2 * time.Second},
	}
}

// FetchFreezeStatus mirrors kycclient.FetchKycStatus's error-handling
// contract: a transport failure is a Go error, a successfully-answered
// "not frozen" is a normal response.
func (client *BackofficeClient) FetchFreezeStatus(accountIdentifier string) (*FreezeStatusWireResponse, error) {
	requestUrl := fmt.Sprintf("%s/accounts/freeze-status?accountId=%s", client.backofficeBaseUrl, url.QueryEscape(accountIdentifier))

	httpResponse, requestError := client.httpClient.Get(requestUrl)
	if requestError != nil {
		return nil, fmt.Errorf("could not reach backoffice at %s: %w", client.backofficeBaseUrl, requestError)
	}
	defer httpResponse.Body.Close()

	if httpResponse.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("backoffice returned HTTP %d for account %s", httpResponse.StatusCode, accountIdentifier)
	}

	var wireResponse FreezeStatusWireResponse
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&wireResponse); decodeError != nil {
		return nil, fmt.Errorf("malformed freeze status response: %w", decodeError)
	}

	return &wireResponse, nil
}
