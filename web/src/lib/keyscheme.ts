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
  return `layers/${layer}/${timestep}.json`;
}

export function layerIndexKey(layer: string): string {
  return `layers/${layer}/index.json`;
}

/**
 * The time-step nearest to `year` (ARCH-3: "the client snaps the time cursor
 * to the nearest step"). Nearest, not covering: whether the step has anything
 * to say about that year is a separate question its coverage window answers.
 */
export function nearestTimestep(steps: number[], year: number): number | null {
  let best: number | null = null;
  let bestGap = Infinity;
  for (const s of steps) {
    const gap = Math.abs(s - year);
    if (gap < bestGap) {
      best = s;
      bestGap = gap;
    }
  }
  return best;
}

/**
 * The non-empty windows of `bucket` for one category overlapping [t0,t1]
 * (manifest-driven, API-1): only listed (window, category) pairs were baked.
 */
export function windowsInRange(
  bucket: Bucket,
  category: string,
  t0: number,
  t1: number,
): number[] {
  const windows = bucket.windows?.[category] ?? [];
  if (bucket.window_s === 0) {
    return windows.length ? [0] : [];
  }
  const w0 = windowIndex(bucket, t0);
  const w1 = windowIndex(bucket, t1);
  return windows.filter((w) => w >= w0 && w <= w1);
}
