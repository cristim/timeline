import type { EntityDoc } from "./data";
import type { PropertyDef } from "./manifest";
import { formatDuration, isOngoing } from "./timefmt";

export class ComparisonArtifactError extends Error {
  constructor(message: string) {
    super(`compare artifact: ${message}`);
    this.name = "ComparisonArtifactError";
  }
}

export type PropertyValueType = "measured" | "estimated" | "reconstructed" | "projected";

interface CompareClaim {
  property: string;
  unit: string;
  value_type: string;
  value?: unknown;
  min?: unknown;
  max?: unknown;
}

export interface CanonicalPropertySide {
  status: "value";
  min: number;
  max: number;
  kind: "point" | "range";
  unit: string;
  claimCount: number;
  valueTypes: PropertyValueType[];
}

export interface UnsupportedPropertySide {
  status: "unsupported_unit";
  units: string[];
  claimCount: number;
  valueTypes: PropertyValueType[];
}

export interface MissingPropertySide {
  status: "missing_value";
  claimCount: number;
  valueTypes: PropertyValueType[];
}

export type PropertySide = CanonicalPropertySide | UnsupportedPropertySide | MissingPropertySide;

export interface AlignedProperty {
  property: string;
  label: string;
  canonicalUnit: string;
  a: PropertySide | null;
  b: PropertySide | null;
  comparison: "comparable" | "gap" | "unsupported_unit";
}

export interface Span {
  t0: number;
  t1: number;
}

export interface Overlap {
  overlaps: boolean;
  sharedFor: number;
  apartBy: number;
  laterToNow: number;
  later: 0 | 1;
  closerToNow: boolean;
}

export interface CompareBar {
  left: number;
  width: number;
}

export interface BarLayout {
  from: number;
  to: number;
  a: CompareBar;
  b: CompareBar;
  nowAt: number | null;
}

export const MIN_BAR_FRACTION = 0.004;
const AXIS_PAD = 0.04;
const PROPERTY_VALUE_TYPES = new Set<PropertyValueType>([
  "measured",
  "estimated",
  "reconstructed",
  "projected",
]);

function isPropertyValueType(value: string): value is PropertyValueType {
  return PROPERTY_VALUE_TYPES.has(value as PropertyValueType);
}

export function alignProperties(
  a: EntityDoc | null,
  b: EntityDoc | null,
  definitions: Record<string, PropertyDef> | undefined,
): AlignedProperty[] {
  if (
    !definitions ||
    typeof definitions !== "object" ||
    Array.isArray(definitions) ||
    Object.keys(definitions).length === 0
  ) {
    throw new ComparisonArtifactError("missing property_def registry");
  }
  const sideA = propertySides(a, definitions);
  const sideB = propertySides(b, definitions);
  const properties = [...new Set([...sideA.keys(), ...sideB.keys()])];

  return properties
    .map((property) => {
      const definition = definitions[property];
      if (!definition) throw new ComparisonArtifactError(`missing property_def ${JSON.stringify(property)}`);
      const valueA = sideA.get(property) ?? null;
      const valueB = sideB.get(property) ?? null;
      const comparison = valueA?.status === "unsupported_unit" || valueB?.status === "unsupported_unit"
        ? "unsupported_unit"
        : valueA === null || valueB === null || valueA.status === "missing_value" || valueB.status === "missing_value"
          ? "gap"
          : "comparable";
      return {
        property,
        label: definition.label,
        canonicalUnit: definition.canonical_unit,
        a: valueA,
        b: valueB,
        comparison,
      } satisfies AlignedProperty;
    })
    .sort(
      (x, y) =>
        Number(hasComparableSides(y)) - Number(hasComparableSides(x)) ||
        x.property.localeCompare(y.property),
    );
}

function hasComparableSides(row: AlignedProperty): boolean {
  return row.a !== null && row.b !== null &&
    row.a.status !== "missing_value" && row.b.status !== "missing_value";
}

