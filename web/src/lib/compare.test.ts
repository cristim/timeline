// FE-8: the compare view's claims are arithmetic, so they are tested as
// arithmetic - the spec's own example ("T. rex and Stegosaurus: ✗, closer to
// you than to each other") is the headline case.
import { describe, expect, it } from "vitest";
import {
  alignProperties,
  compareBars,
  MIN_BAR_FRACTION,
  temporalOverlap,
  verdict,
} from "./compare";
import type { EntityDoc } from "./data";
import { yearToSeconds } from "./keyscheme";

const MA = (y: number) => yearToSeconds(y * 1e6);
const NOW = yearToSeconds(2026);

const TREX = { t0: MA(-68), t1: MA(-66) };
const STEGO = { t0: MA(-155), t1: MA(-145) };
const TRICERATOPS = { t0: MA(-68), t1: MA(-66) };

function doc(slug: string, props: EntityDoc["properties"]): EntityDoc {
  return {
    slug,
    type: "species",
    name: slug,
    temporal: { t0: 0, t1: 1, precision: "year", status: "estimated" },
    categories: ["life"],
    importance: 0.5,
    properties: props,
    links: {},
  };
}

function prop(property: string, min: number, max: number, unit?: string, claims = 1) {
  return { property, synthesis: { min, max, unit, claim_count: claims }, claims: [] };
}

describe("temporal overlap", () => {
  it("reports the shared interval when two lifespans overlap", () => {
    const o = temporalOverlap(TREX, TRICERATOPS, NOW);
    expect(o.overlaps).toBe(true);
    expect(o.sharedFor).toBeCloseTo(MA(-66) - MA(-68), 0);
    expect(o.apartBy).toBe(0);
  });

  it("reports the gap, and that T. rex is closer to you than to Stegosaurus", () => {
    const o = temporalOverlap(TREX, STEGO, NOW);
    expect(o.overlaps).toBe(false);
    // 145 Ma to 68 Ma of empty time, against 66 Myr from T. rex to now.
    expect(o.apartBy).toBeCloseTo(MA(-68) - MA(-145), 0);
    expect(o.laterToNow).toBeCloseTo(NOW - MA(-66), 0);
    expect(o.closerToNow).toBe(true);
    expect(o.later).toBe(0);
    expect(verdict(["T. rex", "Stegosaurus"], o)).toBe(
      "77 Myr apart · T. rex is closer to you (66 Myr) than to Stegosaurus",
    );
  });

  it("says so when the pair is nearer to each other than to the present", () => {
    const wwii = { t0: yearToSeconds(1939), t1: yearToSeconds(1945) };
    const wwi = { t0: yearToSeconds(1914), t1: yearToSeconds(1918) };
    const o = temporalOverlap(wwi, wwii, NOW);
    expect(o.closerToNow).toBe(false);
    expect(o.later).toBe(1);
    expect(verdict(["WWI", "WWII"], o)).toContain("closer to each other than to you");
  });

  it("treats a span containing the present as spanning it, not as a distance", () => {
    const ongoing = { t0: yearToSeconds(2000), t1: NOW + 1000 };
    const ancient = { t0: yearToSeconds(-500), t1: yearToSeconds(-400) };
    const o = temporalOverlap(ancient, ongoing, NOW);
    expect(o.laterToNow).toBe(0);
    expect(verdict(["Ancient", "Ongoing"], o)).toContain("Ongoing spans the present");
  });

  it("measures a still-future subject forwards, not as if it were here", () => {
    const stalingrad = { t0: yearToSeconds(1942.6), t1: yearToSeconds(1943.1) };
    const mars = { t0: yearToSeconds(2060), t1: yearToSeconds(2060) };
    const o = temporalOverlap(stalingrad, mars, NOW);
    expect(o.laterToNow).toBeCloseTo(yearToSeconds(2060) - NOW, 0);
    expect(o.closerToNow).toBe(true);
    expect(verdict(["Stalingrad", "Mars settlement"], o)).toBe(
      "117 years apart · Mars settlement is closer to you (34 years) than to Stalingrad",
    );
  });

  it("counts touching spans as an overlap of no duration", () => {
    const o = temporalOverlap({ t0: 0, t1: 100 }, { t0: 100, t1: 200 }, NOW);
    expect(o.overlaps).toBe(true);
    expect(o.sharedFor).toBe(0);
    expect(verdict(["a", "b"], o)).toBe("meet at a single moment");
  });

  it("does not depend on argument order for the verdict", () => {
    const forward = verdict(["T. rex", "Stegosaurus"], temporalOverlap(TREX, STEGO, NOW));
    const reversed = verdict(["Stegosaurus", "T. rex"], temporalOverlap(STEGO, TREX, NOW));
    expect(reversed).toBe(forward);
  });
});

