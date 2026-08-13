package accountstore

import "testing"

func TestRegisteredAccountCanAuthenticateWithTheSamePassword(t *testing.T) {
	store := NewAccountStore()

	accountIdentifier, registerError := store.RegisterAccount("jane@example.com", "correct horse battery staple")
	if registerError != nil {
		t.Fatalf("unexpected error registering: %v", registerError)
	}
	if accountIdentifier == "" {
		t.Fatal("expected a non-empty account identifier")
	}

	authenticatedIdentifier, authError := store.AuthenticateWithPassword("jane@example.com", "correct horse battery staple")
	if authError != nil {
		t.Fatalf("unexpected error authenticating: %v", authError)
	}
	if authenticatedIdentifier != accountIdentifier {
		t.Fatalf("expected authentication to return the same account identifier, got %q vs %q", authenticatedIdentifier, accountIdentifier)
	}
}

func TestAuthenticationFailsWithTheWrongPassword(t *testing.T) {
	store := NewAccountStore()
	store.RegisterAccount("jane@example.com", "correct horse battery staple")

	_, authError := store.AuthenticateWithPassword("jane@example.com", "wrong password")
	if authError != ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials, got %v", authError)
	}
}

func TestAuthenticationFailsForAnUnregisteredEmail(t *testing.T) {
	store := NewAccountStore()

	_, authError := store.AuthenticateWithPassword("nobody@example.com", "anything")
	if authError != ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials (same error as wrong password, to avoid account enumeration), got %v", authError)
	}
}

func TestRegisteringTheSameEmailTwiceIsRejected(t *testing.T) {
	store := NewAccountStore()
	store.RegisterAccount("jane@example.com", "first-password")

	_, registerError := store.RegisterAccount("jane@example.com", "second-password")
	if registerError != ErrEmailAlreadyRegistered {
		t.Fatalf("expected ErrEmailAlreadyRegistered, got %v", registerError)
	}
}

func TestEmailMatchingIsCaseInsensitiveAndTrimsWhitespace(t *testing.T) {
	store := NewAccountStore()
	accountIdentifier, _ := store.RegisterAccount("  Jane@Example.com  ", "a-password")

	authenticatedIdentifier, authError := store.AuthenticateWithPassword("jane@example.com", "a-password")
	if authError != nil {
		t.Fatalf("expected case-insensitive/trimmed email to authenticate, got err=%v", authError)
	}
	if authenticatedIdentifier != accountIdentifier {
		t.Fatal("expected the normalized email to resolve to the same account")
	}

	_, duplicateRegisterError := store.RegisterAccount("JANE@EXAMPLE.COM", "another-password")
	if duplicateRegisterError != ErrEmailAlreadyRegistered {
		t.Fatalf("expected registering a case-variant of an existing email to be rejected, got %v", duplicateRegisterError)
	}
}

func TestTwoDifferentAccountsGetDifferentIdentifiers(t *testing.T) {
	store := NewAccountStore()
	firstIdentifier, _ := store.RegisterAccount("first@example.com", "password-one")
	secondIdentifier, _ := store.RegisterAccount("second@example.com", "password-two")

	if firstIdentifier == secondIdentifier {
		t.Fatal("expected two different accounts to get different identifiers")
	}
}
