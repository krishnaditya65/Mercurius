package webhookdelivery

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func startAuditTrailStub(t *testing.T, entriesProvider func() []auditTrailEntryWire) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(entriesProvider())
	}))
}

func TestPollOnceDeliversOnlyNewEntriesSinceLastPoll(t *testing.T) {
	receiver := newRealReceiver(0)
	defer receiver.close()

	entries := []auditTrailEntryWire{
		{EventType: "ORDER_SUBMITTED", ClientAccountIdentifier: "acct-1"},
	}
	auditServer := startAuditTrailStub(t, func() []auditTrailEntryWire { return entries })
	defer auditServer.Close()

	manager := NewWebhookDeliveryManager(DefaultRetryPolicy)
	manager.RegisterSubscription("acct-1", EventTypeAny, receiver.server.URL)
	source := NewAuditTrailEventSource(auditServer.URL, manager)

	if delivered := source.PollOnce(); delivered != 1 {
		t.Fatalf("expected 1 new entry delivered on first poll, got %d", delivered)
	}
	if delivered := source.PollOnce(); delivered != 0 {
		t.Fatalf("expected 0 new entries on second poll (nothing new appended), got %d", delivered)
	}

	entries = append(entries, auditTrailEntryWire{EventType: "ORDER_FILLED", ClientAccountIdentifier: "acct-1"})
	if delivered := source.PollOnce(); delivered != 1 {
		t.Fatalf("expected exactly the 1 newly appended entry delivered, got %d", delivered)
	}

	if receiver.eventCount() != 2 {
		t.Fatalf("expected the real receiver to have received 2 events total, got %d", receiver.eventCount())
	}
}

func TestPollOnceIgnoresIrrelevantEventTypes(t *testing.T) {
	receiver := newRealReceiver(0)
	defer receiver.close()

	auditServer := startAuditTrailStub(t, func() []auditTrailEntryWire {
		return []auditTrailEntryWire{
			{EventType: "MARKET_SESSION_OPENED"}, // not order-lifecycle, no account
			{EventType: "ORDER_SUBMITTED", ClientAccountIdentifier: "acct-1"},
		}
	})
	defer auditServer.Close()

	manager := NewWebhookDeliveryManager(DefaultRetryPolicy)
	manager.RegisterSubscription("acct-1", EventTypeAny, receiver.server.URL)
	source := NewAuditTrailEventSource(auditServer.URL, manager)

	source.PollOnce()
	if receiver.eventCount() != 1 {
		t.Fatalf("expected only the order-lifecycle event to be delivered, got %d events", receiver.eventCount())
	}
}

func TestPollOnceOnUnreachableAuditTrailFailsSoft(t *testing.T) {
	manager := NewWebhookDeliveryManager(DefaultRetryPolicy)
	source := NewAuditTrailEventSource("http://127.0.0.1:1", manager)

	if delivered := source.PollOnce(); delivered != 0 {
		t.Fatalf("expected 0 delivered when the audit trail is unreachable, got %d", delivered)
	}
}

func TestPollOnceIgnoresEntriesWithNoAccountIdentifier(t *testing.T) {
	receiver := newRealReceiver(0)
	defer receiver.close()

	auditServer := startAuditTrailStub(t, func() []auditTrailEntryWire {
		return []auditTrailEntryWire{{EventType: "ORDER_SUBMITTED", ClientAccountIdentifier: ""}}
	})
	defer auditServer.Close()

	manager := NewWebhookDeliveryManager(DefaultRetryPolicy)
	source := NewAuditTrailEventSource(auditServer.URL, manager)
	source.PollOnce()

	if receiver.eventCount() != 0 {
		t.Fatalf("expected no delivery for an entry with an empty account identifier, got %d", receiver.eventCount())
	}
}
