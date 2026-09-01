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

export type PropertyDatatype = "number";

export interface UnitConversion {
  scale: number;
  offset?: number;
}

export interface PropertyDef {
  label: string;
  datatype: PropertyDatatype;
  canonical_unit: string;
  conversions: Record<string, UnitConversion>;
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
  property_def: Record<string, PropertyDef>;
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

function manifestFiniteNumber(value: unknown, field: string): number {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    throw new Error(`${field} must be a finite number`);
  }
  return value;
}

function manifestUnitName(value: unknown, field: string): string {
  const unit = manifestString(value, field);
  if (!unit.trim()) throw new Error(`${field} unit name must not be empty`);
  if (unit !== unit.trim()) throw new Error(`${field} unit name must not contain surrounding whitespace`);
  return unit;
}

function manifestPropertyDefinitions(value: unknown): Record<string, PropertyDef> {
  const raw = manifestObject(value, "manifest.property_def");
  if (Object.keys(raw).length === 0) {
    throw new Error("manifest.property_def must not be empty");
  }

  const out = Object.create(null) as Record<string, PropertyDef>;
  for (const [id, value] of Object.entries(raw)) {
    const field = `manifest.property_def.${id}`;
    if (!/^[a-z][a-z0-9_]*$/.test(id)) {
      throw new Error(`${field} has an invalid property id`);
    }
    const definition = manifestObject(value, field);
    const label = manifestString(definition.label, `${field}.label`);
    if (!label.trim()) throw new Error(`${field}.label must not be empty`);
    if (definition.datatype !== "number") {
      throw new Error(`${field}.datatype must be number`);
    }
    const canonicalUnit = manifestUnitName(definition.canonical_unit, `${field}.canonical_unit`);

    const rawConversions = manifestObject(definition.conversions, `${field}.conversions`);
    const conversions = Object.create(null) as Record<string, UnitConversion>;
    for (const [unit, value] of Object.entries(rawConversions)) {
      const conversionField = `${field}.conversions.${unit}`;
      manifestUnitName(unit, conversionField);
      const rawConversion = manifestObject(value, conversionField);
      const scale = manifestFiniteNumber(rawConversion.scale, `${conversionField}.scale`);
      if (scale === 0) throw new Error(`${conversionField}.scale must not be zero`);
      const offset = rawConversion.offset === undefined
        ? undefined
        : manifestFiniteNumber(rawConversion.offset, `${conversionField}.offset`);
      conversions[unit] = offset === undefined ? { scale } : { scale, offset };
    }
    const canonicalConversion = conversions[canonicalUnit];
    if (canonicalConversion === undefined) {
      throw new Error(`${field}.conversions.${canonicalUnit} must define the canonical unit`);
    }
    if (canonicalConversion.scale !== 1 || (canonicalConversion.offset ?? 0) !== 0) {
      throw new Error(`${field}.conversions.${canonicalUnit} must be an identity conversion`);
    }
    out[id] = {
      label,
      datatype: "number",
      canonical_unit: canonicalUnit,
      conversions,
    };
  }
  return out;
}

function manifestTimesteps(value: unknown): Record<string, number[]> {
  const raw = manifestObject(value, "manifest.timesteps");
  const out = Object.create(null) as Record<string, number[]>;
  for (const [layer, value] of Object.entries(raw)) {
    if (!Array.isArray(value)) throw new Error(`manifest.timesteps.${layer} must be an array`);
    out[layer] = value.map((step, i) => manifestSafeInteger(step, `manifest.timesteps.${layer}[${i}]`));
  }
  return out;
}

function manifestCounts(value: unknown): Record<string, number> {
  const raw = manifestObject(value, "manifest.counts");
  const out = Object.create(null) as Record<string, number>;
  for (const [name, value] of Object.entries(raw)) {
    const count = manifestSafeInteger(value, `manifest.counts.${name}`);
    if (count < 0) throw new Error(`manifest.counts.${name} must not be negative`);
    out[name] = count;
  }
  return out;
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
    const id = manifestString(rawBucket.id, `manifest.buckets[${i}].id`);
    const windowS = manifestSafeInteger(rawBucket.window_s, `manifest.buckets[${i}].window_s`);
    if (windowS < 0) throw new Error(`manifest.buckets[${i}].window_s must not be negative`);
    return {
      id,
      window_s: windowS,
      windows: manifestWindowRuns(rawBucket.windows, `manifest.buckets[${i}].windows`),
    };
  });
  const timesteps = manifestTimesteps(raw.timesteps);
  const counts = manifestCounts(raw.counts);
  const propertyDef = manifestPropertyDefinitions(raw.property_def);

  return {
    ...(raw as unknown as Manifest),
    dataset,
    basemap: { key, source, attribution, sha256 },
    buckets,
    timesteps,
    counts,
    property_def: propertyDef,
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
