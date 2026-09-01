// Artifact reads (the whole "query engine", API-1): fetch, cache, union.
// Everything under /v/<dataset>/ is immutable, so caching is unconditional.
import { layerIndexKey, layerKey } from "./keyscheme";
import { artifactURL, type Manifest } from "./manifest";
import { isValidEntitySlug } from "./slug";

export interface ChunkItem {
  slug: string;
  type: string;
  name: string;
  t0: number;
  t1: number;
  precision: string;
  status: string;
  point?: [number, number];
  categories: string[];
  importance: number;
  media_thumb?: string;
  child_count?: number;
}

export interface PropertyClaim {
  property?: string;
  value?: number;
  min?: number;
  max?: number;
  unit?: string;
  value_type: string;
  method?: string;
  source: string;
  published_at: string;
  confidence?: number;
}

export interface EntityDoc {
  slug: string;
  type: string;
  name: string;
  description?: string;
  temporal: { t0: number; t1: number; precision: string; status: string };
  categories: string[];
  importance: number;
  point?: [number, number];
  properties?: {
    property: string;
    synthesis: { min: number; max: number; unit?: string; claim_count: number };
    claims: PropertyClaim[];
  }[];
  relationships?: { type: string; target: EntityRef }[];
  contemporaries?: EntityRef[];
  children?: EntityRef[];
  links: { wikipedia?: string; wikidata?: string };
  media_thumb?: string;
  /** Time-sliced geometry (DM-7); for a war, its dated front positions. */
  geometry?: GeometryRecord[];
}

export interface EntityRef {
  slug: string;
  name: string;
  type: string;
  t0: number;
  t1: number;
}

/** One DM-7 geometry record on an entity document: a shape valid from a date. */
export interface GeometryRecord {
  valid_from: number;
  label?: string;
  representation: string;
  source: string;
  geometry: { type: "LineString"; coordinates: [number, number][] };
}

/** layers/<layer>/index.json: every time-step with its coverage window. */
export interface LayerIndexDoc {
  layer: string;
  steps: { year: number; t_from: number; t_to: number; label: string; source: string }[];
}

/** One immutable PMTiles archive selected from a layer's coverage index. */
export interface AreaLayerSlice {
  layer: string;
  year: number;
  t_from: number;
  t_to: number;
  label: string;
  source: string;
  url: string;
}

export interface SearchEntry {
  slug: string;
  name: string;
  type: string;
  t0: number;
  t1: number;
  importance: number;
  media_thumb?: string;
}

const cache = new Map<string, Promise<unknown>>();

export class ArtifactHTTPError extends Error {
  constructor(public readonly key: string, public readonly status: number) {
    super(`artifact ${key}: HTTP ${status}`);
    this.name = "ArtifactHTTPError";
  }
}

export class ArtifactSchemaError extends Error {
  constructor(message: string) {
    super(`artifact schema: ${message}`);
    this.name = "ArtifactSchemaError";
  }
}

const TEMPORAL_PRECISIONS = new Set([
  "billion_year", "million_year", "millennium", "century", "decade", "year",
  "month", "day", "hour", "minute", "second",
]);
const TEMPORAL_STATUSES = new Set([
  "observed", "documented", "estimated", "projected", "model_dependent",
  "speculative", "legendary", "disputed",
]);
const ENTITY_TYPES = new Set([
  "event", "person", "species", "object", "place", "organization", "state",
  "civilization", "idea", "theory", "discovery", "invention", "patent",
  "paper", "book", "artwork", "film", "game", "product", "technology",
  "standard", "law", "religion", "language", "disease", "chemical",
  "material", "structure", "astronomical_object", "natural_event",
  "future_event", "record", "time_series",
]);
const CATEGORIES = new Set([
  "universe", "earth", "life", "war", "politics", "science", "technology",
  "culture", "exploration", "economy", "religion", "disaster", "future",
]);

