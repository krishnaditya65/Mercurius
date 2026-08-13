// Package webhookdelivery is FEATURES.md §18's "Webhook system for
// order/portfolio events" — real webhook URL registration per
// account/event-type, and real delivery: when a real event occurs
// (api-gateway polls oms-gateway's real audit trail for new
// order-lifecycle events — see sourcepoller.go), this package POSTs it
// to every registered webhook URL for that account, with real
// retry-on-failure logic, not fire-and-forget.
package webhookdelivery

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// ErrAccountIdentifierRequired is returned when registering a
// subscription with no owning account.
var ErrAccountIdentifierRequired = errors.New("webhookdelivery: accountIdentifier is required")

// ErrTargetUrlRequired is returned when registering a subscription with
// no target URL.
var ErrTargetUrlRequired = errors.New("webhookdelivery: targetUrl is required")

// ErrSubscriptionNotFound is returned when a lookup key does not match
// any registered subscription.
var ErrSubscriptionNotFound = errors.New("webhookdelivery: subscription not found")

// EventType names the kind of order/portfolio event a subscription can
// register for. "ANY" (EventTypeAny) subscribes to every event type.
type EventType string

const (
	EventTypeOrderSubmitted EventType = "ORDER_SUBMITTED"
	EventTypeOrderFilled    EventType = "ORDER_FILLED"
	EventTypeOrderRejected  EventType = "ORDER_REJECTED"
	EventTypeOrderCancelled EventType = "ORDER_CANCELLED"
	EventTypeAny            EventType = "ANY"
)

// Subscription is one registered webhook: deliver events of EventType
// for AccountIdentifier to TargetUrl.
type Subscription struct {
	SubscriptionIdentifier string    `json:"subscriptionIdentifier"`
	AccountIdentifier      string    `json:"accountIdentifier"`
	EventType              EventType `json:"eventType"`
	TargetUrl              string    `json:"targetUrl"`
	RegisteredAtTime       time.Time `json:"registeredAtTime"`
}

// Event is one order/portfolio event to be delivered to matching
// subscriptions.
type Event struct {
	AccountIdentifier string    `json:"accountIdentifier"`
	EventType         EventType `json:"eventType"`
	DetailMessage     string    `json:"detailMessage,omitempty"`
	InstrumentSymbol  string    `json:"instrumentSymbol,omitempty"`
	OccurredAtTime    time.Time `json:"occurredAtTime"`
}

// DeliveryAttemptOutcome records one real attempt to deliver one event
// to one subscription — queryable so an operator/developer can see
// what was actually sent and what happened.
type DeliveryAttemptOutcome struct {
	SubscriptionIdentifier string    `json:"subscriptionIdentifier"`
	TargetUrl              string    `json:"targetUrl"`
	EventType              EventType `json:"eventType"`
	AttemptNumber          int       `json:"attemptNumber"`
	WasSuccessful          bool      `json:"wasSuccessful"`
	HttpStatusCode         int       `json:"httpStatusCode,omitempty"`
	ErrorMessage           string    `json:"errorMessage,omitempty"`
	AttemptedAtTime        time.Time `json:"attemptedAtTime"`
}

// RetryPolicy configures real retry-on-failure behavior.
type RetryPolicy struct {
	MaxAttempts         int
	DelayBetweenRetries time.Duration
}

// DefaultRetryPolicy retries a failed delivery twice more (3 attempts
// total) with a short fixed delay — enough to survive a brief receiver
// hiccup without holding up the delivery goroutine indefinitely.
var DefaultRetryPolicy = RetryPolicy{MaxAttempts: 3, DelayBetweenRetries: 200 * time.Millisecond}

// WebhookDeliveryManager holds registered subscriptions, delivers
// events to them with real HTTP POSTs and real retries, and records
// every attempt outcome. Safe for concurrent use.
type WebhookDeliveryManager struct {
	mutexGuardingState sync.Mutex
	subscriptions      map[string]*Subscription
	deliveryHistory    []DeliveryAttemptOutcome
	subscriptionIdSeq  uint64
	httpClient         *http.Client
	retryPolicy        RetryPolicy
	nowFunc            func() time.Time
}

func NewWebhookDeliveryManager(retryPolicy RetryPolicy) *WebhookDeliveryManager {
	return &WebhookDeliveryManager{
		subscriptions: make(map[string]*Subscription),
		httpClient:    &http.Client{Timeout: 5 * time.Second},
		retryPolicy:   retryPolicy,
		nowFunc:       time.Now,
	}
}

// RegisterSubscription adds a new webhook subscription.
func (manager *WebhookDeliveryManager) RegisterSubscription(accountIdentifier string, eventType EventType, targetUrl string) (Subscription, error) {
	if accountIdentifier == "" {
		return Subscription{}, ErrAccountIdentifierRequired
	}
	if targetUrl == "" {
		return Subscription{}, ErrTargetUrlRequired
	}
	if eventType == "" {
		eventType = EventTypeAny
	}

	manager.mutexGuardingState.Lock()
	defer manager.mutexGuardingState.Unlock()

	manager.subscriptionIdSeq++
	subscription := Subscription{
		SubscriptionIdentifier: fmt.Sprintf("sub-%d", manager.subscriptionIdSeq),
		AccountIdentifier:      accountIdentifier,
		EventType:              eventType,
		TargetUrl:              targetUrl,
		RegisteredAtTime:       manager.nowFunc(),
	}
	manager.subscriptions[subscription.SubscriptionIdentifier] = &subscription
	return subscription, nil
}

