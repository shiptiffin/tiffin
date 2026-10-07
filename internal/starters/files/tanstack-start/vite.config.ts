import { tanstackStart } from "@tanstack/react-start/plugin/vite";
import viteReact from "@vitejs/plugin-react";
import { nitro } from "nitro/vite";
import { defineConfig } from "vite";

// TanStack Start on Nitro: `vite build` writes a Bun server to
// .output/server, which the box runs and serves on $PORT. The preset is pinned
// so the build is the same wherever it runs (Nitro picks one by the runtime
// otherwise). The box serves the hashed JS and CSS in .output/public itself.
export default defineConfig({
  server: { port: 3000 },
  plugins: [
    tanstackStart({
      // Pages that never change are rendered once, at build time, to plain
      // HTML. The notes page reads Postgres, so it renders per request.
      prerender: { enabled: true, crawlLinks: false, filter: ({ path }) => path === "/about" },
      pages: [{ path: "/about" }],
    }),
    nitro({ preset: "bun" }),
    viteReact(),
  ],
});
