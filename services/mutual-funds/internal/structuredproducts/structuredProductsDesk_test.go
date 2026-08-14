package structuredproducts

import (
	"testing"
	"time"
)

func newTestDesk() (*Catalog, *Desk) {
	catalog := NewCatalog()
	return catalog, NewDesk(catalog)
}

func TestNewCatalogSeedsThreeNotes(t *testing.T) {
	catalog := NewCatalog()
	if len(catalog.ListAll()) != 3 {
		t.Fatalf("expected 3 seed notes, got %d", len(catalog.ListAll()))
	}
}

func TestLookupFindsKnownNote(t *testing.T) {
	catalog := NewCatalog()
	note, wasFound := catalog.Lookup("SP-CPN-NIFTY-150-20")
	if !wasFound {
		t.Fatal("expected to find SP-CPN-NIFTY-150-20")
	}
	if note.ParticipationRatePercent != 150 || note.CapPercent != 20 {
		t.Fatalf("unexpected note fields: %+v", note)
	}
}

func TestSubscribeRejectsNonPositivePrincipal(t *testing.T) {
	_, desk := newTestDesk()
	if _, err := desk.Subscribe("acct-1", "SP-CPN-NIFTY-150-20", 0, time.Now()); err != ErrInvalidPrincipalAmount {
		t.Fatalf("expected ErrInvalidPrincipalAmount, got %v", err)
	}
}

func TestSubscribeRejectsUnknownNote(t *testing.T) {
	_, desk := newTestDesk()
	if _, err := desk.Subscribe("acct-1", "NOT-A-NOTE", 100000, time.Now()); err != ErrUnknownNoteForSubscription {
		t.Fatalf("expected ErrUnknownNoteForSubscription, got %v", err)
	}
}

func TestSubscribeStartsSubscribed(t *testing.T) {
	_, desk := newTestDesk()
	subscription, err := desk.Subscribe("acct-1", "SP-CPN-NIFTY-150-20", 100000, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if subscription.Status != SubscriptionStatusSubscribed {
		t.Fatalf("expected SUBSCRIBED, got %s", subscription.Status)
	}
}

// TestMatureSubscriptionHandWorkedPayoff exercises the full state machine
// with the same hand-worked math as
// TestCalculatePayoffUncappedParticipation: 100000 principal, 150%
// participation, +10% index return -> payout 115000.
func TestMatureSubscriptionHandWorkedPayoff(t *testing.T) {
	_, desk := newTestDesk()
	subscription, _ := desk.Subscribe("acct-1", "SP-CPN-NIFTY-150-20", 100000, time.Now())

	matured, err := desk.MatureSubscription(subscription.SubscriptionId, 10.0, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if matured.Status != SubscriptionStatusMatured {
		t.Fatalf("expected MATURED, got %s", matured.Status)
	}
	if matured.PayoutInMinorUnits != 115000 {
		t.Fatalf("expected payout 115000, got %d", matured.PayoutInMinorUnits)
	}
	if matured.EffectiveReturnPercent != 15.0 {
		t.Fatalf("expected effective return 15%%, got %v", matured.EffectiveReturnPercent)
	}
}

func TestMatureSubscriptionRejectsAlreadyMatured(t *testing.T) {
	_, desk := newTestDesk()
	subscription, _ := desk.Subscribe("acct-1", "SP-CPN-NIFTY-150-20", 100000, time.Now())
	desk.MatureSubscription(subscription.SubscriptionId, 10.0, time.Now())

	if _, err := desk.MatureSubscription(subscription.SubscriptionId, 10.0, time.Now()); err != ErrSubscriptionAlreadyMatured {
		t.Fatalf("expected ErrSubscriptionAlreadyMatured, got %v", err)
	}
}

func TestMatureSubscriptionUnknownReturnsError(t *testing.T) {
	_, desk := newTestDesk()
	if _, err := desk.MatureSubscription("no-such-subscription", 10.0, time.Now()); err != ErrUnknownSubscription {
		t.Fatalf("expected ErrUnknownSubscription, got %v", err)
	}
}

func TestMatureSubscriptionCapitalProtectedOnDownside(t *testing.T) {
	_, desk := newTestDesk()
	subscription, _ := desk.Subscribe("acct-1", "SP-CPN-SENSEX-100-15", 200000, time.Now())

	matured, err := desk.MatureSubscription(subscription.SubscriptionId, -25.0, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if matured.PayoutInMinorUnits != 200000 {
		t.Fatalf("expected full principal 200000 protected on a -25%% index move, got %d", matured.PayoutInMinorUnits)
	}
}

func TestSubscriptionsForAccountReturnsOnlyThatAccountSortedBySubscribedAt(t *testing.T) {
	_, desk := newTestDesk()
	now := time.Now()
	desk.Subscribe("acct-1", "SP-CPN-NIFTY-150-20", 100000, now)
	desk.Subscribe("acct-2", "SP-CPN-NIFTY-150-20", 100000, now)
	desk.Subscribe("acct-1", "SP-CPN-SENSEX-100-15", 100000, now.Add(time.Hour))

	subscriptions := desk.SubscriptionsForAccount("acct-1")
	if len(subscriptions) != 2 {
		t.Fatalf("expected 2 subscriptions for acct-1, got %d", len(subscriptions))
	}
	if subscriptions[0].SubscribedAt.After(subscriptions[1].SubscribedAt) {
		t.Fatal("expected subscriptions sorted by SubscribedAt")
	}
}

func TestLookupSubscriptionFindsSubscribed(t *testing.T) {
	_, desk := newTestDesk()
	subscription, _ := desk.Subscribe("acct-1", "SP-CPN-NIFTY-150-20", 100000, time.Now())
	found, wasFound := desk.LookupSubscription(subscription.SubscriptionId)
	if !wasFound || found.SubscriptionId != subscription.SubscriptionId {
		t.Fatal("expected to find the subscribed subscription")
	}
}

func TestLookupSubscriptionUnknownReturnsFalse(t *testing.T) {
	_, desk := newTestDesk()
	if _, wasFound := desk.LookupSubscription("no-such-subscription"); wasFound {
		t.Fatal("expected not to find an unknown subscription")
	}
}
