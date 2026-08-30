import { useEffect, useMemo, useRef, useState } from "react";
import { bucketForSpan, chunkKey, coveringTimestep, secondsToYear, windowsInRange } from "./keyscheme";
import {
  areaLayerSlice,
  fetchChunk,
  fetchEntity,
  fetchLayerIndex,
  type AreaLayerSlice,
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
  /** Chunk fetch failure for the current window set (FE-9: explicit, not a
   * silently empty timeline). Cleared by the next successful load. */
  error: string | null;
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
  const [state, setState] = useState<ViewportData>({
    items: [],
    bucket: 0,
    loading: true,
    error: null,
  });
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
        setState({ items, bucket: bucketIdx, loading: false, error: null });
      })
      .catch((e: unknown) => {
        if (generation.current !== gen) return;
        console.error("viewport load failed:", e);
        setState((s) => ({ ...s, loading: false, error: String(e) }));
      });
  }, [manifest, keysStr, bucketIdx, minImportance]);

  return state;
}

/**
 * How long the cursor must settle before its slice is selected. The layers tile
 * the whole timeline, so a fast drag crosses dozens of windows;
 * without this, each one would start PMTiles range reads the next obsoletes.
 * Well under the crossfade, so a deliberate scrub still lands on every slice.
 */
const STEP_SETTLE_MS = 120;

/**
 * The descriptor of one time-sliced map layer for the cursor time (FE-3: moving
 * the timeline re-requests the time-dependent layers).
 *
 * The index is fetched once and answers "does any slice cover this date?";
 * only then does MapLibre receive an immutable PMTiles URL. Booting the
 * whole-universe view therefore costs one small fetch and correctly creates no
 * vector source for Roman Britain at 6 billion BCE.
 *
 * Used once per area layer: political borders through recorded history,
 * reconstructed coastlines before it. Their coverage windows are disjoint, so
 * at most one of them ever returns a descriptor.
 */
export interface TimeLayerState {
  /** The slice covering the cursor, or null when none does. */
  slice: AreaLayerSlice | null;
  /**
   * The years this layer speaks for at all, across every slice; null until the
   * index lands. What separates "no reconstruction exists this far back" from
   * "past the end of the atlas", which the map has to answer differently.
   */
  coverage: { from: number; to: number } | null;
}

export function useTimeLayer(
  manifest: Manifest | null,
  tc: number,
  layer: string,
): TimeLayerState {
  const [index, setIndex] = useState<LayerIndexDoc | null>(null);
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

  const slice = useMemo(
    () => (manifest && step ? areaLayerSlice(manifest, layer, step) : null),
    [manifest, layer, step],
  );

  const coverage = useMemo(() => {
    if (!index?.steps.length) return null;
    let from = index.steps[0].t_from;
    let to = index.steps[0].t_to;
    for (const s of index.steps) {
      from = Math.min(from, s.t_from);
      to = Math.max(to, s.t_to);
    }
    return { from, to };
  }, [index]);

  return useMemo(() => ({ slice, coverage }), [slice, coverage]);
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
