import { defineConfig } from "@shiptiffin/sdk";

// A realistic Tiffin project: a Next.js storefront, a Hono API, a background
// worker, Postgres with pgvector, Valkey and one public bucket.
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
  },
});
