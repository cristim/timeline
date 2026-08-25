// FE-6: the inspector panel. Claims render as ranges with sources - the
// DM-5 model made visible. No page navigation; selecting related entities
// swaps the panel in place.
import type { EntityDoc, EntityRef } from "../lib/data";
import { markerStyle } from "../lib/colors";
import { formatRange, isOngoing } from "../lib/timefmt";

interface Props {
  doc: EntityDoc | null;
  onSelect: (slug: string) => void;
  onFocusTime: (t0: number, t1: number) => void;
  onClose: () => void;
}

export function Inspector({ doc, onSelect, onFocusTime, onClose }: Props) {
  if (!doc) {
    return (
      <aside className="inspector empty">
        <p className="hint">
          Select anything on the timeline or map.
          <br />
          Search works too - try “stalingrad” or “rex”.
        </p>
      </aside>
    );
  }
  const { temporal } = doc;
  const ongoing = isOngoing(temporal.t1) && temporal.t1 !== temporal.t0;
  return (
    <aside className="inspector">
      <div className="insp-head">
        <div className="insp-topline">
          <span className={`status-badge s-${markerStyle(temporal.status)}`}>{temporal.status}</span>
          <span className="type-badge">{doc.type.replace(/_/g, " ")}</span>
          <button className="close" onClick={onClose} aria-label="Close inspector">×</button>
        </div>
        <h2>{doc.name}</h2>
        <button
          className="when"
          title="Focus the timeline on this entity"
          onClick={() => focusEntity(doc, onFocusTime)}
        >
          {formatRange(temporal.t0, temporal.t1, temporal.precision)}
          {ongoing ? " · ongoing" : ""}
          <span className="prec"> · {temporal.precision.replace(/_/g, " ")}</span>
        </button>
        {doc.description && <p className="desc">{doc.description}</p>}
      </div>

      {doc.properties && doc.properties.length > 0 && (
        <section>
          <h4>Measurements</h4>
          {doc.properties.map((p) => (
            <details key={p.property} className="prop">
              <summary>
                <span className="prop-name">{p.property.replace(/_/g, " ")}</span>
                <span className="prop-val">
                  {fmtNum(p.synthesis.min)}
                  {p.synthesis.max !== p.synthesis.min && <> – {fmtNum(p.synthesis.max)}</>}
                  {p.synthesis.unit && <span className="unit"> {p.synthesis.unit}</span>}
                </span>
                <span className="claims-n">
                  {p.synthesis.claim_count} claim{p.synthesis.claim_count > 1 ? "s" : ""}
                </span>
              </summary>
              <ul className="claims">
                {p.claims.map((c, i) => (
                  <li key={i}>
                    <span className="c-val">
                      {c.value != null ? fmtNum(c.value) : `${fmtNum(c.min!)} – ${fmtNum(c.max!)}`}
                      {c.unit ? ` ${c.unit}` : ""}
                    </span>
                    <span className={`c-type t-${c.value_type}`}>{c.value_type}</span>
                    <span className="c-src" title={c.method ?? ""}>
                      {c.source} ({c.published_at})
                    </span>
                  </li>
                ))}
              </ul>
            </details>
          ))}
        </section>
      )}

      {doc.relationships && doc.relationships.length > 0 && (
        <section>
          <h4>Related</h4>
          <div className="chips">
            {doc.relationships.map((r, i) => (
              <button key={i} className="chip" onClick={() => onSelect(r.target.slug)}>
                <span className="rel-type">{r.type.replace(/_/g, " ")}</span> {r.target.name}
              </button>
            ))}
          </div>
        </section>
      )}

      {doc.children && doc.children.length > 0 && (
        <section>
          <h4>Contains</h4>
          <RefChips refs={doc.children} onSelect={onSelect} />
        </section>
      )}

      {doc.contemporaries && doc.contemporaries.length > 0 && (
        <section>
          <h4>At the same time</h4>
          <RefChips refs={doc.contemporaries.slice(0, 12)} onSelect={onSelect} />
        </section>
      )}

      {(doc.links.wikipedia || doc.links.wikidata) && (
        <section>
          <h4>Read &amp; verify</h4>
          <div className="chips">
            {doc.links.wikipedia && (
              <a className="chip" href={doc.links.wikipedia} target="_blank" rel="noreferrer">
                Wikipedia ↗
              </a>
            )}
            {doc.links.wikidata && (
              <a
                className="chip"
                href={`https://www.wikidata.org/wiki/${doc.links.wikidata}`}
                target="_blank"
                rel="noreferrer"
              >
                Wikidata {doc.links.wikidata} ↗
              </a>
            )}
          </div>
        </section>
      )}
    </aside>
  );
}

function RefChips({ refs, onSelect }: { refs: EntityRef[]; onSelect: (slug: string) => void }) {
  return (
    <div className="chips">
      {refs.map((r) => (
        <button key={r.slug} className="chip" onClick={() => onSelect(r.slug)}>
          {r.name}
        </button>
      ))}
    </div>
  );
}

export function focusEntity(
  doc: { temporal: { t0: number; t1: number } },
  onFocusTime: (t0: number, t1: number) => void,
) {
  const { t0, t1 } = doc.temporal;
  const span = Math.max(t1 - t0, 86_400);
  onFocusTime(t0 - span * 1.5, t1 + span * 1.5);
}

function fmtNum(v: number): string {
  if (Math.abs(v) >= 1e9) return `${round2(v / 1e9)}B`;
  if (Math.abs(v) >= 1e6) return `${round2(v / 1e6)}M`;
  if (Math.abs(v) >= 1e4) return `${round2(v / 1e3)}k`;
  return `${round2(v)}`;
}
function round2(v: number): number {
  return Math.round(v * 100) / 100;
}
