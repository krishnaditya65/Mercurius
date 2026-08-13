package sipscheduler

import (
	"testing"
	"time"

	"mercurius/mutualFunds/internal/amcrouting"
	"mercurius/mutualFunds/internal/fundcatalog"
)

const testSchemeId = "MF-EQ-BLUECHIP001"

func newTestScheduler() (*SipScheduler, *fundcatalog.FundCatalog, *amcrouting.AmcOrderRouter) {
	catalog := fundcatalog.NewFundCatalog()
	router := amcrouting.NewAmcOrderRouter(catalog, 0)
	return NewSipScheduler(catalog, router), catalog, router
}

func mustParseDate(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, parseError := time.Parse(time.RFC3339, value)
	if parseError != nil {
		t.Fatalf("failed to parse test date %q: %v", value, parseError)
	}
	return parsed
}

func TestRegisterSipRejectsNonPositiveAmount(t *testing.T) {
	scheduler, _, _ := newTestScheduler()
	startDate := mustParseDate(t, "2024-01-15T00:00:00Z")

	if _, registerError := scheduler.RegisterSip("acct-1", testSchemeId, 0, FrequencyMonthly, startDate, 0); registerError != ErrInvalidAmount {
		t.Errorf("expected ErrInvalidAmount, got %v", registerError)
	}
}

func TestRegisterSipRejectsUnsupportedFrequency(t *testing.T) {
	scheduler, _, _ := newTestScheduler()
	startDate := mustParseDate(t, "2024-01-15T00:00:00Z")

	if _, registerError := scheduler.RegisterSip("acct-1", testSchemeId, 500000, "WEEKLY", startDate, 0); registerError != ErrUnsupportedFrequency {
		t.Errorf("expected ErrUnsupportedFrequency, got %v", registerError)
	}
}

func TestRegisterSipRejectsUnknownScheme(t *testing.T) {
	scheduler, _, _ := newTestScheduler()
	startDate := mustParseDate(t, "2024-01-15T00:00:00Z")

	if _, registerError := scheduler.RegisterSip("acct-1", "MF-DOES-NOT-EXIST", 500000, FrequencyMonthly, startDate, 0); registerError != ErrUnknownScheme {
		t.Errorf("expected ErrUnknownScheme, got %v", registerError)
	}
}

func TestRegisterSipRejectsMissingAccountIdentifier(t *testing.T) {
	scheduler, _, _ := newTestScheduler()
	startDate := mustParseDate(t, "2024-01-15T00:00:00Z")

	if _, registerError := scheduler.RegisterSip("", testSchemeId, 500000, FrequencyMonthly, startDate, 0); registerError != ErrMissingAccountIdentifier {
		t.Errorf("expected ErrMissingAccountIdentifier, got %v", registerError)
	}
}

func TestRegisterSipRejectsZeroStartDate(t *testing.T) {
	scheduler, _, _ := newTestScheduler()

	if _, registerError := scheduler.RegisterSip("acct-1", testSchemeId, 500000, FrequencyMonthly, time.Time{}, 0); registerError != ErrZeroStartDate {
		t.Errorf("expected ErrZeroStartDate, got %v", registerError)
	}
}

func TestRegisterSipRejectsOutOfRangeStepUpPercent(t *testing.T) {
	scheduler, _, _ := newTestScheduler()
	startDate := mustParseDate(t, "2024-01-15T00:00:00Z")

	if _, registerError := scheduler.RegisterSip("acct-1", testSchemeId, 500000, FrequencyMonthly, startDate, -1); registerError != ErrInvalidStepUpPercent {
		t.Errorf("expected ErrInvalidStepUpPercent for negative percent, got %v", registerError)
	}
	if _, registerError := scheduler.RegisterSip("acct-1", testSchemeId, 500000, FrequencyMonthly, startDate, 101); registerError != ErrInvalidStepUpPercent {
		t.Errorf("expected ErrInvalidStepUpPercent for >100 percent, got %v", registerError)
	}
}

