#!/usr/bin/env node
import { mkdir, readdir, readFile, stat, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const MAX_PAGES_BYTES = 1024 * 1024 * 1024;
// Mirrors web/src/lib/slug.ts (ENTITY_SLUG), web/src/lib/manifest.ts (DATASET_ID),
// and web/src/lib/keyscheme.ts + web/src/lib/timefmt.ts (SECONDS_PER_YEAR).
const ENTITY_SLUG = /^[a-z0-9]+(?:-+[a-z0-9]+)*$/;
const DATASET_ID = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;
const WIKIDATA_QID = /^Q[1-9]\d*$/;
const SECONDS_PER_YEAR = 31_556_952;
const MONTHS_UTC = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

export function escapeHTML(value) {
  return String(value).replace(/[&<>"']/g, (ch) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  })[ch]);
}

export function canonicalEntityURL(baseURL, slug) {
  if (typeof slug !== "string" || !ENTITY_SLUG.test(slug)) {
    throw new Error(`${slug}: slug must match the entity route syntax`);
  }
  const base = new URL(baseURL);
  if ((base.protocol !== "https:" && base.protocol !== "http:") || base.username || base.password) {
    throw new Error("baseURL must be an http(s) URL without credentials");
  }
  const root = base.pathname.endsWith("/") ? base.pathname : `${base.pathname}/`;
  base.pathname = `${root}entity/${encodeURIComponent(slug)}/`;
  base.search = "";
  base.hash = "";
  return base.href;
}

function secondsToYear(t) {
  return t / SECONDS_PER_YEAR + 1970;
}

function trimTimeNumber(v) {
  const r = Math.abs(v) >= 100 ? Math.round(v) : Math.round(v * 10) / 10;
  return `${r}`;
}

function rangeDisplaySpan(precision) {
  switch (precision) {
    case "billion_year":
    case "million_year":
    case "millennium":
    case "century":
    case "decade":
    case "year":
      return 10 * SECONDS_PER_YEAR;
    case "month":
      return 0.5 * SECONDS_PER_YEAR;
    case "day":
      return 5 * 86_400;
    default:
      return 3_600;
  }
}

export function formatTimeUTC(t, span) {
  const year = secondsToYear(t);
  const spanYears = span / SECONDS_PER_YEAR;

  if (Math.abs(year) >= 1e6) {
    const [div, unit] = Math.abs(year) >= 1e9 ? [1e9, "Gyr"] : [1e6, "Myr"];
    const v = trimTimeNumber(Math.abs(year) / div);
    return year < 0 ? `${v} ${unit} ago` : `in ${v} ${unit}`;
  }
  if (year < -10000) return `${trimTimeNumber(-year / 1000)} kyr ago`;
  if (year <= 0) return `${Math.round(-year) + 1} BCE`;
  if (spanYears > 3) return `${Math.floor(year)}`;

  const d = new Date(t * 1000);
  if (Number.isNaN(d.getTime())) return `${Math.floor(year)}`;
  const month = MONTHS_UTC[d.getUTCMonth()];
  const fullYear = d.getUTCFullYear();
  if (spanYears > 0.2) {
    return `${month} ${fullYear}`;
  }
  const day = d.getUTCDate();
  if (span > 3 * 86_400) {
    return `${day} ${month} ${fullYear}`;
  }
  const hh = String(d.getUTCHours()).padStart(2, "0");
  const mm = String(d.getUTCMinutes()).padStart(2, "0");
  return `${day} ${month} ${fullYear}, ${hh}:${mm} UTC`;
}

export function formatEntityRange(temporal) {
  const span = rangeDisplaySpan(temporal.precision);
  const a = formatTimeUTC(temporal.t0, span);
  if (temporal.t1 === temporal.t0) return a;
  const b = formatTimeUTC(temporal.t1, span);
  return a === b ? a : `${a} – ${b}`;
}

export function resolveSourceLink(name, rawValue) {
  if (typeof rawValue !== "string") return null;
  const value = rawValue.trim();
  if (!value) return null;
  if (name === "wikidata" && WIKIDATA_QID.test(value)) {
    return `https://www.wikidata.org/wiki/${value}`;
  }
  if (
    name === "wikipedia" &&
    !value.includes("://") &&
    !value.startsWith("//") &&
    !/^(?:https?|ftp|mailto|javascript|data|vbscript|file):/i.test(value)
  ) {
    return `https://en.wikipedia.org/wiki/${encodeURIComponent(value.replace(/ /g, "_"))}`;
  }
  if (!value.startsWith("https://")) return null;
  try {
    const parsed = new URL(value);
    if (parsed.protocol !== "https:" || !parsed.hostname || parsed.username || parsed.password) {
      return null;
    }
    return parsed.href;
  } catch {
    return null;
  }
}

export function validateEntityDoc(doc, expectedSlug) {
  if (!doc || typeof doc !== "object" || Array.isArray(doc)) {
    throw new Error(`${expectedSlug}: entity document must be an object`);
  }
  if (typeof expectedSlug !== "string" || !ENTITY_SLUG.test(expectedSlug)) {
    throw new Error(`${expectedSlug}: slug must match the entity route syntax`);
  }
  for (const field of ["slug", "name", "type"]) {
    if (typeof doc[field] !== "string" || doc[field].trim() === "") {
      throw new Error(`${expectedSlug}: ${field} must be a non-empty string`);
    }
  }
  if (doc.slug !== expectedSlug) {
    throw new Error(`${expectedSlug}: document slug is ${JSON.stringify(doc.slug)}`);
  }
  if (doc.description !== undefined && typeof doc.description !== "string") {
    throw new Error(`${expectedSlug}: description must be a string`);
  }
  if (!doc.temporal || typeof doc.temporal !== "object" || Array.isArray(doc.temporal)) {
    throw new Error(`${expectedSlug}: temporal must be an object`);
  }
  for (const field of ["t0", "t1"]) {
    if (typeof doc.temporal[field] !== "number" || !Number.isFinite(doc.temporal[field])) {
      throw new Error(`${expectedSlug}: temporal.${field} must be finite`);
    }
  }
  for (const field of ["precision", "status"]) {
    if (typeof doc.temporal[field] !== "string" || doc.temporal[field].trim() === "") {
      throw new Error(`${expectedSlug}: temporal.${field} must be a non-empty string`);
    }
  }
  if (doc.links !== undefined && (typeof doc.links !== "object" || doc.links === null || Array.isArray(doc.links))) {
    throw new Error(`${expectedSlug}: links must be an object`);
  }
  return doc;
}

export function renderEntityPage(indexHTML, entity, options) {
  validateEntityDoc(entity, entity?.slug);
  if (!indexHTML.includes("</head>")) {
    throw new Error("index.html is missing </head>");
  }
  if (!indexHTML.includes('<div id="root"></div>')) {
    throw new Error('index.html is missing <div id="root"></div>');
  }

  const canonical = canonicalEntityURL(options.baseURL, entity.slug);
  const title = `${entity.name} | Timeline`;
  const description = entity.description || `${entity.name} on the Timeline knowledge map.`;
  const when = formatEntityRange(entity.temporal);
  const sourceLinks = Object.entries(entity.links || {})
    .map(([name, rawValue]) => [name, resolveSourceLink(name, rawValue)])
    .filter(([, url]) => Boolean(url))
    .map(([name, url]) => `<li><a href="${escapeHTML(url)}">${escapeHTML(name)}</a></li>`)
    .join("");
  const head = [
    `<title>${escapeHTML(title)}</title>`,
    `<link rel="canonical" href="${escapeHTML(canonical)}" />`,
    `<meta name="description" content="${escapeHTML(description)}" />`,
    `<meta property="og:title" content="${escapeHTML(title)}" />`,
    `<meta property="og:description" content="${escapeHTML(description)}" />`,
    `<meta property="og:url" content="${escapeHTML(canonical)}" />`,
  ].join("\n  ");
  const body = [
    `<article class="ssg-entity">`,
    `<h1>${escapeHTML(entity.name)}</h1>`,
    `<p>${escapeHTML(description)}</p>`,
    `<dl>`,
    `<dt>Type</dt><dd>${escapeHTML(entity.type)}</dd>`,
    `<dt>When</dt><dd>${escapeHTML(when)}</dd>`,
    `<dt>Status</dt><dd>${escapeHTML(entity.temporal.status)}</dd>`,
    `</dl>`,
    sourceLinks ? `<h2>Sources</h2><ul>${sourceLinks}</ul>` : "",
    `</article>`,
  ].join("");

  return indexHTML
    .replace(/<title\b[^>]*>[\s\S]*?<\/title>/i, "")
    .replace("</head>", () => `  ${head}\n</head>`)
    .replace('<div id="root"></div>', () => `<div id="root">${body}</div>`);
}

export async function renderStaticEntityPages({ outDir, baseURL, maxBytes = MAX_PAGES_BYTES }) {
  const indexHTML = await readFile(join(outDir, "index.html"), "utf8");
  const manifest = JSON.parse(await readFile(join(outDir, "manifest.json"), "utf8"));
  if (
    !manifest ||
    typeof manifest.dataset !== "string" ||
    !DATASET_ID.test(manifest.dataset) ||
    manifest.dataset.includes("..")
  ) {
    throw new Error("manifest.dataset must be a valid dataset identifier");
  }
  const entityDir = join(outDir, "v", manifest.dataset, "entity");
  const entries = await readdir(entityDir, { withFileTypes: true });
  const files = entries
    .filter((entry) => {
      if (entry.isSymbolicLink()) {
        throw new Error(`refusing symbolic link in entity directory: ${entry.name}`);
      }
      return entry.isFile() && entry.name.endsWith(".json");
    })
    .map((entry) => entry.name)
    .sort();
  let rendered = 0;
  for (const file of files) {
    const slug = file.slice(0, -".json".length);
    const entity = validateEntityDoc(JSON.parse(await readFile(join(entityDir, file), "utf8")), slug);
    await mkdir(join(outDir, "entity", slug), { recursive: true });
    await writeFile(
      join(outDir, "entity", slug, "index.html"),
      renderEntityPage(indexHTML, entity, { baseURL }),
      "utf8",
    );
    rendered++;
  }
  const bytes = await directorySize(outDir);
  if (bytes > maxBytes) {
    throw new Error(`Pages artifact is ${bytes} bytes, above ${maxBytes}`);
  }
  return { rendered, bytes };
}

async function directorySize(dir) {
  let total = 0;
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isSymbolicLink()) {
      throw new Error(`refusing symbolic link in Pages artifact: ${path}`);
    }
    if (entry.isDirectory()) {
      total += await directorySize(path);
    } else if (entry.isFile()) {
      total += (await stat(path)).size;
    }
  }
  return total;
}

async function main() {
  const [outDir, baseURL] = process.argv.slice(2);
  if (!outDir || !baseURL) {
    console.error("usage: render-static-entity-pages.mjs <out-dir> <canonical-base-url>");
    process.exit(1);
  }
  const { rendered, bytes } = await renderStaticEntityPages({ outDir, baseURL });
  console.log(`rendered ${rendered} entity pages; Pages artifact size ${bytes} bytes`);
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  main().catch((error) => {
    console.error(error instanceof Error ? error.message : String(error));
    process.exit(1);
  });
}
