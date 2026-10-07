import { defineConfig } from "@shiptiffin/sdk";

// An API: a small notes service in Hono on Bun, with its own Postgres database.
export default defineConfig({
  project: "notes",
  apps: {
    api: { framework: "hono", healthcheck: "/healthz" },
  },
  services: {
    postgres: {},
  },
});
