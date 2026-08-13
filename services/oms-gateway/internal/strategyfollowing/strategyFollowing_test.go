package strategyfollowing

import (
	"errors"
	"testing"
)

func TestNewRegistryStartsWithNoVerifiedStrategies(t *testing.T) {
	registry := NewRegistry()
	if strategies := registry.ListVerifiedStrategies(); len(strategies) != 0 {
		t.Fatalf("expected no verified strategies, got %v", strategies)
	}
}

func TestMarkStrategyVerifiedRequiresAnIdentifier(t *testing.T) {
	registry := NewRegistry()
	if err := registry.MarkStrategyVerified("", "Nice Name", "desc"); !errors.Is(err, ErrStrategyIdentifierRequired) {
		t.Fatalf("expected ErrStrategyIdentifierRequired, got %v", err)
	}
}

func TestMarkStrategyVerifiedThenListedWithZeroFollowers(t *testing.T) {
	registry := NewRegistry()
	if err := registry.MarkStrategyVerified("algo-1", "Momentum Alpha", "a momentum strategy"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	strategies := registry.ListVerifiedStrategies()
	if len(strategies) != 1 {
		t.Fatalf("expected 1 verified strategy, got %d", len(strategies))
	}
	if strategies[0].StrategyIdentifier != "algo-1" || strategies[0].DisplayName != "Momentum Alpha" {
		t.Fatalf("unexpected strategy metadata: %+v", strategies[0])
	}
	if strategies[0].FollowerCount != 0 {
		t.Fatalf("expected 0 followers, got %d", strategies[0].FollowerCount)
	}
}

func TestMarkStrategyVerifiedTwiceOverwritesMetadataWithoutDuplicating(t *testing.T) {
	registry := NewRegistry()
	_ = registry.MarkStrategyVerified("algo-1", "Old Name", "old desc")
	_ = registry.MarkStrategyVerified("algo-1", "New Name", "new desc")

	strategies := registry.ListVerifiedStrategies()
	if len(strategies) != 1 {
		t.Fatalf("expected exactly 1 entry after re-verifying, got %d", len(strategies))
	}
	if strategies[0].DisplayName != "New Name" {
		t.Fatalf("expected metadata to be overwritten, got %q", strategies[0].DisplayName)
	}
}

func TestFollowRejectsAnUnverifiedStrategy(t *testing.T) {
	registry := NewRegistry()
	err := registry.Follow("acct-001", "not-verified")
	if !errors.Is(err, ErrStrategyNotVerified) {
		t.Fatalf("expected ErrStrategyNotVerified, got %v", err)
	}
}

func TestFollowRequiresAccountAndStrategyIdentifiers(t *testing.T) {
	registry := NewRegistry()
	_ = registry.MarkStrategyVerified("algo-1", "Momentum Alpha", "")

	if err := registry.Follow("", "algo-1"); !errors.Is(err, ErrAccountIdentifierRequired) {
		t.Fatalf("expected ErrAccountIdentifierRequired, got %v", err)
	}
	if err := registry.Follow("acct-001", ""); !errors.Is(err, ErrStrategyIdentifierRequired) {
		t.Fatalf("expected ErrStrategyIdentifierRequired, got %v", err)
	}
}

func TestFollowThenAppearsInFollowersAndFollowing(t *testing.T) {
	registry := NewRegistry()
	_ = registry.MarkStrategyVerified("algo-1", "Momentum Alpha", "")

	if err := registry.Follow("acct-001", "algo-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	followers := registry.FollowersOfStrategy("algo-1")
	if len(followers) != 1 || followers[0] != "acct-001" {
		t.Fatalf("expected [acct-001], got %v", followers)
	}

	following := registry.FollowingOfAccount("acct-001")
	if len(following) != 1 || following[0] != "algo-1" {
		t.Fatalf("expected [algo-1], got %v", following)
	}

	strategies := registry.ListVerifiedStrategies()
	if strategies[0].FollowerCount != 1 {
		t.Fatalf("expected follower count 1, got %d", strategies[0].FollowerCount)
	}
}

func TestFollowingTheSameStrategyTwiceIsIdempotent(t *testing.T) {
	registry := NewRegistry()
	_ = registry.MarkStrategyVerified("algo-1", "Momentum Alpha", "")

	_ = registry.Follow("acct-001", "algo-1")
	_ = registry.Follow("acct-001", "algo-1")

	if followers := registry.FollowersOfStrategy("algo-1"); len(followers) != 1 {
		t.Fatalf("expected exactly 1 follower after following twice, got %v", followers)
	}
}

func TestUnfollowRemovesTheRelationship(t *testing.T) {
	registry := NewRegistry()
	_ = registry.MarkStrategyVerified("algo-1", "Momentum Alpha", "")
	_ = registry.Follow("acct-001", "algo-1")

	if err := registry.Unfollow("acct-001", "algo-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if followers := registry.FollowersOfStrategy("algo-1"); len(followers) != 0 {
		t.Fatalf("expected no followers after unfollow, got %v", followers)
	}
	if following := registry.FollowingOfAccount("acct-001"); len(following) != 0 {
		t.Fatalf("expected no following after unfollow, got %v", following)
	}
}

func TestUnfollowingSomethingNeverFollowedIsANoOp(t *testing.T) {
	registry := NewRegistry()
	_ = registry.MarkStrategyVerified("algo-1", "Momentum Alpha", "")

	if err := registry.Unfollow("acct-001", "algo-1"); err != nil {
		t.Fatalf("expected idempotent no-op, got error: %v", err)
	}
}

func TestUnfollowRequiresAccountAndStrategyIdentifiers(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Unfollow("", "algo-1"); !errors.Is(err, ErrAccountIdentifierRequired) {
		t.Fatalf("expected ErrAccountIdentifierRequired, got %v", err)
	}
	if err := registry.Unfollow("acct-001", ""); !errors.Is(err, ErrStrategyIdentifierRequired) {
		t.Fatalf("expected ErrStrategyIdentifierRequired, got %v", err)
	}
}

func TestTwoAccountsCanFollowTheSameStrategyIndependently(t *testing.T) {
	registry := NewRegistry()
	_ = registry.MarkStrategyVerified("algo-1", "Momentum Alpha", "")

	_ = registry.Follow("acct-001", "algo-1")
	_ = registry.Follow("acct-002", "algo-1")

	followers := registry.FollowersOfStrategy("algo-1")
	if len(followers) != 2 || followers[0] != "acct-001" || followers[1] != "acct-002" {
		t.Fatalf("expected [acct-001 acct-002], got %v", followers)
	}
}

func TestOneAccountCanFollowMultipleStrategiesIndependently(t *testing.T) {
	registry := NewRegistry()
	_ = registry.MarkStrategyVerified("algo-1", "Momentum Alpha", "")
	_ = registry.MarkStrategyVerified("algo-2", "Mean Reversion Beta", "")

	_ = registry.Follow("acct-001", "algo-1")
	_ = registry.Follow("acct-001", "algo-2")

	following := registry.FollowingOfAccount("acct-001")
	if len(following) != 2 || following[0] != "algo-1" || following[1] != "algo-2" {
		t.Fatalf("expected [algo-1 algo-2], got %v", following)
	}
}

func TestIsStrategyVerifiedReflectsTheVerifiedList(t *testing.T) {
	registry := NewRegistry()
	if registry.IsStrategyVerified("algo-1") {
		t.Fatalf("expected algo-1 to not be verified yet")
	}
	_ = registry.MarkStrategyVerified("algo-1", "Momentum Alpha", "")
	if !registry.IsStrategyVerified("algo-1") {
		t.Fatalf("expected algo-1 to be verified")
	}
}

func TestFollowersOfAnUnknownStrategyIsEmptyNotAnError(t *testing.T) {
	registry := NewRegistry()
	if followers := registry.FollowersOfStrategy("never-heard-of-it"); len(followers) != 0 {
		t.Fatalf("expected empty, got %v", followers)
	}
}

func TestFollowingOfAnUnknownAccountIsEmptyNotAnError(t *testing.T) {
	registry := NewRegistry()
	if following := registry.FollowingOfAccount("never-heard-of-it"); len(following) != 0 {
		t.Fatalf("expected empty, got %v", following)
	}
}

func TestListVerifiedStrategiesIsSortedByStrategyIdentifier(t *testing.T) {
	registry := NewRegistry()
	_ = registry.MarkStrategyVerified("zeta", "Z", "")
	_ = registry.MarkStrategyVerified("alpha", "A", "")

	strategies := registry.ListVerifiedStrategies()
	if len(strategies) != 2 || strategies[0].StrategyIdentifier != "alpha" || strategies[1].StrategyIdentifier != "zeta" {
		t.Fatalf("expected [alpha zeta], got %v", strategies)
	}
}