function propertySides(
  doc: EntityDoc | null,
  definitions: Record<string, PropertyDef>,
): Map<string, PropertySide> {
  const out = new Map<string, PropertySide>();
  if (doc === null) return out;
  const rawDoc = artifactRecord(doc, "entity");
  if (rawDoc.properties === undefined) return out;
  if (!Array.isArray(rawDoc.properties)) {
    throw new ComparisonArtifactError("entity properties must be an array");
  }

  for (let index = 0; index < rawDoc.properties.length; index++) {
    const rawProperty = artifactRecord(rawDoc.properties[index], `entity properties[${index}]`);
    const property = requiredString(rawProperty.property, `entity properties[${index}].property`);
    if (out.has(property)) {
      throw new ComparisonArtifactError(`duplicate property ${JSON.stringify(property)}`);
    }
    const definition = definitions[property];
    if (!definition) {
      throw new ComparisonArtifactError(`missing property_def ${JSON.stringify(property)}`);
    }
    if (!Array.isArray(rawProperty.claims)) {
      throw new ComparisonArtifactError(`property ${JSON.stringify(property)} claims must be an array`);
    }
    const claims = rawProperty.claims.map((claim, claimIndex) =>
      compareClaim(claim, `property ${JSON.stringify(property)} claims[${claimIndex}]`));
    out.set(property, canonicalSide(property, claims, definition));
  }
  return out;
}

function compareClaim(value: unknown, field: string): CompareClaim {
  const claim = artifactRecord(value, field);
  return {
    property: requiredString(claim.property, `${field}.property`),
    unit: exactToken(claim.unit, `${field}.unit`),
    value_type: requiredString(claim.value_type, `${field}.value_type`),
    value: claim.value,
    min: claim.min,
    max: claim.max,
  };
}

function canonicalSide(
  property: string,
  claims: CompareClaim[],
  definition: PropertyDef,
): PropertySide {
  const valueTypes: PropertyValueType[] = [];
  const unsupportedUnits = new Set<string>();
  let canonicalMin = Number.POSITIVE_INFINITY;
  let canonicalMax = Number.NEGATIVE_INFINITY;
  let values = 0;

  for (const claim of claims) {
    if (!isPropertyValueType(claim.value_type)) {
      throw new ComparisonArtifactError(
        `property ${JSON.stringify(property)} has unknown value_type ${JSON.stringify(claim.value_type)}`,
      );
    }
    valueTypes.push(claim.value_type);
    if (claim.property !== undefined && claim.property !== property) {
      throw new ComparisonArtifactError(
        `property ${JSON.stringify(property)} contains claim for ${JSON.stringify(claim.property)}`,
      );
    }

    const range = claimRange(property, claim);
    if (!range) continue;
    const unit = claim.unit ?? "";
    const conversion = definition.conversions[unit];
    if (!conversion) {
      unsupportedUnits.add(unit);
      continue;
    }
    const first = convert(range[0], conversion.scale, conversion.offset ?? 0, property);
    const second = convert(range[1], conversion.scale, conversion.offset ?? 0, property);
    canonicalMin = Math.min(canonicalMin, first, second);
    canonicalMax = Math.max(canonicalMax, first, second);
    values++;
  }

  if (unsupportedUnits.size > 0) {
    return {
      status: "unsupported_unit",
      units: [...unsupportedUnits].sort(),
      claimCount: claims.length,
      valueTypes,
    };
  }
  if (values === 0) {
    return { status: "missing_value", claimCount: claims.length, valueTypes };
  }
  return {
    status: "value",
    min: canonicalMin,
    max: canonicalMax,
    kind: canonicalMin === canonicalMax ? "point" : "range",
    unit: definition.canonical_unit,
    claimCount: claims.length,
    valueTypes,
  };
}

function claimRange(property: string, claim: CompareClaim): [number, number] | null {
  const hasValue = claim.value !== undefined;
  const hasMin = claim.min !== undefined;
  const hasMax = claim.max !== undefined;
  if (hasMin !== hasMax) {
    throw new ComparisonArtifactError(`property ${JSON.stringify(property)} has a partial claim range`);
  }
  if (!hasValue && !hasMin) return null;

  const min = hasMin ? finiteNumber(claim.min, `${property} claim minimum`) : finiteNumber(claim.value, `${property} claim value`);
  const max = hasMax ? finiteNumber(claim.max, `${property} claim maximum`) : min;
  if (max < min) {
    throw new ComparisonArtifactError(`property ${JSON.stringify(property)} has an inverted claim range`);
  }
  return [min, max];
}

function finiteNumber(value: unknown, field: string): number {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    throw new ComparisonArtifactError(`${field} must be a finite number`);
  }
  return value;
}

function artifactRecord(value: unknown, field: string): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new ComparisonArtifactError(`${field} must be an object`);
  }
  return value as Record<string, unknown>;
}

