import { afterEach, describe, expect, it, vi } from "vitest";
import {
  basemapArtifactURL,
  loadManifest,
  reloadIfDatasetChanged,
  type BasemapDescriptor,
} from "./manifest";

const basemap: BasemapDescriptor = {
  key: "basemap/protomaps-20260829-z0-6.pmtiles",
  source: "https://build.protomaps.com/20260829.pmtiles",
  attribution: '<a href="https://github.com/protomaps/basemaps">Protomaps</a>',
  sha256: "91578880b31e965f7e1c27c3efe1e2f53bb60e87b758349761a5f32cbb37b675",
};

const manifest = {
  dataset: "atlas-v1.2",
  generated_at: "2026-08-30T00:00:00Z",
  basemap,
  buckets: [],
  categories: [],
  layers: [],
  timesteps: {},
  counts: {},
  search_shards: [],
};

function stubManifest(body: unknown): void {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: true, json: async () => body }));
}

afterEach(() => {
  vi.unstubAllEnvs();
  vi.unstubAllGlobals();
});

describe("manifest basemap descriptor", () => {
  it("accepts the production descriptor and derives static and gateway URLs", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(manifest)));
    vi.stubGlobal("fetch", fetchMock);
    await expect(loadManifest()).resolves.toMatchObject({ dataset: "atlas-v1.2", basemap });
    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringMatching(/^\/manifest\.json\?current=\d+$/),
      { cache: "no-store" },
    );

    vi.stubEnv("BASE_URL", "/timeline/");
    expect(basemapArtifactURL(manifest.dataset, basemap)).toBe(
      "/timeline/v/atlas-v1.2/basemap/protomaps-20260829-z0-6.pmtiles",
    );
    vi.stubEnv("VITE_DATA_URL", "https://data.example.test");
    expect(basemapArtifactURL(manifest.dataset, basemap)).toBe(
      "https://data.example.test/v/atlas-v1.2/basemap/protomaps-20260829-z0-6.pmtiles",
    );
  });

  it("reloads a stale open app from a cache-bypassing manifest read", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ ...manifest, dataset: "atlas-v2" })),
    );
    const reload = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    vi.stubGlobal("window", { location: { reload } });

    await expect(reloadIfDatasetChanged("atlas-v1.2")).resolves.toBe(true);
    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringMatching(/^\/manifest\.json\?current=\d+$/),
      { cache: "no-store" },
    );
    expect(reload).toHaveBeenCalledOnce();
  });

  it("does not reload when a layer failed within the current dataset", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(manifest))));
    const reload = vi.fn();
    vi.stubGlobal("window", { location: { reload } });

    await expect(reloadIfDatasetChanged(manifest.dataset)).resolves.toBe(false);
    expect(reload).not.toHaveBeenCalled();
  });

  it.each([
    ["missing basemap", { ...manifest, basemap: undefined }, "manifest.basemap"],
    ["wrong basemap", { ...manifest, basemap: [] }, "manifest.basemap"],
    ["missing key", { ...manifest, basemap: { ...basemap, key: undefined } }, "manifest.basemap.key"],
    ["wrong key type", { ...manifest, basemap: { ...basemap, key: 7 } }, "manifest.basemap.key"],
    ["nested key", { ...manifest, basemap: { ...basemap, key: "basemap/nested/map.pmtiles" } }, "manifest.basemap.key"],
    ["traversal key", { ...manifest, basemap: { ...basemap, key: "basemap/../map.pmtiles" } }, "manifest.basemap.key"],
    ["query key", { ...manifest, basemap: { ...basemap, key: "basemap/map.pmtiles?x=1" } }, "manifest.basemap.key"],
    ["missing source", { ...manifest, basemap: { ...basemap, source: undefined } }, "manifest.basemap.source"],
    ["wrong source type", { ...manifest, basemap: { ...basemap, source: 7 } }, "manifest.basemap.source"],
    ["unsafe source", { ...manifest, basemap: { ...basemap, source: "http://example.test/map.pmtiles" } }, "manifest.basemap.source"],
    ["missing attribution", { ...manifest, basemap: { ...basemap, attribution: undefined } }, "manifest.basemap.attribution"],
    ["wrong attribution type", { ...manifest, basemap: { ...basemap, attribution: 7 } }, "manifest.basemap.attribution"],
    ["empty attribution", { ...manifest, basemap: { ...basemap, attribution: "   " } }, "manifest.basemap.attribution"],
    ["missing digest", { ...manifest, basemap: { ...basemap, sha256: undefined } }, "manifest.basemap.sha256"],
    ["wrong digest type", { ...manifest, basemap: { ...basemap, sha256: 7 } }, "manifest.basemap.sha256"],
    ["invalid digest", { ...manifest, basemap: { ...basemap, sha256: "ABC" } }, "manifest.basemap.sha256"],
    ["missing dataset", { ...manifest, dataset: undefined }, "manifest.dataset"],
    ["wrong dataset type", { ...manifest, dataset: 7 }, "manifest.dataset"],
    ["dot dataset", { ...manifest, dataset: "." }, "manifest.dataset"],
    ["dot-dot dataset", { ...manifest, dataset: ".." }, "manifest.dataset"],
    ["invalid dataset", { ...manifest, dataset: "../atlas" }, "manifest.dataset"],
  ])("rejects %s", async (_name, body, field) => {
    stubManifest(body);
    await expect(loadManifest()).rejects.toThrow(field as string);
  });
});

