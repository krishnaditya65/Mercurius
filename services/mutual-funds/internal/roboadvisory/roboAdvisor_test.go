package roboadvisory

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

const floatTolerance = 1e-9

func floatsAlmostEqual(a, b float64) bool {
	return math.Abs(a-b) < floatTolerance
}

var allRiskCategories = []RiskCategory{
	CategoryConservative,
	CategoryModeratelyConservative,
	CategoryModerate,
	CategoryModeratelyAggressive,
	CategoryAggressive,
}

func TestRecommendAllocationRejectsUnknownRiskCategory(t *testing.T) {
	_, err := RecommendAllocation(context.Background(), "NOT_A_REAL_CATEGORY", nil)
	if err != ErrUnknownRiskCategory {
		t.Errorf("expected ErrUnknownRiskCategory, got %v", err)
	}
}

func TestModelAllocationsSumTo100ForEveryCategory(t *testing.T) {
	for _, category := range allRiskCategories {
		recommendation, err := RecommendAllocation(context.Background(), category, nil)
		if err != nil {
			t.Fatalf("unexpected error for %s: %v", category, err)
		}
		total := recommendation.Allocation.EquityPercent + recommendation.Allocation.DebtPercent + recommendation.Allocation.HybridPercent
		if !floatsAlmostEqual(total, 100.0) {
			t.Errorf("expected %s allocation to sum to 100, got %v (equity=%v debt=%v hybrid=%v)",
				category, total, recommendation.Allocation.EquityPercent, recommendation.Allocation.DebtPercent, recommendation.Allocation.HybridPercent)
		}
	}
}

