package ledgerclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchAccountBalance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/balance" || r.URL.Query().Get("accountId") != "acct-001" {
			t.Errorf("unexpected request: %s", r.URL.String())
		}
		_ = json.NewEncoder(w).Encode(balanceLookupWireResponse{
			AccountIdentifier:            "acct-001",
			CurrentBalanceInMinorUnits:   1000000,
			AvailableBalanceInMinorUnits: 900000,
		})
	}))
	defer server.Close()

	client := NewLedgerClient(server.URL)
	current, available, err := client.FetchAccountBalance("acct-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if current != 1000000 || available != 900000 {
		t.Fatalf("unexpected balances: current=%d available=%d", current, available)
	}
}

func TestFetchAccountBalanceReturnsErrorOnNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewLedgerClient(server.URL)
	if _, _, err := client.FetchAccountBalance("acct-999"); err == nil {
		t.Fatal("expected an error for a 404 response")
	}
}

func TestFetchDepositsForAccount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]DepositWireFormat{
			{DepositId: "dep-1", AccountIdentifier: "acct-001", AmountInMinorUnits: 500000, Status: "CONFIRMED", InitiatedAt: "2025-01-01T00:00:00Z"},
		})
	}))
	defer server.Close()

	client := NewLedgerClient(server.URL)
	deposits, err := client.FetchDepositsForAccount("acct-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deposits) != 1 || deposits[0].AmountInMinorUnits != 500000 {
		t.Fatalf("unexpected deposits: %+v", deposits)
	}
}

func TestFetchWithdrawalsForAccount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]WithdrawalWireFormat{
			{WithdrawalId: "wd-1", AccountIdentifier: "acct-001", AmountInMinorUnits: 200000, Status: "COMPLETED"},
		})
	}))
	defer server.Close()

	client := NewLedgerClient(server.URL)
	withdrawals, err := client.FetchWithdrawalsForAccount("acct-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(withdrawals) != 1 || withdrawals[0].AmountInMinorUnits != 200000 {
		t.Fatalf("unexpected withdrawals: %+v", withdrawals)
	}
}
