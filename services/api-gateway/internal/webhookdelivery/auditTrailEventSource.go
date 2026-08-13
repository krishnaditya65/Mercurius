// auditTrailEventSource.go supplies the "real event occurs" half of
// this package's doc comment: it polls oms-gateway's real, running
// GET /audit-trail endpoint (see
// services/oms-gateway/internal/audittrail) on a real interval, and for
// every NEW entry since the last poll, converts it into an Event and
// delivers it via WebhookDeliveryManager.DeliverEvent.
package webhookdelivery

import (
	"encoding/json"
	"log"
	"net/http"
	"time"
)

// auditTrailEntryWire mirrors the JSON shape of
// services/oms-gateway/internal/audittrail.Entry — this package
// deliberately doesn't import oms-gateway's Go module (api-gateway and
// oms-gateway are separate services/modules, communicating only over
// HTTP, same as every other cross-service relationship in this repo),
// so it declares its own matching wire struct instead.
type auditTrailEntryWire struct {
	RecordedAtTime          time.Time `json:"recordedAtTime"`
	EventType               string    `json:"eventType"`
	ClientAccountIdentifier string    `json:"clientAccountIdentifier,omitempty"`
	InstrumentSymbol        string    `json:"instrumentSymbol,omitempty"`
	DetailMessage           string    `json:"detailMessage,omitempty"`
}

// auditTrailEventTypeToWebhookEventType maps oms-gateway's audit-trail
// EventType strings onto this package's own, smaller EventType set —
// only order-lifecycle events a webhook subscriber plausibly cares
// about; internal/administrative event types (e.g. market session
// open/close) are intentionally not surfaced as webhooks.
var auditTrailEventTypeToWebhookEventType = map[string]EventType{
	"ORDER_SUBMITTED": EventTypeOrderSubmitted,
	"ORDER_FILLED":    EventTypeOrderFilled,
	"ORDER_REJECTED":  EventTypeOrderRejected,
	"ORDER_CANCELLED": EventTypeOrderCancelled,
}

// AuditTrailEventSource polls one oms-gateway instance's audit trail
// and forwards new order-lifecycle entries to a WebhookDeliveryManager.
type AuditTrailEventSource struct {
	omsGatewayBaseUrl string
	httpClient        *http.Client
	deliveryManager   *WebhookDeliveryManager
	entriesSeenSoFar  int
}

// NewAuditTrailEventSource wires a poller against omsGatewayBaseUrl
// (e.g. "http://127.0.0.1:8081") that forwards new events to
// deliveryManager.
func NewAuditTrailEventSource(omsGatewayBaseUrl string, deliveryManager *WebhookDeliveryManager) *AuditTrailEventSource {
	return &AuditTrailEventSource{
		omsGatewayBaseUrl: omsGatewayBaseUrl,
		httpClient:        &http.Client{Timeout: 5 * time.Second},
		deliveryManager:   deliveryManager,
	}
}

// PollOnce fetches the current audit trail and delivers webhook events
// for every entry appended since the previous call to PollOnce (the
// audit trail is append-only, so "new since last poll" is simply
// "beyond the count seen last time" — see
// internal/audittrail.AuditTrail's own append-only guarantee). Returns
// the number of NEW entries processed this call.
func (source *AuditTrailEventSource) PollOnce() int {
	response, err := source.httpClient.Get(source.omsGatewayBaseUrl + "/audit-trail")
	if err != nil {
		log.Printf("webhookdelivery: failed polling oms-gateway audit trail: %v", err)
		return 0
	}
	defer response.Body.Close()

	var entries []auditTrailEntryWire
	if decodeErr := json.NewDecoder(response.Body).Decode(&entries); decodeErr != nil {
		log.Printf("webhookdelivery: failed decoding oms-gateway audit trail: %v", decodeErr)
		return 0
	}

	if len(entries) <= source.entriesSeenSoFar {
		return 0
	}
	newEntries := entries[source.entriesSeenSoFar:]
	source.entriesSeenSoFar = len(entries)

	deliveredCount := 0
	for _, entry := range newEntries {
		webhookEventType, isRelevant := auditTrailEventTypeToWebhookEventType[entry.EventType]
		if !isRelevant || entry.ClientAccountIdentifier == "" {
			continue
		}
		source.deliveryManager.DeliverEvent(Event{
			AccountIdentifier: entry.ClientAccountIdentifier,
			EventType:         webhookEventType,
			DetailMessage:     entry.DetailMessage,
			InstrumentSymbol:  entry.InstrumentSymbol,
			OccurredAtTime:    entry.RecordedAtTime,
		})
		deliveredCount++
	}
	return deliveredCount
}

// RunForever calls PollOnce every pollInterval until stopChannel is
// closed. Meant to be run in its own goroutine from main.go.
func (source *AuditTrailEventSource) RunForever(pollInterval time.Duration, stopChannel <-chan struct{}) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stopChannel:
			return
		case <-ticker.C:
			source.PollOnce()
		}
	}
}
