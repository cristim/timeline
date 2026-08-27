import { describe, expect, it } from "vitest";
import {
  laneItems,
  mapItems,
  nearCursor,
  pointLike,
  startsOrEndsWithin,
  visibleAt,
  type Visibility,
} from "./visible";
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

/** A 0..1000 view. Cursor centred unless overridden; 10% band = ±100. */
function view(over: Partial<Visibility> = {}): Visibility {
  return { t0: 0, t1: 1000, cursor: 500, selected: null, ...over };
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

describe("pointLike", () => {
  it("treats instants as point-like", () => {
    expect(pointLike(item("moment", 500, 500), 1000)).toBe(true);
  });
  it("treats intervals under 1% of the span as point-like", () => {
    expect(pointLike(item("tiny", 500, 509), 1000)).toBe(true);
  });
  it("treats an interval of exactly 1% as a real interval", () => {
    expect(pointLike(item("edge", 500, 510), 1000)).toBe(false);
  });
  it("reclassifies with the zoom: WWII is an interval in a century, a moment in an eon", () => {
    const wwii = item("wwii", 0, 6);
    expect(pointLike(wwii, 100)).toBe(false); // 6% of a century
    expect(pointLike(wwii, 1_000_000)).toBe(true); // nothing next to an eon
  });
});

describe("nearCursor", () => {
  it("keeps an instant inside the band", () => {
    expect(nearCursor(item("in", 560, 560), view())).toBe(true);
  });
  it("drops an instant outside the band", () => {
    expect(nearCursor(item("out", 700, 700), view())).toBe(false);
  });
  it("includes the band edges", () => {
    expect(nearCursor(item("lo", 400, 400), view())).toBe(true);
    expect(nearCursor(item("hi", 600, 600), view())).toBe(true);
    expect(nearCursor(item("just-past", 601, 601), view())).toBe(false);
  });
  it("keeps a short interval that only reaches into the band", () => {
    expect(nearCursor(item("reaches", 380, 405), view())).toBe(true);
  });
  it("follows a pinned cursor rather than the centre", () => {
    expect(nearCursor(item("x", 900, 900), view({ cursor: 950 }))).toBe(true);
    expect(nearCursor(item("x", 900, 900), view({ cursor: 200 }))).toBe(false);
  });
});

describe("visibleAt", () => {
  it("hides an off-cursor instant that is nonetheless in view", () => {
    expect(visibleAt(item("far", 900, 900), view())).toBe(false);
  });
  it("shows the same instant once the cursor moves to it", () => {
    expect(visibleAt(item("far", 900, 900), view({ cursor: 880 }))).toBe(true);
  });
  it("shows real intervals regardless of the cursor", () => {
    expect(visibleAt(item("era", 700, 900), view())).toBe(true);
  });
  it("still hides anything outside the view", () => {
    expect(visibleAt(item("gone", 5000, 5000), view({ cursor: 5000 }))).toBe(false);
  });
  it("never hides the selected item", () => {
    const off = item("chosen", 900, 900);
    expect(visibleAt(off, view())).toBe(false);
    expect(visibleAt(off, view({ selected: "chosen" }))).toBe(true);
    // even when it is nowhere near the view at all
    expect(visibleAt(item("chosen", 9e9, 9e9), view({ selected: "chosen" }))).toBe(true);
  });
});

describe("whollyInPeriphery", () => {
  // 15% bands: [0,150] and [850,1000]. These are all real intervals (>= 1%).
  it("skips items entirely inside the left band", () => {
    expect(laneItems([item("l", 20, 120)], view())).toHaveLength(0);
  });
  it("skips items entirely inside the right band", () => {
    expect(laneItems([item("r", 880, 950)], view())).toHaveLength(0);
  });
  it("skips edge slivers of items running off-view", () => {
    expect(laneItems([item("sliver", -500, 50)], view())).toHaveLength(0);
    expect(laneItems([item("sliver2", 960, 2000)], view())).toHaveLength(0);
  });
  it("keeps items straddling a band boundary", () => {
    expect(laneItems([item("straddle", 100, 400)], view())).toHaveLength(1);
    expect(laneItems([item("center", 400, 600)], view())).toHaveLength(1);
  });
  it("does not apply to point-like items: an edge-pinned cursor still shows its own", () => {
    // Cursor at 5% of the view, band [-50,150] - entirely inside the left
    // periphery band. Declutter must not swallow what the user pinned.
    const out = laneItems([item("pinned-moment", 60, 60)], view({ cursor: 50 }));
    expect(out.map((i) => i.slug)).toEqual(["pinned-moment"]);
  });
});

describe("laneItems", () => {
  it("caps to the most important eligible items", () => {
    const items = Array.from({ length: 150 }, (_, i) => item(`e${i}`, 480, 520, i / 1000));
    items.push(item("spanning", -100, 1100, 1.0)); // most important but pass-through
    const out = laneItems(items, view(), 100);
    expect(out).toHaveLength(100);
    expect(out.find((i) => i.slug === "spanning")).toBeUndefined();
    expect(out[0].slug).toBe("e149");
  });

  it("keeps the selection even when the cap is full of more important items", () => {
    const items = Array.from({ length: 150 }, (_, i) => item(`e${i}`, 480, 520, 0.5 + i / 1000));
    items.push(item("humble", 480, 520, 0.0));
    const out = laneItems(items, view({ selected: "humble" }), 100);
    expect(out).toHaveLength(100);
    expect(out[0].slug).toBe("humble");
  });

  it("drops off-cursor moments but keeps the intervals around them", () => {
    const items = [
      item("constantinople", 900, 900),
      item("near-moment", 505, 505),
      item("long-era", 300, 700),
    ];
    expect(laneItems(items, view()).map((i) => i.slug).sort()).toEqual([
      "long-era",
      "near-moment",
    ]);
  });

  it("reveals an off-cursor moment when the cursor is dragged onto it", () => {
    const items = [item("constantinople", 900, 900), item("long-era", 300, 700)];
    // 900 is inside the right periphery band, so this also proves point-like
    // items skip the declutter rule.
    expect(laneItems(items, view({ cursor: 890 })).map((i) => i.slug)).toContain(
      "constantinople",
    );
  });
});

describe("mapItems", () => {
  it("applies the same gate without a cap", () => {
    const items = Array.from({ length: 150 }, (_, i) => item(`e${i}`, 480, 520, i / 1000));
    items.push(item("elsewhere", 900, 900));
    const out = mapItems(items, view());
    expect(out).toHaveLength(150);
    expect(out.find((i) => i.slug === "elsewhere")).toBeUndefined();
  });

  it("keeps edge-band intervals the lanes declutter away", () => {
    // The map has room for it; only the lanes are row-constrained.
    expect(mapItems([item("r", 880, 950)], view())).toHaveLength(1);
    expect(laneItems([item("r", 880, 950)], view())).toHaveLength(0);
  });

  it("keeps the selection off-cursor", () => {
    const items = [item("chosen", 900, 900)];
    expect(mapItems(items, view())).toHaveLength(0);
    expect(mapItems(items, view({ selected: "chosen" }))).toHaveLength(1);
  });
});
