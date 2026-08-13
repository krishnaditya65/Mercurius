package backofficeclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchFreezeStatusParsesNotFrozenResponse(t *testing.T) {
	fakeBackofficeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(FreezeStatusWireResponse{AccountIdentifier: "acct-001", IsFrozen: false})
	}))
	defer fakeBackofficeServer.Close()

	client := NewBackofficeClient(fakeBackofficeServer.URL)
	status, fetchError := client.FetchFreezeStatus("acct-001")

	if fetchError != nil {
		t.Fatalf("expected successful fetch, got: %v", fetchError)
	}
	if status.IsFrozen {
		t.Fatal("expected account to not be frozen")
	}
}

func TestFetchFreezeStatusParsesFrozenResponseWithReason(t *testing.T) {
	fakeBackofficeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(FreezeStatusWireResponse{
			AccountIdentifier: "acct-001",
			IsFrozen:          true,
			FreezeReason:      "suspected AML flag",
		})
	}))
	defer fakeBackofficeServer.Close()

	client := NewBackofficeClient(fakeBackofficeServer.URL)
	status, fetchError := client.FetchFreezeStatus("acct-001")

	if fetchError != nil {
		t.Fatalf("a frozen-but-successfully-answered status must not be a Go error, got: %v", fetchError)
	}
	if !status.IsFrozen || status.FreezeReason != "suspected AML flag" {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestFetchFreezeStatusReturnsErrorWhenUnreachable(t *testing.T) {
	client := NewBackofficeClient("http://127.0.0.1:1")

	_, fetchError := client.FetchFreezeStatus("acct-001")

	if fetchError == nil {
		t.Fatal("expected an error when backoffice is unreachable")
	}
}
