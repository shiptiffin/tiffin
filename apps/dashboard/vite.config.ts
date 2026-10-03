import { fileURLToPath, URL } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// The dashboard is embedded in the tiffin binary (internal/dashboard) and
// served under a strict CSP: everything is self-hosted, no inline scripts.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  build: {
    outDir: "../../internal/dashboard/dist",
    emptyOutDir: true,
    assetsInlineLimit: 0,
    sourcemap: false,
    chunkSizeWarningLimit: 700,
  },
  server: {
    port: 5391,
    strictPort: true,
    proxy: {
      "/v1": { target: process.env.TIFFIN_URL ?? "http://127.0.0.1:7391", changeOrigin: false },
      "/mcp": { target: process.env.TIFFIN_URL ?? "http://127.0.0.1:7391", changeOrigin: false },
    },
  },
});
