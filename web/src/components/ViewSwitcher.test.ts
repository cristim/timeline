import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { EntityDoc, PropertyClaim } from "../lib/data";
import type { EntityState } from "../lib/hooks";
import { yearToSeconds } from "../lib/keyscheme";
import type { PropertyDef } from "../lib/manifest";
import { CompareContent } from "./CompareShell";
import { ViewSwitcher } from "./ViewSwitcher";

const propertyDefs: Record<string, PropertyDef> = {
  mass: {
    label: "Mass",
    datatype: "number",
    canonical_unit: "kg",
    conversions: { kg: { scale: 1 }, g: { scale: 0.001 } },
  },
};

afterEach(() => vi.restoreAllMocks());

function claim(unit: string, valueType: PropertyClaim["value_type"], value?: number, range?: [number, number]): PropertyClaim {
  return {
    property: "mass",
    unit,
    value_type: valueType,
    value,
    min: range?.[0],
    max: range?.[1],
    source: "test",
    published_at: "2026",
  };
}

function entity(slug: string, name: string, claims?: PropertyClaim[]): EntityDoc {
  return {
    slug,
    name,
    type: "place",
    temporal: {
      t0: yearToSeconds(slug === "earth" ? 2000 : 2005),
      t1: yearToSeconds(slug === "earth" ? 2010 : 2015),
      precision: "year",
      status: "documented",
    },
    categories: ["earth"],
    importance: 1,
    properties: claims === undefined ? undefined : [{
      property: "mass",
      synthesis: { min: 0, max: 0, unit: "kg", claim_count: claims.length },
      claims,
    }],
    links: {},
  };
}

function state(slug: string | null, overrides: Partial<EntityState> = {}): EntityState {
  return { slug, doc: null, loading: false, missing: false, error: null, ...overrides };
}

function compare(subjects: string[], states: [EntityState, EntityState]): string {
  return renderToStaticMarkup(createElement(CompareContent, { subjects, states, propertyDefs }));
}

describe("ViewSwitcher", () => {
  it("enables Map and Compare and labels the remaining views unavailable", () => {
    const html = renderToStaticMarkup(createElement(ViewSwitcher, {
      active: "map",
      onSwitch: () => undefined,
    }));
    expect(html).toContain('aria-pressed="true"');
    expect(html).toContain(">Map<");
    expect(html).toContain(">Compare<");
    expect(html.match(/disabled=""/g)).toHaveLength(3);
    expect(html.match(/Unavailable/g)).toHaveLength(3);
  });
});