export function validateEntityDoc(value: unknown, expectedSlug: string, label: string): EntityDoc {
  const doc = artifactRecord(value, `${label} entity`);
  if (doc.slug !== expectedSlug) {
    throw new ArtifactSchemaError(`${label} entity slug does not match ${JSON.stringify(expectedSlug)}`);
  }
  const name = requiredString(doc.name, `${label} entity name`);
  if (name.trim().length === 0) {
    throw new ArtifactSchemaError(`${label} entity name must not be blank`);
  }
  const type = requiredString(doc.type, `${label} entity type`);
  if (!ENTITY_TYPES.has(type)) {
    throw new ArtifactSchemaError(`${label} entity has invalid type`);
  }
  const temporal = artifactRecord(doc.temporal, `${label} entity temporal`);
  const precision = requiredString(temporal.precision, `${label} entity temporal precision`);
  if (!TEMPORAL_PRECISIONS.has(precision)) {
    throw new ArtifactSchemaError(`${label} entity has invalid temporal precision`);
  }
  const status = requiredString(temporal.status, `${label} entity temporal status`);
  if (!TEMPORAL_STATUSES.has(status)) {
    throw new ArtifactSchemaError(`${label} entity has invalid temporal status`);
  }
  const t0 = finiteNumber(temporal.t0, `${label} entity temporal.t0`);
  const t1 = finiteNumber(temporal.t1, `${label} entity temporal.t1`);
  if (t1 < t0) {
    throw new ArtifactSchemaError(`${label} entity temporal is inverted`);
  }
  if (
    !Array.isArray(doc.categories) ||
    doc.categories.length === 0 ||
    !doc.categories.every((item) => typeof item === "string" && CATEGORIES.has(item))
  ) {
    throw new ArtifactSchemaError(`${label} entity categories must contain known category tokens`);
  }
  const importance = finiteNumber(doc.importance, `${label} entity importance`);
  if (importance <= 0 || importance > 1) {
    throw new ArtifactSchemaError(`${label} entity importance must be within (0,1]`);
  }
  if (doc.point !== undefined) {
    if (!Array.isArray(doc.point) || doc.point.length !== 2) {
      throw new ArtifactSchemaError(`${label} entity point must contain two coordinates`);
    }
    const longitude = finiteNumber(doc.point[0], `${label} entity point longitude`);
    const latitude = finiteNumber(doc.point[1], `${label} entity point latitude`);
    if (longitude < -180 || longitude > 180 || latitude < -90 || latitude > 90) {
      throw new ArtifactSchemaError(`${label} entity point is outside longitude/latitude bounds`);
    }
  }
  if (doc.children !== undefined && !Array.isArray(doc.children)) {
    throw new ArtifactSchemaError(`${label} entity children must be an array`);
  }
  return value as EntityDoc;
}

function finiteNumber(value: unknown, field: string): number {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    throw new ArtifactSchemaError(`${field} must be a finite number`);
  }
  return value;
}

function artifactRecord(value: unknown, field: string): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new ArtifactSchemaError(`${field} must be an object`);
  }
  return value as Record<string, unknown>;
}

function requiredString(value: unknown, field: string): string {
  if (typeof value !== "string" || value.length === 0) {
    throw new ArtifactSchemaError(`${field} must be a non-empty string`);
  }
  return value;
}

function fetchArtifact<T>(dataset: string, relKey: string): Promise<T> {
  const url = artifactURL(`/v/${dataset}/${relKey}`);
  let p = cache.get(url);
  if (!p) {
    p = fetch(url).then((res) => {
      if (!res.ok) {
        // Preserve status so callers can distinguish a missing optional entity
        // from a required artifact failure without changing cache behavior.
        throw new ArtifactHTTPError(relKey, res.status);
      }
      return res.json();
    });
    p.catch(() => cache.delete(url)); // don't cache failures
    cache.set(url, p);
  }
  return p as Promise<T>;
}

export function fetchChunk(m: Manifest, relKey: string): Promise<{ items: ChunkItem[] }> {
  return fetchArtifact(m.dataset, relKey);
}

export function fetchEntity(m: Manifest, slug: string): Promise<EntityDoc> {
  if (!isValidEntitySlug(slug)) return Promise.reject(new Error(`invalid entity slug: ${slug}`));
  return fetchArtifact(m.dataset, `entity/${slug}.json`);
}

export function fetchSearchShard(m: Manifest, shard: string): Promise<{ entries: SearchEntry[] }> {
  return fetchArtifact(m.dataset, `search/${shard}.json`);
}

export function fetchLayerIndex(m: Manifest, layer: string): Promise<LayerIndexDoc> {
  return fetchArtifact(m.dataset, layerIndexKey(layer));
}

export function areaLayerSlice(
  m: Manifest,
  layer: string,
  step: LayerIndexDoc["steps"][number],
): AreaLayerSlice {
  return {
    layer,
    ...step,
    url: `pmtiles://${artifactURL(`/v/${m.dataset}/${layerKey(layer, step.year)}`)}`,
  };
}
