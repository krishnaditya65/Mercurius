// Package familyaccountaccess implements FEATURES.md §21's "Family/
// joint account views with granular permissions (view-only access for a
// spouse/dependent, not full trading rights)". This is a real
// account-linking registry (link "viewer" account B to "owner" account A
// with a specific permission level) plus a real read-only aggregation
// path a linked viewer can use to see the owner's holdings/positions —
// pulled from oms-gateway's real `GET /positions` endpoint via
// internal/omsgatewayclient, never mocked/stubbed.
//
// Permission levels: PermissionViewOnly and PermissionViewAndTrade are
// both modeled (a real link records which one it grants), but per this
// package's explicit scope, only PermissionViewOnly is actually
// meaningfully ENFORCED here — this package exposes NO order-submission
// capability at all (backoffice doesn't submit orders to begin with), so
// there is no code path a PermissionViewAndTrade link could exercise
// beyond what PermissionViewOnly already exercises. See the package-
// level TODO and this file's own tests (particularly
// TestExposedCapabilitySetIsReadOnly) for why that boundary is real, not
// just documented.
//
// TODO(real build): in-memory only (every family link is lost on
// restart); no auth on RegisterFamilyLink itself — anyone who can reach
// this HTTP endpoint can link any two accounts (the same admin/RBAC gap
// accountcontrol's freeze endpoints already document); no identity
// verification that the linked "viewer" account is actually who they
// claim to be in relation to the owner (a real build would want some
// proof of the family/dependent relationship, not just an unverified
// self-report); PermissionViewAndTrade is modeled as a value this
// package can RECORD but grants no additional capability through this
// package today — a real build implementing actual trade-on-behalf-of
// would need a much more careful design (limits, revocability, its own
// audit trail) than flipping this enum value.
package familyaccountaccess

import (
	"errors"
	"sort"
	"sync"
)

// PermissionLevel enumerates the granted access level of one family
// link. A closed set of string constants, like accountcontrol's/
// audittrail's own enums elsewhere in this repo.
type PermissionLevel string

const (
	// PermissionViewOnly grants read-only visibility into the owner
	// account's holdings/positions — nothing else. This is the ONLY
	// permission level this package's aggregation endpoint actually
	// checks/enforces.
	PermissionViewOnly PermissionLevel = "VIEW_ONLY"

	// PermissionViewAndTrade is modeled (recorded, returned in queries)
	// but grants NO additional capability through this package — there
	// is no order-submission code path here at all. See the package doc.
	PermissionViewAndTrade PermissionLevel = "VIEW_AND_TRADE"
)

var (
	// ErrOwnerAccountIdentifierRequired is returned when a caller
	// supplies an empty owner accountIdentifier.
	ErrOwnerAccountIdentifierRequired = errors.New("ownerAccountIdentifier is required")

	// ErrViewerAccountIdentifierRequired is returned when a caller
	// supplies an empty viewer accountIdentifier.
	ErrViewerAccountIdentifierRequired = errors.New("viewerAccountIdentifier is required")

	// ErrViewerCannotBeOwner is returned when a caller tries to link an
	// account as its own family viewer — never a meaningful link.
	ErrViewerCannotBeOwner = errors.New("an account cannot be linked as its own family viewer")

	// ErrInvalidPermissionLevel is returned for anything other than
	// PermissionViewOnly/PermissionViewAndTrade.
	ErrInvalidPermissionLevel = errors.New("permissionLevel must be VIEW_ONLY or VIEW_AND_TRADE")

	// ErrNoLinkGrantsAccess is returned when the caller-claimed viewer
	// has no family link to the owner at all.
	ErrNoLinkGrantsAccess = errors.New("no family link grants this viewer access to this owner account")
)

// FamilyLink is one real link: viewerAccountIdentifier has
// permissionLevel access to ownerAccountIdentifier's account.
type FamilyLink struct {
	OwnerAccountIdentifier  string          `json:"ownerAccountIdentifier"`
	ViewerAccountIdentifier string          `json:"viewerAccountIdentifier"`
	PermissionLevel         PermissionLevel `json:"permissionLevel"`
}

// Registry is the mutex-guarded, in-memory home for every family link.
// Keyed by (ownerAccountIdentifier, viewerAccountIdentifier) — one
// viewer can be linked to many owners, and one owner can have many
// viewers, but a given (owner, viewer) pair has exactly one permission
// level at a time (re-registering overwrites it, exactly like
// strategyfollowing.MarkStrategyVerified overwriting metadata).
type Registry struct {
	mutexGuardingState sync.Mutex

	linksByOwnerThenViewer map[string]map[string]FamilyLink
}

