package webhookdelivery

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestRegisterSubscriptionSucceeds(t *testing.T) {
	manager := NewWebhookDeliveryManager(DefaultRetryPolicy)
	subscription, err := manager.RegisterSubscription("acct-1", EventTypeOrderFilled, "http://example.test/hook")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if subscription.SubscriptionIdentifier == "" {
		t.Fatalf("expected a non-empty subscription identifier")
	}
}

func TestRegisterSubscriptionRequiresAccountIdentifier(t *testing.T) {
	manager := NewWebhookDeliveryManager(DefaultRetryPolicy)
	_, err := manager.RegisterSubscription("", EventTypeOrderFilled, "http://example.test/hook")
	if !errors.Is(err, ErrAccountIdentifierRequired) {
		t.Fatalf("expected ErrAccountIdentifierRequired, got %v", err)
	}
}

func TestRegisterSubscriptionRequiresTargetUrl(t *testing.T) {
	manager := NewWebhookDeliveryManager(DefaultRetryPolicy)
	_, err := manager.RegisterSubscription("acct-1", EventTypeOrderFilled, "")
	if !errors.Is(err, ErrTargetUrlRequired) {
		t.Fatalf("expected ErrTargetUrlRequired, got %v", err)
	}
}

func TestRegisterSubscriptionDefaultsToEventTypeAny(t *testing.T) {
	manager := NewWebhookDeliveryManager(DefaultRetryPolicy)
	subscription, _ := manager.RegisterSubscription("acct-1", "", "http://example.test/hook")
	if subscription.EventType != EventTypeAny {
		t.Fatalf("expected default EventTypeAny, got %v", subscription.EventType)
	}
}

func TestRemoveSubscriptionOnUnknownIdReturnsNotFound(t *testing.T) {
	manager := NewWebhookDeliveryManager(DefaultRetryPolicy)
	err := manager.RemoveSubscription("nope")
	if !errors.Is(err, ErrSubscriptionNotFound) {
		t.Fatalf("expected ErrSubscriptionNotFound, got %v", err)
	}
}

// realReceiver is a real local HTTP server standing in for a webhook
// receiver — this test suite delivers to it over a real HTTP POST, not
// a mock assertion, per the task's explicit requirement.
type realReceiver struct {
	server           *httptest.Server
	mutexGuardingLog sync.Mutex
	receivedEvents   []Event
	failFirstNCalls  int
	callCount        int
}

func newRealReceiver(failFirstNCalls int) *realReceiver {
	receiver := &realReceiver{failFirstNCalls: failFirstNCalls}
	receiver.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receiver.mutexGuardingLog.Lock()
		receiver.callCount++
		shouldFail := receiver.callCount <= receiver.failFirstNCalls
		receiver.mutexGuardingLog.Unlock()

		if shouldFail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		var event Event
		json.NewDecoder(r.Body).Decode(&event)
		receiver.mutexGuardingLog.Lock()
		receiver.receivedEvents = append(receiver.receivedEvents, event)
		receiver.mutexGuardingLog.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	return receiver
}

func (receiver *realReceiver) close() { receiver.server.Close() }

func (receiver *realReceiver) eventCount() int {
	receiver.mutexGuardingLog.Lock()
	defer receiver.mutexGuardingLog.Unlock()
	return len(receiver.receivedEvents)
}

func TestDeliverEventPostsToARealLocalReceiver(t *testing.T) {
	receiver := newRealReceiver(0)
	defer receiver.close()

	manager := NewWebhookDeliveryManager(DefaultRetryPolicy)
	manager.RegisterSubscription("acct-1", EventTypeOrderFilled, receiver.server.URL)

	outcomes := manager.DeliverEvent(Event{AccountIdentifier: "acct-1", EventType: EventTypeOrderFilled, InstrumentSymbol: "RELIANCE"})

	if len(outcomes) != 1 || !outcomes[0].WasSuccessful {
		t.Fatalf("expected exactly 1 successful delivery outcome, got %+v", outcomes)
	}
	if receiver.eventCount() != 1 {
		t.Fatalf("expected the real receiver to have received exactly 1 event, got %d", receiver.eventCount())
	}
}

