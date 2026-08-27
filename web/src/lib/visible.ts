// What the map and the timeline are allowed to show at a given view + cursor.
//
// Two rules stack. The older one is about the *view*: lanes hold the top items
// by importance that START or END inside the visible range, because items that
// merely pass through (Christianity across a 1942 view) are background context,
// not lane content.
//
// The newer one is about the *cursor*. A chunk window at deep-time zoom covers
// all of human history, so at 250 Ma the raw union still contains the Fall of
// Constantinople - a zero-length event 250 million years from anything on
// screen. Anything point-like has to earn its place by sitting near the cursor,
// the moment the map is actually showing.
import type { ChunkItem } from "./data";

export const LANE_CAP = 100;

/** Fraction of the view at each edge treated as periphery (declutter zone). */
export const PERIPHERY = 0.15;

/**
 * Half-width of the cursor's proximity band, as a fraction of the visible span.
 * Point-like items are shown only inside it.
 */
export const CURSOR_NEAR = 0.1;

/**
 * An interval shorter than this fraction of the visible span is indistinguishable
 * from a moment at this zoom, so it is treated as one - which is what subjects it
 * to the cursor rule. Frees the lanes to show the real intervals instead.
 */
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

/** True when the item's extent reaches into the cursor's proximity band. */
export function nearCursor(item: ChunkItem, v: Visibility): boolean {
  const band = (v.t1 - v.t0) * CURSOR_NEAR;
  return item.t1 >= v.cursor - band && item.t0 <= v.cursor + band;
}

/**
 * The gate both the map and the lanes apply. The selected entity is exempt:
 * a selection that vanishes because the cursor moved is a bug, not declutter.
 */
export function visibleAt(item: ChunkItem, v: Visibility): boolean {
  if (item.slug === v.selected) return true;
  if (!startsOrEndsWithin(item, v.t0, v.t1)) return false;
  if (pointLike(item, v.t1 - v.t0)) return nearCursor(item, v);
  return true;
}

/** Markers the map may draw. No cap: the map declutters by geography, not rows. */
export function mapItems(items: ChunkItem[], v: Visibility): ChunkItem[] {
  return items.filter((i) => visibleAt(i, v));
}

/**
 * Lane eligibility: `visibleAt` plus the periphery declutter, which applies
 * only to real intervals. A point-like item has already been judged against the
 * cursor, and a cursor pinned near an edge puts its own band inside the edge
 * band - declutter would then hide exactly what the user pinned.
 */
function laneEligible(item: ChunkItem, v: Visibility): boolean {
  if (item.slug === v.selected) return true;
  if (!visibleAt(item, v)) return false;
  if (pointLike(item, v.t1 - v.t0)) return true;
  return !whollyInPeriphery(item, v.t0, v.t1);
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
    .filter((i) => laneEligible(i, v))
    .sort(
      (a, b) =>
        Number(b.slug === v.selected) - Number(a.slug === v.selected) ||
        b.importance - a.importance ||
        (a.slug < b.slug ? -1 : 1),
    )
    .slice(0, cap);
}
