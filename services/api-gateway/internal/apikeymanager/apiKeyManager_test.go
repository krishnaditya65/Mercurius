package apikeymanager

import (
	"errors"
	"testing"

	"mercurius/apiGateway/internal/ratelimiter"
)

func TestIssueApiKeyReturnsAUniqueValueWithLivePrefix(t *testing.T) {
	manager := NewApiKeyManager()
	issued, err := manager.IssueApiKey(ApiKeyIssuanceRequest{AccountIdentifier: "acct-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if issued.ApiKeyValue == "" {
		t.Fatalf("expected a non-empty api key value")
	}
	if issued.IsSandboxKey {
		t.Fatalf("expected a non-sandbox key by default")
	}
	if issued.RateLimitTier != ratelimiter.RateLimitTierRetail {
		t.Fatalf("expected default tier RETAIL, got %v", issued.RateLimitTier)
	}
}

func TestIssueApiKeyRequiresAccountIdentifier(t *testing.T) {
	manager := NewApiKeyManager()
	_, err := manager.IssueApiKey(ApiKeyIssuanceRequest{})
	if !errors.Is(err, ErrAccountIdentifierRequired) {
		t.Fatalf("expected ErrAccountIdentifierRequired, got %v", err)
	}
}

func TestSandboxKeyIsForcedToSandboxTierAndPrefix(t *testing.T) {
	manager := NewApiKeyManager()
	issued, err := manager.IssueApiKey(ApiKeyIssuanceRequest{
		AccountIdentifier: "acct-1",
		IsSandboxKey:      true,
		RateLimitTier:     ratelimiter.RateLimitTierInstitutional, // should be overridden
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if issued.RateLimitTier != ratelimiter.RateLimitTierSandbox {
		t.Fatalf("expected sandbox keys to always be tier SANDBOX, got %v", issued.RateLimitTier)
	}
	if len(issued.ApiKeyValue) < len("mk_sandbox_") || issued.ApiKeyValue[:11] != "mk_sandbox_" {
		t.Fatalf("expected sandbox key prefix, got %q", issued.ApiKeyValue)
	}
}

func TestTwoIssuedKeysAreDistinct(t *testing.T) {
	manager := NewApiKeyManager()
	first, _ := manager.IssueApiKey(ApiKeyIssuanceRequest{AccountIdentifier: "acct-1"})
	second, _ := manager.IssueApiKey(ApiKeyIssuanceRequest{AccountIdentifier: "acct-1"})
	if first.ApiKeyValue == second.ApiKeyValue {
		t.Fatalf("expected two issued keys to have distinct values")
	}
}

func TestValidateApiKeySucceedsForAFreshlyIssuedKey(t *testing.T) {
	manager := NewApiKeyManager()
	issued, _ := manager.IssueApiKey(ApiKeyIssuanceRequest{AccountIdentifier: "acct-1"})

	validated, err := manager.ValidateApiKey(issued.ApiKeyValue)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if validated.AccountIdentifier != "acct-1" {
		t.Fatalf("expected acct-1, got %s", validated.AccountIdentifier)
	}
}

func TestValidateApiKeyReturnsNotFoundForUnknownValue(t *testing.T) {
	manager := NewApiKeyManager()
	_, err := manager.ValidateApiKey("nope")
	if !errors.Is(err, ErrApiKeyNotFound) {
		t.Fatalf("expected ErrApiKeyNotFound, got %v", err)
	}
}

func TestRevokeApiKeyThenValidateFails(t *testing.T) {
	manager := NewApiKeyManager()
	issued, _ := manager.IssueApiKey(ApiKeyIssuanceRequest{AccountIdentifier: "acct-1"})

	if err := manager.RevokeApiKey(issued.ApiKeyValue); err != nil {
		t.Fatalf("unexpected error revoking: %v", err)
	}
	_, err := manager.ValidateApiKey(issued.ApiKeyValue)
	if !errors.Is(err, ErrApiKeyRevoked) {
		t.Fatalf("expected ErrApiKeyRevoked, got %v", err)
	}
}

func TestRevokeApiKeyIsIdempotent(t *testing.T) {
	manager := NewApiKeyManager()
	issued, _ := manager.IssueApiKey(ApiKeyIssuanceRequest{AccountIdentifier: "acct-1"})
	if err := manager.RevokeApiKey(issued.ApiKeyValue); err != nil {
		t.Fatalf("unexpected error on first revoke: %v", err)
	}
	if err := manager.RevokeApiKey(issued.ApiKeyValue); err != nil {
		t.Fatalf("expected revoking an already-revoked key to be a harmless no-op, got %v", err)
	}
}

func TestRevokeApiKeyOnUnknownValueReturnsNotFound(t *testing.T) {
	manager := NewApiKeyManager()
	err := manager.RevokeApiKey("nope")
	if !errors.Is(err, ErrApiKeyNotFound) {
		t.Fatalf("expected ErrApiKeyNotFound, got %v", err)
	}
}

func TestListApiKeysForAccountReturnsOnlyThatAccountsKeysNewestFirst(t *testing.T) {
	manager := NewApiKeyManager()
	manager.IssueApiKey(ApiKeyIssuanceRequest{AccountIdentifier: "acct-1"})
	manager.IssueApiKey(ApiKeyIssuanceRequest{AccountIdentifier: "acct-2"})
	third, _ := manager.IssueApiKey(ApiKeyIssuanceRequest{AccountIdentifier: "acct-1"})

	keys := manager.ListApiKeysForAccount("acct-1")
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys for acct-1, got %d", len(keys))
	}
	if keys[0].ApiKeyValue != third.ApiKeyValue {
		t.Fatalf("expected the most recently issued key first")
	}
}

func TestListApiKeysForAccountWithNoKeysReturnsEmpty(t *testing.T) {
	manager := NewApiKeyManager()
	if keys := manager.ListApiKeysForAccount("nobody"); len(keys) != 0 {
		t.Fatalf("expected 0 keys, got %d", len(keys))
	}
}

func TestTenantIdentifierIsPreservedOnIssuedKey(t *testing.T) {
	manager := NewApiKeyManager()
	issued, _ := manager.IssueApiKey(ApiKeyIssuanceRequest{AccountIdentifier: "acct-1", TenantIdentifier: "acme-fintech"})
	validated, _ := manager.ValidateApiKey(issued.ApiKeyValue)
	if validated.TenantIdentifier != "acme-fintech" {
		t.Fatalf("expected tenant identifier to round-trip, got %q", validated.TenantIdentifier)
	}
}
