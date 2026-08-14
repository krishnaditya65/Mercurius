// Package retirementaccounts is a real account-type classification and
// rules engine for illustrative tax-advantaged retirement wrappers (an
// "NPS-equivalent" / "IRA-equivalent" structure) — FEATURES.md §17,
// "Wealth & Product Breadth", a `[P4]` item ("Retirement account wrappers
// (NPS/IRA-equivalent, tax-advantaged structures per jurisdiction)").
//
// LOUD CAVEAT: the underlying TAX-ADVANTAGE regime this package models is
// entirely ILLUSTRATIVE — it is NOT integrated with any real tax authority
// (India's PFRDA/NPS, the US IRS's IRA rules, or any other jurisdiction's
// real retirement-account regulator), and no real tax benefit is computed,
// claimed, or filed anywhere in this repo. What IS real, and genuinely
// enforced, is the RULES ENGINE shape a retirement wrapper needs: a
// per-account-type annual contribution limit that's actually checked and
// rejected when exceeded, and a lock-in/withdrawal-restriction rule that's
// actually enforced against an illustrative retirement age/date — the same
// honest-but-real pattern internal/sipscheduler's step-up math and
// internal/basketrebalancing's rebalance math use: illustrative INPUTS
// (the specific limit numbers, the specific retirement age), but REAL,
// ENFORCED logic over them.
package retirementaccounts

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"
)

// AccountType classifies which illustrative tax-advantaged wrapper an
// account is. Each type carries its own illustrative annual contribution
// limit and minimum retirement age — see accountTypeRules below.
type AccountType string

const (
	AccountTypeNpsEquivalent AccountType = "NPS_EQUIVALENT"
	AccountTypeIraEquivalent AccountType = "IRA_EQUIVALENT"
)

// accountTypeRule bundles one AccountType's illustrative, hand-picked
// annual contribution limit and minimum retirement age. NOT sourced from
// any real jurisdiction's actual current-year limits.
type accountTypeRule struct {
	AnnualContributionLimitInMinorUnits int64
	MinimumRetirementAge                int
}

var accountTypeRules = map[AccountType]accountTypeRule{
	// Illustrative, loosely modeled on India's NPS annual contribution
	// norms and its age-60 vesting — NOT the real current PFRDA limit.
	AccountTypeNpsEquivalent: {AnnualContributionLimitInMinorUnits: 15_00_000_00, MinimumRetirementAge: 60},
	// Illustrative, loosely modeled on a US-style IRA annual contribution
	// cap and age-59½-ish (rounded to 60 here for simplicity) early-
	// withdrawal threshold — NOT the real current IRS limit.
	AccountTypeIraEquivalent: {AnnualContributionLimitInMinorUnits: 7_00_000_00, MinimumRetirementAge: 60},
}

var ErrUnknownAccountType = fmt.Errorf("unrecognized retirement account type")
var ErrInvalidDateOfBirth = fmt.Errorf("date of birth must be in the past")
var ErrUnknownAccount = fmt.Errorf("no such retirement account exists")
var ErrInvalidContributionAmount = fmt.Errorf("contribution amount must be strictly positive")
var ErrContributionExceedsAnnualLimit = fmt.Errorf("contribution would exceed this account type's illustrative annual contribution limit")
var ErrInvalidWithdrawalAmount = fmt.Errorf("withdrawal amount must be strictly positive")
var ErrInsufficientBalance = fmt.Errorf("withdrawal amount exceeds the account's current balance")
var ErrWithdrawalBeforeRetirementAge = fmt.Errorf("withdrawal rejected: account holder has not yet reached this account type's illustrative minimum retirement age")

// Contribution is one recorded contribution into a retirement account,
// tagged with the calendar year it counts against for annual-limit
// purposes.
type Contribution struct {
	AmountInMinorUnits int64
	ContributedAt      time.Time
	CalendarYear       int
}

// Account is one illustrative tax-advantaged retirement wrapper.
type Account struct {
	AccountId           string
	AccountIdentifier   string
	AccountType         AccountType
	DateOfBirth         time.Time
	BalanceInMinorUnits int64
	OpenedAt            time.Time
	Contributions       []Contribution
}

// RulesEngine is safe for concurrent use.
type RulesEngine struct {
	mutexGuardingState sync.Mutex
	accountsById       map[string]*Account
}

// NewRulesEngine builds an empty rules engine.
func NewRulesEngine() *RulesEngine {
	return &RulesEngine{accountsById: make(map[string]*Account)}
}

// OpenAccount opens a new retirement account of accountType for
// accountIdentifier.
func (engine *RulesEngine) OpenAccount(accountIdentifier string, accountType AccountType, dateOfBirth time.Time, now time.Time) (*Account, error) {
	if _, wasKnownType := accountTypeRules[accountType]; !wasKnownType {
		return nil, ErrUnknownAccountType
	}
	if !dateOfBirth.Before(now) {
		return nil, ErrInvalidDateOfBirth
	}

	accountId, genError := generateAccountId()
	if genError != nil {
		return nil, fmt.Errorf("failed to generate account id: %w", genError)
	}

	account := &Account{
		AccountId:         accountId,
		AccountIdentifier: accountIdentifier,
		AccountType:       accountType,
		DateOfBirth:       dateOfBirth,
		OpenedAt:          now,
		Contributions:     make([]Contribution, 0),
	}

	engine.mutexGuardingState.Lock()
	engine.accountsById[accountId] = account
	engine.mutexGuardingState.Unlock()

	return account, nil
}

