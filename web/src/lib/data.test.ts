import { afterEach, describe, expect, it, vi } from "vitest";
import {
  ArtifactHTTPError,
  ArtifactSchemaError,
  areaLayerSlice,
  fetchEntity,
  fetchLayerIndex,
  validateEntityDoc,
  type EntityDoc,
} from "./data";
import type { Manifest } from "./manifest";

const manifest: Manifest = {
  dataset: "atlas-v1",
  generated_at: "2026-08-30T00:00:00Z",
  basemap: {
    key: "basemap/test.pmtiles",
    source: "https://example.test/test.pmtiles",
    attribution: "test attribution",
    sha256: "0000000000000000000000000000000000000000000000000000000000000000",
  },
  buckets: [],
  categories: [],
  layers: ["borders"],
  timesteps: { borders: [1500] },
  counts: {},
  search_shards: [],
  property_def: {
    mass: {
      label: "Mass",
      datatype: "number",
      canonical_unit: "kg",
      conversions: { kg: { scale: 1 } },
    },
  },
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

describe("entity artifact boundary", () => {
  it.each(["../manifest", "a/b", "a%2Fb", "", "Uppercase"])(
    "rejects invalid slug %j without fetching a path",
    async (slug) => {
      const fetchMock = vi.fn();
      vi.stubGlobal("fetch", fetchMock);

      await expect(fetchEntity(manifest, slug)).rejects.toThrow("invalid entity slug");
      expect(fetchMock).not.toHaveBeenCalled();
    },
  );

  it("preserves HTTP status for a valid missing entity", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(null, { status: 404 })));
    const error = await fetchEntity(manifest, "missing-subject").catch((caught: unknown) => caught);
    expect(error).toBeInstanceOf(ArtifactHTTPError);
    expect(error).toMatchObject({ status: 404, key: "entity/missing-subject.json" });
  });

  it("returns a structurally valid entity with the requested slug", () => {
    const entity = entityDoc("earth", "Earth");
    expect(validateEntityDoc(entity, "earth", "requested")).toBe(entity);
  });

  it.each([
    ["mismatched slug", (entity: EntityDoc) => { entity.slug = "mars"; }],
    ["missing temporal", (entity: EntityDoc) => { delete (entity as Partial<EntityDoc>).temporal; }],
    ["blank name", (entity: EntityDoc) => { entity.name = "  "; }],
    ["unknown type", (entity: EntityDoc) => { entity.type = "spaceship"; }],
    ["empty categories", (entity: EntityDoc) => { entity.categories = []; }],
    ["unknown category", (entity: EntityDoc) => { entity.categories = ["sports"]; }],
    ["padded category", (entity: EntityDoc) => { entity.categories = [" life"]; }],
    ["malformed categories", (entity: EntityDoc) => { entity.categories = null as unknown as string[]; }],
    ["invalid point", (entity: EntityDoc) => { entity.point = [181, 0]; }],
    ["non-finite importance", (entity: EntityDoc) => { entity.importance = Number.NaN; }],
    ["zero importance", (entity: EntityDoc) => { entity.importance = 0; }],
    ["importance above one", (entity: EntityDoc) => { entity.importance = 1.01; }],
  ])("rejects %s before callers consume it", (_name, mutate) => {
    const entity = entityDoc("earth", "Earth");
    mutate(entity);
    expect(() => validateEntityDoc(entity, "earth", "requested"))
      .toThrow(ArtifactSchemaError);
  });
});

function entityDoc(slug: string, name: string): EntityDoc {
  return {
    slug,
    type: "place",
    name,
    temporal: { t0: 0, t1: 1, precision: "year", status: "documented" },
    categories: ["earth"],
    importance: 1,
    links: {},
  };
}
