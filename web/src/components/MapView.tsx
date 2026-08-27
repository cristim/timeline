// FE-3: MapLibre map, time-synchronized with the timeline. M3 uses the free
// MapLibre demotiles world basemap; PMTiles layers arrive with M4.
import { useEffect, useMemo, useRef, useState } from "react";
import * as maplibregl from "maplibre-gl";
import maplibreWorkerUrl from "maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url";
import "maplibre-gl/dist/maplibre-gl.css";

// MapLibre v6 resolves its worker (and the worker's ./maplibre-gl-shared.mjs
// import) as siblings of import.meta.url, which only holds when served from
// node_modules (dev). In the bundled build those files don't exist and the map
// silently never loads tiles; ?worker&url makes Vite bundle the worker with
// its dependency and hand back a servable URL.
maplibregl.setWorkerUrl(maplibreWorkerUrl);
import type { FeatureCollection, Point } from "geojson";
import type { BorderLayerDoc, ChunkItem } from "../lib/data";
import type { FrontSample } from "../lib/fronts";
import type { MapMode } from "../lib/mapmode";
import { categoryColor, FALLBACK_COLOR, polityColor } from "../lib/colors";
import { devHook } from "../lib/devhook";

// The globe-ready demotiles style (projection: globe baked in). The plain
// style.json renders an empty pale sphere under globe projection.
const STYLE_URL = "https://demotiles.maplibre.org/globe.json";
const SOURCE = "wk-items";

// Two source slots so one slice can fade out while the next fades in (FE-2
// asks for crossfades between datasets, not hard cuts).
const SLOTS = ["a", "b"] as const;
const FADE_MS = 450;
const EMPTY_FC: FeatureCollection = { type: "FeatureCollection", features: [] };

/** How one time-sliced area layer is painted. */
interface LayerStyle {
  /** A colour, or a MapLibre expression reading one off each feature. */
  fill: NonNullable<maplibregl.AllPaintProperties["fill-color"]>;
  line: string;
  fillOpacity: number;
  lineOpacity: number;
  lineWidth: number;
}

// A political slice REPLACES the modern political map rather than washing over
// it: opaque polities, each its own colour off the name hash, on a neutral land
// base. A translucent overlay left modern Germany legible under the 1500 map
// and gave no way to tell which borders were which.
const ERA_STYLE: LayerStyle = {
  fill: ["get", "color"],
  line: "#101726",
  fillOpacity: 1,
  lineOpacity: 0.85,
  lineWidth: 1.2,
};

// Deep time is the same problem, one step further: none of the modern
// geography existed, not even the coastlines. Reconstructed landmasses on the
// same opaque ocean, in one colour because the source knows of only one thing
// ("land" - the GPlates coastlines carry no names).
const PALEO_STYLE: LayerStyle = {
  fill: "#9a8c66",
  line: "#6d6144",
  fillOpacity: 1,
  lineOpacity: 0.85,
  lineWidth: 1,
};

// The globe's own surface, under everything we draw and over everything the
// basemap draws.
const OCEAN = "#16384f";
/** No reconstruction exists: a dead slate sphere, deliberately not ocean blue. */
const VOID_SURFACE = "#222a37";
/** Modern coastlines as a neutral base for the political slices to sit on. */
const LAND = "#3b4455";
const OCEAN_SOURCE = "wk-base-ocean";
const OCEAN_LAYER = "wk-base-ocean-fill";
const MODERN_LAND_LAYER = "wk-modern-land";

// The demotiles source and source-layer that hold modern country polygons.
// Reused as the neutral land base: they are the right geometry for recorded
// history, they are already downloaded, and coastlines have barely moved.
const BASEMAP_SOURCE = "maplibre";
const BASEMAP_COUNTRIES = "countries";

/**
 * The basemap layers that assert something about the *modern* world, and the
 * paint property that hides each. Zeroed whenever a slice is on screen, so a
 * modern label or frontier cannot read as part of the historical map.
 *
 * Opacity rather than `visibility: none` on purpose: a hidden layer stops its
 * source loading tiles, and `countries-fill` - which stays live under the
 * opaque ocean - is what keeps the vector tiles coming for the land layer.
 */
