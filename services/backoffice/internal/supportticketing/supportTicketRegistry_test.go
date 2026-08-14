package supportticketing

import (
	"errors"
	"testing"
	"time"
)

func TestCreateTicketSeedsThreadWithFirstMessage(t *testing.T) {
	registry := NewRegistry()
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)

	ticket, createError := registry.CreateTicket("acct-001", "Cannot withdraw funds", "My withdrawal has been stuck for 2 days.", now)
	if createError != nil {
		t.Fatalf("unexpected error: %v", createError)
	}
	if ticket.Status != TicketStatusOpen {
		t.Fatalf("expected new ticket to be open, got %s", ticket.Status)
	}
	if ticket.TicketIdentifier == "" {
		t.Fatalf("expected a generated ticket identifier")
	}

	thread, threadError := registry.MessageThread(ticket.TicketIdentifier)
	if threadError != nil {
		t.Fatalf("unexpected error fetching thread: %v", threadError)
	}
	if len(thread) != 1 {
		t.Fatalf("expected 1 seeded message, got %d", len(thread))
	}
	if thread[0].AuthorType != MessageAuthorTypeCustomer || thread[0].AuthorIdentifier != "acct-001" {
		t.Fatalf("expected seeded message to be from the customer, got %+v", thread[0])
	}
}

func TestCreateTicketValidatesRequiredFields(t *testing.T) {
	registry := NewRegistry()
	now := time.Now()

	if _, err := registry.CreateTicket("", "subject", "body", now); err != ErrAccountIdentifierRequired {
		t.Fatalf("expected ErrAccountIdentifierRequired, got %v", err)
	}
	if _, err := registry.CreateTicket("acct-001", "", "body", now); err != ErrSubjectRequired {
		t.Fatalf("expected ErrSubjectRequired, got %v", err)
	}
	if _, err := registry.CreateTicket("acct-001", "subject", "", now); err != ErrInitialMessageRequired {
		t.Fatalf("expected ErrInitialMessageRequired, got %v", err)
	}
}

func TestAgentReplyMovesOpenTicketToInProgressAndAutoAssigns(t *testing.T) {
	registry := NewRegistry()
	now := time.Now()

	ticket, _ := registry.CreateTicket("acct-001", "KYC document rejected", "Why was my PAN card rejected?", now)

	_, replyError := registry.AddAgentReply(ticket.TicketIdentifier, "agent-priya", "Looking into this now, one moment.", now.Add(time.Minute))
	if replyError != nil {
		t.Fatalf("unexpected error: %v", replyError)
	}

	updated, _ := registry.GetTicket(ticket.TicketIdentifier)
	if updated.Status != TicketStatusInProgress {
		t.Fatalf("expected in-progress after first agent reply, got %s", updated.Status)
	}
	if updated.AssignedAgentIdentifier != "agent-priya" {
		t.Fatalf("expected auto-assignment to replying agent, got %q", updated.AssignedAgentIdentifier)
	}
}

