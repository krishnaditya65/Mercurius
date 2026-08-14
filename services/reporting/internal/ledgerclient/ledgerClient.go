// Package ledgerclient is reporting's real, read-only HTTP client for
// the ledger service (services/ledger, default :8082). reporting never
// imports ledger's Go code and never posts a journal entry — every call
// here is a plain GET against ledger's genuine already-shipped HTTP API.
//
// HONEST GAP, stated loudly: ledger's InMemoryDoubleEntryLedgerBook
// (internal/doubleentry) keeps every posted JournalEntry in
// postedJournalEntriesInPostOrder, but that field is private and ledger
// exposes NO HTTP endpoint to list/read historical journal entries —
// only a current-balance snapshot (GET /accounts/balance) and the two
// higher-level sub-ledgers that DO expose real per-transaction history
// with timestamps: GET /deposits and GET /withdrawals. Since reporting
// is instructed to only ever call other services' real, already-existing
// HTTP endpoints (never edit their code), a genuine "every journal
// entry, chronologically" ledger statement is not obtainable through
// ledger's current API. ledgerstatement works around this honestly: it
// uses /deposits and /withdrawals for real cash-movement rows, and
// separately derives trade-settlement rows from oms-gateway's real fill
// history (see omsgatewayclient) rather than from ledger's own journal —
// see ledgerstatement's package doc for the full accounting of what is
// and isn't reconciled against ledger's real current balance.
package ledgerclient

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type balanceLookupWireResponse struct {
	AccountIdentifier            string `json:"accountIdentifier"`
	CurrentBalanceInMinorUnits   int64  `json:"currentBalanceInMinorUnits"`
	AvailableBalanceInMinorUnits int64  `json:"availableBalanceInMinorUnits"`
}

// DepositWireFormat mirrors ledger's depositWireResponse.
type DepositWireFormat struct {
	DepositId          string `json:"depositId,omitempty"`
	AccountIdentifier  string `json:"accountIdentifier,omitempty"`
	Method             string `json:"method,omitempty"`
	AmountInMinorUnits int64  `json:"amountInMinorUnits,omitempty"`
	Status             string `json:"status,omitempty"`
	InitiatedAt        string `json:"initiatedAt,omitempty"`
	ConfirmedAt        string `json:"confirmedAt,omitempty"`
}

// WithdrawalWireFormat mirrors ledger's withdrawalRequestWireResponse.
type WithdrawalWireFormat struct {
	WithdrawalId        string `json:"withdrawalId,omitempty"`
	AccountIdentifier   string `json:"accountIdentifier,omitempty"`
	AmountInMinorUnits  int64  `json:"amountInMinorUnits,omitempty"`
	Status              string `json:"status,omitempty"`
	EligibleForPayoutAt string `json:"eligibleForPayoutAt,omitempty"`
}

// LedgerClient is reporting's real HTTP client for the ledger service.
type LedgerClient struct {
	ledgerBaseUrl string
	httpClient    *http.Client
}

func NewLedgerClient(ledgerBaseUrl string) *LedgerClient {
	return &LedgerClient{
		ledgerBaseUrl: ledgerBaseUrl,
		httpClient:    &http.Client{Timeout: 3 * time.Second},
	}
}

// FetchAccountBalance returns ledger's real, authoritative current and
// available balance for one account.
func (client *LedgerClient) FetchAccountBalance(accountIdentifier string) (currentBalanceInMinorUnits int64, availableBalanceInMinorUnits int64, fetchError error) {
	requestUrl := fmt.Sprintf("%s/accounts/balance?accountId=%s", client.ledgerBaseUrl, accountIdentifier)

	httpResponse, requestError := client.httpClient.Get(requestUrl)
	if requestError != nil {
		return 0, 0, fmt.Errorf("could not reach ledger at %s: %w", client.ledgerBaseUrl, requestError)
	}
	defer httpResponse.Body.Close()

	if httpResponse.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("ledger returned HTTP %d for balance of account %s", httpResponse.StatusCode, accountIdentifier)
	}

	var wireResponse balanceLookupWireResponse
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&wireResponse); decodeError != nil {
		return 0, 0, fmt.Errorf("malformed balance response from ledger: %w", decodeError)
	}
	return wireResponse.CurrentBalanceInMinorUnits, wireResponse.AvailableBalanceInMinorUnits, nil
}

// FetchDepositsForAccount returns every real recorded deposit for one
// account, any status, chronological (as ledger itself returns them).
func (client *LedgerClient) FetchDepositsForAccount(accountIdentifier string) ([]DepositWireFormat, error) {
	requestUrl := fmt.Sprintf("%s/deposits?accountId=%s", client.ledgerBaseUrl, accountIdentifier)

	httpResponse, requestError := client.httpClient.Get(requestUrl)
	if requestError != nil {
		return nil, fmt.Errorf("could not reach ledger at %s: %w", client.ledgerBaseUrl, requestError)
	}
	defer httpResponse.Body.Close()

	if httpResponse.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ledger returned HTTP %d for deposits of account %s", httpResponse.StatusCode, accountIdentifier)
	}

	var deposits []DepositWireFormat
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&deposits); decodeError != nil {
		return nil, fmt.Errorf("malformed deposits response from ledger: %w", decodeError)
	}
	return deposits, nil
}

// FetchWithdrawalsForAccount returns every real recorded withdrawal
// request for one account, chronological.
func (client *LedgerClient) FetchWithdrawalsForAccount(accountIdentifier string) ([]WithdrawalWireFormat, error) {
	requestUrl := fmt.Sprintf("%s/withdrawals?accountId=%s", client.ledgerBaseUrl, accountIdentifier)

	httpResponse, requestError := client.httpClient.Get(requestUrl)
	if requestError != nil {
		return nil, fmt.Errorf("could not reach ledger at %s: %w", client.ledgerBaseUrl, requestError)
	}
	defer httpResponse.Body.Close()

	if httpResponse.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ledger returned HTTP %d for withdrawals of account %s", httpResponse.StatusCode, accountIdentifier)
	}

	var withdrawals []WithdrawalWireFormat
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&withdrawals); decodeError != nil {
		return nil, fmt.Errorf("malformed withdrawals response from ledger: %w", decodeError)
	}
	return withdrawals, nil
}
