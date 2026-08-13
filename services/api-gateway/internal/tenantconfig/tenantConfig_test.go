package tenantconfig

import (
	"errors"
	"testing"

	"mercurius/apiGateway/internal/ratelimiter"
)

func TestRegisterTenantSucceeds(t *testing.T) {
	registry := NewTenantRegistry()
	tenant, err := registry.RegisterTenant("acme-fintech", BrandingMetadata{DisplayName: "Acme"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tenant.TenantIdentifier != "acme-fintech" {
		t.Fatalf("expected acme-fintech, got %s", tenant.TenantIdentifier)
	}
}

func TestRegisterTenantRequiresIdentifier(t *testing.T) {
	registry := NewTenantRegistry()
	_, err := registry.RegisterTenant("", BrandingMetadata{}, nil)
	if !errors.Is(err, ErrTenantIdentifierRequired) {
		t.Fatalf("expected ErrTenantIdentifierRequired, got %v", err)
	}
}

func TestRegisterTenantRejectsDuplicate(t *testing.T) {
	registry := NewTenantRegistry()
	registry.RegisterTenant("acme-fintech", BrandingMetadata{}, nil)
	_, err := registry.RegisterTenant("acme-fintech", BrandingMetadata{}, nil)
	if !errors.Is(err, ErrTenantAlreadyExists) {
		t.Fatalf("expected ErrTenantAlreadyExists, got %v", err)
	}
}

func TestRegisterTenantWithNilTiersInheritsDefaults(t *testing.T) {
	registry := NewTenantRegistry()
	tenant, _ := registry.RegisterTenant("acme-fintech", BrandingMetadata{}, nil)
	if tenant.RateLimitTiers[ratelimiter.RateLimitTierRetail].RequestsPerSecond != ratelimiter.DefaultTierLimits[ratelimiter.RateLimitTierRetail].RequestsPerSecond {
		t.Fatalf("expected default tier limits to be inherited")
	}
}

func TestRegisterTenantWithCustomTiersOverridesDefaults(t *testing.T) {
	registry := NewTenantRegistry()
	customTiers := map[ratelimiter.RateLimitTier]ratelimiter.TierLimit{
		ratelimiter.RateLimitTierRetail: {RequestsPerSecond: 1, BurstCapacity: 1},
	}
	tenant, _ := registry.RegisterTenant("tight-tenant", BrandingMetadata{}, customTiers)
	if tenant.RateLimitTiers[ratelimiter.RateLimitTierRetail].RequestsPerSecond != 1 {
		t.Fatalf("expected custom tier override of 1 req/s, got %v", tenant.RateLimitTiers[ratelimiter.RateLimitTierRetail].RequestsPerSecond)
	}
}

func TestGetTenantReturnsNotFoundForUnknownTenant(t *testing.T) {
	registry := NewTenantRegistry()
	_, err := registry.GetTenant("nope")
	if !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestListTenantsReturnsEveryRegisteredTenant(t *testing.T) {
	registry := NewTenantRegistry()
	registry.RegisterTenant("tenant-a", BrandingMetadata{}, nil)
	registry.RegisterTenant("tenant-b", BrandingMetadata{}, nil)

	tenants := registry.ListTenants()
	if len(tenants) != 2 {
		t.Fatalf("expected 2 tenants, got %d", len(tenants))
	}
}

func TestListTenantsOnEmptyRegistryReturnsEmpty(t *testing.T) {
	registry := NewTenantRegistry()
	if tenants := registry.ListTenants(); len(tenants) != 0 {
		t.Fatalf("expected 0 tenants, got %d", len(tenants))
	}
}

func TestRateLimiterForTenantReturnsIndependentLimiterInstances(t *testing.T) {
	registry := NewTenantRegistry()
	tightTiers := map[ratelimiter.RateLimitTier]ratelimiter.TierLimit{
		ratelimiter.RateLimitTierRetail: {RequestsPerSecond: 1000, BurstCapacity: 1},
	}
	registry.RegisterTenant("tenant-a", BrandingMetadata{}, tightTiers)
	registry.RegisterTenant("tenant-b", BrandingMetadata{}, tightTiers)

	limiterA, err := registry.RateLimiterForTenant("tenant-a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	limiterB, err := registry.RateLimiterForTenant("tenant-b")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Exhaust tenant-a's limiter for a given key; tenant-b's limiter for
	// the SAME key must be completely unaffected — proves isolation.
	limiterA.AllowRequest("same-key", ratelimiter.RateLimitTierRetail)
	if limiterA.AllowRequest("same-key", ratelimiter.RateLimitTierRetail) {
		t.Fatalf("expected tenant-a's burst capacity of 1 to be exhausted")
	}
	if !limiterB.AllowRequest("same-key", ratelimiter.RateLimitTierRetail) {
		t.Fatalf("expected tenant-b's independent limiter to still allow the request")
	}
}

func TestRateLimiterForUnknownTenantReturnsNotFound(t *testing.T) {
	registry := NewTenantRegistry()
	_, err := registry.RateLimiterForTenant("nope")
	if !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestBrandingMetadataRoundTrips(t *testing.T) {
	registry := NewTenantRegistry()
	branding := BrandingMetadata{DisplayName: "Acme", LogoUrl: "https://acme.example/logo.png", PrimaryColorHex: "#112233"}
	registry.RegisterTenant("acme-fintech", branding, nil)

	tenant, _ := registry.GetTenant("acme-fintech")
	if tenant.Branding != branding {
		t.Fatalf("expected branding to round-trip exactly, got %+v", tenant.Branding)
	}
}
