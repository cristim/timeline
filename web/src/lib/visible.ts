import type { ChunkItem } from "./data";

export const LANE_CAP = 100;

/** Fraction of the view at each edge treated as periphery (declutter zone). */
export const PERIPHERY = 0.15;

/**
 * Half-width of the cursor's proximity band, as a fraction of the visible span.
 * Point-like items are shown only inside it.
 */
export const CURSOR_NEAR = 0.1;

/** Intervals shorter than this fraction of the view are treated as moments. */
export const MIN_INTERVAL_FRACTION = 0.01;

/** The view and cursor an item's visibility is judged against. */
export interface Visibility {
  t0: number;
  t1: number;
  /** The moment the map is showing (state.cursorTime). */
  cursor: number;
  selected: string | null;
}

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

/** Instant events, and intervals too short to read as intervals at this zoom. */
export function pointLike(item: ChunkItem, span: number): boolean {
  return item.t1 - item.t0 < span * MIN_INTERVAL_FRACTION;
}

/** True when the item existed at the moment shown on the map. */
export function coversCursor(item: ChunkItem, v: Visibility): boolean {
  return item.t0 <= v.cursor && item.t1 >= v.cursor;
}

/** True when the item's extent reaches into the cursor's proximity band. */
export function nearCursor(item: ChunkItem, v: Visibility): boolean {
  const band = (v.t1 - v.t0) * CURSOR_NEAR;
  return item.t1 >= v.cursor - band && item.t0 <= v.cursor + band;
}

/** Map items cover the cursor, or start/end in view and pass the point gate. */
export function mapVisible(item: ChunkItem, v: Visibility): boolean {
  if (item.slug === v.selected) return true;
  if (coversCursor(item, v)) return true;
  if (!startsOrEndsWithin(item, v.t0, v.t1)) return false;
  if (pointLike(item, v.t1 - v.t0)) return nearCursor(item, v);
  return true;
}

/** Lane items start/end in view; point-like items use the cursor gate. */
export function laneVisible(item: ChunkItem, v: Visibility): boolean {
  if (item.slug === v.selected) return true;
  if (!startsOrEndsWithin(item, v.t0, v.t1)) return false;
  if (pointLike(item, v.t1 - v.t0)) return nearCursor(item, v);
  return !whollyInPeriphery(item, v.t0, v.t1);
}

/** Markers the map may draw. No cap: the map declutters by geography, not rows. */
export function mapItems(items: ChunkItem[], v: Visibility): ChunkItem[] {
  return items.filter((i) => mapVisible(i, v));
}

/** Right-gutter items keep real intervals but cursor-gate point-like ones. */
export function futureGutterItems(
  items: ChunkItem[],
  v: Visibility,
  cap: number = 12,
): ChunkItem[] {
  const out: ChunkItem[] = [];
  for (const item of items) {
    if (item.t0 <= v.t1) continue;
    if (
      item.slug !== v.selected &&
      pointLike(item, v.t1 - v.t0) &&
      !nearCursor(item, v)
    ) {
      continue;
    }
    out.push(item);
  }
  return out
    .sort((a, b) => Number(b.slug === v.selected) - Number(a.slug === v.selected))
    .slice(0, cap);
}

/**
 * Top `cap` eligible items, importance-ordered (ties by slug for stability).
 * The selection sorts first so the cap can never drop it.
 */
export function laneItems(
  items: ChunkItem[],
  v: Visibility,
  cap: number = LANE_CAP,
): ChunkItem[] {
  return items
    .filter((i) => laneVisible(i, v))
    .sort(
      (a, b) =>
        Number(b.slug === v.selected) - Number(a.slug === v.selected) ||
        b.importance - a.importance ||
        (a.slug < b.slug ? -1 : 1),
    )
    .slice(0, cap);
}
