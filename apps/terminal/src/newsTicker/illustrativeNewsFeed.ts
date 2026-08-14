// Mercurius / terminal — news/sentiment ticker data source.
//
// FEATURES.md §10 "[P3] News/sentiment ticker widget".
//
// HONESTY NOTE (read before wiring this to anything real): every headline
// in `ILLUSTRATIVE_NEWS_HEADLINES` below is HAND-WRITTEN FIXTURE DATA, not
// a real news feed. There is no network call, no scraper, no RSS/webhook
// ingestion anywhere in this module — checked across the rest of this
// monorepo before writing this: no service exposes a real news/headlines
// HTTP endpoint (`quant-engine`'s `illustrativeSentimentTradingHook.py` is
// the closest relative — a toy lexicon-based sentiment SCORER over
// caller-supplied text, explicitly documented there as not doing real
// ingestion either, and explicitly out of scope to wire to any order path
// — see that module's own header comment). This ticker exists to prove
// the SHAPE of a real-time scrolling news/sentiment widget (headline text
// + a sentiment label + a per-instrument tag, animated across the screen)
// is genuinely built and testable, not to be anyone's real news source.
// The `sentiment` field is computed by reusing the exact same TOY lexicon scoring
// approach as quant-engine's module (small hand-built positive/negative
// word list) applied to each fixture headline — real, runnable code, just
// not a real NLP model, for the same reasons documented there.
//
// A real build would replace `getIllustrativeNewsFeedSnapshot` with a
// fetch against a real news/wire-service API and pipe through a real
// sentiment model — everything downstream of it (the ticker's scrolling
// render, the sentiment-colored badges) is written against this module's
// types and wouldn't need to change shape to swap the data source out.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

export type NewsSentiment = "bullish" | "bearish" | "neutral";

export type NewsTickerHeadline = {
  headlineId: string;
  instrumentSymbol: string;
  headlineText: string;
  sentiment: NewsSentiment;
  publishedAtEpochMillis: number;
};

// Same tiny, deliberately naive lexicon as quant-engine's
// illustrativeSentimentTradingHook.py, reimplemented here in TypeScript
// (duplicated rather than shared cross-language, matching this task's
// "duplicate small pieces of logic rather than a risky cross-package
// refactor" guidance).
const POSITIVE_WORDS = new Set([
  "beat", "beats", "strong", "growth", "record", "profit", "profitable", "surge", "surged",
  "outperform", "outperformed", "upgrade", "upgraded", "exceeded", "raised", "raise", "bullish",
  "gain", "gains", "improved", "robust", "expand", "expansion", "positive",
]);
const NEGATIVE_WORDS = new Set([
  "miss", "missed", "weak", "decline", "declined", "loss", "losses", "plunge", "plunged",
  "underperform", "underperformed", "downgrade", "downgraded", "cut", "lowered", "bearish",
  "drop", "dropped", "warning", "recall", "lawsuit", "bankruptcy", "layoffs", "negative",
  "fell", "falling",
]);

export function classifyIllustrativeHeadlineSentiment(headlineText: string): NewsSentiment {
  const words = headlineText.toLowerCase().match(/[a-z]+/g) ?? [];
  let positiveCount = 0;
  let negativeCount = 0;
  for (const word of words) {
    if (POSITIVE_WORDS.has(word)) positiveCount++;
    if (NEGATIVE_WORDS.has(word)) negativeCount++;
  }
  if (positiveCount > negativeCount) return "bullish";
  if (negativeCount > positiveCount) return "bearish";
  return "neutral";
}

const ILLUSTRATIVE_HEADLINE_TEMPLATES: Array<{ instrumentSymbol: string; headlineText: string }> = [
  { instrumentSymbol: "AAPL", headlineText: "AAPL beats quarterly revenue estimates, raised full-year guidance" },
  { instrumentSymbol: "TSLA", headlineText: "TSLA deliveries missed analyst expectations for the second straight quarter" },
  { instrumentSymbol: "MSFT", headlineText: "MSFT cloud segment shows robust growth, analysts upgraded price target" },
  { instrumentSymbol: "AMZN", headlineText: "AMZN announces layoffs across logistics division amid cost-cutting" },
  { instrumentSymbol: "NVDA", headlineText: "NVDA surged after data-center demand exceeded prior projections" },
  { instrumentSymbol: "META", headlineText: "META faces new lawsuit over data-privacy practices" },
  { instrumentSymbol: "GOOGL", headlineText: "GOOGL search ad revenue declined for the first time in two years" },
  { instrumentSymbol: "DEMO-EQ", headlineText: "DEMO-EQ trading volume remains steady ahead of scheduled earnings" },
];

/** Returns a deterministic-order snapshot of illustrative headlines — a
 * caller wanting a live-scrolling feel should re-call this on an interval
 * and/or shuffle client-side; this module itself performs no I/O and no
 * timers, keeping it trivially testable. */
export function getIllustrativeNewsFeedSnapshot(nowEpochMillis: number = Date.now()): NewsTickerHeadline[] {
  return ILLUSTRATIVE_HEADLINE_TEMPLATES.map((template, index) => ({
    headlineId: `illustrative-${index}`,
    instrumentSymbol: template.instrumentSymbol,
    headlineText: template.headlineText,
    sentiment: classifyIllustrativeHeadlineSentiment(template.headlineText),
    // Spread fabricated timestamps a few minutes apart, strictly in the
    // past relative to `nowEpochMillis`, purely so a UI can sort/display
    // "N minutes ago" plausibly — these are not real publish times.
    publishedAtEpochMillis: nowEpochMillis - index * 4 * 60 * 1000,
  }));
}
