// Package multicurrencywallet implements FEATURES.md §2's "Multi-currency
// wallet (for platforms offering global/US stocks)" entirely as a NEW
// layer on top of the existing accounting core — it does not change
// internal/doubleentry's currency-agnostic contract (still just "minor
// units", no currency tag) or any of its method signatures, so
// oms-gateway's trade-settlement posts and every other existing caller
// are completely unaffected.
//
// THE FX RATE TABLE IS STATIC AND ILLUSTRATIVE — NOT A LIVE FX FEED.
// See FxRateTable below: rates are hardcoded config values (e.g.
// USD/INR = 83.0), not sourced from any real market data provider, and
// never change while the process runs. A real build needs a live,
// continuously-updated rate feed (with bid/ask spread, not one flat
// mid-rate) and almost certainly a real cross-border settlement/custody
// arrangement to actually hold foreign-currency cash, neither of which
// exists here.
//
// Design: each (accountIdentifier, currencyCode) pair is its own ledger
// account inside the SAME shared *doubleentry.InMemoryDoubleEntryLedgerBook
// everything else in this service uses — real doubleentry-backed numbers,
// not a fabricated multiplier applied on top of one balance. The
// account's NATIVE currency (INR, by convention matching every other
// endpoint in this service) is deliberately an ALIAS for the
// pre-existing raw "<accountIdentifier>" ledger account that
// /accounts/balance, /withdrawals/*, /client-funds/*, and AML monitoring
// already read and write — an account's "INR wallet" IS its ordinary
// balance, viewed through this package's currency-aware API, not a
// separate shadow copy of it. A non-native currency (USD, etc.) instead
// gets its OWN, genuinely new sub-account, "<accountIdentifier>:<CURRENCY>"
// (e.g. "acct-001:USD"), registered lazily via
// doubleentry.RegisterAccountIfAbsent the first time it's needed. That
// keeps this package fully additive for every non-native currency: it
// never touches the balance any pre-existing endpoint depends on, and
// depositing/withdrawing/converting a foreign currency can never move
// acct-001's INR balance except via the one explicit, intentional path
// (a conversion where INR is itself one of the two legs).
//
// Client fund segregation: this package deliberately does NOT extend
// fundsegregation's account classification to new per-currency
// sub-accounts (e.g. it never classifies "acct-001:USD" as CLIENT) —
// mixing a foreign currency's minor units into the SAME custody-pool sum
// as acct-001/acct-002's INR minor units would make that sum numerically
// meaningless (cents and paise summed as if commensurable), which is a
// worse kind of corruption than just leaving it alone. Instead: a
// deposit/withdrawal into an account's NATIVE-currency wallet (INR, by
// this service's convention) routes through the existing
// *fundsegregation.SegregationGuard exactly when that account is already
// CLIENT-classified — calling PostClientMoneyMovement against the
// account's own identifier, the exact same path /client-funds/deposit
// already uses, so the pre-existing acct-001/acct-002 invariant is
// reused unchanged, never re-implemented or duplicated. Every other case
// (a non-native currency wallet, or a non-CLIENT account) posts a real,
// balanced journal entry directly, entirely outside the segregation
// guard's tracked accounts — a parallel path, per FEATURES.md's explicit
// "your call on the cleanest design" latitude, so the pre-existing
// invariant's account set is never touched, let alone corrupted, by any
// foreign-currency wallet activity.
//
// Currency conversion: fundsegregation.SegregationGuard.PostClientMoneyMovement
// only supports moving one account by one amount, so it can't directly
// express "debit currency A, credit currency B at a different numeric
// magnitude" in one call. ConvertBetweenCurrencyWallets instead builds
// one single balanced doubleentry.JournalEntry by hand: the primary two
// legs are the real currency movement (credit the source wallet the
// source amount, debit the destination wallet the converted amount), and
// — because internal/doubleentry's balance check is a plain numeric sum
// with no concept of currency, so a debit of 830000 against a credit of
// 10000 would be rejected outright as "does not balance" even though
// that is exactly the intended real-world effect of an 83.0 exchange
// rate — a third "fx-conversion-clearing" leg absorbs exactly the
// numeric difference between the two currencies' minor-unit magnitudes
// so the entry balances. That clearing account's balance is ledger
// plumbing to satisfy the currency-agnostic core, not real money and not
// meaningful as a currency figure. If either leg of the conversion is a
// CLIENT-classified account's NATIVE-currency wallet, the SAME journal
// entry also moves the custody pool by that leg's exact delta —
// reproducing fundsegregation's custody-mirroring invariant inline,
// since PostClientMoneyMovement itself can't be composed into a
// two-account swap.
//
// TODO(real build): no live FX feed (see above); no real foreign-currency
// custody/settlement rail (a real global-stocks broker needs an actual
// USD-denominated account somewhere, not just an internal ledger
// sub-account); no per-currency regulatory reporting (each currency a
// platform offers typically has its own regulatory/reporting
// obligations — e.g. LRS (Liberalised Remittance Scheme) limits and
// reporting for Indian residents investing in US stocks — none of which
// is modelled here); the combined-currency custody-pool sum noted above
// needs to become either a genuinely per-currency custody pool + per-
// currency invariant, or every balance normalized to one base currency
// via a LIVE rate before summing, not this package's static table.
package multicurrencywallet

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"

	"mercurius/ledger/internal/doubleentry"
	"mercurius/ledger/internal/fundsegregation"
)

