// Package goalinvesting implements goal-based investing (retirement,
// education, ...) with progress tracking — FEATURES.md §4, "Goal-based
// investing (retirement, education) with progress tracking".
//
// A Goal names a target (targetAmountInMinorUnits by targetDate) and is
// linked to whichever schemes are actually funding it — the scheme(s) a
// SIP (internal/sipscheduler) or a basket subscription
// (internal/basketrebalancing) invests into. This package does not import
// either of those directly (it only needs internal/amcrouting's holdings
// and internal/fundcatalog's NAVs to value progress) — a caller wires the
// link by passing in the scheme id(s) the SIP/basket subscription
// actually invests into when creating the goal.
//
// CalculateProgress is the real math: CURRENT invested value is
// units-held × current-NAV summed across every linked scheme for the
// goal's account (internal/amcrouting's HoldingsForAccount +
// internal/fundcatalog's current NAVs — the same valuation approach
// internal/basketrebalancing uses for drift). "Are we on track" is a real
// projection: it compounds the CURRENT value forward to targetDate at an
// ILLUSTRATIVE assumed monthly growth rate, adds the future value of an
// ordinary annuity of the goal's assumed monthly contribution rate over
// the same remaining period, and compares that projected value to
// targetAmount. See CalculateProgress's doc comment and the test file's
// hand-worked example for the exact formula and arithmetic.
//
// LOUD CAVEAT: assumedMonthlyGrowthRate is a single illustrative constant,
// not a forecast, not derived from any real historical return data (this
// repo has none — see internal/roboadvisory's identical caveat), and
// does not vary by the goal's linked schemes' actual category/risk mix. A
// real "on track" projection would need real historical/expected return
// assumptions per asset class, plus almost certainly a range/confidence
// interval rather than one deterministic point estimate.
package goalinvesting

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"mercurius/mutualFunds/internal/amcrouting"
	"mercurius/mutualFunds/internal/fundcatalog"
)

// GoalType is the purpose of a goal. Fixed set, matching the style of
// sipscheduler's SipFrequency (only MONTHLY implemented) — a real product
// might allow a free-text label, but validating against a known set keeps
// downstream reporting/analytics sane.
type GoalType string

const (
	GoalTypeRetirement     GoalType = "RETIREMENT"
	GoalTypeEducation      GoalType = "EDUCATION"
	GoalTypeHomePurchase   GoalType = "HOME_PURCHASE"
	GoalTypeWealthCreation GoalType = "WEALTH_CREATION"
	GoalTypeOther          GoalType = "OTHER"
)

var validGoalTypes = map[GoalType]bool{
	GoalTypeRetirement:     true,
	GoalTypeEducation:      true,
	GoalTypeHomePurchase:   true,
	GoalTypeWealthCreation: true,
	GoalTypeOther:          true,
}

// Goal is one named savings target, linked to whichever scheme(s) an
// account's SIP or basket subscription actually invests into.
type Goal struct {
	GoalId                                 string
	AccountIdentifier                      string
	Name                                   string
	GoalType                               GoalType
	TargetAmountInMinorUnits               int64
	TargetDate                             time.Time
	LinkedSchemeIds                        []string // sorted
	AssumedMonthlyContributionInMinorUnits int64
	CreatedAt                              time.Time
}

var ErrMissingAccountIdentifier = fmt.Errorf("accountIdentifier is required")
var ErrEmptyGoalName = fmt.Errorf("goal name is required")
var ErrUnsupportedGoalType = fmt.Errorf("unsupported goal type")
var ErrInvalidTargetAmount = fmt.Errorf("targetAmountInMinorUnits must be strictly positive")
var ErrTargetDateNotInFuture = fmt.Errorf("targetDate must be after the goal's creation time")
var ErrNoLinkedSchemes = fmt.Errorf("a goal must be linked to at least one scheme")
var ErrUnknownScheme = fmt.Errorf("no such scheme exists in the catalog")
var ErrNegativeContribution = fmt.Errorf("assumedMonthlyContributionInMinorUnits cannot be negative")
var ErrGoalNotFound = fmt.Errorf("no goal exists with that id")