// RemoveSubscription deletes a subscription by identifier.
func (manager *WebhookDeliveryManager) RemoveSubscription(subscriptionIdentifier string) error {
	manager.mutexGuardingState.Lock()
	defer manager.mutexGuardingState.Unlock()

	if _, exists := manager.subscriptions[subscriptionIdentifier]; !exists {
		return ErrSubscriptionNotFound
	}
	delete(manager.subscriptions, subscriptionIdentifier)
	return nil
}

// subscriptionsMatching returns every registered subscription that
// should receive event — same account, and either that exact event
// type or EventTypeAny.
func (manager *WebhookDeliveryManager) subscriptionsMatching(event Event) []Subscription {
	manager.mutexGuardingState.Lock()
	defer manager.mutexGuardingState.Unlock()

	var matches []Subscription
	for _, subscription := range manager.subscriptions {
		if subscription.AccountIdentifier != event.AccountIdentifier {
			continue
		}
		if subscription.EventType != event.EventType && subscription.EventType != EventTypeAny {
			continue
		}
		matches = append(matches, *subscription)
	}
	return matches
}

// DeliverEvent delivers event to every matching subscription
// SYNCHRONOUSLY, with real HTTP POSTs and real retries per
// manager.retryPolicy, recording every attempt. Returns the outcomes
// for every (subscription, attempt) pair. Callers that want
// fire-and-forget delivery should call this from their own goroutine —
// this method itself never drops an event silently: every attempt,
// success or failure, is recorded.
func (manager *WebhookDeliveryManager) DeliverEvent(event Event) []DeliveryAttemptOutcome {
	matchingSubscriptions := manager.subscriptionsMatching(event)

	var allOutcomes []DeliveryAttemptOutcome
	for _, subscription := range matchingSubscriptions {
		outcomes := manager.deliverToOneSubscriptionWithRetries(subscription, event)
		allOutcomes = append(allOutcomes, outcomes...)
	}
	return allOutcomes
}

func (manager *WebhookDeliveryManager) deliverToOneSubscriptionWithRetries(subscription Subscription, event Event) []DeliveryAttemptOutcome {
	payload, marshalErr := json.Marshal(event)
	if marshalErr != nil {
		outcome := DeliveryAttemptOutcome{
			SubscriptionIdentifier: subscription.SubscriptionIdentifier,
			TargetUrl:              subscription.TargetUrl,
			EventType:              event.EventType,
			AttemptNumber:          1,
			WasSuccessful:          false,
			ErrorMessage:           marshalErr.Error(),
			AttemptedAtTime:        manager.nowFunc(),
		}
		manager.recordOutcome(outcome)
		return []DeliveryAttemptOutcome{outcome}
	}

	maxAttempts := manager.retryPolicy.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	var outcomes []DeliveryAttemptOutcome
	for attemptNumber := 1; attemptNumber <= maxAttempts; attemptNumber++ {
		outcome := manager.attemptOneDelivery(subscription, event.EventType, payload, attemptNumber)
		outcomes = append(outcomes, outcome)
		manager.recordOutcome(outcome)

		if outcome.WasSuccessful {
			break
		}
		if attemptNumber < maxAttempts {
			time.Sleep(manager.retryPolicy.DelayBetweenRetries)
		}
	}
	return outcomes
}

func (manager *WebhookDeliveryManager) attemptOneDelivery(subscription Subscription, eventType EventType, payload []byte, attemptNumber int) DeliveryAttemptOutcome {
	outcome := DeliveryAttemptOutcome{
		SubscriptionIdentifier: subscription.SubscriptionIdentifier,
		TargetUrl:              subscription.TargetUrl,
		EventType:              eventType,
		AttemptNumber:          attemptNumber,
		AttemptedAtTime:        manager.nowFunc(),
	}

	request, requestErr := http.NewRequest(http.MethodPost, subscription.TargetUrl, bytes.NewReader(payload))
	if requestErr != nil {
		outcome.ErrorMessage = requestErr.Error()
		return outcome
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Mercurius-Webhook-Event", string(eventType))

	response, doErr := manager.httpClient.Do(request)
	if doErr != nil {
		outcome.ErrorMessage = doErr.Error()
		return outcome
	}
	defer response.Body.Close()

	outcome.HttpStatusCode = response.StatusCode
	outcome.WasSuccessful = response.StatusCode >= 200 && response.StatusCode < 300
	if !outcome.WasSuccessful {
		outcome.ErrorMessage = fmt.Sprintf("receiver returned non-2xx status %d", response.StatusCode)
	}
	return outcome
}

func (manager *WebhookDeliveryManager) recordOutcome(outcome DeliveryAttemptOutcome) {
	manager.mutexGuardingState.Lock()
	defer manager.mutexGuardingState.Unlock()
	manager.deliveryHistory = append(manager.deliveryHistory, outcome)
}

// DeliveryHistory returns every recorded delivery attempt outcome,
// oldest first.
func (manager *WebhookDeliveryManager) DeliveryHistory() []DeliveryAttemptOutcome {
	manager.mutexGuardingState.Lock()
	defer manager.mutexGuardingState.Unlock()

	historyCopy := make([]DeliveryAttemptOutcome, len(manager.deliveryHistory))
	copy(historyCopy, manager.deliveryHistory)
	return historyCopy
}

// ListSubscriptionsForAccount returns every subscription registered for
// accountIdentifier.
func (manager *WebhookDeliveryManager) ListSubscriptionsForAccount(accountIdentifier string) []Subscription {
	manager.mutexGuardingState.Lock()
	defer manager.mutexGuardingState.Unlock()

	var matches []Subscription
	for _, subscription := range manager.subscriptions {
		if subscription.AccountIdentifier == accountIdentifier {
			matches = append(matches, *subscription)
		}
	}
	return matches
}
