// FE-1: the three-area layout - map canvas, always-visible timeline,
// inspector - over static artifacts only (no server, ARCH-1).
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { MapView } from "./components/MapView";
import { SpaceView, SPACE_MAX } from "./components/SpaceView";
import { Timeline } from "./components/Timeline";
import { Inspector, focusEntity } from "./components/Inspector";
import { SearchBox } from "./components/SearchBox";
import { useEntity, useManifest, useTimeLayer, useViewportItems } from "./lib/hooks";
import { cursorTime, DEFAULT_VIEW, parseView, serializeView, type ViewState } from "./lib/state";
import { laneItems, mapItems } from "./lib/visible";
import { frontAt, frontBounds } from "./lib/fronts";
import { formatTime } from "./lib/timefmt";
import { secondsToYear } from "./lib/keyscheme";
import { mapMode, voidChipLabel } from "./lib/mapmode";
import { categoryColor } from "./lib/colors";
import type { ChunkItem, SearchEntry } from "./lib/data";

// Timeline shell sizing: draggable between MIN and MAX; dragging below the
// collapse threshold folds it down to just the status bar.
const TL_MIN = 140;
const TL_COLLAPSE_BELOW = 100;
const TL_COLLAPSED = 33;
const TL_DEFAULT = 240;
const tlMax = () => Math.round(window.innerHeight * 0.6);

// The two time-aware area layers the baker produces (API-4). Their coverage
// windows are disjoint and tile the whole timeline: political borders through
// recorded history, reconstructed coastlines before it.
const BORDERS_LAYER = "borders";
const PALEO_LAYER = "paleocoast";

