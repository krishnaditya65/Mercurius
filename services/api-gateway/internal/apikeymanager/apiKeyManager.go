// Package apikeymanager is FEATURES.md §18's "Public developer API with
// sandbox, API-key management, tiered rate limits" — real developer
// API-key issuance and validation. Every issued key carries a rate-limit
// tier (see internal/ratelimiter) and a sandbox flag; api-gateway's HTTP
// layer (cmd/server/main.go) is what actually enforces rate limiting and
// sandbox routing per request using the tier/flag this package returns.
package apikeymanager

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"mercurius/apiGateway/internal/ratelimiter"
)

// ErrApiKeyNotFound is returned when a lookup key does not correspond to
// any issued API key.
var ErrApiKeyNotFound = errors.New("apikeymanager: api key not found")

// ErrApiKeyRevoked is returned when a lookup succeeds but the key has
// been revoked — distinct from not-found so callers/logs can tell "never
// existed" apart from "existed, then was pulled".
var ErrApiKeyRevoked = errors.New("apikeymanager: api key has been revoked")

// ErrAccountIdentifierRequired is returned when issuing a key without an
// owning account identifier.
var ErrAccountIdentifierRequired = errors.New("apikeymanager: accountIdentifier is required")

// IssuedApiKey is everything api-gateway needs to know about one
// developer API key.
type IssuedApiKey struct {
	ApiKeyValue       string                    `json:"apiKeyValue"`
	AccountIdentifier string                    `json:"accountIdentifier"`
	TenantIdentifier  string                    `json:"tenantIdentifier,omitempty"`
	RateLimitTier     ratelimiter.RateLimitTier `json:"rateLimitTier"`
	IsSandboxKey      bool                      `json:"isSandboxKey"`
	IssuedAtTime      time.Time                 `json:"issuedAtTime"`
	IsRevoked         bool                      `json:"isRevoked"`
	RevokedAtTime     *time.Time                `json:"revokedAtTime,omitempty"`
}

// ApiKeyIssuanceRequest is the input to IssueApiKey.
type ApiKeyIssuanceRequest struct {
	AccountIdentifier string
	TenantIdentifier  string
	RateLimitTier     ratelimiter.RateLimitTier
	IsSandboxKey      bool
}

// ApiKeyManager issues and validates developer API keys. Safe for
// concurrent use.
//
// TODO(real build): in-memory only, same as every other skeleton store
// in this repo — a real build persists issued keys durably (they're
// bearer credentials; losing the ability to revoke one on restart is a
// real security gap) and stores only a hash of the key value, never the
// raw value, mirroring how services/auth should handle passwords.
type ApiKeyManager struct {
	mutexGuardingKeys sync.Mutex
	keysByValue       map[string]*IssuedApiKey
	randomKeyBytes    func(numBytes int) ([]byte, error)
	nowFunc           func() time.Time
}

func NewApiKeyManager() *ApiKeyManager {
	return &ApiKeyManager{
		keysByValue: make(map[string]*IssuedApiKey),
		randomKeyBytes: func(numBytes int) ([]byte, error) {
			buf := make([]byte, numBytes)
			_, err := rand.Read(buf)
			return buf, err
		},
		nowFunc: time.Now,
	}
}

// IssueApiKey generates and stores a brand-new API key for request.
func (manager *ApiKeyManager) IssueApiKey(request ApiKeyIssuanceRequest) (IssuedApiKey, error) {
	if request.AccountIdentifier == "" {
		return IssuedApiKey{}, ErrAccountIdentifierRequired
	}

	randomBytes, randomErr := manager.randomKeyBytes(24)
	if randomErr != nil {
		return IssuedApiKey{}, fmt.Errorf("apikeymanager: failed generating random key material: %w", randomErr)
	}

	keyPrefix := "mk_live_"
	if request.IsSandboxKey {
		keyPrefix = "mk_sandbox_"
	}
	apiKeyValue := keyPrefix + hex.EncodeToString(randomBytes)

	tier := request.RateLimitTier
	if tier == "" {
		tier = ratelimiter.RateLimitTierRetail
	}
	if request.IsSandboxKey {
		tier = ratelimiter.RateLimitTierSandbox
	}

	issued := IssuedApiKey{
		ApiKeyValue:       apiKeyValue,
		AccountIdentifier: request.AccountIdentifier,
		TenantIdentifier:  request.TenantIdentifier,
		RateLimitTier:     tier,
		IsSandboxKey:      request.IsSandboxKey,
		IssuedAtTime:      manager.nowFunc(),
	}

	manager.mutexGuardingKeys.Lock()
	defer manager.mutexGuardingKeys.Unlock()
	stored := issued
	manager.keysByValue[apiKeyValue] = &stored
	return issued, nil
}

// ValidateApiKey looks up apiKeyValue and returns its current record if
// it exists and has not been revoked.
func (manager *ApiKeyManager) ValidateApiKey(apiKeyValue string) (IssuedApiKey, error) {
	manager.mutexGuardingKeys.Lock()
	defer manager.mutexGuardingKeys.Unlock()

	stored, exists := manager.keysByValue[apiKeyValue]
	if !exists {
		return IssuedApiKey{}, ErrApiKeyNotFound
	}
	if stored.IsRevoked {
		return *stored, ErrApiKeyRevoked
	}
	return *stored, nil
}

// RevokeApiKey marks apiKeyValue as revoked. Idempotent: revoking an
// already-revoked key is a harmless no-op that still returns nil.
func (manager *ApiKeyManager) RevokeApiKey(apiKeyValue string) error {
	manager.mutexGuardingKeys.Lock()
	defer manager.mutexGuardingKeys.Unlock()

	stored, exists := manager.keysByValue[apiKeyValue]
	if !exists {
		return ErrApiKeyNotFound
	}
	if stored.IsRevoked {
		return nil
	}
	revokedAt := manager.nowFunc()
	stored.IsRevoked = true
	stored.RevokedAtTime = &revokedAt
	return nil
}

// ListApiKeysForAccount returns every key (revoked or not) issued to
// accountIdentifier, newest first.
func (manager *ApiKeyManager) ListApiKeysForAccount(accountIdentifier string) []IssuedApiKey {
	manager.mutexGuardingKeys.Lock()
	defer manager.mutexGuardingKeys.Unlock()

	var matches []IssuedApiKey
	for _, key := range manager.keysByValue {
		if key.AccountIdentifier == accountIdentifier {
			matches = append(matches, *key)
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].IssuedAtTime.After(matches[j].IssuedAtTime)
	})
	return matches
}
