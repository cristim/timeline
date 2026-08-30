// FE-8 compare view, the arithmetic half: align two entity documents by
// property name, and work out how their lifespans sit relative to each other
// and to now. Everything here is pure so the panel only renders verdicts it
// was given, and so the honesty rules (a gap is a gap, mismatched units are
// not comparable) are testable.
import type { EntityDoc } from "./data";
import { formatDuration } from "./timefmt";

/** One entity's synthesized value for a property (DM-5). */
export interface PropertySide {
  min: number;
  max: number;
  unit?: string;
  claimCount: number;
}

export interface AlignedProperty {
  property: string;
  /** null when this entity has no claim for the property: a gap, not a zero. */
  a: PropertySide | null;
  b: PropertySide | null;
  /** Both sides present but stated in different units, so the pair of numbers
   *  must not be read as a comparison. */
  unitMismatch: boolean;
}

/** A time span; both entity docs and chunk items expose one. */
export interface Span {
  t0: number;
  t1: number;
}

export interface Overlap {
  overlaps: boolean;
  /** Length of the shared interval; 0 when disjoint or merely touching. */
  sharedFor: number;
  /** Empty time between the two spans; 0 when they overlap. */
  apartBy: number;
  /**
   * How far the later span is from now: how long ago it ended, or how far
   * ahead it starts. 0 when it contains the present - a projected settlement in
   * 2060 is not "at" the present just because it has not ended.
   */
  laterToNow: number;
  /** Which of the two spans ends later: 0 for the first, 1 for the second. */
  later: 0 | 1;
  /**
   * The spec's "closer to you than to each other": the later entity ended
   * nearer to now than to the earlier entity. Only meaningful when disjoint.
   */
  closerToNow: boolean;
}

/** One entity's bar on the shared axis, as fractions of the axis width. */
export interface CompareBar {
  left: number;
  width: number;
}

export interface BarLayout {
  from: number;
  to: number;
  a: CompareBar;
  b: CompareBar;
  /** Where now sits on the axis, or null when now is off it. */
  nowAt: number | null;
}

/**
 * A moment would otherwise be a zero-width bar. Deep-time comparisons put whole
 * species inside a fraction of a percent, so the floor is what keeps a subject
 * of the comparison from disappearing out of it.
 */
export const MIN_BAR_FRACTION = 0.004;

/** Padding at each end of the shared axis, as a fraction of the span. */
const AXIS_PAD = 0.04;

export function alignProperties(a: EntityDoc | null, b: EntityDoc | null): AlignedProperty[] {
  const sideA = sides(a);
  const sideB = sides(b);
  const names = [...new Set([...sideA.keys(), ...sideB.keys()])];
  return names
    .map((property) => {
      const pa = sideA.get(property) ?? null;
      const pb = sideB.get(property) ?? null;
      return {
        property,
        a: pa,
        b: pb,
        unitMismatch: !!pa && !!pb && (pa.unit ?? "") !== (pb.unit ?? ""),
      };
    })
    // Properties both sides claim are the actual comparison; the one-sided
    // rows are still shown, below, because a missing measurement is a fact.
    .sort(
      (x, y) =>
        Number(!!y.a && !!y.b) - Number(!!x.a && !!x.b) ||
        x.property.localeCompare(y.property),
    );
}

function sides(doc: EntityDoc | null): Map<string, PropertySide> {
  return new Map(
    (doc?.properties ?? []).map((p) => [
      p.property,
      {
        min: p.synthesis.min,
        max: p.synthesis.max,
        unit: p.synthesis.unit,
        claimCount: p.synthesis.claim_count,
      },
    ]),
  );
}

export function temporalOverlap(a: Span, b: Span, now: number): Overlap {
  const start = Math.max(a.t0, b.t0);
  const end = Math.min(a.t1, b.t1);
  const overlaps = end >= start;
  const later: 0 | 1 = a.t1 >= b.t1 ? 0 : 1;
  const laterSpan = later === 0 ? a : b;
  const laterToNow =
    laterSpan.t0 > now ? laterSpan.t0 - now : Math.max(0, now - laterSpan.t1);
  const apartBy = overlaps ? 0 : start - end;
  return {
    overlaps,
    sharedFor: overlaps ? end - start : 0,
    apartBy,
    laterToNow,
    later,
    closerToNow: !overlaps && laterToNow < apartBy,
  };
}

/**
 * The sentence the compare view leads with - FE-8's "T. rex and Stegosaurus:
 * ✗, closer to you than to each other". A gap is always stated as a gap, and
 * the reference point for "you" is stated with it, so the reader never has to
 * infer the scale from the picture.
 */
export function verdict(names: [string, string], o: Overlap): string {
  if (o.overlaps) {
    return o.sharedFor > 0
      ? `overlap for ${formatDuration(o.sharedFor)}`
      : "meet at a single moment";
  }
  const later = names[o.later];
  const earlier = names[1 - o.later];
  const apart = `${formatDuration(o.apartBy)} apart`;
  if (o.laterToNow === 0) return `${apart} · ${later} spans the present`;
  return o.closerToNow
    ? `${apart} · ${later} is closer to you (${formatDuration(o.laterToNow)}) than to ${earlier}`
    : `${apart} · closer to each other than to you (${formatDuration(o.laterToNow)})`;
}

/**
 * The shared axis both lifespans are drawn on. It spans the two entities, not
 * the two entities plus now: stretching a 1940s comparison to the present would
 * squash both bars into a sliver. Now is marked when it happens to fall on the
 * axis, and stated in words by the verdict when it does not.
 */
export function compareBars(a: Span, b: Span, now: number): BarLayout {
  const lo = Math.min(a.t0, b.t0);
  const hi = Math.max(a.t1, b.t1);
  // Two instants at the same moment have no span to pad; give the axis a
  // nominal second so both dots land in the middle instead of dividing by zero.
  const pad = hi > lo ? (hi - lo) * AXIS_PAD : 1;
  const from = lo - pad;
  const to = hi + pad;
  const width = to - from;
  return {
    from,
    to,
    a: bar(a, from, width),
    b: bar(b, from, width),
    nowAt: now >= from && now <= to ? (now - from) / width : null,
  };
}

function bar(s: Span, from: number, width: number): CompareBar {
  const true0 = (s.t0 - from) / width;
  const trueW = (s.t1 - s.t0) / width;
  const w = Math.max(trueW, MIN_BAR_FRACTION);
  // A bar widened to the visible floor grows both ways from where the span
  // really sits, so it never reads as having ended later than it did.
  const left = true0 - (w - trueW) / 2;
  return { left: Math.max(0, Math.min(left, 1 - w)), width: w };
}