// assumedMonthlyGrowthRate is the illustrative, non-forecast monthly
// growth rate CalculateProgress compounds a goal's current value and
// future contributions at. 0.007 per month compounds to
// (1.007)^12 - 1 ≈ 8.73% a year — a deliberately unremarkable, round
// illustrative figure, not tuned to any real asset class or historical
// data. See the package doc comment's loud caveat.
const assumedMonthlyGrowthRate = 0.007

// GoalTracker is safe for concurrent use.
type GoalTracker struct {
	catalog   *fundcatalog.FundCatalog
	amcRouter *amcrouting.AmcOrderRouter

	mutexGuardingGoals sync.Mutex
	goalsById          map[string]*Goal
}

func NewGoalTracker(catalog *fundcatalog.FundCatalog, amcRouter *amcrouting.AmcOrderRouter) *GoalTracker {
	return &GoalTracker{
		catalog:   catalog,
		amcRouter: amcRouter,
		goalsById: make(map[string]*Goal),
	}
}

// CreateGoal validates and stores a new goal. linkedSchemeIds is the set
// of schemes whose holdings for accountIdentifier count toward this
// goal's progress — typically the single scheme a SIP invests into, or
// every constituent scheme of a basket subscription.
// assumedMonthlyContributionInMinorUnits is the goal's expected ongoing
// contribution rate (e.g. the linked SIP's installment amount) used by
// CalculateProgress's on-track projection; 0 is valid for a lumpsum-only
// goal with no further planned contributions.
func (tracker *GoalTracker) CreateGoal(
	accountIdentifier string,
	name string,
	goalType GoalType,
	targetAmountInMinorUnits int64,
	targetDate time.Time,
	linkedSchemeIds []string,
	assumedMonthlyContributionInMinorUnits int64,
	now time.Time,
) (*Goal, error) {
	if accountIdentifier == "" {
		return nil, ErrMissingAccountIdentifier
	}
	if name == "" {
		return nil, ErrEmptyGoalName
	}
	if !validGoalTypes[goalType] {
		return nil, ErrUnsupportedGoalType
	}
	if targetAmountInMinorUnits <= 0 {
		return nil, ErrInvalidTargetAmount
	}
	if !targetDate.After(now) {
		return nil, ErrTargetDateNotInFuture
	}
	if len(linkedSchemeIds) == 0 {
		return nil, ErrNoLinkedSchemes
	}
	if assumedMonthlyContributionInMinorUnits < 0 {
		return nil, ErrNegativeContribution
	}

	sortedSchemeIds := make([]string, len(linkedSchemeIds))
	copy(sortedSchemeIds, linkedSchemeIds)
	sort.Strings(sortedSchemeIds)
	for _, schemeId := range sortedSchemeIds {
		if _, wasFound := tracker.catalog.Lookup(schemeId); !wasFound {
			return nil, ErrUnknownScheme
		}
	}

	goalId, genError := generateGoalId()
	if genError != nil {
		return nil, fmt.Errorf("failed to generate goal id: %w", genError)
	}

	goal := &Goal{
		GoalId:                                 goalId,
		AccountIdentifier:                      accountIdentifier,
		Name:                                   name,
		GoalType:                               goalType,
		TargetAmountInMinorUnits:               targetAmountInMinorUnits,
		TargetDate:                             targetDate,
		LinkedSchemeIds:                        sortedSchemeIds,
		AssumedMonthlyContributionInMinorUnits: assumedMonthlyContributionInMinorUnits,
		CreatedAt:                              now,
	}

	tracker.mutexGuardingGoals.Lock()
	tracker.goalsById[goalId] = goal
	tracker.mutexGuardingGoals.Unlock()

	return goal, nil
}

// LookupGoal returns the goal, or false if goalId doesn't exist.
func (tracker *GoalTracker) LookupGoal(goalId string) (*Goal, bool) {
	tracker.mutexGuardingGoals.Lock()
	defer tracker.mutexGuardingGoals.Unlock()

	goal, wasFound := tracker.goalsById[goalId]
	return goal, wasFound
}