const MODERN_ASSERTIONS: [layer: string, paint: keyof maplibregl.AllPaintProperties][] = [
  ["coastline", "line-opacity"],
  ["countries-boundary", "line-opacity"],
  ["countries-label", "text-opacity"],
  ["geolines", "line-opacity"],
  ["geolines-label", "text-opacity"],
  ["crimea-fill", "fill-opacity"],
];

/**
 * The globe, as four 90-degree longitude bands. This is the sphere's surface
 * whenever the basemap is not it: a `background` layer would paint the space
 * around the sphere too.
 *
 * Four bands rather than one -180..180 rectangle because a polygon spanning
 * the entire longitude range is ambiguous - it describes both the sphere and
 * its complement - and globe projection resolved it by leaving a wedge near
 * the antimeridian unpainted, through which the modern basemap showed. Each
 * band is also densified, so projection bends its edges instead of chording
 * them across the poles.
 */
function worldPolygon(): FeatureCollection {
  const band = (west: number, east: number): [number, number][] => {
    const ring: [number, number][] = [];
    for (let lon = west; lon <= east; lon += 5) ring.push([lon, -90]);
    for (let lat = -90; lat <= 90; lat += 5) ring.push([east, lat]);
    for (let lon = east; lon >= west; lon -= 5) ring.push([lon, 90]);
    for (let lat = 90; lat >= -90; lat -= 5) ring.push([west, lat]);
    return ring;
  };
  return {
    type: "FeatureCollection",
    features: [-180, -90, 0, 90].map((west) => ({
      type: "Feature",
      properties: {},
      geometry: { type: "Polygon", coordinates: [band(west, west + 90)] },
    })),
  };
}

// The front line uses the war category colour, since that is what it is.
const FRONT_SOURCE = "wk-front";
const FRONT_COLOR = "#c96b4a";

interface Props {
  items: ChunkItem[];
  selected: string | null;
  /** What kind of world to draw at the cursor (lib/mapmode.ts). */
  mode: MapMode;
  /** Political extents for the cursor time, or null outside recorded history. */
  era: BorderLayerDoc | null;
  /** Reconstructed coastlines when the cursor is in deep time, else null. */
  paleo: BorderLayerDoc | null;
  /** The selected war's front at the cursor time, or null for everything else. */
  front: FrontSample | null;
  /** Bounds framing the selection's geometry, when it has any. */
  focusBounds: [[number, number], [number, number]] | null;
  onSelect: (slug: string) => void;
  /** Called when the user zooms out past the whole globe (cosmic handoff). */
  onZoomPastGlobe: () => void;
}

/** Below this zoom the whole globe is on screen; further zoom-out leaves it. */
const GLOBE_MIN_ZOOM = 1.05;

// Live map singleton for the space view's Earth capture (one map per app).
let liveMap: maplibregl.Map | null = null;

/**
 * Snapshots the currently rendered globe into a square sprite (the space
 * view's Earth texture, giving pixel-continuity across the handoff).
 * Returns null before the map has rendered anything usable.
 */
export function captureGlobeSprite(): HTMLCanvasElement | null {
  if (!liveMap) return null;
  const src = liveMap.getCanvas();
  if (!src.width || !src.height) return null;
  const dpr = window.devicePixelRatio || 1;
  // Globe pixel radius: worldSize / 2π (tileSize 512).
  const r = ((512 * Math.pow(2, liveMap.getZoom())) / (2 * Math.PI)) * dpr;
  const side = Math.min(2 * r * 1.02, Math.min(src.width, src.height));
  const out = document.createElement("canvas");
  out.width = out.height = 512;
  const ctx = out.getContext("2d");
  if (!ctx) return null;
  ctx.drawImage(src, src.width / 2 - side / 2, src.height / 2 - side / 2, side, side, 0, 0, 512, 512);
  // A blank capture (context lost, buffer not preserved) must not be used.
  const probe = ctx.getImageData(256, 256, 1, 1).data;
  if (probe[3] === 0 || (probe[0] === 0 && probe[1] === 0 && probe[2] === 0)) {
    return null;
  }
  return out;
}

