package dmagateway

import (
	"strings"
	"testing"

	"mercurius/omsgateway/internal/orders"
)

// fakeSubmitOrder is a deterministic, network-free stand-in for
// processOrderSubmission — tests check that HandleLine calls it with the
// right converted request and correctly reflects its (accepted/rejected)
// response back into an EXECUTION_REPORT.
func fakeSubmitOrder(shouldAccept bool, rejectionReason string) (OrderSubmitFunc, *[]orders.OrderSubmissionRequest) {
	var captured []orders.OrderSubmissionRequest
	fn := func(request orders.OrderSubmissionRequest) orders.OrderAcknowledgementResponse {
		captured = append(captured, request)
		if !shouldAccept {
			return orders.OrderAcknowledgementResponse{
				WasOrderAccepted:             false,
				HumanReadableRejectionReason: rejectionReason,
			}
		}
		return orders.OrderAcknowledgementResponse{
			WasOrderAccepted:             true,
			AssignedGlobalSequenceNumber: 7,
		}
	}
	return fn, &captured
}

func TestHandleLineAcceptsLogonAtSequenceOne(t *testing.T) {
	submit, _ := fakeSubmitOrder(true, "")
	handler := NewSessionHandler(NewSessionState(), submit)

	responses, shouldClose := handler.HandleLine("MsgType=LOGON|MsgSeqNum=1|SenderCompID=CLIENT1|TargetCompID=MERCURIUS")
	if shouldClose {
		t.Fatal("logon should not close the connection")
	}
	if len(responses) != 1 || !strings.Contains(responses[0], "MsgType=LOGON") || !strings.Contains(responses[0], "Status=ACCEPTED") {
		t.Fatalf("unexpected logon response: %v", responses)
	}
	if !handler.state.isLoggedOn {
		t.Error("expected session to be marked logged on")
	}
}

func TestHandleLineRejectsLogonWithWrongInitialSequence(t *testing.T) {
	submit, _ := fakeSubmitOrder(true, "")
	handler := NewSessionHandler(NewSessionState(), submit)

	responses, shouldClose := handler.HandleLine("MsgType=LOGON|MsgSeqNum=5|SenderCompID=CLIENT1")
	if !shouldClose {
		t.Fatal("expected connection to close on bad logon sequence")
	}
	if len(responses) != 1 || !strings.Contains(responses[0], "MsgType=REJECT") {
		t.Fatalf("expected a REJECT response, got %v", responses)
	}
	if handler.state.isLoggedOn {
		t.Error("session should not be logged on after a rejected logon")
	}
}

func TestHandleLineRejectsDoubleLogon(t *testing.T) {
	submit, _ := fakeSubmitOrder(true, "")
	handler := NewSessionHandler(NewSessionState(), submit)

	handler.HandleLine("MsgType=LOGON|MsgSeqNum=1|SenderCompID=CLIENT1")
	responses, shouldClose := handler.HandleLine("MsgType=LOGON|MsgSeqNum=2|SenderCompID=CLIENT1")
	if !shouldClose {
		t.Fatal("expected connection to close on double logon")
	}
	if !strings.Contains(responses[0], "already logged on") {
		t.Fatalf("expected 'already logged on' reject, got %v", responses)
	}
}

func TestHandleLineRejectsNewOrderSingleBeforeLogon(t *testing.T) {
	submit, captured := fakeSubmitOrder(true, "")
	handler := NewSessionHandler(NewSessionState(), submit)

	responses, shouldClose := handler.HandleLine("MsgType=NEW_ORDER_SINGLE|MsgSeqNum=1|ClOrdID=ord-1|Account=acct-001|Symbol=DEMO-EQ|Side=BUY|OrderQty=10|OrdType=LIMIT|Price=10000")
	if !shouldClose {
		t.Fatal("expected connection to close when order arrives before logon")
	}
	if !strings.Contains(responses[0], "not logged on") {
		t.Fatalf("expected 'not logged on' reject, got %v", responses)
	}
	if len(*captured) != 0 {
		t.Error("submitOrder must never be called before logon")
	}
}

