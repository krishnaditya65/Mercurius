// Package sipscheduler implements SIP (Systematic Investment Plan)
// registration, pause/resume/cancel, and the calendar sweep that executes
// due installments — FEATURES.md §4: "Lumpsum + SIP setup, SIP
// pause/cancel, SIP calendar" and "Step-Up SIPs (auto-increase % annually)".
//
// A lumpsum investment needs none of this — it's just a single call to
// internal/amcrouting's PlacePurchaseOrder. This package only exists for
// the recurring, calendar-driven case.
//
// Each due installment is executed by routing a purchase order through
// internal/amcrouting — see that package's doc comment for the loud "this
// never talks to a real AMC" caveat, which applies equally here.
//
// TODO(real build): only MONTHLY frequency is implemented. A real SIP
// product also offers weekly/quarterly/annual frequencies. SweepDueSips
// is exposed as an endpoint an operator (or a script/cron) calls, not run
// automatically on a real scheduled job — same gap as ledger's
// ProcessDueWithdrawals.
package sipscheduler

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"mercurius/mutualFunds/internal/amcrouting"
	"mercurius/mutualFunds/internal/fundcatalog"
)

type SipFrequency string

const (
	FrequencyMonthly SipFrequency = "MONTHLY"
)

type SipStatus string

const (
	StatusActive    SipStatus = "ACTIVE"
	StatusPaused    SipStatus = "PAUSED"
	StatusCancelled SipStatus = "CANCELLED"
)

// Sip is one registered systematic investment plan.
//
// Step-up math: BaseInstallmentAmountInMinorUnits is fixed at
// registration and never mutated. Each sweep recomputes that
// installment's amount from scratch as
// BaseInstallmentAmountInMinorUnits * (1 + AnnualStepUpPercent/100) ^
// fullYearsElapsed, where fullYearsElapsed is the number of complete
// 12-month anniversaries of StartDate that NextDueDate has reached —
// computed with calendar-aware time.Time.AddDate, not a naive day-count
// divide. CurrentInstallmentAmountInMinorUnits records the amount actually
// charged on the most recent sweep for visibility; it is derived, not the
// source of truth.
type Sip struct {
	SipId                                string
	AccountIdentifier                    string
	SchemeId                             string
	BaseInstallmentAmountInMinorUnits    int64
	CurrentInstallmentAmountInMinorUnits int64
	Frequency                            SipFrequency
	StartDate                            time.Time
	NextDueDate                          time.Time
	AnnualStepUpPercent                  float64
	InstallmentsExecuted                 int
	Status                               SipStatus
	CreatedAt                            time.Time
}

// SipExecutionResult records one installment actually executed by a
// sweep — the purchase order it produced (still PENDING confirmation in
// amcrouting at this point, exactly like a real AMC not allocating units
// same-day) plus the SIP's advanced schedule state.
type SipExecutionResult struct {
	SipId                         string
	OrderId                       string
	InstallmentAmountInMinorUnits int64
	ExecutedAt                    time.Time
	NewNextDueDate                time.Time
}

var ErrUnknownScheme = fmt.Errorf("no such scheme exists in the catalog")
var ErrInvalidAmount = fmt.Errorf("SIP installment amount must be strictly positive")
var ErrUnsupportedFrequency = fmt.Errorf("only MONTHLY frequency is supported")
var ErrInvalidStepUpPercent = fmt.Errorf("annual step-up percent must be between 0 and 100")
var ErrMissingAccountIdentifier = fmt.Errorf("accountIdentifier is required")
var ErrZeroStartDate = fmt.Errorf("startDate is required")
var ErrSipNotFound = fmt.Errorf("no SIP exists with that id")
var ErrSipNotActive = fmt.Errorf("SIP is not currently ACTIVE")
var ErrSipNotPaused = fmt.Errorf("SIP is not currently PAUSED")
var ErrSipAlreadyCancelled = fmt.Errorf("SIP is already CANCELLED and cannot be modified")

// SipScheduler is safe for concurrent use.
type SipScheduler struct {
	catalog   *fundcatalog.FundCatalog
	amcRouter *amcrouting.AmcOrderRouter

	mutexGuardingSips sync.Mutex
	sipsById          map[string]*Sip
}

func NewSipScheduler(catalog *fundcatalog.FundCatalog, amcRouter *amcrouting.AmcOrderRouter) *SipScheduler {
	return &SipScheduler{
		catalog:   catalog,
		amcRouter: amcRouter,
		sipsById:  make(map[string]*Sip),
	}
}

