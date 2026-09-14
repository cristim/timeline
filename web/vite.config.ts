import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";
import type { Plugin } from "vite";
import { defineConfig } from "vitest/config";

const previewRoot = fileURLToPath(new URL("./dist/", import.meta.url));
const fallbackDocument = fileURLToPath(new URL("./dist/404.html", import.meta.url));
const staticPreviewBase: Plugin = {
  name: "static-preview-base",
  configurePreviewServer(server) {
    server.middlewares.use((request, response, next) => {
      if (!request.url?.startsWith("/timeline/")) return next();
      const pathname = new URL(request.url, "http://preview.invalid").pathname.slice("/timeline".length);
      const isArtifact = pathname === "/manifest.json" || pathname.startsWith("/v/");
      if (isArtifact && !existsSync(fileURLToPath(new URL(`.${pathname}`, `file://${previewRoot}`)))) {
        response.statusCode = 404;
        response.end("not found");
        return;
      }
      if (pathname !== "/" && request.headers.accept?.includes("text/html")) {
        response.statusCode = 404;
        response.setHeader("Content-Type", "text/html; charset=utf-8");
        response.end(readFileSync(fallbackDocument));
        return;
      }
      next();
    });
  },
};

export default defineConfig({
  // Subpath deploys (GitHub Pages serves at /<repo>/) set VITE_BASE.
  base: process.env.VITE_BASE ?? "/",
  plugins: [react(), ...(process.env.E2E_STATIC ? [staticPreviewBase] : [])],
  // Unit tests only; e2e/*.spec.ts belongs to Playwright, not vitest.
  test: { include: ["src/**/*.test.ts"] },
  // maplibre-gl ships a worker bundle the dep optimizer mangles; leave it be.
  optimizeDeps: { exclude: ["maplibre-gl"] },
  server: {
    host: true,
    port: 5173,
    strictPort: true,
  },
});
