import { describe, expect, it } from "vitest";
import { laneItems, startsOrEndsWithin } from "./visible";
import type { ChunkItem } from "./data";

function item(slug: string, t0: number, t1: number, importance = 0.5): ChunkItem {
  return {
    slug,
    type: "event",
    name: slug,
    t0,
    t1,
    precision: "year",
    status: "documented",
    categories: ["war"],
    importance,
  };
}

describe("startsOrEndsWithin", () => {
  const [v0, v1] = [100, 200];
  it("keeps items starting within the view", () => {
    expect(startsOrEndsWithin(item("a", 150, 300), v0, v1)).toBe(true);
  });
  it("keeps items ending within the view", () => {
    expect(startsOrEndsWithin(item("b", 0, 150), v0, v1)).toBe(true);
  });
  it("keeps items fully inside", () => {
    expect(startsOrEndsWithin(item("c", 120, 180), v0, v1)).toBe(true);
  });
  it("skips pass-through items spanning the whole view", () => {
    expect(startsOrEndsWithin(item("d", 0, 300), v0, v1)).toBe(false);
  });
  it("skips items fully outside", () => {
    expect(startsOrEndsWithin(item("e", 300, 400), v0, v1)).toBe(false);
    expect(startsOrEndsWithin(item("f", 0, 50), v0, v1)).toBe(false);
  });
  it("keeps moments on the boundary", () => {
    expect(startsOrEndsWithin(item("g", 100, 100), v0, v1)).toBe(true);
    expect(startsOrEndsWithin(item("h", 200, 200), v0, v1)).toBe(true);
  });
});

describe("laneItems", () => {
  it("caps to the most important eligible items", () => {
    const items = Array.from({ length: 150 }, (_, i) =>
      item(`e${i}`, 110, 120, i / 1000),
    );
    items.push(item("spanning", 0, 999, 1.0)); // most important but pass-through
    const out = laneItems(items, 100, 200, 100);
    expect(out).toHaveLength(100);
    expect(out.find((i) => i.slug === "spanning")).toBeUndefined();
    // highest-importance eligible item leads
    expect(out[0].slug).toBe("e149");
  });
});
