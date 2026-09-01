import { describe, expect, it } from "vitest";
import {
  alignProperties,
  compareBars,
  ComparisonArtifactError,
  MIN_BAR_FRACTION,
  temporalOverlap,
  verdict,
  type PropertyValueType,
} from "./compare";
import type { EntityDoc, PropertyClaim } from "./data";
import { yearToSeconds } from "./keyscheme";
import type { PropertyDef } from "./manifest";

const MA = (year: number) => yearToSeconds(year * 1e6);
const NOW = yearToSeconds(2026);
const TREX = { t0: MA(-68), t1: MA(-66) };
const STEGO = { t0: MA(-155), t1: MA(-145) };

const propertyDefs: Record<string, PropertyDef> = {
  length: {
    label: "Length",
    datatype: "number",
    canonical_unit: "m",
    conversions: { m: { scale: 1 } },
  },
  mass: {
    label: "Mass",
    datatype: "number",
    canonical_unit: "kg",
    conversions: { kg: { scale: 1 }, g: { scale: 0.001 } },
  },
  warming: {
    label: "Warming",
    datatype: "number",
    canonical_unit: "Cel",
    conversions: {
      Cel: { scale: 1 },
      degF: { scale: 5 / 9, offset: -160 / 9 },
    },
  },
  reverse: {
    label: "Reverse scale",
    datatype: "number",
    canonical_unit: "canonical",
    conversions: {
      canonical: { scale: 1 },
      inverted: { scale: -1 },
    },
  },
};

function doc(slug: string, properties: EntityDoc["properties"]): EntityDoc {
  return {
    slug,
    type: "species",
    name: slug,
    temporal: { t0: 0, t1: 1, precision: "year", status: "estimated" },
    categories: ["life"],
    importance: 0.5,
    properties,
    links: {},
  };
}

function claim(
  property: string,
  unit: string,
  valueType: PropertyValueType,
  value?: number,
  range?: [number, number],
): PropertyClaim {
  return {
    property,
    unit,
    value_type: valueType,
    value,
    min: range?.[0],
    max: range?.[1],
    source: "source",
    published_at: "2026",
  };
}

function property(property: string, claims: PropertyClaim[]) {
  return {
    property,
    synthesis: { min: 0, max: 0, unit: claims[0]?.unit, claim_count: claims.length },
    claims,
  };
}