function requiredString(value: unknown, field: string): string {
  if (typeof value !== "string" || value.length === 0) {
    throw new ComparisonArtifactError(`${field} must be a non-empty string`);
  }
  return value;
}

function exactToken(value: unknown, field: string): string {
  const token = requiredString(value, field);
  if (token.trim() !== token) {
    throw new ComparisonArtifactError(`${field} must not contain surrounding whitespace`);
  }
  return token;
}

function convert(value: number, scale: number, offset: number, property: string): number {
  const canonical = value * scale + offset;
  if (!Number.isFinite(canonical)) {
    throw new ComparisonArtifactError(`property ${JSON.stringify(property)} conversion is not finite`);
  }
  return canonical;
}

export function temporalOverlap(a: Span, b: Span, now: number): Overlap {
  finiteNumber(now, "present time");
  const first = compareSpan(a, "first span", now);
  const second = compareSpan(b, "second span", now);

  const start = Math.max(first.t0, second.t0);
  const end = Math.min(first.t1, second.t1);
  const overlaps = end >= start;
  const later: 0 | 1 = first.t1 >= second.t1 ? 0 : 1;
  const laterSpan = later === 0 ? first : second;
  const sharedFor = finiteNumber(overlaps ? end - start : 0, "shared duration");
  const apartBy = finiteNumber(overlaps ? 0 : start - end, "span gap");
  const laterToNow = finiteNumber(
    laterSpan.t0 > now ? laterSpan.t0 - now : Math.max(0, now - laterSpan.t1),
    "distance to present",
  );
  return {
    overlaps,
    sharedFor,
    apartBy,
    laterToNow,
    later,
    closerToNow: !overlaps && laterToNow < apartBy,
  };
}

export function verdict(names: [string, string], overlap: Overlap): string {
  if (overlap.overlaps) {
    return overlap.sharedFor > 0
      ? `overlap for ${formatDuration(overlap.sharedFor)}`
      : "meet at a single moment";
  }
  const later = names[overlap.later];
  const earlier = names[1 - overlap.later];
  const apart = `${formatDuration(overlap.apartBy)} apart`;
  if (overlap.laterToNow === 0) return `${apart} · ${later} spans the present`;
  return overlap.closerToNow
    ? `${apart} · ${later} is closer to you (${formatDuration(overlap.laterToNow)}) than to ${earlier}`
    : `${apart} · closer to each other than to you (${formatDuration(overlap.laterToNow)})`;
}

export function compareBars(a: Span, b: Span, now: number): BarLayout {
  finiteNumber(now, "present time");
  const first = compareSpan(a, "first span", now);
  const second = compareSpan(b, "second span", now);

  const lo = Math.min(first.t0, second.t0);
  const hi = Math.max(first.t1, second.t1);
  const span = hi - lo;
  if (!Number.isFinite(span)) throw new ComparisonArtifactError("comparison axis span must be finite");
  const numericPad = Math.max(Math.abs(lo), Math.abs(hi), 1) * Number.EPSILON * 4;
  const pad = Math.max(span * AXIS_PAD, numericPad, span === 0 ? 1 : 0);
  const from = lo - pad;
  const to = hi + pad;
  const width = to - from;
  if (![from, to, width].every(Number.isFinite) || width <= 0) {
    throw new ComparisonArtifactError("comparison axis must be finite and positive");
  }
  return {
    from,
    to,
    a: bar(first, from, width),
    b: bar(second, from, width),
    nowAt: now >= from && now <= to ? (now - from) / width : null,
  };
}

function compareSpan(value: unknown, field: string, now: number): Span {
  const span = artifactRecord(value, field);
  const t0 = finiteNumber(span.t0, `${field}.t0`);
  let t1 = finiteNumber(span.t1, `${field}.t1`);
  if (t1 < t0) throw new ComparisonArtifactError(`${field} is inverted`);
  if (t0 <= now && t1 < now && isOngoing(t1, now)) t1 = now;
  return { t0, t1 };
}

function bar(span: Span, from: number, width: number): CompareBar {
  const trueLeft = (span.t0 - from) / width;
  const trueWidth = (span.t1 - span.t0) / width;
  const visibleWidth = Math.max(trueWidth, MIN_BAR_FRACTION);
  const left = trueLeft - (visibleWidth - trueWidth) / 2;
  return {
    left: Math.max(0, Math.min(left, 1 - visibleWidth)),
    width: visibleWidth,
  };
}
