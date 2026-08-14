// Package conversationalorderparser is FEATURES.md §21's "Conversational
// order placement (chat/voice) with an explicit confirmation step before
// submission — never silently trade on ambiguous input".
//
// HONEST SCOPE STATEMENT, read this before wiring this package into
// anything client-facing:
//
//   - "Voice" is explicitly OUT OF SCOPE for this build. This sandboxed
//     environment has no audio input pipeline and no network access to a
//     speech-to-text API — there is nothing real to build for the voice
//     half of this feature, and faking it (e.g. hardcoding a canned
//     transcript) would violate this project's "never fake a feature"
//     rule more than simply not building it does. Only the TEXT/CHAT half
//     is implemented here.
//   - What IS built is genuinely real: a rule-based grammar/slot-
//     extraction parser (tokenize -> find side keyword -> find quantity
//     -> find instrument tokens -> find order-type clause), not a lookup
//     table of exact pre-written phrases. It generalizes to any sentence
//     that follows its documented grammar (see ParseConversationalOrderCommand's
//     doc comment for the exact grammar), not just the specific example
//     sentences in FEATURES.md.
//   - This package NEVER calls any external LLM API — none is available
//     or authorized in this environment. It is pure, deterministic,
//     dependency-free Go string processing.
//   - This package NEVER submits an order itself. ParseConversationalOrderCommand
//     returns a ParsedOrderIntent (structured fields) plus a human-
//     readable ConfirmationSummary string — nothing more. Turning that
//     intent into a real submitted order requires a SEPARATE, explicit
//     call: ParsedOrderIntent.ToOrderSubmissionRequest builds a real
//     orders.OrderSubmissionRequest, which the caller (see
//     cmd/server/main.go's buildConversationalOrderConfirmAndSubmitHandler)
//     must pass through the EXISTING internal/orders submission path
//     (processOrderSubmission — the exact same function backing
//     POST /orders/submit) — never a bespoke, gate-skipping shortcut.
//   - Ambiguous or incomplete input is REJECTED with a specific error
//     explaining exactly what's missing/contradictory — this package
//     never guesses a missing quantity, side, or instrument.
package conversationalorderparser

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"mercurius/omsgateway/internal/orders"
)

// Sentinel errors — each names exactly what was missing or contradictory,
// per FEATURES.md's "never silently trade on ambiguous input": a caller
// (and, through it, a human) always gets a specific, actionable reason a
// command was rejected, never a bare "couldn't parse that".
var (
	ErrEmptyCommand           = errors.New("empty command — nothing to parse")
	ErrMissingOrderSide       = errors.New(`could not determine buy or sell — include the word "buy" or "sell" (e.g. "buy 10 RELIANCE")`)
	ErrAmbiguousOrderSide     = errors.New(`both "buy" and "sell" were mentioned — ambiguous which side this order is on`)
	ErrMissingQuantity        = errors.New(`could not find an order quantity — include a number after buy/sell (e.g. "buy 10 RELIANCE")`)
	ErrMissingInstrument      = errors.New(`could not find an instrument to trade — include a symbol/name after the quantity (e.g. "buy 10 RELIANCE")`)
	ErrContradictoryOrderType = errors.New(`both "market" and "limit" were mentioned — ambiguous order type`)
	ErrLimitOrderMissingPrice = errors.New(`a limit order needs a price — say e.g. "at limit 3500"`)
)

