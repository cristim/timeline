// API-0: the manifest is the only mutable object; every artifact key is
// computed client-side from it.
export interface Bucket {
  id: string;
  window_s: number; // 0 = single window spanning all time
  // Non-empty window indexes per category ("all" + each real category).
  // Only listed (window, category) pairs exist as chunk files (API-1).
  windows?: Record<string, number[]>;
}

export interface Manifest {
  dataset: string;
  generated_at: string;
  seed_version?: string;
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

export async function loadManifest(): Promise<Manifest> {
  const res = await fetch(artifactURL("/manifest.json"));
  if (!res.ok) {
    throw new Error(`manifest fetch failed: ${res.status}`);
  }
  return (await res.json()) as Manifest;
}
