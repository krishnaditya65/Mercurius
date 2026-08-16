// Package fundsegregation enforces FEATURES.md §1's "Segregation of
// client funds vs. firm funds" requirement: in most regulated markets,
// money a broker holds on behalf of clients must be ring-fenced from the
// firm's own capital — the firm cannot use client cash to fund its own
// obligations, and a regulator must be able to verify at any moment that
// "client money held" == "money actually segregated for clients".
//
// This package does NOT replace internal/doubleentry — it classifies
// accounts as CLIENT or FIRM and layers one additional invariant on top:
// a single designated custody-pool account's balance must always equal
// the sum of every CLIENT account's balance. Any journal entry that would
// move money into or out of a CLIENT account without an equal, opposite
// movement on the custody-pool account is rejected before it's posted —
// so the invariant cannot drift by construction, not just by convention.
//
// TODO(real build): the real implementation needs this custody pool to
// correspond to an actual segregated bank account held in trust (verified
// against a real bank statement, not just an internal ledger account),
// and needs every existing money-movement path in this codebase (trade
// settlement, withdrawals, future deposit rails) migrated to route
// through PostClientMoneyMovement/PostInterClientTransfer rather than
// posting directly against internal/doubleentry. As of this build, only
// deposits into client accounts go through this guard — see
// docs/DOCUMENTATION.md for the exact list of what's covered and what
// isn't yet.
package fundsegregation

import (
	"errors"
	"fmt"
	"math"

	"mercurius/ledger/internal/doubleentry"
)

// AccountKind classifies a ledger account for segregation purposes.
type AccountKind string

const (
	AccountKindClient AccountKind = "CLIENT"
	AccountKindFirm   AccountKind = "FIRM"
)

var ErrWouldBreakSegregation = errors.New("journal entry would break client fund segregation invariant")
var ErrUnclassifiedAccount = errors.New("account is not classified as a CLIENT account")
var ErrInvalidMovementAmount = errors.New("movement amount must be positive")
var ErrMovementAmountTooLarge = errors.New("movement amount is too large to process safely")

// maxMovementAmountInMinorUnits is the largest absolute amount
// PostClientMoneyMovement will accept. The entry it builds doubles the
// absolute amount (twiceTheAmount) for the external-cash-suspense leg, so
// anything larger than math.MaxInt64/2 would silently overflow int64 on
// that multiply — reject it outright instead.
const maxMovementAmountInMinorUnits = math.MaxInt64 / 2

// SegregationGuard classifies accounts and enforces the ring-fencing
// invariant on top of a shared doubleentry.LedgerBook.
// It holds no balances of its own — internal/doubleentry remains the one
// system of record.
type SegregationGuard struct {
	ledgerBook                    doubleentry.LedgerBook
	custodyPoolAccountId          string
	externalCashSuspenseAccountId string
	accountKindByIdentifier       map[string]AccountKind
}

// NewSegregationGuard builds a guard over an already-constructed ledger
// book. custodyPoolAccountId and externalCashSuspenseAccountId must
// themselves already exist as accounts in ledgerBook.
//
// externalCashSuspenseAccountId is the "outside world" counterparty for
// money genuinely entering or leaving the firm (a bank deposit, a
// withdrawal payout) — the same clearing-account role
// "firm-clearing-acct" plays for ordinary (non-segregated) journal
// entries elsewhere in this ledger. It's expected to run a large,
// unbounded balance (it nets every external cash movement ever made) —
// that's normal for a suspense/clearing account and is not itself part
// of the segregation invariant.
func NewSegregationGuard(
	ledgerBook doubleentry.LedgerBook,
	custodyPoolAccountId string,
	externalCashSuspenseAccountId string,
	clientAccountIdentifiers []string,
	firmAccountIdentifiers []string,
) *SegregationGuard {
	kindByIdentifier := make(map[string]AccountKind, len(clientAccountIdentifiers)+len(firmAccountIdentifiers))
	for _, accountIdentifier := range clientAccountIdentifiers {
		kindByIdentifier[accountIdentifier] = AccountKindClient
	}
	for _, accountIdentifier := range firmAccountIdentifiers {
		kindByIdentifier[accountIdentifier] = AccountKindFirm
	}
	return &SegregationGuard{
		ledgerBook:                    ledgerBook,
		custodyPoolAccountId:          custodyPoolAccountId,
		externalCashSuspenseAccountId: externalCashSuspenseAccountId,
		accountKindByIdentifier:       kindByIdentifier,
	}
}

