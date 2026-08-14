package conversationalorderparser

import (
	"errors"
	"testing"
)

// --- Successful parses -------------------------------------------------

func TestParseConversationalOrderCommand_BuyMarketSharesOf(t *testing.T) {
	intent, err := ParseConversationalOrderCommand("buy 10 shares of RELIANCE at market")
	if err != nil {
		t.Fatalf("expected a successful parse, got error: %v", err)
	}
	if !intent.OrderSideIsBuyNotSell {
		t.Fatal("expected a BUY order")
	}
	if intent.OrderQuantity != 10 {
		t.Fatalf("expected quantity 10, got %d", intent.OrderQuantity)
	}
	if intent.InstrumentSymbol != "RELIANCE" {
		t.Fatalf("expected instrument RELIANCE, got %q", intent.InstrumentSymbol)
	}
	if !intent.OrderIsMarketOrderNotLimit {
		t.Fatal("expected a market order")
	}
	if intent.IsLotBasedQuantity {
		t.Fatal("did not expect lot-based quantity")
	}
	if intent.ConfirmationSummary == "" {
		t.Fatal("expected a non-empty confirmation summary")
	}
}

func TestParseConversationalOrderCommand_SellLimitWithPrice(t *testing.T) {
	intent, err := ParseConversationalOrderCommand("sell 5 TCS at limit 3500")
	if err != nil {
		t.Fatalf("expected a successful parse, got error: %v", err)
	}
	if intent.OrderSideIsBuyNotSell {
		t.Fatal("expected a SELL order")
	}
	if intent.OrderQuantity != 5 {
		t.Fatalf("expected quantity 5, got %d", intent.OrderQuantity)
	}
	if intent.InstrumentSymbol != "TCS" {
		t.Fatalf("expected instrument TCS, got %q", intent.InstrumentSymbol)
	}
	if intent.OrderIsMarketOrderNotLimit {
		t.Fatal("expected a limit order")
	}
	if intent.LimitPriceInMinorUnits != 3500 {
		t.Fatalf("expected limit price 3500, got %d", intent.LimitPriceInMinorUnits)
	}
}

func TestParseConversationalOrderCommand_BuyLotsOfAnOptionsInstrumentDefaultsToMarket(t *testing.T) {
	intent, err := ParseConversationalOrderCommand("buy 2 lots of NIFTY 22000 CE")
	if err != nil {
		t.Fatalf("expected a successful parse, got error: %v", err)
	}
	if !intent.OrderSideIsBuyNotSell {
		t.Fatal("expected a BUY order")
	}
	if intent.OrderQuantity != 2 {
		t.Fatalf("expected quantity 2, got %d", intent.OrderQuantity)
	}
	if !intent.IsLotBasedQuantity {
		t.Fatal("expected IsLotBasedQuantity=true")
	}
	if intent.InstrumentSymbol != "NIFTY-22000-CE-OPT" {
		t.Fatalf("expected instrument NIFTY-22000-CE-OPT, got %q", intent.InstrumentSymbol)
	}
	if !intent.IsOptionsInstrument {
		t.Fatal("expected IsOptionsInstrument=true")
	}
	if !intent.OrderIsMarketOrderNotLimit {
		t.Fatal("expected a market order (no order-type clause present -> defaults to market)")
	}
	if intent.ConfirmationSummary == "" {
		t.Fatal("expected a confirmation summary")
	}
	if !containsSubstring(intent.ConfirmationSummary, "LOTS") {
		t.Fatalf("expected the confirmation summary to warn about lot-based quantity, got: %s", intent.ConfirmationSummary)
	}
}

func TestParseConversationalOrderCommand_ShortFormNoSharesOfKeywords(t *testing.T) {
	intent, err := ParseConversationalOrderCommand("buy 25 INFY at market")
	if err != nil {
		t.Fatalf("expected a successful parse, got error: %v", err)
	}
	if intent.InstrumentSymbol != "INFY" || intent.OrderQuantity != 25 {
		t.Fatalf("unexpected intent: %+v", intent)
	}
}

func TestParseConversationalOrderCommand_IsCaseInsensitiveAndTrimsPunctuation(t *testing.T) {
	intent, err := ParseConversationalOrderCommand("BUY 10 Shares Of reliance At Market.")
	if err != nil {
		t.Fatalf("expected a successful parse, got error: %v", err)
	}
	if intent.InstrumentSymbol != "RELIANCE" {
		t.Fatalf("expected instrument RELIANCE, got %q", intent.InstrumentSymbol)
	}
	if !intent.OrderIsMarketOrderNotLimit {
		t.Fatal("expected a market order")
	}
}

