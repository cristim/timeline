import { afterEach, describe, expect, it, vi } from "vitest";
import { areaLayerSlice, fetchLayerIndex } from "./data";
import type { Manifest } from "./manifest";

const manifest: Manifest = {
  dataset: "atlas-v1",
  generated_at: "2026-08-30T00:00:00Z",
  buckets: [],
  categories: [],
  layers: ["borders"],
  timesteps: { borders: [1500] },
  counts: {},
  search_shards: [],
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("area-layer data", () => {
  it("fetches only the layer index and builds an immutable PMTiles descriptor", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          layer: "borders",
          steps: [
            {
              year: 1500,
              t_from: 1450,
              t_to: 1549,
              label: "world borders · 1500",
              source: "historical-basemaps",
            },
          ],
        }),
        { status: 200 },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const index = await fetchLayerIndex(manifest, "borders");
    const slice = areaLayerSlice(manifest, "borders", index.steps[0]);

    expect(slice).toEqual({
      layer: "borders",
      year: 1500,
      t_from: 1450,
      t_to: 1549,
      label: "world borders · 1500",
      source: "historical-basemaps",
      url: "pmtiles:///v/atlas-v1/layers/borders/1500.pmtiles",
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledWith("/v/atlas-v1/layers/borders/index.json");
  });
});
