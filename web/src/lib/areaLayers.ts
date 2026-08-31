import type * as maplibregl from "maplibre-gl";
import type { AreaLayerSlice } from "./data";

const SLOTS = ["a", "b"] as const;
const SOURCE_LAYER = "areas";
const INSERT_BEFORE = "wk-front-line";
const FADE_MS = 450;

type Slot = (typeof SLOTS)[number];

export interface AreaLayerStyle {
  fill: NonNullable<maplibregl.AllPaintProperties["fill-color"]>;
  line: string;
  fillOpacity: number;
  lineOpacity: number;
  lineWidth: number;
  dashed: boolean;
  attribution: string;
}

interface PendingLoad {
  slot: Slot;
  slice: AreaLayerSlice;
  cancel: () => void;
}

/** Cancelled fetches (navigation teardown, a superseded slice) are lifecycle
 * events, not failures - they must neither log as errors nor blank the map. */
export function isAbort(error: unknown): boolean {
  return (error as { name?: string } | undefined)?.name === "AbortError";
}

export interface AreaFeatureProperties {
  name?: string;
  slug?: string;
  source?: string;
  sourceId?: string;
}

export class SlotPair {
  private visible: Slot | null = null;
  private requestedUrl: string | null = null;
  private pending: PendingLoad | null = null;
  private readonly created = new Set<Slot>();
  private visibleSlice: AreaLayerSlice | null = null;
  private lastSlice: AreaLayerSlice | null = null;
  private errorsOwned = false;
  private retriedUrl: string | null = null;
  private retryTimer: number | null = null;

  constructor(
    private readonly kind: string,
    private readonly style: AreaLayerStyle,
    private readonly onLoadFailure?: () => void,
  ) {}

  private sourceId(slot: Slot): string {
    return `wk-${this.kind}-${slot}`;
  }

  private fillLayerId(slot: Slot): string {
    return `${this.sourceId(slot)}-fill`;
  }

  private lineLayerId(slot: Slot): string {
    return `${this.sourceId(slot)}-line`;
  }

  private ensureSlot(map: maplibregl.Map, slot: Slot, url: string): void {
    const source = this.sourceId(slot);
    if (this.created.has(slot)) {
      (map.getSource(source) as maplibregl.VectorTileSource).setUrl(url);
      return;
    }

    map.addSource(source, {
      type: "vector",
      url,
      attribution: this.style.attribution,
    });
    map.addLayer(
      {
        id: this.fillLayerId(slot),
        type: "fill",
        source,
        "source-layer": SOURCE_LAYER,
        layout: {
          "fill-sort-key": ["coalesce", ["to-number", ["get", "render_rank"]], 0],
        },
        paint: {
          "fill-color": this.style.fill,
          "fill-opacity": 0,
          "fill-opacity-transition": { duration: FADE_MS },
        },
      },
      INSERT_BEFORE,
    );
    map.addLayer(
      {
        id: this.lineLayerId(slot),
        type: "line",
        source,
        "source-layer": SOURCE_LAYER,
        layout: {
          "line-sort-key": ["coalesce", ["to-number", ["get", "render_rank"]], 0],
        },
        paint: {
          "line-color": this.style.line,
          "line-width": this.style.lineWidth,
          ...(this.style.dashed ? { "line-dasharray": [3, 2] } : {}),
          "line-opacity": 0,
          "line-opacity-transition": { duration: FADE_MS },
        },
      },
      INSERT_BEFORE,
    );
    this.created.add(slot);
  }

  private setOpacity(map: maplibregl.Map, slot: Slot, on: boolean): void {
    map.setPaintProperty(
      this.fillLayerId(slot),
      "fill-opacity",
      on ? this.style.fillOpacity : 0,
    );
    map.setPaintProperty(
      this.lineLayerId(slot),
      "line-opacity",
      on ? this.style.lineOpacity : 0,
    );
  }

  liveFillLayerId(): string | null {
    return this.visible === null ? null : this.fillLayerId(this.visible);
  }

  /**
   * True for map "error" events this pair owns end-to-end. MapView's catch-all
   * error logger must skip these - the pair knows whether a failure is a
   * cancellation, a first attempt worth retrying, or final.
   */
  ownsSource(sourceId: string | undefined): boolean {
    return !!sourceId?.startsWith(`wk-${this.kind}-`);
  }