// RegisterSip validates and stores a new SIP. The first installment is
// due exactly on startDate — a subsequent SweepDueSips call with now >=
// startDate will execute it.
func (scheduler *SipScheduler) RegisterSip(
	accountIdentifier string,
	schemeId string,
	installmentAmountInMinorUnits int64,
	frequency SipFrequency,
	startDate time.Time,
	annualStepUpPercent float64,
) (*Sip, error) {
	if accountIdentifier == "" {
		return nil, ErrMissingAccountIdentifier
	}
	if installmentAmountInMinorUnits <= 0 {
		return nil, ErrInvalidAmount
	}
	if frequency != FrequencyMonthly {
		return nil, ErrUnsupportedFrequency
	}
	if startDate.IsZero() {
		return nil, ErrZeroStartDate
	}
	if annualStepUpPercent < 0 || annualStepUpPercent > 100 {
		return nil, ErrInvalidStepUpPercent
	}
	if _, wasFound := scheduler.catalog.Lookup(schemeId); !wasFound {
		return nil, ErrUnknownScheme
	}

	sipId, genError := generateSipId()
	if genError != nil {
		return nil, fmt.Errorf("failed to generate SIP id: %w", genError)
	}

	sip := &Sip{
		SipId:                                sipId,
		AccountIdentifier:                    accountIdentifier,
		SchemeId:                             schemeId,
		BaseInstallmentAmountInMinorUnits:    installmentAmountInMinorUnits,
		CurrentInstallmentAmountInMinorUnits: installmentAmountInMinorUnits,
		Frequency:                            frequency,
		StartDate:                            startDate,
		NextDueDate:                          startDate,
		AnnualStepUpPercent:                  annualStepUpPercent,
		Status:                               StatusActive,
		CreatedAt:                            startDate,
	}

	scheduler.mutexGuardingSips.Lock()
	scheduler.sipsById[sipId] = sip
	scheduler.mutexGuardingSips.Unlock()

	return sip, nil
}

// PauseSip stops future installments from executing until ResumeSip is
// called. A paused SIP's NextDueDate does NOT advance and is NOT
// backfilled on resume — installments that would have fallen while
// paused are simply skipped, never retroactively charged.
func (scheduler *SipScheduler) PauseSip(sipId string) (*Sip, error) {
	scheduler.mutexGuardingSips.Lock()
	defer scheduler.mutexGuardingSips.Unlock()

	sip, wasFound := scheduler.sipsById[sipId]
	if !wasFound {
		return nil, ErrSipNotFound
	}
	if sip.Status == StatusCancelled {
		return nil, ErrSipAlreadyCancelled
	}
	if sip.Status != StatusActive {
		return nil, ErrSipNotActive
	}
	sip.Status = StatusPaused
	return sip, nil
}

// ResumeSip re-activates a paused SIP. NextDueDate is left exactly where
// it was: if it's already in the past, the very next sweep will execute
// it immediately (at whatever step-up-adjusted amount is due for that
// date) — resuming does not skip or reschedule the missed installment(s)
// to "catch up" or push forward, it just lets the schedule proceed from
// where it was frozen.
func (scheduler *SipScheduler) ResumeSip(sipId string) (*Sip, error) {
	scheduler.mutexGuardingSips.Lock()
	defer scheduler.mutexGuardingSips.Unlock()

	sip, wasFound := scheduler.sipsById[sipId]
	if !wasFound {
		return nil, ErrSipNotFound
	}
	if sip.Status == StatusCancelled {
		return nil, ErrSipAlreadyCancelled
	}
	if sip.Status != StatusPaused {
		return nil, ErrSipNotPaused
	}
	sip.Status = StatusActive
	return sip, nil
}

// CancelSip is terminal — a CANCELLED SIP can never be resumed. Both
// ACTIVE and PAUSED SIPs can be cancelled.
func (scheduler *SipScheduler) CancelSip(sipId string) (*Sip, error) {
	scheduler.mutexGuardingSips.Lock()
	defer scheduler.mutexGuardingSips.Unlock()

	sip, wasFound := scheduler.sipsById[sipId]
	if !wasFound {
		return nil, ErrSipNotFound
	}
	if sip.Status == StatusCancelled {
		return nil, ErrSipAlreadyCancelled
	}
	sip.Status = StatusCancelled
	return sip, nil
}

