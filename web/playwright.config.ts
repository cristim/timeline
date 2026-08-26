import { defineConfig } from "@playwright/test";

// E2E runs against a PROD-PARITY serving path, never the bare dev server:
// - default: the local gateway (make up && make bake) at localhost:8080,
//   which mirrors the CDN's URL contract and cache headers;
// - E2E_STATIC=1: the built static site (vite preview over web/dist, the
//   exact artifact GitHub Pages serves), used by CI before deploying.
const base = process.env.E2E_BASE_URL ?? "http://localhost:8080/";

export default defineConfig({
  testDir: "./e2e",
  timeout: 30_000,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? "github" : "list",
  use: {
    baseURL: base.endsWith("/") ? base : `${base}/`,
    viewport: { width: 1360, height: 850 },
    trace: "retain-on-failure",
  },
  webServer: process.env.E2E_STATIC
    ? {
        command: "npm run preview -- --port 4173 --strictPort",
        url: base,
        reuseExistingServer: false,
        timeout: 30_000,
      }
    : undefined,
});
