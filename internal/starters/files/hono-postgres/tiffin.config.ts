import { defineConfig } from "tiffin-sdk";

// A notes API: Hono on Bun, with its own Postgres database.
export default defineConfig({
  project: "notes",
  apps: {
    api: { framework: "hono", healthcheck: "/healthz" },
  },
  services: {
    postgres: {},
  },
});
