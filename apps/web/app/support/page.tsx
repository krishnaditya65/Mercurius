"use client";

// Mercurius / web — In-app support chat / ticketing (FEATURES.md §14, item
// 1), wired against backoffice's real `internal/supportticketing`
// endpoints on :8084:
//
//   POST /support/tickets/create           {accountIdentifier, subject, initialMessageBody}
//   POST /support/tickets/agent-reply      {ticketIdentifier, agentIdentifier, messageBody}
//   POST /support/tickets/customer-message {ticketIdentifier, accountIdentifier, messageBody}
//   POST /support/tickets/status           {ticketIdentifier, newStatus}
//   GET  /support/tickets/thread?ticketId=...
//   GET  /support/tickets/by-account?accountId=...
//   GET  /support/tickets/by-agent?agentId=...
//   GET  /support/tickets/queue[?all=true]
//
// A real, enforced status lifecycle (open -> in-progress -> resolved ->
// closed, with a customer follow-up automatically reopening a resolved
// ticket) and real per-ticket message threads — see backoffice's README
// "In-app support chat / ticketing" section for the full contract this
// page was verified against (curl'd live against a real running
// backoffice on :8084 while building this page).
//
// Honest gap (inherited from the backend): in-memory storage (a restart
// loses every ticket), no auth/RBAC, no real-time push — this page polls
// the thread view on demand rather than opening any socket.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useState } from "react";
import Link from "next/link";

const backofficeBaseUrl = process.env.NEXT_PUBLIC_BACKOFFICE_BASE_URL ?? "http://localhost:8084";

type SupportTicketSummary = {
  ticketIdentifier: string;
  accountIdentifier: string;
  subject: string;
  status: string;
  assignedAgentIdentifier?: string;
  createdAtTime: string;
  lastUpdatedAtTime: string;
};

type SupportTicketMessage = {
  ticketIdentifier: string;
  authorType: "customer" | "agent" | string;
  authorIdentifier: string;
  messageBody: string;
  sentAtTime: string;
};