// TestConservativeAllocationExactPercentages and
// TestAggressiveAllocationExactPercentages are the hand-worked examples:
// the illustrative table is defined as CONSERVATIVE -> 20/70/10 and
// AGGRESSIVE -> 80/10/10 (see the package doc comment and README), so
// these assert the table wasn't accidentally transposed or mistyped.
func TestConservativeAllocationExactPercentages(t *testing.T) {
	recommendation, err := RecommendAllocation(context.Background(), CategoryConservative, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if recommendation.Allocation.EquityPercent != 20 || recommendation.Allocation.DebtPercent != 70 || recommendation.Allocation.HybridPercent != 10 {
		t.Errorf("expected CONSERVATIVE 20/70/10, got %+v", recommendation.Allocation)
	}
}

func TestAggressiveAllocationExactPercentages(t *testing.T) {
	recommendation, err := RecommendAllocation(context.Background(), CategoryAggressive, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if recommendation.Allocation.EquityPercent != 80 || recommendation.Allocation.DebtPercent != 10 || recommendation.Allocation.HybridPercent != 10 {
		t.Errorf("expected AGGRESSIVE 80/10/10, got %+v", recommendation.Allocation)
	}
}

func TestModerateAllocationExactPercentages(t *testing.T) {
	recommendation, err := RecommendAllocation(context.Background(), CategoryModerate, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if recommendation.Allocation.EquityPercent != 50 || recommendation.Allocation.DebtPercent != 40 || recommendation.Allocation.HybridPercent != 10 {
		t.Errorf("expected MODERATE 50/40/10, got %+v", recommendation.Allocation)
	}
}

func TestAllocationEquityIncreasesMonotonicallyWithRiskCategory(t *testing.T) {
	var previousEquityPercent float64 = -1
	for _, category := range allRiskCategories { // already ordered least -> most aggressive
		recommendation, err := RecommendAllocation(context.Background(), category, nil)
		if err != nil {
			t.Fatalf("unexpected error for %s: %v", category, err)
		}
		if recommendation.Allocation.EquityPercent <= previousEquityPercent {
			t.Errorf("expected equity percent to strictly increase from %s onward, got %v after %v", category, recommendation.Allocation.EquityPercent, previousEquityPercent)
		}
		previousEquityPercent = recommendation.Allocation.EquityPercent
	}
}

func TestGenerateIllustrativeReturnSeriesLength(t *testing.T) {
	series := GenerateIllustrativeReturnSeries(ModelAllocation{EquityPercent: 50, DebtPercent: 40, HybridPercent: 10})
	if len(series) != 12 {
		t.Errorf("expected a 12-period (monthly) series, got %d", len(series))
	}
}

func TestGenerateIllustrativeReturnSeriesScalesWithEquityWeight(t *testing.T) {
	conservativeSeries := GenerateIllustrativeReturnSeries(illustrativeModelAllocationsByRiskCategory[CategoryConservative])
	aggressiveSeries := GenerateIllustrativeReturnSeries(illustrativeModelAllocationsByRiskCategory[CategoryAggressive])

	for i := range conservativeSeries {
		if math.Abs(aggressiveSeries[i]) <= math.Abs(conservativeSeries[i]) {
			t.Fatalf("expected AGGRESSIVE's period-%d swing to be larger in magnitude than CONSERVATIVE's: aggressive=%v conservative=%v", i, aggressiveSeries[i], conservativeSeries[i])
		}
	}

	// Hand-worked: period 0's base pattern value is 0.020. CONSERVATIVE is
	// 20% equity -> volatilityMultiplier = 0.25 + 0.20 = 0.45 -> 0.020 *
	// 0.45 = 0.009. AGGRESSIVE is 80% equity -> multiplier = 0.25 + 0.80 =
	// 1.05 -> 0.020 * 1.05 = 0.021.
	if !floatsAlmostEqual(conservativeSeries[0], 0.009) {
		t.Errorf("expected CONSERVATIVE period-0 return 0.009, got %v", conservativeSeries[0])
	}
	if !floatsAlmostEqual(aggressiveSeries[0], 0.021) {
		t.Errorf("expected AGGRESSIVE period-0 return 0.021, got %v", aggressiveSeries[0])
	}
}

func TestGenerateIllustrativeReturnSeriesIsDeterministic(t *testing.T) {
	allocation := ModelAllocation{EquityPercent: 65, DebtPercent: 25, HybridPercent: 10}
	first := GenerateIllustrativeReturnSeries(allocation)
	second := GenerateIllustrativeReturnSeries(allocation)
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("expected deterministic output, period %d differed: %v vs %v", i, first[i], second[i])
		}
	}
}

// TestFetchRiskStatisticsHandWorkedExample stubs quant-engine's
// POST /risk/statistics with the response for the return series
// [0.02, -0.01, 0.03, -0.02], hand-worked as follows (population-stddev
// convention, matching quant-engine's riskStatistics.py):
//
//	mean = (0.02 - 0.01 + 0.03 - 0.02) / 4 = 0.005
//	deviations from mean: 0.015, -0.015, 0.025, -0.025
//	sum of squared deviations = 0.000225+0.000225+0.000625+0.000625 = 0.0017
//	population variance = 0.0017 / 4 = 0.000425
//	population stddev = sqrt(0.000425) = 0.0206155281...
//	Sharpe (periodsPerYear=12, riskFreeRate=0) =
//	  (0.005 / 0.0206155281) * sqrt(12) = 0.8401680504...
//
//	downside deviations (below 0): -0.01 -> -0.01, -0.02 -> -0.02, others 0
//	sum of squares = 0.0001 + 0.0004 = 0.0005, /4 = 0.000125
//	downside deviation = sqrt(0.000125) = 0.0111803399...
//	Sortino = (0.005 / 0.0111803399) * sqrt(12) = 1.5491933385...
//
//	equity curve (start 1.0): 1.0, 1.02, 1.0098, 1.040094, 1.01929212
//	running peak reaches 1.040094 at index 3, trough after that is
//	1.01929212 at index 4 -> max drawdown = (1.040094-1.01929212)/1.040094
//	= 0.02 exactly (to float precision)
func TestFetchRiskStatisticsHandWorkedExample(t *testing.T) {
	const expectedSharpe = 0.8401680504168056
	const expectedSortino = 1.5491933384829664
	const expectedMaxDrawdown = 0.020000000000000046
	const expectedPeak = 1.040094
	const expectedTrough = 1.01929212

	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var requestBody riskStatisticsWireRequest
		if decodeError := json.NewDecoder(r.Body).Decode(&requestBody); decodeError != nil {
			t.Fatalf("failed to decode request body: %v", decodeError)
		}
		expectedReturns := []float64{0.02, -0.01, 0.03, -0.02}
		if len(requestBody.PeriodicReturns) != len(expectedReturns) {
			t.Fatalf("expected %d periodic returns, got %d", len(expectedReturns), len(requestBody.PeriodicReturns))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(riskStatisticsWireResponse{
			AnnualizedSharpeRatio:            expectedSharpe,
			AnnualizedSortinoRatio:           expectedSortino,
			MaximumDrawdownFraction:          expectedMaxDrawdown,
			MaximumDrawdownPeakEquityValue:   expectedPeak,
			MaximumDrawdownTroughEquityValue: expectedTrough,
		})
	}))
	defer testServer.Close()

	client := NewQuantEngineClient(testServer.URL)
	stats, err := client.FetchRiskStatistics(context.Background(), []float64{0.02, -0.01, 0.03, -0.02})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !floatsAlmostEqual(stats.AnnualizedSharpeRatio, expectedSharpe) {
		t.Errorf("expected Sharpe %v, got %v", expectedSharpe, stats.AnnualizedSharpeRatio)
	}
	if !floatsAlmostEqual(stats.AnnualizedSortinoRatio, expectedSortino) {
		t.Errorf("expected Sortino %v, got %v", expectedSortino, stats.AnnualizedSortinoRatio)
	}
	if !floatsAlmostEqual(stats.MaximumDrawdownFraction, expectedMaxDrawdown) {
		t.Errorf("expected max drawdown %v, got %v", expectedMaxDrawdown, stats.MaximumDrawdownFraction)
	}
}

