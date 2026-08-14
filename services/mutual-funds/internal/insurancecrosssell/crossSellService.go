package insurancecrosssell

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"
)

// LeadStatus is the real, minimal state of a registered interest lead:
// REGISTERED is terminal here — this platform's job stops at handing the
// lead to the partner insurer; anything past that (application,
// underwriting, policy issuance) happens entirely on the partner's side,
// outside this repo.
type LeadStatus string

const LeadStatusRegistered LeadStatus = "REGISTERED"

// Lead is one account's registered interest in a Quote.
type Lead struct {
	LeadId            string
	AccountIdentifier string
	QuoteId           string
	Status            LeadStatus
	RegisteredAt      time.Time
}

var ErrUnknownQuote = fmt.Errorf("no such quote exists — request a fresh quote before registering interest")

// Service is the thin orchestration layer this platform owns: it calls
// PartnerClient for quotes and keeps a local record of which quotes exist
// (so RegisterInterest can validate a quoteId) and which accounts have
// registered interest in which quote. It does NOT underwrite, price, or
// issue anything — see the package doc comment.
type Service struct {
	partnerClient PartnerClient

	mutexGuardingState sync.Mutex
	quotesById         map[string]Quote
	leadsById          map[string]*Lead
}

// NewService builds a service against partnerClient.
func NewService(partnerClient PartnerClient) *Service {
	return &Service{
		partnerClient: partnerClient,
		quotesById:    make(map[string]Quote),
		leadsById:     make(map[string]*Lead),
	}
}

// RequestQuote calls out to the partner insurer (via PartnerClient) for an
// illustrative quote and records it locally so a later RegisterInterest
// call can reference it.
func (service *Service) RequestQuote(productType ProductType, applicantAge int, coverageAmountInMinorUnits int64, now time.Time) (Quote, error) {
	quote, quoteError := service.partnerClient.GetQuote(productType, applicantAge, coverageAmountInMinorUnits, now)
	if quoteError != nil {
		return Quote{}, quoteError
	}

	service.mutexGuardingState.Lock()
	service.quotesById[quote.QuoteId] = quote
	service.mutexGuardingState.Unlock()

	return quote, nil
}

// RegisterInterest records accountIdentifier's interest in a previously
// quoted quoteId — the ENTIRE scope of what this platform does with an
// insurance lead; everything past this point (application form,
// underwriting, policy issuance, premium collection) is the partner
// insurer's real, separately regulated process, not this repo's.
func (service *Service) RegisterInterest(accountIdentifier string, quoteId string, now time.Time) (*Lead, error) {
	service.mutexGuardingState.Lock()
	defer service.mutexGuardingState.Unlock()

	if _, wasFound := service.quotesById[quoteId]; !wasFound {
		return nil, ErrUnknownQuote
	}

	leadId, genError := generateLeadId()
	if genError != nil {
		return nil, fmt.Errorf("failed to generate lead id: %w", genError)
	}

	lead := &Lead{
		LeadId:            leadId,
		AccountIdentifier: accountIdentifier,
		QuoteId:           quoteId,
		Status:            LeadStatusRegistered,
		RegisteredAt:      now,
	}
	service.leadsById[leadId] = lead

	return lead, nil
}

// LookupQuote returns a previously requested quote, or false if quoteId
// isn't known.
func (service *Service) LookupQuote(quoteId string) (Quote, bool) {
	service.mutexGuardingState.Lock()
	defer service.mutexGuardingState.Unlock()

	quote, wasFound := service.quotesById[quoteId]
	return quote, wasFound
}

// LeadsForAccount returns every lead accountIdentifier has registered,
// sorted by RegisteredAt.
func (service *Service) LeadsForAccount(accountIdentifier string) []*Lead {
	service.mutexGuardingState.Lock()
	defer service.mutexGuardingState.Unlock()

	matching := make([]*Lead, 0)
	for _, lead := range service.leadsById {
		if lead.AccountIdentifier == accountIdentifier {
			matching = append(matching, lead)
		}
	}
	sort.Slice(matching, func(i, j int) bool { return matching[i].RegisteredAt.Before(matching[j].RegisteredAt) })
	return matching
}

func generateLeadId() (string, error) {
	randomBytes := make([]byte, 8)
	if _, readError := rand.Read(randomBytes); readError != nil {
		return "", readError
	}
	return "ins-lead-" + hex.EncodeToString(randomBytes), nil
}
