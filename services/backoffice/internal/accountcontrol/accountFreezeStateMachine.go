// Package accountcontrol is the real slice of FEATURES.md §14's "account
// freeze/unfreeze" backoffice feature: someone (compliance, support, an
// automated AML flag eventually) needs to be able to stop one account
// from trading immediately, independent of that account's KYC or margin
// status. This is the manual-intervention tool ARCHITECTURE.md §1 says
// every real platform needs from day one.
//
// TODO(real build): needs persistence, an actual admin auth/RBAC layer
// (right now anyone who can reach this HTTP endpoint can freeze any
// account), and an audit trail of who froze/unfroze what and when.
package accountcontrol

import "sync"

type AccountFreezeStatus struct {
	AccountIdentifier string
	IsFrozen          bool
	FreezeReason      string
}

// AccountFreezeStateMachine is an in-memory registry of frozen accounts.
// Absence from the map means "not frozen" — there's no need to
// pre-register every account, unlike kyc-onboarding's KYC records.
type AccountFreezeStateMachine struct {
	mutexGuardingFrozenAccounts sync.RWMutex
	freezeReasonByAccountId     map[string]string
}

func NewAccountFreezeStateMachine() *AccountFreezeStateMachine {
	return &AccountFreezeStateMachine{
		freezeReasonByAccountId: make(map[string]string),
	}
}

func (stateMachine *AccountFreezeStateMachine) FreezeAccount(accountIdentifier string, freezeReason string) {
	stateMachine.mutexGuardingFrozenAccounts.Lock()
	defer stateMachine.mutexGuardingFrozenAccounts.Unlock()

	stateMachine.freezeReasonByAccountId[accountIdentifier] = freezeReason
}

func (stateMachine *AccountFreezeStateMachine) UnfreezeAccount(accountIdentifier string) {
	stateMachine.mutexGuardingFrozenAccounts.Lock()
	defer stateMachine.mutexGuardingFrozenAccounts.Unlock()

	delete(stateMachine.freezeReasonByAccountId, accountIdentifier)
}

func (stateMachine *AccountFreezeStateMachine) CheckFreezeStatus(accountIdentifier string) AccountFreezeStatus {
	stateMachine.mutexGuardingFrozenAccounts.RLock()
	defer stateMachine.mutexGuardingFrozenAccounts.RUnlock()

	freezeReason, isFrozen := stateMachine.freezeReasonByAccountId[accountIdentifier]
	return AccountFreezeStatus{
		AccountIdentifier: accountIdentifier,
		IsFrozen:          isFrozen,
		FreezeReason:      freezeReason,
	}
}
