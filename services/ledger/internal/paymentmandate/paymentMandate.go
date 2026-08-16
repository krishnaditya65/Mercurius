// Package paymentmandate implements SIMULATED auto-payment mandates for
// SIPs (Systematic Investment Plans) — FEATURES.md §2: "Auto-payment
// mandates for SIPs (eNACH/standing instructions)."
//
// THIS IS NOT REAL eNACH. No actual bank mandate registration happens
// anywhere in this package — there is no NPCI eNACH API call, no
// physical/digital mandate signed with a bank, no debit actually pulled
// through a real bank rail. What this package DOES model, honestly, is
// the recurring-debit state machine a real eNACH integration would need:
// a client REGISTERS a standing instruction (an amount, a frequency, and
// the date the next debit is due), and a sweep — SweepDueMandates,
// mirroring internal/withdrawalworkflow's ProcessDueWithdrawals pattern
// — executes every mandate whose NextDebitDate has arrived. Each
// executed sweep is where real money genuinely moves: it posts a REAL
// client money movement (a negative amount — a debit/payout, since a SIP
// mandate takes money OUT of the client's account to invest elsewhere)
// through the exact same fundsegregation.SegregationGuard the rest of
// this service's ring-fenced paths use, then advances NextDebitDate by
// one Frequency period. This is not a fake status flip pretending to be
// a recurring payment.
//
// TODO(real build): integrate an actual eNACH registration flow (NPCI
// eNACH API / bank mandate API) so a mandate is backed by a real,
// bank-verified standing instruction instead of a same-process register
// call any caller can invoke, and a real scheduled job (not a
// manually/externally-triggered sweep-due endpoint) to run
// SweepDueMandates. Also needs: a FAILED/insufficient-funds terminal
// outcome per sweep attempt (this skeleton only records success or
// silently skips a failed post — see SweepDueMandates), retry/backoff
// for a failed debit, and a real investment destination for the swept
// money (right now it only leaves the client account via the segregation
// guard's external suspense leg — same category of gap as
// internal/withdrawalworkflow's payout not being a real bank transfer).
package paymentmandate

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"mercurius/ledger/internal/fundsegregation"
)

// MandateFrequency is how often a mandate debits the account.
type MandateFrequency string

const (
	FrequencyDaily   MandateFrequency = "DAILY"
	FrequencyWeekly  MandateFrequency = "WEEKLY"
	FrequencyMonthly MandateFrequency = "MONTHLY"
)

func (frequency MandateFrequency) isValid() bool {
	switch frequency {
	case FrequencyDaily, FrequencyWeekly, FrequencyMonthly:
		return true
	default:
		return false
	}
}

// advance returns the next debit date after currentDebitDate for this
// frequency.
func (frequency MandateFrequency) advance(currentDebitDate time.Time) time.Time {
	switch frequency {
	case FrequencyDaily:
		return currentDebitDate.AddDate(0, 0, 1)
	case FrequencyWeekly:
		return currentDebitDate.AddDate(0, 0, 7)
	case FrequencyMonthly:
		return currentDebitDate.AddDate(0, 1, 0)
	default:
		return currentDebitDate
	}
}

// MandateStatus is where a standing instruction sits in its lifecycle.
type MandateStatus string

const (
	MandateStatusActive    MandateStatus = "ACTIVE"
	MandateStatusPaused    MandateStatus = "PAUSED"
	MandateStatusCancelled MandateStatus = "CANCELLED"
	// MandateStatusSuspended is a terminal-until-manually-reviewed state:
	// a mandate lands here after maxConsecutiveFailuresBeforeSuspension
	// straight-line failed sweep attempts (e.g. its account was never
	// properly CLIENT-classified) — see SweepDueMandates. Unlike
	// MandateStatusPaused (an intentional, reversible user action), this
	// signals something is actually broken and needs a human to look at
	// it before the mandate can run again.
	MandateStatusSuspended MandateStatus = "SUSPENDED"
)

// maxConsecutiveFailuresBeforeSuspension is the small, documented
// threshold of consecutive failed sweep attempts after which
// SweepDueMandates stops retrying a mandate and flips it to
// MandateStatusSuspended instead — see the package doc: silently retrying
// a permanently-failing mandate forever, with NextDebitDate never
// advancing, would otherwise "run" a SIP that never actually invests,
// with no signal to anyone.
const maxConsecutiveFailuresBeforeSuspension = 3

var ErrInvalidMandateAmount = fmt.Errorf("mandate amount must be strictly positive")
var ErrInvalidMandateFrequency = fmt.Errorf("frequency must be one of DAILY, WEEKLY, MONTHLY")
var ErrMissingAccountIdentifier = fmt.Errorf("accountIdentifier must not be empty")
var ErrMandateNotFound = fmt.Errorf("no payment mandate exists with that id")
var ErrMandateNotActive = fmt.Errorf("only an ACTIVE mandate can be paused")
var ErrMandateNotPausable = fmt.Errorf("a CANCELLED mandate cannot be paused")
var ErrMandateAlreadyCancelled = fmt.Errorf("mandate has already been cancelled")
var ErrMandateNotResumable = fmt.Errorf("only a PAUSED mandate can be resumed")

