// API-0: the manifest is the only mutable object; every artifact key is
// computed client-side from it.
/**
 * A maximal range of consecutive window indexes, [start, end] inclusive, whose
 * chunk bodies are byte-identical. One artifact exists per run, at `start`, and
 * fetching it from any window inside the range yields exactly the chunk that
 * window holds. Mirrors model.WindowRun in internal/model/buckets.go.
 */
export type WindowRun = [start: number, end: number];

export interface Bucket {
  id: string;
  window_s: number; // 0 = single window spanning all time
  // Baked window runs per category ("all" + each real category). Only windows
  // covered by a listed run exist as chunk files (API-1).
  windows?: Record<string, WindowRun[]>;
}

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

/**
 * Window runs are the one part of the manifest whose shape changed after the
 * client shipped, and the manifest is the only mutable object (API-0): a
 * browser can hold a fresh bundle against a manifest up to its 60s max-age old,
 * and an ARCH-2 rollback can repoint at a dataset baked before the change.
 * Reading a bare window index as a run throws deep inside rendering, so the
 * mismatch is caught here, where the load already has an error path.
 */
function manifestWindows(value: unknown, field: string): Record<string, WindowRun[]> | undefined {
  if (value === undefined) return undefined;
  const raw = manifestObject(value, field);
  const out: Record<string, WindowRun[]> = {};
  for (const [category, runs] of Object.entries(raw)) {
    if (!Array.isArray(runs)) {
      throw new Error(`${field}.${category} must be an array of [start,end] runs`);
    }
    out[category] = runs.map((run) => {
      if (
        !Array.isArray(run) ||
        run.length !== 2 ||
        typeof run[0] !== "number" ||
        typeof run[1] !== "number"
      ) {
        throw new Error(
          `${field}.${category} must hold [start,end] window runs, got ${JSON.stringify(run)}`,
        );
      }
      if (run[1] < run[0]) {
        throw new Error(`${field}.${category} run ${JSON.stringify(run)} ends before it starts`);
      }
      return [run[0], run[1]] as WindowRun;
    });
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
  const buckets = raw.buckets.map((value, i) => {
    const bucket = manifestObject(value, `manifest.buckets[${i}]`);
    return {
      ...(bucket as unknown as Bucket),
      windows: manifestWindows(bucket.windows, `manifest.buckets[${i}].windows`),
    };
  });

  return {
    ...(raw as unknown as Manifest),
    dataset,
    basemap: { key, source, attribution, sha256 },
    buckets,
  };
}

export async function loadManifest(): Promise<Manifest> {
  const res = await fetch(artifactURL("/manifest.json"));
  if (!res.ok) {
    throw new Error(`manifest fetch failed: ${res.status}`);
  }
  return decodeManifest(await res.json());
}