func TestParseConversationalOrderCommand_ToOrderSubmissionRequestMapsFieldsCorrectly(t *testing.T) {
	intent, err := ParseConversationalOrderCommand("sell 5 TCS at limit 3500")
	if err != nil {
		t.Fatalf("expected a successful parse, got error: %v", err)
	}
	request := intent.ToOrderSubmissionRequest("acct-001")
	if request.ClientAccountIdentifier != "acct-001" {
		t.Fatalf("expected acct-001, got %s", request.ClientAccountIdentifier)
	}
	if request.InstrumentSymbol != "TCS" || request.OrderSideIsBuyNotSell || request.OrderIsMarketOrderNotLimit {
		t.Fatalf("unexpected request: %+v", request)
	}
	if request.LimitPriceInMinorUnits != 3500 || request.OrderQuantity != 5 {
		t.Fatalf("unexpected request: %+v", request)
	}
}

// --- Rejected / ambiguous input -----------------------------------------

func TestParseConversationalOrderCommand_RejectsEmptyCommand(t *testing.T) {
	_, err := ParseConversationalOrderCommand("   ")
	if !errors.Is(err, ErrEmptyCommand) {
		t.Fatalf("expected ErrEmptyCommand, got %v", err)
	}
}

func TestParseConversationalOrderCommand_RejectsMissingSide(t *testing.T) {
	_, err := ParseConversationalOrderCommand("10 shares of RELIANCE at market")
	if !errors.Is(err, ErrMissingOrderSide) {
		t.Fatalf("expected ErrMissingOrderSide, got %v", err)
	}
}

func TestParseConversationalOrderCommand_RejectsAmbiguousSide(t *testing.T) {
	_, err := ParseConversationalOrderCommand("buy sell 10 RELIANCE at market")
	if !errors.Is(err, ErrAmbiguousOrderSide) {
		t.Fatalf("expected ErrAmbiguousOrderSide, got %v", err)
	}
}

func TestParseConversationalOrderCommand_RejectsMissingQuantity(t *testing.T) {
	_, err := ParseConversationalOrderCommand("buy shares of RELIANCE at market")
	if !errors.Is(err, ErrMissingQuantity) {
		t.Fatalf("expected ErrMissingQuantity, got %v", err)
	}
}

func TestParseConversationalOrderCommand_RejectsMissingInstrument(t *testing.T) {
	_, err := ParseConversationalOrderCommand("buy 10 at market")
	if !errors.Is(err, ErrMissingInstrument) {
		t.Fatalf("expected ErrMissingInstrument, got %v", err)
	}
}

func TestParseConversationalOrderCommand_RejectsLimitWithoutPrice(t *testing.T) {
	_, err := ParseConversationalOrderCommand("sell 5 TCS at limit")
	if !errors.Is(err, ErrLimitOrderMissingPrice) {
		t.Fatalf("expected ErrLimitOrderMissingPrice, got %v", err)
	}
}

func TestParseConversationalOrderCommand_RejectsContradictoryOrderType(t *testing.T) {
	_, err := ParseConversationalOrderCommand("buy 10 RELIANCE at market limit 100")
	if !errors.Is(err, ErrContradictoryOrderType) {
		t.Fatalf("expected ErrContradictoryOrderType, got %v", err)
	}
}

func TestParseConversationalOrderCommand_NeverReturnsAnOrderSubmissionSideEffect(t *testing.T) {
	// Documentation-as-test: ParseConversationalOrderCommand's return type
	// is (ParsedOrderIntent, error) — there is no way for this function to
	// have submitted anything; the ONLY way to get an
	// orders.OrderSubmissionRequest out of this package is the separate,
	// explicit ToOrderSubmissionRequest call below, which itself only
	// builds a value and does not submit it either.
	intent, err := ParseConversationalOrderCommand("buy 10 RELIANCE at market")
	if err != nil {
		t.Fatalf("expected a successful parse, got error: %v", err)
	}
	if intent.ConfirmationSummary == "" {
		t.Fatal("expected a confirmation summary requiring separate explicit confirmation")
	}
	_ = intent.ToOrderSubmissionRequest("acct-001") // building the request is not submitting it
}

func containsSubstring(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