func TestCustomerMessageReopensResolvedTicket(t *testing.T) {
	registry := NewRegistry()
	now := time.Now()

	ticket, _ := registry.CreateTicket("acct-002", "Order stuck pending", "My order has been pending all day.", now)
	if _, err := registry.TransitionStatus(ticket.TicketIdentifier, TicketStatusResolved, now); err != nil {
		t.Fatalf("unexpected error resolving: %v", err)
	}

	if _, err := registry.AddCustomerMessage(ticket.TicketIdentifier, "acct-002", "This is happening again.", now.Add(time.Hour)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	reopened, _ := registry.GetTicket(ticket.TicketIdentifier)
	if reopened.Status != TicketStatusInProgress {
		t.Fatalf("expected follow-up message to reopen to in-progress, got %s", reopened.Status)
	}
}

func TestClosedTicketRejectsFurtherActivity(t *testing.T) {
	registry := NewRegistry()
	now := time.Now()

	ticket, _ := registry.CreateTicket("acct-001", "Question about margin", "How is margin computed?", now)
	if _, err := registry.TransitionStatus(ticket.TicketIdentifier, TicketStatusResolved, now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := registry.TransitionStatus(ticket.TicketIdentifier, TicketStatusClosed, now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := registry.AddCustomerMessage(ticket.TicketIdentifier, "acct-001", "one more thing", now); err != ErrTicketAlreadyClosed {
		t.Fatalf("expected ErrTicketAlreadyClosed for customer message, got %v", err)
	}
	if _, err := registry.AddAgentReply(ticket.TicketIdentifier, "agent-priya", "reply", now); err != ErrTicketAlreadyClosed {
		t.Fatalf("expected ErrTicketAlreadyClosed for agent reply, got %v", err)
	}
	if _, err := registry.TransitionStatus(ticket.TicketIdentifier, TicketStatusOpen, now); !errors.Is(err, ErrInvalidStatusTransition) {
		t.Fatalf("expected ErrInvalidStatusTransition out of closed, got %v", err)
	}
}

func TestInvalidStatusTransitionsAreRejected(t *testing.T) {
	registry := NewRegistry()
	now := time.Now()

	ticket, _ := registry.CreateTicket("acct-001", "subject", "body", now)

	// open -> closed is not a direct allowed transition (must pass through resolved).
	if _, err := registry.TransitionStatus(ticket.TicketIdentifier, TicketStatusClosed, now); !errors.Is(err, ErrInvalidStatusTransition) {
		t.Fatalf("expected ErrInvalidStatusTransition open->closed, got %v", err)
	}
}

func TestNonOwnerCannotMessageAnotherAccountsTicket(t *testing.T) {
	registry := NewRegistry()
	now := time.Now()

	ticket, _ := registry.CreateTicket("acct-001", "subject", "body", now)

	if _, err := registry.AddCustomerMessage(ticket.TicketIdentifier, "acct-999", "I want in on this ticket", now); err != ErrNotTicketOwner {
		t.Fatalf("expected ErrNotTicketOwner, got %v", err)
	}
}

func TestQueriesByAccountAgentAndUnassignedQueue(t *testing.T) {
	registry := NewRegistry()
	now := time.Now()

	ticketA, _ := registry.CreateTicket("acct-001", "issue A", "body A", now)
	ticketB, _ := registry.CreateTicket("acct-001", "issue B", "body B", now)
	ticketC, _ := registry.CreateTicket("acct-002", "issue C", "body C", now)

	if _, err := registry.AssignAgent(ticketA.TicketIdentifier, "agent-priya", now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	acct001Tickets := registry.TicketsForAccount("acct-001")
	if len(acct001Tickets) != 2 {
		t.Fatalf("expected 2 tickets for acct-001, got %d", len(acct001Tickets))
	}

	priyaQueue := registry.TicketsForAgent("agent-priya")
	if len(priyaQueue) != 1 || priyaQueue[0].TicketIdentifier != ticketA.TicketIdentifier {
		t.Fatalf("expected agent-priya's queue to contain exactly ticketA, got %+v", priyaQueue)
	}

	unassigned := registry.UnassignedOpenTickets()
	if len(unassigned) != 2 {
		t.Fatalf("expected 2 unassigned open tickets (B and C), got %d", len(unassigned))
	}

	all := registry.AllTickets()
	if len(all) != 3 {
		t.Fatalf("expected 3 total tickets, got %d", len(all))
	}
	_ = ticketB
	_ = ticketC
}

func TestGetTicketAndMessageThreadForUnknownTicketFail(t *testing.T) {
	registry := NewRegistry()

	if _, exists := registry.GetTicket("ticket-nonexistent"); exists {
		t.Fatalf("expected unknown ticket to not exist")
	}
	if _, err := registry.MessageThread("ticket-nonexistent"); err != ErrTicketNotFound {
		t.Fatalf("expected ErrTicketNotFound, got %v", err)
	}
	if _, err := registry.AddCustomerMessage("ticket-nonexistent", "acct-001", "hi", time.Now()); err != ErrTicketNotFound {
		t.Fatalf("expected ErrTicketNotFound, got %v", err)
	}
	if _, err := registry.AddAgentReply("ticket-nonexistent", "agent-x", "hi", time.Now()); err != ErrTicketNotFound {
		t.Fatalf("expected ErrTicketNotFound, got %v", err)
	}
	if _, err := registry.AssignAgent("ticket-nonexistent", "agent-x", time.Now()); err != ErrTicketNotFound {
		t.Fatalf("expected ErrTicketNotFound, got %v", err)
	}
	if _, err := registry.TransitionStatus("ticket-nonexistent", TicketStatusResolved, time.Now()); err != ErrTicketNotFound {
		t.Fatalf("expected ErrTicketNotFound, got %v", err)
	}
}

func TestMessageThreadInterleavesCustomerAndAgentInOrder(t *testing.T) {
	registry := NewRegistry()
	now := time.Now()

	ticket, _ := registry.CreateTicket("acct-001", "subject", "first message", now)
	registry.AddAgentReply(ticket.TicketIdentifier, "agent-priya", "agent reply 1", now.Add(time.Minute))
	registry.AddCustomerMessage(ticket.TicketIdentifier, "acct-001", "customer follow-up", now.Add(2*time.Minute))
	registry.AddAgentReply(ticket.TicketIdentifier, "agent-priya", "agent reply 2", now.Add(3*time.Minute))

	thread, _ := registry.MessageThread(ticket.TicketIdentifier)
	if len(thread) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(thread))
	}
	expectedAuthorTypes := []MessageAuthorType{
		MessageAuthorTypeCustomer, MessageAuthorTypeAgent, MessageAuthorTypeCustomer, MessageAuthorTypeAgent,
	}
	for i, expected := range expectedAuthorTypes {
		if thread[i].AuthorType != expected {
			t.Fatalf("message %d: expected authorType %s, got %s", i, expected, thread[i].AuthorType)
		}
	}
}
