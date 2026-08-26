// Front lines over time: the client half of the DM-7 geometry records the
// baker puts on a war's entity document. FE-3 describes a war as "a set of
// changing fronts"; this turns the handful of dated positions we curate into
// a continuous line driven by the time cursor.
import type { GeometryRecord } from "./data";

export interface FrontSample {
  coordinates: [number, number][];
  /** The dated position this sample is nearest to, for labelling. */
  label: string;
  /** True when the cursor is outside the curated range, so the line is held. */
  held: boolean;
}

/**
 * The front at time `t`, interpolated vertex by vertex between the two
 * bracketing dated positions. Outside the curated range the nearest end is
 * held rather than extrapolated: we know where the front was in 1943, not
 * where a trend line would put it in 1960.
 *
 * Vertex-wise interpolation is only meaningful because the baker rejects any
 * sequence whose positions disagree on vertex count.
 */
export function frontAt(positions: GeometryRecord[], t: number): FrontSample | null {
  if (positions.length === 0) return null;
  const first = positions[0];
  const last = positions[positions.length - 1];
  if (positions.length === 1) return sample(first.geometry.coordinates, first, true);
  // Strictly outside: a cursor sitting exactly on a documented trace is not
  // being held, it is on the trace.
  if (t < first.valid_from) return sample(first.geometry.coordinates, first, true);
  if (t > last.valid_from) return sample(last.geometry.coordinates, last, true);

  let i = 0;
  while (i < positions.length - 2 && positions[i + 1].valid_from <= t) i++;
  const a = positions[i];
  const b = positions[i + 1];
  const span = b.valid_from - a.valid_from;
  // The baker rejects out-of-order positions, so span is always positive here.
  const f = (t - a.valid_from) / span;
  const coords = a.geometry.coordinates.map(
    (p, n): [number, number] => {
      const q = b.geometry.coordinates[n];
      return [p[0] + (q[0] - p[0]) * f, p[1] + (q[1] - p[1]) * f];
    },
  );
  return sample(coords, f < 0.5 ? a : b, false);
}

function sample(
  coordinates: [number, number][],
  from: GeometryRecord,
  held: boolean,
): FrontSample {
  return { coordinates, label: from.label ?? "", held };
}

/** Bounding box over every dated position, for framing a war on the map. */
export function frontBounds(
  positions: GeometryRecord[],
): [[number, number], [number, number]] | null {
  let w = Infinity;
  let s = Infinity;
  let e = -Infinity;
  let n = -Infinity;
  for (const p of positions) {
    for (const [lon, lat] of p.geometry.coordinates) {
      w = Math.min(w, lon);
      e = Math.max(e, lon);
      s = Math.min(s, lat);
      n = Math.max(n, lat);
    }
  }
  return Number.isFinite(w) ? [[w, s], [e, n]] : null;
}