func TestRegisterSipHappyPathFirstInstallmentDueOnStartDate(t *testing.T) {
	scheduler, _, _ := newTestScheduler()
	startDate := mustParseDate(t, "2024-01-15T00:00:00Z")

	sip, registerError := scheduler.RegisterSip("acct-1", testSchemeId, 500000, FrequencyMonthly, startDate, 0)
	if registerError != nil {
		t.Fatalf("unexpected error: %v", registerError)
	}
	if sip.Status != StatusActive {
		t.Errorf("expected new SIP to be ACTIVE, got %s", sip.Status)
	}
	if !sip.NextDueDate.Equal(startDate) {
		t.Errorf("expected first installment due exactly on startDate, got %v", sip.NextDueDate)
	}
}

func TestSweepDueSipsExecutesDueInstallmentAndAdvancesNextDueDate(t *testing.T) {
	scheduler, _, router := newTestScheduler()
	startDate := mustParseDate(t, "2024-01-15T00:00:00Z")

	sip, _ := scheduler.RegisterSip("acct-1", testSchemeId, 500000, FrequencyMonthly, startDate, 0)

	executed, failed := scheduler.SweepDueSips(startDate)
	if len(failed) != 0 {
		t.Fatalf("expected no failures, got %v", failed)
	}
	if len(executed) != 1 || executed[0].SipId != sip.SipId {
		t.Fatalf("expected exactly the one due SIP executed, got %+v", executed)
	}
	if executed[0].InstallmentAmountInMinorUnits != 500000 {
		t.Errorf("expected installment amount 500000, got %d", executed[0].InstallmentAmountInMinorUnits)
	}

	expectedNextDueDate := startDate.AddDate(0, 1, 0)
	if !executed[0].NewNextDueDate.Equal(expectedNextDueDate) {
		t.Errorf("expected next due date %v, got %v", expectedNextDueDate, executed[0].NewNextDueDate)
	}

	updatedSip, _ := scheduler.LookupSip(sip.SipId)
	if !updatedSip.NextDueDate.Equal(expectedNextDueDate) {
		t.Errorf("expected stored SIP's next due date to be advanced to %v, got %v", expectedNextDueDate, updatedSip.NextDueDate)
	}
	if updatedSip.InstallmentsExecuted != 1 {
		t.Errorf("expected InstallmentsExecuted=1, got %d", updatedSip.InstallmentsExecuted)
	}

	// A real purchase order should have been routed through amcrouting.
	orders := router.OrdersForAccount("acct-1")
	if len(orders) != 1 || orders[0].OrderId != executed[0].OrderId {
		t.Errorf("expected the sweep to have routed a purchase order through amcrouting, got %+v", orders)
	}
}

// TestSweepDueSipsIsIdempotentBeforeNextDueDate proves sweeping twice in a
// row at the same "now" (before the next installment is due) executes
// the installment only once.
func TestSweepDueSipsIsIdempotentBeforeNextDueDate(t *testing.T) {
	scheduler, _, _ := newTestScheduler()
	startDate := mustParseDate(t, "2024-01-15T00:00:00Z")

	scheduler.RegisterSip("acct-1", testSchemeId, 500000, FrequencyMonthly, startDate, 0)

	firstSweepExecuted, _ := scheduler.SweepDueSips(startDate)
	if len(firstSweepExecuted) != 1 {
		t.Fatalf("expected first sweep to execute exactly 1 installment, got %d", len(firstSweepExecuted))
	}

	secondSweepExecuted, secondSweepFailed := scheduler.SweepDueSips(startDate)
	if len(secondSweepExecuted) != 0 || len(secondSweepFailed) != 0 {
		t.Errorf("expected second sweep at the same 'now' to do nothing, got executed=%v failed=%v", secondSweepExecuted, secondSweepFailed)
	}

	// Also confirm a sweep for a time still before the second installment
	// is due (one day later, nowhere near a month later) does nothing.
	thirdSweepExecuted, _ := scheduler.SweepDueSips(startDate.AddDate(0, 0, 1))
	if len(thirdSweepExecuted) != 0 {
		t.Errorf("expected no installment due just 1 day later, got %+v", thirdSweepExecuted)
	}
}

