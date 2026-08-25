import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  // maplibre-gl ships a worker bundle the dep optimizer mangles; leave it be.
  optimizeDeps: { exclude: ["maplibre-gl"] },
  server: {
    host: true,
    port: 5173,
    strictPort: true,
  },
});
