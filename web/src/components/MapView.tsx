// FE-3: MapLibre map, time-synchronized with the timeline. M3 uses the free
// MapLibre demotiles world basemap; PMTiles layers arrive with M4.
import { useCallback, useEffect, useRef } from "react";
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
import { categoryColor, FALLBACK_COLOR } from "../lib/colors";
import { devHook } from "../lib/devhook";

// The globe-ready demotiles style (projection: globe baked in). The plain
// style.json renders an empty pale sphere under globe projection.
const STYLE_URL = "https://demotiles.maplibre.org/globe.json";
const SOURCE = "wk-items";

// Historical extents get a deep version of the politics palette colour,
// because that is what they are. It has to be dark: the demotiles basemap is
// bright pastel, and the category-palette periwinkle disappears against it.
// Two source slots so one slice can fade out while the next fades in (FE-2
// asks for crossfades between datasets, not hard cuts).
const SLOTS = ["a", "b"] as const;
const FADE_MS = 450;
const EMPTY_FC: FeatureCollection = { type: "FeatureCollection", features: [] };

/** How one time-sliced area layer is painted. */
interface LayerStyle {
  fill: string;
  line: string;
  fillOpacity: number;
  lineOpacity: number;
  lineWidth: number;
}

// Political borders sit over a legible modern basemap, so they are a
// translucent wash with a dashed frontier.
const ERA_STYLE: LayerStyle = {
  fill: "#2f4487",
  line: "#16224a",
  fillOpacity: 0.38,
  lineOpacity: 0.9,
  lineWidth: 2,
};

// Deep time is the opposite problem. The basemap's coastlines and countries
// are not merely decoration there, they are wrong: none of that geography
// existed. So the paleo layer paints an opaque ocean over the whole globe and
// puts the reconstructed landmasses on top, hiding the modern world entirely.
const PALEO_STYLE: LayerStyle = {
  fill: "#9a8c66",
  line: "#6d6144",
  fillOpacity: 1,
  lineOpacity: 0.85,
  lineWidth: 1,
};
const PALEO_OCEAN = "#16384f";
const OCEAN_SOURCE = "wk-paleo-ocean";

/**
 * A lon/lat rectangle covering the globe, densified so that globe projection
 * bends its edges instead of chording them. Used as the deep-time ocean: a
 * `background` layer would paint the space around the sphere too.
 */
function worldPolygon(): FeatureCollection {
  const ring: [number, number][] = [];
  for (let lon = -180; lon <= 180; lon += 5) ring.push([lon, -90]);
  for (let lat = -90; lat <= 90; lat += 5) ring.push([180, lat]);
  for (let lon = 180; lon >= -180; lon -= 5) ring.push([lon, 90]);
  for (let lat = 90; lat >= -90; lat -= 5) ring.push([-180, lat]);
  return {
    type: "FeatureCollection",
    features: [{ type: "Feature", properties: {}, geometry: { type: "Polygon", coordinates: [ring] } }],
  };
}

// The front line uses the war category colour, since that is what it is.
const FRONT_SOURCE = "wk-front";
const FRONT_COLOR = "#c96b4a";

interface Props {
  items: ChunkItem[];
  selected: string | null;
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

export function MapView({
  items,
  selected,
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
  const eraRef = useRef<BorderLayerDoc | null>(era);
  const paleoRef = useRef<BorderLayerDoc | null>(paleo);
  const eraSlots = useRef(new SlotPair("era", ERA_STYLE, true));
  const paleoSlots = useRef(new SlotPair("paleo", PALEO_STYLE, false));
  const frontRef = useRef<FeatureCollection>(frontFC(front));
  const onSelectRef = useRef(onSelect);
  onSelectRef.current = onSelect;
  const onZoomPastGlobeRef = useRef(onZoomPastGlobe);
  onZoomPastGlobeRef.current = onZoomPastGlobe;

  // The ocean fades with the layer it belongs to, so the handoff between deep
  // time and recorded history is a dissolve back to the modern basemap rather
  // than the world blinking.
  const applyPaleo = useCallback((map: maplibregl.Map, next: BorderLayerDoc | null) => {
    map.setPaintProperty("wk-paleo-ocean-fill", "fill-opacity", next ? 1 : 0);
    paleoSlots.current.apply(map, next);
  }, []);
  const applyPaleoRef = useRef(applyPaleo);
  applyPaleoRef.current = applyPaleo;

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
      // Deep time goes down first: the ocean hides the modern basemap, the
      // reconstructed land sits on it, and the political layers, item dots and
      // halos all draw above. The two area layers never show together (their
      // coverage windows are disjoint), so the order between them only decides
      // what happens mid-crossfade at the boundary.
      map.addSource(OCEAN_SOURCE, { type: "geojson", data: worldPolygon() });
      map.addLayer({
        id: "wk-paleo-ocean-fill",
        type: "fill",
        source: OCEAN_SOURCE,
        paint: {
          "fill-color": PALEO_OCEAN,
          "fill-opacity": 0,
          "fill-opacity-transition": { duration: FADE_MS },
        },
      });
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
      readyRef.current = true;
      // A slice can resolve before the style finishes loading; apply whatever
      // the latest render handed us rather than waiting for the next change.
      eraSlots.current.apply(map, eraRef.current);
      applyPaleoRef.current(map, paleoRef.current);
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
    eraRef.current = era;
    const map = mapRef.current;
    if (map && readyRef.current) eraSlots.current.apply(map, era);
    devHook("__wkera", era);
  }, [era]);

  useEffect(() => {
    paleoRef.current = paleo;
    const map = mapRef.current;
    if (map && readyRef.current) applyPaleo(map, paleo);
    devHook("__wkpaleo", paleo);
  }, [paleo, applyPaleo]);

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

  return <div ref={containerRef} className="map-container" />;
}
