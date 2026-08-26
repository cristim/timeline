// FE-3: MapLibre map, time-synchronized with the timeline. M3 uses the free
// MapLibre demotiles world basemap; PMTiles layers arrive with M4.
import { useEffect, useRef } from "react";
import * as maplibregl from "maplibre-gl";
import "maplibre-gl/dist/maplibre-gl.css";
import type { FeatureCollection, Point } from "geojson";
import type { ChunkItem } from "../lib/data";
import { categoryColor, FALLBACK_COLOR } from "../lib/colors";

const STYLE_URL = "https://demotiles.maplibre.org/style.json";
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
    });
    // Globe projection where the MapLibre build supports it (v5+).
    const setProjection = (map as unknown as { setProjection?: (p: { type: string }) => void }).setProjection;
    if (typeof setProjection === "function") {
      map.on("style.load", () => setProjection.call(map, { type: "globe" }));
    }
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
    if (import.meta.env.DEV) {
      // e2e test hook (dev server only; stripped from prod builds)
      (window as unknown as Record<string, unknown>).__wkmap = map;
    }
    return () => {
      container.removeEventListener("wheel", onWheelCapture, { capture: true });
      readyRef.current = false;
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