func TestHandleLineAcceptsNewOrderSingleAndSubmitsRealOrder(t *testing.T) {
	submit, captured := fakeSubmitOrder(true, "")
	handler := NewSessionHandler(NewSessionState(), submit)

	handler.HandleLine("MsgType=LOGON|MsgSeqNum=1|SenderCompID=CLIENT1")
	responses, shouldClose := handler.HandleLine("MsgType=NEW_ORDER_SINGLE|MsgSeqNum=2|ClOrdID=ord-1|Account=acct-001|Symbol=DEMO-EQ|Side=BUY|OrderQty=10|OrdType=LIMIT|Price=10000")

	if shouldClose {
		t.Fatal("a successful order should not close the connection")
	}
	if len(*captured) != 1 {
		t.Fatalf("expected exactly one submitted order, got %d", len(*captured))
	}
	submittedRequest := (*captured)[0]
	if submittedRequest.ClientAccountIdentifier != "acct-001" || submittedRequest.InstrumentSymbol != "DEMO-EQ" ||
		!submittedRequest.OrderSideIsBuyNotSell || submittedRequest.OrderQuantity != 10 || submittedRequest.LimitPriceInMinorUnits != 10000 {
		t.Fatalf("unexpected converted request: %+v", submittedRequest)
	}
	if !strings.Contains(responses[0], "MsgType=EXECUTION_REPORT") || !strings.Contains(responses[0], "OrdStatus=NEW") || !strings.Contains(responses[0], "ClOrdID=ord-1") {
		t.Fatalf("unexpected execution report: %v", responses)
	}
}

func TestHandleLineSurfacesGenuineRiskRejection(t *testing.T) {
	submit, _ := fakeSubmitOrder(false, "Insufficient margin: this order needs 500 more minor units than you have available.")
	handler := NewSessionHandler(NewSessionState(), submit)

	handler.HandleLine("MsgType=LOGON|MsgSeqNum=1|SenderCompID=CLIENT1")
	responses, shouldClose := handler.HandleLine("MsgType=NEW_ORDER_SINGLE|MsgSeqNum=2|ClOrdID=ord-2|Account=acct-001|Symbol=DEMO-EQ|Side=BUY|OrderQty=999999|OrdType=LIMIT|Price=10000")

	if shouldClose {
		t.Fatal("a rejected order should not close the connection — the session itself is still healthy")
	}
	if !strings.Contains(responses[0], "OrdStatus=REJECTED") || !strings.Contains(responses[0], "Insufficient margin") {
		t.Fatalf("expected a REJECTED execution report carrying the real risk rejection reason, got %v", responses)
	}
}

func TestHandleLineRejectsOutOfSequenceMessageAfterLogon(t *testing.T) {
	submit, captured := fakeSubmitOrder(true, "")
	handler := NewSessionHandler(NewSessionState(), submit)

	handler.HandleLine("MsgType=LOGON|MsgSeqNum=1|SenderCompID=CLIENT1")
	// expected next is 2, but this jumps to 4
	responses, shouldClose := handler.HandleLine("MsgType=NEW_ORDER_SINGLE|MsgSeqNum=4|ClOrdID=ord-1|Account=acct-001|Symbol=DEMO-EQ|Side=BUY|OrderQty=10|OrdType=LIMIT|Price=10000")

	if !shouldClose {
		t.Fatal("expected connection to close on out-of-sequence message")
	}
	if !strings.Contains(responses[0], "MsgType=REJECT") || !strings.Contains(responses[0], "out-of-sequence") {
		t.Fatalf("expected an out-of-sequence REJECT, got %v", responses)
	}
	if len(*captured) != 0 {
		t.Error("submitOrder must never be called for an out-of-sequence message")
	}
}

func TestHandleLineAcceptsInOrderSequenceAcrossMultipleMessages(t *testing.T) {
	submit, captured := fakeSubmitOrder(true, "")
	handler := NewSessionHandler(NewSessionState(), submit)

	_, close1 := handler.HandleLine("MsgType=LOGON|MsgSeqNum=1|SenderCompID=CLIENT1")
	_, close2 := handler.HandleLine("MsgType=NEW_ORDER_SINGLE|MsgSeqNum=2|ClOrdID=ord-1|Account=acct-001|Symbol=DEMO-EQ|Side=BUY|OrderQty=10|OrdType=LIMIT|Price=10000")
	_, close3 := handler.HandleLine("MsgType=NEW_ORDER_SINGLE|MsgSeqNum=3|ClOrdID=ord-2|Account=acct-001|Symbol=DEMO-EQ|Side=SELL|OrderQty=5|OrdType=MARKET|Price=0")

	if close1 || close2 || close3 {
		t.Fatal("no message in this in-sequence run should close the connection")
	}
	if len(*captured) != 2 {
		t.Fatalf("expected 2 submitted orders, got %d", len(*captured))
	}
	if (*captured)[1].OrderSideIsBuyNotSell {
		t.Error("expected second order to be a SELL")
	}
	if !(*captured)[1].OrderIsMarketOrderNotLimit {
		t.Error("expected second order to be a MARKET order")
	}
}

