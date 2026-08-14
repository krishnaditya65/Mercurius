package familyaccountaccess

import (
	"reflect"
	"strings"
	"testing"
)

// methodSetOf returns the exported method names on registry's type, via
// reflection — used only by TestExposedCapabilitySetIsReadOnly below.
func methodSetOf(registry *Registry) []string {
	registryType := reflect.TypeOf(registry)
	methodNames := make([]string, 0, registryType.NumMethod())
	for i := 0; i < registryType.NumMethod(); i++ {
		methodNames = append(methodNames, registryType.Method(i).Name)
	}
	return methodNames
}

func containsIgnoreCase(haystack string, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

func TestNoAccessByDefault(t *testing.T) {
	registry := NewRegistry()

	_, hasAccess := registry.CheckAccess("acct-owner", "acct-viewer")

	if hasAccess {
		t.Fatal("expected no access before any link is registered")
	}
}

func TestRegisterFamilyLinkGrantsAccessAtTheGivenLevel(t *testing.T) {
	registry := NewRegistry()

	err := registry.RegisterFamilyLink("acct-owner", "acct-viewer", PermissionViewOnly)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	link, hasAccess := registry.CheckAccess("acct-owner", "acct-viewer")
	if !hasAccess {
		t.Fatal("expected access after registering a family link")
	}
	if link.PermissionLevel != PermissionViewOnly {
		t.Fatalf("expected VIEW_ONLY, got %q", link.PermissionLevel)
	}
}

func TestRegisterFamilyLinkRequiresOwnerAccountIdentifier(t *testing.T) {
	registry := NewRegistry()

	err := registry.RegisterFamilyLink("", "acct-viewer", PermissionViewOnly)
	if err != ErrOwnerAccountIdentifierRequired {
		t.Fatalf("expected ErrOwnerAccountIdentifierRequired, got %v", err)
	}
}

func TestRegisterFamilyLinkRequiresViewerAccountIdentifier(t *testing.T) {
	registry := NewRegistry()

	err := registry.RegisterFamilyLink("acct-owner", "", PermissionViewOnly)
	if err != ErrViewerAccountIdentifierRequired {
		t.Fatalf("expected ErrViewerAccountIdentifierRequired, got %v", err)
	}
}

func TestRegisterFamilyLinkRejectsSelfLink(t *testing.T) {
	registry := NewRegistry()

	err := registry.RegisterFamilyLink("acct-same", "acct-same", PermissionViewOnly)
	if err != ErrViewerCannotBeOwner {
		t.Fatalf("expected ErrViewerCannotBeOwner, got %v", err)
	}
}

func TestRegisterFamilyLinkRejectsInvalidPermissionLevel(t *testing.T) {
	registry := NewRegistry()

	err := registry.RegisterFamilyLink("acct-owner", "acct-viewer", PermissionLevel("FULL_ADMIN"))
	if err != ErrInvalidPermissionLevel {
		t.Fatalf("expected ErrInvalidPermissionLevel, got %v", err)
	}
}

func TestRegisteringAgainOverwritesThePermissionLevel(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterFamilyLink("acct-owner", "acct-viewer", PermissionViewOnly)

	err := registry.RegisterFamilyLink("acct-owner", "acct-viewer", PermissionViewAndTrade)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	link, _ := registry.CheckAccess("acct-owner", "acct-viewer")
	if link.PermissionLevel != PermissionViewAndTrade {
		t.Fatalf("expected the second registration to overwrite the permission level, got %q", link.PermissionLevel)
	}
}

func TestRevokeFamilyLinkRemovesAccess(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterFamilyLink("acct-owner", "acct-viewer", PermissionViewOnly)

	registry.RevokeFamilyLink("acct-owner", "acct-viewer")

	_, hasAccess := registry.CheckAccess("acct-owner", "acct-viewer")
	if hasAccess {
		t.Fatal("expected access to be gone after revocation")
	}
}

func TestRevokingANonexistentLinkIsANoOp(t *testing.T) {
	registry := NewRegistry()

	registry.RevokeFamilyLink("acct-owner", "acct-viewer") // must not panic

	_, hasAccess := registry.CheckAccess("acct-owner", "acct-viewer")
	if hasAccess {
		t.Fatal("expected still no access")
	}
}

func TestLinkingOneOwnerDoesNotAffectAnother(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterFamilyLink("acct-owner-1", "acct-viewer", PermissionViewOnly)

	_, hasAccess := registry.CheckAccess("acct-owner-2", "acct-viewer")
	if hasAccess {
		t.Fatal("expected acct-owner-2 to be unaffected by acct-owner-1's link")
	}
}

func TestAuthorizeViewOnlyAccessSucceedsForAViewOnlyLink(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterFamilyLink("acct-owner", "acct-viewer", PermissionViewOnly)

	if err := registry.AuthorizeViewOnlyAccess("acct-owner", "acct-viewer"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAuthorizeViewOnlyAccessSucceedsForAViewAndTradeLinkToo(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterFamilyLink("acct-owner", "acct-viewer", PermissionViewAndTrade)

	if err := registry.AuthorizeViewOnlyAccess("acct-owner", "acct-viewer"); err != nil {
		t.Fatalf("unexpected error: %v — VIEW_AND_TRADE must still include view access", err)
	}
}

func TestAuthorizeViewOnlyAccessFailsWithNoLink(t *testing.T) {
	registry := NewRegistry()

	err := registry.AuthorizeViewOnlyAccess("acct-owner", "acct-stranger")
	if err != ErrNoLinkGrantsAccess {
		t.Fatalf("expected ErrNoLinkGrantsAccess, got %v", err)
	}
}

func TestAuthorizeViewOnlyAccessFailsAfterRevocation(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterFamilyLink("acct-owner", "acct-viewer", PermissionViewOnly)
	registry.RevokeFamilyLink("acct-owner", "acct-viewer")

	err := registry.AuthorizeViewOnlyAccess("acct-owner", "acct-viewer")
	if err != ErrNoLinkGrantsAccess {
		t.Fatalf("expected ErrNoLinkGrantsAccess after revocation, got %v", err)
	}
}

func TestLinksForOwnerReturnsEveryViewerSorted(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterFamilyLink("acct-owner", "acct-viewer-z", PermissionViewOnly)
	registry.RegisterFamilyLink("acct-owner", "acct-viewer-a", PermissionViewAndTrade)

	links := registry.LinksForOwner("acct-owner")
	if len(links) != 2 {
		t.Fatalf("expected 2 links, got %d", len(links))
	}
	if links[0].ViewerAccountIdentifier != "acct-viewer-a" || links[1].ViewerAccountIdentifier != "acct-viewer-z" {
		t.Fatalf("expected links sorted by viewer identifier, got %v", links)
	}
}

func TestLinksForOwnerWithNoLinksReturnsEmpty(t *testing.T) {
	registry := NewRegistry()

	links := registry.LinksForOwner("acct-owner")
	if len(links) != 0 {
		t.Fatalf("expected no links, got %v", links)
	}
}

// TestExposedCapabilitySetIsReadOnly is the explicit boundary-assertion
// test this feature's requirements call for: it asserts, by reflection
// over the package's exported method set, that NOTHING on *Registry
// looks like an order-submission capability. This package has no
// dependency on any order-related type, and this test makes sure that
// property can't silently regress — a future edit that added, say, a
// `SubmitOrderOnBehalfOf` method would fail this test by name-matching
// alone, forcing a deliberate, visible decision rather than an
// accidental one.
func TestExposedCapabilitySetIsReadOnly(t *testing.T) {
	disallowedMethodNameSubstrings := []string{
		"Submit", "PlaceOrder", "Trade", "Cancel", "Execute", "Buy", "Sell",
	}

	registry := NewRegistry()
	registryType := methodSetOf(registry)

	for _, methodName := range registryType {
		for _, disallowed := range disallowedMethodNameSubstrings {
			if containsIgnoreCase(methodName, disallowed) {
				t.Fatalf("found method %q on Registry, which looks like an order-submission capability — this package must expose read-only access only", methodName)
			}
		}
	}
}
