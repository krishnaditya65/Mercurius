// Mercurius / terminal — command bar grammar parser.
//
// FEATURES.md §10 "[P2] Command bar / hotkey system (`AAPL DES <GO>`
// style)". This is a genuine small recursive-descent-free (the grammar is
// simple enough not to need one) parser for a Bloomberg-terminal-style
// command line: `<TICKER> <VERB> [ARGS...] [<GO>]`. It is pure, has no
// side effects, and does not know how to dispatch a parsed command to a
// widget — that's `dispatchParsedCommandBarCommand` in `CommandBar.tsx`,
// kept separate so this module stays trivially unit-testable.
//
// GRAMMAR:
//   command   := ticker WS verb (WS arg)* WS? goSuffix?
//   ticker    := /^[A-Z][A-Z0-9.\-]{0,9}$/   (e.g. AAPL, BRK.B, RELIANCE-BE)
//   verb      := one of KNOWN_COMMAND_VERBS, case-insensitive
//   arg       := any non-whitespace token
//   goSuffix  := "<GO>" | "GO"               (case-insensitive)
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

/** Every verb this build's command bar understands, plus a short
 * human-readable description used by the command bar's own help text. Real
 * Bloomberg mnemonics inspired the shape (DES/description, GP/graph, MOD/
 * modify order, DOM/depth-of-market) but these are Mercurius's own verbs,
 * wired to Mercurius's own widgets — not a claim of Bloomberg
 * compatibility. */
export const KNOWN_COMMAND_VERBS = {
  DES: "Open the instrument description/overview panel",
  MOD: "Open the order-modification ticket for this instrument",
  GP: "Open a price chart (graph) for this instrument",
  DOM: "Open a DOM ladder for this instrument",
  BLOTTER: "Open the order blotter filtered to this instrument",
  NEWS: "Filter the news/sentiment ticker to this instrument",
} as const;

export type CommandBarVerb = keyof typeof KNOWN_COMMAND_VERBS;

const TICKER_PATTERN = /^[A-Z][A-Z0-9.\-]{0,14}$/;

export type ParsedCommandBarCommand = {
  tickerSymbol: string;
  verb: CommandBarVerb;
  args: string[];
  /** Whether the input ended in an explicit "<GO>"/"GO" — Bloomberg-style
   * command lines are conventionally only "committed" once GO is typed,
   * so a caller may choose to only dispatch when this is true and treat
   * everything else as a live preview/autocomplete state. */
  wasGoTriggered: boolean;
};

export type CommandBarParseResult =
  | { wasParseSuccessful: true; command: ParsedCommandBarCommand }
  | { wasParseSuccessful: false; errorMessage: string };

/** Parses one raw command bar input line. Never throws — always returns a
 * discriminated result so a UI can render `errorMessage` inline without a
 * try/catch. */
export function parseCommandBarInput(rawInputLine: string): CommandBarParseResult {
  const collapsedWhitespaceLine = rawInputLine.trim().replace(/\s+/g, " ");
  if (collapsedWhitespaceLine.length === 0) {
    return { wasParseSuccessful: false, errorMessage: "Type a command, e.g. \"AAPL DES <GO>\"." };
  }

  const rawTokens = collapsedWhitespaceLine.split(" ");

  const lastToken = rawTokens[rawTokens.length - 1];
  const wasGoTriggered = isGoToken(lastToken);
  const tokensExcludingGo = wasGoTriggered ? rawTokens.slice(0, -1) : rawTokens;

  if (tokensExcludingGo.length === 0) {
    return {
      wasParseSuccessful: false,
      errorMessage: "Missing ticker and verb before <GO> — expected \"TICKER VERB [args] <GO>\".",
    };
  }

  if (tokensExcludingGo.length === 1) {
    return {
      wasParseSuccessful: false,
      errorMessage: `Missing a command verb after "${tokensExcludingGo[0]}" — expected e.g. "${tokensExcludingGo[0]} DES <GO>".`,
    };
  }

  const [rawTicker, rawVerb, ...args] = tokensExcludingGo;
  const tickerSymbol = rawTicker.toUpperCase();
  if (!TICKER_PATTERN.test(tickerSymbol)) {
    return {
      wasParseSuccessful: false,
      errorMessage: `"${rawTicker}" doesn't look like a ticker symbol (expected letters/digits/./- starting with a letter, up to 15 characters).`,
    };
  }

  const normalizedVerb = rawVerb.toUpperCase();
  if (!isKnownCommandVerb(normalizedVerb)) {
    const knownVerbList = Object.keys(KNOWN_COMMAND_VERBS).join(", ");
    return {
      wasParseSuccessful: false,
      errorMessage: `Unknown verb "${rawVerb}" — known verbs are: ${knownVerbList}.`,
    };
  }

  return {
    wasParseSuccessful: true,
    command: {
      tickerSymbol,
      verb: normalizedVerb,
      args,
      wasGoTriggered,
    },
  };
}

function isGoToken(token: string): boolean {
  const normalized = token.toUpperCase();
  return normalized === "GO" || normalized === "<GO>";
}

function isKnownCommandVerb(candidate: string): candidate is CommandBarVerb {
  return Object.prototype.hasOwnProperty.call(KNOWN_COMMAND_VERBS, candidate);
}
