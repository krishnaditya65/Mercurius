package matchingengineclient

import (
	"bufio"
	"net"
	"testing"
)

// startFakeMatchingEngineServer spins up a real TCP listener that reads
// one line and writes back a fixed canned response — enough to verify
// MatchingEngineClient's wire behavior without needing the actual Rust
// binary running. Returns the address to dial and a stop function.
func startFakeMatchingEngineServer(t *testing.T, cannedResponseLine string) string {
	t.Helper()

	listener, listenError := net.Listen("tcp", "127.0.0.1:0")
	if listenError != nil {
		t.Fatalf("failed to start fake matching-engine listener: %v", listenError)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		connection, acceptError := listener.Accept()
		if acceptError != nil {
			return
		}
		defer connection.Close()

		_, _ = bufio.NewReader(connection).ReadString('\n')
		_, _ = connection.Write([]byte(cannedResponseLine + "\n"))
	}()

	return listener.Addr().String()
}

func TestSuccessfulOrderSubmissionParsesTradeExecutionEvents(t *testing.T) {
	fakeServerAddress := startFakeMatchingEngineServer(
		t,
		`{"tradeExecutionEvents":[{"buyingClientAccountId":"acct-001","sellingClientAccountId":"acct-002","executedPriceInMinorUnits":10000,"executedQuantity":4}],"errorMessage":null}`,
	)

	client := NewMatchingEngineClient(fakeServerAddress)
	response, submitError := client.SubmitOrderAndAwaitMatchResult(OrderSubmissionWireRequest{
		ClientAccountIdentifier: "acct-001",
		InstrumentSymbol:        "DEMO-EQ",
		OrderSideIsBuyNotSell:   true,
		LimitPriceInMinorUnits:  10_000,
		OrderQuantity:           10,
	})

	if submitError != nil {
		t.Fatalf("expected successful submission, got error: %v", submitError)
	}
	if len(response.TradeExecutionEvents) != 1 {
		t.Fatalf("expected 1 trade execution event, got %d", len(response.TradeExecutionEvents))
	}
	if response.TradeExecutionEvents[0].ExecutedQuantity != 4 {
		t.Fatalf("expected executed quantity 4, got %d", response.TradeExecutionEvents[0].ExecutedQuantity)
	}
	if response.ErrorMessage != nil {
		t.Fatalf("expected nil ErrorMessage, got %q", *response.ErrorMessage)
	}
}

func TestBusinessLevelRejectionIsNotReturnedAsAGoError(t *testing.T) {
	fakeServerAddress := startFakeMatchingEngineServer(
		t,
		`{"tradeExecutionEvents":[],"errorMessage":"matching-engine (skeleton) only trades DEMO-EQ, got WRONG"}`,
	)

	client := NewMatchingEngineClient(fakeServerAddress)
	response, submitError := client.SubmitOrderAndAwaitMatchResult(OrderSubmissionWireRequest{
		ClientAccountIdentifier: "acct-001",
		InstrumentSymbol:        "WRONG",
		OrderSideIsBuyNotSell:   true,
		LimitPriceInMinorUnits:  100,
		OrderQuantity:           1,
	})

	if submitError != nil {
		t.Fatalf("a business-level rejection should not surface as a Go error, got: %v", submitError)
	}
	if response.ErrorMessage == nil {
		t.Fatal("expected a populated ErrorMessage")
	}
}

func TestSuccessfulSubmissionCarriesAssignedOrderSequenceNumber(t *testing.T) {
	fakeServerAddress := startFakeMatchingEngineServer(
		t,
		`{"tradeExecutionEvents":[],"errorMessage":null,"assignedOrderSequenceNumber":7}`,
	)

	client := NewMatchingEngineClient(fakeServerAddress)
	response, submitError := client.SubmitOrderAndAwaitMatchResult(OrderSubmissionWireRequest{
		ClientAccountIdentifier: "acct-001",
		InstrumentSymbol:        "DEMO-EQ",
		OrderSideIsBuyNotSell:   true,
		LimitPriceInMinorUnits:  10_000,
		OrderQuantity:           5,
	})

	if submitError != nil {
		t.Fatalf("expected successful submission, got error: %v", submitError)
	}
	if response.AssignedOrderSequenceNumber == nil || *response.AssignedOrderSequenceNumber != 7 {
		t.Fatalf("expected AssignedOrderSequenceNumber=7, got %v", response.AssignedOrderSequenceNumber)
	}
}