func TestPauseSipPreventsSubsequentSweepFromExecutingIt(t *testing.T) {
	scheduler, _, _ := newTestScheduler()
	startDate := mustParseDate(t, "2024-01-15T00:00:00Z")

	sip, _ := scheduler.RegisterSip("acct-1", testSchemeId, 500000, FrequencyMonthly, startDate, 0)

	if _, pauseError := scheduler.PauseSip(sip.SipId); pauseError != nil {
		t.Fatalf("unexpected error pausing: %v", pauseError)
	}

	executed, _ := scheduler.SweepDueSips(startDate)
	if len(executed) != 0 {
		t.Errorf("expected a PAUSED SIP to not execute on sweep, got %+v", executed)
	}

	pausedSip, _ := scheduler.LookupSip(sip.SipId)
	if pausedSip.Status != StatusPaused {
		t.Errorf("expected SIP status PAUSED, got %s", pausedSip.Status)
	}
	if pausedSip.InstallmentsExecuted != 0 {
		t.Errorf("expected 0 installments executed while paused, got %d", pausedSip.InstallmentsExecuted)
	}
}

func TestPauseSipRejectsWhenNotActive(t *testing.T) {
	scheduler, _, _ := newTestScheduler()
	startDate := mustParseDate(t, "2024-01-15T00:00:00Z")
	sip, _ := scheduler.RegisterSip("acct-1", testSchemeId, 500000, FrequencyMonthly, startDate, 0)

	scheduler.PauseSip(sip.SipId)
	if _, pauseError := scheduler.PauseSip(sip.SipId); pauseError != ErrSipNotActive {
		t.Errorf("expected ErrSipNotActive pausing an already-paused SIP, got %v", pauseError)
	}
}

func TestResumeSipAllowsSweepToPickBackUpFromFrozenSchedule(t *testing.T) {
	scheduler, _, _ := newTestScheduler()
	startDate := mustParseDate(t, "2024-01-15T00:00:00Z")
	sip, _ := scheduler.RegisterSip("acct-1", testSchemeId, 500000, FrequencyMonthly, startDate, 0)

	scheduler.PauseSip(sip.SipId)
	// Time passes while paused — a sweep well after startDate still does nothing.
	scheduler.SweepDueSips(startDate.AddDate(0, 2, 0))

	if _, resumeError := scheduler.ResumeSip(sip.SipId); resumeError != nil {
		t.Fatalf("unexpected error resuming: %v", resumeError)
	}

	// Now the next sweep executes the (still overdue) installment exactly
	// once — no catch-up backfilling for the months it was paused.
	executed, _ := scheduler.SweepDueSips(startDate.AddDate(0, 2, 0))
	if len(executed) != 1 {
		t.Fatalf("expected exactly 1 installment executed on resume, got %d", len(executed))
	}

	resumedSip, _ := scheduler.LookupSip(sip.SipId)
	if resumedSip.InstallmentsExecuted != 1 {
		t.Errorf("expected exactly 1 installment ever executed (no backfill), got %d", resumedSip.InstallmentsExecuted)
	}
}

func TestResumeSipRejectsWhenNotPaused(t *testing.T) {
	scheduler, _, _ := newTestScheduler()
	startDate := mustParseDate(t, "2024-01-15T00:00:00Z")
	sip, _ := scheduler.RegisterSip("acct-1", testSchemeId, 500000, FrequencyMonthly, startDate, 0)

	if _, resumeError := scheduler.ResumeSip(sip.SipId); resumeError != ErrSipNotPaused {
		t.Errorf("expected ErrSipNotPaused resuming an ACTIVE SIP, got %v", resumeError)
	}
}