// PaymentMandate is one modelled recurring-debit standing instruction.
type PaymentMandate struct {
	MandateId            string
	AccountIdentifier    string
	AmountInMinorUnits   int64
	Frequency            MandateFrequency
	NextDebitDate        time.Time
	RegisteredAt         time.Time
	Status               MandateStatus
	SuccessfulSweepCount int
	// ConsecutiveFailureCount counts sweep attempts that have failed
	// back-to-back, most recently — reset to 0 on any successful sweep.
	// Reaching maxConsecutiveFailuresBeforeSuspension flips Status to
	// MandateStatusSuspended.
	ConsecutiveFailureCount int
	// LastFailureReason is the most recent failed sweep's error message,
	// for a compliance/ops reviewer inspecting a suspended (or
	// currently-failing) mandate — cleared on any successful sweep.
	LastFailureReason string
}

// SweptMandate is one line of SweepDueMandates' result — which mandate,
// whether its debit for this sweep actually posted, and (on failure) how
// many consecutive failures it's now accumulated and whether this sweep
// was the one that suspended it.
type SweptMandate struct {
	MandateId               string
	WasPosted               bool
	Error                   string
	ConsecutiveFailureCount int
	WasSuspended            bool
}

// PaymentMandateRegistry is safe for concurrent use. A successfully
// swept mandate moves REAL money through the shared
// *fundsegregation.SegregationGuard — the exact same ring-fenced path
// /client-funds/deposit uses, just with a negative (debit/payout)
// amount — so paymentmandate is not a parallel bookkeeping system.
type PaymentMandateRegistry struct {
	segregationGuard *fundsegregation.SegregationGuard

	mutexGuardingMandates sync.Mutex
	mandatesById          map[string]*PaymentMandate
}

func NewPaymentMandateRegistry(segregationGuard *fundsegregation.SegregationGuard) *PaymentMandateRegistry {
	return &PaymentMandateRegistry{
		segregationGuard: segregationGuard,
		mandatesById:     make(map[string]*PaymentMandate),
	}
}

// RegisterMandate creates a new ACTIVE standing instruction. nextDebitDate
// is the first date the mandate becomes due for sweeping — a caller
// registering a "start today" SIP should pass now (or earlier); a
// future-dated first debit is also allowed.
func (registry *PaymentMandateRegistry) RegisterMandate(
	accountIdentifier string,
	amountInMinorUnits int64,
	frequency MandateFrequency,
	nextDebitDate time.Time,
	now time.Time,
) (*PaymentMandate, error) {
	if accountIdentifier == "" {
		return nil, ErrMissingAccountIdentifier
	}
	if amountInMinorUnits <= 0 {
		return nil, ErrInvalidMandateAmount
	}
	if !frequency.isValid() {
		return nil, ErrInvalidMandateFrequency
	}

	mandateId, genError := generateMandateId()
	if genError != nil {
		return nil, fmt.Errorf("failed to generate mandate id: %w", genError)
	}

	mandate := &PaymentMandate{
		MandateId:          mandateId,
		AccountIdentifier:  accountIdentifier,
		AmountInMinorUnits: amountInMinorUnits,
		Frequency:          frequency,
		NextDebitDate:      nextDebitDate,
		RegisteredAt:       now,
		Status:             MandateStatusActive,
	}

	registry.mutexGuardingMandates.Lock()
	registry.mandatesById[mandateId] = mandate
	registry.mutexGuardingMandates.Unlock()

	return mandate, nil
}

// PauseMandate suspends an ACTIVE mandate — it is skipped by
// SweepDueMandates until resumed. Only an ACTIVE mandate can be paused.
func (registry *PaymentMandateRegistry) PauseMandate(mandateId string) (*PaymentMandate, error) {
	registry.mutexGuardingMandates.Lock()
	defer registry.mutexGuardingMandates.Unlock()

	mandate, wasFound := registry.mandatesById[mandateId]
	if !wasFound {
		return nil, ErrMandateNotFound
	}
	if mandate.Status == MandateStatusCancelled {
		return nil, ErrMandateNotPausable
	}
	if mandate.Status != MandateStatusActive {
		return nil, ErrMandateNotActive
	}

	mandate.Status = MandateStatusPaused
	return mandate, nil
}

// ResumeMandate reactivates a PAUSED mandate so it becomes eligible for
// sweeping again once its NextDebitDate arrives.
func (registry *PaymentMandateRegistry) ResumeMandate(mandateId string) (*PaymentMandate, error) {
	registry.mutexGuardingMandates.Lock()
	defer registry.mutexGuardingMandates.Unlock()

	mandate, wasFound := registry.mandatesById[mandateId]
	if !wasFound {
		return nil, ErrMandateNotFound
	}
	if mandate.Status != MandateStatusPaused {
		return nil, ErrMandateNotResumable
	}

	mandate.Status = MandateStatusActive
	return mandate, nil
}