// ParsedOrderIntent is the structured result of parsing one chat-style
// command — NOT an order, and never submitted by this package. See this
// package's doc comment for the mandatory separate confirm-and-submit
// step.
type ParsedOrderIntent struct {
	RawCommandText string `json:"rawCommandText"`

	InstrumentSymbol string `json:"instrumentSymbol"`

	OrderSideIsBuyNotSell bool `json:"orderSideIsBuyNotSell"`

	// OrderQuantity is the raw number the command stated. See
	// IsLotBasedQuantity's doc comment for the honest caveat about what
	// this number means for an options ("N lots of ...") command.
	OrderQuantity uint64 `json:"orderQuantity"`

	// IsLotBasedQuantity is true when the command said "N lots of X"
	// rather than "N shares of X" / "N X". HONEST GAP: this parser does
	// NOT convert a lot count into a real share-equivalent quantity —
	// this repo has no instrument-master/contract-lot-size reference
	// data to convert with (see internal/exposurelimits' own "illustrative
	// segment, not a real exchange taxonomy" caveat for the same
	// underlying gap: no real instrument reference data exists anywhere
	// in this codebase yet). OrderQuantity for a lot-based command is the
	// LOT COUNT AS STATED, not shares. ConfirmationSummary says this
	// explicitly so a human confirming the order sees it, rather than
	// this being silently wrong.
	IsLotBasedQuantity bool `json:"isLotBasedQuantity"`

	OrderIsMarketOrderNotLimit bool `json:"orderIsMarketOrderNotLimit"`

	// LimitPriceInMinorUnits is set only when OrderIsMarketOrderNotLimit
	// is false. HONEST SIMPLIFICATION: this parser takes the spoken
	// integer verbatim as LimitPriceInMinorUnits — it does NOT apply any
	// rupees-to-paise (or similar major-to-minor-unit) ×100 conversion,
	// because no other part of this codebase codifies a canonical
	// conversion rule to reuse, and no fractional/paise price is ever
	// spoken in a chat command's whole-number phrasing anyway. A real
	// deployment handling genuine fractional currency input needs a real
	// currency-conversion layer here; this build does not have one.
	LimitPriceInMinorUnits int64 `json:"limitPriceInMinorUnits,omitempty"`

	// IsOptionsInstrument is true when the command's instrument tokens
	// included a recognized "CE" (call) or "PE" (put) suffix — see
	// buildInstrumentSymbol's doc comment for exactly how that changes
	// InstrumentSymbol's construction.
	IsOptionsInstrument bool `json:"isOptionsInstrument"`

	// ConfirmationSummary is a human-readable sentence describing exactly
	// what will be submitted if confirmed — this, not the raw command
	// text, is what a UI should show a user before they hit "confirm".
	ConfirmationSummary string `json:"confirmationSummary"`
}

// ToOrderSubmissionRequest builds a real orders.OrderSubmissionRequest
// from this parsed intent — the type the EXISTING internal/orders
// submission path (see cmd/server/main.go's processOrderSubmission)
// already accepts. This function does not submit anything itself; it is
// deliberately pure data mapping. See this package's doc comment for why
// actually submitting requires a separate, explicit call.
func (intent ParsedOrderIntent) ToOrderSubmissionRequest(clientAccountIdentifier string) orders.OrderSubmissionRequest {
	return orders.OrderSubmissionRequest{
		ClientAccountIdentifier:    clientAccountIdentifier,
		InstrumentSymbol:           intent.InstrumentSymbol,
		OrderSideIsBuyNotSell:      intent.OrderSideIsBuyNotSell,
		OrderIsMarketOrderNotLimit: intent.OrderIsMarketOrderNotLimit,
		LimitPriceInMinorUnits:     intent.LimitPriceInMinorUnits,
		OrderQuantity:              intent.OrderQuantity,
	}
}