func TestDeliverEventRetriesOnFailureThenSucceeds(t *testing.T) {
	receiver := newRealReceiver(2) // fails first 2 calls, succeeds 3rd
	defer receiver.close()

	manager := NewWebhookDeliveryManager(RetryPolicy{MaxAttempts: 3, DelayBetweenRetries: 5 * time.Millisecond})
	manager.RegisterSubscription("acct-1", EventTypeOrderFilled, receiver.server.URL)

	outcomes := manager.DeliverEvent(Event{AccountIdentifier: "acct-1", EventType: EventTypeOrderFilled})

	if len(outcomes) != 3 {
		t.Fatalf("expected 3 delivery attempts (2 failed retries + 1 success), got %d", len(outcomes))
	}
	if outcomes[0].WasSuccessful || outcomes[1].WasSuccessful {
		t.Fatalf("expected the first two attempts to fail")
	}
	if !outcomes[2].WasSuccessful {
		t.Fatalf("expected the third attempt to succeed")
	}
	if receiver.eventCount() != 1 {
		t.Fatalf("expected the receiver to have durably received the event exactly once, got %d", receiver.eventCount())
	}
}

func TestDeliverEventExhaustsRetriesAndRecordsEveryFailedAttempt(t *testing.T) {
	receiver := newRealReceiver(999) // always fails
	defer receiver.close()

	manager := NewWebhookDeliveryManager(RetryPolicy{MaxAttempts: 3, DelayBetweenRetries: 1 * time.Millisecond})
	manager.RegisterSubscription("acct-1", EventTypeOrderFilled, receiver.server.URL)

	outcomes := manager.DeliverEvent(Event{AccountIdentifier: "acct-1", EventType: EventTypeOrderFilled})

	if len(outcomes) != 3 {
		t.Fatalf("expected exactly MaxAttempts=3 recorded outcomes, got %d", len(outcomes))
	}
	for _, outcome := range outcomes {
		if outcome.WasSuccessful {
			t.Fatalf("expected every attempt against an always-failing receiver to fail")
		}
	}
}

func TestDeliverEventDoesNotDeliverToNonMatchingEventType(t *testing.T) {
	receiver := newRealReceiver(0)
	defer receiver.close()

	manager := NewWebhookDeliveryManager(DefaultRetryPolicy)
	manager.RegisterSubscription("acct-1", EventTypeOrderCancelled, receiver.server.URL)

	outcomes := manager.DeliverEvent(Event{AccountIdentifier: "acct-1", EventType: EventTypeOrderFilled})

	if len(outcomes) != 0 {
		t.Fatalf("expected no delivery attempts for a non-matching event type, got %d", len(outcomes))
	}
}

func TestDeliverEventDoesNotDeliverToOtherAccounts(t *testing.T) {
	receiver := newRealReceiver(0)
	defer receiver.close()

	manager := NewWebhookDeliveryManager(DefaultRetryPolicy)
	manager.RegisterSubscription("acct-1", EventTypeAny, receiver.server.URL)

	outcomes := manager.DeliverEvent(Event{AccountIdentifier: "acct-2", EventType: EventTypeOrderFilled})

	if len(outcomes) != 0 {
		t.Fatalf("expected no delivery attempts for a different account, got %d", len(outcomes))
	}
}

func TestSubscriptionWithEventTypeAnyReceivesEveryEventType(t *testing.T) {
	receiver := newRealReceiver(0)
	defer receiver.close()

	manager := NewWebhookDeliveryManager(DefaultRetryPolicy)
	manager.RegisterSubscription("acct-1", EventTypeAny, receiver.server.URL)

	manager.DeliverEvent(Event{AccountIdentifier: "acct-1", EventType: EventTypeOrderFilled})
	manager.DeliverEvent(Event{AccountIdentifier: "acct-1", EventType: EventTypeOrderRejected})

	if receiver.eventCount() != 2 {
		t.Fatalf("expected an ANY subscription to receive both event types, got %d", receiver.eventCount())
	}
}

func TestDeliveryHistoryAccumulatesAcrossCalls(t *testing.T) {
	receiver := newRealReceiver(0)
	defer receiver.close()

	manager := NewWebhookDeliveryManager(DefaultRetryPolicy)
	manager.RegisterSubscription("acct-1", EventTypeAny, receiver.server.URL)

	manager.DeliverEvent(Event{AccountIdentifier: "acct-1", EventType: EventTypeOrderFilled})
	manager.DeliverEvent(Event{AccountIdentifier: "acct-1", EventType: EventTypeOrderFilled})

	if len(manager.DeliveryHistory()) != 2 {
		t.Fatalf("expected 2 recorded history entries, got %d", len(manager.DeliveryHistory()))
	}
}

func TestListSubscriptionsForAccountReturnsOnlyThatAccount(t *testing.T) {
	manager := NewWebhookDeliveryManager(DefaultRetryPolicy)
	manager.RegisterSubscription("acct-1", EventTypeAny, "http://example.test/a")
	manager.RegisterSubscription("acct-2", EventTypeAny, "http://example.test/b")

	subscriptions := manager.ListSubscriptionsForAccount("acct-1")
	if len(subscriptions) != 1 {
		t.Fatalf("expected 1 subscription for acct-1, got %d", len(subscriptions))
	}
}
