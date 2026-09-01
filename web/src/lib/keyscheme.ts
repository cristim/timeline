// Client half of the chunk key scheme (API-1/API-5). MUST stay behaviorally
// identical to internal/model/buckets.go - both sides are pinned by
// keycases.json; a drift between them is the failure mode of this
// architecture.
import type { Bucket } from "./manifest";

export const SECONDS_PER_YEAR = 31_556_952;

export function windowIndex(bucket: Bucket, t: number): number {
  if (bucket.window_s === 0) {
    return 0;
  }
  return Math.floor(t / bucket.window_s);
}

export function yearToSeconds(y: number): number {
  return (y - 1970) * SECONDS_PER_YEAR;
}

export function secondsToYear(t: number): number {
  return t / SECONDS_PER_YEAR + 1970;
}

/**
 * Picks the bucket for a visible time span (ZOOM-3): the finest windowed
 * bucket that covers the span in at most maxWindows chunk windows. Spans too
 * large even for T3 fall into the single-window buckets T2/T1/T0 by size.
 */
export function bucketForSpan(buckets: Bucket[], span: number): number {
  const maxWindows = 10;
  for (let i = buckets.length - 1; i >= 3; i--) {
    if (span <= buckets[i].window_s * maxWindows) {
      return i;
    }
  }
  if (span <= 2e16) return 2; // ~600 My
  if (span <= 2e17) return 1; // ~6 Gy
  return 0;
}

/** Chunk artifact key, relative to /v/<dataset>/ (API-1). */
export function chunkKey(bucket: Bucket, window: number, category: string): string {
  return `chunks/${bucket.id}/${window}/world/${category}.json`;
}

/** Map-layer artifact keys (API-4), relative to /v/<dataset>/. */
export function layerKey(layer: string, timestep: number): string {
  return `layers/${layer}/${timestep}.pmtiles`;
}

export function layerIndexKey(layer: string): string {
  return `layers/${layer}/index.json`;
}

/** The shape of a layer index row that this module needs to choose a step. */
export interface CoverageWindow {
  t_from: number;
  t_to: number;
}

/**
 * The time-step whose coverage window holds `year`, or null if none does.
 *
 * ARCH-3 describes this as snapping to the *nearest* step, which was right
 * when the layer was five hand-traced eras far apart. It is wrong for a layer
 * that tiles: slice spacing is wildly uneven - 113,000 years between the first
 * two political slices, six between the last two - so the nearest slice year is
 * routinely one whose window ends long before the cursor. Snapping to it and
 * then testing coverage blanked the map across most of prehistory, because
 * every year after 66500 BC is nearer to the 10000 BC slice than to the
 * 123000 BC slice that actually covers it.
 *
 * Windows tile and are disjoint (enforced at ingest), so the covering step is
 * unique where it exists.
 */
export function coveringTimestep<T extends CoverageWindow>(steps: T[], year: number): T | null {
  for (const s of steps) {
    if (year >= s.t_from && year <= s.t_to) return s;
  }
  return null;
}

/**
 * The chunk artifact windows of `bucket` for one category overlapping [t0,t1].
 * A run resolves to its start window, which is the only baked artifact key.
 */
export function windowsInRange(
  bucket: Bucket,
  category: string,
  t0: number,
  t1: number,
): number[] {
  const runs = bucket.windows?.[category] ?? [];
  const w0 = bucket.window_s === 0 ? 0 : windowIndex(bucket, t0);
  const w1 = bucket.window_s === 0 ? 0 : windowIndex(bucket, t1);
  const out: number[] = [];
  for (const [start, end] of runs) {
    if (end < w0 || start > w1) continue;
    if (!out.includes(start)) out.push(start);
  }
  return out;
}