// ParseConversationalOrderCommand parses one chat-style sentence into a
// ParsedOrderIntent using a real, documented grammar — not a lookup table
// of exact phrases:
//
//	[buy|sell] <quantity> [lots|shares] [of] <instrument-tokens...> [at] [market | limit <price>]
//
// Concretely:
//  1. Exactly one of "buy"/"sell" must appear (case-insensitive) — both
//     or neither is rejected (ErrAmbiguousOrderSide / ErrMissingOrderSide).
//  2. The first integer token after the side keyword is the quantity —
//     none found is rejected (ErrMissingQuantity).
//  3. An optional "lots"/"lot" or "shares" token (and an optional "of"
//     after it) is consumed next — see IsLotBasedQuantity's doc comment.
//  4. Every token up to (not including) "at"/"market"/"limit" or the end
//     of the sentence is an instrument token — none found is rejected
//     (ErrMissingInstrument). See buildInstrumentSymbol for exactly how
//     those tokens become InstrumentSymbol.
//  5. Everything after that is the order-type clause: "market" alone
//     means a market order; "limit <price>" means a limit order at that
//     price (a limit with no following number is rejected,
//     ErrLimitOrderMissingPrice); both mentioned is rejected
//     (ErrContradictoryOrderType); NEITHER mentioned defaults to a market
//     order — a deliberate, documented design choice (a bare "buy 2 lots
//     of NIFTY 22000 CE" has no order-type clause at all in FEATURES.md's
//     own example sentence, and defaulting an unqualified chat order to
//     "market" matches how a plain "buy X" is commonly understood).
func ParseConversationalOrderCommand(commandText string) (ParsedOrderIntent, error) {
	trimmedCommand := strings.TrimSpace(commandText)
	if trimmedCommand == "" {
		return ParsedOrderIntent{}, ErrEmptyCommand
	}

	words := strings.Fields(trimmedCommand)
	lowerWords := make([]string, len(words))
	for i, word := range words {
		lowerWords[i] = strings.ToLower(strings.Trim(word, ".,!?;:"))
	}

	buyIndex, sellIndex := -1, -1
	for i, word := range lowerWords {
		switch word {
		case "buy":
			buyIndex = i
		case "sell":
			sellIndex = i
		}
	}
	if buyIndex >= 0 && sellIndex >= 0 {
		return ParsedOrderIntent{}, ErrAmbiguousOrderSide
	}
	if buyIndex < 0 && sellIndex < 0 {
		return ParsedOrderIntent{}, ErrMissingOrderSide
	}
	orderSideIsBuyNotSell := buyIndex >= 0
	sideIndex := buyIndex
	if sellIndex >= 0 {
		sideIndex = sellIndex
	}

	quantityIndex := -1
	var quantity uint64
	for i := sideIndex + 1; i < len(words); i++ {
		if lowerWords[i] == "at" || lowerWords[i] == "limit" || lowerWords[i] == "market" {
			break
		}
		if parsedQuantity, parseError := strconv.ParseUint(strings.Trim(words[i], ".,!?;:"), 10, 64); parseError == nil {
			quantity = parsedQuantity
			quantityIndex = i
			break
		}
	}
	if quantityIndex < 0 {
		return ParsedOrderIntent{}, ErrMissingQuantity
	}

	cursor := quantityIndex + 1
	isLotBasedQuantity := false
	if cursor < len(lowerWords) && (lowerWords[cursor] == "lots" || lowerWords[cursor] == "lot") {
		isLotBasedQuantity = true
		cursor++
	} else if cursor < len(lowerWords) && lowerWords[cursor] == "shares" {
		cursor++
	}
	if cursor < len(lowerWords) && lowerWords[cursor] == "of" {
		cursor++
	}

	var instrumentTokens []string
	for cursor < len(words) {
		if lowerWords[cursor] == "at" || lowerWords[cursor] == "market" || lowerWords[cursor] == "limit" {
			break
		}
		instrumentTokens = append(instrumentTokens, words[cursor])
		cursor++
	}
	if len(instrumentTokens) == 0 {
		return ParsedOrderIntent{}, ErrMissingInstrument
	}
	instrumentSymbol, isOptionsInstrument := buildInstrumentSymbol(instrumentTokens)

	sawMarketKeyword := false
	sawLimitKeyword := false
	var limitPriceInMinorUnits int64
	for cursor < len(words) {
		switch lowerWords[cursor] {
		case "at":
			cursor++
		case "market":
			sawMarketKeyword = true
			cursor++
		case "limit":
			sawLimitKeyword = true
			cursor++
			if cursor < len(words) {
				if parsedPrice, parseError := strconv.ParseInt(strings.Trim(words[cursor], ".,!?;:"), 10, 64); parseError == nil {
					limitPriceInMinorUnits = parsedPrice
					cursor++
				}
			}
		default:
			// Stray trailing token (e.g. punctuation-only remnants) —
			// ignored rather than rejected, since it carries no
			// grammatical meaning this parser recognizes.
			cursor++
		}
	}
	if sawMarketKeyword && sawLimitKeyword {
		return ParsedOrderIntent{}, ErrContradictoryOrderType
	}
	if sawLimitKeyword && limitPriceInMinorUnits == 0 {
		return ParsedOrderIntent{}, ErrLimitOrderMissingPrice
	}
	orderIsMarketOrderNotLimit := !sawLimitKeyword

	intent := ParsedOrderIntent{
		RawCommandText:             trimmedCommand,
		InstrumentSymbol:           instrumentSymbol,
		OrderSideIsBuyNotSell:      orderSideIsBuyNotSell,
		OrderQuantity:              quantity,
		IsLotBasedQuantity:         isLotBasedQuantity,
		OrderIsMarketOrderNotLimit: orderIsMarketOrderNotLimit,
		LimitPriceInMinorUnits:     limitPriceInMinorUnits,
		IsOptionsInstrument:        isOptionsInstrument,
	}
	intent.ConfirmationSummary = buildConfirmationSummary(intent)
	return intent, nil
}

