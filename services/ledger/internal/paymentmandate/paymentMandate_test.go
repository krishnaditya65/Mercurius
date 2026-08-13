package paymentmandate

import (
	"testing"
	"time"

	"mercurius/ledger/internal/doubleentry"
	"mercurius/ledger/internal/fundsegregation"
)

var testNow = time.Unix(1_700_000_000, 0)

func newTestRegistry(t *testing.T, fundedAmount int64) (*PaymentMandateRegistry, *doubleentry.InMemoryDoubleEntryLedgerBook, *fundsegregation.SegregationGuard) {
	t.Helper()
	ledgerBook := doubleentry.NewInMemoryDoubleEntryLedgerBookWithAccounts([]string{
		"acct-001", "acct-002", "firm-clearing-acct", "client-money-custody-pool", "external-cash-suspense",
	})
	segregationGuard := fundsegregation.NewSegregationGuard(
		ledgerBook,
		"client-money-custody-pool",
		"external-cash-suspense",
		[]string{"acct-001", "acct-002"},
		[]string{"firm-clearing-acct"},
	)
	if fundedAmount > 0 {
		if err := segregationGuard.PostClientMoneyMovement("acct-001", fundedAmount, "test funding"); err != nil {
			t.Fatalf("test setup: failed to fund account: %v", err)
		}
	}
	registry := NewPaymentMandateRegistry(segregationGuard)
	return registry, ledgerBook, segregationGuard
}

