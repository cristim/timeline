// API-0: the manifest is the only mutable object; every artifact key is
// computed client-side from it.
export interface Bucket {
  id: string;
  window_s: number; // 0 = single window spanning all time
  windows?: number[]; // non-empty window indexes in the baked dataset
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

export const DATA_URL: string =
  import.meta.env.VITE_DATA_URL ?? "http://localhost:8080";

export async function loadManifest(): Promise<Manifest> {
  const res = await fetch(`${DATA_URL}/manifest.json`);
  if (!res.ok) {
    throw new Error(`manifest fetch failed: ${res.status}`);
  }
  return (await res.json()) as Manifest;
}
