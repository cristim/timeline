#!/usr/bin/env node
import { mkdir, readdir, readFile, stat, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const MAX_PAGES_BYTES = 1024 * 1024 * 1024;
const ENTITY_SLUG = /^[a-z0-9]+(?:-+[a-z0-9]+)*$/;

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
  const base = new URL(baseURL);
  const root = base.pathname.endsWith("/") ? base.pathname : `${base.pathname}/`;
  base.pathname = `${root}entity/${encodeURIComponent(slug)}/`;
  base.search = "";
  base.hash = "";
  return base.href;
}

export function validateEntityDoc(doc, expectedSlug) {
  if (!doc || typeof doc !== "object" || Array.isArray(doc)) {
    throw new Error(`${expectedSlug}: entity document must be an object`);
  }
  if (!ENTITY_SLUG.test(expectedSlug)) {
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
  if (!indexHTML.includes("</head>")) {
    throw new Error("index.html is missing </head>");
  }
  if (!indexHTML.includes('<div id="root"></div>')) {
    throw new Error('index.html is missing <div id="root"></div>');
  }

  const canonical = canonicalEntityURL(options.baseURL, entity.slug);
  const title = `${entity.name} | Timeline`;
  const description = entity.description || `${entity.name} on the Timeline knowledge map.`;
  const when = `${entity.temporal.t0} to ${entity.temporal.t1}`;
  const sourceLinks = Object.entries(entity.links || {})
    .filter(([, url]) => typeof url === "string" && url.startsWith("https://"))
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
    .replace(/<title>.*?<\/title>/i, "")
    .replace("</head>", `  ${head}\n</head>`)
    .replace('<div id="root"></div>', `<div id="root">${body}</div>`);
}

export async function renderStaticEntityPages({ outDir, baseURL, maxBytes = MAX_PAGES_BYTES }) {
  const indexHTML = await readFile(join(outDir, "index.html"), "utf8");
  const manifest = JSON.parse(await readFile(join(outDir, "manifest.json"), "utf8"));
  if (!manifest || typeof manifest.dataset !== "string" || !manifest.dataset) {
    throw new Error("manifest.dataset must be a non-empty string");
  }
  const entityDir = join(outDir, "v", manifest.dataset, "entity");
  const files = (await readdir(entityDir)).filter((name) => name.endsWith(".json")).sort();
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
