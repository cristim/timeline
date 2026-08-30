import type { StyleSpecification } from "maplibre-gl";

export const BASEMAP_SOURCE_ID = "wk-basemap";
export const BASEMAP_EARTH_SOURCE_LAYER = "earth";

export function createBasemapStyle(
  archiveURL: string,
  attribution: string,
): StyleSpecification {
  return {
    version: 8,
    projection: { type: "globe" },
    sources: {
      [BASEMAP_SOURCE_ID]: {
        type: "vector",
        url: `pmtiles://${archiveURL}`,
        attribution,
      },
    },
    layers: [
      {
        id: "wk-basemap-background",
        type: "background",
        paint: { "background-color": "#0b1722" },
      },
      {
        id: "wk-basemap-earth",
        type: "fill",
        source: BASEMAP_SOURCE_ID,
        "source-layer": BASEMAP_EARTH_SOURCE_LAYER,
        paint: { "fill-color": "#596453" },
      },
      {
        id: "wk-basemap-landcover",
        type: "fill",
        source: BASEMAP_SOURCE_ID,
        "source-layer": "landcover",
        paint: {
          "fill-color": [
            "match",
            ["get", "kind"],
            "forest",
            "#435d45",
            "grassland",
            "#607153",
            "farmland",
            "#746f52",
            "glacier",
            "#cad8d6",
            "#596453",
          ],
          "fill-opacity": 0.75,
        },
      },
      {
        id: "wk-basemap-water",
        type: "fill",
        source: BASEMAP_SOURCE_ID,
        "source-layer": "water",
        paint: { "fill-color": "#16384f" },
      },
      {
        id: "wk-basemap-highway",
        type: "line",
        source: BASEMAP_SOURCE_ID,
        "source-layer": "roads",
        filter: ["==", ["get", "kind"], "highway"],
        paint: {
          "line-color": "#b9a875",
          "line-width": ["interpolate", ["linear"], ["zoom"], 3, 0.25, 6, 1.2],
        },
      },
      {
        id: "wk-basemap-major-road",
        type: "line",
        source: BASEMAP_SOURCE_ID,
        "source-layer": "roads",
        filter: ["==", ["get", "kind"], "major_road"],
        paint: {
          "line-color": "#958b68",
          "line-width": ["interpolate", ["linear"], ["zoom"], 5, 0.2, 6, 0.8],
        },
      },
      {
        id: "wk-basemap-region-boundary",
        type: "line",
        source: BASEMAP_SOURCE_ID,
        "source-layer": "boundaries",
        filter: ["==", ["get", "kind"], "region"],
        paint: {
          "line-color": "#727b7e",
          "line-width": 0.6,
          "line-dasharray": [2, 2],
        },
      },
      {
        id: "wk-basemap-country-boundary",
        type: "line",
        source: BASEMAP_SOURCE_ID,
        "source-layer": "boundaries",
        filter: ["==", ["get", "kind"], "country"],
        paint: { "line-color": "#a4abad", "line-width": 1 },
      },
    ],
  };
}