  apply(map: maplibregl.Map, next: AreaLayerSlice | null): void {
    const url = next?.url ?? null;
    const retryScheduled = this.retryTimer !== null;
    if (this.retryTimer !== null) {
      window.clearTimeout(this.retryTimer);
      this.retryTimer = null;
    }
    if (url !== null && url === this.requestedUrl && !retryScheduled) return;
    this.requestedUrl = url;
    this.ownErrors(map);

    const pendingSlot = this.pending?.slot ?? null;
    this.pending?.cancel();
    this.pending = null;

    if (!next) {
      for (const slot of this.created) this.setOpacity(map, slot, false);
      this.visible = null;
      this.lastSlice = null;
      this.retriedUrl = null;
      return;
    }

    if (next.url !== this.lastSlice?.url) {
      this.retriedUrl = null;
    }
    this.lastSlice = next;
    const slot = pendingSlot ?? (this.visible === "a" ? "b" : "a");
    const outgoing = this.visible;
    const sourceId = this.sourceId(slot);
    const onSourceData = (event: maplibregl.MapSourceDataEvent) => {
      if (event.sourceId !== sourceId || !event.isSourceLoaded) return;
      map.off("sourcedata", onSourceData);
      this.pending = null;
      this.retriedUrl = null;
      this.setOpacity(map, slot, true);
      if (outgoing !== null && outgoing !== slot) this.setOpacity(map, outgoing, false);
      this.visible = slot;
      this.visibleSlice = next;
    };
    map.on("sourcedata", onSourceData);
    this.pending = { slot, slice: next, cancel: () => map.off("sourcedata", onSourceData) };
    this.ensureSlot(map, slot, next.url);
  }

  /**
   * One permanent error listener per pair. A failed archive fires "error",
   * never "sourcedata"; without this the old slice keeps painting under a
   * chip naming the new one, forever. Cancellations are lifecycle noise; a
   * first real failure gets one delayed retry (a camera move during boot can
   * kill the shared archive fetch under every waiter); a second failure logs
   * and blanks the layer, and forgets the URL so a cursor move tries again.
   */
  private ownErrors(map: maplibregl.Map): void {
    if (this.errorsOwned) return;
    this.errorsOwned = true;
    map.on("error", (event) => {
      const e = event as unknown as { sourceId?: string; error?: unknown };
      if (!this.ownsSource(e.sourceId) || isAbort(e.error)) return;
      if (this.retryTimer !== null) return;
      const pending = this.pending;
      // Errors from an outgoing or superseded slot must not cancel the newer
      // pending slice or resurrect an older cursor position.
      if (pending && e.sourceId !== this.sourceId(pending.slot)) return;
      const slice = pending
        ? pending.slice
        : this.visible !== null && e.sourceId === this.sourceId(this.visible)
          ? this.visibleSlice
          : null;
      if (!slice) return;
      pending?.cancel();
      this.pending = null;
      this.requestedUrl = null;
      if (this.retriedUrl !== slice.url) {
        this.retriedUrl = slice.url;
        console.warn(`area layer ${e.sourceId} load interrupted; retrying:`, e.error);
        this.retryTimer = window.setTimeout(() => {
          this.retryTimer = null;
          this.apply(map, slice);
        }, 1000);
        return;
      }
      console.error(`area layer ${e.sourceId} failed to load:`, e.error);
      for (const s of this.created) this.setOpacity(map, s, false);
      this.visible = null;
      this.visibleSlice = null;
      this.onLoadFailure?.();
    });
  }

  reset(): void {
    this.pending?.cancel();
    if (this.retryTimer !== null) window.clearTimeout(this.retryTimer);
    this.visible = null;
    this.visibleSlice = null;
    this.lastSlice = null;
    this.requestedUrl = null;
    this.pending = null;
    this.retriedUrl = null;
    this.retryTimer = null;
    this.errorsOwned = false;
    this.created.clear();
  }
}

export function queryAreaFeature(
  map: maplibregl.Map,
  slots: SlotPair,
  point: maplibregl.PointLike,
): AreaFeatureProperties | null {
  const layer = slots.liveFillLayerId();
  if (!layer || !map.getLayer(layer)) return null;
  const properties = map.queryRenderedFeatures(point, { layers: [layer] })[0]?.properties;
  const name = typeof properties?.name === "string" ? properties.name : undefined;
  const slug = typeof properties?.slug === "string" ? properties.slug : undefined;
  const source = typeof properties?.source === "string" ? properties.source : undefined;
  const sourceId = typeof properties?.source_id === "string" ? properties.source_id : undefined;
  return name || slug
    ? {
        ...(name ? { name } : {}),
        ...(slug ? { slug } : {}),
        ...(source ? { source } : {}),
        ...(sourceId ? { sourceId } : {}),
      }
    : null;
}
