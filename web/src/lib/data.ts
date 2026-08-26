// Artifact reads (the whole "query engine", API-1): fetch, cache, union.
// Everything under /v/<dataset>/ is immutable, so caching is unconditional.
import { artifactURL, type Manifest } from "./manifest";

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
}

export interface EntityRef {
  slug: string;
  name: string;
  type: string;
  t0: number;
  t1: number;
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

function fetchArtifact<T>(dataset: string, relKey: string): Promise<T> {
  const url = artifactURL(`/v/${dataset}/${relKey}`);
  let p = cache.get(url);
  if (!p) {
    p = fetch(url).then((res) => {
      if (!res.ok) {
        // A 404 on a manifest-valid key is a bake bug (API-1) - surface it.
        throw new Error(`artifact ${relKey}: HTTP ${res.status}`);
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
  return fetchArtifact(m.dataset, `entity/${slug}.json`);
}

export function fetchSearchShard(m: Manifest, shard: string): Promise<{ entries: SearchEntry[] }> {
  return fetchArtifact(m.dataset, `search/${shard}.json`);
}
