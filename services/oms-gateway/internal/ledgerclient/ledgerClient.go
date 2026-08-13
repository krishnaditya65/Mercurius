// Package ledgerclient is oms-gateway's HTTP client for the `ledger`
// service: fetching an account's authoritative starting balance and
// posting the settlement journal entry for a fill.
//
// TODO(real build): this is synchronous request/response, called inline
// from the order-submission request path for PostTradeSettlementJournalEntry
// (see cmd/server/main.go) — acceptable for a skeleton proving the
// boundary, but ARCHITECTURE.md's Tier 1/Tier 2 split says settlement
// should be posted asynchronously off the hot path, not synchronously
// inside the HTTP handler that also risk-checked and routed the order.
package ledgerclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type JournalEntryLineWireFormat struct {
	LedgerAccountIdentifier string `json:"ledgerAccountIdentifier"`
	AmountInMinorUnits      int64  `json:"amountInMinorUnits"`
}

type PostJournalEntryWireRequest struct {
	HumanReadableDescription string                       `json:"humanReadableDescription"`
	DebitLines               []JournalEntryLineWireFormat `json:"debitLines"`
	CreditLines              []JournalEntryLineWireFormat `json:"creditLines"`
}

type PostJournalEntryWireResponse struct {
	WasJournalEntryPosted bool   `json:"wasJournalEntryPosted"`
	ErrorMessage          string `json:"errorMessage,omitempty"`
}

type balanceLookupWireResponse struct {
	AccountIdentifier          string `json:"accountIdentifier"`
	CurrentBalanceInMinorUnits int64  `json:"currentBalanceInMinorUnits"`
}

// LedgerClient is oms-gateway's HTTP client for the ledger service. Named
// "clearing account" below refers to the same pass-through account the
// ledger service itself seeds (`firm-clearing-acct`) — see
// doubleEntryLedgerCore's convention notes for why a single clearing
// account nets to zero across a balanced buyer/seller settlement entry.
type LedgerClient struct {
	ledgerBaseUrl string
	httpClient    *http.Client
}

func NewLedgerClient(ledgerBaseUrl string) *LedgerClient {
	return &LedgerClient{
		ledgerBaseUrl: ledgerBaseUrl,
		httpClient:    &http.Client{Timeout: 2 * time.Second},
	}
}

// FetchAccountBalance returns the ledger's authoritative current balance
// for one account. Used at startup to seed the risk engine's cache
// instead of hardcoding it (see cmd/server/main.go).
func (client *LedgerClient) FetchAccountBalance(accountIdentifier string) (int64, error) {
	requestUrl := fmt.Sprintf("%s/accounts/balance?accountId=%s", client.ledgerBaseUrl, accountIdentifier)

	httpResponse, requestError := client.httpClient.Get(requestUrl)
	if requestError != nil {
		return 0, fmt.Errorf("could not reach ledger at %s: %w", client.ledgerBaseUrl, requestError)
	}
	defer httpResponse.Body.Close()

	if httpResponse.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("ledger returned HTTP %d for account %s", httpResponse.StatusCode, accountIdentifier)
	}

	var wireResponse balanceLookupWireResponse
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&wireResponse); decodeError != nil {
		return 0, fmt.Errorf("malformed balance response from ledger: %w", decodeError)
	}

	return wireResponse.CurrentBalanceInMinorUnits, nil
}

// PostTradeSettlementJournalEntry posts one balanced journal entry for a
// fill: the buyer's cash account is debited (decreased, per this ledger's
// uniform debit/credit convention — see doubleEntryLedgerCore.go) by the
// executed notional, the seller's is credited (increased) by the same
// amount, routed through `firm-clearing-acct` as a net-zero pass-through
// so the entry balances (2 debit lines summing to the same total as 2
// credit lines) without needing per-side special-casing.
func (client *LedgerClient) PostTradeSettlementJournalEntry(
	buyingClientAccountId string,
	sellingClientAccountId string,
	executedNotionalValueInMinorUnits int64,
	assignedGlobalSequenceNumber uint64,
) error {
	wireRequest := PostJournalEntryWireRequest{
		HumanReadableDescription: fmt.Sprintf("trade settlement for sequence %d", assignedGlobalSequenceNumber),
		DebitLines: []JournalEntryLineWireFormat{
			{LedgerAccountIdentifier: "firm-clearing-acct", AmountInMinorUnits: executedNotionalValueInMinorUnits},
			{LedgerAccountIdentifier: sellingClientAccountId, AmountInMinorUnits: executedNotionalValueInMinorUnits},
		},
		CreditLines: []JournalEntryLineWireFormat{
			{LedgerAccountIdentifier: buyingClientAccountId, AmountInMinorUnits: executedNotionalValueInMinorUnits},
			{LedgerAccountIdentifier: "firm-clearing-acct", AmountInMinorUnits: executedNotionalValueInMinorUnits},
		},
	}

	requestBodyBytes, marshalError := json.Marshal(wireRequest)
	if marshalError != nil {
		return fmt.Errorf("failed to marshal journal entry: %w", marshalError)
	}

	httpResponse, requestError := client.httpClient.Post(
		client.ledgerBaseUrl+"/journal-entries",
		"application/json",
		bytes.NewReader(requestBodyBytes),
	)
	if requestError != nil {
		return fmt.Errorf("could not reach ledger at %s: %w", client.ledgerBaseUrl, requestError)
	}
	defer httpResponse.Body.Close()

	var wireResponse PostJournalEntryWireResponse
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&wireResponse); decodeError != nil {
		return fmt.Errorf("malformed response from ledger: %w", decodeError)
	}
	if !wireResponse.WasJournalEntryPosted {
		return fmt.Errorf("ledger rejected the settlement entry: %s", wireResponse.ErrorMessage)
	}

	return nil
}