// ListGoalsForAccount returns every goal for accountIdentifier, sorted by
// CreatedAt for a deterministic response.
func (tracker *GoalTracker) ListGoalsForAccount(accountIdentifier string) []*Goal {
	tracker.mutexGuardingGoals.Lock()
	defer tracker.mutexGuardingGoals.Unlock()

	matchingGoals := make([]*Goal, 0)
	for _, goal := range tracker.goalsById {
		if goal.AccountIdentifier == accountIdentifier {
			matchingGoals = append(matchingGoals, goal)
		}
	}
	sort.Slice(matchingGoals, func(i, j int) bool { return matchingGoals[i].CreatedAt.Before(matchingGoals[j].CreatedAt) })
	return matchingGoals
}

// GoalProgress is the result of CalculateProgress — see that function's
// doc comment for the exact formula.
type GoalProgress struct {
	GoalId                                  string
	CurrentValueInMinorUnits                int64
	TargetAmountInMinorUnits                int64
	ProgressPercent                         float64
	MonthsElapsed                           int
	MonthsRemaining                         int
	ProjectedValueAtTargetDateInMinorUnits  int64
	IsOnTrack                               bool
	ProjectedSurplusOrShortfallInMinorUnits int64 // projected - target; positive = surplus
	RequiredMonthlyContributionInMinorUnits int64 // the flat monthly contribution (from now, at assumedMonthlyGrowthRate) that would exactly reach target by TargetDate given CurrentValueInMinorUnits today; 0 if already on track or MonthsRemaining is 0
}

// CalculateProgress computes accountIdentifier's current invested value
// across goalId's LinkedSchemeIds (units held × current catalog NAV,
// summed — the same valuation approach internal/basketrebalancing uses),
// then projects forward to TargetDate to answer "are we on track":
//
//	monthsRemaining = full calendar months between now and TargetDate
//	    (0 if TargetDate has already passed)
//	r = assumedMonthlyGrowthRate
//
//	if monthsRemaining == 0:
//	    projectedValue = currentValue   (nothing left to compound)
//	else:
//	    projectedValue = currentValue * (1+r)^monthsRemaining
//	                    + monthlyContribution * (((1+r)^monthsRemaining - 1) / r)
//
// (an ordinary annuity future-value formula: the current corpus compounds
// on its own, and each future monthly contribution compounds for the
// remaining months after it's made). IsOnTrack is
// projectedValue >= targetAmount. See the test file for a fully
// hand-worked numeric example.
func (tracker *GoalTracker) CalculateProgress(goalId string, now time.Time) (*GoalProgress, error) {
	goal, wasFound := tracker.LookupGoal(goalId)
	if !wasFound {
		return nil, ErrGoalNotFound
	}

	holdings := tracker.amcRouter.HoldingsForAccount(goal.AccountIdentifier)
	linkedSchemeIdSet := make(map[string]bool, len(goal.LinkedSchemeIds))
	for _, schemeId := range goal.LinkedSchemeIds {
		linkedSchemeIdSet[schemeId] = true
	}

	currentValue := int64(0)
	for _, holding := range holdings {
		if !linkedSchemeIdSet[holding.SchemeId] {
			continue
		}
		scheme, schemeFound := tracker.catalog.Lookup(holding.SchemeId)
		if !schemeFound {
			continue // scheme vanished from the catalog; nothing sensible to value it at
		}
		currentValue += int64(math.Round(holding.TotalUnits * float64(scheme.CurrentNavInMinorUnits)))
	}

	monthsElapsed := monthsBetween(goal.CreatedAt, now)
	monthsRemaining := monthsBetween(now, goal.TargetDate)

	projectedValue := projectFutureValue(currentValue, goal.AssumedMonthlyContributionInMinorUnits, monthsRemaining, assumedMonthlyGrowthRate)

	progressPercent := 0.0
	if goal.TargetAmountInMinorUnits > 0 {
		progressPercent = float64(currentValue) / float64(goal.TargetAmountInMinorUnits) * 100.0
	}

	isOnTrack := projectedValue >= goal.TargetAmountInMinorUnits
	surplusOrShortfall := projectedValue - goal.TargetAmountInMinorUnits

	requiredMonthlyContribution := int64(0)
	if !isOnTrack && monthsRemaining > 0 {
		requiredMonthlyContribution = requiredMonthlyContributionToReachTarget(currentValue, goal.TargetAmountInMinorUnits, monthsRemaining, assumedMonthlyGrowthRate)
	}

	return &GoalProgress{
		GoalId:                                  goal.GoalId,
		CurrentValueInMinorUnits:                currentValue,
		TargetAmountInMinorUnits:                goal.TargetAmountInMinorUnits,
		ProgressPercent:                         progressPercent,
		MonthsElapsed:                           monthsElapsed,
		MonthsRemaining:                         monthsRemaining,
		ProjectedValueAtTargetDateInMinorUnits:  projectedValue,
		IsOnTrack:                               isOnTrack,
		ProjectedSurplusOrShortfallInMinorUnits: surplusOrShortfall,
		RequiredMonthlyContributionInMinorUnits: requiredMonthlyContribution,
	}, nil
}

