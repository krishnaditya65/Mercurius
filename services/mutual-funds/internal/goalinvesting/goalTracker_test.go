package goalinvesting

import (
	"testing"
	"time"

	"mercurius/mutualFunds/internal/amcrouting"
	"mercurius/mutualFunds/internal/fundcatalog"
)

const schemeA = "MF-EQ-BLUECHIP001"
const schemeB = "MF-EQ-MIDCAP002"

func newTestTracker(t *testing.T) (*GoalTracker, *fundcatalog.FundCatalog, *amcrouting.AmcOrderRouter) {
	t.Helper()
	catalog := fundcatalog.NewFundCatalog()
	router := amcrouting.NewAmcOrderRouter(catalog, 0)
	return NewGoalTracker(catalog, router), catalog, router
}

// buyAndConfirm places and immediately confirms a purchase order so the
// account ends up with a known unit holding priced at nav.
func buyAndConfirm(t *testing.T, catalog *fundcatalog.FundCatalog, router *amcrouting.AmcOrderRouter, accountIdentifier string, schemeId string, nav int64, amountInMinorUnits int64, now time.Time) {
	t.Helper()
	if err := catalog.UpdateNav(schemeId, nav); err != nil {
		t.Fatalf("failed to set NAV: %v", err)
	}
	if _, err := router.PlacePurchaseOrder(accountIdentifier, schemeId, amountInMinorUnits, now); err != nil {
		t.Fatalf("failed to place purchase order: %v", err)
	}
	confirmed, failed := router.ConfirmDueOrders(now)
	if len(failed) != 0 || len(confirmed) != 1 {
		t.Fatalf("expected the purchase order to confirm cleanly, confirmed=%d failed=%v", len(confirmed), failed)
	}
}

