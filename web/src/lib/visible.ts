// Timeline lane eligibility: the lanes show the top items by importance that
// START or END inside the visible range. Items that merely pass through
// (started before the view, end after it - Christianity across a 1942 view)
// are background context, not lane content; the map and the future gutter
// still surface them where appropriate.
import type { ChunkItem } from "./data";

export const LANE_CAP = 100;

/** Fraction of the view at each edge treated as periphery (declutter zone). */
export const PERIPHERY = 0.15;

export function startsOrEndsWithin(item: ChunkItem, t0: number, t1: number): boolean {
  return (item.t0 >= t0 && item.t0 <= t1) || (item.t1 >= t0 && item.t1 <= t1);
}

/**
 * True when the item's visible portion sits wholly inside one 15% edge band:
 * edge slivers steal lane rows from the center of the view, and panning
 * slightly brings them back anyway.
 */
export function whollyInPeriphery(item: ChunkItem, t0: number, t1: number): boolean {
  const band = (t1 - t0) * PERIPHERY;
  const visStart = Math.max(item.t0, t0);
  const visEnd = Math.min(item.t1, t1);
  return visEnd <= t0 + band || visStart >= t1 - band;
}

/** Top `cap` eligible items, importance-ordered (ties by slug for stability). */
export function laneItems(
  items: ChunkItem[],
  t0: number,
  t1: number,
  cap: number = LANE_CAP,
): ChunkItem[] {
  return items
    .filter((i) => startsOrEndsWithin(i, t0, t1) && !whollyInPeriphery(i, t0, t1))
    .sort((a, b) => b.importance - a.importance || (a.slug < b.slug ? -1 : 1))
    .slice(0, cap);
}
