// FE-1: the three-area layout - map canvas, always-visible timeline,
// inspector - over static artifacts only (no server, ARCH-1).
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { MapView } from "./components/MapView";
import { Timeline } from "./components/Timeline";
import { Inspector, focusEntity } from "./components/Inspector";
import { SearchBox } from "./components/SearchBox";
import { useEntity, useManifest, useViewportItems } from "./lib/hooks";
import { DEFAULT_VIEW, parseView, serializeView, type ViewState } from "./lib/state";
import { laneItems } from "./lib/visible";
import { formatTime } from "./lib/timefmt";
import { categoryColor } from "./lib/colors";
import type { SearchEntry } from "./lib/data";

export function App() {
  const { manifest, error } = useManifest();
  const [view, setView] = useState<ViewState>(() => parseView(window.location.search));

  // URL sync (API-6): selection changes push a history entry (back/forward
  // walks selections, FE-9); pan/zoom/filter changes replace in place.
  const urlTimer = useRef<number | undefined>(undefined);
  const lastPushedSel = useRef(view.selected);
  useEffect(() => {
    window.clearTimeout(urlTimer.current);
    urlTimer.current = window.setTimeout(() => {
      const url = serializeView(view);
      if (url === window.location.search) return;
      if (view.selected !== lastPushedSel.current) {
        history.pushState(null, "", url);
      } else {
        history.replaceState(null, "", url);
      }
      lastPushedSel.current = view.selected;
    }, 250);
    return () => window.clearTimeout(urlTimer.current);
  }, [view]);
  useEffect(() => {
    const onPop = () => setView(parseView(window.location.search));
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);

  const { items, bucket, loading } = useViewportItems(
    manifest,
    view.t0,
    view.t1,
    view.cats,
    view.minImportance,
  );
  const selectedDoc = useEntity(manifest, view.selected);

  const setRange = useCallback(
    (t0: number, t1: number) => setView((v) => ({ ...v, t0, t1 })),
    [],
  );
  const setSelected = useCallback(
    (slug: string | null) => setView((v) => ({ ...v, selected: slug })),
    [],
  );
  const toggleCat = (cat: string) =>
    setView((v) => ({
      ...v,
      cats: v.cats.includes(cat) ? v.cats.filter((c) => c !== cat) : [...v.cats, cat],
    }));

  const onSearchPick = (e: SearchEntry) => {
    setView((v) => {
      const span = Math.max(e.t1 - e.t0, 86_400);
      return { ...v, selected: e.slug, t0: e.t0 - span * 1.5, t1: e.t1 + span * 1.5 };
    });
  };

  const rangeLabel = useMemo(() => {
    const span = view.t1 - view.t0;
    return `${formatTime(view.t0, span)} — ${formatTime(view.t1, span)}`;
  }, [view.t0, view.t1]);

  // The status-bar count matches what the timeline lanes actually consider
  // (top-100 starting/ending in view), not the raw chunk union.
  const laneCount = useMemo(
    () => laneItems(items, view.t0, view.t1).length,
    [items, view.t0, view.t1],
  );

  if (error) {
    return (
      <div className="boot-error">
        <p>Failed to load the dataset manifest: {error}</p>
        <p>
          Is the stack up? <code>make up &amp;&amp; make bake</code>
        </p>
      </div>
    );
  }
  if (!manifest) {
    return <div className="boot-error"><p>Loading…</p></div>;
  }

  return (
    <div className="app">
      <header className="topbar">
        <button
          className="brand"
          onClick={() => setView({ ...DEFAULT_VIEW })}
          title="Reset to the whole-universe view"
        >
          Everything <em>Timeline</em>
        </button>
        <SearchBox manifest={manifest} onPick={onSearchPick} />
        <div className="cat-chips">
          {manifest.categories.map((c) => (
            <button
              key={c}
              className={`cat-chip ${view.cats.length === 0 || view.cats.includes(c) ? "on" : "off"}`}
              style={{ ["--cat" as string]: categoryColor[c] ?? "#9aa3b5" }}
              onClick={() => toggleCat(c)}
              title={`Toggle ${c}`}
            >
              {c}
            </button>
          ))}
        </div>
      </header>

      <div className="mid">
        <MapView items={items} selected={view.selected} onSelect={setSelected} />
        <Inspector
          doc={selectedDoc}
          onSelect={setSelected}
          onFocusTime={setRange}
          onClose={() => setSelected(null)}
        />
      </div>

      <footer className="timeline-shell">
        <Timeline
          t0={view.t0}
          t1={view.t1}
          items={items}
          selected={view.selected}
          onRange={setRange}
          onSelect={setSelected}
        />
        <div className="tl-status">
          <span className="bucket-badge">{manifest.buckets[bucket]?.id}</span>
          <span className="range-label">{rangeLabel}</span>
          <span className="count">{loading ? "…" : `${laneCount} shown`}</span>
          <label className="imp">
            importance ≥ {view.minImportance.toFixed(2)}
            <input
              type="range"
              min={0}
              max={0.95}
              step={0.05}
              value={view.minImportance}
              onChange={(e) => setView((v) => ({ ...v, minImportance: Number(e.target.value) }))}
            />
          </label>
          <span className="status-key">
            <i className="k-solid" /> documented <i className="k-hollow" /> estimated{" "}
            <i className="k-fuzzy" /> projected
          </span>
          <span className="dataset">
            {manifest.dataset} · {manifest.seed_version}
          </span>
        </div>
      </footer>

      {selectedDoc && (
        <FocusOnSelect
          doc={selectedDoc}
          setRange={setRange}
          slug={view.selected}
          t0={view.t0}
          t1={view.t1}
        />
      )}
    </div>
  );
}

/**
 * When a selection arrives from search/related-chips and lies outside the
 * visible range, pull the timeline to it once per selection change.
 */
function FocusOnSelect({
  doc,
  setRange,
  slug,
  t0,
  t1,
}: {
  doc: { slug: string; temporal: { t0: number; t1: number } };
  setRange: (a: number, b: number) => void;
  slug: string | null;
  t0: number;
  t1: number;
}) {
  const last = useRef<string | null>(null);
  useEffect(() => {
    if (!slug || slug !== doc.slug || last.current === slug) return;
    last.current = slug;
    if (doc.temporal.t0 > t1 || doc.temporal.t1 < t0) {
      focusEntity(doc, setRange);
    }
  }, [slug, doc, setRange, t0, t1]);
  return null;
}
