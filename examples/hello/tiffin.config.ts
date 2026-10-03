import { defineConfig } from "tiffin-sdk";

// A realistic Tiffin project: a Next.js storefront, a Hono API, a background
// worker, Postgres with pgvector, Valkey, one public bucket, accounts with
// teams, email, analytics, a background queue with a topic and a nightly cron.
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
  queues: {
    // Jobs are pushed to the worker (no routes needed), 4 at a time, and
    // retried up to 5 times before they land in the dead-letter queue.
    emails: { app: "worker", concurrency: 4, maxAttempts: 5 },
    audit: { app: "worker", path: "/events/audit" },
  },
  topics: {
    // Sending to "order.created" delivers one job to each subscribed queue.
    "order.created": { subscribers: ["emails", "audit"] },
  },
  crons: {
    // Crons push to the app internally, so a worker (no routes) is a valid target.
    "nightly-report": { schedule: "0 3 * * *", app: "worker", path: "/cron/report" },
  },
});
