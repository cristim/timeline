// View state <-> URL (API-6): every view is shareable; back/forward works.
// M3 encodes state in query params; pretty /time/... paths arrive with the
// SSG entity pages.
export interface ViewState {
  t0: number;
  t1: number;
  cats: string[]; // empty = all categories
  minImportance: number;
  selected: string | null;
  /** Cosmic zoom (SpaceView): 0 = normal map, up to SPACE_MAX. */
  space: number;
}

export const DEFAULT_VIEW: ViewState = {
  // Whole-universe view: Big Bang margin to a bit past the near future.
  t0: -4.6e17,
  t1: 0.6e17,
  cats: [],
  minImportance: 0,
  selected: null,
  space: 0,
};

export function parseView(search: string): ViewState {
  const q = new URLSearchParams(search);
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
  v.selected = q.get("sel");
  const space = Number(q.get("space"));
  if (Number.isFinite(space) && space > 0) v.space = Math.min(space, 4);
  return v;
}

export function serializeView(v: ViewState): string {
  const q = new URLSearchParams();
  q.set("t0", compactNum(v.t0));
  q.set("t1", compactNum(v.t1));
  if (v.cats.length) q.set("cats", v.cats.join(","));
  if (v.minImportance > 0) q.set("imp", `${v.minImportance}`);
  if (v.selected) q.set("sel", v.selected);
  if (v.space > 0) q.set("space", v.space.toFixed(2));
  return `?${q.toString()}`;
}

function compactNum(n: number): string {
  // Exponential keeps deep-time URLs short; 9 digits is far below any
  // bucket's window size relative to its span.
  return n.toExponential(9);
}