describe("property alignment", () => {
  it("aligns registry properties with shared rows first and supported absences as gaps", () => {
    const a = doc("a", [
      property("mass", [claim("mass", "kg", "measured", 8_000)]),
      property("length", [claim("length", "m", "estimated", undefined, [6, 9])]),
    ]);
    const b = doc("b", [property("mass", [claim("mass", "kg", "estimated", 5_000)])]);

    const rows = alignProperties(a, b, propertyDefs);
    expect(rows.map((row) => row.property)).toEqual(["mass", "length"]);
    expect(rows[0]).toMatchObject({ label: "Mass", canonicalUnit: "kg", comparison: "comparable" });
    expect(rows[1]).toMatchObject({ comparison: "gap", b: null });
  });

  it("converts every declared unit to canonical and preserves every claim value type", () => {
    const a = doc("a", [
      property("mass", [
        claim("mass", "g", "measured", 8_000_000),
        claim("mass", "kg", "estimated", undefined, [7_500, 10_200]),
      ]),
    ]);
    const b = doc("b", [property("mass", [
      claim("mass", "kg", "reconstructed", 5_300),
      claim("mass", "kg", "projected", 5_300),
    ])]);

    const [row] = alignProperties(a, b, propertyDefs);
    expect(row.a).toEqual({
      status: "value",
      min: 7_500,
      max: 10_200,
      kind: "range",
      unit: "kg",
      claimCount: 2,
      valueTypes: ["measured", "estimated"],
    });
    expect(row.b).toMatchObject({ kind: "point", min: 5_300, max: 5_300 });
    expect(row.b?.valueTypes).toEqual(["reconstructed", "projected"]);
  });

  it("applies declared offsets as well as scales", () => {
    const fahrenheit = doc("f", [property("warming", [claim("warming", "degF", "measured", 32)])]);
    const celsius = doc("c", [property("warming", [claim("warming", "Cel", "measured", 0)])]);

    const [row] = alignProperties(fahrenheit, celsius, propertyDefs);
    expect(row.comparison).toBe("comparable");
    expect(row.a).toMatchObject({ status: "value", min: 0, max: 0, unit: "Cel" });
  });

  it("reorders converted bounds for a declared negative scale", () => {
    const inverted = doc("inverted", [
      property("reverse", [claim("reverse", "inverted", "measured", undefined, [1, 3])]),
    ]);
    const canonical = doc("canonical", [
      property("reverse", [claim("reverse", "canonical", "measured", undefined, [-3, -1])]),
    ]);

    const [row] = alignProperties(inverted, canonical, propertyDefs);
    expect(row.a).toMatchObject({ status: "value", min: -3, max: -1, kind: "range" });
  });

  it("keeps a property with no numeric claims as a supported gap", () => {
    const empty = doc("empty", [property("mass", [])]);
    const measured = doc("measured", [property("mass", [claim("mass", "kg", "measured", 1)])]);

    const [row] = alignProperties(empty, measured, propertyDefs);
    expect(row).toMatchObject({
      comparison: "gap",
      a: { status: "missing_value", claimCount: 0, valueTypes: [] },
    });
  });

  it("makes a valid but undeclared claim unit explicitly non-comparable", () => {
    const pounds = doc("pounds", [property("mass", [claim("mass", "lb", "measured", 10)])]);
    const kilograms = doc("kg", [property("mass", [claim("mass", "kg", "measured", 5)])]);

    const [row] = alignProperties(pounds, kilograms, propertyDefs);
    expect(row.comparison).toBe("unsupported_unit");
    expect(row.a).toEqual({
      status: "unsupported_unit",
      units: ["lb"],
      claimCount: 1,
      valueTypes: ["measured"],
    });
  });

  it("keeps mixed supported and unsupported claims non-comparable", () => {
    const mixed = doc("mixed", [property("mass", [
      claim("mass", "kg", "measured", 5),
      claim("mass", "lb", "estimated", 10),
    ])]);
    const kilograms = doc("kg", [property("mass", [claim("mass", "kg", "measured", 5)])]);

    const [row] = alignProperties(mixed, kilograms, propertyDefs);
    expect(row).toMatchObject({ comparison: "unsupported_unit" });
    expect(row.a).toEqual({
      status: "unsupported_unit",
      units: ["lb"],
      claimCount: 2,
      valueTypes: ["measured", "estimated"],
    });
  });

  it("fails loud for a missing registry or unknown property id", () => {
    const measured = doc("measured", [property("mass", [claim("mass", "kg", "measured", 1)])]);
    const unknown = doc("unknown", [property("weight", [claim("weight", "kg", "measured", 1)])]);

    expect(() => alignProperties(measured, null, undefined)).toThrow(ComparisonArtifactError);
    expect(() => alignProperties(measured, null, {})).toThrow("missing property_def registry");
    expect(() => alignProperties(unknown, null, propertyDefs)).toThrow('missing property_def "weight"');
  });

  it("fails loud for malformed claim ranges and duplicate property groups", () => {
    const partial = property("mass", [claim("mass", "kg", "measured")]);
    partial.claims[0].min = 1;
    const malformed = doc("malformed", [partial]);
    const duplicate = doc("duplicate", [
      property("mass", [claim("mass", "kg", "measured", 1)]),
      property("mass", [claim("mass", "kg", "estimated", 2)]),
    ]);

    expect(() => alignProperties(malformed, null, propertyDefs)).toThrow(ComparisonArtifactError);
    expect(() => alignProperties(duplicate, null, propertyDefs)).toThrow("duplicate property");

    const invalidType = claim("mass", "kg", "measured", 1);
    invalidType.value_type = "modeled" as PropertyValueType;
    expect(() => alignProperties(doc("invalid", [property("mass", [invalidType])]), null, propertyDefs))
      .toThrow("unknown value_type");
  });

  it("handles documents with no properties", () => {
    expect(alignProperties(doc("bare", undefined), null, propertyDefs)).toEqual([]);
  });

  it.each([
    ["null properties", { ...doc("bad", undefined), properties: null }],
    ["object properties", { ...doc("bad", undefined), properties: {} }],
    ["null property entry", { ...doc("bad", undefined), properties: [null] }],
    ["null claims", { ...doc("bad", undefined), properties: [{ ...property("mass", []), claims: null }] }],
    ["null claim entry", { ...doc("bad", undefined), properties: [{ ...property("mass", []), claims: [null] }] }],
    ["missing claim property", { ...doc("bad", undefined), properties: [property("mass", [{ ...claim("mass", "kg", "measured", 1), property: undefined }])] }],
    ["missing claim unit", { ...doc("bad", undefined), properties: [property("mass", [{ ...claim("mass", "kg", "measured", 1), unit: undefined }])] }],
    ["null claim unit", { ...doc("bad", undefined), properties: [property("mass", [{ ...claim("mass", "kg", "measured", 1), unit: null as unknown as string }])] }],
    ["blank claim unit", { ...doc("bad", undefined), properties: [property("mass", [claim("mass", "   ", "measured", 1)])] }],
    ["padded claim unit", { ...doc("bad", undefined), properties: [property("mass", [claim("mass", " kg ", "measured", 1)])] }],
  ])("fails loud for %s at the entity artifact boundary", (_name, malformed) => {
    expect(() => alignProperties(malformed as unknown as EntityDoc, null, propertyDefs))
      .toThrow(ComparisonArtifactError);
  });
});

