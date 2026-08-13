// Package tenantconfig is FEATURES.md §18's "White-label / Broker-as-a-
// Service offering for fintechs" — the real platform PRIMITIVE such an
// offering needs: multi-tenancy in the gateway layer. Each named tenant
// gets its own branding metadata, its own rate-limit tier configuration
// (see internal/ratelimiter), and its own API-key namespace (enforced by
// internal/apikeymanager's IssuedApiKey.TenantIdentifier field plus this
// package's lookup) — real isolation at the layer api-gateway controls.
//
// TODO(real build): this is the PLATFORM PRIMITIVE a white-label offering
// needs, not a complete commercial BaaS product. A real white-label
// offering additionally needs separate legal/compliance entities per
// tenant (each fintech partner is its own regulated broker-dealer or
// operates under this platform's license via a real legal agreement),
// tenant-scoped data residency/segregation guarantees, and per-tenant
// billing — none of which exist in this repository and all of which are
// out of scope for a gateway-layer config package.
package tenantconfig

import (
	"errors"
	"sync"

	"mercurius/apiGateway/internal/ratelimiter"
)

// ErrTenantNotFound is returned when a lookup key does not correspond to
// any registered tenant.
var ErrTenantNotFound = errors.New("tenantconfig: tenant not found")

// ErrTenantAlreadyExists is returned by RegisterTenant when
// tenantIdentifier is already registered.
var ErrTenantAlreadyExists = errors.New("tenantconfig: tenant already exists")

// ErrTenantIdentifierRequired is returned when registering a tenant with
// no identifier.
var ErrTenantIdentifierRequired = errors.New("tenantconfig: tenantIdentifier is required")

// BrandingMetadata is the white-label surface a tenant can customize —
// deliberately minimal (this is a primitive, not a full theming engine).
type BrandingMetadata struct {
	DisplayName     string `json:"displayName"`
	LogoUrl         string `json:"logoUrl,omitempty"`
	PrimaryColorHex string `json:"primaryColorHex,omitempty"`
}

// Tenant is one white-label partner's full configuration.
type Tenant struct {
	TenantIdentifier string                                              `json:"tenantIdentifier"`
	Branding         BrandingMetadata                                    `json:"branding"`
	RateLimitTiers   map[ratelimiter.RateLimitTier]ratelimiter.TierLimit `json:"rateLimitTiers"`
}

// TenantRegistry holds every registered tenant. Safe for concurrent use.
type TenantRegistry struct {
	mutexGuardingTenants sync.Mutex
	tenantsByIdentifier  map[string]*Tenant
}

func NewTenantRegistry() *TenantRegistry {
	return &TenantRegistry{tenantsByIdentifier: make(map[string]*Tenant)}
}

// RegisterTenant adds a brand-new tenant. If rateLimitTiers is nil, the
// tenant inherits ratelimiter.DefaultTierLimits — real independent
// tiering per tenant is opt-in, not mandatory, so a tenant that doesn't
// need custom quotas doesn't have to specify anything.
func (registry *TenantRegistry) RegisterTenant(tenantIdentifier string, branding BrandingMetadata, rateLimitTiers map[ratelimiter.RateLimitTier]ratelimiter.TierLimit) (Tenant, error) {
	if tenantIdentifier == "" {
		return Tenant{}, ErrTenantIdentifierRequired
	}

	registry.mutexGuardingTenants.Lock()
	defer registry.mutexGuardingTenants.Unlock()

	if _, exists := registry.tenantsByIdentifier[tenantIdentifier]; exists {
		return Tenant{}, ErrTenantAlreadyExists
	}

	if rateLimitTiers == nil {
		rateLimitTiers = ratelimiter.DefaultTierLimits
	}

	tenant := Tenant{
		TenantIdentifier: tenantIdentifier,
		Branding:         branding,
		RateLimitTiers:   rateLimitTiers,
	}
	registry.tenantsByIdentifier[tenantIdentifier] = &tenant
	return tenant, nil
}

// GetTenant returns the registered tenant for tenantIdentifier.
func (registry *TenantRegistry) GetTenant(tenantIdentifier string) (Tenant, error) {
	registry.mutexGuardingTenants.Lock()
	defer registry.mutexGuardingTenants.Unlock()

	tenant, exists := registry.tenantsByIdentifier[tenantIdentifier]
	if !exists {
		return Tenant{}, ErrTenantNotFound
	}
	return *tenant, nil
}

// ListTenants returns every registered tenant. Order is not guaranteed.
func (registry *TenantRegistry) ListTenants() []Tenant {
	registry.mutexGuardingTenants.Lock()
	defer registry.mutexGuardingTenants.Unlock()

	tenants := make([]Tenant, 0, len(registry.tenantsByIdentifier))
	for _, tenant := range registry.tenantsByIdentifier {
		tenants = append(tenants, *tenant)
	}
	return tenants
}

// RateLimiterForTenant returns a fresh, independent
// ratelimiter.TokenBucketRateLimiter configured with tenantIdentifier's
// own tier limits — this is the actual isolation mechanism: each
// tenant's API-key traffic is metered against its OWN limiter instance,
// so one tenant's institutional-tier partner blasting traffic can never
// exhaust another tenant's retail-tier quota bucket.
func (registry *TenantRegistry) RateLimiterForTenant(tenantIdentifier string) (*ratelimiter.TokenBucketRateLimiter, error) {
	tenant, err := registry.GetTenant(tenantIdentifier)
	if err != nil {
		return nil, err
	}
	return ratelimiter.NewTokenBucketRateLimiter(tenant.RateLimitTiers), nil
}
