package insurancecrosssell

import (
	"testing"
	"time"
)

func newTestService() *Service {
	return NewService(NewMockInsurancePartnerClient())
}

func TestRequestQuoteReturnsAndStoresQuote(t *testing.T) {
	service := newTestService()
	quote, err := service.RequestQuote(ProductTypeTermLife, 30, 100000000, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found, wasFound := service.LookupQuote(quote.QuoteId)
	if !wasFound {
		t.Fatal("expected the quote to be findable after RequestQuote")
	}
	if found.QuoteId != quote.QuoteId {
		t.Fatal("expected the found quote to match the requested quote")
	}
}

func TestRequestQuotePropagatesPartnerClientError(t *testing.T) {
	service := newTestService()
	if _, err := service.RequestQuote("NOT_A_PRODUCT", 30, 100000000, time.Now()); err != ErrInvalidProductType {
		t.Fatalf("expected ErrInvalidProductType to propagate, got %v", err)
	}
}

func TestRegisterInterestSucceedsForKnownQuote(t *testing.T) {
	service := newTestService()
	quote, _ := service.RequestQuote(ProductTypeHealth, 40, 50000000, time.Now())

	lead, err := service.RegisterInterest("acct-1", quote.QuoteId, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lead.Status != LeadStatusRegistered {
		t.Fatalf("expected REGISTERED, got %s", lead.Status)
	}
	if lead.QuoteId != quote.QuoteId {
		t.Fatalf("expected lead to reference the quoted quoteId")
	}
}

func TestRegisterInterestRejectsUnknownQuote(t *testing.T) {
	service := newTestService()
	if _, err := service.RegisterInterest("acct-1", "no-such-quote", time.Now()); err != ErrUnknownQuote {
		t.Fatalf("expected ErrUnknownQuote, got %v", err)
	}
}

func TestLeadsForAccountReturnsOnlyThatAccountSortedByRegisteredAt(t *testing.T) {
	service := newTestService()
	now := time.Now()
	quoteA, _ := service.RequestQuote(ProductTypeTermLife, 30, 100000000, now)
	quoteB, _ := service.RequestQuote(ProductTypeHealth, 40, 50000000, now)

	service.RegisterInterest("acct-1", quoteA.QuoteId, now)
	service.RegisterInterest("acct-2", quoteB.QuoteId, now)
	service.RegisterInterest("acct-1", quoteB.QuoteId, now.Add(time.Hour))

	leads := service.LeadsForAccount("acct-1")
	if len(leads) != 2 {
		t.Fatalf("expected 2 leads for acct-1, got %d", len(leads))
	}
	if leads[0].RegisteredAt.After(leads[1].RegisteredAt) {
		t.Fatal("expected leads sorted by RegisteredAt")
	}
}

func TestLeadsForAccountUnknownAccountReturnsEmpty(t *testing.T) {
	service := newTestService()
	leads := service.LeadsForAccount("no-such-account")
	if len(leads) != 0 {
		t.Fatalf("expected empty slice, got %d", len(leads))
	}
}

func TestLookupQuoteUnknownReturnsFalse(t *testing.T) {
	service := newTestService()
	if _, wasFound := service.LookupQuote("no-such-quote"); wasFound {
		t.Fatal("expected not to find an unknown quote")
	}
}

// fakePartnerClient lets tests simulate a real partner outage/error
// without depending on MockInsurancePartnerClient's own validation.
type fakePartnerClient struct {
	err error
}

func (fake *fakePartnerClient) GetQuote(productType ProductType, applicantAge int, coverageAmountInMinorUnits int64, now time.Time) (Quote, error) {
	if fake.err != nil {
		return Quote{}, fake.err
	}
	return Quote{QuoteId: "fake-quote", ProductType: productType, QuotedAt: now}, nil
}

func TestRequestQuoteWorksAgainstAnyPartnerClientImplementation(t *testing.T) {
	service := NewService(&fakePartnerClient{})
	quote, err := service.RequestQuote(ProductTypeTermLife, 30, 100000000, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if quote.QuoteId != "fake-quote" {
		t.Fatalf("expected the fake partner's quote id to pass through, got %s", quote.QuoteId)
	}
}
