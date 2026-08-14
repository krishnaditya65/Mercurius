// Mercurius / terminal — scrolling news/sentiment ticker widget.
//
// FEATURES.md §10 "[P3] News/sentiment ticker widget". Renders
// `illustrativeNewsFeed.ts`'s (explicitly illustrative, see that module's
// header comment) headlines as a real horizontally-scrolling marquee via
// CSS animation over a duplicated track (the standard seamless-marquee
// technique: render the headline sequence twice back-to-back and animate
// -50% translateX so the loop point is invisible).
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useEffect, useState } from "react";
import { getIllustrativeNewsFeedSnapshot, type NewsTickerHeadline } from "./illustrativeNewsFeed";

const NEWS_REFRESH_INTERVAL_MILLISECONDS = 30_000;

export function NewsTickerWidget() {
  const [headlines, setHeadlines] = useState<NewsTickerHeadline[]>(() => getIllustrativeNewsFeedSnapshot());

  useEffect(() => {
    const intervalId = setInterval(() => {
      setHeadlines(getIllustrativeNewsFeedSnapshot());
    }, NEWS_REFRESH_INTERVAL_MILLISECONDS);
    return () => clearInterval(intervalId);
  }, []);

  return (
    <div className="newsTickerWidget">
      <div className="newsTickerWidget__badge">ILLUSTRATIVE DATA</div>
      <div className="newsTickerWidget__track">
        <NewsTickerHeadlineSequence headlines={headlines} />
        <NewsTickerHeadlineSequence headlines={headlines} ariaHidden />
      </div>
    </div>
  );
}

function NewsTickerHeadlineSequence(props: { headlines: NewsTickerHeadline[]; ariaHidden?: boolean }) {
  return (
    <div className="newsTickerWidget__sequence" aria-hidden={props.ariaHidden}>
      {props.headlines.map((headline) => (
        <span key={`${headline.headlineId}-${props.ariaHidden ? "dup" : "orig"}`} className="newsTickerWidget__item">
          <span className={`newsTickerWidget__sentiment newsTickerWidget__sentiment--${headline.sentiment}`}>
            {sentimentGlyph(headline.sentiment)}
          </span>
          <strong>{headline.instrumentSymbol}</strong>
          <span>{headline.headlineText}</span>
        </span>
      ))}
    </div>
  );
}

function sentimentGlyph(sentiment: NewsTickerHeadline["sentiment"]): string {
  if (sentiment === "bullish") return "▲";
  if (sentiment === "bearish") return "▼";
  return "●";
}