// Contribute records a contribution into accountId, REJECTING it if it
// would push the SUM of this calendar year's contributions (as of now)
// past this account type's illustrative AnnualContributionLimitInMinorUnits
// — a real, enforced check, not just a display figure.
func (engine *RulesEngine) Contribute(accountId string, amountInMinorUnits int64, now time.Time) (*Account, error) {
	if amountInMinorUnits <= 0 {
		return nil, ErrInvalidContributionAmount
	}

	engine.mutexGuardingState.Lock()
	defer engine.mutexGuardingState.Unlock()

	account, wasFound := engine.accountsById[accountId]
	if !wasFound {
		return nil, ErrUnknownAccount
	}

	rule := accountTypeRules[account.AccountType]
	calendarYear := now.Year()

	alreadyContributedThisYear := int64(0)
	for _, contribution := range account.Contributions {
		if contribution.CalendarYear == calendarYear {
			alreadyContributedThisYear += contribution.AmountInMinorUnits
		}
	}

	if alreadyContributedThisYear+amountInMinorUnits > rule.AnnualContributionLimitInMinorUnits {
		return nil, ErrContributionExceedsAnnualLimit
	}

	account.Contributions = append(account.Contributions, Contribution{
		AmountInMinorUnits: amountInMinorUnits,
		ContributedAt:      now,
		CalendarYear:       calendarYear,
	})
	account.BalanceInMinorUnits += amountInMinorUnits

	return account, nil
}

// Withdraw REJECTS the withdrawal unless the account holder has reached
// this account type's illustrative MinimumRetirementAge as of now (a real,
// enforced lock-in rule — age is computed via calendar-aware
// time.Time.AddDate, the same pattern internal/sipscheduler uses for
// step-up anniversaries: the holder has "reached" the age once
// dateOfBirth.AddDate(minimumRetirementAge, 0, 0) is on or before now) AND
// unless the balance covers it.
func (engine *RulesEngine) Withdraw(accountId string, amountInMinorUnits int64, now time.Time) (*Account, error) {
	if amountInMinorUnits <= 0 {
		return nil, ErrInvalidWithdrawalAmount
	}

	engine.mutexGuardingState.Lock()
	defer engine.mutexGuardingState.Unlock()

	account, wasFound := engine.accountsById[accountId]
	if !wasFound {
		return nil, ErrUnknownAccount
	}

	rule := accountTypeRules[account.AccountType]
	eligibleWithdrawalDate := account.DateOfBirth.AddDate(rule.MinimumRetirementAge, 0, 0)
	if now.Before(eligibleWithdrawalDate) {
		return nil, ErrWithdrawalBeforeRetirementAge
	}

	if amountInMinorUnits > account.BalanceInMinorUnits {
		return nil, ErrInsufficientBalance
	}

	account.BalanceInMinorUnits -= amountInMinorUnits
	return account, nil
}

// RemainingContributionRoomForYear returns how much more accountId can
// contribute in calendarYear before hitting its illustrative annual limit.
func (engine *RulesEngine) RemainingContributionRoomForYear(accountId string, calendarYear int) (int64, error) {
	engine.mutexGuardingState.Lock()
	defer engine.mutexGuardingState.Unlock()

	account, wasFound := engine.accountsById[accountId]
	if !wasFound {
		return 0, ErrUnknownAccount
	}

	rule := accountTypeRules[account.AccountType]
	alreadyContributed := int64(0)
	for _, contribution := range account.Contributions {
		if contribution.CalendarYear == calendarYear {
			alreadyContributed += contribution.AmountInMinorUnits
		}
	}

	remaining := rule.AnnualContributionLimitInMinorUnits - alreadyContributed
	if remaining < 0 {
		remaining = 0
	}
	return remaining, nil
}

// LookupAccount returns the account, or false if accountId isn't known.
func (engine *RulesEngine) LookupAccount(accountId string) (*Account, bool) {
	engine.mutexGuardingState.Lock()
	defer engine.mutexGuardingState.Unlock()

	account, wasFound := engine.accountsById[accountId]
	return account, wasFound
}

// AccountsForHolder returns every retirement account accountIdentifier
// holds, sorted by OpenedAt.
func (engine *RulesEngine) AccountsForHolder(accountIdentifier string) []*Account {
	engine.mutexGuardingState.Lock()
	defer engine.mutexGuardingState.Unlock()

	matching := make([]*Account, 0)
	for _, account := range engine.accountsById {
		if account.AccountIdentifier == accountIdentifier {
			matching = append(matching, account)
		}
	}
	sort.Slice(matching, func(i, j int) bool { return matching[i].OpenedAt.Before(matching[j].OpenedAt) })
	return matching
}

func generateAccountId() (string, error) {
	randomBytes := make([]byte, 8)
	if _, readError := rand.Read(randomBytes); readError != nil {
		return "", readError
	}
	return "retire-acct-" + hex.EncodeToString(randomBytes), nil
}