export function App() {
  const { manifest, error } = useManifest();
  const [view, setView] = useState<ViewState>(() => parseView(window.location.search));
  const [tlHeight, setTlHeight] = useState<number>(() => {
    const saved = Number(localStorage.getItem("wk-tl-height"));
    return Number.isFinite(saved) && saved >= TL_COLLAPSED ? saved : TL_DEFAULT;
  });
  const collapsed = tlHeight <= TL_COLLAPSED;
  const lastExpanded = useRef(TL_DEFAULT);
  if (!collapsed) lastExpanded.current = tlHeight;
  useEffect(() => {
    localStorage.setItem("wk-tl-height", `${tlHeight}`);
  }, [tlHeight]);

  const onHandleDown = (e: React.PointerEvent<HTMLDivElement>) => {
    e.preventDefault();
    const startY = e.clientY;
    const startH = tlHeight;
    const onMove = (ev: PointerEvent) => {
      const raw = startH + (startY - ev.clientY);
      if (raw < TL_COLLAPSE_BELOW) {
        setTlHeight(TL_COLLAPSED);
      } else {
        setTlHeight(Math.min(tlMax(), Math.max(TL_MIN, raw)));
      }
    };
    const onUp = () => {
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
    };
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
  };
  const toggleCollapsed = () =>
    setTlHeight(collapsed ? Math.max(TL_MIN, lastExpanded.current) : TL_COLLAPSED);

  // Inspector width: draggable between 260 and 560px, persisted.
  const [inspWidth, setInspWidth] = useState<number>(() => {
    const saved = Number(localStorage.getItem("wk-insp-width"));
    return Number.isFinite(saved) && saved >= 260 && saved <= 560 ? saved : 330;
  });
  useEffect(() => {
    localStorage.setItem("wk-insp-width", `${inspWidth}`);
  }, [inspWidth]);
  const onInspHandleDown = (e: React.PointerEvent<HTMLDivElement>) => {
    e.preventDefault();
    const startX = e.clientX;
    const startW = inspWidth;
    const onMove = (ev: PointerEvent) =>
      setInspWidth(Math.min(560, Math.max(260, startW + (startX - ev.clientX))));
    const onUp = () => {
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
    };
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
  };

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
    const onPop = () => {
      const v = parseView(window.location.search);
      // History navigation is not a new selection: sync the ref so the URL
      // normalization write below stays a replaceState - a pushState here
      // would destroy the forward history stack.
      lastPushedSel.current = v.selected;
      setView(v);
    };
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
  // The cursor is the moment the map is showing (FE-3/FE-5), and the map
  // follows it wherever curated data exists (DEV-6 M4).
  const tc = cursorTime(view);
  const eraLayer = useTimeLayer(manifest, tc, BORDERS_LAYER);
  const paleoLayer = useTimeLayer(manifest, tc, PALEO_LAYER);
  const era = eraLayer.slice;
  const paleo = paleoLayer.slice;
  const cursorYear = secondsToYear(tc);
  const mode = mapMode({
    year: cursorYear,
    hasPaleo: !!paleo,
    hasEra: !!era,
    eraTo: eraLayer.coverage?.to ?? null,
  });

  // War focus: a selected entity carrying dated front positions (DM-7) gets
  // its front interpolated to the cursor and the map framed on the theatre.
  // Only line geometry is a front. A future polygon record on the same entity
  // (a battlefield extent, say) is a different thing and must not be
  // interpolated as one.
  const frontPositions = useMemo(
    () => selectedDoc?.geometry?.filter((g) => g.geometry.type === "LineString"),
    [selectedDoc],
  );
  const front = useMemo(
    () => (frontPositions?.length ? frontAt(frontPositions, tc) : null),
    [frontPositions, tc],
  );
  const focusBounds = useMemo(
    () => (frontPositions?.length ? frontBounds(frontPositions) : null),
    [frontPositions],
  );

  const setRange = useCallback(
    (t0: number, t1: number) => setView((v) => ({ ...v, t0, t1 })),
    [],
  );
  const setSelected = useCallback(
    (slug: string | null) => setView((v) => ({ ...v, selected: slug })),
    [],
  );
  const setCursor = useCallback((t: number | null) => setView((v) => ({ ...v, tc: t })), []);
  const setSpace = useCallback(
    (next: number) => setView((v) => ({ ...v, space: Math.max(0, Math.min(SPACE_MAX, next)) })),
    [],
  );
  const enterSpace = useCallback(() => setSpace(0.25), [setSpace]);
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

  const cursorLabel = formatTime(tc, view.t1 - view.t0);
  // At most one area layer covers any moment, so the chip has one subject.
  const mapLayer = paleo ?? era;
  // Older than every reconstruction the globe says so, and says why.
  const voidLabel =
    mode === "void" ? voidChipLabel(cursorYear, paleoLayer.coverage?.from ?? null) : null;

  // One visibility judgement, shared by the map, the lanes and the count.
  const visibility = useMemo(
    () => ({ t0: view.t0, t1: view.t1, cursor: tc, selected: view.selected }),
    [view.t0, view.t1, tc, view.selected],
  );
  const displayItems = useMemo<ChunkItem[]>(() => {
    if (!selectedDoc || items.some((i) => i.slug === selectedDoc.slug)) return items;
    return [
      ...items,
      {
        slug: selectedDoc.slug,
        type: selectedDoc.type,
        name: selectedDoc.name,
        t0: selectedDoc.temporal.t0,
        t1: selectedDoc.temporal.t1,
        precision: selectedDoc.temporal.precision,
        status: selectedDoc.temporal.status,
        point: selectedDoc.point,
        categories: selectedDoc.categories,
        importance: selectedDoc.importance,
        media_thumb: selectedDoc.media_thumb,
        child_count: selectedDoc.children?.length,
      },
    ];
  }, [items, selectedDoc]);
  const mapVisible = useMemo(() => mapItems(displayItems, visibility), [displayItems, visibility]);
  // The status-bar count matches what the timeline lanes actually show
  // (top-100 after declutter), not the raw chunk union.
  const laneCount = useMemo(() => laneItems(displayItems, visibility).length, [displayItems, visibility]);

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
          <em>Timeline</em>
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
        <div className="stage">
          <MapView
            items={mapVisible}
            selected={view.selected}
            mode={mode}
            era={era}
            paleo={paleo}
            front={front}
            focusBounds={focusBounds}
            onSelect={setSelected}
            onZoomPastGlobe={enterSpace}
          />
          {/* Say what the overlay is, and say when there is nothing to show
              rather than leaving the map silently modern. */}
          <div className="map-chips">
            <div
              className={`era-chip ${mapLayer ? "" : "empty"} ${paleo ? "paleo" : ""} ${
                mode === "void" ? "void" : ""
              }`}
            >
              {mapLayer
                ? mapLayer.label
                : (voidLabel ?? `no map data for ${cursorLabel}`)}
            </div>
            {front && (
              <div className={`front-chip ${front.held ? "held" : ""}`}>
                <b>{cursorLabel}</b> front line
                {front.label && <span className="fc-near"> · nearest trace: {front.label}</span>}
                {front.held && <span className="fc-held"> · held: cursor is outside the war</span>}
              </div>
            )}
          </div>
          {view.space > 0 && (
            <SpaceView s={view.space} onZoom={setSpace} onSelect={setSelected} />
          )}
        </div>
        <div
          className="insp-resize-handle"
          title="Drag to resize the inspector"
          onPointerDown={onInspHandleDown}
        />
        <Inspector
          doc={selectedDoc}
          width={inspWidth}
          onSelect={setSelected}
          onFocusTime={setRange}
          onClose={() => setSelected(null)}
        />
      </div>

      <footer className={`timeline-shell ${collapsed ? "collapsed" : ""}`} style={{ height: tlHeight }}>
        <div
          className="tl-resize-handle"
          title="Drag to resize the timeline; drag down to collapse"
          onPointerDown={onHandleDown}
          onDoubleClick={toggleCollapsed}
        />
        {!collapsed && (
          <Timeline
            t0={view.t0}
            t1={view.t1}
            items={displayItems}
            selected={view.selected}
            tc={view.tc}
            onRange={setRange}
            onSelect={setSelected}
            onCursor={setCursor}
          />
        )}
        <div className="tl-status">
          <button
            className="tl-toggle"
            title={collapsed ? "Expand the timeline" : "Collapse the timeline"}
            onClick={toggleCollapsed}
          >
            {collapsed ? "▲" : "▼"}
          </button>
          <span className="bucket-badge">{manifest.buckets[bucket]?.id}</span>
          <span className="range-label">{rangeLabel}</span>
          <span className={`cursor-readout ${view.tc === null ? "unpinned" : ""}`}>
            <span className="cur-label" title="The moment the map is showing">
              ▾ {cursorLabel}
            </span>
            {view.tc !== null && (
              <button
                className="cur-unpin"
                title="Unpin the cursor: it follows the middle of the view again"
                onClick={() => setCursor(null)}
              >
                ×
              </button>
            )}
          </span>
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