describe("temporal overlap", () => {
  it("reports positive overlap and touching spans inclusively", () => {
    const shared = temporalOverlap(TREX, { t0: MA(-67), t1: MA(-65) }, NOW);
    expect(shared).toMatchObject({ overlaps: true, apartBy: 0 });
    expect(shared.sharedFor).toBeCloseTo(MA(-66) - MA(-67), 0);

    const touching = temporalOverlap({ t0: 0, t1: 100 }, { t0: 100, t1: 200 }, NOW);
    expect(touching).toMatchObject({ overlaps: true, sharedFor: 0 });
    expect(verdict(["a", "b"], touching)).toBe("meet at a single moment");
  });

  it("reports the gap and which disjoint subject is closer to now", () => {
    const overlap = temporalOverlap(TREX, STEGO, NOW);
    expect(overlap).toMatchObject({ overlaps: false, closerToNow: true, later: 0 });
    expect(overlap.apartBy).toBeCloseTo(MA(-68) - MA(-145), 0);
    expect(verdict(["T. rex", "Stegosaurus"], overlap)).toBe(
      "77 Myr apart · T. rex is closer to you (66 Myr) than to Stegosaurus",
    );
  });

  it("handles an open-through-present span and a wholly future span", () => {
    const ancient = { t0: yearToSeconds(-500), t1: yearToSeconds(-400) };
    const open = temporalOverlap(ancient, { t0: yearToSeconds(2000), t1: NOW }, NOW);
    expect(open.laterToNow).toBe(0);
    expect(verdict(["Ancient", "Open"], open)).toContain("Open spans the present");

    const future = temporalOverlap(
      { t0: yearToSeconds(1942.6), t1: yearToSeconds(1943.1) },
      { t0: yearToSeconds(2060), t1: yearToSeconds(2060) },
      NOW,
    );
    expect(future.laterToNow).toBeCloseTo(yearToSeconds(2060) - NOW, 0);
    expect(verdict(["Stalingrad", "Mars settlement"], future)).toBe(
      "117 years apart · Mars settlement is closer to you (34 years) than to Stalingrad",
    );
  });

  it("extends a real-shaped current entity through the supplied present", () => {
    const syntheticNow = yearToSeconds(2040.5);
    const ongoing = doc("ongoing", undefined);
    ongoing.temporal = {
      t0: yearToSeconds(-300_000),
      t1: yearToSeconds(2040),
      precision: "year",
      status: "documented",
    };
    const ancient = doc("ancient", undefined);
    ancient.temporal = {
      t0: yearToSeconds(-500_000),
      t1: yearToSeconds(-400_000),
      precision: "year",
      status: "documented",
    };

    const overlap = temporalOverlap(ancient.temporal, ongoing.temporal, syntheticNow);
    expect(overlap.laterToNow).toBe(0);
    expect(verdict([ancient.name, ongoing.name], overlap)).toContain("ongoing spans the present");
  });

  it("is stable when argument order is reversed", () => {
    const forward = verdict(["T. rex", "Stegosaurus"], temporalOverlap(TREX, STEGO, NOW));
    const reverse = verdict(["Stegosaurus", "T. rex"], temporalOverlap(STEGO, TREX, NOW));
    expect(reverse).toBe(forward);
  });

  it("reports when disjoint subjects are closer to each other than now", () => {
    const first = { t0: yearToSeconds(1914), t1: yearToSeconds(1918) };
    const second = { t0: yearToSeconds(1939), t1: yearToSeconds(1945) };
    expect(verdict(["WWI", "WWII"], temporalOverlap(first, second, NOW))).toContain(
      "closer to each other than to you",
    );
  });

  it("fails loud for inverted or non-finite spans", () => {
    expect(() => temporalOverlap({ t0: 2, t1: 1 }, { t0: 0, t1: 1 }, NOW)).toThrow(ComparisonArtifactError);
    expect(() => temporalOverlap({ t0: 0, t1: Number.NaN }, { t0: 0, t1: 1 }, NOW)).toThrow(ComparisonArtifactError);
  });

  it("fails loud for null and non-record spans", () => {
    expect(() => temporalOverlap(null as unknown as { t0: number; t1: number }, TREX, NOW))
      .toThrow(ComparisonArtifactError);
    expect(() => compareBars([] as unknown as { t0: number; t1: number }, TREX, NOW))
      .toThrow(ComparisonArtifactError);
  });

  it.each([
    ["shared duration", { t0: -Number.MAX_VALUE, t1: Number.MAX_VALUE }, { t0: -Number.MAX_VALUE, t1: Number.MAX_VALUE }, 0],
    ["gap", { t0: -Number.MAX_VALUE, t1: -Number.MAX_VALUE }, { t0: Number.MAX_VALUE, t1: Number.MAX_VALUE }, 0],
    ["distance to now", { t0: 0, t1: 0 }, { t0: Number.MAX_VALUE, t1: Number.MAX_VALUE }, -Number.MAX_VALUE],
  ])("fails loud when derived %s is non-finite", (_name, first, second, now) => {
    expect(() => temporalOverlap(first, second, now)).toThrow(ComparisonArtifactError);
  });
});

