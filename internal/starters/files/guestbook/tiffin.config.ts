import { defineConfig } from "@shiptiffin/sdk";

// A guestbook in one Hono app: the page, a JSON API, Postgres for entries,
// Valkey for a visit counter and cookieless analytics.
export default defineConfig({
  project: "guestbook",
  apps: {
    guestbook: { framework: "hono", healthcheck: "/api/healthz", env: { GREETING: "Welcome to the box" } },
  },
});
