// FE-3: MapLibre map, time-synchronized with the timeline.
import { useEffect, useRef, useState } from "react";
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
import type { AreaLayerSlice, ChunkItem } from "../lib/data";
import type { FrontSample } from "../lib/fronts";
import type { MapMode } from "../lib/mapmode";
import { categoryColor, FALLBACK_COLOR } from "../lib/colors";
import { devHook } from "../lib/devhook";
import { queryAreaFeature, SlotPair, type AreaLayerStyle } from "../lib/areaLayers";
import { registerPMTilesProtocol } from "../lib/pmtilesProtocol";

registerPMTilesProtocol();

// The globe-ready demotiles style (projection: globe baked in). The plain
// style.json renders an empty pale sphere under globe projection.
const STYLE_URL = "https://demotiles.maplibre.org/globe.json";
const SOURCE = "wk-items";

const FADE_MS = 450;
const EMPTY_FC: FeatureCollection = { type: "FeatureCollection", features: [] };

// Political slices replace the modern political map rather than overlay it.
const ERA_STYLE: AreaLayerStyle = {
  fill: ["get", "color"],
  line: "#101726",
  fillOpacity: 1,
  lineOpacity: 0.85,
  lineWidth: 1.2,
  dashed: true,
  attribution:
    "historical-basemaps (GPL-3.0): https://github.com/aourednik/historical-basemaps · OpenHistoricalMap (CC0/public domain): https://www.openhistoricalmap.org/",
};

// GPlates carries no feature names, so reconstructed land uses one colour.
const PALEO_STYLE: AreaLayerStyle = {
  fill: "#9a8c66",
  line: "#6d6144",
  fillOpacity: 1,
  lineOpacity: 0.85,
  lineWidth: 1,
  dashed: false,
  attribution:
    "Merdith et al. 2021 (CC-BY 4.0): https://doi.org/10.1016/j.earscirev.2020.103477",
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
  era: AreaLayerSlice | null;
  /** Reconstructed coastlines when the cursor is in deep time, else null. */
  paleo: AreaLayerSlice | null;
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
  source?: string;
  sourceId?: string;
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
  const eraRef = useRef<AreaLayerSlice | null>(era);
  const paleoRef = useRef<AreaLayerSlice | null>(paleo);
  const modeRef = useRef<MapMode>(mode);
  const eraSlots = useRef(new SlotPair("era", ERA_STYLE));
  const paleoSlots = useRef(new SlotPair("paleo", PALEO_STYLE));
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
    // Area-source attribution is attached to each lazy vector source. The
    // always-present basemap and entity data are named by the map control.
    map.addControl(
      new maplibregl.AttributionControl({
        compact: true,
        customAttribution: [
          "© OpenStreetMap contributors",
          "Wikidata CC0",
        ].join(" · "),
      }),
    );
    map.addControl(new maplibregl.NavigationControl({ showCompass: false }), "bottom-right");
    map.on("error", (event) => {
      const areaError = event as unknown as { sourceId?: string; error?: unknown };
      if (
        !areaError.sourceId?.startsWith("wk-era-") &&
        !areaError.sourceId?.startsWith("wk-paleo-")
      ) {
        return;
      }
      console.error(`PMTiles area source ${areaError.sourceId} failed:`, areaError.error);
    });
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
      map.on("click", (e: maplibregl.MapMouseEvent) => {
        // A dot on top of an extent is the more specific target, and its own
        // handler handles the selection.
        if (map.queryRenderedFeatures(e.point, { layers: ["wk-dots"] }).length) return;
        const slug = queryAreaFeature(map, eraSlots.current, e.point)?.slug;
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
        if (modeRef.current !== "political") return setTip(null);
        // A dot is the more specific target, as it is for clicks.
        if (map.queryRenderedFeatures(e.point, { layers: ["wk-dots"] }).length) {
          return setTip(null);
        }
        const feature = queryAreaFeature(map, eraSlots.current, e.point);
        setTip(feature?.name ? { x: e.point.x, y: e.point.y, ...feature, name: feature.name } : null);
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
      eraSlots.current.reset();
      paleoSlots.current.reset();
      map.remove();
      mapRef.current = null;
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
    eraRef.current = era;
    const map = mapRef.current;
    if (map && readyRef.current) eraSlots.current.apply(map, era);
    devHook("__wkera", era);
  }, [era]);

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
          <span>{tip.name}</span>
          {tip.source && (
            <span className="map-tooltip-source">
              {tip.source}{tip.sourceId ? ` · ${tip.sourceId}` : ""}
            </span>
          )}
        </div>
      )}
    </>
  );
}
