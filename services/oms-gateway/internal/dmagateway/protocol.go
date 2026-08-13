// Package dmagateway implements FEATURES.md §3's "Direct Market Access
// (DMA) / FIX gateway for institutional clients".
//
// ==========================================================================
// LOUD, REPEATED, LOAD-BEARING WARNING: THIS IS NOT FIX-PROTOCOL-CERTIFIED.
// ==========================================================================
// This is a real, working, session-based TCP protocol that BORROWS a few
// well-known FIX (Financial Information eXchange) SESSION CONCEPTS for
// illustrative realism — a Logon handshake, monotonically increasing
// message sequence numbers per session, rejecting an out-of-sequence
// message, an order-acceptance response modeled loosely on an
// ExecutionReport, and a Logout handshake. It is NOT:
//   - FIX 4.2 or FIX 4.4 (or any other FIX version) wire-format compliant
//   - validated against, or certified by, any FIX conformance test suite
//   - using real FIX tag numbers (this uses human-readable field names
//     like "MsgType" and "ClOrdID" instead of numeric tags like 35/11 —
//     deliberately, so nobody mistakes a captured session for real FIX
//     traffic on the wire)
//   - encoding messages with FIX's actual SOH (0x01) field delimiter,
//     BeginString/BodyLength/CheckSum framing, or binary length-prefixing
//   - implementing FIX's real session-recovery behavior (ResendRequest /
//     SequenceReset / gap fill) — this gateway simply REJECTS an
//     out-of-sequence message and closes the connection; see
//     SessionHandler.HandleLine's doc comment.
//   - anything that would ever be accepted by a real exchange's or real
//     institutional counterparty's FIX engine.
//
// This exists purely to demonstrate the SHAPE of a session-based,
// sequence-number-enforcing institutional order-entry gateway, reusing
// oms-gateway's real risk-check/audit-trail/matching-engine pipeline
// underneath (see OrderSubmitFunc) — nothing more. A real DMA/FIX
// integration needs a certified FIX engine (e.g. QuickFIX or a licensed
// commercial engine), real exchange onboarding, and a full FIX
// conformance test pass before it could ever touch a real order book.
// ==========================================================================
//
// Wire format: one message per line (newline-terminated), fields
// separated by "|", each field "Name=Value" — inspired by FIX's
// tag=value structure, using names instead of numbers. Example:
//
//	MsgType=LOGON|MsgSeqNum=1|SenderCompID=CLIENT1|TargetCompID=MERCURIUS|HeartBtInt=30
package dmagateway

import (
	"errors"
	"strconv"
	"strings"
)

// Message type constants — the small, closed set this illustrative
// gateway understands. Named after (but not identical in shape to) the
// FIX messages that inspired them.
const (
	MsgTypeLogon           = "LOGON"
	MsgTypeNewOrderSingle  = "NEW_ORDER_SINGLE"
	MsgTypeExecutionReport = "EXECUTION_REPORT"
	MsgTypeLogout          = "LOGOUT"
	MsgTypeReject          = "REJECT"
)

var (
	// ErrEmptyMessage is returned when a line has no content at all.
	ErrEmptyMessage = errors.New("empty message line")

	// ErrMalformedField is returned when a "|"-separated segment isn't a
	// valid "Name=Value" pair.
	ErrMalformedField = errors.New("malformed field, expected Name=Value")

	// ErrMissingMsgType is returned when a parsed message has no
	// "MsgType" field at all.
	ErrMissingMsgType = errors.New("message missing required MsgType field")
)

// Message is one parsed protocol line: an unordered bag of named fields.
type Message struct {
	Fields map[string]string
}

// ParseMessage parses one wire line into a Message. Fields are
// "|"-separated "Name=Value" pairs; a trailing/leading empty segment
// (e.g. from a trailing "|") is silently skipped rather than treated as
// malformed, matching how a real line-oriented protocol tolerates a
// trailing delimiter.
func ParseMessage(line string) (Message, error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return Message{}, ErrEmptyMessage
	}

	segments := strings.Split(trimmed, "|")
	fields := make(map[string]string, len(segments))
	for _, segment := range segments {
		if segment == "" {
			continue
		}
		keyAndValue := strings.SplitN(segment, "=", 2)
		if len(keyAndValue) != 2 || keyAndValue[0] == "" {
			return Message{}, ErrMalformedField
		}
		fields[keyAndValue[0]] = keyAndValue[1]
	}

	if _, hasMsgType := fields["MsgType"]; !hasMsgType {
		return Message{}, ErrMissingMsgType
	}

	return Message{Fields: fields}, nil
}

// orderedField is one Name=Value pair in the deterministic order the
// caller wants it serialized — Go maps don't have a stable iteration
// order, and a deterministic wire format matters for both real client
// parsing and this package's own tests.
type orderedField struct {
	Name  string
	Value string
}

// formatMessage renders msgType, msgSeqNum, and additionalFields (in the
// order given) into one wire line, newline-terminated.
func formatMessage(msgType string, msgSeqNum uint64, additionalFields []orderedField) string {
	var builder strings.Builder
	builder.WriteString("MsgType=")
	builder.WriteString(msgType)
	builder.WriteString("|MsgSeqNum=")
	builder.WriteString(strconv.FormatUint(msgSeqNum, 10))
	for _, field := range additionalFields {
		builder.WriteString("|")
		builder.WriteString(field.Name)
		builder.WriteString("=")
		builder.WriteString(field.Value)
	}
	builder.WriteString("\n")
	return builder.String()
}
