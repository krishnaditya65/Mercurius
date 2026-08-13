package ledgerclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchAccountBalanceParsesLedgerResponse(t *testing.T) {
	fakeLedgerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("accountId") != "acct-001" {
			t.Fatalf("unexpected accountId: %s", r.URL.Query().Get("accountId"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(balanceLookupWireResponse{
			AccountIdentifier:          "acct-001",
			CurrentBalanceInMinorUnits: 750_000,
		})
	}))
	defer fakeLedgerServer.Close()

	client := NewLedgerClient(fakeLedgerServer.URL)
	balance, fetchError := client.FetchAccountBalance("acct-001")

	if fetchError != nil {
		t.Fatalf("expected successful fetch, got: %v", fetchError)
	}
	if balance != 750_000 {
		t.Fatalf("expected balance 750000, got %d", balance)
	}
}

func TestPostTradeSettlementJournalEntrySendsABalancedRequest(t *testing.T) {
	var capturedRequest PostJournalEntryWireRequest

	fakeLedgerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/journal-entries" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&capturedRequest)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(PostJournalEntryWireResponse{WasJournalEntryPosted: true})
	}))
	defer fakeLedgerServer.Close()

	client := NewLedgerClient(fakeLedgerServer.URL)
	postError := client.PostTradeSettlementJournalEntry("buyer-acct", "seller-acct", 40_000, 7)

	if postError != nil {
		t.Fatalf("expected successful post, got: %v", postError)
	}

	sumOfDebits := int64(0)
	for _, line := range capturedRequest.DebitLines {
		sumOfDebits += line.AmountInMinorUnits
	}
	sumOfCredits := int64(0)
	for _, line := range capturedRequest.CreditLines {
		sumOfCredits += line.AmountInMinorUnits
	}
	if sumOfDebits != sumOfCredits {
		t.Fatalf("settlement entry must balance: debits=%d credits=%d", sumOfDebits, sumOfCredits)
	}
}

func TestPostTradeSettlementJournalEntrySurfacesLedgerRejection(t *testing.T) {
	fakeLedgerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(PostJournalEntryWireResponse{
			WasJournalEntryPosted: false,
			ErrorMessage:          "referenced ledger account does not exist: buyer-acct",
		})
	}))
	defer fakeLedgerServer.Close()

	client := NewLedgerClient(fakeLedgerServer.URL)
	postError := client.PostTradeSettlementJournalEntry("buyer-acct", "seller-acct", 1_000, 1)

	if postError == nil {
		t.Fatal("expected an error when the ledger rejects the entry")
	}
}
