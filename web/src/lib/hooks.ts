import { useEffect, useMemo, useRef, useState } from "react";
import { bucketForSpan, chunkKey, windowsInRange } from "./keyscheme";
import { fetchChunk, fetchEntity, type ChunkItem, type EntityDoc } from "./data";
import { loadManifest, type Manifest } from "./manifest";

export function useManifest(): { manifest: Manifest | null; error: string | null } {
  const [manifest, setManifest] = useState<Manifest | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    loadManifest().then(setManifest, (e: unknown) => setError(String(e)));
  }, []);
  return { manifest, error };
}

export interface ViewportData {
  items: ChunkItem[];
  bucket: number; // bucket index in manifest.buckets
  loading: boolean;
}

/**
 * The client half of API-1: snap the visible range to a bucket + window set,
 * fetch each selected category's chunks (or "all"), union, dedupe by slug,
 * apply the importance slider. Adjacent windows are prefetched so panning
 * rarely shows an empty lane (FE-2).
 */
export function useViewportItems(
  manifest: Manifest | null,
  t0: number,
  t1: number,
  cats: string[],
  minImportance: number,
): ViewportData {
  const [state, setState] = useState<ViewportData>({ items: [], bucket: 0, loading: true });
  const generation = useRef(0);

  const bucketIdx = useMemo(
    () => (manifest ? bucketForSpan(manifest.buckets, t1 - t0) : 0),
    [manifest, t0, t1],
  );

  // Joined-string identity: continuous pan/zoom only refetches when the
  // window set actually changes, not on every pixel of movement.
  const keysStr = useMemo(() => {
    if (!manifest) return "";
    const bucket = manifest.buckets[bucketIdx];
    const pad = bucket.window_s || 0;
    const windows = windowsInRange(bucket, t0 - pad, t1 + pad);
    const categories = cats.length ? cats : ["all"];
    const out: string[] = [];
    for (const w of windows) {
      for (const c of categories) {
        out.push(chunkKey(bucket, w, c));
      }
    }
    return out.join("|");
  }, [manifest, bucketIdx, t0, t1, cats]);

  useEffect(() => {
    if (!manifest) return;
    const keys = keysStr ? keysStr.split("|") : [];
    const gen = ++generation.current;
    setState((s) => ({ ...s, loading: true }));
    Promise.all(keys.map((k) => fetchChunk(manifest, k)))
      .then((chunks) => {
        if (generation.current !== gen) return;
        const bySlug = new Map<string, ChunkItem>();
        for (const chunk of chunks) {
          for (const item of chunk.items) {
            if (!bySlug.has(item.slug)) bySlug.set(item.slug, item);
          }
        }
        const items = [...bySlug.values()]
          .filter((i) => i.importance >= minImportance)
          .sort((a, b) => b.importance - a.importance || (a.slug < b.slug ? -1 : 1));
        setState({ items, bucket: bucketIdx, loading: false });
      })
      .catch((e: unknown) => {
        if (generation.current !== gen) return;
        console.error("viewport load failed:", e);
        setState((s) => ({ ...s, loading: false }));
      });
  }, [manifest, keysStr, bucketIdx, minImportance]);

  return state;
}

export function useEntity(manifest: Manifest | null, slug: string | null): EntityDoc | null {
  const [doc, setDoc] = useState<EntityDoc | null>(null);
  useEffect(() => {
    if (!manifest || !slug) {
      setDoc(null);
      return;
    }
    let live = true;
    fetchEntity(manifest, slug).then(
      (d) => live && setDoc(d),
      (e: unknown) => {
        console.error("entity load failed:", e);
        if (live) setDoc(null);
      },
    );
    return () => {
      live = false;
    };
  }, [manifest, slug]);
  return doc;
}
