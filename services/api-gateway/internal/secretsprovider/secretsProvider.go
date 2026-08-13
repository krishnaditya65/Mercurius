// Package secretsprovider is FEATURES.md §13's "Secrets management,
// least-privilege IAM per service" — the secrets-management half of that
// item. It defines a real, minimal interface any secrets backend can
// satisfy, plus a real, working reference implementation backed by
// process environment variables.
//
// TODO(real build): environment variables are NOT a real secrets
// manager — they have no audit log, no rotation, no encryption-at-rest
// guarantee beyond whatever the host OS/process supervisor provides, and
// they leak into crash dumps/process listings more readily than a
// dedicated store. A real build swaps EnvironmentVariableSecretsProvider
// for an implementation backed by HashiCorp Vault, AWS Secrets Manager,
// GCP Secret Manager, or equivalent — the SecretsProvider interface
// below is deliberately narrow (one method) so that swap requires no
// caller-side changes, only a `NewXxxSecretsProvider` constructor change
// at each service's composition root (main.go).
//
// The "least-privilege IAM per service" half of this FEATURES.md item is
// NOT code — it's a documented access matrix, since there is no real
// cloud IAM system in this environment to enforce against. See
// services/api-gateway/config/secretsAccessMatrix.yaml, which is a real,
// concrete config file (not fabricated code pretending to enforce
// something that doesn't exist here).
package secretsprovider

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrSecretNotFound is returned when the requested key has no value in
// the backing store. Callers should treat this identically regardless of
// which SecretsProvider implementation raised it.
var ErrSecretNotFound = errors.New("secretsprovider: secret not found")

// ErrEmptySecretKey is returned when GetSecret is called with an empty
// key — never a valid lookup, and worth distinguishing from a genuine
// not-found so a caller doesn't mistake a programming bug for a missing
// ops-provisioned secret.
var ErrEmptySecretKey = errors.New("secretsprovider: secret key must not be empty")

// SecretsProvider is the one method every secrets backend must support.
// Deliberately narrow: no list, no write, no delete — a running service
// only ever needs to READ secrets it was provisioned with; provisioning
// and rotation are operational concerns handled outside the application
// process (by whatever real secrets manager sits behind this
// interface).
type SecretsProvider interface {
	// GetSecret returns the current value for key, or ErrSecretNotFound
	// if no value is provisioned for it (or ErrEmptySecretKey if key is
	// empty).
	GetSecret(key string) (string, error)
}

// EnvironmentVariableSecretsProvider is the reference SecretsProvider
// implementation: it reads secrets from process environment variables,
// each prefixed with envVarPrefix (so this provider can't accidentally
// leak an unrelated environment variable — e.g. PATH — as though it were
// a provisioned secret).
//
// This IS a real, working implementation — every method fully functions
// against a real running process's environment — but it is explicitly
// NOT a production-grade secrets manager; see the package doc comment
// above.
type EnvironmentVariableSecretsProvider struct {
	envVarPrefix string
}

// NewEnvironmentVariableSecretsProvider returns a provider that looks up
// key "foo" as the environment variable "<envVarPrefix>FOO" (key is
// upper-cased and prefixed). An empty envVarPrefix is allowed — it just
// means keys map directly to their upper-cased environment variable
// name.
func NewEnvironmentVariableSecretsProvider(envVarPrefix string) *EnvironmentVariableSecretsProvider {
	return &EnvironmentVariableSecretsProvider{envVarPrefix: envVarPrefix}
}

func (provider *EnvironmentVariableSecretsProvider) environmentVariableNameForKey(key string) string {
	return provider.envVarPrefix + strings.ToUpper(key)
}

// GetSecret implements SecretsProvider.
func (provider *EnvironmentVariableSecretsProvider) GetSecret(key string) (string, error) {
	if key == "" {
		return "", ErrEmptySecretKey
	}

	envVarName := provider.environmentVariableNameForKey(key)
	value, isSet := os.LookupEnv(envVarName)
	if !isSet || value == "" {
		return "", fmt.Errorf("%w: %s (looked up environment variable %s)", ErrSecretNotFound, key, envVarName)
	}
	return value, nil
}

// StaticInMemorySecretsProvider is a second reference implementation
// useful for tests and local development — it holds a fixed map handed
// to it at construction time instead of reading the real process
// environment. Real callers (main.go composition roots) should prefer
// EnvironmentVariableSecretsProvider; this exists so packages that
// depend on SecretsProvider can be unit-tested without mutating process
// environment variables (which are global, shared, mutable state and a
// bad fit for parallel tests).
type StaticInMemorySecretsProvider struct {
	secretsByKey map[string]string
}

// NewStaticInMemorySecretsProvider returns a provider seeded with
// secretsByKey. The map is copied, so later mutation of the caller's map
// does not affect the provider.
func NewStaticInMemorySecretsProvider(secretsByKey map[string]string) *StaticInMemorySecretsProvider {
	copyOfSecrets := make(map[string]string, len(secretsByKey))
	for key, value := range secretsByKey {
		copyOfSecrets[key] = value
	}
	return &StaticInMemorySecretsProvider{secretsByKey: copyOfSecrets}
}

// GetSecret implements SecretsProvider.
func (provider *StaticInMemorySecretsProvider) GetSecret(key string) (string, error) {
	if key == "" {
		return "", ErrEmptySecretKey
	}
	value, exists := provider.secretsByKey[key]
	if !exists || value == "" {
		return "", fmt.Errorf("%w: %s", ErrSecretNotFound, key)
	}
	return value, nil
}
