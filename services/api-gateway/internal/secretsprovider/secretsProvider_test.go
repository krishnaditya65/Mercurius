package secretsprovider

import (
	"errors"
	"os"
	"testing"
)

func TestEnvironmentVariableSecretsProviderReturnsAProvisionedSecret(t *testing.T) {
	os.Setenv("MERCURIUS_TEST_DATABASE_PASSWORD", "hunter2")
	defer os.Unsetenv("MERCURIUS_TEST_DATABASE_PASSWORD")

	provider := NewEnvironmentVariableSecretsProvider("MERCURIUS_TEST_")
	value, err := provider.GetSecret("database_password")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if value != "hunter2" {
		t.Fatalf("expected %q, got %q", "hunter2", value)
	}
}

func TestEnvironmentVariableSecretsProviderIsCaseInsensitiveOnKey(t *testing.T) {
	os.Setenv("MERCURIUS_TEST_API_TOKEN", "abc123")
	defer os.Unsetenv("MERCURIUS_TEST_API_TOKEN")

	provider := NewEnvironmentVariableSecretsProvider("MERCURIUS_TEST_")
	value, err := provider.GetSecret("API_TOKEN")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if value != "abc123" {
		t.Fatalf("expected %q, got %q", "abc123", value)
	}
}

func TestEnvironmentVariableSecretsProviderReturnsErrSecretNotFoundWhenUnset(t *testing.T) {
	provider := NewEnvironmentVariableSecretsProvider("MERCURIUS_TEST_DEFINITELY_UNSET_")
	_, err := provider.GetSecret("nope")
	if !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("expected ErrSecretNotFound, got %v", err)
	}
}

func TestEnvironmentVariableSecretsProviderTreatsEmptyStringAsNotFound(t *testing.T) {
	os.Setenv("MERCURIUS_TEST_EMPTY_SECRET", "")
	defer os.Unsetenv("MERCURIUS_TEST_EMPTY_SECRET")

	provider := NewEnvironmentVariableSecretsProvider("MERCURIUS_TEST_")
	_, err := provider.GetSecret("empty_secret")
	if !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("expected ErrSecretNotFound for an empty-string env var, got %v", err)
	}
}

func TestEnvironmentVariableSecretsProviderRejectsEmptyKey(t *testing.T) {
	provider := NewEnvironmentVariableSecretsProvider("MERCURIUS_TEST_")
	_, err := provider.GetSecret("")
	if !errors.Is(err, ErrEmptySecretKey) {
		t.Fatalf("expected ErrEmptySecretKey, got %v", err)
	}
}

func TestEnvironmentVariableSecretsProviderWithEmptyPrefixMapsDirectly(t *testing.T) {
	os.Setenv("DIRECTKEY", "direct-value")
	defer os.Unsetenv("DIRECTKEY")

	provider := NewEnvironmentVariableSecretsProvider("")
	value, err := provider.GetSecret("directKey")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if value != "direct-value" {
		t.Fatalf("expected %q, got %q", "direct-value", value)
	}
}

func TestStaticInMemorySecretsProviderReturnsSeededSecret(t *testing.T) {
	provider := NewStaticInMemorySecretsProvider(map[string]string{"apiKey": "seeded-value"})
	value, err := provider.GetSecret("apiKey")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if value != "seeded-value" {
		t.Fatalf("expected %q, got %q", "seeded-value", value)
	}
}

func TestStaticInMemorySecretsProviderReturnsErrSecretNotFoundForMissingKey(t *testing.T) {
	provider := NewStaticInMemorySecretsProvider(map[string]string{"apiKey": "value"})
	_, err := provider.GetSecret("otherKey")
	if !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("expected ErrSecretNotFound, got %v", err)
	}
}

func TestStaticInMemorySecretsProviderRejectsEmptyKey(t *testing.T) {
	provider := NewStaticInMemorySecretsProvider(map[string]string{"apiKey": "value"})
	_, err := provider.GetSecret("")
	if !errors.Is(err, ErrEmptySecretKey) {
		t.Fatalf("expected ErrEmptySecretKey, got %v", err)
	}
}

func TestStaticInMemorySecretsProviderCopiesInputMapAtConstruction(t *testing.T) {
	seed := map[string]string{"apiKey": "original"}
	provider := NewStaticInMemorySecretsProvider(seed)
	seed["apiKey"] = "mutated-after-construction"

	value, err := provider.GetSecret("apiKey")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if value != "original" {
		t.Fatalf("expected provider to be insulated from later mutation of the input map, got %q", value)
	}
}

func TestBothImplementationsSatisfySecretsProviderInterface(t *testing.T) {
	var _ SecretsProvider = (*EnvironmentVariableSecretsProvider)(nil)
	var _ SecretsProvider = (*StaticInMemorySecretsProvider)(nil)
}