describe("CompareContent", () => {
  it("renders honest empty, pending, and loading states", () => {
    expect(compare([], [state(null), state(null)])).toContain("Choose a subject");
    expect(compare(["earth"], [state("earth", { doc: entity("earth", "Earth") }), state(null)]))
      .toContain("Search for a second subject");
    expect(compare(["earth", "mars"], [state("earth", { loading: true }), state("mars", { loading: true })]))
      .toContain("Loading earth");
  });

  it("renders identities, temporal output, canonical ranges, claim counts, and ordered badges", () => {
    const earth = entity("earth", "Earth", [
      claim("kg", "projected", undefined, [1, 3]),
      claim("kg", "measured", 2),
    ]);
    const mars = entity("mars", "Mars", [
      claim("g", "reconstructed", 1000),
      claim("g", "estimated", 1200),
    ]);
    const html = compare(["earth", "mars"], [
      state("earth", { doc: earth }),
      state("mars", { doc: mars }),
    ]);
    expect(html).toContain("Earth");
    expect(html).toContain("Mars");
    expect(html).toContain("overlap for 5 years");
    expect(html).toContain("1 – 3");
    expect(html).toContain(" kg");
    expect(html).toContain("2 claims");
    expect(html.indexOf("measured")).toBeLessThan(html.indexOf("projected"));
    expect(html.indexOf("estimated")).toBeLessThan(html.indexOf("reconstructed"));
    expect(html).toContain("cmp-span a");
    expect(html).toContain("cmp-span b");
    expect(html).toContain('role="img"');
    expect(html).toContain("Earth: 2000 – 2010, documented");
    expect(html).toContain("Temporal comparison of Earth and Mars: overlap for 5 years");
    expect(html).toContain("Mass for Earth: 1 – 3 kg, 2 claims, measured and projected");
  });

  it("renders canonical point values without range punctuation", () => {
    const html = compare(["earth", "mars"], [
      state("earth", { doc: entity("earth", "Earth", [claim("kg", "measured", 1)]) }),
      state("mars", { doc: entity("mars", "Mars", [claim("g", "measured", 1000)]) }),
    ]);
    expect(html.match(/cmp-val">1<span class="unit"> kg/g)).toHaveLength(2);
  });

  it("renders explicit gaps and unsupported units", () => {
    const gap = compare(["earth", "mars"], [
      state("earth", { doc: entity("earth", "Earth", [claim("kg", "measured", 1)]) }),
      state("mars", { doc: entity("mars", "Mars") }),
    ]);
    expect(gap).toContain("not recorded for Mars");

    const supportedAbsent = compare(["earth", "mars"], [
      state("earth", { doc: entity("earth", "Earth", [claim("kg", "measured")]) }),
      state("mars", { doc: entity("mars", "Mars", [claim("kg", "measured", 1)]) }),
    ]);
    expect(supportedAbsent).toContain("supported, no value");

    const unsupported = compare(["earth", "mars"], [
      state("earth", { doc: entity("earth", "Earth", [claim("lb", "measured", 1)]) }),
      state("mars", { doc: entity("mars", "Mars", [claim("kg", "measured", 1)]) }),
    ]);
    expect(unsupported).toContain("unsupported unit: lb");
  });

  it("distinguishes a side-specific 404 from an artifact schema failure", () => {
    expect(compare(["earth", "missing"], [
      state("earth", { doc: entity("earth", "Earth") }),
      state("missing", { missing: true }),
    ])).toContain("Second subject missing was not found");

    const malformed = entity("earth", "Earth", [claim(" kg ", "measured", 1)]);
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);
    expect(compare(["earth", "mars"], [
      state("earth", { doc: malformed }),
      state("mars", { doc: entity("mars", "Mars") }),
    ])).toContain("Comparison artifact error");

    expect(renderToStaticMarkup(createElement(CompareContent, {
      subjects: ["earth", "mars"],
      states: [state("earth", { doc: entity("earth", "Earth") }), state("mars", { doc: entity("mars", "Mars") })],
      propertyDefs: {},
    }))).toContain("missing property_def registry");
    expect(consoleError).toHaveBeenCalledTimes(2);
  });

  it("fails loud for a malformed pending entity", () => {
    const malformed = entity("earth", "Earth");
    malformed.temporal.t0 = Number.NaN;
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);
    expect(compare(["earth"], [state("earth", { doc: malformed }), state(null)]))
      .toContain("Comparison artifact error");
    expect(consoleError).toHaveBeenCalledOnce();
  });

  it.each([
    ["empty pending precision", "precision", "", false],
    ["padded pair precision", "precision", " year ", true],
    ["unknown pair precision", "precision", "fortnight", true],
    ["empty pair status", "status", "", true],
    ["padded pending status", "status", " documented ", false],
    ["unknown pair status", "status", "certain", true],
  ] as const)("rejects %s", (_name, field, value, pair) => {
    const malformed = entity("earth", "Earth");
    malformed.temporal[field] = value;
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const html = pair
      ? compare(["earth", "mars"], [
        state("earth", { doc: malformed }),
        state("mars", { doc: entity("mars", "Mars") }),
      ])
      : compare(["earth"], [state("earth", { doc: malformed }), state(null)]);
    expect(html).toContain("Comparison artifact error");
    expect(consoleError).toHaveBeenCalledOnce();
  });
});
