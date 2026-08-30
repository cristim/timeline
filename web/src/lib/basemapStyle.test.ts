import { describe, expect, it } from "vitest";
import {
  BASEMAP_EARTH_SOURCE_LAYER,
  BASEMAP_SOURCE_ID,
  createBasemapStyle,
} from "./basemapStyle";

describe("createBasemapStyle", () => {
  it("builds one label-free local globe style in stable draw order", () => {
    const url = "/timeline/v/dev/basemap/world.pmtiles";
    const attribution = '<a href="https://example.test">complete credit</a>';
    const style = createBasemapStyle(url, attribution);

    expect(style.version).toBe(8);
    expect(style.projection).toEqual({ type: "globe" });
    expect(style.sources).toEqual({
      [BASEMAP_SOURCE_ID]: {
        type: "vector",
        url: `pmtiles://${url}`,
        attribution,
      },
    });
    expect(style.layers.map((layer) => layer.id)).toEqual([
      "wk-basemap-background",
      "wk-basemap-earth",
      "wk-basemap-landcover",
      "wk-basemap-water",
      "wk-basemap-highway",
      "wk-basemap-major-road",
      "wk-basemap-region-boundary",
      "wk-basemap-country-boundary",
    ]);

    const vectorLayers = style.layers.filter((layer) => layer.type !== "background");
    expect(
      vectorLayers.every((layer) => "source" in layer && layer.source === BASEMAP_SOURCE_ID),
    ).toBe(true);
    expect(
      vectorLayers.map((layer) => ("source-layer" in layer ? layer["source-layer"] : undefined)),
    ).toEqual([
      BASEMAP_EARTH_SOURCE_LAYER,
      "landcover",
      "water",
      "roads",
      "roads",
      "boundaries",
      "boundaries",
    ]);
    expect(style.layers.some((layer) => layer.type === "symbol")).toBe(false);
    expect(style).not.toHaveProperty("glyphs");
    expect(style).not.toHaveProperty("sprite");
    expect(Object.values(style.sources).some((source) => source.type === "raster")).toBe(false);
    expect(JSON.stringify(style).match(/https?:\/\//g)).toHaveLength(1);
  });
});
