package kycclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchKycStatusParsesEligibleResponse(t *testing.T) {
	fakeKycServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("accountId") != "acct-001" {
			t.Fatalf("unexpected accountId: %s", r.URL.Query().Get("accountId"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(KycStatusWireResponse{
			AccountIdentifier:       "acct-001",
			KycVerificationStage:    "VERIFIED",
			IsEligibleToPlaceOrders: true,
		})
	}))
	defer fakeKycServer.Close()

	client := NewKycClient(fakeKycServer.URL)
	status, fetchError := client.FetchKycStatus("acct-001")

	if fetchError != nil {
		t.Fatalf("expected successful fetch, got: %v", fetchError)
	}
	if !status.IsEligibleToPlaceOrders {
		t.Fatal("expected account to be eligible")
	}
}

func TestFetchKycStatusParsesIneligibleResponseWithoutAGoError(t *testing.T) {
	fakeKycServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(KycStatusWireResponse{
			AccountIdentifier:       "acct-002",
			KycVerificationStage:    "NOT_SUBMITTED",
			IsEligibleToPlaceOrders: false,
		})
	}))
	defer fakeKycServer.Close()

	client := NewKycClient(fakeKycServer.URL)
	status, fetchError := client.FetchKycStatus("acct-002")

	if fetchError != nil {
		t.Fatalf("an ineligible-but-successfully-answered status must not be a Go error, got: %v", fetchError)
	}
	if status.IsEligibleToPlaceOrders {
		t.Fatal("expected account to be ineligible")
	}
}

func TestFetchKycStatusReturnsErrorWhenUnreachable(t *testing.T) {
	client := NewKycClient("http://127.0.0.1:1") // reserved, never listening

	_, fetchError := client.FetchKycStatus("acct-001")

	if fetchError == nil {
		t.Fatal("expected an error when kyc-onboarding is unreachable")
	}
}