// PostClientMoneyMovement moves real external money (a deposit or a
// payout) into or out of one client account, atomically keeping the
// custody-pool balance equal to that client account's change — this is
// the ring-fenced replacement for posting a raw journal entry directly
// against a client account from an external cash rail.
//
// A positive amountInMinorUnits is money arriving for the client
// (custody pool and client account both increase); negative is money
// leaving on the client's behalf (both decrease). The entry's third leg
// is externalCashSuspenseAccountId, the real-world counterparty — this
// is what lets both the client account AND the custody pool move in the
// same direction while the overall entry still balances.
func (guard *SegregationGuard) PostClientMoneyMovement(
	clientAccountIdentifier string,
	amountInMinorUnits int64,
	humanReadableDescription string,
) error {
	if amountInMinorUnits == 0 {
		return fmt.Errorf("%w: got 0", ErrInvalidMovementAmount)
	}
	if kind, isClassified := guard.accountKindByIdentifier[clientAccountIdentifier]; !isClassified || kind != AccountKindClient {
		return fmt.Errorf("%w: %s", ErrUnclassifiedAccount, clientAccountIdentifier)
	}

	absoluteAmount := amountInMinorUnits
	if amountInMinorUnits < 0 {
		absoluteAmount = -amountInMinorUnits
	}
	if absoluteAmount > maxMovementAmountInMinorUnits {
		return fmt.Errorf("%w: got %d, max is %d", ErrMovementAmountTooLarge, absoluteAmount, int64(maxMovementAmountInMinorUnits))
	}

	// Money arriving (positive amount): debit the client account and
	// debit the custody pool (both increase), credit the external
	// suspense account for double the amount (it decreases — cash left
	// the outside world and entered the firm on the clients' behalf).
	// Money leaving (negative amount) is the exact mirror image.
	twiceTheAmount := absoluteAmount * 2
	journalEntry := doubleentry.JournalEntry{
		HumanReadableDescription: humanReadableDescription,
		DebitLines: []doubleentry.LedgerAccountLine{
			{LedgerAccountIdentifier: clientAccountIdentifier, AmountInMinorUnits: absoluteAmount},
			{LedgerAccountIdentifier: guard.custodyPoolAccountId, AmountInMinorUnits: absoluteAmount},
		},
		CreditLines: []doubleentry.LedgerAccountLine{
			{LedgerAccountIdentifier: guard.externalCashSuspenseAccountId, AmountInMinorUnits: twiceTheAmount},
		},
	}
	if amountInMinorUnits < 0 {
		journalEntry.DebitLines, journalEntry.CreditLines = journalEntry.CreditLines, journalEntry.DebitLines
	}
	return guard.ledgerBook.PostJournalEntry(journalEntry)
}

// SegregationInvariantReport is what a compliance officer / regulator
// inquiry actually needs to see: does segregated client money on the
// books match what clients are collectively owed, right now?
type SegregationInvariantReport struct {
	CustodyPoolAccountId               string
	CustodyPoolBalanceInMinorUnits     int64
	AggregateClientBalanceInMinorUnits int64
	DiscrepancyInMinorUnits            int64
	IsSegregationIntact                bool
	ClientAccountCount                 int
}

// CheckSegregationInvariant sums every classified CLIENT account's
// current balance and compares it against the custody pool's balance.
// In a fully-migrated real build these are always equal; a nonzero
// discrepancy here is exactly the kind of thing a regulator inquiry
// (FEATURES.md §1) or an internal audit needs surfaced immediately, not
// discovered after the fact.
func (guard *SegregationGuard) CheckSegregationInvariant() (SegregationInvariantReport, error) {
	custodyBalance, lookupError := guard.ledgerBook.CurrentBalanceInMinorUnits(guard.custodyPoolAccountId)
	if lookupError != nil {
		return SegregationInvariantReport{}, lookupError
	}

	aggregateClientBalance := int64(0)
	clientAccountCount := 0
	for accountIdentifier, kind := range guard.accountKindByIdentifier {
		if kind != AccountKindClient {
			continue
		}
		clientBalance, lookupError := guard.ledgerBook.CurrentBalanceInMinorUnits(accountIdentifier)
		if lookupError != nil {
			return SegregationInvariantReport{}, lookupError
		}
		aggregateClientBalance += clientBalance
		clientAccountCount++
	}

	discrepancy := custodyBalance - aggregateClientBalance
	return SegregationInvariantReport{
		CustodyPoolAccountId:               guard.custodyPoolAccountId,
		CustodyPoolBalanceInMinorUnits:     custodyBalance,
		AggregateClientBalanceInMinorUnits: aggregateClientBalance,
		DiscrepancyInMinorUnits:            discrepancy,
		IsSegregationIntact:                discrepancy == 0,
		ClientAccountCount:                 clientAccountCount,
	}, nil
}

