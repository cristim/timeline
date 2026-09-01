// API-6: every view is shareable, so every field has to survive the round
// trip through the URL - including the ones that are legitimately absent.
import { describe, expect, it } from "vitest";
import { isValidEntitySlug } from "./slug";
import { cursorTime, DEFAULT_VIEW, parseView, serializeView } from "./state";

function parseURL(url: string, basePath = "/") {
  const parsed = new URL(url, "https://timeline.test");
  return parseView(parsed, basePath);
}

describe("time cursor state", () => {
  it("is unpinned by default and follows the centre of the view", () => {
    expect(DEFAULT_VIEW.tc).toBeNull();
    expect(cursorTime({ t0: -100, t1: 300, tc: null })).toBe(100);
    expect(cursorTime({ t0: -100, t1: 300, tc: 42 })).toBe(42);
  });

  it("stays out of the URL until it is pinned", () => {
    expect(serializeView(DEFAULT_VIEW)).not.toContain("tc=");
    expect(serializeView({ ...DEFAULT_VIEW, tc: -8.6e8 })).toContain("tc=");
  });

  it("round-trips a pinned cursor", () => {
    const v = { ...DEFAULT_VIEW, tc: -8.64e8 };
    expect(parseURL(serializeView(v)).tc).toBeCloseTo(-8.64e8, 3);
  });

  it("treats a pinned zero as pinned, not as absent", () => {
    // Number(null) is 0: without the has() guard an absent param would pin
    // the cursor to 1970.
    expect(parseURL("/?t0=-1e9&t1=1e9&tc=0").tc).toBe(0);
    expect(parseURL("/?t0=-1e9&t1=1e9").tc).toBeNull();
  });

  it("ignores a non-numeric cursor rather than pinning NaN", () => {
    expect(parseURL("/?tc=yesterday").tc).toBeNull();
  });
});

describe("canonical Compare routes (API-6)", () => {
  it("serializes zero, one, and two-subject Compare states canonically", () => {
    const zero = { ...DEFAULT_VIEW, mode: "compare" as const };
    const pending = { ...zero, compare: ["tyrannosaurus-rex"] };
    const pair = { ...zero, compare: ["tyrannosaurus-rex", "african-elephant"] };

    expect(new URL(serializeView(zero), "https://timeline.test").pathname).toBe("/");
    expect(new URL(serializeView(zero), "https://timeline.test").searchParams.get("view")).toBe(
      "compare",
    );
    expect(new URL(serializeView(pending), "https://timeline.test").pathname).toBe(
      "/entity/tyrannosaurus-rex",
    );
    expect(new URL(serializeView(pending), "https://timeline.test").searchParams.get("view")).toBe(
      "compare",
    );
    expect(new URL(serializeView(pair), "https://timeline.test").pathname).toBe(
      "/entity/tyrannosaurus-rex",
    );
    expect(new URL(serializeView(pair), "https://timeline.test").searchParams.get("compare")).toBe(
      "african-elephant",
    );
  });

  it("round-trips canonical routes in subject order", () => {
    expect(parseURL("/entity/tyrannosaurus-rex?compare=african-elephant")).toMatchObject({
      mode: "compare",
      compare: ["tyrannosaurus-rex", "african-elephant"],
    });
    expect(parseURL("/entity/tyrannosaurus-rex?view=compare")).toMatchObject({
      mode: "compare",
      compare: ["tyrannosaurus-rex"],
    });
    expect(parseURL("/?view=compare")).toMatchObject({ mode: "compare", compare: [] });
  });

  it("normalizes duplicate subjects to a pending comparison", () => {
    const view = parseURL("/entity/earth?compare=earth");
    expect(view).toMatchObject({ mode: "compare", compare: ["earth"] });
    const canonical = new URL(serializeView(view), "https://timeline.test");
    expect(canonical.pathname).toBe("/entity/earth");
    expect(canonical.searchParams.get("view")).toBe("compare");
    expect(canonical.searchParams.has("compare")).toBe(false);
  });

  it("migrates the old cmp query to the canonical route", () => {
    const view = parseURL("/?cmp=earth,mars");
    expect(view).toMatchObject({ mode: "compare", compare: ["earth", "mars"] });
    const canonical = new URL(serializeView(view), "https://timeline.test");
    expect(canonical.pathname).toBe("/entity/earth");
    expect(canonical.searchParams.get("compare")).toBe("mars");
    expect(canonical.searchParams.has("cmp")).toBe(false);
  });

  it("prefers canonical pair state when a legacy query is also present", () => {
    expect(parseURL("/entity/earth?compare=mars&cmp=venus,jupiter").compare).toEqual([
      "earth",
      "mars",
    ]);
  });

  it("prefers canonical pending and zero-subject views over legacy pairs", () => {
    expect(parseURL("/entity/earth?view=compare&cmp=venus,jupiter")).toMatchObject({
      mode: "compare",
      compare: ["earth"],
    });
    expect(parseURL("/?view=compare&cmp=venus,jupiter")).toMatchObject({
      mode: "compare",
      compare: [],
    });
  });

  it("keeps canonical routes under the deployed base path", () => {
    const view = parseURL(
      "https://timeline.test/timeline/entity/earth?compare=mars",
      "/timeline/",
    );
    expect(view.compare).toEqual(["earth", "mars"]);
    expect(serializeView(view, "/timeline/")).toMatch(/^\/timeline\/entity\/earth\?/);
  });
});

describe("entity slug boundary", () => {
  it("accepts emitted slug syntax, including negative-year disambiguators", () => {
    expect(isValidEntitySlug("tyrannosaurus-rex")).toBe(true);
    expect(isValidEntitySlug("mercury--1500")).toBe(true);
    expect(isValidEntitySlug("unknown-but-valid")).toBe(true);
  });

  it.each(["", "../x", "a/b", "a%2Fb", "Uppercase", "-edge", "edge-"])(
    "rejects %j",
    (slug) => expect(isValidEntitySlug(slug)).toBe(false),
  );

  it("does not retain invalid path, selection, or secondary slugs", () => {
    expect(parseView({ pathname: "/entity/../x", search: "?compare=mars" }).compare).toEqual([]);
    expect(parseView({ pathname: "/entity/a%2Fb", search: "?compare=mars" }).compare).toEqual([]);
    expect(parseURL("/?sel=..%2Fx").selected).toBeNull();
    expect(parseURL("/entity/earth?compare=..%2Fx")).toMatchObject({
      mode: "compare",
      compare: ["earth"],
    });
    expect(parseURL("/entity/earth?compare=mars,,venus")).toMatchObject({
      mode: "compare",
      compare: ["earth"],
    });
  });

  it("rejects malformed legacy lists instead of filtering empty entries", () => {
    expect(parseURL("/?cmp=earth,,mars")).toMatchObject({ mode: "compare", compare: [] });
    expect(parseURL("/?cmp=earth,mars,venus")).toMatchObject({ mode: "compare", compare: [] });
    expect(parseURL("/?cmp=earth,%2Fmars")).toMatchObject({ mode: "compare", compare: [] });
  });
});
