// Package riskprofiling implements a risk-tolerance questionnaire and
// scores it into an investor risk category — FEATURES.md §1: "Risk
// profiling questionnaire → investor risk category (feeds
// Robo-Advisory)". The Robo-Advisory feature this feeds is NOT built
// (see FEATURES.md's own phasing) — this package produces the
// classification a future Robo-Advisory feature would consume, nothing
// downstream actually reads it yet.
//
// The questionnaire design (6 fixed Likert-style questions, 1-5 points
// each, summed into a 6-30 range mapped onto 5 categories) mirrors the
// shape of real investor risk-tolerance questionnaires (Vanguard/
// Fidelity-style) without claiming to BE one — a real build should have
// this reviewed by an actual investment-suitability/compliance
// professional, not treated as legally sufficient because it resembles
// the real thing.
package riskprofiling

import (
	"fmt"
	"sync"
)

type RiskCategory string

const (
	CategoryConservative           RiskCategory = "CONSERVATIVE"
	CategoryModeratelyConservative RiskCategory = "MODERATELY_CONSERVATIVE"
	CategoryModerate               RiskCategory = "MODERATE"
	CategoryModeratelyAggressive   RiskCategory = "MODERATELY_AGGRESSIVE"
	CategoryAggressive             RiskCategory = "AGGRESSIVE"
)

type QuestionOption struct {
	OptionText string
	PointValue int
}

type Question struct {
	QuestionId   string
	QuestionText string
	Options      []QuestionOption
}

// StandardQuestionnaire is the fixed set of questions every account
// answers — deliberately not per-account customizable in this skeleton.
// Each question's options are worth 1 to 5 points; six questions give a
// total score range of 6 (most conservative possible) to 30 (most
// aggressive possible).
var StandardQuestionnaire = []Question{
	{
		QuestionId:   "investment-horizon",
		QuestionText: "When do you expect to need this money?",
		Options: []QuestionOption{
			{OptionText: "Within 1 year", PointValue: 1},
			{OptionText: "1-3 years", PointValue: 2},
			{OptionText: "3-5 years", PointValue: 3},
			{OptionText: "5-10 years", PointValue: 4},
			{OptionText: "More than 10 years", PointValue: 5},
		},
	},
	{
		QuestionId:   "drawdown-reaction",
		QuestionText: "If your portfolio fell 20% in a month, what would you do?",
		Options: []QuestionOption{
			{OptionText: "Sell everything immediately", PointValue: 1},
			{OptionText: "Sell some to reduce risk", PointValue: 2},
			{OptionText: "Do nothing and wait it out", PointValue: 3},
			{OptionText: "Buy a little more", PointValue: 4},
			{OptionText: "Buy significantly more — it's a discount", PointValue: 5},
		},
	},
	{
		QuestionId:   "income-stability",
		QuestionText: "How stable is your primary source of income?",
		Options: []QuestionOption{
			{OptionText: "Very unstable / no regular income", PointValue: 1},
			{OptionText: "Somewhat unstable", PointValue: 2},
			{OptionText: "Stable", PointValue: 3},
			{OptionText: "Very stable (e.g. government job)", PointValue: 4},
			{OptionText: "Very stable with significant additional savings/assets", PointValue: 5},
		},
	},
	{
		QuestionId:   "investment-goal",
		QuestionText: "What is the primary goal for this investment?",
		Options: []QuestionOption{
			{OptionText: "Capital preservation — I cannot afford to lose money", PointValue: 1},
			{OptionText: "Steady income generation", PointValue: 2},
			{OptionText: "Balanced growth and income", PointValue: 3},
			{OptionText: "Long-term capital growth", PointValue: 4},
			{OptionText: "Maximum growth — I can tolerate high volatility", PointValue: 5},
		},
	},
	{
		QuestionId:   "investing-experience",
		QuestionText: "How much experience do you have with market-linked investments?",
		Options: []QuestionOption{
			{OptionText: "None", PointValue: 1},
			{OptionText: "A little — mostly fixed deposits/savings", PointValue: 2},
			{OptionText: "Some — mutual funds, occasionally direct equity", PointValue: 3},
			{OptionText: "Significant — regular direct equity investor", PointValue: 4},
			{OptionText: "Extensive — including derivatives/leveraged products", PointValue: 5},
		},
	},
	{
		QuestionId:   "existing-emergency-fund",
		QuestionText: "Do you have an emergency fund covering at least 6 months of expenses, separate from this investment?",
		Options: []QuestionOption{
			{OptionText: "No emergency fund at all", PointValue: 1},
			{OptionText: "Covers less than 3 months", PointValue: 2},
			{OptionText: "Covers 3-6 months", PointValue: 3},
			{OptionText: "Covers 6-12 months", PointValue: 4},
			{OptionText: "Covers more than 12 months", PointValue: 5},
		},
	},
}

