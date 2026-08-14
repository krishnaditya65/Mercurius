package jointholding

import "testing"

func TestRegisterIndividualAccount(t *testing.T) {
	registry := NewHoldingRegistry()

	structure, err := registry.RegisterIndividualAccount("acct-001", "Alice Trader")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if structure.HoldingType != HoldingTypeIndividual {
		t.Fatalf("expected HoldingTypeIndividual, got %v", structure.HoldingType)
	}
	if len(structure.Holders) != 1 {
		t.Fatalf("expected exactly 1 holder, got %d", len(structure.Holders))
	}
	primary, hasPrimary := structure.PrimaryHolder()
	if !hasPrimary || primary.FullName != "Alice Trader" {
		t.Fatalf("expected the sole holder to be primary, got %+v", primary)
	}
}

func TestRegisterIndividualAccountRejectsMissingFields(t *testing.T) {
	registry := NewHoldingRegistry()

	if _, err := registry.RegisterIndividualAccount("", "Alice"); err != ErrAccountIdentifierRequired {
		t.Fatalf("expected ErrAccountIdentifierRequired, got %v", err)
	}
	if _, err := registry.RegisterIndividualAccount("acct-001", ""); err != ErrSoleHolderFullNameRequired {
		t.Fatalf("expected ErrSoleHolderFullNameRequired, got %v", err)
	}
}

