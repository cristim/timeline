import type { EntityDoc } from "../lib/data";
import {
  alignProperties,
  compareBars,
  ComparisonArtifactError,
  temporalOverlap,
  verdict,
  type AlignedProperty,
  type PropertySide,
} from "../lib/compare";
import { markerStyle } from "../lib/colors";
import type { EntityState } from "../lib/hooks";
import type { Manifest } from "../lib/manifest";
import { fmtScaled, scaleFor, type Scale } from "../lib/numfmt";
import { formatRange, formatTime, NOW_S } from "../lib/timefmt";

interface ViewProps {
  subjects: string[];
  manifest: Manifest;
  states: [EntityState, EntityState];
}

const VALUE_TYPE_ORDER = ["measured", "estimated", "reconstructed", "projected"] as const;
// Mirrors the closed schemas in internal/model/entity.go and time.go.
const TEMPORAL_PRECISIONS = new Set([
  "billion_year", "million_year", "millennium", "century", "decade", "year",
  "month", "day", "hour", "minute", "second",
]);
const TEMPORAL_STATUSES = new Set([
  "observed", "documented", "estimated", "projected", "model_dependent",
  "speculative", "legendary", "disputed",
]);

export function CompareView({ subjects, manifest, states }: ViewProps) {
  return <CompareContent subjects={subjects} propertyDefs={manifest.property_def} states={states} />;
}

export function CompareContent({ subjects, propertyDefs, states }: {
  subjects: string[];
  propertyDefs: Manifest["property_def"];
  states: [EntityState, EntityState];
}) {
  let content;
  if (subjects.length === 0) {
    content = <p>Choose a subject in the Inspector, then choose Compare.</p>;
  } else if (subjects.length === 1) {
    content = stateMessage(states[0], 0) ?? pendingContent(subjects[0], states[0].doc, propertyDefs);
  } else {
    content = stateMessage(states[0], 0) ?? stateMessage(states[1], 1);
    if (!content && states[0].doc && states[1].doc) {
      try {
        content = pairContent(subjects, states[0].doc, states[1].doc, propertyDefs);
      } catch (error) {
        console.error("comparison artifact failed:", error);
        content = <ArtifactError error={error} />;
      }
    } else if (!content) {
      content = <p>Loading comparison…</p>;
    }
  }

  return (
    <section className="compare-shell" aria-label="Compare">
      <h2>Compare</h2>
      <div className="compare-body">{content}</div>
    </section>
  );
}

function pendingContent(
  subject: string,
  doc: EntityDoc | null,
  propertyDefs: Manifest["property_def"],
) {
  if (!doc) return <p>Loading {subject}…</p>;
  try {
    const name = subjectName(doc, subject, "first");
    temporalOverlap(doc.temporal, doc.temporal, NOW_S);
    alignProperties(doc, null, propertyDefs);
    return <p>Comparing <b>{name}</b>. Search for a second subject.</p>;
  } catch (error) {
    console.error("comparison artifact failed:", error);
    return <ArtifactError error={error} />;
  }
}

function stateMessage(state: EntityState, index: 0 | 1) {
  const side = index === 0 ? "First" : "Second";
  if (state.missing) return <p className="cmp-missing">{side} subject {state.slug} was not found.</p>;
  if (state.error) return <ArtifactError error={state.error} side={side} />;
  if (state.loading) return <p>Loading {state.slug}…</p>;
  if (state.slug && !state.doc) {
    return <ArtifactError error={`entity ${JSON.stringify(state.slug)} returned no document`} side={side} />;
  }
  return null;
}

function ArtifactError({ error, side }: { error: unknown; side?: string }) {
  const message = error instanceof Error ? error.message : String(error);
  return (
    <p className="cmp-artifact-error">
      {side ? `${side} subject artifact error: ` : "Comparison artifact error: "}{message}
    </p>
  );
}