// RiskProfile is the stored result of one account's completed
// questionnaire.
type RiskProfile struct {
	AccountIdentifier string
	TotalScore        int
	RiskCategory      RiskCategory
}

var ErrUnknownQuestionId = fmt.Errorf("answers must cover exactly the standard questionnaire's question ids, no more, no less")
var ErrInvalidPointValueForQuestion = fmt.Errorf("point value is not one of the valid options for that question")

// RiskProfiler is safe for concurrent use.
//
// TODO(real build): in-memory only, no persistence. Nothing downstream
// (a future Robo-Advisory feature) actually reads this yet — see the
// package doc.
type RiskProfiler struct {
	mutexGuardingProfiles sync.Mutex
	profilesByAccount     map[string]RiskProfile
}

func NewRiskProfiler() *RiskProfiler {
	return &RiskProfiler{profilesByAccount: make(map[string]RiskProfile)}
}

// SubmitAnswers scores a completed questionnaire and stores the
// resulting profile. answerPointValuesByQuestionId must have EXACTLY one
// entry per question in StandardQuestionnaire (no more, no less), each
// a point value that's actually one of that question's real options —
// this validates the caller didn't just make up a score, only that they
// selected real options.
func (profiler *RiskProfiler) SubmitAnswers(accountIdentifier string, answerPointValuesByQuestionId map[string]int) (RiskProfile, error) {
	if len(answerPointValuesByQuestionId) != len(StandardQuestionnaire) {
		return RiskProfile{}, ErrUnknownQuestionId
	}

	totalScore := 0
	for _, question := range StandardQuestionnaire {
		pointValue, wasAnswered := answerPointValuesByQuestionId[question.QuestionId]
		if !wasAnswered {
			return RiskProfile{}, ErrUnknownQuestionId
		}
		if !isValidPointValueForQuestion(question, pointValue) {
			return RiskProfile{}, ErrInvalidPointValueForQuestion
		}
		totalScore += pointValue
	}

	profile := RiskProfile{
		AccountIdentifier: accountIdentifier,
		TotalScore:        totalScore,
		RiskCategory:      classifyScore(totalScore),
	}

	profiler.mutexGuardingProfiles.Lock()
	profiler.profilesByAccount[accountIdentifier] = profile
	profiler.mutexGuardingProfiles.Unlock()

	return profile, nil
}

// LookupProfile returns the stored profile, or (RiskProfile{}, false) if
// the account has never completed the questionnaire.
func (profiler *RiskProfiler) LookupProfile(accountIdentifier string) (RiskProfile, bool) {
	profiler.mutexGuardingProfiles.Lock()
	defer profiler.mutexGuardingProfiles.Unlock()

	profile, wasFound := profiler.profilesByAccount[accountIdentifier]
	return profile, wasFound
}

func isValidPointValueForQuestion(question Question, pointValue int) bool {
	for _, option := range question.Options {
		if option.PointValue == pointValue {
			return true
		}
	}
	return false
}

// classifyScore maps a total score (range: len(StandardQuestionnaire)
// to len(StandardQuestionnaire)*5) onto one of the five risk categories
// in equal-width bands. With 6 questions the range is 6-30 (25 possible
// values across 5 categories — not evenly divisible by 5, so the bands
// below are deliberately explicit rather than computed, to avoid an
// off-by-one from an integer-division shortcut).
func classifyScore(totalScore int) RiskCategory {
	switch {
	case totalScore <= 10:
		return CategoryConservative
	case totalScore <= 15:
		return CategoryModeratelyConservative
	case totalScore <= 20:
		return CategoryModerate
	case totalScore <= 25:
		return CategoryModeratelyAggressive
	default:
		return CategoryAggressive
	}
}