// CurrencyCode is a short currency identifier, e.g. "INR", "USD".
type CurrencyCode string

var ErrMissingAccountIdentifier = errors.New("accountIdentifier must not be empty")
var ErrMissingCurrencyCode = errors.New("currencyCode must not be empty")
var ErrInvalidWalletAmount = errors.New("amount must be strictly positive")
var ErrInsufficientCurrencyWalletBalance = errors.New("amount exceeds that currency wallet's balance")
var ErrUnknownCurrencyPair = errors.New("no static FX rate is configured for this currency pair")
var ErrCannotConvertSameCurrency = errors.New("fromCurrencyCode and toCurrencyCode must differ")

// FxRateTable is a STATIC, ILLUSTRATIVE set of exchange rates — NOT a
// live market feed. Each entry is a one-directional rate: how many
// minor-unit-equivalents of toCurrencyCode one minor-unit-equivalent of
// fromCurrencyCode is worth (e.g. "USD/INR" -> 83.0 means 1 USD buys
// 83 INR). Reverse-direction rates are looked up separately — they are
// NOT automatically derived as a reciprocal, since a real FX desk would
// never assume a flat, spread-free reciprocal either; if you want
// INR->USD to work, configure it explicitly.
type FxRateTable struct {
	rateByOrderedCurrencyPairKey map[string]float64
}

// NewStaticFxRateTable builds a rate table from ordered-pair rates keyed
// "FROM/TO", e.g. map[string]float64{"USD/INR": 83.0, "INR/USD": 1.0 / 83.0}.
func NewStaticFxRateTable(rateByOrderedCurrencyPairKey map[string]float64) *FxRateTable {
	copiedRates := make(map[string]float64, len(rateByOrderedCurrencyPairKey))
	for pairKey, rate := range rateByOrderedCurrencyPairKey {
		copiedRates[pairKey] = rate
	}
	return &FxRateTable{rateByOrderedCurrencyPairKey: copiedRates}
}

func orderedCurrencyPairKey(fromCurrencyCode, toCurrencyCode CurrencyCode) string {
	return string(fromCurrencyCode) + "/" + string(toCurrencyCode)
}

// Rate looks up the static illustrative rate for converting FROM
// fromCurrencyCode TO toCurrencyCode. found is false if that exact
// ordered pair was never configured.
func (fxRateTable *FxRateTable) Rate(fromCurrencyCode, toCurrencyCode CurrencyCode) (rate float64, found bool) {
	rate, found = fxRateTable.rateByOrderedCurrencyPairKey[orderedCurrencyPairKey(fromCurrencyCode, toCurrencyCode)]
	return rate, found
}

// CurrencyWalletBalance is one currency's real balance for one account,
// as returned by GetWalletBalancesForAccount.
type CurrencyWalletBalance struct {
	CurrencyCode        CurrencyCode
	BalanceInMinorUnits int64
}