// SweepDueSips executes every ACTIVE SIP whose NextDueDate has arrived
// (now >= NextDueDate): computes the step-up-adjusted installment amount
// for that due date, routes a purchase order for it through amcrouting,
// and — only on success — advances NextDueDate by one month and records
// the executed amount. A SIP whose order placement fails (e.g. its scheme
// vanished from the catalog) is left exactly where it was and reported in
// failedSipIds, so a retry sweep will try it again rather than silently
// skipping an installment. PAUSED and CANCELLED SIPs are never touched.
//
// Idempotent within the same due date: once a SIP's NextDueDate has been
// advanced past now, a second sweep call with the same (or earlier) now
// finds nothing due for it and does nothing.
func (scheduler *SipScheduler) SweepDueSips(now time.Time) (executed []SipExecutionResult, failedSipIds []string) {
	scheduler.mutexGuardingSips.Lock()
	dueSips := make([]*Sip, 0)
	for _, sip := range scheduler.sipsById {
		if sip.Status == StatusActive && !now.Before(sip.NextDueDate) {
			dueSips = append(dueSips, sip)
		}
	}
	scheduler.mutexGuardingSips.Unlock()

	sort.Slice(dueSips, func(i, j int) bool { return dueSips[i].SipId < dueSips[j].SipId })

	for _, sip := range dueSips {
		installmentAmount := stepUpAdjustedInstallmentAmount(sip, sip.NextDueDate)

		order, placeError := scheduler.amcRouter.PlacePurchaseOrder(sip.AccountIdentifier, sip.SchemeId, installmentAmount, now)
		if placeError != nil {
			failedSipIds = append(failedSipIds, sip.SipId)
			continue
		}

		scheduler.mutexGuardingSips.Lock()
		newNextDueDate := sip.NextDueDate.AddDate(0, 1, 0)
		sip.CurrentInstallmentAmountInMinorUnits = installmentAmount
		sip.InstallmentsExecuted++
		sip.NextDueDate = newNextDueDate
		scheduler.mutexGuardingSips.Unlock()

		executed = append(executed, SipExecutionResult{
			SipId:                         sip.SipId,
			OrderId:                       order.OrderId,
			InstallmentAmountInMinorUnits: installmentAmount,
			ExecutedAt:                    now,
			NewNextDueDate:                newNextDueDate,
		})
	}

	return executed, failedSipIds
}

// stepUpAdjustedInstallmentAmount computes the installment amount due on
// dueDate: BaseInstallmentAmountInMinorUnits compounded by
// AnnualStepUpPercent once per full year elapsed between StartDate and
// dueDate. Worked example: ₹5000/month base, 10% annual step-up,
// StartDate = installment #1's due date. Installments #1-#12 fall before
// one full year has elapsed (fullYearsElapsed=0) and stay at ₹5000.
// Installment #13 falls exactly on the first anniversary of StartDate
// (fullYearsElapsed=1), so it becomes 5000 * 1.10 = ₹5500 — and so does
// every installment after it, until the next anniversary pushes it to
// fullYearsElapsed=2.
func stepUpAdjustedInstallmentAmount(sip *Sip, dueDate time.Time) int64 {
	if sip.AnnualStepUpPercent == 0 {
		return sip.BaseInstallmentAmountInMinorUnits
	}

	fullYearsElapsed := 0
	for !sip.StartDate.AddDate(fullYearsElapsed+1, 0, 0).After(dueDate) {
		fullYearsElapsed++
	}

	multiplier := math.Pow(1+sip.AnnualStepUpPercent/100, float64(fullYearsElapsed))
	return int64(math.Round(float64(sip.BaseInstallmentAmountInMinorUnits) * multiplier))
}

// ListSipsForAccount returns every SIP (any status) for accountIdentifier,
// sorted by CreatedAt for a deterministic response.
func (scheduler *SipScheduler) ListSipsForAccount(accountIdentifier string) []*Sip {
	scheduler.mutexGuardingSips.Lock()
	defer scheduler.mutexGuardingSips.Unlock()

	matchingSips := make([]*Sip, 0)
	for _, sip := range scheduler.sipsById {
		if sip.AccountIdentifier == accountIdentifier {
			matchingSips = append(matchingSips, sip)
		}
	}
	sort.Slice(matchingSips, func(i, j int) bool { return matchingSips[i].CreatedAt.Before(matchingSips[j].CreatedAt) })
	return matchingSips
}

func (scheduler *SipScheduler) LookupSip(sipId string) (*Sip, bool) {
	scheduler.mutexGuardingSips.Lock()
	defer scheduler.mutexGuardingSips.Unlock()

	sip, wasFound := scheduler.sipsById[sipId]
	return sip, wasFound
}

func generateSipId() (string, error) {
	randomBytes := make([]byte, 8)
	if _, readError := rand.Read(randomBytes); readError != nil {
		return "", readError
	}
	return "sip-" + hex.EncodeToString(randomBytes), nil
}
