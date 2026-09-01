// View state <-> URL (API-6): every view is shareable; back/forward works.
import { isValidEntitySlug } from "./slug";

export type ViewMode = "map" | "compare";

export interface ViewState {
  t0: number;
  t1: number;
  cats: string[]; // empty = all categories
  minImportance: number;
  selected: string | null;
  mode: ViewMode;
  /** Ordered Compare subjects: empty, pending primary, or complete pair. */
  compare: string[];
  /** Cosmic zoom (SpaceView): 0 = normal map, up to SPACE_MAX. */
  space: number;
  /**
   * The time cursor: the single moment the map is showing (FE-3/FE-5).
   * null means unpinned, in which case it follows the centre of the visible
   * range - so the map answers "what did the world look like in the middle of
   * what I am looking at?" until the user drags the cursor somewhere.
   */
  tc: number | null;
}

export const DEFAULT_VIEW: ViewState = {
  // Whole-universe view: Big Bang margin to a bit past the near future.
  t0: -4.6e17,
  t1: 0.6e17,
  cats: [],
  minImportance: 0,
  selected: null,
  mode: "map",
  compare: [],
  space: 0,
  tc: null,
};

/** Where the cursor actually is: pinned tc, else the centre of the view. */
export function cursorTime(v: Pick<ViewState, "t0" | "t1" | "tc">): number {
  return v.tc ?? (v.t0 + v.t1) / 2;
}

export function switchView(view: ViewState, mode: ViewMode): ViewState {
  if (view.mode === mode) return view;
  return { ...view, mode, compare: [] };
}

export function enterCompare(view: ViewState, slug: string): ViewState {
  if (!isValidEntitySlug(slug)) return view;
  return { ...view, mode: "compare", compare: [slug] };
}

export function applySearchPick(
  view: ViewState,
  entry: { slug: string; t0: number; t1: number },
): ViewState {
  if (view.mode === "compare" && view.compare.length === 1 && view.compare[0] !== entry.slug) {
    return { ...view, compare: [view.compare[0], entry.slug] };
  }
  const span = Math.max(entry.t1 - entry.t0, 86_400);
  return {
    ...view,
    selected: entry.slug,
    t0: entry.t0 - span * 1.5,
    t1: entry.t1 + span * 1.5,
  };
}

export function parseView(
  location: Pick<Location, "pathname" | "search">,
  basePath = "/",
): ViewState {
  const q = new URLSearchParams(location.search);
  const t0 = Number(q.get("t0"));
  const t1 = Number(q.get("t1"));
  const v: ViewState = { ...DEFAULT_VIEW };
  if (Number.isFinite(t0) && Number.isFinite(t1) && t1 > t0 && q.has("t0")) {
    v.t0 = t0;
    v.t1 = t1;
  }
  const cats = q.get("cats");
  if (cats) v.cats = cats.split(",").filter(Boolean);
  const imp = Number(q.get("imp"));
  if (Number.isFinite(imp) && q.has("imp")) v.minImportance = imp;
  const routeSlug = entitySlugFromPath(location.pathname, basePath);
  const selected = q.get("sel");
  v.selected = selected && isValidEntitySlug(selected) ? selected : null;

  const hasCanonicalCompare = q.get("view") === "compare" || q.has("compare");
  const hasLegacyCompare = q.has("cmp") && !hasCanonicalCompare;
  const hasCompareIntent = hasCanonicalCompare || hasLegacyCompare;
  if (hasCompareIntent) {
    v.mode = "compare";
    if (hasLegacyCompare) {
      v.compare = parseLegacyPair(q.get("cmp"));
    } else if (routeSlug) {
      const secondary = q.get("compare");
      v.compare = normalizePair([
        routeSlug,
        ...(secondary && isValidEntitySlug(secondary) ? [secondary] : []),
      ]);
    }
  } else if (routeSlug) {
    v.selected = routeSlug;
  }
  const space = Number(q.get("space"));
  if (Number.isFinite(space) && space > 0) v.space = Math.min(space, 4);
  const tc = Number(q.get("tc"));
  if (q.has("tc") && Number.isFinite(tc)) v.tc = tc;
  return v;
}

export function serializeView(v: ViewState, basePath = "/"): string {
  const q = new URLSearchParams();
  q.set("t0", compactNum(v.t0));
  q.set("t1", compactNum(v.t1));
  if (v.cats.length) q.set("cats", v.cats.join(","));
  if (v.minImportance > 0) q.set("imp", `${v.minImportance}`);
  const compare = normalizePair(v.compare);
  let routeSlug: string | null = null;
  if (v.mode === "compare") {
    routeSlug = compare[0] ?? null;
    if (compare.length === 2) q.set("compare", compare[1]);
    else q.set("view", "compare");
    if (v.selected && isValidEntitySlug(v.selected)) q.set("sel", v.selected);
  } else if (v.selected && isValidEntitySlug(v.selected)) {
    routeSlug = v.selected;
  }
  if (v.space > 0) q.set("space", v.space.toFixed(2));
  if (v.tc !== null) q.set("tc", compactNum(v.tc));
  const root = normalizedBasePath(basePath);
  const pathname = routeSlug ? `${root}entity/${encodeURIComponent(routeSlug)}` : root;
  return `${pathname}?${q.toString()}`;
}

function entitySlugFromPath(pathname: string, basePath: string): string | null {
  const root = normalizedBasePath(basePath);
  let relative: string;
  if (root === "/") {
    relative = pathname;
  } else if (pathname === root.slice(0, -1) || pathname === root) {
    relative = "/";
  } else if (pathname.startsWith(root)) {
    relative = `/${pathname.slice(root.length)}`;
  } else {
    return null;
  }

  const match = relative.match(/^\/entity\/([^/]+)\/?$/);
  if (!match) return null;
  let slug: string;
  try {
    slug = decodeURIComponent(match[1]);
  } catch {
    return null;
  }
  return isValidEntitySlug(slug) ? slug : null;
}

function parseLegacyPair(value: string | null): string[] {
  if (!value) return [];
  const entries = value.split(",");
  if (entries.length > 2 || entries.some((slug) => !isValidEntitySlug(slug))) return [];
  return normalizePair(entries);
}

function normalizePair(slugs: string[]): string[] {
  const pair: string[] = [];
  for (const slug of slugs) {
    if (!isValidEntitySlug(slug) || pair.includes(slug)) continue;
    pair.push(slug);
    if (pair.length === 2) break;
  }
  return pair;
}

function normalizedBasePath(basePath: string): string {
  const withLeadingSlash = basePath.startsWith("/") ? basePath : `/${basePath}`;
  return withLeadingSlash.endsWith("/") ? withLeadingSlash : `${withLeadingSlash}/`;
}

function compactNum(n: number): string {
  // Exponential keeps deep-time URLs short; 9 digits is far below any
  // bucket's window size relative to its span.
  return n.toExponential(9);
}
