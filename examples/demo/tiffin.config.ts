import { defineConfig } from "@shiptiffin/sdk";

// A guestbook: a static page, a Hono API on Postgres and Valkey, and
// cookieless analytics. Deploy with `tiffin deploy` from this folder.
export default defineConfig({
  project: "demo",
  apps: {
    site: { framework: "static", path: "site", routes: ["demo"] },
    api: { framework: "hono", path: "api", routes: ["demo/api"], healthcheck: "/api/healthz" },
  },
  services: {
    postgres: {},
    valkey: {},
    analytics: {},
  },
  env: { GREETING: "Welcome to the box" },
});
