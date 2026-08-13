package bankverification

import "testing"

func TestNewVerificationStartsPending(t *testing.T) {
	verifier := NewBankAccountVerifier()

	verificationId, err := verifier.InitiateVerification("acct-001", "1234567890", "HDFC0001234")
	if err != nil {
		t.Fatalf("unexpected error initiating verification: %v", err)
	}
	if verificationId == "" {
		t.Fatal("expected a non-empty verification id")
	}

	status := verifier.QueryVerificationStatus(verificationId)
	if status != StatusPending {
		t.Fatalf("expected StatusPending, got %v", status)
	}
}

func TestCorrectAmountVerifies(t *testing.T) {
	verifier := NewBankAccountVerifier()
	verificationId, _ := verifier.InitiateVerification("acct-001", "1234567890", "HDFC0001234")

	actualAmount, wasFound := verifier.PeekAtMicroDepositAmountForTesting(verificationId)
	if !wasFound {
		t.Fatal("expected to find the deposited amount for a just-created verification")
	}

	resultStatus := verifier.ConfirmMicroDepositAmount(verificationId, actualAmount)
	if resultStatus != StatusVerified {
		t.Fatalf("expected StatusVerified for the correct amount, got %v", resultStatus)
	}
	if verifier.QueryVerificationStatus(verificationId) != StatusVerified {
		t.Fatal("expected the status to persist as VERIFIED on subsequent queries")
	}
}

func TestWrongAmountConsumesAnAttemptButStaysPending(t *testing.T) {
	verifier := NewBankAccountVerifier()
	verificationId, _ := verifier.InitiateVerification("acct-001", "1234567890", "HDFC0001234")
	actualAmount, _ := verifier.PeekAtMicroDepositAmountForTesting(verificationId)

	wrongAmount := actualAmount + 1
	if wrongAmount > maxMicroDepositMinorUnits {
		wrongAmount = actualAmount - 1
	}

	resultStatus := verifier.ConfirmMicroDepositAmount(verificationId, wrongAmount)
	if resultStatus != StatusPending {
		t.Fatalf("expected a single wrong guess to stay PENDING (attempts remain), got %v", resultStatus)
	}
}

func TestExhaustingAllAttemptsLocksTheVerification(t *testing.T) {
	verifier := NewBankAccountVerifier()
	verificationId, _ := verifier.InitiateVerification("acct-001", "1234567890", "HDFC0001234")
	actualAmount, _ := verifier.PeekAtMicroDepositAmountForTesting(verificationId)

	wrongAmount := actualAmount + 1
	if wrongAmount > maxMicroDepositMinorUnits {
		wrongAmount = actualAmount - 1
	}

	var finalStatus VerificationStatus
	for attemptIndex := 0; attemptIndex < maxConfirmationAttempts; attemptIndex++ {
		finalStatus = verifier.ConfirmMicroDepositAmount(verificationId, wrongAmount)
	}

	if finalStatus != StatusFailedLocked {
		t.Fatalf("expected StatusFailedLocked after exhausting %d attempts, got %v", maxConfirmationAttempts, finalStatus)
	}

	// A subsequent guess — even the CORRECT one — must not un-lock it.
	statusAfterLock := verifier.ConfirmMicroDepositAmount(verificationId, actualAmount)
	if statusAfterLock != StatusFailedLocked {
		t.Fatalf("expected a locked verification to stay locked even against the correct amount, got %v", statusAfterLock)
	}
}

func TestUnknownVerificationIdReturnsNotFound(t *testing.T) {
	verifier := NewBankAccountVerifier()

	if verifier.QueryVerificationStatus("never-issued") != StatusNotFound {
		t.Fatal("expected StatusNotFound for an unknown verification id")
	}
	if verifier.ConfirmMicroDepositAmount("never-issued", 42) != StatusNotFound {
		t.Fatal("expected StatusNotFound when confirming an unknown verification id")
	}
}

func TestLatestVerificationIdForAccountReturnsTheMostRecentOne(t *testing.T) {
	verifier := NewBankAccountVerifier()
	first, _ := verifier.InitiateVerification("acct-001", "1111111111", "HDFC0001111")
	second, _ := verifier.InitiateVerification("acct-001", "2222222222", "HDFC0002222")

	latest, wasFound := verifier.LatestVerificationIdForAccount("acct-001")
	if !wasFound {
		t.Fatal("expected to find a latest verification id")
	}
	if latest != second {
		t.Fatalf("expected the latest verification (%q) to be returned, got %q (first was %q)", second, latest, first)
	}
}

func TestTwoDifferentVerificationsGetDifferentRandomAmountsMostOfTheTime(t *testing.T) {
	// Not a strict guarantee (the range is only 1-99, a collision is
	// plausible) — just a sanity check that InitiateVerification isn't
	// deterministically returning the same amount every time by running
	// several and confirming at least SOME variation.
	verifier := NewBankAccountVerifier()
	seenAmounts := make(map[int64]bool)
	for i := 0; i < 20; i++ {
		verificationId, _ := verifier.InitiateVerification("acct-001", "1234567890", "HDFC0001234")
		amount, _ := verifier.PeekAtMicroDepositAmountForTesting(verificationId)
		if amount < minMicroDepositMinorUnits || amount > maxMicroDepositMinorUnits {
			t.Fatalf("amount %d is outside the documented [%d, %d] range", amount, minMicroDepositMinorUnits, maxMicroDepositMinorUnits)
		}
		seenAmounts[amount] = true
	}
	if len(seenAmounts) < 2 {
		t.Fatal("expected at least some variation across 20 generated amounts")
	}
}
