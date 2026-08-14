package structuredproducts

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"
)

// SubscriptionStatus is a real state machine: SUBSCRIBED -> MATURED.
type SubscriptionStatus string

const (
	SubscriptionStatusSubscribed SubscriptionStatus = "SUBSCRIBED"
	SubscriptionStatusMatured    SubscriptionStatus = "MATURED"
)

// Subscription is one account's illustrative investment into one
// structured note.
type Subscription struct {
	SubscriptionId               string
	AccountIdentifier            string
	NoteId                       string
	PrincipalInMinorUnits        int64
	SubscribedAt                 time.Time
	Status                       SubscriptionStatus
	MaturedAt                    time.Time
	UnderlyingIndexReturnPercent float64
	EffectiveReturnPercent       float64
	WasCapped                    bool
	PayoutInMinorUnits           int64
}

var ErrUnknownNoteForSubscription = fmt.Errorf("no such structured note exists in the catalog")
var ErrInvalidPrincipalAmount = fmt.Errorf("principal amount must be strictly positive")
var ErrUnknownSubscription = fmt.Errorf("no such subscription exists")
var ErrSubscriptionAlreadyMatured = fmt.Errorf("subscription has already matured")

// Desk is safe for concurrent use. See the package doc comment for the
// loud "this is not a real structured products desk" caveat.
type Desk struct {
	catalog *Catalog

	mutexGuardingState sync.Mutex
	subscriptionsById  map[string]*Subscription
}

// NewDesk builds a desk against catalog.
func NewDesk(catalog *Catalog) *Desk {
	return &Desk{catalog: catalog, subscriptionsById: make(map[string]*Subscription)}
}

// Subscribe records a new SUBSCRIBED position into noteId, funded with
// principalInMinorUnits.
func (desk *Desk) Subscribe(accountIdentifier string, noteId string, principalInMinorUnits int64, now time.Time) (*Subscription, error) {
	if principalInMinorUnits <= 0 {
		return nil, ErrInvalidPrincipalAmount
	}
	if _, wasFound := desk.catalog.Lookup(noteId); !wasFound {
		return nil, ErrUnknownNoteForSubscription
	}

	subscriptionId, genError := generateSubscriptionId()
	if genError != nil {
		return nil, fmt.Errorf("failed to generate subscription id: %w", genError)
	}

	subscription := &Subscription{
		SubscriptionId:        subscriptionId,
		AccountIdentifier:     accountIdentifier,
		NoteId:                noteId,
		PrincipalInMinorUnits: principalInMinorUnits,
		SubscribedAt:          now,
		Status:                SubscriptionStatusSubscribed,
	}

	desk.mutexGuardingState.Lock()
	desk.subscriptionsById[subscriptionId] = subscription
	desk.mutexGuardingState.Unlock()

	return subscription, nil
}

// MatureSubscription transitions a SUBSCRIBED subscription to MATURED,
// computing its real payoff via CalculatePayoff given
// underlyingIndexReturnPercent — the underlying index's total return over
// the note's tenor, supplied by the caller (see the package doc comment:
// there is no real index feed here).
func (desk *Desk) MatureSubscription(subscriptionId string, underlyingIndexReturnPercent float64, now time.Time) (*Subscription, error) {
	desk.mutexGuardingState.Lock()
	defer desk.mutexGuardingState.Unlock()

	subscription, wasFound := desk.subscriptionsById[subscriptionId]
	if !wasFound {
		return nil, ErrUnknownSubscription
	}
	if subscription.Status == SubscriptionStatusMatured {
		return nil, ErrSubscriptionAlreadyMatured
	}

	note, wasFound := desk.catalog.Lookup(subscription.NoteId)
	if !wasFound {
		return nil, ErrUnknownNoteForSubscription
	}

	payout, effectiveReturn, wasCapped := CalculatePayoff(note, subscription.PrincipalInMinorUnits, underlyingIndexReturnPercent)

	subscription.UnderlyingIndexReturnPercent = underlyingIndexReturnPercent
	subscription.EffectiveReturnPercent = effectiveReturn
	subscription.WasCapped = wasCapped
	subscription.PayoutInMinorUnits = payout
	subscription.MaturedAt = now
	subscription.Status = SubscriptionStatusMatured

	return subscription, nil
}

// SubscriptionsForAccount returns every subscription accountIdentifier
// holds, sorted by SubscribedAt.
func (desk *Desk) SubscriptionsForAccount(accountIdentifier string) []*Subscription {
	desk.mutexGuardingState.Lock()
	defer desk.mutexGuardingState.Unlock()

	matching := make([]*Subscription, 0)
	for _, subscription := range desk.subscriptionsById {
		if subscription.AccountIdentifier == accountIdentifier {
			matching = append(matching, subscription)
		}
	}
	sort.Slice(matching, func(i, j int) bool { return matching[i].SubscribedAt.Before(matching[j].SubscribedAt) })
	return matching
}

// LookupSubscription returns the subscription, or false if subscriptionId
// isn't known.
func (desk *Desk) LookupSubscription(subscriptionId string) (*Subscription, bool) {
	desk.mutexGuardingState.Lock()
	defer desk.mutexGuardingState.Unlock()

	subscription, wasFound := desk.subscriptionsById[subscriptionId]
	return subscription, wasFound
}

func generateSubscriptionId() (string, error) {
	randomBytes := make([]byte, 8)
	if _, readError := rand.Read(randomBytes); readError != nil {
		return "", readError
	}
	return "sp-sub-" + hex.EncodeToString(randomBytes), nil
}