describe("overlap bars", () => {
  it("puts both lifespans on one axis spanning them", () => {
    const bars = compareBars(TREX, STEGO, NOW);
    expect(bars.from).toBeLessThan(STEGO.t0);
    expect(bars.to).toBeGreaterThan(TREX.t1);
    expect(bars.b.left).toBeLessThan(bars.a.left);
    expect(bars.a.left + bars.a.width).toBeLessThanOrEqual(1);
    expect(bars.b.left + bars.b.width).toBeLessThanOrEqual(1);
    // Deep time is nowhere near the present, so the axis cannot mark now.
    expect(bars.nowAt).toBeNull();
  });

  it("marks now when it falls on the axis", () => {
    const bars = compareBars(
      { t0: yearToSeconds(1939), t1: yearToSeconds(1945) },
      { t0: yearToSeconds(2000), t1: NOW },
      NOW,
    );
    expect(bars.nowAt).not.toBeNull();
    expect(bars.nowAt!).toBeGreaterThan(0.9);
  });

  it("keeps a moment visible, centred on when it happened", () => {
    const moment = { t0: yearToSeconds(1969), t1: yearToSeconds(1969) };
    const bars = compareBars({ t0: yearToSeconds(-3000), t1: yearToSeconds(2000) }, moment, NOW);
    expect(bars.b.width).toBe(MIN_BAR_FRACTION);
    expect(bars.b.left + bars.b.width).toBeLessThanOrEqual(1);
    // Widened for visibility, not stretched into the future: the true instant
    // sits at the middle of the drawn bar, not at its left edge.
    const trueAt = (yearToSeconds(1969) - bars.from) / (bars.to - bars.from);
    expect(bars.b.left + bars.b.width / 2).toBeCloseTo(trueAt, 6);
  });

  it("gives two identical moments an axis rather than dividing by zero", () => {
    const m = { t0: 1000, t1: 1000 };
    const bars = compareBars(m, m, NOW);
    expect(bars.to).toBeGreaterThan(bars.from);
    expect(Number.isFinite(bars.a.left)).toBe(true);
    expect(bars.a.width).toBe(MIN_BAR_FRACTION);
  });
});

describe("property alignment", () => {
  const rex = doc("rex", [prop("mass", 8000, 10200, "kg", 2), prop("max_speed", 18, 29, "km/h", 2)]);
  const stego = doc("stego", [prop("mass", 3500, 5300, "kg"), prop("length", 6.5, 9, "m")]);

  it("aligns shared properties first and keeps one-sided ones below", () => {
    const rows = alignProperties(rex, stego);
    expect(rows.map((r) => r.property)).toEqual(["mass", "length", "max_speed"]);
    expect(rows[0].a?.max).toBe(10200);
    expect(rows[0].b?.max).toBe(5300);
  });

  it("reports a missing property as absent rather than as a value", () => {
    const rows = alignProperties(rex, stego);
    const speed = rows.find((r) => r.property === "max_speed")!;
    expect(speed.a).not.toBeNull();
    expect(speed.b).toBeNull();
  });

  it("flags mismatched units as not comparable", () => {
    const tonnes = doc("t", [prop("mass", 8, 10, "t")]);
    expect(alignProperties(rex, tonnes)[0].unitMismatch).toBe(true);
    expect(alignProperties(rex, stego)[0].unitMismatch).toBe(false);
  });

  it("handles an entity with no properties at all", () => {
    expect(alignProperties(doc("bare", undefined), null)).toEqual([]);
    expect(alignProperties(null, stego).map((r) => r.property)).toEqual(["length", "mass"]);
  });
});
