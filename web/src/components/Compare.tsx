// FE-8 (MVP): compare exactly two entities - an aligned property table and a
// temporal overlap bar. It sits over the map rather than replacing it, so the
// timeline and the map keep showing both subjects while the comparison is open.
import type { EntityDoc } from "../lib/data";
import {
  alignProperties,
  compareBars,
  temporalOverlap,
  verdict,
  type PropertySide,
} from "../lib/compare";
import { markerStyle } from "../lib/colors";
import { fmtScaled, scaleFor, type Scale } from "../lib/numfmt";
import { formatRange, formatTime, NOW_S } from "../lib/timefmt";

interface Props {
  /** The compare subjects from the URL state: one is a pending pick, two is a comparison. */
  slugs: string[];
  a: EntityDoc | null;
  b: EntityDoc | null;
  /** Whether each subject's document came back wrong, per slugs index. */
  failed: [boolean, boolean];
  onClose: () => void;
}

export function Compare({ slugs, a, b, failed, onClose }: Props) {
  if (!slugs.length) return null;
  const broken = slugs.filter((_, i) => failed[i]);
  return (
    <section className="compare-panel" aria-label="Compare">
      <header className="cmp-head">
        <h3>Compare</h3>
        <button className="cmp-close" onClick={onClose} aria-label="Leave compare">
          ×
        </button>
      </header>
      <div className="cmp-body">
        {broken.length > 0 ? (
          // A link can outlive a bake. Say which subject is missing rather than
          // waiting on a document that is never coming.
          <p className="cmp-failed">Could not load {broken.join(" or ")}.</p>
        ) : slugs.length < 2 ? (
          <p className="cmp-pending">
            Comparing <b>{a?.name ?? slugs[0]}</b>. Search above to pick the second entity.
          </p>
        ) : a && b ? (
          <Pair a={a} b={b} />
        ) : (
          <p className="cmp-pending">Loading…</p>
        )}
      </div>
    </section>
  );
}

function Pair({ a, b }: { a: EntityDoc; b: EntityDoc }) {
  const overlap = temporalOverlap(a.temporal, b.temporal, NOW_S);
  const bars = compareBars(a.temporal, b.temporal, NOW_S);
  const rows = alignProperties(a, b);
  const axisSpan = bars.to - bars.from;
  return (
    <>
      <p className="cmp-verdict">
        <span className={`cmp-mark ${overlap.overlaps ? "yes" : "no"}`}>
          {overlap.overlaps ? "✓" : "✗"}
        </span>
        <b>{a.name}</b> and <b>{b.name}</b>: {verdict([a.name, b.name], overlap)}
      </p>

      <div className="cmp-axis">
        {[
          { doc: a, bar: bars.a, side: "a" },
          { doc: b, bar: bars.b, side: "b" },
        ].map(({ doc, bar, side }) => (
          <div className="cmp-lane" key={doc.slug}>
            <span className={`cmp-legend ${side}`}>{doc.name}</span>
            <div className="cmp-track">
              <div
                className={`cmp-span ${side}`}
                style={{ left: pct(bar.left), width: pct(bar.width) }}
              />
              {bars.nowAt !== null && <div className="cmp-now" style={{ left: pct(bars.nowAt) }} />}
            </div>
            {/* The dates carry their status (FE-7): two reconstructed spans
                must not read as two measured ones. */}
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
          {/* An axis that does not reach the present says so, rather than
              letting the reader take an end of it for now. The distance itself
              is the verdict's job, measured from a subject and not from an
              edge that only exists because the axis had to stop somewhere. */}
          {bars.nowAt === null && <span className="cmp-offaxis">now is off this axis</span>}
          <span>{formatTime(bars.to, axisSpan)}</span>
        </div>
      </div>

      {rows.length > 0 ? (
        <table className="cmp-table">
          <thead>
            <tr>
              <th />
              <th>{a.name}</th>
              <th>{b.name}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => {
              // One scale per row, from both sides: the point of the row is
              // that its four numbers can be read against each other.
              const scale = scaleFor(
                [r.a?.min, r.a?.max, r.b?.min, r.b?.max].filter((v) => v !== undefined),
              );
              return (
                <tr key={r.property} className={r.a && r.b ? "" : "one-sided"}>
                  <th scope="row">
                    {r.property.replace(/_/g, " ")}
                    {r.unitMismatch && (
                      <span
                        className="cmp-mismatch"
                        title="Different units: these values are not directly comparable"
                      >
                        ≠ units
                      </span>
                    )}
                  </th>
                  <Cell side={r.a} scale={scale} name={a.name} />
                  <Cell side={r.b} scale={scale} name={b.name} />
                </tr>
              );
            })}
          </tbody>
        </table>
      ) : (
        <p className="cmp-none">Neither entity has recorded measurements.</p>
      )}
    </>
  );
}

/** One entity's value for a property, or the honest absence of one. */
function Cell({ side, scale, name }: { side: PropertySide | null; scale: Scale; name: string }) {
  if (!side) {
    return (
      <td className="cmp-gap" title={`Not recorded for ${name}`}>
        not recorded
      </td>
    );
  }
  return (
    <td>
      <span className="cmp-val">
        {fmtScaled(side.min, scale)}
        {side.max !== side.min && <> – {fmtScaled(side.max, scale)}</>}
        {side.unit && <span className="unit"> {side.unit}</span>}
      </span>
      <span className="cmp-claims">
        {side.claimCount} claim{side.claimCount === 1 ? "" : "s"}
      </span>
    </td>
  );
}

function pct(fraction: number): string {
  return `${(fraction * 100).toFixed(3)}%`;
}
