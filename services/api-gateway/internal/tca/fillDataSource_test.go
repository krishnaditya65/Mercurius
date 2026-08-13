package tca

import (
	"errors"
	"testing"
)

func TestFetchLiveFilledOrdersReturnsTheDocumentedGapErrorHonestly(t *testing.T) {
	_, err := FetchLiveFilledOrders("http://127.0.0.1:8081", "acct-1")
	if !errors.Is(err, ErrLiveFillHistoryNotYetAvailable) {
		t.Fatalf("expected ErrLiveFillHistoryNotYetAvailable, got %v", err)
	}
}

func TestFixtureFilledOrdersForAccountReturnsNonEmptyRealisticData(t *testing.T) {
	orders := FixtureFilledOrdersForAccount("acct-1")
	if len(orders) == 0 {
		t.Fatalf("expected non-empty fixture order set")
	}
	for _, order := range orders {
		if order.AccountIdentifier != "acct-1" {
			t.Fatalf("expected every fixture order to be tagged with the requested account, got %q", order.AccountIdentifier)
		}
		if order.OrderQuantity == 0 {
			t.Fatalf("expected every fixture order to have a positive quantity")
		}
	}
}

func TestFixtureFilledOrdersFeedRealMathThroughBuildAccountReport(t *testing.T) {
	report := BuildAccountReport("acct-1", FixtureFilledOrdersForAccount("acct-1"))
	if report.OrderCount != len(FixtureFilledOrdersForAccount("acct-1")) {
		t.Fatalf("expected every fixture order to produce a metrics entry")
	}
}
