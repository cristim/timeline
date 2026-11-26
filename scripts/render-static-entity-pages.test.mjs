import { mkdir, mkdtemp, readFile, symlink, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import test from "node:test";
import assert from "node:assert/strict";
import {
  canonicalEntityURL,
  escapeHTML,
  formatEntityRange,
  renderEntityPage,
  renderStaticEntityPages,
  resolveSourceLink,
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
    temporal: { t0: 0, t1: 31_556_952, precision: "year", status: "documented" },
    links: {
      wikipedia: "https://en.wikipedia.org/wiki/Alpha",
      wikidata: "Q1",
    },
    ...overrides,
  };
}

test("escapes HTML special characters", () => {
  assert.equal(escapeHTML(`&<>"'`), "&amp;&lt;&gt;&quot;&#39;");
});

test("builds trailing-slash canonical entity URLs under the deployed base and rejects invalid schemes", () => {
  assert.equal(
    canonicalEntityURL("https://cristim.github.io/timeline/", "alpha"),
    "https://cristim.github.io/timeline/entity/alpha/",
  );
  assert.throws(
    () => canonicalEntityURL("javascript:alert(1)", "alpha"),
    /http\(s\) URL/,
  );
  assert.throws(
    () => canonicalEntityURL("https://user:pass@cristim.github.io/timeline/", "alpha"),
    /without credentials/,
  );
});

test("resolves bare Wikidata QIDs, bare Wikipedia titles, and valid https URLs while rejecting invalid links", () => {
  assert.equal(resolveSourceLink("wikidata", "Q14332"), "https://www.wikidata.org/wiki/Q14332");
  assert.equal(resolveSourceLink("wikipedia", "Battle of Waterloo"), "https://en.wikipedia.org/wiki/Battle_of_Waterloo");
  assert.equal(resolveSourceLink("wikipedia", "Dune: Part One"), "https://en.wikipedia.org/wiki/Dune%3A_Part_One");
  assert.equal(resolveSourceLink("wikipedia", "javascript:alert(1)"), null);
  assert.equal(
    resolveSourceLink("wikipedia", "https://en.wikipedia.org/wiki/Tyrannosaurus"),
    "https://en.wikipedia.org/wiki/Tyrannosaurus",
  );
  assert.equal(resolveSourceLink("source", "https://"), null);
  assert.equal(resolveSourceLink("source", "https:// invalid"), null);
  assert.equal(resolveSourceLink("source", "http://example.com/"), null);
});

test("formats temporal ranges deterministically across deep time and historical dates", () => {
  assert.equal(
    formatEntityRange({
      t0: -2145934903195440,
      t1: -2082820999195440,
      precision: "million_year",
    }),
    "68 Myr ago – 66 Myr ago",
  );
  assert.equal(
    formatEntityRange({
      t0: -14191200,
      t1: -14191200,
      precision: "day",
    }),
    "20 Jul 1969",
  );
  assert.equal(
    formatEntityRange({
      t0: 0,
      t1: 31_556_952,
      precision: "year",
    }),
    "1970 – 1971",
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
  assert.match(html, /href="https:\/\/www\.wikidata\.org\/wiki\/Q1"/);
  assert.match(html, /href="https:\/\/en\.wikipedia\.org\/wiki\/Alpha"/);
  assert.match(html, /<dt>When<\/dt><dd>1970 – 1971<\/dd>/);
  assert.match(html, /src="\/timeline\/assets\/index-abc\.js"/);
  assert.match(html, /href="\/timeline\/assets\/index-def\.css"/);
  assert.doesNotMatch(html, /<Beta>/);
});

test("preserves literal dollar replacement patterns in entity text and multiline title tags", () => {
  const multilineIndex = indexHTML.replace(
    "<title>Timeline</title>",
    `<title lang="en">\n    Timeline\n  </title>`,
  );
  const literalDollars = "Price $$100, match $&, prefix $`, suffix $'";
  const html = renderEntityPage(
    multilineIndex,
    entity({
      name: "Dollar $' Entity $$",
      description: literalDollars,
    }),
    {
      baseURL: "https://cristim.github.io/timeline/",
    },
  );
  assert.equal((html.match(/<div id="root">/g) || []).length, 1);
  assert.equal((html.match(/<\/body>/g) || []).length, 1);
  assert.ok(html.includes(`<h1>Dollar $&#39; Entity $$</h1>`));
  assert.ok(html.includes(`<p>Price $$100, match $&amp;, prefix $\`, suffix $&#39;</p>`));
  assert.doesNotMatch(html, /<title lang="en">/);
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
  assert.throws(
    () => renderEntityPage(indexHTML, { ...entity(), slug: "../escape" }, { baseURL: "https://cristim.github.io/timeline/" }),
    /slug must match/,
  );
});

test("rejects invalid or path-traversing manifest.dataset values and symbolic links", async () => {
  const outDir = await mkdtemp(join(tmpdir(), "wk-ssg-bad-ds-"));
  await writeFile(join(outDir, "index.html"), indexHTML);
  await writeFile(join(outDir, "manifest.json"), JSON.stringify({ dataset: "../escape" }));
  await assert.rejects(
    () => renderStaticEntityPages({ outDir, baseURL: "https://cristim.github.io/timeline/" }),
    /manifest\.dataset must be a valid dataset identifier/,
  );

  await writeFile(join(outDir, "manifest.json"), JSON.stringify({ dataset: "dev" }));
  await mkdir(join(outDir, "v", "dev", "entity"), { recursive: true });
  await writeFile(join(outDir, "outside.json"), JSON.stringify(entity()));
  await symlink(join(outDir, "outside.json"), join(outDir, "v", "dev", "entity", "alpha.json"));
  await assert.rejects(
    () => renderStaticEntityPages({ outDir, baseURL: "https://cristim.github.io/timeline/" }),
    /refusing symbolic link/,
  );
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