func TestRegisterJointAccountEitherOrSurvivorWithTwoHolders(t *testing.T) {
	registry := NewHoldingRegistry()

	structure, err := registry.RegisterJointAccount("acct-001", ModeEitherOrSurvivor, []string{"Alice", "Bob"}, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if structure.HoldingType != HoldingTypeJoint || structure.HoldingMode != ModeEitherOrSurvivor {
		t.Fatalf("unexpected structure: %+v", structure)
	}
	primary, _ := structure.PrimaryHolder()
	if primary.FullName != "Alice" {
		t.Fatalf("expected Alice as primary holder, got %+v", primary)
	}
}

func TestRegisterJointAccountEitherOrSurvivorRejectsMoreThanTwoHolders(t *testing.T) {
	registry := NewHoldingRegistry()

	_, err := registry.RegisterJointAccount("acct-001", ModeEitherOrSurvivor, []string{"Alice", "Bob", "Carol"}, 0)
	if err != ErrEitherOrSurvivorRequiresExactlyTwoHolders {
		t.Fatalf("expected ErrEitherOrSurvivorRequiresExactlyTwoHolders, got %v", err)
	}
}

func TestRegisterJointAccountAnyoneOrSurvivorRequiresAtLeastThree(t *testing.T) {
	registry := NewHoldingRegistry()

	_, err := registry.RegisterJointAccount("acct-001", ModeAnyoneOrSurvivor, []string{"Alice", "Bob"}, 0)
	if err != ErrAnyoneOrSurvivorRequiresAtLeastThreeHolders {
		t.Fatalf("expected ErrAnyoneOrSurvivorRequiresAtLeastThreeHolders, got %v", err)
	}

	structure, err := registry.RegisterJointAccount("acct-002", ModeAnyoneOrSurvivor, []string{"Alice", "Bob", "Carol"}, 2)
	if err != nil {
		t.Fatalf("unexpected error with 3 holders: %v", err)
	}
	primary, _ := structure.PrimaryHolder()
	if primary.FullName != "Carol" {
		t.Fatalf("expected Carol as primary, got %+v", primary)
	}
}

func TestRegisterJointAccountJointlyAcceptsTwoOrMore(t *testing.T) {
	registry := NewHoldingRegistry()

	if _, err := registry.RegisterJointAccount("acct-001", ModeJointly, []string{"Alice", "Bob"}, 0); err != nil {
		t.Fatalf("unexpected error with 2 holders: %v", err)
	}
	if _, err := registry.RegisterJointAccount("acct-002", ModeJointly, []string{"Alice", "Bob", "Carol", "Dave"}, 1); err != nil {
		t.Fatalf("unexpected error with 4 holders: %v", err)
	}
}

func TestRegisterJointAccountRejectsFewerThanTwoHolders(t *testing.T) {
	registry := NewHoldingRegistry()

	_, err := registry.RegisterJointAccount("acct-001", ModeJointly, []string{"Alice"}, 0)
	if err != ErrJointAccountRequiresAtLeastTwoHolders {
		t.Fatalf("expected ErrJointAccountRequiresAtLeastTwoHolders, got %v", err)
	}
}

func TestRegisterJointAccountRejectsInvalidMode(t *testing.T) {
	registry := NewHoldingRegistry()

	_, err := registry.RegisterJointAccount("acct-001", "NOT_A_REAL_MODE", []string{"Alice", "Bob"}, 0)
	if err != ErrInvalidHoldingMode {
		t.Fatalf("expected ErrInvalidHoldingMode, got %v", err)
	}
}

func TestRegisterJointAccountRejectsBlankHolderName(t *testing.T) {
	registry := NewHoldingRegistry()

	_, err := registry.RegisterJointAccount("acct-001", ModeJointly, []string{"Alice", ""}, 0)
	if err != ErrHolderFullNameRequired {
		t.Fatalf("expected ErrHolderFullNameRequired, got %v", err)
	}
}

func TestRegisterJointAccountRejectsOutOfRangePrimaryIndex(t *testing.T) {
	registry := NewHoldingRegistry()

	_, err := registry.RegisterJointAccount("acct-001", ModeJointly, []string{"Alice", "Bob"}, 5)
	if err != ErrPrimaryHolderIndexOutOfRange {
		t.Fatalf("expected ErrPrimaryHolderIndexOutOfRange, got %v", err)
	}
}

func TestAuthorizeOperationIndividualSoleHolderSufficient(t *testing.T) {
	registry := NewHoldingRegistry()
	structure, _ := registry.RegisterIndividualAccount("acct-001", "Alice")

	authorized, err := registry.AuthorizeOperation("acct-001", []string{structure.Holders[0].HolderId})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !authorized {
		t.Fatal("expected the sole holder's consent to authorize the operation")
	}
}

func TestAuthorizeOperationEitherOrSurvivorAnySingleHolderSufficient(t *testing.T) {
	registry := NewHoldingRegistry()
	structure, _ := registry.RegisterJointAccount("acct-001", ModeEitherOrSurvivor, []string{"Alice", "Bob"}, 0)

	authorized, err := registry.AuthorizeOperation("acct-001", []string{structure.Holders[1].HolderId})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !authorized {
		t.Fatal("expected any single EITHER_OR_SURVIVOR holder's consent to authorize the operation")
	}
}

func TestAuthorizeOperationJointlyRequiresAllHolders(t *testing.T) {
	registry := NewHoldingRegistry()
	structure, _ := registry.RegisterJointAccount("acct-001", ModeJointly, []string{"Alice", "Bob", "Carol"}, 0)

	_, err := registry.AuthorizeOperation("acct-001", []string{structure.Holders[0].HolderId, structure.Holders[1].HolderId})
	if err != ErrJointlyModeRequiresAllHoldersConsent {
		t.Fatalf("expected ErrJointlyModeRequiresAllHoldersConsent with 2 of 3 consents, got %v", err)
	}

	allIds := []string{structure.Holders[0].HolderId, structure.Holders[1].HolderId, structure.Holders[2].HolderId}
	authorized, err := registry.AuthorizeOperation("acct-001", allIds)
	if err != nil {
		t.Fatalf("unexpected error with all holders' consent: %v", err)
	}
	if !authorized {
		t.Fatal("expected all-holder consent to authorize a JOINTLY operation")
	}
}

func TestAuthorizeOperationRejectsUnknownHolderId(t *testing.T) {
	registry := NewHoldingRegistry()
	registry.RegisterJointAccount("acct-001", ModeEitherOrSurvivor, []string{"Alice", "Bob"}, 0)

	_, err := registry.AuthorizeOperation("acct-001", []string{"holder-does-not-exist"})
	if err != ErrConsentFromUnknownHolder {
		t.Fatalf("expected ErrConsentFromUnknownHolder, got %v", err)
	}
}

func TestAuthorizeOperationRejectsNoConsentsOrNoStructure(t *testing.T) {
	registry := NewHoldingRegistry()
	registry.RegisterIndividualAccount("acct-001", "Alice")

	if _, err := registry.AuthorizeOperation("acct-001", nil); err != ErrAtLeastOneConsentRequired {
		t.Fatalf("expected ErrAtLeastOneConsentRequired, got %v", err)
	}
	if _, err := registry.AuthorizeOperation("acct-never-registered", []string{"holder-x"}); err != ErrNoHoldingStructureFound {
		t.Fatalf("expected ErrNoHoldingStructureFound, got %v", err)
	}
}

func TestListAccountsByHoldingTypeIsSortedAndFiltered(t *testing.T) {
	registry := NewHoldingRegistry()
	registry.RegisterIndividualAccount("acct-b", "Alice")
	registry.RegisterIndividualAccount("acct-a", "Bob")
	registry.RegisterJointAccount("acct-c", ModeJointly, []string{"Carol", "Dave"}, 0)

	individualAccounts := registry.ListAccountsByHoldingType(HoldingTypeIndividual)
	if len(individualAccounts) != 2 || individualAccounts[0] != "acct-a" || individualAccounts[1] != "acct-b" {
		t.Fatalf("expected sorted [acct-a acct-b], got %v", individualAccounts)
	}

	jointAccounts := registry.ListAccountsByHoldingType(HoldingTypeJoint)
	if len(jointAccounts) != 1 || jointAccounts[0] != "acct-c" {
		t.Fatalf("expected [acct-c], got %v", jointAccounts)
	}
}

func TestGetHoldingStructureReturnsFalseWhenUnregistered(t *testing.T) {
	registry := NewHoldingRegistry()

	_, exists := registry.GetHoldingStructure("acct-never-registered")
	if exists {
		t.Fatal("expected no holding structure to exist")
	}
}
