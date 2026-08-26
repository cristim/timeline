import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  // Subpath deploys (GitHub Pages serves at /<repo>/) set VITE_BASE.
  base: process.env.VITE_BASE ?? "/",
  plugins: [react()],
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
