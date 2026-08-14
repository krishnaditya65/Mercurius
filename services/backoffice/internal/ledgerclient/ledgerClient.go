// Package ledgerclient is backoffice's own small HTTP client for the
// `ledger` service — used exclusively by internal/referralrewards to
// post a real cash-reward credit when a referral genuinely qualifies.
//
// This is deliberately a much smaller client than oms-gateway's own
// internal/ledgerclient (same package name, different module, same
// convention every other client-package pair in this repo already
// follows — e.g. oms-gateway's internal/backofficeclient vs.
// backoffice's own internal — kept decoupled rather than importing
// across service module boundaries) — it implements exactly the one
// operation backoffice needs: crediting real cash into a client
// account via a balanced journal entry through
// firm-clearing-acct, mirroring oms-gateway's
// ledgerclient.PostDividendCreditJournalEntry's exact debit/credit
// assignment (client account DEBITED/increased per doubleEntryLedgerCore's
// "debit increases the named account" convention, clearing account
// CREDITED/decreased).
package ledgerclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type journalEntryLineWireFormat struct {
	LedgerAccountIdentifier string `json:"ledgerAccountIdentifier"`
	AmountInMinorUnits      int64  `json:"amountInMinorUnits"`
}

type postJournalEntryWireRequest struct {
	HumanReadableDescription string                       `json:"humanReadableDescription"`
	DebitLines               []journalEntryLineWireFormat `json:"debitLines"`
	CreditLines              []journalEntryLineWireFormat `json:"creditLines"`
}

type postJournalEntryWireResponse struct {
	WasJournalEntryPosted bool   `json:"wasJournalEntryPosted"`
	ErrorMessage          string `json:"errorMessage,omitempty"`
}

// firmClearingAccountIdentifier mirrors the same "firm-clearing-acct"
// pass-through account every other real cash-movement client in this
// repo (oms-gateway's ledgerclient, its own doc explains why) routes
// through so a single-account credit still balances as a two-line
// journal entry.
const firmClearingAccountIdentifier = "firm-clearing-acct"

// LedgerClient is backoffice's real HTTP client for the ledger service.
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

// PostCashRewardCreditJournalEntry posts a real, balanced journal entry
// crediting an account's cash for a referral (or any other real-cash)
// reward — the client account is DEBITED (increased) by
// amountInMinorUnits and firm-clearing-acct is CREDITED (decreased) by
// the same amount, exactly like every other disbursement-shaped journal
// entry in this repo.
func (client *LedgerClient) PostCashRewardCreditJournalEntry(clientAccountIdentifier string, amountInMinorUnits int64, humanReadableDescription string) error {
	wireRequest := postJournalEntryWireRequest{
		HumanReadableDescription: humanReadableDescription,
		DebitLines: []journalEntryLineWireFormat{
			{LedgerAccountIdentifier: clientAccountIdentifier, AmountInMinorUnits: amountInMinorUnits},
		},
		CreditLines: []journalEntryLineWireFormat{
			{LedgerAccountIdentifier: firmClearingAccountIdentifier, AmountInMinorUnits: amountInMinorUnits},
		},
	}

	requestBodyBytes, marshalError := json.Marshal(wireRequest)
	if marshalError != nil {
		return fmt.Errorf("failed to marshal reward journal entry: %w", marshalError)
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

	var wireResponse postJournalEntryWireResponse
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&wireResponse); decodeError != nil {
		return fmt.Errorf("malformed response from ledger: %w", decodeError)
	}
	if !wireResponse.WasJournalEntryPosted {
		return fmt.Errorf("ledger rejected the reward journal entry: %s", wireResponse.ErrorMessage)
	}

	return nil
}