// ValidateEntryPreservesSegregation is a dry-run check for an arbitrary
// journal entry (e.g. one an operator is about to post through the raw
// /journal-entries endpoint): would posting it move client money without
// an equal, opposite movement on the custody pool? It never posts
// anything — it only computes what the entry's net effect on "client
// money in aggregate" and "custody pool balance" would be and compares
// them. A firm-to-firm entry, or a client-to-client transfer where the
// deltas cancel out, always passes; a deposit path that credits the
// firm's own operating account instead of the custody pool does not.
func (guard *SegregationGuard) ValidateEntryPreservesSegregation(entry doubleentry.JournalEntry) error {
	netClientDelta := int64(0)
	netCustodyDelta := int64(0)

	applyLine := func(accountIdentifier string, amount int64, sign int64) {
		if accountIdentifier == guard.custodyPoolAccountId {
			netCustodyDelta += sign * amount
			return
		}
		if kind, isClassified := guard.accountKindByIdentifier[accountIdentifier]; isClassified && kind == AccountKindClient {
			netClientDelta += sign * amount
		}
	}

	for _, debitLine := range entry.DebitLines {
		applyLine(debitLine.LedgerAccountIdentifier, debitLine.AmountInMinorUnits, +1)
	}
	for _, creditLine := range entry.CreditLines {
		applyLine(creditLine.LedgerAccountIdentifier, creditLine.AmountInMinorUnits, -1)
	}

	if netClientDelta != netCustodyDelta {
		return fmt.Errorf(
			"%w: net client-account delta=%d, net custody-pool delta=%d, entry=%q",
			ErrWouldBreakSegregation, netClientDelta, netCustodyDelta, entry.HumanReadableDescription,
		)
	}
	return nil
}

// AccountKindOf reports how an account is classified, for callers (e.g.
// an HTTP handler validating a transfer) that need to reject a
// non-client-to-client movement before it's attempted.
func (guard *SegregationGuard) AccountKindOf(accountIdentifier string) (AccountKind, bool) {
	kind, isClassified := guard.accountKindByIdentifier[accountIdentifier]
	return kind, isClassified
}

// PostInterClientTransfer moves money between two CLIENT accounts (e.g.
// a trade settlement, a family transfer) without touching the custody
// pool at all — since no money enters or leaves client custody in
// aggregate, the segregation invariant is preserved automatically. This
// is rejected outright if either side isn't actually classified CLIENT,
// so it can't be used to sneak money out to a FIRM account.
func (guard *SegregationGuard) PostInterClientTransfer(
	fromClientAccountIdentifier string,
	toClientAccountIdentifier string,
	amountInMinorUnits int64,
	humanReadableDescription string,
) error {
	if amountInMinorUnits <= 0 {
		return fmt.Errorf("%w: got %d", ErrInvalidMovementAmount, amountInMinorUnits)
	}
	fromKind, fromClassified := guard.accountKindByIdentifier[fromClientAccountIdentifier]
	toKind, toClassified := guard.accountKindByIdentifier[toClientAccountIdentifier]
	if !fromClassified || fromKind != AccountKindClient {
		return fmt.Errorf("%w: %s", ErrUnclassifiedAccount, fromClientAccountIdentifier)
	}
	if !toClassified || toKind != AccountKindClient {
		return fmt.Errorf("%w: %s", ErrUnclassifiedAccount, toClientAccountIdentifier)
	}

	return guard.ledgerBook.PostJournalEntry(doubleentry.JournalEntry{
		HumanReadableDescription: humanReadableDescription,
		DebitLines: []doubleentry.LedgerAccountLine{
			{LedgerAccountIdentifier: toClientAccountIdentifier, AmountInMinorUnits: amountInMinorUnits},
		},
		CreditLines: []doubleentry.LedgerAccountLine{
			{LedgerAccountIdentifier: fromClientAccountIdentifier, AmountInMinorUnits: amountInMinorUnits},
		},
	})
}
