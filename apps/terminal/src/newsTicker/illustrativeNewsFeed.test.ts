import { describe, expect, it } from "vitest";
import {
  classifyIllustrativeHeadlineSentiment,
  getIllustrativeNewsFeedSnapshot,
} from "./illustrativeNewsFeed";

describe("classifyIllustrativeHeadlineSentiment", () => {
  it("classifies a headline with more positive words as bullish", () => {
    expect(classifyIllustrativeHeadlineSentiment("Strong growth and record profit beat estimates")).toBe(
      "bullish"
    );
  });

  it("classifies a headline with more negative words as bearish", () => {
    expect(
      classifyIllustrativeHeadlineSentiment("Losses widen as sales declined and layoffs loom")
    ).toBe("bearish");
  });

  it("classifies a headline with no sentiment words as neutral", () => {
    expect(classifyIllustrativeHeadlineSentiment("Trading volume remains steady today")).toBe(
      "neutral"
    );
  });

  it("classifies a headline with an equal count of positive/negative words as neutral", () => {
    expect(classifyIllustrativeHeadlineSentiment("Strong gains offset by weak losses")).toBe(
      "neutral"
    );
  });

  it("is case-insensitive", () => {
    expect(classifyIllustrativeHeadlineSentiment("STRONG GROWTH BEAT ESTIMATES")).toBe("bullish");
  });
});

describe("getIllustrativeNewsFeedSnapshot", () => {
  it("returns a non-empty, uniquely-identified set of headlines", () => {
    const snapshot = getIllustrativeNewsFeedSnapshot(1_000_000);
    expect(snapshot.length).toBeGreaterThan(0);
    const ids = new Set(snapshot.map((headline) => headline.headlineId));
    expect(ids.size).toBe(snapshot.length);
  });

  it("gives every headline a strictly past timestamp relative to nowEpochMillis", () => {
    const now = 1_000_000_000;
    const snapshot = getIllustrativeNewsFeedSnapshot(now);
    for (const headline of snapshot) {
      expect(headline.publishedAtEpochMillis).toBeLessThanOrEqual(now);
    }
  });

  it("assigns a sentiment consistent with classifyIllustrativeHeadlineSentiment for each headline", () => {
    const snapshot = getIllustrativeNewsFeedSnapshot();
    for (const headline of snapshot) {
      expect(headline.sentiment).toBe(classifyIllustrativeHeadlineSentiment(headline.headlineText));
    }
  });
});