function toGeoJSON(items: ChunkItem[], selected: string | null): FeatureCollection {
  return {
    type: "FeatureCollection",
    features: items
      .filter((i) => i.point)
      .map((i) => ({
        type: "Feature",
        geometry: { type: "Point", coordinates: i.point! },
        properties: {
          slug: i.slug,
          name: i.name,
          color: i.categories.map((c) => categoryColor[c]).find(Boolean) ?? FALLBACK_COLOR,
          selected: i.slug === selected ? 1 : 0,
        },
      })),
  };
}

/**
 * The two-slot crossfade for one time-sliced area layer. Both area layers
 * (political borders, paleo coastlines) behave identically: load the incoming
 * slice into the idle slot, and swap opacities only once it has actually
 * parsed. Raising the incoming slot straight after setData would fade in an
 * empty layer and pop the shapes in afterwards, which is the hard cut the two
 * slots exist to avoid.
 */
class SlotPair {
  private live: (typeof SLOTS)[number] = "a";
  private shown: number | null = null;
  private pendingFade: (() => void) | null = null;

  constructor(
    private readonly kind: string,
    private readonly style: LayerStyle,
    private readonly dashed: boolean,
  ) {}

  add(map: maplibregl.Map) {
    for (const slot of SLOTS) {
      const source = `wk-${this.kind}-${slot}`;
      map.addSource(source, { type: "geojson", data: EMPTY_FC });
      map.addLayer({
        id: `${source}-fill`,
        type: "fill",
        source,
        paint: {
          "fill-color": this.style.fill,
          "fill-opacity": 0,
          "fill-opacity-transition": { duration: FADE_MS },
        },
      });
      map.addLayer({
        id: `${source}-line`,
        type: "line",
        source,
        paint: {
          "line-color": this.style.line,
          "line-width": this.style.lineWidth,
          // Every slice is representation=estimated (DM-7), and FE-3 wants
          // that drawn as a hedge, not a hard border. An exact layer would
          // need its own solid line layer: line-dasharray is not data-driven.
          ...(this.dashed ? { "line-dasharray": [3, 2] } : {}),
          "line-opacity": 0,
          "line-opacity-transition": { duration: FADE_MS },
        },
      });
    }
  }

  fillLayerIds(): string[] {
    return SLOTS.map((s) => `wk-${this.kind}-${s}-fill`);
  }

  /** The slot currently faded in - the one a hover should be read against. */
  liveFillLayerId(): string {
    return `wk-${this.kind}-${this.live}-fill`;
  }

  private setOpacity(map: maplibregl.Map, slot: string, on: boolean) {
    map.setPaintProperty(
      `wk-${this.kind}-${slot}-fill`,
      "fill-opacity",
      on ? this.style.fillOpacity : 0,
    );
    map.setPaintProperty(
      `wk-${this.kind}-${slot}-line`,
      "line-opacity",
      on ? this.style.lineOpacity : 0,
    );
  }

  /**
   * The `shown` guard keeps a re-render from restarting a fade already
   * running; `pendingFade` keeps a fast drag across two slice boundaries from
   * leaving both slots visible.
   */
  apply(map: maplibregl.Map, next: BorderLayerDoc | null) {
    const year = next?.properties.year ?? null;
    if (year === this.shown) return;
    this.shown = year;
    this.pendingFade?.(); // settle any half-finished swap first
    this.pendingFade = null;

    if (!next) {
      for (const slot of SLOTS) this.setOpacity(map, slot, false);
      return;
    }
    const slot = this.live === "a" ? "b" : "a";
    const outgoing = this.live;
    this.live = slot;

    const swap = () => {
      map.off("sourcedata", onSourceData);
      this.pendingFade = null;
      this.setOpacity(map, slot, true);
      this.setOpacity(map, outgoing, false);
    };
    const sourceId = `wk-${this.kind}-${slot}`;
    const onSourceData = (e: maplibregl.MapSourceDataEvent) => {
      if (e.sourceId === sourceId && e.isSourceLoaded) swap();
    };
    this.pendingFade = swap;
    map.on("sourcedata", onSourceData);
    (map.getSource(sourceId) as maplibregl.GeoJSONSource | undefined)?.setData(next);
  }

  /** A new map starts with both slots empty and transparent. */
  reset() {
    this.live = "a";
    this.shown = null;
    this.pendingFade = null;
  }
}

/**
 * Gives every polity in a slice its own fill colour, so a political map reads
 * as many countries rather than one wash. Done here rather than in the baker:
 * it is a rendering choice, and the artifacts are immutable and cached for a
 * year.
 */