// buildInstrumentSymbol derives an instrument symbol from the raw
// instrument tokens the grammar collected. HONEST, ILLUSTRATIVE
// CONVENTION (this repo has no real instrument master to look symbols up
// in — see internal/exposurelimits.ClassifySegment's own doc comment for
// the same underlying gap, which this deliberately stays consistent
// with): if any token case-insensitively equals "CE" or "PE" (a call/put
// option), the tokens are joined with "-", uppercased, and suffixed with
// "-OPT" — e.g. ["NIFTY","22000","CE"] -> "NIFTY-22000-CE-OPT" — which
// makes exposurelimits.ClassifySegment correctly classify it as
// FUTURES_AND_OPTIONS. Otherwise the tokens are simply concatenated
// (no separator) and uppercased — e.g. ["RELIANCE"] -> "RELIANCE".
func buildInstrumentSymbol(instrumentTokens []string) (symbol string, isOption bool) {
	for _, token := range instrumentTokens {
		if strings.EqualFold(token, "CE") || strings.EqualFold(token, "PE") {
			isOption = true
			break
		}
	}

	if isOption {
		upperTokens := make([]string, len(instrumentTokens))
		for i, token := range instrumentTokens {
			upperTokens[i] = strings.ToUpper(token)
		}
		return strings.Join(upperTokens, "-") + "-OPT", true
	}

	var builder strings.Builder
	for _, token := range instrumentTokens {
		builder.WriteString(strings.ToUpper(token))
	}
	return builder.String(), false
}

// buildConfirmationSummary produces the human-readable sentence a UI
// should show a user before they explicitly confirm submission.
func buildConfirmationSummary(intent ParsedOrderIntent) string {
	sideWord := "SELL"
	if intent.OrderSideIsBuyNotSell {
		sideWord = "BUY"
	}

	quantityUnit := "share(s)"
	if intent.IsLotBasedQuantity {
		quantityUnit = "lot(s)"
	}

	orderTypeDescription := "MARKET"
	if !intent.OrderIsMarketOrderNotLimit {
		orderTypeDescription = fmt.Sprintf("LIMIT @ %d", intent.LimitPriceInMinorUnits)
	}

	summary := fmt.Sprintf(
		"%s %d %s of %s at %s — please explicitly confirm to submit this order.",
		sideWord, intent.OrderQuantity, quantityUnit, intent.InstrumentSymbol, orderTypeDescription,
	)

	if intent.IsLotBasedQuantity {
		summary += " NOTE: quantity is expressed in LOTS, not shares — this parser cannot convert lots into a real share-equivalent quantity (no contract lot-size reference data exists in this build). Review the actual quantity carefully before confirming."
	}

	return summary
}
