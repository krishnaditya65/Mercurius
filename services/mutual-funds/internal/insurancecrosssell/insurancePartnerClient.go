// Package insurancecrosssell is a THIN, HONEST integration stub for
// cross-selling term life and health insurance — FEATURES.md §17, "Wealth
// & Product Breadth", a `[P4]` item ("Insurance cross-sell (term/health) —
// separate regulated entity, integrated via API only").
//
// LOUD CAVEAT: this package deliberately does NOT contain an insurance
// underwriting engine. A real insurer is a separate, independently
// regulated legal entity (e.g. in India, an IRDAI-licensed insurer) — this
// platform can only ever call OUT to a real partner insurer's quote API
// and register a lead/interest, never underwrite or price a policy
// itself. PartnerClient models exactly that boundary: an interface this
// platform calls, with MockInsurancePartnerClient standing in for a real
// partner's API using a simple, illustrative, hand-picked premium formula
// — NOT a real actuarial rate table, NOT medical underwriting, and NOT
// connected to any real insurer anywhere. A real build would swap
// MockInsurancePartnerClient for an actual HTTP client calling a real
// partner insurer's real quote endpoint; everything downstream of
// PartnerClient (Service, Lead) would stay the same.
package insurancecrosssell

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// ProductType is the illustrative insurance product line being quoted.
type ProductType string

const (
	ProductTypeTermLife ProductType = "TERM_LIFE"
	ProductTypeHealth   ProductType = "HEALTH"
)

// productRate bundles one ProductType's illustrative, hand-picked flat
// premium-rate formula: annualRatePercent = BaseRatePercent +
// AgeLoadingPercentPerYear * applicantAge. NOT a real actuarial table —
// real underwriting depends on medical history, occupation, smoking
// status, sum-assured tier, and dozens of other real risk factors this
// stub does not model at all.
type productRate struct {
	BaseRatePercent          float64
	AgeLoadingPercentPerYear float64
}

var productRates = map[ProductType]productRate{
	ProductTypeTermLife: {BaseRatePercent: 0.10, AgeLoadingPercentPerYear: 0.01},
	ProductTypeHealth:   {BaseRatePercent: 1.00, AgeLoadingPercentPerYear: 0.05},
}

var ErrInvalidProductType = fmt.Errorf("unrecognized insurance product type")
var ErrInvalidApplicantAge = fmt.Errorf("applicant age must be between 1 and 120")
var ErrInvalidCoverageAmount = fmt.Errorf("coverage amount must be strictly positive")

// Quote is one illustrative quote returned by a (real or mock) partner
// insurer's API.
type Quote struct {
	QuoteId                               string
	ProductType                           ProductType
	ApplicantAge                          int
	CoverageAmountInMinorUnits            int64
	IllustrativeAnnualPremiumInMinorUnits int64
	PartnerName                           string
	QuotedAt                              time.Time
}

// PartnerClient is the boundary this platform calls OUT across to reach a
// separate, independently regulated insurer. See the package doc comment.
type PartnerClient interface {
	GetQuote(productType ProductType, applicantAge int, coverageAmountInMinorUnits int64, now time.Time) (Quote, error)
}

// MockInsurancePartnerClient stands in for a real partner insurer's quote
// API using the illustrative flat-rate formula in productRates. LOUD
// CAVEAT: this is a fixture, not a real insurer — see the package doc
// comment.
type MockInsurancePartnerClient struct {
	PartnerName string
}

// NewMockInsurancePartnerClient returns a mock partner client under an
// illustrative, entirely fictitious partner name.
func NewMockInsurancePartnerClient() *MockInsurancePartnerClient {
	return &MockInsurancePartnerClient{PartnerName: "Illustrative Partner Assurance Co. (fixture, not real)"}
}

// GetQuote computes an illustrative annual premium as
// coverageAmountInMinorUnits * annualRatePercent/100, where
// annualRatePercent = BaseRatePercent + AgeLoadingPercentPerYear *
// applicantAge.
func (client *MockInsurancePartnerClient) GetQuote(productType ProductType, applicantAge int, coverageAmountInMinorUnits int64, now time.Time) (Quote, error) {
	rate, wasKnownType := productRates[productType]
	if !wasKnownType {
		return Quote{}, ErrInvalidProductType
	}
	if applicantAge < 1 || applicantAge > 120 {
		return Quote{}, ErrInvalidApplicantAge
	}
	if coverageAmountInMinorUnits <= 0 {
		return Quote{}, ErrInvalidCoverageAmount
	}

	annualRatePercent := rate.BaseRatePercent + rate.AgeLoadingPercentPerYear*float64(applicantAge)
	premium := int64(float64(coverageAmountInMinorUnits) * annualRatePercent / 100)

	quoteId, genError := generateQuoteId()
	if genError != nil {
		return Quote{}, fmt.Errorf("failed to generate quote id: %w", genError)
	}

	return Quote{
		QuoteId:                               quoteId,
		ProductType:                           productType,
		ApplicantAge:                          applicantAge,
		CoverageAmountInMinorUnits:            coverageAmountInMinorUnits,
		IllustrativeAnnualPremiumInMinorUnits: premium,
		PartnerName:                           client.PartnerName,
		QuotedAt:                              now,
	}, nil
}

func generateQuoteId() (string, error) {
	randomBytes := make([]byte, 8)
	if _, readError := rand.Read(randomBytes); readError != nil {
		return "", readError
	}
	return "ins-quote-" + hex.EncodeToString(randomBytes), nil
}
