// FE-3: MapLibre map, time-synchronized with the timeline. M3 uses the free
// MapLibre demotiles world basemap; PMTiles layers arrive with M4.
import { useEffect, useRef } from "react";
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
import type { ChunkItem } from "../lib/data";
import { categoryColor, FALLBACK_COLOR } from "../lib/colors";

// The globe-ready demotiles style (projection: globe baked in). The plain
// style.json renders an empty pale sphere under globe projection.
const STYLE_URL = "https://demotiles.maplibre.org/globe.json";
const SOURCE = "wk-items";

interface Props {
  items: ChunkItem[];
  selected: string | null;
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

export function MapView({ items, selected, onSelect, onZoomPastGlobe }: Props) {
  const containerRef = useRef<HTMLDivElement>(null);
  const mapRef = useRef<maplibregl.Map | null>(null);
  const readyRef = useRef(false);
  const dataRef = useRef<FeatureCollection>({ type: "FeatureCollection", features: [] });
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
    map.addControl(
      new maplibregl.AttributionControl({
        compact: true,
        customAttribution: "© OpenStreetMap contributors · Wikidata CC0",
      }),
    );
    map.addControl(new maplibregl.NavigationControl({ showCompass: false }), "bottom-right");
    map.on("load", () => {
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
    };
  }, []);

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