func TestCancelSipIsTerminalAndPreventsFurtherMutation(t *testing.T) {
	scheduler, _, _ := newTestScheduler()
	startDate := mustParseDate(t, "2024-01-15T00:00:00Z")
	sip, _ := scheduler.RegisterSip("acct-1", testSchemeId, 500000, FrequencyMonthly, startDate, 0)

	if _, cancelError := scheduler.CancelSip(sip.SipId); cancelError != nil {
		t.Fatalf("unexpected error cancelling: %v", cancelError)
	}

	cancelledSip, _ := scheduler.LookupSip(sip.SipId)
	if cancelledSip.Status != StatusCancelled {
		t.Errorf("expected CANCELLED, got %s", cancelledSip.Status)
	}

	if _, resumeError := scheduler.ResumeSip(sip.SipId); resumeError != ErrSipAlreadyCancelled {
		t.Errorf("expected ErrSipAlreadyCancelled trying to resume a cancelled SIP, got %v", resumeError)
	}
	if _, pauseError := scheduler.PauseSip(sip.SipId); pauseError != ErrSipAlreadyCancelled {
		t.Errorf("expected ErrSipAlreadyCancelled trying to pause a cancelled SIP, got %v", pauseError)
	}
	if _, cancelAgainError := scheduler.CancelSip(sip.SipId); cancelAgainError != ErrSipAlreadyCancelled {
		t.Errorf("expected ErrSipAlreadyCancelled cancelling twice, got %v", cancelAgainError)
	}

	executed, _ := scheduler.SweepDueSips(startDate)
	if len(executed) != 0 {
		t.Errorf("expected a CANCELLED SIP to never execute, got %+v", executed)
	}
}

func TestSipMutationsRejectUnknownSipId(t *testing.T) {
	scheduler, _, _ := newTestScheduler()

	if _, err := scheduler.PauseSip("sip-does-not-exist"); err != ErrSipNotFound {
		t.Errorf("expected ErrSipNotFound from PauseSip, got %v", err)
	}
	if _, err := scheduler.ResumeSip("sip-does-not-exist"); err != ErrSipNotFound {
		t.Errorf("expected ErrSipNotFound from ResumeSip, got %v", err)
	}
	if _, err := scheduler.CancelSip("sip-does-not-exist"); err != ErrSipNotFound {
		t.Errorf("expected ErrSipNotFound from CancelSip, got %v", err)
	}
}

// TestStepUpAppliesExactlyStartingTheThirteenthInstallment is the
// hand-worked example from the task: ₹5000/month (500000 minor units)
// with a 10% annual step-up. Installments #1-#12 (months 0-11 from
// StartDate) must stay at ₹5000. Installment #13 lands exactly on the
// first anniversary of StartDate and must become ₹5500 (500000 * 1.10 =
// 550000) — and every installment after it, until the next anniversary.
func TestStepUpAppliesExactlyStartingTheThirteenthInstallment(t *testing.T) {
	scheduler, _, _ := newTestScheduler()
	startDate := mustParseDate(t, "2024-01-15T00:00:00Z")

	sip, registerError := scheduler.RegisterSip("acct-1", testSchemeId, 500000, FrequencyMonthly, startDate, 10)
	if registerError != nil {
		t.Fatalf("unexpected error: %v", registerError)
	}

	const expectedBaseAmount = int64(500000)
	const expectedSteppedUpAmount = int64(550000) // 500000 * 1.10, exactly

	for installmentNumber := 1; installmentNumber <= 13; installmentNumber++ {
		currentSip, _ := scheduler.LookupSip(sip.SipId)
		dueDate := currentSip.NextDueDate

		executed, failed := scheduler.SweepDueSips(dueDate)
		if len(failed) != 0 {
			t.Fatalf("installment #%d: unexpected failure %v", installmentNumber, failed)
		}
		if len(executed) != 1 {
			t.Fatalf("installment #%d: expected exactly 1 execution, got %d", installmentNumber, len(executed))
		}

		got := executed[0].InstallmentAmountInMinorUnits
		var want int64
		if installmentNumber <= 12 {
			want = expectedBaseAmount
		} else {
			want = expectedSteppedUpAmount
		}
		if got != want {
			t.Errorf("installment #%d (due %v): expected %d, got %d", installmentNumber, dueDate.Format("2006-01-02"), want, got)
		}
	}

	// Sanity-check the exact due date of installment #13 is the first
	// anniversary of StartDate, per the calendar-based, not day-count,
	// year-boundary computation.
	finalSip, _ := scheduler.LookupSip(sip.SipId)
	// After 13 executions NextDueDate has advanced to installment #14's
	// due date, which is the anniversary date + 1 month.
	expectedInstallment14Due := startDate.AddDate(1, 1, 0)
	if !finalSip.NextDueDate.Equal(expectedInstallment14Due) {
		t.Errorf("expected installment #14 due date %v, got %v", expectedInstallment14Due, finalSip.NextDueDate)
	}
}

