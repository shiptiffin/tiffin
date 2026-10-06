import { defineConfig } from "@shiptiffin/sdk/config";

export default defineConfig({
  project: "shop",
  env: { LOG_LEVEL: "info", SITE_NAME: "Shop & <Co>" },
  apps: {
    web: {
      framework: "next",
      path: "apps/web",
      routes: ["shop", "Example.com/"],
      instances: 2,
      memoryMB: 1024,
      env: { NEXT_TELEMETRY_DISABLED: "1" },
    },
    api: {
      framework: "hono",
      path: "apps/api",
      routes: ["example.com/api"],
      healthcheck: "/healthz",
    },
    docs: { framework: "static", path: "apps/docs" },
    jobs: { framework: "bun", path: "apps/jobs", role: "worker", memoryMB: 256 },
  },
  services: {
    postgres: { extensions: ["vector", "pg_cron", "vector"] },
    valkey: {},
    storage: {
      buckets: {
        uploads: { maxFileSize: 10485760, allowedTypes: ["image/*", "application/pdf"], cors: ["https://example.com"] },
        public: { public: true },
      },
    },
  },
});