func TestCancelOrderAndAwaitResultParsesWasOrderCancelled(t *testing.T) {
	fakeServerAddress := startFakeMatchingEngineServer(
		t,
		`{"tradeExecutionEvents":[],"errorMessage":null,"wasOrderCancelled":true}`,
	)

	client := NewMatchingEngineClient(fakeServerAddress)
	response, cancelError := client.CancelOrderAndAwaitResult("DEMO-EQ", 7)

	if cancelError != nil {
		t.Fatalf("expected successful cancel round-trip, got error: %v", cancelError)
	}
	if response.WasOrderCancelled == nil || !*response.WasOrderCancelled {
		t.Fatalf("expected WasOrderCancelled=true, got %v", response.WasOrderCancelled)
	}
}

func TestQueryOrderStatusAndAwaitResultParsesRestingLimitStatus(t *testing.T) {
	fakeServerAddress := startFakeMatchingEngineServer(
		t,
		`{"tradeExecutionEvents":[],"errorMessage":null,"orderStatus":"RESTING_LIMIT","orderStatusSideIsBuyNotSell":true,"orderStatusPriceInMinorUnits":100,"orderStatusQuantity":3}`,
	)

	client := NewMatchingEngineClient(fakeServerAddress)
	response, queryError := client.QueryOrderStatusAndAwaitResult("DEMO-EQ", 1)

	if queryError != nil {
		t.Fatalf("expected successful status query round-trip, got error: %v", queryError)
	}
	if response.OrderStatus == nil || *response.OrderStatus != "RESTING_LIMIT" {
		t.Fatalf("expected orderStatus=RESTING_LIMIT, got %v", response.OrderStatus)
	}
	if response.OrderStatusQuantity == nil || *response.OrderStatusQuantity != 3 {
		t.Fatalf("expected orderStatusQuantity=3, got %v", response.OrderStatusQuantity)
	}
}

func TestQueryOrderStatusAndAwaitResultParsesNotFoundStatus(t *testing.T) {
	fakeServerAddress := startFakeMatchingEngineServer(
		t,
		`{"tradeExecutionEvents":[],"errorMessage":null,"orderStatus":"NOT_FOUND"}`,
	)

	client := NewMatchingEngineClient(fakeServerAddress)
	response, queryError := client.QueryOrderStatusAndAwaitResult("DEMO-EQ", 999)

	if queryError != nil {
		t.Fatalf("expected successful status query round-trip, got error: %v", queryError)
	}
	if response.OrderStatus == nil || *response.OrderStatus != "NOT_FOUND" {
		t.Fatalf("expected orderStatus=NOT_FOUND, got %v", response.OrderStatus)
	}
}

func TestUnreachableMatchingEngineReturnsAnError(t *testing.T) {
	// Port 1 is reserved and never listening — a reliable way to force a
	// connection failure without relying on timing/flakiness.
	client := NewMatchingEngineClient("127.0.0.1:1")

	_, submitError := client.SubmitOrderAndAwaitMatchResult(OrderSubmissionWireRequest{
		ClientAccountIdentifier: "acct-001",
		InstrumentSymbol:        "DEMO-EQ",
		OrderSideIsBuyNotSell:   true,
		LimitPriceInMinorUnits:  100,
		OrderQuantity:           1,
	})

	if submitError == nil {
		t.Fatal("expected an error when the matching engine is unreachable")
	}
}
