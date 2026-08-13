package dmagateway

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"

	"mercurius/omsgateway/internal/orders"
)

// TestServerEndToEndOverRealTcpSocket proves the whole stack — TCP
// listener, line scanning, session sequencing, real order-submission
// callback — works over an actual socket, not just in-process function
// calls.
func TestServerEndToEndOverRealTcpSocket(t *testing.T) {
	var capturedRequests []orders.OrderSubmissionRequest
	submit := func(request orders.OrderSubmissionRequest) orders.OrderAcknowledgementResponse {
		capturedRequests = append(capturedRequests, request)
		return orders.OrderAcknowledgementResponse{WasOrderAccepted: true, AssignedGlobalSequenceNumber: 1}
	}

	server := NewServer("127.0.0.1:0", submit)
	listener, listenError := net.Listen("tcp", "127.0.0.1:0")
	if listenError != nil {
		t.Fatalf("failed to reserve a test port: %v", listenError)
	}
	testAddress := listener.Addr().String()
	listener.Close()
	server.listenAddress = testAddress

	go func() {
		_ = server.ListenAndServe()
	}()

	// give the listener a moment to actually bind
	var connection net.Conn
	var dialError error
	for attempt := 0; attempt < 50; attempt++ {
		connection, dialError = net.Dial("tcp", testAddress)
		if dialError == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if dialError != nil {
		t.Fatalf("failed to dial test server: %v", dialError)
	}
	defer connection.Close()

	reader := bufio.NewReader(connection)

	writeLine := func(line string) {
		if _, err := connection.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("write failed: %v", err)
		}
	}
	readLine := func() string {
		_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
		responseLine, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read failed: %v", err)
		}
		return responseLine
	}

	writeLine("MsgType=LOGON|MsgSeqNum=1|SenderCompID=CLIENT1|TargetCompID=MERCURIUS")
	logonResponse := readLine()
	if !strings.Contains(logonResponse, "Status=ACCEPTED") {
		t.Fatalf("expected LOGON ack, got %q", logonResponse)
	}

	writeLine("MsgType=NEW_ORDER_SINGLE|MsgSeqNum=2|ClOrdID=ord-1|Account=acct-001|Symbol=DEMO-EQ|Side=BUY|OrderQty=10|OrdType=LIMIT|Price=10000")
	execReport := readLine()
	if !strings.Contains(execReport, "OrdStatus=NEW") {
		t.Fatalf("expected NEW execution report, got %q", execReport)
	}

	// out-of-sequence: expected 3, send 10
	writeLine("MsgType=NEW_ORDER_SINGLE|MsgSeqNum=10|ClOrdID=ord-2|Account=acct-001|Symbol=DEMO-EQ|Side=BUY|OrderQty=10|OrdType=LIMIT|Price=10000")
	rejectResponse := readLine()
	if !strings.Contains(rejectResponse, "MsgType=REJECT") || !strings.Contains(rejectResponse, "out-of-sequence") {
		t.Fatalf("expected out-of-sequence REJECT, got %q", rejectResponse)
	}

	// connection should now be closed by the server
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	buffer := make([]byte, 16)
	if _, err := connection.Read(buffer); err == nil {
		t.Fatal("expected connection to be closed after an out-of-sequence message")
	}

	if len(capturedRequests) != 1 {
		t.Fatalf("expected exactly 1 order actually submitted (the out-of-sequence one must never reach submitOrder), got %d", len(capturedRequests))
	}
}
