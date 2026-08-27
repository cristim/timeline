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

/**
 * True when the item's own interval contains the cursor - it existed at the
 * moment the map is showing. Type-agnostic on purpose: a city founded in 300 BC
 * and still standing qualifies at a 1500 cursor for the same reason a war does
 * mid-war, without either being special-cased by entity type.
 */
export function coversCursor(item: ChunkItem, v: Visibility): boolean {
  return item.t0 <= v.cursor && item.t1 >= v.cursor;
}

/** True when the item's extent reaches into the cursor's proximity band. */
export function nearCursor(item: ChunkItem, v: Visibility): boolean {
  const band = (v.t1 - v.t0) * CURSOR_NEAR;
  return item.t1 >= v.cursor - band && item.t0 <= v.cursor + band;
}

/**
 * What the map may draw: covers the cursor, or sits near it.
 *
 * Covering the cursor is its own admission ticket, ahead of the in-view test,
 * because the long-lived things are exactly the ones that span the whole view
 * and so "start or end within" it - a rule written for lane content, where
 * pass-through items are background rather than rows. On the map they are the
 * subject: at a 1500 cursor, Rome is what a map of 1500 should have on it.
 *
 * Everything else has to start or end in view, and anything point-like at this
 * zoom additionally has to sit inside the cursor's band. The selected entity is
 * exempt from all of it: a selection that vanishes because the cursor moved is
 * a bug, not declutter.
 */
export function mapVisible(item: ChunkItem, v: Visibility): boolean {
  if (item.slug === v.selected) return true;
  if (coversCursor(item, v)) return true;
  if (!startsOrEndsWithin(item, v.t0, v.t1)) return false;
  if (pointLike(item, v.t1 - v.t0)) return nearCursor(item, v);
  return true;
}

/**
 * What the lanes may hold. Unlike the map, an item that merely passes through
 * the whole view stays background context rather than taking a row (that is
 * what the map is for), so there is no covers-the-cursor bypass here - and none
 * is needed for point-like items, whose interval containing the cursor already
 * puts them inside its band.
 *
 * Periphery declutter applies only to real intervals: a point-like item has
 * been judged against the cursor already, and a cursor pinned near an edge puts
 * its own band inside the edge band, where declutter would hide exactly what
 * the user pinned.
 */
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
