import { defineConfig } from "tiffin-sdk";

export default defineConfig({
  project: "hello-static",
  apps: {
    // Static apps run no container. Tiffin serves public/ (or dist/, build/,
    // out/, or a Staticfile's root). With a "build" script in package.json,
    // `bun install && bun run build` runs first.
    site: { framework: "static" },
  },
});
