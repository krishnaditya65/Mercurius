// Package strategyfollowing implements FEATURES.md §11's "Social/
// copy-trading: follow verified strategies or traders (opt-in,
// disclosed)" — and ONLY that. This is deliberately, explicitly NOT a
// copy-trading engine: there is no order mirroring, no auto-replication
// of a followed strategy's trades into a follower's account, and no
// automatic following of anything. It is just a real, persisted (in
// memory, like every other piece of state in this service)
// follow/unfollow relationship graph between accountIdentifiers and
// strategyIdentifiers (the same strategyIdentifier concept
// internal/algolimits already tags orders with), plus a small public
// registry of "verified" strategies an admin has explicitly flagged —
// reusing the spirit of backoffice's admin-approval pattern (a human
// decision gates something becoming publicly visible/followable) without
// depending on backoffice itself, since this state has nothing to do
// with KYC.
//
// A real build's order-mirroring feature (if ever built) would be a
// SEPARATE, much bigger, much riskier piece of work — this package
// exists purely to prove the disclosed opt-in follow relationship end to
// end.
//
// TODO(real build): in-memory only (a restart loses every follow
// relationship and every verified-strategy flag); no auth (any caller
// can follow/unfollow on behalf of any accountIdentifier, and any caller
// can mark any strategy verified — a real build gates the admin-verify
// endpoint behind whatever auth backoffice's own admin actions use); no
// strategy metadata beyond a display name/description (no track record,
// no performance stats, no risk disclosures beyond the ones documented
// here).
package strategyfollowing

import (
	"errors"
	"sort"
	"sync"
)

var (
	// ErrStrategyIdentifierRequired is returned when a caller supplies an
	// empty strategyIdentifier.
	ErrStrategyIdentifierRequired = errors.New("strategyIdentifier is required")

	// ErrAccountIdentifierRequired is returned when a caller supplies an
	// empty accountIdentifier.
	ErrAccountIdentifierRequired = errors.New("accountIdentifier is required")

	// ErrStrategyNotVerified is returned by Follow when the target
	// strategyIdentifier has not been marked verified by an admin —
	// following is only ever allowed against the publicly disclosed,
	// admin-verified list, never an arbitrary unvetted strategyIdentifier.
	ErrStrategyNotVerified = errors.New("strategy is not on the verified list — only verified strategies can be followed")
)

// VerifiedStrategy is one entry on the public, admin-curated list of
// strategies retail accounts are allowed to follow.
type VerifiedStrategy struct {
	StrategyIdentifier string `json:"strategyIdentifier"`
	DisplayName        string `json:"displayName"`
	Description        string `json:"description"`
}

// VerifiedStrategyWithFollowerCount is what GET /strategies returns per
// entry — the verified strategy's own metadata plus a real, live
// follower count derived from the follow graph.
type VerifiedStrategyWithFollowerCount struct {
	VerifiedStrategy
	FollowerCount int `json:"followerCount"`
}

// Registry is the mutex-guarded, in-memory home for both the verified-
// strategy list and the follow relationship graph.
type Registry struct {
	mutexGuardingState sync.Mutex

	verifiedStrategiesById map[string]VerifiedStrategy
	// followedStrategiesByAccount[accountIdentifier] is the set of
	// strategyIdentifiers that account currently follows.
	followedStrategiesByAccount map[string]map[string]bool
	// followingAccountsByStrategy[strategyIdentifier] is the set of
	// accountIdentifiers currently following that strategy — kept as the
	// inverse index of followedStrategiesByAccount so both
	// FollowersOfStrategy and FollowingOfAccount are O(1) lookups rather
	// than a full scan.
	followingAccountsByStrategy map[string]map[string]bool
}

// NewRegistry builds an empty Registry — no strategy starts verified, no
// account starts following anything.
func NewRegistry() *Registry {
	return &Registry{
		verifiedStrategiesById:      make(map[string]VerifiedStrategy),
		followedStrategiesByAccount: make(map[string]map[string]bool),
		followingAccountsByStrategy: make(map[string]map[string]bool),
	}
}