describe("overlap bars", () => {
  it("puts both spans on one padded axis and excludes distant now", () => {
    const bars = compareBars(TREX, STEGO, NOW);
    expect(bars.from).toBeLessThan(STEGO.t0);
    expect(bars.to).toBeGreaterThan(TREX.t1);
    expect(bars.b.left).toBeLessThan(bars.a.left);
    expect(bars.nowAt).toBeNull();
  });

  it("marks now when it is on the axis", () => {
    const bars = compareBars(
      { t0: yearToSeconds(1939), t1: yearToSeconds(1945) },
      { t0: yearToSeconds(2000), t1: NOW },
      NOW,
    );
    expect(bars.nowAt).not.toBeNull();
    expect(bars.nowAt!).toBeGreaterThan(0.9);
  });

  it("keeps moments visible and centered", () => {
    const moment = { t0: yearToSeconds(1969), t1: yearToSeconds(1969) };
    const bars = compareBars({ t0: yearToSeconds(-3000), t1: yearToSeconds(2000) }, moment, NOW);
    const trueAt = (moment.t0 - bars.from) / (bars.to - bars.from);
    expect(bars.b.width).toBe(MIN_BAR_FRACTION);
    expect(bars.b.left + bars.b.width / 2).toBeCloseTo(trueAt, 6);
  });

  it("gives identical moments a finite axis", () => {
    const moment = { t0: 1000, t1: 1000 };
    const bars = compareBars(moment, moment, NOW);
    expect(bars.to).toBeGreaterThan(bars.from);
    expect(Number.isFinite(bars.a.left)).toBe(true);
    expect(bars.a.width).toBe(MIN_BAR_FRACTION);
  });

  it("gives identical deep-future moments a representable axis", () => {
    const moment = { t0: 3.2e107, t1: 3.2e107 };
    const bars = compareBars(moment, moment, NOW);
    expect(bars.to).toBeGreaterThan(bars.from);
    expect(Number.isFinite(bars.a.left)).toBe(true);
  });
});