// CurrencyConversionResult is what a successful ConvertBetweenCurrencyWallets
// call actually did, for the caller (typically an HTTP handler) to report.
type CurrencyConversionResult struct {
	FromCurrencyCode                        CurrencyCode
	ToCurrencyCode                          CurrencyCode
	AmountDebitedFromSourceInMinorUnits     int64
	AmountCreditedToDestinationInMinorUnits int64
	RateApplied                             float64
}

// MultiCurrencyWalletRegistry is safe for concurrent use. It shares the
// SAME *doubleentry.InMemoryDoubleEntryLedgerBook and
// *fundsegregation.SegregationGuard the rest of this service uses —
// every wallet balance is a real, doubleentry-backed number, not a
// parallel bookkeeping system.
type MultiCurrencyWalletRegistry struct {
	ledgerBook       *doubleentry.InMemoryDoubleEntryLedgerBook
	segregationGuard *fundsegregation.SegregationGuard
	fxRateTable      *FxRateTable

	nativeCurrencyCode                  CurrencyCode
	custodyPoolAccountId                string
	walletExternalCashSuspenseAccountId string
	fxConversionClearingAccountId       string

	mutexGuardingOpenedWallets sync.Mutex
	openedCurrenciesByAccount  map[string]map[CurrencyCode]bool
}

// NewMultiCurrencyWalletRegistry wires a new wallet layer on top of an
// already-constructed ledger book and segregation guard.
// walletExternalCashSuspenseAccountId and fxConversionClearingAccountId
// are dedicated ledger accounts this package registers for itself (if
// they don't already exist) — the wallet layer's own counterparties for
// external cash movements and FX-rounding plumbing, kept separate from
// firm-clearing-acct / external-cash-suspense so a compliance reviewer
// can tell wallet-layer plumbing apart from the pre-existing accounting
// core's own clearing accounts at a glance.
func NewMultiCurrencyWalletRegistry(
	ledgerBook *doubleentry.InMemoryDoubleEntryLedgerBook,
	segregationGuard *fundsegregation.SegregationGuard,
	custodyPoolAccountId string,
	walletExternalCashSuspenseAccountId string,
	fxConversionClearingAccountId string,
	nativeCurrencyCode CurrencyCode,
	fxRateTable *FxRateTable,
) *MultiCurrencyWalletRegistry {
	ledgerBook.RegisterAccountIfAbsent(walletExternalCashSuspenseAccountId)
	ledgerBook.RegisterAccountIfAbsent(fxConversionClearingAccountId)

	return &MultiCurrencyWalletRegistry{
		ledgerBook:                          ledgerBook,
		segregationGuard:                    segregationGuard,
		fxRateTable:                         fxRateTable,
		nativeCurrencyCode:                  nativeCurrencyCode,
		custodyPoolAccountId:                custodyPoolAccountId,
		walletExternalCashSuspenseAccountId: walletExternalCashSuspenseAccountId,
		fxConversionClearingAccountId:       fxConversionClearingAccountId,
		openedCurrenciesByAccount:           make(map[string]map[CurrencyCode]bool),
	}
}

// ledgerAccountIdentifierFor is the one place this package decides which
// underlying doubleentry account a (accountIdentifier, currencyCode)
// wallet lives in — see the package doc for why the native currency IS
// an alias for the raw accountIdentifier account, and every other
// currency is a genuinely new "<accountIdentifier>:<CURRENCY>" account.
func (registry *MultiCurrencyWalletRegistry) ledgerAccountIdentifierFor(accountIdentifier string, currencyCode CurrencyCode) string {
	if currencyCode == registry.nativeCurrencyCode {
		return accountIdentifier
	}
	return fmt.Sprintf("%s:%s", accountIdentifier, currencyCode)
}

func (registry *MultiCurrencyWalletRegistry) currentWalletBalance(ledgerAccountIdentifier string) int64 {
	balance, lookupError := registry.ledgerBook.CurrentBalanceInMinorUnits(ledgerAccountIdentifier)
	if lookupError != nil {
		return 0
	}
	return balance
}

