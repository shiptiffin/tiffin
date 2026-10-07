import { defineConfig } from "@shiptiffin/sdk";

// A static site built with Astro: the box runs `bun run build` and its edge
// serves dist/ over HTTPS. No container runs.
export default defineConfig({
  project: "my-site",
  apps: {
    site: { framework: "static" },
  },
});