func TestRegisterMandateStartsActive(t *testing.T) {
	registry, _, _ := newTestRegistry(t, 100_000)

	mandate, err := registry.RegisterMandate("acct-001", 5_000, FrequencyMonthly, testNow, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mandate.Status != MandateStatusActive {
		t.Fatalf("expected ACTIVE, got %v", mandate.Status)
	}
	if mandate.SuccessfulSweepCount != 0 {
		t.Fatalf("expected 0 successful sweeps at registration, got %d", mandate.SuccessfulSweepCount)
	}
}

func TestRegisterMandateRejectsNonPositiveAmount(t *testing.T) {
	registry, _, _ := newTestRegistry(t, 100_000)
	_, err := registry.RegisterMandate("acct-001", 0, FrequencyMonthly, testNow, testNow)
	if err != ErrInvalidMandateAmount {
		t.Fatalf("expected ErrInvalidMandateAmount for zero, got %v", err)
	}
	_, err = registry.RegisterMandate("acct-001", -500, FrequencyMonthly, testNow, testNow)
	if err != ErrInvalidMandateAmount {
		t.Fatalf("expected ErrInvalidMandateAmount for negative, got %v", err)
	}
}

func TestRegisterMandateRejectsInvalidFrequency(t *testing.T) {
	registry, _, _ := newTestRegistry(t, 100_000)
	_, err := registry.RegisterMandate("acct-001", 5_000, MandateFrequency("YEARLY"), testNow, testNow)
	if err != ErrInvalidMandateFrequency {
		t.Fatalf("expected ErrInvalidMandateFrequency, got %v", err)
	}
}

func TestRegisterMandateRejectsMissingAccountIdentifier(t *testing.T) {
	registry, _, _ := newTestRegistry(t, 100_000)
	_, err := registry.RegisterMandate("", 5_000, FrequencyMonthly, testNow, testNow)
	if err != ErrMissingAccountIdentifier {
		t.Fatalf("expected ErrMissingAccountIdentifier, got %v", err)
	}
}

func TestSweepDueMandatesSkipsAMandateBeforeItsNextDebitDate(t *testing.T) {
	registry, ledgerBook, _ := newTestRegistry(t, 100_000)
	registry.RegisterMandate("acct-001", 5_000, FrequencyMonthly, testNow.Add(24*time.Hour), testNow)

	results := registry.SweepDueMandates(testNow) // before the debit date
	if len(results) != 0 {
		t.Fatalf("expected no mandates swept before their due date, got %d", len(results))
	}

	balance, _ := ledgerBook.CurrentBalanceInMinorUnits("acct-001")
	if balance != 100_000 {
		t.Fatalf("expected balance untouched, got %d", balance)
	}
}

func TestSweepDueMandatesActuallyDebitsTheAccountAndAdvancesNextDebitDate(t *testing.T) {
	registry, ledgerBook, _ := newTestRegistry(t, 100_000)
	mandate, _ := registry.RegisterMandate("acct-001", 5_000, FrequencyMonthly, testNow, testNow)

	results := registry.SweepDueMandates(testNow)
	if len(results) != 1 || !results[0].WasPosted || results[0].MandateId != mandate.MandateId {
		t.Fatalf("expected exactly one successful sweep, got %+v", results)
	}

	balance, _ := ledgerBook.CurrentBalanceInMinorUnits("acct-001")
	if balance != 95_000 {
		t.Fatalf("expected the mandate debit to actually reduce the balance to 95000, got %d", balance)
	}

	reloaded, _ := registry.LookupMandate(mandate.MandateId)
	expectedNextDebitDate := testNow.AddDate(0, 1, 0)
	if !reloaded.NextDebitDate.Equal(expectedNextDebitDate) {
		t.Fatalf("expected NextDebitDate advanced to %v, got %v", expectedNextDebitDate, reloaded.NextDebitDate)
	}
	if reloaded.SuccessfulSweepCount != 1 {
		t.Fatalf("expected SuccessfulSweepCount 1, got %d", reloaded.SuccessfulSweepCount)
	}
}

func TestSweepDueMandatesDoesNotSweepTwiceInTheSameSweepBeforeTheNextPeriod(t *testing.T) {
	registry, ledgerBook, _ := newTestRegistry(t, 100_000)
	registry.RegisterMandate("acct-001", 5_000, FrequencyDaily, testNow, testNow)

	registry.SweepDueMandates(testNow)
	// A second sweep at the exact same instant should find nothing due,
	// since NextDebitDate was already advanced a day forward.
	results := registry.SweepDueMandates(testNow)
	if len(results) != 0 {
		t.Fatalf("expected nothing due on an immediate re-sweep, got %d", len(results))
	}

	balance, _ := ledgerBook.CurrentBalanceInMinorUnits("acct-001")
	if balance != 95_000 {
		t.Fatalf("expected exactly one debit applied, got balance %d", balance)
	}
}

func TestSweepDueMandatesEventuallySweepsAgainOnceTheNextPeriodArrives(t *testing.T) {
	registry, ledgerBook, _ := newTestRegistry(t, 100_000)
	registry.RegisterMandate("acct-001", 5_000, FrequencyDaily, testNow, testNow)

	registry.SweepDueMandates(testNow)
	registry.SweepDueMandates(testNow.Add(24 * time.Hour))

	balance, _ := ledgerBook.CurrentBalanceInMinorUnits("acct-001")
	if balance != 90_000 {
		t.Fatalf("expected two debits applied across two due periods, got balance %d", balance)
	}
}

func TestPauseMandatePreventsFurtherSweeps(t *testing.T) {
	registry, ledgerBook, _ := newTestRegistry(t, 100_000)
	mandate, _ := registry.RegisterMandate("acct-001", 5_000, FrequencyDaily, testNow, testNow)

	paused, err := registry.PauseMandate(mandate.MandateId)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if paused.Status != MandateStatusPaused {
		t.Fatalf("expected PAUSED, got %v", paused.Status)
	}

	results := registry.SweepDueMandates(testNow)
	if len(results) != 0 {
		t.Fatalf("expected a paused mandate to be skipped, got %d results", len(results))
	}
	balance, _ := ledgerBook.CurrentBalanceInMinorUnits("acct-001")
	if balance != 100_000 {
		t.Fatalf("expected balance untouched while paused, got %d", balance)
	}
}

func TestResumeMandateAllowsSweepingAgain(t *testing.T) {
	registry, ledgerBook, _ := newTestRegistry(t, 100_000)
	mandate, _ := registry.RegisterMandate("acct-001", 5_000, FrequencyDaily, testNow, testNow)
	registry.PauseMandate(mandate.MandateId)

	resumed, err := registry.ResumeMandate(mandate.MandateId)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resumed.Status != MandateStatusActive {
		t.Fatalf("expected ACTIVE after resume, got %v", resumed.Status)
	}

	results := registry.SweepDueMandates(testNow)
	if len(results) != 1 || !results[0].WasPosted {
		t.Fatalf("expected the resumed mandate to sweep successfully, got %+v", results)
	}
	balance, _ := ledgerBook.CurrentBalanceInMinorUnits("acct-001")
	if balance != 95_000 {
		t.Fatalf("expected the debit to apply after resuming, got %d", balance)
	}
}

func TestCancelMandatePermanentlyPreventsSweepsEvenAfterResumeAttempt(t *testing.T) {
	registry, ledgerBook, _ := newTestRegistry(t, 100_000)
	mandate, _ := registry.RegisterMandate("acct-001", 5_000, FrequencyDaily, testNow, testNow)

	cancelled, err := registry.CancelMandate(mandate.MandateId)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cancelled.Status != MandateStatusCancelled {
		t.Fatalf("expected CANCELLED, got %v", cancelled.Status)
	}

	if _, err := registry.ResumeMandate(mandate.MandateId); err != ErrMandateNotResumable {
		t.Fatalf("expected ErrMandateNotResumable, got %v", err)
	}
	if _, err := registry.PauseMandate(mandate.MandateId); err != ErrMandateNotPausable {
		t.Fatalf("expected ErrMandateNotPausable, got %v", err)
	}

	results := registry.SweepDueMandates(testNow)
	if len(results) != 0 {
		t.Fatalf("expected a cancelled mandate to never sweep, got %d results", len(results))
	}
	balance, _ := ledgerBook.CurrentBalanceInMinorUnits("acct-001")
	if balance != 100_000 {
		t.Fatalf("expected balance untouched after cancellation, got %d", balance)
	}
}

func TestCancellingAnAlreadyCancelledMandateFails(t *testing.T) {
	registry, _, _ := newTestRegistry(t, 100_000)
	mandate, _ := registry.RegisterMandate("acct-001", 5_000, FrequencyDaily, testNow, testNow)
	registry.CancelMandate(mandate.MandateId)

	_, err := registry.CancelMandate(mandate.MandateId)
	if err != ErrMandateAlreadyCancelled {
		t.Fatalf("expected ErrMandateAlreadyCancelled, got %v", err)
	}
}

func TestPausingAnUnknownMandateFails(t *testing.T) {
	registry, _, _ := newTestRegistry(t, 100_000)
	_, err := registry.PauseMandate("never-registered")
	if err != ErrMandateNotFound {
		t.Fatalf("expected ErrMandateNotFound, got %v", err)
	}
}

func TestSweepDueMandatesReportsAFailedDebitWithoutStoppingOtherMandates(t *testing.T) {
	registry, ledgerBook, _ := newTestRegistry(t, 100_000)
	// Fund acct-002 too, but this mandate targets an account that is
	// NOT classified CLIENT (firm-clearing-acct exists in the ledger
	// but isn't in the CLIENT list) so its debit will fail.
	failingMandate, _ := registry.RegisterMandate("firm-clearing-acct", 1_000, FrequencyDaily, testNow, testNow)
	goodMandate, _ := registry.RegisterMandate("acct-001", 5_000, FrequencyDaily, testNow, testNow)

	results := registry.SweepDueMandates(testNow)
	if len(results) != 2 {
		t.Fatalf("expected both mandates attempted, got %d", len(results))
	}

	var sawFailure, sawSuccess bool
	for _, result := range results {
		if result.MandateId == failingMandate.MandateId {
			if result.WasPosted {
				t.Fatal("expected the unclassified-account mandate to fail")
			}
			if result.Error == "" {
				t.Fatal("expected a non-empty error message on the failed sweep")
			}
			sawFailure = true
		}
		if result.MandateId == goodMandate.MandateId {
			if !result.WasPosted {
				t.Fatal("expected the good mandate to succeed despite the other one failing")
			}
			sawSuccess = true
		}
	}
	if !sawFailure || !sawSuccess {
		t.Fatalf("expected to observe both a failure and a success, got %+v", results)
	}

	balance, _ := ledgerBook.CurrentBalanceInMinorUnits("acct-001")
	if balance != 95_000 {
		t.Fatalf("expected the successful mandate's debit to still apply, got %d", balance)
	}
}

func TestMandatesForAccountReturnsOnlyThatAccountsMandatesInRegisteredOrder(t *testing.T) {
	registry, _, _ := newTestRegistry(t, 100_000)
	first, _ := registry.RegisterMandate("acct-001", 1_000, FrequencyMonthly, testNow, testNow)
	second, _ := registry.RegisterMandate("acct-001", 2_000, FrequencyMonthly, testNow, testNow.Add(time.Minute))
	registry.RegisterMandate("acct-002", 3_000, FrequencyMonthly, testNow, testNow)

	mandates := registry.MandatesForAccount("acct-001")
	if len(mandates) != 2 {
		t.Fatalf("expected 2 mandates for acct-001, got %d", len(mandates))
	}
	if mandates[0].MandateId != first.MandateId || mandates[1].MandateId != second.MandateId {
		t.Fatal("expected mandates ordered by RegisteredAt")
	}
}

func TestLookupMandateReturnsNotFoundForUnknownId(t *testing.T) {
	registry, _, _ := newTestRegistry(t, 100_000)
	_, wasFound := registry.LookupMandate("never-registered")
	if wasFound {
		t.Fatal("expected not found for an unknown mandate id")
	}
}

func TestWeeklyFrequencyAdvancesBySevenDays(t *testing.T) {
	registry, _, _ := newTestRegistry(t, 100_000)
	mandate, _ := registry.RegisterMandate("acct-001", 1_000, FrequencyWeekly, testNow, testNow)
	registry.SweepDueMandates(testNow)

	reloaded, _ := registry.LookupMandate(mandate.MandateId)
	expected := testNow.AddDate(0, 0, 7)
	if !reloaded.NextDebitDate.Equal(expected) {
		t.Fatalf("expected NextDebitDate advanced by 7 days to %v, got %v", expected, reloaded.NextDebitDate)
	}
}
