import { defineConfig } from "@shiptiffin/sdk";

export default defineConfig({
  project: "shop",
  apps: {
    web: { framework: "next", git: { repo: "acme/shop", path: "/apps/web/" } },
    docs: { framework: "static", git: { repo: "acme/shop", branch: "release", path: "docs", previews: "off" } },
    api: { framework: "hono", git: { repo: "acme/api", previews: "forks" } },
  },
});
