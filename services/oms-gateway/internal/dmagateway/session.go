// See protocol.go's package doc for the full "NOT FIX-certified"
// warning — repeated here because this is the file that actually
// enforces session state and sequence numbers, the part of this package
// most likely to be mistaken for something more rigorous than it is.
package dmagateway

import (
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"

	"mercurius/omsgateway/internal/orders"
)

// OrderSubmitFunc is however the caller wants a converted order actually
// submitted — in cmd/server/main.go this is a closure around
// processOrderSubmission, so an order accepted over this TCP session runs
// through the EXACT SAME risk-check/audit-trail/matching-engine pipeline
// as an HTTP /orders/submit call. This package deliberately has no idea
// what's inside that function — it only converts a parsed
// NEW_ORDER_SINGLE message into orders.OrderSubmissionRequest and reads
// back an orders.OrderAcknowledgementResponse.
type OrderSubmitFunc func(orders.OrderSubmissionRequest) orders.OrderAcknowledgementResponse

// SessionState is the real, mutex-guarded per-connection state a genuine
// FIX-inspired session needs: whether Logon has happened yet, the next
// incoming sequence number this session will accept, and an independent
// outgoing sequence counter for this gateway's own responses.
type SessionState struct {
	mutexGuardingState sync.Mutex

	isLoggedOn                 bool
	senderCompID               string
	expectedNextIncomingSeqNum uint64
	nextOutgoingSeqNum         uint64
}

// NewSessionState returns a fresh, not-yet-logged-on session expecting
// its first incoming message to carry MsgSeqNum=1 — standard FIX-session
// convention this illustrative gateway borrows.
func NewSessionState() *SessionState {
	return &SessionState{
		expectedNextIncomingSeqNum: 1,
		nextOutgoingSeqNum:         1,
	}
}

// execIdCounter is a process-wide monotonic counter for synthesizing an
// illustrative ExecID on every EXECUTION_REPORT — not a real
// exchange-assigned execution id, just enough to make each report
// distinguishable.
var execIdCounter uint64

// SessionHandler processes one TCP connection's message lines against
// one SessionState, converting an accepted NEW_ORDER_SINGLE into a real
// order submission via submitOrder.
type SessionHandler struct {
	state       *SessionState
	submitOrder OrderSubmitFunc
}

func NewSessionHandler(state *SessionState, submitOrder OrderSubmitFunc) *SessionHandler {
	return &SessionHandler{state: state, submitOrder: submitOrder}
}

// HandleLine processes exactly one incoming wire line and returns the
// response line(s) to write back plus whether the connection should now
// be closed.
//
// Sequencing behavior — the one genuinely FIX-like piece of session
// logic here: every message (Logon included) must carry MsgSeqNum equal
// to this session's expectedNextIncomingSeqNum, or it's rejected. A real
// FIX engine, on detecting a gap, sends a ResendRequest and waits for the
// missing messages to be replayed (or a SequenceReset/gap-fill) before
// continuing the session. This illustrative gateway does NOT do that —
// it has no message store to resend from — it simply REJECTs and closes
// the connection, forcing the client to reconnect and Logon again. A
// real build needs genuine gap-fill/resend support before this could
// ever be called FIX-compliant, which — per this package's loud warning
// — it never claims to be anyway.
func (handler *SessionHandler) HandleLine(line string) (responseLines []string, shouldCloseConnection bool) {
	message, parseError := ParseMessage(line)
	if parseError != nil {
		return []string{handler.buildReject(0, fmt.Sprintf("malformed message: %v", parseError))}, true
	}

	seqNumString, hasSeqNum := message.Fields["MsgSeqNum"]
	if !hasSeqNum {
		return []string{handler.buildReject(0, "missing required MsgSeqNum field")}, true
	}
	seqNum, seqNumParseError := strconv.ParseUint(seqNumString, 10, 64)
	if seqNumParseError != nil {
		return []string{handler.buildReject(0, "MsgSeqNum must be a non-negative integer")}, true
	}

	handler.state.mutexGuardingState.Lock()
	defer handler.state.mutexGuardingState.Unlock()

	msgType := message.Fields["MsgType"]

	if msgType == MsgTypeLogon {
		if handler.state.isLoggedOn {
			return []string{handler.buildRejectLocked(seqNum, "session already logged on")}, true
		}
		if seqNum != handler.state.expectedNextIncomingSeqNum {
			return []string{handler.buildRejectLocked(seqNum, fmt.Sprintf(
				"out-of-sequence LOGON: expected MsgSeqNum=%d, got %d", handler.state.expectedNextIncomingSeqNum, seqNum,
			))}, true
		}
		handler.state.isLoggedOn = true
		handler.state.senderCompID = message.Fields["SenderCompID"]
		handler.state.expectedNextIncomingSeqNum++
		return []string{handler.buildLogonAckLocked()}, false
	}

	if !handler.state.isLoggedOn {
		return []string{handler.buildRejectLocked(seqNum, "session not logged on — send LOGON first")}, true
	}

	if seqNum != handler.state.expectedNextIncomingSeqNum {
		return []string{handler.buildRejectLocked(seqNum, fmt.Sprintf(
			"out-of-sequence message: expected MsgSeqNum=%d, got %d", handler.state.expectedNextIncomingSeqNum, seqNum,
		))}, true
	}
	handler.state.expectedNextIncomingSeqNum++

	switch msgType {
	case MsgTypeNewOrderSingle:
		return []string{handler.handleNewOrderSingleLocked(message)}, false
	case MsgTypeLogout:
		return []string{handler.buildLogoutAckLocked()}, true
	default:
		return []string{handler.buildRejectLocked(seqNum, fmt.Sprintf("unknown MsgType %q", msgType))}, false
	}
}

