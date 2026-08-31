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
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(body))));
    await expect(loadManifest()).rejects.toThrow(field as string);
  });
});
