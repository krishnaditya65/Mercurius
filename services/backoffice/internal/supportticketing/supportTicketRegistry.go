// Package supportticketing implements FEATURES.md §14's "in-app support
// chat / ticketing integration": real ticket creation, a real status
// lifecycle (open -> in-progress -> resolved -> closed, with a resolved
// ticket re-openable back to in-progress if the customer follows up),
// real per-ticket message threads (customer messages AND agent replies,
// both timestamped and attributed), real agent assignment, and real
// query paths for both account holders ("my tickets") and support staff
// ("my queue" / "unassigned queue" / "everything").
//
// TODO(real build): in-memory, not persisted (a restart loses every
// ticket and message — same honest gap as every other in-memory registry
// in this service); no auth/RBAC (same documented gap as
// internal/accountcontrol) — nothing here checks that the caller
// claiming to be `accountIdentifier` or `agentIdentifier` actually is
// that person; no real-time delivery (no websocket/SSE push — a client
// has to poll GetMessageThread); no attachments/file upload.
package supportticketing

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// TicketStatus is a closed set of real lifecycle states.
type TicketStatus string

const (
	TicketStatusOpen       TicketStatus = "open"
	TicketStatusInProgress TicketStatus = "in-progress"
	TicketStatusResolved   TicketStatus = "resolved"
	TicketStatusClosed     TicketStatus = "closed"
)

// MessageAuthorType distinguishes a customer's own message from an
// agent's reply within one thread.
type MessageAuthorType string

const (
	MessageAuthorTypeCustomer MessageAuthorType = "customer"
	MessageAuthorTypeAgent    MessageAuthorType = "agent"
)

var (
	ErrTicketNotFound            = errors.New("supportticketing: ticket not found")
	ErrSubjectRequired           = errors.New("supportticketing: subject is required")
	ErrInitialMessageRequired    = errors.New("supportticketing: initialMessageBody is required")
	ErrAccountIdentifierRequired = errors.New("supportticketing: accountIdentifier is required")
	ErrMessageBodyRequired       = errors.New("supportticketing: message body is required")
	ErrNotTicketOwner            = errors.New("supportticketing: accountIdentifier does not own this ticket")
	ErrTicketAlreadyClosed       = errors.New("supportticketing: ticket is closed and cannot accept further activity")
	ErrAgentIdentifierRequired   = errors.New("supportticketing: agentIdentifier is required")
	ErrInvalidStatusTransition   = errors.New("supportticketing: invalid ticket status transition")
)

// SupportMessage is one real message in a ticket's thread.
type SupportMessage struct {
	TicketIdentifier string            `json:"ticketIdentifier"`
	AuthorType       MessageAuthorType `json:"authorType"`
	AuthorIdentifier string            `json:"authorIdentifier"`
	MessageBody      string            `json:"messageBody"`
	SentAtTime       time.Time         `json:"sentAtTime"`
}

// SupportTicket is one real customer support ticket.
type SupportTicket struct {
	TicketIdentifier        string       `json:"ticketIdentifier"`
	AccountIdentifier       string       `json:"accountIdentifier"`
	Subject                 string       `json:"subject"`
	Status                  TicketStatus `json:"status"`
	AssignedAgentIdentifier string       `json:"assignedAgentIdentifier,omitempty"`
	CreatedAtTime           time.Time    `json:"createdAtTime"`
	LastUpdatedAtTime       time.Time    `json:"lastUpdatedAtTime"`
}

// Registry is a real, mutex-guarded, in-memory store of every support
// ticket and its message thread.
type Registry struct {
	mutexGuardingState sync.RWMutex
	ticketsById        map[string]SupportTicket
	messagesByTicketId map[string][]SupportMessage
	nextTicketSequence uint64
}

func NewRegistry() *Registry {
	return &Registry{
		ticketsById:        make(map[string]SupportTicket),
		messagesByTicketId: make(map[string][]SupportMessage),
	}
}

// CreateTicket opens a brand-new ticket in TicketStatusOpen, seeded with
// the customer's own first message so every ticket's thread always has
// at least one entry — there's no such thing as a ticket with zero
// messages.
func (registry *Registry) CreateTicket(accountIdentifier string, subject string, initialMessageBody string, now time.Time) (SupportTicket, error) {
	if accountIdentifier == "" {
		return SupportTicket{}, ErrAccountIdentifierRequired
	}
	if subject == "" {
		return SupportTicket{}, ErrSubjectRequired
	}
	if initialMessageBody == "" {
		return SupportTicket{}, ErrInitialMessageRequired
	}

	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	registry.nextTicketSequence++
	ticketIdentifier := fmt.Sprintf("ticket-%06d", registry.nextTicketSequence)

	ticket := SupportTicket{
		TicketIdentifier:  ticketIdentifier,
		AccountIdentifier: accountIdentifier,
		Subject:           subject,
		Status:            TicketStatusOpen,
		CreatedAtTime:     now,
		LastUpdatedAtTime: now,
	}
	registry.ticketsById[ticketIdentifier] = ticket
	registry.messagesByTicketId[ticketIdentifier] = []SupportMessage{{
		TicketIdentifier: ticketIdentifier,
		AuthorType:       MessageAuthorTypeCustomer,
		AuthorIdentifier: accountIdentifier,
		MessageBody:      initialMessageBody,
		SentAtTime:       now,
	}}

	return ticket, nil
}