func TestFetchRiskStatisticsSurfacesUnreachableError(t *testing.T) {
	client := NewQuantEngineClient("http://127.0.0.1:1")
	_, err := client.FetchRiskStatistics(context.Background(), []float64{0.01, -0.01})
	if err == nil {
		t.Fatal("expected an error calling an unreachable quant-engine, got nil")
	}
}

func TestFetchRiskStatisticsSurfacesNon200Error(t *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(riskStatisticsWireResponse{ErrorMessage: "zero variance return series"})
	}))
	defer testServer.Close()

	client := NewQuantEngineClient(testServer.URL)
	_, err := client.FetchRiskStatistics(context.Background(), []float64{0.0, 0.0})
	if err == nil {
		t.Fatal("expected an error for a non-200 quant-engine response, got nil")
	}
}

func TestRecommendAllocationWithNilQuantEngineClientSetsError(t *testing.T) {
	recommendation, err := RecommendAllocation(context.Background(), CategoryModerate, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if recommendation.RiskStatistics != nil {
		t.Errorf("expected nil RiskStatistics with no quant-engine client, got %+v", recommendation.RiskStatistics)
	}
	if recommendation.RiskStatisticsError == "" {
		t.Errorf("expected a non-empty RiskStatisticsError explaining why no statistics were fetched")
	}
}

func TestRecommendAllocationWithReachableQuantEngineClientAttachesStatistics(t *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(riskStatisticsWireResponse{
			AnnualizedSharpeRatio:  1.23,
			AnnualizedSortinoRatio: 1.5,
		})
	}))
	defer testServer.Close()

	client := NewQuantEngineClient(testServer.URL)
	recommendation, err := RecommendAllocation(context.Background(), CategoryAggressive, client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if recommendation.RiskStatistics == nil {
		t.Fatal("expected non-nil RiskStatistics from a reachable quant-engine")
	}
	if recommendation.RiskStatisticsError != "" {
		t.Errorf("expected empty RiskStatisticsError, got %q", recommendation.RiskStatisticsError)
	}
	if !floatsAlmostEqual(recommendation.RiskStatistics.AnnualizedSharpeRatio, 1.23) {
		t.Errorf("expected Sharpe 1.23, got %v", recommendation.RiskStatistics.AnnualizedSharpeRatio)
	}
}

func TestRecommendAllocationRiskStatisticsErrorDoesNotFailRecommend(t *testing.T) {
	client := NewQuantEngineClient("http://127.0.0.1:1")
	recommendation, err := RecommendAllocation(context.Background(), CategoryModerate, client)
	if err != nil {
		t.Fatalf("expected Recommend to succeed even with an unreachable quant-engine, got error: %v", err)
	}
	if recommendation.RiskStatistics != nil {
		t.Errorf("expected nil RiskStatistics when quant-engine is unreachable")
	}
	if recommendation.RiskStatisticsError == "" {
		t.Errorf("expected a non-empty RiskStatisticsError")
	}
	if recommendation.Allocation.EquityPercent != 50 {
		t.Errorf("expected the allocation itself to still be correct despite the quant-engine failure")
	}
}
