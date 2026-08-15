package accountstore

import (
	"testing"

	"mercurius/auth/internal/jwtauth"
)

func TestRegisteredAccountCanAuthenticateWithTheSamePassword(t *testing.T) {
	store := NewAccountStore()

	accountIdentifier, registerError := store.RegisterAccount("jane@example.com", "correct horse battery staple", "", "")
	if registerError != nil {
		t.Fatalf("unexpected error registering: %v", registerError)
	}
	if accountIdentifier == "" {
		t.Fatal("expected a non-empty account identifier")
	}

	authenticatedIdentifier, role, authError := store.AuthenticateWithPassword("jane@example.com", "correct horse battery staple")
	if authError != nil {
		t.Fatalf("unexpected error authenticating: %v", authError)
	}
	if authenticatedIdentifier != accountIdentifier {
		t.Fatalf("expected authentication to return the same account identifier, got %q vs %q", authenticatedIdentifier, accountIdentifier)
	}
	if role != jwtauth.RoleRetail {
		t.Fatalf("expected default role %q, got %q", jwtauth.RoleRetail, role)
	}
}

func TestAuthenticationFailsWithTheWrongPassword(t *testing.T) {
	store := NewAccountStore()
	store.RegisterAccount("jane@example.com", "correct horse battery staple", "", "")

	_, _, authError := store.AuthenticateWithPassword("jane@example.com", "wrong password")
	if authError != ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials, got %v", authError)
	}
}

func TestAuthenticationFailsForAnUnregisteredEmail(t *testing.T) {
	store := NewAccountStore()

	_, _, authError := store.AuthenticateWithPassword("nobody@example.com", "anything")
	if authError != ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials (same error as wrong password, to avoid account enumeration), got %v", authError)
	}
}

func TestRegisteringTheSameEmailTwiceIsRejected(t *testing.T) {
	store := NewAccountStore()
	store.RegisterAccount("jane@example.com", "first-password", "", "")

	_, registerError := store.RegisterAccount("jane@example.com", "second-password", "", "")
	if registerError != ErrEmailAlreadyRegistered {
		t.Fatalf("expected ErrEmailAlreadyRegistered, got %v", registerError)
	}
}

func TestEmailMatchingIsCaseInsensitiveAndTrimsWhitespace(t *testing.T) {
	store := NewAccountStore()
	accountIdentifier, _ := store.RegisterAccount("  Jane@Example.com  ", "a-password", "", "")

	authenticatedIdentifier, _, authError := store.AuthenticateWithPassword("jane@example.com", "a-password")
	if authError != nil {
		t.Fatalf("expected case-insensitive/trimmed email to authenticate, got err=%v", authError)
	}
	if authenticatedIdentifier != accountIdentifier {
		t.Fatal("expected the normalized email to resolve to the same account")
	}

	_, duplicateRegisterError := store.RegisterAccount("JANE@EXAMPLE.COM", "another-password", "", "")
	if duplicateRegisterError != ErrEmailAlreadyRegistered {
		t.Fatalf("expected registering a case-variant of an existing email to be rejected, got %v", duplicateRegisterError)
	}
}

func TestTwoDifferentAccountsGetDifferentIdentifiers(t *testing.T) {
	store := NewAccountStore()
	firstIdentifier, _ := store.RegisterAccount("first@example.com", "password-one", "", "")
	secondIdentifier, _ := store.RegisterAccount("second@example.com", "password-two", "", "")

	if firstIdentifier == secondIdentifier {
		t.Fatal("expected two different accounts to get different identifiers")
	}
}

func TestOptionalCallerSuppliedAccountIdentifierIsHonored(t *testing.T) {
	store := NewAccountStore()

	accountIdentifier, registerError := store.RegisterAccount("demo@example.com", "demo-password", "acct-001", "")
	if registerError != nil {
		t.Fatalf("unexpected error registering with an explicit identifier: %v", registerError)
	}
	if accountIdentifier != "acct-001" {
		t.Fatalf("expected the caller-supplied identifier acct-001 to be honored, got %q", accountIdentifier)
	}

	authenticatedIdentifier, _, authError := store.AuthenticateWithPassword("demo@example.com", "demo-password")
	if authError != nil {
		t.Fatalf("unexpected error authenticating: %v", authError)
	}
	if authenticatedIdentifier != "acct-001" {
		t.Fatalf("expected authentication to return acct-001, got %q", authenticatedIdentifier)
	}
}

func TestDuplicateExplicitAccountIdentifierIsRejected(t *testing.T) {
	store := NewAccountStore()
	if _, registerError := store.RegisterAccount("first@example.com", "password-one", "acct-001", ""); registerError != nil {
		t.Fatalf("unexpected error on first registration: %v", registerError)
	}

	_, registerError := store.RegisterAccount("second@example.com", "password-two", "acct-001", "")
	if registerError != ErrAccountIdentifierAlreadyExists {
		t.Fatalf("expected ErrAccountIdentifierAlreadyExists, got %v", registerError)
	}
}

func TestOmittedAccountIdentifierStillAutoGenerates(t *testing.T) {
	store := NewAccountStore()

	accountIdentifier, registerError := store.RegisterAccount("real-user@example.com", "a-password", "", "")
	if registerError != nil {
		t.Fatalf("unexpected error registering: %v", registerError)
	}
	if accountIdentifier == "" || len(accountIdentifier) < len("acct-") {
		t.Fatalf("expected an auto-generated acct-<hex> identifier, got %q", accountIdentifier)
	}
}

func TestExplicitRoleIsHonoredAndDefaultsToRetail(t *testing.T) {
	store := NewAccountStore()

	store.RegisterAccount("retail@example.com", "password", "", "")
	store.RegisterAccount("admin@example.com", "password", "", jwtauth.RoleAdmin)

	_, retailRole, _ := store.AuthenticateWithPassword("retail@example.com", "password")
	if retailRole != jwtauth.RoleRetail {
		t.Fatalf("expected default role %q, got %q", jwtauth.RoleRetail, retailRole)
	}

	_, adminRole, _ := store.AuthenticateWithPassword("admin@example.com", "password")
	if adminRole != jwtauth.RoleAdmin {
		t.Fatalf("expected role %q, got %q", jwtauth.RoleAdmin, adminRole)
	}
}