// AddCustomerMessage appends a real customer follow-up message to an
// existing ticket's thread. A message on a resolved ticket automatically
// re-opens it to in-progress — a customer following up means the issue
// was not actually resolved for them. A closed ticket rejects further
// messages: FEATURES.md's lifecycle treats "closed" as genuinely
// terminal (a customer with a new problem opens a new ticket).
func (registry *Registry) AddCustomerMessage(ticketIdentifier string, accountIdentifier string, messageBody string, now time.Time) (SupportMessage, error) {
	if messageBody == "" {
		return SupportMessage{}, ErrMessageBodyRequired
	}

	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	ticket, exists := registry.ticketsById[ticketIdentifier]
	if !exists {
		return SupportMessage{}, ErrTicketNotFound
	}
	if ticket.AccountIdentifier != accountIdentifier {
		return SupportMessage{}, ErrNotTicketOwner
	}
	if ticket.Status == TicketStatusClosed {
		return SupportMessage{}, ErrTicketAlreadyClosed
	}

	if ticket.Status == TicketStatusResolved {
		ticket.Status = TicketStatusInProgress
	}
	ticket.LastUpdatedAtTime = now
	registry.ticketsById[ticketIdentifier] = ticket

	message := SupportMessage{
		TicketIdentifier: ticketIdentifier,
		AuthorType:       MessageAuthorTypeCustomer,
		AuthorIdentifier: accountIdentifier,
		MessageBody:      messageBody,
		SentAtTime:       now,
	}
	registry.messagesByTicketId[ticketIdentifier] = append(registry.messagesByTicketId[ticketIdentifier], message)
	return message, nil
}

// AddAgentReply appends a real support-agent reply to a ticket's thread.
// A reply to an open ticket moves it to in-progress — an agent replying
// is the real, observable signal that work on the ticket has started.
// Replying does not require prior assignment (a team-inbox model where
// any agent can jump in), but if the ticket has no assigned agent yet,
// the replying agent is auto-assigned — mirrors how real helpdesk tools
// (Zendesk, Freshdesk) assign-on-first-reply.
func (registry *Registry) AddAgentReply(ticketIdentifier string, agentIdentifier string, messageBody string, now time.Time) (SupportMessage, error) {
	if agentIdentifier == "" {
		return SupportMessage{}, ErrAgentIdentifierRequired
	}
	if messageBody == "" {
		return SupportMessage{}, ErrMessageBodyRequired
	}

	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	ticket, exists := registry.ticketsById[ticketIdentifier]
	if !exists {
		return SupportMessage{}, ErrTicketNotFound
	}
	if ticket.Status == TicketStatusClosed {
		return SupportMessage{}, ErrTicketAlreadyClosed
	}

	if ticket.Status == TicketStatusOpen {
		ticket.Status = TicketStatusInProgress
	}
	if ticket.AssignedAgentIdentifier == "" {
		ticket.AssignedAgentIdentifier = agentIdentifier
	}
	ticket.LastUpdatedAtTime = now
	registry.ticketsById[ticketIdentifier] = ticket

	message := SupportMessage{
		TicketIdentifier: ticketIdentifier,
		AuthorType:       MessageAuthorTypeAgent,
		AuthorIdentifier: agentIdentifier,
		MessageBody:      messageBody,
		SentAtTime:       now,
	}
	registry.messagesByTicketId[ticketIdentifier] = append(registry.messagesByTicketId[ticketIdentifier], message)
	return message, nil
}

// AssignAgent explicitly (re)assigns a ticket to a support agent,
// independent of AddAgentReply's auto-assign-on-first-reply — e.g. a
// team lead routing a ticket before anyone has replied yet.
func (registry *Registry) AssignAgent(ticketIdentifier string, agentIdentifier string, now time.Time) (SupportTicket, error) {
	if agentIdentifier == "" {
		return SupportTicket{}, ErrAgentIdentifierRequired
	}

	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	ticket, exists := registry.ticketsById[ticketIdentifier]
	if !exists {
		return SupportTicket{}, ErrTicketNotFound
	}
	if ticket.Status == TicketStatusClosed {
		return SupportTicket{}, ErrTicketAlreadyClosed
	}

	ticket.AssignedAgentIdentifier = agentIdentifier
	ticket.LastUpdatedAtTime = now
	registry.ticketsById[ticketIdentifier] = ticket
	return ticket, nil
}

