import { useEffect, useMemo, useRef, useState } from "react";
import { bucketForSpan, chunkKey, coveringTimestep, secondsToYear, windowsInRange } from "./keyscheme";
import {
  fetchBorderLayer,
  fetchChunk,
  fetchEntity,
  fetchLayerIndex,
  type BorderLayerDoc,
  type ChunkItem,
  type EntityDoc,
  type LayerIndexDoc,
} from "./data";
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
    const categories = cats.length ? cats : ["all"];
    const out: string[] = [];
    for (const c of categories) {
      for (const w of windowsInRange(bucket, c, t0 - pad, t1 + pad)) {
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

/**
 * How long the cursor must settle before its slice is fetched. The layers tile
 * the whole timeline in 89 slices, so a fast drag crosses dozens of windows;
 * without this, each one would start a download the next instantly obsoletes.
 * Well under the crossfade, so a deliberate scrub still lands on every slice.
 */
const STEP_SETTLE_MS = 120;

/**
 * The snapshot of one time-sliced map layer for the cursor time (FE-3: moving
 * the timeline re-requests the time-dependent layers).
 *
 * The index is fetched once and answers "does any slice cover this date?";
 * only then is a body downloaded. Booting the whole-universe view therefore
 * costs one small fetch and correctly shows nothing, instead of pulling down
 * Roman Britain to render at 6 billion BCE.
 *
 * Used once per area layer: political borders through recorded history,
 * reconstructed coastlines before it. Their coverage windows are disjoint, so
 * at most one of them ever returns a document.
 */
export function useTimeLayer(
  manifest: Manifest | null,
  tc: number,
  layer: string,
): BorderLayerDoc | null {
  const [index, setIndex] = useState<LayerIndexDoc | null>(null);
  const [doc, setDoc] = useState<BorderLayerDoc | null>(null);
  // The cursor the fetch follows, a beat behind the one the map follows.
  const [settledTc, setSettledTc] = useState(tc);

  useEffect(() => {
    if (!manifest || !manifest.layers.includes(layer)) {
      setIndex(null);
      return;
    }
    let live = true;
    fetchLayerIndex(manifest, layer).then(
      (d) => live && setIndex(d),
      (e: unknown) => {
        console.error("layer index load failed:", e);
        if (live) setIndex(null); // no index means no overlay, not a stale one
      },
    );
    return () => {
      live = false;
    };
  }, [manifest, layer]);

  useEffect(() => {
    const id = window.setTimeout(() => setSettledTc(tc), STEP_SETTLE_MS);
    return () => window.clearTimeout(id);
  }, [tc]);

  const step = useMemo(
    () => (index ? coveringTimestep(index.steps, secondsToYear(settledTc)) : null),
    [index, settledTc],
  );

  useEffect(() => {
    if (!manifest || !step) {
      setDoc(null);
      return;
    }
    // `live` is the latest-wins guard: React tears down the previous effect
    // before running this one, so a slower earlier response cannot land after
    // a faster later one.
    let live = true;
    fetchBorderLayer(manifest, layer, step.year).then(
      (d) => live && setDoc(d),
      (e: unknown) => {
        console.error("map layer load failed:", e);
        if (live) setDoc(null);
      },
    );
    return () => {
      live = false;
    };
  }, [manifest, layer, step]);

  return doc;
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
