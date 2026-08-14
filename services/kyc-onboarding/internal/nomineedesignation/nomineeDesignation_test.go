package nomineedesignation

import (
	"testing"
	"time"
)

var testNow = time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)

func adultDob() time.Time {
	return testNow.AddDate(-30, 0, 0)
}

func minorDob() time.Time {
	return testNow.AddDate(-10, 0, 0)
}

func TestSubmitNominationWithTwoNomineesSummingTo100Succeeds(t *testing.T) {
	registry := NewNomineeDesignationRegistry()

	designation, err := registry.SubmitNomination("acct-001", []NomineeInput{
		{FullName: "Alice Trader", Relationship: "spouse", DateOfBirth: adultDob(), PercentageAllocation: 60},
		{FullName: "Bob Trader", Relationship: "child", DateOfBirth: adultDob(), PercentageAllocation: 40},
	}, false, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !designation.IsComplete() {
		t.Fatal("expected designation to be complete")
	}
	if len(designation.Nominees) != 2 {
		t.Fatalf("expected 2 nominees, got %d", len(designation.Nominees))
	}
	for _, nominee := range designation.Nominees {
		if nominee.NomineeId == "" {
			t.Fatal("expected every nominee to be assigned a non-empty NomineeId")
		}
	}
}

func TestSubmitNominationRejectsPercentagesNotSummingTo100(t *testing.T) {
	registry := NewNomineeDesignationRegistry()

	_, err := registry.SubmitNomination("acct-001", []NomineeInput{
		{FullName: "Alice Trader", Relationship: "spouse", DateOfBirth: adultDob(), PercentageAllocation: 60},
		{FullName: "Bob Trader", Relationship: "child", DateOfBirth: adultDob(), PercentageAllocation: 30},
	}, false, testNow)
	if err != ErrPercentageAllocationsMustSumTo100 {
		t.Fatalf("expected ErrPercentageAllocationsMustSumTo100, got %v", err)
	}
}

func TestSubmitNominationRejectsZeroNomineesWithoutOptOut(t *testing.T) {
	registry := NewNomineeDesignationRegistry()

	_, err := registry.SubmitNomination("acct-001", nil, false, testNow)
	if err != ErrAtLeastOneNomineeOrOptOutRequired {
		t.Fatalf("expected ErrAtLeastOneNomineeOrOptOutRequired, got %v", err)
	}
}

func TestSubmitNominationAcceptsOptOutWithZeroNominees(t *testing.T) {
	registry := NewNomineeDesignationRegistry()

	designation, err := registry.SubmitNomination("acct-001", nil, true, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !designation.IsOptedOutOfNomination {
		t.Fatal("expected IsOptedOutOfNomination to be true")
	}
	if !designation.IsComplete() {
		t.Fatal("expected an opted-out designation to be complete")
	}
}

func TestSubmitNominationOptOutIgnoresSuppliedNominees(t *testing.T) {
	registry := NewNomineeDesignationRegistry()

	designation, err := registry.SubmitNomination("acct-001", []NomineeInput{
		{FullName: "Alice Trader", Relationship: "spouse", DateOfBirth: adultDob(), PercentageAllocation: 100},
	}, true, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(designation.Nominees) != 0 {
		t.Fatalf("expected opt-out to clear nominees, got %d", len(designation.Nominees))
	}
}

func TestSubmitNominationRejectsMinorNomineeWithoutGuardianDetails(t *testing.T) {
	registry := NewNomineeDesignationRegistry()

	_, err := registry.SubmitNomination("acct-001", []NomineeInput{
		{FullName: "Child Trader", Relationship: "child", DateOfBirth: minorDob(), PercentageAllocation: 100},
	}, false, testNow)
	if err != ErrGuardianDetailsRequiredForMinorNominee {
		t.Fatalf("expected ErrGuardianDetailsRequiredForMinorNominee, got %v", err)
	}
}

func TestSubmitNominationAcceptsMinorNomineeWithGuardianDetails(t *testing.T) {
	registry := NewNomineeDesignationRegistry()

	designation, err := registry.SubmitNomination("acct-001", []NomineeInput{
		{
			FullName:                          "Child Trader",
			Relationship:                      "child",
			DateOfBirth:                       minorDob(),
			PercentageAllocation:              100,
			GuardianFullName:                  "Parent Trader",
			GuardianRelationship:              "mother",
			GuardianIdentityDocumentReference: "GOV-ID-12345",
		},
	}, false, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !designation.Nominees[0].IsMinorAsOf(testNow) {
		t.Fatal("expected nominee to be computed as a minor")
	}
}

func TestSubmitNominationRejectsMissingRequiredFields(t *testing.T) {
	registry := NewNomineeDesignationRegistry()

	if _, err := registry.SubmitNomination("", nil, true, testNow); err != ErrAccountIdentifierRequired {
		t.Fatalf("expected ErrAccountIdentifierRequired, got %v", err)
	}

	_, err := registry.SubmitNomination("acct-001", []NomineeInput{
		{FullName: "", Relationship: "spouse", DateOfBirth: adultDob(), PercentageAllocation: 100},
	}, false, testNow)
	if err != ErrNomineeFullNameRequired {
		t.Fatalf("expected ErrNomineeFullNameRequired, got %v", err)
	}

	_, err = registry.SubmitNomination("acct-001", []NomineeInput{
		{FullName: "Alice", Relationship: "", DateOfBirth: adultDob(), PercentageAllocation: 100},
	}, false, testNow)
	if err != ErrNomineeRelationshipRequired {
		t.Fatalf("expected ErrNomineeRelationshipRequired, got %v", err)
	}

	_, err = registry.SubmitNomination("acct-001", []NomineeInput{
		{FullName: "Alice", Relationship: "spouse", DateOfBirth: testNow.AddDate(1, 0, 0), PercentageAllocation: 100},
	}, false, testNow)
	if err != ErrInvalidDateOfBirth {
		t.Fatalf("expected ErrInvalidDateOfBirth for a future date of birth, got %v", err)
	}

	_, err = registry.SubmitNomination("acct-001", []NomineeInput{
		{FullName: "Alice", Relationship: "spouse", DateOfBirth: adultDob(), PercentageAllocation: 0},
	}, false, testNow)
	if err != ErrInvalidPercentageAllocation {
		t.Fatalf("expected ErrInvalidPercentageAllocation for 0%%, got %v", err)
	}

	_, err = registry.SubmitNomination("acct-001", []NomineeInput{
		{FullName: "Alice", Relationship: "spouse", DateOfBirth: adultDob(), PercentageAllocation: 101},
	}, false, testNow)
	if err != ErrInvalidPercentageAllocation {
		t.Fatalf("expected ErrInvalidPercentageAllocation for 101%%, got %v", err)
	}
}

func TestSubmitNominationOnExistingAccountReplacesPriorDesignation(t *testing.T) {
	registry := NewNomineeDesignationRegistry()
	if _, err := registry.SubmitNomination("acct-001", []NomineeInput{
		{FullName: "Alice", Relationship: "spouse", DateOfBirth: adultDob(), PercentageAllocation: 100},
	}, false, testNow); err != nil {
		t.Fatalf("unexpected error on first submit: %v", err)
	}

	designation, err := registry.SubmitNomination("acct-001", []NomineeInput{
		{FullName: "Bob", Relationship: "child", DateOfBirth: adultDob(), PercentageAllocation: 100},
	}, false, testNow)
	if err != nil {
		t.Fatalf("unexpected error on replacing submit: %v", err)
	}
	if len(designation.Nominees) != 1 || designation.Nominees[0].FullName != "Bob" {
		t.Fatalf("expected the second submit to fully replace the first, got %+v", designation.Nominees)
	}
}

func TestAddNomineeBuildsIncompleteDraftUnderLimit(t *testing.T) {
	registry := NewNomineeDesignationRegistry()

	designation, err := registry.AddNominee("acct-001", NomineeInput{
		FullName: "Alice", Relationship: "spouse", DateOfBirth: adultDob(), PercentageAllocation: 40,
	}, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if designation.IsComplete() {
		t.Fatal("expected a 40%% single-nominee draft to be incomplete")
	}
	if designation.TotalPercentageAllocated() != 40 {
		t.Fatalf("expected total allocation 40, got %d", designation.TotalPercentageAllocated())
	}
}

func TestAddNomineeCompletingTo100BecomesComplete(t *testing.T) {
	registry := NewNomineeDesignationRegistry()
	registry.AddNominee("acct-001", NomineeInput{
		FullName: "Alice", Relationship: "spouse", DateOfBirth: adultDob(), PercentageAllocation: 40,
	}, testNow)

	designation, err := registry.AddNominee("acct-001", NomineeInput{
		FullName: "Bob", Relationship: "child", DateOfBirth: adultDob(), PercentageAllocation: 60,
	}, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !designation.IsComplete() {
		t.Fatal("expected 40+60=100 to be complete")
	}
}

func TestAddNomineeRejectsExceeding100(t *testing.T) {
	registry := NewNomineeDesignationRegistry()
	registry.AddNominee("acct-001", NomineeInput{
		FullName: "Alice", Relationship: "spouse", DateOfBirth: adultDob(), PercentageAllocation: 70,
	}, testNow)

	_, err := registry.AddNominee("acct-001", NomineeInput{
		FullName: "Bob", Relationship: "child", DateOfBirth: adultDob(), PercentageAllocation: 40,
	}, testNow)
	if err != ErrPercentageAllocationsExceed100 {
		t.Fatalf("expected ErrPercentageAllocationsExceed100, got %v", err)
	}

	designation, _ := registry.GetDesignation("acct-001")
	if len(designation.Nominees) != 1 {
		t.Fatalf("expected the rejected add to leave the prior state untouched, got %d nominees", len(designation.Nominees))
	}
}

func TestAddNomineeUnsetsPriorOptOut(t *testing.T) {
	registry := NewNomineeDesignationRegistry()
	registry.SubmitNomination("acct-001", nil, true, testNow)

	designation, err := registry.AddNominee("acct-001", NomineeInput{
		FullName: "Alice", Relationship: "spouse", DateOfBirth: adultDob(), PercentageAllocation: 100,
	}, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if designation.IsOptedOutOfNomination {
		t.Fatal("expected adding a nominee to un-set the opt-out flag")
	}
}

func TestUpdateNomineeAppliesChangesAndRevalidates(t *testing.T) {
	registry := NewNomineeDesignationRegistry()
	initial, _ := registry.SubmitNomination("acct-001", []NomineeInput{
		{FullName: "Alice", Relationship: "spouse", DateOfBirth: adultDob(), PercentageAllocation: 50},
		{FullName: "Bob", Relationship: "child", DateOfBirth: adultDob(), PercentageAllocation: 50},
	}, false, testNow)
	targetId := initial.Nominees[0].NomineeId

	updated, err := registry.UpdateNominee("acct-001", targetId, NomineeInput{
		FullName: "Alice Renamed", Relationship: "spouse", DateOfBirth: adultDob(), PercentageAllocation: 30,
	}, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.TotalPercentageAllocated() != 80 {
		t.Fatalf("expected total 80 (30+50), got %d", updated.TotalPercentageAllocated())
	}
	if updated.IsComplete() {
		t.Fatal("expected 80%% total to no longer be complete")
	}
}

func TestUpdateNomineeRejectsExceeding100(t *testing.T) {
	registry := NewNomineeDesignationRegistry()
	initial, _ := registry.SubmitNomination("acct-001", []NomineeInput{
		{FullName: "Alice", Relationship: "spouse", DateOfBirth: adultDob(), PercentageAllocation: 50},
		{FullName: "Bob", Relationship: "child", DateOfBirth: adultDob(), PercentageAllocation: 50},
	}, false, testNow)
	targetId := initial.Nominees[0].NomineeId

	_, err := registry.UpdateNominee("acct-001", targetId, NomineeInput{
		FullName: "Alice", Relationship: "spouse", DateOfBirth: adultDob(), PercentageAllocation: 90,
	}, testNow)
	if err != ErrPercentageAllocationsExceed100 {
		t.Fatalf("expected ErrPercentageAllocationsExceed100, got %v", err)
	}
}

func TestUpdateNomineeUnknownIdReturnsNotFound(t *testing.T) {
	registry := NewNomineeDesignationRegistry()
	registry.SubmitNomination("acct-001", []NomineeInput{
		{FullName: "Alice", Relationship: "spouse", DateOfBirth: adultDob(), PercentageAllocation: 100},
	}, false, testNow)

	_, err := registry.UpdateNominee("acct-001", "nominee-does-not-exist", NomineeInput{
		FullName: "X", Relationship: "spouse", DateOfBirth: adultDob(), PercentageAllocation: 50,
	}, testNow)
	if err != ErrNomineeNotFound {
		t.Fatalf("expected ErrNomineeNotFound, got %v", err)
	}
}

func TestUpdateNomineeOnAccountWithNoDesignationReturnsNoDesignationFound(t *testing.T) {
	registry := NewNomineeDesignationRegistry()

	_, err := registry.UpdateNominee("acct-never-submitted", "whatever", NomineeInput{
		FullName: "X", Relationship: "spouse", DateOfBirth: adultDob(), PercentageAllocation: 50,
	}, testNow)
	if err != ErrNoDesignationFound {
		t.Fatalf("expected ErrNoDesignationFound, got %v", err)
	}
}

func TestRemoveNomineeAlwaysSucceedsAndMayBecomeIncomplete(t *testing.T) {
	registry := NewNomineeDesignationRegistry()
	initial, _ := registry.SubmitNomination("acct-001", []NomineeInput{
		{FullName: "Alice", Relationship: "spouse", DateOfBirth: adultDob(), PercentageAllocation: 50},
		{FullName: "Bob", Relationship: "child", DateOfBirth: adultDob(), PercentageAllocation: 50},
	}, false, testNow)
	targetId := initial.Nominees[0].NomineeId

	updated, err := registry.RemoveNominee("acct-001", targetId, testNow)
	if err != nil {
		t.Fatalf("unexpected error removing a nominee: %v", err)
	}
	if len(updated.Nominees) != 1 {
		t.Fatalf("expected 1 remaining nominee, got %d", len(updated.Nominees))
	}
	if updated.IsComplete() {
		t.Fatal("expected removal to leave the designation incomplete (50%% total)")
	}
}

func TestRemoveNomineeUnknownIdReturnsNotFound(t *testing.T) {
	registry := NewNomineeDesignationRegistry()
	registry.SubmitNomination("acct-001", []NomineeInput{
		{FullName: "Alice", Relationship: "spouse", DateOfBirth: adultDob(), PercentageAllocation: 100},
	}, false, testNow)

	_, err := registry.RemoveNominee("acct-001", "nominee-does-not-exist", testNow)
	if err != ErrNomineeNotFound {
		t.Fatalf("expected ErrNomineeNotFound, got %v", err)
	}
}

func TestGetDesignationReturnsFalseWhenNeverSubmitted(t *testing.T) {
	registry := NewNomineeDesignationRegistry()

	_, exists := registry.GetDesignation("acct-never-submitted")
	if exists {
		t.Fatal("expected no designation to exist")
	}
}

func TestComputeAgeInYearsHandlesBirthdayNotYetOccurredThisYear(t *testing.T) {
	// Born on a date later in the calendar year than testNow's month/day
	// — birthday hasn't happened yet this year, so age should be one less
	// than the naive year difference.
	dob := time.Date(testNow.Year()-18, 12, 31, 0, 0, 0, 0, time.UTC)
	age := computeAgeInYears(dob, testNow)
	if age != 17 {
		t.Fatalf("expected age 17 (birthday not yet reached), got %d", age)
	}
}