// allowedStatusTransitions is the real, enforced lifecycle graph:
// open -> in-progress -> resolved -> closed, with resolved able to fall
// back to in-progress (customer follow-up, handled automatically by
// AddCustomerMessage above, but also allowed here as an explicit agent
// action) and open able to jump straight to resolved (an agent
// resolving a ticket without ever formally moving it to in-progress
// first — a real, common support workflow). Closed is terminal: no
// transition out of it exists in this map.
var allowedStatusTransitions = map[TicketStatus]map[TicketStatus]bool{
	TicketStatusOpen: {
		TicketStatusInProgress: true,
		TicketStatusResolved:   true,
	},
	TicketStatusInProgress: {
		TicketStatusResolved: true,
	},
	TicketStatusResolved: {
		TicketStatusInProgress: true,
		TicketStatusClosed:     true,
	},
}

// TransitionStatus applies one real, validated status transition,
// rejecting any transition not present in allowedStatusTransitions
// (including any transition out of TicketStatusClosed, which is
// terminal).
func (registry *Registry) TransitionStatus(ticketIdentifier string, newStatus TicketStatus, now time.Time) (SupportTicket, error) {
	registry.mutexGuardingState.Lock()
	defer registry.mutexGuardingState.Unlock()

	ticket, exists := registry.ticketsById[ticketIdentifier]
	if !exists {
		return SupportTicket{}, ErrTicketNotFound
	}

	if !allowedStatusTransitions[ticket.Status][newStatus] {
		return SupportTicket{}, fmt.Errorf("%w: %s -> %s", ErrInvalidStatusTransition, ticket.Status, newStatus)
	}

	ticket.Status = newStatus
	ticket.LastUpdatedAtTime = now
	registry.ticketsById[ticketIdentifier] = ticket
	return ticket, nil
}

func (registry *Registry) GetTicket(ticketIdentifier string) (SupportTicket, bool) {
	registry.mutexGuardingState.RLock()
	defer registry.mutexGuardingState.RUnlock()

	ticket, exists := registry.ticketsById[ticketIdentifier]
	return ticket, exists
}

// MessageThread returns a copy of a ticket's full message thread,
// oldest first — customer messages and agent replies interleaved in
// the real order they happened.
func (registry *Registry) MessageThread(ticketIdentifier string) ([]SupportMessage, error) {
	registry.mutexGuardingState.RLock()
	defer registry.mutexGuardingState.RUnlock()

	if _, exists := registry.ticketsById[ticketIdentifier]; !exists {
		return nil, ErrTicketNotFound
	}
	threadCopy := make([]SupportMessage, len(registry.messagesByTicketId[ticketIdentifier]))
	copy(threadCopy, registry.messagesByTicketId[ticketIdentifier])
	return threadCopy, nil
}

// TicketsForAccount answers "my tickets" for an account holder.
func (registry *Registry) TicketsForAccount(accountIdentifier string) []SupportTicket {
	registry.mutexGuardingState.RLock()
	defer registry.mutexGuardingState.RUnlock()

	var matching []SupportTicket
	for _, ticket := range registry.ticketsById {
		if ticket.AccountIdentifier == accountIdentifier {
			matching = append(matching, ticket)
		}
	}
	return matching
}

// TicketsForAgent answers "my queue" for a support agent — every ticket
// currently assigned to them, any status.
func (registry *Registry) TicketsForAgent(agentIdentifier string) []SupportTicket {
	registry.mutexGuardingState.RLock()
	defer registry.mutexGuardingState.RUnlock()

	var matching []SupportTicket
	for _, ticket := range registry.ticketsById {
		if ticket.AssignedAgentIdentifier == agentIdentifier {
			matching = append(matching, ticket)
		}
	}
	return matching
}

// UnassignedOpenTickets answers the real triage queue support staff work
// from: every ticket with no assigned agent yet that isn't already
// closed.
func (registry *Registry) UnassignedOpenTickets() []SupportTicket {
	registry.mutexGuardingState.RLock()
	defer registry.mutexGuardingState.RUnlock()

	var matching []SupportTicket
	for _, ticket := range registry.ticketsById {
		if ticket.AssignedAgentIdentifier == "" && ticket.Status != TicketStatusClosed {
			matching = append(matching, ticket)
		}
	}
	return matching
}

// AllTickets returns every ticket regardless of status/assignment — the
// full staff-facing view.
func (registry *Registry) AllTickets() []SupportTicket {
	registry.mutexGuardingState.RLock()
	defer registry.mutexGuardingState.RUnlock()

	all := make([]SupportTicket, 0, len(registry.ticketsById))
	for _, ticket := range registry.ticketsById {
		all = append(all, ticket)
	}
	return all
}