// MarkStrategyVerified adds (or updates the display name/description of)
// a strategy on the public verified list — the admin-approval step that
// gates a strategy ever becoming followable. Calling this again for an
// already-verified strategyIdentifier just overwrites its metadata; it
// never un-verifies anything (there's no "unverify" in this minimal
// scope — a real build would want one).
func (registry *Registry) MarkStrategyVerified(strategyIdentifier string, displayName string, description string) error {
	if strategyIdentifier == "" {
		return ErrStrategyIdentifierRequired
	}

	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	registry.verifiedStrategiesById[strategyIdentifier] = VerifiedStrategy{
		StrategyIdentifier: strategyIdentifier,
		DisplayName:        displayName,
		Description:        description,
	}
	return nil
}

// IsStrategyVerified reports whether strategyIdentifier is currently on
// the verified list.
func (registry *Registry) IsStrategyVerified(strategyIdentifier string) bool {
	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	_, isVerified := registry.verifiedStrategiesById[strategyIdentifier]
	return isVerified
}

// ListVerifiedStrategies returns every verified strategy with its live
// follower count, sorted by strategyIdentifier for a deterministic
// response.
func (registry *Registry) ListVerifiedStrategies() []VerifiedStrategyWithFollowerCount {
	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	results := make([]VerifiedStrategyWithFollowerCount, 0, len(registry.verifiedStrategiesById))
	for _, strategy := range registry.verifiedStrategiesById {
		results = append(results, VerifiedStrategyWithFollowerCount{
			VerifiedStrategy: strategy,
			FollowerCount:    len(registry.followingAccountsByStrategy[strategy.StrategyIdentifier]),
		})
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].StrategyIdentifier < results[j].StrategyIdentifier
	})
	return results
}

// Follow records accountIdentifier as following strategyIdentifier — the
// one and only opt-in action this package supports. Idempotent: following
// a strategy you already follow is a no-op, not an error. Returns
// ErrStrategyNotVerified if strategyIdentifier isn't on the verified
// list — a follow relationship can only ever point at a strategy the
// account was shown as verified/disclosed, never an arbitrary id.
func (registry *Registry) Follow(accountIdentifier string, strategyIdentifier string) error {
	if accountIdentifier == "" {
		return ErrAccountIdentifierRequired
	}
	if strategyIdentifier == "" {
		return ErrStrategyIdentifierRequired
	}

	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	if _, isVerified := registry.verifiedStrategiesById[strategyIdentifier]; !isVerified {
		return ErrStrategyNotVerified
	}

	if registry.followedStrategiesByAccount[accountIdentifier] == nil {
		registry.followedStrategiesByAccount[accountIdentifier] = make(map[string]bool)
	}
	registry.followedStrategiesByAccount[accountIdentifier][strategyIdentifier] = true

	if registry.followingAccountsByStrategy[strategyIdentifier] == nil {
		registry.followingAccountsByStrategy[strategyIdentifier] = make(map[string]bool)
	}
	registry.followingAccountsByStrategy[strategyIdentifier][accountIdentifier] = true

	return nil
}

// Unfollow removes the follow relationship, if any. Idempotent:
// unfollowing a strategy you don't follow (or that doesn't exist at all)
// is a no-op, not an error — mirrors watchlist.rs's removeSymbol
// idempotency convention elsewhere in this repo.
func (registry *Registry) Unfollow(accountIdentifier string, strategyIdentifier string) error {
	if accountIdentifier == "" {
		return ErrAccountIdentifierRequired
	}
	if strategyIdentifier == "" {
		return ErrStrategyIdentifierRequired
	}

	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	if followedStrategies, exists := registry.followedStrategiesByAccount[accountIdentifier]; exists {
		delete(followedStrategies, strategyIdentifier)
	}
	if followingAccounts, exists := registry.followingAccountsByStrategy[strategyIdentifier]; exists {
		delete(followingAccounts, accountIdentifier)
	}
	return nil
}

// FollowersOfStrategy returns every accountIdentifier currently
// following strategyIdentifier, sorted for a deterministic response.
func (registry *Registry) FollowersOfStrategy(strategyIdentifier string) []string {
	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	return sortedKeys(registry.followingAccountsByStrategy[strategyIdentifier])
}

// FollowingOfAccount returns every strategyIdentifier accountIdentifier
// currently follows, sorted for a deterministic response.
func (registry *Registry) FollowingOfAccount(accountIdentifier string) []string {
	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	return sortedKeys(registry.followedStrategiesByAccount[accountIdentifier])
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key, isPresent := range set {
		if isPresent {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}