function withPolityColors(doc: BorderLayerDoc): BorderLayerDoc {
  return {
    ...doc,
    features: doc.features.map((f) => ({
      ...f,
      properties: { ...f.properties, color: polityColor(f.properties.name) },
    })),
  };
}

/** Paints the globe's surface and the basemap for one mode. */
function applyMode(map: maplibregl.Map, mode: MapMode) {
  const modern = mode === "modern";
  map.setPaintProperty(OCEAN_LAYER, "fill-color", mode === "void" ? VOID_SURFACE : OCEAN);
  map.setPaintProperty(OCEAN_LAYER, "fill-opacity", modern ? 0 : 1);
  if (map.getLayer(MODERN_LAND_LAYER)) {
    map.setPaintProperty(MODERN_LAND_LAYER, "fill-opacity", mode === "political" ? 1 : 0);
  }
  for (const [layer, paint] of MODERN_ASSERTIONS) {
    if (map.getLayer(layer)) map.setPaintProperty(layer, paint, modern ? 1 : 0);
  }
}

/** GeoJSON for the front sample, or an empty collection when there is none. */
function frontFC(front: FrontSample | null): FeatureCollection {
  if (!front) return EMPTY_FC;
  return {
    type: "FeatureCollection",
    features: [
      {
        type: "Feature",
        properties: {},
        geometry: { type: "LineString", coordinates: front.coordinates },
      },
    ],
  };
}

/** A polity name pinned under the pointer, in map-container pixels. */
interface Tip {
  x: number;
  y: number;
  name: string;
}