describe("manifest window runs", () => {
  it("accepts sorted inclusive runs and buckets with no windows", async () => {
    stubManifest({
      ...manifest,
      buckets: [
        { id: "T0", window_s: 0, windows: { all: [[0, 0]], war: [[0, 0]] } },
        { id: "T10", window_s: 31_556_952, windows: { all: [[-31, -29], [-28, -28], [7, 7]] } },
        { id: "T11", window_s: 2_629_746 },
      ],
    });

    await expect(loadManifest()).resolves.toMatchObject({
      buckets: [
        { windows: { all: [[0, 0]], war: [[0, 0]] } },
        { windows: { all: [[-31, -29], [-28, -28], [7, 7]] } },
        { windows: undefined },
      ],
    });
  });

  it.each([
    ["missing buckets", { ...manifest, buckets: undefined }, "manifest.buckets"],
    ["non-array buckets", { ...manifest, buckets: {} }, "manifest.buckets"],
    ["non-object bucket", { ...manifest, buckets: [null] }, "manifest.buckets[0]"],
    ["old bare-window list", { ...manifest, buckets: [{ id: "T10", window_s: 1, windows: { all: [1, 2] } }] }, "manifest.buckets[0].windows.all[0]"],
    ["non-object windows", { ...manifest, buckets: [{ id: "T10", window_s: 1, windows: [] }] }, "manifest.buckets[0].windows"],
    ["non-array category", { ...manifest, buckets: [{ id: "T10", window_s: 1, windows: { all: {} } }] }, "manifest.buckets[0].windows.all"],
    ["one-bound run", { ...manifest, buckets: [{ id: "T10", window_s: 1, windows: { all: [[1]] } }] }, "manifest.buckets[0].windows.all[0]"],
    ["three-bound run", { ...manifest, buckets: [{ id: "T10", window_s: 1, windows: { all: [[1, 2, 3]] } }] }, "manifest.buckets[0].windows.all[0]"],
    ["string bound", { ...manifest, buckets: [{ id: "T10", window_s: 1, windows: { all: [["1", 1]] } }] }, "manifest.buckets[0].windows.all[0][0]"],
    ["null bound", { ...manifest, buckets: [{ id: "T10", window_s: 1, windows: { all: [[null, 1]] } }] }, "manifest.buckets[0].windows.all[0][0]"],
    ["decimal bound", { ...manifest, buckets: [{ id: "T10", window_s: 1, windows: { all: [[1.5, 2]] } }] }, "manifest.buckets[0].windows.all[0][0]"],
    ["unsafe bound", { ...manifest, buckets: [{ id: "T10", window_s: 1, windows: { all: [[Number.MAX_SAFE_INTEGER + 1, Number.MAX_SAFE_INTEGER + 1]] } }] }, "manifest.buckets[0].windows.all[0][0]"],
    ["inverted run", { ...manifest, buckets: [{ id: "T10", window_s: 1, windows: { all: [[2, 1]] } }] }, "manifest.buckets[0].windows.all[0]"],
    ["overlapping run", { ...manifest, buckets: [{ id: "T10", window_s: 1, windows: { all: [[1, 3], [3, 4]] } }] }, "manifest.buckets[0].windows.all[1]"],
    ["unsorted run", { ...manifest, buckets: [{ id: "T10", window_s: 1, windows: { all: [[4, 4], [2, 2]] } }] }, "manifest.buckets[0].windows.all[1]"],
  ])("rejects %s", async (_name, body, field) => {
    stubManifest(body);
    await expect(loadManifest()).rejects.toThrow(field as string);
  });

  it("rejects sparse run arrays before they become undefined bounds", async () => {
    const sparse = [1, 2] as unknown[];
    delete sparse[0];
    stubManifest({ ...manifest, buckets: [{ id: "T10", window_s: 1, windows: { all: [sparse] } }] });

    await expect(loadManifest()).rejects.toThrow("manifest.buckets[0].windows.all[0]");
  });
});
