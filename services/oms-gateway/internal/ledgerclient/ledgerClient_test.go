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

// Per doubleEntryLedgerCore's convention ("debit increases the named
// account, credit decreases it"), a disbursement that INCREASES the
// client's balance must DEBIT the client account, not credit it.

func TestPostMarginFundingDisbursementDebitsClientAccountFromClearingAccount(t *testing.T) {
	var capturedRequest PostJournalEntryWireRequest

	fakeLedgerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedRequest)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(PostJournalEntryWireResponse{WasJournalEntryPosted: true})
	}))
	defer fakeLedgerServer.Close()

	client := NewLedgerClient(fakeLedgerServer.URL)
	postError := client.PostMarginFundingDisbursementJournalEntry("acct-001", 50_000, "margin funding disbursement")

	if postError != nil {
		t.Fatalf("expected successful post, got: %v", postError)
	}
	if len(capturedRequest.DebitLines) != 1 || capturedRequest.DebitLines[0].LedgerAccountIdentifier != "acct-001" || capturedRequest.DebitLines[0].AmountInMinorUnits != 50_000 {
		t.Fatalf("expected client account debited (increased) 50000, got %+v", capturedRequest.DebitLines)
	}
	if len(capturedRequest.CreditLines) != 1 || capturedRequest.CreditLines[0].LedgerAccountIdentifier != marginFundingClearingAccountIdentifier {
		t.Fatalf("expected clearing account credited (decreased), got %+v", capturedRequest.CreditLines)
	}
}

func TestPostMarginFundingRepaymentCreditsClientAccountToClearingAccount(t *testing.T) {
	var capturedRequest PostJournalEntryWireRequest

	fakeLedgerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedRequest)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(PostJournalEntryWireResponse{WasJournalEntryPosted: true})
	}))
	defer fakeLedgerServer.Close()

	client := NewLedgerClient(fakeLedgerServer.URL)
	postError := client.PostMarginFundingRepaymentJournalEntry("acct-001", 20_000, "margin funding repayment")

	if postError != nil {
		t.Fatalf("expected successful post, got: %v", postError)
	}
	if len(capturedRequest.CreditLines) != 1 || capturedRequest.CreditLines[0].LedgerAccountIdentifier != "acct-001" || capturedRequest.CreditLines[0].AmountInMinorUnits != 20_000 {
		t.Fatalf("expected client account credited (decreased) 20000, got %+v", capturedRequest.CreditLines)
	}
	if len(capturedRequest.DebitLines) != 1 || capturedRequest.DebitLines[0].LedgerAccountIdentifier != marginFundingClearingAccountIdentifier {
		t.Fatalf("expected clearing account debited (increased), got %+v", capturedRequest.DebitLines)
	}
}

func TestPostMarginFundingDisbursementSurfacesLedgerRejection(t *testing.T) {
	fakeLedgerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(PostJournalEntryWireResponse{
			WasJournalEntryPosted: false,
			ErrorMessage:          "referenced ledger account does not exist: acct-001",
		})
	}))
	defer fakeLedgerServer.Close()

	client := NewLedgerClient(fakeLedgerServer.URL)
	postError := client.PostMarginFundingDisbursementJournalEntry("acct-001", 1_000, "test")

	if postError == nil {
		t.Fatal("expected an error when the ledger rejects the entry")
	}
}

func TestPostDividendCreditDebitsClientAccountFromClearingAccount(t *testing.T) {
	var capturedRequest PostJournalEntryWireRequest

	fakeLedgerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedRequest)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(PostJournalEntryWireResponse{WasJournalEntryPosted: true})
	}))
	defer fakeLedgerServer.Close()

	client := NewLedgerClient(fakeLedgerServer.URL)
	postError := client.PostDividendCreditJournalEntry("acct-001", 1_500, "dividend credit for DEMO-EQ")

	if postError != nil {
		t.Fatalf("expected successful post, got: %v", postError)
	}
	if len(capturedRequest.DebitLines) != 1 || capturedRequest.DebitLines[0].LedgerAccountIdentifier != "acct-001" || capturedRequest.DebitLines[0].AmountInMinorUnits != 1_500 {
		t.Fatalf("expected client account debited (increased) 1500, got %+v", capturedRequest.DebitLines)
	}
	if len(capturedRequest.CreditLines) != 1 || capturedRequest.CreditLines[0].LedgerAccountIdentifier != marginFundingClearingAccountIdentifier {
		t.Fatalf("expected clearing account credited (decreased), got %+v", capturedRequest.CreditLines)
	}
}

func TestPostDividendCreditSurfacesLedgerRejection(t *testing.T) {
	fakeLedgerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(PostJournalEntryWireResponse{
			WasJournalEntryPosted: false,
			ErrorMessage:          "referenced ledger account does not exist: acct-001",
		})
	}))
	defer fakeLedgerServer.Close()

	client := NewLedgerClient(fakeLedgerServer.URL)
	postError := client.PostDividendCreditJournalEntry("acct-001", 1_000, "test")

	if postError == nil {
		t.Fatal("expected an error when the ledger rejects the entry")
	}
}

func TestPostLoanAgainstSecuritiesDisbursementDebitsClientAccountFromClearingAccount(t *testing.T) {
	var capturedRequest PostJournalEntryWireRequest

	fakeLedgerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedRequest)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(PostJournalEntryWireResponse{WasJournalEntryPosted: true})
	}))
	defer fakeLedgerServer.Close()

	client := NewLedgerClient(fakeLedgerServer.URL)
	postError := client.PostLoanAgainstSecuritiesDisbursementJournalEntry("acct-001", 75_000, "LAS disbursement")

	if postError != nil {
		t.Fatalf("expected successful post, got: %v", postError)
	}
	if len(capturedRequest.DebitLines) != 1 || capturedRequest.DebitLines[0].LedgerAccountIdentifier != "acct-001" || capturedRequest.DebitLines[0].AmountInMinorUnits != 75_000 {
		t.Fatalf("expected client account debited (increased) 75000, got %+v", capturedRequest.DebitLines)
	}
}

func TestPostLoanAgainstSecuritiesRepaymentCreditsClientAccountToClearingAccount(t *testing.T) {
	var capturedRequest PostJournalEntryWireRequest

	fakeLedgerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedRequest)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(PostJournalEntryWireResponse{WasJournalEntryPosted: true})
	}))
	defer fakeLedgerServer.Close()

	client := NewLedgerClient(fakeLedgerServer.URL)
	postError := client.PostLoanAgainstSecuritiesRepaymentJournalEntry("acct-001", 25_000, "LAS repayment")

	if postError != nil {
		t.Fatalf("expected successful post, got: %v", postError)
	}
	if len(capturedRequest.CreditLines) != 1 || capturedRequest.CreditLines[0].LedgerAccountIdentifier != "acct-001" || capturedRequest.CreditLines[0].AmountInMinorUnits != 25_000 {
		t.Fatalf("expected client account credited (decreased) 25000, got %+v", capturedRequest.CreditLines)
	}
}

func TestPostLoanAgainstSecuritiesDisbursementSurfacesLedgerRejection(t *testing.T) {
	fakeLedgerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(PostJournalEntryWireResponse{
			WasJournalEntryPosted: false,
			ErrorMessage:          "referenced ledger account does not exist: acct-001",
		})
	}))
	defer fakeLedgerServer.Close()

	client := NewLedgerClient(fakeLedgerServer.URL)
	postError := client.PostLoanAgainstSecuritiesDisbursementJournalEntry("acct-001", 1_000, "test")

	if postError == nil {
		t.Fatal("expected an error when the ledger rejects the entry")
	}
}