export function MapView({
  items,
  selected,
  mode,
  era,
  paleo,
  front,
  focusBounds,
  onSelect,
  onZoomPastGlobe,
}: Props) {
  const containerRef = useRef<HTMLDivElement>(null);
  const mapRef = useRef<maplibregl.Map | null>(null);
  const readyRef = useRef(false);
  const dataRef = useRef<FeatureCollection>({ type: "FeatureCollection", features: [] });
  const painted = useMemo(() => (era ? withPolityColors(era) : null), [era]);
  const eraRef = useRef<BorderLayerDoc | null>(painted);
  const paleoRef = useRef<BorderLayerDoc | null>(paleo);
  const modeRef = useRef<MapMode>(mode);
  const eraSlots = useRef(new SlotPair("era", ERA_STYLE, true));
  const paleoSlots = useRef(new SlotPair("paleo", PALEO_STYLE, false));
  const frontRef = useRef<FeatureCollection>(frontFC(front));
  const [tip, setTip] = useState<Tip | null>(null);
  const onSelectRef = useRef(onSelect);
  onSelectRef.current = onSelect;
  const onZoomPastGlobeRef = useRef(onZoomPastGlobe);
  onZoomPastGlobeRef.current = onZoomPastGlobe;

  useEffect(() => {
    if (!containerRef.current) return;
    const map = new maplibregl.Map({
      container: containerRef.current,
      style: STYLE_URL,
      center: [15, 35],
      zoom: 1.4,
      minZoom: GLOBE_MIN_ZOOM,
      attributionControl: false,
      // The space view snapshots the globe as its Earth texture on handoff;
      // without a preserved buffer, drawImage from the WebGL canvas is blank.
      canvasContextAttributes: { preserveDrawingBuffer: true },
    } as maplibregl.MapOptions);
    // Wheel-out at the globe's minimum zoom hands off to the cosmic view.
    // Capture phase so MapLibre's own (exhausted) zoom handling never sees it.
    const container = containerRef.current;
    const onWheelCapture = (e: WheelEvent) => {
      if (e.deltaY > 0 && map.getZoom() <= GLOBE_MIN_ZOOM + 0.01) {
        e.preventDefault();
        e.stopPropagation();
        onZoomPastGlobeRef.current();
      }
    };
    container.addEventListener("wheel", onWheelCapture, { capture: true, passive: false });
    // Both fetched layers carry licence conditions that require naming their
    // source wherever the data is shown, so they are named here rather than
    // only in the README. Full terms and citations: data/geo/*/NOTICE.md.
    map.addControl(
      new maplibregl.AttributionControl({
        compact: true,
        customAttribution: [
          "© OpenStreetMap contributors",
          "Wikidata CC0",
          '<a href="https://github.com/aourednik/historical-basemaps">historical-basemaps</a> GPL-3.0',
          '<a href="https://doi.org/10.1016/j.earscirev.2020.103477">GPlates/Merdith et al. 2021</a> CC-BY 4.0',
        ].join(" · "),
      }),
    );
    map.addControl(new maplibregl.NavigationControl({ showCompass: false }), "bottom-right");
    map.on("load", () => {
      // The globe's surface goes down first: an opaque sphere over the whole
      // basemap. Then the neutral modern land for political mode, then the two
      // area layers, then the front, the halos and the dots. The two area
      // layers never show together (their coverage windows are disjoint), so
      // the order between them only decides what happens mid-crossfade at the
      // boundary.
      map.addSource(OCEAN_SOURCE, { type: "geojson", data: worldPolygon() });
      map.addLayer({
        id: OCEAN_LAYER,
        type: "fill",
        source: OCEAN_SOURCE,
        paint: {
          "fill-color": VOID_SURFACE,
          "fill-opacity": 0,
          "fill-color-transition": { duration: FADE_MS },
          "fill-opacity-transition": { duration: FADE_MS },
        },
      });
      // Recorded history needs a land base under the polities, and modern
      // coastlines are the right one at this scale. If the upstream style ever
      // drops the source, say so rather than silently reverting to a modern
      // political map under the historical one - the exact bug this replaces.
      if (map.getSource(BASEMAP_SOURCE)) {
        map.addLayer({
          id: MODERN_LAND_LAYER,
          type: "fill",
          source: BASEMAP_SOURCE,
          "source-layer": BASEMAP_COUNTRIES,
          paint: {
            "fill-color": LAND,
            "fill-opacity": 0,
            "fill-opacity-transition": { duration: FADE_MS },
          },
        });
      } else {
        console.error(
          `basemap source "${BASEMAP_SOURCE}" is missing: political slices will draw on bare ocean`,
        );
      }
      paleoSlots.current.add(map);
      eraSlots.current.add(map);
      for (const id of eraSlots.current.fillLayerIds()) {
        map.on("click", id, (e: maplibregl.MapLayerMouseEvent) => {
          // A dot on top of an extent is the more specific target, and its own
          // handler will take the click.
          if (map.queryRenderedFeatures(e.point, { layers: ["wk-dots"] }).length) return;
          const slug = e.features?.[0]?.properties?.slug as string | undefined;
          if (slug) onSelectRef.current(slug);
        });
      }

      map.addSource(SOURCE, { type: "geojson", data: dataRef.current });
      map.addLayer({
        id: "wk-halo",
        type: "circle",
        source: SOURCE,
        filter: ["==", ["get", "selected"], 1],
        paint: {
          "circle-radius": 12,
          "circle-color": "#e8b45a",
          "circle-opacity": 0.3,
        },
      });
      map.addLayer({
        id: "wk-dots",
        type: "circle",
        source: SOURCE,
        paint: {
          "circle-radius": ["case", ["==", ["get", "selected"], 1], 7, 5],
          "circle-color": ["case", ["==", ["get", "selected"], 1], "#e8b45a", ["get", "color"]],
          "circle-stroke-width": 1.4,
          "circle-stroke-color": "#e8e4d8",
        },
      });
      map.on("click", "wk-dots", (e: maplibregl.MapLayerMouseEvent) => {
        const slug = e.features?.[0]?.properties?.slug as string | undefined;
        if (slug) onSelectRef.current(slug);
      });
      map.on("mouseenter", "wk-dots", () => (map.getCanvas().style.cursor = "pointer"));
      map.on("mouseleave", "wk-dots", () => (map.getCanvas().style.cursor = ""));

      // The front line sits above the era fill but below the item dots: it is
      // the subject of a war focus, not the background and not a target.
      map.addSource(FRONT_SOURCE, { type: "geojson", data: frontRef.current });
      map.addLayer(
        {
          id: "wk-front-line",
          type: "line",
          source: FRONT_SOURCE,
          layout: { "line-cap": "round", "line-join": "round" },
          paint: {
            "line-color": FRONT_COLOR,
            "line-width": 3.5,
            // Interpolated between a handful of dated traces, so it is at
            // best representation=estimated (DM-7): never a hard line.
            "line-dasharray": [2, 1.2],
          },
        },
        "wk-halo",
      );
      // Polity names are the only thing the border slices carry beyond
      // geometry, and a coloured blob nobody can name is not much of a map.
      // Read against the live slot only: the outgoing one is still queryable
      // while it fades, and would answer with the previous century's polity.
      const onHover = (e: maplibregl.MapMouseEvent) => {
        const fill = eraSlots.current.liveFillLayerId();
        if (modeRef.current !== "political" || !map.getLayer(fill)) return setTip(null);
        // A dot is the more specific target, as it is for clicks.
        if (map.queryRenderedFeatures(e.point, { layers: ["wk-dots"] }).length) {
          return setTip(null);
        }
        const name = map.queryRenderedFeatures(e.point, { layers: [fill] })[0]?.properties
          ?.name;
        setTip(typeof name === "string" ? { x: e.point.x, y: e.point.y, name } : null);
      };
      map.on("mousemove", onHover);
      map.on("mouseout", () => setTip(null));
      // A drag is a pan, not a hover; the tooltip must not ride along.
      map.on("dragstart", () => setTip(null));

      readyRef.current = true;
      // A slice can resolve before the style finishes loading; apply whatever
      // the latest render handed us rather than waiting for the next change.
      applyMode(map, modeRef.current);
      eraSlots.current.apply(map, eraRef.current);
      paleoSlots.current.apply(map, paleoRef.current);
    });
    mapRef.current = map;
    liveMap = map;
    devHook("__wkmap", map);
    return () => {
      container.removeEventListener("wheel", onWheelCapture, { capture: true });
      readyRef.current = false;
      liveMap = null;
      map.remove();
      mapRef.current = null;
      // The next map starts with every slot empty and transparent, so the
      // "what is already shown" bookkeeping has to start over with it.
      eraSlots.current.reset();
      paleoSlots.current.reset();
    };
  }, []);

  useEffect(() => {
    modeRef.current = mode;
    const map = mapRef.current;
    if (map && readyRef.current) applyMode(map, mode);
    // Leaving political mode strands whatever name was under the pointer.
    if (mode !== "political") setTip(null);
    devHook("__wkmode", mode);
  }, [mode]);

  useEffect(() => {
    eraRef.current = painted;
    const map = mapRef.current;
    if (map && readyRef.current) eraSlots.current.apply(map, painted);
    devHook("__wkera", era);
  }, [painted, era]);

  useEffect(() => {
    paleoRef.current = paleo;
    const map = mapRef.current;
    if (map && readyRef.current) paleoSlots.current.apply(map, paleo);
    devHook("__wkpaleo", paleo);
  }, [paleo]);

  useEffect(() => {
    dataRef.current = toGeoJSON(items, selected);
    const map = mapRef.current;
    if (map && readyRef.current) {
      (map.getSource(SOURCE) as maplibregl.GeoJSONSource | undefined)?.setData(dataRef.current);
    }
  }, [items, selected]);

  useEffect(() => {
    frontRef.current = frontFC(front);
    const map = mapRef.current;
    if (map && readyRef.current) {
      (map.getSource(FRONT_SOURCE) as maplibregl.GeoJSONSource | undefined)?.setData(
        frontRef.current,
      );
    }
    devHook("__wkfront", front);
  }, [front]);

  // Frame a selection: a war with curated geometry gets its whole theatre,
  // anything else with a location gets flown to.
  useEffect(() => {
    const map = mapRef.current;
    if (!map || !selected) return;
    if (focusBounds) {
      map.fitBounds(focusBounds, { padding: 80, maxZoom: 5, duration: 900 });
      return;
    }
    const feature = dataRef.current.features.find((f) => f.properties?.slug === selected);
    const coords = (feature?.geometry as Point | undefined)?.coordinates;
    if (coords) {
      map.flyTo({ center: coords as [number, number], zoom: Math.max(map.getZoom(), 3), duration: 700 });
    }
  }, [selected, focusBounds]);

  return (
    <>
      <div ref={containerRef} className="map-container" />
      {tip && (
        <div className="map-tooltip" style={{ left: tip.x + 14, top: tip.y + 16 }}>
          {tip.name}
        </div>
      )}
    </>
  );
}
