import type { FeatureCollection, Point } from "geojson";
import type { ChunkItem, EntityDoc } from "./data";
import { categoryColor, FALLBACK_COLOR } from "./colors";

export type ComparePinRole = "compare-a" | "compare-b";
export const COMPARE_A_COLOR = "#e8b45a";
export const COMPARE_B_COLOR = "#6fd4d0";

export function comparePinColor(role: ComparePinRole | null): string | null {
  if (role === "compare-a") return COMPARE_A_COLOR;
  if (role === "compare-b") return COMPARE_B_COLOR;
  return null;
}

export function comparePinRole(slug: string, compare: readonly string[]): ComparePinRole | null {
  if (slug === compare[0]) return "compare-a";
  if (slug === compare[1]) return "compare-b";
  return null;
}

export function entityChunkItem(doc: EntityDoc): ChunkItem {
  return {
    slug: doc.slug,
    type: doc.type,
    name: doc.name,
    t0: doc.temporal.t0,
    t1: doc.temporal.t1,
    precision: doc.temporal.precision,
    status: doc.temporal.status,
    point: doc.point,
    categories: doc.categories,
    importance: doc.importance,
    media_thumb: doc.media_thumb,
    child_count: doc.children?.length,
  };
}

export function withPinnedEntities(items: ChunkItem[], docs: readonly (EntityDoc | null)[]): ChunkItem[] {
  const seen = new Set(items.map((item) => item.slug));
  const added: ChunkItem[] = [];
  for (const doc of docs) {
    if (!doc || seen.has(doc.slug)) continue;
    seen.add(doc.slug);
    added.push(entityChunkItem(doc));
  }
  return [...items, ...added];
}

export function mapPointFeatures(
  items: ChunkItem[],
  selected: string | null,
  compare: readonly string[],
): FeatureCollection<Point> {
  return {
    type: "FeatureCollection",
    features: items.flatMap((item) => {
      if (!item.point) return [];
      return [{
        type: "Feature" as const,
        geometry: { type: "Point" as const, coordinates: item.point },
        properties: {
          slug: item.slug,
          name: item.name,
          color: item.categories.map((category) => categoryColor[category]).find(Boolean) ?? FALLBACK_COLOR,
          selected: item.slug === selected ? 1 : 0,
          compare: comparePinRole(item.slug, compare) ?? "",
        },
      }];
    }),
  };
}

export function clickedEntitySlug(features: readonly { properties?: unknown }[]): string | null {
  const properties = features[0]?.properties;
  if (!properties || typeof properties !== "object") return null;
  const slug = (properties as Record<string, unknown>).slug;
  return typeof slug === "string" ? slug : null;
}
