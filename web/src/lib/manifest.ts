// API-0: the manifest is the only mutable object; every artifact key is
// computed client-side from it.
export interface Bucket {
  id: string;
  window_s: number; // 0 = single window spanning all time
  // Non-empty inclusive window runs per category ("all" + each real category).
  // Only each run's start window exists as a chunk file (API-1).
  windows?: Record<string, WindowRun[]>;
}

export type WindowRun = [start: number, end: number];

export interface BasemapDescriptor {
  key: string;
  source: string;
  attribution: string;
  sha256: string;
}

export interface Manifest {
  dataset: string;
  generated_at: string;
  seed_version?: string;
  basemap: BasemapDescriptor;
  buckets: Bucket[];
  categories: string[];
  layers: string[];
  timesteps: Record<string, number[]>;
  counts: Record<string, number>;
  search_shards: string[];
  golden_views?: string;
}

/**
 * Artifact URL resolution: an explicit VITE_DATA_URL (dev gateway, CDN) wins;
 * otherwise artifacts are same-origin relative to the app's base path - the
 * static-hosting case (GitHub Pages serves under /<repo>/).
 */
export function artifactURL(path: string): string {
  const explicit = import.meta.env.VITE_DATA_URL;
  if (explicit) {
    return `${explicit}${path}`;
  }
  return `${import.meta.env.BASE_URL.replace(/\/$/, "")}${path}`;
}

export function basemapArtifactURL(dataset: string, basemap: BasemapDescriptor): string {
  return artifactURL(`/v/${dataset}/${basemap.key}`);
}

function manifestObject(value: unknown, field: string): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new Error(`${field} must be an object`);
  }
  return value as Record<string, unknown>;
}

function manifestString(value: unknown, field: string): string {
  if (typeof value !== "string") throw new Error(`${field} must be a string`);
  return value;
}

function manifestSafeInteger(value: unknown, field: string): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value)) {
    throw new Error(`${field} must be a safe integer`);
  }
  return value;
}

function manifestWindowRuns(value: unknown, field: string): Record<string, WindowRun[]> | undefined {
  if (value === undefined) return undefined;
  const raw = manifestObject(value, field);
  const out = Object.create(null) as Record<string, WindowRun[]>;
  for (const [category, rawRuns] of Object.entries(raw)) {
    const categoryField = `${field}.${category}`;
    if (!Array.isArray(rawRuns)) {
      throw new Error(`${categoryField} must be an array of [start,end] runs`);
    }
    const runs: WindowRun[] = [];
    let previousEnd: number | undefined;
    for (let i = 0; i < rawRuns.length; i++) {
      const rawRun = rawRuns[i];
      const runField = `${categoryField}[${i}]`;
      if (
        !Array.isArray(rawRun) ||
        rawRun.length !== 2 ||
        !Object.hasOwn(rawRun, 0) ||
        !Object.hasOwn(rawRun, 1)
      ) {
        throw new Error(`${runField} must be a [start,end] run`);
      }
      const start = manifestSafeInteger(rawRun[0], `${runField}[0]`);
      const end = manifestSafeInteger(rawRun[1], `${runField}[1]`);
      if (end < start) {
        throw new Error(`${runField} end must be greater than or equal to start`);
      }
      if (previousEnd !== undefined && start <= previousEnd) {
        throw new Error(`${runField} must be sorted and non-overlapping`);
      }
      runs.push([start, end]);
      previousEnd = end;
    }
    out[category] = runs;
  }
  return out;
}

function decodeManifest(value: unknown): Manifest {
  const raw = manifestObject(value, "manifest");
  const dataset = manifestString(raw.dataset, "manifest.dataset");
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(dataset)) {
    throw new Error("manifest.dataset must be a non-empty dataset token");
  }

  const rawBasemap = manifestObject(raw.basemap, "manifest.basemap");
  const key = manifestString(rawBasemap.key, "manifest.basemap.key");
  if (!/^basemap\/[A-Za-z0-9][A-Za-z0-9._-]*\.pmtiles$/.test(key)) {
    throw new Error("manifest.basemap.key must be one basemap/<filename>.pmtiles path");
  }
  const source = manifestString(rawBasemap.source, "manifest.basemap.source");
  let sourceURL: URL;
  try {
    sourceURL = new URL(source);
  } catch {
    throw new Error("manifest.basemap.source must be an absolute HTTPS URL");
  }
  if (sourceURL.protocol !== "https:") {
    throw new Error("manifest.basemap.source must be an absolute HTTPS URL");
  }
  const attribution = manifestString(rawBasemap.attribution, "manifest.basemap.attribution");
  if (!attribution.trim()) throw new Error("manifest.basemap.attribution must not be empty");
  const sha256 = manifestString(rawBasemap.sha256, "manifest.basemap.sha256");
  if (!/^[0-9a-f]{64}$/.test(sha256)) {
    throw new Error("manifest.basemap.sha256 must be 64 lowercase hexadecimal characters");
  }
  if (!Array.isArray(raw.buckets)) {
    throw new Error("manifest.buckets must be an array");
  }
  const buckets = raw.buckets.map((bucket, i) => {
    const rawBucket = manifestObject(bucket, `manifest.buckets[${i}]`);
    return {
      ...(rawBucket as unknown as Bucket),
      windows: manifestWindowRuns(rawBucket.windows, `manifest.buckets[${i}].windows`),
    };
  });

  return {
    ...(raw as unknown as Manifest),
    dataset,
    basemap: { key, source, attribution, sha256 },
    buckets,
  };
}

async function fetchManifest(url: string, init?: RequestInit): Promise<Manifest> {
  const res = await fetch(url, init);
  if (!res.ok) {
    throw new Error(`manifest fetch failed: ${res.status}`);
  }
  return decodeManifest(await res.json());
}

export function loadManifest(): Promise<Manifest> {
  const url = `${artifactURL("/manifest.json")}?current=${Date.now()}`;
  return fetchManifest(url, { cache: "no-store" });
}

/** Reload an open app only when a fresh manifest points at a newer dataset. */
export async function reloadIfDatasetChanged(currentDataset: string): Promise<boolean> {
  const latest = await loadManifest();
  if (latest.dataset === currentDataset) return false;
  window.location.reload();
  return true;
}
