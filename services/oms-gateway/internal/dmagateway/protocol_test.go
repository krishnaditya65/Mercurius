package dmagateway

import (
	"errors"
	"testing"
)

func TestParseMessageParsesFieldsCorrectly(t *testing.T) {
	message, err := ParseMessage("MsgType=LOGON|MsgSeqNum=1|SenderCompID=CLIENT1|TargetCompID=MERCURIUS")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if message.Fields["MsgType"] != MsgTypeLogon {
		t.Errorf("expected MsgType LOGON, got %q", message.Fields["MsgType"])
	}
	if message.Fields["MsgSeqNum"] != "1" {
		t.Errorf("expected MsgSeqNum 1, got %q", message.Fields["MsgSeqNum"])
	}
	if message.Fields["SenderCompID"] != "CLIENT1" {
		t.Errorf("expected SenderCompID CLIENT1, got %q", message.Fields["SenderCompID"])
	}
}

func TestParseMessageTrimsWhitespaceAndTrailingDelimiter(t *testing.T) {
	message, err := ParseMessage("  MsgType=LOGOUT|MsgSeqNum=5|  \n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if message.Fields["MsgType"] != MsgTypeLogout {
		t.Errorf("expected MsgType LOGOUT, got %q", message.Fields["MsgType"])
	}
}

func TestParseMessageRejectsEmptyLine(t *testing.T) {
	_, err := ParseMessage("   ")
	if !errors.Is(err, ErrEmptyMessage) {
		t.Errorf("expected ErrEmptyMessage, got %v", err)
	}
}

func TestParseMessageRejectsMissingMsgType(t *testing.T) {
	_, err := ParseMessage("MsgSeqNum=1|Foo=bar")
	if !errors.Is(err, ErrMissingMsgType) {
		t.Errorf("expected ErrMissingMsgType, got %v", err)
	}
}

func TestParseMessageRejectsMalformedField(t *testing.T) {
	_, err := ParseMessage("MsgType=LOGON|ThisHasNoEquals")
	if !errors.Is(err, ErrMalformedField) {
		t.Errorf("expected ErrMalformedField, got %v", err)
	}
}

func TestParseMessageRejectsFieldWithEmptyName(t *testing.T) {
	_, err := ParseMessage("MsgType=LOGON|=value")
	if !errors.Is(err, ErrMalformedField) {
		t.Errorf("expected ErrMalformedField, got %v", err)
	}
}

func TestFormatMessageProducesDeterministicOrderedOutput(t *testing.T) {
	line := formatMessage(MsgTypeExecutionReport, 3, []orderedField{
		{Name: "ClOrdID", Value: "ord-1"},
		{Name: "OrdStatus", Value: "NEW"},
	})
	expected := "MsgType=EXECUTION_REPORT|MsgSeqNum=3|ClOrdID=ord-1|OrdStatus=NEW\n"
	if line != expected {
		t.Errorf("expected %q, got %q", expected, line)
	}
}

func TestFormatMessageRoundTripsThroughParseMessage(t *testing.T) {
	line := formatMessage(MsgTypeLogon, 42, []orderedField{{Name: "Status", Value: "ACCEPTED"}})
	parsed, err := ParseMessage(line)
	if err != nil {
		t.Fatalf("unexpected error round-tripping: %v", err)
	}
	if parsed.Fields["MsgSeqNum"] != "42" || parsed.Fields["Status"] != "ACCEPTED" {
		t.Errorf("round-trip lost data: %+v", parsed.Fields)
	}
}
