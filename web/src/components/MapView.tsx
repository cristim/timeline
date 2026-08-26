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
import { categoryColor, FALLBACK_COLOR } from "../lib/colors";

// The globe-ready demotiles style (projection: globe baked in). The plain
// style.json renders an empty pale sphere under globe projection.
const STYLE_URL = "https://demotiles.maplibre.org/globe.json";
const SOURCE = "wk-items";

// Historical extents get a deep version of the politics palette colour,
// because that is what they are. It has to be dark: the demotiles basemap is
// bright pastel, and the category-palette periwinkle disappears against it.
// Two source slots so one era can fade out while the next fades in (FE-2 asks
// for crossfades between datasets, not hard cuts).
const ERA_SLOTS = ["a", "b"] as const;
const ERA_FILL = "#2f4487";
const ERA_LINE = "#16224a";
const ERA_FADE_MS = 450;
const EMPTY_FC: FeatureCollection = { type: "FeatureCollection", features: [] };

interface Props {
  items: ChunkItem[];
  selected: string | null;
  /** Historical extents for the cursor time, or null when none are curated. */
  era: BorderLayerDoc | null;
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

/** Adds the fill + outline layers for one era slot, below `beforeId`. */
function addEraLayers(map: maplibregl.Map, slot: string, beforeId?: string) {
  const source = `wk-era-${slot}`;
  map.addSource(source, { type: "geojson", data: EMPTY_FC });
  map.addLayer(
    {
      id: `${source}-fill`,
      type: "fill",
      source,
      paint: {
        "fill-color": ERA_FILL,
        "fill-opacity": 0,
        "fill-opacity-transition": { duration: ERA_FADE_MS },
      },
    },
    beforeId,
  );
  map.addLayer(
    {
      id: `${source}-line`,
      type: "line",
      source,
      paint: {
        "line-color": ERA_LINE,
        "line-width": 2,
        // Every curated extent is representation=estimated (DM-7), and FE-3
        // wants that drawn as a hedge, not a hard border. An exact layer would
        // need its own solid line layer: line-dasharray is not data-driven.
        "line-dasharray": [3, 2],
        "line-opacity": 0,
        "line-opacity-transition": { duration: ERA_FADE_MS },
      },
    },
    beforeId,
  );
}

function setEraOpacity(map: maplibregl.Map, slot: string, on: boolean) {
  map.setPaintProperty(`wk-era-${slot}-fill`, "fill-opacity", on ? 0.38 : 0);
  map.setPaintProperty(`wk-era-${slot}-line`, "line-opacity", on ? 0.9 : 0);
}

export function MapView({ items, selected, era, onSelect, onZoomPastGlobe }: Props) {
  const containerRef = useRef<HTMLDivElement>(null);
  const mapRef = useRef<maplibregl.Map | null>(null);
  const readyRef = useRef(false);
  const dataRef = useRef<FeatureCollection>({ type: "FeatureCollection", features: [] });
  const eraRef = useRef<BorderLayerDoc | null>(era);
  const liveEraSlot = useRef<(typeof ERA_SLOTS)[number]>("a");
  const shownEra = useRef<number | null>(null);
  const onSelectRef = useRef(onSelect);
  onSelectRef.current = onSelect;
  const onZoomPastGlobeRef = useRef(onZoomPastGlobe);
  onZoomPastGlobeRef.current = onZoomPastGlobe;

  /**
   * Crossfades to `next` by loading it into the idle slot and swapping the
   * opacities; MapLibre animates both through the paint transitions. The
   * shownEra guard keeps a re-render from restarting a fade that is already
   * running, and keeps a fast drag across two era boundaries from leaving
   * both slots visible.
   */
  const applyEra = useCallback((map: maplibregl.Map, next: BorderLayerDoc | null) => {
    const year = next?.properties.year ?? null;
    if (year === shownEra.current) return;
    shownEra.current = year;
    if (!next) {
      for (const slot of ERA_SLOTS) setEraOpacity(map, slot, false);
      return;
    }
    const slot = liveEraSlot.current === "a" ? "b" : "a";
    (map.getSource(`wk-era-${slot}`) as maplibregl.GeoJSONSource | undefined)?.setData(next);
    setEraOpacity(map, slot, true);
    setEraOpacity(map, liveEraSlot.current, false);
    liveEraSlot.current = slot;
  }, []);

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
    map.addControl(
      new maplibregl.AttributionControl({
        compact: true,
        customAttribution: "© OpenStreetMap contributors · Wikidata CC0",
      }),
    );
    map.addControl(new maplibregl.NavigationControl({ showCompass: false }), "bottom-right");
    map.on("load", () => {
      // Era layers first, so the item dots and halos always draw over them.
      for (const slot of ERA_SLOTS) addEraLayers(map, slot);
      for (const slot of ERA_SLOTS) {
        map.on("click", `wk-era-${slot}-fill`, (e: maplibregl.MapLayerMouseEvent) => {
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
      readyRef.current = true;
      // An era can resolve before the style finishes loading; apply whatever
      // the latest render handed us rather than waiting for the next change.
      applyEra(map, eraRef.current);
    });
    mapRef.current = map;
    liveMap = map;
    if (import.meta.env.DEV) {
      // e2e test hook (dev server only; stripped from prod builds)
      (window as unknown as Record<string, unknown>).__wkmap = map;
    }
    return () => {
      container.removeEventListener("wheel", onWheelCapture, { capture: true });
      readyRef.current = false;
      liveMap = null;
      map.remove();
      mapRef.current = null;
      // The next map starts with both era slots empty and transparent, so the
      // "what is already shown" bookkeeping has to start over with it.
      shownEra.current = null;
      liveEraSlot.current = "a";
    };
  }, [applyEra]);

  useEffect(() => {
    eraRef.current = era;
    const map = mapRef.current;
    if (map && readyRef.current) applyEra(map, era);
    if (import.meta.env.DEV) {
      // e2e test hook (dev server only; stripped from prod builds)
      (window as unknown as Record<string, unknown>).__wkera = era;
    }
  }, [era, applyEra]);

  useEffect(() => {
    dataRef.current = toGeoJSON(items, selected);
    const map = mapRef.current;
    if (map && readyRef.current) {
      (map.getSource(SOURCE) as maplibregl.GeoJSONSource | undefined)?.setData(dataRef.current);
    }
  }, [items, selected]);

  // Fly to a selected entity that has a location.
  useEffect(() => {
    const map = mapRef.current;
    if (!map || !selected) return;
    const feature = dataRef.current.features.find((f) => f.properties?.slug === selected);
    const coords = (feature?.geometry as Point | undefined)?.coordinates;
    if (coords) {
      map.flyTo({ center: coords as [number, number], zoom: Math.max(map.getZoom(), 3), duration: 700 });
    }
  }, [selected]);

  return <div ref={containerRef} className="map-container" />;
}