func (registry *MultiCurrencyWalletRegistry) isClientAccount(accountIdentifier string) bool {
	kind, isClassified := registry.segregationGuard.AccountKindOf(accountIdentifier)
	return isClassified && kind == fundsegregation.AccountKindClient
}

func (registry *MultiCurrencyWalletRegistry) markWalletOpened(accountIdentifier string, currencyCode CurrencyCode) {
	registry.mutexGuardingOpenedWallets.Lock()
	defer registry.mutexGuardingOpenedWallets.Unlock()

	if registry.openedCurrenciesByAccount[accountIdentifier] == nil {
		registry.openedCurrenciesByAccount[accountIdentifier] = make(map[CurrencyCode]bool)
	}
	registry.openedCurrenciesByAccount[accountIdentifier][currencyCode] = true
}

func validateWalletMutationArgs(accountIdentifier string, currencyCode CurrencyCode, amountInMinorUnits int64) error {
	if accountIdentifier == "" {
		return ErrMissingAccountIdentifier
	}
	if currencyCode == "" {
		return ErrMissingCurrencyCode
	}
	if amountInMinorUnits <= 0 {
		return ErrInvalidWalletAmount
	}
	return nil
}

// DepositIntoCurrencyWallet is a real, currency-scoped deposit. If
// accountIdentifier is CLIENT-classified (per the shared
// fundsegregation.SegregationGuard) and currencyCode is this registry's
// native currency, this routes through
// SegregationGuard.PostClientMoneyMovement — the exact same ring-fenced
// path /client-funds/deposit uses, keeping the custody-pool invariant
// intact automatically. Every other case (a non-native currency wallet,
// or an account that isn't CLIENT-classified at all) posts a real,
// balanced journal entry directly against this registry's dedicated
// wallet-external-cash-suspense account instead. A wallet that has never
// been deposited into before is opened lazily here — see
// GetWalletBalancesForAccount.
func (registry *MultiCurrencyWalletRegistry) DepositIntoCurrencyWallet(accountIdentifier string, currencyCode CurrencyCode, amountInMinorUnits int64) error {
	if validationError := validateWalletMutationArgs(accountIdentifier, currencyCode, amountInMinorUnits); validationError != nil {
		return validationError
	}

	description := fmt.Sprintf("multi-currency wallet deposit, account=%s currency=%s", accountIdentifier, currencyCode)

	if currencyCode == registry.nativeCurrencyCode && registry.isClientAccount(accountIdentifier) {
		if postError := registry.segregationGuard.PostClientMoneyMovement(accountIdentifier, amountInMinorUnits, description); postError != nil {
			return postError
		}
		registry.markWalletOpened(accountIdentifier, currencyCode)
		return nil
	}

	ledgerAccountIdentifier := registry.ledgerAccountIdentifierFor(accountIdentifier, currencyCode)
	registry.ledgerBook.RegisterAccountIfAbsent(ledgerAccountIdentifier)

	postError := registry.ledgerBook.PostJournalEntry(doubleentry.JournalEntry{
		HumanReadableDescription: description,
		DebitLines:               []doubleentry.LedgerAccountLine{{LedgerAccountIdentifier: ledgerAccountIdentifier, AmountInMinorUnits: amountInMinorUnits}},
		CreditLines:              []doubleentry.LedgerAccountLine{{LedgerAccountIdentifier: registry.walletExternalCashSuspenseAccountId, AmountInMinorUnits: amountInMinorUnits}},
	})
	if postError != nil {
		return postError
	}
	registry.markWalletOpened(accountIdentifier, currencyCode)
	return nil
}

