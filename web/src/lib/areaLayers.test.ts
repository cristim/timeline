import { describe, expect, it } from "vitest";
import type { Map as MapLibreMap, MapSourceDataEvent, PointLike } from "maplibre-gl";
import type { AreaLayerSlice } from "./data";
import { queryAreaFeature, SlotPair, type AreaLayerStyle } from "./areaLayers";
import { polityColor } from "./colors";

const style: AreaLayerStyle = {
  fill: ["get", "color"],
  line: "#101726",
  fillOpacity: 1,
  lineOpacity: 0.85,
  lineWidth: 1.2,
  dashed: true,
  attribution: "historical-basemaps",
};

const slice = (year: number): AreaLayerSlice => ({
  layer: "borders",
  year,
  t_from: year,
  t_to: year,
  label: `world borders · ${year}`,
  source: "historical-basemaps",
  url: `pmtiles:///v/atlas/layers/borders/${year}.pmtiles`,
});

class FakeVectorSource {
  readonly urls: string[];

  constructor(url: string) {
    this.urls = [url];
  }

  setUrl(url: string) {
    this.urls.push(url);
  }
}

class FakeMap {
  readonly sources = new Map<string, FakeVectorSource>();
  readonly sourceSpecs = new Map<string, { type?: string; url?: string; attribution?: string }>();
  readonly layers = new Map<string, unknown>();
  readonly insertions: { id: string; before?: string }[] = [];
  readonly paints: { layer: string; property: string; value: unknown }[] = [];
  queryResult: { properties?: Record<string, unknown> }[] = [];
  private readonly sourceHandlers = new Set<(event: MapSourceDataEvent) => void>();

  addSource(id: string, source: { type?: string; url?: string; attribution?: string }) {
    this.sourceSpecs.set(id, source);
    this.sources.set(id, new FakeVectorSource(source.url ?? ""));
  }

  getSource(id: string) {
    return this.sources.get(id);
  }

  addLayer(layer: { id: string }, before?: string) {
    this.layers.set(layer.id, layer);
    this.insertions.push({ id: layer.id, before });
  }

  getLayer(id: string) {
    return this.layers.get(id);
  }

  setPaintProperty(layer: string, property: string, value: unknown) {
    this.paints.push({ layer, property, value });
  }

  on(type: string, listener: (event: MapSourceDataEvent) => void) {
    if (type === "sourcedata") this.sourceHandlers.add(listener);
  }

  off(type: string, listener: (event: MapSourceDataEvent) => void) {
    if (type === "sourcedata") this.sourceHandlers.delete(listener);
  }

  emitLoaded(sourceId: string) {
    for (const handler of [...this.sourceHandlers]) {
      handler({ sourceId, isSourceLoaded: true } as MapSourceDataEvent);
    }
  }

  queryRenderedFeatures() {
    return this.queryResult;
  }
}

const asMap = (map: FakeMap) => map as unknown as MapLibreMap;

describe("SlotPair", () => {
  it("creates vector slots lazily, inserts below the front, and reuses them with setUrl", () => {
    const map = new FakeMap();
    const pair = new SlotPair("era", style);

    pair.apply(asMap(map), slice(1900));
    expect([...map.sources]).toEqual([
      ["wk-era-a", expect.objectContaining({ urls: [slice(1900).url] })],
    ]);
    expect(map.sourceSpecs.get("wk-era-a")).toEqual({
      type: "vector",
      url: slice(1900).url,
      attribution: "historical-basemaps",
    });
    expect(map.insertions).toEqual([
      { id: "wk-era-a-fill", before: "wk-front-line" },
      { id: "wk-era-a-line", before: "wk-front-line" },
    ]);
    expect(map.layers.get("wk-era-a-fill")).toMatchObject({
      source: "wk-era-a",
      "source-layer": "areas",
      layout: {
        "fill-sort-key": ["coalesce", ["to-number", ["get", "render_rank"]], 0],
      },
    });
    expect(map.layers.get("wk-era-a-line")).toMatchObject({
      source: "wk-era-a",
      "source-layer": "areas",
      layout: {
        "line-sort-key": ["coalesce", ["to-number", ["get", "render_rank"]], 0],
      },
    });
    expect(map.paints).toEqual([]);

    map.emitLoaded("wk-era-a");
    expect(pair.liveFillLayerId()).toBe("wk-era-a-fill");
    expect(map.paints).toContainEqual({ layer: "wk-era-a-fill", property: "fill-opacity", value: 1 });

    pair.apply(asMap(map), slice(1914));
    expect(map.sources.get("wk-era-b")?.urls).toEqual([slice(1914).url]);
    map.emitLoaded("wk-era-b");
    expect(pair.liveFillLayerId()).toBe("wk-era-b-fill");

    pair.apply(asMap(map), slice(1920));
    expect(map.sources).toHaveLength(2);
    expect(map.sources.get("wk-era-a")?.urls).toEqual([slice(1900).url, slice(1920).url]);
  });

  it("waits for source data and lets the latest pending slice win", () => {
    const map = new FakeMap();
    const pair = new SlotPair("era", style);
    pair.apply(asMap(map), slice(1900));
    map.emitLoaded("wk-era-a");

    pair.apply(asMap(map), slice(1914));
    pair.apply(asMap(map), slice(1920));
    expect(map.sources.get("wk-era-b")?.urls).toEqual([slice(1914).url, slice(1920).url]);
    expect(pair.liveFillLayerId()).toBe("wk-era-a-fill");

    map.emitLoaded("wk-era-b");
    expect(pair.liveFillLayerId()).toBe("wk-era-b-fill");
    expect(map.paints.at(-2)).toEqual({
      layer: "wk-era-a-fill",
      property: "fill-opacity",
      value: 0,
    });
  });

  it("does not create an invalid source for an uncovered time", () => {
    const map = new FakeMap();
    const pair = new SlotPair("era", style);

    pair.apply(asMap(map), null);

    expect(map.sources).toHaveLength(0);
    expect(pair.liveFillLayerId()).toBeNull();
  });
});

describe("queryAreaFeature", () => {
  it("reads hover and selection properties from the live vector fill", () => {
    const map = new FakeMap();
    const pair = new SlotPair("era", style);
    pair.apply(asMap(map), slice(1500));
    map.emitLoaded("wk-era-a");
    map.queryResult = [{ properties: { name: "Ottoman Empire", slug: "ottoman-empire" } }];

    expect(queryAreaFeature(asMap(map), pair, { x: 10, y: 20 } as PointLike)).toEqual({
      name: "Ottoman Empire",
      slug: "ottoman-empire",
    });
  });

  it("reads OHM source provenance from the live vector fill", () => {
    const map = new FakeMap();
    const pair = new SlotPair("era", style);
    pair.apply(asMap(map), slice(1965));
    map.emitLoaded("wk-era-a");
    map.queryResult = [{
      properties: {
        name: "London Borough of Westminster",
        source: "OpenHistoricalMap",
        source_id: "relation/2693967@9",
      },
    }];

    expect(queryAreaFeature(asMap(map), pair, { x: 10, y: 20 } as PointLike)).toEqual({
      name: "London Borough of Westminster",
      source: "OpenHistoricalMap",
      sourceId: "relation/2693967@9",
    });
  });
});

describe("political tile colors", () => {
  it("matches the baker's FNV-1a over UTF-16 vectors", () => {
    expect(polityColor("Axis")).toBe("hsl(84, 34%, 45%)");
    expect(polityColor("Québec")).toBe("hsl(232, 34%, 45%)");
    expect(polityColor("𐍈 Empire")).toBe("hsl(283, 34%, 45%)");
  });
});
