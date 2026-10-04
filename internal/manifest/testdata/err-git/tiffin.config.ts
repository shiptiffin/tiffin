import { defineConfig } from "tiffin-sdk";

export default defineConfig({
  project: "shop",
  apps: {
    web: { git: { repo: "https://github.com/acme/shop" } },
    api: { git: { repo: "acme/api", path: "apps/../../etc", previews: "yes" } },
    jobs: { git: { branch: "main" } },
  },
});