// handleNewOrderSingleLocked converts message into orders.OrderSubmissionRequest
// and submits it via submitOrder — the exact same pipeline an HTTP
// /orders/submit call uses (see OrderSubmitFunc's doc comment). Must be
// called with state's mutex already held.
func (handler *SessionHandler) handleNewOrderSingleLocked(message Message) string {
	clOrdID := message.Fields["ClOrdID"]

	clientAccountIdentifier := message.Fields["Account"]
	instrumentSymbol := message.Fields["Symbol"]
	if clientAccountIdentifier == "" || instrumentSymbol == "" {
		return handler.buildExecutionReportLocked(clOrdID, "REJECTED", "missing required Account or Symbol field")
	}

	side := message.Fields["Side"]
	if side != "BUY" && side != "SELL" {
		return handler.buildExecutionReportLocked(clOrdID, "REJECTED", `Side must be "BUY" or "SELL"`)
	}

	orderQuantity, quantityParseError := strconv.ParseUint(message.Fields["OrderQty"], 10, 64)
	if quantityParseError != nil || orderQuantity == 0 {
		return handler.buildExecutionReportLocked(clOrdID, "REJECTED", "OrderQty must be a positive integer")
	}

	ordType := message.Fields["OrdType"]
	isMarketOrder := ordType == "MARKET"
	if ordType != "MARKET" && ordType != "LIMIT" {
		return handler.buildExecutionReportLocked(clOrdID, "REJECTED", `OrdType must be "MARKET" or "LIMIT"`)
	}

	var limitPriceInMinorUnits int64
	if !isMarketOrder {
		price, priceParseError := strconv.ParseInt(message.Fields["Price"], 10, 64)
		if priceParseError != nil {
			return handler.buildExecutionReportLocked(clOrdID, "REJECTED", "LIMIT order requires a valid integer Price")
		}
		limitPriceInMinorUnits = price
	}

	acknowledgement := handler.submitOrder(orders.OrderSubmissionRequest{
		ClientAccountIdentifier:    clientAccountIdentifier,
		InstrumentSymbol:           instrumentSymbol,
		OrderSideIsBuyNotSell:      side == "BUY",
		OrderIsMarketOrderNotLimit: isMarketOrder,
		LimitPriceInMinorUnits:     limitPriceInMinorUnits,
		OrderQuantity:              orderQuantity,
	})

	if !acknowledgement.WasOrderAccepted {
		return handler.buildExecutionReportLocked(clOrdID, "REJECTED", acknowledgement.HumanReadableRejectionReason)
	}
	return handler.buildExecutionReportLocked(clOrdID, "NEW", "")
}

func (handler *SessionHandler) buildExecutionReportLocked(clOrdID string, ordStatus string, text string) string {
	seq := handler.nextOutgoingSeqNumLocked()
	execId := strconv.FormatUint(atomic.AddUint64(&execIdCounter, 1), 10)
	fields := []orderedField{
		{Name: "ClOrdID", Value: clOrdID},
		{Name: "ExecID", Value: execId},
		{Name: "OrdStatus", Value: ordStatus},
	}
	if text != "" {
		fields = append(fields, orderedField{Name: "Text", Value: text})
	}
	return formatMessage(MsgTypeExecutionReport, seq, fields)
}

func (handler *SessionHandler) buildLogonAckLocked() string {
	seq := handler.nextOutgoingSeqNumLocked()
	return formatMessage(MsgTypeLogon, seq, []orderedField{
		{Name: "Status", Value: "ACCEPTED"},
	})
}

func (handler *SessionHandler) buildLogoutAckLocked() string {
	seq := handler.nextOutgoingSeqNumLocked()
	return formatMessage(MsgTypeLogout, seq, []orderedField{
		{Name: "Text", Value: "session terminated"},
	})
}

// buildReject acquires the lock itself — used only from the two
// call sites in HandleLine that run BEFORE the lock is taken (a
// malformed line or an unparseable MsgSeqNum, neither of which can
// safely read state yet).
func (handler *SessionHandler) buildReject(refSeqNum uint64, reasonText string) string {
	handler.state.mutexGuardingState.Lock()
	defer handler.state.mutexGuardingState.Unlock()
	return handler.buildRejectLocked(refSeqNum, reasonText)
}

func (handler *SessionHandler) buildRejectLocked(refSeqNum uint64, reasonText string) string {
	seq := handler.nextOutgoingSeqNumLocked()
	return formatMessage(MsgTypeReject, seq, []orderedField{
		{Name: "RefSeqNum", Value: strconv.FormatUint(refSeqNum, 10)},
		{Name: "Text", Value: reasonText},
	})
}

func (handler *SessionHandler) nextOutgoingSeqNumLocked() uint64 {
	seq := handler.state.nextOutgoingSeqNum
	handler.state.nextOutgoingSeqNum++
	return seq
}