// projectFutureValue computes the future value described in
// CalculateProgress's doc comment: currentValue compounded for
// monthsRemaining at monthlyRate, plus the future value of an ordinary
// annuity of monthlyContribution over the same period.
func projectFutureValue(currentValue int64, monthlyContribution int64, monthsRemaining int, monthlyRate float64) int64 {
	if monthsRemaining <= 0 {
		return currentValue
	}
	growthFactor := math.Pow(1+monthlyRate, float64(monthsRemaining))
	futureValueOfCurrent := float64(currentValue) * growthFactor
	futureValueOfContributions := 0.0
	if monthlyRate == 0 {
		futureValueOfContributions = float64(monthlyContribution) * float64(monthsRemaining)
	} else {
		futureValueOfContributions = float64(monthlyContribution) * ((growthFactor - 1) / monthlyRate)
	}
	return int64(math.Round(futureValueOfCurrent + futureValueOfContributions))
}

// requiredMonthlyContributionToReachTarget solves the same annuity
// formula projectFutureValue uses for the monthly contribution C that
// makes projectFutureValue(currentValue, C, monthsRemaining, monthlyRate)
// exactly equal targetAmount:
//
//	targetAmount = currentValue*(1+r)^n + C * (((1+r)^n - 1) / r)
//	C = (targetAmount - currentValue*(1+r)^n) / (((1+r)^n - 1) / r)
//
// Returns 0 if the current trajectory already reaches target with zero
// further contributions (a negative required contribution isn't
// meaningful — the goal is simply already on track without needing this
// number, which CalculateProgress reflects via IsOnTrack instead).
func requiredMonthlyContributionToReachTarget(currentValue int64, targetAmount int64, monthsRemaining int, monthlyRate float64) int64 {
	if monthsRemaining <= 0 {
		return 0
	}
	growthFactor := math.Pow(1+monthlyRate, float64(monthsRemaining))
	futureValueOfCurrent := float64(currentValue) * growthFactor
	remainingNeeded := float64(targetAmount) - futureValueOfCurrent
	if remainingNeeded <= 0 {
		return 0
	}

	var annuityFactor float64
	if monthlyRate == 0 {
		annuityFactor = float64(monthsRemaining)
	} else {
		annuityFactor = (growthFactor - 1) / monthlyRate
	}
	return int64(math.Ceil(remainingNeeded / annuityFactor))
}

// monthsBetween counts full calendar months from start to end using
// calendar-aware time.Time.AddDate, the same pattern sipscheduler's
// stepUpAdjustedInstallmentAmount uses for full-year anniversaries.
// Returns 0 if end is not after start.
func monthsBetween(start time.Time, end time.Time) int {
	if !end.After(start) {
		return 0
	}
	months := 0
	for !start.AddDate(0, months+1, 0).After(end) {
		months++
	}
	return months
}

func generateGoalId() (string, error) {
	randomBytes := make([]byte, 8)
	if _, readError := rand.Read(randomBytes); readError != nil {
		return "", readError
	}
	return "goal-" + hex.EncodeToString(randomBytes), nil
}