// TestStepUpSecondAnniversaryCompoundsCorrectly extends the hand-worked
// example one more year: installment #25 (the second anniversary) must
// compound to 500000 * 1.10^2 = 605000, not another flat +10% of 550000
// applied twice differently, and not a reset back to the base amount.
func TestStepUpSecondAnniversaryCompoundsCorrectly(t *testing.T) {
	scheduler, _, _ := newTestScheduler()
	startDate := mustParseDate(t, "2024-01-15T00:00:00Z")
	sip, _ := scheduler.RegisterSip("acct-1", testSchemeId, 500000, FrequencyMonthly, startDate, 10)

	var lastExecutedAmount int64
	for installmentNumber := 1; installmentNumber <= 25; installmentNumber++ {
		currentSip, _ := scheduler.LookupSip(sip.SipId)
		executed, _ := scheduler.SweepDueSips(currentSip.NextDueDate)
		if len(executed) == 1 {
			lastExecutedAmount = executed[0].InstallmentAmountInMinorUnits
		}
	}

	const expectedThirdYearAmount = int64(605000) // 500000 * 1.10^2 = 605000.0 exactly
	if lastExecutedAmount != expectedThirdYearAmount {
		t.Errorf("expected installment #25 amount %d, got %d", expectedThirdYearAmount, lastExecutedAmount)
	}
}

func TestZeroStepUpPercentNeverChangesTheInstallmentAmount(t *testing.T) {
	scheduler, _, _ := newTestScheduler()
	startDate := mustParseDate(t, "2024-01-15T00:00:00Z")
	sip, _ := scheduler.RegisterSip("acct-1", testSchemeId, 500000, FrequencyMonthly, startDate, 0)

	for installmentNumber := 1; installmentNumber <= 15; installmentNumber++ {
		currentSip, _ := scheduler.LookupSip(sip.SipId)
		executed, _ := scheduler.SweepDueSips(currentSip.NextDueDate)
		if len(executed) == 1 && executed[0].InstallmentAmountInMinorUnits != 500000 {
			t.Errorf("installment #%d: expected flat 500000 with 0%% step-up, got %d", installmentNumber, executed[0].InstallmentAmountInMinorUnits)
		}
	}
}

func TestListSipsForAccountOnlyReturnsThatAccountsSips(t *testing.T) {
	scheduler, _, _ := newTestScheduler()
	startDate := mustParseDate(t, "2024-01-15T00:00:00Z")

	scheduler.RegisterSip("acct-1", testSchemeId, 500000, FrequencyMonthly, startDate, 0)
	scheduler.RegisterSip("acct-2", testSchemeId, 700000, FrequencyMonthly, startDate, 0)

	acct1Sips := scheduler.ListSipsForAccount("acct-1")
	if len(acct1Sips) != 1 || acct1Sips[0].AccountIdentifier != "acct-1" {
		t.Errorf("expected exactly 1 SIP for acct-1, got %+v", acct1Sips)
	}
}
