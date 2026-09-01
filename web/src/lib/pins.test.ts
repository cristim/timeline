import { describe, expect, it } from "vitest";
import type { ChunkItem, EntityDoc } from "./data";
import { clickedEntitySlug, comparePinColor, comparePinRole, mapPointFeatures, withPinnedEntities } from "./pins";

function item(slug: string, point?: [number, number]): ChunkItem {
  return { slug, type: "place", name: slug, t0: 0, t1: 1, precision: "year", status: "documented", categories: [], importance: 1, point };
}

function doc(slug: string, point?: [number, number]): EntityDoc {
  return { slug, type: "place", name: slug, temporal: { t0: 0, t1: 1, precision: "year", status: "documented" }, categories: [], importance: 1, point, links: {} };
}

describe("comparison pins", () => {
  it("adds loaded subjects once and preserves their real point or lack of one", () => {
    const items = withPinnedEntities([item("a", [1, 2])], [doc("a", [9, 9]), doc("b"), doc("b"), null]);
    expect(items.map((entry) => entry.slug)).toEqual(["a", "b"]);
    expect(items[1].point).toBeUndefined();
  });

  it("gives the two subjects distinct map roles and draws only real points", () => {
    const features = mapPointFeatures([item("a", [1, 2]), item("b", [3, 4]), item("none")], null, ["a", "b"]);
    expect(features.features.map((feature) => [feature.properties?.slug, feature.properties?.compare])).toEqual([
      ["a", "compare-a"],
      ["b", "compare-b"],
    ]);
    expect(comparePinRole("other", ["a", "b"])).toBeNull();
    expect(comparePinColor("compare-a")).not.toBe(comparePinColor("compare-b"));
  });

  it("does not invent a map location when one or both subjects lack a point", () => {
    const one = mapPointFeatures([item("a", [1, 2]), item("b")], null, ["a", "b"]);
    expect(one.features.map((feature) => feature.properties?.slug)).toEqual(["a"]);
    expect(mapPointFeatures([item("a"), item("b")], null, ["a", "b"]).features).toEqual([]);
  });

  it("keeps map clicks selecting the real feature slug", () => {
    expect(clickedEntitySlug([{ properties: { slug: "a" } }])).toBe("a");
    expect(clickedEntitySlug([{ properties: { slug: 4 } }])).toBeNull();
    expect(clickedEntitySlug([])).toBeNull();
  });
});
