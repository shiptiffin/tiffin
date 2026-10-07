import { defineConfig } from "@shiptiffin/sdk";

// A static site built with Vite + React: the box runs `bun run build` and its
// edge serves dist/ over HTTPS. No container runs.
export default defineConfig({
  project: "my-app",
  apps: {
    site: { framework: "static" },
  },
});
