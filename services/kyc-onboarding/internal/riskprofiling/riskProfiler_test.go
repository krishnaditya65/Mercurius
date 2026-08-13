package riskprofiling

import "testing"

// mostConservativeAnswers picks the lowest-point option for every
// question — total score 6, the floor of the range.
func mostConservativeAnswers() map[string]int {
	answers := make(map[string]int)
	for _, question := range StandardQuestionnaire {
		answers[question.QuestionId] = 1
	}
	return answers
}

// mostAggressiveAnswers picks the highest-point option for every
// question — total score 30, the ceiling of the range.
func mostAggressiveAnswers() map[string]int {
	answers := make(map[string]int)
	for _, question := range StandardQuestionnaire {
		answers[question.QuestionId] = 5
	}
	return answers
}

func TestLowestPossibleScoreClassifiesAsConservative(t *testing.T) {
	profiler := NewRiskProfiler()
	profile, err := profiler.SubmitAnswers("acct-001", mostConservativeAnswers())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if profile.TotalScore != len(StandardQuestionnaire) {
		t.Fatalf("expected the floor score %d, got %d", len(StandardQuestionnaire), profile.TotalScore)
	}
	if profile.RiskCategory != CategoryConservative {
		t.Fatalf("expected CategoryConservative, got %v", profile.RiskCategory)
	}
}

func TestHighestPossibleScoreClassifiesAsAggressive(t *testing.T) {
	profiler := NewRiskProfiler()
	profile, err := profiler.SubmitAnswers("acct-001", mostAggressiveAnswers())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if profile.TotalScore != len(StandardQuestionnaire)*5 {
		t.Fatalf("expected the ceiling score %d, got %d", len(StandardQuestionnaire)*5, profile.TotalScore)
	}
	if profile.RiskCategory != CategoryAggressive {
		t.Fatalf("expected CategoryAggressive, got %v", profile.RiskCategory)
	}
}

func TestAllFiveCategoryBandsAreReachable(t *testing.T) {
	// Every question answered with the same point value N gives a total
	// score of N * len(StandardQuestionnaire) = N * 6, so this sweeps
	// every band without needing to hand-pick mixed answers.
	expectedCategoryByUniformPointValue := map[int]RiskCategory{
		1: CategoryConservative,           // score 6
		2: CategoryModeratelyConservative, // score 12
		3: CategoryModerate,               // score 18
		4: CategoryModeratelyAggressive,   // score 24
		5: CategoryAggressive,             // score 30
	}

	for pointValue, expectedCategory := range expectedCategoryByUniformPointValue {
		profiler := NewRiskProfiler()
		answers := make(map[string]int)
		for _, question := range StandardQuestionnaire {
			answers[question.QuestionId] = pointValue
		}

		profile, err := profiler.SubmitAnswers("acct-001", answers)
		if err != nil {
			t.Fatalf("unexpected error for uniform point value %d: %v", pointValue, err)
		}
		if profile.RiskCategory != expectedCategory {
			t.Fatalf("uniform point value %d (score %d): expected %v, got %v", pointValue, profile.TotalScore, expectedCategory, profile.RiskCategory)
		}
	}
}

func TestMissingAQuestionIsRejected(t *testing.T) {
	profiler := NewRiskProfiler()
	answers := mostConservativeAnswers()
	delete(answers, StandardQuestionnaire[0].QuestionId)

	_, err := profiler.SubmitAnswers("acct-001", answers)
	if err != ErrUnknownQuestionId {
		t.Fatalf("expected ErrUnknownQuestionId for a missing question, got %v", err)
	}
}

func TestUnknownQuestionIdIsRejected(t *testing.T) {
	profiler := NewRiskProfiler()
	answers := mostConservativeAnswers()
	delete(answers, StandardQuestionnaire[0].QuestionId)
	answers["not-a-real-question"] = 3

	_, err := profiler.SubmitAnswers("acct-001", answers)
	if err != ErrUnknownQuestionId {
		t.Fatalf("expected ErrUnknownQuestionId for an unrecognized question id, got %v", err)
	}
}

func TestInvalidPointValueForAQuestionIsRejected(t *testing.T) {
	profiler := NewRiskProfiler()
	answers := mostConservativeAnswers()
	answers[StandardQuestionnaire[0].QuestionId] = 999 // not a real option's point value

	_, err := profiler.SubmitAnswers("acct-001", answers)
	if err != ErrInvalidPointValueForQuestion {
		t.Fatalf("expected ErrInvalidPointValueForQuestion, got %v", err)
	}
}

func TestLookupProfileReturnsNotFoundBeforeSubmission(t *testing.T) {
	profiler := NewRiskProfiler()
	_, wasFound := profiler.LookupProfile("acct-001")
	if wasFound {
		t.Fatal("expected no profile before any submission")
	}
}

func TestLookupProfileReturnsTheStoredProfileAfterSubmission(t *testing.T) {
	profiler := NewRiskProfiler()
	submitted, _ := profiler.SubmitAnswers("acct-001", mostAggressiveAnswers())

	looked_up, wasFound := profiler.LookupProfile("acct-001")
	if !wasFound {
		t.Fatal("expected to find the profile after submission")
	}
	if looked_up.RiskCategory != submitted.RiskCategory || looked_up.TotalScore != submitted.TotalScore {
		t.Fatalf("expected lookup to return the same profile that was submitted, got %+v vs %+v", looked_up, submitted)
	}
}

func TestSubmittingAgainOverwritesThePreviousProfile(t *testing.T) {
	profiler := NewRiskProfiler()
	profiler.SubmitAnswers("acct-001", mostConservativeAnswers())
	profiler.SubmitAnswers("acct-001", mostAggressiveAnswers())

	profile, _ := profiler.LookupProfile("acct-001")
	if profile.RiskCategory != CategoryAggressive {
		t.Fatalf("expected the second submission to overwrite the first, got %v", profile.RiskCategory)
	}
}

func TestTwoAccountsHaveIndependentProfiles(t *testing.T) {
	profiler := NewRiskProfiler()
	profiler.SubmitAnswers("acct-001", mostConservativeAnswers())
	profiler.SubmitAnswers("acct-002", mostAggressiveAnswers())

	profileOne, _ := profiler.LookupProfile("acct-001")
	profileTwo, _ := profiler.LookupProfile("acct-002")

	if profileOne.RiskCategory != CategoryConservative {
		t.Fatalf("expected acct-001 to stay Conservative, got %v", profileOne.RiskCategory)
	}
	if profileTwo.RiskCategory != CategoryAggressive {
		t.Fatalf("expected acct-002 to stay Aggressive, got %v", profileTwo.RiskCategory)
	}
}

func TestStandardQuestionnaireHasSixQuestionsWithFiveOptionsEach(t *testing.T) {
	if len(StandardQuestionnaire) != 6 {
		t.Fatalf("expected 6 questions, got %d", len(StandardQuestionnaire))
	}
	for _, question := range StandardQuestionnaire {
		if len(question.Options) != 5 {
			t.Fatalf("expected question %q to have 5 options, got %d", question.QuestionId, len(question.Options))
		}
	}
}
