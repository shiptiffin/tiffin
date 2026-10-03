import { defineConfig } from "tiffin-sdk";

// A realistic Tiffin project: a Next.js storefront, a Hono API, a background
// worker, Postgres with pgvector, Valkey, one public bucket, accounts with
// teams, email, analytics and a nightly cron.
export default defineConfig({
  project: "hello",
  env: {
    LOG_LEVEL: "info",
  },
  apps: {
    web: {
      framework: "next",
      path: "apps/web",
      routes: ["hello"],
      memoryMB: 1024,
    },
    api: {
      framework: "hono",
      path: "apps/api",
      routes: ["hello/api"],
      healthcheck: "/healthz",
      instances: 2,
      env: { CORS_ORIGIN: "https://hello" },
    },
    worker: {
      framework: "bun",
      path: "apps/worker",
      role: "worker",
      memoryMB: 256,
    },
  },
  services: {
    postgres: { extensions: ["vector"] },
    valkey: { maxMemoryMB: 128 },
    storage: {
      buckets: {
        uploads: { public: true },
      },
    },
    auth: { methods: ["email", "magic-link", "google"], organizations: true },
    email: { from: "hello@example.com" },
    analytics: { retentionDays: 90 },
  },
  crons: {
    // Crons push to the app internally, so a worker (no routes) is a valid target.
    "nightly-report": { schedule: "0 3 * * *", app: "worker", path: "/cron/report" },
  },
});