func TestCreateGoalRejectsMissingAccountIdentifier(t *testing.T) {
	tracker, _, _ := newTestTracker(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := tracker.CreateGoal("", "Retirement", GoalTypeRetirement, 1000000, now.AddDate(1, 0, 0), []string{schemeA}, 10000, now)
	if err != ErrMissingAccountIdentifier {
		t.Errorf("expected ErrMissingAccountIdentifier, got %v", err)
	}
}

func TestCreateGoalRejectsEmptyName(t *testing.T) {
	tracker, _, _ := newTestTracker(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := tracker.CreateGoal("acct-1", "", GoalTypeRetirement, 1000000, now.AddDate(1, 0, 0), []string{schemeA}, 10000, now)
	if err != ErrEmptyGoalName {
		t.Errorf("expected ErrEmptyGoalName, got %v", err)
	}
}

func TestCreateGoalRejectsUnsupportedGoalType(t *testing.T) {
	tracker, _, _ := newTestTracker(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := tracker.CreateGoal("acct-1", "Vacation", "VACATION", 1000000, now.AddDate(1, 0, 0), []string{schemeA}, 10000, now)
	if err != ErrUnsupportedGoalType {
		t.Errorf("expected ErrUnsupportedGoalType, got %v", err)
	}
}

func TestCreateGoalRejectsNonPositiveTargetAmount(t *testing.T) {
	tracker, _, _ := newTestTracker(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := tracker.CreateGoal("acct-1", "Retirement", GoalTypeRetirement, 0, now.AddDate(1, 0, 0), []string{schemeA}, 10000, now)
	if err != ErrInvalidTargetAmount {
		t.Errorf("expected ErrInvalidTargetAmount, got %v", err)
	}
}

func TestCreateGoalRejectsTargetDateNotInFuture(t *testing.T) {
	tracker, _, _ := newTestTracker(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := tracker.CreateGoal("acct-1", "Retirement", GoalTypeRetirement, 1000000, now, []string{schemeA}, 10000, now)
	if err != ErrTargetDateNotInFuture {
		t.Errorf("expected ErrTargetDateNotInFuture, got %v", err)
	}
}

func TestCreateGoalRejectsNoLinkedSchemes(t *testing.T) {
	tracker, _, _ := newTestTracker(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := tracker.CreateGoal("acct-1", "Retirement", GoalTypeRetirement, 1000000, now.AddDate(1, 0, 0), nil, 10000, now)
	if err != ErrNoLinkedSchemes {
		t.Errorf("expected ErrNoLinkedSchemes, got %v", err)
	}
}

func TestCreateGoalRejectsUnknownScheme(t *testing.T) {
	tracker, _, _ := newTestTracker(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := tracker.CreateGoal("acct-1", "Retirement", GoalTypeRetirement, 1000000, now.AddDate(1, 0, 0), []string{"MF-DOES-NOT-EXIST"}, 10000, now)
	if err != ErrUnknownScheme {
		t.Errorf("expected ErrUnknownScheme, got %v", err)
	}
}

func TestCreateGoalRejectsNegativeContribution(t *testing.T) {
	tracker, _, _ := newTestTracker(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := tracker.CreateGoal("acct-1", "Retirement", GoalTypeRetirement, 1000000, now.AddDate(1, 0, 0), []string{schemeA}, -1, now)
	if err != ErrNegativeContribution {
		t.Errorf("expected ErrNegativeContribution, got %v", err)
	}
}

func TestCreateGoalSucceedsAndSortsLinkedSchemeIds(t *testing.T) {
	tracker, _, _ := newTestTracker(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	goal, err := tracker.CreateGoal("acct-1", "Retirement", GoalTypeRetirement, 1000000, now.AddDate(1, 0, 0), []string{schemeB, schemeA}, 10000, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(goal.LinkedSchemeIds) != 2 || goal.LinkedSchemeIds[0] != schemeA || goal.LinkedSchemeIds[1] != schemeB {
		t.Errorf("expected LinkedSchemeIds sorted [%s %s], got %v", schemeA, schemeB, goal.LinkedSchemeIds)
	}
}

func TestCalculateProgressRejectsUnknownGoal(t *testing.T) {
	tracker, _, _ := newTestTracker(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := tracker.CalculateProgress("no-such-goal", now)
	if err != ErrGoalNotFound {
		t.Errorf("expected ErrGoalNotFound, got %v", err)
	}
}

func TestCalculateProgressWithNoHoldingsIsZeroProgress(t *testing.T) {
	tracker, _, _ := newTestTracker(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	goal, err := tracker.CreateGoal("acct-never-invested", "Retirement", GoalTypeRetirement, 1000000, now.AddDate(1, 0, 0), []string{schemeA}, 0, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	progress, err := tracker.CalculateProgress(goal.GoalId, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if progress.CurrentValueInMinorUnits != 0 {
		t.Errorf("expected zero current value, got %d", progress.CurrentValueInMinorUnits)
	}
	if progress.ProgressPercent != 0 {
		t.Errorf("expected zero progress percent, got %v", progress.ProgressPercent)
	}
	if progress.IsOnTrack {
		t.Errorf("expected not on track with zero holdings and zero contribution toward a positive target")
	}
}

// TestCalculateProgressHandWorkedOneMonthExample is the simplest possible
// hand-worked example of the projection formula:
//
//	currentValue = 100000, monthlyContribution = 10000, r = 0.007 (the
//	package's assumedMonthlyGrowthRate), monthsRemaining = 1.
//
//	factor = (1+r)^1 = 1.007
//	futureValueOfCurrent      = 100000 * 1.007 = 100700
//	futureValueOfContributions = 10000 * ((1.007-1)/0.007) = 10000 * 1 = 10000
//	projectedValue = 100700 + 10000 = 110700
func TestCalculateProgressHandWorkedOneMonthExample(t *testing.T) {
	tracker, catalog, router := newTestTracker(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	targetDate := now.AddDate(0, 1, 0)

	goal, err := tracker.CreateGoal("acct-1mo", "Short Goal", GoalTypeOther, 50000, targetDate, []string{schemeA}, 10000, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// currentValue = 100000: 1000 units at NAV 100.
	buyAndConfirm(t, catalog, router, "acct-1mo", schemeA, 100, 100000, now)

	progress, err := tracker.CalculateProgress(goal.GoalId, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if progress.CurrentValueInMinorUnits != 100000 {
		t.Fatalf("expected current value 100000, got %d", progress.CurrentValueInMinorUnits)
	}
	if progress.MonthsRemaining != 1 {
		t.Fatalf("expected 1 month remaining, got %d", progress.MonthsRemaining)
	}
	if progress.ProjectedValueAtTargetDateInMinorUnits != 110700 {
		t.Errorf("expected projected value 110700, got %d", progress.ProjectedValueAtTargetDateInMinorUnits)
	}
	if !progress.IsOnTrack {
		t.Errorf("expected on track: projected 110700 >= target 50000")
	}
	if progress.ProjectedSurplusOrShortfallInMinorUnits != 110700-50000 {
		t.Errorf("expected surplus %d, got %d", 110700-50000, progress.ProjectedSurplusOrShortfallInMinorUnits)
	}
}

// TestCalculateProgressHandWorkedOnTrackExample is the fuller hand-worked
// example over 12 months:
//
//	currentValue = 100000, monthlyContribution = 10000, r = 0.007,
//	monthsRemaining = 12.
//
//	factor = 1.007^12 = 1.0873106619155055
//	futureValueOfCurrent       = 100000 * 1.0873106619155055 = 108731.06619155055
//	annuityFactor               = (1.0873106619155055-1)/0.007 = 12.472951702215079
//	futureValueOfContributions = 10000 * 12.472951702215079 = 124729.51702215074 (roughly)
//	projectedValue (rounded)   = round(108731.06619155055 + 124729.51702215074)
//	                            = round(233460.5832137013) = 233461
//
//	target = 200000, so projected 233461 >= target -> on track, surplus
//	= 233461 - 200000 = 33461.
func TestCalculateProgressHandWorkedOnTrackExample(t *testing.T) {
	tracker, catalog, router := newTestTracker(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	targetDate := now.AddDate(1, 0, 0) // exactly 12 months out

	goal, err := tracker.CreateGoal("acct-12mo", "Retirement", GoalTypeRetirement, 200000, targetDate, []string{schemeA}, 10000, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// currentValue = 100000: 1000 units at NAV 100.
	buyAndConfirm(t, catalog, router, "acct-12mo", schemeA, 100, 100000, now)

	progress, err := tracker.CalculateProgress(goal.GoalId, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if progress.MonthsRemaining != 12 {
		t.Fatalf("expected 12 months remaining, got %d", progress.MonthsRemaining)
	}
	if progress.ProjectedValueAtTargetDateInMinorUnits != 233461 {
		t.Errorf("expected projected value 233461, got %d", progress.ProjectedValueAtTargetDateInMinorUnits)
	}
	if !progress.IsOnTrack {
		t.Errorf("expected on track: projected 233461 >= target 200000")
	}
	if progress.ProjectedSurplusOrShortfallInMinorUnits != 33461 {
		t.Errorf("expected surplus 33461, got %d", progress.ProjectedSurplusOrShortfallInMinorUnits)
	}
	if progress.RequiredMonthlyContributionInMinorUnits != 0 {
		t.Errorf("expected zero required additional contribution when already on track, got %d", progress.RequiredMonthlyContributionInMinorUnits)
	}
	expectedProgressPercent := 100000.0 / 200000.0 * 100.0
	if progress.ProgressPercent != expectedProgressPercent {
		t.Errorf("expected progress percent %v, got %v", expectedProgressPercent, progress.ProgressPercent)
	}
}

// TestCalculateProgressNotOnTrackComputesRequiredContribution: currentValue
// = 50000, target = 300000, monthsRemaining = 24, current monthly
// contribution = 0 (so the goal, as configured, is NOT on track), r=0.007.
//
//	factor = 1.007^24 = 1.1822444755151345
//	futureValueOfCurrent = 50000 * 1.1822444755151345 = 59112.22377575672
//	projectedValue (contribution=0) = round(59112.22377575672) = 59112
//	59112 < 300000 -> NOT on track.
//
//	requiredMonthlyContribution solves:
//	  remaining = 300000 - 59112.22377575672 = 240887.77622424328
//	  annuityFactor = (1.1822444755151345-1)/0.007 = 26.034925073590635
//	  C = 240887.77622424328 / 26.034925073590635 = 9252.4858644051
//	  -> ceil(C) = 9253 (rounds UP so the contribution is at least
//	     sufficient, never short by a rounding sliver)
func TestCalculateProgressNotOnTrackComputesRequiredContribution(t *testing.T) {
	tracker, catalog, router := newTestTracker(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	targetDate := now.AddDate(2, 0, 0) // exactly 24 months out

	goal, err := tracker.CreateGoal("acct-24mo", "Education", GoalTypeEducation, 300000, targetDate, []string{schemeA}, 0, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// currentValue = 50000: 500 units at NAV 100.
	buyAndConfirm(t, catalog, router, "acct-24mo", schemeA, 100, 50000, now)

	progress, err := tracker.CalculateProgress(goal.GoalId, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if progress.MonthsRemaining != 24 {
		t.Fatalf("expected 24 months remaining, got %d", progress.MonthsRemaining)
	}
	if progress.ProjectedValueAtTargetDateInMinorUnits != 59112 {
		t.Errorf("expected projected value 59112, got %d", progress.ProjectedValueAtTargetDateInMinorUnits)
	}
	if progress.IsOnTrack {
		t.Errorf("expected NOT on track: projected 59112 < target 300000")
	}
	if progress.RequiredMonthlyContributionInMinorUnits != 9253 {
		t.Errorf("expected required monthly contribution 9253, got %d", progress.RequiredMonthlyContributionInMinorUnits)
	}
}

func TestCalculateProgressIgnoresHoldingsInUnlinkedSchemes(t *testing.T) {
	tracker, catalog, router := newTestTracker(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	goal, err := tracker.CreateGoal("acct-unlinked", "Retirement", GoalTypeRetirement, 1000000, now.AddDate(1, 0, 0), []string{schemeA}, 0, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	buyAndConfirm(t, catalog, router, "acct-unlinked", schemeA, 100, 10000, now)
	buyAndConfirm(t, catalog, router, "acct-unlinked", schemeB, 100, 999999, now) // large but NOT linked to the goal

	progress, err := tracker.CalculateProgress(goal.GoalId, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if progress.CurrentValueInMinorUnits != 10000 {
		t.Errorf("expected current value to count only the linked scheme (10000), got %d", progress.CurrentValueInMinorUnits)
	}
}

func TestListGoalsForAccountSortedByCreatedAt(t *testing.T) {
	tracker, _, _ := newTestTracker(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	first, _ := tracker.CreateGoal("acct-list", "First", GoalTypeRetirement, 1000000, now.AddDate(1, 0, 0), []string{schemeA}, 0, now)
	second, _ := tracker.CreateGoal("acct-list", "Second", GoalTypeEducation, 1000000, now.AddDate(2, 0, 0), []string{schemeA}, 0, now.AddDate(0, 1, 0))

	goals := tracker.ListGoalsForAccount("acct-list")
	if len(goals) != 2 {
		t.Fatalf("expected 2 goals, got %d", len(goals))
	}
	if goals[0].GoalId != first.GoalId || goals[1].GoalId != second.GoalId {
		t.Errorf("expected goals sorted by CreatedAt (first, then second)")
	}
}

func TestMonthsBetweenCalendarAware(t *testing.T) {
	start := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	end := time.Date(2027, 3, 15, 0, 0, 0, 0, time.UTC)
	if got := monthsBetween(start, end); got != 14 {
		t.Errorf("expected 14 full months between Jan 15 2026 and Mar 15 2027, got %d", got)
	}
	// One day short of the 14th month anniversary -> only 13 full months.
	almostFourteen := time.Date(2027, 3, 14, 0, 0, 0, 0, time.UTC)
	if got := monthsBetween(start, almostFourteen); got != 13 {
		t.Errorf("expected 13 full months, got %d", got)
	}
}

func TestMonthsBetweenReturnsZeroForPastOrEqualDates(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := monthsBetween(now, now); got != 0 {
		t.Errorf("expected 0 for equal dates, got %d", got)
	}
	if got := monthsBetween(now, now.AddDate(0, 0, -1)); got != 0 {
		t.Errorf("expected 0 for an end date before start, got %d", got)
	}
}

func TestProjectFutureValueZeroMonthsReturnsCurrentValue(t *testing.T) {
	if got := projectFutureValue(50000, 10000, 0, assumedMonthlyGrowthRate); got != 50000 {
		t.Errorf("expected projectFutureValue with 0 months remaining to return currentValue unchanged, got %d", got)
	}
}
