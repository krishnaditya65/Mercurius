package passwordhashing

import "testing"

func TestCorrectPasswordVerifiesSuccessfully(t *testing.T) {
	encodedHash, hashError := HashPassword("correct horse battery staple")
	if hashError != nil {
		t.Fatalf("unexpected error hashing password: %v", hashError)
	}

	wasVerified, verifyError := VerifyPassword("correct horse battery staple", encodedHash)
	if verifyError != nil {
		t.Fatalf("unexpected error verifying password: %v", verifyError)
	}
	if !wasVerified {
		t.Fatal("expected the correct password to verify successfully")
	}
}

func TestWrongPasswordFailsVerification(t *testing.T) {
	encodedHash, _ := HashPassword("correct horse battery staple")

	wasVerified, verifyError := VerifyPassword("wrong password", encodedHash)
	if verifyError != nil {
		t.Fatalf("unexpected error verifying password: %v", verifyError)
	}
	if wasVerified {
		t.Fatal("expected an incorrect password to fail verification")
	}
}

func TestTwoHashesOfTheSamePasswordAreDifferentBecauseOfRandomSalt(t *testing.T) {
	firstHash, _ := HashPassword("same password")
	secondHash, _ := HashPassword("same password")

	if firstHash == secondHash {
		t.Fatal("expected two hashes of the same password to differ due to independent random salts")
	}

	// Both must still independently verify against the original password.
	firstVerified, _ := VerifyPassword("same password", firstHash)
	secondVerified, _ := VerifyPassword("same password", secondHash)
	if !firstVerified || !secondVerified {
		t.Fatal("expected both independently-salted hashes to verify against the original password")
	}
}

func TestMalformedStoredHashReturnsAnErrorRatherThanPanicking(t *testing.T) {
	_, verifyError := VerifyPassword("anything", "not-a-real-hash-format")
	if verifyError == nil {
		t.Fatal("expected an error for a malformed stored hash")
	}
}

func TestEmptyPasswordStillHashesAndVerifiesConsistently(t *testing.T) {
	// Not a recommendation to allow empty passwords (a real build should
	// reject them at the registration layer) — just confirming this
	// package itself doesn't panic or behave inconsistently on the edge
	// case; policy enforcement belongs elsewhere (internal/accountstore).
	encodedHash, hashError := HashPassword("")
	if hashError != nil {
		t.Fatalf("unexpected error hashing empty password: %v", hashError)
	}
	wasVerified, _ := VerifyPassword("", encodedHash)
	if !wasVerified {
		t.Fatal("expected an empty password to verify against its own hash")
	}
}
