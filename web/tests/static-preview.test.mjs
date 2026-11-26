import { strict as assert } from "node:assert";
import { existsSync, readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { after, before, test } from "node:test";
import { preview } from "vite";

const webRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const distRoot = resolve(webRoot, "dist");
const requiredFiles = ["index.html", "404.html", "manifest.json"];
const indexDocument = resolve(distRoot, "index.html");
const fallbackDocument = resolve(distRoot, "404.html");

const missingFiles = requiredFiles.filter((file) => !existsSync(resolve(distRoot, file)));
if (missingFiles.length > 0) {
  throw new Error(`Static preview requires a built web/dist artifact; missing: ${missingFiles.join(", ")}`);
}

const indexHtml = readFileSync(indexDocument, "utf8");
const moduleAsset = indexHtml.match(/<script[^>]+type="module"[^>]+src="([^"]+)"/i)?.[1];
if (!moduleAsset) throw new Error("Static preview requires a module asset in web/dist/index.html");

const request = async (url, accept = "*/*") => {
  const response = await fetch(url, {
    headers: { Accept: accept },
    redirect: "manual",
    signal: AbortSignal.timeout(5000),
  });
  return { body: await response.text(), headers: response.headers, statusCode: response.status };
};

const startPreview = async (base) => {
  process.env.E2E_STATIC = "1";
  process.env.VITE_BASE = base;
  const server = await preview({
    configFile: resolve(webRoot, "vite.config.ts"),
    preview: { host: "127.0.0.1", port: 0, strictPort: false },
  });
  const address = server.httpServer.address();
  assert.equal(typeof address, "object");
  return { server, origin: `http://127.0.0.1:${address.port}` };
};

let timelinePreview;
let rootPreview;

before(async () => {
  timelinePreview = await startPreview("/timeline/");
  rootPreview = await startPreview("/");
});

after(async () => {
  await Promise.all(
    [timelinePreview, rootPreview]
      .filter(Boolean)
      .map(({ server }) => server.close()),
  );
});

test("serves the timeline static preview without redirect loops", async () => {
  const landing = await request(`${timelinePreview.origin}/timeline/`);
  assert.equal(landing.statusCode, 200);
  assert.equal(landing.headers.get("location"), null);
  assert.match(landing.headers.get("content-type"), /text\/html/);

  const queryLanding = await request(`${timelinePreview.origin}/timeline/?from=preview`);
  assert.equal(queryLanding.statusCode, 200);
  assert.equal(queryLanding.headers.get("location"), null);

  const asset = await request(`${timelinePreview.origin}${moduleAsset}`);
  assert.equal(asset.statusCode, 200);
  assert.match(asset.headers.get("content-type"), /javascript/);

  const manifest = await request(`${timelinePreview.origin}/timeline/manifest.json`);
  assert.equal(manifest.statusCode, 200);
  assert.match(manifest.headers.get("content-type"), /json/);

  const missingArtifact = await request(`${timelinePreview.origin}/timeline/v/missing.json`);
  assert.equal(missingArtifact.statusCode, 404);
  assert.equal(missingArtifact.body, "not found");

  const deepRoute = await request(
    `${timelinePreview.origin}/timeline/entity/tyrannosaurus-rex?compare=african-bush-elephant`,
    "text/html",
  );
  assert.equal(deepRoute.statusCode, 404);
  assert.equal(deepRoute.body, readFileSync(fallbackDocument, "utf8"));

  const staticEntityPage = await request(
    `${timelinePreview.origin}/timeline/entity/tyrannosaurus-rex/?compare=african-bush-elephant`,
    "text/html",
  );
  assert.equal(staticEntityPage.statusCode, 200);
  assert.match(staticEntityPage.body, /<h1>Tyrannosaurus rex<\/h1>/);

  const unknownStaticEntityPage = await request(
    `${timelinePreview.origin}/timeline/entity/unknown-but-valid/`,
    "text/html",
  );
  assert.equal(unknownStaticEntityPage.statusCode, 404);
  assert.equal(unknownStaticEntityPage.body, readFileSync(fallbackDocument, "utf8"));

  const rootRedirect = await request(`${timelinePreview.origin}/`);
  assert.equal(rootRedirect.statusCode, 302);
  assert.equal(rootRedirect.headers.get("location"), "/timeline/");
});

test("serves the existing bundle from a root preview", async () => {
  const landing = await request(`${rootPreview.origin}/`);
  assert.equal(landing.statusCode, 200);
  assert.equal(landing.headers.get("location"), null);

  const rootAsset = moduleAsset.replace(/^\/timeline/, "");
  const asset = await request(`${rootPreview.origin}${rootAsset}`);
  assert.equal(asset.statusCode, 200);
  assert.match(asset.headers.get("content-type"), /javascript/);
});
