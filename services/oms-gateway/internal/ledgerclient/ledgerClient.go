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

// PostMarginFundingDisbursementJournalEntry posts a real balanced journal
// entry disbursing a margin-funding cash advance to an account: per
// doubleEntryLedgerCore's convention ("debit increases the named
// account"), the account is DEBITED (increased — mirrors how
// PostTradeSettlementJournalEntry debits the selling account, the other
// side that receives cash, above) by `amountInMinorUnits`, and the
// margin-funding clearing account is CREDITED (decreased) by the same
// amount — see internal/marginfunding's package doc for the full
// FEATURES.md §2 context. This is genuinely REAL cash movement through
// the same ledger every trade settles through, not a local bookkeeping
// fiction.
func (client *LedgerClient) PostMarginFundingDisbursementJournalEntry(
	clientAccountIdentifier string,
	amountInMinorUnits int64,
	humanReadableDescription string,
) error {
	return client.postSingleAccountJournalEntry(clientAccountIdentifier, amountInMinorUnits, humanReadableDescription, true)
}

// PostMarginFundingRepaymentJournalEntry posts the reverse of
// PostMarginFundingDisbursementJournalEntry: the account is CREDITED
// (decreased) by `amountInMinorUnits` and the margin-funding clearing
// account is DEBITED (increased) by the same amount, paying down real
// principal. Not yet wired to an HTTP endpoint (see internal/
// marginfunding's package doc gap §3) but exercised directly by this
// package's own tests.
func (client *LedgerClient) PostMarginFundingRepaymentJournalEntry(
	clientAccountIdentifier string,
	amountInMinorUnits int64,
	humanReadableDescription string,
) error {
	return client.postSingleAccountJournalEntry(clientAccountIdentifier, amountInMinorUnits, humanReadableDescription, false)
}

// PostDividendCreditJournalEntry posts a real balanced journal entry
// crediting an account's cash for a dividend payout — FEATURES.md §17's
// DRIP feature (internal/drip). Mechanically identical to
// PostMarginFundingDisbursementJournalEntry (the client account is
// DEBITED/increased, the clearing account CREDITED/decreased — real cash
// arriving in the client's account, exactly like a disbursement), reusing
// the same postSingleAccountJournalEntry machinery and the same
// firm-clearing-acct clearing account this package already uses
// everywhere else — kept as its own named method (not a bare alias) so a
// reader of internal/drip's call site sees an honest, dividend-specific
// name rather than a margin-funding one.
func (client *LedgerClient) PostDividendCreditJournalEntry(
	clientAccountIdentifier string,
	amountInMinorUnits int64,
	humanReadableDescription string,
) error {
	return client.postSingleAccountJournalEntry(clientAccountIdentifier, amountInMinorUnits, humanReadableDescription, true)
}

// PostLoanAgainstSecuritiesDisbursementJournalEntry posts a real balanced
// journal entry disbursing a Loan Against Securities (LAS) cash advance —
// FEATURES.md §17's LAS product (internal/loanagainstsecurities).
// Mechanically identical to PostMarginFundingDisbursementJournalEntry
// (client account DEBITED/increased, clearing account CREDITED/decreased
// — real cash arriving in the client's account) but kept as its own named
// method, exactly like PostDividendCreditJournalEntry above, so a reader
// of internal/loanagainstsecurities' call site sees an honest,
// LAS-specific name rather than a margin-funding one — LAS is a distinct,
// longer-tenure loan PRODUCT even though the underlying ledger mechanics
// (and the reused firm-clearing-acct clearing account — see
// marginFundingClearingAccountIdentifier's own gap note below) are the
// same shape.
func (client *LedgerClient) PostLoanAgainstSecuritiesDisbursementJournalEntry(
	clientAccountIdentifier string,
	amountInMinorUnits int64,
	humanReadableDescription string,
) error {
	return client.postSingleAccountJournalEntry(clientAccountIdentifier, amountInMinorUnits, humanReadableDescription, true)
}

// PostLoanAgainstSecuritiesRepaymentJournalEntry posts the reverse of
// PostLoanAgainstSecuritiesDisbursementJournalEntry: the client account is
// CREDITED (decreased) and the clearing account DEBITED (increased),
// paying down real LAS principal.
func (client *LedgerClient) PostLoanAgainstSecuritiesRepaymentJournalEntry(
	clientAccountIdentifier string,
	amountInMinorUnits int64,
	humanReadableDescription string,
) error {
	return client.postSingleAccountJournalEntry(clientAccountIdentifier, amountInMinorUnits, humanReadableDescription, false)
}

// marginFundingClearingAccountIdentifier mirrors
// marginfunding.FirmMarginFundingClearingAccountIdentifier — duplicated
// as a plain string constant here (rather than importing internal/
// marginfunding) to keep ledgerclient decoupled from that package, the
// same decoupling convention every other internal package here follows.
// See that constant's doc comment for the honest gap on why this reuses
// firm-clearing-acct instead of a dedicated margin-funding account.
const marginFundingClearingAccountIdentifier = "firm-clearing-acct"

// postSingleAccountJournalEntry posts a balanced two-line journal entry
// moving `amountInMinorUnits` between `clientAccountIdentifier` and
// marginFundingClearingAccountIdentifier. Per doubleEntryLedgerCore's
// uniform convention ("debit increases the named account, credit
// decreases it" — see that package's PostJournalEntry comment): when
// creditClientAccountFalseMeansDebit is true this is a DISBURSEMENT — the
// client account is DEBITED (increased) and the clearing account
// CREDITED (decreased), the exact same debit/credit assignment
// PostTradeSettlementJournalEntry already uses for the account on the
// receiving side of cash (there: the seller; here: the borrowing
// client). When false, this is a REPAYMENT and the assignment reverses.
func (client *LedgerClient) postSingleAccountJournalEntry(
	clientAccountIdentifier string,
	amountInMinorUnits int64,
	humanReadableDescription string,
	isDisbursementNotRepayment bool,
) error {
	wireRequest := PostJournalEntryWireRequest{
		HumanReadableDescription: humanReadableDescription,
	}
	if isDisbursementNotRepayment {
		wireRequest.DebitLines = []JournalEntryLineWireFormat{
			{LedgerAccountIdentifier: clientAccountIdentifier, AmountInMinorUnits: amountInMinorUnits},
		}
		wireRequest.CreditLines = []JournalEntryLineWireFormat{
			{LedgerAccountIdentifier: marginFundingClearingAccountIdentifier, AmountInMinorUnits: amountInMinorUnits},
		}
	} else {
		wireRequest.DebitLines = []JournalEntryLineWireFormat{
			{LedgerAccountIdentifier: marginFundingClearingAccountIdentifier, AmountInMinorUnits: amountInMinorUnits},
		}
		wireRequest.CreditLines = []JournalEntryLineWireFormat{
			{LedgerAccountIdentifier: clientAccountIdentifier, AmountInMinorUnits: amountInMinorUnits},
		}
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
		return fmt.Errorf("ledger rejected the journal entry: %s", wireResponse.ErrorMessage)
	}

	return nil
}
