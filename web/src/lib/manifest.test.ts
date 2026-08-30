import { afterEach, describe, expect, it, vi } from "vitest";
import { basemapArtifactURL, loadManifest, type BasemapDescriptor } from "./manifest";

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

afterEach(() => {
  vi.unstubAllEnvs();
  vi.unstubAllGlobals();
});

describe("manifest basemap descriptor", () => {
  it("accepts the production descriptor and derives static and gateway URLs", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(manifest))));
    await expect(loadManifest()).resolves.toMatchObject({ dataset: "atlas-v1.2", basemap });

    vi.stubEnv("BASE_URL", "/timeline/");
    expect(basemapArtifactURL(manifest.dataset, basemap)).toBe(
      "/timeline/v/atlas-v1.2/basemap/protomaps-20260829-z0-6.pmtiles",
    );
    vi.stubEnv("VITE_DATA_URL", "https://data.example.test");
    expect(basemapArtifactURL(manifest.dataset, basemap)).toBe(
      "https://data.example.test/v/atlas-v1.2/basemap/protomaps-20260829-z0-6.pmtiles",
    );
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
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(body))));
    await expect(loadManifest()).rejects.toThrow(field as string);
  });
});

describe("manifest window runs", () => {
  const withBuckets = (windows: unknown) => ({
    ...manifest,
    buckets: [{ id: "T10", window_s: 31_556_952, windows }],
  });

  it("accepts runs and a bucket with no windows at all", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify(withBuckets({ war: [[-31, -29]] })))),
    );
    await expect(loadManifest()).resolves.toMatchObject({
      buckets: [{ id: "T10", windows: { war: [[-31, -29]] } }],
    });

    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify(withBuckets(undefined)))),
    );
    await expect(loadManifest()).resolves.toMatchObject({ buckets: [{ id: "T10" }] });
  });

  it.each([
    // A manifest baked before runs existed: the shape a rollback or a stale
    // cached manifest hands a current bundle. It must fail the load, not throw
    // mid-render.
    ["bare window indexes", withBuckets({ war: [-31, -30, -29] })],
    ["a one-element run", withBuckets({ war: [[-31]] })],
    ["a three-element run", withBuckets({ war: [[-31, -29, 4]] })],
    ["a non-numeric bound", withBuckets({ war: [["-31", -29]] })],
    ["a run that ends before it starts", withBuckets({ war: [[-29, -31]] })],
    ["a non-array category", withBuckets({ war: 3 })],
    ["a non-object windows field", withBuckets([])],
  ])("rejects %s", async (_name, body) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(body))));
    await expect(loadManifest()).rejects.toThrow("manifest.buckets[0].windows");
  });

  it("rejects buckets that are not an array", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify({ ...manifest, buckets: {} }))),
    );
    await expect(loadManifest()).rejects.toThrow("manifest.buckets");
  });
});