// WithdrawFromCurrencyWallet is a real, currency-scoped withdrawal. It
// rejects outright if amountInMinorUnits exceeds THAT currency wallet's
// own balance — a healthy balance in a different currency for the same
// account never lets this succeed, since each (account, currency) pair
// is backed by its own, separate ledger account. Routing (segregation
// guard vs. raw journal entry) mirrors DepositIntoCurrencyWallet exactly.
func (registry *MultiCurrencyWalletRegistry) WithdrawFromCurrencyWallet(accountIdentifier string, currencyCode CurrencyCode, amountInMinorUnits int64) error {
	if validationError := validateWalletMutationArgs(accountIdentifier, currencyCode, amountInMinorUnits); validationError != nil {
		return validationError
	}

	ledgerAccountIdentifier := registry.ledgerAccountIdentifierFor(accountIdentifier, currencyCode)
	if registry.currentWalletBalance(ledgerAccountIdentifier) < amountInMinorUnits {
		return ErrInsufficientCurrencyWalletBalance
	}

	description := fmt.Sprintf("multi-currency wallet withdrawal, account=%s currency=%s", accountIdentifier, currencyCode)

	if currencyCode == registry.nativeCurrencyCode && registry.isClientAccount(accountIdentifier) {
		return registry.segregationGuard.PostClientMoneyMovement(accountIdentifier, -amountInMinorUnits, description)
	}

	return registry.ledgerBook.PostJournalEntry(doubleentry.JournalEntry{
		HumanReadableDescription: description,
		DebitLines:               []doubleentry.LedgerAccountLine{{LedgerAccountIdentifier: registry.walletExternalCashSuspenseAccountId, AmountInMinorUnits: amountInMinorUnits}},
		CreditLines:              []doubleentry.LedgerAccountLine{{LedgerAccountIdentifier: ledgerAccountIdentifier, AmountInMinorUnits: amountInMinorUnits}},
	})
}

// ConvertBetweenCurrencyWallets moves amountInFromCurrencyMinorUnits out
// of accountIdentifier's fromCurrencyCode wallet and the FxRateTable-
// computed equivalent into its toCurrencyCode wallet — both wallets
// belonging to the SAME account — via one real, balanced journal entry.
// See the package doc for exactly how that entry is constructed (the
// fx-conversion-clearing leg, and the custody-pool mirroring for
// CLIENT-classified native-currency legs).
//
// The converted amount is computed as
// round(amountInFromCurrencyMinorUnits * rate) — exact whenever the rate
// and amount combine to a whole number (e.g. 10,000 minor units at an
// 83.0 rate is exactly 830,000, no rounding at all), and rounded to the
// nearest minor unit otherwise. This is illustrative float64 arithmetic,
// not the fixed-point/rational math a real money-moving system should
// use — see the package doc's TODO section.
func (registry *MultiCurrencyWalletRegistry) ConvertBetweenCurrencyWallets(
	accountIdentifier string,
	fromCurrencyCode CurrencyCode,
	toCurrencyCode CurrencyCode,
	amountInFromCurrencyMinorUnits int64,
) (CurrencyConversionResult, error) {
	if accountIdentifier == "" {
		return CurrencyConversionResult{}, ErrMissingAccountIdentifier
	}
	if fromCurrencyCode == "" || toCurrencyCode == "" {
		return CurrencyConversionResult{}, ErrMissingCurrencyCode
	}
	if fromCurrencyCode == toCurrencyCode {
		return CurrencyConversionResult{}, ErrCannotConvertSameCurrency
	}
	if amountInFromCurrencyMinorUnits <= 0 {
		return CurrencyConversionResult{}, ErrInvalidWalletAmount
	}

	rate, rateFound := registry.fxRateTable.Rate(fromCurrencyCode, toCurrencyCode)
	if !rateFound {
		return CurrencyConversionResult{}, ErrUnknownCurrencyPair
	}

	fromLedgerAccountIdentifier := registry.ledgerAccountIdentifierFor(accountIdentifier, fromCurrencyCode)
	toLedgerAccountIdentifier := registry.ledgerAccountIdentifierFor(accountIdentifier, toCurrencyCode)

	if registry.currentWalletBalance(fromLedgerAccountIdentifier) < amountInFromCurrencyMinorUnits {
		return CurrencyConversionResult{}, ErrInsufficientCurrencyWalletBalance
	}

	convertedAmountInMinorUnits := int64(math.Round(float64(amountInFromCurrencyMinorUnits) * rate))
	if convertedAmountInMinorUnits <= 0 {
		return CurrencyConversionResult{}, ErrInvalidWalletAmount
	}

	registry.ledgerBook.RegisterAccountIfAbsent(toLedgerAccountIdentifier)

	debitLines := []doubleentry.LedgerAccountLine{{LedgerAccountIdentifier: toLedgerAccountIdentifier, AmountInMinorUnits: convertedAmountInMinorUnits}}
	creditLines := []doubleentry.LedgerAccountLine{{LedgerAccountIdentifier: fromLedgerAccountIdentifier, AmountInMinorUnits: amountInFromCurrencyMinorUnits}}

	isClient := registry.isClientAccount(accountIdentifier)
	if fromCurrencyCode == registry.nativeCurrencyCode && isClient {
		creditLines = append(creditLines, doubleentry.LedgerAccountLine{LedgerAccountIdentifier: registry.custodyPoolAccountId, AmountInMinorUnits: amountInFromCurrencyMinorUnits})
	}
	if toCurrencyCode == registry.nativeCurrencyCode && isClient {
		debitLines = append(debitLines, doubleentry.LedgerAccountLine{LedgerAccountIdentifier: registry.custodyPoolAccountId, AmountInMinorUnits: convertedAmountInMinorUnits})
	}

	totalDebits := sumLines(debitLines)
	totalCredits := sumLines(creditLines)
	if difference := totalDebits - totalCredits; difference > 0 {
		creditLines = append(creditLines, doubleentry.LedgerAccountLine{LedgerAccountIdentifier: registry.fxConversionClearingAccountId, AmountInMinorUnits: difference})
	} else if difference < 0 {
		debitLines = append(debitLines, doubleentry.LedgerAccountLine{LedgerAccountIdentifier: registry.fxConversionClearingAccountId, AmountInMinorUnits: -difference})
	}

	postError := registry.ledgerBook.PostJournalEntry(doubleentry.JournalEntry{
		HumanReadableDescription: fmt.Sprintf(
			"multi-currency wallet conversion, account=%s %s->%s rate=%.6f (STATIC/ILLUSTRATIVE)",
			accountIdentifier, fromCurrencyCode, toCurrencyCode, rate,
		),
		DebitLines:  debitLines,
		CreditLines: creditLines,
	})
	if postError != nil {
		return CurrencyConversionResult{}, postError
	}

	registry.markWalletOpened(accountIdentifier, toCurrencyCode)
	return CurrencyConversionResult{
		FromCurrencyCode:                        fromCurrencyCode,
		ToCurrencyCode:                          toCurrencyCode,
		AmountDebitedFromSourceInMinorUnits:     amountInFromCurrencyMinorUnits,
		AmountCreditedToDestinationInMinorUnits: convertedAmountInMinorUnits,
		RateApplied:                             rate,
	}, nil
}

