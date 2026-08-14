package insurancecrosssell

import (
	"testing"
	"time"
)

// TestGetQuoteTermLifeHandWorked: TERM_LIFE base rate 0.10%, age loading
// 0.01%/year. age=30 -> annualRatePercent = 0.10 + 0.01*30 = 0.40%.
// coverage 100000000 (10,00,000.00) -> premium = 100000000 * 0.004 =
// exactly 400000 (4000.00).
func TestGetQuoteTermLifeHandWorked(t *testing.T) {
	client := NewMockInsurancePartnerClient()
	quote, err := client.GetQuote(ProductTypeTermLife, 30, 100000000, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if quote.IllustrativeAnnualPremiumInMinorUnits != 400000 {
		t.Fatalf("expected premium exactly 400000, got %d", quote.IllustrativeAnnualPremiumInMinorUnits)
	}
}

// TestGetQuoteHealthHandWorked: HEALTH base rate 1.00%, age loading
// 0.05%/year. age=40 -> annualRatePercent = 1.00 + 0.05*40 = 3.00%.
// coverage 50000000 (5,00,000.00) -> premium = 50000000 * 0.03 = exactly
// 1500000 (15000.00).
func TestGetQuoteHealthHandWorked(t *testing.T) {
	client := NewMockInsurancePartnerClient()
	quote, err := client.GetQuote(ProductTypeHealth, 40, 50000000, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if quote.IllustrativeAnnualPremiumInMinorUnits != 1500000 {
		t.Fatalf("expected premium exactly 1500000, got %d", quote.IllustrativeAnnualPremiumInMinorUnits)
	}
}

func TestGetQuotePremiumIncreasesWithAge(t *testing.T) {
	client := NewMockInsurancePartnerClient()
	younger, _ := client.GetQuote(ProductTypeTermLife, 25, 100000000, time.Now())
	older, _ := client.GetQuote(ProductTypeTermLife, 55, 100000000, time.Now())
	if older.IllustrativeAnnualPremiumInMinorUnits <= younger.IllustrativeAnnualPremiumInMinorUnits {
		t.Fatal("expected an older applicant's premium to exceed a younger applicant's for the same coverage")
	}
}

func TestGetQuotePremiumScalesLinearlyWithCoverage(t *testing.T) {
	client := NewMockInsurancePartnerClient()
	small, _ := client.GetQuote(ProductTypeTermLife, 30, 10000000, time.Now())
	large, _ := client.GetQuote(ProductTypeTermLife, 30, 100000000, time.Now())
	if large.IllustrativeAnnualPremiumInMinorUnits != small.IllustrativeAnnualPremiumInMinorUnits*10 {
		t.Fatalf("expected premium to scale linearly with coverage, got %d vs %d*10=%d", large.IllustrativeAnnualPremiumInMinorUnits, small.IllustrativeAnnualPremiumInMinorUnits, small.IllustrativeAnnualPremiumInMinorUnits*10)
	}
}

func TestGetQuoteRejectsUnknownProductType(t *testing.T) {
	client := NewMockInsurancePartnerClient()
	if _, err := client.GetQuote("NOT_A_PRODUCT", 30, 100000000, time.Now()); err != ErrInvalidProductType {
		t.Fatalf("expected ErrInvalidProductType, got %v", err)
	}
}

func TestGetQuoteRejectsInvalidAge(t *testing.T) {
	client := NewMockInsurancePartnerClient()
	if _, err := client.GetQuote(ProductTypeTermLife, 0, 100000000, time.Now()); err != ErrInvalidApplicantAge {
		t.Fatalf("expected ErrInvalidApplicantAge for age 0, got %v", err)
	}
	if _, err := client.GetQuote(ProductTypeTermLife, 121, 100000000, time.Now()); err != ErrInvalidApplicantAge {
		t.Fatalf("expected ErrInvalidApplicantAge for age 121, got %v", err)
	}
}

func TestGetQuoteRejectsNonPositiveCoverage(t *testing.T) {
	client := NewMockInsurancePartnerClient()
	if _, err := client.GetQuote(ProductTypeTermLife, 30, 0, time.Now()); err != ErrInvalidCoverageAmount {
		t.Fatalf("expected ErrInvalidCoverageAmount, got %v", err)
	}
}

func TestGetQuoteCarriesPartnerNameAndProductType(t *testing.T) {
	client := NewMockInsurancePartnerClient()
	quote, _ := client.GetQuote(ProductTypeHealth, 30, 100000000, time.Now())
	if quote.PartnerName == "" {
		t.Fatal("expected a non-empty PartnerName")
	}
	if quote.ProductType != ProductTypeHealth {
		t.Fatalf("expected ProductType HEALTH, got %s", quote.ProductType)
	}
}