function pairContent(
  subjects: string[],
  a: EntityDoc,
  b: EntityDoc,
  propertyDefs: Manifest["property_def"],
) {
  const aName = subjectName(a, subjects[0], "first");
  const bName = subjectName(b, subjects[1], "second");
  const overlap = temporalOverlap(a.temporal, b.temporal, NOW_S);
  const bars = compareBars(a.temporal, b.temporal, NOW_S);
  const rows = alignProperties(a, b, propertyDefs);
  const axisSpan = bars.to - bars.from;

  return (
    <>
      <div className="cmp-identities"><b>{aName}</b><span>compared with</span><b>{bName}</b></div>
      <p className="cmp-verdict">{verdict([aName, bName], overlap)}</p>
      <div className="cmp-axis" aria-label={`Temporal comparison of ${aName} and ${bName}`}>
        {[
          { doc: a, name: aName, bar: bars.a, side: "a" },
          { doc: b, name: bName, bar: bars.b, side: "b" },
        ].map(({ doc, name, bar, side }) => (
          <div className="cmp-lane" key={doc.slug}>
            <span className={`cmp-legend ${side}`}>{name}</span>
            <div className="cmp-track">
              <div className={`cmp-span ${side}`} style={{ left: pct(bar.left), width: pct(bar.width) }} />
              {bars.nowAt !== null && <div className="cmp-now" style={{ left: pct(bars.nowAt) }} />}
            </div>
            <span className="cmp-when">
              {formatRange(doc.temporal.t0, doc.temporal.t1, doc.temporal.precision)}
              <span className={`status-badge s-${markerStyle(doc.temporal.status)}`}>
                {doc.temporal.status}
              </span>
            </span>
          </div>
        ))}
        <div className="cmp-scale">
          <span>{formatTime(bars.from, axisSpan)}</span>
          {bars.nowAt === null && <span>now is off this axis</span>}
          <span>{formatTime(bars.to, axisSpan)}</span>
        </div>
      </div>
      {rows.length ? (
        <table className="cmp-table" aria-label={`Aligned properties for ${aName} and ${bName}`}>
          <thead><tr><th>Property</th><th>{aName}</th><th>{bName}</th></tr></thead>
          <tbody>{rows.map((row) => <PropertyRow key={row.property} row={row} names={[aName, bName]} />)}</tbody>
        </table>
      ) : <p>Neither subject has recorded measurements.</p>}
    </>
  );
}

function subjectName(doc: EntityDoc, expectedSlug: string, side: string): string {
  if (doc.slug !== expectedSlug) {
    throw new ComparisonArtifactError(`${side} entity slug does not match ${JSON.stringify(expectedSlug)}`);
  }
  if (typeof doc.name !== "string" || !doc.name.trim()) {
    throw new ComparisonArtifactError(`${side} entity name must be a non-empty string`);
  }
  if (typeof doc.temporal?.precision !== "string" || !TEMPORAL_PRECISIONS.has(doc.temporal.precision)) {
    throw new ComparisonArtifactError(`${side} entity has invalid temporal precision`);
  }
  if (typeof doc.temporal.status !== "string" || !TEMPORAL_STATUSES.has(doc.temporal.status)) {
    throw new ComparisonArtifactError(`${side} entity has invalid temporal status`);
  }
  return doc.name;
}

function PropertyRow({ row, names }: { row: AlignedProperty; names: [string, string] }) {
  const values = [row.a, row.b].flatMap((side) => side?.status === "value" ? [side.min, side.max] : []);
  const scale = scaleFor(values);
  return (
    <tr className={`cmp-${row.comparison}`}>
      <th scope="row">{row.label}</th>
      <PropertyCell side={row.a} scale={scale} name={names[0]} />
      <PropertyCell side={row.b} scale={scale} name={names[1]} />
    </tr>
  );
}

function PropertyCell({ side, scale, name }: { side: PropertySide | null; scale: Scale; name: string }) {
  if (!side) return <td className="cmp-gap">not recorded for {name}</td>;
  const badges = VALUE_TYPE_ORDER.filter((type) => side.valueTypes.includes(type));
  return (
    <td>
      {side.status === "value" && (
        <span className="cmp-val">
          {fmtScaled(side.min, scale)}
          {side.kind === "range" && <> – {fmtScaled(side.max, scale)}</>}
          <span className="unit"> {side.unit}</span>
        </span>
      )}
      {side.status === "missing_value" && <span className="cmp-gap">supported, no value</span>}
      {side.status === "unsupported_unit" && (
        <span className="cmp-unsupported">unsupported unit: {side.units.join(", ")}</span>
      )}
      <span className="cmp-claims">{side.claimCount} claim{side.claimCount === 1 ? "" : "s"}</span>
      <span className="cmp-value-types">
        {badges.map((type) => <span key={type} className={`c-type t-${type}`}>{type}</span>)}
      </span>
    </td>
  );
}

function pct(fraction: number): string {
  return `${(fraction * 100).toFixed(3)}%`;
}
