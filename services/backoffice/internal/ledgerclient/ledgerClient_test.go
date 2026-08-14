package ledgerclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPostCashRewardCreditJournalEntrySendsABalancedDebitCreditPair(t *testing.T) {
	var capturedRequest postJournalEntryWireRequest

	fakeLedgerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/journal-entries" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&capturedRequest)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(postJournalEntryWireResponse{WasJournalEntryPosted: true})
	}))
	defer fakeLedgerServer.Close()

	client := NewLedgerClient(fakeLedgerServer.URL)
	postError := client.PostCashRewardCreditJournalEntry("acct-referred", 10_000, "referral reward")

	if postError != nil {
		t.Fatalf("expected successful post, got: %v", postError)
	}

	if len(capturedRequest.DebitLines) != 1 || capturedRequest.DebitLines[0].LedgerAccountIdentifier != "acct-referred" || capturedRequest.DebitLines[0].AmountInMinorUnits != 10_000 {
		t.Fatalf("expected a single debit line crediting acct-referred 10000, got %+v", capturedRequest.DebitLines)
	}
	if len(capturedRequest.CreditLines) != 1 || capturedRequest.CreditLines[0].LedgerAccountIdentifier != firmClearingAccountIdentifier || capturedRequest.CreditLines[0].AmountInMinorUnits != 10_000 {
		t.Fatalf("expected a single credit line against firm-clearing-acct for 10000, got %+v", capturedRequest.CreditLines)
	}
}

func TestPostCashRewardCreditJournalEntryPropagatesLedgerRejection(t *testing.T) {
	fakeLedgerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(postJournalEntryWireResponse{WasJournalEntryPosted: false, ErrorMessage: "unknown ledger account"})
	}))
	defer fakeLedgerServer.Close()

	client := NewLedgerClient(fakeLedgerServer.URL)
	postError := client.PostCashRewardCreditJournalEntry("acct-unknown", 5_000, "referral reward")

	if postError == nil {
		t.Fatalf("expected an error when the ledger rejects the entry")
	}
}
