import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  // Subpath deploys (GitHub Pages serves at /<repo>/) set VITE_BASE.
  base: process.env.VITE_BASE ?? "/",
  plugins: [react()],
  // maplibre-gl ships a worker bundle the dep optimizer mangles; leave it be.
  optimizeDeps: { exclude: ["maplibre-gl"] },
  server: {
    host: true,
    port: 5173,
    strictPort: true,
  },
});