func TestHandleLineLogoutClosesConnection(t *testing.T) {
	submit, _ := fakeSubmitOrder(true, "")
	handler := NewSessionHandler(NewSessionState(), submit)

	handler.HandleLine("MsgType=LOGON|MsgSeqNum=1|SenderCompID=CLIENT1")
	responses, shouldClose := handler.HandleLine("MsgType=LOGOUT|MsgSeqNum=2")

	if !shouldClose {
		t.Fatal("expected LOGOUT to close the connection")
	}
	if !strings.Contains(responses[0], "MsgType=LOGOUT") {
		t.Fatalf("expected a LOGOUT ack, got %v", responses)
	}
}

func TestHandleLineRejectsMalformedLine(t *testing.T) {
	submit, _ := fakeSubmitOrder(true, "")
	handler := NewSessionHandler(NewSessionState(), submit)

	responses, shouldClose := handler.HandleLine("not a valid message at all")
	if !shouldClose {
		t.Fatal("expected malformed line to close the connection")
	}
	if !strings.Contains(responses[0], "MsgType=REJECT") {
		t.Fatalf("expected a REJECT, got %v", responses)
	}
}

func TestHandleLineRejectsMissingSequenceNumber(t *testing.T) {
	submit, _ := fakeSubmitOrder(true, "")
	handler := NewSessionHandler(NewSessionState(), submit)

	responses, shouldClose := handler.HandleLine("MsgType=LOGON|SenderCompID=CLIENT1")
	if !shouldClose {
		t.Fatal("expected missing MsgSeqNum to close the connection")
	}
	if !strings.Contains(responses[0], "MsgSeqNum") {
		t.Fatalf("expected reject mentioning MsgSeqNum, got %v", responses)
	}
}

func TestHandleLineRejectsNewOrderSingleWithInvalidSide(t *testing.T) {
	submit, captured := fakeSubmitOrder(true, "")
	handler := NewSessionHandler(NewSessionState(), submit)

	handler.HandleLine("MsgType=LOGON|MsgSeqNum=1|SenderCompID=CLIENT1")
	responses, shouldClose := handler.HandleLine("MsgType=NEW_ORDER_SINGLE|MsgSeqNum=2|ClOrdID=ord-1|Account=acct-001|Symbol=DEMO-EQ|Side=SIDEWAYS|OrderQty=10|OrdType=LIMIT|Price=10000")

	if shouldClose {
		t.Fatal("a validation rejection should not close the session")
	}
	if !strings.Contains(responses[0], "OrdStatus=REJECTED") {
		t.Fatalf("expected a REJECTED execution report, got %v", responses)
	}
	if len(*captured) != 0 {
		t.Error("submitOrder must never be called for an invalid Side")
	}
}

func TestHandleLineUnknownMessageTypeIsRejectedWithoutClosing(t *testing.T) {
	submit, _ := fakeSubmitOrder(true, "")
	handler := NewSessionHandler(NewSessionState(), submit)

	handler.HandleLine("MsgType=LOGON|MsgSeqNum=1|SenderCompID=CLIENT1")
	responses, shouldClose := handler.HandleLine("MsgType=HEARTBEAT|MsgSeqNum=2")

	if shouldClose {
		t.Fatal("an unknown but in-sequence message type should not close the connection")
	}
	if !strings.Contains(responses[0], "unknown MsgType") {
		t.Fatalf("expected unknown-MsgType reject, got %v", responses)
	}
}

func TestHandleLineOutOfSequenceDoesNotAdvanceExpectedSequence(t *testing.T) {
	submit, _ := fakeSubmitOrder(true, "")
	state := NewSessionState()
	handler := NewSessionHandler(state, submit)

	handler.HandleLine("MsgType=LOGON|MsgSeqNum=1|SenderCompID=CLIENT1")
	handler.HandleLine("MsgType=NEW_ORDER_SINGLE|MsgSeqNum=99|ClOrdID=ord-1|Account=acct-001|Symbol=DEMO-EQ|Side=BUY|OrderQty=10|OrdType=LIMIT|Price=10000")

	if state.expectedNextIncomingSeqNum != 2 {
		t.Errorf("expected next incoming sequence to remain 2 after a rejected out-of-sequence message, got %d", state.expectedNextIncomingSeqNum)
	}
}