// NewRegistry builds an empty Registry — no links exist until
// RegisterFamilyLink is called.
func NewRegistry() *Registry {
	return &Registry{
		linksByOwnerThenViewer: make(map[string]map[string]FamilyLink),
	}
}

// RegisterFamilyLink creates (or updates the permission level of) a link
// granting viewerAccountIdentifier permissionLevel access to
// ownerAccountIdentifier's account.
func (registry *Registry) RegisterFamilyLink(ownerAccountIdentifier string, viewerAccountIdentifier string, permissionLevel PermissionLevel) error {
	if ownerAccountIdentifier == "" {
		return ErrOwnerAccountIdentifierRequired
	}
	if viewerAccountIdentifier == "" {
		return ErrViewerAccountIdentifierRequired
	}
	if ownerAccountIdentifier == viewerAccountIdentifier {
		return ErrViewerCannotBeOwner
	}
	if permissionLevel != PermissionViewOnly && permissionLevel != PermissionViewAndTrade {
		return ErrInvalidPermissionLevel
	}

	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	if registry.linksByOwnerThenViewer[ownerAccountIdentifier] == nil {
		registry.linksByOwnerThenViewer[ownerAccountIdentifier] = make(map[string]FamilyLink)
	}
	registry.linksByOwnerThenViewer[ownerAccountIdentifier][viewerAccountIdentifier] = FamilyLink{
		OwnerAccountIdentifier:  ownerAccountIdentifier,
		ViewerAccountIdentifier: viewerAccountIdentifier,
		PermissionLevel:         permissionLevel,
	}
	return nil
}

// RevokeFamilyLink removes any link from viewerAccountIdentifier to
// ownerAccountIdentifier. Idempotent: revoking a link that doesn't exist
// is a no-op, not an error — mirrors strategyfollowing.Unfollow's
// idempotency convention.
func (registry *Registry) RevokeFamilyLink(ownerAccountIdentifier string, viewerAccountIdentifier string) {
	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	if viewersForOwner, exists := registry.linksByOwnerThenViewer[ownerAccountIdentifier]; exists {
		delete(viewersForOwner, viewerAccountIdentifier)
	}
}

// CheckAccess reports whether viewerAccountIdentifier currently has ANY
// family link to ownerAccountIdentifier, and if so, at what permission
// level.
func (registry *Registry) CheckAccess(ownerAccountIdentifier string, viewerAccountIdentifier string) (link FamilyLink, hasAccess bool) {
	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	viewersForOwner, ownerExists := registry.linksByOwnerThenViewer[ownerAccountIdentifier]
	if !ownerExists {
		return FamilyLink{}, false
	}
	link, hasAccess = viewersForOwner[viewerAccountIdentifier]
	return link, hasAccess
}

// AuthorizeViewOnlyAccess is the one real enforcement point this package
// exposes: it returns ErrNoLinkGrantsAccess unless viewerAccountIdentifier
// has SOME family link to ownerAccountIdentifier (VIEW_ONLY or
// VIEW_AND_TRADE both satisfy it — either level includes at least
// view access). Callers building a read-only aggregation endpoint (see
// cmd/server/main.go's family-account-access handler) call this before
// ever reaching out to oms-gateway for the owner's real positions data.
func (registry *Registry) AuthorizeViewOnlyAccess(ownerAccountIdentifier string, viewerAccountIdentifier string) error {
	link, hasAccess := registry.CheckAccess(ownerAccountIdentifier, viewerAccountIdentifier)
	if !hasAccess {
		return ErrNoLinkGrantsAccess
	}
	// Every currently-modeled PermissionLevel includes at least view
	// access — VIEW_ONLY explicitly, VIEW_AND_TRADE implicitly, since
	// "can trade" was always meant to be a superset of "can view", not a
	// replacement for it. If a future PermissionLevel were added that did
	// NOT include view access, it would need to be excluded here
	// explicitly; there is deliberately no such value today.
	_ = link
	return nil
}

// LinksForOwner returns every viewer currently linked to
// ownerAccountIdentifier, sorted by viewerAccountIdentifier for a
// deterministic response.
func (registry *Registry) LinksForOwner(ownerAccountIdentifier string) []FamilyLink {
	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	viewersForOwner := registry.linksByOwnerThenViewer[ownerAccountIdentifier]
	links := make([]FamilyLink, 0, len(viewersForOwner))
	for _, link := range viewersForOwner {
		links = append(links, link)
	}
	sort.Slice(links, func(i, j int) bool {
		return links[i].ViewerAccountIdentifier < links[j].ViewerAccountIdentifier
	})
	return links
}
