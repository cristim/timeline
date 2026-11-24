import { mkdir, mkdtemp, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import test from "node:test";
import assert from "node:assert/strict";
import {
  canonicalEntityURL,
  escapeHTML,
  renderEntityPage,
  renderStaticEntityPages,
  validateEntityDoc,
} from "./render-static-entity-pages.mjs";

const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>Timeline</title>
  <script type="module" crossorigin src="/timeline/assets/index-abc.js"></script>
  <link rel="stylesheet" crossorigin href="/timeline/assets/index-def.css">
</head>
<body>
  <div id="root"></div>
</body>
</html>`;

function entity(overrides = {}) {
  return {
    slug: "alpha",
    name: "Alpha & <Beta>",
    type: "place",
    description: `quoted "description"`,
    temporal: { t0: 1, t1: 2, precision: "year", status: "documented" },
    links: { wikidata: "https://www.wikidata.org/wiki/Q1" },
    ...overrides,
  };
}

test("escapes HTML special characters", () => {
  assert.equal(escapeHTML(`&<>"'`), "&amp;&lt;&gt;&quot;&#39;");
});

test("builds trailing-slash canonical entity URLs under the deployed base", () => {
  assert.equal(
    canonicalEntityURL("https://cristim.github.io/timeline/", "alpha"),
    "https://cristim.github.io/timeline/entity/alpha/",
  );
});

test("renders escaped indexable content without rewriting built assets", () => {
  const html = renderEntityPage(indexHTML, entity(), {
    baseURL: "https://cristim.github.io/timeline/",
  });
  assert.match(html, /<title>Alpha &amp; &lt;Beta&gt; \| Timeline<\/title>/);
  assert.match(html, /<h1>Alpha &amp; &lt;Beta&gt;<\/h1>/);
  assert.match(html, /content="quoted &quot;description&quot;"/);
  assert.match(html, /href="https:\/\/cristim.github.io\/timeline\/entity\/alpha\/"/);
  assert.match(html, /src="\/timeline\/assets\/index-abc.js"/);
  assert.match(html, /href="\/timeline\/assets\/index-def.css"/);
  assert.doesNotMatch(html, /<Beta>/);
});

test("fails loudly if the built app shell cannot host static content", () => {
  assert.throws(
    () => renderEntityPage(indexHTML.replace('<div id="root"></div>', '<div id="app"></div>'), entity(), {
      baseURL: "https://cristim.github.io/timeline/",
    }),
    /missing <div id="root"><\/div>/,
  );
});

test("rejects malformed entity JSON before rendering", () => {
  assert.throws(() => validateEntityDoc({ ...entity(), slug: "other" }, "alpha"), /document slug/);
  assert.throws(() => validateEntityDoc({ ...entity(), temporal: { t0: "1" } }, "alpha"), /temporal.t0/);
});

test("renders entity pages from an already-built static artifact directory", async () => {
  const outDir = await mkdtemp(join(tmpdir(), "wk-ssg-"));
  await writeFile(join(outDir, "index.html"), indexHTML);
  await writeFile(join(outDir, "manifest.json"), JSON.stringify({ dataset: "dev" }));
  await mkdir(join(outDir, "v", "dev", "entity"), { recursive: true });
  await writeFile(join(outDir, "v", "dev", "entity", "alpha.json"), JSON.stringify(entity()));

  const report = await renderStaticEntityPages({
    outDir,
    baseURL: "https://cristim.github.io/timeline/",
  });
  const html = await readFile(join(outDir, "entity", "alpha", "index.html"), "utf8");

  assert.equal(report.rendered, 1);
  assert.ok(report.bytes > 0);
  assert.match(html, /<article class="ssg-entity">/);
});

test("enforces the Pages artifact size gate", async () => {
  const outDir = await mkdtemp(join(tmpdir(), "wk-ssg-size-"));
  await writeFile(join(outDir, "index.html"), indexHTML);
  await writeFile(join(outDir, "manifest.json"), JSON.stringify({ dataset: "dev" }));
  await mkdir(join(outDir, "v", "dev", "entity"), { recursive: true });
  await writeFile(join(outDir, "v", "dev", "entity", "alpha.json"), JSON.stringify(entity()));

  await assert.rejects(
    () => renderStaticEntityPages({
      outDir,
      baseURL: "https://cristim.github.io/timeline/",
      maxBytes: 1,
    }),
    /Pages artifact/,
  );
});