// CancelMandate permanently terminates a mandate — a CANCELLED mandate
// can never be resumed or swept again. Idempotent-refusal: cancelling an
// already-cancelled mandate is rejected rather than silently no-op'd, so
// a caller can't mistake a stale double-cancel for success.
func (registry *PaymentMandateRegistry) CancelMandate(mandateId string) (*PaymentMandate, error) {
	registry.mutexGuardingMandates.Lock()
	defer registry.mutexGuardingMandates.Unlock()

	mandate, wasFound := registry.mandatesById[mandateId]
	if !wasFound {
		return nil, ErrMandateNotFound
	}
	if mandate.Status == MandateStatusCancelled {
		return nil, ErrMandateAlreadyCancelled
	}

	mandate.Status = MandateStatusCancelled
	return mandate, nil
}

// SweepDueMandates executes every ACTIVE mandate whose NextDebitDate has
// arrived: posts a real debit (negative client money movement) via
// fundsegregation.SegregationGuard.PostClientMoneyMovement.
//
// NextDebitDate only advances on a SUCCESSFUL sweep — a mandate that
// fails stays due at the SAME date so the next sweep retries it, rather
// than silently "running" forever on schedule without ever actually
// investing. Each failure increments ConsecutiveFailureCount and records
// LastFailureReason; a successful sweep resets both to zero/empty. Once
// ConsecutiveFailureCount reaches maxConsecutiveFailuresBeforeSuspension,
// the mandate is flipped to MandateStatusSuspended and stops being picked
// up by future sweeps at all — a clear, terminal, surfaced signal instead
// of an endless silent retry loop. A mandate whose debit fails is still
// reported in the result with WasPosted=false and its Error populated,
// not silently dropped.
func (registry *PaymentMandateRegistry) SweepDueMandates(now time.Time) []SweptMandate {
	registry.mutexGuardingMandates.Lock()
	dueMandates := make([]*PaymentMandate, 0)
	for _, mandate := range registry.mandatesById {
		if mandate.Status == MandateStatusActive && !now.Before(mandate.NextDebitDate) {
			dueMandates = append(dueMandates, mandate)
		}
	}
	registry.mutexGuardingMandates.Unlock()

	sort.Slice(dueMandates, func(i, j int) bool { return dueMandates[i].MandateId < dueMandates[j].MandateId })

	results := make([]SweptMandate, 0, len(dueMandates))
	for _, mandate := range dueMandates {
		postError := registry.segregationGuard.PostClientMoneyMovement(
			mandate.AccountIdentifier,
			-mandate.AmountInMinorUnits,
			fmt.Sprintf("simulated SIP mandate debit, mandateId=%s", mandate.MandateId),
		)

		registry.mutexGuardingMandates.Lock()
		result := SweptMandate{MandateId: mandate.MandateId, WasPosted: postError == nil}
		if postError == nil {
			mandate.SuccessfulSweepCount++
			mandate.ConsecutiveFailureCount = 0
			mandate.LastFailureReason = ""
			mandate.NextDebitDate = mandate.Frequency.advance(mandate.NextDebitDate)
		} else {
			// Deliberately NOT advancing NextDebitDate here — see the
			// doc comment above.
			mandate.ConsecutiveFailureCount++
			mandate.LastFailureReason = postError.Error()
			result.Error = postError.Error()
			result.ConsecutiveFailureCount = mandate.ConsecutiveFailureCount
			if mandate.ConsecutiveFailureCount >= maxConsecutiveFailuresBeforeSuspension {
				mandate.Status = MandateStatusSuspended
				result.WasSuspended = true
			}
		}
		registry.mutexGuardingMandates.Unlock()

		results = append(results, result)
	}

	return results
}

// MandatesForAccount returns every mandate (any status) for
// accountIdentifier, sorted by RegisteredAt for a deterministic response.
func (registry *PaymentMandateRegistry) MandatesForAccount(accountIdentifier string) []*PaymentMandate {
	registry.mutexGuardingMandates.Lock()
	defer registry.mutexGuardingMandates.Unlock()

	matching := make([]*PaymentMandate, 0)
	for _, mandate := range registry.mandatesById {
		if mandate.AccountIdentifier == accountIdentifier {
			matching = append(matching, mandate)
		}
	}
	sort.Slice(matching, func(i, j int) bool { return matching[i].RegisteredAt.Before(matching[j].RegisteredAt) })
	return matching
}

// LookupMandate returns a single mandate by id, if it exists.
func (registry *PaymentMandateRegistry) LookupMandate(mandateId string) (*PaymentMandate, bool) {
	registry.mutexGuardingMandates.Lock()
	defer registry.mutexGuardingMandates.Unlock()

	mandate, wasFound := registry.mandatesById[mandateId]
	return mandate, wasFound
}

func generateMandateId() (string, error) {
	randomBytes := make([]byte, 8)
	if _, readError := rand.Read(randomBytes); readError != nil {
		return "", readError
	}
	return "mandate-" + hex.EncodeToString(randomBytes), nil
}