func sumLines(lines []doubleentry.LedgerAccountLine) int64 {
	total := int64(0)
	for _, line := range lines {
		total += line.AmountInMinorUnits
	}
	return total
}

// GetWalletBalancesForAccount returns every currency wallet ever opened
// (by a deposit or as a conversion's destination) for accountIdentifier,
// each backed by its own real doubleentry balance, sorted by currency
// code for a deterministic response. An account with no wallets opened
// yet gets an empty (non-nil) slice, not an error.
func (registry *MultiCurrencyWalletRegistry) GetWalletBalancesForAccount(accountIdentifier string) []CurrencyWalletBalance {
	registry.mutexGuardingOpenedWallets.Lock()
	openedCurrencies := make([]CurrencyCode, 0, len(registry.openedCurrenciesByAccount[accountIdentifier]))
	for currencyCode := range registry.openedCurrenciesByAccount[accountIdentifier] {
		openedCurrencies = append(openedCurrencies, currencyCode)
	}
	registry.mutexGuardingOpenedWallets.Unlock()

	sort.Slice(openedCurrencies, func(i, j int) bool { return openedCurrencies[i] < openedCurrencies[j] })

	balances := make([]CurrencyWalletBalance, 0, len(openedCurrencies))
	for _, currencyCode := range openedCurrencies {
		ledgerAccountIdentifier := registry.ledgerAccountIdentifierFor(accountIdentifier, currencyCode)
		balances = append(balances, CurrencyWalletBalance{
			CurrencyCode:        currencyCode,
			BalanceInMinorUnits: registry.currentWalletBalance(ledgerAccountIdentifier),
		})
	}
	return balances
}
