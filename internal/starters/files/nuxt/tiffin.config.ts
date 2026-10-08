import { defineConfig } from "@shiptiffin/sdk";

// A web app: Nuxt 4 (Nitro's node-server output), reading and writing Postgres.
export default defineConfig({
  project: "nuxt-notes",
  apps: {
    web: { framework: "bun", healthcheck: "/healthz" },
  },
});
