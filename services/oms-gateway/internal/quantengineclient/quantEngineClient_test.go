package quantengineclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPriceOptionContractParsesSuccessfulResponse(t *testing.T) {
	var capturedRequest OptionPricingRequest

	fakeQuantEngineServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/options/price" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&capturedRequest)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(OptionPricingResponse{
			TheoreticalPriceInMinorUnits:      10.45,
			Delta:                             0.6368,
			Gamma:                             0.0187,
			VegaPerOnePercentVolatilityChange: 0.3752,
			ThetaPerCalendarDay:               -0.0177,
		})
	}))
	defer fakeQuantEngineServer.Close()

	client := NewQuantEngineClient(fakeQuantEngineServer.URL)
	response, err := client.PriceOptionContract(OptionPricingRequest{
		UnderlyingSpotPrice:            100,
		OptionStrikePrice:              100,
		AnnualizedRiskFreeInterestRate: 0.05,
		AnnualizedVolatility:           0.2,
		TimeToExpiryInYears:            1.0,
		IsCallOptionNotPut:             true,
	})
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if response.TheoreticalPriceInMinorUnits != 10.45 {
		t.Errorf("expected theoretical price 10.45, got %v", response.TheoreticalPriceInMinorUnits)
	}
	if !capturedRequest.IsCallOptionNotPut {
		t.Errorf("expected IsCallOptionNotPut true to be sent through")
	}
}

func TestPriceOptionContractReturnsErrorWhenUnreachable(t *testing.T) {
	client := NewQuantEngineClient("http://127.0.0.1:1")
	_, err := client.PriceOptionContract(OptionPricingRequest{})
	if err == nil {
		t.Fatal("expected an error when quant-engine is unreachable")
	}
}

func TestPriceOptionContractSurfacesBusinessLevelRejection(t *testing.T) {
	fakeQuantEngineServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(OptionPricingResponse{ErrorMessage: "timeToExpiryInYears must be positive"})
	}))
	defer fakeQuantEngineServer.Close()

	client := NewQuantEngineClient(fakeQuantEngineServer.URL)
	_, err := client.PriceOptionContract(OptionPricingRequest{})
	if err == nil {
		t.Fatal("expected an error for a business-level rejection")
	}
}

func TestPriceOptionContractReturnsErrorOnMalformedResponse(t *testing.T) {
	fakeQuantEngineServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	}))
	defer fakeQuantEngineServer.Close()

	client := NewQuantEngineClient(fakeQuantEngineServer.URL)
	_, err := client.PriceOptionContract(OptionPricingRequest{})
	if err == nil {
		t.Fatal("expected an error for a malformed response")
	}
}
