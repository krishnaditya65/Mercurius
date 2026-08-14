import { describe, expect, it } from "vitest";
import { buildLadderRowsFromSnapshot } from "./DomLadderWidget";

describe("buildLadderRowsFromSnapshot", () => {
  it("merges bid and ask levels at the same price into one row", () => {
    const rows = buildLadderRowsFromSnapshot({
      epochMillis: 0,
      walEventIndex: 0,
      bidLevelsBestFirst: [[100, 5]],
      askLevelsBestFirst: [[100, 3]],
    });
    expect(rows).toEqual([{ priceInMinorUnits: 100, bidQuantity: 5, askQuantity: 3 }]);
  });

  it("sorts rows by price descending (highest price first)", () => {
    const rows = buildLadderRowsFromSnapshot({
      epochMillis: 0,
      walEventIndex: 0,
      bidLevelsBestFirst: [
        [98, 2],
        [99, 4],
      ],
      askLevelsBestFirst: [
        [101, 1],
        [102, 6],
      ],
    });
    expect(rows.map((row) => row.priceInMinorUnits)).toEqual([102, 101, 99, 98]);
  });

  it("leaves the opposite side null when only one side quotes a price", () => {
    const rows = buildLadderRowsFromSnapshot({
      epochMillis: 0,
      walEventIndex: 0,
      bidLevelsBestFirst: [[99, 4]],
      askLevelsBestFirst: [],
    });
    expect(rows).toEqual([{ priceInMinorUnits: 99, bidQuantity: 4, askQuantity: null }]);
  });

  it("returns an empty array for an empty snapshot", () => {
    const rows = buildLadderRowsFromSnapshot({
      epochMillis: 0,
      walEventIndex: 0,
      bidLevelsBestFirst: [],
      askLevelsBestFirst: [],
    });
    expect(rows).toEqual([]);
  });
});
