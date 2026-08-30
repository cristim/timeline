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
  cancel: () => void;
}

export interface AreaFeatureProperties {
  name?: string;
  slug?: string;
}

export class SlotPair {
  private visible: Slot | null = null;
  private requestedUrl: string | null = null;
  private pending: PendingLoad | null = null;
  private readonly created = new Set<Slot>();

  constructor(
    private readonly kind: string,
    private readonly style: AreaLayerStyle,
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

  apply(map: maplibregl.Map, next: AreaLayerSlice | null): void {
    const url = next?.url ?? null;
    if (url === this.requestedUrl) return;
    this.requestedUrl = url;

    const pendingSlot = this.pending?.slot ?? null;
    this.pending?.cancel();
    this.pending = null;

    if (!next) {
      for (const slot of this.created) this.setOpacity(map, slot, false);
      this.visible = null;
      return;
    }

    const slot = pendingSlot ?? (this.visible === "a" ? "b" : "a");
    const outgoing = this.visible;
    const sourceId = this.sourceId(slot);
    const onSourceData = (event: maplibregl.MapSourceDataEvent) => {
      if (event.sourceId !== sourceId || !event.isSourceLoaded) return;
      map.off("sourcedata", onSourceData);
      this.pending = null;
      this.setOpacity(map, slot, true);
      if (outgoing !== null && outgoing !== slot) this.setOpacity(map, outgoing, false);
      this.visible = slot;
    };
    map.on("sourcedata", onSourceData);
    this.pending = { slot, cancel: () => map.off("sourcedata", onSourceData) };
    this.ensureSlot(map, slot, next.url);
  }

  reset(): void {
    this.pending?.cancel();
    this.visible = null;
    this.requestedUrl = null;
    this.pending = null;
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
  return name || slug ? { ...(name ? { name } : {}), ...(slug ? { slug } : {}) } : null;
}