export default function SupportTicketingPage() {
  const [accountIdentifier, setAccountIdentifier] = useState("acct-001");
  const [myTickets, setMyTickets] = useState<SupportTicketSummary[]>([]);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  const [newTicketSubject, setNewTicketSubject] = useState("Withdrawal stuck");
  const [newTicketInitialMessage, setNewTicketInitialMessage] = useState(
    "My withdrawal has not landed in 2 days."
  );
  const [isCreatingTicket, setIsCreatingTicket] = useState(false);
  const [createStatusMessage, setCreateStatusMessage] = useState<string | null>(null);

  const [selectedTicketIdentifier, setSelectedTicketIdentifier] = useState<string | null>(null);
  const [threadMessages, setThreadMessages] = useState<SupportTicketMessage[]>([]);
  const [isLoadingThread, setIsLoadingThread] = useState(false);

  const [customerReplyBody, setCustomerReplyBody] = useState("");
  const [isSendingCustomerReply, setIsSendingCustomerReply] = useState(false);

  const [agentIdentifier, setAgentIdentifier] = useState("agent-priya");
  const [agentReplyBody, setAgentReplyBody] = useState("");
  const [isSendingAgentReply, setIsSendingAgentReply] = useState(false);

  const [agentTicketQueue, setAgentTicketQueue] = useState<SupportTicketSummary[]>([]);
  const [isLoadingQueue, setIsLoadingQueue] = useState(false);

  async function refreshMyTickets() {
    setErrorMessage(null);
    try {
      const httpResponse = await fetch(
        `${backofficeBaseUrl}/support/tickets/by-account?accountId=${encodeURIComponent(accountIdentifier)}`
      );
      if (!httpResponse.ok) throw new Error(`HTTP ${httpResponse.status}`);
      const parsed: { accountIdentifier: string; tickets: SupportTicketSummary[] } = await httpResponse.json();
      setMyTickets(parsed.tickets ?? []);
    } catch (thrownError) {
      setErrorMessage(
        thrownError instanceof Error
          ? `Couldn't reach backoffice: ${thrownError.message}. Is it running on ${backofficeBaseUrl}?`
          : "Unknown error fetching tickets."
      );
    }
  }

  async function refreshThread(ticketIdentifier: string) {
    setSelectedTicketIdentifier(ticketIdentifier);
    setIsLoadingThread(true);
    try {
      const httpResponse = await fetch(
        `${backofficeBaseUrl}/support/tickets/thread?ticketId=${encodeURIComponent(ticketIdentifier)}`
      );
      if (!httpResponse.ok) throw new Error(`HTTP ${httpResponse.status}`);
      const parsed: { ticketIdentifier: string; messages: SupportTicketMessage[] } = await httpResponse.json();
      setThreadMessages(parsed.messages ?? []);
    } catch (thrownError) {
      setErrorMessage(thrownError instanceof Error ? thrownError.message : "Failed to load thread.");
    } finally {
      setIsLoadingThread(false);
    }
  }

  async function createTicket() {
    setIsCreatingTicket(true);
    setCreateStatusMessage(null);
    try {
      const httpResponse = await fetch(`${backofficeBaseUrl}/support/tickets/create`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          accountIdentifier,
          subject: newTicketSubject,
          initialMessageBody: newTicketInitialMessage,
        }),
      });
      const bodyText = await httpResponse.text();
      if (!httpResponse.ok) throw new Error(bodyText || `HTTP ${httpResponse.status}`);
      const parsed: SupportTicketSummary = JSON.parse(bodyText);
      setCreateStatusMessage(`Created ${parsed.ticketIdentifier} (status: ${parsed.status}).`);
      await refreshMyTickets();
    } catch (thrownError) {
      setCreateStatusMessage(thrownError instanceof Error ? `Failed: ${thrownError.message}` : "Unknown error creating ticket.");
    } finally {
      setIsCreatingTicket(false);
    }
  }

  async function sendCustomerReply() {
    if (!selectedTicketIdentifier || !customerReplyBody.trim()) return;
    setIsSendingCustomerReply(true);
    try {
      const httpResponse = await fetch(`${backofficeBaseUrl}/support/tickets/customer-message`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          ticketIdentifier: selectedTicketIdentifier,
          accountIdentifier,
          messageBody: customerReplyBody,
        }),
      });
      if (!httpResponse.ok) throw new Error(`HTTP ${httpResponse.status}`);
      setCustomerReplyBody("");
      await Promise.all([refreshThread(selectedTicketIdentifier), refreshMyTickets()]);
    } catch (thrownError) {
      setErrorMessage(thrownError instanceof Error ? thrownError.message : "Failed to send message.");
    } finally {
      setIsSendingCustomerReply(false);
    }
  }

  async function sendAgentReply() {
    if (!selectedTicketIdentifier || !agentReplyBody.trim()) return;
    setIsSendingAgentReply(true);
    try {
      const httpResponse = await fetch(`${backofficeBaseUrl}/support/tickets/agent-reply`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          ticketIdentifier: selectedTicketIdentifier,
          agentIdentifier,
          messageBody: agentReplyBody,
        }),
      });
      if (!httpResponse.ok) throw new Error(`HTTP ${httpResponse.status}`);
      setAgentReplyBody("");
      await Promise.all([refreshThread(selectedTicketIdentifier), refreshAgentQueue()]);
    } catch (thrownError) {
      setErrorMessage(thrownError instanceof Error ? thrownError.message : "Failed to send agent reply.");
    } finally {
      setIsSendingAgentReply(false);
    }
  }

  async function setTicketStatus(newStatus: string) {
    if (!selectedTicketIdentifier) return;
    try {
      const httpResponse = await fetch(`${backofficeBaseUrl}/support/tickets/status`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ticketIdentifier: selectedTicketIdentifier, newStatus }),
      });
      const bodyText = await httpResponse.text();
      if (!httpResponse.ok) throw new Error(bodyText || `HTTP ${httpResponse.status}`);
      await Promise.all([refreshThread(selectedTicketIdentifier), refreshMyTickets(), refreshAgentQueue()]);
    } catch (thrownError) {
      setErrorMessage(thrownError instanceof Error ? thrownError.message : "Failed to update ticket status.");
    }
  }

  async function refreshAgentQueue() {
    setIsLoadingQueue(true);
    try {
      const httpResponse = await fetch(`${backofficeBaseUrl}/support/tickets/queue?all=true`);
      if (!httpResponse.ok) throw new Error(`HTTP ${httpResponse.status}`);
      const parsed: { tickets: SupportTicketSummary[] } = await httpResponse.json();
      setAgentTicketQueue(parsed.tickets ?? []);
    } catch (thrownError) {
      setErrorMessage(thrownError instanceof Error ? thrownError.message : "Failed to load agent queue.");
    } finally {
      setIsLoadingQueue(false);
    }
  }

  return (
    <main className="mx-auto flex max-w-3xl flex-col gap-8 p-8 font-sans">
      <div>
        <Link href="/" className="text-sm text-neutral-500 underline">
          ← Back to dashboard
        </Link>
        <h1 className="text-xl font-semibold">Support tickets</h1>
        <p className="text-sm text-neutral-500">
          Backed by backoffice&apos;s real <code>internal/supportticketing</code> package on :8084 — real ticket
          lifecycle, real per-ticket threads, real agent assignment.
        </p>
      </div>

      {errorMessage && <p className="text-sm text-red-600">{errorMessage}</p>}

      <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
        <h2 className="text-lg font-medium">My tickets</h2>
        <label className="flex flex-col gap-1 text-sm">
          Account
          <div className="flex gap-2">
            <input
              className="w-48 rounded border px-3 py-2"
              value={accountIdentifier}
              onChange={(changeEvent) => setAccountIdentifier(changeEvent.target.value)}
            />
            <button type="button" className="rounded border px-3 py-2 text-sm" onClick={refreshMyTickets}>
              Load my tickets
            </button>
          </div>
        </label>

        {myTickets.length === 0 ? (
          <p className="text-sm text-neutral-500">No tickets loaded yet.</p>
        ) : (
          <ul className="flex flex-col gap-2">
            {myTickets.map((ticket) => (
              <li key={ticket.ticketIdentifier}>
                <button
                  type="button"
                  className={`w-full rounded border px-3 py-2 text-left text-sm ${
                    selectedTicketIdentifier === ticket.ticketIdentifier ? "border-black" : "border-neutral-200"
                  }`}
                  onClick={() => refreshThread(ticket.ticketIdentifier)}
                >
                  <span className="font-medium">{ticket.ticketIdentifier}</span> — {ticket.subject}{" "}
                  <span className="text-neutral-500">({ticket.status}{ticket.assignedAgentIdentifier ? `, ${ticket.assignedAgentIdentifier}` : ""})</span>
                </button>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
        <h2 className="text-lg font-medium">Open a new ticket</h2>
        <label className="flex flex-col gap-1 text-sm">
          Subject
          <input
            className="rounded border px-3 py-2"
            value={newTicketSubject}
            onChange={(changeEvent) => setNewTicketSubject(changeEvent.target.value)}
          />
        </label>
        <label className="flex flex-col gap-1 text-sm">
          Message
          <textarea
            className="rounded border px-3 py-2"
            rows={3}
            value={newTicketInitialMessage}
            onChange={(changeEvent) => setNewTicketInitialMessage(changeEvent.target.value)}
          />
        </label>
        <button
          type="button"
          disabled={isCreatingTicket}
          onClick={createTicket}
          className="self-start rounded bg-black px-4 py-2 text-sm text-white disabled:opacity-50"
        >
          {isCreatingTicket ? "Creating…" : "Create ticket"}
        </button>
        {createStatusMessage && <p className="text-sm text-neutral-600">{createStatusMessage}</p>}
      </section>

      {selectedTicketIdentifier && (
        <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
          <h2 className="text-lg font-medium">Thread: {selectedTicketIdentifier}</h2>
          {isLoadingThread ? (
            <p className="text-sm text-neutral-500">Loading…</p>
          ) : (
            <ul className="flex flex-col gap-2">
              {threadMessages.map((message, messageIndex) => (
                <li
                  key={messageIndex}
                  className={`rounded border p-2 text-sm ${
                    message.authorType === "agent" ? "border-blue-200 bg-blue-50" : "border-neutral-200"
                  }`}
                >
                  <p className="text-xs text-neutral-500">
                    {message.authorType} · {message.authorIdentifier} · {new Date(message.sentAtTime).toLocaleString()}
                  </p>
                  <p>{message.messageBody}</p>
                </li>
              ))}
            </ul>
          )}

          <div className="flex gap-2 text-sm">
            <button type="button" className="rounded border px-3 py-2" onClick={() => setTicketStatus("resolved")}>
              Mark resolved
            </button>
            <button type="button" className="rounded border px-3 py-2" onClick={() => setTicketStatus("closed")}>
              Close
            </button>
            <button type="button" className="rounded border px-3 py-2" onClick={() => setTicketStatus("in-progress")}>
              Reopen (in-progress)
            </button>
          </div>

          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div className="flex flex-col gap-2 rounded border border-neutral-100 p-3">
              <p className="text-sm font-medium">Reply as customer ({accountIdentifier})</p>
              <textarea
                className="rounded border px-3 py-2 text-sm"
                rows={2}
                value={customerReplyBody}
                onChange={(changeEvent) => setCustomerReplyBody(changeEvent.target.value)}
              />
              <button
                type="button"
                disabled={isSendingCustomerReply}
                onClick={sendCustomerReply}
                className="self-start rounded border px-3 py-1.5 text-sm disabled:opacity-50"
              >
                {isSendingCustomerReply ? "Sending…" : "Send customer message"}
              </button>
            </div>

            <div className="flex flex-col gap-2 rounded border border-neutral-100 p-3">
              <p className="text-sm font-medium">Reply as agent</p>
              <input
                className="rounded border px-3 py-2 text-sm"
                value={agentIdentifier}
                onChange={(changeEvent) => setAgentIdentifier(changeEvent.target.value)}
              />
              <textarea
                className="rounded border px-3 py-2 text-sm"
                rows={2}
                value={agentReplyBody}
                onChange={(changeEvent) => setAgentReplyBody(changeEvent.target.value)}
              />
              <button
                type="button"
                disabled={isSendingAgentReply}
                onClick={sendAgentReply}
                className="self-start rounded border px-3 py-1.5 text-sm disabled:opacity-50"
              >
                {isSendingAgentReply ? "Sending…" : "Send agent reply"}
              </button>
            </div>
          </div>
        </section>
      )}

      <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
        <div className="flex items-center justify-between">
          <h2 className="text-lg font-medium">Support staff: full ticket queue</h2>
          <button type="button" className="rounded border px-3 py-2 text-sm" onClick={refreshAgentQueue}>
            {isLoadingQueue ? "Loading…" : "Refresh queue"}
          </button>
        </div>
        {agentTicketQueue.length === 0 ? (
          <p className="text-sm text-neutral-500">No tickets loaded — click refresh.</p>
        ) : (
          <ul className="flex flex-col gap-2">
            {agentTicketQueue.map((ticket) => (
              <li key={ticket.ticketIdentifier}>
                <button
                  type="button"
                  className="w-full rounded border border-neutral-200 px-3 py-2 text-left text-sm"
                  onClick={() => refreshThread(ticket.ticketIdentifier)}
                >
                  <span className="font-medium">{ticket.ticketIdentifier}</span> — {ticket.subject}{" "}
                  <span className="text-neutral-500">
                    ({ticket.status}, account {ticket.accountIdentifier}
                    {ticket.assignedAgentIdentifier ? `, assigned ${ticket.assignedAgentIdentifier}` : ", unassigned"})
                  </span>
                </button>
              </li>
            ))}
          </ul>
        )}
      </section>
    </main>
  );
}
